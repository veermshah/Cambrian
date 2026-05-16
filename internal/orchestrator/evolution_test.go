package orchestrator

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/veermshah/cambrian/internal/agent"
)

// validParent is the canonical healthy genome we mutate from. Validate()
// passes on it; child mutations should pass at least 99% of the time.
func validParent() agent.AgentGenome {
	return agent.AgentGenome{
		Name:                      "alpha",
		Generation:                3,
		LineageDepth:              2,
		TaskType:                  "cross_chain_yield",
		Chain:                     "solana",
		StrategyConfig:            map[string]interface{}{"target_apy_pct": 8.0, "max_position_size_usd": 1000.0},
		StrategistPrompt:          "you are a defi strategist",
		StrategistModel:           "claude-haiku-4-5-20251001",
		StrategistIntervalSeconds: 14400,
		BanditPolicies:            []string{"default"},
		CapitalAllocation:         0.10,
		ReproductionPolicy: agent.ReproductionPolicy{
			MinProfitableEpochs:       3,
			MinRealizedNetProfitUSD:   10.0,
			OffspringSeedCapitalUSD:   25.0,
			OffspringAPIReserveUSD:    5.0,
			OffspringFailureBufferUSD: 2.0,
			MaxDescendantsPerEpoch:    2,
			AllowTaskTypeMutation:     true,
			AllowChainMutation:        true,
			AllowModelMutation:        true,
		},
		CostPolicy: agent.CostPolicy{
			MonthlyLLMBudgetUSD:       50.0,
			MonthlyInfraRentBudgetUSD: 20.0,
		},
		CommunicationPolicy: agent.CommunicationPolicy{
			MaxBroadcastsPerCycle: 5,
			IntelSummaryMaxItems:  10,
		},
		SleepSchedule: agent.SleepSchedule{
			AwakeWindowMinutes: 60,
			SleepBetween:       true,
		},
	}
}

func TestMutate_ValidPassRate(t *testing.T) {
	parent := validParent()
	const trials = 1000
	failures := 0
	var lastErr error
	for i := 0; i < trials; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 1))
		child := Mutate(rng, parent)
		if err := child.Validate(); err != nil {
			failures++
			lastErr = err
		}
	}
	if failures > 10 { // 99% pass = ≤ 10 / 1000
		t.Fatalf("mutation pass rate: %d/%d failures (>1%%), last err: %v",
			failures, trials, lastErr)
	}
	t.Logf("mutation pass rate: %d/%d failures over 1000 trials", failures, trials)
}

func TestMutate_DeterministicSeed(t *testing.T) {
	parent := validParent()
	rng1 := rand.New(rand.NewPCG(42, 0))
	rng2 := rand.New(rand.NewPCG(42, 0))
	a := Mutate(rng1, parent)
	b := Mutate(rng2, parent)
	if a.Chain != b.Chain {
		t.Errorf("chain non-deterministic: %q vs %q", a.Chain, b.Chain)
	}
	if a.TaskType != b.TaskType {
		t.Errorf("task_type non-deterministic")
	}
	if a.StrategistModel != b.StrategistModel {
		t.Errorf("model non-deterministic")
	}
	if a.StrategistIntervalSeconds != b.StrategistIntervalSeconds {
		t.Errorf("interval non-deterministic")
	}
	if a.CapitalAllocation != b.CapitalAllocation {
		t.Errorf("capital_allocation non-deterministic: %v vs %v", a.CapitalAllocation, b.CapitalAllocation)
	}
}

func TestMutate_AdvancesGenerationAndLineage(t *testing.T) {
	parent := validParent()
	rng := rand.New(rand.NewPCG(7, 0))
	child := Mutate(rng, parent)
	if child.Generation != parent.Generation+1 {
		t.Errorf("generation = %d, want %d", child.Generation, parent.Generation+1)
	}
	if child.LineageDepth != parent.LineageDepth+1 {
		t.Errorf("lineage_depth = %d, want %d", child.LineageDepth, parent.LineageDepth+1)
	}
}

func TestMutate_RespectsAllowFlags(t *testing.T) {
	parent := validParent()
	parent.ReproductionPolicy.AllowChainMutation = false
	parent.ReproductionPolicy.AllowTaskTypeMutation = false
	parent.ReproductionPolicy.AllowModelMutation = false

	// Run many trials with different seeds; chain/task/model must
	// never change under the disable flags.
	for i := 0; i < 200; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 99))
		child := Mutate(rng, parent)
		if child.Chain != parent.Chain {
			t.Fatalf("trial %d: chain mutated despite disable flag (%q → %q)", i, parent.Chain, child.Chain)
		}
		if child.TaskType != parent.TaskType {
			t.Fatalf("trial %d: task_type mutated despite disable flag", i)
		}
		if child.StrategistModel != parent.StrategistModel {
			t.Fatalf("trial %d: model mutated despite disable flag", i)
		}
	}
}

func TestMutate_CapitalAllocationStaysInRange(t *testing.T) {
	parent := validParent()
	parent.CapitalAllocation = 0.95
	for i := 0; i < 200; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 0))
		child := Mutate(rng, parent)
		if child.CapitalAllocation < 0 || child.CapitalAllocation > 1 {
			t.Fatalf("trial %d: capital_allocation %v out of [0,1]", i, child.CapitalAllocation)
		}
	}
}

func TestMutate_USDFieldsStayNonNegative(t *testing.T) {
	parent := validParent()
	for i := 0; i < 200; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 0))
		child := Mutate(rng, parent)
		rp := child.ReproductionPolicy
		usdFields := []float64{
			rp.MinRealizedNetProfitUSD,
			rp.OffspringSeedCapitalUSD,
			rp.OffspringAPIReserveUSD,
			rp.OffspringFailureBufferUSD,
			child.CostPolicy.MonthlyLLMBudgetUSD,
			child.CostPolicy.MonthlyInfraRentBudgetUSD,
		}
		for _, v := range usdFields {
			if v < 0 {
				t.Fatalf("trial %d: USD field went negative: %v", i, v)
			}
		}
	}
}

func TestMutate_StrategistIntervalFloorEnforced(t *testing.T) {
	parent := validParent()
	parent.StrategistIntervalSeconds = 70 // close to the 60s floor
	for i := 0; i < 200; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 0))
		child := Mutate(rng, parent)
		if child.StrategistIntervalSeconds < 60 {
			t.Fatalf("trial %d: strategist_interval_seconds %d below 60s floor",
				i, child.StrategistIntervalSeconds)
		}
	}
}

func TestMutate_PromptMutationMarkerAppended(t *testing.T) {
	parent := validParent()
	rng := rand.New(rand.NewPCG(13, 0))
	child := Mutate(rng, parent)
	wantMarker := "mut-v" // generation-specific suffix follows
	if !strings.Contains(child.StrategistPrompt, wantMarker) {
		t.Errorf("strategist_prompt missing mutation marker: %q", child.StrategistPrompt)
	}
	if !strings.HasPrefix(child.StrategistPrompt, parent.StrategistPrompt) {
		t.Errorf("original prompt not preserved as prefix")
	}
}

func TestMutate_BanditPoliciesNeverEmpty(t *testing.T) {
	parent := validParent()
	parent.BanditPolicies = []string{"default"}
	for i := 0; i < 200; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 0))
		child := Mutate(rng, parent)
		if len(child.BanditPolicies) == 0 {
			t.Fatalf("trial %d: bandit_policies empty after mutation", i)
		}
	}
}

func TestMutate_ParentNotModified(t *testing.T) {
	parent := validParent()
	parent.StrategyConfig = map[string]interface{}{"k": 1.0}
	origK := parent.StrategyConfig["k"]
	rng := rand.New(rand.NewPCG(99, 0))
	_ = Mutate(rng, parent)
	if parent.StrategyConfig["k"] != origK {
		t.Errorf("Mutate mutated parent.StrategyConfig")
	}
}

func TestMutate_NilRNGSafe(t *testing.T) {
	parent := validParent()
	child := Mutate(nil, parent)
	if err := child.Validate(); err != nil {
		t.Errorf("nil rng child failed validate: %v", err)
	}
}
