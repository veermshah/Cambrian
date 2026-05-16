package orchestrator

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

type fakeEconStore struct {
	start, end time.Time
	agg        TradeAggregates
	llmCost    float64
	debt       float64
	insertErr  error
	inserted   []Ledger

	windowErr error
	aggErr    error
	llmErr    error
	debtErr   error
}

func (f *fakeEconStore) EpochWindow(_ context.Context, _ string) (time.Time, time.Time, error) {
	return f.start, f.end, f.windowErr
}
func (f *fakeEconStore) AggregateTrades(_ context.Context, _, _ string) (TradeAggregates, error) {
	return f.agg, f.aggErr
}
func (f *fakeEconStore) AggregateStrategistCost(_ context.Context, _ string, _, _ time.Time) (float64, error) {
	return f.llmCost, f.llmErr
}
func (f *fakeEconStore) CarriedDebt(_ context.Context, _ string) (float64, error) {
	return f.debt, f.debtErr
}
func (f *fakeEconStore) InsertLedger(_ context.Context, l Ledger) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inserted = append(f.inserted, l)
	return nil
}

type fakeCosts struct {
	infra, rpc float64
	err        error
}

func (f fakeCosts) Attribute(_ context.Context, _ string, _, _ time.Time) (float64, float64, error) {
	return f.infra, f.rpc, f.err
}

func newSettleHarness() (*fakeEconStore, fakeCosts) {
	return &fakeEconStore{
		start: time.Unix(1_700_000_000, 0).UTC(),
		end:   time.Unix(1_700_021_600, 0).UTC(), // 6h later
	}, fakeCosts{}
}

func TestSettle_FormulaTable(t *testing.T) {
	cases := []struct {
		name                                            string
		pnl, fees, slip, llm, infra, rpc, debt, wantNet float64
	}{
		{"all positive", 100, 5, 3, 2, 1, 1, 0, 88},
		{"all losses", -50, 5, 3, 2, 1, 1, 0, -62},
		{"upstream debt eats profit", 100, 0, 0, 0, 0, 0, 80, 20},
		{"zero everything", 0, 0, 0, 0, 0, 0, 0, 0},
		{"every component zeroed except pnl", 25.5, 0, 0, 0, 0, 0, 0, 25.5},
		{"every component nonzero, combined", 1000, 12, 8, 6, 4, 2, 100, 868},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, _ := newSettleHarness()
			store.agg = TradeAggregates{PnL: c.pnl, Fees: c.fees, Slippage: c.slip}
			store.llmCost = c.llm
			store.debt = c.debt
			costs := fakeCosts{infra: c.infra, rpc: c.rpc}

			ledger, err := Settle(context.Background(), store, costs, "a", "e")
			if err != nil {
				t.Fatalf("Settle: %v", err)
			}
			if math.Abs(ledger.RealizedNetProfit-c.wantNet) > 1e-9 {
				t.Errorf("net = %v, want %v", ledger.RealizedNetProfit, c.wantNet)
			}
			if len(store.inserted) != 1 {
				t.Fatalf("ledger not persisted")
			}
		})
	}
}

func TestSettle_RequiresArgs(t *testing.T) {
	store, costs := newSettleHarness()
	if _, err := Settle(context.Background(), store, costs, "", "e"); err == nil {
		t.Error("expected error for empty agent_id")
	}
	if _, err := Settle(context.Background(), store, costs, "a", ""); err == nil {
		t.Error("expected error for empty epoch_id")
	}
	if _, err := Settle(context.Background(), nil, costs, "a", "e"); err == nil {
		t.Error("expected error for nil store")
	}
	if _, err := Settle(context.Background(), store, nil, "a", "e"); err == nil {
		t.Error("expected error for nil costs")
	}
}

func TestSettle_BackwardWindowRejected(t *testing.T) {
	store, costs := newSettleHarness()
	store.start, store.end = store.end, store.start
	if _, err := Settle(context.Background(), store, costs, "a", "e"); err == nil {
		t.Error("expected error for non-positive window")
	}
}

func TestSettle_StoreErrorBubbles(t *testing.T) {
	cases := []struct {
		name string
		set  func(*fakeEconStore)
	}{
		{"window err", func(s *fakeEconStore) { s.windowErr = errors.New("x") }},
		{"agg err", func(s *fakeEconStore) { s.aggErr = errors.New("x") }},
		{"llm err", func(s *fakeEconStore) { s.llmErr = errors.New("x") }},
		{"debt err", func(s *fakeEconStore) { s.debtErr = errors.New("x") }},
		{"insert err", func(s *fakeEconStore) { s.insertErr = errors.New("x") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, costs := newSettleHarness()
			c.set(store)
			if _, err := Settle(context.Background(), store, costs, "a", "e"); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestSettle_CostAttributorErrorBubbles(t *testing.T) {
	store, _ := newSettleHarness()
	costs := fakeCosts{err: errors.New("cost db down")}
	if _, err := Settle(context.Background(), store, costs, "a", "e"); err == nil {
		t.Fatal("expected error")
	}
}
