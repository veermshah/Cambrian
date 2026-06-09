package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veermshah/cambrian/internal/chain"
)

// LiquidationHuntingDeps bundles the runtime dependencies for the
// liquidation_hunting task. Shape matches the sibling tasks so the
// runtime (chunk 14) wires every task from the same chain-client map.
//
// Now is overridable for deterministic tests; production passes nil
// (which falls back to time.Now) or time.Now directly.
type LiquidationHuntingDeps struct {
	Clients map[string]chain.ChainClient
	Wallets map[string]*chain.Wallet
	Now     func() time.Time
}

// LiquidationHunting implements the liquidation_hunting task. Spec
// lines 302–317. It polls every protocol on the configured chain
// for lending positions whose health factor has fallen below
// HealthFactorThreshold, then — for each at-risk position — estimates
// the seized-collateral profit and triggers ExecuteLiquidation if the
// profit exceeds MinProfitUSD.
//
// Daily quota enforcement is per-UTC-day: count resets when the
// current tick observes a wall-clock date later than the day the last
// observed count was last attributed to.
type LiquidationHunting struct {
	cfg  LiquidationHuntingConfig
	deps LiquidationHuntingDeps

	mu sync.Mutex

	freeCapital float64

	lastCheck time.Time

	// Daily liquidation accounting — reset on UTC date change.
	dailyDate    string
	dailyCount   int
	dailySuccess int
	dailyAttempt int
	totalSuccess int
	totalAttempt int

	atRiskSnapshot []chain.LendingPosition
}

var _ Task = (*LiquidationHunting)(nil)

// NewLiquidationHunting builds a task with the supplied deps and config.
// Used directly by tests; the registered factory wraps this once the
// runtime calls SetLiquidationHuntingDeps.
func NewLiquidationHunting(deps LiquidationHuntingDeps, cfg LiquidationHuntingConfig) (*LiquidationHunting, error) {
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if deps.Clients == nil {
		return nil, errors.New("liquidation_hunting: deps.Clients required")
	}
	if _, ok := deps.Clients[cfg.Chain]; !ok {
		return nil, fmt.Errorf("liquidation_hunting: chain %q missing from deps.Clients", cfg.Chain)
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &LiquidationHunting{cfg: cfg, deps: deps}, nil
}

// SeedFreeCapital sets the USD capital available for liquidation
// operations. The runtime (chunk 14) calls this on spawn; tests call
// it directly.
func (l *LiquidationHunting) SeedFreeCapital(usd float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.freeCapital = usd
}

// --- Task interface --------------------------------------------------------

func (l *LiquidationHunting) RunTick(ctx context.Context) ([]Trade, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.deps.Now()
	l.maybeResetDailyLocked(now)

	if !l.lastCheck.IsZero() && now.Sub(l.lastCheck) < time.Duration(l.cfg.CheckIntervalSecs*float64(time.Second)) {
		return nil, nil
	}
	l.lastCheck = now

	if l.freeCapital < l.cfg.MinCapitalToOperate {
		return nil, nil
	}
	if l.dailyCount >= l.cfg.MaxDailyLiquidations {
		return nil, nil
	}

	client := l.deps.Clients[l.cfg.Chain]
	wallet := l.deps.Wallets[l.cfg.Chain]

	var (
		out      []Trade
		atRisk   []chain.LendingPosition
		lastErr  error
	)

	for _, proto := range l.cfg.Protocols {
		positions, err := client.GetLendingPositions(ctx, proto)
		if err != nil {
			lastErr = fmt.Errorf("liquidation_hunting: GetLendingPositions(%s): %w", proto, err)
			continue
		}
		for _, pos := range positions {
			if pos.HealthFactor >= l.cfg.HealthFactorThreshold {
				continue
			}
			atRisk = append(atRisk, pos)

			collateralUSD := pos.CollateralAmt
			if collateralUSD > l.cfg.MaxPositionSizeUSD {
				// Skip oversize positions outright — we don't partial-fill;
				// chunk 27 may add laddered execution. The position stays
				// in atRisk for the strategist's summary.
				continue
			}
			bonusFraction := pos.LiquidationBonus / 10_000.0
			estProfit := collateralUSD * bonusFraction
			if estProfit < l.cfg.MinProfitUSD {
				continue
			}
			if l.dailyCount >= l.cfg.MaxDailyLiquidations {
				break
			}

			l.dailyAttempt++
			l.totalAttempt++
			res, err := client.ExecuteLiquidation(ctx, &pos, wallet)
			if err != nil || res == nil || !res.Success {
				if err != nil {
					lastErr = fmt.Errorf("liquidation_hunting: ExecuteLiquidation: %w", err)
				}
				continue
			}
			l.dailyCount++
			l.dailySuccess++
			l.totalSuccess++

			// PnL is the seized bonus; gas / fees are charged separately
			// by the chain layer via TxResult.FeePaidUSD when those costs
			// are modelled. The devnet implementation reports 0 for
			// FeePaidUSD and stashes the bonus in TxResult.GasUsed; the
			// fake reports the bonus *as* FeePaidUSD. We trust our own
			// estProfit calculation here to stay independent of those
			// quirks.
			out = append(out, Trade{
				Chain:        pos.Chain,
				TradeType:    "liquidation",
				TokenPair:    pos.CollateralAsset + "/" + pos.DebtAsset,
				DEX:          pos.Protocol,
				AmountIn:     collateralUSD,
				AmountOut:    collateralUSD + estProfit,
				FeePaid:      0,
				PnL:          estProfit,
				TxSignature:  res.Signature,
				IsPaperTrade: false,
				ExecutedAt:   now,
				Metadata: map[string]interface{}{
					"protocol":              pos.Protocol,
					"owner":                 pos.Owner,
					"health_factor":         pos.HealthFactor,
					"liquidation_bonus_bps": pos.LiquidationBonus,
					"collateral_asset":      pos.CollateralAsset,
					"debt_asset":            pos.DebtAsset,
				},
			})
		}
	}
	l.atRiskSnapshot = atRisk
	if lastErr != nil && len(out) == 0 {
		return nil, lastErr
	}
	return out, nil
}

// GetStateSummary reports liquidations today, success rate, and the
// top-3 at-risk positions (by health factor ascending — lowest HF is
// most urgent). Matches spec line 1280.
func (l *LiquidationHunting) GetStateSummary(_ context.Context) (map[string]interface{}, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	successRate := 0.0
	if l.totalAttempt > 0 {
		successRate = float64(l.totalSuccess) / float64(l.totalAttempt)
	}

	top := append([]chain.LendingPosition(nil), l.atRiskSnapshot...)
	sort.Slice(top, func(i, j int) bool { return top[i].HealthFactor < top[j].HealthFactor })
	if len(top) > 3 {
		top = top[:3]
	}
	topOut := make([]map[string]interface{}, 0, len(top))
	for _, p := range top {
		topOut = append(topOut, map[string]interface{}{
			"protocol":         p.Protocol,
			"owner":            p.Owner,
			"health_factor":    p.HealthFactor,
			"collateral_asset": p.CollateralAsset,
			"collateral_amt":   p.CollateralAmt,
			"debt_asset":       p.DebtAsset,
			"debt_amt":         p.DebtAmt,
			"bonus_bps":        p.LiquidationBonus,
		})
	}

	return map[string]interface{}{
		"chain":              l.cfg.Chain,
		"protocols":          l.cfg.Protocols,
		"liquidations_today": l.dailyCount,
		"max_daily":          l.cfg.MaxDailyLiquidations,
		"success_rate":       successRate,
		"total_success":      l.totalSuccess,
		"total_attempts":     l.totalAttempt,
		"free_capital_usd":   l.freeCapital,
		"top_at_risk":        topOut,
		"daily_quota_used":   l.dailyCount,
	}, nil
}

// ApplyAdjustments accepts strategist-controlled clamps on the three
// thresholds called out by chunk 26's prompt-pack scope.
func (l *LiquidationHunting) ApplyAdjustments(adjustments map[string]interface{}) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	for k, raw := range adjustments {
		switch k {
		case "min_profit_usd":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("liquidation_hunting: min_profit_usd not numeric: %T", raw)
			}
			l.cfg.MinProfitUSD = clampFloat(v, liqMinProfitFloor, liqMinProfitCeil)
		case "health_factor_threshold":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("liquidation_hunting: health_factor_threshold not numeric: %T", raw)
			}
			l.cfg.HealthFactorThreshold = clampFloat(v, liqHFThresholdFloor, liqHFThresholdCeil)
		case "max_daily_liquidations":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("liquidation_hunting: max_daily_liquidations not numeric: %T", raw)
			}
			clamped := int(clampFloat(v, float64(liqMaxDailyFloor), float64(liqMaxDailyCeil)))
			l.cfg.MaxDailyLiquidations = clamped
		case "check_interval_secs":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("liquidation_hunting: check_interval_secs not numeric: %T", raw)
			}
			l.cfg.CheckIntervalSecs = clampFloat(v, liqCheckIntervalFloor, liqCheckIntervalCeil)
		default:
			// Silently drop unknown keys — sibling tasks share strategist
			// vocabulary; not every key applies to every task.
		}
	}
	return nil
}

// GetPositionValue is always zero — the liquidation_hunting strategy
// flips collateral immediately on seize, so it never holds open
// positions of its own.
func (l *LiquidationHunting) GetPositionValue(_ context.Context) (float64, error) {
	return 0, nil
}

// CloseAllPositions is a no-op for the same reason as GetPositionValue.
// Returning an empty trade slice (rather than an error) keeps the
// orchestrator's kill path uniform across task types.
func (l *LiquidationHunting) CloseAllPositions(_ context.Context) ([]Trade, error) {
	return nil, nil
}

// --- internal --------------------------------------------------------------

// maybeResetDailyLocked rolls over the daily quota when the UTC date
// changes. Caller must hold l.mu.
func (l *LiquidationHunting) maybeResetDailyLocked(now time.Time) {
	day := now.UTC().Format("2006-01-02")
	if l.dailyDate == "" {
		l.dailyDate = day
		return
	}
	if day != l.dailyDate {
		l.dailyDate = day
		l.dailyCount = 0
		l.dailySuccess = 0
		l.dailyAttempt = 0
	}
}

// --- registration ----------------------------------------------------------

var liquidationHuntingDeps atomic.Pointer[LiquidationHuntingDeps]

// SetLiquidationHuntingDeps wires the runtime's chain clients into the
// task factory. The runtime (chunk 14) calls this before booting any
// liquidation_hunting agent. Tests construct via NewLiquidationHunting.
func SetLiquidationHuntingDeps(d LiquidationHuntingDeps) {
	cp := d
	liquidationHuntingDeps.Store(&cp)
}

func liquidationHuntingFactory(_ context.Context, raw json.RawMessage) (Task, error) {
	d := liquidationHuntingDeps.Load()
	if d == nil {
		return nil, errors.New("liquidation_hunting: SetLiquidationHuntingDeps not called")
	}
	var cfg LiquidationHuntingConfig
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("liquidation_hunting: decode config: %w", err)
		}
	}
	return NewLiquidationHunting(*d, cfg)
}

func init() {
	Register("liquidation_hunting", liquidationHuntingFactory)
}
