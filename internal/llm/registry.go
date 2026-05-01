package llm

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registry resolves a model name to a constructed LLMClient. Brain
// modules and the strategist call Get; provider selection is automatic
// from the model prefix (claude- → Anthropic, gpt- → OpenAI). The
// registry is package-global because the orchestrator passes around
// model names, not client instances.
//
// API keys are loaded once via Configure(); tests use SetClient to
// inject a FakeLLMClient under whatever model name they care about.

var (
	regMu       sync.RWMutex
	overrides   = map[string]LLMClient{}
	anthropicAPIKey string
	openaiAPIKey    string
)

// Configure binds API keys for the registry. Call once at startup
// (chunk 1's bootstrap path). Empty keys are allowed — Get will only
// fail when the missing-key model is requested.
func Configure(anthropicKey, openaiKey string) {
	regMu.Lock()
	defer regMu.Unlock()
	anthropicAPIKey = anthropicKey
	openaiAPIKey = openaiKey
}

// SetClient registers an explicit client for a model name. Tests use
// it to swap in FakeLLMClient; production code should not call it.
func SetClient(model string, c LLMClient) {
	regMu.Lock()
	defer regMu.Unlock()
	if c == nil {
		delete(overrides, model)
		return
	}
	overrides[model] = c
}

// ResetOverrides clears all SetClient registrations. Tests call this in
// teardown.
func ResetOverrides() {
	regMu.Lock()
	defer regMu.Unlock()
	overrides = map[string]LLMClient{}
}

// Get returns an LLMClient for the given model. Override > rate-table
// lookup > error. Unknown models return an error rather than a
// best-effort guess: the strategist tracks model identity in its
// fitness signal, so silent fallbacks would corrupt evolution.
func Get(model string) (LLMClient, error) {
	regMu.RLock()
	defer regMu.RUnlock()
	if c, ok := overrides[model]; ok {
		return c, nil
	}
	if _, ok := modelRates[model]; !ok {
		return nil, fmt.Errorf("llm: unknown model %q (registered: %v)", model, listModelsLocked())
	}
	switch {
	case strings.HasPrefix(model, "claude-"):
		if anthropicAPIKey == "" {
			return nil, errors.New("llm: anthropic api key not configured")
		}
		return NewAnthropicClient(anthropicAPIKey, model)
	case strings.HasPrefix(model, "gpt-"):
		if openaiAPIKey == "" {
			return nil, errors.New("llm: openai api key not configured")
		}
		return NewOpenAIClient(openaiAPIKey, model)
	default:
		return nil, fmt.Errorf("llm: model %q has no provider mapping", model)
	}
}

// Models returns every registered model, sorted. Used by the strategist
// for bandit arm enumeration.
func Models() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	return listModelsLocked()
}

func listModelsLocked() []string {
	seen := map[string]struct{}{}
	for k := range modelRates {
		seen[k] = struct{}{}
	}
	for k := range overrides {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
