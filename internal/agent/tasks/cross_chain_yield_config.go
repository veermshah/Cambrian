package tasks

import (
	"errors"
	"fmt"
)

// CrossChainYieldConfig is the persisted strategy_config for the
// cross_chain_yield task. Field set mirrors spec lines 268–280 verbatim.
//
// Units:
//   - MinYieldDiffBps:       basis points (100 = 1.00 percentage point)
//   - MaxSingleProtocolPct:  fraction in [0, 1] (0.4 = 40 %)
//   - RebalanceIntervalSecs: seconds between consecutive rebalances
//   - BridgeCostThreshold:   USD ceiling on per-rebalance bridge cost
//   - MaxDrawdownPct:        fraction in [0, 1] (advisory; chunk 21 enforces)
//   - MinCapitalToOperate:   USD floor — task no-ops below this
//   - CheckIntervalSecs:     seconds between yield-rate polls inside RunTick
type CrossChainYieldConfig struct {
	PrimaryChain          string   `json:"primary_chain"`
	AllowedProtocols      []string `json:"allowed_protocols"`
	MinYieldDiffBps       float64  `json:"min_yield_diff_bps"`
	MaxSingleProtocolPct  float64  `json:"max_single_protocol_pct"`
	RebalanceIntervalSecs int      `json:"rebalance_interval_secs"`
	BridgeCostThreshold   float64  `json:"bridge_cost_threshold"`
	MaxDrawdownPct        float64  `json:"max_drawdown_pct"`
	MinCapitalToOperate   float64  `json:"min_capital_to_operate"`
	CheckIntervalSecs     float64  `json:"check_interval_secs"`
}

// Clamp ranges for ApplyAdjustments. Values outside these bounds are
// snapped to the nearest edge rather than rejected — the strategist may
// emit out-of-range numbers and the spec calls for a defensive clamp
// (see prompt-pack chunk 11 "ApplyAdjustments: clamps then applies").
const (
	minYieldDiffBpsFloor   = 1.0
	minYieldDiffBpsCeil    = 5000.0
	maxSingleProtocolFloor = 0.05
	maxSingleProtocolCeil  = 1.0
	rebalanceIntervalFloor = 60      // 1 min
	rebalanceIntervalCeil  = 7 * 24 * 3600
	bridgeCostFloor        = 0.0
	bridgeCostCeil         = 10000.0
	checkIntervalSecsFloor = 5.0
	checkIntervalSecsCeil  = 3600.0
)

// applyDefaults fills zero-valued numeric fields with the spec defaults
// (CheckIntervalSecs=60, RebalanceIntervalSecs=3600) so a freshly
// unmarshaled config is immediately usable.
func (c *CrossChainYieldConfig) applyDefaults() {
	if c.CheckIntervalSecs == 0 {
		c.CheckIntervalSecs = 60
	}
	if c.RebalanceIntervalSecs == 0 {
		c.RebalanceIntervalSecs = 3600
	}
	if c.MaxSingleProtocolPct == 0 {
		c.MaxSingleProtocolPct = 1.0
	}
	if c.MinYieldDiffBps == 0 {
		c.MinYieldDiffBps = 50 // 0.5 pp default
	}
}

// Validate checks that required string/slice fields are populated and
// that numerics are inside the clamp envelope. Numeric out-of-range is a
// soft failure — Validate returns an error so the runtime can refuse to
// boot a malformed genome, while ApplyAdjustments uses the same bounds
// to clamp live updates without erroring.
func (c *CrossChainYieldConfig) Validate() error {
	if c.PrimaryChain == "" {
		return errors.New("cross_chain_yield: primary_chain required")
	}
	if c.PrimaryChain != "solana" && c.PrimaryChain != "base" {
		return fmt.Errorf("cross_chain_yield: primary_chain %q must be solana or base", c.PrimaryChain)
	}
	if len(c.AllowedProtocols) == 0 {
		return errors.New("cross_chain_yield: allowed_protocols cannot be empty")
	}
	if c.MinYieldDiffBps < minYieldDiffBpsFloor || c.MinYieldDiffBps > minYieldDiffBpsCeil {
		return fmt.Errorf("cross_chain_yield: min_yield_diff_bps=%v out of [%v,%v]",
			c.MinYieldDiffBps, minYieldDiffBpsFloor, minYieldDiffBpsCeil)
	}
	if c.MaxSingleProtocolPct < maxSingleProtocolFloor || c.MaxSingleProtocolPct > maxSingleProtocolCeil {
		return fmt.Errorf("cross_chain_yield: max_single_protocol_pct=%v out of [%v,%v]",
			c.MaxSingleProtocolPct, maxSingleProtocolFloor, maxSingleProtocolCeil)
	}
	if c.RebalanceIntervalSecs < rebalanceIntervalFloor || c.RebalanceIntervalSecs > rebalanceIntervalCeil {
		return fmt.Errorf("cross_chain_yield: rebalance_interval_secs=%v out of [%v,%v]",
			c.RebalanceIntervalSecs, rebalanceIntervalFloor, rebalanceIntervalCeil)
	}
	if c.BridgeCostThreshold < bridgeCostFloor || c.BridgeCostThreshold > bridgeCostCeil {
		return fmt.Errorf("cross_chain_yield: bridge_cost_threshold=%v out of [%v,%v]",
			c.BridgeCostThreshold, bridgeCostFloor, bridgeCostCeil)
	}
	if c.CheckIntervalSecs < checkIntervalSecsFloor || c.CheckIntervalSecs > checkIntervalSecsCeil {
		return fmt.Errorf("cross_chain_yield: check_interval_secs=%v out of [%v,%v]",
			c.CheckIntervalSecs, checkIntervalSecsFloor, checkIntervalSecsCeil)
	}
	return nil
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
