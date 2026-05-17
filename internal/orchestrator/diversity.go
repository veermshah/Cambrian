package orchestrator

import "github.com/veermshah/cambrian/internal/agent"

// DiversityScore returns the Simpson-style diversity index of the swarm
// over (task_type, chain, model) triples. Spec line 432 ("rare task_type
// /chain/model triples get a small boost") implies a 0-1 metric that
// climbs as the swarm spreads across more distinct genome triples.
//
// Definition (Gini-Simpson, 1 − Σ pᵢ²):
//   - 0   when every member shares one triple (no diversity)
//   - →1  as members spread across many equally-populated triples
//
// Edge cases:
//   - empty swarm → 0 (no diversity possible)
//   - single member → 0 (one triple, p = 1, score = 1 − 1² = 0)
//
// Monotonicity: adding a duplicate to a perfectly diverse swarm strictly
// lowers the score; adding a brand-new triple to a homogeneous swarm
// strictly raises it.
func DiversityScore(swarm []agent.AgentGenome) float64 {
	if len(swarm) <= 1 {
		return 0
	}
	counts := tripleCounts(swarm)
	total := float64(len(swarm))
	sumSq := 0.0
	for _, c := range counts {
		p := float64(c) / total
		sumSq += p * p
	}
	return 1.0 - sumSq
}

// DiversityBonus returns a 0-1 boost reflecting how rare the candidate's
// (task_type, chain, model) triple is in the swarm. Spec line 432 — used
// by chunk 21's fitness function to nudge offspring toward
// underrepresented niches without overwhelming P&L signal.
//
// Formula: 1 - (count_of_candidate_triple / swarm_size). An empty swarm
// returns 1.0 (maximum bonus — any genome is novel by definition).
//
// Monotonicity: as the candidate's triple becomes rarer in the swarm,
// the bonus rises; an entirely novel triple returns 1.0.
func DiversityBonus(candidate agent.AgentGenome, swarm []agent.AgentGenome) float64 {
	if len(swarm) == 0 {
		return 1.0
	}
	key := tripleKey(candidate.TaskType, candidate.Chain, candidate.StrategistModel)
	matches := 0
	for _, g := range swarm {
		if tripleKey(g.TaskType, g.Chain, g.StrategistModel) == key {
			matches++
		}
	}
	return 1.0 - float64(matches)/float64(len(swarm))
}

func tripleCounts(swarm []agent.AgentGenome) map[string]int {
	out := make(map[string]int, len(swarm))
	for _, g := range swarm {
		out[tripleKey(g.TaskType, g.Chain, g.StrategistModel)]++
	}
	return out
}

func tripleKey(task, chain, model string) string {
	return task + "|" + chain + "|" + model
}
