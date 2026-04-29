package chain_test

import (
	"context"
	"testing"

	"github.com/veermshah/cambrian/internal/chain"
)

func newFake(t *testing.T) *chain.FakeChainClient {
	t.Helper()
	return chain.NewFake("solana", "SOL")
}

func TestFakeChainClient_BalancesAndTokens(t *testing.T) {
	ctx := context.Background()
	f := newFake(t).
		WithBalance("alice", 12.5).
		WithTokenBalance("alice", "USDC", 1000)

	got, err := f.GetBalance(ctx, "alice")
	if err != nil || got != 12.5 {
		t.Fatalf("GetBalance(alice) = %v, %v; want 12.5, nil", got, err)
	}
	if _, err := f.GetBalance(ctx, "bob"); err == nil {
		t.Fatal("GetBalance(bob) want error for unstaged address")
	}

	tb, err := f.GetTokenBalance(ctx, "alice", "USDC")
	if err != nil || tb != 1000 {
		t.Fatalf("GetTokenBalance = %v, %v; want 1000, nil", tb, err)
	}
	if _, err := f.GetTokenBalance(ctx, "alice", "SOL"); err == nil {
		t.Fatal("GetTokenBalance(alice, SOL) want error — token not staged")
	}
	if _, err := f.GetTokenBalance(ctx, "bob", "USDC"); err == nil {
		t.Fatal("GetTokenBalance(bob, USDC) want error — address not staged")
	}
}

func TestFakeChainClient_QuoteScalesWithAmount(t *testing.T) {
	ctx := context.Background()
	f := newFake(t).WithQuote("SOL", "USDC", &chain.Quote{
		Chain:     "solana",
		DEX:       "jupiter",
		AmountIn:  1,
		AmountOut: 150,
		Price:     150,
	})

	q, err := f.GetQuote(ctx, "SOL", "USDC", 4)
	if err != nil {
		t.Fatalf("GetQuote: %v", err)
	}
	if q.AmountIn != 4 || q.AmountOut != 600 {
		t.Fatalf("GetQuote scaled wrong: in=%v out=%v want in=4 out=600", q.AmountIn, q.AmountOut)
	}
	if q.GeneratedAt.IsZero() {
		t.Fatal("GeneratedAt should be populated when staged quote leaves it zero")
	}
	if _, err := f.GetQuote(ctx, "SOL", "JTO", 1); err == nil {
		t.Fatal("GetQuote unstaged pair want error")
	}
}

func TestFakeChainClient_ExecuteSwapAndSendTransaction(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	wallet := &chain.Wallet{Chain: "solana", Address: "alice"}

	q := &chain.Quote{Chain: "solana", DEX: "jupiter", TokenIn: "SOL", TokenOut: "USDC", AmountIn: 1, AmountOut: 150}
	res, err := f.ExecuteSwap(ctx, q, wallet)
	if err != nil || !res.Success || res.Signature == "" {
		t.Fatalf("ExecuteSwap = %+v, %v", res, err)
	}
	if len(f.Swaps) != 1 {
		t.Fatalf("Swaps len = %d, want 1", len(f.Swaps))
	}

	if _, err := f.ExecuteSwap(ctx, nil, wallet); err == nil {
		t.Fatal("ExecuteSwap nil quote want error")
	}
	if _, err := f.ExecuteSwap(ctx, q, nil); err == nil {
		t.Fatal("ExecuteSwap nil wallet want error")
	}

	tx := &chain.Transaction{Chain: "solana", Raw: []byte{0x01}}
	tr, err := f.SendTransaction(ctx, tx, wallet)
	if err != nil || !tr.Success {
		t.Fatalf("SendTransaction = %+v, %v", tr, err)
	}
	if len(f.Sent) != 1 {
		t.Fatalf("Sent len = %d, want 1", len(f.Sent))
	}
}

func TestFakeChainClient_SimulateTransaction(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)

	tx := &chain.Transaction{Chain: "solana", Raw: []byte{0x01}}
	sim, err := f.SimulateTransaction(ctx, tx)
	if err != nil || !sim.WouldSucceed {
		t.Fatalf("default sim = %+v, %v; want WouldSucceed=true", sim, err)
	}

	f.WithSimResult(&chain.SimResult{WouldSucceed: false, ErrorMsg: "boom"})
	sim, err = f.SimulateTransaction(ctx, tx)
	if err != nil || sim.WouldSucceed || sim.ErrorMsg != "boom" {
		t.Fatalf("staged sim = %+v, %v; want WouldSucceed=false ErrorMsg=boom", sim, err)
	}

	if _, err := f.SimulateTransaction(ctx, nil); err == nil {
		t.Fatal("SimulateTransaction nil tx want error")
	}
}

func TestFakeChainClient_LendingAndYield(t *testing.T) {
	ctx := context.Background()
	f := newFake(t).
		WithLendingPosition("kamino", chain.LendingPosition{
			Chain: "solana", Protocol: "kamino", Owner: "carol",
			CollateralAmt: 1000, LiquidationBonus: 500, // 5%
		}).
		WithYieldRate("marinade", chain.YieldRate{Chain: "solana", Protocol: "marinade", Asset: "SOL", APY: 7.2})

	pos, err := f.GetLendingPositions(ctx, "kamino")
	if err != nil || len(pos) != 1 || pos[0].Owner != "carol" {
		t.Fatalf("GetLendingPositions = %+v, %v", pos, err)
	}
	empty, err := f.GetLendingPositions(ctx, "aave")
	if err != nil || len(empty) != 0 {
		t.Fatalf("GetLendingPositions(aave) = %+v, %v; want empty", empty, err)
	}

	rates, err := f.GetYieldRates(ctx, []string{"marinade", "missing"})
	if err != nil || len(rates) != 1 || rates[0].APY != 7.2 {
		t.Fatalf("GetYieldRates = %+v, %v", rates, err)
	}

	wallet := &chain.Wallet{Chain: "solana", Address: "liquidator"}
	res, err := f.ExecuteLiquidation(ctx, &pos[0], wallet)
	if err != nil || !res.Success {
		t.Fatalf("ExecuteLiquidation = %+v, %v", res, err)
	}
	if res.FeePaidUSD != 50 { // 1000 * 500/10000
		t.Fatalf("FeePaidUSD = %v, want 50", res.FeePaidUSD)
	}
	if len(f.Liquidations) != 1 {
		t.Fatalf("Liquidations len = %d, want 1", len(f.Liquidations))
	}

	if _, err := f.ExecuteLiquidation(ctx, nil, wallet); err == nil {
		t.Fatal("ExecuteLiquidation nil pos want error")
	}
	if _, err := f.ExecuteLiquidation(ctx, &pos[0], nil); err == nil {
		t.Fatal("ExecuteLiquidation nil wallet want error")
	}
}

func TestFakeChainClient_DryRunOnlyRefusesRealExecution(t *testing.T) {
	f := newFake(t).WithDryRunOnly(true)
	wallet := &chain.Wallet{Chain: "solana", Address: "alice"}
	q := &chain.Quote{Chain: "solana", DEX: "jupiter", TokenIn: "SOL", TokenOut: "USDC", AmountIn: 1, AmountOut: 150}
	tx := &chain.Transaction{Chain: "solana", Raw: []byte{0x01}}
	pos := &chain.LendingPosition{Chain: "solana", Protocol: "kamino", CollateralAmt: 100, LiquidationBonus: 500}

	if _, err := f.ExecuteSwap(context.Background(), q, wallet); err == nil {
		t.Fatal("ExecuteSwap real ctx in dry-only mode want error")
	}
	if _, err := f.SendTransaction(context.Background(), tx, wallet); err == nil {
		t.Fatal("SendTransaction real ctx in dry-only mode want error")
	}
	if _, err := f.ExecuteLiquidation(context.Background(), pos, wallet); err == nil {
		t.Fatal("ExecuteLiquidation real ctx in dry-only mode want error")
	}

	dctx := chain.WithDryRun(context.Background())
	if !chain.IsDryRun(dctx) {
		t.Fatal("WithDryRun ctx not flagged by IsDryRun")
	}
	if _, err := f.ExecuteSwap(dctx, q, wallet); err != nil {
		t.Fatalf("ExecuteSwap dry-run want ok, got %v", err)
	}
	if _, err := f.SendTransaction(dctx, tx, wallet); err != nil {
		t.Fatalf("SendTransaction dry-run want ok, got %v", err)
	}
	if _, err := f.ExecuteLiquidation(dctx, pos, wallet); err != nil {
		t.Fatalf("ExecuteLiquidation dry-run want ok, got %v", err)
	}
}

func TestFakeChainClient_Identity(t *testing.T) {
	f := chain.NewFake("base", "ETH")
	if f.ChainName() != "base" || f.NativeToken() != "ETH" {
		t.Fatalf("identity = (%s, %s), want (base, ETH)", f.ChainName(), f.NativeToken())
	}
}
