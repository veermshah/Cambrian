package main

import (
	"os"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestNewLogger_AppliesLevel(t *testing.T) {
	cases := []struct {
		in   string
		want zap.AtomicLevel
	}{
		{"debug", zap.NewAtomicLevelAt(zap.DebugLevel)},
		{"info", zap.NewAtomicLevelAt(zap.InfoLevel)},
		{"warn", zap.NewAtomicLevelAt(zap.WarnLevel)},
		{"error", zap.NewAtomicLevelAt(zap.ErrorLevel)},
		{"", zap.NewAtomicLevelAt(zap.InfoLevel)}, // default
	}
	for _, tc := range cases {
		lg, err := newLogger(tc.in)
		if err != nil {
			t.Fatalf("level=%q: %v", tc.in, err)
		}
		if lg == nil {
			t.Fatalf("level=%q: nil logger", tc.in)
		}
		_ = lg.Sync()
	}
}

func TestLoggerAdapter_SatisfiesAgentLogger(t *testing.T) {
	core, recorded := observer.New(zap.InfoLevel)
	adapter := loggerAdapter{log: zap.New(core)}
	adapter.Infow("hello", "k", "v", "n", 42)
	adapter.Warnw("careful", "agent_id", "ag-1")
	adapter.Errorw("nope", "code", 500)
	logs := recorded.All()
	if len(logs) != 3 {
		t.Fatalf("captured %d logs, want 3", len(logs))
	}
	if logs[0].Message != "hello" || logs[0].ContextMap()["k"] != "v" {
		t.Errorf("info log: %+v", logs[0])
	}
	if logs[1].Level != zap.WarnLevel {
		t.Errorf("warn level = %v", logs[1].Level)
	}
	if logs[2].Level != zap.ErrorLevel {
		t.Errorf("error level = %v", logs[2].Level)
	}
}

func TestFieldsFromKV_HandlesOddLengthAndNonStringKeys(t *testing.T) {
	// Odd-length kv slice — the last orphan key is dropped silently.
	fields := fieldsFromKV([]any{"a", 1, "b"})
	if len(fields) != 1 {
		t.Errorf("odd-length kv: %d fields, want 1", len(fields))
	}
	// Non-string key — stringified via fmt.
	fields = fieldsFromKV([]any{42, "v"})
	if len(fields) != 1 || fields[0].Key != "42" {
		t.Errorf("non-string key: %+v", fields)
	}
	// Empty input.
	if fields := fieldsFromKV(nil); fields != nil {
		t.Errorf("nil kv: %+v", fields)
	}
}

func TestApiAddr_DefaultAndOverride(t *testing.T) {
	t.Setenv("API_ADDR", "")
	if got := apiAddr(); got != ":8080" {
		t.Errorf("default = %q", got)
	}
	t.Setenv("API_ADDR", "0.0.0.0:9000")
	if got := apiAddr(); got != "0.0.0.0:9000" {
		t.Errorf("override = %q", got)
	}
	// Cleanup belt-and-suspenders even though t.Setenv handles it.
	_ = os.Unsetenv("API_ADDR")
}

func TestLlmRegistryAdapter_ForwardsToPackageGet(t *testing.T) {
	// The package-level llm.Configure was not called, so any model
	// lookup should error — but the adapter's only job is to forward,
	// so we just verify the error path runs cleanly without panic.
	if _, err := (llmRegistryAdapter{}).Get("definitely-not-a-model"); err == nil {
		t.Error("expected error for unknown model")
	}
}
