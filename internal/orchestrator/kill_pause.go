package orchestrator

import (
	"time"
)

// LifecycleActionKind is the closed set of state transitions DeterministicPolicy
// can emit. Matches the lifecycle channels in runtime.LifecycleManager
// (kill / pause / resume / promote / demote).
type LifecycleActionKind string

const (
	ActionKill   LifecycleActionKind = "kill"
	ActionPause  LifecycleActionKind = "pause"
	ActionResume LifecycleActionKind = "resume"
)

// LifecycleAction is one decision DeterministicPolicy emits for the
// parent orchestrator to execute. Reason is persisted to lifecycle log
// rows + Redis events so an operator can audit why an agent died.
type LifecycleAction struct {
	AgentID string
	Kind    LifecycleActionKind
	Reason  string
}

// AgentSnapshot is the per-agent slice of state DeterministicPolicy
// consumes. The parent orchestrator builds these from db.Queries +
// EconomicsStore once per epoch before calling Evaluate.
type AgentSnapshot struct {
	AgentID string
	Status  string // "active" | "paused" | "dead"

	// Balance is the agent wallet's USD value. Spec line 514: kill when
	// balance < MinBalanceUSD.
	BalanceUSD float64
	// Drawdown is the agent's peak-to-trough P&L drawdown as a fraction.
	// Kill when ≥ MaxDrawdownFraction.
	Drawdown float64
	// ConsecutiveLosingEpochs is the streak of epochs with negative
	// NetProfit. Kill when ≥ MaxConsecutiveLosingEpochs.
	ConsecutiveLosingEpochs int
	// OperatingDebtUSD is unpaid debt carried from prior epochs (spec
	// line 91's `carriedDebt`). Kill when > MaxOperatingDebtUSD.
	OperatingDebtUSD float64
	// LastHeartbeat is the most recent heartbeat write. Kill when older
	// than HeartbeatGracePeriod (spec line 514: "heartbeat missing").
	LastHeartbeat time.Time

	// Strategist verdict from the last cycle (chunk 14's strategist
	// writes "continue" | "pause" | "aggressive" | "defensive"). Used
	// for the regime-mismatch pause (spec line 518).
	LastStrategistVerdict string

	// SolventButWeak: agent has positive balance but cumulative
	// NetProfit < threshold over the evaluation window (spec line 519:
	// "solvent but weak" → pause).
	NetProfitWindowUSD float64

	// BudgetTight is the global flag from BudgetTracker.State(). When
	// true, pause shadow nodes (spec line 520 + line 587: "budget tight"
	// → pause). NodeClass distinguishes shadow vs funded.
	NodeClass string // "shadow" | "funded"
}

// LifecyclePolicyConfig pins the thresholds DeterministicPolicy uses.
// Defaults come from spec lines 514-520; chunk 21 wires the actual
// numbers from config.
type LifecyclePolicyConfig struct {
	MinBalanceUSD              float64
	MaxDrawdownFraction        float64
	MaxConsecutiveLosingEpochs int
	MaxOperatingDebtUSD        float64
	HeartbeatGracePeriod       time.Duration
	WeakNetProfitThresholdUSD  float64
}

// DefaultLifecyclePolicyConfig returns the spec defaults. The orchestrator
// can override any field; zero values are treated as "use default."
func DefaultLifecyclePolicyConfig() LifecyclePolicyConfig {
	return LifecyclePolicyConfig{
		MinBalanceUSD:              1.0,
		MaxDrawdownFraction:        0.40,
		MaxConsecutiveLosingEpochs: 5,
		MaxOperatingDebtUSD:        5.0,
		HeartbeatGracePeriod:       10 * time.Minute,
		WeakNetProfitThresholdUSD:  0.10,
	}
}

// DeterministicLifecyclePolicy applies spec lines 514-520 to a set of
// agent snapshots and returns the resulting actions. Pure function —
// inputs in, actions out, no LLM, no DB. The parent orchestrator
// executes the actions through runtime.LifecycleManager.
//
// Precedence (highest first): kill > pause > resume.
func DeterministicLifecyclePolicy(now time.Time, cfg LifecyclePolicyConfig, snapshots []AgentSnapshot, budgetTight bool) []LifecycleAction {
	cfg = applyDefaults(cfg)
	var actions []LifecycleAction
	for _, s := range snapshots {
		if s.Status == "dead" {
			continue
		}
		if action, ok := killReason(now, cfg, s); ok {
			actions = append(actions, LifecycleAction{
				AgentID: s.AgentID,
				Kind:    ActionKill,
				Reason:  action,
			})
			continue
		}
		if reason, ok := pauseReason(cfg, s, budgetTight); ok {
			if s.Status == "paused" {
				continue
			}
			actions = append(actions, LifecycleAction{
				AgentID: s.AgentID,
				Kind:    ActionPause,
				Reason:  reason,
			})
			continue
		}
		// Healthy and currently paused → resume.
		if s.Status == "paused" {
			actions = append(actions, LifecycleAction{
				AgentID: s.AgentID,
				Kind:    ActionResume,
				Reason:  "policy_healthy",
			})
		}
	}
	return actions
}

func killReason(now time.Time, cfg LifecyclePolicyConfig, s AgentSnapshot) (string, bool) {
	if s.BalanceUSD < cfg.MinBalanceUSD {
		return "balance_below_minimum", true
	}
	if s.Drawdown >= cfg.MaxDrawdownFraction {
		return "max_drawdown_exceeded", true
	}
	if s.ConsecutiveLosingEpochs >= cfg.MaxConsecutiveLosingEpochs {
		return "consecutive_losing_epochs", true
	}
	if s.OperatingDebtUSD > cfg.MaxOperatingDebtUSD {
		return "operating_debt_exceeded", true
	}
	if !s.LastHeartbeat.IsZero() && now.Sub(s.LastHeartbeat) > cfg.HeartbeatGracePeriod {
		return "heartbeat_missing", true
	}
	return "", false
}

func pauseReason(cfg LifecyclePolicyConfig, s AgentSnapshot, budgetTight bool) (string, bool) {
	switch s.LastStrategistVerdict {
	case "pause":
		return "strategy_regime_mismatch", true
	case "defensive":
		// defensive verdict pauses risky shadow nodes; funded stay live.
		if s.NodeClass == "shadow" {
			return "strategy_defensive", true
		}
	}
	if budgetTight && s.NodeClass == "shadow" {
		return "budget_tight", true
	}
	if s.BalanceUSD > 0 && s.NetProfitWindowUSD < cfg.WeakNetProfitThresholdUSD {
		return "solvent_but_weak", true
	}
	return "", false
}

func applyDefaults(cfg LifecyclePolicyConfig) LifecyclePolicyConfig {
	d := DefaultLifecyclePolicyConfig()
	if cfg.MinBalanceUSD == 0 {
		cfg.MinBalanceUSD = d.MinBalanceUSD
	}
	if cfg.MaxDrawdownFraction == 0 {
		cfg.MaxDrawdownFraction = d.MaxDrawdownFraction
	}
	if cfg.MaxConsecutiveLosingEpochs == 0 {
		cfg.MaxConsecutiveLosingEpochs = d.MaxConsecutiveLosingEpochs
	}
	if cfg.MaxOperatingDebtUSD == 0 {
		cfg.MaxOperatingDebtUSD = d.MaxOperatingDebtUSD
	}
	if cfg.HeartbeatGracePeriod == 0 {
		cfg.HeartbeatGracePeriod = d.HeartbeatGracePeriod
	}
	if cfg.WeakNetProfitThresholdUSD == 0 {
		cfg.WeakNetProfitThresholdUSD = d.WeakNetProfitThresholdUSD
	}
	return cfg
}
