package orchestrator

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

type fakeActivity struct {
	agent ActivityCounts
	swarm ActivityCounts
	err   error
}

func (f fakeActivity) AgentActivity(_ context.Context, _ string, _, _ time.Time) (ActivityCounts, error) {
	return f.agent, f.err
}
func (f fakeActivity) SwarmActivity(_ context.Context, _, _ time.Time) (ActivityCounts, error) {
	return f.swarm, f.err
}

func TestMonthlyAttributor_Validation(t *testing.T) {
	store := fakeActivity{}
	cases := []MonthlyCostInputs{
		{MonthHours: 0, MonthlyInfraUSD: 10},
		{MonthHours: -1, MonthlyInfraUSD: 10},
		{MonthHours: 720, MonthlyInfraUSD: -1},
		{MonthHours: 720, MonthlyRPCUSD: -1},
	}
	for i, c := range cases {
		if _, err := NewMonthlyAttributor(store, c); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
	if _, err := NewMonthlyAttributor(nil, MonthlyCostInputs{MonthHours: 720}); err == nil {
		t.Error("expected error for nil store")
	}
}

func TestMonthlyAttributor_Attribute(t *testing.T) {
	start := time.Unix(1_700_000_000, 0).UTC()
	end := start.Add(6 * time.Hour) // 1/120 of a 720h month

	cases := []struct {
		name        string
		agent       ActivityCounts
		swarm       ActivityCounts
		monthlyI    float64
		monthlyR    float64
		wantInfra   float64
		wantRPC     float64
		expectErr   bool
		emptyWindow bool
	}{
		{
			name:      "half the swarm activity ⇒ half the window cost",
			agent:     ActivityCounts{StrategistCalls: 5, MonitorTicks: 5},
			swarm:     ActivityCounts{StrategistCalls: 10, MonitorTicks: 10},
			monthlyI:  720, // 1$/hour
			monthlyR:  360, // 0.5$/hour
			wantInfra: 6 * 1.0 * 0.5,
			wantRPC:   6 * 0.5 * 0.5,
		},
		{
			name:      "zero swarm activity ⇒ zero charge",
			agent:     ActivityCounts{},
			swarm:     ActivityCounts{},
			monthlyI:  720,
			monthlyR:  360,
			wantInfra: 0,
			wantRPC:   0,
		},
		{
			name:      "agent silent ⇒ zero charge",
			agent:     ActivityCounts{},
			swarm:     ActivityCounts{StrategistCalls: 100, MonitorTicks: 100},
			monthlyI:  720,
			monthlyR:  360,
			wantInfra: 0,
			wantRPC:   0,
		},
		{
			name:        "non-positive window ⇒ zero charge",
			emptyWindow: true,
			monthlyI:    720,
			monthlyR:    360,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := fakeActivity{agent: c.agent, swarm: c.swarm}
			m, err := NewMonthlyAttributor(store, MonthlyCostInputs{
				MonthlyInfraUSD: c.monthlyI,
				MonthlyRPCUSD:   c.monthlyR,
				MonthHours:      720,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			s, e := start, end
			if c.emptyWindow {
				s, e = end, start
			}
			infra, rpc, err := m.Attribute(context.Background(), "agent-1", s, e)
			if err != nil {
				t.Fatalf("Attribute: %v", err)
			}
			if math.Abs(infra-c.wantInfra) > 1e-9 {
				t.Errorf("infra = %v, want %v", infra, c.wantInfra)
			}
			if math.Abs(rpc-c.wantRPC) > 1e-9 {
				t.Errorf("rpc = %v, want %v", rpc, c.wantRPC)
			}
		})
	}
}

func TestMonthlyAttributor_StoreErrorBubbles(t *testing.T) {
	store := fakeActivity{err: errors.New("boom")}
	m, err := NewMonthlyAttributor(store, MonthlyCostInputs{
		MonthlyInfraUSD: 100, MonthHours: 720,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	start := time.Unix(1_700_000_000, 0).UTC()
	if _, _, err := m.Attribute(context.Background(), "a", start, start.Add(time.Hour)); err == nil {
		t.Error("expected error to bubble")
	}
}

func TestMonthlyAttributor_RejectsEmptyAgentID(t *testing.T) {
	store := fakeActivity{}
	m, _ := NewMonthlyAttributor(store, MonthlyCostInputs{MonthHours: 720})
	if _, _, err := m.Attribute(context.Background(), "", time.Now(), time.Now().Add(time.Hour)); err == nil {
		t.Error("expected error")
	}
}
