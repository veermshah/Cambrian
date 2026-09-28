package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/llm"
)

// enqueuingStore is fakeEpochStore + ProposalEnqueuer so RunEpoch's
// type assertion picks it up.
type enqueuingStore struct {
	fakeEpochStore
	enqueued []OffspringProposal
}

func (e *enqueuingStore) EnqueueOffspringProposal(_ context.Context, p OffspringProposal) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enqueued = append(e.enqueued, p)
	return nil
}

type fakeMaterializer struct {
	calls []OffspringProposal
	err   error
}

func (f *fakeMaterializer) Materialize(_ context.Context, p OffspringProposal) (string, error) {
	f.calls = append(f.calls, p)
	if f.err != nil {
		return "", f.err
	}
	return "child-" + p.ProposalID, nil
}

func approveAllLLM() llm.LLMClient {
	return llm.NewFakeLLMClient("claude-haiku-4-5-20251001").
		WithResponseQueue(
			`{"verdict":"approve","reasoning":"good"}`,
			"bull text",
			"bear text",
			`{"verdict":"approve","synthesis":"all clear"}`,
		).
		WithTokenUsage(50, 20)
}

func approvedState() EpochState {
	return EpochState{
		EpochID:      "ep-1",
		Genomes:      map[string]agent.AgentGenome{},
		SwarmGenomes: []agent.AgentGenome{parentGenome()},
		OffspringProposals: []OffspringProposal{{
			ProposalID: "p1", ParentID: "parent-1",
			Candidate:              parentGenome(),
			ReproductionReserveUSD: 10,
			ParentLedger:           Ledger{RealizedNetProfit: 100},
			EvolutionMethod:        EvolutionMutation,
		}},
	}
}

func TestRunEpoch_ApprovedProposalIsMaterialized(t *testing.T) {
	store := &fakeEpochStore{state: approvedState()}
	cfg := minimalConfig(store, &fakeLifecycle{}, &fakeBreaker{}, &fakeBus{}, approveAllLLM())
	mat := &fakeMaterializer{}
	cfg.Materializer = mat
	r, _ := NewRootOrchestrator(cfg)
	out, err := r.RunEpoch(context.Background(), "ep-1")
	if err != nil {
		t.Fatal(err)
	}
	d := out.OffspringDecisions[0]
	if d.Outcome != OffspringMaterialized || d.ChildID != "child-p1" {
		t.Fatalf("expected materialized child-p1, got %+v", d)
	}
	if len(mat.calls) != 1 || mat.calls[0].ProposalID != "p1" {
		t.Fatalf("materializer should be called once with p1, got %+v", mat.calls)
	}
	if store.persistedOffspring[0].Outcome != OffspringMaterialized {
		t.Fatal("persisted decision should carry materialized outcome")
	}
}

func TestRunEpoch_MaterializerFailureKeepsApproved(t *testing.T) {
	store := &fakeEpochStore{state: approvedState()}
	cfg := minimalConfig(store, &fakeLifecycle{}, &fakeBreaker{}, &fakeBus{}, approveAllLLM())
	cfg.Materializer = &fakeMaterializer{err: errors.New("wallet down")}
	r, _ := NewRootOrchestrator(cfg)
	out, _ := r.RunEpoch(context.Background(), "ep-1")
	d := out.OffspringDecisions[0]
	if d.Outcome != OffspringApproved || d.ChildID != "" {
		t.Fatalf("expected approved w/o child on materializer error, got %+v", d)
	}
}

func TestRunEpoch_ProposesAndEnqueuesOffspring(t *testing.T) {
	st := reproState()
	store := &enqueuingStore{fakeEpochStore: fakeEpochStore{state: st}}
	cfg := minimalConfig(&store.fakeEpochStore, &fakeLifecycle{}, &fakeBreaker{}, &fakeBus{}, approveAllLLM())
	cfg.Store = store
	cfg.Reproduction = ReproductionConfig{MaxProposalsPerEpoch: 2}
	r, _ := NewRootOrchestrator(cfg)
	out, err := r.RunEpoch(context.Background(), "epoch-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Proposals) != 2 {
		t.Fatalf("expected 2 proposals, got %d", len(out.Proposals))
	}
	if len(store.enqueued) != 2 {
		t.Fatalf("expected 2 enqueued, got %d", len(store.enqueued))
	}
	for _, p := range store.enqueued {
		if p.EpochID != "epoch-1" || p.Candidate.Name == "" {
			t.Fatalf("enqueued proposal malformed: %+v", p)
		}
	}
}

func TestRunEpoch_NoProposalsWhenBreakerHalted(t *testing.T) {
	store := &enqueuingStore{fakeEpochStore: fakeEpochStore{state: reproState()}}
	cfg := minimalConfig(&store.fakeEpochStore, &fakeLifecycle{}, &fakeBreaker{halted: true}, &fakeBus{}, approveAllLLM())
	cfg.Store = store
	r, _ := NewRootOrchestrator(cfg)
	out, _ := r.RunEpoch(context.Background(), "epoch-1")
	if len(out.Proposals) != 0 || len(store.enqueued) != 0 {
		t.Fatalf("halted breaker must freeze reproduction, got %d/%d", len(out.Proposals), len(store.enqueued))
	}
}

func TestProvenanceRoundTrip(t *testing.T) {
	cases := []struct{ method, second, text string }{
		{EvolutionCrossover, "b1", "archive parent a1"},
		{EvolutionMutation, "", "archive parent a1"},
		{"", "", "hand-written rationale"},
	}
	for _, c := range cases {
		m, s, rest := parseProvenance(formatProvenance(c.method, c.second, c.text))
		if m != c.method || s != c.second || rest != c.text {
			t.Errorf("%+v → (%q,%q,%q)", c, m, s, rest)
		}
	}
	if m, _, rest := parseProvenance("[unterminated rationale"); m != "" || rest != "[unterminated rationale" {
		t.Error("unterminated tag must be passed through verbatim")
	}
}
