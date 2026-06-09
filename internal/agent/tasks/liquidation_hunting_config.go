package tasks

import (
	"errors"
	"fmt"
)

// LiquidationHuntingConfig is the persisted strategy_config for the
// liquidation_hunting task. Field set mirrors spec lines 308–316
// verbatim — the strategist (and the test runner) read and write JSON
// directly against this shape.
//
// Units:
//   - MinProfitUSD:          USD floor below which a candidate
//                            liquidation is skipped even if otherwise
//                            executable. Spec: 5-10% bonus is common,
//                            so a $5-25 floor is realistic for devnet.
//   - HealthFactorThreshold: dimensionless. A position with HF below
//                            this value is at risk and considered for
//                            liquidation. 1.0 is the on-chain default;
//                            running a hair below (0.95-1.05) is the
//                            ergonomic range.
//   - CheckIntervalSecs:     poll cadence in seconds. Spec line 312
//                            calls out "10 seconds — fast" — the
//                            tightest of the four tasks.
//   - MaxPositionSizeUSD:    cap on a single liquidation's collateral
//                            seized, so a runaway scrape doesn't blow
//                            the agent's wallet.
//   - MaxDailyLiquidations:  per-UTC-day execution cap. Resets when the
//                            current tick crosses midnight UTC.
//   - MinCapitalToOperate:   floor below which RunTick is a no-op.
type LiquidationHuntingConfig struct {
	Chain                 string   `json:"chain"`
	Protocols             []string `json:"protocols"`
	MinProfitUSD          float64  `json:"min_profit_usd"`
	HealthFactorThreshold float64  `json:"health_factor_threshold"`
	CheckIntervalSecs     float64  `json:"check_interval_secs"`
	MaxPositionSizeUSD    float64  `json:"max_position_size_usd"`
	MaxDailyLiquidations  int      `json:"max_daily_liquidations"`
	MinCapitalToOperate   float64  `json:"min_capital_to_operate"`
}

// Clamp envelopes shared by Validate (refuses out-of-range at boot) and
// ApplyAdjustments (clamps live updates).
const (
	liqMinProfitFloor     = 0.0
	liqMinProfitCeil      = 100_000.0
	liqHFThresholdFloor   = 0.50
	liqHFThresholdCeil    = 1.50
	liqCheckIntervalFloor = 1.0
	liqCheckIntervalCeil  = 600.0
	liqMaxDailyFloor      = 1
	liqMaxDailyCeil       = 1_000
)

func (c *LiquidationHuntingConfig) applyDefaults() {
	if c.HealthFactorThreshold == 0 {
		c.HealthFactorThreshold = 1.0
	}
	if c.CheckIntervalSecs == 0 {
		c.CheckIntervalSecs = 10 // spec line 312: 10 seconds — fast
	}
	if c.MaxDailyLiquidations == 0 {
		c.MaxDailyLiquidations = 20
	}
	if c.MinProfitUSD == 0 {
		c.MinProfitUSD = 5.0
	}
	if c.MaxPositionSizeUSD == 0 {
		c.MaxPositionSizeUSD = 10_000.0
	}
}

func (c *LiquidationHuntingConfig) Validate() error {
	if c.Chain == "" {
		return errors.New("liquidation_hunting: chain required")
	}
	if c.Chain != "solana" && c.Chain != "base" {
		return fmt.Errorf("liquidation_hunting: chain %q must be solana or base", c.Chain)
	}
	if len(c.Protocols) == 0 {
		return errors.New("liquidation_hunting: at least one protocol required")
	}
	if c.MinProfitUSD < liqMinProfitFloor || c.MinProfitUSD > liqMinProfitCeil {
		return fmt.Errorf("liquidation_hunting: min_profit_usd=%v out of [%v,%v]",
			c.MinProfitUSD, liqMinProfitFloor, liqMinProfitCeil)
	}
	if c.HealthFactorThreshold < liqHFThresholdFloor || c.HealthFactorThreshold > liqHFThresholdCeil {
		return fmt.Errorf("liquidation_hunting: health_factor_threshold=%v out of [%v,%v]",
			c.HealthFactorThreshold, liqHFThresholdFloor, liqHFThresholdCeil)
	}
	if c.CheckIntervalSecs < liqCheckIntervalFloor || c.CheckIntervalSecs > liqCheckIntervalCeil {
		return fmt.Errorf("liquidation_hunting: check_interval_secs=%v out of [%v,%v]",
			c.CheckIntervalSecs, liqCheckIntervalFloor, liqCheckIntervalCeil)
	}
	if c.MaxDailyLiquidations < liqMaxDailyFloor || c.MaxDailyLiquidations > liqMaxDailyCeil {
		return fmt.Errorf("liquidation_hunting: max_daily_liquidations=%v out of [%v,%v]",
			c.MaxDailyLiquidations, liqMaxDailyFloor, liqMaxDailyCeil)
	}
	if c.MaxPositionSizeUSD <= 0 {
		return errors.New("liquidation_hunting: max_position_size_usd must be positive")
	}
	if c.MinCapitalToOperate < 0 {
		return errors.New("liquidation_hunting: min_capital_to_operate must be non-negative")
	}
	return nil
}
