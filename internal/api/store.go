package api

import (
	"context"
	"errors"
)

// Store is the read-only data surface every handler depends on. The
// production binding (PostgresStore) wraps the pgx pool; tests inject
// MemoryStore so handler behavior can be verified without a database.
//
// One narrow interface beats wiring fourteen tiny ones because the
// handlers are simple read-out functions — the cost of mocking lives
// almost entirely in shaping the canned responses, not in the method
// surface.
type Store interface {
	ListAgents(ctx context.Context, opts ListAgentsOpts) ([]AgentSummary, error)
	GetAgent(ctx context.Context, id string) (AgentDetail, error)
	ListTrades(ctx context.Context, opts ListTradesOpts) ([]TradeRow, error)
	ListEpochs(ctx context.Context, limit int) ([]EpochRow, error)
	GetLineage(ctx context.Context) ([]LineageNode, error)
	GetTreasury(ctx context.Context) (TreasuryState, error)
	ListPostmortems(ctx context.Context, limit int) ([]PostmortemRow, error)
	ListOffspring(ctx context.Context, status string) ([]OffspringRow, error)
	GetBudget(ctx context.Context) (BudgetState, error)
	GetCircuitBreaker(ctx context.Context) (CircuitBreakerState, error)
	ListBacktests(ctx context.Context, limit int) ([]BacktestRow, error)
	ListIntel(ctx context.Context, opts ListIntelOpts) ([]IntelRow, error)
	ListModels(ctx context.Context) ([]ModelPerformance, error)
	ListEvolution(ctx context.Context, limit int) ([]EvolutionEvent, error)
	GetDashboardSnapshot(ctx context.Context) (DashboardSnapshot, error)
}

// ListAgentsOpts narrows the agents list. Empty fields don't filter.
type ListAgentsOpts struct {
	Chain     string
	NodeClass string
	Status    string
	TaskType  string
}

// ListTradesOpts narrows the trades list. Limit ≤ 0 ⇒ default 100.
type ListTradesOpts struct {
	AgentID string
	Chain   string
	Limit   int
}

// ListIntelOpts narrows the intelligence feed. Empty fields don't filter.
type ListIntelOpts struct {
	Channel   string
	Sentiment string
	Limit     int
}

// ErrNotFound is the canonical not-found sentinel handlers translate to
// HTTP 404.
var ErrNotFound = errors.New("api: not found")
