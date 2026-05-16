package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/veermshah/cambrian/internal/db"
	"github.com/veermshah/cambrian/internal/llm"
)

// fakeClock is a manual clock for runtime tests. Advance + tickers fire
// synchronously so cadence assertions don't sleep. Used by every test
// in this package — keeping it in a non-_test file would expose it to
// production callers, but the _test.go convention on this file keeps
// it test-only.

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeTicker
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	ch <- c.now.Add(d)
	c.mu.Unlock()
	return ch
}

func (c *fakeClock) NewTicker(d time.Duration) Ticker {
	t := &fakeTicker{
		c:        make(chan time.Time, 64),
		interval: d,
		clock:    c,
	}
	c.mu.Lock()
	t.next = c.now.Add(d)
	c.tickers = append(c.tickers, t)
	c.mu.Unlock()
	return t
}

// TickerCount returns the number of registered tickers (test plumbing
// to wait until all loops have started).
func (c *fakeClock) TickerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.tickers)
}

// Fire pushes one tick onto every active ticker, regardless of interval.
// Tests use this when they care about loop body behavior rather than
// the timing model.
func (c *fakeClock) Fire() {
	c.mu.Lock()
	tickers := append([]*fakeTicker(nil), c.tickers...)
	c.mu.Unlock()
	for _, t := range tickers {
		select {
		case t.c <- t.next:
		default:
		}
	}
}

// FireFor pushes one tick onto only the ticker whose interval equals d.
// Lets a test advance just the heartbeat loop, etc.
func (c *fakeClock) FireFor(d time.Duration) {
	c.mu.Lock()
	tickers := append([]*fakeTicker(nil), c.tickers...)
	c.mu.Unlock()
	for _, t := range tickers {
		if t.interval == d {
			select {
			case t.c <- t.next:
			default:
			}
		}
	}
}

type fakeTicker struct {
	c        chan time.Time
	interval time.Duration
	next     time.Time
	clock    *fakeClock
	stopped  bool
	mu       sync.Mutex
}

func (t *fakeTicker) C() <-chan time.Time { return t.c }
func (t *fakeTicker) Stop() {
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
}

// fakeStore is the in-memory NodeStore / LifecycleStore / SwarmStore
// for tests. Concurrent writes from the three NodeRunner loops require
// the mutex.

type fakeStore struct {
	mu sync.Mutex

	trades     []db.Trade
	decisions  []db.StrategistDecision
	heartbeats map[string]int

	insertErr error
	tradeErr  error
	decErr    error
	beatErr   error

	statusByID    map[string]string
	classByID     map[string]string
	reasonByID    map[string]string
	insertedRows  []db.Agent
	activeAgents  []db.Agent
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		heartbeats: map[string]int{},
		statusByID: map[string]string{},
		classByID:  map[string]string{},
		reasonByID: map[string]string{},
	}
}

func (s *fakeStore) LogTrade(_ context.Context, t db.Trade) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tradeErr != nil {
		return s.tradeErr
	}
	s.trades = append(s.trades, t)
	return nil
}

func (s *fakeStore) LogStrategistDecision(_ context.Context, d db.StrategistDecision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.decErr != nil {
		return s.decErr
	}
	s.decisions = append(s.decisions, d)
	return nil
}

func (s *fakeStore) UpdateHeartbeat(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.beatErr != nil {
		return s.beatErr
	}
	s.heartbeats[id]++
	return nil
}

func (s *fakeStore) InsertAgent(_ context.Context, a db.Agent) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.insertErr != nil {
		return "", s.insertErr
	}
	if a.ID == "" {
		a.ID = "fake-" + a.Name
	}
	s.insertedRows = append(s.insertedRows, a)
	s.statusByID[a.ID] = "active"
	s.classByID[a.ID] = a.NodeClass
	return a.ID, nil
}

func (s *fakeStore) SetAgentStatus(_ context.Context, agentID, status, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if agentID == "" {
		return errors.New("fake store: empty id")
	}
	s.statusByID[agentID] = status
	if reason != "" {
		s.reasonByID[agentID] = reason
	}
	return nil
}

func (s *fakeStore) SetAgentNodeClass(_ context.Context, agentID, class string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if agentID == "" {
		return errors.New("fake store: empty id")
	}
	s.classByID[agentID] = class
	return nil
}

func (s *fakeStore) ListActiveAgents(_ context.Context) ([]db.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.Agent, len(s.activeAgents))
	copy(out, s.activeAgents)
	return out, nil
}

func (s *fakeStore) status(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusByID[id]
}

func (s *fakeStore) class(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.classByID[id]
}

func (s *fakeStore) reason(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reasonByID[id]
}

func (s *fakeStore) heartbeatCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heartbeats[id]
}

func (s *fakeStore) tradeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.trades)
}

func (s *fakeStore) decisionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.decisions)
}

// fakeLLMRegistry resolves every model to the same FakeLLMClient. The
// real llm.Get reaches into package globals; this keeps tests hermetic.
type fakeLLMRegistry struct{ client llm.LLMClient }

func (f *fakeLLMRegistry) Get(_ string) (llm.LLMClient, error) {
	if f.client == nil {
		return nil, errors.New("fake llm registry: no client set")
	}
	return f.client, nil
}
