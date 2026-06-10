package e2e

import (
	"math/rand/v2"
	"testing"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/orchestrator"
)

// TestEvolution_MutationAdvancesGeneration — spec line 1190.
// Pure-logic. Mutate preserves identity invariants: generation +1,
// lineage_depth +1, child remains a valid genome.
func TestEvolution_MutationAdvancesGeneration(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 42))
	parent := agent.AgentGenome{
		Name:                      "scout-7",
		Generation:                3,
		LineageDepth:              2,
		TaskType:                  "momentum",
		Chain:                     "solana",
		StrategistModel:           "claude-haiku-4-5",
		StrategistPrompt:          "be greedy",
		StrategistIntervalSeconds: 14400,
		CapitalAllocation:         100,
	}
	child := orchestrator.Mutate(rng, parent)
	if child.Generation != 4 {
		t.Errorf("child generation = %d, want 4", child.Generation)
	}
	if child.LineageDepth != 3 {
		t.Errorf("child lineage_depth = %d, want 3", child.LineageDepth)
	}
	if child.Name == "" {
		t.Errorf("child name lost")
	}
}

// TestEvolution_CrossoverCoherence — spec line 1190.
// Crossover should always pick each module from one parent or the other,
// never produce a hybrid that mixes incompatible task_type + chain.
func TestEvolution_CrossoverCoherence(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 7))
	parentA := agent.AgentGenome{
		Name: "a", TaskType: "momentum", Chain: "solana",
		StrategistModel: "claude-haiku-4-5", StrategistPrompt: "a prompt",
		StrategistIntervalSeconds: 14400, Generation: 5, LineageDepth: 3,
	}
	parentB := agent.AgentGenome{
		Name: "b", TaskType: "cross_chain_yield", Chain: "base",
		StrategistModel: "gpt-5-mini", StrategistPrompt: "b prompt",
		StrategistIntervalSeconds: 7200, Generation: 4, LineageDepth: 2,
	}
	perfA := orchestrator.ModulePerformance{TaskPnL: 100, BrainEfficiency: 0.5}
	perfB := orchestrator.ModulePerformance{TaskPnL: 50, BrainEfficiency: 0.8}

	for i := 0; i < 20; i++ {
		child, _, err := orchestrator.Crossover(rng, parentA, parentB, perfA, perfB)
		if err != nil {
			t.Fatal(err)
		}
		// task_type and chain must travel as a pair from the SAME parent.
		var fromA bool
		switch child.TaskType {
		case parentA.TaskType:
			fromA = true
		case parentB.TaskType:
			fromA = false
		default:
			t.Fatalf("child task_type %q from neither parent", child.TaskType)
		}
		wantChain := parentB.Chain
		if fromA {
			wantChain = parentA.Chain
		}
		if child.Chain != wantChain {
			t.Errorf("task/chain incoherent: task from %v, chain = %q, want %q",
				map[bool]string{true: "A", false: "B"}[fromA], child.Chain, wantChain)
		}
		// Generation must be max(parents) + 1.
		if child.Generation != 6 {
			t.Errorf("child generation = %d, want 6", child.Generation)
		}
	}
}
