package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/agent/tasks"
	"github.com/veermshah/cambrian/internal/db"
	"github.com/veermshah/cambrian/internal/llm"
	"golang.org/x/sync/errgroup"
)

// Clock is the small time surface NodeRunner depends on. The runtime
// uses real time in production; tests inject a fake clock so cadence
// assertions don't sleep.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
	After(d time.Duration) <-chan time.Time
}

// Ticker mirrors time.Ticker so a fake clock can drive deterministic
// ticks in tests.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// RealClock is the production Clock. Wrap of time.* primitives.
type RealClock struct{}

func (RealClock) Now() time.Time                       { return time.Now() }
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (RealClock) NewTicker(d time.Duration) Ticker      { return realTicker{t: time.NewTicker(d)} }

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

// NodeRunnerConfig assembles every dependency a NodeRunner needs. The
// SwarmRuntime builds one of these per agent from the DB row + shared
// services (LLM registry, Scheduler, store).
type NodeRunnerConfig struct {
	AgentID   string
	Genome    agent.AgentGenome
	Task      tasks.Task
	LLM       llm.LLMClient
	Bandit    *agent.TickBandit
	Store     NodeStore
	Scheduler *Scheduler
	Log       agent.Logger

	// MonitorInterval is the fast-loop tick cadence. Zero ⇒ 30s
	// (spec doesn't pin a number; tasks themselves throttle internally).
	MonitorInterval time.Duration
	// StrategistInterval is the slow-loop cadence. Zero ⇒
	// Genome.StrategistIntervalSeconds. If that's also zero ⇒ 4h.
	StrategistInterval time.Duration
	// HeartbeatInterval is the heartbeat cadence. Zero ⇒ 30s.
	HeartbeatInterval time.Duration
	// MaxOutputTokens caps the strategist's LLM response. Zero ⇒ 1024.
	MaxOutputTokens int

	Clock Clock
}

// NodeStore is the DB surface a NodeRunner needs. Production: *db.Queries.
// Tests pass a fake. Kept narrow so the runtime stays unit-testable.
type NodeStore interface {
	LogTrade(ctx context.Context, t db.Trade) error
	LogStrategistDecision(ctx context.Context, d db.StrategistDecision) error
	UpdateHeartbeat(ctx context.Context, agentID string) error
}

// NodeRunner owns the three loops that keep one agent alive: a monitor
// (Task.RunTick), a strategist (LLM → ApplyAdjustments), and a
// heartbeat (UpdateHeartbeat). Each runs in its own errgroup goroutine
// so a panic or a slow call on one loop never starves the others.
//
// State transitions (pause/resume/kill) are not driven from inside the
// runner — the LifecycleManager updates DB rows and the SwarmRuntime
// reacts by stopping or starting NodeRunners.
type NodeRunner struct {
	cfg NodeRunnerConfig

	// runtime state
	paused     atomic.Bool
	tickCount  atomic.Int64
	stratCount atomic.Int64
	beatCount  atomic.Int64
}

// NewNodeRunner validates the config and returns a runner that hasn't
// started yet. Call Run(ctx) to start the loops; cancel ctx to stop.
func NewNodeRunner(cfg NodeRunnerConfig) (*NodeRunner, error) {
	if cfg.AgentID == "" {
		return nil, errors.New("noderunner: agent id required")
	}
	if cfg.Task == nil {
		return nil, errors.New("noderunner: task required")
	}
	if cfg.LLM == nil {
		return nil, errors.New("noderunner: llm client required")
	}
	if cfg.Store == nil {
		return nil, errors.New("noderunner: store required")
	}
	if cfg.MonitorInterval == 0 {
		cfg.MonitorInterval = 30 * time.Second
	}
	if cfg.StrategistInterval == 0 {
		if secs := cfg.Genome.StrategistIntervalSeconds; secs > 0 {
			cfg.StrategistInterval = time.Duration(secs) * time.Second
		} else {
			cfg.StrategistInterval = 4 * time.Hour
		}
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.MaxOutputTokens <= 0 {
		cfg.MaxOutputTokens = 1024
	}
	if cfg.Clock == nil {
		cfg.Clock = RealClock{}
	}
	if cfg.Log == nil {
		cfg.Log = agent.NopLogger{}
	}
	return &NodeRunner{cfg: cfg}, nil
}

// Pause halts the monitor + strategist loops (heartbeat keeps writing
// so an operator can tell the goroutine is alive but idle). Idempotent.
func (n *NodeRunner) Pause() { n.paused.Store(true) }

// Resume reverses Pause. Idempotent.
func (n *NodeRunner) Resume() { n.paused.Store(false) }

// IsPaused reports the current paused state.
func (n *NodeRunner) IsPaused() bool { return n.paused.Load() }

// TickCount returns how many monitor ticks have fired. Test introspection.
func (n *NodeRunner) TickCount() int64 { return n.tickCount.Load() }

// StrategistCount returns how many strategist cycles have completed.
func (n *NodeRunner) StrategistCount() int64 { return n.stratCount.Load() }

// HeartbeatCount returns how many heartbeats have been written.
func (n *NodeRunner) HeartbeatCount() int64 { return n.beatCount.Load() }

// Run starts the three loops and blocks until ctx is cancelled or any
// loop returns a fatal error. Use a stagger offset (Scheduler.StaggerOffset)
// before calling Run if N nodes are starting at once.
func (n *NodeRunner) Run(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return n.monitorLoop(gctx) })
	g.Go(func() error { return n.strategistLoop(gctx) })
	g.Go(func() error { return n.heartbeatLoop(gctx) })
	err := g.Wait()
	// On shutdown, ask the task to flatten positions. Use a fresh
	// background context with a short budget — the parent ctx is
	// already cancelled, so passing it would no-op every chain call.
	if errors.Is(err, context.Canceled) || err == nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, closeErr := n.cfg.Task.CloseAllPositions(closeCtx); closeErr != nil {
			n.cfg.Log.Warnw("noderunner_close_positions_failed",
				"agent_id", n.cfg.AgentID, "error", closeErr)
		}
	}
	return err
}

// monitorLoop calls Task.RunTick on cadence. Tick output is persisted
// via Store.LogTrade. Pause flag skips the tick but keeps the timer
// running (so resume picks up at the next tick boundary, not late).
func (n *NodeRunner) monitorLoop(ctx context.Context) error {
	tk := n.cfg.Clock.NewTicker(n.cfg.MonitorInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C():
			if n.paused.Load() {
				continue
			}
			n.tickCount.Add(1)
			trades, err := n.cfg.Task.RunTick(ctx)
			if err != nil {
				n.cfg.Log.Warnw("monitor_tick_failed",
					"agent_id", n.cfg.AgentID, "error", err)
				continue
			}
			for _, t := range trades {
				row := db.Trade{
					AgentID:          n.cfg.AgentID,
					Chain:            t.Chain,
					TradeType:        t.TradeType,
					TokenPair:        t.TokenPair,
					DEX:              t.DEX,
					AmountIn:         t.AmountIn,
					AmountOut:        t.AmountOut,
					FeePaid:          t.FeePaid,
					PnL:              t.PnL,
					TxSignature:      t.TxSignature,
					IsPaperTrade:     t.IsPaperTrade,
					BanditPolicyUsed: t.BanditPolicyUsed,
					Metadata:         marshalMetadata(t.Metadata),
				}
				if err := n.cfg.Store.LogTrade(ctx, row); err != nil {
					n.cfg.Log.Warnw("monitor_log_trade_failed",
						"agent_id", n.cfg.AgentID, "error", err)
				}
			}
		}
	}
}

// strategistLoop fires one Strategist.Run per slow tick. The scheduler
// semaphore caps swarm-wide concurrency so a 50-agent swarm can't
// blast 50 simultaneous LLM calls when their clocks align.
func (n *NodeRunner) strategistLoop(ctx context.Context) error {
	tk := n.cfg.Clock.NewTicker(n.cfg.StrategistInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C():
			if n.paused.Load() {
				continue
			}
			if err := n.runStrategistOnce(ctx); err != nil {
				n.cfg.Log.Warnw("strategist_cycle_failed",
					"agent_id", n.cfg.AgentID, "error", err)
			}
		}
	}
}

// runStrategistOnce is the per-tick body, factored out so tests can
// drive it without spinning up the loop.
func (n *NodeRunner) runStrategistOnce(ctx context.Context) error {
	if n.cfg.Scheduler != nil {
		if err := n.cfg.Scheduler.AcquireStrategist(ctx); err != nil {
			return fmt.Errorf("acquire strategist slot: %w", err)
		}
		defer n.cfg.Scheduler.ReleaseStrategist()
	}
	s := &agent.Strategist{
		AgentID:         n.cfg.AgentID,
		AgentName:       n.cfg.Genome.Name,
		Genome:          n.cfg.Genome,
		LLM:             n.cfg.LLM,
		Task:            n.cfg.Task,
		Bandit:          n.cfg.Bandit,
		Store:           n.cfg.Store,
		Log:             n.cfg.Log,
		MaxOutputTokens: n.cfg.MaxOutputTokens,
	}
	err := s.Run(ctx)
	n.stratCount.Add(1)
	return err
}

// heartbeatLoop writes agents.last_heartbeat_at on cadence. Independent
// of the pause flag so an operator can tell a paused-but-alive goroutine
// apart from a stuck/crashed one.
func (n *NodeRunner) heartbeatLoop(ctx context.Context) error {
	tk := n.cfg.Clock.NewTicker(n.cfg.HeartbeatInterval)
	defer tk.Stop()
	// Write one immediate heartbeat on start so the dashboard shows the
	// node alive without waiting a full interval.
	if err := n.cfg.Store.UpdateHeartbeat(ctx, n.cfg.AgentID); err == nil {
		n.beatCount.Add(1)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C():
			if err := n.cfg.Store.UpdateHeartbeat(ctx, n.cfg.AgentID); err != nil {
				n.cfg.Log.Warnw("heartbeat_failed",
					"agent_id", n.cfg.AgentID, "error", err)
				continue
			}
			n.beatCount.Add(1)
		}
	}
}

func marshalMetadata(m map[string]interface{}) []byte {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}
