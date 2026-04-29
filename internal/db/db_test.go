package db_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/veermshah/cambrian/internal/db"
)

// expectedTables enumerates every table the 0001_init migration creates.
// Keep in sync with migrations/0001_init.up.sql.
var expectedTables = []string{
	"agents",
	"epochs",
	"backtest_results",
	"trades",
	"strategist_decisions",
	"agent_ledgers",
	"offspring_proposals",
	"postmortems",
	"profit_sweeps",
	"lineage",
	"price_history",
	"intel_log",
	"market_knowledge",
	"signal_outcomes",
}

func TestMigrationsUpDownRoundTrip(t *testing.T) {
	if os.Getenv("TESTCONTAINERS_DISABLED") == "1" {
		t.Skip("TESTCONTAINERS_DISABLED=1 — skipping testcontainers-backed test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("cambrian_test"),
		tcpostgres.WithUsername("cambrian"),
		tcpostgres.WithPassword("cambrian"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminate container: %v", err)
		}
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	// Up.
	if err := db.Run(dsn); err != nil {
		t.Fatalf("Run (up): %v", err)
	}

	// Idempotent — second Up is a no-op.
	if err := db.Run(dsn); err != nil {
		t.Fatalf("Run (up, second time): %v", err)
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })

	for _, table := range expectedTables {
		var present bool
		err := conn.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = 'public' AND table_name = $1
			)`, table).Scan(&present)
		if err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if !present {
			t.Errorf("expected table %q to exist after up migration", table)
		}
	}

	// pool can execute SELECT 1 through pgxpool with QueryExecModeExec.
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	var one int
	if err := pool.QueryRow(ctx, "select 1").Scan(&one); err != nil {
		t.Fatalf("pool select 1: %v", err)
	}
	if one != 1 {
		t.Fatalf("pool select 1 = %d, want 1", one)
	}

	// Down.
	if err := db.Down(dsn); err != nil {
		t.Fatalf("Down: %v", err)
	}
	for _, table := range expectedTables {
		var present bool
		err := conn.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = 'public' AND table_name = $1
			)`, table).Scan(&present)
		if err != nil {
			t.Fatalf("check table %s post-down: %v", table, err)
		}
		if present {
			t.Errorf("expected table %q to be dropped after down migration", table)
		}
	}
}

func TestNewPoolRequiresURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.NewPool(ctx, ""); err == nil {
		t.Fatal("expected error for empty pool URL")
	}
}

func TestRunRequiresURL(t *testing.T) {
	if err := db.Run(""); err == nil {
		t.Fatal("expected error for empty direct URL")
	}
}
