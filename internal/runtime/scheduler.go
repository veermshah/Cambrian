// Package runtime is the goroutine-level orchestration layer for the
// swarm: per-agent NodeRunner with monitor / strategist / heartbeat
// loops, a SwarmRuntime that owns the map of running nodes, a
// LifecycleManager that drives spawn/kill/pause/promote transitions,
// and a Scheduler that staggers cold-start ticks and caps concurrent
// LLM calls.
//
// Spec references:
//   - lines 437–481: NodeRunner shape and three-loop cadence
//   - lines 165–185: strategist contract
//   - lines 514–528: kill/pause rules (the LifecycleManager honors these
//     by writing status + emitting lifecycle events; the root
//     orchestrator in chunk 21 is the one that decides when to call us)
package runtime

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sync/semaphore"
)

// Scheduler holds the cross-node concerns that don't belong inside any
// individual NodeRunner: a stagger offset so a fresh-boot swarm doesn't
// stampede the chain RPC endpoint with N simultaneous first ticks, and
// a weighted semaphore that caps concurrent strategist LLM calls
// (Upstash + Anthropic both rate-limit hard; the swarm pays for that
// limit in real money).
type Scheduler struct {
	staggerStep time.Duration
	strategist  *semaphore.Weighted
	maxLLM      int64
}

// NewScheduler builds a Scheduler with the given concurrency cap on
// strategist LLM calls. staggerStep is the per-node delay used by
// StaggerOffset; pass 0 to default to 500ms.
func NewScheduler(maxConcurrentLLM int, staggerStep time.Duration) *Scheduler {
	if maxConcurrentLLM <= 0 {
		maxConcurrentLLM = 4
	}
	if staggerStep <= 0 {
		staggerStep = 500 * time.Millisecond
	}
	return &Scheduler{
		staggerStep: staggerStep,
		strategist:  semaphore.NewWeighted(int64(maxConcurrentLLM)),
		maxLLM:      int64(maxConcurrentLLM),
	}
}

// StaggerOffset returns the cold-start delay for the i-th node in the
// swarm. i*step keeps the first tick of each node spaced out so the
// chain client connection pool and any per-request rate limit don't
// saturate on boot.
func (s *Scheduler) StaggerOffset(i int) time.Duration {
	if s == nil || i < 0 {
		return 0
	}
	return time.Duration(i) * s.staggerStep
}

// AcquireStrategist blocks until a strategist slot is available or ctx
// is cancelled. Release with ReleaseStrategist exactly once per
// successful Acquire.
func (s *Scheduler) AcquireStrategist(ctx context.Context) error {
	if s == nil {
		return errors.New("scheduler: nil receiver")
	}
	return s.strategist.Acquire(ctx, 1)
}

// ReleaseStrategist returns a slot to the strategist pool.
func (s *Scheduler) ReleaseStrategist() {
	if s == nil {
		return
	}
	s.strategist.Release(1)
}

// MaxStrategistConcurrency returns the configured cap (mostly useful
// for tests and dashboard reporting).
func (s *Scheduler) MaxStrategistConcurrency() int64 {
	if s == nil {
		return 0
	}
	return s.maxLLM
}
