package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Queries is the read/write surface the rest of the swarm uses for Postgres.
// Method bodies are stubbed in chunk 2; chunks 9, 14, 15, and 21 fill them
// in as the orchestrator and agent runtime come online.
type Queries struct {
	pool *pgxpool.Pool
}

// NewQueries constructs a Queries bound to the given pool.
func NewQueries(pool *pgxpool.Pool) *Queries {
	return &Queries{pool: pool}
}

// Pool exposes the underlying pgxpool for transactions and ad-hoc queries.
func (q *Queries) Pool() *pgxpool.Pool { return q.pool }

var errNotImplemented = errors.New("db: not implemented")

// Agent represents a row in the agents table. Concrete fields are added in
// later chunks alongside the methods that consume them.
type Agent struct{}

// Trade represents a row in the trades table.
type Trade struct{}

// StrategistDecision represents a row in strategist_decisions.
type StrategistDecision struct{}

// Epoch represents a row in epochs.
type Epoch struct{}

// OffspringProposal represents a row in offspring_proposals.
type OffspringProposal struct{}

// Postmortem represents a row in postmortems.
type Postmortem struct{}

// LedgerRow represents a row in agent_ledgers.
type LedgerRow struct{}

func (q *Queries) InsertAgent(ctx context.Context, a Agent) error {
	return errNotImplemented
}

func (q *Queries) GetAgent(ctx context.Context, id string) (Agent, error) {
	return Agent{}, errNotImplemented
}

func (q *Queries) ListActiveAgents(ctx context.Context) ([]Agent, error) {
	return nil, errNotImplemented
}

func (q *Queries) LogTrade(ctx context.Context, t Trade) error {
	return errNotImplemented
}

func (q *Queries) LogStrategistDecision(ctx context.Context, d StrategistDecision) error {
	return errNotImplemented
}

func (q *Queries) InsertEpoch(ctx context.Context, e Epoch) error {
	return errNotImplemented
}

func (q *Queries) InsertOffspringProposal(ctx context.Context, p OffspringProposal) error {
	return errNotImplemented
}

func (q *Queries) InsertPostmortem(ctx context.Context, p Postmortem) error {
	return errNotImplemented
}

func (q *Queries) InsertLedgerRow(ctx context.Context, r LedgerRow) error {
	return errNotImplemented
}
