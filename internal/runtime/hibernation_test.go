package runtime

import (
	"math"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/agent"
)

func TestHibernation_FundedAlwaysAwake(t *testing.T) {
	s := NewHibernationScheduler(func() time.Time {
		return time.Date(2026, 6, 9, 3, 14, 15, 0, time.UTC)
	})
	dec := s.Decide("funded",
		agent.SleepSchedule{AwakeWindowMinutes: 120, SleepBetween: true},
		time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC),
		12*time.Hour,
	)
	if !dec.Awake || dec.Reason != "funded" {
		t.Errorf("funded must be awake, got %+v", dec)
	}
}

func TestHibernation_ShadowAwakeInWindow(t *testing.T) {
	anchor := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	s := NewHibernationScheduler(func() time.Time { return anchor }) // exactly on tick
	dec := s.Decide("shadow",
		agent.SleepSchedule{AwakeWindowMinutes: 120, SleepBetween: true},
		anchor,
		12*time.Hour,
	)
	if !dec.Awake || dec.Reason != "shadow_awake" {
		t.Errorf("at tick must be awake, got %+v", dec)
	}
}

func TestHibernation_ShadowAsleepOutsideWindow(t *testing.T) {
	anchor := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	// 6h into a 12h cycle, with a ±60min window → asleep.
	s := NewHibernationScheduler(func() time.Time { return anchor.Add(6 * time.Hour) })
	dec := s.Decide("shadow",
		agent.SleepSchedule{AwakeWindowMinutes: 120, SleepBetween: true},
		anchor,
		12*time.Hour,
	)
	if dec.Awake {
		t.Errorf("mid-cycle should be asleep, got awake")
	}
	if dec.Reason != "shadow_asleep" {
		t.Errorf("reason = %q, want shadow_asleep", dec.Reason)
	}
}

func TestHibernation_AwakeWindowCenteredOnTick(t *testing.T) {
	anchor := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	window := agent.SleepSchedule{AwakeWindowMinutes: 120, SleepBetween: true}
	interval := 12 * time.Hour
	cases := []struct {
		offset   time.Duration
		wantAwake bool
		label    string
	}{
		{0, true, "tick"},
		{59 * time.Minute, true, "59m past tick"},
		{61 * time.Minute, false, "61m past tick"},
		{11*time.Hour + 1*time.Minute, true, "59m before next tick"},
		{10*time.Hour + 59*time.Minute, false, "61m before next tick"},
	}
	for _, tc := range cases {
		s := NewHibernationScheduler(func() time.Time { return anchor.Add(tc.offset) })
		got := s.IsAwake("shadow", window, anchor, interval)
		if got != tc.wantAwake {
			t.Errorf("%s (offset %v): got awake=%v, want %v", tc.label, tc.offset, got, tc.wantAwake)
		}
	}
}

func TestHibernation_24hAwakeFractionUnderThreshold(t *testing.T) {
	// Acceptance criterion: shadow with AwakeWindowMinutes=120 and 12h
	// strategist cadence ≤ 30% wall clock (target ~17% = 4h/24h).
	anchor := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	s := NewHibernationScheduler(nil)
	frac := s.AwakeFractionOver24h(
		"shadow",
		agent.SleepSchedule{AwakeWindowMinutes: 120, SleepBetween: true},
		anchor, anchor, 12*time.Hour,
	)
	if frac > 0.30 {
		t.Errorf("awake fraction = %.3f, want ≤ 0.30", frac)
	}
	// Sanity: should be roughly 4h / 24h = 0.166...
	if math.Abs(frac-(4.0/24.0)) > 0.02 {
		t.Errorf("awake fraction = %.3f, want ≈ 0.167", frac)
	}
}

func TestHibernation_DisabledWhenSleepBetweenFalse(t *testing.T) {
	anchor := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	s := NewHibernationScheduler(func() time.Time { return anchor.Add(6 * time.Hour) })
	dec := s.Decide("shadow",
		agent.SleepSchedule{AwakeWindowMinutes: 120, SleepBetween: false},
		anchor,
		12*time.Hour,
	)
	if !dec.Awake || dec.Reason != "hibernation_disabled" {
		t.Errorf("SleepBetween=false should keep agent awake, got %+v", dec)
	}
}

func TestHibernation_ZeroWindowKeepsAgentAwake(t *testing.T) {
	anchor := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	s := NewHibernationScheduler(func() time.Time { return anchor.Add(6 * time.Hour) })
	dec := s.Decide("shadow",
		agent.SleepSchedule{AwakeWindowMinutes: 0, SleepBetween: true},
		anchor,
		12*time.Hour,
	)
	if !dec.Awake {
		t.Errorf("zero awake window should not hibernate, got asleep")
	}
}

func TestHibernation_WindowGreaterThanCycleAlwaysAwake(t *testing.T) {
	anchor := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	s := NewHibernationScheduler(func() time.Time { return anchor.Add(8 * time.Hour) })
	dec := s.Decide("shadow",
		agent.SleepSchedule{AwakeWindowMinutes: 60 * 24, SleepBetween: true},
		anchor,
		12*time.Hour,
	)
	if !dec.Awake {
		t.Error("window ≥ interval should mean always awake")
	}
}

func TestHibernation_AnchorAfterNowPhaseNormalised(t *testing.T) {
	// Defensive: anchor in the future shouldn't crash; the modulo
	// arithmetic should normalise back into [0, interval).
	now := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	anchor := now.Add(48 * time.Hour) // future
	s := NewHibernationScheduler(func() time.Time { return now })
	// Should not panic and should produce a sensible boolean.
	_ = s.IsAwake("shadow",
		agent.SleepSchedule{AwakeWindowMinutes: 120, SleepBetween: true},
		anchor, 12*time.Hour,
	)
}
