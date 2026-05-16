package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/agent/tasks"
	"github.com/veermshah/cambrian/internal/llm"
)

func newTestRunner(t *testing.T, opts ...func(*NodeRunnerConfig)) (*NodeRunner, *fakeStore, *tasks.FakeTask, *llm.FakeLLMClient, *fakeClock) {
	t.Helper()
	store := newFakeStore()
	task := &tasks.FakeTask{
		TickTrades: []tasks.Trade{{
			Chain: "solana", TradeType: "swap", TokenPair: "SOL/USDC", DEX: "jup",
			AmountIn: 1, AmountOut: 1, BanditPolicyUsed: "default",
		}},
		Summary: map[string]interface{}{"positions": 1},
	}
	fake := llm.NewFakeLLMClient("fake-model").
		WithResponse(`{"reasoning":"hold","action_signal":"continue"}`).
		WithTokenUsage(50, 20)
	clk := newFakeClock(time.Unix(1_700_000_000, 0).UTC())

	cfg := NodeRunnerConfig{
		AgentID:            "agent-1",
		Genome:             agent.AgentGenome{Name: "alpha", TaskType: "fake", Chain: "solana"},
		Task:               task,
		LLM:                fake,
		Store:              store,
		Scheduler:          NewScheduler(2, 0),
		Log:                agent.NopLogger{},
		MonitorInterval:    1 * time.Second,
		StrategistInterval: 1 * time.Second,
		HeartbeatInterval:  1 * time.Second,
		Clock:              clk,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	nr, err := NewNodeRunner(cfg)
	if err != nil {
		t.Fatalf("NewNodeRunner: %v", err)
	}
	return nr, store, task, fake, clk
}

func TestNodeRunner_RejectsMissingDeps(t *testing.T) {
	cases := []struct {
		name string
		cfg  NodeRunnerConfig
	}{
		{"no agent id", NodeRunnerConfig{Task: &tasks.FakeTask{}, LLM: llm.NewFakeLLMClient("m"), Store: newFakeStore()}},
		{"no task", NodeRunnerConfig{AgentID: "a", LLM: llm.NewFakeLLMClient("m"), Store: newFakeStore()}},
		{"no llm", NodeRunnerConfig{AgentID: "a", Task: &tasks.FakeTask{}, Store: newFakeStore()}},
		{"no store", NodeRunnerConfig{AgentID: "a", Task: &tasks.FakeTask{}, LLM: llm.NewFakeLLMClient("m")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewNodeRunner(c.cfg); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestNodeRunner_RunCadence(t *testing.T) {
	nr, store, task, fake, clk := newTestRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- nr.Run(ctx) }()

	// Wait until all three loops have registered their tickers.
	waitFor(t, func() bool { return clk.TickerCount() >= 3 })

	// Wait for the immediate heartbeat the loop writes on start.
	waitFor(t, func() bool { return store.heartbeatCount("agent-1") >= 1 })

	// Fire a few rounds of all three tickers.
	for i := 0; i < 3; i++ {
		clk.Fire()
		waitFor(t, func() bool { return task.RunTickCallCount >= i+1 })
	}

	// At least three monitor ticks, three strategist cycles, and four
	// heartbeats (1 immediate + 3 from Fire) should be visible.
	if task.RunTickCallCount < 3 {
		t.Errorf("RunTick called %d times, want ≥ 3", task.RunTickCallCount)
	}
	if got := nr.StrategistCount(); got < 3 {
		t.Errorf("StrategistCount = %d, want ≥ 3", got)
	}
	if got := len(fake.Calls()); got < 3 {
		t.Errorf("LLM call count = %d, want ≥ 3", got)
	}
	if got := store.tradeCount(); got < 3 {
		t.Errorf("trades logged = %d, want ≥ 3", got)
	}
	if got := store.decisionCount(); got < 3 {
		t.Errorf("decisions logged = %d, want ≥ 3", got)
	}
	if got := store.heartbeatCount("agent-1"); got < 4 {
		t.Errorf("heartbeats = %d, want ≥ 4", got)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if task.CloseAllCallCount != 1 {
		t.Errorf("CloseAllPositions called %d times, want 1", task.CloseAllCallCount)
	}
}

func TestNodeRunner_PauseSkipsMonitorAndStrategist(t *testing.T) {
	nr, store, task, _, clk := newTestRunner(t)
	nr.Pause()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go nr.Run(ctx)
	waitFor(t, func() bool { return clk.TickerCount() >= 3 })
	waitFor(t, func() bool { return store.heartbeatCount("agent-1") >= 1 })

	for i := 0; i < 5; i++ {
		clk.Fire()
	}
	// Heartbeats keep running while paused.
	waitFor(t, func() bool { return store.heartbeatCount("agent-1") >= 4 })

	if task.RunTickCallCount != 0 {
		t.Errorf("RunTick called %d times while paused, want 0", task.RunTickCallCount)
	}
	if got := nr.StrategistCount(); got != 0 {
		t.Errorf("StrategistCount = %d while paused, want 0", got)
	}

	// Resume and verify ticks resume.
	nr.Resume()
	clk.Fire()
	waitFor(t, func() bool { return task.RunTickCallCount >= 1 })
}

func TestNodeRunner_MalformedLLMStillWritesDecision(t *testing.T) {
	nr, store, _, _, clk := newTestRunner(t, func(c *NodeRunnerConfig) {
		c.LLM = llm.NewFakeLLMClient("fake-model").
			WithResponse("not even close to json").
			WithTokenUsage(10, 5)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go nr.Run(ctx)

	waitFor(t, func() bool { return clk.TickerCount() >= 3 })
	clk.Fire()
	waitFor(t, func() bool { return store.decisionCount() >= 1 })

	store.mu.Lock()
	defer store.mu.Unlock()
	d := store.decisions[0]
	if d.OutputRaw == "" {
		t.Error("decision row should preserve OutputRaw on parse failure")
	}
	if len(d.ConfigChanges) != 0 {
		t.Errorf("config_changes should be empty on parse failure, got %s", d.ConfigChanges)
	}
}

func TestNodeRunner_HeartbeatErrorIsNonFatal(t *testing.T) {
	nr, store, _, _, clk := newTestRunner(t)
	store.beatErr = errors.New("boom")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- nr.Run(ctx) }()

	waitFor(t, func() bool { return clk.TickerCount() >= 3 })
	clk.Fire()
	// Loop should still be alive after a failed heartbeat — verify by
	// firing again and seeing strategist count progress.
	clk.Fire()
	waitFor(t, func() bool { return nr.StrategistCount() >= 1 })

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
