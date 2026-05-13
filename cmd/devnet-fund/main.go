// Command devnet-fund tops up the two root treasury wallets on test
// networks. Solana devnet has a faucet RPC method (requestAirdrop) so we
// just call it. Base Sepolia has no usable programmatic faucet without
// authentication; we print the address and a curl command for the public
// Base Sepolia faucet, and optionally POST to faucet.base.org if
// BASE_SEPOLIA_FAUCET_KEY is set.
//
// Usage:
//
//	NETWORK=devnet \
//	DATABASE_URL=postgres://... REDIS_URL=rediss://... \
//	MASTER_ENCRYPTION_KEY=<64 hex> API_KEY=... \
//	go run ./cmd/devnet-fund
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"

	"github.com/veermshah/cambrian/internal/config"
	"github.com/veermshah/cambrian/internal/db"
)

const (
	solanaTreasuryName = "root_treasury_solana"
	baseTreasuryName   = "root_treasury_base"
	solanaAirdropSOL   = 5
	lamportsPerSOL     = 1_000_000_000
	baseFaucetURL      = "https://faucet.base.org/api/sepolia"
)

func main() {
	if err := run(); err != nil {
		log.Printf("devnet-fund: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if cfg.Network != "devnet" {
		return fmt.Errorf("NETWORK=%q: devnet-fund only runs against devnet", cfg.Network)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DatabasePoolURL)
	if err != nil {
		return fmt.Errorf("db pool: %w", err)
	}
	defer pool.Close()
	q := db.NewQueries(pool)

	solAgent, err := q.GetAgentByName(ctx, solanaTreasuryName)
	if err != nil {
		return fmt.Errorf("solana treasury: %w (run cmd/init-treasury first)", err)
	}
	baseAgent, err := q.GetAgentByName(ctx, baseTreasuryName)
	if err != nil {
		return fmt.Errorf("base treasury: %w (run cmd/init-treasury first)", err)
	}

	if err := fundSolana(ctx, cfg.HeliusDevnetURL, solAgent.WalletAddress); err != nil {
		// Solana airdrops are rate-limited; surface but don't abort —
		// the Base half is still worth printing.
		log.Printf("solana airdrop: %v", err)
	}
	printSepoliaInstructions(ctx, baseAgent.WalletAddress)
	return nil
}

// fundSolana asks the devnet RPC to airdrop solanaAirdropSOL into addr
// and waits for confirmation by polling the signature.
func fundSolana(ctx context.Context, rpcURL, addr string) error {
	if rpcURL == "" {
		rpcURL = rpc.DevNet_RPC
	}
	client := rpc.New(rpcURL)
	pk, err := solana.PublicKeyFromBase58(addr)
	if err != nil {
		return fmt.Errorf("parse address: %w", err)
	}

	before, _ := client.GetBalance(ctx, pk, rpc.CommitmentConfirmed)
	beforeLamports := uint64(0)
	if before != nil {
		beforeLamports = before.Value
	}

	sig, err := client.RequestAirdrop(ctx, pk, solanaAirdropSOL*lamportsPerSOL, rpc.CommitmentConfirmed)
	if err != nil {
		return fmt.Errorf("requestAirdrop: %w", err)
	}
	fmt.Printf("solana: requested %d SOL airdrop, sig=%s\n", solanaAirdropSOL, sig.String())

	if err := waitForSignature(ctx, client, sig); err != nil {
		return fmt.Errorf("await airdrop: %w", err)
	}

	after, err := client.GetBalance(ctx, pk, rpc.CommitmentConfirmed)
	if err != nil {
		return fmt.Errorf("post-airdrop balance: %w", err)
	}
	fmt.Printf("solana: balance %.4f -> %.4f SOL\n",
		float64(beforeLamports)/float64(lamportsPerSOL),
		float64(after.Value)/float64(lamportsPerSOL),
	)
	return nil
}

func waitForSignature(ctx context.Context, c *rpc.Client, sig solana.Signature) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %s", sig.String())
		}
		statuses, err := c.GetSignatureStatuses(ctx, false, sig)
		if err == nil && len(statuses.Value) > 0 && statuses.Value[0] != nil {
			s := statuses.Value[0]
			if s.Err != nil {
				return fmt.Errorf("airdrop tx failed: %v", s.Err)
			}
			if s.ConfirmationStatus == rpc.ConfirmationStatusConfirmed ||
				s.ConfirmationStatus == rpc.ConfirmationStatusFinalized {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// printSepoliaInstructions prints the treasury address and a curl
// command the user can paste into a shell. If BASE_SEPOLIA_FAUCET_KEY
// is set, attempt the faucet.base.org POST directly — the API is
// rate-limited and unreliable, so a failure is logged but not fatal.
func printSepoliaInstructions(ctx context.Context, addr string) {
	fmt.Println()
	fmt.Printf("base sepolia treasury: %s\n", addr)
	fmt.Printf("manual faucet (Coinbase Wallet auth required):\n")
	fmt.Printf("  open https://portal.cdp.coinbase.com/products/faucet?network=base-sepolia&address=%s\n", addr)
	fmt.Printf("alternative (programmatic, rate-limited):\n")
	fmt.Printf("  curl -sS -X POST %s -H 'content-type: application/json' \\\n", baseFaucetURL)
	fmt.Printf("    -d '{\"address\":\"%s\",\"network\":\"sepolia\"}'\n", addr)

	key := os.Getenv("BASE_SEPOLIA_FAUCET_KEY")
	if key == "" {
		return
	}
	if err := attemptBaseFaucet(ctx, addr, key); err != nil {
		log.Printf("base sepolia faucet: %v", err)
		return
	}
	fmt.Println("base sepolia: faucet accepted request")
}

func attemptBaseFaucet(ctx context.Context, addr, key string) error {
	body, _ := json.Marshal(map[string]string{
		"address": addr,
		"network": "sepolia",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseFaucetURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("faucet returned %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

