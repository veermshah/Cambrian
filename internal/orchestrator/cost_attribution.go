package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// MonthlyCostInputs is what the prorater uses to slice monthly infra
// and RPC spend across active agents. Caller supplies the totals; the
// shares come from the trade + strategist-call counts persisted by
// chunk 14's NodeRunner.
type MonthlyCostInputs struct {
	// MonthlyInfraUSD is the fixed monthly bill for the shared worker
	// + API + Postgres + Redis. Spec line 533 caps the default infra
	// stack at "1 shared Go worker, 1 Go API server, 1 Postgres, 1
	// Redis"; treat this as one configured number.
	MonthlyInfraUSD float64
	// MonthlyRPCUSD is the fixed monthly bill for Helius/Alchemy. Same
	// shape as infra — chunk 21's budget tracker computes both.
	MonthlyRPCUSD float64
	// MonthHours is the number of hours in the current calendar month
	// (used to convert "monthly bill" into "per-window bill"). 24 *
	// days-in-month is fine; tests pass an explicit value.
	MonthHours float64
}

// ActivityCounts is the per-agent strategist + monitor call count over
// some window. The attributor uses sum-of-counts as the proration key.
type ActivityCounts struct {
	StrategistCalls int
	MonitorTicks    int
}

// ActivityStore returns activity counts for an agent in a window, and
// the swarm-wide totals over the same window.
type ActivityStore interface {
	AgentActivity(ctx context.Context, agentID string, since, until time.Time) (ActivityCounts, error)
	SwarmActivity(ctx context.Context, since, until time.Time) (ActivityCounts, error)
}

// MonthlyAttributor distributes the configured monthly infra + RPC
// dollars across agents in proportion to their activity counts.
// Satisfies CostAttributor (interface in economics.go).
type MonthlyAttributor struct {
	store ActivityStore
	cfg   MonthlyCostInputs
}

// NewMonthlyAttributor constructs an attributor. cfg.MonthHours must be
// > 0 — chunk 21's budget tracker computes it from the calendar.
func NewMonthlyAttributor(store ActivityStore, cfg MonthlyCostInputs) (*MonthlyAttributor, error) {
	if store == nil {
		return nil, errors.New("cost_attribution: nil store")
	}
	if cfg.MonthHours <= 0 {
		return nil, errors.New("cost_attribution: MonthHours must be > 0")
	}
	if cfg.MonthlyInfraUSD < 0 || cfg.MonthlyRPCUSD < 0 {
		return nil, fmt.Errorf("cost_attribution: negative monthly cost (infra=%v, rpc=%v)",
			cfg.MonthlyInfraUSD, cfg.MonthlyRPCUSD)
	}
	return &MonthlyAttributor{store: store, cfg: cfg}, nil
}

// Attribute returns the agent's prorated infra and RPC share over
// [since, until]. The split is:
//
//	windowFraction = window hours / MonthHours
//	totalForWindow = monthly * windowFraction
//	share          = (agentCalls + agentTicks) / (swarmCalls + swarmTicks)
//	cost           = totalForWindow * share
//
// Edge cases:
//   - Swarm has zero activity → cost is 0 (no one ran, no one pays).
//   - Window has zero duration → returns (0, 0, nil).
//   - Agent has zero activity → 0 share (other agents pay).
func (m *MonthlyAttributor) Attribute(ctx context.Context, agentID string, since, until time.Time) (float64, float64, error) {
	if agentID == "" {
		return 0, 0, errors.New("cost_attribution: agent_id required")
	}
	if !until.After(since) {
		return 0, 0, nil
	}
	windowHours := until.Sub(since).Hours()
	frac := windowHours / m.cfg.MonthHours
	if frac < 0 {
		frac = 0
	}
	infraForWindow := m.cfg.MonthlyInfraUSD * frac
	rpcForWindow := m.cfg.MonthlyRPCUSD * frac

	agent, err := m.store.AgentActivity(ctx, agentID, since, until)
	if err != nil {
		return 0, 0, fmt.Errorf("cost_attribution: agent activity: %w", err)
	}
	swarm, err := m.store.SwarmActivity(ctx, since, until)
	if err != nil {
		return 0, 0, fmt.Errorf("cost_attribution: swarm activity: %w", err)
	}
	denom := float64(swarm.StrategistCalls + swarm.MonitorTicks)
	if denom <= 0 {
		return 0, 0, nil
	}
	numer := float64(agent.StrategistCalls + agent.MonitorTicks)
	share := numer / denom
	return infraForWindow * share, rpcForWindow * share, nil
}
