package tasks

import (
	"errors"
	"fmt"
	"strings"
)

// LiquidityProvisionConfig is the persisted strategy_config for the
// liquidity_provision task. Field set mirrors spec lines 289–298.
//
// Units:
//   - RangeWidthPct:         fraction in (0, 1] — total width of the band
//                            as a fraction of the band's center price
//                            (0.10 = ±5 % around center).
//   - RebalanceThresholdPct: fraction in (0, 1] — how far the spot price
//                            must drift from band center before we
//                            rebalance (0.03 = ±3 %).
//   - CheckIntervalSecs:     seconds between spot-price polls.
type LiquidityProvisionConfig struct {
	Chain                 string  `json:"chain"`
	TokenPair             string  `json:"token_pair"`
	PoolAddress           string  `json:"pool_address"`
	FeeTier               string  `json:"fee_tier"`
	RangeWidthPct         float64 `json:"range_width_pct"`
	RebalanceThresholdPct float64 `json:"rebalance_threshold_pct"`
	CheckIntervalSecs     float64 `json:"check_interval_secs"`
	MaxDrawdownPct        float64 `json:"max_drawdown_pct"`
	MinCapitalToOperate   float64 `json:"min_capital_to_operate"`
}

// Clamp envelopes shared by Validate (refuses out-of-range at boot) and
// ApplyAdjustments (clamps live updates).
const (
	rangeWidthFloor       = 0.005 // 0.5 %
	rangeWidthCeil        = 1.0
	rebalanceThreshFloor  = 0.001 // 0.1 %
	rebalanceThreshCeil   = 0.5
	lpCheckIntervalFloor  = 5.0
	lpCheckIntervalCeil   = 3600.0
)

func (c *LiquidityProvisionConfig) applyDefaults() {
	if c.RangeWidthPct == 0 {
		c.RangeWidthPct = 0.10 // ±5 % default band
	}
	if c.RebalanceThresholdPct == 0 {
		c.RebalanceThresholdPct = 0.03 // 3 % drift triggers rebalance
	}
	if c.CheckIntervalSecs == 0 {
		c.CheckIntervalSecs = 60
	}
}

func (c *LiquidityProvisionConfig) Validate() error {
	if c.Chain == "" {
		return errors.New("liquidity_provision: chain required")
	}
	if c.Chain != "solana" && c.Chain != "base" {
		return fmt.Errorf("liquidity_provision: chain %q must be solana or base", c.Chain)
	}
	if c.TokenPair == "" {
		return errors.New("liquidity_provision: token_pair required")
	}
	if _, _, err := splitTokenPair(c.TokenPair); err != nil {
		return err
	}
	if c.RangeWidthPct < rangeWidthFloor || c.RangeWidthPct > rangeWidthCeil {
		return fmt.Errorf("liquidity_provision: range_width_pct=%v out of [%v,%v]",
			c.RangeWidthPct, rangeWidthFloor, rangeWidthCeil)
	}
	if c.RebalanceThresholdPct < rebalanceThreshFloor || c.RebalanceThresholdPct > rebalanceThreshCeil {
		return fmt.Errorf("liquidity_provision: rebalance_threshold_pct=%v out of [%v,%v]",
			c.RebalanceThresholdPct, rebalanceThreshFloor, rebalanceThreshCeil)
	}
	if c.CheckIntervalSecs < lpCheckIntervalFloor || c.CheckIntervalSecs > lpCheckIntervalCeil {
		return fmt.Errorf("liquidity_provision: check_interval_secs=%v out of [%v,%v]",
			c.CheckIntervalSecs, lpCheckIntervalFloor, lpCheckIntervalCeil)
	}
	return nil
}

// splitTokenPair turns "USDC/SOL" or "USDC-SOL" into ("USDC", "SOL").
// The strategist receives the pair as a single string from the genome;
// the task itself wants the two legs for chain.GetQuote.
func splitTokenPair(pair string) (string, string, error) {
	for _, sep := range []string{"/", "-", ":"} {
		if i := strings.Index(pair, sep); i > 0 && i < len(pair)-1 {
			return strings.ToUpper(pair[:i]), strings.ToUpper(pair[i+1:]), nil
		}
	}
	return "", "", fmt.Errorf("liquidity_provision: token_pair %q must look like TOKEN_A/TOKEN_B", pair)
}
