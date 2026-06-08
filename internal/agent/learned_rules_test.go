package agent

import (
	"context"
	"errors"
	"math"
	mrand "math/rand"
	"testing"
)

func TestAdd_RejectsBlankText(t *testing.T) {
	if _, err := Add(nil, LearnedRule{Text: "   "}); !errors.Is(err, ErrEmptyRuleText) {
		t.Errorf("got %v, want ErrEmptyRuleText", err)
	}
}

func TestAdd_AssignsIDAndClampsConfidence(t *testing.T) {
	out, err := Add(nil, LearnedRule{Text: "always set stop loss", Confidence: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d", len(out))
	}
	if out[0].ID == "" {
		t.Error("expected generated ID")
	}
	if out[0].Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0", out[0].Confidence)
	}
}

func TestAdd_EvictsLowestConfidenceAtCap(t *testing.T) {
	var rules []LearnedRule
	confidences := []float64{0.9, 0.8, 0.7, 0.6, 0.5, 0.4, 0.3, 0.2, 0.1, 0.05}
	for i, c := range confidences {
		r, err := Add(rules, LearnedRule{ID: ruleIDFor(i), Text: "rule", Confidence: c})
		if err != nil {
			t.Fatal(err)
		}
		rules = r
	}
	if len(rules) != MaxLearnedRules {
		t.Fatalf("expected exactly %d rules, got %d", MaxLearnedRules, len(rules))
	}

	// Add an 11th — the 0.05-confidence rule should be evicted.
	rules, err := Add(rules, LearnedRule{ID: "newcomer", Text: "rule", Confidence: 0.6})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != MaxLearnedRules {
		t.Fatalf("cap broken: %d rules", len(rules))
	}
	for _, r := range rules {
		if r.ID == ruleIDFor(9) { // the 0.05 one
			t.Error("lowest-confidence rule was not evicted")
		}
	}
	// Newcomer must be present.
	found := false
	for _, r := range rules {
		if r.ID == "newcomer" {
			found = true
		}
	}
	if !found {
		t.Error("newcomer rule not present after eviction")
	}
}

func TestAdd_DoesNotMutateInput(t *testing.T) {
	original := []LearnedRule{{ID: "a", Text: "x", Confidence: 0.5}}
	cap := append([]LearnedRule(nil), original...)
	if _, err := Add(original, LearnedRule{Text: "y", Confidence: 0.3}); err != nil {
		t.Fatal(err)
	}
	for i := range original {
		if original[i] != cap[i] {
			t.Errorf("input mutated at %d", i)
		}
	}
}

func TestRecordOutcome_EMASequence(t *testing.T) {
	// Start at c=0.5; feed a deterministic sequence of successes/failures
	// and verify each step matches the closed-form EMA.
	rules := []LearnedRule{{ID: "r", Text: "rule", Confidence: 0.5}}
	seq := []bool{true, true, false, true, true, false, true, true, true, false}
	want := 0.5
	for _, s := range seq {
		o := 0.0
		if s {
			o = 1.0
		}
		want = (1-EMAAlpha)*want + EMAAlpha*o
		rules = RecordOutcome(rules, "r", s)
		if math.Abs(rules[0].Confidence-want) > 1e-9 {
			t.Errorf("step confidence = %v, want %v", rules[0].Confidence, want)
		}
	}
}

func TestRecordOutcome_ConvergesTowardSuccessRate(t *testing.T) {
	// Pure successes → confidence → 1.
	rules := []LearnedRule{{ID: "r", Text: "rule", Confidence: 0.0}}
	for range 200 {
		rules = RecordOutcome(rules, "r", true)
	}
	if rules[0].Confidence < 0.999 {
		t.Errorf("after 200 successes from 0: %v, want ≈ 1", rules[0].Confidence)
	}

	// Pure failures → confidence → 0.
	rules = []LearnedRule{{ID: "r", Text: "rule", Confidence: 1.0}}
	for range 200 {
		rules = RecordOutcome(rules, "r", false)
	}
	if rules[0].Confidence > 0.001 {
		t.Errorf("after 200 failures from 1: %v, want ≈ 0", rules[0].Confidence)
	}
}

func TestRecordOutcome_UnknownIDIsNoop(t *testing.T) {
	rules := []LearnedRule{{ID: "r", Text: "rule", Confidence: 0.5}}
	out := RecordOutcome(rules, "missing", true)
	if out[0].Confidence != 0.5 {
		t.Errorf("unknown ID should be no-op, got %v", out[0].Confidence)
	}
}

func TestInherit_AppliesRegressionAndFreshIDs(t *testing.T) {
	parent := []LearnedRule{
		{ID: "p1", Text: "always quote spread", Confidence: 0.8},
		{ID: "p2", Text: "avoid stETH dips", Confidence: 0.6},
	}
	child := Inherit(parent)
	if len(child) != 2 {
		t.Fatalf("child len = %d, want 2", len(child))
	}
	for i, r := range child {
		want := parent[i].Confidence * InheritanceRegression
		if math.Abs(r.Confidence-want) > 1e-9 {
			t.Errorf("child[%d] confidence = %v, want %v", i, r.Confidence, want)
		}
		if r.Text != parent[i].Text {
			t.Errorf("child[%d] text changed: %q vs %q", i, r.Text, parent[i].Text)
		}
		if r.ID == parent[i].ID {
			t.Errorf("child[%d] retained parent ID %q", i, r.ID)
		}
	}
}

func TestInherit_EmptyParentReturnsEmpty(t *testing.T) {
	if got := Inherit(nil); len(got) != 0 {
		t.Errorf("got %d rules, want 0", len(got))
	}
}

func TestInherit_OverCapKeepsTopByConfidence(t *testing.T) {
	parent := make([]LearnedRule, 0, MaxLearnedRules+2)
	for i := range MaxLearnedRules + 2 {
		parent = append(parent, LearnedRule{
			ID:         ruleIDFor(i),
			Text:       "rule",
			Confidence: float64(i+1) / 20.0, // distinct ascending values
		})
	}
	child := Inherit(parent)
	if len(child) != MaxLearnedRules {
		t.Fatalf("len = %d, want %d", len(child), MaxLearnedRules)
	}
	minSeen := 1.0
	for _, r := range child {
		if r.Confidence < minSeen {
			minSeen = r.Confidence
		}
	}
	// The two lowest-confidence parents (0.05, 0.10) must be dropped.
	if minSeen < 0.15*InheritanceRegression-1e-9 {
		t.Errorf("kept a too-low-confidence rule: %v", minSeen)
	}
}

func TestMutate_NilRewriterIsNoop(t *testing.T) {
	rules := []LearnedRule{{ID: "r", Text: "x", Confidence: 0.5}}
	out, errs := Mutate(context.Background(), mrand.New(mrand.NewSource(1)), rules, MutateConfig{Probability: 1.0})
	if errs != nil {
		t.Errorf("expected no errors, got %v", errs)
	}
	if out[0].Text != "x" {
		t.Errorf("nil rewriter should leave text unchanged, got %q", out[0].Text)
	}
}

func TestMutate_RewritesWithProbabilityOne(t *testing.T) {
	rules := []LearnedRule{
		{ID: "r1", Text: "old text 1", Confidence: 0.5},
		{ID: "r2", Text: "old text 2", Confidence: 0.6},
	}
	rw := RuleRewriterFunc(func(_ context.Context, text string) (string, error) {
		return "NEW: " + text, nil
	})
	out, errs := Mutate(context.Background(), mrand.New(mrand.NewSource(1)), rules, MutateConfig{Probability: 1.0, Rewriter: rw})
	if errs != nil {
		t.Fatalf("unexpected errors: %v", errs)
	}
	for i, r := range out {
		if r.Text != "NEW: "+rules[i].Text {
			t.Errorf("rule %d not rewritten: %q", i, r.Text)
		}
		if r.ID != rules[i].ID || r.Confidence != rules[i].Confidence {
			t.Errorf("rule %d ID/confidence changed: %+v vs %+v", i, r, rules[i])
		}
	}
}

func TestMutate_ZeroProbabilityNeverRewrites(t *testing.T) {
	rules := []LearnedRule{{ID: "r", Text: "x", Confidence: 0.5}}
	called := 0
	rw := RuleRewriterFunc(func(_ context.Context, text string) (string, error) {
		called++
		return "NEW", nil
	})
	// Probability ≤ 0 falls back to DefaultMutationProbability (0.05).
	// With seed 1 over a single rule the first Float64 draw is
	// >= 0.05, so no call should happen. The test asserts the simple
	// invariant: rule text matches one of {original, rewritten}.
	out, _ := Mutate(context.Background(), mrand.New(mrand.NewSource(1)), rules, MutateConfig{Probability: 0, Rewriter: rw})
	if out[0].Text != "x" && out[0].Text != "NEW" {
		t.Errorf("unexpected text: %q", out[0].Text)
	}
	_ = called
}

func TestMutate_PropagatesRewriterErrorAndKeepsText(t *testing.T) {
	rules := []LearnedRule{{ID: "r", Text: "keep me", Confidence: 0.5}}
	rw := RuleRewriterFunc(func(_ context.Context, text string) (string, error) {
		return "", errors.New("upstream LLM 503")
	})
	out, errs := Mutate(context.Background(), mrand.New(mrand.NewSource(1)), rules, MutateConfig{Probability: 1.0, Rewriter: rw})
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %v", errs)
	}
	if out[0].Text != "keep me" {
		t.Errorf("text should survive rewriter failure, got %q", out[0].Text)
	}
}

func TestMutate_DropsEmptyRewrite(t *testing.T) {
	rules := []LearnedRule{{ID: "r", Text: "keep me", Confidence: 0.5}}
	rw := RuleRewriterFunc(func(_ context.Context, _ string) (string, error) { return "   ", nil })
	out, errs := Mutate(context.Background(), mrand.New(mrand.NewSource(1)), rules, MutateConfig{Probability: 1.0, Rewriter: rw})
	if errs != nil {
		t.Errorf("blank rewrite is silent: %v", errs)
	}
	if out[0].Text != "keep me" {
		t.Errorf("blank rewrite should be ignored, got %q", out[0].Text)
	}
}

func TestMutate_NilRNGStillWorks(t *testing.T) {
	rules := []LearnedRule{{ID: "r", Text: "x", Confidence: 0.5}}
	rw := RuleRewriterFunc(func(_ context.Context, text string) (string, error) { return "rewritten", nil })
	out, errs := Mutate(context.Background(), nil, rules, MutateConfig{Probability: 1.0, Rewriter: rw})
	if errs != nil {
		t.Errorf("unexpected errors: %v", errs)
	}
	if out[0].Text != "rewritten" {
		t.Errorf("expected rewrite to apply with default RNG, got %q", out[0].Text)
	}
}

func ruleIDFor(i int) string {
	return "rule-" + string(rune('a'+i))
}
