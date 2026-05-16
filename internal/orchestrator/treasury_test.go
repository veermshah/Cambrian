package orchestrator

import (
	"context"
	"errors"
	"math"
	"testing"
)

type fakeSweepStore struct {
	rows []Sweep
	err  error
}

func (f *fakeSweepStore) InsertSweep(_ context.Context, s Sweep) error {
	if f.err != nil {
		return f.err
	}
	f.rows = append(f.rows, s)
	return nil
}

func TestComputeSweep_Table(t *testing.T) {
	cases := []struct {
		name string
		in   SweepInputs

		wantSkip   bool
		wantReason string
		wantParent float64
		wantRoot   float64
		wantRetain float64
	}{
		{
			name: "solvent healthy with parent (30/20/50)",
			in: SweepInputs{
				AgentID:               "a",
				ParentAgentID:         "p",
				NetProfit:             100,
				LineageReserveBalance: 100,
				LineageReserveFloor:   50,
				ParentSplitPct:        0.30,
				RootSplitPct:          0.20,
			},
			wantParent: 30, wantRoot: 20, wantRetain: 50,
		},
		{
			name: "solvent healthy no parent (parent share → root)",
			in: SweepInputs{
				AgentID:               "a",
				NetProfit:             100,
				LineageReserveBalance: 100,
				LineageReserveFloor:   50,
				ParentSplitPct:        0.30,
				RootSplitPct:          0.20,
			},
			wantParent: 0, wantRoot: 50, wantRetain: 50,
		},
		{
			name: "insolvent — no sweep",
			in: SweepInputs{
				AgentID: "a", ParentAgentID: "p",
				NetProfit:      -10,
				ParentSplitPct: 0.30, RootSplitPct: 0.20,
			},
			wantSkip: true, wantReason: "insolvent",
		},
		{
			name: "zero profit — no sweep",
			in: SweepInputs{
				AgentID: "a", NetProfit: 0,
				ParentSplitPct: 0.30, RootSplitPct: 0.20,
			},
			wantSkip: true, wantReason: "insolvent",
		},
		{
			name: "carrying debt — no sweep",
			in: SweepInputs{
				AgentID: "a", ParentAgentID: "p",
				NetProfit:             50,
				CarriedDebt:           10,
				LineageReserveBalance: 100,
				LineageReserveFloor:   50,
				ParentSplitPct:        0.30, RootSplitPct: 0.20,
			},
			wantSkip: true, wantReason: "carrying_debt",
		},
		{
			name: "below reserve floor — no sweep",
			in: SweepInputs{
				AgentID: "a", NetProfit: 50,
				LineageReserveBalance: 10, LineageReserveFloor: 50,
				ParentSplitPct: 0.30, RootSplitPct: 0.20,
			},
			wantSkip: true, wantReason: "below_reserve",
		},
		{
			name: "splits sum to 1 ⇒ retain 0",
			in: SweepInputs{
				AgentID: "a", ParentAgentID: "p",
				NetProfit:             100,
				LineageReserveBalance: 100,
				LineageReserveFloor:   0,
				ParentSplitPct:        0.5, RootSplitPct: 0.5,
			},
			wantParent: 50, wantRoot: 50, wantRetain: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := ComputeSweep(c.in)
			if err != nil {
				t.Fatalf("ComputeSweep: %v", err)
			}
			if d.Skipped != c.wantSkip {
				t.Fatalf("skipped = %v, want %v (reason=%q)", d.Skipped, c.wantSkip, d.Reason)
			}
			if c.wantSkip {
				if d.Reason != c.wantReason {
					t.Errorf("reason = %q, want %q", d.Reason, c.wantReason)
				}
				return
			}
			if math.Abs(d.Sweep.AmountToParent-c.wantParent) > 1e-9 {
				t.Errorf("parent = %v, want %v", d.Sweep.AmountToParent, c.wantParent)
			}
			if math.Abs(d.Sweep.AmountToRoot-c.wantRoot) > 1e-9 {
				t.Errorf("root = %v, want %v", d.Sweep.AmountToRoot, c.wantRoot)
			}
			if math.Abs(d.Sweep.AmountRetained-c.wantRetain) > 1e-9 {
				t.Errorf("retain = %v, want %v", d.Sweep.AmountRetained, c.wantRetain)
			}
		})
	}
}

func TestComputeSweep_RejectsInvalidSplits(t *testing.T) {
	cases := []SweepInputs{
		{AgentID: "a", NetProfit: 1, ParentSplitPct: -0.1, RootSplitPct: 0.2},
		{AgentID: "a", NetProfit: 1, ParentSplitPct: 1.1, RootSplitPct: 0.2},
		{AgentID: "a", NetProfit: 1, ParentSplitPct: 0.5, RootSplitPct: 0.6},
		{AgentID: "", NetProfit: 1, ParentSplitPct: 0.3, RootSplitPct: 0.2},
	}
	for i, c := range cases {
		if _, err := ComputeSweep(c); err == nil {
			t.Errorf("case %d: expected error, got nil", i)
		}
	}
}

func TestPersistSweep_HappyPath(t *testing.T) {
	store := &fakeSweepStore{}
	d := SweepDecision{
		Sweep: Sweep{AgentID: "a", AmountToRoot: 50, AmountRetained: 50},
	}
	if err := PersistSweep(context.Background(), store, d); err != nil {
		t.Fatalf("PersistSweep: %v", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(store.rows))
	}
}

func TestPersistSweep_SkippedIsNoop(t *testing.T) {
	store := &fakeSweepStore{}
	d := SweepDecision{Skipped: true, Reason: "insolvent"}
	if err := PersistSweep(context.Background(), store, d); err != nil {
		t.Fatalf("PersistSweep: %v", err)
	}
	if len(store.rows) != 0 {
		t.Errorf("rows = %d, want 0 (skipped should not insert)", len(store.rows))
	}
}

func TestPersistSweep_StoreErrorBubbles(t *testing.T) {
	store := &fakeSweepStore{err: errors.New("boom")}
	d := SweepDecision{Sweep: Sweep{AgentID: "a", AmountToRoot: 1}}
	if err := PersistSweep(context.Background(), store, d); err == nil {
		t.Error("expected error to bubble")
	}
}
