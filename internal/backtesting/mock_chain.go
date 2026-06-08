package backtesting

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/veermshah/cambrian/internal/chain"
)

// MockChainConfig parameterises the replay client.
type MockChainConfig struct {
	// ChainName is "solana" or "base" — surfaced via ChainClient.ChainName.
	ChainName string
	// NativeToken is "SOL" or "ETH" — surfaced via ChainClient.NativeToken.
	NativeToken string
	// SlippageBps adds a per-swap slippage cost in basis points: output
	// is reduced by SlippageBps/10000. Spec line 1235 explicitly calls
	// out a slippage simulator.
	SlippageBps int
	// FeeBps is the DEX fee in basis points. The mock charges this on
	// AmountIn and reports it as FeePaid / FeePaidUSD.
	FeeBps int
	// InitialBalance is the native-token balance the wallet starts
	// with. The mock decrements this on swap costs (in native units,
	// approximated as fee_amount / price).
	InitialBalance float64
}

// MockChainClient is an in-memory ChainClient that replays a slice of
// PriceRow as if they were live quotes. It serves the BacktestEngine
// and any tests that need a deterministic ChainClient.
//
// The mock keeps a single virtual wallet balance (native token), plus
// per-token ERC-20-style balances mutated by ExecuteSwap. Liquidation
// & lending position queries return empty slices — the four core tasks
// either don't need them or already have richer fakes in
// internal/chain/fake.go.
type MockChainClient struct {
	cfg MockChainConfig

	mu sync.RWMutex
	// pricesByPair: "USDC/SOL" → ascending-by-time list of observations.
	pricesByPair map[string][]PriceRow
	// cursor tracks the simulated "now" — used to pick the most recent
	// row at-or-before the cursor for a deterministic replay.
	cursor time.Time

	balances     map[string]float64 // address → native balance
	tokenBals    map[string]float64 // "address|token" → balance
	trades       []chain.Trade
	txSequence   int
}

// NewMockChainClient builds a fresh replay client populated from rows.
// Rows may be unsorted; the mock sorts them per-pair on construction
// so price-at-time lookups are O(log n).
func NewMockChainClient(cfg MockChainConfig, rows []PriceRow) *MockChainClient {
	if cfg.ChainName == "" {
		cfg.ChainName = "mock"
	}
	if cfg.NativeToken == "" {
		cfg.NativeToken = "MOCK"
	}
	m := &MockChainClient{
		cfg:          cfg,
		pricesByPair: map[string][]PriceRow{},
		balances:     map[string]float64{},
		tokenBals:    map[string]float64{},
	}
	for _, r := range rows {
		k := strings.ToUpper(r.TokenPair)
		m.pricesByPair[k] = append(m.pricesByPair[k], r)
	}
	for k := range m.pricesByPair {
		rs := m.pricesByPair[k]
		sort.Slice(rs, func(i, j int) bool { return rs[i].RecordedAt.Before(rs[j].RecordedAt) })
		m.pricesByPair[k] = rs
	}
	return m
}

// SetCursor advances the replay clock. The engine calls this between
// ticks so GetQuote returns the price as of the simulated moment.
// Going backwards is allowed (and tested) so a single mock can drive
// multiple backtest sweeps.
func (m *MockChainClient) SetCursor(t time.Time) {
	m.mu.Lock()
	m.cursor = t
	m.mu.Unlock()
}

// Cursor returns the current simulated time.
func (m *MockChainClient) Cursor() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cursor
}

// Trades returns a snapshot of every executed swap in arrival order.
// Used by the engine and tests to compute metrics.
func (m *MockChainClient) Trades() []chain.Trade {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]chain.Trade, len(m.trades))
	copy(out, m.trades)
	return out
}

// ---- ChainClient implementation -----------------------------------------

func (m *MockChainClient) ChainName() string   { return m.cfg.ChainName }
func (m *MockChainClient) NativeToken() string { return m.cfg.NativeToken }

func (m *MockChainClient) GetBalance(_ context.Context, address string) (float64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.balances[address]; ok {
		return v, nil
	}
	return m.cfg.InitialBalance, nil
}

func (m *MockChainClient) GetTokenBalance(_ context.Context, address, tokenAddr string) (float64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tokenBals[address+"|"+strings.ToUpper(tokenAddr)], nil
}

// GetQuote looks up the most recent observation at-or-before the
// cursor for the requested pair and returns a Quote with slippage and
// fees applied to AmountOut.
func (m *MockChainClient) GetQuote(_ context.Context, tokenIn, tokenOut string, amount float64) (*chain.Quote, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("mock_chain: amount must be positive, got %v", amount)
	}
	pair := strings.ToUpper(tokenIn + "/" + tokenOut)
	price, at, err := m.priceAtCursor(pair)
	if err != nil {
		return nil, err
	}
	gross := amount * price
	fee := gross * float64(m.cfg.FeeBps) / 10000
	netAfterFee := gross - fee
	slip := netAfterFee * float64(m.cfg.SlippageBps) / 10000
	out := netAfterFee - slip
	return &chain.Quote{
		Chain:       m.cfg.ChainName,
		DEX:         "mock",
		TokenIn:     strings.ToUpper(tokenIn),
		TokenOut:    strings.ToUpper(tokenOut),
		AmountIn:    amount,
		AmountOut:   out,
		Price:       price,
		PriceImpact: float64(m.cfg.SlippageBps) / 10000,
		FeeAmount:   fee,
		SlippageBps: m.cfg.SlippageBps,
		GeneratedAt: at,
	}, nil
}

// ExecuteSwap "fills" the supplied quote, updating the wallet's
// token balance and recording the trade. The replay treats every swap
// as instantly successful (slippage already baked in by GetQuote) and
// charges a constant FeePaidUSD = fee_amount.
func (m *MockChainClient) ExecuteSwap(_ context.Context, quote *chain.Quote, wallet *chain.Wallet) (*chain.TxResult, error) {
	if quote == nil {
		return nil, errors.New("mock_chain: nil quote")
	}
	if wallet == nil {
		return nil, errors.New("mock_chain: nil wallet")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	addr := wallet.Address
	inKey := addr + "|" + quote.TokenIn
	outKey := addr + "|" + quote.TokenOut
	// Allow going short — backtest semantics, no liquidity checks here.
	m.tokenBals[inKey] -= quote.AmountIn
	m.tokenBals[outKey] += quote.AmountOut
	m.txSequence++
	sig := fmt.Sprintf("mock-tx-%06d", m.txSequence)
	t := chain.Trade{
		Chain:       quote.Chain,
		DEX:         quote.DEX,
		TokenPair:   quote.TokenIn + "/" + quote.TokenOut,
		AmountIn:    quote.AmountIn,
		AmountOut:   quote.AmountOut,
		FeePaid:     quote.FeeAmount,
		PnL:         0,
		TxSignature: sig,
		IsPaper:     true,
		ExecutedAt:  m.cursor,
	}
	m.trades = append(m.trades, t)
	return &chain.TxResult{
		Signature:  sig,
		Success:    true,
		FeePaidUSD: quote.FeeAmount,
	}, nil
}

func (m *MockChainClient) SimulateTransaction(_ context.Context, _ *chain.Transaction) (*chain.SimResult, error) {
	return &chain.SimResult{WouldSucceed: true}, nil
}

func (m *MockChainClient) SendTransaction(_ context.Context, _ *chain.Transaction, _ *chain.Wallet) (*chain.TxResult, error) {
	m.mu.Lock()
	m.txSequence++
	sig := fmt.Sprintf("mock-tx-%06d", m.txSequence)
	m.mu.Unlock()
	return &chain.TxResult{Signature: sig, Success: true}, nil
}

func (m *MockChainClient) GetLendingPositions(_ context.Context, _ string) ([]chain.LendingPosition, error) {
	return nil, nil
}

func (m *MockChainClient) GetYieldRates(_ context.Context, _ []string) ([]chain.YieldRate, error) {
	return nil, nil
}

func (m *MockChainClient) ExecuteLiquidation(_ context.Context, _ *chain.LendingPosition, _ *chain.Wallet) (*chain.TxResult, error) {
	return &chain.TxResult{Success: false, ErrorMsg: "mock_chain: ExecuteLiquidation not supported in backtest"}, nil
}

// priceAtCursor returns the most recent observation at-or-before the
// cursor for the pair. Falls back to the *earliest* row when the
// cursor sits before the first observation — the engine often calls
// GetQuote during its initial warm-up tick before SetCursor has been
// called.
func (m *MockChainClient) priceAtCursor(pair string) (float64, time.Time, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rows, ok := m.pricesByPair[pair]
	if !ok || len(rows) == 0 {
		return 0, time.Time{}, fmt.Errorf("mock_chain: no price history for pair %q", pair)
	}
	if m.cursor.IsZero() {
		r := rows[0]
		return r.Price, r.RecordedAt, nil
	}
	// Binary search for the last row at-or-before cursor.
	lo, hi := 0, len(rows)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if rows[mid].RecordedAt.After(m.cursor) {
			hi = mid - 1
		} else {
			lo = mid
		}
	}
	if rows[lo].RecordedAt.After(m.cursor) {
		// Cursor predates everything — use the earliest sample.
		r := rows[0]
		return r.Price, r.RecordedAt, nil
	}
	return rows[lo].Price, rows[lo].RecordedAt, nil
}

// Compile-time assertion the mock satisfies the chain.ChainClient
// contract. Future additions to the interface will fail the build here
// before reaching the engine.
var _ chain.ChainClient = (*MockChainClient)(nil)
