// Package agent holds the agent-level data model — the genome that
// defines an agent's identity, task, brain, economics, and communication
// behavior. Mutation (chunk 17), crossover (chunk 18), and the runtime
// loop (chunk 14) all live in this package later.
package agent

// LearnedRule is the minimal shape stored alongside an agent's genome.
// Chunk 17 (postmortem reasoning) owns the LRU/EMA accounting; for now
// we just need a stable JSONB-compatible struct so genomes round-trip
// through the DB.
type LearnedRule struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
}

// SleepSchedule controls the Hermes-style shadow scheduling: the agent
// is awake during a window, sleeps between cycles to save inference
// dollars, and may wake out-of-band when a backtest demands it.
type SleepSchedule struct {
	AwakeWindowMinutes int  `json:"awake_window_minutes"`
	SleepBetween       bool `json:"sleep_between_windows"`
	WakeForBacktest    bool `json:"wake_for_backtest"`
}

// ReproductionPolicy encodes the economic gates that must clear before
// an agent is allowed to spawn offspring, plus which mutation axes are
// permitted on the descendant.
type ReproductionPolicy struct {
	MinProfitableEpochs       int     `json:"min_profitable_epochs"`
	MinRealizedNetProfitUSD   float64 `json:"min_realized_net_profit_usd"`
	OffspringSeedCapitalUSD   float64 `json:"offspring_seed_capital_usd"`
	OffspringAPIReserveUSD    float64 `json:"offspring_api_reserve_usd"`
	OffspringFailureBufferUSD float64 `json:"offspring_failure_buffer_usd"`
	MaxDescendantsPerEpoch    int     `json:"max_descendants_per_epoch"`
	AllowTaskTypeMutation     bool    `json:"allow_task_type_mutation"`
	AllowChainMutation        bool    `json:"allow_chain_mutation"`
	AllowModelMutation        bool    `json:"allow_model_mutation"`
}

// CostPolicy is the monthly spend ceiling enforced by the monitor.
// PauseOnBudgetBreach=true tells the orchestrator to halt the agent
// rather than overspend.
type CostPolicy struct {
	MonthlyLLMBudgetUSD       float64 `json:"monthly_llm_budget_usd"`
	MonthlyInfraRentBudgetUSD float64 `json:"monthly_infra_rent_budget_usd"`
	PauseOnBudgetBreach       bool    `json:"pause_on_budget_breach"`
}

// CommunicationPolicy controls the agent's behavior on the intel bus
// (chunk 22). Subscribe/publish channel names are free-form strings —
// the bus enforces routing.
type CommunicationPolicy struct {
	SubscribeChannels     []string `json:"subscribe_channels"`
	PublishChannels       []string `json:"publish_channels"`
	MaxBroadcastsPerCycle int      `json:"max_broadcasts_per_cycle"`
	IntelSummaryMaxItems  int      `json:"intel_summary_max_items"`
	RequireBullBearTag    bool     `json:"require_bull_bear_tag"`
}

// AgentGenome is the full configuration that defines an agent. JSON
// tags match spec lines 577–605 verbatim so values round-trip through
// the `agents.genome` JSONB column without translation. Mutation and
// crossover land in chunks 17 and 18.
type AgentGenome struct {
	// Identity module.
	Name         string `json:"name"`
	Generation   int    `json:"generation"`
	LineageDepth int    `json:"lineage_depth"`

	// Task module. TaskType is one of:
	//   cross_chain_yield, liquidity_provision, liquidation_hunting, momentum
	// Chain is one of: solana, base.
	TaskType       string                 `json:"task_type"`
	Chain          string                 `json:"chain"`
	StrategyConfig map[string]interface{} `json:"strategy_config"`

	// Brain module. StrategistModel must be a key registered in the
	// internal/llm rate table.
	StrategistPrompt          string        `json:"strategist_prompt"`
	StrategistModel           string        `json:"strategist_model"`
	StrategistIntervalSeconds int           `json:"strategist_interval_seconds"`
	BanditPolicies            []string      `json:"bandit_policies"`
	LearnedRules              []LearnedRule `json:"learned_rules"`

	// Economics module. CapitalAllocation is a fraction in [0, 1] of the
	// treasury earmarked for this agent.
	CapitalAllocation  float64            `json:"capital_allocation"`
	ReproductionPolicy ReproductionPolicy `json:"reproduction_policy"`
	CostPolicy         CostPolicy         `json:"cost_policy"`

	// Communication module.
	CommunicationPolicy CommunicationPolicy `json:"communication_policy"`

	// Shadow scheduling (from Hermes).
	SleepSchedule SleepSchedule `json:"sleep_schedule"`
}
