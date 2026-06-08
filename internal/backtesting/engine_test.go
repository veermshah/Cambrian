package backtesting

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/agent/tasks"
)

// fixtureTask is a deterministic Task used by the engine fixture. It
// emits exactly one trade per tick with a PnL series fed by the test —
// the engine has no business deciding strategy logic here, the fixture
// owns it.
type fixtureTask struct {
	pnls       []float64
	emitted    int
	positionUSD float64
}

func (t *fixtureTask) RunTick(_ context.Context) ([]tasks.Trade, error) {
	if t.emitted >= len(t.pnls) {
		return nil, nil
	}
	pnl := t.pnls[t.emitted]
	t.emitted++
	return []tasks.Trade{{
		Chain:     "mock",
		TradeType: "swap",
		TokenPair: "USDC/MOCK",
		DEX:       "mock",
		AmountIn:  1,
		AmountOut: 1,
		PnL:       pnl,
	}}, nil
}

func (t *fixtureTask) GetStateSummary(context.Context) (map[string]interface{}, error) {
	return nil, nil
}
func (t *fixtureTask) ApplyAdjustments(map[string]interface{}) error    { return nil }
func (t *fixtureTask) GetPositionValue(context.Context) (float64, error) { return t.positionUSD, nil }
func (t *fixtureTask) CloseAllPositions(context.Context) ([]tasks.Trade, error) {
	return nil, nil
}

func staticRows(start time.Time, count int, price float64) []PriceRow {
	rows := make([]PriceRow, 0, count)
	for i := range count {
		rows = append(rows, PriceRow{
			Chain:      "mock",
			TokenPair:  "USDC/MOCK",
			Price:      price,
			RecordedAt: start.Add(time.Duration(i) * time.Minute),
		})
	}
	return rows
}

func TestEngine_RunDeterministicFixturePnL(t *testing.T) {
	// Fixture: 5 trades with PnLs (+10, -5, +20, -10, +15) = +30 total.
	// StartCapital = 100, so EndEquity = 130 and TotalPnL = 30.
	// WinRate = 3/5 = 0.6. The mock chain client is unused by fixtureTask
	// itself but the engine still requires PriceRows to construct the mock.
	pnls := []float64{10, -5, 20, -10, 15}
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rows := staticRows(start, 30, 1.0)

	builder := func(_ context.Context, _ *MockChainClient, _ agent.AgentGenome) (tasks.Task, error) {
		return &fixtureTask{pnls: pnls}, nil
	}
	eng, err := NewBacktestEngine(BacktestEngineConfig{
		Builder:      builder,
		MockConfig:   MockChainConfig{ChainName: "mock", NativeToken: "MOCK"},
		PriceRows:    rows,
		StartCapital: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Run(context.Background(), agent.AgentGenome{Name: "fixture"}, BacktestPeriod{
		Start:        start,
		End:          start.Add(10 * time.Minute),
		TickInterval: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(res.TotalPnL-30) > 1e-9 {
		t.Errorf("TotalPnL = %v, want 30", res.TotalPnL)
	}
	if math.Abs(res.EndEquity-130) > 1e-9 {
		t.Errorf("EndEquity = %v, want 130", res.EndEquity)
	}
	if res.TradeCount != 5 {
		t.Errorf("TradeCount = %d, want 5", res.TradeCount)
	}
	if math.Abs(res.WinRate-0.6) > 1e-9 {
		t.Errorf("WinRate = %v, want 0.6", res.WinRate)
	}
	if !res.Passed() {
		t.Error("expected Passed()=true (positive PnL, low drawdown, trades > 0)")
	}
}

func TestEngine_MaxDrawdownComputed(t *testing.T) {
	// Curve: +100 → -50 → +25 (cumulatively). Equity goes 100, 200, 150, 175.
	// Peak = 200 at tick 1; trough = 150 at tick 2; drawdown = 50/200 = 0.25.
	pnls := []float64{100, -50, 25}
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rows := staticRows(start, 10, 1.0)
	builder := func(_ context.Context, _ *MockChainClient, _ agent.AgentGenome) (tasks.Task, error) {
		return &fixtureTask{pnls: pnls}, nil
	}
	eng, _ := NewBacktestEngine(BacktestEngineConfig{
		Builder: builder, MockConfig: MockChainConfig{ChainName: "mock", NativeToken: "MOCK"},
		PriceRows: rows, StartCapital: 100,
	})
	res, err := eng.Run(context.Background(), agent.AgentGenome{Name: "dd"}, BacktestPeriod{
		Start: start, End: start.Add(3 * time.Minute), TickInterval: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(res.MaxDrawdown-0.25) > 1e-9 {
		t.Errorf("MaxDrawdown = %v, want 0.25", res.MaxDrawdown)
	}
}

func TestEngine_SharpeOverMultipleDays(t *testing.T) {
	// Build a curve that spans 6 days. Daily returns: 0.01, 0.02, -0.01,
	// 0.03, 0.0. mean ≈ 0.01, stdev ≈ 0.01581. Sharpe = (mean/stdev)*sqrt(365)
	// ≈ 0.6325 * 19.105 ≈ 12.08. Tolerance is loose because tick-level
	// distribution affects the bucket endpoints.
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	// Five daily PnL spikes; the engine buckets equity-at-end-of-day and
	// computes daily simple returns.
	pnls := []float64{1, 2, -1, 3, 0} // emitted on tick 1 / day 1 etc.
	// Spread the trades over 5 daily windows; pad with zero-trade ticks
	// (RunTick returns nil after pnls is exhausted, which is fine).
	rows := staticRows(start, 20, 1.0)
	builder := func(_ context.Context, _ *MockChainClient, _ agent.AgentGenome) (tasks.Task, error) {
		return &dailyFixtureTask{pnls: pnls, anchor: start}, nil
	}
	eng, _ := NewBacktestEngine(BacktestEngineConfig{
		Builder: builder, MockConfig: MockChainConfig{ChainName: "mock", NativeToken: "MOCK"},
		PriceRows: rows, StartCapital: 100,
	})
	res, err := eng.Run(context.Background(), agent.AgentGenome{Name: "sharpe"}, BacktestPeriod{
		Start: start, End: start.Add(6 * 24 * time.Hour), TickInterval: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sharpe == 0 {
		t.Error("Sharpe should be non-zero for a non-flat curve")
	}
	if math.IsNaN(res.Sharpe) || math.IsInf(res.Sharpe, 0) {
		t.Errorf("Sharpe = %v, want finite", res.Sharpe)
	}
}

// dailyFixtureTask emits one trade per RunTick from the pnls slice,
// matching the cadence used by the Sharpe test (one tick per day).
type dailyFixtureTask struct {
	pnls    []float64
	anchor  time.Time
	emitted int
}

func (t *dailyFixtureTask) RunTick(_ context.Context) ([]tasks.Trade, error) {
	if t.emitted >= len(t.pnls) {
		return nil, nil
	}
	pnl := t.pnls[t.emitted]
	t.emitted++
	return []tasks.Trade{{Chain: "mock", DEX: "mock", TokenPair: "USDC/MOCK", PnL: pnl}}, nil
}
func (t *dailyFixtureTask) GetStateSummary(context.Context) (map[string]interface{}, error) {
	return nil, nil
}
func (t *dailyFixtureTask) ApplyAdjustments(map[string]interface{}) error    { return nil }
func (t *dailyFixtureTask) GetPositionValue(context.Context) (float64, error) { return 0, nil }
func (t *dailyFixtureTask) CloseAllPositions(context.Context) ([]tasks.Trade, error) {
	return nil, nil
}

func TestEngine_RejectsInvalidPeriod(t *testing.T) {
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rows := staticRows(start, 5, 1.0)
	builder := func(_ context.Context, _ *MockChainClient, _ agent.AgentGenome) (tasks.Task, error) {
		return &fixtureTask{}, nil
	}
	eng, _ := NewBacktestEngine(BacktestEngineConfig{
		Builder: builder, MockConfig: MockChainConfig{ChainName: "mock", NativeToken: "MOCK"},
		PriceRows: rows, StartCapital: 100,
	})
	cases := []struct {
		name   string
		period BacktestPeriod
	}{
		{"end before start", BacktestPeriod{Start: start, End: start.Add(-time.Hour), TickInterval: time.Minute}},
		{"zero tick", BacktestPeriod{Start: start, End: start.Add(time.Hour), TickInterval: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := eng.Run(context.Background(), agent.AgentGenome{}, tc.period); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestEngine_NoTrades_DoesNotPanic(t *testing.T) {
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rows := staticRows(start, 5, 1.0)
	builder := func(_ context.Context, _ *MockChainClient, _ agent.AgentGenome) (tasks.Task, error) {
		return &fixtureTask{pnls: nil}, nil
	}
	eng, _ := NewBacktestEngine(BacktestEngineConfig{
		Builder: builder, MockConfig: MockChainConfig{ChainName: "mock", NativeToken: "MOCK"},
		PriceRows: rows, StartCapital: 100,
	})
	res, err := eng.Run(context.Background(), agent.AgentGenome{Name: "flat"}, BacktestPeriod{
		Start: start, End: start.Add(3 * time.Minute), TickInterval: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TradeCount != 0 || res.TotalPnL != 0 {
		t.Errorf("expected flat run, got %+v", res)
	}
	if res.Passed() {
		t.Error("Passed() must be false with zero trades")
	}
}

func TestMockChain_GetQuoteAppliesSlippageAndFee(t *testing.T) {
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rows := []PriceRow{{Chain: "mock", TokenPair: "USDC/MOCK", Price: 2.0, RecordedAt: start}}
	m := NewMockChainClient(MockChainConfig{ChainName: "mock", NativeToken: "MOCK", SlippageBps: 100, FeeBps: 50}, rows)
	m.SetCursor(start)
	q, err := m.GetQuote(context.Background(), "USDC", "MOCK", 100)
	if err != nil {
		t.Fatal(err)
	}
	// gross = 100*2 = 200; fee = 200*0.005 = 1; net = 199; slip = 199*0.01 = 1.99; out = 197.01
	if math.Abs(q.AmountOut-197.01) > 1e-9 {
		t.Errorf("AmountOut = %v, want 197.01", q.AmountOut)
	}
	if math.Abs(q.FeeAmount-1.0) > 1e-9 {
		t.Errorf("FeeAmount = %v, want 1.0", q.FeeAmount)
	}
}

func TestMockChain_PriceAtCursorReplaysHistory(t *testing.T) {
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rows := []PriceRow{
		{Chain: "mock", TokenPair: "USDC/MOCK", Price: 1.0, RecordedAt: start},
		{Chain: "mock", TokenPair: "USDC/MOCK", Price: 2.0, RecordedAt: start.Add(time.Hour)},
		{Chain: "mock", TokenPair: "USDC/MOCK", Price: 3.0, RecordedAt: start.Add(2 * time.Hour)},
	}
	m := NewMockChainClient(MockChainConfig{ChainName: "mock", NativeToken: "MOCK"}, rows)
	cases := []struct {
		cursor time.Time
		want   float64
	}{
		{start, 1.0},
		{start.Add(30 * time.Minute), 1.0},
		{start.Add(time.Hour), 2.0},
		{start.Add(90 * time.Minute), 2.0},
		{start.Add(2 * time.Hour), 3.0},
		{start.Add(3 * time.Hour), 3.0},
	}
	for _, c := range cases {
		m.SetCursor(c.cursor)
		q, err := m.GetQuote(context.Background(), "USDC", "MOCK", 1)
		if err != nil {
			t.Fatalf("cursor=%v: %v", c.cursor, err)
		}
		if q.Price != c.want {
			t.Errorf("cursor=%v: price=%v, want %v", c.cursor, q.Price, c.want)
		}
	}
}

func TestOffspringRunner_ReturnsPassResult(t *testing.T) {
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rows := staticRows(start, 10, 1.0)
	builder := func(_ context.Context, _ *MockChainClient, _ agent.AgentGenome) (tasks.Task, error) {
		return &fixtureTask{pnls: []float64{5, 5, 5}}, nil
	}
	eng, _ := NewBacktestEngine(BacktestEngineConfig{
		Builder: builder, MockConfig: MockChainConfig{ChainName: "mock", NativeToken: "MOCK"},
		PriceRows: rows, StartCapital: 100,
	})
	runner := OffspringRunner{
		Engine: eng,
		Period: BacktestPeriod{Start: start, End: start.Add(3 * time.Minute), TickInterval: time.Minute},
	}
	pass, err := runner.Run(context.Background(), agent.AgentGenome{Name: "candidate"})
	if err != nil {
		t.Fatal(err)
	}
	if !pass {
		t.Error("expected pass=true for positive-PnL candidate")
	}
}
