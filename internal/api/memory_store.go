package api

import (
	"context"
	"sort"
	"sync"
)

// MemoryStore is the in-process Store used by handler tests and by the
// dashboard when bring-up tests want a deterministic dataset. It's also
// what the early integration tests use — testcontainers Postgres is
// gated by Docker, which isn't available in every CI lane.
type MemoryStore struct {
	mu sync.RWMutex

	Agents       []AgentSummary
	AgentDetails map[string]AgentDetail
	Trades       []TradeRow
	Epochs       []EpochRow
	Lineage      []LineageNode
	Treasury     TreasuryState
	Postmortems  []PostmortemRow
	Offspring    []OffspringRow
	Budget       BudgetState
	Breaker      CircuitBreakerState
	Backtests    []BacktestRow
	Intel        []IntelRow
	Models       []ModelPerformance
	Evolution    []EvolutionEvent
	Snapshot     DashboardSnapshot
}

// NewMemoryStore returns an empty MemoryStore. Tests typically populate
// the public fields before passing it to NewServer.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{AgentDetails: map[string]AgentDetail{}}
}

func (s *MemoryStore) ListAgents(_ context.Context, opts ListAgentsOpts) ([]AgentSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AgentSummary, 0, len(s.Agents))
	for _, a := range s.Agents {
		if opts.Chain != "" && a.Chain != opts.Chain {
			continue
		}
		if opts.NodeClass != "" && a.NodeClass != opts.NodeClass {
			continue
		}
		if opts.Status != "" && a.Status != opts.Status {
			continue
		}
		if opts.TaskType != "" && a.TaskType != opts.TaskType {
			continue
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TotalPnLUSD > out[j].TotalPnLUSD })
	return out, nil
}

func (s *MemoryStore) GetAgent(_ context.Context, id string) (AgentDetail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.AgentDetails[id]
	if !ok {
		return AgentDetail{}, ErrNotFound
	}
	return d, nil
}

func (s *MemoryStore) ListTrades(_ context.Context, opts ListTradesOpts) ([]TradeRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	out := make([]TradeRow, 0, limit)
	for _, t := range s.Trades {
		if opts.AgentID != "" && t.AgentID != opts.AgentID {
			continue
		}
		if opts.Chain != "" && t.Chain != opts.Chain {
			continue
		}
		out = append(out, t)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *MemoryStore) ListEpochs(_ context.Context, limit int) ([]EpochRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.Epochs) {
		limit = len(s.Epochs)
	}
	out := make([]EpochRow, limit)
	copy(out, s.Epochs[:limit])
	return out, nil
}

func (s *MemoryStore) GetLineage(_ context.Context) ([]LineageNode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]LineageNode, len(s.Lineage))
	copy(out, s.Lineage)
	return out, nil
}

func (s *MemoryStore) GetTreasury(_ context.Context) (TreasuryState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Treasury, nil
}

func (s *MemoryStore) ListPostmortems(_ context.Context, limit int) ([]PostmortemRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.Postmortems) {
		limit = len(s.Postmortems)
	}
	out := make([]PostmortemRow, limit)
	copy(out, s.Postmortems[:limit])
	return out, nil
}

func (s *MemoryStore) ListOffspring(_ context.Context, status string) ([]OffspringRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]OffspringRow, 0, len(s.Offspring))
	for _, o := range s.Offspring {
		if status != "" && o.Status != status {
			continue
		}
		out = append(out, o)
	}
	return out, nil
}

func (s *MemoryStore) GetBudget(_ context.Context) (BudgetState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Budget, nil
}

func (s *MemoryStore) GetCircuitBreaker(_ context.Context) (CircuitBreakerState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Breaker, nil
}

func (s *MemoryStore) ListBacktests(_ context.Context, limit int) ([]BacktestRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.Backtests) {
		limit = len(s.Backtests)
	}
	out := make([]BacktestRow, limit)
	copy(out, s.Backtests[:limit])
	return out, nil
}

func (s *MemoryStore) ListIntel(_ context.Context, opts ListIntelOpts) ([]IntelRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	out := make([]IntelRow, 0, limit)
	for _, r := range s.Intel {
		if opts.Channel != "" && r.Channel != opts.Channel {
			continue
		}
		if opts.Sentiment != "" && r.Sentiment != opts.Sentiment {
			continue
		}
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *MemoryStore) ListModels(_ context.Context) ([]ModelPerformance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ModelPerformance, len(s.Models))
	copy(out, s.Models)
	sort.SliceStable(out, func(i, j int) bool { return out[i].PnLPerDollar > out[j].PnLPerDollar })
	return out, nil
}

func (s *MemoryStore) ListEvolution(_ context.Context, limit int) ([]EvolutionEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.Evolution) {
		limit = len(s.Evolution)
	}
	out := make([]EvolutionEvent, limit)
	copy(out, s.Evolution[:limit])
	return out, nil
}

func (s *MemoryStore) GetDashboardSnapshot(_ context.Context) (DashboardSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Snapshot, nil
}
