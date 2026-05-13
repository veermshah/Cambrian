package tasks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/chain"
)

func newTestCCY(t *testing.T, opts ...func(*CrossChainYieldConfig)) (*CrossChainYield, *chain.FakeChainClient, *chain.FakeChainClient, *fakeClock) {
	t.Helper()
	sol := chain.NewFake("solana", "SOL").
		WithYieldRate("marginfi", chain.YieldRate{Chain: "solana", Protocol: "marginfi", Asset: "USDC", APY: 0.05}).
		WithYieldRate("kamino", chain.YieldRate{Chain: "solana", Protocol: "kamino", Asset: "USDC", APY: 0.05})
	base := chain.NewFake("base", "ETH").
		WithYieldRate("aave_v3", chain.YieldRate{Chain: "base", Protocol: "aave_v3", Asset: "USDC", APY: 0.05}).
		WithYieldRate("morpho", chain.YieldRate{Chain: "base", Protocol: "morpho", Asset: "USDC", APY: 0.05})

	clk := &fakeClock{t: time.Unix(1_700_000_000, 0).UTC()}

	cfg := CrossChainYieldConfig{
		PrimaryChain:          "solana",
		AllowedProtocols:      []string{"marginfi", "kamino", "aave_v3", "morpho"},
		MinYieldDiffBps:       100,  // 1pp
		MaxSingleProtocolPct:  1.0,
		RebalanceIntervalSecs: 3600,
		BridgeCostThreshold:  100,
		MinCapitalToOperate:   10,
		CheckIntervalSecs:     60,
	}
	for _, o := range opts {
		o(&cfg)
	}

	task, err := NewCrossChainYield(CrossChainYieldDeps{
		Clients: map[string]chain.ChainClient{"solana": sol, "base": base},
		Wallets: map[string]*chain.Wallet{
			"solana": {Chain: "solana", Address: "sol1"},
			"base":   {Chain: "base", Address: "0xbase"},
		},
		Now: clk.Now,
	}, cfg)
	if err != nil {
		t.Fatalf("NewCrossChainYield: %v", err)
	}
	task.SeedFreeCapital(1000)
	return task, sol, base, clk
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time      { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func TestCrossChainYield_EqualRatesNoRebalance(t *testing.T) {
	task, _, _, clk := newTestCCY(t)
	// Seed an existing allocation at the same APY as the rates.
	task.allocation["marginfi"] = positionState{
		Chain: "solana", Asset: "USDC", ValueUSD: 1000, APY: 0.05,
		OpenedAt: clk.Now().Add(-2 * time.Hour),
	}
	task.freeCapital = 0
	// Push past the rebalance throttle.
	task.lastRebalance = clk.Now().Add(-2 * time.Hour)

	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(trades) != 0 {
		t.Errorf("expected no trades for equal-rate scenario, got %d: %+v", len(trades), trades)
	}
}

func TestCrossChainYield_DiffBelowThresholdNoRebalance(t *testing.T) {
	task, sol, _, clk := newTestCCY(t, func(c *CrossChainYieldConfig) {
		c.MinYieldDiffBps = 200 // 2 pp threshold
	})
	// Current position: 5% APY.
	task.allocation["marginfi"] = positionState{
		Chain: "solana", Asset: "USDC", ValueUSD: 1000, APY: 0.05,
		OpenedAt: clk.Now().Add(-2 * time.Hour),
	}
	task.freeCapital = 0
	task.lastRebalance = clk.Now().Add(-2 * time.Hour)
	// Best alternative: 6.5% APY → 150 bps diff < 200 bps threshold.
	sol.WithYieldRate("kamino", chain.YieldRate{Chain: "solana", Protocol: "kamino", Asset: "USDC", APY: 0.065})

	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(trades) != 0 {
		t.Errorf("expected no rebalance below threshold, got %d trades", len(trades))
	}
}

func TestCrossChainYield_DiffAboveThresholdWithinIntervalNoRebalance(t *testing.T) {
	task, sol, _, clk := newTestCCY(t, func(c *CrossChainYieldConfig) {
		c.MinYieldDiffBps = 100
		c.RebalanceIntervalSecs = 3600
	})
	task.allocation["marginfi"] = positionState{
		Chain: "solana", Asset: "USDC", ValueUSD: 1000, APY: 0.05,
		OpenedAt: clk.Now().Add(-30 * time.Minute),
	}
	task.freeCapital = 0
	// Last rebalance 30 minutes ago — still inside the 1-hour throttle.
	task.lastRebalance = clk.Now().Add(-30 * time.Minute)

	// Plenty above threshold: 200 bps diff.
	sol.WithYieldRate("kamino", chain.YieldRate{Chain: "solana", Protocol: "kamino", Asset: "USDC", APY: 0.07})

	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(trades) != 0 {
		t.Errorf("expected no rebalance inside throttle, got %d trades", len(trades))
	}
}

func TestCrossChainYield_DiffAboveThresholdPastIntervalRebalances(t *testing.T) {
	task, sol, _, clk := newTestCCY(t, func(c *CrossChainYieldConfig) {
		c.MinYieldDiffBps = 100
		c.RebalanceIntervalSecs = 3600
	})
	task.allocation["marginfi"] = positionState{
		Chain: "solana", Asset: "USDC", ValueUSD: 1000, APY: 0.05,
		OpenedAt: clk.Now().Add(-2 * time.Hour),
	}
	task.freeCapital = 0
	task.lastRebalance = clk.Now().Add(-2 * time.Hour)

	sol.WithYieldRate("kamino", chain.YieldRate{Chain: "solana", Protocol: "kamino", Asset: "USDC", APY: 0.07})

	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(trades) == 0 {
		t.Fatalf("expected rebalance, got no trades")
	}
	// Should have at least one yield_exit (from marginfi) and one
	// yield_enter (to kamino, the new highest-APY same-chain protocol).
	var sawExit, sawEnter bool
	var enteredProto string
	for _, tr := range trades {
		if tr.TradeType == "yield_exit" && tr.DEX == "marginfi" {
			sawExit = true
		}
		if tr.TradeType == "yield_enter" {
			sawEnter = true
			enteredProto = tr.DEX
		}
	}
	if !sawExit {
		t.Errorf("expected yield_exit from marginfi: %+v", trades)
	}
	if !sawEnter {
		t.Errorf("expected yield_enter: %+v", trades)
	}
	if enteredProto != "kamino" {
		t.Errorf("expected to enter kamino, got %q", enteredProto)
	}
	if got, _ := task.GetPositionValue(context.Background()); got <= 0 {
		t.Errorf("position value after rebalance should be > 0, got %v", got)
	}
}

func TestCrossChainYield_SingleProtocolCapEnforced(t *testing.T) {
	task, sol, _, clk := newTestCCY(t, func(c *CrossChainYieldConfig) {
		c.MaxSingleProtocolPct = 0.4
		c.MinYieldDiffBps = 50
	})
	// No existing allocation — let rebalance fully decide allocations
	// from a non-zero free capital pool.
	task.freeCapital = 1000

	// One clearly-best rate; cap should still force a split.
	sol.WithYieldRate("kamino", chain.YieldRate{Chain: "solana", Protocol: "kamino", Asset: "USDC", APY: 0.10})
	sol.WithYieldRate("marginfi", chain.YieldRate{Chain: "solana", Protocol: "marginfi", Asset: "USDC", APY: 0.08})
	_ = clk

	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(trades) == 0 {
		t.Fatalf("expected initial rebalance, got no trades")
	}

	total, _ := task.GetPositionValue(context.Background())
	if total <= 0 {
		t.Fatalf("expected positive position value, got %v", total)
	}
	// Every position must be at or below 40% of total deployed.
	for proto, p := range task.allocation {
		share := p.ValueUSD / total
		if share > task.cfg.MaxSingleProtocolPct+1e-9 {
			t.Errorf("protocol %s holds %.2f%% > cap %.2f%%", proto, share*100, task.cfg.MaxSingleProtocolPct*100)
		}
	}
	if len(task.allocation) < 2 {
		t.Errorf("cap of 40%% should force a split across ≥2 protocols, got %d", len(task.allocation))
	}
}

func TestCrossChainYield_CheckIntervalThrottlesPolling(t *testing.T) {
	task, sol, _, clk := newTestCCY(t)
	sol.WithYieldRate("kamino", chain.YieldRate{Chain: "solana", Protocol: "kamino", Asset: "USDC", APY: 0.10})

	// First call: lastCheck is zero, runs.
	if _, err := task.RunTick(context.Background()); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	firstLastCheck := task.lastCheck

	// Advance only 10 s — below the 60 s CheckIntervalSecs.
	clk.Advance(10 * time.Second)
	if _, err := task.RunTick(context.Background()); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if !task.lastCheck.Equal(firstLastCheck) {
		t.Errorf("lastCheck should not advance inside throttle window: was %v now %v", firstLastCheck, task.lastCheck)
	}

	// Advance past the throttle and try again — now lastCheck should move.
	clk.Advance(time.Minute)
	if _, err := task.RunTick(context.Background()); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if task.lastCheck.Equal(firstLastCheck) {
		t.Errorf("lastCheck should advance after throttle window")
	}
}

func TestCrossChainYield_ApplyAdjustmentsClamps(t *testing.T) {
	task, _, _, _ := newTestCCY(t)

	// Out-of-range high.
	if err := task.ApplyAdjustments(map[string]interface{}{
		"min_yield_diff_bps":      10000.0, // ceil is 5000
		"max_single_protocol_pct": 5.0,     // ceil is 1.0
		"bridge_cost_threshold":   99999.0, // ceil is 10000
		"rebalance_interval_secs": 99999999.0,
		"check_interval_secs":     0.1, // floor is 5
	}); err != nil {
		t.Fatalf("ApplyAdjustments: %v", err)
	}
	if task.cfg.MinYieldDiffBps != minYieldDiffBpsCeil {
		t.Errorf("MinYieldDiffBps not clamped to ceil: got %v", task.cfg.MinYieldDiffBps)
	}
	if task.cfg.MaxSingleProtocolPct != maxSingleProtocolCeil {
		t.Errorf("MaxSingleProtocolPct not clamped to ceil: got %v", task.cfg.MaxSingleProtocolPct)
	}
	if task.cfg.BridgeCostThreshold != bridgeCostCeil {
		t.Errorf("BridgeCostThreshold not clamped to ceil: got %v", task.cfg.BridgeCostThreshold)
	}
	if task.cfg.RebalanceIntervalSecs != rebalanceIntervalCeil {
		t.Errorf("RebalanceIntervalSecs not clamped to ceil: got %v", task.cfg.RebalanceIntervalSecs)
	}
	if task.cfg.CheckIntervalSecs != checkIntervalSecsFloor {
		t.Errorf("CheckIntervalSecs not clamped to floor: got %v", task.cfg.CheckIntervalSecs)
	}

	// Out-of-range low.
	if err := task.ApplyAdjustments(map[string]interface{}{
		"min_yield_diff_bps":      0.0,
		"max_single_protocol_pct": 0.0,
		"bridge_cost_threshold":   -10.0,
		"rebalance_interval_secs": 1.0,
	}); err != nil {
		t.Fatalf("ApplyAdjustments low: %v", err)
	}
	if task.cfg.MinYieldDiffBps != minYieldDiffBpsFloor {
		t.Errorf("MinYieldDiffBps not clamped to floor: got %v", task.cfg.MinYieldDiffBps)
	}
	if task.cfg.MaxSingleProtocolPct != maxSingleProtocolFloor {
		t.Errorf("MaxSingleProtocolPct not clamped to floor: got %v", task.cfg.MaxSingleProtocolPct)
	}
	if task.cfg.BridgeCostThreshold != bridgeCostFloor {
		t.Errorf("BridgeCostThreshold not clamped to floor: got %v", task.cfg.BridgeCostThreshold)
	}
	if task.cfg.RebalanceIntervalSecs != rebalanceIntervalFloor {
		t.Errorf("RebalanceIntervalSecs not clamped to floor: got %v", task.cfg.RebalanceIntervalSecs)
	}
}

func TestCrossChainYield_ApplyAdjustmentsRejectsNonNumeric(t *testing.T) {
	task, _, _, _ := newTestCCY(t)
	err := task.ApplyAdjustments(map[string]interface{}{
		"min_yield_diff_bps": "not-a-number",
	})
	if err == nil {
		t.Error("expected error on non-numeric adjustment")
	}
}

func TestCrossChainYield_ApplyAdjustmentsIgnoresUnknownKeys(t *testing.T) {
	task, _, _, _ := newTestCCY(t)
	if err := task.ApplyAdjustments(map[string]interface{}{
		"unknown_key": 42.0,
	}); err != nil {
		t.Errorf("unknown key should be ignored, got: %v", err)
	}
}

func TestCrossChainYield_CloseAllPositionsExits(t *testing.T) {
	task, _, _, clk := newTestCCY(t)
	task.allocation["marginfi"] = positionState{
		Chain: "solana", Asset: "USDC", ValueUSD: 600, APY: 0.05, OpenedAt: clk.Now(),
	}
	task.allocation["aave_v3"] = positionState{
		Chain: "base", Asset: "USDC", ValueUSD: 400, APY: 0.06, OpenedAt: clk.Now(),
	}
	task.freeCapital = 0

	trades, err := task.CloseAllPositions(context.Background())
	if err != nil {
		t.Fatalf("CloseAllPositions: %v", err)
	}
	if len(trades) != 2 {
		t.Fatalf("expected 2 exit trades, got %d", len(trades))
	}
	if len(task.allocation) != 0 {
		t.Errorf("allocation should be empty after CloseAllPositions, got %d", len(task.allocation))
	}
	if task.freeCapital != 1000 {
		t.Errorf("freeCapital should be 1000 (returned to wallet), got %v", task.freeCapital)
	}
}

func TestCrossChainYield_GetStateSummaryShape(t *testing.T) {
	task, _, _, clk := newTestCCY(t)
	task.allocation["marginfi"] = positionState{
		Chain: "solana", Asset: "USDC", ValueUSD: 1000, APY: 0.05, OpenedAt: clk.Now().Add(-time.Hour),
	}
	task.freeCapital = 0
	task.lastRebalance = clk.Now().Add(-time.Hour)

	if _, err := task.RunTick(context.Background()); err != nil {
		t.Fatalf("RunTick: %v", err)
	}

	sum, err := task.GetStateSummary(context.Background())
	if err != nil {
		t.Fatalf("GetStateSummary: %v", err)
	}
	for _, key := range []string{"allocation", "free_capital_usd", "top_rates", "last_rebalance_at", "realized_24h_usd"} {
		if _, ok := sum[key]; !ok {
			t.Errorf("summary missing key %q", key)
		}
	}
	// 500-token budget check — serialize and look at byte length.
	b, err := json.Marshal(sum)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	if len(b) > 4000 { // ~500 tokens at ~8 bytes/token average
		t.Errorf("summary serializes to %d bytes — too large for strategist 500-token budget", len(b))
	}
}

func TestCrossChainYield_FactoryRequiresDeps(t *testing.T) {
	// Make sure the registered init() factory complains when no deps
	// are wired. Use a fresh registry to be safe.
	ResetForTest()
	t.Cleanup(func() {
		ResetForTest()
		// Re-run init via direct Register so subsequent tests see
		// cross_chain_yield in the registry.
		Register("cross_chain_yield", crossChainYieldFactory)
	})
	Register("cross_chain_yield", crossChainYieldFactory)

	// Stash and clear deps pointer for the duration of the test.
	prev := crossChainYieldDeps.Load()
	crossChainYieldDeps.Store(nil)
	t.Cleanup(func() { crossChainYieldDeps.Store(prev) })

	if _, err := Build(context.Background(), "cross_chain_yield", json.RawMessage(`{}`)); err == nil {
		t.Error("expected error when deps not set")
	}
}

func TestCrossChainYield_FactoryWithDeps(t *testing.T) {
	sol := chain.NewFake("solana", "SOL")
	SetCrossChainYieldDeps(CrossChainYieldDeps{
		Clients: map[string]chain.ChainClient{"solana": sol},
	})
	t.Cleanup(func() { crossChainYieldDeps.Store(nil) })

	cfg := CrossChainYieldConfig{
		PrimaryChain:     "solana",
		AllowedProtocols: []string{"marginfi"},
		MinYieldDiffBps:  100,
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	task, err := Build(context.Background(), "cross_chain_yield", raw)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := task.(*CrossChainYield); !ok {
		t.Errorf("expected *CrossChainYield, got %T", task)
	}
}
