package orchestrator

import (
	"math"
	"testing"

	"github.com/veermshah/cambrian/internal/agent"
)

func genome(task, chain, model string) agent.AgentGenome {
	return agent.AgentGenome{TaskType: task, Chain: chain, StrategistModel: model}
}

func TestDiversityScore_Empty(t *testing.T) {
	if got := DiversityScore(nil); got != 0 {
		t.Errorf("empty swarm score = %v, want 0", got)
	}
	if got := DiversityScore([]agent.AgentGenome{}); got != 0 {
		t.Errorf("zero-len swarm score = %v, want 0", got)
	}
}

func TestDiversityScore_SingleMember(t *testing.T) {
	swarm := []agent.AgentGenome{genome("cross_chain_yield", "solana", "claude-haiku-4-5-20251001")}
	if got := DiversityScore(swarm); got != 0 {
		t.Errorf("single-member score = %v, want 0", got)
	}
}

func TestDiversityScore_AllDuplicates(t *testing.T) {
	swarm := []agent.AgentGenome{
		genome("cross_chain_yield", "solana", "claude-haiku-4-5-20251001"),
		genome("cross_chain_yield", "solana", "claude-haiku-4-5-20251001"),
		genome("cross_chain_yield", "solana", "claude-haiku-4-5-20251001"),
	}
	if got := DiversityScore(swarm); got != 0 {
		t.Errorf("homogeneous swarm score = %v, want 0", got)
	}
}

func TestDiversityScore_AllDistinct(t *testing.T) {
	swarm := []agent.AgentGenome{
		genome("cross_chain_yield", "solana", "claude-haiku-4-5-20251001"),
		genome("momentum", "base", "claude-sonnet-4-6"),
		genome("liquidity_provision", "solana", "gpt-4o-mini"),
		genome("liquidation_hunting", "base", "claude-haiku-4-5-20251001"),
	}
	// 4 equal buckets of size 1 → p = 0.25 each → 1 − 4·(0.0625) = 0.75
	got := DiversityScore(swarm)
	want := 0.75
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("4-distinct score = %v, want %v", got, want)
	}
}

func TestDiversityScore_MonotonicallyDecreasingWithDuplicates(t *testing.T) {
	// Start with 4 distinct, then progressively duplicate the first
	// triple. Each duplicate should strictly lower the score.
	base := []agent.AgentGenome{
		genome("cross_chain_yield", "solana", "claude-haiku-4-5-20251001"),
		genome("momentum", "base", "claude-sonnet-4-6"),
		genome("liquidity_provision", "solana", "gpt-4o-mini"),
		genome("liquidation_hunting", "base", "claude-haiku-4-5-20251001"),
	}
	prev := DiversityScore(base)
	for i := 0; i < 5; i++ {
		base = append(base, genome("cross_chain_yield", "solana", "claude-haiku-4-5-20251001"))
		curr := DiversityScore(base)
		if curr >= prev {
			t.Errorf("step %d: score did not decrease (prev=%v curr=%v)", i, prev, curr)
		}
		prev = curr
	}
}

func TestDiversityBonus_EmptySwarmIsMaxBonus(t *testing.T) {
	if got := DiversityBonus(genome("cross_chain_yield", "solana", "claude-haiku-4-5-20251001"), nil); got != 1.0 {
		t.Errorf("empty swarm bonus = %v, want 1.0", got)
	}
}

func TestDiversityBonus_NovelTripleGetsMaxBonus(t *testing.T) {
	swarm := []agent.AgentGenome{
		genome("momentum", "base", "claude-sonnet-4-6"),
		genome("liquidity_provision", "solana", "gpt-4o-mini"),
	}
	got := DiversityBonus(genome("cross_chain_yield", "solana", "claude-haiku-4-5-20251001"), swarm)
	if got != 1.0 {
		t.Errorf("novel triple bonus = %v, want 1.0", got)
	}
}

func TestDiversityBonus_DuplicateTripleGetsZeroBonus(t *testing.T) {
	swarm := []agent.AgentGenome{
		genome("momentum", "base", "claude-sonnet-4-6"),
		genome("momentum", "base", "claude-sonnet-4-6"),
	}
	got := DiversityBonus(genome("momentum", "base", "claude-sonnet-4-6"), swarm)
	if got != 0 {
		t.Errorf("fully-duplicate triple bonus = %v, want 0", got)
	}
}

func TestDiversityBonus_MonotonicallyIncreasingAsTripleRarer(t *testing.T) {
	// Candidate triple. Build a swarm dominated by it, then progressively
	// replace duplicates with novel triples. Bonus should rise.
	cand := genome("cross_chain_yield", "solana", "claude-haiku-4-5-20251001")
	swarm := []agent.AgentGenome{
		genome("cross_chain_yield", "solana", "claude-haiku-4-5-20251001"),
		genome("cross_chain_yield", "solana", "claude-haiku-4-5-20251001"),
		genome("cross_chain_yield", "solana", "claude-haiku-4-5-20251001"),
		genome("cross_chain_yield", "solana", "claude-haiku-4-5-20251001"),
	}
	prev := DiversityBonus(cand, swarm)
	novel := []agent.AgentGenome{
		genome("momentum", "base", "claude-sonnet-4-6"),
		genome("liquidity_provision", "solana", "gpt-4o-mini"),
		genome("liquidation_hunting", "base", "claude-haiku-4-5-20251001"),
	}
	for i, n := range novel {
		swarm[i] = n
		curr := DiversityBonus(cand, swarm)
		if curr <= prev {
			t.Errorf("step %d: bonus did not increase (prev=%v curr=%v)", i, prev, curr)
		}
		prev = curr
	}
}

func TestDiversityBonus_TableDriven(t *testing.T) {
	cand := genome("momentum", "base", "claude-sonnet-4-6")
	cases := []struct {
		name  string
		swarm []agent.AgentGenome
		want  float64
	}{
		{
			name:  "1 of 4 matches",
			swarm: []agent.AgentGenome{cand, genome("a", "b", "c"), genome("d", "e", "f"), genome("g", "h", "i")},
			want:  0.75,
		},
		{
			name:  "2 of 4 match",
			swarm: []agent.AgentGenome{cand, cand, genome("d", "e", "f"), genome("g", "h", "i")},
			want:  0.5,
		},
		{
			name:  "3 of 4 match",
			swarm: []agent.AgentGenome{cand, cand, cand, genome("g", "h", "i")},
			want:  0.25,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DiversityBonus(cand, tc.swarm)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
