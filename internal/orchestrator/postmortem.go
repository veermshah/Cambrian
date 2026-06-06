package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/veermshah/cambrian/internal/llm"
)

// LessonCategory is the closed set the postmortem can return. Spec line
// 1051: chunk 21's postmortem categorizes the lesson learned so the
// dashboard can group by failure mode.
type LessonCategory string

const (
	LessonStrategyDrift     LessonCategory = "strategy_drift"
	LessonRegimeMismatch    LessonCategory = "regime_mismatch"
	LessonCostBreach        LessonCategory = "cost_breach"
	LessonUnrecoverableLoss LessonCategory = "unrecoverable_loss"
	LessonUnknown           LessonCategory = "unknown"
)

// PostmortemInput captures the dead agent's final state the LLM reasons
// over. Constructed by the parent orchestrator after each kill action.
type PostmortemInput struct {
	AgentID     string  `json:"agent_id"`
	Name        string  `json:"name"`
	TaskType    string  `json:"task_type"`
	Chain       string  `json:"chain"`
	Model       string  `json:"strategist_model"`
	Generation  int     `json:"generation"`
	KillReason  string  `json:"kill_reason"`
	FinalBalanceUSD     float64 `json:"final_balance_usd"`
	FinalDrawdown       float64 `json:"final_drawdown"`
	LifetimeNetProfitUSD float64 `json:"lifetime_net_profit_usd"`
	LifetimeLLMCostUSD  float64 `json:"lifetime_llm_cost_usd"`
	EpochsLived         int     `json:"epochs_lived"`
	LastStrategistNote  string  `json:"last_strategist_note"`
}

// PostmortemResult is the structured output returned to the caller and
// persisted to the `postmortems` table.
type PostmortemResult struct {
	Category   LessonCategory
	Summary    string
	Diagnosis  string
	CostUSD    float64
	ModelUsed  string
	InputTokens, OutputTokens int
}

const postmortemSystem = `You are a postmortem analyst for an evolutionary DeFi agent swarm. You
are shown one dead agent's final state — kill reason, lifetime P&L, LLM
cost, drawdown, and the last strategist note. Decide what category the
failure falls into and write a 2-4 sentence diagnosis the swarm can
learn from.

Categories (use exactly one):
  - "strategy_drift"      — the strategy was sound but execution drifted
                            (parameter creep, prompt erosion, regime
                            assumption rotted).
  - "regime_mismatch"     — the market regime moved away from what this
                            strategy is good at; correct decision was
                            to pause / hibernate earlier.
  - "cost_breach"         — LLM or infra cost outran realized P&L; the
                            agent was profitable per trade but
                            unprofitable per dollar.
  - "unrecoverable_loss"  — a single bad position or rapid drawdown made
                            this agent non-viable regardless of cost or
                            regime.

Return JSON only:

  {
    "category": "<one of the four above>",
    "summary": "<one-line takeaway, ≤ 100 chars>",
    "diagnosis": "<2-4 sentence explanation, plain text>"
  }`

type rawPostmortemResponse struct {
	Category  string `json:"category"`
	Summary   string `json:"summary"`
	Diagnosis string `json:"diagnosis"`
}

// GeneratePostmortem issues one LLM call against the dead agent's final
// state and returns the categorized lesson. Defensive: a malformed
// response collapses to category=unknown with the raw output preserved
// in Diagnosis. Transport errors propagate so the caller can retry.
func GeneratePostmortem(ctx context.Context, client llm.LLMClient, in PostmortemInput, maxTokens int) (PostmortemResult, error) {
	if client == nil {
		return PostmortemResult{}, errors.New("postmortem: nil llm client")
	}
	if in.AgentID == "" {
		return PostmortemResult{}, errors.New("postmortem: empty agent id")
	}
	if maxTokens <= 0 {
		maxTokens = 768
	}

	user, err := json.Marshal(in)
	if err != nil {
		return PostmortemResult{}, fmt.Errorf("postmortem: marshal input: %w", err)
	}

	resp, err := client.Complete(ctx, postmortemSystem, string(user), maxTokens)
	if err != nil {
		return PostmortemResult{}, fmt.Errorf("postmortem: llm.Complete: %w", err)
	}

	result := PostmortemResult{
		CostUSD:      resp.CostUSD,
		ModelUsed:    resp.Model,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
	}

	body := extractJSONObject(resp.Content)
	if body == "" {
		result.Category = LessonUnknown
		result.Diagnosis = fmt.Sprintf("parse_error: no JSON object; raw=%s", truncate(resp.Content, 240))
		return result, nil
	}
	var raw rawPostmortemResponse
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&raw); err != nil {
		result.Category = LessonUnknown
		result.Diagnosis = fmt.Sprintf("parse_error: %v; raw=%s", err, truncate(resp.Content, 240))
		return result, nil
	}
	if raw.Category == "" || raw.Summary == "" || raw.Diagnosis == "" {
		result.Category = LessonUnknown
		result.Summary = raw.Summary
		result.Diagnosis = fmt.Sprintf("incomplete_response: %s", truncate(resp.Content, 240))
		return result, nil
	}
	result.Category = normalizeLessonCategory(raw.Category)
	result.Summary = raw.Summary
	result.Diagnosis = raw.Diagnosis
	return result, nil
}

func normalizeLessonCategory(s string) LessonCategory {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "strategy_drift":
		return LessonStrategyDrift
	case "regime_mismatch":
		return LessonRegimeMismatch
	case "cost_breach":
		return LessonCostBreach
	case "unrecoverable_loss":
		return LessonUnrecoverableLoss
	default:
		return LessonUnknown
	}
}
