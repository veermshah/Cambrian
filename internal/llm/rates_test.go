package llm

import (
	"math"
	"testing"
)

func TestRatesKnownInputKnownCost(t *testing.T) {
	cases := []struct {
		model  string
		in     int
		out    int
		want   float64
	}{
		// 1M in + 1M out, haiku-4-5: $1 + $5 = $6.
		{"claude-haiku-4-5-20251001", 1_000_000, 1_000_000, 6.00},
		// 100k in + 50k out, sonnet-4-6: 0.3 + 0.75 = $1.05.
		{"claude-sonnet-4-6", 100_000, 50_000, 1.05},
		// 10k in + 5k out, opus-4-7: 0.15 + 0.375 = $0.525.
		{"claude-opus-4-7", 10_000, 5_000, 0.525},
		// 1M in + 1M out, gpt-4o: 2.50 + 10.00 = $12.50.
		{"gpt-4o", 1_000_000, 1_000_000, 12.50},
		// 1M in + 1M out, gpt-4o-mini: 0.15 + 0.60 = $0.75.
		{"gpt-4o-mini", 1_000_000, 1_000_000, 0.75},
	}
	for _, tc := range cases {
		got := computeCost(tc.model, tc.in, tc.out)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("computeCost(%s, %d, %d) = %v, want %v", tc.model, tc.in, tc.out, got, tc.want)
		}
	}
}

func TestRatesUnknownModelReturnsZero(t *testing.T) {
	got := computeCost("claude-not-a-model", 1000, 1000)
	if got != 0 {
		t.Errorf("computeCost on unknown model = %v, want 0", got)
	}
}

func TestRateForRoundtrip(t *testing.T) {
	r, ok := RateFor("gpt-4o-mini")
	if !ok {
		t.Fatal("RateFor(gpt-4o-mini) not found")
	}
	if r.InputPerToken != 0.15/1_000_000 {
		t.Errorf("InputPerToken = %v", r.InputPerToken)
	}
	if r.OutputPerToken != 0.60/1_000_000 {
		t.Errorf("OutputPerToken = %v", r.OutputPerToken)
	}
}

func TestRatesSnapshotDateIsSet(t *testing.T) {
	if RatesSnapshotDate == "" {
		t.Error("RatesSnapshotDate must be set")
	}
}

func TestEveryRegisteredModelHasNonZeroCost(t *testing.T) {
	for model, rate := range modelRates {
		cost := computeCost(model, 1000, 1000)
		if cost <= 0 {
			t.Errorf("%s: cost on 1k+1k = %v, want > 0", model, cost)
		}
		if rate.InputPerToken <= 0 || rate.OutputPerToken <= 0 {
			t.Errorf("%s: rate row has non-positive entry: %+v", model, rate)
		}
	}
}
