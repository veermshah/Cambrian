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

// Trade represents a row in the trades table. Mirrors the schema in
// migrations/0001_init.up.sql; chunk 14's NodeRunner produces these
// from the Task layer's chain-agnostic trades and hands them straight
// to LogTrade.
type Trade struct {
	AgentID          string
	EpochID          string // empty string ⇒ NULL
	Chain            string
	TradeType        string
	TokenPair        string
	DEX              string
	AmountIn         float64
	AmountOut        float64
	FeePaid          float64
	PnL              float64 // 0 ⇒ NULL (we never insert literal 0 PnL because the schema allows NULL)
	TxSignature      string  // empty ⇒ NULL
	IsPaperTrade     bool
	BanditPolicyUsed string
	Metadata         json.RawMessage
}

// StrategistDecision represents a row in strategist_decisions. Inserted
// by the strategist after every LLM call — including malformed
// responses, where output_raw still carries the raw model text so the
// operator can diagnose drift offline.
type StrategistDecision struct {
	AgentID                    string
	InputSummary               json.RawMessage
	OutputRaw                  string
	ConfigChanges              json.RawMessage // optional
	Reasoning                  string
	IntelBroadcasts            json.RawMessage // optional
	OffspringProposalSubmitted bool
	NewLearnedRule             json.RawMessage // optional
	ModelUsed                  string
	InputTokens                int
	OutputTokens               int
	CostUSD                    float64
}

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

func nullableJSON(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

func nullableText(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullableUUID(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func (q *Queries) GetAgent(ctx context.Context, id string) (Agent, error) {
	return Agent{}, errNotImplemented
}

// ListActiveAgents returns every agent whose status='active'. Used by
// SwarmRuntime on boot to materialize NodeRunners.
func (q *Queries) ListActiveAgents(ctx context.Context) ([]Agent, error) {
	const stmt = `
        SELECT id, name, chain, wallet_address, wallet_key_encrypted,
               task_type, strategy_config, strategist_prompt,
               strategist_model, strategist_interval_seconds,
               bandit_policies, learned_rules,
               sleep_schedule, reproduction_policy, cost_policy, communication_policy,
               node_class, generation, lineage_depth, capital_allocated
        FROM agents
        WHERE status = 'active'
        ORDER BY created_at ASC
    `
	rows, err := q.pool.Query(ctx, stmt)
	if err != nil {
		return nil, fmt.Errorf("db: list active agents: %w", err)
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(
			&a.ID, &a.Name, &a.Chain, &a.WalletAddress, &a.WalletKeyEncrypted,
			&a.TaskType, &a.StrategyConfig, &a.StrategistPrompt,
			&a.StrategistModel, &a.StrategistIntervalSeconds,
			&a.BanditPolicies, &a.LearnedRules,
			&a.SleepSchedule, &a.ReproductionPolicy, &a.CostPolicy, &a.CommunicationPolicy,
			&a.NodeClass, &a.Generation, &a.LineageDepth, &a.CapitalAllocated,
		); err != nil {
			return nil, fmt.Errorf("db: scan active agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LogTrade inserts a row into the trades table. NodeRunner's monitor
// loop calls this for every Task.RunTick output.
func (q *Queries) LogTrade(ctx context.Context, t Trade) error {
	if t.AgentID == "" {
		return errors.New("db: LogTrade requires agent_id")
	}
	const stmt = `
        INSERT INTO trades (
            agent_id, epoch_id, chain, trade_type, token_pair, dex,
            amount_in, amount_out, fee_paid, pnl, tx_signature,
            is_paper_trade, bandit_policy_used, metadata
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
    `
	epochID := nullableUUID(t.EpochID)
	txSig := nullableText(t.TxSignature)
	bandit := nullableText(t.BanditPolicyUsed)
	meta := defaultJSON(t.Metadata, "null")
	_, err := q.pool.Exec(ctx, stmt,
		t.AgentID, epochID, t.Chain, t.TradeType, t.TokenPair, t.DEX,
		t.AmountIn, t.AmountOut, t.FeePaid, t.PnL, txSig,
		t.IsPaperTrade, bandit, meta,
	)
	if err != nil {
		return fmt.Errorf("db: log trade: %w", err)
	}
	return nil
}

// LogStrategistDecision inserts a row into strategist_decisions. The
// strategist writes one every LLM call — including malformed responses
// where output_raw carries the raw model text and config_changes is null.
func (q *Queries) LogStrategistDecision(ctx context.Context, d StrategistDecision) error {
	if d.AgentID == "" {
		return errors.New("db: LogStrategistDecision requires agent_id")
	}
	if d.ModelUsed == "" {
		return errors.New("db: LogStrategistDecision requires model_used")
	}
	const stmt = `
        INSERT INTO strategist_decisions (
            agent_id, input_summary, output_raw, config_changes, reasoning,
            intel_broadcasts, offspring_proposal_submitted, new_learned_rule,
            model_used, input_tokens, output_tokens, cost_usd
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
    `
	input := defaultJSON(d.InputSummary, "{}")
	cfg := nullableJSON(d.ConfigChanges)
	intel := nullableJSON(d.IntelBroadcasts)
	rule := nullableJSON(d.NewLearnedRule)
	reasoning := nullableText(d.Reasoning)
	_, err := q.pool.Exec(ctx, stmt,
		d.AgentID, input, d.OutputRaw, cfg, reasoning,
		intel, d.OffspringProposalSubmitted, rule,
		d.ModelUsed, d.InputTokens, d.OutputTokens, d.CostUSD,
	)
	if err != nil {
		return fmt.Errorf("db: log strategist decision: %w", err)
	}
	return nil
}

// UpdateHeartbeat sets last_heartbeat_at = now() for the given agent.
// Called by the NodeRunner heartbeat loop every 30s.
func (q *Queries) UpdateHeartbeat(ctx context.Context, agentID string) error {
	if agentID == "" {
		return errors.New("db: UpdateHeartbeat requires agent_id")
	}
	const stmt = `UPDATE agents SET last_heartbeat_at = now() WHERE id = $1`
	_, err := q.pool.Exec(ctx, stmt, agentID)
	if err != nil {
		return fmt.Errorf("db: update heartbeat: %w", err)
	}
	return nil
}

// SetAgentStatus updates the agent's lifecycle status. Pause / Kill /
// Resume go through this; Promote / Demote use SetAgentNodeClass.
// status must be one of: active, paused, dead.
func (q *Queries) SetAgentStatus(ctx context.Context, agentID, status, killReason string) error {
	if agentID == "" {
		return errors.New("db: SetAgentStatus requires agent_id")
	}
	switch status {
	case "active", "paused", "dead":
	default:
		return fmt.Errorf("db: SetAgentStatus invalid status %q", status)
	}
	const stmt = `
        UPDATE agents
        SET status = $2,
            kill_reason = COALESCE($3, kill_reason),
            killed_at = CASE WHEN $2 = 'dead' THEN now() ELSE killed_at END
        WHERE id = $1
    `
	_, err := q.pool.Exec(ctx, stmt, agentID, status, nullableText(killReason))
	if err != nil {
		return fmt.Errorf("db: set agent status: %w", err)
	}
	return nil
}

// SetAgentNodeClass updates node_class. Promote / Demote move agents
// between funded / shadow; the constraint at the schema level rejects
// invalid values.
func (q *Queries) SetAgentNodeClass(ctx context.Context, agentID, class string) error {
	if agentID == "" {
		return errors.New("db: SetAgentNodeClass requires agent_id")
	}
	switch class {
	case "funded", "shadow", "paused", "dead":
	default:
		return fmt.Errorf("db: SetAgentNodeClass invalid class %q", class)
	}
	const stmt = `UPDATE agents SET node_class = $2 WHERE id = $1`
	_, err := q.pool.Exec(ctx, stmt, agentID, class)
	if err != nil {
		return fmt.Errorf("db: set node class: %w", err)
	}
	return nil
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
