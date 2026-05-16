package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeSpendStore struct {
	mu    sync.Mutex
	spend float64
	err   error
	calls int
}

func (f *fakeSpendStore) MonthlySpendUSD(_ context.Context, _ time.Time) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.spend, f.err
}

func (f *fakeSpendStore) set(v float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spend = v
}

type recActions struct {
	halve, disableShadow, freezeOff int
	halveErr                        error
}

func (r *recActions) HalveStrategistFrequencies(_ context.Context) error {
	r.halve++
	return r.halveErr
}
func (r *recActions) DisableShadowStrategists(_ context.Context) error {
	r.disableShadow++
	return nil
}
func (r *recActions) FreezeOffspringProposals(_ context.Context) error {
	r.freezeOff++
	return nil
}

func newTracker(t *testing.T, spend float64) (*BudgetTracker, *fakeSpendStore, *recActions) {
	t.Helper()
	store := &fakeSpendStore{spend: spend}
	actions := &recActions{}
	b, err := NewBudgetTracker(store, actions, BudgetConfig{MonthlyBudgetUSD: 100})
	if err != nil {
		t.Fatalf("NewBudgetTracker: %v", err)
	}
	return b, store, actions
}

func TestBudgetState_String(t *testing.T) {
	cases := map[BudgetState]string{
		BudgetHealthy:  "healthy",
		BudgetTight:    "tight",
		BudgetBreached: "breached",
	}
	for s, want := range cases {
		if s.String() != want {
			t.Errorf("%d.String() = %q, want %q", s, s.String(), want)
		}
	}
}

func TestBudgetTracker_ClassificationTable(t *testing.T) {
	cases := []struct {
		spend float64
		want  BudgetState
	}{
		{0, BudgetHealthy},
		{50, BudgetHealthy},
		{79.99, BudgetHealthy},
		{80, BudgetTight},
		{99.99, BudgetTight},
		{100, BudgetBreached},
		{120, BudgetBreached},
	}
	for _, c := range cases {
		b, _, _ := newTracker(t, c.spend)
		got, err := b.Refresh(context.Background())
		if err != nil {
			t.Fatalf("Refresh(%v): %v", c.spend, err)
		}
		if got != c.want {
			t.Errorf("spend=%v → %v, want %v", c.spend, got, c.want)
		}
	}
}

func TestBudgetTracker_HealthyFiresNothing(t *testing.T) {
	b, _, actions := newTracker(t, 10)
	if _, err := b.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if actions.halve != 0 || actions.disableShadow != 0 || actions.freezeOff != 0 {
		t.Errorf("healthy fired hooks: %+v", actions)
	}
}

func TestBudgetTracker_TightFiresHalveAndDisableShadow(t *testing.T) {
	b, _, actions := newTracker(t, 85)
	if _, err := b.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if actions.halve != 1 || actions.disableShadow != 1 {
		t.Errorf("tight should fire halve+disableShadow exactly once, got %+v", actions)
	}
	if actions.freezeOff != 0 {
		t.Errorf("tight should not freeze offspring, got %d", actions.freezeOff)
	}
}

func TestBudgetTracker_BreachedFiresAllThree(t *testing.T) {
	b, _, actions := newTracker(t, 110)
	if _, err := b.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if actions.halve != 1 || actions.disableShadow != 1 || actions.freezeOff != 1 {
		t.Errorf("breached should fire all three exactly once, got %+v", actions)
	}
}

func TestBudgetTracker_Idempotent_HooksFireOncePerMonth(t *testing.T) {
	b, store, actions := newTracker(t, 85)
	for i := 0; i < 5; i++ {
		if _, err := b.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if actions.halve != 1 {
		t.Errorf("halve fired %d times, want 1 (idempotent within month)", actions.halve)
	}

	// Bump to breached — should fire freeze once even though tight already fired.
	store.set(120)
	for i := 0; i < 3; i++ {
		if _, err := b.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if actions.freezeOff != 1 {
		t.Errorf("freezeOff = %d, want 1", actions.freezeOff)
	}
	if actions.halve != 1 {
		t.Errorf("halve should not re-fire on tight→breached transition (already fired this month), got %d", actions.halve)
	}
}

func TestBudgetTracker_MonthBoundaryResetsHooks(t *testing.T) {
	b, _, actions := newTracker(t, 85)
	month1 := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return month1 }
	if _, err := b.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if actions.halve != 1 {
		t.Fatalf("halve = %d, want 1", actions.halve)
	}

	// Cross into a new calendar month. fireHistory should clear
	// implicitly — the next tight Refresh re-fires.
	month2 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return month2 }
	if _, err := b.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if actions.halve != 2 {
		t.Errorf("halve = %d after month rollover, want 2", actions.halve)
	}
}

func TestBudgetTracker_RefreshErrorBubbles(t *testing.T) {
	store := &fakeSpendStore{err: errors.New("db down")}
	b, err := NewBudgetTracker(store, &recActions{}, BudgetConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Refresh(context.Background()); err == nil {
		t.Error("expected refresh error to bubble")
	}
}

func TestBudgetTracker_ActionErrorBubbles(t *testing.T) {
	store := &fakeSpendStore{spend: 85}
	actions := &recActions{halveErr: errors.New("db write down")}
	b, err := NewBudgetTracker(store, actions, BudgetConfig{MonthlyBudgetUSD: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Refresh(context.Background()); err == nil {
		t.Error("expected halve error to bubble")
	}
}

func TestBudgetTracker_NilActionsIsSafe(t *testing.T) {
	store := &fakeSpendStore{spend: 85}
	b, err := NewBudgetTracker(store, nil, BudgetConfig{MonthlyBudgetUSD: 100})
	if err != nil {
		t.Fatal(err)
	}
	state, err := b.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh with nil actions: %v", err)
	}
	if state != BudgetTight {
		t.Errorf("state = %v, want tight", state)
	}
}

func TestNewBudgetTracker_Validation(t *testing.T) {
	store := &fakeSpendStore{}
	// nil store rejected.
	if _, err := NewBudgetTracker(nil, nil, BudgetConfig{}); err == nil {
		t.Error("expected error for nil store")
	}
	// Inverted thresholds rejected.
	if _, err := NewBudgetTracker(store, nil, BudgetConfig{
		TightThreshold: 1.5, BreachedThreshold: 1.0,
	}); err == nil {
		t.Error("expected error for tight ≥ breached")
	}
	// Defaults applied.
	b, err := NewBudgetTracker(store, nil, BudgetConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if b.cfg.MonthlyBudgetUSD != 100 {
		t.Errorf("default budget = %v, want 100", b.cfg.MonthlyBudgetUSD)
	}
	if b.cfg.TightThreshold != 0.80 {
		t.Errorf("default tight = %v, want 0.80", b.cfg.TightThreshold)
	}
	if b.cfg.RefreshInterval != 60*time.Second {
		t.Errorf("default refresh = %v, want 60s", b.cfg.RefreshInterval)
	}
}

func TestBudgetTracker_RunRefreshesAndStops(t *testing.T) {
	store := &fakeSpendStore{spend: 10}
	b, err := NewBudgetTracker(store, &recActions{}, BudgetConfig{
		MonthlyBudgetUSD: 100,
		RefreshInterval:  10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.Run(ctx, nil)
		close(done)
	}()
	// Let it tick a few times.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after cancel")
	}
	store.mu.Lock()
	calls := store.calls
	store.mu.Unlock()
	if calls < 2 {
		t.Errorf("expected ≥ 2 refresh calls, got %d", calls)
	}
}
