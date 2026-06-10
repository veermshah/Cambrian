package e2e

import (
	"math"
	"testing"

	"github.com/veermshah/cambrian/internal/agent"
)

// TestLearnedRules_AccumulateAndEvictAtCap — spec line 1192.
// Adding rules past MaxLearnedRules evicts the lowest-confidence one,
// not the oldest. Pure-logic, runs anywhere.
func TestLearnedRules_AccumulateAndEvictAtCap(t *testing.T) {
	var rules []agent.LearnedRule
	// Fill to cap with descending confidence.
	for i := 0; i < agent.MaxLearnedRules; i++ {
		conf := 1.0 - float64(i)*0.05 // 1.0, 0.95, ... 0.55
		updated, err := agent.Add(rules, agent.LearnedRule{
			Text:       "rule",
			Confidence: conf,
		})
		if err != nil {
			t.Fatal(err)
		}
		rules = updated
	}
	if len(rules) != agent.MaxLearnedRules {
		t.Fatalf("len = %d, want %d", len(rules), agent.MaxLearnedRules)
	}
	// Add an 11th with mid confidence — the 0.55 rule should evict.
	rules, err := agent.Add(rules, agent.LearnedRule{Text: "newcomer", Confidence: 0.7})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != agent.MaxLearnedRules {
		t.Errorf("after eviction len = %d", len(rules))
	}
	for _, r := range rules {
		if r.Confidence < 0.6 {
			t.Errorf("0.55 rule survived eviction: %+v", r)
		}
	}
	var found bool
	for _, r := range rules {
		if r.Text == "newcomer" {
			found = true
		}
	}
	if !found {
		t.Error("newcomer not retained")
	}
}

// TestLearnedRules_InheritRegressesConfidence — spec line 1192.
// Offspring start at 0.7 × parent.
func TestLearnedRules_InheritRegressesConfidence(t *testing.T) {
	parent := []agent.LearnedRule{
		{ID: "p1", Text: "rule-a", Confidence: 1.0},
		{ID: "p2", Text: "rule-b", Confidence: 0.5},
	}
	child := agent.Inherit(parent)
	if len(child) != 2 {
		t.Fatalf("len = %d", len(child))
	}
	want := []float64{0.7, 0.35}
	for i, r := range child {
		if math.Abs(r.Confidence-want[i]) > 0.001 {
			t.Errorf("child[%d].Confidence = %v, want %v", i, r.Confidence, want[i])
		}
		if r.ID == parent[i].ID {
			t.Errorf("child[%d].ID should be regenerated", i)
		}
	}
}

// TestLearnedRules_OutcomeMovesConfidence — EMA bumps confidence toward
// observed outcome. Pure-logic.
func TestLearnedRules_OutcomeMovesConfidence(t *testing.T) {
	rules := []agent.LearnedRule{{ID: "r1", Text: "rule", Confidence: 0.5}}
	// 20 successes should pull confidence well above 0.5.
	for i := 0; i < 20; i++ {
		rules = agent.RecordOutcome(rules, "r1", true)
	}
	if rules[0].Confidence < 0.8 {
		t.Errorf("20 successes: confidence = %v, want ≥0.8", rules[0].Confidence)
	}
	// 20 failures from there should pull back below 0.5.
	for i := 0; i < 20; i++ {
		rules = agent.RecordOutcome(rules, "r1", false)
	}
	if rules[0].Confidence > 0.3 {
		t.Errorf("after failures: confidence = %v, want ≤0.3", rules[0].Confidence)
	}
}
