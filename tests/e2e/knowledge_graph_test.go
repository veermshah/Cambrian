package e2e

import (
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/intelligence"
)

// TestKnowledgeGraph_EdgeAccumulationAndReinforcement — spec line 1193.
// Pure-logic. Repeated upserts of the same edge bump evidence_count and
// reinforce strength without overflowing [0,1].
func TestKnowledgeGraph_EdgeAccumulationAndReinforcement(t *testing.T) {
	g := intelligence.NewKnowledgeGraph()
	edge := intelligence.Edge{
		EntityA:      "ETH",
		Relationship: "leads",
		EntityB:      "SOL",
		Direction:    intelligence.DirectionPositive,
		Strength:     0.4,
	}
	for i := 0; i < 5; i++ {
		res, err := g.Upsert(edge)
		if err != nil {
			t.Fatal(err)
		}
		_ = res
	}
	got := g.Neighbors("ETH")
	if len(got) != 1 {
		t.Fatalf("neighbors = %d, want 1", len(got))
	}
	if got[0].EvidenceCount < 5 {
		t.Errorf("evidence_count = %d, want ≥5", got[0].EvidenceCount)
	}
	if got[0].Strength > 1.0 {
		t.Errorf("strength = %v, exceeds 1.0", got[0].Strength)
	}
}

// TestKnowledgeGraph_ContradictionReducesStrength — spec line 1193.
// A negative-direction edge on the same (A, rel, B) tuple penalizes
// the existing positive edge.
func TestKnowledgeGraph_ContradictionReducesStrength(t *testing.T) {
	g := intelligence.NewKnowledgeGraph()
	_, err := g.Upsert(intelligence.Edge{
		EntityA: "BTC", Relationship: "leads", EntityB: "ETH",
		Direction: intelligence.DirectionPositive, Strength: 0.8,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Upsert(intelligence.Edge{
		EntityA: "BTC", Relationship: "leads", EntityB: "ETH",
		Direction: intelligence.DirectionNegative, Strength: 0.6,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.ContradictionApplied {
		t.Error("ContradictionApplied should be true")
	}
	// The original positive edge should have lost ContradictionPenalty.
	for _, e := range g.Edges() {
		if e.Direction == intelligence.DirectionPositive && e.Strength > 0.8-intelligence.ContradictionPenalty+0.01 {
			t.Errorf("positive edge strength = %v, expected reduction", e.Strength)
		}
	}
}

// TestKnowledgeGraph_DecayLeavesFreshEdgesAlone — spec line 1193.
// Fast-forwarding the graph's clock requires an unexported seam, so
// we verify the inverse: a freshly-validated edge survives Decay with
// no change. The aging behavior is covered by the unit tests in
// internal/intelligence/knowledge_graph_test.go which can drive the
// unexported withClock helper.
func TestKnowledgeGraph_DecayLeavesFreshEdgesAlone(t *testing.T) {
	g := intelligence.NewKnowledgeGraph()
	if _, err := g.Upsert(intelligence.Edge{
		EntityA: "X", Relationship: "rel", EntityB: "Y",
		Direction: intelligence.DirectionPositive, Strength: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	decayed, deleted := g.Decay()
	if decayed != 0 || deleted != 0 {
		t.Errorf("fresh edge decayed=%d deleted=%d, want 0/0", decayed, deleted)
	}
	if len(g.Edges()) != 1 {
		t.Errorf("edges = %d, want 1", len(g.Edges()))
	}
}

// silence unused import.
var _ = time.Now
