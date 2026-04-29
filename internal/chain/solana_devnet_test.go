package chain_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/chain"
)

// Spec acceptance criterion (chunk 4):
// "A devnet integration test successfully fetches the SOL balance of a
//  known devnet address and gets a Jupiter quote for SOL -> USDC."
//
// Gated behind INTEGRATION=1 because it hits live Helius + Jupiter.
// Configurable via env so the user picks their RPC and a probe address:
//
//   HELIUS_DEVNET_URL  — RPC endpoint (defaults to public devnet)
//   SOLANA_PROBE_ADDR  — pubkey to read balance from (any funded address)
//
// Quote target tokens are mainnet mints (Jupiter routes against mainnet
// liquidity even when tests run from devnet config) — that part exercises
// the Jupiter HTTP path; the balance call exercises the RPC path.
func TestSolanaDevnetSmoke(t *testing.T) {
	if os.Getenv("INTEGRATION") != "1" {
		t.Skip("INTEGRATION=1 not set — skipping live devnet smoke")
	}
	rpcURL := os.Getenv("HELIUS_DEVNET_URL")
	probe := os.Getenv("SOLANA_PROBE_ADDR")
	if probe == "" {
		// Solana foundation devnet faucet authority — always funded.
		probe = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	}

	factory, err := chain.Get("solana")
	if err != nil {
		t.Fatalf("chain.Get(solana): %v", err)
	}
	c, err := factory(chain.Config{Network: "devnet", RPCURL: rpcURL})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bal, err := c.GetBalance(ctx, probe)
	if err != nil {
		t.Fatalf("GetBalance(%s): %v", probe, err)
	}
	t.Logf("devnet balance for %s = %f SOL", probe, bal)

	const solMint = "So11111111111111111111111111111111111111112"
	const usdcMint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	q, err := c.GetQuote(ctx, solMint, usdcMint, 0.1)
	if err != nil {
		t.Fatalf("GetQuote SOL->USDC: %v", err)
	}
	if q.AmountOut <= 0 {
		t.Fatalf("AmountOut = %v, want > 0; full quote = %+v", q.AmountOut, q)
	}
	t.Logf("Jupiter quote: 0.1 SOL -> %f USDC (price impact %f%%)", q.AmountOut, q.PriceImpact*100)
}
