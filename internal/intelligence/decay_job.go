package intelligence

import (
	"context"
	"log/slog"
	"time"
)

// DecayJobConfig parameterizes the nightly decay sweep.
//
// In production the orchestrator schedules this once per UTC day. For
// tests, Interval can be reduced so the loop fires quickly.
type DecayJobConfig struct {
	// Graph is the knowledge graph to decay.
	Graph *KnowledgeGraph
	// Interval is how often to run a decay pass. Defaults to 24h.
	Interval time.Duration
	// Now is injected so tests can drive the next-run calculation
	// deterministically. Defaults to time.Now.
	Now func() time.Time
	// Logger receives one structured log line per pass. nil → discarded.
	Logger *slog.Logger
}

// DecayJob is the long-running goroutine that calls Graph.Decay on a
// fixed cadence. It exits when ctx is cancelled.
//
// The decay logic itself is in KnowledgeGraph.Decay (spec line 384:
// edges not validated in 30 days have strength *= 0.95; below 0.1 are
// deleted) — this struct is a thin scheduler so the same decay
// function can also be driven by tests or a one-shot CLI.
type DecayJob struct {
	cfg DecayJobConfig
}

func NewDecayJob(cfg DecayJobConfig) *DecayJob {
	if cfg.Interval <= 0 {
		cfg.Interval = 24 * time.Hour
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &DecayJob{cfg: cfg}
}

// RunOnce performs one decay pass synchronously and returns the
// outcome. Useful for tests, admin tooling, and warm-up on startup.
func (j *DecayJob) RunOnce() (decayed, deleted int) {
	if j == nil || j.cfg.Graph == nil {
		return 0, 0
	}
	decayed, deleted = j.cfg.Graph.Decay()
	if j.cfg.Logger != nil {
		j.cfg.Logger.Info("knowledge_graph.decay",
			"decayed", decayed,
			"deleted", deleted,
			"at", j.cfg.Now().UTC().Format(time.RFC3339),
		)
	}
	return decayed, deleted
}

// Run blocks until ctx is cancelled, firing one decay pass every
// Interval. The first pass fires immediately on Run() — convenient for
// startup warm-up; harmless because Decay is no-op on a fresh graph.
func (j *DecayJob) Run(ctx context.Context) error {
	if j == nil || j.cfg.Graph == nil {
		return nil
	}
	j.RunOnce()
	t := time.NewTicker(j.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			j.RunOnce()
		}
	}
}
