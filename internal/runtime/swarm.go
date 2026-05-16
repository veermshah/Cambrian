package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/agent/tasks"
	"github.com/veermshah/cambrian/internal/db"
	"github.com/veermshah/cambrian/internal/llm"
	"github.com/veermshah/cambrian/internal/redis"
)

// SwarmStore is the DB surface SwarmRuntime needs at startup and for
// lifecycle event reactions. *db.Queries satisfies it.
type SwarmStore interface {
	ListActiveAgents(ctx context.Context) ([]db.Agent, error)
	NodeStore
}

// LLMRegistry resolves a model id to an LLMClient. internal/llm exposes
// a package-level Get function; this interface exists so the runtime
// can be tested without setting global state.
type LLMRegistry interface {
	Get(model string) (llm.LLMClient, error)
}

// SwarmRuntime owns the map[agent_id]*NodeRunner, holds the shared
// Scheduler and store, and listens for lifecycle:* events on Redis to
// add/remove runners while the swarm is live.
//
// Spec line 487 describes the per-epoch evolution loop the root
// orchestrator owns (chunk 21). This struct is the goroutine substrate
// that loop drives: when the orchestrator decides "spawn agent X",
// it publishes lifecycle:spawn and we wire up the NodeRunner.
type SwarmRuntime struct {
	store     SwarmStore
	llms      LLMRegistry
	scheduler *Scheduler
	bus       redis.Client
	log       agent.Logger
	clock     Clock

	mu      sync.Mutex
	runners map[string]*runnerHandle
	staged  int // number of nodes started this lifetime (drives stagger)

	monitorInterval    time.Duration
	heartbeatInterval  time.Duration
	strategistInterval time.Duration
}

// runnerHandle is the per-agent tuple the swarm tracks. cancel stops the
// runner; the underlying NodeRunner is exposed for test introspection.
type runnerHandle struct {
	runner *NodeRunner
	cancel context.CancelFunc
	done   chan struct{}
}

// SwarmConfig assembles the dependencies SwarmRuntime needs. Intervals
// may be zero — NodeRunner applies its own defaults.
type SwarmConfig struct {
	Store              SwarmStore
	LLMs               LLMRegistry
	Scheduler          *Scheduler
	Bus                redis.Client
	Log                agent.Logger
	Clock              Clock
	MonitorInterval    time.Duration
	HeartbeatInterval  time.Duration
	StrategistInterval time.Duration
}

// NewSwarm builds an unstarted SwarmRuntime. Run starts the lifecycle
// subscriber and blocks; the caller usually invokes LoadAndStart first
// to materialize already-active agents from Postgres.
func NewSwarm(cfg SwarmConfig) (*SwarmRuntime, error) {
	if cfg.Store == nil {
		return nil, errors.New("swarm: store required")
	}
	if cfg.LLMs == nil {
		return nil, errors.New("swarm: llm registry required")
	}
	if cfg.Scheduler == nil {
		cfg.Scheduler = NewScheduler(0, 0)
	}
	if cfg.Log == nil {
		cfg.Log = agent.NopLogger{}
	}
	if cfg.Clock == nil {
		cfg.Clock = RealClock{}
	}
	return &SwarmRuntime{
		store:              cfg.Store,
		llms:               cfg.LLMs,
		scheduler:          cfg.Scheduler,
		bus:                cfg.Bus,
		log:                cfg.Log,
		clock:              cfg.Clock,
		runners:            map[string]*runnerHandle{},
		monitorInterval:    cfg.MonitorInterval,
		heartbeatInterval:  cfg.HeartbeatInterval,
		strategistInterval: cfg.StrategistInterval,
	}, nil
}

// LoadAndStart pulls every status='active' agent and spawns a
// NodeRunner for each. Returns the number of runners started. Errors
// from individual agents are logged; the swarm keeps starting the rest.
func (s *SwarmRuntime) LoadAndStart(ctx context.Context) (int, error) {
	rows, err := s.store.ListActiveAgents(ctx)
	if err != nil {
		return 0, fmt.Errorf("swarm: list active agents: %w", err)
	}
	started := 0
	for _, row := range rows {
		if err := s.startFromRow(ctx, row); err != nil {
			s.log.Warnw("swarm_start_agent_failed",
				"agent_id", row.ID, "error", err)
			continue
		}
		started++
	}
	return started, nil
}

// Run subscribes to lifecycle channels and dispatches events to the
// matching handler. Blocks until ctx is cancelled. The lifecycle:spawn
// handler re-reads the agent row before starting (cheap and avoids a
// race where the publisher published a stale view).
func (s *SwarmRuntime) Run(ctx context.Context) error {
	if s.bus == nil {
		<-ctx.Done()
		s.stopAll()
		return nil
	}
	channels := []string{
		ChannelLifecycleSpawn,
		ChannelLifecycleKill,
		ChannelLifecyclePause,
		ChannelLifecycleResume,
	}
	msgs, err := s.bus.Subscribe(ctx, channels...)
	if err != nil {
		return fmt.Errorf("swarm: subscribe: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			s.stopAll()
			return nil
		case m, ok := <-msgs:
			if !ok {
				s.stopAll()
				return nil
			}
			s.dispatch(ctx, m)
		}
	}
}

func (s *SwarmRuntime) dispatch(ctx context.Context, m redis.Message) {
	var ev LifecycleEvent
	if err := json.Unmarshal(m.Payload, &ev); err != nil {
		s.log.Warnw("swarm_bad_lifecycle_payload",
			"channel", m.Channel, "error", err)
		return
	}
	if ev.AgentID == "" {
		return
	}
	switch m.Channel {
	case ChannelLifecycleSpawn:
		if err := s.spawnByID(ctx, ev.AgentID); err != nil {
			s.log.Warnw("swarm_spawn_failed",
				"agent_id", ev.AgentID, "error", err)
		}
	case ChannelLifecycleKill:
		s.stop(ev.AgentID)
	case ChannelLifecyclePause:
		s.withRunner(ev.AgentID, func(h *runnerHandle) { h.runner.Pause() })
	case ChannelLifecycleResume:
		s.withRunner(ev.AgentID, func(h *runnerHandle) { h.runner.Resume() })
	}
}

// spawnByID re-fetches the agent row by id (via ListActiveAgents, which
// is cheap on devnet scale) and starts a runner. If a runner already
// exists, this is a no-op.
func (s *SwarmRuntime) spawnByID(ctx context.Context, agentID string) error {
	s.mu.Lock()
	if _, exists := s.runners[agentID]; exists {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	rows, err := s.store.ListActiveAgents(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ID == agentID {
			return s.startFromRow(ctx, row)
		}
	}
	return fmt.Errorf("agent %s not found among active", agentID)
}

// startFromRow builds a Task + Bandit + NodeRunner from the DB row and
// starts its loops in a background goroutine. Caller-side cancellation
// happens through stop or stopAll.
func (s *SwarmRuntime) startFromRow(parent context.Context, row db.Agent) error {
	if row.ID == "" {
		return errors.New("agent row missing id")
	}
	task, err := tasks.Build(parent, row.TaskType, row.StrategyConfig)
	if err != nil {
		return fmt.Errorf("build task: %w", err)
	}
	client, err := s.llms.Get(row.StrategistModel)
	if err != nil {
		return fmt.Errorf("resolve llm: %w", err)
	}
	genome := agent.AgentGenome{
		Name:                      row.Name,
		Generation:                row.Generation,
		LineageDepth:              row.LineageDepth,
		TaskType:                  row.TaskType,
		Chain:                     row.Chain,
		StrategistPrompt:          row.StrategistPrompt,
		StrategistModel:           row.StrategistModel,
		StrategistIntervalSeconds: row.StrategistIntervalSeconds,
		CapitalAllocation:         row.CapitalAllocated,
	}
	var bandit *agent.TickBandit
	if len(row.BanditPolicies) > 0 {
		var names []string
		if err := json.Unmarshal(row.BanditPolicies, &names); err == nil && len(names) > 0 {
			bandit = agent.NewTickBandit(names)
		}
	}

	cfg := NodeRunnerConfig{
		AgentID:            row.ID,
		Genome:             genome,
		Task:               task,
		LLM:                client,
		Bandit:             bandit,
		Store:              s.store,
		Scheduler:          s.scheduler,
		Log:                s.log,
		MonitorInterval:    s.monitorInterval,
		StrategistInterval: s.strategistInterval,
		HeartbeatInterval:  s.heartbeatInterval,
		Clock:              s.clock,
	}
	nr, err := NewNodeRunner(cfg)
	if err != nil {
		return fmt.Errorf("new runner: %w", err)
	}

	s.mu.Lock()
	offset := s.scheduler.StaggerOffset(s.staged)
	s.staged++
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	handle := &runnerHandle{runner: nr, cancel: cancel, done: done}
	s.runners[row.ID] = handle
	s.mu.Unlock()

	go func() {
		defer close(done)
		if offset > 0 {
			select {
			case <-ctx.Done():
				return
			case <-s.clock.After(offset):
			}
		}
		if err := nr.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warnw("noderunner_exited", "agent_id", row.ID, "error", err)
		}
	}()
	return nil
}

// stop cancels a runner by id and waits briefly for the goroutine to
// exit so CloseAllPositions has a chance to flush.
func (s *SwarmRuntime) stop(agentID string) {
	s.mu.Lock()
	h, ok := s.runners[agentID]
	if ok {
		delete(s.runners, agentID)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(15 * time.Second):
		s.log.Warnw("noderunner_stop_timeout", "agent_id", agentID)
	}
}

// stopAll cancels every runner concurrently and waits for shutdown.
func (s *SwarmRuntime) stopAll() {
	s.mu.Lock()
	handles := make([]*runnerHandle, 0, len(s.runners))
	for id, h := range s.runners {
		handles = append(handles, h)
		delete(s.runners, id)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, h := range handles {
		h.cancel()
		wg.Add(1)
		go func(h *runnerHandle) {
			defer wg.Done()
			select {
			case <-h.done:
			case <-time.After(15 * time.Second):
			}
		}(h)
	}
	wg.Wait()
}

// withRunner runs f under the swarm lock so the runner pointer can't
// race with a concurrent kill.
func (s *SwarmRuntime) withRunner(agentID string, f func(*runnerHandle)) {
	s.mu.Lock()
	h, ok := s.runners[agentID]
	s.mu.Unlock()
	if !ok {
		return
	}
	f(h)
}

// RunnerCount returns the current number of active runners. Test
// introspection.
func (s *SwarmRuntime) RunnerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runners)
}

// Runner returns the NodeRunner for an agent id, or nil if absent.
// Test introspection.
func (s *SwarmRuntime) Runner(agentID string) *NodeRunner {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.runners[agentID]; ok {
		return h.runner
	}
	return nil
}
