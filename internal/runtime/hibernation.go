package runtime

import (
	"time"

	"github.com/veermshah/cambrian/internal/agent"
)

// Class strings used by the lifecycle layer. Kept as private constants
// so the hibernation logic doesn't accidentally drift from the
// canonical strings stored in agents.node_class.
const (
	nodeClassFunded = "funded"
	nodeClassShadow = "shadow"
)

// HibernationDecision tells the NodeRunner whether to act on this tick
// or skip it. Reason is included so the log emits a single distinct
// label per skip — useful for the dashboard's awake-time histogram.
type HibernationDecision struct {
	Awake  bool
	Reason string // "funded", "shadow_awake", "shadow_asleep", "hibernation_disabled", "shadow_no_window"
}

// HibernationScheduler implements the Hermes shadow-node sleep/wake
// cycle (spec lines 207–217 + 553–558). The scheduler is global —
// every NodeRunner consults the same instance via NodeRunnerConfig.
// Per-agent state (the strategist-tick anchor) is supplied each call,
// so the scheduler itself stays lock-free.
//
// Model: a shadow agent is awake for `AwakeWindowMinutes` total around
// each scheduled strategist tick, centered on the tick boundary. With
// a 12 h strategist cadence and a 120 min awake window, the agent is
// awake for ±60 min around each of the two daily ticks — 4 h total per
// day, matching the spec's "≤ 30% wall clock" target (~17% here).
type HibernationScheduler struct {
	clock func() time.Time
}

// NewHibernationScheduler returns a scheduler using the given clock
// (or time.Now when clock is nil).
func NewHibernationScheduler(clock func() time.Time) *HibernationScheduler {
	if clock == nil {
		clock = time.Now
	}
	return &HibernationScheduler{clock: clock}
}

// Decide computes the wake/sleep state for one agent. Inputs:
//
//   - nodeClass: "funded" / "shadow" (anything else treated as funded).
//   - sched: the genome's SleepSchedule. AwakeWindowMinutes ≤ 0 ⇒
//     hibernation disabled, agent always awake.
//   - anchor: a stable per-agent reference time the cycle anchors to.
//     The runtime passes the agent's created_at; tests pass whatever.
//   - strategistInterval: the StrategistInterval the NodeRunner is
//     using. ≤ 0 ⇒ hibernation disabled.
//
// Decide is pure and side-effect-free.
func (h *HibernationScheduler) Decide(
	nodeClass string,
	sched agent.SleepSchedule,
	anchor time.Time,
	strategistInterval time.Duration,
) HibernationDecision {
	if nodeClass != nodeClassShadow {
		return HibernationDecision{Awake: true, Reason: "funded"}
	}
	if !sched.SleepBetween {
		return HibernationDecision{Awake: true, Reason: "hibernation_disabled"}
	}
	if sched.AwakeWindowMinutes <= 0 {
		return HibernationDecision{Awake: true, Reason: "shadow_no_window"}
	}
	if strategistInterval <= 0 {
		return HibernationDecision{Awake: true, Reason: "hibernation_disabled"}
	}
	window := time.Duration(sched.AwakeWindowMinutes) * time.Minute
	if window >= strategistInterval {
		// Awake window covers the whole cycle ⇒ always awake.
		return HibernationDecision{Awake: true, Reason: "shadow_awake"}
	}
	now := h.clock()
	awake := isAwakeAt(now, anchor, strategistInterval, window)
	if awake {
		return HibernationDecision{Awake: true, Reason: "shadow_awake"}
	}
	return HibernationDecision{Awake: false, Reason: "shadow_asleep"}
}

// IsAwake is the boolean-only variant for callers that don't need the
// reason. Equivalent to `Decide(...).Awake`.
func (h *HibernationScheduler) IsAwake(
	nodeClass string,
	sched agent.SleepSchedule,
	anchor time.Time,
	strategistInterval time.Duration,
) bool {
	return h.Decide(nodeClass, sched, anchor, strategistInterval).Awake
}

// isAwakeAt is the pure-math kernel: given the cycle anchor and a
// centered awake window of total length `window` around each tick,
// return true iff `now` falls inside any cycle's window.
//
// The check uses modular arithmetic on (now - anchor) to find the
// agent's current phase within the cycle. Awake when the phase is
// within window/2 of either boundary (the tick itself).
func isAwakeAt(now, anchor time.Time, interval, window time.Duration) bool {
	if interval <= 0 {
		return true
	}
	elapsed := now.Sub(anchor)
	// Normalise into [0, interval) — Go's % preserves sign so an anchor
	// later than now still yields a sensible phase.
	phase := elapsed % interval
	if phase < 0 {
		phase += interval
	}
	half := window / 2
	return phase < half || phase >= interval-half
}

// AwakeFractionOver24h is a helper for tests and observability: it
// samples Decide at 1-minute intervals over a 24-hour span starting at
// `start` and returns the awake fraction in [0, 1]. Production code
// shouldn't need this; it powers the chunk's acceptance criterion test.
func (h *HibernationScheduler) AwakeFractionOver24h(
	nodeClass string,
	sched agent.SleepSchedule,
	anchor, start time.Time,
	strategistInterval time.Duration,
) float64 {
	const samples = 24 * 60
	if strategistInterval <= 0 || sched.AwakeWindowMinutes <= 0 || !sched.SleepBetween || nodeClass != nodeClassShadow {
		return 1.0
	}
	window := time.Duration(sched.AwakeWindowMinutes) * time.Minute
	awakeCount := 0
	for i := 0; i < samples; i++ {
		t := start.Add(time.Duration(i) * time.Minute)
		if isAwakeAt(t, anchor, strategistInterval, window) {
			awakeCount++
		}
	}
	return float64(awakeCount) / float64(samples)
}
