package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Queries is the read/write surface the rest of the swarm uses for Postgres.
// Method bodies are filled in chunk-by-chunk as the orchestrator and agent
// runtime come online (chunks 9, 14, 15, 21).
type Queries struct {
	pool *pgxpool.Pool
}

// NewQueries constructs a Queries bound to the given pool.
func NewQueries(pool *pgxpool.Pool) *Queries {
	return &Queries{pool: pool}
}

// Pool exposes the underlying pgxpool for transactions and ad-hoc queries.
func (q *Queries) Pool() *pgxpool.Pool { return q.pool }

var errNotImplemented = errors.New("db: not implemented")

// ErrAgentNotFound is returned by GetAgentByName when no row matches.
var ErrAgentNotFound = errors.New("db: agent not found")

// Agent mirrors the agents table closely enough for inserts. JSON columns
// are passed as raw json.RawMessage so the caller (e.g. cmd/init-treasury)
// owns serialization — keeps internal/db from importing the genome package.
type Agent struct {
	ID                        string
	Name                      string
	Chain                     string
	WalletAddress             string
	WalletKeyEncrypted        []byte
	TaskType                  string
	StrategyConfig            json.RawMessage
	StrategistPrompt          string
	StrategistModel           string
	StrategistIntervalSeconds int
	BanditPolicies            json.RawMessage
	LearnedRules              json.RawMessage
	SleepSchedule             json.RawMessage
	ReproductionPolicy        json.RawMessage
	CostPolicy                json.RawMessage
	CommunicationPolicy       json.RawMessage
	NodeClass                 string
	Generation                int
	LineageDepth              int
	CapitalAllocated          float64
}

// Trade represents a row in the trades table.
type Trade struct{}

// StrategistDecision represents a row in strategist_decisions.
type StrategistDecision struct{}

// Epoch represents a row in epochs.
type Epoch struct{}

// OffspringProposal represents a row in offspring_proposals.
type OffspringProposal struct{}

// Postmortem represents a row in postmortems.
type Postmortem struct{}

// LedgerRow represents a row in agent_ledgers.
type LedgerRow struct{}

// InsertAgent inserts a row into the agents table and returns the
// generated UUID. JSON columns default to "{}" / "[]" when the caller
// leaves them nil — saves init-treasury from having to marshal six empty
// objects.
func (q *Queries) InsertAgent(ctx context.Context, a Agent) (string, error) {
	if a.Name == "" {
		return "", errors.New("db: agent name required")
	}
	if a.WalletAddress == "" {
		return "", errors.New("db: agent wallet_address required")
	}
	if len(a.WalletKeyEncrypted) == 0 {
		return "", errors.New("db: agent wallet_key_encrypted required")
	}
	if a.TaskType == "" {
		return "", errors.New("db: agent task_type required")
	}

	chain := a.Chain
	if chain == "" {
		chain = "solana"
	}
	nodeClass := a.NodeClass
	if nodeClass == "" {
		nodeClass = "funded"
	}
	model := a.StrategistModel
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	interval := a.StrategistIntervalSeconds
	if interval <= 0 {
		interval = 14400
	}

	strategyCfg := defaultJSON(a.StrategyConfig, "{}")
	bandits := defaultJSON(a.BanditPolicies, `["default"]`)
	learned := defaultJSON(a.LearnedRules, "[]")
	sleep := defaultJSON(a.SleepSchedule, "{}")
	repro := defaultJSON(a.ReproductionPolicy, "{}")
	cost := defaultJSON(a.CostPolicy, "{}")
	comms := defaultJSON(a.CommunicationPolicy, "{}")

	const stmt = `
        INSERT INTO agents (
            name, chain, wallet_address, wallet_key_encrypted,
            task_type, strategy_config, strategist_prompt,
            strategist_model, strategist_interval_seconds,
            bandit_policies, learned_rules,
            sleep_schedule, reproduction_policy, cost_policy, communication_policy,
            node_class, generation, lineage_depth, capital_allocated
        ) VALUES (
            $1, $2, $3, $4,
            $5, $6, $7,
            $8, $9,
            $10, $11,
            $12, $13, $14, $15,
            $16, $17, $18, $19
        )
        RETURNING id
    `

	var id string
	err := q.pool.QueryRow(ctx, stmt,
		a.Name, chain, a.WalletAddress, a.WalletKeyEncrypted,
		a.TaskType, strategyCfg, a.StrategistPrompt,
		model, interval,
		bandits, learned,
		sleep, repro, cost, comms,
		nodeClass, a.Generation, a.LineageDepth, a.CapitalAllocated,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("db: insert agent %q: %w", a.Name, err)
	}
	return id, nil
}

// GetAgentByName returns the first agent row with the given name. Used by
// init-treasury to detect that the treasury has already been set up.
// Returns ErrAgentNotFound if no row matches.
func (q *Queries) GetAgentByName(ctx context.Context, name string) (Agent, error) {
	const stmt = `
        SELECT id, name, chain, wallet_address, wallet_key_encrypted,
               task_type, node_class, generation, lineage_depth
        FROM agents
        WHERE name = $1
        LIMIT 1
    `
	var a Agent
	err := q.pool.QueryRow(ctx, stmt, name).Scan(
		&a.ID, &a.Name, &a.Chain, &a.WalletAddress, &a.WalletKeyEncrypted,
		&a.TaskType, &a.NodeClass, &a.Generation, &a.LineageDepth,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrAgentNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("db: get agent %q: %w", name, err)
	}
	return a, nil
}

// TreasuryInitialized returns true if both root_treasury rows exist.
// Used by cmd/swarm to bail out early on a fresh deployment that hasn't
// run init-treasury yet.
func (q *Queries) TreasuryInitialized(ctx context.Context) (bool, error) {
	const stmt = `
        SELECT COUNT(*) FROM agents
        WHERE name IN ('root_treasury_solana', 'root_treasury_base')
    `
	var n int
	if err := q.pool.QueryRow(ctx, stmt).Scan(&n); err != nil {
		return false, fmt.Errorf("db: treasury check: %w", err)
	}
	return n >= 2, nil
}

func defaultJSON(raw json.RawMessage, fallback string) []byte {
	if len(raw) == 0 {
		return []byte(fallback)
	}
	return raw
}

func (q *Queries) GetAgent(ctx context.Context, id string) (Agent, error) {
	return Agent{}, errNotImplemented
}

func (q *Queries) ListActiveAgents(ctx context.Context) ([]Agent, error) {
	return nil, errNotImplemented
}

func (q *Queries) LogTrade(ctx context.Context, t Trade) error {
	return errNotImplemented
}

func (q *Queries) LogStrategistDecision(ctx context.Context, d StrategistDecision) error {
	return errNotImplemented
}

func (q *Queries) InsertEpoch(ctx context.Context, e Epoch) error {
	return errNotImplemented
}

func (q *Queries) InsertOffspringProposal(ctx context.Context, p OffspringProposal) error {
	return errNotImplemented
}

func (q *Queries) InsertPostmortem(ctx context.Context, p Postmortem) error {
	return errNotImplemented
}

func (q *Queries) InsertLedgerRow(ctx context.Context, r LedgerRow) error {
	return errNotImplemented
}
