package intelligence

import (
	"math"
	"testing"
	"time"
)

func newEdge(a, rel, b string, dir Direction, strength float64) Edge {
	return Edge{
		EntityA:      a,
		Relationship: rel,
		EntityB:      b,
		Direction:    dir,
		Strength:     strength,
	}
}

func TestKnowledgeGraph_InsertNewEdge(t *testing.T) {
	g := NewKnowledgeGraph()
	res, err := g.Upsert(newEdge("SOL", "correlates_with", "ETH", DirectionPositive, 0.85))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Inserted || res.Reinforced || res.ContradictionApplied {
		t.Errorf("expected fresh insert, got %+v", res)
	}
	if got, ok := g.Get(EdgeKey{EntityA: "SOL", Relationship: "correlates_with", EntityB: "ETH"}, DirectionPositive); !ok {
		t.Fatal("edge not found")
	} else if got.Strength != 0.85 || got.EvidenceCount != 1 {
		t.Errorf("got %+v", got)
	}
}

func TestKnowledgeGraph_ReinforcementBumpsLastValidatedAndEvidence(t *testing.T) {
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	clock := t0
	g := NewKnowledgeGraph().withClock(func() time.Time { return clock })
	if _, err := g.Upsert(newEdge("SOL", "correlates_with", "ETH", DirectionPositive, 0.6)); err != nil {
		t.Fatal(err)
	}
	clock = t0.Add(48 * time.Hour)
	if _, err := g.Upsert(newEdge("SOL", "correlates_with", "ETH", DirectionPositive, 0.9)); err != nil {
		t.Fatal(err)
	}
	got, _ := g.Get(EdgeKey{EntityA: "SOL", Relationship: "correlates_with", EntityB: "ETH"}, DirectionPositive)
	if got.EvidenceCount != 2 {
		t.Errorf("evidence_count = %d, want 2", got.EvidenceCount)
	}
	if !got.LastValidated.Equal(clock) {
		t.Errorf("last_validated = %v, want %v", got.LastValidated, clock)
	}
	if math.Abs(got.Strength-0.75) > 1e-9 {
		t.Errorf("strength = %v, want 0.75 (avg of 0.6 and 0.9)", got.Strength)
	}
}

func TestKnowledgeGraph_ContradictingEvidenceReducesStrength(t *testing.T) {
	g := NewKnowledgeGraph()
	if _, err := g.Upsert(newEdge("SOL", "correlates_with", "ETH", DirectionPositive, 0.8)); err != nil {
		t.Fatal(err)
	}
	res, err := g.Upsert(newEdge("SOL", "correlates_with", "ETH", DirectionNegative, 0.5))
	if err != nil {
		t.Fatal(err)
	}
	if !res.ContradictionApplied {
		t.Error("expected contradiction flag")
	}
	pos, _ := g.Get(EdgeKey{EntityA: "SOL", Relationship: "correlates_with", EntityB: "ETH"}, DirectionPositive)
	if math.Abs(pos.Strength-0.6) > 1e-9 {
		t.Errorf("positive strength = %v, want 0.6 (0.8 - 0.2)", pos.Strength)
	}
	neg, ok := g.Get(EdgeKey{EntityA: "SOL", Relationship: "correlates_with", EntityB: "ETH"}, DirectionNegative)
	if !ok {
		t.Fatal("negative edge not stored")
	}
	if neg.Strength != 0.5 {
		t.Errorf("negative strength = %v, want 0.5 (new edge unaffected by penalty)", neg.Strength)
	}
}

func TestKnowledgeGraph_ContradictionDeletesIfBelowThreshold(t *testing.T) {
	g := NewKnowledgeGraph()
	if _, err := g.Upsert(newEdge("X", "leads", "Y", DirectionPositive, 0.15)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Upsert(newEdge("X", "leads", "Y", DirectionNegative, 0.5)); err != nil {
		t.Fatal(err)
	}
	// Positive edge: 0.15 - 0.2 = -0.05 → clamped to 0 → below 0.1 → deleted.
	if _, ok := g.Get(EdgeKey{EntityA: "X", Relationship: "leads", EntityB: "Y"}, DirectionPositive); ok {
		t.Error("expected positive edge to be deleted")
	}
}

func TestKnowledgeGraph_DecayOver60Days(t *testing.T) {
	// Fixture per chunk-23 spec acceptance criteria: 60-day decay.
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	clock := t0
	g := NewKnowledgeGraph().withClock(func() time.Time { return clock })
	if _, err := g.Upsert(newEdge("A", "r", "B", DirectionPositive, 0.5)); err != nil {
		t.Fatal(err)
	}
	// Within window — no decay.
	clock = t0.Add(29 * 24 * time.Hour)
	if d, del := g.Decay(); d != 0 || del != 0 {
		t.Errorf("within window: decayed=%d deleted=%d, want 0/0", d, del)
	}
	// Past window — one pass: 0.5 * 0.95 = 0.475
	clock = t0.Add(31 * 24 * time.Hour)
	g.Decay()
	got, _ := g.Get(EdgeKey{EntityA: "A", Relationship: "r", EntityB: "B"}, DirectionPositive)
	if math.Abs(got.Strength-0.475) > 1e-9 {
		t.Errorf("after 1 decay: strength = %v, want 0.475", got.Strength)
	}
	// Re-validation should reset last_validated and protect from next decay.
	clock = t0.Add(31 * 24 * time.Hour)
	if _, err := g.Upsert(newEdge("A", "r", "B", DirectionPositive, 0.475)); err != nil {
		t.Fatal(err)
	}
	clock = t0.Add(45 * 24 * time.Hour) // 14 days after revalidation — still inside window
	if d, _ := g.Decay(); d != 0 {
		t.Errorf("revalidation should reset window, decayed=%d", d)
	}
}

func TestKnowledgeGraph_DecayDeletesBelowThreshold(t *testing.T) {
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	clock := t0
	g := NewKnowledgeGraph().withClock(func() time.Time { return clock })
	if _, err := g.Upsert(newEdge("A", "r", "B", DirectionPositive, 0.12)); err != nil {
		t.Fatal(err)
	}
	// Advance past the window and decay once: 0.12 * 0.95 = 0.114 → still alive
	clock = t0.Add(31 * 24 * time.Hour)
	g.Decay()
	if g.Len() != 1 {
		t.Errorf("after 1 decay edge count = %d, want 1", g.Len())
	}
	// Decay repeatedly without re-validation; eventually drops below 0.1
	// and gets deleted. The check ensures the deletion-threshold path
	// actually fires.
	deleted := false
	for range 50 {
		_, del := g.Decay()
		if del > 0 {
			deleted = true
			break
		}
	}
	if !deleted {
		t.Error("expected edge to eventually drop below threshold and be deleted")
	}
	if g.Len() != 0 {
		t.Errorf("after decay-to-zero: len=%d, want 0", g.Len())
	}
}

func TestKnowledgeGraph_NeighborsOrdering(t *testing.T) {
	g := NewKnowledgeGraph()
	if _, err := g.Upsert(newEdge("SOL", "correlates_with", "ETH", DirectionPositive, 0.85)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Upsert(newEdge("SOL", "correlates_with", "BTC", DirectionPositive, 0.45)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Upsert(newEdge("ARB", "leads", "SOL", DirectionPositive, 0.7)); err != nil {
		t.Fatal(err)
	}
	got := g.Neighbors("SOL")
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Strength != 0.85 || got[1].Strength != 0.7 || got[2].Strength != 0.45 {
		t.Errorf("ordering: %v / %v / %v", got[0].Strength, got[1].Strength, got[2].Strength)
	}
}

func TestKnowledgeGraph_Validation(t *testing.T) {
	g := NewKnowledgeGraph()
	cases := []struct {
		name string
		e    Edge
	}{
		{"missing entity_a", Edge{Relationship: "r", EntityB: "b", Direction: DirectionPositive, Strength: 0.5}},
		{"missing entity_b", Edge{EntityA: "a", Relationship: "r", Direction: DirectionPositive, Strength: 0.5}},
		{"missing relationship", Edge{EntityA: "a", EntityB: "b", Direction: DirectionPositive, Strength: 0.5}},
		{"bad direction", Edge{EntityA: "a", Relationship: "r", EntityB: "b", Direction: "sideways", Strength: 0.5}},
		{"strength too high", Edge{EntityA: "a", Relationship: "r", EntityB: "b", Direction: DirectionPositive, Strength: 1.5}},
		{"strength negative", Edge{EntityA: "a", Relationship: "r", EntityB: "b", Direction: DirectionPositive, Strength: -0.1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := g.Upsert(tc.e); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestKnowledgeGraph_NeutralHasNoContradiction(t *testing.T) {
	g := NewKnowledgeGraph()
	if _, err := g.Upsert(newEdge("A", "r", "B", DirectionNeutral, 0.5)); err != nil {
		t.Fatal(err)
	}
	// A second neutral edge reinforces (same direction).
	res, err := g.Upsert(newEdge("A", "r", "B", DirectionNeutral, 0.5))
	if err != nil {
		t.Fatal(err)
	}
	if res.ContradictionApplied {
		t.Error("neutral against neutral should not be a contradiction")
	}
	if !res.Reinforced {
		t.Error("neutral against neutral should reinforce")
	}
}

func TestKnowledgeGraph_StrengthClampedOnUpsert(t *testing.T) {
	g := NewKnowledgeGraph()
	if _, err := g.Upsert(newEdge("A", "r", "B", DirectionPositive, 0.95)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Upsert(newEdge("A", "r", "B", DirectionPositive, 1.0)); err != nil {
		t.Fatal(err)
	}
	got, _ := g.Get(EdgeKey{EntityA: "A", Relationship: "r", EntityB: "B"}, DirectionPositive)
	if got.Strength > 1.0 || got.Strength < 0.9 {
		t.Errorf("strength = %v, want in [0.9, 1.0]", got.Strength)
	}
}

func TestKnowledgeGraph_EdgesSnapshotIsStable(t *testing.T) {
	g := NewKnowledgeGraph()
	mustUpsert(t, g, newEdge("SOL", "correlates_with", "ETH", DirectionPositive, 0.8))
	mustUpsert(t, g, newEdge("ARB", "leads", "SOL", DirectionPositive, 0.6))
	mustUpsert(t, g, newEdge("BTC", "correlates_with", "ETH", DirectionNegative, 0.5))
	first := g.Edges()
	second := g.Edges()
	if len(first) != len(second) {
		t.Fatalf("lengths differ: %d / %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("not stable at %d: %v vs %v", i, first[i], second[i])
		}
	}
}

func mustUpsert(t *testing.T, g *KnowledgeGraph, e Edge) {
	t.Helper()
	if _, err := g.Upsert(e); err != nil {
		t.Fatal(err)
	}
}
