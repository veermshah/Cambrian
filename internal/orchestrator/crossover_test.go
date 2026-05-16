package orchestrator

import (
	"math/rand/v2"
	"testing"

	"github.com/veermshah/cambrian/internal/agent"
)

func parentA() agent.AgentGenome {
	return agent.AgentGenome{
		Name:                      "alpha",
		Generation:                3,
		LineageDepth:              2,
		TaskType:                  "cross_chain_yield",
		Chain:                     "solana",
		StrategyConfig:            map[string]interface{}{"target_apy_pct": 8.0},
		StrategistPrompt:          "prompt-A",
		StrategistModel:           "claude-haiku-4-5-20251001",
		StrategistIntervalSeconds: 14400,
		BanditPolicies:            []string{"default"},
		LearnedRules:              []agent.LearnedRule{{ID: "r1", Text: "from A", Confidence: 0.7}},
		CapitalAllocation:         0.10,
		ReproductionPolicy:        agent.ReproductionPolicy{MinProfitableEpochs: 3, MaxDescendantsPerEpoch: 2},
		CostPolicy:                agent.CostPolicy{MonthlyLLMBudgetUSD: 50},
		CommunicationPolicy:       agent.CommunicationPolicy{MaxBroadcastsPerCycle: 5, IntelSummaryMaxItems: 10},
		SleepSchedule:             agent.SleepSchedule{AwakeWindowMinutes: 60, SleepBetween: true},
	}
}

func parentB() agent.AgentGenome {
	return agent.AgentGenome{
		Name:                      "beta",
		Generation:                5,
		LineageDepth:              4,
		TaskType:                  "momentum",
		Chain:                     "base",
		StrategyConfig:            map[string]interface{}{"momentum_window": 30.0},
		StrategistPrompt:          "prompt-B",
		StrategistModel:           "claude-sonnet-4-6",
		StrategistIntervalSeconds: 7200,
		BanditPolicies:            []string{"aggressive", "defensive"},
		LearnedRules:              []agent.LearnedRule{{ID: "r2", Text: "from B", Confidence: 0.9}},
		CapitalAllocation:         0.30,
		ReproductionPolicy:        agent.ReproductionPolicy{MinProfitableEpochs: 5, MaxDescendantsPerEpoch: 1},
		CostPolicy:                agent.CostPolicy{MonthlyLLMBudgetUSD: 75},
		CommunicationPolicy:       agent.CommunicationPolicy{MaxBroadcastsPerCycle: 3, IntelSummaryMaxItems: 5},
		SleepSchedule:             agent.SleepSchedule{AwakeWindowMinutes: 90},
	}
}

func TestCrossover_RequiresRNG(t *testing.T) {
	_, _, err := Crossover(nil, parentA(), parentB(), ModulePerformance{}, ModulePerformance{})
	if err == nil {
		t.Fatal("expected error for nil rng")
	}
}

func TestCrossover_AdvancesGenerationAndLineage(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 0))
	child, _, err := Crossover(rng, parentA(), parentB(), ModulePerformance{}, ModulePerformance{})
	if err != nil {
		t.Fatal(err)
	}
	if child.Generation != 6 {
		t.Errorf("generation = %d, want max(3,5)+1 = 6", child.Generation)
	}
	if child.LineageDepth != 5 {
		t.Errorf("lineage_depth = %d, want max(2,4)+1 = 5", child.LineageDepth)
	}
}

func TestCrossover_DominantParentWinsModuleApproximately80Pct(t *testing.T) {
	// Parent A strictly dominates every module — child should take from
	// A in ≈80% of trials per module. Allow ±5pp for sampling noise.
	perfA := ModulePerformance{
		TaskPnL: 1000, BrainEfficiency: 100, EconomicsHealth: 10, CommunicationAccuracy: 0.95,
	}
	perfB := ModulePerformance{
		TaskPnL: 10, BrainEfficiency: 1, EconomicsHealth: 1, CommunicationAccuracy: 0.10,
	}

	const trials = 5000
	counts := map[string]int{"task": 0, "brain": 0, "econ": 0, "comms": 0}
	for i := 0; i < trials; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 0))
		_, inh, err := Crossover(rng, parentA(), parentB(), perfA, perfB)
		if err != nil {
			t.Fatal(err)
		}
		if inh.Task == "A" {
			counts["task"]++
		}
		if inh.Brain == "A" {
			counts["brain"]++
		}
		if inh.Economics == "A" {
			counts["econ"]++
		}
		if inh.Communication == "A" {
			counts["comms"]++
		}
	}
	const want = 0.80
	const tol = 0.03 // 3pp at n=5000 is well outside random noise
	for module, c := range counts {
		got := float64(c) / float64(trials)
		if got < want-tol || got > want+tol {
			t.Errorf("module %s: A-share = %.3f, want %.2f ± %.2f", module, got, want, tol)
		}
	}
}

func TestCrossover_DominantParentLosesModuleApproximately20Pct(t *testing.T) {
	// Parent B strictly dominates — verify the 20% upset rate from A's perspective.
	perfA := ModulePerformance{}
	perfB := ModulePerformance{
		TaskPnL: 1000, BrainEfficiency: 100, EconomicsHealth: 10, CommunicationAccuracy: 0.95,
	}
	const trials = 5000
	aWins := 0
	for i := 0; i < trials; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 0))
		_, inh, _ := Crossover(rng, parentA(), parentB(), perfA, perfB)
		if inh.Task == "A" {
			aWins++
		}
	}
	got := float64(aWins) / float64(trials)
	if got < 0.17 || got > 0.23 {
		t.Errorf("A-upset share = %.3f, want ≈0.20", got)
	}
}

func TestCrossover_Deterministic(t *testing.T) {
	pa, pb := parentA(), parentB()
	perf := ModulePerformance{TaskPnL: 1}
	r1 := rand.New(rand.NewPCG(42, 0))
	r2 := rand.New(rand.NewPCG(42, 0))
	c1, i1, _ := Crossover(r1, pa, pb, perf, ModulePerformance{})
	c2, i2, _ := Crossover(r2, pa, pb, perf, ModulePerformance{})
	if i1 != i2 {
		t.Errorf("inheritance non-deterministic: %+v vs %+v", i1, i2)
	}
	if c1.TaskType != c2.TaskType || c1.StrategistModel != c2.StrategistModel {
		t.Errorf("child non-deterministic")
	}
}

func TestCrossover_ChildHasExactlyFourInheritedModules(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 0))
	_, inh, err := Crossover(rng, parentA(), parentB(), ModulePerformance{}, ModulePerformance{})
	if err != nil {
		t.Fatal(err)
	}
	// Each module letter must be "A" or "B" — never empty, never any other value.
	for name, letter := range map[string]string{
		"Task": inh.Task, "Brain": inh.Brain, "Economics": inh.Economics, "Communication": inh.Communication,
	} {
		if letter != "A" && letter != "B" {
			t.Errorf("module %s inheritance = %q, want A or B", name, letter)
		}
	}
}

func TestCrossover_AllSixteenCombinationsReachable(t *testing.T) {
	// With equal scores and many trials, the 16 (A/B)^4 combinations
	// should all appear. Probability of any single combination over
	// 5000 trials is 5000/16 ≈ 312, so seeing all 16 is overwhelmingly
	// likely if module picks are independent.
	const trials = 5000
	seen := map[string]bool{}
	for i := 0; i < trials; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 99))
		_, inh, _ := Crossover(rng, parentA(), parentB(), ModulePerformance{}, ModulePerformance{})
		key := inh.Task + inh.Brain + inh.Economics + inh.Communication
		seen[key] = true
	}
	if len(seen) != 16 {
		t.Errorf("saw %d/16 combinations: %v", len(seen), seen)
	}
}

func TestCrossover_TakesValuesFromCorrectParent(t *testing.T) {
	// Parent A wins every module decisively; with seed forcing the
	// 80% branch, child should equal A on every field.
	perfA := ModulePerformance{TaskPnL: 1, BrainEfficiency: 1, EconomicsHealth: 1, CommunicationAccuracy: 1}
	perfB := ModulePerformance{}

	pa, pb := parentA(), parentB()
	for i := 0; i < 100; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 0))
		child, inh, _ := Crossover(rng, pa, pb, perfA, perfB)
		if inh.Task == "A" && child.TaskType != pa.TaskType {
			t.Errorf("trial %d: Task inheritance A but TaskType = %q", i, child.TaskType)
		}
		if inh.Task == "B" && child.TaskType != pb.TaskType {
			t.Errorf("trial %d: Task inheritance B but TaskType = %q", i, child.TaskType)
		}
		if inh.Brain == "A" && child.StrategistModel != pa.StrategistModel {
			t.Errorf("trial %d: Brain inheritance A but model = %q", i, child.StrategistModel)
		}
		if inh.Economics == "A" && child.CapitalAllocation != pa.CapitalAllocation {
			t.Errorf("trial %d: Economics inheritance A but capital = %v", i, child.CapitalAllocation)
		}
		if inh.Communication == "A" && child.CommunicationPolicy.MaxBroadcastsPerCycle != pa.CommunicationPolicy.MaxBroadcastsPerCycle {
			t.Errorf("trial %d: Communication inheritance A but max_broadcasts = %d",
				i, child.CommunicationPolicy.MaxBroadcastsPerCycle)
		}
	}
}

func TestCrossover_TiesFavorA(t *testing.T) {
	// Equal scores: A should win at exactly the 80% rate (since
	// pickWinner falls through to winnerWins == 0.80 branch).
	const trials = 5000
	aWins := 0
	for i := 0; i < trials; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 0))
		_, inh, _ := Crossover(rng, parentA(), parentB(), ModulePerformance{}, ModulePerformance{})
		if inh.Task == "A" {
			aWins++
		}
	}
	got := float64(aWins) / float64(trials)
	if got < 0.77 || got > 0.83 {
		t.Errorf("tie-A-share = %.3f, want ≈0.80", got)
	}
}

func TestCrossover_ParentNotMutated(t *testing.T) {
	pa, pb := parentA(), parentB()
	origK := pa.StrategyConfig["target_apy_pct"]
	rng := rand.New(rand.NewPCG(1, 0))
	child, _, _ := Crossover(rng, pa, pb, ModulePerformance{}, ModulePerformance{})
	// Modify child's StrategyConfig; parent's must not change.
	if child.StrategyConfig != nil {
		for k := range child.StrategyConfig {
			child.StrategyConfig[k] = 999.0
		}
	}
	if pa.StrategyConfig["target_apy_pct"] != origK {
		t.Errorf("parent A's StrategyConfig was mutated")
	}
}
