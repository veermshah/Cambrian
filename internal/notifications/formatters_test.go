package notifications

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFormat_CircuitBreaker(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"reason": "market_crash",
		"at":     "2026-06-09T12:00:00Z",
	})
	got := Format(Event{Type: EventCircuitBreaker, Payload: payload})
	for _, want := range []string{"Circuit breaker tripped", "market_crash", "2026-06-09T12:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
}

func TestFormat_AgentKilledAndSpawned(t *testing.T) {
	killed, _ := json.Marshal(map[string]any{
		"agent_id":   "abc-123",
		"name":       "scout-7",
		"reason":     "max_drawdown_exceeded",
		"node_class": "funded",
	})
	got := Format(Event{Type: EventAgentKilled, Payload: killed})
	for _, want := range []string{"killed", "abc-123", "scout-7", "funded", "max_drawdown_exceeded"} {
		if !strings.Contains(got, want) {
			t.Errorf("kill: want %q in:\n%s", want, got)
		}
	}
	spawned, _ := json.Marshal(map[string]any{"agent_id": "xyz", "name": "hunter-1"})
	gotS := Format(Event{Type: EventAgentSpawned, Payload: spawned})
	for _, want := range []string{"spawned", "xyz", "hunter-1"} {
		if !strings.Contains(gotS, want) {
			t.Errorf("spawn: want %q in:\n%s", want, gotS)
		}
	}
}

func TestFormat_EpochCompletedRawPayload(t *testing.T) {
	// Chunk 21 currently publishes just the epoch ID as raw bytes.
	got := Format(Event{Type: EventEpochCompleted, Payload: []byte("epoch-42")})
	if !strings.Contains(got, "epoch-42") {
		t.Errorf("raw ID lost: %s", got)
	}
}

func TestFormat_EpochCompletedJSONPayload(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"epoch_id":         "42",
		"realized_pnl_usd": 1234.56,
		"promotions":       float64(3),
	})
	got := Format(Event{Type: EventEpochCompleted, Payload: payload})
	for _, want := range []string{"42", "1234.56", "Promotions: 3"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
}

func TestFormat_BudgetWarning(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"agent_id":  "a-1",
		"scope":     "monthly",
		"used_pct":  0.83,
		"limit_usd": 500.0,
	})
	got := Format(Event{Type: EventBudgetWarning, Payload: payload})
	for _, want := range []string{"Budget warning", "a-1", "monthly", "83.0%", "500.00"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
}

func TestFormat_TreasuryLow(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"reserve_usd":   123.45,
		"threshold_usd": 200.0,
		"reserve_pct":   0.29,
	})
	got := Format(Event{Type: EventTreasuryLow, Payload: payload})
	for _, want := range []string{"Treasury", "123.45", "200.00", "29%"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
}

func TestFormat_DailyDigest(t *testing.T) {
	d := DigestSummary{
		Date:              "2026-06-09",
		TotalPnLUSD:       250.5,
		BestAgent:         AgentScore{AgentID: "a", Name: "winner", PnLUSD: 100},
		WorstAgent:        AgentScore{AgentID: "b", Name: "loser", PnLUSD: -50},
		PromotionsPending: 2,
		AgentsActive:      10,
		NewAgents:         3,
		AgentsKilled:      1,
	}
	got := FormatDigest(d)
	for _, want := range []string{"2026-06-09", "250.50", "winner", "loser", "Promotions pending: 2", "Active agents: 10"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
}

func TestFormat_UnknownTypeSurfacesPayload(t *testing.T) {
	got := Format(Event{Type: "mystery", Payload: []byte(`{"foo":"bar"}`), At: time.Now()})
	if !strings.Contains(got, "mystery") || !strings.Contains(got, "bar") {
		t.Errorf("unknown event should surface type+payload: %s", got)
	}
}
