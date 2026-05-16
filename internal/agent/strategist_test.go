package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/veermshah/cambrian/internal/agent/tasks"
	"github.com/veermshah/cambrian/internal/db"
	"github.com/veermshah/cambrian/internal/llm"
)

type recStore struct {
	rows []db.StrategistDecision
	err  error
}

func (s *recStore) LogStrategistDecision(_ context.Context, d db.StrategistDecision) error {
	if s.err != nil {
		return s.err
	}
	s.rows = append(s.rows, d)
	return nil
}

func newTestStrategist(t *testing.T, resp string) (*Strategist, *recStore, *tasks.FakeTask, *llm.FakeLLMClient) {
	t.Helper()
	store := &recStore{}
	task := &tasks.FakeTask{Summary: map[string]interface{}{"positions": 1}}
	fake := llm.NewFakeLLMClient("fake-model").WithResponse(resp).WithTokenUsage(40, 12)
	s := &Strategist{
		AgentID:   "agent-1",
		AgentName: "alpha",
		Genome:    AgentGenome{Name: "alpha", TaskType: "fake", Chain: "solana"},
		LLM:       fake,
		Task:      task,
		Store:     store,
		Log:       NopLogger{},
	}
	return s, store, task, fake
}

func TestStrategist_HappyPathAppliesAdjustments(t *testing.T) {
	resp := `{"reasoning":"shift down","config_changes":{"target_apy_pct":12.5},"action_signal":"defensive"}`
	s, store, task, _ := newTestStrategist(t, resp)
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("decisions = %d, want 1", len(store.rows))
	}
	got := store.rows[0]
	if got.Reasoning != "shift down" {
		t.Errorf("reasoning = %q", got.Reasoning)
	}
	if len(got.ConfigChanges) == 0 {
		t.Errorf("config_changes empty, want populated")
	}
	if task.LastAdjustments == nil {
		t.Fatalf("ApplyAdjustments not called")
	}
	if v, ok := task.LastAdjustments["target_apy_pct"].(float64); !ok || v != 12.5 {
		t.Errorf("target_apy_pct = %v, want 12.5 float64", task.LastAdjustments["target_apy_pct"])
	}
}

func TestStrategist_MalformedJSONPersistsDecisionRow(t *testing.T) {
	s, store, task, _ := newTestStrategist(t, "not json at all")
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run returned err: %v", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("decisions = %d, want 1 even on parse failure", len(store.rows))
	}
	if !strings.Contains(store.rows[0].Reasoning, "parse_error") {
		t.Errorf("reasoning = %q, want parse_error prefix", store.rows[0].Reasoning)
	}
	if task.LastAdjustments != nil {
		t.Errorf("ApplyAdjustments must not be called on parse failure, got %v", task.LastAdjustments)
	}
}

func TestStrategist_UnknownActionSignalDowngradedToContinue(t *testing.T) {
	resp := `{"reasoning":"x","action_signal":"YOLO"}`
	s, store, _, _ := newTestStrategist(t, resp)
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("decisions = %d", len(store.rows))
	}
	if !strings.Contains(store.rows[0].Reasoning, "unknown_action_signal=YOLO") {
		t.Errorf("expected note in reasoning, got %q", store.rows[0].Reasoning)
	}
}

func TestStrategist_LLMErrorPropagates(t *testing.T) {
	s, store, _, fake := newTestStrategist(t, `{}`)
	fake.WithError(errors.New("rate limited"))
	err := s.Run(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(store.rows) != 0 {
		t.Errorf("decisions = %d, want 0 (LLM never returned)", len(store.rows))
	}
}

func TestStrategist_ApplyErrorRecordedInReasoning(t *testing.T) {
	resp := `{"reasoning":"adjust","config_changes":{"foo":1}}`
	s, store, task, _ := newTestStrategist(t, resp)
	task.AdjustErr = errors.New("out of range")
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("decisions = %d", len(store.rows))
	}
	if !strings.Contains(store.rows[0].Reasoning, "apply_error") {
		t.Errorf("expected apply_error in reasoning, got %q", store.rows[0].Reasoning)
	}
	if len(store.rows[0].ConfigChanges) != 0 {
		t.Errorf("config_changes should NOT persist when apply failed")
	}
}

func TestStrategist_JSONInsideMarkdownFences(t *testing.T) {
	resp := "Sure, here you go:\n```json\n{\"reasoning\":\"ok\",\"action_signal\":\"continue\"}\n```"
	s, store, _, _ := newTestStrategist(t, resp)
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(store.rows) != 1 || store.rows[0].Reasoning != "ok" {
		t.Errorf("expected fenced JSON to parse, got %+v", store.rows)
	}
}

func TestStrategist_RequiresClient(t *testing.T) {
	s := &Strategist{AgentID: "a", Task: &tasks.FakeTask{}, Store: &recStore{}}
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("expected error for missing LLM")
	}
}

func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`{"a":1}`, `{"a":1}`},
		{"prefix {\"a\":1} suffix", `{"a":1}`},
		{`{"a":"}"}`, `{"a":"}"}`},
		{`no braces here`, ``},
		{`{"a":{"b":2}}`, `{"a":{"b":2}}`},
		{`{"escaped":"\""}`, `{"escaped":"\""}`},
	}
	for _, c := range cases {
		got := extractJSONObject(c.in)
		if got != c.want {
			t.Errorf("extractJSONObject(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
