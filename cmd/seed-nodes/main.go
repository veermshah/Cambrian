// Command seed-nodes creates the initial Cambrian swarm from a YAML
// config file. The file enumerates each agent's task type, chain, node
// class (funded / shadow), seed capital, and strategist model; the
// command generates a wallet, encrypts the private key with
// MASTER_ENCRYPTION_KEY, and inserts the row via LifecycleManager.Spawn
// so the swarm's lifecycle:spawn subscriber picks it up when it boots.
//
// Idempotent: nodes whose name already exists in the agents table are
// skipped (and printed as such). Re-running after a crash is safe.
//
// Usage:
//
//	NETWORK=devnet \
//	DATABASE_URL=postgres://... DATABASE_POOL_URL=postgres://... \
//	REDIS_URL=rediss://... API_KEY=... \
//	MASTER_ENCRYPTION_KEY=<64 hex> \
//	go run ./cmd/seed-nodes seed.yaml
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/chain"
	"github.com/veermshah/cambrian/internal/config"
	"github.com/veermshah/cambrian/internal/db"
	"github.com/veermshah/cambrian/internal/redis"
	"github.com/veermshah/cambrian/internal/runtime"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("seed-nodes: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: seed-nodes <seed.yaml>")
	}
	path := args[0]

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

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	specs, err := ParseSeedYAML(raw)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if len(specs) == 0 {
		return errors.New("seed file has no nodes")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DatabasePoolURL)
	if err != nil {
		return fmt.Errorf("db pool: %w", err)
	}
	defer pool.Close()
	queries := db.NewQueries(pool)

	rdb, err := redis.New(ctx, cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer rdb.Close()

	lifecycle := runtime.NewLifecycleManager(queries, rdb, nil)
	report, err := SeedAll(ctx, specs, &spawnDeps{
		store:     queries,
		masterKey: masterKey,
		spawn:     lifecycle.Spawn,
	})
	if err != nil {
		return err
	}

	fmt.Printf("network=%s spawned=%d skipped=%d\n", cfg.Network, report.Spawned, report.Skipped)
	for _, line := range report.Lines {
		fmt.Println(line)
	}
	return nil
}

// SeedSpec is one node line in the YAML.
type SeedSpec struct {
	Name              string  `yaml:"name"`
	TaskType          string  `yaml:"task_type"`
	Chain             string  `yaml:"chain"`
	NodeClass         string  `yaml:"node_class"`
	CapitalUSD        float64 `yaml:"capital_usd"`
	StrategistModel   string  `yaml:"strategist_model,omitempty"`
	StrategistPrompt  string  `yaml:"strategist_prompt,omitempty"`
	StrategistInterval int    `yaml:"strategist_interval_seconds,omitempty"`
}

// SeedReport summarizes a run. Lines is one human-readable string per
// spec, in the order they appeared in the file.
type SeedReport struct {
	Spawned int
	Skipped int
	Lines   []string
}

// spawnDeps is the narrow surface SeedAll talks to so tests can swap in
// fakes without spinning up Postgres / Redis.
type spawnDeps struct {
	store     seedStore
	masterKey []byte
	spawn     func(ctx context.Context, agentRow db.Agent, reason string) (string, error)
}

type seedStore interface {
	GetAgentByName(ctx context.Context, name string) (db.Agent, error)
}

// SeedAll runs the idempotent insert loop. Each spec → check by name →
// generate wallet → call Spawn. Exported so the test can drive it with
// fakes.
func SeedAll(ctx context.Context, specs []SeedSpec, deps *spawnDeps) (SeedReport, error) {
	var report SeedReport
	for _, spec := range specs {
		if err := spec.Validate(); err != nil {
			return report, fmt.Errorf("seed %q: %w", spec.Name, err)
		}
		existing, err := deps.store.GetAgentByName(ctx, spec.Name)
		if err == nil {
			report.Skipped++
			report.Lines = append(report.Lines, fmt.Sprintf("- %s: skip (existing wallet=%s)", spec.Name, existing.WalletAddress))
			continue
		}
		if !errors.Is(err, db.ErrAgentNotFound) {
			return report, fmt.Errorf("check %s: %w", spec.Name, err)
		}

		wallet, err := newWallet(spec.Chain, deps.masterKey)
		if err != nil {
			return report, fmt.Errorf("wallet %s: %w", spec.Name, err)
		}
		row := spec.ToAgentRow(wallet)
		id, err := deps.spawn(ctx, row, "seed-nodes")
		if err != nil {
			return report, fmt.Errorf("spawn %s: %w", spec.Name, err)
		}
		report.Spawned++
		report.Lines = append(report.Lines, fmt.Sprintf("- %s: spawn id=%s wallet=%s class=%s task=%s", spec.Name, id, wallet.Address, spec.NodeClass, spec.TaskType))
	}
	return report, nil
}

// Validate enforces the constraints the agents-table CHECK clauses
// would enforce on insert, but we want them caught with a friendlier
// error message before any wallet is generated.
func (s SeedSpec) Validate() error {
	if s.Name == "" {
		return errors.New("name required")
	}
	switch s.Chain {
	case "solana", "base":
	default:
		return fmt.Errorf("chain %q: must be solana or base", s.Chain)
	}
	switch s.TaskType {
	case "cross_chain_yield", "liquidity_provision", "liquidation_hunting", "momentum":
	default:
		return fmt.Errorf("task_type %q: must be one of cross_chain_yield, liquidity_provision, liquidation_hunting, momentum", s.TaskType)
	}
	switch s.NodeClass {
	case "funded", "shadow":
	default:
		return fmt.Errorf("node_class %q: must be funded or shadow", s.NodeClass)
	}
	if s.CapitalUSD < 0 {
		return fmt.Errorf("capital_usd %.2f: must be >= 0", s.CapitalUSD)
	}
	return nil
}

// ToAgentRow builds the db.Agent row from this spec + a freshly
// generated wallet. Default genome JSON values come from
// defaultStrategyConfig — wired in the same package so it stays in step
// with the task implementations.
func (s SeedSpec) ToAgentRow(wallet *chain.Wallet) db.Agent {
	model := s.StrategistModel
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	interval := s.StrategistInterval
	if interval <= 0 {
		interval = 14400
	}
	prompt := s.StrategistPrompt
	if prompt == "" {
		prompt = defaultPromptFor(s.TaskType)
	}
	return db.Agent{
		Name:                      s.Name,
		Chain:                     s.Chain,
		WalletAddress:             wallet.Address,
		WalletKeyEncrypted:        wallet.KeyEncrypted,
		TaskType:                  s.TaskType,
		StrategyConfig:            defaultStrategyConfigJSON(s.TaskType),
		StrategistPrompt:          prompt,
		StrategistModel:           model,
		StrategistIntervalSeconds: interval,
		NodeClass:                 s.NodeClass,
		CapitalAllocated:          s.CapitalUSD,
	}
}

func newWallet(chainName string, masterKey []byte) (*chain.Wallet, error) {
	switch chainName {
	case "solana":
		return chain.NewSolanaWallet(masterKey)
	case "base":
		return chain.NewBaseWallet(masterKey)
	}
	return nil, fmt.Errorf("unknown chain %q", chainName)
}

// defaultStrategyConfigJSON returns the canonical zero-config for each
// task type. The task config parsers all accept empty JSON and fall
// back to their own defaults (see momentum_config.go, liquidation_hunting_config.go,
// etc.), so {} is a safe seed for every task. Specifying genome
// overrides per-spec in the YAML is a future extension.
func defaultStrategyConfigJSON(_ string) json.RawMessage {
	return json.RawMessage(`{}`)
}

// defaultPromptFor returns a starting strategist prompt for the task
// type. The strategist's mutation loop edits this over time; this is
// just the seed.
func defaultPromptFor(taskType string) string {
	switch taskType {
	case "cross_chain_yield":
		return "you scan cross-chain yield. raise rates beat fees-plus-slippage; rebalance otherwise. be conservative on devnet."
	case "liquidity_provision":
		return "you manage an LP position. widen ranges in volatile regimes, tighten when IL risk is low."
	case "liquidation_hunting":
		return "you hunt under-collateralized positions. take only liquidations whose bonus clears gas + slippage with margin."
	case "momentum":
		return "you trade momentum on a single pair. enter on confirmed breakouts, exit on threshold reversal or stop-loss."
	}
	return "you are a strategist."
}

// genomeFromRow is a tiny convenience used by both the YAML parser and
// the test harness; kept here so package agent's heavier helpers don't
// leak in.
//
//nolint:unused // referenced by the test harness in seed_test.go
func genomeFromRow(row db.Agent) agent.AgentGenome {
	return agent.AgentGenome{
		Name:                      row.Name,
		TaskType:                  row.TaskType,
		Chain:                     row.Chain,
		StrategistPrompt:          row.StrategistPrompt,
		StrategistModel:           row.StrategistModel,
		StrategistIntervalSeconds: row.StrategistIntervalSeconds,
		CapitalAllocation:         row.CapitalAllocated,
	}
}
