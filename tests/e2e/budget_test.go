package e2e

import (
	"context"
	"testing"

	"github.com/veermshah/cambrian/internal/llm"
)

// TestBudget_TwentyNodesUnderHundredPerMonth — spec line 1195.
// Acceptance: 20+ shadow nodes at default cadence (4h strategist) stay
// under $100/month for LLM costs. Pure-logic — uses FakeLLMClient with
// real rate table to compute arithmetic only.
//
// Calculation:
//   - strategist cadence: 4h ⇒ 6 calls/day/agent ⇒ 180/month/agent.
//   - 20 agents ⇒ 3600 calls/month.
//   - per call: ~1200 input tokens + ~400 output tokens (typical
//     strategist prompt + JSON decision response).
//   - claude-haiku-4-5 pricing: $1/M input, $5/M output (chunk 18 rates).
//   - cost/call = (1200 / 1_000_000) * 1 + (400 / 1_000_000) * 5
//                = 0.0012 + 0.002 = 0.0032 USD
//   - 3600 * 0.0032 ≈ $11.52/month ⇒ well under $100.
func TestBudget_TwentyNodesUnderHundredPerMonth(t *testing.T) {
	const (
		agents       = 20
		callsPerDay  = 6 // 4h strategist cadence
		daysPerMonth = 30
		inputTokens  = 1200
		outputTokens = 400
	)
	client := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").WithTokenUsage(inputTokens, outputTokens)
	costPerCall := client.CalculateCost(inputTokens, outputTokens)
	totalCalls := agents * callsPerDay * daysPerMonth
	monthlyCost := float64(totalCalls) * costPerCall
	t.Logf("agents=%d calls=%d cost_per_call=$%.5f monthly=$%.2f", agents, totalCalls, costPerCall, monthlyCost)
	if monthlyCost > 100 {
		t.Errorf("monthly cost = $%.2f, want ≤ $100", monthlyCost)
	}
}

// TestBudget_PostmortemPathStaysInBudget — spec line 1195 corollary.
// Even when every kill triggers a postmortem (one extra LLM call per
// killed agent), the budget should hold for a realistic kill rate.
// At 30% monthly kill rate over 20 agents that's 6 postmortems/month —
// negligible vs the strategist baseline.
func TestBudget_PostmortemPathStaysInBudget(t *testing.T) {
	const (
		strategistCalls   = 20 * 6 * 30 // baseline
		strategistInToks  = 1200
		strategistOutToks = 400
		postmortemCalls   = 6
		postmortemInToks  = 800
		postmortemOutToks = 600
	)
	_ = context.Background()
	client := llm.NewFakeLLMClient("claude-haiku-4-5-20251001")
	baseline := float64(strategistCalls) * client.CalculateCost(strategistInToks, strategistOutToks)
	postmortems := float64(postmortemCalls) * client.CalculateCost(postmortemInToks, postmortemOutToks)
	total := baseline + postmortems
	t.Logf("baseline=$%.2f postmortems=$%.2f total=$%.2f", baseline, postmortems, total)
	if total > 100 {
		t.Errorf("with-postmortem total = $%.2f, want ≤ $100", total)
	}
}
