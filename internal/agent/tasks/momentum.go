package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veermshah/cambrian/internal/chain"
)

// MomentumDeps bundles runtime dependencies for the momentum task.
// Shape matches the sibling tasks.
type MomentumDeps struct {
	Clients map[string]chain.ChainClient
	Wallets map[string]*chain.Wallet
	Now     func() time.Time
}

// Momentum implements the momentum task. Spec lines 319–338. The
// strategy holds at most one open long position. RunTick:
//
//  1. Sample current spot via chain.GetQuote and push (price, volume)
//     into a rolling ring buffer.
//  2. If no position open: enter long when the latest price closes
//     above the lookback-window high × (1 + EntryThresholdPct). Volume
//     confirmation requires latest volume above the lookback-window
//     average.
//  3. If a position is open: flatten on either (a) price falling below
//     the lookback-window midpoint × (1 - ExitThresholdPct), or (b)
//     stop-loss — price below entry × (1 - StopLossPerTradePct).
//
// Daily trade cap (MaxDailyTrades) resets on UTC date change. Position
// sizing capped at MaxPositionSizePct of free capital.
type Momentum struct {
	cfg  MomentumConfig
	deps MomentumDeps

	tokenA, tokenB string

	mu sync.Mutex

	freeCapital float64

	// Rolling history of the last LookbackMinutes samples. The slice
	// is treated as a FIFO bounded by lookbackSamples().
	history []priceSample

	position *momentumPosition

	lastCheck time.Time

	// Daily trade accounting.
	dailyDate  string
	dailyTotal int

	totalTrades int
	totalWins   int
}

type priceSample struct {
	At     time.Time
	Price  float64
	Volume float64
}

type momentumPosition struct {
	OpenedAt   time.Time
	EntryPrice float64
	StopPrice  float64
	SizeUSD    float64 // notional capital deployed
	Units      float64 // tokenB acquired
}

var _ Task = (*Momentum)(nil)

// NewMomentum builds the task. Used directly by tests; the registered
// factory wraps this once the runtime calls SetMomentumDeps.
func NewMomentum(deps MomentumDeps, cfg MomentumConfig) (*Momentum, error) {
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if deps.Clients == nil {
		return nil, errors.New("momentum: deps.Clients required")
	}
	if _, ok := deps.Clients[cfg.Chain]; !ok {
		return nil, fmt.Errorf("momentum: chain %q missing from deps.Clients", cfg.Chain)
	}
	a, b, err := splitTokenPair(cfg.TokenPair)
	if err != nil {
		return nil, err
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Momentum{
		cfg:    cfg,
		deps:   deps,
		tokenA: a,
		tokenB: b,
	}, nil
}

// SeedFreeCapital seeds the USD capital available for new positions.
func (m *Momentum) SeedFreeCapital(usd float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.freeCapital = usd
}

// IngestSample is the test seam used to push a synthetic price sample
// into the ring buffer without going through chain.GetQuote. The
// runtime never calls this; RunTick is the production path.
func (m *Momentum) IngestSample(at time.Time, price, volume float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pushSampleLocked(priceSample{At: at, Price: price, Volume: volume})
}

// --- Task interface --------------------------------------------------------

func (m *Momentum) RunTick(ctx context.Context) ([]Trade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.deps.Now()
	m.maybeResetDailyLocked(now)

	if !m.lastCheck.IsZero() && now.Sub(m.lastCheck) < time.Duration(m.cfg.CheckIntervalSecs*float64(time.Second)) {
		return nil, nil
	}
	m.lastCheck = now

	client := m.deps.Clients[m.cfg.Chain]
	quote, err := client.GetQuote(ctx, m.tokenA, m.tokenB, 1.0)
	if err != nil {
		return nil, fmt.Errorf("momentum: get quote: %w", err)
	}
	price := quote.Price
	if price <= 0 && quote.AmountIn > 0 {
		price = quote.AmountOut / quote.AmountIn
	}
	if price <= 0 {
		return nil, fmt.Errorf("momentum: non-positive price %v", price)
	}
	m.pushSampleLocked(priceSample{At: now, Price: price, Volume: 0})

	if m.position != nil {
		return m.maybeExitLocked(now, price), nil
	}
	return m.maybeEnterLocked(now, price), nil
}

func (m *Momentum) GetStateSummary(_ context.Context) (map[string]interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	high, low, mid, vol := m.lookbackStatsLocked()
	out := map[string]interface{}{
		"chain":             m.cfg.Chain,
		"token_pair":        m.cfg.TokenPair,
		"lookback_minutes":  m.cfg.LookbackMinutes,
		"window_high":       high,
		"window_low":        low,
		"window_mid":        mid,
		"window_avg_volume": vol,
		"free_capital_usd":  m.freeCapital,
		"trades_today":      m.dailyTotal,
		"max_daily":         m.cfg.MaxDailyTrades,
		"total_trades":      m.totalTrades,
		"win_rate":          m.winRateLocked(),
	}
	if m.position != nil {
		out["position"] = map[string]interface{}{
			"entry_price": m.position.EntryPrice,
			"stop_price":  m.position.StopPrice,
			"size_usd":    m.position.SizeUSD,
			"units":       m.position.Units,
			"opened_at":   m.position.OpenedAt.UTC().Format(time.RFC3339),
		}
	}
	return out, nil
}

func (m *Momentum) ApplyAdjustments(adjustments map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, raw := range adjustments {
		switch k {
		case "entry_threshold_pct":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("momentum: entry_threshold_pct not numeric: %T", raw)
			}
			m.cfg.EntryThresholdPct = clampFloat(v, momEntryThreshFloor, momEntryThreshCeil)
		case "exit_threshold_pct":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("momentum: exit_threshold_pct not numeric: %T", raw)
			}
			m.cfg.ExitThresholdPct = clampFloat(v, momExitThreshFloor, momExitThreshCeil)
		case "max_position_size_pct":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("momentum: max_position_size_pct not numeric: %T", raw)
			}
			m.cfg.MaxPositionSizePct = clampFloat(v, momMaxPosPctFloor, momMaxPosPctCeil)
		case "stop_loss_per_trade_pct":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("momentum: stop_loss_per_trade_pct not numeric: %T", raw)
			}
			m.cfg.StopLossPerTradePct = clampFloat(v, momStopLossFloor, momStopLossCeil)
		case "max_daily_trades":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("momentum: max_daily_trades not numeric: %T", raw)
			}
			m.cfg.MaxDailyTrades = int(clampFloat(v, float64(momMaxDailyFloor), float64(momMaxDailyCeil)))
		case "check_interval_secs":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("momentum: check_interval_secs not numeric: %T", raw)
			}
			m.cfg.CheckIntervalSecs = clampFloat(v, momCheckIntervalFloor, momCheckIntervalCeil)
		case "volume_confirmation":
			b, ok := raw.(bool)
			if !ok {
				return fmt.Errorf("momentum: volume_confirmation not boolean: %T", raw)
			}
			m.cfg.VolumeConfirmation = b
		default:
			// drop unknown
		}
	}
	return nil
}

func (m *Momentum) GetPositionValue(_ context.Context) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.position == nil {
		return 0, nil
	}
	last := m.latestPriceLocked()
	if last <= 0 {
		return m.position.SizeUSD, nil
	}
	return m.position.Units * last, nil
}

func (m *Momentum) CloseAllPositions(_ context.Context) ([]Trade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.position == nil {
		return nil, nil
	}
	return m.exitLocked(m.deps.Now(), m.latestPriceLocked(), "close_all"), nil
}

// --- internal --------------------------------------------------------------

func (m *Momentum) lookbackSamples() int {
	// One sample per CheckIntervalSecs; we keep enough to cover
	// LookbackMinutes plus a small head-room so the buffer doesn't
	// thrash at boundary.
	n := int(float64(m.cfg.LookbackMinutes) * 60 / m.cfg.CheckIntervalSecs)
	if n < 5 {
		n = 5
	}
	return n + 4
}

func (m *Momentum) pushSampleLocked(s priceSample) {
	m.history = append(m.history, s)
	cap := m.lookbackSamples()
	if len(m.history) > cap {
		m.history = append([]priceSample(nil), m.history[len(m.history)-cap:]...)
	}
}

// lookbackStatsLocked returns (high, low, mid, avg_volume) over the
// configured lookback window — EXCLUDING the most recent sample. The
// latest tick is the candidate breakout price; comparing it against a
// window that already contains it would mean a breakout could never
// trigger (the new high is always tied with the latest). Returns
// zeros when there are not yet enough samples.
func (m *Momentum) lookbackStatsLocked() (high, low, mid, avgVol float64) {
	if len(m.history) < 2 {
		return 0, 0, 0, 0
	}
	cutoff := m.deps.Now().Add(-time.Duration(m.cfg.LookbackMinutes) * time.Minute)
	first := true
	var volSum float64
	var n int
	// Iterate history[:len-1] — exclude the latest sample.
	for _, s := range m.history[:len(m.history)-1] {
		if s.At.Before(cutoff) {
			continue
		}
		if first {
			high = s.Price
			low = s.Price
			first = false
		} else {
			if s.Price > high {
				high = s.Price
			}
			if s.Price < low {
				low = s.Price
			}
		}
		volSum += s.Volume
		n++
	}
	if first {
		return 0, 0, 0, 0
	}
	mid = (high + low) / 2
	if n > 0 {
		avgVol = volSum / float64(n)
	}
	return high, low, mid, avgVol
}

func (m *Momentum) latestPriceLocked() float64 {
	if len(m.history) == 0 {
		return 0
	}
	return m.history[len(m.history)-1].Price
}

func (m *Momentum) latestVolumeLocked() float64 {
	if len(m.history) == 0 {
		return 0
	}
	return m.history[len(m.history)-1].Volume
}

func (m *Momentum) maybeEnterLocked(now time.Time, price float64) []Trade {
	if m.freeCapital < m.cfg.MinCapitalToOperate {
		return nil
	}
	if m.dailyTotal >= m.cfg.MaxDailyTrades {
		return nil
	}
	high, _, _, avgVol := m.lookbackStatsLocked()
	// Need enough samples to span the window before signaling.
	if high <= 0 {
		return nil
	}
	trigger := high * (1 + m.cfg.EntryThresholdPct)
	if price < trigger {
		return nil
	}
	if m.cfg.VolumeConfirmation {
		latestVol := m.latestVolumeLocked()
		if latestVol <= avgVol {
			return nil
		}
	}
	notional := m.freeCapital * m.cfg.MaxPositionSizePct
	if notional <= 0 {
		return nil
	}
	units := notional / price
	pos := &momentumPosition{
		OpenedAt:   now,
		EntryPrice: price,
		StopPrice:  price * (1 - m.cfg.StopLossPerTradePct),
		SizeUSD:    notional,
		Units:      units,
	}
	m.position = pos
	m.freeCapital -= notional
	m.dailyTotal++
	m.totalTrades++
	return []Trade{{
		Chain:        m.cfg.Chain,
		TradeType:    "momentum_enter",
		TokenPair:    m.cfg.TokenPair,
		DEX:          "spot",
		AmountIn:     notional,
		AmountOut:    units,
		IsPaperTrade: true,
		ExecutedAt:   now,
		Metadata: map[string]interface{}{
			"entry_price":  price,
			"stop_price":   pos.StopPrice,
			"window_high":  high,
			"trigger":      trigger,
			"avg_volume":   avgVol,
		},
	}}
}

func (m *Momentum) maybeExitLocked(now time.Time, price float64) []Trade {
	pos := m.position
	if pos == nil {
		return nil
	}
	// Stop-loss takes precedence.
	if price <= pos.StopPrice {
		return m.exitLocked(now, price, "stop_loss")
	}
	_, _, mid, _ := m.lookbackStatsLocked()
	if mid > 0 && price <= mid*(1-m.cfg.ExitThresholdPct) {
		return m.exitLocked(now, price, "exit_threshold")
	}
	return nil
}

func (m *Momentum) exitLocked(now time.Time, price float64, reason string) []Trade {
	pos := m.position
	if pos == nil {
		return nil
	}
	proceeds := pos.Units * price
	pnl := proceeds - pos.SizeUSD
	m.freeCapital += proceeds
	m.position = nil
	if pnl > 0 {
		m.totalWins++
	}
	return []Trade{{
		Chain:        m.cfg.Chain,
		TradeType:    "momentum_exit",
		TokenPair:    m.cfg.TokenPair,
		DEX:          "spot",
		AmountIn:     pos.Units,
		AmountOut:    proceeds,
		PnL:          pnl,
		IsPaperTrade: true,
		ExecutedAt:   now,
		Metadata: map[string]interface{}{
			"reason":      reason,
			"entry_price": pos.EntryPrice,
			"exit_price":  price,
		},
	}}
}

func (m *Momentum) winRateLocked() float64 {
	if m.totalTrades == 0 {
		return 0
	}
	return float64(m.totalWins) / float64(m.totalTrades)
}

func (m *Momentum) maybeResetDailyLocked(now time.Time) {
	day := now.UTC().Format("2006-01-02")
	if m.dailyDate == "" {
		m.dailyDate = day
		return
	}
	if day != m.dailyDate {
		m.dailyDate = day
		m.dailyTotal = 0
	}
}

// --- registration ----------------------------------------------------------

var momentumDeps atomic.Pointer[MomentumDeps]

// SetMomentumDeps wires the runtime's chain clients into the task
// factory.
func SetMomentumDeps(d MomentumDeps) {
	cp := d
	momentumDeps.Store(&cp)
}

func momentumFactory(_ context.Context, raw json.RawMessage) (Task, error) {
	d := momentumDeps.Load()
	if d == nil {
		return nil, errors.New("momentum: SetMomentumDeps not called")
	}
	var cfg MomentumConfig
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("momentum: decode config: %w", err)
		}
	}
	return NewMomentum(*d, cfg)
}

func init() {
	Register("momentum", momentumFactory)
}
