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

func TestOpenAICompleteSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		var req openaiChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if req.Model != "gpt-4o-mini" {
			t.Errorf("model = %q", req.Model)
		}
		if len(req.Messages) != 2 {
			t.Errorf("expected system+user, got %d messages", len(req.Messages))
		}
		if req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
			t.Errorf("roles = %s,%s", req.Messages[0].Role, req.Messages[1].Role)
		}
		_, _ = io.WriteString(w, `{
			"choices": [{"message": {"role": "assistant", "content": "hold steady"}}],
			"model": "gpt-4o-mini",
			"usage": {"prompt_tokens": 800, "completion_tokens": 50}
		}`)
	}))
	defer srv.Close()

	c, err := NewOpenAIClient("test-key", "gpt-4o-mini")
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}
	c.WithBaseURL(srv.URL)

	got, err := c.Complete(context.Background(), "you are a trader", "ETH up?", 64)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.Content != "hold steady" {
		t.Errorf("Content = %q", got.Content)
	}
	if got.InputTokens != 800 || got.OutputTokens != 50 {
		t.Errorf("tokens = %d/%d", got.InputTokens, got.OutputTokens)
	}
	// 800 * 0.15/1M + 50 * 0.60/1M
	wantCost := 800*0.15/1_000_000 + 50*0.60/1_000_000
	if math.Abs(got.CostUSD-wantCost) > 1e-9 {
		t.Errorf("CostUSD = %v, want %v", got.CostUSD, wantCost)
	}
	if got.CostUSD <= 0 {
		t.Error("CostUSD must be > 0 for a real completion")
	}
}

func TestOpenAIOmitsSystemWhenEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req openaiChatRequest
		_ = json.Unmarshal(body, &req)
		if len(req.Messages) != 1 {
			t.Errorf("len(messages) = %d, want 1 (no system row)", len(req.Messages))
		}
		if req.Messages[0].Role != "user" {
			t.Errorf("role = %q, want user", req.Messages[0].Role)
		}
		_, _ = io.WriteString(w, `{
			"choices":[{"message":{"role":"assistant","content":"ok"}}],
			"usage":{"prompt_tokens":1,"completion_tokens":1}
		}`)
	}))
	defer srv.Close()
	c, err := NewOpenAIClient("k", "gpt-4o")
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}
	c.WithBaseURL(srv.URL)
	if _, err := c.Complete(context.Background(), "", "hi", 5); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func TestOpenAIAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer srv.Close()
	c, err := NewOpenAIClient("k", "gpt-4o")
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}
	c.WithBaseURL(srv.URL)
	_, err = c.Complete(context.Background(), "", "hi", 1)
	if err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("err = %v, want status 401", err)
	}
}

func TestOpenAIRejectsUnknownModel(t *testing.T) {
	_, err := NewOpenAIClient("k", "gpt-not-a-model")
	if err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIEmptyChoicesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":0}}`)
	}))
	defer srv.Close()
	c, err := NewOpenAIClient("k", "gpt-4o")
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}
	c.WithBaseURL(srv.URL)
	_, err = c.Complete(context.Background(), "", "hi", 1)
	if err == nil || !strings.Contains(err.Error(), "empty choices") {
		t.Fatalf("err = %v", err)
	}
}
