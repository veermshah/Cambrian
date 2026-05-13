// Command init-treasury creates the two root treasury wallets — one
// Solana (Ed25519), one Base (secp256k1) — encrypts their private keys
// with the master encryption key, and inserts them as `node_class=funded`
// agent rows named root_treasury_solana / root_treasury_base.
//
// Idempotent: a second run detects the existing rows and prints their
// addresses without generating new keys. The wallet_address UNIQUE
// constraint on the agents table is a hard backstop against double
// insert; the in-code check by name avoids ever generating a key we'd
// fail to insert.
//
// Usage:
//
//	NETWORK=devnet \
//	DATABASE_URL=postgres://... \
//	MASTER_ENCRYPTION_KEY=<64 hex chars> \
//	REDIS_URL=rediss://... API_KEY=... \
//	go run ./cmd/init-treasury
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/veermshah/cambrian/internal/chain"
	"github.com/veermshah/cambrian/internal/config"
	"github.com/veermshah/cambrian/internal/db"
)

const (
	solanaTreasuryName = "root_treasury_solana"
	baseTreasuryName   = "root_treasury_base"
)

func main() {
	if err := run(); err != nil {
		log.Printf("init-treasury: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	masterKey, err := hex.DecodeString(cfg.MasterEncryptionKey)
	if err != nil {
		return fmt.Errorf("decode MASTER_ENCRYPTION_KEY: %w", err)
	}
	if len(masterKey) != 32 {
		return fmt.Errorf("MASTER_ENCRYPTION_KEY decodes to %d bytes, want 32", len(masterKey))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DatabasePoolURL)
	if err != nil {
		return fmt.Errorf("db pool: %w", err)
	}
	defer pool.Close()
	q := db.NewQueries(pool)

	solAddr, solCreated, err := ensureTreasury(ctx, q, masterKey, solanaTreasuryName, "solana")
	if err != nil {
		return err
	}
	baseAddr, baseCreated, err := ensureTreasury(ctx, q, masterKey, baseTreasuryName, "base")
	if err != nil {
		return err
	}

	fmt.Printf("network=%s\n", cfg.Network)
	fmt.Printf("solana treasury: %s%s\n", solAddr, suffix(solCreated))
	fmt.Printf("base treasury:   %s%s\n", baseAddr, suffix(baseCreated))
	return nil
}

// ensureTreasury returns the address of the named treasury, creating it
// if absent. The boolean reports whether this run inserted the row.
func ensureTreasury(ctx context.Context, q *db.Queries, masterKey []byte, name, chainName string) (string, bool, error) {
	existing, err := q.GetAgentByName(ctx, name)
	if err == nil {
		return existing.WalletAddress, false, nil
	}
	if !errors.Is(err, db.ErrAgentNotFound) {
		return "", false, fmt.Errorf("check %s: %w", name, err)
	}

	wallet, err := newTreasuryWallet(chainName, masterKey)
	if err != nil {
		return "", false, err
	}

	agent := db.Agent{
		Name:               name,
		Chain:              chainName,
		WalletAddress:      wallet.Address,
		WalletKeyEncrypted: wallet.KeyEncrypted,
		// Treasury is not a real strategist — pick a benign task_type
		// from the CHECK constraint and a tiny model. cmd/swarm and the
		// orchestrator never schedule the root treasury for execution.
		TaskType:                  "cross_chain_yield",
		StrategistPrompt:          "root treasury — not a strategist",
		StrategistModel:           "claude-haiku-4-5-20251001",
		StrategistIntervalSeconds: 14400,
		NodeClass:                 "funded",
	}
	if _, err := q.InsertAgent(ctx, agent); err != nil {
		return "", false, fmt.Errorf("insert %s: %w", name, err)
	}
	return wallet.Address, true, nil
}

func newTreasuryWallet(chainName string, masterKey []byte) (*chain.Wallet, error) {
	switch chainName {
	case "solana":
		return chain.NewSolanaWallet(masterKey)
	case "base":
		return chain.NewBaseWallet(masterKey)
	default:
		return nil, fmt.Errorf("unknown chain %q", chainName)
	}
}

func suffix(created bool) string {
	if created {
		return " (created)"
	}
	return " (existing)"
}
