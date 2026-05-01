package llm

import (
	"errors"
	"strings"
	"testing"
)

func TestRegistryUnknownModelError(t *testing.T) {
	ResetOverrides()
	Configure("ak", "ok")
	_, err := Get("claude-not-a-model")
	if err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("err = %v, want unknown model", err)
	}
}

func TestRegistryRoutesByPrefix(t *testing.T) {
	ResetOverrides()
	Configure("ak", "ok")
	// claude-* → AnthropicClient
	c, err := Get("claude-haiku-4-5-20251001")
	if err != nil {
		t.Fatalf("Get claude: %v", err)
	}
	if _, ok := c.(*AnthropicClient); !ok {
		t.Errorf("claude routed to %T, want *AnthropicClient", c)
	}
	// gpt-* → OpenAIClient
	c, err = Get("gpt-4o")
	if err != nil {
		t.Fatalf("Get gpt: %v", err)
	}
	if _, ok := c.(*OpenAIClient); !ok {
		t.Errorf("gpt routed to %T, want *OpenAIClient", c)
	}
}

func TestRegistryMissingKey(t *testing.T) {
	ResetOverrides()
	Configure("", "")
	_, err := Get("claude-haiku-4-5-20251001")
	if err == nil || !strings.Contains(err.Error(), "anthropic api key") {
		t.Fatalf("err = %v, want anthropic key", err)
	}
	_, err = Get("gpt-4o-mini")
	if err == nil || !strings.Contains(err.Error(), "openai api key") {
		t.Fatalf("err = %v, want openai key", err)
	}
}

func TestRegistryOverrideTakesPrecedence(t *testing.T) {
	ResetOverrides()
	Configure("", "") // no keys — would normally fail
	fake := NewFakeLLMClient("claude-haiku-4-5-20251001")
	SetClient("claude-haiku-4-5-20251001", fake)
	got, err := Get("claude-haiku-4-5-20251001")
	if err != nil {
		t.Fatalf("Get with override: %v", err)
	}
	if got != fake {
		t.Errorf("Get returned %T, want the fake we registered", got)
	}
}

func TestRegistryModelsListsAll(t *testing.T) {
	ResetOverrides()
	models := Models()
	expected := []string{
		"claude-haiku-4-5-20251001",
		"claude-sonnet-4-6",
		"claude-opus-4-7",
		"gpt-4o",
		"gpt-4o-mini",
	}
	got := map[string]bool{}
	for _, m := range models {
		got[m] = true
	}
	for _, want := range expected {
		if !got[want] {
			t.Errorf("Models() missing %q (got %v)", want, models)
		}
	}
}

func TestFakeLLMClientRecordsCallsAndComputesCost(t *testing.T) {
	f := NewFakeLLMClient("gpt-4o-mini").
		WithResponse("hello").
		WithTokenUsage(1000, 500)

	resp, err := f.Complete(nil, "sys", "user", 64)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "hello" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.CostUSD <= 0 {
		t.Error("CostUSD must be > 0 (real rate table)")
	}
	if resp.Model != "gpt-4o-mini" {
		t.Errorf("Model = %q", resp.Model)
	}

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	if calls[0].System != "sys" || calls[0].User != "user" || calls[0].MaxTokens != 64 {
		t.Errorf("call = %+v", calls[0])
	}

	f.ResetCalls()
	if len(f.Calls()) != 0 {
		t.Error("ResetCalls did not clear")
	}
}

func TestFakeLLMClientWithError(t *testing.T) {
	sentinel := errors.New("forced")
	f := NewFakeLLMClient("gpt-4o").WithError(sentinel)
	_, err := f.Complete(nil, "", "hi", 10)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	// Error fires once.
	resp, err := f.Complete(nil, "", "hi", 10)
	if err != nil {
		t.Fatalf("second call err = %v", err)
	}
	if resp == nil {
		t.Fatal("second call: nil resp")
	}
}
