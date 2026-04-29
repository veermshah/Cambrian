package db_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/db"
)

// TestSupabaseSmoke exercises the configured Supabase pooled URL end-to-end
// using QueryExecModeExec. Gated by INTEGRATION=1 because it requires real
// network credentials and would otherwise fail in CI / on contributor
// laptops without a .env file.
func TestSupabaseSmoke(t *testing.T) {
	if os.Getenv("INTEGRATION") != "1" {
		t.Skip("INTEGRATION!=1 — skipping Supabase smoke test")
	}
	poolURL := os.Getenv("DATABASE_POOL_URL")
	if poolURL == "" {
		poolURL = os.Getenv("DATABASE_URL")
	}
	if poolURL == "" {
		t.Skip("neither DATABASE_POOL_URL nor DATABASE_URL set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.NewPool(ctx, poolURL)
	if err != nil {
		t.Fatalf("NewPool against Supabase: %v", err)
	}
	t.Cleanup(pool.Close)

	var one int
	if err := pool.QueryRow(ctx, "select 1").Scan(&one); err != nil {
		t.Fatalf("Supabase select 1: %v", err)
	}
	if one != 1 {
		t.Fatalf("Supabase select 1 = %d, want 1", one)
	}
}
