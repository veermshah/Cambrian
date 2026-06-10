package e2e

import (
	"testing"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/orchestrator"
)

// TestDiversity_MaintainedAcrossSelection — spec line 1197.
// Pure-logic. A diverse swarm should score noticeably above zero; a
// homogeneous swarm should score zero. Selection that culls one task
// type collapses score.
func TestDiversity_MaintainedAcrossSelection(t *testing.T) {
	diverse := []agent.AgentGenome{
		{TaskType: "momentum", Chain: "solana", StrategistModel: "claude-haiku-4-5"},
		{TaskType: "cross_chain_yield", Chain: "base", StrategistModel: "claude-haiku-4-5"},
		{TaskType: "liquidation_hunting", Chain: "solana", StrategistModel: "gpt-5-mini"},
		{TaskType: "liquidity_provision", Chain: "base", StrategistModel: "claude-haiku-4-5"},
	}
	d := orchestrator.DiversityScore(diverse)
	if d < 0.5 {
		t.Errorf("diverse swarm score = %.3f, want ≥0.5", d)
	}

	// Homogeneous — every agent same triple.
	mono := []agent.AgentGenome{
		{TaskType: "momentum", Chain: "solana", StrategistModel: "claude-haiku-4-5"},
		{TaskType: "momentum", Chain: "solana", StrategistModel: "claude-haiku-4-5"},
		{TaskType: "momentum", Chain: "solana", StrategistModel: "claude-haiku-4-5"},
		{TaskType: "momentum", Chain: "solana", StrategistModel: "claude-haiku-4-5"},
	}
	if got := orchestrator.DiversityScore(mono); got != 0 {
		t.Errorf("homogeneous score = %.3f, want 0", got)
	}

	// Selection pressure: cull the rarest niche → score drops.
	post := []agent.AgentGenome{
		{TaskType: "momentum", Chain: "solana", StrategistModel: "claude-haiku-4-5"},
		{TaskType: "momentum", Chain: "solana", StrategistModel: "claude-haiku-4-5"},
		{TaskType: "cross_chain_yield", Chain: "base", StrategistModel: "claude-haiku-4-5"},
	}
	postScore := orchestrator.DiversityScore(post)
	if postScore >= d {
		t.Errorf("post-cull score %.3f should be less than pre-cull %.3f", postScore, d)
	}
}

// TestDiversity_BonusRewardsNovelTriples — chunk 21's fitness add-on:
// a novel (task, chain, model) gets bonus = 1.0; a fully duplicated
// triple gets bonus 0.
func TestDiversity_BonusRewardsNovelTriples(t *testing.T) {
	swarm := []agent.AgentGenome{
		{TaskType: "momentum", Chain: "solana", StrategistModel: "claude-haiku-4-5"},
		{TaskType: "momentum", Chain: "solana", StrategistModel: "claude-haiku-4-5"},
	}
	novel := agent.AgentGenome{TaskType: "liquidity_provision", Chain: "base", StrategistModel: "gpt-5-mini"}
	dup := agent.AgentGenome{TaskType: "momentum", Chain: "solana", StrategistModel: "claude-haiku-4-5"}

	if got := orchestrator.DiversityBonus(novel, swarm); got != 1.0 {
		t.Errorf("novel bonus = %.3f, want 1.0", got)
	}
	if got := orchestrator.DiversityBonus(dup, swarm); got != 0 {
		t.Errorf("dup bonus = %.3f, want 0", got)
	}
}
