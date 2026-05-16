package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/veermshah/cambrian/internal/agent/tasks"
	"github.com/veermshah/cambrian/internal/db"
	"github.com/veermshah/cambrian/internal/llm"
)

// StrategistResponse is the JSON shape the strategist LLM must return.
// Spec lines 178–184 describe the contract verbatim. Every field is
// optional except `action_signal` — the strategist is allowed to
// produce nothing but a signal on quiet ticks.
//
// Decoding is defensive: unknown fields are ignored, out-of-range
// numerics in ConfigChanges are clamped by the Task (the strategist
// hands them straight to Task.ApplyAdjustments without inspection
// here — every Task already owns its own clamp envelope).
type StrategistResponse struct {
	Reasoning         string                 `json:"reasoning"`
	ConfigChanges     map[string]interface{} `json:"config_changes"`
	ActionSignal      string                 `json:"action_signal"`
	OffspringProposal json.RawMessage        `json:"offspring_proposal,omitempty"`
	IntelBroadcasts   json.RawMessage        `json:"intel_broadcasts,omitempty"`
	NewLearnedRule    json.RawMessage        `json:"new_learned_rule,omitempty"`
}

// validActionSignals is the closed set the spec defines (line 181).
// Anything else is treated as "continue" with the offending value
// echoed back in the decision row's reasoning.
var validActionSignals = map[string]struct{}{
	"continue":  {},
	"pause":     {},
	"aggressive": {},
	"defensive": {},
}

// StrategistInput is what the strategist hands to the LLM via
// json-encoded user prompt. The system prompt is the agent's persisted
// strategist_prompt; this struct is the payload that varies per call.
type StrategistInput struct {
	AgentName    string                 `json:"agent_name"`
	TaskType     string                 `json:"task_type"`
	Chain        string                 `json:"chain"`
	State        map[string]interface{} `json:"task_state"`
	Bandit       map[string]interface{} `json:"bandit"`
	Postmortems  []string               `json:"recent_postmortems,omitempty"`
	IntelSummary []string               `json:"recent_intel,omitempty"`
	LearnedRules []LearnedRule          `json:"learned_rules,omitempty"`
}

// DecisionStore is the small DB surface the strategist needs. The
// production implementation is *db.Queries; tests use an in-memory fake.
type DecisionStore interface {
	LogStrategistDecision(ctx context.Context, d db.StrategistDecision) error
}

// Logger is the structured-log surface the strategist uses for
// observability. zap.Logger satisfies it via SugaredLogger; tests pass
// a no-op.
type Logger interface {
	Warnw(msg string, kv ...interface{})
	Errorw(msg string, kv ...interface{})
	Infow(msg string, kv ...interface{})
}

// NopLogger discards every log call. Default for tests that don't
// care about output.
type NopLogger struct{}

func (NopLogger) Warnw(string, ...interface{})  {}
func (NopLogger) Errorw(string, ...interface{}) {}
func (NopLogger) Infow(string, ...interface{})  {}

// Strategist owns the slow-loop LLM cycle for a single agent. One Run
// call → one LLM completion → one strategist_decisions row, win or
// lose. Defensive validation never lets a bad LLM response corrupt the
// task config.
type Strategist struct {
	AgentID         string
	AgentName       string
	Genome          AgentGenome
	LLM             llm.LLMClient
	Task            tasks.Task
	Bandit          *TickBandit
	Store           DecisionStore
	Log             Logger
	MaxOutputTokens int
}

// Run executes one strategist cycle: gather state, prompt LLM, parse
// defensively, apply clamped adjustments, persist the decision row.
//
// Failure modes:
//   - LLM call errors          → log error, no decision row, return error.
//   - JSON parse fails         → log warn, write decision row with output_raw + empty config_changes, return nil.
//   - Out-of-range value       → Task.ApplyAdjustments clamps (we don't preview here).
//   - Task.ApplyAdjustments err → log warn, decision row written with reasoning=err.
//
// Returns an error only on LLM transport failure; everything else is
// recorded in the decision row and treated as a no-op.
func (s *Strategist) Run(ctx context.Context) error {
	if s == nil {
		return errors.New("strategist: nil receiver")
	}
	if s.LLM == nil {
		return errors.New("strategist: nil LLM client")
	}
	if s.Task == nil {
		return errors.New("strategist: nil task")
	}
	if s.Store == nil {
		return errors.New("strategist: nil store")
	}
	if s.Log == nil {
		s.Log = NopLogger{}
	}
	maxTokens := s.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	input, err := s.buildInput(ctx)
	if err != nil {
		return fmt.Errorf("strategist: build input: %w", err)
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("strategist: marshal input: %w", err)
	}

	system := s.Genome.StrategistPrompt
	if system == "" {
		system = defaultStrategistSystem
	}

	resp, err := s.LLM.Complete(ctx, system, string(inputJSON), maxTokens)
	if err != nil {
		s.Log.Errorw("strategist_llm_call_failed", "agent_id", s.AgentID, "error", err)
		return fmt.Errorf("strategist: llm.Complete: %w", err)
	}

	parsed, parseErr := parseStrategistResponse(resp.Content)
	decision := db.StrategistDecision{
		AgentID:      s.AgentID,
		InputSummary: inputJSON,
		OutputRaw:    resp.Content,
		ModelUsed:    resp.Model,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		CostUSD:      resp.CostUSD,
	}

	if parseErr != nil {
		s.Log.Warnw("strategist_parse_failed",
			"agent_id", s.AgentID,
			"error", parseErr,
			"output_raw_len", len(resp.Content),
		)
		decision.Reasoning = "parse_error: " + parseErr.Error()
		// Persist the decision row even on parse failure so the
		// operator can diagnose model drift offline. No config
		// changes applied.
		if err := s.Store.LogStrategistDecision(ctx, decision); err != nil {
			s.Log.Errorw("strategist_persist_failed", "agent_id", s.AgentID, "error", err)
		}
		return nil
	}

	decision.Reasoning = parsed.Reasoning

	// Validate ActionSignal — out-of-set is downgraded to "continue"
	// and noted in reasoning so the next epoch evaluation can see it.
	if parsed.ActionSignal != "" {
		if _, ok := validActionSignals[parsed.ActionSignal]; !ok {
			s.Log.Warnw("strategist_unknown_action_signal",
				"agent_id", s.AgentID, "signal", parsed.ActionSignal)
			decision.Reasoning = appendNote(decision.Reasoning,
				"unknown_action_signal="+parsed.ActionSignal)
			parsed.ActionSignal = "continue"
		}
	}

	// Apply config changes. Task clamps; we surface any rejection.
	if len(parsed.ConfigChanges) > 0 {
		if err := s.Task.ApplyAdjustments(parsed.ConfigChanges); err != nil {
			s.Log.Warnw("strategist_apply_adjustments_failed",
				"agent_id", s.AgentID, "error", err)
			decision.Reasoning = appendNote(decision.Reasoning,
				"apply_error: "+err.Error())
			// Don't persist the config_changes column when application
			// failed — operator should see the raw output and the
			// reasoning, not a column that misrepresents what landed.
		} else {
			b, _ := json.Marshal(parsed.ConfigChanges)
			decision.ConfigChanges = b
		}
	}

	if len(parsed.IntelBroadcasts) > 0 {
		decision.IntelBroadcasts = parsed.IntelBroadcasts
	}
	if len(parsed.OffspringProposal) > 0 {
		decision.OffspringProposalSubmitted = true
	}
	if len(parsed.NewLearnedRule) > 0 {
		decision.NewLearnedRule = parsed.NewLearnedRule
	}

	if err := s.Store.LogStrategistDecision(ctx, decision); err != nil {
		s.Log.Errorw("strategist_persist_failed", "agent_id", s.AgentID, "error", err)
		return fmt.Errorf("strategist: persist decision: %w", err)
	}
	return nil
}

// buildInput assembles the StrategistInput payload. State comes from
// Task.GetStateSummary; bandit data from the optional TickBandit. The
// strategist prompt builder (chunk 19) will swap in real postmortems +
// intel later — for now those slices come from the genome's persistent
// fields.
func (s *Strategist) buildInput(ctx context.Context) (*StrategistInput, error) {
	state, err := s.Task.GetStateSummary(ctx)
	if err != nil {
		return nil, fmt.Errorf("task.GetStateSummary: %w", err)
	}
	in := &StrategistInput{
		AgentName:    s.AgentName,
		TaskType:     s.Genome.TaskType,
		Chain:        s.Genome.Chain,
		State:        state,
		LearnedRules: s.Genome.LearnedRules,
	}
	if s.Bandit != nil {
		raw, err := s.Bandit.SaveState()
		if err == nil && len(raw) > 0 {
			var m map[string]interface{}
			if json.Unmarshal(raw, &m) == nil {
				in.Bandit = m
			}
		}
	}
	return in, nil
}

// parseStrategistResponse pulls a JSON object out of the LLM response.
// Models often wrap JSON in markdown fences or chatter; this finds the
// first `{ … }` span and decodes that. Returns a non-nil error if
// nothing decodable was found.
func parseStrategistResponse(s string) (*StrategistResponse, error) {
	body := extractJSONObject(s)
	if body == "" {
		return nil, errors.New("no JSON object found in response")
	}
	var out StrategistResponse
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	// Re-marshal numeric fields in ConfigChanges from json.Number to
	// float64 so downstream Task.ApplyAdjustments (which type-asserts
	// to float64 / int) is happy.
	if out.ConfigChanges != nil {
		out.ConfigChanges = normalizeNumbers(out.ConfigChanges)
	}
	return &out, nil
}

// extractJSONObject finds the first balanced `{ ... }` span in s. If
// the LLM returns a clean JSON object, this is the whole string;
// otherwise it strips markdown fences and prose.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if inString {
			switch c {
			case '\\':
				escape = true
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func normalizeNumbers(m map[string]interface{}) map[string]interface{} {
	for k, v := range m {
		if n, ok := v.(json.Number); ok {
			if f, err := n.Float64(); err == nil {
				m[k] = f
			}
		}
	}
	return m
}

func appendNote(prev, note string) string {
	if prev == "" {
		return note
	}
	return prev + " | " + note
}

const defaultStrategistSystem = `You are a strategist for a DeFi agent in an evolutionary swarm.

You receive the agent's current task state, bandit performance data,
and learned rules. You return a JSON object with these fields:

  - reasoning: brief diagnosis (1-3 sentences)
  - config_changes: object of config keys to adjust (empty if no change)
  - action_signal: one of "continue", "pause", "aggressive", "defensive"
  - offspring_proposal: optional object
  - intel_broadcasts: optional array
  - new_learned_rule: optional object

Constraints:
  - Make exactly one adjustment per call (or none).
  - Output only the JSON object, no surrounding prose.
  - Never adjust keys you don't recognize.
`

// strategistInputDeadline is the soft time budget for the entire
// strategist cycle (build input + LLM call + parse + apply + persist).
// Spec lines 167–169 want the strategist to be reflective, not
// complex; capping at 30s keeps the slow-loop ticker honest.
const strategistInputDeadline = 30 * time.Second
