package chain

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// PriceCache is a shared, in-process price oracle used by every task
// that needs spot prices (cross_chain_yield, liquidity_provision,
// momentum, …). Without this cache each task would independently hit
// Jupiter / 1inch on every RunTick — for N agents trading M pairs the
// load multiplies to N*M*tickrate. With this cache, concurrent reads
// of the same (chain, pair) collapse into a single in-flight RPC via
// `singleflight`, and results are served from memory for `ttl`
// (default 30 s) before another fetch fires.
//
// Spec line 1140 anchors this — every strategy uses the same cache. A
// 60 s background refresh goroutine pre-warms `Pin`ned pairs so the
// strategist sees fresh data without paying first-call latency.
type PriceCache struct {
	clients map[string]ChainClient

	mu      sync.RWMutex
	entries map[string]priceEntry
	pinned  map[string]struct{}

	sf              singleflight.Group
	ttl             time.Duration
	refreshInterval time.Duration

	now func() time.Time

	// Observability — exported via Stats(). Atomic so concurrent Get
	// calls don't contend on the cache mutex when bumping counters.
	hits, misses, fetches atomic.Int64

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

type priceEntry struct {
	price     float64
	fetchedAt time.Time
}

// PriceCacheOption configures a PriceCache at construction time.
type PriceCacheOption func(*PriceCache)

// WithTTL overrides the default 30 s freshness window. The strategist's
// reasoning runs on minute-scale; tighter TTLs cost RPC calls without
// changing decisions. Looser TTLs (>60 s) let the LP task's drift
// detection miss real moves.
func WithTTL(d time.Duration) PriceCacheOption {
	return func(c *PriceCache) { c.ttl = d }
}

// WithRefreshInterval overrides the default 60 s background refresh.
func WithRefreshInterval(d time.Duration) PriceCacheOption {
	return func(c *PriceCache) { c.refreshInterval = d }
}

// WithClock injects a clock for deterministic tests.
func WithClock(now func() time.Time) PriceCacheOption {
	return func(c *PriceCache) { c.now = now }
}

// NewPriceCache builds a cache backed by the given per-chain clients.
// The map keys are canonical chain names ("solana", "base") — the same
// keys CrossChainYieldDeps / LiquidityProvisionDeps use.
func NewPriceCache(clients map[string]ChainClient, opts ...PriceCacheOption) *PriceCache {
	if clients == nil {
		clients = map[string]ChainClient{}
	}
	c := &PriceCache{
		clients:         clients,
		entries:         map[string]priceEntry{},
		pinned:          map[string]struct{}{},
		ttl:             30 * time.Second,
		refreshInterval: 60 * time.Second,
		now:             time.Now,
		stopCh:          make(chan struct{}),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Get returns the price for (chain, pair). If the cached entry is
// within TTL, returns it without an RPC. Otherwise singleflights an
// underlying GetQuote call so concurrent waiters share the result.
//
// `pair` is "USDC/SOL" / "USDC-SOL" / "USDC:SOL" — the cache splits it
// before handing the two legs to `ChainClient.GetQuote`.
func (c *PriceCache) Get(ctx context.Context, chainName, pair string) (float64, error) {
	key := cacheKey(chainName, pair)

	c.mu.RLock()
	e, ok := c.entries[key]
	fresh := ok && c.now().Sub(e.fetchedAt) < c.ttl
	c.mu.RUnlock()

	if fresh {
		c.hits.Add(1)
		return e.price, nil
	}
	c.misses.Add(1)

	v, err, _ := c.sf.Do(key, func() (interface{}, error) {
		// Double-check inside the flight: another goroutine may have
		// populated the entry while we were queued.
		c.mu.RLock()
		if e, ok := c.entries[key]; ok && c.now().Sub(e.fetchedAt) < c.ttl {
			c.mu.RUnlock()
			return e.price, nil
		}
		c.mu.RUnlock()

		c.fetches.Add(1)
		price, err := c.fetchPrice(ctx, chainName, pair)
		if err != nil {
			return 0.0, err
		}
		c.mu.Lock()
		c.entries[key] = priceEntry{price: price, fetchedAt: c.now()}
		c.mu.Unlock()
		return price, nil
	})
	if err != nil {
		return 0, err
	}
	return v.(float64), nil
}

// Pin marks a pair for background refresh. The runtime (chunk 14)
// pins each active agent's configured pair on spawn and unpins on
// kill, so the cache always knows what to pre-warm.
func (c *PriceCache) Pin(chainName, pair string) {
	c.mu.Lock()
	c.pinned[cacheKey(chainName, pair)] = struct{}{}
	c.mu.Unlock()
}

// Unpin removes a pair from the background refresh set.
func (c *PriceCache) Unpin(chainName, pair string) {
	c.mu.Lock()
	delete(c.pinned, cacheKey(chainName, pair))
	c.mu.Unlock()
}

// Pinned returns the set of pinned pairs in deterministic order. Used
// by the background loop and tests.
func (c *PriceCache) Pinned() [][2]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([][2]string, 0, len(c.pinned))
	for k := range c.pinned {
		chain, pair, ok := splitCacheKey(k)
		if !ok {
			continue
		}
		out = append(out, [2]string{chain, pair})
	}
	return out
}

// Start launches the background pre-warm goroutine. Idempotent —
// subsequent calls are no-ops. Stop with Stop().
func (c *PriceCache) Start(ctx context.Context) {
	c.startOnce.Do(func() {
		c.wg.Add(1)
		go c.refreshLoop(ctx)
	})
}

// Stop halts the background refresh goroutine and waits for it to
// exit. Safe to call multiple times.
func (c *PriceCache) Stop() {
	c.stopOnce.Do(func() { close(c.stopCh) })
	c.wg.Wait()
}

// Stats returns hit / miss / fetch counters. Hits are cache reads that
// returned without an RPC; misses are cache reads that fell through to
// the fetch path; fetches are actual underlying GetQuote calls (always
// ≤ misses thanks to singleflight).
func (c *PriceCache) Stats() (hits, misses, fetches int64) {
	return c.hits.Load(), c.misses.Load(), c.fetches.Load()
}

// Invalidate forgets the cached price for (chain, pair). Used in tests
// and by the strategist when it suspects stale data after an unusual
// market event.
func (c *PriceCache) Invalidate(chainName, pair string) {
	c.mu.Lock()
	delete(c.entries, cacheKey(chainName, pair))
	c.mu.Unlock()
}

// --- internal --------------------------------------------------------------

func (c *PriceCache) fetchPrice(ctx context.Context, chainName, pair string) (float64, error) {
	client, ok := c.clients[chainName]
	if !ok {
		return 0, fmt.Errorf("price_cache: no client for chain %q", chainName)
	}
	tokenIn, tokenOut, err := splitPair(pair)
	if err != nil {
		return 0, err
	}
	q, err := client.GetQuote(ctx, tokenIn, tokenOut, 1.0)
	if err != nil {
		return 0, fmt.Errorf("price_cache: %s GetQuote(%s, %s): %w", chainName, tokenIn, tokenOut, err)
	}
	price := q.Price
	if price <= 0 && q.AmountIn > 0 {
		price = q.AmountOut / q.AmountIn
	}
	if price <= 0 {
		return 0, fmt.Errorf("price_cache: non-positive price for %s/%s on %s", tokenIn, tokenOut, chainName)
	}
	return price, nil
}

func (c *PriceCache) refreshLoop(ctx context.Context) {
	defer c.wg.Done()
	ticker := time.NewTicker(c.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.refreshPinned(ctx)
		}
	}
}

func (c *PriceCache) refreshPinned(ctx context.Context) {
	pairs := c.Pinned()
	for _, p := range pairs {
		// Invalidate first so Get falls through to the fetch path even
		// if the entry is still inside the TTL — the refresh exists
		// precisely so the next consumer reads warm data.
		c.Invalidate(p[0], p[1])
		_, _ = c.Get(ctx, p[0], p[1])
	}
}

// splitPair turns "USDC/SOL", "USDC-SOL", or "USDC:SOL" into the two
// legs. Mirrors the helper in package tasks so the price cache can be
// fed the same token_pair string the strategy_config carries.
func splitPair(pair string) (string, string, error) {
	for _, sep := range []string{"/", "-", ":"} {
		if i := strings.Index(pair, sep); i > 0 && i < len(pair)-1 {
			return strings.ToUpper(pair[:i]), strings.ToUpper(pair[i+1:]), nil
		}
	}
	return "", "", fmt.Errorf("price_cache: pair %q must look like TOKEN_A/TOKEN_B", pair)
}

func cacheKey(chainName, pair string) string {
	return chainName + "|" + strings.ToUpper(pair)
}

func splitCacheKey(k string) (string, string, bool) {
	i := strings.Index(k, "|")
	if i <= 0 || i >= len(k)-1 {
		return "", "", false
	}
	return k[:i], k[i+1:], true
}

