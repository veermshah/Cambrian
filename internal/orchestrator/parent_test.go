package orchestrator

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/llm"
)

// ---------- fakes ----------

type fakeEpochStore struct {
	mu                  sync.Mutex
	state               EpochState
	stateErr            error
	persistedLedgers    []Ledger
	persistedSweeps     []SweepDecision
	persistedOffspring  []OffspringDecision
	persistedPostmortems []PostmortemResult
	loggedEpochs        []EpochResult
}

func (f *fakeEpochStore) LoadEpochState(_ context.Context, _ string) (EpochState, error) {
	return f.state, f.stateErr
}
func (f *fakeEpochStore) PersistLedger(_ context.Context, l Ledger) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.persistedLedgers = append(f.persistedLedgers, l)
	return nil
}
func (f *fakeEpochStore) PersistSweep(_ context.Context, d SweepDecision) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.persistedSweeps = append(f.persistedSweeps, d)
	return nil
}
func (f *fakeEpochStore) PersistOffspringDecision(_ context.Context, d OffspringDecision) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.persistedOffspring = append(f.persistedOffspring, d)
	return nil
}
func (f *fakeEpochStore) PersistPostmortem(_ context.Context, p PostmortemResult, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.persistedPostmortems = append(f.persistedPostmortems, p)
	return nil
}
func (f *fakeEpochStore) LogEpoch(_ context.Context, r EpochResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loggedEpochs = append(f.loggedEpochs, r)
	return nil
}

type fakeLifecycle struct {
	mu      sync.Mutex
	kills   []string
	pauses  []string
	resumes []string
}

func (f *fakeLifecycle) Kill(_ context.Context, agentID, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kills = append(f.kills, agentID)
	return nil
}
func (f *fakeLifecycle) Pause(_ context.Context, agentID, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauses = append(f.pauses, agentID)
	return nil
}
func (f *fakeLifecycle) Resume(_ context.Context, agentID, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes = append(f.resumes, agentID)
	return nil
}

type fakeBreaker struct {
	halted  bool
	tripped string
}

func (f *fakeBreaker) Halted() bool { return f.halted }
func (f *fakeBreaker) EvaluateEpoch(o EpochBreakerOutcome) string {
	if o.FundedNodes > 0 && float64(o.NodesHitStopLoss)/float64(o.FundedNodes) >= 0.5 {
		f.tripped = "mass_stop_out"
		f.halted = true
		return "mass_stop_out"
	}
	return ""
}

type fakeBus struct {
	mu        sync.Mutex
	published []string
}

func (f *fakeBus) Publish(_ context.Context, channel string, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, channel)
	return nil
}

type fakeBacktest struct {
	pass bool
	err  error
}

func (f *fakeBacktest) Run(_ context.Context, _ agent.AgentGenome) (bool, error) {
	return f.pass, f.err
}

// ---------- helpers ----------

func minimalConfig(store *fakeEpochStore, lc *fakeLifecycle, br *fakeBreaker, bus *fakeBus, llmClient llm.LLMClient) RootOrchestratorConfig {
	return RootOrchestratorConfig{
		Store:     store,
		Lifecycle: lc,
		Breaker:   br,
		Bus:       bus,
		LLM:       llmClient,
		Clock:     func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) },
		RNG:       func() *rand.Rand { return rand.New(rand.NewPCG(1, 0)) },
	}
}

func parentGenome() agent.AgentGenome {
	return agent.AgentGenome{
		Name:             "parent-1",
		Generation:       3,
		TaskType:         "cross_chain_yield",
		Chain:            "solana",
		StrategistModel:  "claude-haiku-4-5-20251001",
		StrategistPrompt: "you are a strategist",
	}
}

// ---------- tests ----------

func TestRootOrchestrator_NewValidatesConfig(t *testing.T) {
	_, err := NewRootOrchestrator(RootOrchestratorConfig{})
	if err == nil {
		t.Error("expected error for empty config")
	}
}

func TestRootOrchestrator_RunEpoch_HealthyState(t *testing.T) {
	store := &fakeEpochStore{
		state: EpochState{
			EpochID: "ep-1",
			Snapshots: []AgentSnapshot{{
				AgentID: "agent-1", Status: "active", BalanceUSD: 100, NetProfitWindowUSD: 5.0,
				LastStrategistVerdict: "continue", NodeClass: "funded",
				LastHeartbeat: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).Add(-1 * time.Minute),
			}},
			Genomes:      map[string]agent.AgentGenome{"agent-1": parentGenome()},
			SwarmGenomes: []agent.AgentGenome{parentGenome()},
		},
	}
	lc := &fakeLifecycle{}
	br := &fakeBreaker{}
	bus := &fakeBus{}
	cli := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse(`{"category":"strategy_drift","summary":"x","diagnosis":"y"}`).
		WithTokenUsage(10, 5)
	r, _ := NewRootOrchestrator(minimalConfig(store, lc, br, bus, cli))
	out, err := r.RunEpoch(context.Background(), "ep-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.LifecycleActions) != 0 {
		t.Errorf("healthy state should yield no actions, got %+v", out.LifecycleActions)
	}
	if len(store.loggedEpochs) != 1 {
		t.Errorf("expected 1 epoch logged, got %d", len(store.loggedEpochs))
	}
	if len(bus.published) != 1 || bus.published[0] != "events:epoch_completed" {
		t.Errorf("expected events:epoch_completed publish, got %v", bus.published)
	}
}

func TestRootOrchestrator_KilledAgentGetsPostmortem(t *testing.T) {
	store := &fakeEpochStore{
		state: EpochState{
			EpochID: "ep-1",
			Snapshots: []AgentSnapshot{{
				AgentID: "dying-1", Status: "active", BalanceUSD: 0.1, // below minimum
				LastHeartbeat: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).Add(-1 * time.Minute),
				NodeClass:     "funded",
			}},
			Genomes:      map[string]agent.AgentGenome{"dying-1": parentGenome()},
			SwarmGenomes: []agent.AgentGenome{parentGenome()},
		},
	}
	lc := &fakeLifecycle{}
	br := &fakeBreaker{}
	bus := &fakeBus{}
	cli := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse(`{"category":"unrecoverable_loss","summary":"balance crashed","diagnosis":"the agent could not recover"}`).
		WithTokenUsage(50, 20)
	r, _ := NewRootOrchestrator(minimalConfig(store, lc, br, bus, cli))
	out, err := r.RunEpoch(context.Background(), "ep-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(lc.kills) != 1 || lc.kills[0] != "dying-1" {
		t.Errorf("expected kill of dying-1, got %v", lc.kills)
	}
	if len(out.Postmortems) != 1 || out.Postmortems[0].Category != LessonUnrecoverableLoss {
		t.Errorf("expected unrecoverable_loss postmortem, got %+v", out.Postmortems)
	}
	if len(store.persistedPostmortems) != 1 {
		t.Errorf("postmortem not persisted, got %d", len(store.persistedPostmortems))
	}
}

func TestRootOrchestrator_OffspringPipeline_InsolventRejected(t *testing.T) {
	cli := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse(`{"verdict":"approve","reasoning":"ok"}`).WithTokenUsage(10, 5)
	store := &fakeEpochStore{
		state: EpochState{
			EpochID:      "ep-1",
			Snapshots:    []AgentSnapshot{}, // skip lifecycle work
			Genomes:      map[string]agent.AgentGenome{},
			SwarmGenomes: []agent.AgentGenome{parentGenome()},
			OffspringProposals: []OffspringProposal{{
				ProposalID: "p1", ParentID: "parent-1",
				Candidate:              parentGenome(),
				ReproductionReserveUSD: 50,
				ParentLedger:           Ledger{RealizedNetProfit: -5}, // negative
			}},
		},
	}
	r, _ := NewRootOrchestrator(minimalConfig(store, &fakeLifecycle{}, &fakeBreaker{}, &fakeBus{}, cli))
	out, _ := r.RunEpoch(context.Background(), "ep-1")
	if len(out.OffspringDecisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(out.OffspringDecisions))
	}
	if out.OffspringDecisions[0].Outcome != OffspringRejected ||
		out.OffspringDecisions[0].RejectTag != "insolvent" {
		t.Errorf("expected insolvent rejection, got %+v", out.OffspringDecisions[0])
	}
}

func TestRootOrchestrator_OffspringPipeline_QualityReject(t *testing.T) {
	cli := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponse(`{"verdict":"reject","reasoning":"near-duplicate"}`).WithTokenUsage(50, 20)
	store := &fakeEpochStore{
		state: EpochState{
			EpochID:      "ep-1",
			Snapshots:    []AgentSnapshot{},
			Genomes:      map[string]agent.AgentGenome{},
			SwarmGenomes: []agent.AgentGenome{parentGenome()},
			OffspringProposals: []OffspringProposal{{
				ProposalID: "p1", ParentID: "parent-1",
				Candidate:              parentGenome(),
				ReproductionReserveUSD: 10,
				ParentLedger:           Ledger{RealizedNetProfit: 100},
			}},
		},
	}
	r, _ := NewRootOrchestrator(minimalConfig(store, &fakeLifecycle{}, &fakeBreaker{}, &fakeBus{}, cli))
	out, _ := r.RunEpoch(context.Background(), "ep-1")
	if out.OffspringDecisions[0].RejectTag != "quality_reject" {
		t.Errorf("expected quality_reject, got %+v", out.OffspringDecisions[0])
	}
}

func TestRootOrchestrator_OffspringPipeline_AdversarialReject(t *testing.T) {
	// Quality approves; adversarial rejects.
	cli := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponseQueue(
			`{"verdict":"approve","reasoning":"good"}`,
			"bull text",
			"bear text",
			`{"verdict":"reject","synthesis":"bear wins"}`,
		).
		WithTokenUsage(50, 20)
	store := &fakeEpochStore{
		state: EpochState{
			EpochID:      "ep-1",
			Snapshots:    []AgentSnapshot{},
			Genomes:      map[string]agent.AgentGenome{},
			SwarmGenomes: []agent.AgentGenome{parentGenome()},
			OffspringProposals: []OffspringProposal{{
				ProposalID: "p1", ParentID: "parent-1",
				Candidate:              parentGenome(),
				ReproductionReserveUSD: 10,
				ParentLedger:           Ledger{RealizedNetProfit: 100},
			}},
		},
	}
	r, _ := NewRootOrchestrator(minimalConfig(store, &fakeLifecycle{}, &fakeBreaker{}, &fakeBus{}, cli))
	out, _ := r.RunEpoch(context.Background(), "ep-1")
	if out.OffspringDecisions[0].RejectTag != "adversarial_reject" {
		t.Errorf("expected adversarial_reject, got %+v", out.OffspringDecisions[0])
	}
}

func TestRootOrchestrator_OffspringPipeline_BacktestFail(t *testing.T) {
	cli := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponseQueue(
			`{"verdict":"approve","reasoning":"good"}`,
			"bull text",
			"bear text",
			`{"verdict":"approve","synthesis":"all clear"}`,
		).
		WithTokenUsage(50, 20)
	cfg := minimalConfig(&fakeEpochStore{
		state: EpochState{
			EpochID:      "ep-1",
			Snapshots:    []AgentSnapshot{},
			Genomes:      map[string]agent.AgentGenome{},
			SwarmGenomes: []agent.AgentGenome{parentGenome()},
			OffspringProposals: []OffspringProposal{{
				ProposalID: "p1", ParentID: "parent-1",
				Candidate:              parentGenome(),
				ReproductionReserveUSD: 10,
				ParentLedger:           Ledger{RealizedNetProfit: 100},
			}},
		},
	}, &fakeLifecycle{}, &fakeBreaker{}, &fakeBus{}, cli)
	cfg.Backtest = &fakeBacktest{pass: false}
	r, _ := NewRootOrchestrator(cfg)
	out, _ := r.RunEpoch(context.Background(), "ep-1")
	if out.OffspringDecisions[0].RejectTag != "backtest_failed" {
		t.Errorf("expected backtest_failed, got %+v", out.OffspringDecisions[0])
	}
}

func TestRootOrchestrator_OffspringPipeline_FullApprove(t *testing.T) {
	cli := llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponseQueue(
			`{"verdict":"approve","reasoning":"good"}`,
			"bull text",
			"bear text",
			`{"verdict":"approve","synthesis":"all clear"}`,
		).
		WithTokenUsage(50, 20)
	cfg := minimalConfig(&fakeEpochStore{
		state: EpochState{
			EpochID:      "ep-1",
			Snapshots:    []AgentSnapshot{},
			Genomes:      map[string]agent.AgentGenome{},
			SwarmGenomes: []agent.AgentGenome{parentGenome()},
			OffspringProposals: []OffspringProposal{{
				ProposalID: "p1", ParentID: "parent-1",
				Candidate:              parentGenome(),
				ReproductionReserveUSD: 10,
				ParentLedger:           Ledger{RealizedNetProfit: 100},
			}},
		},
	}, &fakeLifecycle{}, &fakeBreaker{}, &fakeBus{}, cli)
	cfg.Backtest = &fakeBacktest{pass: true}
	r, _ := NewRootOrchestrator(cfg)
	out, _ := r.RunEpoch(context.Background(), "ep-1")
	if out.OffspringDecisions[0].Outcome != OffspringApproved {
		t.Errorf("expected approved, got %+v", out.OffspringDecisions[0])
	}
}

func TestRootOrchestrator_LoadEpochStateErrorPropagates(t *testing.T) {
	store := &fakeEpochStore{stateErr: errors.New("db down")}
	cli := llm.NewFakeLLMClient("claude-haiku-4-5-20251001")
	r, _ := NewRootOrchestrator(minimalConfig(store, &fakeLifecycle{}, &fakeBreaker{}, &fakeBus{}, cli))
	if _, err := r.RunEpoch(context.Background(), "ep-1"); err == nil {
		t.Error("expected error from store")
	}
}
