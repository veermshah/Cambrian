package backtesting

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/agent/tasks"
)

// BacktestResult is what BacktestEngine.Run returns. Fields match the
// spec's stated metrics (line 493 — total P&L, max drawdown, win rate,
// Sharpe ratio, equity curve) plus the bookkeeping the offspring
// pipeline (chunk 21) needs to render a verdict.
type BacktestResult struct {
	GenomeName   string
	Period       BacktestPeriod
	StartCapital float64
	EndEquity    float64
	TotalPnL     float64
	MaxDrawdown  float64 // fraction in [0, 1]
	WinRate      float64 // fraction in [0, 1] over closed trades
	Sharpe       float64 // annualized over daily returns
	TradeCount   int
	EquityCurve  []EquityPoint
	Trades       []tasks.Trade
	Notes        string
}

// EquityPoint is one sample of total agent equity at a simulated tick.
type EquityPoint struct {
	At     time.Time
	Equity float64
}

// BacktestPeriod is the simulated time range and tick cadence the
// engine replays. Start, End and TickInterval are required; the
// engine refuses to run if End <= Start or TickInterval <= 0.
type BacktestPeriod struct {
	Start        time.Time
	End          time.Time
	TickInterval time.Duration
}

// TaskBuilder is the closure the engine uses to instantiate a Task
// bound to the supplied MockChainClient. Different task types have
// different dependency shapes (CrossChainYieldDeps, LP deps, …); the
// runtime owns the wiring and exposes a single builder closure to keep
// the engine type-agnostic.
type TaskBuilder func(ctx context.Context, mock *MockChainClient, genome agent.AgentGenome) (tasks.Task, error)

// BacktestEngineConfig parameterises Run.
type BacktestEngineConfig struct {
	// Builder produces the Task under test. Required.
	Builder TaskBuilder
	// MockConfig is forwarded to NewMockChainClient. Required.
	MockConfig MockChainConfig
	// PriceRows are the historical observations the mock replays.
	// Required and must cover Period.Start..Period.End for whatever
	// pair(s) the task queries.
	PriceRows []PriceRow
	// StartCapital is the agent's notional starting equity in USD —
	// used for drawdown and Sharpe calculations.
	StartCapital float64
	// PnLValuation, if non-nil, maps a (chain, token) to its USD price
	// so the engine can mark open positions to market between ticks.
	// nil ⇒ rely on the task's GetPositionValue alone.
	PnLValuation func(ctx context.Context, mock *MockChainClient) (float64, error)
}

// BacktestEngine runs one genome against historical prices and reports
// the metrics that drive offspring promotion (chunk 21).
type BacktestEngine struct {
	cfg BacktestEngineConfig
}

// NewBacktestEngine validates the config and returns an engine.
func NewBacktestEngine(cfg BacktestEngineConfig) (*BacktestEngine, error) {
	if cfg.Builder == nil {
		return nil, errors.New("backtesting: engine requires a Builder")
	}
	if len(cfg.PriceRows) == 0 {
		return nil, errors.New("backtesting: engine requires PriceRows")
	}
	if cfg.StartCapital <= 0 {
		cfg.StartCapital = 1000.0
	}
	return &BacktestEngine{cfg: cfg}, nil
}

// Run executes the backtest. The engine advances simulated time in
// TickInterval steps, calling Task.RunTick on each tick. Equity is
// marked at every tick using PnLValuation if provided, otherwise via
// Task.GetPositionValue.
//
// The engine deliberately ignores Task.GetStateSummary and
// ApplyAdjustments — those are strategist-loop concerns; a backtest is
// a pure replay of the *fast* loop.
func (e *BacktestEngine) Run(ctx context.Context, genome agent.AgentGenome, period BacktestPeriod) (BacktestResult, error) {
	if e == nil {
		return BacktestResult{}, errors.New("backtesting: nil engine")
	}
	if !period.End.After(period.Start) {
		return BacktestResult{}, fmt.Errorf("backtesting: period end %v must be after start %v", period.End, period.Start)
	}
	if period.TickInterval <= 0 {
		return BacktestResult{}, errors.New("backtesting: period.TickInterval must be positive")
	}

	mock := NewMockChainClient(e.cfg.MockConfig, e.cfg.PriceRows)
	mock.SetCursor(period.Start)
	task, err := e.cfg.Builder(ctx, mock, genome)
	if err != nil {
		return BacktestResult{}, fmt.Errorf("backtesting: builder: %w", err)
	}

	result := BacktestResult{
		GenomeName:   genome.Name,
		Period:       period,
		StartCapital: e.cfg.StartCapital,
		EndEquity:    e.cfg.StartCapital,
	}

	var (
		equityCurve []EquityPoint
		trades      []tasks.Trade
		closedPnLs  []float64
	)
	equityCurve = append(equityCurve, EquityPoint{At: period.Start, Equity: e.cfg.StartCapital})

	for cursor := period.Start; !cursor.After(period.End); cursor = cursor.Add(period.TickInterval) {
		mock.SetCursor(cursor)
		tradesThisTick, runErr := task.RunTick(ctx)
		if runErr != nil {
			result.Notes = fmt.Sprintf("RunTick error at %s: %v", cursor.Format(time.RFC3339), runErr)
			return result, runErr
		}
		for _, tr := range tradesThisTick {
			trades = append(trades, tr)
			closedPnLs = append(closedPnLs, tr.PnL)
		}

		var posValue float64
		if e.cfg.PnLValuation != nil {
			v, err := e.cfg.PnLValuation(ctx, mock)
			if err != nil {
				result.Notes = fmt.Sprintf("PnLValuation error at %s: %v", cursor.Format(time.RFC3339), err)
				return result, err
			}
			posValue = v
		} else {
			v, err := task.GetPositionValue(ctx)
			if err == nil {
				posValue = v
			}
		}

		realizedDelta := sumPnL(tradesThisTick)
		equity := equityCurve[len(equityCurve)-1].Equity + realizedDelta + (posValue - equityCurve[len(equityCurve)-1].Equity*0)
		_ = equity
		// Simpler accounting: equity = start_capital + sum(realized_pnl) + open_position_value.
		equity = e.cfg.StartCapital + sumPnLSlice(closedPnLs) + posValue
		equityCurve = append(equityCurve, EquityPoint{At: cursor, Equity: equity})
	}

	result.Trades = trades
	result.TradeCount = len(trades)
	result.EquityCurve = equityCurve
	result.EndEquity = equityCurve[len(equityCurve)-1].Equity
	result.TotalPnL = result.EndEquity - result.StartCapital
	result.MaxDrawdown = computeMaxDrawdown(equityCurve)
	result.WinRate = computeWinRate(closedPnLs)
	result.Sharpe = computeSharpe(equityCurve)
	return result, nil
}

// Passed returns true if the result meets the chunk-21 promotion
// floor: TotalPnL > 0 AND MaxDrawdown < 0.5 AND TradeCount > 0. The
// offspring pipeline calls this to decide promotion.
func (r BacktestResult) Passed() bool {
	return r.TotalPnL > 0 && r.MaxDrawdown < 0.5 && r.TradeCount > 0
}

// OffspringRunner is a thin adapter that implements
// orchestrator.BacktestRunner from chunk 21 without forcing the
// backtesting package to depend on orchestrator.
type OffspringRunner struct {
	Engine *BacktestEngine
	Period BacktestPeriod
}

// Run satisfies orchestrator.BacktestRunner.
func (o OffspringRunner) Run(ctx context.Context, candidate agent.AgentGenome) (bool, error) {
	if o.Engine == nil {
		return false, errors.New("backtesting: OffspringRunner missing engine")
	}
	res, err := o.Engine.Run(ctx, candidate, o.Period)
	if err != nil {
		return false, err
	}
	return res.Passed(), nil
}

// ---- metric helpers ------------------------------------------------------

func sumPnL(trs []tasks.Trade) float64 {
	var s float64
	for _, t := range trs {
		s += t.PnL
	}
	return s
}

func sumPnLSlice(p []float64) float64 {
	var s float64
	for _, v := range p {
		s += v
	}
	return s
}

func computeWinRate(pnls []float64) float64 {
	if len(pnls) == 0 {
		return 0
	}
	wins := 0
	for _, p := range pnls {
		if p > 0 {
			wins++
		}
	}
	return float64(wins) / float64(len(pnls))
}

// computeMaxDrawdown walks the curve, tracking the highest equity
// seen so far, and returns the largest fractional drop from that peak.
// Returns 0 when the curve only goes up (or is empty).
func computeMaxDrawdown(curve []EquityPoint) float64 {
	if len(curve) == 0 {
		return 0
	}
	peak := curve[0].Equity
	worst := 0.0
	for _, p := range curve {
		if p.Equity > peak {
			peak = p.Equity
		}
		if peak <= 0 {
			continue
		}
		drawdown := (peak - p.Equity) / peak
		if drawdown > worst {
			worst = drawdown
		}
	}
	return worst
}

// computeSharpe approximates an annualized Sharpe ratio over daily
// returns. The engine bucketizes the equity curve into 24h windows,
// computes the simple return between bucket endpoints, then reports
//
//	mean(returns) / stdev(returns) * sqrt(365)
//
// Risk-free rate is taken as 0 (the spec doesn't specify one, and for
// devnet token-pair backtests the comparison is to "do nothing" anyway).
// Returns 0 when fewer than 2 daily buckets are available — Sharpe is
// undefined on a single sample.
func computeSharpe(curve []EquityPoint) float64 {
	if len(curve) < 2 {
		return 0
	}
	// Sort by timestamp defensively (Run inserts in order).
	sort.Slice(curve, func(i, j int) bool { return curve[i].At.Before(curve[j].At) })
	buckets := bucketByDay(curve)
	if len(buckets) < 2 {
		return 0
	}
	returns := make([]float64, 0, len(buckets)-1)
	for i := 1; i < len(buckets); i++ {
		prev := buckets[i-1]
		cur := buckets[i]
		if prev <= 0 {
			continue
		}
		returns = append(returns, (cur-prev)/prev)
	}
	if len(returns) < 2 {
		return 0
	}
	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))
	sqSum := 0.0
	for _, r := range returns {
		d := r - mean
		sqSum += d * d
	}
	variance := sqSum / float64(len(returns)-1)
	if variance <= 0 {
		return 0
	}
	stdev := math.Sqrt(variance)
	if stdev == 0 {
		return 0
	}
	return (mean / stdev) * math.Sqrt(365)
}

// bucketByDay returns the last equity sample per UTC day in time order.
func bucketByDay(curve []EquityPoint) []float64 {
	if len(curve) == 0 {
		return nil
	}
	type kv struct {
		day    string
		equity float64
		at     time.Time
	}
	byDay := map[string]kv{}
	order := []string{}
	for _, p := range curve {
		day := p.At.UTC().Format("2006-01-02")
		prev, ok := byDay[day]
		if !ok {
			order = append(order, day)
		}
		if !ok || p.At.After(prev.at) {
			byDay[day] = kv{day: day, equity: p.Equity, at: p.At}
		}
	}
	out := make([]float64, 0, len(order))
	for _, d := range order {
		out = append(out, byDay[d].equity)
	}
	return out
}
