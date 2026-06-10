package e2e

import (
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/runtime"
)

// TestHibernation_ShadowSleepSavingsAtLeast60Pct — spec line 1196.
// Pure-logic. Shadow node on a 12-hour cadence with a 2-hour awake
// window should be awake at most ~17% of the day (4h/24h ≈ 16.7%).
// Spec acceptance: ≥ 60% sleep savings ⇒ awake fraction ≤ 0.40.
func TestHibernation_ShadowSleepSavingsAtLeast60Pct(t *testing.T) {
	h := runtime.NewHibernationScheduler(time.Now)
	sched := agent.SleepSchedule{
		AwakeWindowMinutes: 120,
		SleepBetween:       true,
	}
	anchor := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	start := anchor
	strategistInterval := 12 * time.Hour

	awake := h.AwakeFractionOver24h("shadow", sched, anchor, start, strategistInterval)
	if awake > 0.40 {
		t.Errorf("shadow awake fraction = %.3f, want ≤ 0.40 (≥60%% savings)", awake)
	}
	// A reasonable lower bound — the window should actually fire at
	// least once every 12 hours, so we expect roughly 4h/24h ≈ 0.167.
	if awake < 0.10 {
		t.Errorf("shadow awake fraction = %.3f, suspiciously low (window may not fire)", awake)
	}
}

// TestHibernation_FundedAlwaysAwake — sanity: funded nodes never sleep.
func TestHibernation_FundedAlwaysAwake(t *testing.T) {
	h := runtime.NewHibernationScheduler(time.Now)
	sched := agent.SleepSchedule{AwakeWindowMinutes: 120, SleepBetween: true}
	awake := h.AwakeFractionOver24h(
		"funded", sched,
		time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC),
		12*time.Hour,
	)
	if awake != 1.0 {
		t.Errorf("funded awake fraction = %.3f, want 1.0", awake)
	}
}
