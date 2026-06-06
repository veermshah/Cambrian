package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/llm"
)

// EpochState carries every collection of data RunEpoch needs to do its
// job. The parent orchestrator builds it from db.Queries once per
// epoch; pure-function epoch logic operates on this snapshot.
type EpochState struct {
	EpochID    string
	StartedAt  time.Time
	Snapshots  []AgentSnapshot
	// Genomes maps agent_id → genome for quality check / crossover /
	// postmortem context. Built from the same query that produced
	// Snapshots.
	Genomes map[string]agent.AgentGenome
	// SwarmGenomes is the flat list — same data, but ordered for the
	// diversity score and adversarial review consumers.
	SwarmGenomes []agent.AgentGenome
	// CarriedDebt per agent at epoch start, for Settle.
	CarriedDebt map[string]float64
	// BudgetTight is set by BudgetTracker.State() == Tight.
	BudgetTight bool
	// OffspringProposals is the queue of candidates to evaluate this
	// epoch. Built by chunk 21's reproduction-eligibility query.
	OffspringProposals []OffspringProposal
	// DeadAgents is the list of agent IDs killed earlier this epoch
	// (by the lifecycle policy step) whose postmortems still need
	// running. The orchestrator appends to this as kills happen.
	DeadAgents []PostmortemInput
}

// OffspringProposal is one candidate to consider spawning this epoch.
// Built from healthy parent agents whose ReproductionPolicy allows it.
type OffspringProposal struct {
	ProposalID string
	ParentID   string
	// Candidate is the post-Mutate (and optionally post-Crossover)
	// genome. The parent orchestrator runs Mutate before queuing the
	// proposal.
	Candidate agent.AgentGenome
	// ReproductionReserveUSD must cover the candidate's seed capital +
	// API reserve + failure buffer. Solvency check rejects when the
	// parent doesn't have it.
	ReproductionReserveUSD float64
	// ParentLedger is the parent's latest Ledger row, used by the
	// solvency check.
	ParentLedger Ledger
	// ParentCarriedDebt is the parent's outstanding debt at proposal
	// time. Solvency check rejects when > 0.
	ParentCarriedDebt float64
}

// OffspringDecision is the per-proposal outcome the pipeline writes to
// the `offspring_proposals` table.
type OffspringDecision struct {
	ProposalID string
	ParentID   string
	Outcome    OffspringOutcome
	RejectTag  string  // e.g. "insolvent", "quality_reject", "adversarial_reject", "backtest_failed"
	CostUSD    float64 // sum of quality + adversarial + backtest costs
	Quality    *QualityResult
	Adversarial *AdversarialResult
}

// OffspringOutcome is the closed set of states an offspring proposal
// can land in.
type OffspringOutcome string

const (
	OffspringApproved OffspringOutcome = "approved"
	OffspringRejected OffspringOutcome = "rejected"
	OffspringRevise   OffspringOutcome = "revise"
)

// EpochResult is what RunEpoch returns. The parent orchestrator
// persists each field to the corresponding DB table.
type EpochResult struct {
	EpochID            string
	Ledgers            []Ledger
	Sweeps             []SweepDecision
	LifecycleActions   []LifecycleAction
	OffspringDecisions []OffspringDecision
	Postmortems        []PostmortemResult
	BreakerTripped     bool
	BreakerReason      string
	EndedAt            time.Time
}

// EpochStore is the narrow DB surface RunEpoch needs. The parent
// orchestrator wires *db.Queries to it.
type EpochStore interface {
	LoadEpochState(ctx context.Context, epochID string) (EpochState, error)
	PersistLedger(ctx context.Context, l Ledger) error
	PersistSweep(ctx context.Context, d SweepDecision) error
	PersistOffspringDecision(ctx context.Context, d OffspringDecision) error
	PersistPostmortem(ctx context.Context, p PostmortemResult, agentID string) error
	LogEpoch(ctx context.Context, r EpochResult) error
}

// LifecycleExecutor is the runtime side that actually performs kills /
// pauses / resumes. In production this is runtime.LifecycleManager;
// tests inject a fake that records calls.
type LifecycleExecutor interface {
	Kill(ctx context.Context, agentID, reason string) error
	Pause(ctx context.Context, agentID, reason string) error
	Resume(ctx context.Context, agentID, reason string) error
}

// BreakerControl is the parent orchestrator's view of the circuit
// breaker. Defined here (rather than imported from runtime) so this
// package doesn't depend on runtime — the parent orchestrator's main()
// glue code wires runtime.CircuitBreaker into this interface.
type BreakerControl interface {
	Halted() bool
	EvaluateEpoch(outcome EpochBreakerOutcome) string
}

// EpochBreakerOutcome mirrors runtime.EpochOutcome but lives in this
// package so the interface above doesn't pull in runtime.
type EpochBreakerOutcome struct {
	FundedNodes      int
	NodesHitStopLoss int
	RPCRequests      int
	RPCErrors        int
	WindowDuration   time.Duration
}

// EventBus is the narrow Redis publish surface RunEpoch needs to emit
// `events:epoch_completed`. Chunk 28 will consume these for Telegram.
type EventBus interface {
	Publish(ctx context.Context, channel string, payload []byte) error
}

// BacktestRunner is the chunk-25 surface, stubbed here. Returns true
// when the candidate passes the historical backtest. nil ⇒ skip
// backtesting (default for devnet).
type BacktestRunner interface {
	Run(ctx context.Context, candidate agent.AgentGenome) (passed bool, err error)
}

// RootOrchestratorConfig packages every dependency RunEpoch needs.
// Built once at process start; immutable afterwards.
type RootOrchestratorConfig struct {
	Store              EpochStore
	Lifecycle          LifecycleExecutor
	Breaker            BreakerControl
	Bus                EventBus
	LLM                llm.LLMClient
	Backtest           BacktestRunner // optional
	LifecyclePolicyCfg LifecyclePolicyConfig
	Clock              func() time.Time
	RNG                func() *rand.Rand
	MaxLLMTokens       int
}

// RootOrchestrator is the per-process singleton that runs one epoch at
// a time. Not safe to call RunEpoch concurrently against itself — that
// would conflict on the underlying DB rows.
type RootOrchestrator struct {
	cfg RootOrchestratorConfig
}

// NewRootOrchestrator validates the config and returns an orchestrator
// ready to run.
func NewRootOrchestrator(cfg RootOrchestratorConfig) (*RootOrchestrator, error) {
	if cfg.Store == nil {
		return nil, errors.New("root: store required")
	}
	if cfg.Lifecycle == nil {
		return nil, errors.New("root: lifecycle executor required")
	}
	if cfg.Breaker == nil {
		return nil, errors.New("root: breaker required")
	}
	if cfg.Bus == nil {
		return nil, errors.New("root: event bus required")
	}
	if cfg.LLM == nil {
		return nil, errors.New("root: llm client required")
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.RNG == nil {
		cfg.RNG = func() *rand.Rand { return rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0)) }
	}
	if cfg.MaxLLMTokens <= 0 {
		cfg.MaxLLMTokens = 1024
	}
	return &RootOrchestrator{cfg: cfg}, nil
}

// RunEpoch executes the full per-epoch flow from spec line 487:
//
//   1. gather state              (cfg.Store.LoadEpochState)
//   2. deterministic kill/pause  (DeterministicLifecyclePolicy)
//   3. execute lifecycle actions (cfg.Lifecycle)
//   4. process offspring         (solvency → quality → adversarial → backtest)
//   5. postmortem each killed    (GeneratePostmortem)
//   6. evaluate breaker triggers (cfg.Breaker.EvaluateEpoch)
//   7. log epoch + emit event    (cfg.Store.LogEpoch + cfg.Bus.Publish)
//
// Settlement / sweep / cost attribution are wired by the parent process's
// main loop before RunEpoch fires — by the time RunEpoch sees the
// snapshots, each agent's Ledger is final.
//
// Returns an EpochResult summarizing every action taken. The result is
// also persisted via LogEpoch before return.
func (r *RootOrchestrator) RunEpoch(ctx context.Context, epochID string) (EpochResult, error) {
	startedAt := r.cfg.Clock()
	state, err := r.cfg.Store.LoadEpochState(ctx, epochID)
	if err != nil {
		return EpochResult{}, fmt.Errorf("run epoch %s: load state: %w", epochID, err)
	}
	result := EpochResult{EpochID: epochID}

	// Step: deterministic lifecycle policy. Synchronous so postmortems
	// below see the freshly-killed agents.
	actions := DeterministicLifecyclePolicy(startedAt, r.cfg.LifecyclePolicyCfg, state.Snapshots, state.BudgetTight)
	result.LifecycleActions = actions
	for _, action := range actions {
		if err := r.executeAction(ctx, action); err != nil {
			// Log + continue — one failed lifecycle action shouldn't
			// stop the epoch. The parent orchestrator decides whether
			// to alert.
			continue
		}
		if action.Kind == ActionKill {
			if pm, ok := r.postmortemInputFor(state, action); ok {
				state.DeadAgents = append(state.DeadAgents, pm)
			}
		}
	}

	// Step: offspring proposals. Each proposal runs through the gates
	// in order and rejects on the first failure.
	for _, prop := range state.OffspringProposals {
		decision := r.processOffspring(ctx, state, prop)
		result.OffspringDecisions = append(result.OffspringDecisions, decision)
		_ = r.cfg.Store.PersistOffspringDecision(ctx, decision)
	}

	// Step: postmortems for agents killed this epoch.
	for _, in := range state.DeadAgents {
		pm, err := GeneratePostmortem(ctx, r.cfg.LLM, in, r.cfg.MaxLLMTokens)
		if err != nil {
			continue
		}
		_ = r.cfg.Store.PersistPostmortem(ctx, pm, in.AgentID)
		result.Postmortems = append(result.Postmortems, pm)
	}

	// Step: evaluate circuit breaker.
	reason := r.cfg.Breaker.EvaluateEpoch(r.breakerOutcome(state, actions))
	if reason != "" {
		result.BreakerTripped = true
		result.BreakerReason = reason
	}

	result.EndedAt = r.cfg.Clock()
	if err := r.cfg.Store.LogEpoch(ctx, result); err != nil {
		return result, fmt.Errorf("run epoch %s: log: %w", epochID, err)
	}
	_ = r.cfg.Bus.Publish(ctx, "events:epoch_completed", []byte(epochID))
	return result, nil
}

func (r *RootOrchestrator) executeAction(ctx context.Context, a LifecycleAction) error {
	switch a.Kind {
	case ActionKill:
		return r.cfg.Lifecycle.Kill(ctx, a.AgentID, a.Reason)
	case ActionPause:
		return r.cfg.Lifecycle.Pause(ctx, a.AgentID, a.Reason)
	case ActionResume:
		return r.cfg.Lifecycle.Resume(ctx, a.AgentID, a.Reason)
	default:
		return fmt.Errorf("unknown lifecycle action: %s", a.Kind)
	}
}

// processOffspring runs one proposal through the gates. Spec order:
// solvency → quality check → adversarial review → backtest. Reject on
// first failure with a structured tag so the dashboard can group
// rejections by failure mode.
func (r *RootOrchestrator) processOffspring(ctx context.Context, state EpochState, prop OffspringProposal) OffspringDecision {
	decision := OffspringDecision{ProposalID: prop.ProposalID, ParentID: prop.ParentID}

	// Gate 1: solvency. Parent must have positive net profit, no
	// outstanding debt, and enough reproduction reserve.
	if prop.ParentLedger.RealizedNetProfit <= 0 || prop.ParentCarriedDebt > 0 ||
		prop.ParentLedger.RealizedNetProfit < prop.ReproductionReserveUSD {
		decision.Outcome = OffspringRejected
		decision.RejectTag = "insolvent"
		return decision
	}

	// Gate 2: quality check.
	swarmCtx := SwarmContext{
		DiversityScore:  DiversityScore(state.SwarmGenomes),
		ExistingGenomes: summarizeSwarm(state.SwarmGenomes),
	}
	q, err := QualityCheck(ctx, r.cfg.LLM, prop.Candidate, swarmCtx, r.cfg.MaxLLMTokens)
	if err == nil {
		decision.Quality = &q
		decision.CostUSD += q.CostUSD
		if q.Verdict == VerdictReject {
			decision.Outcome = OffspringRejected
			decision.RejectTag = "quality_reject"
			return decision
		}
	}

	// Gate 3: adversarial review.
	a, err := AdversarialReview(ctx, r.cfg.LLM, prop.Candidate, swarmCtx, r.cfg.MaxLLMTokens)
	if err == nil {
		decision.Adversarial = &a
		decision.CostUSD += a.CostUSD
		if a.Verdict == AdversarialReject {
			decision.Outcome = OffspringRejected
			decision.RejectTag = "adversarial_reject"
			return decision
		}
	}

	// Gate 4: optional backtest stub. Chunk 25 lands the real one.
	if r.cfg.Backtest != nil {
		passed, err := r.cfg.Backtest.Run(ctx, prop.Candidate)
		if err != nil || !passed {
			decision.Outcome = OffspringRejected
			decision.RejectTag = "backtest_failed"
			return decision
		}
	}

	switch {
	case decision.Quality != nil && decision.Quality.Verdict == VerdictRevise,
		decision.Adversarial != nil && decision.Adversarial.Verdict == AdversarialRevise:
		decision.Outcome = OffspringRevise
	default:
		decision.Outcome = OffspringApproved
	}
	return decision
}

func (r *RootOrchestrator) postmortemInputFor(state EpochState, action LifecycleAction) (PostmortemInput, bool) {
	g, ok := state.Genomes[action.AgentID]
	if !ok {
		return PostmortemInput{}, false
	}
	var snap AgentSnapshot
	for _, s := range state.Snapshots {
		if s.AgentID == action.AgentID {
			snap = s
			break
		}
	}
	return PostmortemInput{
		AgentID:              action.AgentID,
		Name:                 g.Name,
		TaskType:             g.TaskType,
		Chain:                g.Chain,
		Model:                g.StrategistModel,
		Generation:           g.Generation,
		KillReason:           action.Reason,
		FinalBalanceUSD:      snap.BalanceUSD,
		FinalDrawdown:        snap.Drawdown,
		LifetimeNetProfitUSD: snap.NetProfitWindowUSD,
		LastStrategistNote:   snap.LastStrategistVerdict,
	}, true
}

func (r *RootOrchestrator) breakerOutcome(state EpochState, actions []LifecycleAction) EpochBreakerOutcome {
	out := EpochBreakerOutcome{WindowDuration: 5 * time.Minute}
	for _, s := range state.Snapshots {
		if s.NodeClass == "funded" {
			out.FundedNodes++
		}
	}
	for _, a := range actions {
		if a.Kind == ActionKill && a.Reason == "max_drawdown_exceeded" {
			out.NodesHitStopLoss++
		}
	}
	return out
}

func summarizeSwarm(swarm []agent.AgentGenome) []GenomeSummary {
	out := make([]GenomeSummary, 0, len(swarm))
	for _, g := range swarm {
		out = append(out, GenomeSummary{
			Name:       g.Name,
			TaskType:   g.TaskType,
			Chain:      g.Chain,
			Model:      g.StrategistModel,
			Generation: g.Generation,
		})
	}
	return out
}
