package runtime

import (
	"context"
	"testing"
	"time"
)

func newTestBreaker(clk *fakeClock) *CircuitBreaker {
	return NewCircuitBreaker(BreakerConfig{
		Cooldown: 2 * time.Hour,
		Clock:    clk,
	})
}

func TestCircuitBreaker_StartsUntripped(t *testing.T) {
	b := newTestBreaker(newFakeClock(time.Now()))
	if b.Halted() {
		t.Error("new breaker should not be halted")
	}
	if b.Reason() != BreakerReasonNone {
		t.Errorf("reason = %q, want empty", b.Reason())
	}
}

func TestCircuitBreaker_ManualTripAndReset(t *testing.T) {
	clk := newFakeClock(time.Now())
	b := newTestBreaker(clk)
	b.Trip(BreakerReasonManualOverride)
	if !b.Halted() {
		t.Error("expected halted after Trip")
	}
	if b.Reason() != BreakerReasonManualOverride {
		t.Errorf("reason = %q, want manual_override", b.Reason())
	}
	b.Reset()
	if b.Halted() {
		t.Error("expected un-halted after Reset")
	}
	if b.Reason() != BreakerReasonNone {
		t.Errorf("reason = %q after Reset, want empty", b.Reason())
	}
}

func TestCircuitBreaker_MarketCrashTriggerSOL(t *testing.T) {
	clk := newFakeClock(time.Now())
	b := newTestBreaker(clk)
	got := b.EvaluateTriggers(EpochOutcome{
		MarketSnapshots: []MarketSnapshot{{Asset: "SOL", Pct1h: -0.16}},
	})
	if got != BreakerReasonMarketCrash {
		t.Errorf("got %q, want market_crash", got)
	}
	if !b.Halted() {
		t.Error("breaker should be halted")
	}
}

func TestCircuitBreaker_MarketCrashTriggerETH(t *testing.T) {
	b := newTestBreaker(newFakeClock(time.Now()))
	got := b.EvaluateTriggers(EpochOutcome{
		MarketSnapshots: []MarketSnapshot{{Asset: "ETH", Pct1h: 0.20}},
	})
	if got != BreakerReasonMarketCrash {
		t.Errorf("got %q, want market_crash on positive 20%% move", got)
	}
}

func TestCircuitBreaker_MarketCrashBelowThreshold(t *testing.T) {
	b := newTestBreaker(newFakeClock(time.Now()))
	got := b.EvaluateTriggers(EpochOutcome{
		MarketSnapshots: []MarketSnapshot{{Asset: "SOL", Pct1h: -0.14}, {Asset: "ETH", Pct1h: 0.10}},
	})
	if got != BreakerReasonNone {
		t.Errorf("got %q, want none (under threshold)", got)
	}
	if b.Halted() {
		t.Error("breaker should not be halted")
	}
}

func TestCircuitBreaker_OtherAssetMoveIgnored(t *testing.T) {
	b := newTestBreaker(newFakeClock(time.Now()))
	got := b.EvaluateTriggers(EpochOutcome{
		MarketSnapshots: []MarketSnapshot{{Asset: "BTC", Pct1h: -0.50}},
	})
	if got != BreakerReasonNone {
		t.Errorf("BTC move should not trip the breaker; got %q", got)
	}
}

func TestCircuitBreaker_MassStopOutTrigger(t *testing.T) {
	b := newTestBreaker(newFakeClock(time.Now()))
	got := b.EvaluateTriggers(EpochOutcome{
		FundedNodes: 10, NodesHitStopLoss: 5,
	})
	if got != BreakerReasonMassStopOut {
		t.Errorf("got %q, want mass_stop_out at 50%%", got)
	}
}

func TestCircuitBreaker_MassStopOutBelowThreshold(t *testing.T) {
	b := newTestBreaker(newFakeClock(time.Now()))
	got := b.EvaluateTriggers(EpochOutcome{
		FundedNodes: 10, NodesHitStopLoss: 4,
	})
	if got != BreakerReasonNone {
		t.Errorf("40%% stop-out should not trip; got %q", got)
	}
}

func TestCircuitBreaker_RPCErrorBurstTrigger(t *testing.T) {
	b := newTestBreaker(newFakeClock(time.Now()))
	got := b.EvaluateTriggers(EpochOutcome{
		RPCRequests:    100,
		RPCErrors:      30,
		WindowDuration: 5 * time.Minute,
	})
	if got != BreakerReasonRPCErrorBurst {
		t.Errorf("got %q, want rpc_error_burst", got)
	}
}

func TestCircuitBreaker_RPCErrorBurstShortWindowIgnored(t *testing.T) {
	b := newTestBreaker(newFakeClock(time.Now()))
	got := b.EvaluateTriggers(EpochOutcome{
		RPCRequests:    100,
		RPCErrors:      50,
		WindowDuration: 1 * time.Minute,
	})
	if got != BreakerReasonNone {
		t.Errorf("short window should not trip; got %q", got)
	}
}

func TestCircuitBreaker_AlreadyHaltedShortCircuits(t *testing.T) {
	b := newTestBreaker(newFakeClock(time.Now()))
	b.Trip(BreakerReasonManualOverride)
	got := b.EvaluateTriggers(EpochOutcome{
		MarketSnapshots: []MarketSnapshot{{Asset: "SOL", Pct1h: -0.50}},
	})
	if got != BreakerReasonManualOverride {
		t.Errorf("already-halted should keep original reason; got %q", got)
	}
}

func TestCircuitBreaker_AutoResetAfterCooldown(t *testing.T) {
	clk := newFakeClock(time.Now())
	b := NewCircuitBreaker(BreakerConfig{Cooldown: 2 * time.Hour, Clock: clk})
	b.Trip(BreakerReasonMarketCrash)
	if !b.Halted() {
		t.Fatal("expected halted")
	}
	// Less than cooldown — no reset.
	clk.Advance(1 * time.Hour)
	b.maybeReset()
	if !b.Halted() {
		t.Error("should still be halted before cooldown elapses")
	}
	// Past cooldown — reset.
	clk.Advance(2 * time.Hour)
	b.maybeReset()
	if b.Halted() {
		t.Error("should be reset after cooldown elapses")
	}
}

func TestCircuitBreaker_RunAutoResets(t *testing.T) {
	clk := newFakeClock(time.Now())
	b := NewCircuitBreaker(BreakerConfig{Cooldown: 20 * time.Second, Clock: clk})
	b.Trip(BreakerReasonMarketCrash)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = b.Run(ctx)
		close(done)
	}()

	// Wait for the ticker to register.
	waitFor(t, func() bool { return clk.TickerCount() >= 1 })

	// Advance past cooldown and fire the tick.
	clk.Advance(21 * time.Second)
	clk.Fire()

	waitFor(t, func() bool { return !b.Halted() })

	cancel()
	<-done
}

func TestCircuitBreaker_TripIsIdempotent(t *testing.T) {
	clk := newFakeClock(time.Now())
	b := NewCircuitBreaker(BreakerConfig{Cooldown: 2 * time.Hour, Clock: clk})
	b.Trip(BreakerReasonMarketCrash)
	t1 := b.TrippedAt()
	clk.Advance(1 * time.Hour)
	b.Trip(BreakerReasonMassStopOut)
	t2 := b.TrippedAt()
	if !t2.After(t1) {
		t.Errorf("second Trip should refresh timestamp: t1=%v t2=%v", t1, t2)
	}
	if b.Reason() != BreakerReasonMassStopOut {
		t.Errorf("reason = %q, want updated to mass_stop_out", b.Reason())
	}
}
