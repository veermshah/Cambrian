package llm

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicCompleteSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("path = %q, want /messages", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("anthropic-version = %q", r.Header.Get("anthropic-version"))
		}
		body, _ := io.ReadAll(r.Body)
		var req anthropicMsgRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if req.Model != "claude-haiku-4-5-20251001" {
			t.Errorf("model = %q", req.Model)
		}
		if req.System != "you are a strategist" {
			t.Errorf("system = %q", req.System)
		}
		if len(req.Messages) != 1 || req.Messages[0].Content != "go long ETH?" {
			t.Errorf("messages = %+v", req.Messages)
		}
		if req.MaxTokens != 256 {
			t.Errorf("MaxTokens = %d", req.MaxTokens)
		}
		_, _ = io.WriteString(w, `{
			"content": [{"type": "text", "text": "yes, with stop loss"}],
			"model": "claude-haiku-4-5-20251001",
			"usage": {"input_tokens": 1500, "output_tokens": 200}
		}`)
	}))
	defer srv.Close()

	c, err := NewAnthropicClient("test-key", "claude-haiku-4-5-20251001")
	if err != nil {
		t.Fatalf("NewAnthropicClient: %v", err)
	}
	c.WithBaseURL(srv.URL)

	got, err := c.Complete(context.Background(), "you are a strategist", "go long ETH?", 256)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.Content != "yes, with stop loss" {
		t.Errorf("Content = %q", got.Content)
	}
	if got.InputTokens != 1500 || got.OutputTokens != 200 {
		t.Errorf("tokens = %d/%d", got.InputTokens, got.OutputTokens)
	}
	// 1500 in * $1/1M + 200 out * $5/1M = 0.0015 + 0.001 = 0.0025
	wantCost := 1500*1.0/1_000_000 + 200*5.0/1_000_000
	if math.Abs(got.CostUSD-wantCost) > 1e-9 {
		t.Errorf("CostUSD = %v, want %v", got.CostUSD, wantCost)
	}
	if got.CostUSD <= 0 {
		t.Error("CostUSD must be > 0 for a real completion")
	}
	if got.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model = %q", got.Model)
	}
}

func TestAnthropicAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens too high"}}`)
	}))
	defer srv.Close()
	c, err := NewAnthropicClient("test-key", "claude-haiku-4-5-20251001")
	if err != nil {
		t.Fatalf("NewAnthropicClient: %v", err)
	}
	c.WithBaseURL(srv.URL)
	_, err = c.Complete(context.Background(), "", "hi", 1)
	if err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("err = %v, want status 400", err)
	}
}

func TestAnthropicConcatenatesMultipleTextBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{
			"content": [
				{"type": "text", "text": "first "},
				{"type": "text", "text": "second"}
			],
			"model": "claude-sonnet-4-6",
			"usage": {"input_tokens": 1, "output_tokens": 2}
		}`)
	}))
	defer srv.Close()
	c, err := NewAnthropicClient("k", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("NewAnthropicClient: %v", err)
	}
	c.WithBaseURL(srv.URL)
	got, err := c.Complete(context.Background(), "", "hi", 10)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.Content != "first second" {
		t.Errorf("Content = %q", got.Content)
	}
}

func TestAnthropicRejectsUnknownModel(t *testing.T) {
	_, err := NewAnthropicClient("k", "claude-not-a-model")
	if err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("err = %v, want unknown model", err)
	}
}

func TestAnthropicRejectsZeroMaxTokens(t *testing.T) {
	c, err := NewAnthropicClient("k", "claude-haiku-4-5-20251001")
	if err != nil {
		t.Fatalf("NewAnthropicClient: %v", err)
	}
	_, err = c.Complete(context.Background(), "", "hi", 0)
	if err == nil || !strings.Contains(err.Error(), "maxTokens") {
		t.Fatalf("err = %v, want maxTokens", err)
	}
}

func TestAnthropicCalculateCost(t *testing.T) {
	c, err := NewAnthropicClient("k", "claude-opus-4-7")
	if err != nil {
		t.Fatalf("NewAnthropicClient: %v", err)
	}
	// 1000 in * $15/1M + 1000 out * $75/1M = 0.015 + 0.075 = 0.09
	got := c.CalculateCost(1000, 1000)
	if math.Abs(got-0.09) > 1e-9 {
		t.Errorf("CalculateCost = %v, want 0.09", got)
	}
}
