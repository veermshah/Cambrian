package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// OpenAI Chat Completions API (https://platform.openai.com/docs/api-reference/chat).
// We only use gpt-4o and gpt-4o-mini; the latter is the strategist's
// default for "cheap-and-fast" thinking, the former for offspring
// proposal generation.

const openaiDefaultBaseURL = "https://api.openai.com/v1"

// OpenAIClient implements LLMClient against Chat Completions.
type OpenAIClient struct {
	http    *http.Client
	baseURL string
	apiKey  string
	model   string
}

var _ LLMClient = (*OpenAIClient)(nil)

func NewOpenAIClient(apiKey, model string) (*OpenAIClient, error) {
	if apiKey == "" {
		return nil, errors.New("llm/openai: api key required")
	}
	if _, ok := modelRates[model]; !ok {
		return nil, fmt.Errorf("llm/openai: unknown model %q (no rate)", model)
	}
	return &OpenAIClient{
		http:    &http.Client{Timeout: llmHTTPTimeout},
		baseURL: openaiDefaultBaseURL,
		apiKey:  apiKey,
		model:   model,
	}, nil
}

func (c *OpenAIClient) WithBaseURL(url string) *OpenAIClient {
	c.baseURL = url
	return c
}

type openaiChatRequest struct {
	Model     string          `json:"model"`
	Messages  []openaiMessage `json:"messages"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiChatResponse struct {
	Choices []struct {
		Message openaiMessage `json:"message"`
	} `json:"choices"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *OpenAIClient) Complete(ctx context.Context, system, user string, maxTokens int) (*LLMResponse, error) {
	if maxTokens <= 0 {
		return nil, errors.New("llm/openai: maxTokens must be > 0")
	}
	msgs := make([]openaiMessage, 0, 2)
	if system != "" {
		msgs = append(msgs, openaiMessage{Role: "system", Content: system})
	}
	msgs = append(msgs, openaiMessage{Role: "user", Content: user})
	body, err := json.Marshal(openaiChatRequest{
		Model:     c.model,
		Messages:  msgs,
		MaxTokens: maxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("llm/openai: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm/openai: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm/openai: do: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("llm/openai: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("llm/openai: status %d: %s", resp.StatusCode, truncateForLog(string(respBody), 256))
	}
	var parsed openaiChatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("llm/openai: decode: %w", err)
	}
	if parsed.Error.Message != "" {
		return nil, fmt.Errorf("llm/openai: api error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return nil, errors.New("llm/openai: empty choices")
	}
	return &LLMResponse{
		Content:      parsed.Choices[0].Message.Content,
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
		CostUSD:      computeCost(c.model, parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens),
		Model:        c.model,
	}, nil
}

func (c *OpenAIClient) CalculateCost(inputTokens, outputTokens int) float64 {
	return computeCost(c.model, inputTokens, outputTokens)
}
