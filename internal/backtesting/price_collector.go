// Package backtesting holds the historical price collector (spec
// line 1180), the in-memory mock chain that replays price_history
// rows, and the BacktestEngine that runs an agent's Task against
// historical data using the mock chain (spec lines 491–493).
package backtesting

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/veermshah/cambrian/internal/chain"
)

// PriceRow is one persisted observation. Field names match the
// `price_history` schema (chunk 2 / 0001_init.up.sql) so a row can be
// passed straight to the writer without translation.
type PriceRow struct {
	Chain      string
	TokenPair  string
	Price      float64
	Volume24h  float64
	RecordedAt time.Time
}

// PriceStore is the persistence surface. Production wiring satisfies it
// with a pgx-backed inserter against `price_history`; tests use the
// in-memory implementation below.
type PriceStore interface {
	InsertPriceRow(ctx context.Context, row PriceRow) error
}

// PairSource enumerates the (chain, pair) tuples the collector should
// poll on each tick. The runtime registers active agents' configured
// pairs here and unregisters them on agent kill / migration — same
// pattern as PriceCache.Pin / Unpin (chunk 13).
type PairSource interface {
	ActivePairs() [][2]string // [][chain, pair]
}

// PriceCollectorConfig parameterises the collector.
type PriceCollectorConfig struct {
	// Cache is the shared price cache (chunk 13). Required.
	Cache *chain.PriceCache
	// Pairs enumerates active pairs each tick. Required.
	Pairs PairSource
	// Store persists each observation. Required.
	Store PriceStore
	// Interval is the polling cadence — spec line 1180 sets this at 60s.
	// Configurable to support faster ticks in tests.
	Interval time.Duration
	// Now is injected for deterministic timestamps in tests. Defaults
	// to time.Now.
	Now func() time.Time
	// Logger receives one structured line per tick. nil → discarded.
	Logger *slog.Logger
}

// PriceCollector polls the price cache on a fixed cadence and writes
// every (chain, pair) observation to price_history. The first poll
// fires on Run() rather than after the first tick so a fresh process
// has data on disk immediately.
type PriceCollector struct {
	cfg PriceCollectorConfig
}

// NewPriceCollector validates dependencies and returns a collector.
// All three required fields (Cache, Pairs, Store) must be non-nil.
func NewPriceCollector(cfg PriceCollectorConfig) (*PriceCollector, error) {
	if cfg.Cache == nil {
		return nil, errors.New("backtesting: PriceCollector requires a Cache")
	}
	if cfg.Pairs == nil {
		return nil, errors.New("backtesting: PriceCollector requires a PairSource")
	}
	if cfg.Store == nil {
		return nil, errors.New("backtesting: PriceCollector requires a PriceStore")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 60 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &PriceCollector{cfg: cfg}, nil
}

// CollectOnce runs one pass synchronously: every active pair is read
// from the cache and written to the store. Returns the number of rows
// written and any errors encountered (one per failing pair). A failure
// on one pair never short-circuits the rest of the pass.
func (c *PriceCollector) CollectOnce(ctx context.Context) (int, []error) {
	if c == nil {
		return 0, nil
	}
	pairs := c.cfg.Pairs.ActivePairs()
	var (
		errs    []error
		written int
	)
	for _, p := range pairs {
		chainName, pair := p[0], p[1]
		price, err := c.cfg.Cache.Get(ctx, chainName, pair)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		row := PriceRow{
			Chain:      chainName,
			TokenPair:  strings.ToUpper(pair),
			Price:      price,
			RecordedAt: c.cfg.Now(),
		}
		if err := c.cfg.Store.InsertPriceRow(ctx, row); err != nil {
			errs = append(errs, err)
			continue
		}
		written++
	}
	if c.cfg.Logger != nil {
		c.cfg.Logger.Info("price_collector.tick",
			"pairs", len(pairs),
			"written", written,
			"errors", len(errs),
		)
	}
	return written, errs
}

// Run blocks until ctx is cancelled, firing one CollectOnce immediately
// (so the first row lands within milliseconds of process start) and
// then once per Interval.
func (c *PriceCollector) Run(ctx context.Context) error {
	if c == nil {
		return errors.New("backtesting: nil collector")
	}
	c.CollectOnce(ctx)
	t := time.NewTicker(c.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			c.CollectOnce(ctx)
		}
	}
}

// InMemoryPriceStore is a tests-and-glue PriceStore. Production wiring
// replaces it with a pgx-backed inserter. Thread-safe.
type InMemoryPriceStore struct {
	mu   sync.RWMutex
	rows []PriceRow
}

func NewInMemoryPriceStore() *InMemoryPriceStore {
	return &InMemoryPriceStore{}
}

// InsertPriceRow appends a row.
func (s *InMemoryPriceStore) InsertPriceRow(_ context.Context, row PriceRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, row)
	return nil
}

// Rows returns a snapshot of every row written so far.
func (s *InMemoryPriceStore) Rows() []PriceRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PriceRow, len(s.rows))
	copy(out, s.rows)
	return out
}

// RowsFor returns rows matching (chain, pair) in arrival order.
func (s *InMemoryPriceStore) RowsFor(chainName, pair string) []PriceRow {
	want := strings.ToUpper(pair)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PriceRow, 0)
	for _, r := range s.rows {
		if r.Chain == chainName && r.TokenPair == want {
			out = append(out, r)
		}
	}
	return out
}

// StaticPairSource is the trivial PairSource for tests — returns the
// same pair list on every call. Production satisfies PairSource from
// the agent registry.
type StaticPairSource [][2]string

func (s StaticPairSource) ActivePairs() [][2]string {
	out := make([][2]string, len(s))
	copy(out, s)
	return out
}
