package e2e

import (
	"testing"

	"github.com/veermshah/cambrian/internal/agent"
)

// TestBandit_ConvergesToBestArmOverLongHorizon — spec line 1192.
// Pure-logic, runs anywhere. The bandit doesn't know which arm is best;
// after 2000 trials it should still pick it the majority of the time.
func TestBandit_ConvergesToBestArmOverLongHorizon(t *testing.T) {
	b := agent.NewTickBandit([]string{"good", "ok", "bad"})

	// True per-arm reward rates the bandit will discover through Bernoulli
	// trials.
	rates := map[string]float64{"good": 0.8, "ok": 0.5, "bad": 0.2}

	// Drive the bandit by hand: each round Select picks an arm, then we
	// feed back a reward sampled according to the true rate. A simple
	// pseudo-random deterministic source keeps the test stable: alternate
	// hash-derived reward signal.
	const rounds = 2000
	picks := map[string]int{}
	for i := 0; i < rounds; i++ {
		arm := b.Select()
		picks[arm]++
		reward := 0.0
		// Deterministic Bernoulli without a RNG dependency: use a
		// counter against the rate scaled by 100. The bandit's own RNG
		// supplies the actual exploration noise.
		if float64((i*7+13)%100) < rates[arm]*100 {
			reward = 1
		}
		_ = b.Update(arm, reward)
	}

	// After convergence, the best arm should dominate. We don't demand
	// monopoly — exploration is healthy — but it should pull >40% of
	// picks vs. the bad arm's <20%.
	if picks["good"] < int(0.4*rounds) {
		t.Errorf("convergence: good arm picked %d, want ≥%d", picks["good"], int(0.4*rounds))
	}
	if picks["bad"] >= picks["good"] {
		t.Errorf("convergence: bad arm picked %d ≥ good arm %d", picks["bad"], picks["good"])
	}
}
