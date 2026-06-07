package intelligence

import (
	"context"
	"testing"
	"time"
)

func TestDecayJob_RunOnce(t *testing.T) {
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	clock := t0
	g := NewKnowledgeGraph().withClock(func() time.Time { return clock })
	mustUpsert(t, g, newEdge("A", "r", "B", DirectionPositive, 0.5))
	// Advance past the decay window so the next pass has something to do.
	clock = t0.Add(31 * 24 * time.Hour)

	job := NewDecayJob(DecayJobConfig{Graph: g, Now: func() time.Time { return clock }})
	d, del := job.RunOnce()
	if d != 1 || del != 0 {
		t.Errorf("decayed=%d deleted=%d, want 1/0", d, del)
	}
}

func TestDecayJob_NilGraphIsNoop(t *testing.T) {
	job := NewDecayJob(DecayJobConfig{})
	if d, del := job.RunOnce(); d != 0 || del != 0 {
		t.Errorf("nil graph: decayed=%d deleted=%d", d, del)
	}
}

func TestDecayJob_RunRespectsContextCancellation(t *testing.T) {
	g := NewKnowledgeGraph()
	job := NewDecayJob(DecayJobConfig{Graph: g, Interval: 10 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := job.Run(ctx); err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestDecayJob_DefaultIntervalApplied(t *testing.T) {
	j := NewDecayJob(DecayJobConfig{Graph: NewKnowledgeGraph()})
	if j.cfg.Interval != 24*time.Hour {
		t.Errorf("default interval = %v, want 24h", j.cfg.Interval)
	}
}
