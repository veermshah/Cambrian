package e2e

import (
	"testing"
)

// TestBacktestValidation_ShadowVsBacktest — spec line 1196.
// Gated. Compares a shadow node's realized P&L over a window to its
// backtest prediction; the spec calls for "within 25%" tracking.
//
// Why gated: this requires (a) a live shadow node with at least 30
// days of trades on disk, (b) the chunk-25 backtest engine to replay
// the same window deterministically. Until the operator has a 30-day
// devnet trace, this test asserts only that the gate plumbing works.
func TestBacktestValidation_ShadowVsBacktest(t *testing.T) {
	requireE2E(t)
	t.Skip("requires 30-day devnet shadow node trace + backtest replay — wire after the first 30-day run completes")
}
