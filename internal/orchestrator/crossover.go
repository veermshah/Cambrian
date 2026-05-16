package orchestrator

import (
	"errors"
	"math/rand/v2"

	"github.com/veermshah/cambrian/internal/agent"
)

// ModulePerformance is the per-domain fitness signal Crossover consumes.
// Spec lines 423-424 (module-level crossover): the child takes each
// module from whichever parent scored higher in that domain, with a
// small chance the lower-scoring parent wins so the swarm doesn't
// collapse onto a single locally-optimal lineage.
//
// The root orchestrator (chunk 21) computes these from history; chunk
// 18 is the consumer.
type ModulePerformance struct {
	// TaskPnL is realized USD P&L over the evaluation window. Higher
	// is better. Drives inheritance of TaskType, Chain, StrategyConfig.
	TaskPnL float64
	// BrainEfficiency is P&L per LLM dollar (USD-out / USD-in). Higher
	// is better. Drives inheritance of strategist prompt, model,
	// interval, bandit policies, learned rules.
	BrainEfficiency float64
	// EconomicsHealth is lineage solvency: higher when the agent and
	// its descendants have positive cumulative net profit and no
	// outstanding debt. Drives inheritance of CapitalAllocation,
	// ReproductionPolicy, CostPolicy.
	EconomicsHealth float64
	// CommunicationAccuracy is the fraction of broadcasts whose
	// predicted direction matched realized outcomes. Drives
	// inheritance of CommunicationPolicy + SleepSchedule.
	CommunicationAccuracy float64
}

// CrossoverInheritance records, for each of the four modules, which
// parent the child inherited from. Chunk 21 persists this into the
// lineage.mutations_applied JSONB column so the dashboard can show the
// full ancestry of any genome.
type CrossoverInheritance struct {
	Task          string // "A" or "B"
	Brain         string
	Economics     string
	Communication string
}

// crossoverWinnerProbability is the chance the higher-scoring parent
// wins a module. Spec line 921 (chunk pack): "with 80% probability
// take from the better-performing parent." 0.80 is high enough to
// preserve fitness gradients but low enough that the rare loser
// sometimes wins, which is what keeps the swarm exploring.
const crossoverWinnerProbability = 0.80

// Crossover produces a child genome by independently picking each
// module from one of the two parents. Returns the inheritance record
// so chunk 21 can persist it. Deterministic given rng.
//
// The child's identity (Name, Generation, LineageDepth) is set from
// parentA's lineage; chunk 21 is responsible for renaming and pinning
// generation = max(A, B) + 1 if it wants something different.
func Crossover(rng *rand.Rand, parentA, parentB agent.AgentGenome, perfA, perfB ModulePerformance) (agent.AgentGenome, CrossoverInheritance, error) {
	if rng == nil {
		return agent.AgentGenome{}, CrossoverInheritance{}, errors.New("crossover: nil rng")
	}

	taskFromA := pickWinner(rng, perfA.TaskPnL, perfB.TaskPnL)
	brainFromA := pickWinner(rng, perfA.BrainEfficiency, perfB.BrainEfficiency)
	econFromA := pickWinner(rng, perfA.EconomicsHealth, perfB.EconomicsHealth)
	commsFromA := pickWinner(rng, perfA.CommunicationAccuracy, perfB.CommunicationAccuracy)

	child := agent.AgentGenome{
		Name:         parentA.Name,
		Generation:   maxInt(parentA.Generation, parentB.Generation) + 1,
		LineageDepth: maxInt(parentA.LineageDepth, parentB.LineageDepth) + 1,
	}

	if taskFromA {
		child.TaskType = parentA.TaskType
		child.Chain = parentA.Chain
		child.StrategyConfig = copyStrategyConfig(parentA.StrategyConfig)
	} else {
		child.TaskType = parentB.TaskType
		child.Chain = parentB.Chain
		child.StrategyConfig = copyStrategyConfig(parentB.StrategyConfig)
	}

	if brainFromA {
		child.StrategistPrompt = parentA.StrategistPrompt
		child.StrategistModel = parentA.StrategistModel
		child.StrategistIntervalSeconds = parentA.StrategistIntervalSeconds
		child.BanditPolicies = append([]string(nil), parentA.BanditPolicies...)
		child.LearnedRules = append([]agent.LearnedRule(nil), parentA.LearnedRules...)
	} else {
		child.StrategistPrompt = parentB.StrategistPrompt
		child.StrategistModel = parentB.StrategistModel
		child.StrategistIntervalSeconds = parentB.StrategistIntervalSeconds
		child.BanditPolicies = append([]string(nil), parentB.BanditPolicies...)
		child.LearnedRules = append([]agent.LearnedRule(nil), parentB.LearnedRules...)
	}

	if econFromA {
		child.CapitalAllocation = parentA.CapitalAllocation
		child.ReproductionPolicy = parentA.ReproductionPolicy
		child.CostPolicy = parentA.CostPolicy
	} else {
		child.CapitalAllocation = parentB.CapitalAllocation
		child.ReproductionPolicy = parentB.ReproductionPolicy
		child.CostPolicy = parentB.CostPolicy
	}

	if commsFromA {
		child.CommunicationPolicy = parentA.CommunicationPolicy
		child.SleepSchedule = parentA.SleepSchedule
	} else {
		child.CommunicationPolicy = parentB.CommunicationPolicy
		child.SleepSchedule = parentB.SleepSchedule
	}

	inh := CrossoverInheritance{
		Task:          letter(taskFromA),
		Brain:         letter(brainFromA),
		Economics:     letter(econFromA),
		Communication: letter(commsFromA),
	}
	return child, inh, nil
}

// pickWinner returns true if Crossover should take this module from
// parent A. The higher-scoring parent wins with probability
// crossoverWinnerProbability; ties favor A (the first parent listed —
// matches the lineage table's "parent_id" / "second_parent_id"
// ordering).
func pickWinner(rng *rand.Rand, scoreA, scoreB float64) bool {
	winnerWins := rng.Float64() < crossoverWinnerProbability
	switch {
	case scoreA > scoreB:
		return winnerWins
	case scoreA < scoreB:
		return !winnerWins
	default:
		// Tie: A is the canonical winner.
		return winnerWins
	}
}

func letter(fromA bool) string {
	if fromA {
		return "A"
	}
	return "B"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func copyStrategyConfig(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return nil
	}
	out := make(map[string]interface{}, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
