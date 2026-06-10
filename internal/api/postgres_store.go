package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is the production Store backed by the live pgx pool.
// SQL is hand-written rather than codegened — every query corresponds
// to one endpoint, the shapes are stable, and the dashboard is the
// only consumer.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore wraps the pool used by the rest of the swarm.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const agentSummaryCols = `
    id, name, chain, task_type, node_class, status, health_state,
    generation, lineage_depth,
    capital_allocated, current_balance, total_pnl, total_trades,
    strategist_model, created_at
`

func scanAgentSummary(rows pgx.Row) (AgentSummary, error) {
	var a AgentSummary
	err := rows.Scan(
		&a.ID, &a.Name, &a.Chain, &a.TaskType, &a.NodeClass, &a.Status, &a.HealthState,
		&a.Generation, &a.LineageDepth,
		&a.CapitalUSD, &a.CurrentUSD, &a.TotalPnLUSD, &a.TotalTrades,
		&a.StrategyModel, &a.CreatedAt,
	)
	return a, err
}

func (s *PostgresStore) ListAgents(ctx context.Context, opts ListAgentsOpts) ([]AgentSummary, error) {
	q := `SELECT ` + agentSummaryCols + ` FROM agents WHERE 1=1`
	args := []any{}
	if opts.Chain != "" {
		args = append(args, opts.Chain)
		q += fmt.Sprintf(" AND chain = $%d", len(args))
	}
	if opts.NodeClass != "" {
		args = append(args, opts.NodeClass)
		q += fmt.Sprintf(" AND node_class = $%d", len(args))
	}
	if opts.Status != "" {
		args = append(args, opts.Status)
		q += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if opts.TaskType != "" {
		args = append(args, opts.TaskType)
		q += fmt.Sprintf(" AND task_type = $%d", len(args))
	}
	q += " ORDER BY total_pnl DESC NULLS LAST LIMIT 500"
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("api: list agents: %w", err)
	}
	defer rows.Close()
	var out []AgentSummary
	for rows.Next() {
		a, err := scanAgentSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("api: scan agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetAgent(ctx context.Context, id string) (AgentDetail, error) {
	const q = `
        SELECT ` + agentSummaryCols + `,
               COALESCE(parent_id::text, ''),
               strategist_prompt, strategy_config, bandit_policies, learned_rules
        FROM agents
        WHERE id = $1
    `
	var d AgentDetail
	var (
		parent     string
		strategy   []byte
		bandits    []byte
		learned    []byte
	)
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&d.ID, &d.Name, &d.Chain, &d.TaskType, &d.NodeClass, &d.Status, &d.HealthState,
		&d.Generation, &d.LineageDepth,
		&d.CapitalUSD, &d.CurrentUSD, &d.TotalPnLUSD, &d.TotalTrades,
		&d.StrategyModel, &d.CreatedAt,
		&parent, &d.StrategistPrompt, &strategy, &bandits, &learned,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentDetail{}, ErrNotFound
	}
	if err != nil {
		return AgentDetail{}, fmt.Errorf("api: get agent: %w", err)
	}
	d.ParentID = parent
	_ = json.Unmarshal(strategy, &d.StrategyConfig)
	_ = json.Unmarshal(bandits, &d.BanditPolicies)
	_ = json.Unmarshal(learned, &d.LearnedRules)
	d.RecentTrades, _ = s.ListTrades(ctx, ListTradesOpts{AgentID: d.ID, Limit: 25})
	return d, nil
}

func (s *PostgresStore) ListTrades(ctx context.Context, opts ListTradesOpts) ([]TradeRow, error) {
	q := `
        SELECT t.id, t.agent_id, a.name,
               COALESCE(t.epoch_id::text, ''),
               t.chain, t.trade_type, t.token_pair, t.dex,
               t.amount_in, t.amount_out, t.fee_paid, COALESCE(t.pnl, 0),
               COALESCE(t.tx_signature, ''), t.is_paper_trade, t.executed_at
        FROM trades t
        JOIN agents a ON a.id = t.agent_id
        WHERE 1=1
    `
	args := []any{}
	if opts.AgentID != "" {
		args = append(args, opts.AgentID)
		q += fmt.Sprintf(" AND t.agent_id = $%d", len(args))
	}
	if opts.Chain != "" {
		args = append(args, opts.Chain)
		q += fmt.Sprintf(" AND t.chain = $%d", len(args))
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY t.executed_at DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("api: list trades: %w", err)
	}
	defer rows.Close()
	var out []TradeRow
	for rows.Next() {
		var t TradeRow
		if err := rows.Scan(
			&t.ID, &t.AgentID, &t.AgentName, &t.EpochID,
			&t.Chain, &t.TradeType, &t.TokenPair, &t.DEX,
			&t.AmountIn, &t.AmountOut, &t.FeePaidUSD, &t.PnLUSD,
			&t.TxSignature, &t.IsPaperTrade, &t.ExecutedAt,
		); err != nil {
			return nil, fmt.Errorf("api: scan trade: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListEpochs(ctx context.Context, limit int) ([]EpochRow, error) {
	if limit <= 0 {
		limit = 50
	}
	const q = `
        SELECT id, epoch_number, started_at, ended_at,
               total_agents, funded_agents, shadow_agents,
               agents_spawned, agents_killed, agents_promoted,
               treasury_balance, total_pnl,
               total_llm_cost_usd, monthly_spend_to_date_usd,
               COALESCE(swarm_diversity_score, 0),
               COALESCE(market_regime, ''), COALESCE(parent_reasoning, ''),
               circuit_breaker_triggered
        FROM epochs ORDER BY epoch_number DESC LIMIT $1
    `
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("api: list epochs: %w", err)
	}
	defer rows.Close()
	var out []EpochRow
	for rows.Next() {
		var e EpochRow
		if err := rows.Scan(
			&e.ID, &e.EpochNumber, &e.StartedAt, &e.EndedAt,
			&e.TotalAgents, &e.FundedAgents, &e.ShadowAgents,
			&e.AgentsSpawned, &e.AgentsKilled, &e.AgentsPromoted,
			&e.TreasuryBalanceUSD, &e.TotalPnLUSD,
			&e.TotalLLMCostUSD, &e.MonthlySpendUSD,
			&e.SwarmDiversityScore, &e.MarketRegime, &e.ParentReasoning,
			&e.CircuitBreakerFired,
		); err != nil {
			return nil, fmt.Errorf("api: scan epoch: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetLineage(ctx context.Context) ([]LineageNode, error) {
	const q = `
        SELECT a.id, a.name, a.node_class, a.status, a.generation,
               COALESCE(a.parent_id::text, ''),
               COALESCE(l.second_parent_id::text, ''),
               a.total_pnl,
               COALESCE(l.evolution_method, '')
        FROM agents a
        LEFT JOIN lineage l ON l.child_id = a.id
        ORDER BY a.generation ASC, a.created_at ASC
    `
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("api: get lineage: %w", err)
	}
	defer rows.Close()
	var out []LineageNode
	for rows.Next() {
		var (
			n            LineageNode
			parentID     string
			secondParent string
		)
		if err := rows.Scan(
			&n.AgentID, &n.Name, &n.NodeClass, &n.Status, &n.Generation,
			&parentID, &secondParent, &n.TotalPnLUSD, &n.EvolutionType,
		); err != nil {
			return nil, fmt.Errorf("api: scan lineage: %w", err)
		}
		if parentID != "" {
			n.ParentIDs = append(n.ParentIDs, parentID)
		}
		if secondParent != "" {
			n.ParentIDs = append(n.ParentIDs, secondParent)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetTreasury(ctx context.Context) (TreasuryState, error) {
	const q = `
        SELECT
            COALESCE(SUM(CASE WHEN name IN ('root_treasury_solana','root_treasury_base') THEN current_balance ELSE 0 END), 0),
            COALESCE(SUM(capital_allocated), 0)
        FROM agents
        WHERE status = 'active'
    `
	var ts TreasuryState
	if err := s.pool.QueryRow(ctx, q).Scan(&ts.ReserveUSD, &ts.TotalCapitalAllocatedUSD); err != nil {
		return TreasuryState{}, fmt.Errorf("api: treasury: %w", err)
	}

	const chainQ = `
        SELECT chain, COALESCE(SUM(current_balance), 0)
        FROM agents WHERE status = 'active' GROUP BY chain
    `
	rows, err := s.pool.Query(ctx, chainQ)
	if err != nil {
		return ts, fmt.Errorf("api: treasury per chain: %w", err)
	}
	defer rows.Close()
	ts.PerChain = map[string]float64{}
	for rows.Next() {
		var chain string
		var bal float64
		if err := rows.Scan(&chain, &bal); err != nil {
			return ts, err
		}
		ts.PerChain[chain] = bal
	}
	ts.UpdatedAt = time.Now().UTC()
	return ts, rows.Err()
}

func (s *PostgresStore) ListPostmortems(ctx context.Context, limit int) ([]PostmortemRow, error) {
	if limit <= 0 {
		limit = 50
	}
	const q = `
        SELECT id, agent_id, agent_name, lifespan_epochs, total_trades,
               total_pnl, total_llm_cost_usd,
               COALESCE(failure_category, ''), lessons_summary,
               COALESCE(lessons, '[]'::jsonb), created_at
        FROM postmortems ORDER BY created_at DESC LIMIT $1
    `
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("api: list postmortems: %w", err)
	}
	defer rows.Close()
	var out []PostmortemRow
	for rows.Next() {
		var (
			p       PostmortemRow
			lessons []byte
		)
		if err := rows.Scan(
			&p.ID, &p.AgentID, &p.AgentName, &p.LifespanEpochs, &p.TotalTrades,
			&p.TotalPnLUSD, &p.TotalLLMCostUSD, &p.FailureCategory, &p.LessonsSummary,
			&lessons, &p.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("api: scan postmortem: %w", err)
		}
		_ = json.Unmarshal(lessons, &p.Lessons)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListOffspring(ctx context.Context, status string) ([]OffspringRow, error) {
	q := `
        SELECT id, proposing_agent_id, COALESCE(epoch_id::text, ''),
               requested_seed_capital_usd, requested_api_reserve_usd,
               rationale,
               COALESCE(quality_check_verdict, ''),
               COALESCE(bull_case, ''),
               COALESCE(bear_case, ''),
               COALESCE(adversarial_synthesis, ''),
               status,
               COALESCE(rejection_reason, ''),
               COALESCE(created_child_id::text, ''),
               created_at
        FROM offspring_proposals WHERE 1=1
    `
	args := []any{}
	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(" AND status = $%d", len(args))
	}
	q += " ORDER BY created_at DESC LIMIT 200"
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("api: list offspring: %w", err)
	}
	defer rows.Close()
	var out []OffspringRow
	for rows.Next() {
		var o OffspringRow
		if err := rows.Scan(
			&o.ID, &o.ProposingAgentID, &o.EpochID,
			&o.RequestedSeedUSD, &o.RequestedReserveUSD,
			&o.Rationale, &o.QualityVerdict, &o.BullCase, &o.BearCase,
			&o.AdversarialSynthesis, &o.Status, &o.RejectionReason,
			&o.CreatedChildID, &o.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("api: scan offspring: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetBudget(ctx context.Context) (BudgetState, error) {
	// Pull the most recent epoch as the "this month" anchor — the swarm
	// recomputes monthly spend on each epoch close, so the latest row
	// is the authoritative number.
	const q = `
        SELECT COALESCE(monthly_spend_to_date_usd, 0), started_at
        FROM epochs ORDER BY epoch_number DESC LIMIT 1
    `
	var spent float64
	var monthStart time.Time
	err := s.pool.QueryRow(ctx, q).Scan(&spent, &monthStart)
	if errors.Is(err, pgx.ErrNoRows) {
		return BudgetState{MonthStart: time.Now().UTC(), PerCategory: map[string]float64{}}, nil
	}
	if err != nil {
		return BudgetState{}, fmt.Errorf("api: budget: %w", err)
	}
	state := BudgetState{
		MonthStart:       monthStart.UTC(),
		MonthlyBudgetUSD: 500.0, // spec line 358 default; runtime config can override via /api/budget later
		SpentUSD:         spent,
		PerCategory:      map[string]float64{},
	}
	if state.MonthlyBudgetUSD > 0 {
		state.UsedPct = state.SpentUSD / state.MonthlyBudgetUSD
		state.RemainingUSD = state.MonthlyBudgetUSD - state.SpentUSD
	}

	// Per-category from the latest epoch ledgers.
	const catQ = `
        SELECT
            COALESCE(SUM(llm_cost_usd), 0),
            COALESCE(SUM(infra_rent_usd), 0),
            COALESCE(SUM(rpc_cost_usd), 0)
        FROM agent_ledgers
    `
	var llm, infra, rpc float64
	if err := s.pool.QueryRow(ctx, catQ).Scan(&llm, &infra, &rpc); err == nil {
		state.PerCategory["llm"] = llm
		state.PerCategory["infra"] = infra
		state.PerCategory["rpc"] = rpc
	}

	const agentQ = `
        SELECT a.id, a.name, COALESCE(SUM(l.llm_cost_usd + l.infra_rent_usd + l.rpc_cost_usd), 0)
        FROM agents a LEFT JOIN agent_ledgers l ON l.agent_id = a.id
        GROUP BY a.id, a.name ORDER BY 3 DESC LIMIT 20
    `
	rows, err := s.pool.Query(ctx, agentQ)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var a AgentSpend
			if err := rows.Scan(&a.AgentID, &a.Name, &a.SpentUSD); err == nil {
				state.PerAgent = append(state.PerAgent, a)
			}
		}
	}
	return state, nil
}

func (s *PostgresStore) GetCircuitBreaker(ctx context.Context) (CircuitBreakerState, error) {
	// The breaker doesn't have its own table — derive state from the
	// most recent epoch that recorded a trip.
	const q = `
        SELECT circuit_breaker_triggered, COALESCE(market_regime, ''), ended_at
        FROM epochs ORDER BY epoch_number DESC LIMIT 1
    `
	var (
		state    CircuitBreakerState
		tripped  bool
		regime   string
		seenAt   time.Time
	)
	err := s.pool.QueryRow(ctx, q).Scan(&tripped, &regime, &seenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return CircuitBreakerState{State: "untracked"}, nil
	}
	if err != nil {
		return CircuitBreakerState{}, fmt.Errorf("api: circuit breaker: %w", err)
	}
	state.Tripped = tripped
	if tripped {
		state.State = "tripped"
		state.Reason = regime
		state.TrippedAt = seenAt
	} else {
		state.State = "armed"
	}
	return state, nil
}

func (s *PostgresStore) ListBacktests(ctx context.Context, limit int) ([]BacktestRow, error) {
	if limit <= 0 {
		limit = 50
	}
	const q = `
        SELECT id, chain, token_pair, period_start, period_end,
               initial_capital, final_capital, total_pnl, max_drawdown_pct,
               total_trades, COALESCE(win_rate, 0), COALESCE(sharpe_ratio, 0),
               equity_curve, created_at
        FROM backtest_results ORDER BY created_at DESC LIMIT $1
    `
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("api: list backtests: %w", err)
	}
	defer rows.Close()
	var out []BacktestRow
	for rows.Next() {
		var (
			b     BacktestRow
			curve []byte
		)
		if err := rows.Scan(
			&b.ID, &b.Chain, &b.TokenPair, &b.PeriodStart, &b.PeriodEnd,
			&b.InitialCapital, &b.FinalCapital, &b.TotalPnLUSD, &b.MaxDrawdownPct,
			&b.TotalTrades, &b.WinRate, &b.Sharpe, &curve, &b.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("api: scan backtest: %w", err)
		}
		_ = json.Unmarshal(curve, &b.EquityCurve)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListIntel(ctx context.Context, opts ListIntelOpts) ([]IntelRow, error) {
	q := `
        SELECT i.id, i.source_agent_id, COALESCE(a.name, ''), i.channel, i.signal_type,
               COALESCE(i.sentiment, ''), COALESCE(i.confidence, 0),
               COALESCE(i.source_accuracy_30d, 0), i.data, i.created_at,
               COALESCE(i.expires_at, '0001-01-01T00:00:00Z'::timestamptz)
        FROM intel_log i
        LEFT JOIN agents a ON a.id = i.source_agent_id
        WHERE 1=1
    `
	args := []any{}
	if opts.Channel != "" {
		args = append(args, opts.Channel)
		q += fmt.Sprintf(" AND i.channel = $%d", len(args))
	}
	if opts.Sentiment != "" {
		args = append(args, opts.Sentiment)
		q += fmt.Sprintf(" AND i.sentiment = $%d", len(args))
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY i.created_at DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("api: list intel: %w", err)
	}
	defer rows.Close()
	var out []IntelRow
	for rows.Next() {
		var (
			r    IntelRow
			data []byte
		)
		if err := rows.Scan(
			&r.ID, &r.SourceAgentID, &r.SourceAgentName, &r.Channel, &r.SignalType,
			&r.Sentiment, &r.Confidence, &r.SourceAccuracy, &data, &r.CreatedAt, &r.ExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("api: scan intel: %w", err)
		}
		_ = json.Unmarshal(data, &r.Data)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListModels(ctx context.Context) ([]ModelPerformance, error) {
	const q = `
        SELECT d.model_used,
               COUNT(DISTINCT d.agent_id),
               COUNT(*),
               COALESCE(SUM(d.cost_usd), 0),
               COALESCE(SUM(a.total_pnl), 0),
               COALESCE(AVG(d.input_tokens), 0)::int,
               COALESCE(AVG(d.output_tokens), 0)::int
        FROM strategist_decisions d
        LEFT JOIN agents a ON a.id = d.agent_id
        GROUP BY d.model_used
        ORDER BY 4 DESC
    `
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("api: list models: %w", err)
	}
	defer rows.Close()
	var out []ModelPerformance
	for rows.Next() {
		var m ModelPerformance
		if err := rows.Scan(
			&m.Model, &m.UsedByAgents, &m.Decisions, &m.TotalCostUSD,
			&m.PnLUSD, &m.AvgInputTokens, &m.AvgOutputTokens,
		); err != nil {
			return nil, fmt.Errorf("api: scan model: %w", err)
		}
		if m.TotalCostUSD > 0 {
			m.PnLPerDollar = m.PnLUSD / m.TotalCostUSD
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListEvolution(ctx context.Context, limit int) ([]EvolutionEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `
        SELECT l.child_id, c.name, l.parent_id, p.name,
               COALESCE(l.second_parent_id::text, ''),
               l.evolution_method, l.mutations_applied,
               COALESCE(l.spawn_reasoning, ''), l.created_at
        FROM lineage l
        JOIN agents c ON c.id = l.child_id
        JOIN agents p ON p.id = l.parent_id
        ORDER BY l.created_at DESC LIMIT $1
    `
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("api: list evolution: %w", err)
	}
	defer rows.Close()
	var out []EvolutionEvent
	for rows.Next() {
		var (
			e         EvolutionEvent
			mutations []byte
		)
		if err := rows.Scan(
			&e.ChildID, &e.ChildName, &e.ParentID, &e.ParentName, &e.SecondParentID,
			&e.EvolutionMethod, &mutations, &e.SpawnReasoning, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("api: scan evolution: %w", err)
		}
		_ = json.Unmarshal(mutations, &e.Mutations)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetDashboardSnapshot(ctx context.Context) (DashboardSnapshot, error) {
	var snap DashboardSnapshot
	const agentsQ = `
        SELECT
            COUNT(*),
            COUNT(*) FILTER (WHERE node_class = 'funded'),
            COUNT(*) FILTER (WHERE node_class = 'shadow')
        FROM agents WHERE status = 'active'
    `
	if err := s.pool.QueryRow(ctx, agentsQ).Scan(&snap.TotalAgents, &snap.FundedAgents, &snap.ShadowAgents); err != nil {
		return snap, fmt.Errorf("api: snapshot agents: %w", err)
	}

	ts, err := s.GetTreasury(ctx)
	if err != nil {
		return snap, err
	}
	snap.Treasury = ts

	budget, err := s.GetBudget(ctx)
	if err == nil {
		snap.MonthlySpendUSD = budget.SpentUSD
		snap.MonthlyBudgetUSD = budget.MonthlyBudgetUSD
	}

	epochs, err := s.ListEpochs(ctx, 1)
	if err == nil && len(epochs) > 0 {
		snap.RecentEpoch = &epochs[0]
	}

	const equityQ = `
        SELECT ended_at, treasury_balance
        FROM epochs ORDER BY epoch_number DESC LIMIT 50
    `
	rows, err := s.pool.Query(ctx, equityQ)
	if err == nil {
		defer rows.Close()
		var pts []EquityPoint
		for rows.Next() {
			var p EquityPoint
			if err := rows.Scan(&p.At, &p.EquityUSD); err == nil {
				pts = append(pts, p)
			}
		}
		// Reverse so the dashboard receives oldest → newest.
		for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
			pts[i], pts[j] = pts[j], pts[i]
		}
		snap.EquityCurve = pts
	}

	const openQ = `SELECT COUNT(*) FROM offspring_proposals WHERE status IN ('pending','quality_check','adversarial_review')`
	_ = s.pool.QueryRow(ctx, openQ).Scan(&snap.OpenProposals)

	snap.UpdatedAt = time.Now().UTC()
	return snap, nil
}
