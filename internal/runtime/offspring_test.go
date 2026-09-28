package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/chain"
	"github.com/veermshah/cambrian/internal/db"
	"github.com/veermshah/cambrian/internal/orchestrator"
)

type fakeLineage struct {
	rows []db.Lineage
	err  error
}

func (f *fakeLineage) InsertLineage(_ context.Context, l db.Lineage) error {
	if f.err != nil {
		return f.err
	}
	f.rows = append(f.rows, l)
	return nil
}

func stubWallets(chainName string) (*chain.Wallet, error) {
	return &chain.Wallet{Chain: chainName, Address: "ADDR-" + chainName, KeyEncrypted: []byte{9}}, nil
}

func childGenome() agent.AgentGenome {
	return agent.AgentGenome{
		Name:                      "child-g4",
		Generation:                4,
		LineageDepth:              2,
		TaskType:                  "momentum",
		Chain:                     "base",
		StrategyConfig:            map[string]interface{}{"momentum_window": 20.0},
		StrategistPrompt:          "p",
		StrategistModel:           "claude-haiku-4-5-20251001",
		StrategistIntervalSeconds: 3600,
		BanditPolicies:            []string{"default"},
		LearnedRules:              []agent.LearnedRule{{ID: "r", Text: "t", Confidence: 0.6}},
		CapitalAllocation:         0.05,
	}
}

func TestOffspringSpawner_MaterializesShadowChildWithLineage(t *testing.T) {
	store := newFakeStore()
	bus := &busRecorder{}
	lineage := &fakeLineage{}
	sp := NewOffspringSpawner(NewLifecycleManager(store, bus, nil), lineage, stubWallets, nil)

	inh := orchestrator.CrossoverInheritance{Task: "A", Brain: "B", Economics: "A", Communication: "B"}
	id, err := sp.Materialize(context.Background(), orchestrator.OffspringProposal{
		ProposalID:      "p1",
		ParentID:        "parent-1",
		SecondParentID:  "parent-2",
		EvolutionMethod: orchestrator.EvolutionCrossover,
		Inheritance:     &inh,
		Rationale:       "why",
		Candidate:       childGenome(),
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if id == "" {
		t.Fatal("empty child id")
	}
	row := store.insertedRows[0]
	if row.ParentID != "parent-1" || row.NodeClass != "shadow" || row.Chain != "base" || row.WalletAddress != "ADDR-base" {
		t.Fatalf("unexpected row: %+v", row)
	}
	var rules []agent.LearnedRule
	if err := json.Unmarshal(row.LearnedRules, &rules); err != nil || len(rules) != 1 {
		t.Fatalf("learned_rules json not round-tripped: %s", row.LearnedRules)
	}
	if len(bus.eventsFor(ChannelLifecycleSpawn)) != 1 {
		t.Fatal("expected one lifecycle:spawn event")
	}
	if len(lineage.rows) != 1 {
		t.Fatal("expected lineage row")
	}
	l := lineage.rows[0]
	if l.ChildID != id || l.ParentID != "parent-1" || l.SecondParentID != "parent-2" || l.EvolutionMethod != "crossover" || l.SpawnReasoning != "why" {
		t.Fatalf("lineage row: %+v", l)
	}
	var prov map[string]any
	if err := json.Unmarshal(l.MutationsApplied, &prov); err != nil || prov["proposal_id"] != "p1" {
		t.Fatalf("provenance json: %s", l.MutationsApplied)
	}
}

func TestOffspringSpawner_FundedWhenSeedCapitalSet(t *testing.T) {
	store := newFakeStore()
	sp := NewOffspringSpawner(NewLifecycleManager(store, &busRecorder{}, nil), &fakeLineage{}, stubWallets, nil)
	g := childGenome()
	g.ReproductionPolicy.OffspringSeedCapitalUSD = 25
	if _, err := sp.Materialize(context.Background(), orchestrator.OffspringProposal{ParentID: "parent-1", Candidate: g}); err != nil {
		t.Fatal(err)
	}
	row := store.insertedRows[0]
	if row.NodeClass != "funded" || row.CapitalAllocated != 25 {
		t.Fatalf("expected funded/25, got %s/%v", row.NodeClass, row.CapitalAllocated)
	}
}

func TestOffspringSpawner_RejectsInvalidGenomeBeforeWallet(t *testing.T) {
	walletCalls := 0
	wallets := func(c string) (*chain.Wallet, error) { walletCalls++; return stubWallets(c) }
	store := newFakeStore()
	sp := NewOffspringSpawner(NewLifecycleManager(store, &busRecorder{}, nil), &fakeLineage{}, wallets, nil)
	g := childGenome()
	g.Chain = "ethereum"
	if _, err := sp.Materialize(context.Background(), orchestrator.OffspringProposal{ParentID: "parent-1", Candidate: g}); err == nil {
		t.Fatal("expected validation error")
	}
	if walletCalls != 0 || len(store.insertedRows) != 0 {
		t.Fatal("invalid genome must not mint a wallet or insert a row")
	}
}

func TestOffspringSpawner_LineageFailureDoesNotOrphanChild(t *testing.T) {
	store := newFakeStore()
	sp := NewOffspringSpawner(NewLifecycleManager(store, &busRecorder{}, nil), &fakeLineage{err: errors.New("dup")}, stubWallets, nil)
	id, err := sp.Materialize(context.Background(), orchestrator.OffspringProposal{ParentID: "parent-1", Candidate: childGenome()})
	if err != nil || id == "" {
		t.Fatalf("child should still be materialized: id=%q err=%v", id, err)
	}
}

func TestOffspringSpawner_WalletErrorPropagates(t *testing.T) {
	store := newFakeStore()
	bad := func(string) (*chain.Wallet, error) { return nil, errors.New("hsm offline") }
	sp := NewOffspringSpawner(NewLifecycleManager(store, &busRecorder{}, nil), &fakeLineage{}, bad, nil)
	if _, err := sp.Materialize(context.Background(), orchestrator.OffspringProposal{ParentID: "parent-1", Candidate: childGenome()}); err == nil {
		t.Fatal("expected wallet error")
	}
	if len(store.insertedRows) != 0 {
		t.Fatal("no row should be inserted when wallet minting fails")
	}
}

func TestNewChainWallet_UnknownChain(t *testing.T) {
	if _, err := NewChainWallet(make([]byte, 32))("ethereum"); err == nil {
		t.Fatal("expected unknown chain error")
	}
}
