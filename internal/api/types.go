// Package api hosts the Gin HTTP server that the dashboard talks to.
// Everything here is read-only — mutations stay CLI-driven (spec line
// 1423). Response shapes live in this file so the dashboard's TypeScript
// types can mirror them 1:1 (chunk 30 codegens from these).
package api

import "time"

// AgentSummary is one row in GET /api/agents and the per-agent
// breadcrumb on detail pages.
type AgentSummary struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Chain         string    `json:"chain"`
	TaskType      string    `json:"task_type"`
	NodeClass     string    `json:"node_class"`
	Status        string    `json:"status"`
	HealthState   string    `json:"health_state"`
	Generation    int       `json:"generation"`
	LineageDepth  int       `json:"lineage_depth"`
	CapitalUSD    float64   `json:"capital_usd"`
	CurrentUSD    float64   `json:"current_usd"`
	TotalPnLUSD   float64   `json:"total_pnl_usd"`
	TotalTrades   int       `json:"total_trades"`
	StrategyModel string    `json:"strategy_model"`
	CreatedAt     time.Time `json:"created_at"`
}

// AgentDetail extends AgentSummary with the per-agent panels the
// dashboard's detail page renders.
type AgentDetail struct {
	AgentSummary
	ParentID         string         `json:"parent_id,omitempty"`
	StrategistPrompt string         `json:"strategist_prompt"`
	StrategyConfig   map[string]any `json:"strategy_config"`
	BanditPolicies   []string       `json:"bandit_policies"`
	LearnedRules     []any          `json:"learned_rules"`
	RecentTrades    []TradeRow     `json:"recent_trades"`
}

// TradeRow is one row in GET /api/trades and GET /api/agents/:id.
type TradeRow struct {
	ID           string    `json:"id"`
	AgentID      string    `json:"agent_id"`
	AgentName    string    `json:"agent_name"`
	EpochID      string    `json:"epoch_id,omitempty"`
	Chain        string    `json:"chain"`
	TradeType    string    `json:"trade_type"`
	TokenPair    string    `json:"token_pair"`
	DEX          string    `json:"dex"`
	AmountIn     float64   `json:"amount_in"`
	AmountOut    float64   `json:"amount_out"`
	FeePaidUSD   float64   `json:"fee_paid_usd"`
	PnLUSD       float64   `json:"pnl_usd"`
	TxSignature  string    `json:"tx_signature,omitempty"`
	IsPaperTrade bool      `json:"is_paper_trade"`
	ExecutedAt   time.Time `json:"executed_at"`
}

// EpochRow is one row in GET /api/epochs.
type EpochRow struct {
	ID                   string    `json:"id"`
	EpochNumber          int       `json:"epoch_number"`
	StartedAt            time.Time `json:"started_at"`
	EndedAt              time.Time `json:"ended_at"`
	TotalAgents          int       `json:"total_agents"`
	FundedAgents         int       `json:"funded_agents"`
	ShadowAgents         int       `json:"shadow_agents"`
	AgentsSpawned        int       `json:"agents_spawned"`
	AgentsKilled         int       `json:"agents_killed"`
	AgentsPromoted       int       `json:"agents_promoted"`
	TreasuryBalanceUSD   float64   `json:"treasury_balance_usd"`
	TotalPnLUSD          float64   `json:"total_pnl_usd"`
	TotalLLMCostUSD      float64   `json:"total_llm_cost_usd"`
	MonthlySpendUSD      float64   `json:"monthly_spend_usd"`
	SwarmDiversityScore  float64   `json:"swarm_diversity_score"`
	MarketRegime         string    `json:"market_regime,omitempty"`
	ParentReasoning      string    `json:"parent_reasoning,omitempty"`
	CircuitBreakerFired  bool      `json:"circuit_breaker_fired"`
}

// LineageNode is one node in the agent family tree returned by
// GET /api/lineage. The dashboard renders this as a DAG via dagre.
type LineageNode struct {
	AgentID       string   `json:"agent_id"`
	Name          string   `json:"name"`
	NodeClass     string   `json:"node_class"`
	Status        string   `json:"status"`
	Generation    int      `json:"generation"`
	ParentIDs     []string `json:"parent_ids"`
	TotalPnLUSD   float64  `json:"total_pnl_usd"`
	EvolutionType string   `json:"evolution_type,omitempty"`
}

// TreasuryState is GET /api/treasury — the operator's at-a-glance view
// of root reserves and monthly spend headroom.
type TreasuryState struct {
	ReserveUSD              float64        `json:"reserve_usd"`
	TotalCapitalAllocatedUSD float64       `json:"total_capital_allocated_usd"`
	MonthlySpendUSD          float64       `json:"monthly_spend_usd"`
	MonthlyBudgetUSD         float64       `json:"monthly_budget_usd"`
	UsedPct                  float64       `json:"used_pct"`
	PerChain                 map[string]float64 `json:"per_chain"`
	UpdatedAt                time.Time     `json:"updated_at"`
}

// PostmortemRow is one entry in GET /api/postmortems.
type PostmortemRow struct {
	ID              string    `json:"id"`
	AgentID         string    `json:"agent_id"`
	AgentName       string    `json:"agent_name"`
	LifespanEpochs  int       `json:"lifespan_epochs"`
	TotalTrades     int       `json:"total_trades"`
	TotalPnLUSD     float64   `json:"total_pnl_usd"`
	TotalLLMCostUSD float64   `json:"total_llm_cost_usd"`
	FailureCategory string    `json:"failure_category,omitempty"`
	LessonsSummary  string    `json:"lessons_summary"`
	Lessons         []any     `json:"lessons,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// OffspringRow is one entry in GET /api/offspring.
type OffspringRow struct {
	ID                    string    `json:"id"`
	ProposingAgentID      string    `json:"proposing_agent_id"`
	EpochID               string    `json:"epoch_id,omitempty"`
	RequestedSeedUSD      float64   `json:"requested_seed_usd"`
	RequestedReserveUSD   float64   `json:"requested_reserve_usd"`
	Rationale             string    `json:"rationale"`
	QualityVerdict        string    `json:"quality_verdict,omitempty"`
	BullCase              string    `json:"bull_case,omitempty"`
	BearCase              string    `json:"bear_case,omitempty"`
	AdversarialSynthesis  string    `json:"adversarial_synthesis,omitempty"`
	Status                string    `json:"status"`
	RejectionReason       string    `json:"rejection_reason,omitempty"`
	CreatedChildID        string    `json:"created_child_id,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

// BudgetState is GET /api/budget — monthly spend + per-category and
// per-agent breakdown.
type BudgetState struct {
	MonthStart       time.Time          `json:"month_start"`
	MonthlyBudgetUSD float64            `json:"monthly_budget_usd"`
	SpentUSD         float64            `json:"spent_usd"`
	RemainingUSD     float64            `json:"remaining_usd"`
	UsedPct          float64            `json:"used_pct"`
	PerCategory      map[string]float64 `json:"per_category"`
	PerAgent         []AgentSpend       `json:"per_agent"`
}

// AgentSpend is one row in BudgetState.PerAgent.
type AgentSpend struct {
	AgentID  string  `json:"agent_id"`
	Name     string  `json:"name"`
	SpentUSD float64 `json:"spent_usd"`
}

// CircuitBreakerState is GET /api/circuit-breaker.
type CircuitBreakerState struct {
	Tripped     bool      `json:"tripped"`
	Reason      string    `json:"reason,omitempty"`
	TrippedAt   time.Time `json:"tripped_at,omitempty"`
	State       string    `json:"state"`
	ResetAfter  time.Time `json:"reset_after,omitempty"`
}

// BacktestRow is one row in GET /api/backtests.
type BacktestRow struct {
	ID             string    `json:"id"`
	Chain          string    `json:"chain"`
	TokenPair      string    `json:"token_pair"`
	PeriodStart    time.Time `json:"period_start"`
	PeriodEnd      time.Time `json:"period_end"`
	InitialCapital float64   `json:"initial_capital_usd"`
	FinalCapital   float64   `json:"final_capital_usd"`
	TotalPnLUSD    float64   `json:"total_pnl_usd"`
	MaxDrawdownPct float64   `json:"max_drawdown_pct"`
	TotalTrades    int       `json:"total_trades"`
	WinRate        float64   `json:"win_rate"`
	Sharpe         float64   `json:"sharpe_ratio"`
	EquityCurve    []float64 `json:"equity_curve"`
	CreatedAt      time.Time `json:"created_at"`
}

// IntelRow is one row in GET /api/intelligence — used as both the
// historical feed and the live WebSocket payload shape.
type IntelRow struct {
	ID               string    `json:"id"`
	SourceAgentID    string    `json:"source_agent_id"`
	SourceAgentName  string    `json:"source_agent_name,omitempty"`
	Channel          string    `json:"channel"`
	SignalType       string    `json:"signal_type"`
	Sentiment        string    `json:"sentiment,omitempty"`
	Confidence       float64   `json:"confidence"`
	SourceAccuracy   float64   `json:"source_accuracy_30d"`
	Data             map[string]any `json:"data"`
	CreatedAt        time.Time `json:"created_at"`
	ExpiresAt        time.Time `json:"expires_at,omitempty"`
}

// ModelPerformance is one row in GET /api/models — per-LLM-model
// aggregate the dashboard uses for the "which model is paying for
// itself" page.
type ModelPerformance struct {
	Model           string  `json:"model"`
	UsedByAgents    int     `json:"used_by_agents"`
	Decisions       int     `json:"decisions"`
	TotalCostUSD    float64 `json:"total_cost_usd"`
	PnLUSD          float64 `json:"pnl_usd"`
	PnLPerDollar    float64 `json:"pnl_per_dollar"`
	AvgInputTokens  int     `json:"avg_input_tokens"`
	AvgOutputTokens int     `json:"avg_output_tokens"`
}

// EvolutionEvent is one row in GET /api/evolution — the dashboard
// renders these as a genealogy timeline.
type EvolutionEvent struct {
	ChildID         string    `json:"child_id"`
	ChildName       string    `json:"child_name"`
	ParentID        string    `json:"parent_id"`
	ParentName      string    `json:"parent_name"`
	SecondParentID  string    `json:"second_parent_id,omitempty"`
	EvolutionMethod string    `json:"evolution_method"`
	Mutations       []string  `json:"mutations"`
	SpawnReasoning  string    `json:"spawn_reasoning,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// DashboardSnapshot is GET /api/dashboard — the single roll-up the
// overview page reads on first paint. Everything else streams via the
// per-resource endpoints + WebSocket.
type DashboardSnapshot struct {
	TotalAgents       int           `json:"total_agents"`
	FundedAgents      int           `json:"funded_agents"`
	ShadowAgents      int           `json:"shadow_agents"`
	Treasury          TreasuryState `json:"treasury"`
	MonthlySpendUSD   float64       `json:"monthly_spend_usd"`
	MonthlyBudgetUSD  float64       `json:"monthly_budget_usd"`
	EquityCurve       []EquityPoint `json:"equity_curve"`
	RecentEpoch       *EpochRow     `json:"recent_epoch,omitempty"`
	OpenProposals     int           `json:"open_proposals"`
	UpdatedAt         time.Time     `json:"updated_at"`
}

// EquityPoint is one (t, equity) sample in DashboardSnapshot.EquityCurve.
type EquityPoint struct {
	At        time.Time `json:"at"`
	EquityUSD float64   `json:"equity_usd"`
}

// ErrorResponse is the body returned alongside a non-2xx status.
type ErrorResponse struct {
	Error string `json:"error"`
}

// listResponse wraps any slice so the JSON shape stays consistent
// ({items: [...], count: N}) across every list endpoint. Pagination
// can be layered on top later without breaking the dashboard.
type listResponse[T any] struct {
	Items []T `json:"items"`
	Count int `json:"count"`
}

func wrapList[T any](items []T) listResponse[T] {
	if items == nil {
		items = []T{}
	}
	return listResponse[T]{Items: items, Count: len(items)}
}
