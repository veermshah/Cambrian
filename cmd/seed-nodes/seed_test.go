package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/veermshah/cambrian/internal/chain"
	"github.com/veermshah/cambrian/internal/db"
)

// fakeStore satisfies seedStore + the spawn func signature. It tracks
// which agents exist by name and remembers the rows handed to Spawn.
type fakeStore struct {
	mu      sync.Mutex
	existing map[string]db.Agent
	spawned  []db.Agent
}

func newFakeStore() *fakeStore {
	return &fakeStore{existing: map[string]db.Agent{}}
}

func (f *fakeStore) GetAgentByName(_ context.Context, name string) (db.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.existing[name]; ok {
		return a, nil
	}
	return db.Agent{}, db.ErrAgentNotFound
}

func (f *fakeStore) Spawn(_ context.Context, row db.Agent, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, dup := f.existing[row.Name]; dup {
		return "", errors.New("fake: duplicate name")
	}
	id := "id-" + row.Name
	row.ID = id
	f.existing[row.Name] = row
	f.spawned = append(f.spawned, row)
	return id, nil
}

func testMasterKey() []byte {
	// 32 zero bytes — deterministic and good enough for tests; wallets
	// generated from this key are throwaway.
	b := make([]byte, 32)
	return b
}

func TestParseSeedYAML_AcceptsExampleFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "seed.yaml.example"))
	if err != nil {
		t.Fatal(err)
	}
	specs, err := ParseSeedYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 8 {
		t.Errorf("specs = %d, want 8 (3 funded + 5 shadow)", len(specs))
	}
	var funded, shadow int
	for _, s := range specs {
		switch s.NodeClass {
		case "funded":
			funded++
		case "shadow":
			shadow++
		}
	}
	if funded != 3 || shadow != 5 {
		t.Errorf("funded=%d shadow=%d, want 3 funded + 5 shadow", funded, shadow)
	}
}

func TestParseSeedYAML_RejectsEmpty(t *testing.T) {
	if _, err := ParseSeedYAML(nil); err == nil {
		t.Error("expected error for empty file")
	}
	if _, err := ParseSeedYAML([]byte(`nodes: []`)); err == nil {
		t.Error("expected error for empty nodes list")
	}
}

func TestParseSeedYAML_RejectsDuplicateNames(t *testing.T) {
	raw := []byte(`
nodes:
  - name: a
    task_type: momentum
    chain: solana
    node_class: shadow
    capital_usd: 0
  - name: a
    task_type: momentum
    chain: base
    node_class: shadow
    capital_usd: 0
`)
	if _, err := ParseSeedYAML(raw); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate-name error, got %v", err)
	}
}

func TestSeedSpec_ValidateCatchesBadFields(t *testing.T) {
	bad := []struct {
		name string
		spec SeedSpec
	}{
		{"empty name", SeedSpec{Chain: "solana", TaskType: "momentum", NodeClass: "shadow"}},
		{"bad chain", SeedSpec{Name: "x", Chain: "polygon", TaskType: "momentum", NodeClass: "shadow"}},
		{"bad task", SeedSpec{Name: "x", Chain: "solana", TaskType: "scalping", NodeClass: "shadow"}},
		{"bad class", SeedSpec{Name: "x", Chain: "solana", TaskType: "momentum", NodeClass: "alpha"}},
		{"negative capital", SeedSpec{Name: "x", Chain: "solana", TaskType: "momentum", NodeClass: "funded", CapitalUSD: -1}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.spec.Validate(); err == nil {
				t.Errorf("expected validation error")
			}
		})
	}
}

func TestSeedSpec_ToAgentRowAppliesDefaults(t *testing.T) {
	spec := SeedSpec{
		Name: "scout", Chain: "solana", TaskType: "momentum",
		NodeClass: "shadow", CapitalUSD: 50,
	}
	row := spec.ToAgentRow(&chain.Wallet{Address: "addr-1", KeyEncrypted: []byte{1, 2, 3}})
	if row.StrategistModel != "claude-haiku-4-5-20251001" {
		t.Errorf("default model = %q", row.StrategistModel)
	}
	if row.StrategistIntervalSeconds != 14400 {
		t.Errorf("default interval = %d", row.StrategistIntervalSeconds)
	}
	if row.StrategistPrompt == "" {
		t.Error("default prompt empty")
	}
	if string(row.StrategyConfig) != `{}` {
		t.Errorf("default config = %q", row.StrategyConfig)
	}
	if row.CapitalAllocated != 50 {
		t.Errorf("capital = %v", row.CapitalAllocated)
	}
}

func TestSeedAll_SpawnsThenSkipsOnRerun(t *testing.T) {
	store := newFakeStore()
	deps := &spawnDeps{
		store:     store,
		masterKey: testMasterKey(),
		spawn:     store.Spawn,
	}
	specs := []SeedSpec{
		{Name: "n1", Chain: "solana", TaskType: "momentum", NodeClass: "shadow"},
		{Name: "n2", Chain: "base", TaskType: "liquidation_hunting", NodeClass: "shadow"},
	}

	rep, err := SeedAll(context.Background(), specs, deps)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Spawned != 2 || rep.Skipped != 0 {
		t.Errorf("first run: spawned=%d skipped=%d", rep.Spawned, rep.Skipped)
	}

	// Re-run — every node now exists, so the second run must skip all.
	rep2, err := SeedAll(context.Background(), specs, deps)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Spawned != 0 || rep2.Skipped != 2 {
		t.Errorf("second run: spawned=%d skipped=%d (want 0 spawn, 2 skip)", rep2.Spawned, rep2.Skipped)
	}
}

func TestSeedAll_PartialIdempotency(t *testing.T) {
	store := newFakeStore()
	// Pre-seed one entry by hand.
	store.existing["n1"] = db.Agent{Name: "n1", WalletAddress: "pre-existing"}
	deps := &spawnDeps{store: store, masterKey: testMasterKey(), spawn: store.Spawn}
	specs := []SeedSpec{
		{Name: "n1", Chain: "solana", TaskType: "momentum", NodeClass: "shadow"},
		{Name: "n2", Chain: "base", TaskType: "momentum", NodeClass: "shadow"},
	}
	rep, err := SeedAll(context.Background(), specs, deps)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Spawned != 1 || rep.Skipped != 1 {
		t.Errorf("partial: spawned=%d skipped=%d (want 1 spawn, 1 skip)", rep.Spawned, rep.Skipped)
	}
	if !strings.Contains(rep.Lines[0], "skip") || !strings.Contains(rep.Lines[1], "spawn") {
		t.Errorf("report lines = %v", rep.Lines)
	}
}

func TestSeedAll_ValidationErrorPropagates(t *testing.T) {
	store := newFakeStore()
	deps := &spawnDeps{store: store, masterKey: testMasterKey(), spawn: store.Spawn}
	specs := []SeedSpec{
		{Name: "ok", Chain: "polygon" /* invalid */, TaskType: "momentum", NodeClass: "shadow"},
	}
	if _, err := SeedAll(context.Background(), specs, deps); err == nil {
		t.Error("expected validation error to propagate")
	}
}

func TestSeedAll_BuildsWalletForBothChains(t *testing.T) {
	store := newFakeStore()
	deps := &spawnDeps{store: store, masterKey: testMasterKey(), spawn: store.Spawn}
	specs := []SeedSpec{
		{Name: "sol", Chain: "solana", TaskType: "momentum", NodeClass: "shadow"},
		{Name: "base", Chain: "base", TaskType: "momentum", NodeClass: "shadow"},
	}
	if _, err := SeedAll(context.Background(), specs, deps); err != nil {
		t.Fatal(err)
	}
	if len(store.spawned) != 2 {
		t.Fatalf("spawned %d, want 2", len(store.spawned))
	}
	for _, row := range store.spawned {
		if row.WalletAddress == "" {
			t.Errorf("agent %q missing wallet address", row.Name)
		}
		if len(row.WalletKeyEncrypted) == 0 {
			t.Errorf("agent %q missing encrypted key", row.Name)
		}
	}
}

func TestDefaultPromptFor_KnownTaskTypes(t *testing.T) {
	for _, tt := range []string{"cross_chain_yield", "liquidity_provision", "liquidation_hunting", "momentum"} {
		if defaultPromptFor(tt) == "" {
			t.Errorf("missing default prompt for %s", tt)
		}
	}
}
