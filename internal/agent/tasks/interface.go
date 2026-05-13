// Package tasks defines the Task contract every strategy implements
// (CrossChainYield, LiquidityProvision, LiquidationHunting, Momentum)
// and the registry the orchestrator uses to spawn them. Concrete tasks
// live in their own files inside this package and register themselves
// via init().
//
// Spec references:
//   - lines 227–243: Task interface
//   - lines 249–254: TaskRegistry shape
package tasks

import (
	"context"
	"encoding/json"
	"time"
)

// Trade is one executed (or paper-traded) DEX action. Fields mirror the
// trades table from the chunk-2 schema so a Task can hand the slice
// straight to db.LogTrade without translation.
//
// Empty AgentID / EpochID at this layer is fine — the runtime
// (chunk 14) fills them in before persisting. Tasks should set the
// economic fields (chain, trade_type, token_pair, dex, amounts, pnl)
// and the policy/metadata fields (bandit_policy_used, metadata).
type Trade struct {
	ID               string                 `json:"id,omitempty"`
	AgentID          string                 `json:"agent_id,omitempty"`
	EpochID          string                 `json:"epoch_id,omitempty"`
	Chain            string                 `json:"chain"`
	TradeType        string                 `json:"trade_type"`
	TokenPair        string                 `json:"token_pair"`
	DEX              string                 `json:"dex"`
	AmountIn         float64                `json:"amount_in"`
	AmountOut        float64                `json:"amount_out"`
	FeePaid          float64                `json:"fee_paid"`
	PnL              float64                `json:"pnl"`
	TxSignature      string                 `json:"tx_signature,omitempty"`
	IsPaperTrade     bool                   `json:"is_paper_trade"`
	BanditPolicyUsed string                 `json:"bandit_policy_used,omitempty"`
	ExecutedAt       time.Time              `json:"executed_at"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
}

// Task is the contract every strategy implements. The orchestrator's
// NodeRunner (chunk 14) calls these in a fixed pattern: RunTick on every
// tick of the fast loop, GetStateSummary once per strategist cycle,
// ApplyAdjustments when the strategist returns a config delta, and
// CloseAllPositions on agent kill/pause.
type Task interface {
	// RunTick is the fast loop: check conditions and execute if
	// warranted. Returns the trades produced this tick (may be empty).
	RunTick(ctx context.Context) ([]Trade, error)

	// GetStateSummary packages task-local state for the strategist
	// prompt. Caller treats it as opaque JSON; should fit under 500
	// tokens after marshaling (the strategist budget).
	GetStateSummary(ctx context.Context) (map[string]interface{}, error)

	// ApplyAdjustments applies a bounded config delta from the
	// strategist. Implementations should validate each key and reject
	// out-of-range values rather than silently clamp.
	ApplyAdjustments(adjustments map[string]interface{}) error

	// GetPositionValue returns total USD value of currently held
	// positions (open LP, lent collateral, etc.).
	GetPositionValue(ctx context.Context) (float64, error)

	// CloseAllPositions gracefully exits every open position and
	// returns the resulting trades. Called on kill, pause, or
	// chain-migration mutations.
	CloseAllPositions(ctx context.Context) ([]Trade, error)
}

// TaskFactory builds a Task from its raw JSON config. The runtime hands
// the agent's strategy_config column straight through, so factories are
// responsible for unmarshaling into their concrete config type.
type TaskFactory func(ctx context.Context, config json.RawMessage) (Task, error)
