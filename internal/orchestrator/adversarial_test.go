package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/llm"
)

func advCandidate() agent.AgentGenome {
	return agent.AgentGenome{
		Name:                      "candidate",
		Generation:                2,
		LineageDepth:              1,
		TaskType:                  "liquidation_hunting",
		Chain:                     "base",
		StrategistPrompt:          "you are a liquidation hunter",
		StrategistModel:           "claude-haiku-4-5-20251001",
		StrategistIntervalSeconds: 7200,
		CapitalAllocation:         0.05,
	}
}

func advSwarmCtx() SwarmContext {
	return SwarmContext{
		DiversityScore: 0.5,
		ExistingGenomes: []GenomeSummary{
			{TaskType: "cross_chain_yield", Chain: "solana", Model: "claude-haiku-4-5-20251001"},
			{TaskType: "momentum", Chain: "base", Model: "claude-sonnet-4-6"},
		},
	}
}

func TestAdversarialReview_ApprovePath(t *testing.T) {
	bull := "Fills an open liquidation-hunting niche on base; complements the existing momentum agent."
	bear := "Liquidation flow is competitive; latency to base mempool may be a problem."
	synth := `{"verdict":"approve","synthesis":"bull case wins: niche is open, latency risk bounded by capital cap"}`
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponseQueue(bull, bear, synth).
		WithTokenUsage(100, 30)
	r, err := AdversarialReview(context.Background(), fake, advCandidate(), advSwarmCtx(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != AdversarialApprove {
		t.Errorf("verdict = %q, want approve", r.Verdict)
	}
	if !strings.Contains(r.BullCase, "liquidation-hunting niche") {
		t.Errorf("bull case not preserved: %q", r.BullCase)
	}
	if !strings.Contains(r.BearCase, "latency") {
		t.Errorf("bear case not preserved: %q", r.BearCase)
	}
	if !strings.Contains(r.Synthesis, "niche is open") {
		t.Errorf("synthesis not preserved: %q", r.Synthesis)
	}
	if r.InputTokens != 300 {
		t.Errorf("input tokens = %d, want 300 (3 × 100)", r.InputTokens)
	}
	if r.OutputTokens != 90 {
		t.Errorf("output tokens = %d, want 90 (3 × 30)", r.OutputTokens)
	}
	if r.CostUSD == 0 {
		t.Errorf("cost = 0, want sum of three calls")
	}
}

func TestAdversarialReview_RejectPath(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponseQueue(
			"bull: maybe",
			"bear: this is a near-duplicate of momentum/base/sonnet",
			`{"verdict":"reject","synthesis":"bear case clearly identifies duplication; reject"}`,
		).
		WithTokenUsage(50, 20)
	r, err := AdversarialReview(context.Background(), fake, advCandidate(), advSwarmCtx(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != AdversarialReject {
		t.Errorf("verdict = %q, want reject", r.Verdict)
	}
}

func TestAdversarialReview_BorderlineRevise(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponseQueue(
			"bull: ok",
			"bear: capital cap too aggressive",
			`{"verdict":"revise","synthesis":"reduce capital allocation to 0.03 before spawning"}`,
		).
		WithTokenUsage(60, 25)
	r, err := AdversarialReview(context.Background(), fake, advCandidate(), advSwarmCtx(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != AdversarialRevise {
		t.Errorf("verdict = %q, want revise", r.Verdict)
	}
}

func TestAdversarialReview_MalformedSynthesisDowngradesToRevise(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponseQueue("bull text", "bear text", "yolo not json").
		WithTokenUsage(40, 15)
	r, err := AdversarialReview(context.Background(), fake, advCandidate(), advSwarmCtx(), 0)
	if err != nil {
		t.Fatalf("malformed synthesis should not return error: %v", err)
	}
	if r.Verdict != AdversarialRevise {
		t.Errorf("malformed synthesis verdict = %q, want revise", r.Verdict)
	}
	if !strings.Contains(r.Synthesis, "parse_error") {
		t.Errorf("synthesis should record parse_error, got %q", r.Synthesis)
	}
	if r.BullCase != "bull text" {
		t.Errorf("bull case lost when synthesis was malformed: %q", r.BullCase)
	}
}

func TestAdversarialReview_MissingVerdictCollapsesToRevise(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponseQueue("b", "b", `{"synthesis":"no verdict here"}`).
		WithTokenUsage(40, 15)
	r, err := AdversarialReview(context.Background(), fake, advCandidate(), advSwarmCtx(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != AdversarialRevise {
		t.Errorf("verdict = %q, want revise for missing verdict", r.Verdict)
	}
}

func TestAdversarialReview_UnknownVerdictCollapsesToRevise(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponseQueue("b", "b", `{"verdict":"YOLO","synthesis":"sure"}`).
		WithTokenUsage(40, 15)
	r, err := AdversarialReview(context.Background(), fake, advCandidate(), advSwarmCtx(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != AdversarialRevise {
		t.Errorf("unknown verdict = %q, want revise", r.Verdict)
	}
}

func TestAdversarialReview_ThreeCallsInOrder(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponseQueue(
			"BULL_OUTPUT",
			"BEAR_OUTPUT",
			`{"verdict":"approve","synthesis":"ok"}`,
		).
		WithTokenUsage(10, 5)
	if _, err := AdversarialReview(context.Background(), fake, advCandidate(), advSwarmCtx(), 0); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(calls))
	}
	if !strings.Contains(calls[0].System, "BULL") {
		t.Errorf("call 1 system not bull framing: %q", calls[0].System)
	}
	if !strings.Contains(calls[1].System, "BEAR") {
		t.Errorf("call 2 system not bear framing: %q", calls[1].System)
	}
	if !strings.Contains(calls[2].System, "synthesizer") {
		t.Errorf("call 3 system not synthesis framing: %q", calls[2].System)
	}
	if !strings.Contains(calls[2].User, "BULL_OUTPUT") {
		t.Error("synthesis user prompt missing bull case")
	}
	if !strings.Contains(calls[2].User, "BEAR_OUTPUT") {
		t.Error("synthesis user prompt missing bear case")
	}
}

func TestAdversarialReview_BullErrorPropagates(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithError(errors.New("rate limit"))
	if _, err := AdversarialReview(context.Background(), fake, advCandidate(), advSwarmCtx(), 0); err == nil {
		t.Error("expected error from bull call")
	}
}

func TestAdversarialReview_NilClient(t *testing.T) {
	if _, err := AdversarialReview(context.Background(), nil, advCandidate(), advSwarmCtx(), 0); err == nil {
		t.Error("expected error for nil client")
	}
}

func TestAdversarialReview_PromptContainsCandidate(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponseQueue("b", "b", `{"verdict":"approve","synthesis":"ok"}`).
		WithTokenUsage(10, 5)
	if _, err := AdversarialReview(context.Background(), fake, advCandidate(), advSwarmCtx(), 0); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	for i, c := range calls[:2] {
		if !strings.Contains(c.User, "liquidation_hunting") {
			t.Errorf("call %d: user prompt missing candidate task_type", i+1)
		}
		if !strings.Contains(c.User, "diversity_score") {
			t.Errorf("call %d: user prompt missing swarm diversity_score", i+1)
		}
	}
}

func TestNormalizeAdversarialVerdict(t *testing.T) {
	cases := map[string]AdversarialVerdict{
		"approve":   AdversarialApprove,
		"REJECT":    AdversarialReject,
		" Revise ":  AdversarialRevise,
		"":          AdversarialRevise,
		"maybe":     AdversarialRevise,
	}
	for in, want := range cases {
		if got := normalizeAdversarialVerdict(in); got != want {
			t.Errorf("normalizeAdversarialVerdict(%q) = %q, want %q", in, got, want)
		}
	}
}
