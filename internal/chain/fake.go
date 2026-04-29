package chain

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// FakeChainClient is the in-memory ChainClient used by tests across the
// repo. It satisfies every method on the interface and exposes a small
// builder API so a test can stage exactly the balances, quotes, yields,
// and positions it needs.
//
// All reads are concurrency-safe; tests that mutate state via the With*
// helpers should do so before handing the client off to goroutines.
type FakeChainClient struct {
	mu sync.RWMutex

	chainName    string
	nativeToken  string
	dryRunOnly   bool

	balances      map[string]float64            // address -> native balance
	tokenBalances map[string]map[string]float64 // address -> tokenAddr -> balance
	quotes        map[string]*Quote             // tokenIn|tokenOut -> quote
	yieldRates    map[string]YieldRate          // protocol -> rate
	positions     map[string][]LendingPosition  // protocol -> positions

	simResult *SimResult

	// Recorded calls — useful for assertions.
	Swaps         []*Quote
	Sent          []*Transaction
	Liquidations  []*LendingPosition
}

// Compile-time conformance: every method on ChainClient is implemented.
var _ ChainClient = (*FakeChainClient)(nil)

// NewFake returns a FakeChainClient with empty maps and the given chain
// identity. Pass "solana"/"SOL" or "base"/"ETH" for parity with the real
// clients; tests can use any string.
func NewFake(chainName, nativeToken string) *FakeChainClient {
	return &FakeChainClient{
		chainName:     chainName,
		nativeToken:   nativeToken,
		balances:      map[string]float64{},
		tokenBalances: map[string]map[string]float64{},
		quotes:        map[string]*Quote{},
		yieldRates:    map[string]YieldRate{},
		positions:     map[string][]LendingPosition{},
	}
}

// WithBalance stages the native-token balance for the given address.
func (f *FakeChainClient) WithBalance(address string, amount float64) *FakeChainClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.balances[address] = amount
	return f
}

// WithTokenBalance stages an SPL / ERC-20 balance for an address.
func (f *FakeChainClient) WithTokenBalance(address, tokenAddr string, amount float64) *FakeChainClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokenBalances[address] == nil {
		f.tokenBalances[address] = map[string]float64{}
	}
	f.tokenBalances[address][tokenAddr] = amount
	return f
}

// WithQuote stages the Quote returned for a (tokenIn, tokenOut) pair.
// The amount on the stored quote is overwritten by the request amount at
// GetQuote time so callers can probe with arbitrary inputs.
func (f *FakeChainClient) WithQuote(tokenIn, tokenOut string, q *Quote) *FakeChainClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.quotes[quoteKey(tokenIn, tokenOut)] = q
	return f
}

// WithYieldRate stages a YieldRate keyed by protocol.
func (f *FakeChainClient) WithYieldRate(protocol string, r YieldRate) *FakeChainClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.yieldRates[protocol] = r
	return f
}

// WithLendingPosition appends a position to the protocol's slice.
func (f *FakeChainClient) WithLendingPosition(protocol string, p LendingPosition) *FakeChainClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.positions[protocol] = append(f.positions[protocol], p)
	return f
}

// WithSimResult stages the SimResult returned by SimulateTransaction.
func (f *FakeChainClient) WithSimResult(s *SimResult) *FakeChainClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.simResult = s
	return f
}

// WithDryRunOnly forces ExecuteSwap / SendTransaction / ExecuteLiquidation
// to refuse non-dry-run executions, matching how strategist tasks paper-
// trade in early epochs.
func (f *FakeChainClient) WithDryRunOnly(dry bool) *FakeChainClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dryRunOnly = dry
	return f
}

// dryRunKey is the context.Value key the swarm uses to flag paper trades.
// Defined here so tests can opt in without importing a higher-level
// package; real clients honour the same key.
type dryRunKey struct{}

// WithDryRun returns a context that signals to ExecuteSwap /
// SendTransaction / ExecuteLiquidation that the call is a paper trade.
func WithDryRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, dryRunKey{}, true)
}

// IsDryRun reports whether the context carries the dry-run flag.
func IsDryRun(ctx context.Context) bool {
	v, _ := ctx.Value(dryRunKey{}).(bool)
	return v
}

func quoteKey(tokenIn, tokenOut string) string {
	return tokenIn + "|" + tokenOut
}

// --- ChainClient methods ----------------------------------------------------

func (f *FakeChainClient) GetBalance(_ context.Context, address string) (float64, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	bal, ok := f.balances[address]
	if !ok {
		return 0, fmt.Errorf("fake: no balance staged for %q", address)
	}
	return bal, nil
}

func (f *FakeChainClient) GetTokenBalance(_ context.Context, address, tokenAddr string) (float64, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	byToken, ok := f.tokenBalances[address]
	if !ok {
		return 0, fmt.Errorf("fake: no token balances staged for %q", address)
	}
	bal, ok := byToken[tokenAddr]
	if !ok {
		return 0, fmt.Errorf("fake: no balance staged for token %q on %q", tokenAddr, address)
	}
	return bal, nil
}

func (f *FakeChainClient) GetQuote(_ context.Context, tokenIn, tokenOut string, amount float64) (*Quote, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	q, ok := f.quotes[quoteKey(tokenIn, tokenOut)]
	if !ok {
		return nil, fmt.Errorf("fake: no quote staged for %s -> %s", tokenIn, tokenOut)
	}
	out := *q
	out.AmountIn = amount
	if q.AmountIn > 0 {
		out.AmountOut = q.AmountOut * (amount / q.AmountIn)
	}
	if out.GeneratedAt.IsZero() {
		out.GeneratedAt = time.Now().UTC()
	}
	return &out, nil
}

func (f *FakeChainClient) ExecuteSwap(ctx context.Context, quote *Quote, wallet *Wallet) (*TxResult, error) {
	if quote == nil {
		return nil, errors.New("fake: ExecuteSwap nil quote")
	}
	if wallet == nil {
		return nil, errors.New("fake: ExecuteSwap nil wallet")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dryRunOnly && !IsDryRun(ctx) {
		return nil, errors.New("fake: dry-run only mode, real execution refused")
	}
	f.Swaps = append(f.Swaps, quote)
	return &TxResult{
		Signature: fmt.Sprintf("fake-swap-%d", len(f.Swaps)),
		Success:   true,
	}, nil
}

func (f *FakeChainClient) SimulateTransaction(_ context.Context, tx *Transaction) (*SimResult, error) {
	if tx == nil {
		return nil, errors.New("fake: SimulateTransaction nil tx")
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.simResult != nil {
		out := *f.simResult
		return &out, nil
	}
	return &SimResult{WouldSucceed: true}, nil
}

func (f *FakeChainClient) SendTransaction(ctx context.Context, tx *Transaction, wallet *Wallet) (*TxResult, error) {
	if tx == nil {
		return nil, errors.New("fake: SendTransaction nil tx")
	}
	if wallet == nil {
		return nil, errors.New("fake: SendTransaction nil wallet")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dryRunOnly && !IsDryRun(ctx) {
		return nil, errors.New("fake: dry-run only mode, real execution refused")
	}
	f.Sent = append(f.Sent, tx)
	return &TxResult{
		Signature: fmt.Sprintf("fake-tx-%d", len(f.Sent)),
		Success:   true,
	}, nil
}

func (f *FakeChainClient) GetLendingPositions(_ context.Context, protocol string) ([]LendingPosition, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]LendingPosition, len(f.positions[protocol]))
	copy(out, f.positions[protocol])
	return out, nil
}

func (f *FakeChainClient) GetYieldRates(_ context.Context, protocols []string) ([]YieldRate, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]YieldRate, 0, len(protocols))
	for _, p := range protocols {
		if r, ok := f.yieldRates[p]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *FakeChainClient) ExecuteLiquidation(ctx context.Context, pos *LendingPosition, wallet *Wallet) (*TxResult, error) {
	if pos == nil {
		return nil, errors.New("fake: ExecuteLiquidation nil position")
	}
	if wallet == nil {
		return nil, errors.New("fake: ExecuteLiquidation nil wallet")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dryRunOnly && !IsDryRun(ctx) {
		return nil, errors.New("fake: dry-run only mode, real execution refused")
	}
	f.Liquidations = append(f.Liquidations, pos)
	return &TxResult{
		Signature:  fmt.Sprintf("fake-liq-%d", len(f.Liquidations)),
		Success:    true,
		FeePaidUSD: pos.CollateralAmt * pos.LiquidationBonus / 10000.0,
	}, nil
}

func (f *FakeChainClient) ChainName() string   { return f.chainName }
func (f *FakeChainClient) NativeToken() string { return f.nativeToken }
