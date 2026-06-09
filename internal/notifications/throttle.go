// Package notifications owns the Telegram fan-out goroutine. Spec
// lines 503-506: always-notify events for circuit breaker, agent
// lifecycle, epoch completion, budget warning and treasury-low; a
// daily digest at a configurable hour; throttled to 20 messages per
// hour with a 60-second cooldown between same-type messages.
package notifications

import (
	"sync"
	"time"
)

// Throttle limits the volume and tempo of outgoing Telegram messages.
// Spec line 505:
//
//   - Hourly cap: at most HourlyCap deliveries per rolling hour.
//   - Same-type cooldown: at most one message per (EventType, Cooldown)
//     window.
//
// The hourly cap is implemented as a token bucket with continuous
// refill — this avoids the "21 deliveries in the same second across
// the hour boundary" artifact a fixed-window counter would have. The
// cooldown is a simple per-type "last sent at" map; a candidate is
// admitted only if its event type hasn't been sent within Cooldown.
//
// Throttle is safe for concurrent use; tests and the notifier loop can
// share one.
type Throttle struct {
	mu sync.Mutex

	now func() time.Time

	hourlyCap  float64
	tokens     float64
	refillRate float64 // tokens per second

	cooldown   time.Duration
	lastByType map[string]time.Time

	lastRefill time.Time
}

// ThrottleConfig configures the throttle. Zero values trigger
// production defaults: HourlyCap=20, Cooldown=60s.
type ThrottleConfig struct {
	HourlyCap int
	Cooldown  time.Duration
	Now       func() time.Time
}

// NewThrottle constructs a throttle, starting with a full bucket so a
// freshly-booted notifier doesn't burn the first 20 events to refill.
func NewThrottle(cfg ThrottleConfig) *Throttle {
	if cfg.HourlyCap <= 0 {
		cfg.HourlyCap = 20
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 60 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cap := float64(cfg.HourlyCap)
	return &Throttle{
		now:        cfg.Now,
		hourlyCap:  cap,
		tokens:     cap,
		refillRate: cap / 3600.0,
		cooldown:   cfg.Cooldown,
		lastByType: map[string]time.Time{},
		lastRefill: cfg.Now(),
	}
}

// Allow reports whether a message of the given type may be sent now.
// On success the throttle consumes one token and records the send time.
// On failure (cooldown or empty bucket) it returns false and the
// reason — the notifier can log skipped events without ambiguity.
func (t *Throttle) Allow(eventType string) (bool, string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	t.refillLocked(now)

	if last, ok := t.lastByType[eventType]; ok {
		if now.Sub(last) < t.cooldown {
			return false, "cooldown"
		}
	}
	if t.tokens < 1.0 {
		return false, "hourly_cap"
	}
	t.tokens -= 1.0
	t.lastByType[eventType] = now
	return true, ""
}

// Tokens returns the current token count (fractional). Test
// introspection only — not part of the production interface.
func (t *Throttle) Tokens() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refillLocked(t.now())
	return t.tokens
}

func (t *Throttle) refillLocked(now time.Time) {
	if now.Before(t.lastRefill) {
		t.lastRefill = now
		return
	}
	elapsed := now.Sub(t.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	t.tokens += elapsed * t.refillRate
	if t.tokens > t.hourlyCap {
		t.tokens = t.hourlyCap
	}
	t.lastRefill = now
}
