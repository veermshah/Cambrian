// Package chain defines the chain-agnostic abstraction the swarm uses to
// trade on Solana and Base. The types here are referenced by `ChainClient`
// (see interface.go) and are deliberately concrete enough to flow through
// strategist decisions, intel signals, and the database.
package chain

import "time"

// Quote is the result of asking a DEX aggregator (Jupiter on Solana, 1inch /
// Paraswap on Base) what a given input token would buy. RouteRaw carries the
// implementation-specific data needed to execute the same quote later.
type Quote struct {
	Chain         string    `json:"chain"`
	DEX           string    `json:"dex"`
	TokenIn       string    `json:"token_in"`
	TokenOut      string    `json:"token_out"`
	AmountIn      float64   `json:"amount_in"`
	AmountOut     float64   `json:"amount_out"`
	Price         float64   `json:"price"`
	PriceImpact   float64   `json:"price_impact"`
	FeeAmount     float64   `json:"fee_amount"`
	SlippageBps   int       `json:"slippage_bps"`
	GeneratedAt   time.Time `json:"generated_at"`
	RouteRaw      []byte    `json:"-"`
}

// Trade is a record of an executed swap, stored on agents' ledgers and used
// by the intel bus. Independent of the on-chain primitive — the
// implementation translates ExecuteSwap into chain-specific transaction
// machinery and reports back via Trade.
type Trade struct {
	Chain         string    `json:"chain"`
	DEX           string    `json:"dex"`
	TokenPair     string    `json:"token_pair"`
	AmountIn      float64   `json:"amount_in"`
	AmountOut     float64   `json:"amount_out"`
	FeePaid       float64   `json:"fee_paid"`
	PnL           float64   `json:"pnl"`
	TxSignature   string    `json:"tx_signature"`
	IsPaper       bool      `json:"is_paper"`
	ExecutedAt    time.Time `json:"executed_at"`
}

// LendingPosition describes a borrow / collateral position on a money market
// (Kamino on Solana, Aave / Compound on Base) — shape-compatible with what
// the LiquidationHunting task (chunk 26) expects to see.
type LendingPosition struct {
	Chain          string  `json:"chain"`
	Protocol       string  `json:"protocol"`
	Owner          string  `json:"owner"`
	CollateralAsset string `json:"collateral_asset"`
	CollateralAmt  float64 `json:"collateral_amount"`
	DebtAsset      string  `json:"debt_asset"`
	DebtAmt        float64 `json:"debt_amount"`
	HealthFactor   float64 `json:"health_factor"`
	LiquidationBonus float64 `json:"liquidation_bonus_bps"`
}

// YieldRate is one row of the cross-chain yield table the
// CrossChainYield task (chunk 11) consumes when picking where to park
// idle capital.
type YieldRate struct {
	Chain    string  `json:"chain"`
	Protocol string  `json:"protocol"`
	Asset    string  `json:"asset"`
	APY      float64 `json:"apy"`
	TVL      float64 `json:"tvl_usd"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TxResult is what `SendTransaction`, `ExecuteSwap`, and
// `ExecuteLiquidation` return: enough to log, recover, or refund.
type TxResult struct {
	Signature   string  `json:"signature"`
	Success     bool    `json:"success"`
	GasUsed     uint64  `json:"gas_used"`
	GasPriceWei uint64  `json:"gas_price_wei,omitempty"`
	FeePaidUSD  float64 `json:"fee_paid_usd"`
	BlockNumber uint64  `json:"block_number,omitempty"`
	ErrorMsg    string  `json:"error,omitempty"`
}

// SimResult is what `SimulateTransaction` returns. WouldSucceed is the
// authoritative bit; ExpectedOutput is informational (e.g. expected token
// balances, gas use).
type SimResult struct {
	WouldSucceed   bool    `json:"would_succeed"`
	ExpectedOutput float64 `json:"expected_output"`
	GasEstimate    uint64  `json:"gas_estimate"`
	ErrorMsg       string  `json:"error,omitempty"`
}

// Wallet wraps an encrypted private key. The plaintext key never leaves the
// security package (chunk 17 implements decryption). Address is the public
// chain identity (Ed25519 pubkey for Solana, secp256k1 address for Base).
type Wallet struct {
	Chain         string `json:"chain"`
	Address       string `json:"address"`
	KeyEncrypted  []byte `json:"-"`
}

// Transaction is the chain-agnostic envelope around a serialized
// transaction. Raw is the bytes the implementation will sign + broadcast;
// chain-specific clients know how to interpret it.
type Transaction struct {
	Chain string            `json:"chain"`
	Raw   []byte            `json:"-"`
	Meta  map[string]string `json:"meta,omitempty"`
}
