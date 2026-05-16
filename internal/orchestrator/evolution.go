package orchestrator

import (
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/veermshah/cambrian/internal/agent"
)

// Mutate produces a copy of parent with one or more module mutations
// applied. Spec lines 419-421: numeric perturbation common (~50%),
// prompt/model/chain rarer (5-15%). Mutations respect AllowTaskTypeMutation,
// AllowChainMutation, AllowModelMutation flags on the reproduction policy
// — a parent that disallows chain mutation can't produce chain-mutated
// offspring even if rng picks the chain-mutation branch.
//
// The function is deterministic given rng: same seed → same mutation
// sequence. Pass a freshly seeded rng per child to keep mutations
// independent across spawns.
func Mutate(rng *rand.Rand, parent agent.AgentGenome) agent.AgentGenome {
	if rng == nil {
		rng = rand.New(rand.NewPCG(1, 1))
	}
	child := parent

	// Identity always advances generation and lineage depth. The chunk
	// 18 crossover path bumps these separately; mutation alone is a
	// generation step.
	child.Generation = parent.Generation + 1
	child.LineageDepth = parent.LineageDepth + 1

	child = mutateTaskModule(rng, child)
	child = mutateBrainModule(rng, child)
	child = mutateEconomicsModule(rng, child)
	child = mutateCommunicationModule(rng, child)
	return child
}

// mutateTaskModule perturbs numeric strategy_config fields and (rarely)
// rewrites task_type / chain. Numeric perturbation: each numeric value
// in StrategyConfig has a 50% chance of being scaled by U(0.8, 1.2).
// Chain swap is 5% conditional on AllowChainMutation; task-type swap is
// 5% conditional on AllowTaskTypeMutation.
func mutateTaskModule(rng *rand.Rand, g agent.AgentGenome) agent.AgentGenome {
	if g.StrategyConfig != nil {
		out := make(map[string]interface{}, len(g.StrategyConfig))
		for k, v := range g.StrategyConfig {
			switch x := v.(type) {
			case float64:
				if rng.Float64() < 0.50 {
					out[k] = clampFloat(x * uniform(rng, 0.8, 1.2))
				} else {
					out[k] = x
				}
			case int:
				if rng.Float64() < 0.50 {
					scaled := float64(x) * uniform(rng, 0.8, 1.2)
					if scaled < 0 {
						scaled = 0
					}
					out[k] = int(scaled + 0.5)
				} else {
					out[k] = x
				}
			default:
				out[k] = v
			}
		}
		g.StrategyConfig = out
	}

	if g.ReproductionPolicy.AllowChainMutation && rng.Float64() < 0.05 {
		g.Chain = swapChain(rng, g.Chain)
	}
	if g.ReproductionPolicy.AllowTaskTypeMutation && rng.Float64() < 0.05 {
		g.TaskType = swapTaskType(rng, g.TaskType)
	}
	return g
}

// mutateBrainModule handles the strategist prompt + model + interval +
// bandit policy axes.
//
// Prompt rewrite is deferred to chunk 19 (calls LLM). For now we
// append a deterministic marker like "// mut-v{generation}" so chunk
// 19's quality-check can detect that a rewrite is pending.
//
// Model switch is 10% conditional on AllowModelMutation. Cost-adjusted
// model weighting is chunk 19 territory; here we sample uniformly from
// the allowed list.
//
// Strategist interval is perturbed ±20% with 50% probability.
//
// Bandit policies: with 15% probability we either add or drop one
// policy (clamped to ≥ 1 policy).
func mutateBrainModule(rng *rand.Rand, g agent.AgentGenome) agent.AgentGenome {
	g.StrategistPrompt = appendMutationMarker(g.StrategistPrompt, g.Generation)

	if g.ReproductionPolicy.AllowModelMutation && rng.Float64() < 0.10 {
		g.StrategistModel = swapModel(rng, g.StrategistModel)
	}

	if rng.Float64() < 0.50 && g.StrategistIntervalSeconds > 0 {
		scaled := float64(g.StrategistIntervalSeconds) * uniform(rng, 0.8, 1.2)
		if scaled < 60 {
			scaled = 60 // never let cadence drop under 1 minute
		}
		g.StrategistIntervalSeconds = int(scaled + 0.5)
	}

	if rng.Float64() < 0.15 {
		g.BanditPolicies = mutateBanditPolicies(rng, g.BanditPolicies)
	}

	return g
}

// mutateEconomicsModule perturbs each *USD field with 50% probability,
// scaled ±20%, clamped to ≥ 0. CapitalAllocation is perturbed ±20%
// clamped to [0, 1].
func mutateEconomicsModule(rng *rand.Rand, g agent.AgentGenome) agent.AgentGenome {
	if rng.Float64() < 0.50 {
		g.CapitalAllocation = clampUnit(g.CapitalAllocation * uniform(rng, 0.8, 1.2))
	}

	rp := &g.ReproductionPolicy
	rp.MinRealizedNetProfitUSD = perturbUSD(rng, rp.MinRealizedNetProfitUSD)
	rp.OffspringSeedCapitalUSD = perturbUSD(rng, rp.OffspringSeedCapitalUSD)
	rp.OffspringAPIReserveUSD = perturbUSD(rng, rp.OffspringAPIReserveUSD)
	rp.OffspringFailureBufferUSD = perturbUSD(rng, rp.OffspringFailureBufferUSD)

	cp := &g.CostPolicy
	cp.MonthlyLLMBudgetUSD = perturbUSD(rng, cp.MonthlyLLMBudgetUSD)
	cp.MonthlyInfraRentBudgetUSD = perturbUSD(rng, cp.MonthlyInfraRentBudgetUSD)

	return g
}

// mutateCommunicationModule perturbs integer fields ±20% (≥ 0) and
// toggles bull/bear tag requirement with low probability.
func mutateCommunicationModule(rng *rand.Rand, g agent.AgentGenome) agent.AgentGenome {
	cp := &g.CommunicationPolicy
	if rng.Float64() < 0.50 {
		cp.MaxBroadcastsPerCycle = perturbCountNonNeg(rng, cp.MaxBroadcastsPerCycle)
	}
	if rng.Float64() < 0.50 {
		cp.IntelSummaryMaxItems = perturbCountNonNeg(rng, cp.IntelSummaryMaxItems)
	}
	if rng.Float64() < 0.05 {
		cp.RequireBullBearTag = !cp.RequireBullBearTag
	}
	return g
}

// ---------- helpers ----------

func uniform(rng *rand.Rand, lo, hi float64) float64 {
	return lo + rng.Float64()*(hi-lo)
}

func clampFloat(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}

func clampUnit(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

func perturbUSD(rng *rand.Rand, v float64) float64 {
	if rng.Float64() >= 0.50 {
		return v
	}
	scaled := v * uniform(rng, 0.8, 1.2)
	if scaled < 0 {
		return 0
	}
	return scaled
}

func perturbCountNonNeg(rng *rand.Rand, v int) int {
	scaled := float64(v) * uniform(rng, 0.8, 1.2)
	if scaled < 0 {
		return 0
	}
	return int(scaled + 0.5)
}

// swapChain flips between solana and base. Same value as parent is
// allowed (rng can land on the same one), but it's a fast no-op.
func swapChain(rng *rand.Rand, current string) string {
	options := []string{"solana", "base"}
	return options[rng.IntN(len(options))]
}

// swapTaskType picks uniformly from the four registered task types.
// We intentionally don't filter out the parent's current type — the
// rng is allowed to "mutate to self," which the quality check (chunk
// 19) will collapse to a no-op spawn.
func swapTaskType(rng *rand.Rand, current string) string {
	options := []string{"cross_chain_yield", "liquidity_provision", "liquidation_hunting", "momentum"}
	return options[rng.IntN(len(options))]
}

// swapModel picks uniformly from the registered LLM models. Cost-adjusted
// weighting comes in chunk 19.
func swapModel(rng *rand.Rand, current string) string {
	options := []string{
		"claude-haiku-4-5-20251001",
		"claude-sonnet-4-6",
		"claude-opus-4-7",
		"gpt-4o",
		"gpt-4o-mini",
	}
	return options[rng.IntN(len(options))]
}

// mutateBanditPolicies adds or drops one policy. If the parent has
// only one policy we never drop (every agent needs at least one arm).
func mutateBanditPolicies(rng *rand.Rand, parent []string) []string {
	// Copy so we don't mutate the parent's slice.
	out := append([]string(nil), parent...)
	candidates := []string{"default", "aggressive", "defensive", "momentum", "mean_revert", "trend_follow"}

	if len(out) <= 1 || rng.Float64() < 0.5 {
		// Add: pick a candidate that's not already present.
		have := map[string]bool{}
		for _, p := range out {
			have[p] = true
		}
		var missing []string
		for _, c := range candidates {
			if !have[c] {
				missing = append(missing, c)
			}
		}
		if len(missing) == 0 {
			return out
		}
		sort.Strings(missing)
		return append(out, missing[rng.IntN(len(missing))])
	}
	// Drop one.
	idx := rng.IntN(len(out))
	return append(out[:idx], out[idx+1:]...)
}

// appendMutationMarker tags the prompt so the chunk-19 quality check
// knows a rewrite is pending. We tag with the post-mutation generation
// number so multiple mutations append distinct markers.
func appendMutationMarker(prompt string, generation int) string {
	marker := fmt.Sprintf("\n// mut-v%d", generation+1)
	return prompt + marker
}
