package intelligence

import (
	"math"
	"testing"
	"time"
)

func nowAt(ts time.Time) func() time.Time {
	return func() time.Time { return ts }
}

func TestAccuracyTracker_EmptySource(t *testing.T) {
	tr := NewAccuracyTracker(0) // default 30 days
	s := tr.Score("unknown")
	if s.Observations != 0 || s.Accuracy != 0 {
		t.Errorf("empty source should return zero: %+v", s)
	}
}

func TestAccuracyTracker_BasicCount(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	tr := NewAccuracyTracker(0).withClock(nowAt(now))
	for i := 0; i < 7; i++ {
		tr.Ingest(SignalOutcome{
			SourceAgentID: "agent-A",
			SignalType:    "momentum",
			ObservedAt:    now.Add(-time.Duration(i) * time.Hour),
			Correct:       i < 5, // 5 correct, 2 wrong
		})
	}
	s := tr.Score("agent-A")
	if s.Observations != 7 {
		t.Errorf("observations = %d, want 7", s.Observations)
	}
	if s.Correct != 5 {
		t.Errorf("correct = %d, want 5", s.Correct)
	}
	if math.Abs(s.Accuracy-5.0/7.0) > 1e-9 {
		t.Errorf("accuracy = %v, want 5/7", s.Accuracy)
	}
}

func TestAccuracyTracker_RollingWindowEvictsOldObservations(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	tr := NewAccuracyTracker(30*24*time.Hour).withClock(nowAt(now))

	// Inside window: 3 correct.
	for i := 0; i < 3; i++ {
		tr.Ingest(SignalOutcome{
			SourceAgentID: "agent-A",
			ObservedAt:    now.Add(-time.Duration(i+1) * 24 * time.Hour),
			Correct:       true,
		})
	}
	// Exactly at the cutoff (now-30d) should be evicted.
	tr.Ingest(SignalOutcome{
		SourceAgentID: "agent-A",
		ObservedAt:    now.Add(-30 * 24 * time.Hour),
		Correct:       false,
	})
	// 60 days ago — well outside.
	tr.Ingest(SignalOutcome{
		SourceAgentID: "agent-A",
		ObservedAt:    now.Add(-60 * 24 * time.Hour),
		Correct:       false,
	})

	s := tr.Score("agent-A")
	if s.Observations != 3 {
		t.Errorf("observations = %d, want 3 (old ones dropped)", s.Observations)
	}
	if s.Accuracy != 1.0 {
		t.Errorf("accuracy = %v, want 1.0", s.Accuracy)
	}
}

func TestAccuracyTracker_HandComputedFixture(t *testing.T) {
	// Spec line 388: 30-day rolling window. Fixture: 10 observations
	// spread over 30 days, 7 correct → 70% accuracy.
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	tr := NewAccuracyTracker(30*24*time.Hour).withClock(nowAt(now))
	pattern := []bool{true, true, true, false, true, true, false, true, true, false}
	for i, correct := range pattern {
		tr.Ingest(SignalOutcome{
			SourceAgentID: "scout-1",
			ObservedAt:    now.Add(-time.Duration(i*3) * 24 * time.Hour),
			Correct:       correct,
		})
	}
	s := tr.Score("scout-1")
	if s.Observations != 10 {
		t.Errorf("observations = %d, want 10", s.Observations)
	}
	want := 0.7
	if math.Abs(s.Accuracy-want) > 1e-9 {
		t.Errorf("accuracy = %v, want %v", s.Accuracy, want)
	}
}

func TestAccuracyTracker_WeightNeutralBelowMinObservations(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	tr := NewAccuracyTracker(0).withClock(nowAt(now))
	for i := 0; i < MinObservations-1; i++ {
		tr.Ingest(SignalOutcome{
			SourceAgentID: "new-agent",
			ObservedAt:    now.Add(-time.Hour),
			Correct:       true,
		})
	}
	if w := tr.Weight("new-agent"); w != 0.5 {
		t.Errorf("weight = %v, want 0.5 (insufficient obs)", w)
	}
}

func TestAccuracyTracker_WeightUsesAccuracyAtThreshold(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	tr := NewAccuracyTracker(0).withClock(nowAt(now))
	for i := 0; i < MinObservations; i++ {
		tr.Ingest(SignalOutcome{
			SourceAgentID: "ok-agent",
			ObservedAt:    now.Add(-time.Duration(i) * time.Hour),
			Correct:       i < 4, // 4/5 = 0.8
		})
	}
	if w := tr.Weight("ok-agent"); math.Abs(w-0.8) > 1e-9 {
		t.Errorf("weight = %v, want 0.8", w)
	}
}

func TestAccuracyTracker_AllScoresOrdered(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	tr := NewAccuracyTracker(0).withClock(nowAt(now))

	// agent-A: 5/5 = 1.0
	for i := 0; i < 5; i++ {
		tr.Ingest(SignalOutcome{SourceAgentID: "agent-A", ObservedAt: now.Add(-time.Hour), Correct: true})
	}
	// agent-B: 3/5 = 0.6
	for i := 0; i < 5; i++ {
		tr.Ingest(SignalOutcome{SourceAgentID: "agent-B", ObservedAt: now.Add(-time.Hour), Correct: i < 3})
	}
	// agent-C: 4/5 = 0.8
	for i := 0; i < 5; i++ {
		tr.Ingest(SignalOutcome{SourceAgentID: "agent-C", ObservedAt: now.Add(-time.Hour), Correct: i < 4})
	}

	scores := tr.AllScores()
	if len(scores) != 3 {
		t.Fatalf("expected 3 scores, got %d", len(scores))
	}
	wantOrder := []string{"agent-A", "agent-C", "agent-B"}
	for i, w := range wantOrder {
		if scores[i].SourceAgentID != w {
			t.Errorf("position %d: got %q, want %q", i, scores[i].SourceAgentID, w)
		}
	}
}

func TestAccuracyTracker_IngestRejectsEmptySource(t *testing.T) {
	tr := NewAccuracyTracker(0)
	tr.Ingest(SignalOutcome{ObservedAt: time.Now(), Correct: true})
	if got := tr.Score(""); got.Observations != 0 {
		t.Errorf("empty-source ingest should be dropped, got %+v", got)
	}
}

func TestAccuracyTracker_IngestDropsAlreadyExpired(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	tr := NewAccuracyTracker(30*24*time.Hour).withClock(nowAt(now))
	tr.Ingest(SignalOutcome{
		SourceAgentID: "agent-A",
		ObservedAt:    now.Add(-60 * 24 * time.Hour),
		Correct:       true,
	})
	if got := tr.Score("agent-A"); got.Observations != 0 {
		t.Errorf("pre-window ingest should be dropped, got %+v", got)
	}
}

func TestAccuracyTracker_ConcurrentSafe(t *testing.T) {
	tr := NewAccuracyTracker(0)
	done := make(chan struct{})
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				tr.Ingest(SignalOutcome{
					SourceAgentID: "agent-A",
					ObservedAt:    time.Now(),
					Correct:       j%2 == 0,
				})
				_ = tr.Score("agent-A")
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 5; i++ {
		<-done
	}
	if s := tr.Score("agent-A"); s.Observations == 0 {
		t.Error("concurrent ingest produced no observations")
	}
}

func TestAccuracyTracker_WindowAdvancesOverTime(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	clock := now
	tr := NewAccuracyTracker(30*24*time.Hour).withClock(func() time.Time { return clock })

	// Ingest 5 observations 25 days ago (inside window).
	for i := 0; i < 5; i++ {
		tr.Ingest(SignalOutcome{
			SourceAgentID: "agent-A",
			ObservedAt:    now.Add(-25 * 24 * time.Hour),
			Correct:       true,
		})
	}
	if got := tr.Score("agent-A"); got.Observations != 5 {
		t.Fatalf("initial = %d, want 5", got.Observations)
	}

	// Advance clock 10 days — observations now 35 days old, outside window.
	clock = now.Add(10 * 24 * time.Hour)
	if got := tr.Score("agent-A"); got.Observations != 0 {
		t.Errorf("after window advance: %d observations, want 0", got.Observations)
	}
}
