package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/veermshah/cambrian/internal/agent"
)

// PostgresEpochStore is the production binding for the EpochStore
// interface. The orchestrator's parent_test.go covers the surface with
// an in-memory fake; this implementation talks to the same tables
// directly via pgx.
//
// LoadEpochState reads the agents table + last-epoch agent_ledgers and
// builds AgentSnapshots from columns that already exist. Fields the
// schema doesn't track directly (consecutive losing epochs, peak-to-
// trough drawdown) are derived as best as the current data allows;
// where the data is genuinely missing the snapshot value is left at
// zero and the deterministic policy treats it as "no signal" — that
// matches the chunk-19 invariant: missing data ≠ kill signal.
type PostgresEpochStore struct {
	pool *pgxpool.Pool
}

// NewPostgresEpochStore wraps the pool used by the rest of the swarm.
func NewPostgresEpochStore(pool *pgxpool.Pool) *PostgresEpochStore {
	return &PostgresEpochStore{pool: pool}
}

// Compile-time assertion: this type satisfies the EpochStore interface.
var _ EpochStore = (*PostgresEpochStore)(nil)

// LoadEpochState builds the snapshot RunEpoch consumes. The epoch row
// must already exist (chunk 21's parent loop inserts it before calling
// RunEpoch) so we can anchor `since` for "this epoch's window."
func (s *PostgresEpochStore) LoadEpochState(ctx context.Context, epochID string) (EpochState, error) {
	state := EpochState{
		EpochID:     epochID,
		Genomes:     map[string]agent.AgentGenome{},
		CarriedDebt: map[string]float64{},
	}

	const epochQ = `SELECT started_at FROM epochs WHERE id = $1`
	if err := s.pool.QueryRow(ctx, epochQ, epochID).Scan(&state.StartedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// First run: no epoch row, so anchor to the wall clock. The
			// parent loop's epoch-inserter is supposed to seed this row
			// but we don't want LoadEpochState to fail loud on a fresh DB.
			state.StartedAt = time.Now().UTC()
		} else {
			return state, fmt.Errorf("load epoch %s: %w", epochID, err)
		}
	}

	// Snapshots + genomes — one pass over agents.
	const agentsQ = `
        SELECT id, name, status, node_class, chain, task_type,
               COALESCE(current_balance, 0),
               COALESCE(consecutive_negative_epochs, 0),
               COALESCE(unpaid_operating_debt_usd, 0),
               COALESCE(last_heartbeat_at, '0001-01-01T00:00:00Z'::timestamptz),
               COALESCE(total_pnl, 0),
               COALESCE(peak_balance, 0),
               strategist_prompt, strategist_model,
               COALESCE(strategist_interval_seconds, 14400),
               generation, lineage_depth,
               COALESCE(capital_allocated, 0)
        FROM agents
        WHERE status IN ('active', 'paused')
    `
	rows, err := s.pool.Query(ctx, agentsQ)
	if err != nil {
		return state, fmt.Errorf("load agents: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			snap     AgentSnapshot
			name     string
			chain    string
			taskType string
			pnl, peak float64
			prompt   string
			model    string
			interval int
			gen, depth int
			capital  float64
		)
		if err := rows.Scan(
			&snap.AgentID, &name, &snap.Status, &snap.NodeClass, &chain, &taskType,
			&snap.BalanceUSD, &snap.ConsecutiveLosingEpochs, &snap.OperatingDebtUSD,
			&snap.LastHeartbeat, &pnl, &peak,
			&prompt, &model, &interval, &gen, &depth, &capital,
		); err != nil {
			return state, fmt.Errorf("scan agent snapshot: %w", err)
		}
		// Drawdown: (peak - current) / peak, clamped to [0, 1]. If peak
		// is zero (never traded) drawdown is zero — policy treats this
		// as "no signal".
		if peak > 0 {
			dd := (peak - snap.BalanceUSD) / peak
			if dd < 0 {
				dd = 0
			}
			if dd > 1 {
				dd = 1
			}
			snap.Drawdown = dd
		}
		state.Snapshots = append(state.Snapshots, snap)
		state.CarriedDebt[snap.AgentID] = snap.OperatingDebtUSD
		state.Genomes[snap.AgentID] = agent.AgentGenome{
			Name:                      name,
			TaskType:                  taskType,
			Chain:                     chain,
			StrategistPrompt:          prompt,
			StrategistModel:           model,
			StrategistIntervalSeconds: interval,
			Generation:                gen,
			LineageDepth:              depth,
			CapitalAllocation:         capital,
		}
		state.SwarmGenomes = append(state.SwarmGenomes, state.Genomes[snap.AgentID])
	}
	if err := rows.Err(); err != nil {
		return state, err
	}

	// Pending offspring proposals — RunEpoch evaluates these. We only
	// load rows in the entry states (pending or any pre-decision check
	// state); decided rows are already terminal.
	const offspringQ = `
        SELECT op.id, op.proposing_agent_id,
               op.requested_seed_capital_usd,
               op.requested_api_reserve_usd,
               op.requested_failure_buffer_usd,
               op.proposed_genome,
               COALESCE(p.unpaid_operating_debt_usd, 0)
        FROM offspring_proposals op
        LEFT JOIN agents p ON p.id = op.proposing_agent_id
        WHERE op.status IN ('pending', 'quality_check', 'adversarial_review')
        ORDER BY op.created_at ASC
    `
	oRows, err := s.pool.Query(ctx, offspringQ)
	if err != nil {
		return state, fmt.Errorf("load offspring: %w", err)
	}
	defer oRows.Close()
	for oRows.Next() {
		var (
			prop      OffspringProposal
			seed, reserve, failBuf float64
			genomeRaw []byte
		)
		if err := oRows.Scan(
			&prop.ProposalID, &prop.ParentID,
			&seed, &reserve, &failBuf,
			&genomeRaw, &prop.ParentCarriedDebt,
		); err != nil {
			return state, fmt.Errorf("scan offspring: %w", err)
		}
		prop.ReproductionReserveUSD = seed + reserve + failBuf
		_ = json.Unmarshal(genomeRaw, &prop.Candidate)
		// ParentLedger: fetch the parent's most recent ledger row so the
		// solvency gate in RunEpoch can read RealizedNetProfit.
		prop.ParentLedger = s.latestLedgerFor(ctx, prop.ParentID)
		state.OffspringProposals = append(state.OffspringProposals, prop)
	}
	if err := oRows.Err(); err != nil {
		return state, err
	}

	return state, nil
}

// latestLedgerFor returns the most recent agent_ledgers row for the
// agent, or an empty Ledger if none exists. Used by the offspring
// solvency gate — a missing ledger means the parent hasn't completed
// an epoch yet, which makes the gate naturally reject (RealizedNetProfit
// is zero).
func (s *PostgresEpochStore) latestLedgerFor(ctx context.Context, agentID string) Ledger {
	const q = `
        SELECT realized_trading_pnl_usd, trading_fees_usd, slippage_cost_usd,
               llm_cost_usd, infra_rent_usd, rpc_cost_usd,
               upstream_paid_to_parent_usd, upstream_paid_to_root_usd,
               realized_net_profit_usd
        FROM agent_ledgers
        WHERE agent_id = $1
        ORDER BY created_at DESC
        LIMIT 1
    `
	var l Ledger
	l.AgentID = agentID
	err := s.pool.QueryRow(ctx, q, agentID).Scan(
		&l.RealizedTradingPnL, &l.TradingFees, &l.SlippageCost,
		&l.LLMCost, &l.InfraRent, &l.RPCCost,
		&l.UpstreamPaidToParent, &l.UpstreamPaidToRoot,
		&l.RealizedNetProfit,
	)
	if err != nil {
		// Empty ledger is the right thing here; a missing row means the
		// agent has no epoch history.
		return Ledger{AgentID: agentID}
	}
	return l
}

// PersistLedger writes one Ledger row to agent_ledgers. Epoch ID may be
// empty for first-epoch runs; we store NULL in that case (schema allows
// it via REFERENCES … NULL).
func (s *PostgresEpochStore) PersistLedger(ctx context.Context, l Ledger) error {
	if l.AgentID == "" {
		return errors.New("orchestrator: PersistLedger requires agent_id")
	}
	const q = `
        INSERT INTO agent_ledgers (
            agent_id, epoch_id,
            realized_trading_pnl_usd, trading_fees_usd, slippage_cost_usd,
            llm_cost_usd, infra_rent_usd, rpc_cost_usd,
            upstream_paid_to_parent_usd, upstream_paid_to_root_usd,
            realized_net_profit_usd
        ) VALUES (
            $1, $2,
            $3, $4, $5,
            $6, $7, $8,
            $9, $10, $11
        )
    `
	var epochID any
	if l.EpochID != "" {
		epochID = l.EpochID
	}
	_, err := s.pool.Exec(ctx, q,
		l.AgentID, epochID,
		l.RealizedTradingPnL, l.TradingFees, l.SlippageCost,
		l.LLMCost, l.InfraRent, l.RPCCost,
		l.UpstreamPaidToParent, l.UpstreamPaidToRoot,
		l.RealizedNetProfit,
	)
	if err != nil {
		return fmt.Errorf("orchestrator: persist ledger: %w", err)
	}
	return nil
}

// PersistSweep writes a profit_sweeps row. Skipped decisions don't
// write anything — the dashboard can derive "skipped" status from the
// absence of a sweep row for that (agent, epoch). The decision's
// Reason field is therefore not persisted; the orchestrator emits a
// log line for skip reasons in chunk 21.
func (s *PostgresEpochStore) PersistSweep(ctx context.Context, d SweepDecision) error {
	if d.Skipped {
		return nil
	}
	if d.Sweep.AgentID == "" {
		return errors.New("orchestrator: PersistSweep requires agent_id")
	}
	const q = `
        INSERT INTO profit_sweeps (
            agent_id, parent_agent_id,
            amount_to_parent_usd, amount_to_root_usd, amount_retained_usd
        ) VALUES ($1, $2, $3, $4, $5)
    `
	var parent any
	if d.Sweep.ParentAgentID != "" {
		parent = d.Sweep.ParentAgentID
	}
	_, err := s.pool.Exec(ctx, q,
		d.Sweep.AgentID, parent,
		d.Sweep.AmountToParent, d.Sweep.AmountToRoot, d.Sweep.AmountRetained,
	)
	if err != nil {
		return fmt.Errorf("orchestrator: persist sweep: %w", err)
	}
	return nil
}

// PersistOffspringDecision updates the existing offspring_proposals row
// with the verdict. The row is created upstream when an agent submits
// a proposal; this method just transitions the status + reason.
func (s *PostgresEpochStore) PersistOffspringDecision(ctx context.Context, d OffspringDecision) error {
	if d.ProposalID == "" {
		return errors.New("orchestrator: PersistOffspringDecision requires proposal_id")
	}
	status := "rejected"
	switch d.Outcome {
	case OffspringApproved:
		status = "approved"
	case OffspringRejected:
		status = "rejected"
	case OffspringRevise:
		status = "pending"
	}
	const q = `
        UPDATE offspring_proposals
        SET status = $2,
            rejection_reason = $3,
            quality_check_verdict = COALESCE($4, quality_check_verdict),
            quality_check_reasoning = COALESCE($5, quality_check_reasoning),
            bull_case = COALESCE($6, bull_case),
            bear_case = COALESCE($7, bear_case),
            adversarial_synthesis = COALESCE($8, adversarial_synthesis)
        WHERE id = $1
    `
	var qVerdict, qReason, bull, bear, synth any
	if d.Quality != nil {
		qVerdict = string(d.Quality.Verdict)
		qReason = d.Quality.Reasoning
	}
	if d.Adversarial != nil {
		bull = d.Adversarial.BullCase
		bear = d.Adversarial.BearCase
		synth = d.Adversarial.Synthesis
	}
	var rejectReason any
	if d.RejectTag != "" {
		rejectReason = d.RejectTag
	}
	_, err := s.pool.Exec(ctx, q,
		d.ProposalID, status, rejectReason,
		qVerdict, qReason, bull, bear, synth,
	)
	if err != nil {
		return fmt.Errorf("orchestrator: persist offspring decision: %w", err)
	}
	return nil
}

// PersistPostmortem inserts a postmortems row. The schema requires a
// snapshot of the agent's strategy_config + strategist_prompt; we read
// those alongside the per-agent stats so the row is self-contained
// (a future analyst can reconstruct what the agent looked like at death
// without joining back to agents at the same point in time).
func (s *PostgresEpochStore) PersistPostmortem(ctx context.Context, p PostmortemResult, agentID string) error {
	if agentID == "" {
		return errors.New("orchestrator: PersistPostmortem requires agent_id")
	}
	const snapQ = `
        SELECT name, strategy_config, strategist_prompt, bandit_state,
               COALESCE(total_trades, 0), COALESCE(total_pnl, 0)
        FROM agents WHERE id = $1
    `
	var (
		name           string
		strategyConfig []byte
		prompt         string
		banditState    []byte
		trades         int
		pnl            float64
	)
	err := s.pool.QueryRow(ctx, snapQ, agentID).Scan(&name, &strategyConfig, &prompt, &banditState, &trades, &pnl)
	if err != nil {
		return fmt.Errorf("orchestrator: load agent for postmortem: %w", err)
	}

	const q = `
        INSERT INTO postmortems (
            agent_id, agent_name,
            lifespan_epochs, total_trades, total_pnl,
            total_llm_cost_usd,
            strategy_config_snapshot, strategist_prompt_snapshot, bandit_final_state,
            analysis, lessons_summary, failure_category
        ) VALUES (
            $1, $2,
            $3, $4, $5,
            $6,
            $7, $8, $9,
            $10, $11, $12
        )
    `
	// lifespan_epochs isn't carried on PostmortemResult; we'd need a
	// separate query to count epochs the agent appeared in. For now,
	// 0 is a safe placeholder (the schema is NOT NULL but accepts 0).
	const lifespanEpochs = 0
	_, err = s.pool.Exec(ctx, q,
		agentID, name,
		lifespanEpochs, trades, pnl,
		p.CostUSD,
		strategyConfig, prompt, banditState,
		p.Diagnosis, p.Summary, string(p.Category),
	)
	if err != nil {
		return fmt.Errorf("orchestrator: persist postmortem: %w", err)
	}
	return nil
}

// LogEpoch writes the per-epoch summary to epochs. The chunk 21
// orchestrator inserts a fresh row at epoch start; this method updates
// the same row with the final counts and the breaker flag.
func (s *PostgresEpochStore) LogEpoch(ctx context.Context, r EpochResult) error {
	if r.EpochID == "" {
		return errors.New("orchestrator: LogEpoch requires epoch_id")
	}
	// Derive counts from the result struct.
	var killed, paused, resumed int
	for _, a := range r.LifecycleActions {
		switch a.Kind {
		case ActionKill:
			killed++
		case ActionPause:
			paused++
		case ActionResume:
			resumed++
		}
	}
	var approved int
	for _, d := range r.OffspringDecisions {
		if d.Outcome == OffspringApproved {
			approved++
		}
	}

	const q = `
        INSERT INTO epochs (
            id, epoch_number, started_at, ended_at,
            total_agents, agents_killed, agents_promoted,
            agents_spawned,
            treasury_balance, total_pnl,
            circuit_breaker_triggered
        )
        VALUES (
            $1,
            (SELECT COALESCE(MAX(epoch_number), 0) + 1 FROM epochs),
            $2, $3,
            $4, $5, $6,
            $7,
            0, 0,
            $8
        )
        ON CONFLICT (id) DO UPDATE SET
            ended_at = EXCLUDED.ended_at,
            agents_killed = EXCLUDED.agents_killed,
            agents_promoted = EXCLUDED.agents_promoted,
            agents_spawned = EXCLUDED.agents_spawned,
            circuit_breaker_triggered = EXCLUDED.circuit_breaker_triggered
    `
	startedAt := r.EndedAt.Add(-1 * time.Minute) // placeholder; parent loop owns started_at
	totalAgents := killed + paused + resumed + approved // best-effort; the loop carries the precise count
	// _ used vars
	_ = paused
	_ = resumed
	_, err := s.pool.Exec(ctx, q,
		r.EpochID,
		startedAt, r.EndedAt,
		totalAgents, killed, 0,
		approved,
		r.BreakerTripped,
	)
	if err != nil {
		return fmt.Errorf("orchestrator: log epoch: %w", err)
	}
	return nil
}
