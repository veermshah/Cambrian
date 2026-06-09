package notifications

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Event-type constants — match the redis channel suffixes Chunk 21
// publishes plus the synthetic types this package adds. The notifier
// uses these both as the throttle key and as the formatter switch.
const (
	EventCircuitBreaker = "circuit_breaker"
	EventAgentKilled    = "agent_killed"
	EventAgentSpawned   = "agent_spawned"
	EventEpochCompleted = "epoch_completed"
	EventBudgetWarning  = "budget_warning"
	EventTreasuryLow    = "treasury_low"
	EventDailyDigest    = "daily_digest"
)

// Event is the canonical envelope passed to Format. Type drives the
// formatter dispatch; Payload is the raw bytes the publisher emitted
// on Redis. Format treats Payload as opaque JSON when it parses,
// otherwise falls back to a plain-text path.
type Event struct {
	Type    string
	Payload []byte
	At      time.Time
}

// DigestSummary is the aggregated payload for the daily digest.
// Telegram doesn't render rich tables well, so the formatter renders
// this to a compact markdown list. The orchestrator builds one per
// scheduled tick and hands it to the notifier through DailyDigest.
type DigestSummary struct {
	Date              string
	TotalPnLUSD       float64
	BestAgent         AgentScore
	WorstAgent        AgentScore
	PromotionsPending int
	AgentsActive      int
	AgentsKilled      int
	NewAgents         int
}

// AgentScore is one row inside DigestSummary.
type AgentScore struct {
	AgentID string
	Name    string
	PnLUSD  float64
}

// Format dispatches on Event.Type and returns the Telegram message
// body. The body is plaintext + light markdown — sendMessage parse mode
// is "Markdown", so leading dashes and asterisks render as bullets and
// emphasis.
func Format(e Event) string {
	parsed := parsePayload(e.Payload)
	switch e.Type {
	case EventCircuitBreaker:
		return formatCircuitBreaker(parsed)
	case EventAgentKilled:
		return formatAgentLifecycle("killed", parsed)
	case EventAgentSpawned:
		return formatAgentLifecycle("spawned", parsed)
	case EventEpochCompleted:
		return formatEpochCompleted(parsed, e.Payload)
	case EventBudgetWarning:
		return formatBudgetWarning(parsed)
	case EventTreasuryLow:
		return formatTreasuryLow(parsed)
	case EventDailyDigest:
		return formatDailyDigestPayload(e.Payload)
	default:
		// Unknown event type — surface the raw payload so an operator
		// can spot a producer/consumer mismatch without it silently
		// vanishing.
		return fmt.Sprintf("*Event* `%s`\n```\n%s\n```", e.Type, truncate(string(e.Payload), 500))
	}
}

// FormatDigest is the convenience for the daily-digest scheduler: it
// builds the Event around a DigestSummary so the notifier can pipe it
// through the same Format path as the live events.
func FormatDigest(d DigestSummary) string {
	var b strings.Builder
	b.WriteString("*Daily digest* — ")
	b.WriteString(d.Date)
	b.WriteString("\n")
	fmt.Fprintf(&b, "Total P&L: *$%.2f*\n", d.TotalPnLUSD)
	if d.BestAgent.AgentID != "" {
		fmt.Fprintf(&b, "Best: `%s` (%s)  +$%.2f\n", d.BestAgent.Name, d.BestAgent.AgentID, d.BestAgent.PnLUSD)
	}
	if d.WorstAgent.AgentID != "" {
		fmt.Fprintf(&b, "Worst: `%s` (%s)  $%.2f\n", d.WorstAgent.Name, d.WorstAgent.AgentID, d.WorstAgent.PnLUSD)
	}
	fmt.Fprintf(&b, "Active agents: %d  •  Spawned: %d  •  Killed: %d\n", d.AgentsActive, d.NewAgents, d.AgentsKilled)
	if d.PromotionsPending > 0 {
		fmt.Fprintf(&b, "Promotions pending: %d\n", d.PromotionsPending)
	}
	return b.String()
}

func parsePayload(raw []byte) map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func formatCircuitBreaker(p map[string]interface{}) string {
	reason := strField(p, "reason", "manual_override")
	at := strField(p, "at", "")
	out := fmt.Sprintf("🚨 *Circuit breaker tripped*\nReason: `%s`", reason)
	if at != "" {
		out += "\nAt: " + at
	}
	if state := strField(p, "state", ""); state != "" {
		out += "\nState: " + state
	}
	return out
}

func formatAgentLifecycle(action string, p map[string]interface{}) string {
	id := strField(p, "agent_id", "?")
	name := strField(p, "name", "")
	reason := strField(p, "reason", "")
	class := strField(p, "node_class", "")
	emoji := "🪦"
	if action == "spawned" {
		emoji = "🌱"
	}
	out := fmt.Sprintf("%s *Agent %s*: `%s`", emoji, action, id)
	if name != "" {
		out += " (" + name + ")"
	}
	if class != "" {
		out += " — " + class
	}
	if reason != "" {
		out += "\nReason: " + reason
	}
	return out
}

func formatEpochCompleted(p map[string]interface{}, raw []byte) string {
	// Chunk 21 currently publishes just the epochID as raw bytes — be
	// permissive: if the payload doesn't parse as JSON, treat it as the
	// ID string.
	if p == nil {
		return "✅ *Epoch completed*: `" + strings.TrimSpace(string(raw)) + "`"
	}
	id := strField(p, "epoch_id", "")
	out := "✅ *Epoch completed*"
	if id != "" {
		out += ": `" + id + "`"
	}
	if v, ok := p["realized_pnl_usd"].(float64); ok {
		out += fmt.Sprintf("\nRealized P&L: $%.2f", v)
	}
	if v, ok := p["promotions"].(float64); ok && v > 0 {
		out += fmt.Sprintf("\nPromotions: %d", int(v))
	}
	return out
}

func formatBudgetWarning(p map[string]interface{}) string {
	id := strField(p, "agent_id", "")
	scope := strField(p, "scope", "monthly")
	pctUsed := floatField(p, "used_pct", 0)
	limit := floatField(p, "limit_usd", 0)
	out := "⚠️ *Budget warning*"
	if id != "" {
		out += " — agent `" + id + "`"
	}
	out += fmt.Sprintf("\nScope: %s  •  Used: %.1f%%", scope, pctUsed*100)
	if limit > 0 {
		out += fmt.Sprintf("  •  Limit: $%.2f", limit)
	}
	return out
}

func formatTreasuryLow(p map[string]interface{}) string {
	reserveUSD := floatField(p, "reserve_usd", 0)
	thresholdUSD := floatField(p, "threshold_usd", 0)
	pct := floatField(p, "reserve_pct", 0)
	out := "💧 *Treasury below reserve*"
	out += fmt.Sprintf("\nReserve: $%.2f", reserveUSD)
	if thresholdUSD > 0 {
		out += fmt.Sprintf("  •  Threshold: $%.2f", thresholdUSD)
	}
	if pct > 0 {
		out += fmt.Sprintf("  •  At: %.0f%%", pct*100)
	}
	return out
}

func formatDailyDigestPayload(raw []byte) string {
	var d DigestSummary
	if err := json.Unmarshal(raw, &d); err != nil || d.Date == "" {
		return "🗓 *Daily digest*\n```\n" + truncate(string(raw), 500) + "\n```"
	}
	return FormatDigest(d)
}

func strField(p map[string]interface{}, key, fallback string) string {
	if p == nil {
		return fallback
	}
	v, ok := p[key].(string)
	if !ok || v == "" {
		return fallback
	}
	return v
}

func floatField(p map[string]interface{}, key string, fallback float64) float64 {
	if p == nil {
		return fallback
	}
	switch v := p[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return fallback
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// EventTypesAlphabetical is a stable order for tests and any UI that
// wants a deterministic display order.
func EventTypesAlphabetical() []string {
	out := []string{
		EventAgentKilled, EventAgentSpawned, EventBudgetWarning,
		EventCircuitBreaker, EventDailyDigest, EventEpochCompleted,
		EventTreasuryLow,
	}
	sort.Strings(out)
	return out
}
