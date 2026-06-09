package tasks

import (
	"errors"
	"fmt"
)

// MomentumConfig is the persisted strategy_config for the momentum
// task. Field set mirrors spec lines 324–338 verbatim.
//
// Units:
//   - LookbackMinutes:      window length for the breakout reference
//                           price; 60–240 (1–4 hour) per spec line 321.
//   - EntryThresholdPct:    fraction in (0, 1]. Price must close above
//                           rolling-high × (1 + threshold) to trigger
//                           entry. 0.02 = +2 %.
//   - ExitThresholdPct:     fraction in (0, 1]. Price below rolling-mid
//                           × (1 − threshold) flattens the position.
//   - VolumeConfirmation:   require volume above the lookback average.
//   - CheckIntervalSecs:    poll cadence (spec: 30 s).
//   - MaxPositionSizePct:   cap on per-trade notional as a fraction
//                           of free capital.
//   - MaxDrawdownPct:       overall drawdown cap before the task
//                           refuses to open new positions.
//   - StopLossPerTradePct:  per-position stop in fraction of entry.
//   - MaxDailyTrades:       per-UTC-day execution cap.
//   - MinCapitalToOperate:  USD floor below which RunTick is a no-op.
type MomentumConfig struct {
	Chain               string  `json:"chain"`
	TokenPair           string  `json:"token_pair"`
	LookbackMinutes     int     `json:"lookback_minutes"`
	EntryThresholdPct   float64 `json:"entry_threshold_pct"`
	ExitThresholdPct    float64 `json:"exit_threshold_pct"`
	VolumeConfirmation  bool    `json:"volume_confirmation"`
	CheckIntervalSecs   float64 `json:"check_interval_secs"`
	MaxPositionSizePct  float64 `json:"max_position_size_pct"`
	MaxDrawdownPct      float64 `json:"max_drawdown_pct"`
	StopLossPerTradePct float64 `json:"stop_loss_per_trade_pct"`
	MaxDailyTrades      int     `json:"max_daily_trades"`
	MinCapitalToOperate float64 `json:"min_capital_to_operate"`
}

// Clamp envelopes shared by Validate and ApplyAdjustments.
const (
	momLookbackFloor      = 5
	momLookbackCeil       = 1440
	momEntryThreshFloor   = 0.001 // 0.1 %
	momEntryThreshCeil    = 0.50
	momExitThreshFloor    = 0.001
	momExitThreshCeil     = 0.50
	momCheckIntervalFloor = 5.0
	momCheckIntervalCeil  = 3600.0
	momMaxPosPctFloor     = 0.01
	momMaxPosPctCeil      = 1.0
	momStopLossFloor      = 0.001
	momStopLossCeil       = 0.50
	momMaxDailyFloor      = 1
	momMaxDailyCeil       = 200
)

func (c *MomentumConfig) applyDefaults() {
	if c.LookbackMinutes == 0 {
		c.LookbackMinutes = 60 // 1 hour
	}
	if c.EntryThresholdPct == 0 {
		c.EntryThresholdPct = 0.02 // 2 %
	}
	if c.ExitThresholdPct == 0 {
		c.ExitThresholdPct = 0.01
	}
	if c.CheckIntervalSecs == 0 {
		c.CheckIntervalSecs = 30 // spec line 331
	}
	if c.MaxPositionSizePct == 0 {
		c.MaxPositionSizePct = 0.25
	}
	if c.StopLossPerTradePct == 0 {
		c.StopLossPerTradePct = 0.05
	}
	if c.MaxDailyTrades == 0 {
		c.MaxDailyTrades = 10
	}
}

func (c *MomentumConfig) Validate() error {
	if c.Chain == "" {
		return errors.New("momentum: chain required")
	}
	if c.Chain != "solana" && c.Chain != "base" {
		return fmt.Errorf("momentum: chain %q must be solana or base", c.Chain)
	}
	if c.TokenPair == "" {
		return errors.New("momentum: token_pair required")
	}
	if _, _, err := splitTokenPair(c.TokenPair); err != nil {
		return err
	}
	if c.LookbackMinutes < momLookbackFloor || c.LookbackMinutes > momLookbackCeil {
		return fmt.Errorf("momentum: lookback_minutes=%v out of [%v,%v]", c.LookbackMinutes, momLookbackFloor, momLookbackCeil)
	}
	if c.EntryThresholdPct < momEntryThreshFloor || c.EntryThresholdPct > momEntryThreshCeil {
		return fmt.Errorf("momentum: entry_threshold_pct=%v out of [%v,%v]", c.EntryThresholdPct, momEntryThreshFloor, momEntryThreshCeil)
	}
	if c.ExitThresholdPct < momExitThreshFloor || c.ExitThresholdPct > momExitThreshCeil {
		return fmt.Errorf("momentum: exit_threshold_pct=%v out of [%v,%v]", c.ExitThresholdPct, momExitThreshFloor, momExitThreshCeil)
	}
	if c.CheckIntervalSecs < momCheckIntervalFloor || c.CheckIntervalSecs > momCheckIntervalCeil {
		return fmt.Errorf("momentum: check_interval_secs=%v out of [%v,%v]", c.CheckIntervalSecs, momCheckIntervalFloor, momCheckIntervalCeil)
	}
	if c.MaxPositionSizePct < momMaxPosPctFloor || c.MaxPositionSizePct > momMaxPosPctCeil {
		return fmt.Errorf("momentum: max_position_size_pct=%v out of [%v,%v]", c.MaxPositionSizePct, momMaxPosPctFloor, momMaxPosPctCeil)
	}
	if c.StopLossPerTradePct < momStopLossFloor || c.StopLossPerTradePct > momStopLossCeil {
		return fmt.Errorf("momentum: stop_loss_per_trade_pct=%v out of [%v,%v]", c.StopLossPerTradePct, momStopLossFloor, momStopLossCeil)
	}
	if c.MaxDailyTrades < momMaxDailyFloor || c.MaxDailyTrades > momMaxDailyCeil {
		return fmt.Errorf("momentum: max_daily_trades=%v out of [%v,%v]", c.MaxDailyTrades, momMaxDailyFloor, momMaxDailyCeil)
	}
	if c.MinCapitalToOperate < 0 {
		return errors.New("momentum: min_capital_to_operate must be non-negative")
	}
	return nil
}
