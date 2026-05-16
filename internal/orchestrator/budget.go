package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// BudgetState is the monthly-spend health enum every NodeRunner and the
// root orchestrator query before deciding whether to fire an LLM call,
// accept an offspring proposal, or run shadow strategists.
//
// Spec lines 533-536: the budget fallback ladder is
// reduce-strategist-frequency → disable-shadow-strategists →
// freeze-offspring → keep-deterministic-running.
type BudgetState int

const (
	// BudgetHealthy is the normal state: spend < 80% of cap.
	BudgetHealthy BudgetState = iota
	// BudgetTight fires at 80% spend. OnTight halves strategist
	// frequencies for funded nodes and disables shadow strategists.
	BudgetTight
	// BudgetBreached fires at 100% spend. OnBreached additionally
	// freezes offspring proposals; deterministic loops (monitor,
	// heartbeat) keep running so positions don't go unwatched.
	BudgetBreached
)

func (s BudgetState) String() string {
	switch s {
	case BudgetHealthy:
		return "healthy"
	case BudgetTight:
		return "tight"
	case BudgetBreached:
		return "breached"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// SpendStore returns the swarm's month-to-date spend. The chunk-21
// production impl reads agent_ledgers + infra rent + RPC rent; tests
// pass an in-memory fake.
type SpendStore interface {
	// MonthlySpendUSD returns total spend (LLM + infra + RPC) so far
	// in the calendar month containing `now`.
	MonthlySpendUSD(ctx context.Context, now time.Time) (float64, error)
}

// FallbackActions is the surface BudgetTracker calls when state worsens.
// Production impl is the SwarmRuntime + RootOrchestrator (chunks 14 +
// 21); tests pass a recorder. Each method must be safe to call multiple
// times — BudgetTracker itself dedupes via fireHistory, but defense in
// depth costs nothing.
type FallbackActions interface {
	HalveStrategistFrequencies(ctx context.Context) error
	DisableShadowStrategists(ctx context.Context) error
	FreezeOffspringProposals(ctx context.Context) error
}

// BudgetConfig configures one BudgetTracker.
type BudgetConfig struct {
	// MonthlyBudgetUSD is the hard cap. Default 100 (spec line 533).
	MonthlyBudgetUSD float64
	// TightThreshold is the fraction at which BudgetTight fires.
	// Default 0.80.
	TightThreshold float64
	// BreachedThreshold is the fraction at which BudgetBreached fires.
	// Default 1.00.
	BreachedThreshold float64
	// RefreshInterval is the polling cadence for Run(). Default 60s.
	RefreshInterval time.Duration
}

// BudgetTracker computes the current BudgetState and fires the fallback
// hooks exactly once per month per threshold. Designed to be queried
// synchronously by NodeRunner before any LLM call, and to be Run() in
// a background goroutine that updates the cached state every minute.
type BudgetTracker struct {
	store   SpendStore
	actions FallbackActions
	cfg     BudgetConfig
	now     func() time.Time

	mu          sync.RWMutex
	cachedState BudgetState
	cachedSpend float64
	// fireHistory[state] = the calendar month (truncated to month) in
	// which the hook for that state last fired. Reset implicitly when
	// the month rolls over — see fireOnce.
	fireHistory map[BudgetState]time.Time
}

// NewBudgetTracker validates cfg and returns a tracker. actions may be
// nil only if the caller never invokes Refresh / Run (i.e. it's purely
// reading the state from elsewhere).
func NewBudgetTracker(store SpendStore, actions FallbackActions, cfg BudgetConfig) (*BudgetTracker, error) {
	if store == nil {
		return nil, errors.New("budget: nil store")
	}
	if cfg.MonthlyBudgetUSD <= 0 {
		cfg.MonthlyBudgetUSD = 100
	}
	if cfg.TightThreshold <= 0 {
		cfg.TightThreshold = 0.80
	}
	if cfg.BreachedThreshold <= 0 {
		cfg.BreachedThreshold = 1.00
	}
	if cfg.TightThreshold >= cfg.BreachedThreshold {
		return nil, fmt.Errorf("budget: tight threshold (%v) must be < breached (%v)",
			cfg.TightThreshold, cfg.BreachedThreshold)
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 60 * time.Second
	}
	return &BudgetTracker{
		store:       store,
		actions:     actions,
		cfg:         cfg,
		now:         time.Now,
		fireHistory: map[BudgetState]time.Time{},
	}, nil
}

// State returns the cached BudgetState. Cheap — every NodeRunner can
// call this on every strategist tick. Refresh updates the cache.
func (b *BudgetTracker) State() BudgetState {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.cachedState
}

// Spend returns the cached month-to-date spend.
func (b *BudgetTracker) Spend() float64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.cachedSpend
}

// Refresh re-reads the spend, updates the cached state, and fires any
// fallback hooks that just transitioned in. Returns the new state.
//
// Hooks are idempotent within a calendar month: OnTight fires at most
// once per month, and OnBreached fires at most once per month — even
// if the state oscillates due to refund or accounting correction.
// Crossing the month boundary clears the history.
func (b *BudgetTracker) Refresh(ctx context.Context) (BudgetState, error) {
	if b == nil {
		return BudgetHealthy, errors.New("budget: nil receiver")
	}
	now := b.now()
	spend, err := b.store.MonthlySpendUSD(ctx, now)
	if err != nil {
		return BudgetHealthy, fmt.Errorf("budget.Refresh: %w", err)
	}
	state := b.classify(spend)

	b.mu.Lock()
	b.cachedSpend = spend
	b.cachedState = state
	b.mu.Unlock()

	// Fire hooks for the current state if not already fired this month.
	// We fire OnTight (and OnBreached) — never un-fire on the way down,
	// per spec: "graceful degradation" means once cadence has been
	// halved, we don't snap it back the moment spend dips, to avoid
	// thrash on month-end accounting corrections.
	switch state {
	case BudgetBreached:
		if err := b.fireOnce(ctx, BudgetBreached, now); err != nil {
			return state, err
		}
		// Breached implies tight; ensure the tight actions are also
		// in place. fireOnce is a no-op if already fired this month.
		if err := b.fireOnce(ctx, BudgetTight, now); err != nil {
			return state, err
		}
	case BudgetTight:
		if err := b.fireOnce(ctx, BudgetTight, now); err != nil {
			return state, err
		}
	}
	return state, nil
}

// Run polls Refresh on cfg.RefreshInterval until ctx is cancelled.
// The first refresh is immediate; subsequent refreshes follow the
// ticker. Errors are surfaced through onError (nil ⇒ discard).
func (b *BudgetTracker) Run(ctx context.Context, onError func(error)) {
	if onError == nil {
		onError = func(error) {}
	}
	if _, err := b.Refresh(ctx); err != nil {
		onError(err)
	}
	t := time.NewTicker(b.cfg.RefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := b.Refresh(ctx); err != nil {
				onError(err)
			}
		}
	}
}

func (b *BudgetTracker) classify(spend float64) BudgetState {
	tight := b.cfg.MonthlyBudgetUSD * b.cfg.TightThreshold
	breach := b.cfg.MonthlyBudgetUSD * b.cfg.BreachedThreshold
	switch {
	case spend >= breach:
		return BudgetBreached
	case spend >= tight:
		return BudgetTight
	default:
		return BudgetHealthy
	}
}

// fireOnce invokes the hooks for `state`, but only if they haven't
// already fired in the current calendar month. The month is computed
// from `now` (time.Truncate doesn't honor month boundaries, so we use
// the explicit year/month tuple).
func (b *BudgetTracker) fireOnce(ctx context.Context, state BudgetState, now time.Time) error {
	currentMonth := monthOf(now)
	b.mu.Lock()
	last, ok := b.fireHistory[state]
	if ok && monthOf(last).Equal(currentMonth) {
		b.mu.Unlock()
		return nil
	}
	b.fireHistory[state] = now
	b.mu.Unlock()

	if b.actions == nil {
		return nil
	}
	switch state {
	case BudgetTight:
		if err := b.actions.HalveStrategistFrequencies(ctx); err != nil {
			return fmt.Errorf("budget: halve frequencies: %w", err)
		}
		if err := b.actions.DisableShadowStrategists(ctx); err != nil {
			return fmt.Errorf("budget: disable shadow: %w", err)
		}
	case BudgetBreached:
		if err := b.actions.FreezeOffspringProposals(ctx); err != nil {
			return fmt.Errorf("budget: freeze offspring: %w", err)
		}
	}
	return nil
}

// monthOf returns the UTC instant at the start of t's calendar month.
// We use it as the equality key for "same month as previous fire."
func monthOf(t time.Time) time.Time {
	y, m, _ := t.UTC().Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
}
