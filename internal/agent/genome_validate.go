package agent

import (
	"fmt"

	"github.com/veermshah/cambrian/internal/llm"
)

// validTaskTypes is the closed set of task types the orchestrator knows
// how to run. Matches the CHECK constraint on `agents.task_type` from
// the chunk 2 schema and spec line 584.
var validTaskTypes = map[string]struct{}{
	"cross_chain_yield":    {},
	"liquidity_provision":  {},
	"liquidation_hunting":  {},
	"momentum":             {},
}

// validChains is the closed set of supported chains. Spec line 585.
var validChains = map[string]struct{}{
	"solana": {},
	"base":   {},
}

// Validate enforces the genome invariants documented in the chunk 8
// spec: non-empty name; non-negative generation / lineage depth /
// integer counts; task_type in the registry; chain in {solana, base};
// strategist_model registered in the LLM rate table; capital_allocation
// in [0, 1]; every *USD field ≥ 0. The first failure short-circuits so
// the caller (DB insert path, mutation path) gets a single actionable
// error rather than a list.
func (g *AgentGenome) Validate() error {
	if g == nil {
		return fmt.Errorf("genome: nil")
	}
	if g.Name == "" {
		return fmt.Errorf("genome: name is required")
	}
	if g.Generation < 0 {
		return fmt.Errorf("genome: generation %d must be >= 0", g.Generation)
	}
	if g.LineageDepth < 0 {
		return fmt.Errorf("genome: lineage_depth %d must be >= 0", g.LineageDepth)
	}
	if _, ok := validTaskTypes[g.TaskType]; !ok {
		return fmt.Errorf("genome: task_type %q not in registry", g.TaskType)
	}
	if _, ok := validChains[g.Chain]; !ok {
		return fmt.Errorf("genome: chain %q must be solana or base", g.Chain)
	}
	if _, ok := llm.RateFor(g.StrategistModel); !ok {
		return fmt.Errorf("genome: strategist_model %q not in llm registry", g.StrategistModel)
	}
	if g.StrategistIntervalSeconds < 0 {
		return fmt.Errorf("genome: strategist_interval_seconds %d must be >= 0", g.StrategistIntervalSeconds)
	}
	if g.CapitalAllocation < 0 || g.CapitalAllocation > 1 {
		return fmt.Errorf("genome: capital_allocation %.4f must be in [0, 1]", g.CapitalAllocation)
	}
	if err := g.ReproductionPolicy.validate(); err != nil {
		return err
	}
	if err := g.CostPolicy.validate(); err != nil {
		return err
	}
	if err := g.CommunicationPolicy.validate(); err != nil {
		return err
	}
	if err := g.SleepSchedule.validate(); err != nil {
		return err
	}
	return nil
}

func (p ReproductionPolicy) validate() error {
	if p.MinProfitableEpochs < 0 {
		return fmt.Errorf("genome: reproduction_policy.min_profitable_epochs %d must be >= 0", p.MinProfitableEpochs)
	}
	if p.MaxDescendantsPerEpoch < 0 {
		return fmt.Errorf("genome: reproduction_policy.max_descendants_per_epoch %d must be >= 0", p.MaxDescendantsPerEpoch)
	}
	if p.MinRealizedNetProfitUSD < 0 {
		return fmt.Errorf("genome: reproduction_policy.min_realized_net_profit_usd %.2f must be >= 0", p.MinRealizedNetProfitUSD)
	}
	if p.OffspringSeedCapitalUSD < 0 {
		return fmt.Errorf("genome: reproduction_policy.offspring_seed_capital_usd %.2f must be >= 0", p.OffspringSeedCapitalUSD)
	}
	if p.OffspringAPIReserveUSD < 0 {
		return fmt.Errorf("genome: reproduction_policy.offspring_api_reserve_usd %.2f must be >= 0", p.OffspringAPIReserveUSD)
	}
	if p.OffspringFailureBufferUSD < 0 {
		return fmt.Errorf("genome: reproduction_policy.offspring_failure_buffer_usd %.2f must be >= 0", p.OffspringFailureBufferUSD)
	}
	return nil
}

func (p CostPolicy) validate() error {
	if p.MonthlyLLMBudgetUSD < 0 {
		return fmt.Errorf("genome: cost_policy.monthly_llm_budget_usd %.2f must be >= 0", p.MonthlyLLMBudgetUSD)
	}
	if p.MonthlyInfraRentBudgetUSD < 0 {
		return fmt.Errorf("genome: cost_policy.monthly_infra_rent_budget_usd %.2f must be >= 0", p.MonthlyInfraRentBudgetUSD)
	}
	return nil
}

func (p CommunicationPolicy) validate() error {
	if p.MaxBroadcastsPerCycle < 0 {
		return fmt.Errorf("genome: communication_policy.max_broadcasts_per_cycle %d must be >= 0", p.MaxBroadcastsPerCycle)
	}
	if p.IntelSummaryMaxItems < 0 {
		return fmt.Errorf("genome: communication_policy.intel_summary_max_items %d must be >= 0", p.IntelSummaryMaxItems)
	}
	return nil
}

func (s SleepSchedule) validate() error {
	if s.AwakeWindowMinutes < 0 {
		return fmt.Errorf("genome: sleep_schedule.awake_window_minutes %d must be >= 0", s.AwakeWindowMinutes)
	}
	return nil
}
