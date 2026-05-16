package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduler_StaggerOffset(t *testing.T) {
	s := NewScheduler(4, 500*time.Millisecond)
	cases := []struct {
		i    int
		want time.Duration
	}{
		{0, 0},
		{1, 500 * time.Millisecond},
		{5, 2500 * time.Millisecond},
		{-1, 0},
	}
	for _, c := range cases {
		if got := s.StaggerOffset(c.i); got != c.want {
			t.Errorf("StaggerOffset(%d) = %v, want %v", c.i, got, c.want)
		}
	}
}

func TestScheduler_DefaultStep(t *testing.T) {
	s := NewScheduler(4, 0)
	if got := s.StaggerOffset(2); got != time.Second {
		t.Errorf("default step: got %v, want 1s", got)
	}
}

func TestScheduler_AcquireRelease(t *testing.T) {
	s := NewScheduler(2, 0)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := s.AcquireStrategist(ctx); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		s.ReleaseStrategist()
	}
}

// TestScheduler_BackpressureCap fires 50 concurrent acquire attempts and
// verifies the in-flight count never exceeds the cap. Spec acceptance
// criterion: "50 simultaneous strategist firings cap at the configured
// concurrency."
func TestScheduler_BackpressureCap(t *testing.T) {
	const cap_ = 4
	const fires = 50

	s := NewScheduler(cap_, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var inflight, maxSeen atomic.Int64
	done := make(chan struct{}, fires)
	hold := make(chan struct{})

	for i := 0; i < fires; i++ {
		go func() {
			if err := s.AcquireStrategist(ctx); err != nil {
				done <- struct{}{}
				return
			}
			cur := inflight.Add(1)
			for {
				prev := maxSeen.Load()
				if cur <= prev || maxSeen.CompareAndSwap(prev, cur) {
					break
				}
			}
			<-hold
			inflight.Add(-1)
			s.ReleaseStrategist()
			done <- struct{}{}
		}()
	}

	// Give all goroutines time to reach acquire and the first cap_
	// to enter the critical section.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if inflight.Load() == int64(cap_) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := maxSeen.Load(); got > int64(cap_) {
		t.Fatalf("max in-flight = %d, want ≤ %d", got, cap_)
	}
	if got := inflight.Load(); got != int64(cap_) {
		t.Fatalf("in-flight at saturation = %d, want %d", got, cap_)
	}
	close(hold)

	for i := 0; i < fires; i++ {
		<-done
	}
	if got := maxSeen.Load(); got != int64(cap_) {
		t.Fatalf("final max in-flight = %d, want = %d", got, cap_)
	}
}

func TestScheduler_NilSafe(t *testing.T) {
	var s *Scheduler
	if got := s.StaggerOffset(3); got != 0 {
		t.Errorf("nil receiver StaggerOffset = %v", got)
	}
	if got := s.MaxStrategistConcurrency(); got != 0 {
		t.Errorf("nil receiver Max = %v", got)
	}
	// ReleaseStrategist on nil must not panic.
	s.ReleaseStrategist()
}
