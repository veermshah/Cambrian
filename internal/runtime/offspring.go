package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/chain"
	"github.com/veermshah/cambrian/internal/db"
	"github.com/veermshah/cambrian/internal/orchestrator"
)

// LineageStore is the extra DB surface OffspringSpawner needs beyond
// LifecycleStore. *db.Queries satisfies it.
type LineageStore interface {
	InsertLineage(ctx context.Context, l db.Lineage) error
}

// WalletFactory mints a fresh encrypted wallet for the given chain.
// Production uses NewChainWallet with the swarm master key; tests inject
// a stub.
type WalletFactory func(chainName string) (*chain.Wallet, error)

// NewChainWallet returns the production WalletFactory: Ed25519 for
// solana, secp256k1 for base, both sealed under masterKey.
func NewChainWallet(masterKey []byte) WalletFactory {
	return func(chainName string) (*chain.Wallet, error) {
		switch chainName {
		case "solana":
			return chain.NewSolanaWallet(masterKey)
		case "base":
			return chain.NewBaseWallet(masterKey)
		}
		return nil, fmt.Errorf("offspring: unknown chain %q", chainName)
	}
}

// OffspringSpawner is the production orchestrator.OffspringMaterializer.
// For an approved proposal it: mints a wallet on the candidate's chain,
// inserts the agents row (parent_id set, node_class shadow unless the
// parent pre-funded seed capital) via LifecycleManager.Spawn — which
// also publishes lifecycle:spawn so the SwarmRuntime boots a NodeRunner
// — then records the lineage row.
//
// Children always start as shadow nodes when the proposal carries no
// seed capital: the spec's "shadow → funded" promotion is a later
// lifecycle decision based on paper-trading P&L, not a birthright.
type OffspringSpawner struct {
	lifecycle *LifecycleManager
	lineage   LineageStore
	wallets   WalletFactory
	log       agent.Logger
}

// NewOffspringSpawner wires the materializer. log may be nil.
func NewOffspringSpawner(lifecycle *LifecycleManager, lineage LineageStore, wallets WalletFactory, log agent.Logger) *OffspringSpawner {
	if log == nil {
		log = agent.NopLogger{}
	}
	return &OffspringSpawner{lifecycle: lifecycle, lineage: lineage, wallets: wallets, log: log}
}

var _ orchestrator.OffspringMaterializer = (*OffspringSpawner)(nil)

// Materialize implements orchestrator.OffspringMaterializer.
func (s *OffspringSpawner) Materialize(ctx context.Context, p orchestrator.OffspringProposal) (string, error) {
	if s == nil || s.lifecycle == nil || s.lineage == nil || s.wallets == nil {
		return "", errors.New("offspring: spawner not fully configured")
	}
	if p.ParentID == "" {
		return "", errors.New("offspring: proposal has no parent_id")
	}
	if err := p.Candidate.Validate(); err != nil {
		return "", fmt.Errorf("offspring: candidate genome invalid: %w", err)
	}

	wallet, err := s.wallets(p.Candidate.Chain)
	if err != nil {
		return "", fmt.Errorf("offspring: wallet: %w", err)
	}

	row, err := AgentRowFromGenome(p.Candidate, wallet)
	if err != nil {
		return "", err
	}
	row.ParentID = p.ParentID
	row.NodeClass = "shadow"
	if p.Candidate.ReproductionPolicy.OffspringSeedCapitalUSD > 0 {
		row.NodeClass = "funded"
		row.CapitalAllocated = p.Candidate.ReproductionPolicy.OffspringSeedCapitalUSD
	}

	reason := fmt.Sprintf("offspring of %s via %s (proposal %s)", p.ParentID, methodOrMutation(p.EvolutionMethod), p.ProposalID)
	childID, err := s.lifecycle.Spawn(ctx, row, reason)
	if err != nil {
		return "", err
	}

	provenance, _ := json.Marshal(map[string]any{
		"proposal_id":     p.ProposalID,
		"epoch_id":        p.EpochID,
		"inheritance":     p.Inheritance,
		"generation":      p.Candidate.Generation,
		"lineage_depth":   p.Candidate.LineageDepth,
		"inherited_rules": len(p.Candidate.LearnedRules),
	})
	if err := s.lineage.InsertLineage(ctx, db.Lineage{
		ChildID:          childID,
		ParentID:         p.ParentID,
		SecondParentID:   p.SecondParentID,
		EvolutionMethod:  methodOrMutation(p.EvolutionMethod),
		MutationsApplied: provenance,
		SpawnReasoning:   p.Rationale,
	}); err != nil {
		// The agent exists and is live; a missing lineage row is a
		// reporting gap, not a reason to orphan the child.
		s.log.Warnw("offspring.lineage_insert_failed", "child_id", childID, "err", err.Error())
	}
	s.log.Infow("offspring.materialized",
		"child_id", childID, "parent_id", p.ParentID,
		"method", methodOrMutation(p.EvolutionMethod), "node_class", row.NodeClass)
	return childID, nil
}

func methodOrMutation(m string) string {
	if m == "" {
		return orchestrator.EvolutionMutation
	}
	return m
}

// AgentRowFromGenome serializes a genome into the agents-table row
// shape. JSON module columns are marshaled here so package db stays
// free of the genome type.
func AgentRowFromGenome(g agent.AgentGenome, wallet *chain.Wallet) (db.Agent, error) {
	marshal := func(v any) (json.RawMessage, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("offspring: marshal genome module: %w", err)
		}
		return b, nil
	}
	strategy, err := marshal(g.StrategyConfig)
	if err != nil {
		return db.Agent{}, err
	}
	if g.StrategyConfig == nil {
		strategy = json.RawMessage(`{}`)
	}
	bandits, err := marshal(g.BanditPolicies)
	if err != nil {
		return db.Agent{}, err
	}
	if g.BanditPolicies == nil {
		bandits = json.RawMessage(`["default"]`)
	}
	rules, err := marshal(g.LearnedRules)
	if err != nil {
		return db.Agent{}, err
	}
	if g.LearnedRules == nil {
		rules = json.RawMessage(`[]`)
	}
	sleep, err := marshal(g.SleepSchedule)
	if err != nil {
		return db.Agent{}, err
	}
	repro, err := marshal(g.ReproductionPolicy)
	if err != nil {
		return db.Agent{}, err
	}
	cost, err := marshal(g.CostPolicy)
	if err != nil {
		return db.Agent{}, err
	}
	comms, err := marshal(g.CommunicationPolicy)
	if err != nil {
		return db.Agent{}, err
	}
	return db.Agent{
		Name:                      g.Name,
		Chain:                     g.Chain,
		WalletAddress:             wallet.Address,
		WalletKeyEncrypted:        wallet.KeyEncrypted,
		TaskType:                  g.TaskType,
		StrategyConfig:            strategy,
		StrategistPrompt:          g.StrategistPrompt,
		StrategistModel:           g.StrategistModel,
		StrategistIntervalSeconds: g.StrategistIntervalSeconds,
		BanditPolicies:            bandits,
		LearnedRules:              rules,
		SleepSchedule:             sleep,
		ReproductionPolicy:        repro,
		CostPolicy:                cost,
		CommunicationPolicy:       comms,
		Generation:                g.Generation,
		LineageDepth:              g.LineageDepth,
		CapitalAllocated:          g.CapitalAllocation,
	}, nil
}
