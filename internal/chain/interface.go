package chain

import "context"

// ChainClient is the abstraction every strategy task consumes when it needs
// to read on-chain state or move funds. It is implemented by SolanaClient
// (chunk 4, solana-go + Jupiter) and BaseClient (chunk 5, go-ethereum +
// 1inch / Paraswap), and faked by FakeChainClient (this chunk) for tests.
//
// Method set matches the spec verbatim — see
// evolutionary-swarm-project-description.md lines 346-360.
type ChainClient interface {
	// GetBalance returns the native token balance (SOL on Solana, ETH on
	// Base) for the given address, in human-readable units (e.g. SOL, ETH —
	// not lamports / wei).
	GetBalance(ctx context.Context, address string) (float64, error)

	// GetTokenBalance returns the SPL / ERC-20 token balance for the given
	// owner address, in human-readable units.
	GetTokenBalance(ctx context.Context, address string, tokenAddr string) (float64, error)

	// GetQuote asks the chain's preferred DEX aggregator for an executable
	// quote: how much tokenOut a given amount of tokenIn would buy right
	// now. The returned Quote can later be passed to ExecuteSwap.
	GetQuote(ctx context.Context, tokenIn, tokenOut string, amount float64) (*Quote, error)

	// ExecuteSwap submits the swap described by quote, signed by wallet.
	// Returns a TxResult. Implementations must support a "dry run" mode
	// (e.g. via context value) so the strategist task can paper-trade.
	ExecuteSwap(ctx context.Context, quote *Quote, wallet *Wallet) (*TxResult, error)

	// SimulateTransaction runs the given transaction without broadcasting
	// and reports whether it would succeed. Used by the offspring proposal
	// pipeline (chunk 19) and trade pre-flight checks.
	SimulateTransaction(ctx context.Context, tx *Transaction) (*SimResult, error)

	// SendTransaction signs and broadcasts an arbitrary transaction (used
	// for non-swap actions like opening a lending position or claiming
	// rewards). Returns a TxResult.
	SendTransaction(ctx context.Context, tx *Transaction, wallet *Wallet) (*TxResult, error)

	// GetLendingPositions returns currently observable positions on the
	// given lending protocol. Used by the LiquidationHunting task (chunk 26)
	// to find positions near liquidation.
	GetLendingPositions(ctx context.Context, protocol string) ([]LendingPosition, error)

	// GetYieldRates returns current yield rates for the named protocols.
	// Consumed by the CrossChainYield task (chunk 11) to pick the
	// highest-yield, lowest-risk park for idle capital.
	GetYieldRates(ctx context.Context, protocols []string) ([]YieldRate, error)

	// ExecuteLiquidation triggers a liquidation against the given position,
	// signed by wallet. Returns a TxResult including the seized collateral
	// recorded in FeePaidUSD / Meta.
	ExecuteLiquidation(ctx context.Context, pos *LendingPosition, wallet *Wallet) (*TxResult, error)

	// ChainName returns the canonical name ("solana" or "base"). Used by
	// the registry and intel bus channel routing.
	ChainName() string

	// NativeToken returns the native asset symbol ("SOL", "ETH").
	NativeToken() string
}
