package llm

import (
	"context"
	"errors"
	"sync"
)

// FakeLLMClient is a deterministic stand-in for any LLMClient. Tests
// configure it via the builder API:
//
//   f := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
//       WithResponse("yes").
//       WithTokenUsage(120, 18)
//   llm.SetClient("claude-haiku-4-5-20251001", f)
//
// Every Complete call records the (system, user, maxTokens) tuple so
// tests can assert prompts. CostUSD is computed against the real rate
// table — the fake exists to remove the network, not to fake economics.

type FakeLLMClient struct {
	mu sync.Mutex

	model        string
	response     string
	inputTokens  int
	outputTokens int
	failNext     error

	calls []FakeCall
}

// FakeCall captures the arguments of one Complete invocation for test
// assertion. Recorded in order; cleared by ResetCalls.
type FakeCall struct {
	System    string
	User      string
	MaxTokens int
}

var _ LLMClient = (*FakeLLMClient)(nil)

// NewFakeLLMClient builds a fake bound to the given model. Defaults
// produce empty content + zero tokens (so cost is exactly 0 unless the
// test sets a usage row).
func NewFakeLLMClient(model string) *FakeLLMClient {
	return &FakeLLMClient{model: model}
}

// WithResponse sets the canned content returned by every Complete call.
func (f *FakeLLMClient) WithResponse(s string) *FakeLLMClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.response = s
	return f
}

// WithTokenUsage sets the input/output token counts every call returns.
// Cost is recomputed from the real rate table on each call, so a model
// with a known rate produces a non-zero CostUSD by construction.
func (f *FakeLLMClient) WithTokenUsage(in, out int) *FakeLLMClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputTokens = in
	f.outputTokens = out
	return f
}

// WithError makes the next Complete call return err. Cleared after one
// use — chained errors require chained calls to WithError.
func (f *FakeLLMClient) WithError(err error) *FakeLLMClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = err
	return f
}

func (f *FakeLLMClient) Complete(_ context.Context, system, user string, maxTokens int) (*LLMResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, FakeCall{System: system, User: user, MaxTokens: maxTokens})
	if err := f.failNext; err != nil {
		f.failNext = nil
		return nil, err
	}
	if maxTokens <= 0 {
		return nil, errors.New("fake llm: maxTokens must be > 0")
	}
	return &LLMResponse{
		Content:      f.response,
		InputTokens:  f.inputTokens,
		OutputTokens: f.outputTokens,
		CostUSD:      computeCost(f.model, f.inputTokens, f.outputTokens),
		Model:        f.model,
	}, nil
}

func (f *FakeLLMClient) CalculateCost(inputTokens, outputTokens int) float64 {
	return computeCost(f.model, inputTokens, outputTokens)
}

// Calls returns a copy of the recorded call log.
func (f *FakeLLMClient) Calls() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// ResetCalls clears the recorded call log without touching configured
// responses or token usage.
func (f *FakeLLMClient) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}
