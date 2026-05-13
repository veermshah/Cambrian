package agent

import (
	"math/rand/v2"
	"testing"
)

// fixedRNG returns a deterministic *rand.Rand seeded for reproducible
// convergence and round-trip tests.
func fixedRNG(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed^0xa5a5a5a5))
}

func TestNewTickBanditInitializesUniformPrior(t *testing.T) {
	b := NewTickBandit([]string{"a", "b", "c"})
	for _, p := range []string{"a", "b", "c"} {
		if b.Alphas[p] != 1 || b.Betas[p] != 1 {
			t.Errorf("policy %q: alpha=%v beta=%v, want 1/1", p, b.Alphas[p], b.Betas[p])
		}
	}
	if got := b.Policies(); len(got) != 3 {
		t.Errorf("Policies = %v, want 3", got)
	}
}

func TestSelectEmptyBanditReturnsEmptyString(t *testing.T) {
	b := NewTickBandit(nil)
	if got := b.Select(); got != "" {
		t.Errorf("empty bandit Select = %q, want \"\"", got)
	}
}

func TestUpdateRejectsUnknownPolicy(t *testing.T) {
	b := NewTickBandit([]string{"a"})
	if err := b.Update("ghost", 1.0); err == nil {
		t.Error("Update on unknown policy: want error")
	}
}

func TestUpdateIncrementsAlphaOnPositiveReward(t *testing.T) {
	b := NewTickBandit([]string{"a"})
	if err := b.Update("a", 0.5); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if b.Alphas["a"] != 2 || b.Betas["a"] != 1 {
		t.Errorf("after positive reward: a=%v b=%v, want 2/1", b.Alphas["a"], b.Betas["a"])
	}
}

func TestUpdateIncrementsBetaOnNonPositiveReward(t *testing.T) {
	b := NewTickBandit([]string{"a"})
	if err := b.Update("a", 0); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := b.Update("a", -0.1); err != nil {
		t.Fatalf("Update neg: %v", err)
	}
	if b.Alphas["a"] != 1 || b.Betas["a"] != 3 {
		t.Errorf("after two losses: a=%v b=%v, want 1/3", b.Alphas["a"], b.Betas["a"])
	}
}

func TestAddPolicyIsIdempotent(t *testing.T) {
	b := NewTickBandit([]string{"a"})
	_ = b.Update("a", 1.0) // a should be at (2, 1)
	b.AddPolicy("a")
	if b.Alphas["a"] != 2 || b.Betas["a"] != 1 {
		t.Errorf("AddPolicy clobbered counts: a=%v b=%v", b.Alphas["a"], b.Betas["a"])
	}
	b.AddPolicy("c")
	if b.Alphas["c"] != 1 || b.Betas["c"] != 1 {
		t.Error("AddPolicy new arm should start at 1/1")
	}
}

// TestBanditConvergesToBestArm simulates three arms with fixed true
// success rates and confirms the bandit picks the best arm > 70% of
// the time after 1000 pulls. Spec says >= 70% — we run a 10k-pull
// scaffold and assert on the final 1k-pull window so the threshold
// reflects converged behavior, not warm-up.
func TestBanditConvergesToBestArm(t *testing.T) {
	rng := fixedRNG(42)
	b := newBanditWithRNG([]string{"low", "mid", "best"}, rng)

	trueRates := map[string]float64{
		"low":  0.1,
		"mid":  0.4,
		"best": 0.7,
	}
	const totalPulls = 10000
	const window = 1000
	winsLastWindow := 0
	for i := 0; i < totalPulls; i++ {
		policy := b.Select()
		reward := -1.0
		if rng.Float64() < trueRates[policy] {
			reward = 1.0
		}
		if err := b.Update(policy, reward); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if i >= totalPulls-window && policy == "best" {
			winsLastWindow++
		}
	}
	rate := float64(winsLastWindow) / float64(window)
	if rate < 0.7 {
		t.Errorf("bandit converged to best arm in only %.1f%% of final-1000 pulls; want >= 70%%", rate*100)
	}
}

func TestBanditAvoidsDominatedArm(t *testing.T) {
	// One arm is clearly dominated; after 2k pulls it should be selected
	// rarely (< 15% of the time).
	rng := fixedRNG(7)
	b := newBanditWithRNG([]string{"good", "bad"}, rng)
	trueRates := map[string]float64{"good": 0.8, "bad": 0.1}

	badSelections := 0
	const pulls = 2000
	for i := 0; i < pulls; i++ {
		p := b.Select()
		reward := -1.0
		if rng.Float64() < trueRates[p] {
			reward = 1.0
		}
		_ = b.Update(p, reward)
		if i >= 1000 && p == "bad" {
			badSelections++
		}
	}
	rate := float64(badSelections) / 1000.0
	if rate > 0.15 {
		t.Errorf("dominated arm selected %.1f%% of the time after warm-up; want < 15%%", rate*100)
	}
}
