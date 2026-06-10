package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func seedStore(t *testing.T) *MemoryStore {
	t.Helper()
	s := NewMemoryStore()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	a := AgentSummary{
		ID: "ag-1", Name: "scout-7", Chain: "solana", TaskType: "momentum",
		NodeClass: "funded", Status: "active", HealthState: "healthy",
		Generation: 3, LineageDepth: 2,
		CapitalUSD: 100, CurrentUSD: 142.5, TotalPnLUSD: 42.5, TotalTrades: 27,
		StrategyModel: "claude-haiku-4-5", CreatedAt: now,
	}
	b := AgentSummary{
		ID: "ag-2", Name: "loser-1", Chain: "base", TaskType: "cross_chain_yield",
		NodeClass: "shadow", Status: "active", HealthState: "healthy",
		Generation: 1, LineageDepth: 1,
		CapitalUSD: 100, CurrentUSD: 80, TotalPnLUSD: -20, TotalTrades: 5,
		StrategyModel: "claude-haiku-4-5", CreatedAt: now,
	}
	s.Agents = []AgentSummary{a, b}
	s.AgentDetails["ag-1"] = AgentDetail{
		AgentSummary:     a,
		ParentID:         "root_treasury_solana",
		StrategistPrompt: "be greedy",
		StrategyConfig:   map[string]any{"lookback_minutes": 60.0},
		BanditPolicies:   []string{"thompson"},
		LearnedRules:     []any{},
	}
	s.Trades = []TradeRow{{
		ID: "tr-1", AgentID: "ag-1", AgentName: "scout-7",
		Chain: "solana", TradeType: "swap", TokenPair: "SOL/USDC",
		DEX: "raydium", AmountIn: 10, AmountOut: 11, FeePaidUSD: 0.05,
		PnLUSD: 1, ExecutedAt: now, IsPaperTrade: true,
	}}
	s.Epochs = []EpochRow{{
		ID: "ep-1", EpochNumber: 42, StartedAt: now.Add(-time.Hour), EndedAt: now,
		TotalAgents: 2, FundedAgents: 1, ShadowAgents: 1,
		TreasuryBalanceUSD: 1000, TotalPnLUSD: 22.5,
	}}
	s.Lineage = []LineageNode{
		{AgentID: "ag-1", Name: "scout-7", NodeClass: "funded", Status: "active", Generation: 3, ParentIDs: []string{"root"}},
	}
	s.Treasury = TreasuryState{
		ReserveUSD: 850, TotalCapitalAllocatedUSD: 200,
		MonthlySpendUSD: 75, MonthlyBudgetUSD: 500, UsedPct: 0.15,
		PerChain: map[string]float64{"solana": 500, "base": 350},
		UpdatedAt: now,
	}
	s.Postmortems = []PostmortemRow{{ID: "pm-1", AgentID: "ag-9", AgentName: "dead-1", LessonsSummary: "max drawdown", CreatedAt: now}}
	s.Offspring = []OffspringRow{
		{ID: "of-1", ProposingAgentID: "ag-1", Rationale: "promising", Status: "pending", CreatedAt: now},
		{ID: "of-2", ProposingAgentID: "ag-2", Rationale: "rejected", Status: "rejected", CreatedAt: now},
	}
	s.Budget = BudgetState{
		MonthStart: now.AddDate(0, 0, -8), MonthlyBudgetUSD: 500, SpentUSD: 75,
		RemainingUSD: 425, UsedPct: 0.15,
		PerCategory: map[string]float64{"llm": 50, "infra": 15, "rpc": 10},
		PerAgent: []AgentSpend{{AgentID: "ag-1", Name: "scout-7", SpentUSD: 60}},
	}
	s.Breaker = CircuitBreakerState{State: "armed"}
	s.Backtests = []BacktestRow{{
		ID: "bt-1", Chain: "solana", TokenPair: "SOL/USDC",
		PeriodStart: now.Add(-30 * 24 * time.Hour), PeriodEnd: now,
		InitialCapital: 1000, FinalCapital: 1100, TotalPnLUSD: 100,
		TotalTrades: 50, WinRate: 0.6, Sharpe: 1.2,
		EquityCurve: []float64{1000, 1050, 1100}, CreatedAt: now,
	}}
	s.Intel = []IntelRow{{
		ID: "in-1", SourceAgentID: "ag-1", SourceAgentName: "scout-7",
		Channel: "events:intel", SignalType: "rate_change",
		Sentiment: "bullish", Confidence: 0.8, SourceAccuracy: 0.7,
		Data: map[string]any{"new_rate": 0.05}, CreatedAt: now,
	}}
	s.Models = []ModelPerformance{
		{Model: "claude-haiku-4-5", UsedByAgents: 2, Decisions: 100, TotalCostUSD: 5, PnLUSD: 30, PnLPerDollar: 6},
		{Model: "gpt-5-mini", UsedByAgents: 1, Decisions: 30, TotalCostUSD: 2, PnLUSD: 4, PnLPerDollar: 2},
	}
	s.Evolution = []EvolutionEvent{{
		ChildID: "ag-1", ChildName: "scout-7", ParentID: "ag-0", ParentName: "ancestor",
		EvolutionMethod: "mutation", Mutations: []string{"lookback+30"}, CreatedAt: now,
	}}
	s.Snapshot = DashboardSnapshot{
		TotalAgents: 2, FundedAgents: 1, ShadowAgents: 1,
		Treasury:         s.Treasury,
		MonthlySpendUSD:  75, MonthlyBudgetUSD: 500,
		EquityCurve:      []EquityPoint{{At: now.Add(-time.Hour), EquityUSD: 1000}, {At: now, EquityUSD: 1022.5}},
		RecentEpoch:      &s.Epochs[0],
		OpenProposals:    1,
		UpdatedAt:        now,
	}
	return s
}

func newTestServer(t *testing.T, authRequired bool) (*Server, *MemoryStore) {
	t.Helper()
	store := seedStore(t)
	srv, err := NewServer(ServerConfig{
		Store:        store,
		APIKey:       "test-key",
		AuthRequired: authRequired,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, store
}

func do(t *testing.T, srv *Server, method, path, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	rec := httptest.NewRecorder()
	srv.Engine().ServeHTTP(rec, req)
	return rec
}

func TestAuth_RejectsMissingAndWrongKey(t *testing.T) {
	srv, _ := newTestServer(t, true)
	cases := []struct {
		key  string
		want int
	}{
		{"", http.StatusUnauthorized},
		{"wrong", http.StatusUnauthorized},
		{"test-key", http.StatusOK},
	}
	for _, tc := range cases {
		rec := do(t, srv, http.MethodGet, "/api/agents", tc.key)
		if rec.Code != tc.want {
			t.Errorf("key=%q: status %d, want %d (body=%s)", tc.key, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestAuth_QueryStringFallback(t *testing.T) {
	srv, _ := newTestServer(t, true)
	rec := do(t, srv, http.MethodGet, "/api/agents?api_key=test-key", "")
	if rec.Code != http.StatusOK {
		t.Errorf("query-string auth: status %d, want 200", rec.Code)
	}
}

func TestAuth_HealthzAlwaysOpen(t *testing.T) {
	srv, _ := newTestServer(t, true)
	rec := do(t, srv, http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Errorf("/healthz status %d, want 200", rec.Code)
	}
}

func TestListAgents_ReturnsRows(t *testing.T) {
	srv, _ := newTestServer(t, false)
	rec := do(t, srv, http.MethodGet, "/api/agents", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var resp listResponse[AgentSummary]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 2 {
		t.Errorf("count = %d, want 2", resp.Count)
	}
	if resp.Items[0].ID != "ag-1" {
		t.Errorf("first item = %s, want ag-1 (sorted by pnl desc)", resp.Items[0].ID)
	}
}

func TestListAgents_AppliesFilter(t *testing.T) {
	srv, _ := newTestServer(t, false)
	rec := do(t, srv, http.MethodGet, "/api/agents?chain=base", "")
	var resp listResponse[AgentSummary]
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Count != 1 || resp.Items[0].Chain != "base" {
		t.Errorf("chain filter: %+v", resp)
	}
}

func TestGetAgent_FoundAndNotFound(t *testing.T) {
	srv, _ := newTestServer(t, false)
	ok := do(t, srv, http.MethodGet, "/api/agents/ag-1", "")
	if ok.Code != http.StatusOK {
		t.Errorf("found: status %d", ok.Code)
	}
	miss := do(t, srv, http.MethodGet, "/api/agents/ag-missing", "")
	if miss.Code != http.StatusNotFound {
		t.Errorf("missing: status %d, want 404", miss.Code)
	}
}

func TestListEndpointsAllRespond200(t *testing.T) {
	srv, _ := newTestServer(t, false)
	for _, path := range []string{
		"/api/agents", "/api/trades", "/api/epochs",
		"/api/lineage", "/api/treasury", "/api/postmortems",
		"/api/offspring", "/api/budget", "/api/circuit-breaker",
		"/api/backtests", "/api/intelligence", "/api/models",
		"/api/evolution", "/api/dashboard",
	} {
		rec := do(t, srv, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d body=%s", path, rec.Code, rec.Body.String())
		}
		if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s: content-type %q", path, rec.Header().Get("Content-Type"))
		}
	}
}

func TestListOffspring_AppliesStatusFilter(t *testing.T) {
	srv, _ := newTestServer(t, false)
	rec := do(t, srv, http.MethodGet, "/api/offspring?status=rejected", "")
	var resp listResponse[OffspringRow]
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Count != 1 || resp.Items[0].Status != "rejected" {
		t.Errorf("status filter: %+v", resp)
	}
}

func TestDashboardSnapshot_ShapeIsPopulated(t *testing.T) {
	srv, _ := newTestServer(t, false)
	rec := do(t, srv, http.MethodGet, "/api/dashboard", "")
	var snap DashboardSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.TotalAgents != 2 || snap.FundedAgents != 1 || snap.ShadowAgents != 1 {
		t.Errorf("agent counts: %+v", snap)
	}
	if snap.RecentEpoch == nil || snap.RecentEpoch.EpochNumber != 42 {
		t.Errorf("recent epoch: %+v", snap.RecentEpoch)
	}
	if len(snap.EquityCurve) != 2 {
		t.Errorf("equity curve len = %d, want 2", len(snap.EquityCurve))
	}
}

func TestQueryInt_ClampsAndDefaults(t *testing.T) {
	srv, _ := newTestServer(t, false)
	// non-numeric → 0 → default → 200
	rec := do(t, srv, http.MethodGet, "/api/trades?limit=abc", "")
	if rec.Code != http.StatusOK {
		t.Errorf("limit=abc: status %d", rec.Code)
	}
	// negative → 0 → default → 200
	rec = do(t, srv, http.MethodGet, "/api/trades?limit=-5", "")
	if rec.Code != http.StatusOK {
		t.Errorf("limit=-5: status %d", rec.Code)
	}
}

func TestCORS_AllowedOriginEchoes(t *testing.T) {
	srv, _ := newTestServer(t, false)
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()
	srv.Engine().ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("ACAO = %q", got)
	}
}

func TestCORS_PreflightShortCircuits(t *testing.T) {
	srv, _ := newTestServer(t, false)
	req := httptest.NewRequest(http.MethodOptions, "/api/agents", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()
	srv.Engine().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("OPTIONS = %d, want 204", rec.Code)
	}
}

func TestNewServer_RejectsMissingStore(t *testing.T) {
	_, err := NewServer(ServerConfig{})
	if err == nil {
		t.Error("expected error for missing store")
	}
}

func TestNewServer_RejectsAuthWithoutKey(t *testing.T) {
	_, err := NewServer(ServerConfig{Store: NewMemoryStore(), AuthRequired: true})
	if err == nil {
		t.Error("expected error for missing api key")
	}
}

func TestRun_ShutdownOnContextCancel(t *testing.T) {
	srv, _ := newTestServer(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, "127.0.0.1:0") }()
	// Cancel almost immediately — the listener may not even bind, but
	// the goroutine must exit cleanly either way.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s")
	}
}
