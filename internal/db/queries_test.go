package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/veermshah/cambrian/internal/db"
)

// startPostgres boots a fresh postgres:16-alpine container, runs the
// 0001_init migration, and returns a Queries handle plus a teardown.
// Mirrors TestMigrationsUpDownRoundTrip but bundled for the InsertAgent
// table tests below.
func startPostgres(t *testing.T) *db.Queries {
	t.Helper()
	if os.Getenv("TESTCONTAINERS_DISABLED") == "1" {
		t.Skip("TESTCONTAINERS_DISABLED=1 — skipping testcontainers-backed test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

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
	if err := db.Run(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return db.NewQueries(pool)
}

// sampleAgent returns a minimal-but-valid Agent record. Each test mutates
// the fields it cares about; sharing the rest keeps the table compact.
func sampleAgent() db.Agent {
	return db.Agent{
		Name:               "root_treasury_solana",
		Chain:              "solana",
		WalletAddress:      "Sol1111111111111111111111111111111111111111",
		WalletKeyEncrypted: []byte{0x01, 0x02, 0x03},
		TaskType:           "cross_chain_yield",
		StrategistPrompt:   "treasury",
		NodeClass:          "funded",
	}
}

func TestInsertAgentRoundTrip(t *testing.T) {
	q := startPostgres(t)
	ctx := context.Background()

	id, err := q.InsertAgent(ctx, sampleAgent())
	if err != nil {
		t.Fatalf("InsertAgent: %v", err)
	}
	if id == "" {
		t.Fatal("InsertAgent returned empty id")
	}

	got, err := q.GetAgentByName(ctx, "root_treasury_solana")
	if err != nil {
		t.Fatalf("GetAgentByName: %v", err)
	}
	if got.ID != id {
		t.Errorf("ID = %q, want %q", got.ID, id)
	}
	if got.Chain != "solana" {
		t.Errorf("Chain = %q", got.Chain)
	}
	if got.WalletAddress != "Sol1111111111111111111111111111111111111111" {
		t.Errorf("WalletAddress = %q", got.WalletAddress)
	}
	if got.NodeClass != "funded" {
		t.Errorf("NodeClass = %q", got.NodeClass)
	}
}

func TestInsertAgentDefaultsJSONColumns(t *testing.T) {
	// All six JSONB inputs left nil — InsertAgent must seed sane defaults
	// so the row inserts without violating NOT NULL.
	q := startPostgres(t)
	ctx := context.Background()
	if _, err := q.InsertAgent(ctx, sampleAgent()); err != nil {
		t.Fatalf("InsertAgent: %v", err)
	}
}

func TestInsertAgentRejectsMissingFields(t *testing.T) {
	q := startPostgres(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		mutate func(*db.Agent)
	}{
		{"missing name", func(a *db.Agent) { a.Name = "" }},
		{"missing wallet_address", func(a *db.Agent) { a.WalletAddress = "" }},
		{"missing wallet_key_encrypted", func(a *db.Agent) { a.WalletKeyEncrypted = nil }},
		{"missing task_type", func(a *db.Agent) { a.TaskType = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := sampleAgent()
			tc.mutate(&a)
			if _, err := q.InsertAgent(ctx, a); err == nil {
				t.Fatal("InsertAgent: want error")
			}
		})
	}
}

func TestInsertAgentEnforcesUniqueWalletAddress(t *testing.T) {
	q := startPostgres(t)
	ctx := context.Background()
	if _, err := q.InsertAgent(ctx, sampleAgent()); err != nil {
		t.Fatalf("first InsertAgent: %v", err)
	}
	second := sampleAgent()
	second.Name = "duplicate" // different name, same wallet
	if _, err := q.InsertAgent(ctx, second); err == nil {
		t.Fatal("duplicate wallet_address: want error")
	}
}

func TestGetAgentByNameNotFound(t *testing.T) {
	q := startPostgres(t)
	ctx := context.Background()
	if _, err := q.GetAgentByName(ctx, "ghost"); !errors.Is(err, db.ErrAgentNotFound) {
		t.Fatalf("GetAgentByName: want ErrAgentNotFound, got %v", err)
	}
}

func TestTreasuryInitialized(t *testing.T) {
	q := startPostgres(t)
	ctx := context.Background()

	ready, err := q.TreasuryInitialized(ctx)
	if err != nil {
		t.Fatalf("TreasuryInitialized: %v", err)
	}
	if ready {
		t.Fatal("fresh DB should not report treasury initialized")
	}

	// Insert solana treasury only — still false.
	sol := sampleAgent()
	if _, err := q.InsertAgent(ctx, sol); err != nil {
		t.Fatalf("InsertAgent sol: %v", err)
	}
	ready, _ = q.TreasuryInitialized(ctx)
	if ready {
		t.Fatal("one-of-two treasuries: should be false")
	}

	// Insert base treasury — now true.
	base := db.Agent{
		Name:               "root_treasury_base",
		Chain:              "base",
		WalletAddress:      "0x0000000000000000000000000000000000000001",
		WalletKeyEncrypted: []byte{0xaa},
		TaskType:           "cross_chain_yield",
		StrategistPrompt:   "treasury",
		NodeClass:          "funded",
	}
	if _, err := q.InsertAgent(ctx, base); err != nil {
		t.Fatalf("InsertAgent base: %v", err)
	}
	ready, _ = q.TreasuryInitialized(ctx)
	if !ready {
		t.Fatal("both treasuries inserted: should be true")
	}
}

func TestInsertAgentAcceptsExplicitJSONColumns(t *testing.T) {
	q := startPostgres(t)
	ctx := context.Background()
	a := sampleAgent()
	a.StrategyConfig = json.RawMessage(`{"min_apy_spread_bps": 50}`)
	a.SleepSchedule = json.RawMessage(`{"awake_window_minutes": 15}`)
	a.ReproductionPolicy = json.RawMessage(`{"min_profitable_epochs": 3}`)
	a.CostPolicy = json.RawMessage(`{"monthly_llm_budget_usd": 20}`)
	a.CommunicationPolicy = json.RawMessage(`{"max_broadcasts_per_cycle": 4}`)
	a.BanditPolicies = json.RawMessage(`["thompson"]`)
	a.LearnedRules = json.RawMessage(`[{"id": "r1", "text": "avoid stETH"}]`)
	if _, err := q.InsertAgent(ctx, a); err != nil {
		t.Fatalf("InsertAgent: %v", err)
	}
}
