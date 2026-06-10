package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"github.com/veermshah/cambrian/internal/orchestrator"
)

func TestEpochCadence_DefaultAndOverride(t *testing.T) {
	t.Setenv("EPOCH_INTERVAL", "")
	if got := epochCadence(); got != time.Hour {
		t.Errorf("default = %v, want 1h", got)
	}
	t.Setenv("EPOCH_INTERVAL", "30s")
	if got := epochCadence(); got != 30*time.Second {
		t.Errorf("override = %v", got)
	}
	// Invalid → fall back to default.
	t.Setenv("EPOCH_INTERVAL", "not-a-duration")
	if got := epochCadence(); got != time.Hour {
		t.Errorf("invalid override should fall back: %v", got)
	}
}

func TestBreakerAdapter_TripsAtFiftyPercent(t *testing.T) {
	b := breakerAdapter{}
	if got := b.EvaluateEpoch(orchestrator.EpochBreakerOutcome{FundedNodes: 4, NodesHitStopLoss: 1}); got != "" {
		t.Errorf("25%% stop-out: trip=%q, want untripped", got)
	}
	if got := b.EvaluateEpoch(orchestrator.EpochBreakerOutcome{FundedNodes: 4, NodesHitStopLoss: 2}); got != "mass_stop_out" {
		t.Errorf("50%% stop-out: trip=%q, want mass_stop_out", got)
	}
	if got := b.EvaluateEpoch(orchestrator.EpochBreakerOutcome{FundedNodes: 0, NodesHitStopLoss: 0}); got != "" {
		t.Errorf("no funded: trip=%q, want untripped", got)
	}
	if b.Halted() {
		t.Error("Halted should always be false — runtime breaker is source of truth")
	}
}

// fakePoolRow + fakeEpochPool let us drive seedEpochRow without a DB.
type fakeEpochPool struct {
	nextID  string
	scanErr error
	calls   atomic.Int32
}

type fakeRow struct {
	id  string
	err error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) > 0 {
		if p, ok := dest[0].(*string); ok {
			*p = r.id
		}
	}
	return nil
}

func (f *fakeEpochPool) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	f.calls.Add(1)
	return fakeRow{id: f.nextID, err: f.scanErr}
}

func TestSeedEpochRow_HappyPath(t *testing.T) {
	pool := &fakeEpochPool{nextID: "epoch-uuid-1"}
	id, err := seedEpochRow(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if id != "epoch-uuid-1" {
		t.Errorf("id = %q", id)
	}
}

func TestSeedEpochRow_PropagatesScanError(t *testing.T) {
	pool := &fakeEpochPool{scanErr: errors.New("rpc died")}
	if _, err := seedEpochRow(context.Background(), pool); err == nil {
		t.Error("expected scan error to propagate")
	}
}

func TestRunEpochCron_FiresOnBootAndOnTick(t *testing.T) {
	pool := &fakeEpochPool{nextID: "epoch-1"}
	orch, err := orchestrator.NewRootOrchestrator(orchestrator.RootOrchestratorConfig{
		Store:     noopStore{},
		Lifecycle: noopLifecycle{},
		Breaker:   breakerAdapter{},
		Bus:       noopBus{},
		LLM:       noopLLM{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = runEpochCron(ctx, zap.NewNop(), pool, orch, 50*time.Millisecond)
		close(done)
	}()
	// Wait long enough for at least one boot run + one tick.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	if got := pool.calls.Load(); got < 2 {
		t.Errorf("expected ≥2 epoch runs (boot + tick), got %d", got)
	}
}

func TestEventBusAdapter_Forwards(t *testing.T) {
	rdb := newFakeRedis()
	bus := eventBusAdapter{rdb: rdb}
	if err := bus.Publish(context.Background(), "events:test", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if got := rdb.lastChannel; got != "events:test" {
		t.Errorf("channel = %q", got)
	}
	if got := string(rdb.lastPayload); got != "hi" {
		t.Errorf("payload = %q", got)
	}
}
