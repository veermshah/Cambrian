package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/chain"
)

func newLHTask(t *testing.T, fake *chain.FakeChainClient, cfg LiquidationHuntingConfig) *LiquidationHunting {
	t.Helper()
	task, err := NewLiquidationHunting(LiquidationHuntingDeps{
		Clients: map[string]chain.ChainClient{cfg.Chain: fake},
		Wallets: map[string]*chain.Wallet{cfg.Chain: {Chain: cfg.Chain, Address: "test-wallet"}},
		Now:     func() time.Time { return time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC) },
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	task.SeedFreeCapital(10_000)
	return task
}

func baseCfg() LiquidationHuntingConfig {
	return LiquidationHuntingConfig{
		Chain:                 "solana",
		Protocols:             []string{"marginfi"},
		MinProfitUSD:          5,
		HealthFactorThreshold: 1.0,
		CheckIntervalSecs:     10,
		MaxPositionSizeUSD:    50_000,
		MaxDailyLiquidations:  20,
		MinCapitalToOperate:   100,
	}
}

func atRiskPosition(collateral, bonusBps, hf float64) chain.LendingPosition {
	return chain.LendingPosition{
		Chain:            "solana",
		Protocol:         "marginfi",
		Owner:            "borrower-1",
		CollateralAsset:  "SOL",
		CollateralAmt:    collateral,
		DebtAsset:        "USDC",
		DebtAmt:          collateral * 0.9,
		HealthFactor:     hf,
		LiquidationBonus: bonusBps,
	}
}

func TestLH_HealthyPositionsNoAction(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	// All positions above the 1.0 threshold — task should leave them alone.
	fake.WithLendingPosition("marginfi", atRiskPosition(1000, 500, 1.5))
	fake.WithLendingPosition("marginfi", atRiskPosition(2000, 700, 1.1))
	task := newLHTask(t, fake, baseCfg())
	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 0 {
		t.Errorf("expected no trades on healthy positions, got %d", len(trades))
	}
	if len(fake.Liquidations) != 0 {
		t.Errorf("ExecuteLiquidation was called: %v", fake.Liquidations)
	}
}

func TestLH_AtRiskAndProfitableTriggers(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	// Position: 1000 SOL collateral, 500 bps (5%) bonus → est profit $50
	// HF 0.9 < 1.0 threshold → at risk; profit > $5 floor → should fire.
	fake.WithLendingPosition("marginfi", atRiskPosition(1000, 500, 0.9))
	task := newLHTask(t, fake, baseCfg())
	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 1 {
		t.Fatalf("expected 1 liquidation trade, got %d", len(trades))
	}
	tr := trades[0]
	if tr.TradeType != "liquidation" {
		t.Errorf("TradeType = %q, want liquidation", tr.TradeType)
	}
	if tr.PnL <= 0 {
		t.Errorf("PnL = %v, want positive", tr.PnL)
	}
	if len(fake.Liquidations) != 1 {
		t.Errorf("ExecuteLiquidation called %d times, want 1", len(fake.Liquidations))
	}
}

func TestLH_AtRiskButUnprofitableSkips(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	// 10 collateral × 100 bps (1%) = $0.10 profit < $5 floor.
	fake.WithLendingPosition("marginfi", atRiskPosition(10, 100, 0.9))
	task := newLHTask(t, fake, baseCfg())
	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 0 {
		t.Errorf("expected no trades for unprofitable position, got %d", len(trades))
	}
	if len(fake.Liquidations) != 0 {
		t.Error("ExecuteLiquidation should not be called for unprofitable position")
	}
}

func TestLH_MaxDailyLiquidationsEnforced(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	// Stage 5 at-risk, profitable positions.
	for i := 0; i < 5; i++ {
		fake.WithLendingPosition("marginfi", atRiskPosition(1000, 500, 0.9))
	}
	cfg := baseCfg()
	cfg.MaxDailyLiquidations = 3
	task := newLHTask(t, fake, cfg)
	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 3 {
		t.Errorf("expected 3 trades (cap), got %d", len(trades))
	}
	if len(fake.Liquidations) != 3 {
		t.Errorf("ExecuteLiquidation called %d times, want 3 (cap)", len(fake.Liquidations))
	}

	// Tick advance under cooldown should be a no-op (interval gate).
	trades, err = task.RunTick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 0 {
		t.Errorf("expected interval gate to suppress immediate retick, got %d", len(trades))
	}
}

func TestLH_DailyCapResetsAtMidnight(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	fake.WithLendingPosition("marginfi", atRiskPosition(1000, 500, 0.9))
	cfg := baseCfg()
	cfg.MaxDailyLiquidations = 1
	cfg.CheckIntervalSecs = 1
	// Movable clock.
	now := time.Date(2026, 6, 8, 23, 59, 50, 0, time.UTC)
	clock := &now
	task, err := NewLiquidationHunting(LiquidationHuntingDeps{
		Clients: map[string]chain.ChainClient{cfg.Chain: fake},
		Wallets: map[string]*chain.Wallet{cfg.Chain: {Address: "w"}},
		Now:     func() time.Time { return *clock },
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	task.SeedFreeCapital(10_000)

	// Day 1 tick → uses the only allowed liquidation.
	if trades, _ := task.RunTick(context.Background()); len(trades) != 1 {
		t.Fatalf("day1 trades = %d, want 1", len(trades))
	}
	// Same day, after interval — quota already used.
	*clock = now.Add(2 * time.Second)
	if trades, _ := task.RunTick(context.Background()); len(trades) != 0 {
		t.Errorf("quota should still be exhausted same day, got %d", len(trades))
	}
	// New UTC day — quota resets, second liquidation allowed (we re-staged
	// the position above; FakeChainClient returns the slice on every call).
	*clock = now.Add(15 * time.Minute)
	if trades, _ := task.RunTick(context.Background()); len(trades) != 1 {
		t.Errorf("after midnight quota should reset, got %d trades", len(trades))
	}
}

func TestLH_MinCapitalGate(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	fake.WithLendingPosition("marginfi", atRiskPosition(1000, 500, 0.9))
	cfg := baseCfg()
	cfg.MinCapitalToOperate = 10_000
	task := newLHTask(t, fake, cfg)
	task.SeedFreeCapital(500) // below floor
	trades, _ := task.RunTick(context.Background())
	if len(trades) != 0 {
		t.Errorf("below MinCapitalToOperate should be no-op, got %d trades", len(trades))
	}
}

func TestLH_OversizePositionSkipped(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	// 200k collateral exceeds MaxPositionSizeUSD = 50k.
	fake.WithLendingPosition("marginfi", atRiskPosition(200_000, 500, 0.9))
	task := newLHTask(t, fake, baseCfg())
	trades, _ := task.RunTick(context.Background())
	if len(trades) != 0 {
		t.Errorf("oversize position should be skipped, got %d trades", len(trades))
	}
}

func TestLH_GetStateSummaryReportsCounts(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	fake.WithLendingPosition("marginfi", atRiskPosition(1000, 500, 0.9))
	fake.WithLendingPosition("marginfi", atRiskPosition(1500, 600, 0.5))
	fake.WithLendingPosition("marginfi", atRiskPosition(2000, 700, 0.7))
	fake.WithLendingPosition("marginfi", atRiskPosition(500, 400, 0.95))
	task := newLHTask(t, fake, baseCfg())
	_, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	summary, err := task.GetStateSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary["liquidations_today"].(int) == 0 {
		t.Error("expected liquidations_today > 0")
	}
	top, ok := summary["top_at_risk"].([]map[string]interface{})
	if !ok {
		t.Fatalf("top_at_risk type = %T", summary["top_at_risk"])
	}
	if len(top) > 3 {
		t.Errorf("top_at_risk = %d, want ≤ 3", len(top))
	}
	if len(top) > 0 {
		// Must be sorted by lowest health factor first.
		first := top[0]["health_factor"].(float64)
		if first != 0.5 {
			t.Errorf("top_at_risk[0].health_factor = %v, want 0.5 (most urgent)", first)
		}
	}
	if _, ok := summary["success_rate"].(float64); !ok {
		t.Error("success_rate missing or wrong type")
	}
}

func TestLH_ApplyAdjustmentsClampsThresholds(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	task := newLHTask(t, fake, baseCfg())

	// All three knobs from spec line 1281.
	if err := task.ApplyAdjustments(map[string]interface{}{
		"min_profit_usd":          float64(999_999_999), // above ceil
		"health_factor_threshold": float64(0.1),         // below floor
		"max_daily_liquidations":  float64(99_999),      // above ceil
	}); err != nil {
		t.Fatal(err)
	}
	if task.cfg.MinProfitUSD != liqMinProfitCeil {
		t.Errorf("MinProfitUSD = %v, want %v", task.cfg.MinProfitUSD, liqMinProfitCeil)
	}
	if task.cfg.HealthFactorThreshold != liqHFThresholdFloor {
		t.Errorf("HF threshold = %v, want %v", task.cfg.HealthFactorThreshold, liqHFThresholdFloor)
	}
	if task.cfg.MaxDailyLiquidations != liqMaxDailyCeil {
		t.Errorf("max daily = %v, want %v", task.cfg.MaxDailyLiquidations, liqMaxDailyCeil)
	}

	// Unknown keys silently dropped (sibling tasks share vocabulary).
	if err := task.ApplyAdjustments(map[string]interface{}{"unrelated_key": "x"}); err != nil {
		t.Errorf("unknown key should be silently dropped, got %v", err)
	}

	// Type mismatch returns an error.
	if err := task.ApplyAdjustments(map[string]interface{}{"min_profit_usd": "high"}); err == nil {
		t.Error("expected error for non-numeric min_profit_usd")
	}
}

func TestLH_ConfigValidationRejectsBadInputs(t *testing.T) {
	cases := []struct {
		name string
		cfg  LiquidationHuntingConfig
	}{
		{"missing chain", LiquidationHuntingConfig{Protocols: []string{"x"}, MaxPositionSizeUSD: 1}},
		{"bad chain", LiquidationHuntingConfig{Chain: "ethereum", Protocols: []string{"x"}, MaxPositionSizeUSD: 1}},
		{"no protocols", LiquidationHuntingConfig{Chain: "solana", MaxPositionSizeUSD: 1}},
		{"hf too low", LiquidationHuntingConfig{Chain: "solana", Protocols: []string{"x"}, HealthFactorThreshold: 0.1, MaxPositionSizeUSD: 1}},
		{"hf too high", LiquidationHuntingConfig{Chain: "solana", Protocols: []string{"x"}, HealthFactorThreshold: 5, MaxPositionSizeUSD: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.applyDefaults()
			if err := cfg.Validate(); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestLH_MultipleProtocolsPolled(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	fake.WithLendingPosition("marginfi", atRiskPosition(1000, 500, 0.9))
	fake.WithLendingPosition("kamino", atRiskPosition(800, 400, 0.7))
	cfg := baseCfg()
	cfg.Protocols = []string{"marginfi", "kamino"}
	task := newLHTask(t, fake, cfg)
	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 2 {
		t.Errorf("expected 2 trades across protocols, got %d", len(trades))
	}
}

func TestLH_RegisteredInFactory(t *testing.T) {
	for _, name := range Registered() {
		if name == "liquidation_hunting" {
			return
		}
	}
	t.Error("liquidation_hunting not registered")
}
