package tasks

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/chain"
)

func newTestLP(t *testing.T, price float64, opts ...func(*LiquidityProvisionConfig)) (*LiquidityProvision, *chain.FakeChainClient, *fakeClock) {
	t.Helper()
	sol := chain.NewFake("solana", "SOL").
		WithQuote("USDC", "SOL", &chain.Quote{
			Chain: "solana", DEX: "orca",
			TokenIn: "USDC", TokenOut: "SOL",
			AmountIn: 1, AmountOut: price, Price: price,
		})
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0).UTC()}

	cfg := LiquidityProvisionConfig{
		Chain:                 "solana",
		TokenPair:             "USDC/SOL",
		PoolAddress:           "ORCA_USDC_SOL",
		FeeTier:               "0.30",
		RangeWidthPct:         0.10,
		RebalanceThresholdPct: 0.03,
		CheckIntervalSecs:     60,
		MinCapitalToOperate:   10,
	}
	for _, o := range opts {
		o(&cfg)
	}
	task, err := NewLiquidityProvision(LiquidityProvisionDeps{
		Clients: map[string]chain.ChainClient{"solana": sol},
		Wallets: map[string]*chain.Wallet{"solana": {Chain: "solana", Address: "sol1"}},
		Now:     clk.Now,
	}, cfg)
	if err != nil {
		t.Fatalf("NewLiquidityProvision: %v", err)
	}
	task.SeedFreeCapital(1000)
	return task, sol, clk
}

// stagePrice swaps the staged quote on the given fake to reflect a new
// USDC→SOL price.
func stagePrice(f *chain.FakeChainClient, price float64) {
	f.WithQuote("USDC", "SOL", &chain.Quote{
		Chain: "solana", DEX: "orca",
		TokenIn: "USDC", TokenOut: "SOL",
		AmountIn: 1, AmountOut: price, Price: price,
	})
}

func TestLP_OpensPositionOnFirstTick(t *testing.T) {
	task, _, _ := newTestLP(t, 100.0)
	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(trades) != 1 || trades[0].TradeType != "lp_open" {
		t.Fatalf("expected single lp_open trade, got %+v", trades)
	}
	if task.position == nil {
		t.Fatal("expected open position after first tick")
	}
	if task.freeCapital != 0 {
		t.Errorf("freeCapital should be drained to position; got %v", task.freeCapital)
	}
	gotLower := task.position.LowerPrice
	gotUpper := task.position.UpperPrice
	if math.Abs(gotLower-95.0) > 1e-6 || math.Abs(gotUpper-105.0) > 1e-6 {
		t.Errorf("expected band [95,105] for 10%% width around 100, got [%v,%v]", gotLower, gotUpper)
	}
}

func TestLP_StablePriceNoRebalance(t *testing.T) {
	task, fake, clk := newTestLP(t, 100.0)

	// Open
	if _, err := task.RunTick(context.Background()); err != nil {
		t.Fatalf("first RunTick: %v", err)
	}

	// Same price, advance past throttle.
	clk.Advance(2 * time.Minute)
	stagePrice(fake, 100.0)
	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(trades) != 0 {
		t.Errorf("expected no rebalance on stable price, got %d trades: %+v", len(trades), trades)
	}
}

func TestLP_SmallDriftNoRebalance(t *testing.T) {
	task, fake, clk := newTestLP(t, 100.0)
	if _, err := task.RunTick(context.Background()); err != nil {
		t.Fatalf("first RunTick: %v", err)
	}

	// 1 % drift, default 3 % threshold.
	clk.Advance(2 * time.Minute)
	stagePrice(fake, 101.0)
	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(trades) != 0 {
		t.Errorf("expected no rebalance for 1%% drift, got %d trades", len(trades))
	}
}

func TestLP_LargeDriftRebalances(t *testing.T) {
	task, fake, clk := newTestLP(t, 100.0)
	if _, err := task.RunTick(context.Background()); err != nil {
		t.Fatalf("first RunTick: %v", err)
	}
	openedCenter := task.position.CenterPrice

	clk.Advance(2 * time.Minute)
	stagePrice(fake, 105.5) // 5.5 % drift, threshold 3 %
	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	var sawClose, sawOpen bool
	for _, tr := range trades {
		switch tr.TradeType {
		case "lp_close":
			sawClose = true
		case "lp_open":
			sawOpen = true
		}
	}
	if !sawClose || !sawOpen {
		t.Errorf("expected lp_close + lp_open, got %+v", trades)
	}
	if task.position == nil {
		t.Fatal("expected new position after rebalance")
	}
	if task.position.CenterPrice == openedCenter {
		t.Errorf("rebalanced band should recenter on new price, got same center %v", openedCenter)
	}
	if math.Abs(task.position.CenterPrice-105.5) > 1e-6 {
		t.Errorf("expected recenter to 105.5, got %v", task.position.CenterPrice)
	}
}

func TestLP_PullLiquidityAdjustmentClosesPosition(t *testing.T) {
	task, _, _ := newTestLP(t, 100.0)
	if _, err := task.RunTick(context.Background()); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if task.position == nil {
		t.Fatal("expected open position before pull_liquidity")
	}
	if err := task.ApplyAdjustments(map[string]interface{}{
		"pull_liquidity": true,
	}); err != nil {
		t.Fatalf("ApplyAdjustments: %v", err)
	}
	if task.position != nil {
		t.Errorf("expected position closed after pull_liquidity, got %+v", task.position)
	}
}

func TestLP_ApplyAdjustmentsClamps(t *testing.T) {
	task, _, _ := newTestLP(t, 100.0)
	if err := task.ApplyAdjustments(map[string]interface{}{
		"range_width_pct":         5.0,  // ceil 1.0
		"rebalance_threshold_pct": 0.0001, // floor 0.001
		"check_interval_secs":     0.0,  // floor 5
	}); err != nil {
		t.Fatalf("ApplyAdjustments: %v", err)
	}
	if task.cfg.RangeWidthPct != rangeWidthCeil {
		t.Errorf("RangeWidthPct not clamped: %v", task.cfg.RangeWidthPct)
	}
	if task.cfg.RebalanceThresholdPct != rebalanceThreshFloor {
		t.Errorf("RebalanceThresholdPct not clamped: %v", task.cfg.RebalanceThresholdPct)
	}
	if task.cfg.CheckIntervalSecs != lpCheckIntervalFloor {
		t.Errorf("CheckIntervalSecs not clamped: %v", task.cfg.CheckIntervalSecs)
	}
}

func TestLP_ApplyAdjustmentsRejectsNonNumeric(t *testing.T) {
	task, _, _ := newTestLP(t, 100.0)
	err := task.ApplyAdjustments(map[string]interface{}{
		"range_width_pct": "wide",
	})
	if err == nil {
		t.Error("expected error on non-numeric adjustment")
	}
}

func TestLP_ImpermanentLossMonotonic(t *testing.T) {
	// IL(p_entry, p_current) should be ≤ 0 and monotonically more
	// negative as |log(p_current / p_entry)| grows.
	entry := 100.0
	ratios := []float64{1.0, 1.05, 1.10, 1.25, 1.50, 2.0, 4.0}

	var prev float64
	for i, r := range ratios {
		il := ImpermanentLoss(entry, entry*r)
		if il > 1e-12 {
			t.Errorf("IL should be non-positive, got %v at r=%v", il, r)
		}
		if i > 0 {
			if il > prev+1e-12 {
				t.Errorf("IL not monotonic upward at r=%v: prev=%v cur=%v", r, prev, il)
			}
		}
		prev = il
	}

	// Symmetric — down-moves give same magnitude as up-moves.
	upper := ImpermanentLoss(entry, entry*2.0)
	lower := ImpermanentLoss(entry, entry/2.0)
	if math.Abs(upper-lower) > 1e-9 {
		t.Errorf("IL should be symmetric for r and 1/r; got %v vs %v", upper, lower)
	}
}

func TestLP_GetStateSummaryShape(t *testing.T) {
	task, _, _ := newTestLP(t, 100.0)
	if _, err := task.RunTick(context.Background()); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	sum, err := task.GetStateSummary(context.Background())
	if err != nil {
		t.Fatalf("GetStateSummary: %v", err)
	}
	for _, k := range []string{"band", "fees_24h_usd", "il_estimate", "last_price", "token_pair"} {
		if _, ok := sum[k]; !ok {
			t.Errorf("summary missing %q: %+v", k, sum)
		}
	}
	b, _ := json.Marshal(sum)
	if len(b) > 2000 {
		t.Errorf("summary too large for 500-token budget: %d bytes", len(b))
	}
}

func TestLP_CloseAllPositionsExits(t *testing.T) {
	task, _, _ := newTestLP(t, 100.0)
	if _, err := task.RunTick(context.Background()); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	trades, err := task.CloseAllPositions(context.Background())
	if err != nil {
		t.Fatalf("CloseAllPositions: %v", err)
	}
	if len(trades) != 1 || trades[0].TradeType != "lp_close" {
		t.Errorf("expected one lp_close, got %+v", trades)
	}
	if task.position != nil {
		t.Errorf("expected nil position after CloseAllPositions")
	}
}

func TestLP_CheckIntervalThrottlesPolling(t *testing.T) {
	task, _, clk := newTestLP(t, 100.0)
	if _, err := task.RunTick(context.Background()); err != nil {
		t.Fatalf("first RunTick: %v", err)
	}
	firstLastCheck := task.lastCheck

	clk.Advance(10 * time.Second) // below 60s
	if _, err := task.RunTick(context.Background()); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if !task.lastCheck.Equal(firstLastCheck) {
		t.Errorf("lastCheck should not advance inside throttle window")
	}
}

func TestLP_FactoryRequiresDeps(t *testing.T) {
	prev := liquidityProvisionDeps.Load()
	liquidityProvisionDeps.Store(nil)
	t.Cleanup(func() { liquidityProvisionDeps.Store(prev) })

	if _, err := Build(context.Background(), "liquidity_provision", json.RawMessage(`{}`)); err == nil {
		t.Error("expected error when deps not set")
	}
}

func TestLP_FactoryWithDeps(t *testing.T) {
	sol := chain.NewFake("solana", "SOL").
		WithQuote("USDC", "SOL", &chain.Quote{
			Chain: "solana", DEX: "orca",
			TokenIn: "USDC", TokenOut: "SOL",
			AmountIn: 1, AmountOut: 100, Price: 100,
		})
	SetLiquidityProvisionDeps(LiquidityProvisionDeps{
		Clients: map[string]chain.ChainClient{"solana": sol},
	})
	t.Cleanup(func() { liquidityProvisionDeps.Store(nil) })

	cfg := LiquidityProvisionConfig{
		Chain:                 "solana",
		TokenPair:             "USDC/SOL",
		PoolAddress:           "ORCA_USDC_SOL",
		FeeTier:               "0.30",
		RangeWidthPct:         0.10,
		RebalanceThresholdPct: 0.03,
		CheckIntervalSecs:     60,
	}
	raw, _ := json.Marshal(cfg)
	task, err := Build(context.Background(), "liquidity_provision", raw)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := task.(*LiquidityProvision); !ok {
		t.Errorf("expected *LiquidityProvision, got %T", task)
	}
}

func TestLP_SplitTokenPair(t *testing.T) {
	cases := []struct {
		in, a, b string
		wantErr  bool
	}{
		{"USDC/SOL", "USDC", "SOL", false},
		{"weth-usdc", "WETH", "USDC", false},
		{"USDC:DAI", "USDC", "DAI", false},
		{"USDC", "", "", true},
		{"", "", "", true},
		{"/USDC", "", "", true},
	}
	for _, c := range cases {
		a, b, err := splitTokenPair(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("splitTokenPair(%q) expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitTokenPair(%q) error: %v", c.in, err)
			continue
		}
		if a != c.a || b != c.b {
			t.Errorf("splitTokenPair(%q) = (%q,%q), want (%q,%q)", c.in, a, b, c.a, c.b)
		}
	}
}
