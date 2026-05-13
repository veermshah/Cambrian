package tasks

import (
	"context"
	"encoding/json"
)

// FakeTask is a deterministic Task implementation for tests. Used by
// the NodeRunner tests (chunk 14) and any test that needs a Task
// satisfying the interface without touching real chain clients.
//
// Fields are exported so tests can configure return values directly:
//
//	t := &FakeTask{
//	    TickTrades: []Trade{{Chain: "solana", TradeType: "swap", ...}},
//	    Summary:    map[string]any{"positions": 2},
//	    Position:   1234.56,
//	}
type FakeTask struct {
	TickTrades        []Trade
	TickErr           error
	Summary           map[string]interface{}
	SummaryErr        error
	AdjustErr         error
	LastAdjustments   map[string]interface{}
	Position          float64
	PositionErr       error
	CloseTrades       []Trade
	CloseErr          error
	RunTickCallCount  int
	GetSummaryCount   int
	CloseAllCallCount int
}

var _ Task = (*FakeTask)(nil)

// RunTick records the call and returns the preconfigured trades/err.
func (f *FakeTask) RunTick(_ context.Context) ([]Trade, error) {
	f.RunTickCallCount++
	return f.TickTrades, f.TickErr
}

func (f *FakeTask) GetStateSummary(_ context.Context) (map[string]interface{}, error) {
	f.GetSummaryCount++
	if f.Summary == nil {
		return map[string]interface{}{}, f.SummaryErr
	}
	return f.Summary, f.SummaryErr
}

func (f *FakeTask) ApplyAdjustments(adj map[string]interface{}) error {
	f.LastAdjustments = adj
	return f.AdjustErr
}

func (f *FakeTask) GetPositionValue(_ context.Context) (float64, error) {
	return f.Position, f.PositionErr
}

func (f *FakeTask) CloseAllPositions(_ context.Context) ([]Trade, error) {
	f.CloseAllCallCount++
	return f.CloseTrades, f.CloseErr
}

// FakeFactory returns a TaskFactory that yields a FakeTask. The seed
// FakeTask's fields are copied so callers can register the same factory
// many times without sharing state.
func FakeFactory(seed FakeTask) TaskFactory {
	return func(_ context.Context, _ json.RawMessage) (Task, error) {
		copy := seed
		return &copy, nil
	}
}
