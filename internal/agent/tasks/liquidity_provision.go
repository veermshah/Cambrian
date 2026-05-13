package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veermshah/cambrian/internal/chain"
)

// LiquidityProvisionDeps bundles runtime dependencies for the LP task.
// Shape mirrors CrossChainYieldDeps so the runtime (chunk 14) can wire
// both task types from the same chain-client map.
type LiquidityProvisionDeps struct {
	Clients map[string]chain.ChainClient
	Wallets map[string]*chain.Wallet
	Now     func() time.Time
}

// LiquidityProvision implements the liquidity_provision task. Spec lines
// 284–300. It opens a concentrated-liquidity position centered on the
// current spot price with width RangeWidthPct, then rebalances the band
// whenever the price drifts more than RebalanceThresholdPct from band
// center.
//
// Real on-chain LP txs (Orca / Raydium / Uniswap / Aerodrome) are out of
// scope for chunk 12 — see prompt-pack "Out of scope". Trades are paper
// records; price reads go through chain.ChainClient.GetQuote.
type LiquidityProvision struct {
	cfg  LiquidityProvisionConfig
	deps LiquidityProvisionDeps

	tokenA, tokenB string

	mu sync.Mutex

	position *lpPosition

	freeCapital float64

	lastCheck     time.Time
	lastPrice     float64
	lastRebalance time.Time

	feeHistory []feeEvent
}

type lpPosition struct {
	OpenedAt    time.Time
	CenterPrice float64
	LowerPrice  float64
	UpperPrice  float64
	ValueUSD    float64
	EntryPrice  float64 // spot at open, used for IL math
	FeesEarned  float64
}

type feeEvent struct {
	At     time.Time
	Amount float64
}

var _ Task = (*LiquidityProvision)(nil)

// NewLiquidityProvision builds a task with the supplied deps and config.
// Used directly by tests; the registered factory wraps this once the
// runtime calls SetLiquidityProvisionDeps.
func NewLiquidityProvision(deps LiquidityProvisionDeps, cfg LiquidityProvisionConfig) (*LiquidityProvision, error) {
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if deps.Clients == nil {
		return nil, errors.New("liquidity_provision: deps.Clients required")
	}
	if _, ok := deps.Clients[cfg.Chain]; !ok {
		return nil, fmt.Errorf("liquidity_provision: chain %q missing from deps.Clients", cfg.Chain)
	}
	a, b, err := splitTokenPair(cfg.TokenPair)
	if err != nil {
		return nil, err
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &LiquidityProvision{
		cfg:    cfg,
		deps:   deps,
		tokenA: a,
		tokenB: b,
	}, nil
}

// SeedFreeCapital sets the USD capital available for the next position
// open. The runtime (chunk 14) calls this on spawn; tests call it
// directly.
func (l *LiquidityProvision) SeedFreeCapital(usd float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.freeCapital = usd
}

// --- Task interface --------------------------------------------------------

func (l *LiquidityProvision) RunTick(ctx context.Context) ([]Trade, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.deps.Now()

	if !l.lastCheck.IsZero() && now.Sub(l.lastCheck) < time.Duration(l.cfg.CheckIntervalSecs*float64(time.Second)) {
		return nil, nil
	}
	l.lastCheck = now

	if l.totalCapitalLocked() < l.cfg.MinCapitalToOperate {
		return nil, nil
	}

	client := l.deps.Clients[l.cfg.Chain]
	quote, err := client.GetQuote(ctx, l.tokenA, l.tokenB, 1.0)
	if err != nil {
		return nil, fmt.Errorf("liquidity_provision: get quote: %w", err)
	}
	price := quote.Price
	if price == 0 && quote.AmountIn > 0 {
		price = quote.AmountOut / quote.AmountIn
	}
	if price <= 0 {
		return nil, fmt.Errorf("liquidity_provision: non-positive price %v from quote", price)
	}
	l.lastPrice = price

	// No open position — open one centered on the current price.
	if l.position == nil {
		return l.openPosition(now, price), nil
	}

	// Drift > threshold ⇒ rebalance. Threshold is fraction of center.
	drift := math.Abs(price-l.position.CenterPrice) / l.position.CenterPrice
	if drift < l.cfg.RebalanceThresholdPct {
		return nil, nil
	}

	exits := l.closePosition(now, "rebalance")
	enters := l.openPosition(now, price)
	return append(exits, enters...), nil
}

func (l *LiquidityProvision) GetStateSummary(_ context.Context) (map[string]interface{}, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := map[string]interface{}{
		"chain":            l.cfg.Chain,
		"token_pair":       l.cfg.TokenPair,
		"free_capital_usd": l.freeCapital,
		"last_price":       l.lastPrice,
		"fees_24h_usd":     l.feesEarnedSinceLocked(24 * time.Hour),
	}
	if l.position != nil {
		out["band"] = map[string]interface{}{
			"center": l.position.CenterPrice,
			"lower":  l.position.LowerPrice,
			"upper":  l.position.UpperPrice,
			"value":  l.position.ValueUSD,
		}
		out["il_estimate"] = ImpermanentLoss(l.position.EntryPrice, l.lastPrice)
		out["fees_earned_total_usd"] = l.position.FeesEarned
	}
	if !l.lastRebalance.IsZero() {
		out["last_rebalance_at"] = l.lastRebalance.UTC().Format(time.RFC3339)
	}
	return out, nil
}

// ApplyAdjustments accepts the two strategist-controlled knobs
// (range_width_pct, rebalance_threshold_pct) and a strategist-only
// pull_liquidity action that closes the open position. Unknown keys are
// dropped; type mismatches return an error.
func (l *LiquidityProvision) ApplyAdjustments(adjustments map[string]interface{}) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	for k, raw := range adjustments {
		switch k {
		case "range_width_pct":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("liquidity_provision: range_width_pct not numeric: %T", raw)
			}
			l.cfg.RangeWidthPct = clampFloat(v, rangeWidthFloor, rangeWidthCeil)
		case "rebalance_threshold_pct":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("liquidity_provision: rebalance_threshold_pct not numeric: %T", raw)
			}
			l.cfg.RebalanceThresholdPct = clampFloat(v, rebalanceThreshFloor, rebalanceThreshCeil)
		case "check_interval_secs":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("liquidity_provision: check_interval_secs not numeric: %T", raw)
			}
			l.cfg.CheckIntervalSecs = clampFloat(v, lpCheckIntervalFloor, lpCheckIntervalCeil)
		case "pull_liquidity":
			b, ok := raw.(bool)
			if !ok {
				return fmt.Errorf("liquidity_provision: pull_liquidity not boolean: %T", raw)
			}
			if b && l.position != nil {
				now := l.deps.Now()
				_ = l.closePosition(now, "pull_liquidity")
			}
		default:
			// silently drop — sibling tasks share the strategist
			// vocabulary; not every key applies to every task.
		}
	}
	return nil
}

func (l *LiquidityProvision) GetPositionValue(_ context.Context) (float64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.position == nil {
		return 0, nil
	}
	return l.position.ValueUSD, nil
}

func (l *LiquidityProvision) CloseAllPositions(_ context.Context) ([]Trade, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.position == nil {
		return nil, nil
	}
	return l.closePosition(l.deps.Now(), "close_all"), nil
}

// --- internal helpers ------------------------------------------------------

func (l *LiquidityProvision) totalCapitalLocked() float64 {
	total := l.freeCapital
	if l.position != nil {
		total += l.position.ValueUSD
	}
	return total
}

func (l *LiquidityProvision) openPosition(now time.Time, price float64) []Trade {
	width := l.cfg.RangeWidthPct
	half := width / 2.0
	deposit := l.freeCapital
	if deposit <= 0 {
		return nil
	}
	l.position = &lpPosition{
		OpenedAt:    now,
		CenterPrice: price,
		LowerPrice:  price * (1 - half),
		UpperPrice:  price * (1 + half),
		ValueUSD:    deposit,
		EntryPrice:  price,
	}
	l.freeCapital = 0
	l.lastRebalance = now
	return []Trade{{
		Chain:        l.cfg.Chain,
		TradeType:    "lp_open",
		TokenPair:    l.cfg.TokenPair,
		DEX:          l.cfg.PoolAddress,
		AmountIn:     deposit,
		AmountOut:    deposit,
		IsPaperTrade: true,
		ExecutedAt:   now,
		Metadata: map[string]interface{}{
			"center":   price,
			"lower":    l.position.LowerPrice,
			"upper":    l.position.UpperPrice,
			"fee_tier": l.cfg.FeeTier,
		},
	}}
}

func (l *LiquidityProvision) closePosition(now time.Time, reason string) []Trade {
	if l.position == nil {
		return nil
	}
	pos := l.position
	// Realized IL eats into returned value. Treat fees earned as a credit
	// already accumulated on pos.FeesEarned (this PR doesn't simulate
	// trading volume — chunk 14 + chunk 25 will feed real fee data).
	il := ImpermanentLoss(pos.EntryPrice, l.lastPrice)
	netValue := pos.ValueUSD*(1+il) + pos.FeesEarned
	if netValue < 0 {
		netValue = 0
	}
	l.freeCapital += netValue
	l.position = nil
	return []Trade{{
		Chain:        l.cfg.Chain,
		TradeType:    "lp_close",
		TokenPair:    l.cfg.TokenPair,
		DEX:          l.cfg.PoolAddress,
		AmountIn:     pos.ValueUSD,
		AmountOut:    netValue,
		PnL:          netValue - pos.ValueUSD,
		IsPaperTrade: true,
		ExecutedAt:   now,
		Metadata: map[string]interface{}{
			"reason":       reason,
			"il_fraction":  il,
			"fees_earned":  pos.FeesEarned,
			"entry_price":  pos.EntryPrice,
			"exit_price":   l.lastPrice,
		},
	}}
}

// ImpermanentLoss is the closed-form constant-product AMM IL for a
// price ratio p1 / p0. Returns a non-positive fraction: 0 when prices
// are unchanged, negative as drift grows in either direction. Monotonic
// in |log(price_ratio)|.
//
//   IL(r) = 2 * sqrt(r) / (1 + r) - 1
//
// (See Uniswap's "Impermanent Loss in Uniswap" note.) For
// concentrated-liquidity positions outside the band, the loss
// approaches the held-token-only path; this estimator ignores that
// boundary and treats the position as full-range — a conservative
// (looser-than-actual) bound, fine for strategist reasoning.
func ImpermanentLoss(entry, current float64) float64 {
	if entry <= 0 || current <= 0 {
		return 0
	}
	r := current / entry
	return 2*math.Sqrt(r)/(1+r) - 1
}

func (l *LiquidityProvision) feesEarnedSinceLocked(d time.Duration) float64 {
	cutoff := l.deps.Now().Add(-d)
	var total float64
	for _, e := range l.feeHistory {
		if e.At.After(cutoff) {
			total += e.Amount
		}
	}
	return total
}

// --- registration ----------------------------------------------------------

var liquidityProvisionDeps atomic.Pointer[LiquidityProvisionDeps]

// SetLiquidityProvisionDeps wires the runtime's chain clients into the
// task factory. The runtime (chunk 14) calls this before booting any
// liquidity_provision agent. Tests construct via NewLiquidityProvision.
func SetLiquidityProvisionDeps(d LiquidityProvisionDeps) {
	cp := d
	liquidityProvisionDeps.Store(&cp)
}

func liquidityProvisionFactory(_ context.Context, raw json.RawMessage) (Task, error) {
	d := liquidityProvisionDeps.Load()
	if d == nil {
		return nil, errors.New("liquidity_provision: SetLiquidityProvisionDeps not called")
	}
	var cfg LiquidityProvisionConfig
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("liquidity_provision: decode config: %w", err)
		}
	}
	return NewLiquidityProvision(*d, cfg)
}

func init() {
	Register("liquidity_provision", liquidityProvisionFactory)
}
