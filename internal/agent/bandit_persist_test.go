package agent

import (
	"encoding/json"
	"testing"
)

// TestSaveLoadStateRoundTrips runs the bandit through a hundred pulls,
// snapshots the state, loads it into a fresh bandit, and confirms both
// instances produce identical Select sequences from the same RNG seed.
// That's the contract a restart depends on.
func TestSaveLoadStateRoundTrips(t *testing.T) {
	policies := []string{"a", "b", "c"}
	original := newBanditWithRNG(policies, fixedRNG(99))
	// Run a hundred biased updates so alpha/beta diverge.
	for i := 0; i < 100; i++ {
		policy := original.Select()
		reward := 1.0
		if policy == "a" {
			reward = -1.0 // make "a" look bad
		}
		_ = original.Update(policy, reward)
	}

	blob, err := original.SaveState()
	if err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	restored := newBanditWithRNG(policies, fixedRNG(99))
	if err := restored.LoadState(blob); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	for k, v := range original.Alphas {
		if restored.Alphas[k] != v {
			t.Errorf("alpha[%s]: restored %v, want %v", k, restored.Alphas[k], v)
		}
	}
	for k, v := range original.Betas {
		if restored.Betas[k] != v {
			t.Errorf("beta[%s]: restored %v, want %v", k, restored.Betas[k], v)
		}
	}

	// Use a fresh RNG so original + restored march in lockstep — a
	// genuine continuation contract, not a same-instance check.
	original.rng = fixedRNG(123)
	restored.rng = fixedRNG(123)
	for i := 0; i < 50; i++ {
		got := original.Select()
		want := restored.Select()
		if got != want {
			t.Fatalf("Select divergence at step %d: original=%q restored=%q", i, got, want)
		}
		// Same update to keep posteriors aligned.
		reward := 1.0
		if got == "a" {
			reward = -1.0
		}
		_ = original.Update(got, reward)
		_ = restored.Update(want, reward)
	}
}

func TestLoadStateRejectsEmpty(t *testing.T) {
	b := NewTickBandit([]string{"a"})
	if err := b.LoadState(nil); err == nil {
		t.Error("LoadState(nil): want error")
	}
}

func TestLoadStateRejectsMalformedJSON(t *testing.T) {
	b := NewTickBandit([]string{"a"})
	if err := b.LoadState([]byte("not json")); err == nil {
		t.Error("LoadState(bogus): want error")
	}
}

func TestLoadStateRejectsMissingBetaKey(t *testing.T) {
	b := NewTickBandit([]string{"a"})
	blob, _ := json.Marshal(map[string]map[string]float64{
		"alphas": {"a": 1, "b": 2},
		"betas":  {"a": 1},
	})
	if err := b.LoadState(blob); err == nil {
		t.Error("LoadState with size mismatch: want error")
	}
}

func TestSaveStateProducesValidJSON(t *testing.T) {
	b := NewTickBandit([]string{"a", "b"})
	_ = b.Update("a", 1)
	_ = b.Update("b", -1)
	blob, err := b.SaveState()
	if err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	var decoded map[string]map[string]float64
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["alphas"]["a"] != 2 || decoded["betas"]["b"] != 2 {
		t.Errorf("unexpected snapshot: %s", blob)
	}
}
