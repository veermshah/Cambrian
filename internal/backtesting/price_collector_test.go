package backtesting

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/chain"
)

// stubQuoter is a minimal ChainClient that only implements GetQuote
// (the only method PriceCache calls). All other methods return zero
// values — they're unreachable through PriceCache.
type stubQuoter struct {
	chainName, native string
	price             atomic.Uint64
}

func newStubQuoter(name, native string, initial float64) *stubQuoter {
	s := &stubQuoter{chainName: name, native: native}
	s.SetPrice(initial)
	return s
}

func (s *stubQuoter) SetPrice(p float64) {
	bits := float64ToBits(p)
	s.price.Store(bits)
}

func (s *stubQuoter) ChainName() string   { return s.chainName }
func (s *stubQuoter) NativeToken() string { return s.native }
func (s *stubQuoter) GetQuote(_ context.Context, in, out string, amount float64) (*chain.Quote, error) {
	p := bitsToFloat64(s.price.Load())
	return &chain.Quote{TokenIn: in, TokenOut: out, Price: p, AmountIn: amount, AmountOut: amount * p}, nil
}

// Unused interface methods — return zeros.
func (s *stubQuoter) GetBalance(context.Context, string) (float64, error) { return 0, nil }
func (s *stubQuoter) GetTokenBalance(context.Context, string, string) (float64, error) {
	return 0, nil
}
func (s *stubQuoter) ExecuteSwap(context.Context, *chain.Quote, *chain.Wallet) (*chain.TxResult, error) {
	return nil, nil
}
func (s *stubQuoter) SimulateTransaction(context.Context, *chain.Transaction) (*chain.SimResult, error) {
	return nil, nil
}
func (s *stubQuoter) SendTransaction(context.Context, *chain.Transaction, *chain.Wallet) (*chain.TxResult, error) {
	return nil, nil
}
func (s *stubQuoter) GetLendingPositions(context.Context, string) ([]chain.LendingPosition, error) {
	return nil, nil
}
func (s *stubQuoter) GetYieldRates(context.Context, []string) ([]chain.YieldRate, error) {
	return nil, nil
}
func (s *stubQuoter) ExecuteLiquidation(context.Context, *chain.LendingPosition, *chain.Wallet) (*chain.TxResult, error) {
	return nil, nil
}

var _ chain.ChainClient = (*stubQuoter)(nil)

func float64ToBits(f float64) uint64 { return math.Float64bits(f) }
func bitsToFloat64(b uint64) float64 { return math.Float64frombits(b) }

func TestPriceCollector_RequiresAllDeps(t *testing.T) {
	if _, err := NewPriceCollector(PriceCollectorConfig{}); err == nil {
		t.Error("expected error for missing deps")
	}
}

func TestPriceCollector_CollectOnceWritesOneRowPerActivePair(t *testing.T) {
	ctx := context.Background()
	cache := chain.NewPriceCache(map[string]chain.ChainClient{
		"solana": newStubQuoter("solana", "SOL", 25.0),
		"base":   newStubQuoter("base", "ETH", 3000.0),
	})
	store := NewInMemoryPriceStore()
	pairs := StaticPairSource{{"solana", "USDC/SOL"}, {"base", "USDC/ETH"}}
	at := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	c, err := NewPriceCollector(PriceCollectorConfig{
		Cache: cache, Pairs: pairs, Store: store,
		Now: func() time.Time { return at },
	})
	if err != nil {
		t.Fatal(err)
	}
	written, errs := c.CollectOnce(ctx)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if written != 2 {
		t.Errorf("written = %d, want 2", written)
	}
	rows := store.Rows()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if !r.RecordedAt.Equal(at) {
			t.Errorf("recorded_at = %v, want %v", r.RecordedAt, at)
		}
	}
}

func TestPriceCollector_PartialFailureDoesNotStopOthers(t *testing.T) {
	ctx := context.Background()
	cache := chain.NewPriceCache(map[string]chain.ChainClient{
		"solana": newStubQuoter("solana", "SOL", 25.0),
	})
	store := NewInMemoryPriceStore()
	pairs := StaticPairSource{{"solana", "USDC/SOL"}, {"base", "USDC/ETH"}}
	c, err := NewPriceCollector(PriceCollectorConfig{Cache: cache, Pairs: pairs, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	written, errs := c.CollectOnce(ctx)
	if written != 1 {
		t.Errorf("written = %d, want 1 (the good pair)", written)
	}
	if len(errs) != 1 {
		t.Errorf("errors = %d, want 1 (the missing-chain pair)", len(errs))
	}
}

func TestPriceCollector_OneRowPerMinutePerPair(t *testing.T) {
	// Spec acceptance criterion: 1 row per minute per pair (verify w/ timestamps).
	cache := chain.NewPriceCache(map[string]chain.ChainClient{"solana": newStubQuoter("solana", "SOL", 25.0)})
	store := NewInMemoryPriceStore()
	pairs := StaticPairSource{{"solana", "USDC/SOL"}}
	tick := atomic.Int64{}
	start := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	now := func() time.Time {
		return start.Add(time.Duration(tick.Load()) * time.Minute)
	}
	c, err := NewPriceCollector(PriceCollectorConfig{
		Cache: cache, Pairs: pairs, Store: store,
		Interval: 50 * time.Millisecond, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		tick.Store(int64(i))
		cache.Invalidate("solana", "USDC/SOL")
		c.CollectOnce(context.Background())
	}
	rows := store.RowsFor("solana", "USDC/SOL")
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	seenMinutes := map[int]bool{}
	for _, r := range rows {
		seenMinutes[r.RecordedAt.Minute()] = true
	}
	for m := 0; m < 3; m++ {
		if !seenMinutes[m] {
			t.Errorf("missing minute %d in collected timestamps: %v", m, seenMinutes)
		}
	}
}

type errStubStore struct{ err error }

func (e errStubStore) InsertPriceRow(_ context.Context, _ PriceRow) error { return e.err }

func TestPriceCollector_StoreErrorsAccumulate(t *testing.T) {
	cache := chain.NewPriceCache(map[string]chain.ChainClient{"solana": newStubQuoter("solana", "SOL", 25.0)})
	c, err := NewPriceCollector(PriceCollectorConfig{
		Cache: cache,
		Pairs: StaticPairSource{{"solana", "USDC/SOL"}},
		Store: errStubStore{err: errors.New("db down")},
	})
	if err != nil {
		t.Fatal(err)
	}
	written, errs := c.CollectOnce(context.Background())
	if written != 0 {
		t.Errorf("written = %d, want 0", written)
	}
	if len(errs) != 1 {
		t.Errorf("errors = %d, want 1", len(errs))
	}
}
