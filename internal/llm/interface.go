// Package llm provides a chain-agnostic abstraction over LLM providers.
// Anthropic and OpenAI are the two production providers; FakeLLMClient
// is the only thing tests should use (the strategist's evolutionary
// pressure selects against expensive models, so test runs that hit a
// real provider would corrupt the fitness signal).
package llm

import "context"

// LLMClient is the interface every brain module talks to. The signature
// matches spec lines 405-411 verbatim — adding fields here forces every
// downstream test fake to update, so resist the urge.
type LLMClient interface {
	// Complete sends a single (system, user) pair and returns the model's
	// response together with token counts and the dollar cost charged.
	// maxTokens caps the response length; clients enforce it via the
	// provider's max_tokens parameter.
	Complete(ctx context.Context, system, user string, maxTokens int) (*LLMResponse, error)

	// CalculateCost returns the USD cost of a completion given the input
	// and output token counts. Implementations look up the per-model rate
	// table in rates.go.
	CalculateCost(inputTokens, outputTokens int) float64
}

// LLMResponse is what Complete returns. CostUSD is computed at call time
// — strategist code reads it directly to drive the cost-adjusted bandit
// (chunk 14). Model is the canonical model ID (not a friendly name) so
// the orchestrator can route follow-up calls to the same model.
type LLMResponse struct {
	Content      string  `json:"content"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Model        string  `json:"model"`
}
