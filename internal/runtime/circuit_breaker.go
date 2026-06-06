package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// BreakerReason names the trigger that tripped the global circuit
// breaker. Spec lines 525-527.
type BreakerReason string

const (
	BreakerReasonNone           BreakerReason = ""
	BreakerReasonMarketCrash    BreakerReason = "market_crash"      // SOL or ETH 15%+ in 1h
	BreakerReasonMassStopOut    BreakerReason = "mass_stop_out"     // 50%+ funded nodes hit stops in same epoch
	BreakerReasonRPCErrorBurst  BreakerReason = "rpc_error_burst"   // RPC error rate ≥ 30% over 5min
	BreakerReasonManualOverride BreakerReason = "manual_override"
)

// BreakerConfig holds the auto-reset cooldown. Spec line 528: auto-reset
// after 2 hours. Tests inject a shorter cooldown for speed.
type BreakerConfig struct {
	Cooldown time.Duration
	Clock    Clock
	Log      logger
}

type logger interface {
	Warnw(msg string, kvs ...interface{})
	Infow(msg string, kvs ...interface{})
}

// CircuitBreaker is a global tripwire that halts every NodeRunner's
// monitor + strategist activity. The breaker is process-wide on purpose
// — a market-wide crash or an RPC outage affects every agent regardless
// of strategy. A tripped breaker auto-resets after Cooldown.
type CircuitBreaker struct {
	cfg BreakerConfig

	halted atomic.Bool

	mu        sync.Mutex
	reason    BreakerReason
	trippedAt time.Time
}

// NewCircuitBreaker constructs a breaker in the un-tripped state.
// Defaults: 2h cooldown, RealClock.
func NewCircuitBreaker(cfg BreakerConfig) *CircuitBreaker {
	if cfg.Cooldown == 0 {
		cfg.Cooldown = 2 * time.Hour
	}
	if cfg.Clock == nil {
		cfg.Clock = RealClock{}
	}
	if cfg.Log == nil {
		cfg.Log = nopLogger{}
	}
	return &CircuitBreaker{cfg: cfg}
}

// Halted reports whether monitor + strategist loops should skip work.
// O(1) atomic read — safe to call on every tick of every NodeRunner.
func (b *CircuitBreaker) Halted() bool { return b.halted.Load() }

// Reason returns the current trip reason, or BreakerReasonNone if the
// breaker is not tripped.
func (b *CircuitBreaker) Reason() BreakerReason {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reason
}

// TrippedAt returns the time the breaker was tripped, or zero if not
// tripped.
func (b *CircuitBreaker) TrippedAt() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.trippedAt
}

// Trip halts the swarm with the given reason. Idempotent: re-tripping an
// already-tripped breaker refreshes the timestamp + reason so the
// cooldown restarts from now.
func (b *CircuitBreaker) Trip(reason BreakerReason) {
	b.mu.Lock()
	b.reason = reason
	b.trippedAt = b.cfg.Clock.Now()
	b.mu.Unlock()
	b.halted.Store(true)
	b.cfg.Log.Warnw("circuit_breaker_tripped", "reason", string(reason))
}

// Reset clears the tripped state immediately, regardless of cooldown.
// Used by manual override clear or by Run's auto-reset goroutine.
func (b *CircuitBreaker) Reset() {
	b.mu.Lock()
	b.reason = BreakerReasonNone
	b.trippedAt = time.Time{}
	b.mu.Unlock()
	b.halted.Store(false)
	b.cfg.Log.Infow("circuit_breaker_reset")
}

// Run blocks until ctx is cancelled, polling for auto-reset. Each tick
// it checks whether the breaker is tripped and the cooldown has elapsed;
// if so, it calls Reset. Tick cadence is Cooldown/20 (capped at 1s
// minimum) so a 2h cooldown polls every ~6m and a test cooldown of 200ms
// polls every ~10ms.
func (b *CircuitBreaker) Run(ctx context.Context) error {
	interval := b.cfg.Cooldown / 20
	if interval < time.Second {
		interval = time.Second
	}
	tk := b.cfg.Clock.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C():
			b.maybeReset()
		}
	}
}

func (b *CircuitBreaker) maybeReset() {
	if !b.halted.Load() {
		return
	}
	b.mu.Lock()
	trippedAt := b.trippedAt
	b.mu.Unlock()
	if trippedAt.IsZero() {
		return
	}
	if b.cfg.Clock.Now().Sub(trippedAt) >= b.cfg.Cooldown {
		b.Reset()
	}
}

// MarketSnapshot is the per-asset price-change signal the breaker
// consumes. Pct is fractional (0.15 = 15%).
type MarketSnapshot struct {
	Asset  string
	Pct1h  float64
	AbsPct float64 // |Pct1h|, callers may pre-compute
}

// EpochOutcome is the per-epoch summary the breaker reads to decide
// whether the mass-stop-out trigger fires. Used by the root
// orchestrator after settling each epoch.
type EpochOutcome struct {
	FundedNodes       int
	NodesHitStopLoss  int
	RPCRequests       int
	RPCErrors         int
	WindowDuration    time.Duration
	MarketSnapshots   []MarketSnapshot
}

// Defaults from spec line 525-527.
const (
	defaultMarketMovePct       = 0.15
	defaultStopOutFraction     = 0.50
	defaultRPCErrorFraction    = 0.30
	defaultRPCErrorWindowFloor = 5 * time.Minute
)

// EvaluateTriggers checks every trigger and trips the breaker on the
// first one that fires. Returns the reason if tripped (or
// BreakerReasonNone). Safe to call on a tripped breaker — it short
// circuits without re-tripping.
func (b *CircuitBreaker) EvaluateTriggers(outcome EpochOutcome) BreakerReason {
	if b.halted.Load() {
		return b.Reason()
	}
	for _, snap := range outcome.MarketSnapshots {
		abs := snap.AbsPct
		if abs == 0 {
			abs = snap.Pct1h
			if abs < 0 {
				abs = -abs
			}
		}
		if (snap.Asset == "SOL" || snap.Asset == "ETH") && abs >= defaultMarketMovePct {
			b.Trip(BreakerReasonMarketCrash)
			return BreakerReasonMarketCrash
		}
	}
	if outcome.FundedNodes > 0 {
		frac := float64(outcome.NodesHitStopLoss) / float64(outcome.FundedNodes)
		if frac >= defaultStopOutFraction {
			b.Trip(BreakerReasonMassStopOut)
			return BreakerReasonMassStopOut
		}
	}
	if outcome.RPCRequests > 0 && outcome.WindowDuration >= defaultRPCErrorWindowFloor {
		frac := float64(outcome.RPCErrors) / float64(outcome.RPCRequests)
		if frac >= defaultRPCErrorFraction {
			b.Trip(BreakerReasonRPCErrorBurst)
			return BreakerReasonRPCErrorBurst
		}
	}
	return BreakerReasonNone
}

type nopLogger struct{}

func (nopLogger) Warnw(string, ...interface{}) {}
func (nopLogger) Infow(string, ...interface{}) {}
