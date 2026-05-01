package chain_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/chain"
)

// Spec acceptance criterion (chunk 5):
// "A Sepolia integration test successfully fetches the ETH balance of a
//  known address. The 1inch quote step is best-effort — 1inch does not
//  deploy on Sepolia (chain id 84532) and returns 'Chain id is not
//  supported'; the test surfaces that error rather than failing."
//
// Gated behind INTEGRATION=1. Required env:
//
//   BASE_SEPOLIA_RPC_URL  — Alchemy / Coinbase JSON-RPC endpoint
//   BASE_PROBE_ADDR       — address to read balance from (any non-zero
//                           account; the Base bridge contract works)
func TestBaseSepoliaSmoke(t *testing.T) {
	if os.Getenv("INTEGRATION") != "1" {
		t.Skip("INTEGRATION=1 not set — skipping live Sepolia smoke")
	}
	rpcURL := os.Getenv("BASE_SEPOLIA_RPC_URL")
	if rpcURL == "" {
		t.Skip("BASE_SEPOLIA_RPC_URL not set — skipping")
	}
	probe := os.Getenv("BASE_PROBE_ADDR")
	if probe == "" {
		// Base Sepolia bridge predeploy — held funds, exists on every chain copy.
		probe = "0x4200000000000000000000000000000000000010"
	}

	factory, err := chain.Get("base")
	if err != nil {
		t.Fatalf("chain.Get(base): %v", err)
	}
	c, err := factory(chain.Config{Network: "sepolia", RPCURL: rpcURL})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bal, err := c.GetBalance(ctx, probe)
	if err != nil {
		t.Fatalf("GetBalance(%s): %v", probe, err)
	}
	t.Logf("sepolia balance for %s = %f ETH", probe, bal)

	// Sepolia USDC + WETH (Circle test deployment + canonical predeploy).
	const usdcSepolia = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	const wethBase = "0x4200000000000000000000000000000000000006"

	q, err := c.GetQuote(ctx, wethBase, usdcSepolia, 0.01)
	if err != nil {
		// 1inch on Sepolia is documented as unsupported. The acceptance
		// criterion only requires the balance call to work; the quote
		// step is informational.
		if strings.Contains(err.Error(), "Chain id is not supported") ||
			strings.Contains(err.Error(), "status 4") {
			t.Logf("expected: 1inch does not support Sepolia: %v", err)
			return
		}
		t.Fatalf("GetQuote WETH->USDC: %v", err)
	}
	if q.AmountOut <= 0 {
		t.Fatalf("AmountOut = %v, want > 0", q.AmountOut)
	}
	t.Logf("1inch quote: 0.01 WETH -> %f USDC", q.AmountOut)
}
