package chain

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingClient wraps a FakeChainClient and counts every underlying
// GetQuote call. Used to assert that singleflight collapses concurrent
// reads down to a single RPC.
type countingClient struct {
	*FakeChainClient
	getQuoteCalls atomic.Int64
	// hold gates GetQuote: when set, the call blocks until released,
	// forcing waiters to queue inside singleflight.
	hold chan struct{}
}

func newCounting(price float64) *countingClient {
	c := &countingClient{
		FakeChainClient: NewFake("solana", "SOL").WithQuote("USDC", "SOL", &Quote{
			Chain: "solana", DEX: "orca",
			TokenIn: "USDC", TokenOut: "SOL",
			AmountIn: 1, AmountOut: price, Price: price,
		}),
	}
	return c
}

func (c *countingClient) GetQuote(ctx context.Context, tokenIn, tokenOut string, amount float64) (*Quote, error) {
	c.getQuoteCalls.Add(1)
	if c.hold != nil {
		<-c.hold
	}
	return c.FakeChainClient.GetQuote(ctx, tokenIn, tokenOut, amount)
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestPriceCache_CacheHitWithinTTL(t *testing.T) {
	cc := newCounting(100)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0).UTC()}
	cache := NewPriceCache(map[string]ChainClient{"solana": cc}, WithClock(clk.Now))

	for i := 0; i < 5; i++ {
		p, err := cache.Get(context.Background(), "solana", "USDC/SOL")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if p != 100 {
			t.Errorf("price = %v, want 100", p)
		}
	}
	if got := cc.getQuoteCalls.Load(); got != 1 {
		t.Errorf("expected 1 underlying GetQuote, got %d", got)
	}
	hits, misses, fetches := cache.Stats()
	if fetches != 1 {
		t.Errorf("fetches=%d, want 1", fetches)
	}
	if hits != 4 {
		t.Errorf("hits=%d, want 4", hits)
	}
	if misses != 1 {
		t.Errorf("misses=%d, want 1", misses)
	}
}

func TestPriceCache_RefetchesAfterTTL(t *testing.T) {
	cc := newCounting(100)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0).UTC()}
	cache := NewPriceCache(map[string]ChainClient{"solana": cc}, WithClock(clk.Now), WithTTL(30*time.Second))

	if _, err := cache.Get(context.Background(), "solana", "USDC/SOL"); err != nil {
		t.Fatalf("Get1: %v", err)
	}
	clk.Advance(31 * time.Second)
	if _, err := cache.Get(context.Background(), "solana", "USDC/SOL"); err != nil {
		t.Fatalf("Get2: %v", err)
	}
	if got := cc.getQuoteCalls.Load(); got != 2 {
		t.Errorf("expected 2 underlying GetQuotes after TTL expiry, got %d", got)
	}
}

func TestPriceCache_SingleflightCollapsesConcurrentReads(t *testing.T) {
	cc := newCounting(100)
	cc.hold = make(chan struct{})
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0).UTC()}
	cache := NewPriceCache(map[string]ChainClient{"solana": cc}, WithClock(clk.Now))

	var wg sync.WaitGroup
	const N = 100
	results := make([]float64, N)
	errs := make([]error, N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			p, err := cache.Get(context.Background(), "solana", "USDC/SOL")
			results[i] = p
			errs[i] = err
		}(i)
	}
	close(start)
	// Give every goroutine a chance to reach the singleflight gate.
	time.Sleep(50 * time.Millisecond)
	close(cc.hold) // release the held GetQuote
	wg.Wait()

	if got := cc.getQuoteCalls.Load(); got != 1 {
		t.Errorf("expected 1 underlying GetQuote for 100 concurrent reads, got %d", got)
	}
	for i, p := range results {
		if errs[i] != nil {
			t.Errorf("goroutine %d: %v", i, errs[i])
		}
		if p != 100 {
			t.Errorf("goroutine %d: price=%v want 100", i, p)
		}
	}
	_, _, fetches := cache.Stats()
	if fetches != 1 {
		t.Errorf("Stats.fetches=%d want 1", fetches)
	}
}

func TestPriceCache_GetReturnsUnderlyingError(t *testing.T) {
	cc := newCounting(100)
	// Replace the staged quote with one that will not be found.
	cache := NewPriceCache(map[string]ChainClient{"solana": cc})
	_, err := cache.Get(context.Background(), "solana", "WBTC/SOL")
	if err == nil {
		t.Error("expected error for unstaged pair")
	}
}

func TestPriceCache_UnknownChain(t *testing.T) {
	cache := NewPriceCache(nil)
	_, err := cache.Get(context.Background(), "ethereum", "USDC/SOL")
	if err == nil {
		t.Error("expected error for unknown chain")
	}
}

func TestPriceCache_PinAndUnpin(t *testing.T) {
	cache := NewPriceCache(nil)
	cache.Pin("solana", "USDC/SOL")
	cache.Pin("base", "USDC/ETH")
	if got := len(cache.Pinned()); got != 2 {
		t.Errorf("Pinned size after Pin x2 = %d, want 2", got)
	}
	cache.Unpin("solana", "USDC/SOL")
	if got := len(cache.Pinned()); got != 1 {
		t.Errorf("Pinned size after Unpin = %d, want 1", got)
	}
}

func TestPriceCache_BackgroundRefreshHitsClient(t *testing.T) {
	cc := newCounting(100)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0).UTC()}
	cache := NewPriceCache(
		map[string]ChainClient{"solana": cc},
		WithClock(clk.Now),
		WithRefreshInterval(20*time.Millisecond),
	)
	cache.Pin("solana", "USDC/SOL")

	// Seed the cache so we can observe the refresh replacing the entry.
	if _, err := cache.Get(context.Background(), "solana", "USDC/SOL"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	startCalls := cc.getQuoteCalls.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache.Start(ctx)
	t.Cleanup(cache.Stop)

	// Wait up to 500 ms for at least one background refresh tick.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cc.getQuoteCalls.Load() > startCalls {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("background refresh did not fire: calls stayed at %d", cc.getQuoteCalls.Load())
}

func TestPriceCache_StopIsIdempotent(t *testing.T) {
	cache := NewPriceCache(nil, WithRefreshInterval(time.Hour))
	cache.Start(context.Background())
	cache.Stop()
	cache.Stop() // second call must not panic / block
}

func TestSplitPair(t *testing.T) {
	cases := []struct {
		in, a, b string
		wantErr  bool
	}{
		{"USDC/SOL", "USDC", "SOL", false},
		{"usdc-sol", "USDC", "SOL", false},
		{"USDC:DAI", "USDC", "DAI", false},
		{"USDC", "", "", true},
		{"/SOL", "", "", true},
		{"SOL/", "", "", true},
	}
	for _, c := range cases {
		a, b, err := splitPair(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("splitPair(%q) expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitPair(%q): %v", c.in, err)
			continue
		}
		if a != c.a || b != c.b {
			t.Errorf("splitPair(%q) = (%q,%q), want (%q,%q)", c.in, a, b, c.a, c.b)
		}
	}
}

func TestPriceCache_InvalidateForcesFetch(t *testing.T) {
	cc := newCounting(100)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0).UTC()}
	cache := NewPriceCache(map[string]ChainClient{"solana": cc}, WithClock(clk.Now))

	if _, err := cache.Get(context.Background(), "solana", "USDC/SOL"); err != nil {
		t.Fatalf("Get1: %v", err)
	}
	cache.Invalidate("solana", "USDC/SOL")
	if _, err := cache.Get(context.Background(), "solana", "USDC/SOL"); err != nil {
		t.Fatalf("Get2: %v", err)
	}
	if got := cc.getQuoteCalls.Load(); got != 2 {
		t.Errorf("Invalidate should force refetch: got %d calls, want 2", got)
	}
}

// errorClient is a minimal ChainClient that always errors from
// GetQuote. Used to confirm singleflight propagates errors to every
// waiter.
type errorClient struct {
	*FakeChainClient
	err error
}

func (e *errorClient) GetQuote(_ context.Context, _, _ string, _ float64) (*Quote, error) {
	return nil, e.err
}

func TestPriceCache_PropagatesErrorToAllWaiters(t *testing.T) {
	ec := &errorClient{FakeChainClient: NewFake("solana", "SOL"), err: errors.New("rpc unreachable")}
	cache := NewPriceCache(map[string]ChainClient{"solana": ec})

	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := cache.Get(context.Background(), "solana", "USDC/SOL")
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e == nil {
			t.Errorf("goroutine %d: expected error, got nil", i)
		}
	}
}

// Spot-check that the cache survives concurrent reads across many
// different keys — singleflight only dedupes per-key, so distinct keys
// should each fetch exactly once.
func TestPriceCache_DistinctKeysIndependent(t *testing.T) {
	fake := NewFake("solana", "SOL").
		WithQuote("USDC", "SOL", &Quote{Chain: "solana", AmountIn: 1, AmountOut: 100, Price: 100}).
		WithQuote("USDC", "BTC", &Quote{Chain: "solana", AmountIn: 1, AmountOut: 50000, Price: 50000})
	cc := &countingClient{FakeChainClient: fake}
	cache := NewPriceCache(map[string]ChainClient{"solana": cc})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = cache.Get(context.Background(), "solana", "USDC/SOL")
		}()
		go func() {
			defer wg.Done()
			_, _ = cache.Get(context.Background(), "solana", "USDC/BTC")
		}()
	}
	wg.Wait()
	got := cc.getQuoteCalls.Load()
	// Each key may hit once (best case) or potentially twice if the
	// flights happen at different moments. We assert ≤ 2 per key to
	// account for the race between cache miss and singleflight join.
	if got > 4 {
		t.Errorf("expected ≤ 4 underlying calls for 2 distinct keys, got %d", got)
	}
	if got < 2 {
		t.Errorf("expected ≥ 2 underlying calls (one per key), got %d", got)
	}
}

// Sanity-check the cache key shape so collisions across chains can't happen.
func TestCacheKey(t *testing.T) {
	a := cacheKey("solana", "USDC/SOL")
	b := cacheKey("base", "USDC/SOL")
	if a == b {
		t.Errorf("cacheKey should distinguish chains: %q == %q", a, b)
	}
	if cacheKey("solana", "usdc/sol") != cacheKey("solana", "USDC/SOL") {
		t.Errorf("cacheKey should be case-insensitive on the pair")
	}
}

func TestSplitCacheKey(t *testing.T) {
	tests := []struct {
		in      string
		c, p    string
		wantOk  bool
	}{
		{"solana|USDC/SOL", "solana", "USDC/SOL", true},
		{"base|USDC-ETH", "base", "USDC-ETH", true},
		{"solana|", "", "", false},
		{"|USDC/SOL", "", "", false},
		{"nopipe", "", "", false},
	}
	for _, tc := range tests {
		c, p, ok := splitCacheKey(tc.in)
		if ok != tc.wantOk {
			t.Errorf("splitCacheKey(%q) ok=%v want %v", tc.in, ok, tc.wantOk)
		}
		if c != tc.c || p != tc.p {
			t.Errorf("splitCacheKey(%q) = (%q,%q,%v), want (%q,%q,%v)", tc.in, c, p, ok, tc.c, tc.p, tc.wantOk)
		}
	}
}

// Make sure the staged FakeChainClient still behaves identically when
// wrapped — guards against accidental method-set drift on
// countingClient if FakeChainClient ever grows methods.
func TestCountingClientStillFake(t *testing.T) {
	cc := newCounting(42)
	q, err := cc.GetQuote(context.Background(), "USDC", "SOL", 5)
	if err != nil {
		t.Fatalf("GetQuote: %v", err)
	}
	if q.Price != 42 {
		t.Errorf("price = %v, want 42", q.Price)
	}
	// FakeChainClient scales AmountOut by amount/AmountIn — 5/1 * 42 = 210
	if q.AmountOut != 210 {
		t.Errorf("AmountOut = %v, want 210", q.AmountOut)
	}
	_ = fmt.Sprintf("force fmt import for negative-space coverage if needed: %v", cc.getQuoteCalls.Load())
}
