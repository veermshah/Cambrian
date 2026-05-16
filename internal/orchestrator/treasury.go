package orchestrator

import (
	"context"
	"errors"
	"fmt"
)

// Sweep is one profit_sweeps row (spec lines 810-818). A successful
// Sweep moves funds from the agent up to its parent and up to root,
// retaining the rest in the agent's wallet for future trading capital.
type Sweep struct {
	AgentID        string
	ParentAgentID  string // empty ⇒ root sweep (no parent)
	AmountToParent float64
	AmountToRoot   float64
	AmountRetained float64
}

// SweepInputs is what the caller hands ComputeSweep. Pure inputs only —
// no I/O. Spec lines 514-522 govern eligibility; lines 89-99 govern
// the underlying profit number.
type SweepInputs struct {
	// AgentID identifies the row.
	AgentID string
	// ParentAgentID is empty for funded agents that report directly to
	// root, otherwise the parent agent's UUID.
	ParentAgentID string
	// NetProfit is the realized_net_profit_usd from the just-settled
	// epoch. ComputeSweep does nothing when this is ≤ 0.
	NetProfit float64
	// CarriedDebt is the outstanding upstream obligation (negative
	// ledger) from prior epochs. Sweep is skipped while any debt
	// remains, per spec line 521 "no unpaid debt".
	CarriedDebt float64
	// LineageReserveBalance is the agent's reserve buffer. Spec line
	// 521: "lineage above reserve threshold."
	LineageReserveBalance float64
	// LineageReserveFloor is the threshold below which Sweep is
	// skipped (the agent needs to rebuild its own runway first).
	LineageReserveFloor float64
	// ParentSplitPct is the fraction of NetProfit routed to parent
	// (only applies when ParentAgentID is non-empty). Must be in [0, 1].
	ParentSplitPct float64
	// RootSplitPct is the fraction routed to root. Must be in [0, 1].
	// ParentSplitPct + RootSplitPct must also be ≤ 1.
	RootSplitPct float64
}

// SweepDecision tells the caller what Sweep did and why.
type SweepDecision struct {
	Sweep   Sweep
	Skipped bool
	Reason  string
}

// SweepStore is the persistence surface ComputeSweep does not call —
// caller can use it after a non-skipped decision to write the row.
type SweepStore interface {
	InsertSweep(ctx context.Context, s Sweep) error
}

// ComputeSweep is pure. It evaluates eligibility, splits the profit,
// and returns a decision. Skip reasons are stable strings the dashboard
// can group on:
//
//   - "insolvent"     → NetProfit ≤ 0
//   - "carrying_debt" → CarriedDebt > 0
//   - "below_reserve" → LineageReserveBalance < LineageReserveFloor
//
// Spec line 521 ("active and healthy, positive realized net profit, no
// unpaid debt") is the gating condition; ComputeSweep enforces it.
func ComputeSweep(in SweepInputs) (SweepDecision, error) {
	if in.AgentID == "" {
		return SweepDecision{}, errors.New("treasury.Sweep: agent_id required")
	}
	if in.ParentSplitPct < 0 || in.ParentSplitPct > 1 {
		return SweepDecision{}, fmt.Errorf("treasury.Sweep: ParentSplitPct out of [0,1]: %v", in.ParentSplitPct)
	}
	if in.RootSplitPct < 0 || in.RootSplitPct > 1 {
		return SweepDecision{}, fmt.Errorf("treasury.Sweep: RootSplitPct out of [0,1]: %v", in.RootSplitPct)
	}
	if in.ParentSplitPct+in.RootSplitPct > 1 {
		return SweepDecision{}, fmt.Errorf("treasury.Sweep: splits sum > 1: %v + %v", in.ParentSplitPct, in.RootSplitPct)
	}

	if in.NetProfit <= 0 {
		return SweepDecision{Skipped: true, Reason: "insolvent"}, nil
	}
	if in.CarriedDebt > 0 {
		return SweepDecision{Skipped: true, Reason: "carrying_debt"}, nil
	}
	if in.LineageReserveBalance < in.LineageReserveFloor {
		return SweepDecision{Skipped: true, Reason: "below_reserve"}, nil
	}

	// Compute splits. If the agent has no parent, parent's share rolls
	// into root — spec line 84: "central treasury receives profit
	// sweeps."
	parentShare := in.ParentSplitPct * in.NetProfit
	rootShare := in.RootSplitPct * in.NetProfit
	if in.ParentAgentID == "" {
		rootShare += parentShare
		parentShare = 0
	}
	retained := in.NetProfit - parentShare - rootShare
	if retained < 0 {
		// Belt-and-suspenders: numeric drift can pull retained
		// negative when splits sum to exactly 1; clamp.
		retained = 0
	}

	return SweepDecision{
		Sweep: Sweep{
			AgentID:        in.AgentID,
			ParentAgentID:  in.ParentAgentID,
			AmountToParent: parentShare,
			AmountToRoot:   rootShare,
			AmountRetained: retained,
		},
	}, nil
}

// PersistSweep is the small wrapper that writes a non-skipped decision.
// Lives next to ComputeSweep because both belong to the same call site
// (chunk 21's epoch loop) — keeping them separate functions lets tests
// fuzz ComputeSweep without faking a store.
func PersistSweep(ctx context.Context, store SweepStore, d SweepDecision) error {
	if d.Skipped {
		return nil
	}
	if store == nil {
		return errors.New("treasury.PersistSweep: nil store")
	}
	return store.InsertSweep(ctx, d.Sweep)
}
