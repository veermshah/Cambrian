package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/chain"
)

func newMomentum(t *testing.T, cfg MomentumConfig, fake *chain.FakeChainClient, now func() time.Time) *Momentum {
	t.Helper()
	task, err := NewMomentum(MomentumDeps{
		Clients: map[string]chain.ChainClient{cfg.Chain: fake},
		Wallets: map[string]*chain.Wallet{cfg.Chain: {Address: "w"}},
		Now:     now,
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func baseMomentumCfg() MomentumConfig {
	return MomentumConfig{
		Chain:               "solana",
		TokenPair:           "USDC/SOL",
		LookbackMinutes:     60,
		EntryThresholdPct:   0.02,
		ExitThresholdPct:    0.02,
		CheckIntervalSecs:   5, // tight for tests
		MaxPositionSizePct:  0.5,
		StopLossPerTradePct: 0.05,
		MaxDailyTrades:      10,
		MinCapitalToOperate: 0,
	}
}

func stageQuote(fake *chain.FakeChainClient, in, out string, price float64) {
	fake.WithQuote(in, out, &chain.Quote{
		TokenIn:  in,
		TokenOut: out,
		AmountIn: 1,
		AmountOut: price,
		Price:    price,
	})
}

func TestMomentum_BreakoutEntersThenExitsOnThreshold(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clock := now
	cfg := baseMomentumCfg()
	task := newMomentum(t, cfg, fake, func() time.Time { return clock })
	task.SeedFreeCapital(1000)

	// Seed a flat window: 50 samples at price = 100.
	for i := 0; i < 50; i++ {
		task.IngestSample(now.Add(-time.Duration(50-i)*time.Minute), 100, 0)
	}
	// Now the breakout — price = 105 (> 100 × 1.02 = 102 threshold).
	stageQuote(fake, "USDC", "SOL", 105)
	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 1 || trades[0].TradeType != "momentum_enter" {
		t.Fatalf("expected enter trade, got %+v", trades)
	}
	if task.position == nil {
		t.Fatal("position should be open after breakout")
	}

	// Push price back down through the mid-1% threshold.
	clock = clock.Add(10 * time.Second)
	stageQuote(fake, "USDC", "SOL", 50) // way below mid → exits
	trades, err = task.RunTick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 1 || trades[0].TradeType != "momentum_exit" {
		t.Fatalf("expected exit trade, got %+v", trades)
	}
	if task.position != nil {
		t.Error("position should be closed after exit")
	}
}

func TestMomentum_StopLossHonored(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clock := now
	cfg := baseMomentumCfg()
	task := newMomentum(t, cfg, fake, func() time.Time { return clock })
	task.SeedFreeCapital(1000)
	for i := 0; i < 50; i++ {
		task.IngestSample(now.Add(-time.Duration(50-i)*time.Minute), 100, 0)
	}
	stageQuote(fake, "USDC", "SOL", 105) // enter
	if _, err := task.RunTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	entryPrice := task.position.EntryPrice
	stopPrice := task.position.StopPrice
	if stopPrice >= entryPrice {
		t.Fatalf("stop must be below entry, got entry=%v stop=%v", entryPrice, stopPrice)
	}

	// Move price below stop → exit with stop_loss reason.
	clock = clock.Add(10 * time.Second)
	stageQuote(fake, "USDC", "SOL", stopPrice*0.99)
	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 1 {
		t.Fatalf("expected exit, got %d trades", len(trades))
	}
	if trades[0].Metadata["reason"] != "stop_loss" {
		t.Errorf("exit reason = %v, want stop_loss", trades[0].Metadata["reason"])
	}
	if trades[0].PnL >= 0 {
		t.Errorf("stop-loss exit should have negative PnL, got %v", trades[0].PnL)
	}
}

func TestMomentum_DailyCapEnforced(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clock := now
	cfg := baseMomentumCfg()
	cfg.MaxDailyTrades = 2
	cfg.CheckIntervalSecs = 5
	task := newMomentum(t, cfg, fake, func() time.Time { return clock })
	task.SeedFreeCapital(10000)

	// Helper: one full enter+exit cycle.
	cycle := func() {
		clock = clock.Add(6 * time.Second)
		stageQuote(fake, "USDC", "SOL", 200) // breakout
		task.RunTick(context.Background())
		clock = clock.Add(6 * time.Second)
		stageQuote(fake, "USDC", "SOL", 1) // crash → exit
		task.RunTick(context.Background())
	}
	for i := 0; i < 50; i++ {
		task.IngestSample(now.Add(-time.Duration(50-i)*time.Minute), 100, 0)
	}

	cycle()
	cycle()
	// Third attempt should not enter — cap reached.
	clock = clock.Add(2 * time.Second)
	stageQuote(fake, "USDC", "SOL", 200)
	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range trades {
		if tr.TradeType == "momentum_enter" {
			t.Error("should not enter after daily cap reached")
		}
	}
	if task.position != nil {
		t.Error("no position should be open after daily cap")
	}
}

func TestMomentum_VolumeConfirmationGate(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clock := now
	cfg := baseMomentumCfg()
	cfg.VolumeConfirmation = true
	task := newMomentum(t, cfg, fake, func() time.Time { return clock })
	task.SeedFreeCapital(1000)
	// Window samples have volume = 100; latest must beat the average.
	for i := 0; i < 50; i++ {
		task.IngestSample(now.Add(-time.Duration(50-i)*time.Minute), 100, 100)
	}
	// Latest sample (the one pushed by RunTick) has volume = 0
	// (chain.GetQuote doesn't surface volume), so confirmation fails.
	stageQuote(fake, "USDC", "SOL", 105)
	trades, _ := task.RunTick(context.Background())
	for _, tr := range trades {
		if tr.TradeType == "momentum_enter" {
			t.Error("volume confirmation should have suppressed entry")
		}
	}
}

func TestMomentum_NoSamplesDoesNothing(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clock := now
	cfg := baseMomentumCfg()
	task := newMomentum(t, cfg, fake, func() time.Time { return clock })
	task.SeedFreeCapital(1000)
	stageQuote(fake, "USDC", "SOL", 100)
	trades, err := task.RunTick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// First tick populates one sample; with only one sample there's no
	// breakout high to compare against.
	for _, tr := range trades {
		if tr.TradeType == "momentum_enter" {
			t.Error("should not enter on the first sample")
		}
	}
}

func TestMomentum_ApplyAdjustmentsClamps(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	task := newMomentum(t, baseMomentumCfg(), fake, func() time.Time { return now })
	if err := task.ApplyAdjustments(map[string]interface{}{
		"entry_threshold_pct":     float64(99.0),  // way above ceil
		"stop_loss_per_trade_pct": float64(0.0001), // below floor
		"max_daily_trades":        float64(99999),  // above ceil
		"volume_confirmation":     true,
	}); err != nil {
		t.Fatal(err)
	}
	if task.cfg.EntryThresholdPct != momEntryThreshCeil {
		t.Errorf("entry clamp: %v", task.cfg.EntryThresholdPct)
	}
	if task.cfg.StopLossPerTradePct != momStopLossFloor {
		t.Errorf("stop clamp: %v", task.cfg.StopLossPerTradePct)
	}
	if task.cfg.MaxDailyTrades != momMaxDailyCeil {
		t.Errorf("daily clamp: %v", task.cfg.MaxDailyTrades)
	}
	if !task.cfg.VolumeConfirmation {
		t.Error("volume_confirmation toggle ignored")
	}
}

func TestMomentum_CloseAllPositionsFlattensOpenPosition(t *testing.T) {
	fake := chain.NewFake("solana", "SOL")
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clock := now
	task := newMomentum(t, baseMomentumCfg(), fake, func() time.Time { return clock })
	task.SeedFreeCapital(1000)
	for i := 0; i < 50; i++ {
		task.IngestSample(now.Add(-time.Duration(50-i)*time.Minute), 100, 0)
	}
	stageQuote(fake, "USDC", "SOL", 105)
	task.RunTick(context.Background())
	if task.position == nil {
		t.Fatal("position should be open")
	}
	trades, err := task.CloseAllPositions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 1 {
		t.Fatalf("close: %d trades, want 1", len(trades))
	}
	if task.position != nil {
		t.Error("position not flattened")
	}
}

func TestMomentum_RegisteredInFactory(t *testing.T) {
	for _, name := range Registered() {
		if name == "momentum" {
			return
		}
	}
	t.Error("momentum not registered")
}

func TestMomentum_ConfigValidationRejectsBadInputs(t *testing.T) {
	cases := []struct {
		name string
		cfg  MomentumConfig
	}{
		{"missing chain", MomentumConfig{TokenPair: "USDC/SOL"}},
		{"bad chain", MomentumConfig{Chain: "eth", TokenPair: "USDC/SOL"}},
		{"missing pair", MomentumConfig{Chain: "solana"}},
		{"lookback too low", MomentumConfig{Chain: "solana", TokenPair: "USDC/SOL", LookbackMinutes: 1}},
		{"entry threshold too high", MomentumConfig{Chain: "solana", TokenPair: "USDC/SOL", EntryThresholdPct: 5}},
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
