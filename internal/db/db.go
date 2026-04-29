// Package db owns the Postgres connection pool, the migration runner, and
// query method signatures used by the rest of the swarm.
//
// Two URLs flow in from config:
//
//   - DATABASE_URL is the direct connection (Supabase: db.<ref>.supabase.co:5432).
//     Migrations always use it because golang-migrate needs advisory locks and
//     prepared statements that pgBouncer in transaction-pooling mode does not
//     support.
//   - DATABASE_POOL_URL is the pooled connection (Supabase: <ref>.pooler.
//     supabase.com:6543). Application traffic uses it. Because pgBouncer in
//     transaction mode cannot keep prepared statements across transaction
//     boundaries, the pool is configured with QueryExecModeExec — pgx sends
//     plain SQL with bound parameters instead of preparing.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Default ceiling on simultaneous app-side connections. Supabase's free tier
// allows up to ~60 pooler connections; 20 leaves plenty of headroom for
// migrations, the dashboard, and ad-hoc tooling.
const defaultMaxConns = 20

// NewPool returns a pgxpool.Pool wired for Supabase pgBouncer (transaction
// pooling). poolURL is required; if empty the caller is expected to fall
// back to the direct DATABASE_URL.
func NewPool(ctx context.Context, poolURL string) (*pgxpool.Pool, error) {
	if poolURL == "" {
		return nil, errors.New("db: pool URL is empty")
	}
	cfg, err := pgxpool.ParseConfig(poolURL)
	if err != nil {
		return nil, fmt.Errorf("db: parse pool URL: %w", err)
	}
	cfg.MaxConns = defaultMaxConns
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	// Critical for Supabase pgBouncer transaction mode: never prepare
	// statements server-side, since the same backend connection is not
	// guaranteed for the next query.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}
