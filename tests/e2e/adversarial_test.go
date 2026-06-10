package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/llm"
	"github.com/veermshah/cambrian/internal/orchestrator"
)

// TestAdversarial_RunsBullBearSynthesis — spec line 1191.
// Gated by ANTHROPIC_API_KEY because this test issues 3 real LLM calls
// (bull, bear, synthesis) — each ~$0.01 on claude-haiku-4-5. Verifies
// the orchestrator's adversarial pipeline lands a valid verdict end to
// end against a live model.
func TestAdversarial_RunsBullBearSynthesis(t *testing.T) {
	apiKey := requireLLM(t)
	client, err := llm.NewAnthropicClient(apiKey, "claude-haiku-4-5-20251001")
	if err != nil {
		t.Fatal(err)
	}
	candidate := agent.AgentGenome{
		Name: "candidate", TaskType: "momentum", Chain: "solana",
		StrategistModel:           "claude-haiku-4-5-20251001",
		StrategistPrompt:          "trade momentum on a single pair",
		StrategistIntervalSeconds: 14400,
		CapitalAllocation:         100,
	}
	swarm := orchestrator.SwarmContext{
		DiversityScore: 0.5,
		ExistingGenomes: []orchestrator.GenomeSummary{
			{TaskType: "cross_chain_yield", Chain: "base"},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := orchestrator.AdversarialReview(ctx, client, candidate, swarm, 512)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != orchestrator.AdversarialApprove &&
		res.Verdict != orchestrator.AdversarialReject &&
		res.Verdict != orchestrator.AdversarialRevise {
		t.Errorf("unexpected verdict: %q", res.Verdict)
	}
	if strings.TrimSpace(res.BullCase) == "" {
		t.Error("bull case empty")
	}
	if strings.TrimSpace(res.BearCase) == "" {
		t.Error("bear case empty")
	}
	if strings.TrimSpace(res.Synthesis) == "" {
		t.Error("synthesis empty")
	}
	if res.CostUSD <= 0 {
		t.Errorf("cost = %v, expected positive", res.CostUSD)
	}
}
