package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Anthropic Messages API (https://docs.anthropic.com/en/api/messages).
// Single-shot Complete only — streaming is out of scope per the prompt
// pack (the strategist consumes whole responses, not deltas).
//
// Auth header is `x-api-key`, plus `anthropic-version: 2023-06-01`.
// Rate-table model IDs are listed in rates.go.

const (
	anthropicDefaultBaseURL = "https://api.anthropic.com/v1"
	anthropicVersion        = "2023-06-01"
	llmHTTPTimeout          = 60 * time.Second
)

// AnthropicClient implements LLMClient against the Messages API.
// Construct via NewAnthropicClient; the registry in registry.go is the
// usual call site.
type AnthropicClient struct {
	http    *http.Client
	baseURL string
	apiKey  string
	model   string
}

var _ LLMClient = (*AnthropicClient)(nil)

// NewAnthropicClient binds an API key + model to a fresh HTTP client.
// Returns an error if the model is not in the rate table — we fail
// fast rather than silently emitting CostUSD=0.
func NewAnthropicClient(apiKey, model string) (*AnthropicClient, error) {
	if apiKey == "" {
		return nil, errors.New("llm/anthropic: api key required")
	}
	if _, ok := modelRates[model]; !ok {
		return nil, fmt.Errorf("llm/anthropic: unknown model %q (no rate)", model)
	}
	return &AnthropicClient{
		http:    &http.Client{Timeout: llmHTTPTimeout},
		baseURL: anthropicDefaultBaseURL,
		apiKey:  apiKey,
		model:   model,
	}, nil
}

// WithBaseURL overrides the API endpoint. Tests use it to point at
// httptest.Server; production code should not call it.
func (c *AnthropicClient) WithBaseURL(url string) *AnthropicClient {
	c.baseURL = url
	return c
}

type anthropicMsgRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicMsgResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Model string `json:"model"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *AnthropicClient) Complete(ctx context.Context, system, user string, maxTokens int) (*LLMResponse, error) {
	if maxTokens <= 0 {
		return nil, errors.New("llm/anthropic: maxTokens must be > 0")
	}
	body, err := json.Marshal(anthropicMsgRequest{
		Model:     c.model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  []anthropicMessage{{Role: "user", Content: user}},
	})
	if err != nil {
		return nil, fmt.Errorf("llm/anthropic: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm/anthropic: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm/anthropic: do: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("llm/anthropic: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("llm/anthropic: status %d: %s", resp.StatusCode, truncateForLog(string(respBody), 256))
	}
	var parsed anthropicMsgResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("llm/anthropic: decode: %w", err)
	}
	if parsed.Type == "error" || parsed.Error.Message != "" {
		return nil, fmt.Errorf("llm/anthropic: api error: %s", parsed.Error.Message)
	}
	// Concatenate text blocks; the Messages API returns multiple content
	// parts when the model emits tool-use deltas, but for plain Complete
	// we only ever see "text" blocks.
	var content string
	for _, part := range parsed.Content {
		if part.Type == "text" {
			content += part.Text
		}
	}
	return &LLMResponse{
		Content:      content,
		InputTokens:  parsed.Usage.InputTokens,
		OutputTokens: parsed.Usage.OutputTokens,
		CostUSD:      computeCost(c.model, parsed.Usage.InputTokens, parsed.Usage.OutputTokens),
		Model:        c.model,
	}, nil
}

func (c *AnthropicClient) CalculateCost(inputTokens, outputTokens int) float64 {
	return computeCost(c.model, inputTokens, outputTokens)
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
