package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/veermshah/cambrian/internal/llm"
)

func deadAgentInput() PostmortemInput {
	return PostmortemInput{
		AgentID:              "agent-dead",
		Name:                 "alpha-2",
		TaskType:             "momentum",
		Chain:                "base",
		Model:                "claude-haiku-4-5-20251001",
		Generation:           3,
		KillReason:           "operating_debt_exceeded",
		FinalBalanceUSD:      0.50,
		FinalDrawdown:        0.42,
		LifetimeNetProfitUSD: -8.0,
		LifetimeLLMCostUSD:   12.0,
		EpochsLived:          7,
		LastStrategistNote:   "regime shift to chop, paused for 2 epochs",
	}
}

func TestPostmortem_HappyPathStructuredOutput(t *testing.T) {
	resp := `{"category":"cost_breach","summary":"LLM cost ($12) outran realized P&L ($-8)","diagnosis":"This agent was profitable on a per-trade basis but the 4-hour strategist cadence on Haiku drove cumulative LLM cost above realized P&L."}`
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse(resp).
		WithTokenUsage(200, 80)
	r, err := GeneratePostmortem(context.Background(), fake, deadAgentInput(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Category != LessonCostBreach {
		t.Errorf("category = %q, want cost_breach", r.Category)
	}
	if !strings.Contains(r.Summary, "outran") {
		t.Errorf("summary not preserved: %q", r.Summary)
	}
	if !strings.Contains(r.Diagnosis, "cumulative LLM cost") {
		t.Errorf("diagnosis not preserved: %q", r.Diagnosis)
	}
	if r.CostUSD == 0 {
		t.Error("cost should reflect rate table")
	}
}

func TestPostmortem_AllFourCategoriesNormalize(t *testing.T) {
	cases := map[string]LessonCategory{
		"strategy_drift":     LessonStrategyDrift,
		"regime_mismatch":    LessonRegimeMismatch,
		"cost_breach":        LessonCostBreach,
		"unrecoverable_loss": LessonUnrecoverableLoss,
	}
	for input, want := range cases {
		resp := `{"category":"` + input + `","summary":"x","diagnosis":"y"}`
		fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
			WithResponse(resp).WithTokenUsage(50, 20)
		r, err := GeneratePostmortem(context.Background(), fake, deadAgentInput(), 0)
		if err != nil {
			t.Fatal(err)
		}
		if r.Category != want {
			t.Errorf("input %q: category = %q, want %q", input, r.Category, want)
		}
	}
}

func TestPostmortem_MalformedCollapsesToUnknown(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse("definitely not json").
		WithTokenUsage(50, 20)
	r, err := GeneratePostmortem(context.Background(), fake, deadAgentInput(), 0)
	if err != nil {
		t.Fatalf("malformed should not error: %v", err)
	}
	if r.Category != LessonUnknown {
		t.Errorf("category = %q, want unknown", r.Category)
	}
	if !strings.Contains(r.Diagnosis, "parse_error") {
		t.Errorf("diagnosis should record parse_error: %q", r.Diagnosis)
	}
}

func TestPostmortem_MissingFieldCollapsesToUnknown(t *testing.T) {
	resp := `{"category":"cost_breach","summary":"x"}` // missing diagnosis
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse(resp).WithTokenUsage(50, 20)
	r, err := GeneratePostmortem(context.Background(), fake, deadAgentInput(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Category != LessonUnknown {
		t.Errorf("category = %q, want unknown for missing diagnosis", r.Category)
	}
	if !strings.Contains(r.Diagnosis, "incomplete_response") {
		t.Errorf("diagnosis should record incomplete_response: %q", r.Diagnosis)
	}
}

func TestPostmortem_UnknownCategoryNormalizesToUnknown(t *testing.T) {
	resp := `{"category":"existential_dread","summary":"x","diagnosis":"y"}`
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse(resp).WithTokenUsage(50, 20)
	r, err := GeneratePostmortem(context.Background(), fake, deadAgentInput(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Category != LessonUnknown {
		t.Errorf("unknown category should normalize to unknown, got %q", r.Category)
	}
}

func TestPostmortem_FencedJSONParses(t *testing.T) {
	resp := "Sure thing:\n```json\n{\"category\":\"regime_mismatch\",\"summary\":\"s\",\"diagnosis\":\"d\"}\n```"
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse(resp).WithTokenUsage(50, 20)
	r, err := GeneratePostmortem(context.Background(), fake, deadAgentInput(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Category != LessonRegimeMismatch {
		t.Errorf("category = %q, want regime_mismatch", r.Category)
	}
}

func TestPostmortem_PromptContainsAgentState(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse(`{"category":"strategy_drift","summary":"s","diagnosis":"d"}`).
		WithTokenUsage(50, 20)
	if _, err := GeneratePostmortem(context.Background(), fake, deadAgentInput(), 0); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if !strings.Contains(calls[0].User, "operating_debt_exceeded") {
		t.Errorf("user prompt missing kill reason")
	}
	if !strings.Contains(calls[0].User, "agent-dead") {
		t.Errorf("user prompt missing agent id")
	}
}

func TestPostmortem_NilClient(t *testing.T) {
	_, err := GeneratePostmortem(context.Background(), nil, deadAgentInput(), 0)
	if err == nil {
		t.Error("expected nil-client error")
	}
}

func TestPostmortem_EmptyAgentID(t *testing.T) {
	in := deadAgentInput()
	in.AgentID = ""
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse(`{"category":"strategy_drift","summary":"s","diagnosis":"d"}`).
		WithTokenUsage(10, 5)
	if _, err := GeneratePostmortem(context.Background(), fake, in, 0); err == nil {
		t.Error("expected empty-agent-id error")
	}
}

func TestPostmortem_LLMErrorPropagates(t *testing.T) {
	fake := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").WithError(errors.New("503"))
	if _, err := GeneratePostmortem(context.Background(), fake, deadAgentInput(), 0); err == nil {
		t.Error("expected LLM error to propagate")
	}
}
