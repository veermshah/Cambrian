package e2e

import (
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/intelligence"
)

// TestSignalAccuracy_WeightingByCorrectness — spec line 1194.
// Pure-logic. A source with consistently correct outcomes ranks above
// a source with mixed outcomes; missing sources score zero. This is
// the basis for the chunk-23 aggregator's accuracy weighting.
func TestSignalAccuracy_WeightingByCorrectness(t *testing.T) {
	tr := intelligence.NewAccuracyTracker(30 * 24 * time.Hour)
	now := time.Now()

	// Source A: 9/10 correct.
	for i := 0; i < 10; i++ {
		tr.Ingest(intelligence.SignalOutcome{
			SourceAgentID: "src-A",
			Correct:       i != 0,
			ObservedAt:    now.Add(-time.Hour),
		})
	}
	// Source B: 5/10 correct.
	for i := 0; i < 10; i++ {
		tr.Ingest(intelligence.SignalOutcome{
			SourceAgentID: "src-B",
			Correct:       i%2 == 0,
			ObservedAt:    now.Add(-time.Hour),
		})
	}

	a := tr.Score("src-A")
	b := tr.Score("src-B")
	miss := tr.Score("src-missing")

	if a.Accuracy <= b.Accuracy {
		t.Errorf("A=%v, B=%v: A should outrank B", a, b)
	}
	if miss.Observations != 0 || miss.Accuracy != 0 {
		t.Errorf("missing source: %+v, want zero", miss)
	}
}
