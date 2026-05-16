package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/llm"
)

func candidateForTests() agent.AgentGenome {
	return agent.AgentGenome{
		Name:                      "candidate",
		Generation:                1,
		LineageDepth:              1,
		TaskType:                  "cross_chain_yield",
		Chain:                     "solana",
		StrategistPrompt:          "you are a strategist",
		StrategistModel:           "claude-haiku-4-5-20251001",
		StrategistIntervalSeconds: 14400,
		CapitalAllocation:         0.05,
	}
}

func swarmCtxForTests() SwarmContext {
	return SwarmContext{
		DiversityScore: 0.42,
		ExistingGenomes: []GenomeSummary{
			{TaskType: "cross_chain_yield", Chain: "solana", Model: "claude-haiku-4-5-20251001"},
			{TaskType: "momentum", Chain: "base", Model: "claude-sonnet-4-6"},
		},
	}
}

func TestQualityCheck_ApproveResponse(t *testing.T) {
	resp := `{"verdict":"approve","reasoning":"fills a gap on base + liquidation_hunting"}`
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse(resp).
		WithTokenUsage(200, 40)
	r, err := QualityCheck(context.Background(), fake, candidateForTests(), swarmCtxForTests(), 0)
	if err != nil {
		t.Fatalf("QualityCheck: %v", err)
	}
	if r.Verdict != VerdictApprove {
		t.Errorf("verdict = %q, want approve", r.Verdict)
	}
	if !strings.Contains(r.Reasoning, "gap") {
		t.Errorf("reasoning lost: %q", r.Reasoning)
	}
	if r.CostUSD == 0 {
		t.Errorf("cost = 0, want non-zero from FakeLLMClient rate table")
	}
}

func TestQualityCheck_RejectResponse(t *testing.T) {
	resp := `{"verdict":"reject","reasoning":"near-duplicate of existing alpha-1"}`
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").WithResponse(resp).WithTokenUsage(150, 25)
	r, err := QualityCheck(context.Background(), fake, candidateForTests(), swarmCtxForTests(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != VerdictReject {
		t.Errorf("verdict = %q, want reject", r.Verdict)
	}
}

func TestQualityCheck_ReviseResponseWithSuggestions(t *testing.T) {
	resp := `{"verdict":"revise","reasoning":"too aggressive","suggested_revisions":{"capital_allocation":0.02}}`
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").WithResponse(resp).WithTokenUsage(180, 32)
	r, err := QualityCheck(context.Background(), fake, candidateForTests(), swarmCtxForTests(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != VerdictRevise {
		t.Errorf("verdict = %q, want revise", r.Verdict)
	}
	if len(r.SuggestedRevisions) == 0 {
		t.Error("suggested_revisions should be preserved")
	}
}

func TestQualityCheck_MalformedJSONDowngradesToRevise(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse("yolo not json at all").
		WithTokenUsage(50, 10)
	r, err := QualityCheck(context.Background(), fake, candidateForTests(), swarmCtxForTests(), 0)
	if err != nil {
		t.Fatalf("QualityCheck should not return error on parse failure: %v", err)
	}
	if r.Verdict != VerdictRevise {
		t.Errorf("malformed response should collapse to revise, got %q", r.Verdict)
	}
	if !strings.Contains(r.Reasoning, "parse_error") {
		t.Errorf("reasoning should record parse_error, got %q", r.Reasoning)
	}
}

func TestQualityCheck_UnknownVerdictCollapsesToRevise(t *testing.T) {
	resp := `{"verdict":"YOLO","reasoning":"sure"}`
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").WithResponse(resp).WithTokenUsage(50, 10)
	r, err := QualityCheck(context.Background(), fake, candidateForTests(), swarmCtxForTests(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != VerdictRevise {
		t.Errorf("verdict = %q, want revise for unknown value", r.Verdict)
	}
}

func TestQualityCheck_MissingFieldsRejectedAsRevise(t *testing.T) {
	cases := []string{
		`{"verdict":"approve"}`,                          // missing reasoning
		`{"reasoning":"looks good"}`,                     // missing verdict
		`{}`,                                              // both missing
		`prefix {"verdict":"approve","reasoning":""}suffix`, // empty reasoning
	}
	for _, resp := range cases {
		fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").WithResponse(resp).WithTokenUsage(10, 5)
		r, err := QualityCheck(context.Background(), fake, candidateForTests(), swarmCtxForTests(), 0)
		if err != nil {
			t.Fatalf("response %q: unexpected error %v", resp, err)
		}
		if r.Verdict != VerdictRevise {
			t.Errorf("response %q: verdict = %q, want revise", resp, r.Verdict)
		}
	}
}

func TestQualityCheck_FencedJSONParses(t *testing.T) {
	resp := "Sure!\n```json\n{\"verdict\":\"approve\",\"reasoning\":\"ok\"}\n```"
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").WithResponse(resp).WithTokenUsage(100, 20)
	r, err := QualityCheck(context.Background(), fake, candidateForTests(), swarmCtxForTests(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != VerdictApprove {
		t.Errorf("verdict = %q, want approve", r.Verdict)
	}
}

func TestQualityCheck_PromptContainsCandidateAndSwarm(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse(`{"verdict":"approve","reasoning":"ok"}`).
		WithTokenUsage(100, 20)
	if _, err := QualityCheck(context.Background(), fake, candidateForTests(), swarmCtxForTests(), 0); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if !strings.Contains(calls[0].System, "quality-check reviewer") {
		t.Errorf("system prompt missing reviewer framing")
	}
	if !strings.Contains(calls[0].User, "cross_chain_yield") {
		t.Errorf("user prompt missing candidate task_type")
	}
	if !strings.Contains(calls[0].User, "diversity_score") {
		t.Errorf("user prompt missing swarm diversity_score")
	}
}

func TestQualityCheck_LLMErrorPropagates(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").WithError(errors.New("rate limit"))
	if _, err := QualityCheck(context.Background(), fake, candidateForTests(), swarmCtxForTests(), 0); err == nil {
		t.Error("expected error from LLM")
	}
}

func TestQualityCheck_NilClient(t *testing.T) {
	if _, err := QualityCheck(context.Background(), nil, candidateForTests(), swarmCtxForTests(), 0); err == nil {
		t.Error("expected error for nil client")
	}
}

// ---------- RewriteStrategistPrompt ----------

func TestRewritePrompt_ReturnsRewritten(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse("you are a careful strategist").
		WithTokenUsage(150, 20)
	out, cost, err := RewriteStrategistPrompt(context.Background(), fake, "you are a strategist", "// mut-v8", 0)
	if err != nil {
		t.Fatal(err)
	}
	if out != "you are a careful strategist" {
		t.Errorf("rewritten = %q", out)
	}
	if cost == 0 {
		t.Errorf("cost = 0, want non-zero")
	}
}

func TestRewritePrompt_EmptyResponseFallsBackToParent(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse("   \n  ").
		WithTokenUsage(100, 0)
	out, _, err := RewriteStrategistPrompt(context.Background(), fake, "you are a strategist", "// mut-v1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if out != "you are a strategist" {
		t.Errorf("empty rewrite should fall back to parent, got %q", out)
	}
}

func TestRewritePrompt_RequiresParent(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").WithResponse("x").WithTokenUsage(10, 5)
	if _, _, err := RewriteStrategistPrompt(context.Background(), fake, "", "// mut-v1", 0); err == nil {
		t.Error("expected error for empty parent prompt")
	}
}

func TestRewritePrompt_RequiresClient(t *testing.T) {
	if _, _, err := RewriteStrategistPrompt(context.Background(), nil, "x", "y", 0); err == nil {
		t.Error("expected error for nil client")
	}
}

func TestRewritePrompt_LLMErrorPropagates(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").WithError(errors.New("rate limit"))
	if _, _, err := RewriteStrategistPrompt(context.Background(), fake, "x", "y", 0); err == nil {
		t.Error("expected error from LLM")
	}
}

func TestNormalizeVerdict(t *testing.T) {
	cases := map[string]QualityVerdict{
		"approve":  VerdictApprove,
		"REJECT":   VerdictReject,
		"  Revise ": VerdictRevise,
		"":         VerdictRevise,
		"maybe":    VerdictRevise,
	}
	for in, want := range cases {
		if got := normalizeVerdict(in); got != want {
			t.Errorf("normalizeVerdict(%q) = %q, want %q", in, got, want)
		}
	}
}
