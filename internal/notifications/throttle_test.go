package notifications

import (
	"sync/atomic"
	"testing"
	"time"
)

func clockAt(start time.Time) (func() time.Time, *atomic.Int64) {
	off := &atomic.Int64{}
	return func() time.Time {
		return start.Add(time.Duration(off.Load()))
	}, off
}

func TestThrottle_SameTypeCooldownBlocksBurst(t *testing.T) {
	// Spec acceptance: a burst of 30 same-type events delivers 1, then
	// queues nothing further until cooldown.
	start := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	now, off := clockAt(start)
	th := NewThrottle(ThrottleConfig{HourlyCap: 20, Cooldown: 60 * time.Second, Now: now})
	allowed := 0
	for i := 0; i < 30; i++ {
		if ok, _ := th.Allow("circuit_breaker"); ok {
			allowed++
		}
	}
	if allowed != 1 {
		t.Errorf("burst of 30 same-type delivered %d, want 1", allowed)
	}
	// Advance past the cooldown — one more should fire.
	off.Store(int64(61 * time.Second))
	if ok, _ := th.Allow("circuit_breaker"); !ok {
		t.Error("after cooldown the next same-type message should fire")
	}
}

func TestThrottle_HourlyCapMixedTypes(t *testing.T) {
	// Spec acceptance: 25 mixed-type events deliver exactly 20.
	start := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	now, _ := clockAt(start)
	th := NewThrottle(ThrottleConfig{HourlyCap: 20, Cooldown: 60 * time.Second, Now: now})
	allowed := 0
	for i := 0; i < 25; i++ {
		etype := "type_" + string(rune('a'+i%25)) // 25 distinct types ⇒ no cooldown blocks
		if ok, _ := th.Allow(etype); ok {
			allowed++
		}
	}
	if allowed != 20 {
		t.Errorf("25 mixed-type delivered %d, want 20", allowed)
	}
}

func TestThrottle_RefillRestoresCapacity(t *testing.T) {
	start := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	now, off := clockAt(start)
	th := NewThrottle(ThrottleConfig{HourlyCap: 20, Cooldown: 60 * time.Second, Now: now})
	// Drain the bucket.
	for i := 0; i < 20; i++ {
		th.Allow("type_" + string(rune('a'+i)))
	}
	if ok, reason := th.Allow("type_extra"); ok || reason != "hourly_cap" {
		t.Errorf("expected hourly_cap block, got ok=%v reason=%q", ok, reason)
	}
	// Advance 3 minutes — should refill exactly 1 token (rate = 20/hour).
	off.Store(int64(3 * time.Minute))
	if got := th.Tokens(); got < 0.95 || got > 1.05 {
		t.Errorf("tokens after 3min = %v, want ≈ 1", got)
	}
}

func TestThrottle_DistinctTypesNotBlockedByCooldown(t *testing.T) {
	start := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	now, _ := clockAt(start)
	th := NewThrottle(ThrottleConfig{HourlyCap: 20, Cooldown: 60 * time.Second, Now: now})
	if ok, _ := th.Allow("circuit_breaker"); !ok {
		t.Fatal("first allow should succeed")
	}
	// Distinct type at the same instant should pass — cooldown is
	// per-type, not global.
	if ok, _ := th.Allow("agent_killed"); !ok {
		t.Error("distinct type should not be blocked by cooldown")
	}
}

func TestThrottle_DefaultsAppliedOnZero(t *testing.T) {
	th := NewThrottle(ThrottleConfig{})
	if th.hourlyCap != 20 || th.cooldown != 60*time.Second {
		t.Errorf("defaults: cap=%v cooldown=%v", th.hourlyCap, th.cooldown)
	}
}
