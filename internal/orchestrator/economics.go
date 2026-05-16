// Package orchestrator owns the swarm-level decisions: per-epoch
// economics settlement, profit sweeps to parent/root, monthly cost
// attribution, mutation/crossover (chunks 17-18), and the root
// epoch loop (chunk 21).
//
// This file (chunk 15) implements the realized-net-profit ledger row.
// Spec lines 89-99 define the formula verbatim; lines 78-88 explain the
// three-kinds-of-money invariant that Settle has to honor — trading
// capital, off-chain operating expenses, and internal accounting are
// all separate columns. Hands off the result to chunk 21's epoch loop.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Ledger mirrors the agent_ledgers schema (spec lines 728-742). One
// row per (agent, epoch). Settle constructs and persists it; chunk 21
// reads it back for reproduction gates and circuit-breaker decisions.
type Ledger struct {
	AgentID              string
	EpochID              string
	RealizedTradingPnL   float64
	TradingFees          float64
	SlippageCost         float64
	LLMCost              float64
	InfraRent            float64
	RPCCost              float64
	UpstreamPaidToParent float64
	UpstreamPaidToRoot   float64
	RealizedNetProfit    float64
}

// TradeAggregates is the subset of the trades table Settle needs.
// Implementations sum the rows that match (agent_id, epoch_id).
type TradeAggregates struct {
	PnL      float64
	Fees     float64
	Slippage float64
}

// EconomicsStore is the narrow DB surface Settle needs. Tests pass a
// fake; chunk 21 wires *db.Queries.
type EconomicsStore interface {
	// EpochWindow returns the start and end timestamps for an epoch.
	EpochWindow(ctx context.Context, epochID string) (time.Time, time.Time, error)
	// AggregateTrades returns PnL/fees/slippage summed over the agent's
	// trades in the epoch.
	AggregateTrades(ctx context.Context, agentID, epochID string) (TradeAggregates, error)
	// AggregateStrategistCost returns sum(cost_usd) from
	// strategist_decisions for an agent in [since, until].
	AggregateStrategistCost(ctx context.Context, agentID string, since, until time.Time) (float64, error)
	// CarriedDebt returns the absolute value of the agent's outstanding
	// upstream-obligations-due (e.g. unpaid sweeps from prior epochs).
	// Returns 0 when none.
	CarriedDebt(ctx context.Context, agentID string) (float64, error)
	// InsertLedger persists the row. Returns an error if a duplicate
	// (agent, epoch) row already exists — Settle is meant to be called
	// once per (agent, epoch).
	InsertLedger(ctx context.Context, l Ledger) error
}

// CostAttributor returns per-agent shares of the monthly infra rent
// and the monthly RPC cost for a given epoch window. Concrete impl
// lives in cost_attribution.go.
type CostAttributor interface {
	Attribute(ctx context.Context, agentID string, since, until time.Time) (infra float64, rpc float64, err error)
}

// Settle computes the realized net profit row for (agent, epoch) and
// persists it. Spec lines 89-99 define the arithmetic; we honor it
// exactly and never substitute estimates — every input is either
// observed-and-stored or attributable.
//
// On error the ledger is not written (the caller can retry).
func Settle(ctx context.Context, store EconomicsStore, costs CostAttributor, agentID, epochID string) (Ledger, error) {
	if store == nil {
		return Ledger{}, errors.New("economics.Settle: nil store")
	}
	if costs == nil {
		return Ledger{}, errors.New("economics.Settle: nil cost attributor")
	}
	if agentID == "" || epochID == "" {
		return Ledger{}, errors.New("economics.Settle: agent_id and epoch_id required")
	}

	start, end, err := store.EpochWindow(ctx, epochID)
	if err != nil {
		return Ledger{}, fmt.Errorf("economics.Settle: epoch window: %w", err)
	}
	if !end.After(start) {
		return Ledger{}, fmt.Errorf("economics.Settle: epoch window has non-positive duration (%v to %v)", start, end)
	}

	agg, err := store.AggregateTrades(ctx, agentID, epochID)
	if err != nil {
		return Ledger{}, fmt.Errorf("economics.Settle: aggregate trades: %w", err)
	}

	llmCost, err := store.AggregateStrategistCost(ctx, agentID, start, end)
	if err != nil {
		return Ledger{}, fmt.Errorf("economics.Settle: aggregate strategist cost: %w", err)
	}

	infra, rpc, err := costs.Attribute(ctx, agentID, start, end)
	if err != nil {
		return Ledger{}, fmt.Errorf("economics.Settle: attribute costs: %w", err)
	}

	debt, err := store.CarriedDebt(ctx, agentID)
	if err != nil {
		return Ledger{}, fmt.Errorf("economics.Settle: carried debt: %w", err)
	}

	// Spec line 91: realizedNetProfit = trading - fees - slippage - llm
	//                                   - infra - rpc - upstream obligations
	// Upstream-paid-to-parent / -to-root are filled in by Sweep (separate
	// step); here only the prior-epoch carried debt enters.
	net := agg.PnL - agg.Fees - agg.Slippage - llmCost - infra - rpc - debt

	l := Ledger{
		AgentID:            agentID,
		EpochID:            epochID,
		RealizedTradingPnL: agg.PnL,
		TradingFees:        agg.Fees,
		SlippageCost:       agg.Slippage,
		LLMCost:            llmCost,
		InfraRent:          infra,
		RPCCost:            rpc,
		RealizedNetProfit:  net,
	}
	if err := store.InsertLedger(ctx, l); err != nil {
		return Ledger{}, fmt.Errorf("economics.Settle: insert ledger: %w", err)
	}
	return l, nil
}
