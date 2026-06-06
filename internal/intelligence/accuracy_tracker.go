package intelligence

import (
	"sort"
	"sync"
	"time"
)

// SignalOutcome is one observation recorded by the orchestrator after
// an epoch: did the source agent's signal turn out to match reality?
//
// Spec line 388: each source's accuracy is tracked over rolling 30-day
// windows; consumers weight by source accuracy → emergent authority.
type SignalOutcome struct {
	SourceAgentID string
	SignalType    string
	// ObservedAt is when the signal was published; the rolling window
	// is anchored on this timestamp (not the verification timestamp)
	// so a single observation moves out of the window exactly 30 days
	// after the signal fired.
	ObservedAt time.Time
	// Correct is whether the signal's predicted direction matched the
	// realized outcome.
	Correct bool
}

// SourceAccuracy is the tracker's per-agent summary, reported by Score.
type SourceAccuracy struct {
	SourceAgentID string
	Observations  int
	Correct       int
	Accuracy      float64 // Correct / Observations; 0 when Observations == 0
}

// AccuracyTracker maintains a rolling-window record of signal outcomes
// per source agent. The window is configurable but defaults to 30 days
// (spec line 388). Concurrent-safe.
//
// Storage is in-memory by design: the production wiring (chunk 23+) can
// replay observations from the `signal_outcomes` table on startup and
// then ingest new ones live. Holding ~10k outcomes in RAM is trivial.
type AccuracyTracker struct {
	mu       sync.RWMutex
	window   time.Duration
	now      func() time.Time
	bySource map[string][]SignalOutcome
}

// DefaultAccuracyWindow is the 30-day rolling window from spec line 388.
const DefaultAccuracyWindow = 30 * 24 * time.Hour

// NewAccuracyTracker constructs a tracker with the given window. Zero
// duration ⇒ DefaultAccuracyWindow.
func NewAccuracyTracker(window time.Duration) *AccuracyTracker {
	if window <= 0 {
		window = DefaultAccuracyWindow
	}
	return &AccuracyTracker{
		window:   window,
		now:      time.Now,
		bySource: make(map[string][]SignalOutcome),
	}
}

// withClock is a test seam — production code should not touch it.
func (t *AccuracyTracker) withClock(fn func() time.Time) *AccuracyTracker {
	t.now = fn
	return t
}

// Ingest records one outcome. Outcomes outside the current window are
// dropped immediately to keep memory bounded — pre-window data is
// useless for the rolling score anyway.
func (t *AccuracyTracker) Ingest(o SignalOutcome) {
	if o.SourceAgentID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := t.now().Add(-t.window)
	// Strict: observations exactly at the cutoff are evicted. This makes
	// the window "everything within the last <window>" — a 30-day window
	// excludes the 30-days-ago boundary.
	if !o.ObservedAt.After(cutoff) {
		return
	}
	t.bySource[o.SourceAgentID] = append(t.bySource[o.SourceAgentID], o)
	t.evictExpiredLocked(o.SourceAgentID, cutoff)
}

// Score returns the rolling accuracy for one source. Missing source ⇒
// zero values (Observations=0, Accuracy=0).
func (t *AccuracyTracker) Score(sourceAgentID string) SourceAccuracy {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := t.now().Add(-t.window)
	t.evictExpiredLocked(sourceAgentID, cutoff)
	obs := t.bySource[sourceAgentID]
	out := SourceAccuracy{SourceAgentID: sourceAgentID, Observations: len(obs)}
	for _, o := range obs {
		if o.Correct {
			out.Correct++
		}
	}
	if out.Observations > 0 {
		out.Accuracy = float64(out.Correct) / float64(out.Observations)
	}
	return out
}

// AllScores returns the score for every source seen in the current
// window, sorted descending by Accuracy then descending by
// Observations. Ties resolve by SourceAgentID ascending for
// determinism.
func (t *AccuracyTracker) AllScores() []SourceAccuracy {
	t.mu.Lock()
	cutoff := t.now().Add(-t.window)
	sources := make([]string, 0, len(t.bySource))
	for s := range t.bySource {
		t.evictExpiredLocked(s, cutoff)
		if len(t.bySource[s]) > 0 {
			sources = append(sources, s)
		}
	}
	t.mu.Unlock()
	out := make([]SourceAccuracy, 0, len(sources))
	for _, s := range sources {
		out = append(out, t.Score(s))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Accuracy != out[j].Accuracy {
			return out[i].Accuracy > out[j].Accuracy
		}
		if out[i].Observations != out[j].Observations {
			return out[i].Observations > out[j].Observations
		}
		return out[i].SourceAgentID < out[j].SourceAgentID
	})
	return out
}

// Weight returns the consuming-agent signal weight for one source.
// Spec line 388-390: consuming agents weight by source accuracy. Until
// a source has reached MinObservations it is treated as neutral (weight
// 0.5) rather than fully trusted/distrusted — this prevents brand-new
// agents from dominating the bus or being silenced before they have a
// chance to be evaluated.
const MinObservations = 5

func (t *AccuracyTracker) Weight(sourceAgentID string) float64 {
	s := t.Score(sourceAgentID)
	if s.Observations < MinObservations {
		return 0.5
	}
	return s.Accuracy
}

// evictExpiredLocked drops outcomes older than cutoff from one source's
// slice. Caller must hold t.mu.Lock().
func (t *AccuracyTracker) evictExpiredLocked(source string, cutoff time.Time) {
	obs := t.bySource[source]
	if len(obs) == 0 {
		return
	}
	// Fast path: the slice is usually inserted-in-order, so a single
	// linear scan trims the head.
	i := 0
	for i < len(obs) && !obs[i].ObservedAt.After(cutoff) {
		i++
	}
	if i == 0 {
		return
	}
	remaining := obs[i:]
	if len(remaining) == 0 {
		delete(t.bySource, source)
		return
	}
	t.bySource[source] = append([]SignalOutcome(nil), remaining...)
}
