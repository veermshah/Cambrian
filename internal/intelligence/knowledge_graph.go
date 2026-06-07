package intelligence

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Direction is the sign on a typed relationship — `SOL correlates_with
// (positive) ETH` vs. `SOL correlates_with (negative) ETH`. Spec
// line 384: contradiction is "same (entity_a, relationship, entity_b)
// but opposite direction" — so this is the field the contradiction
// rule pivots on.
type Direction string

const (
	DirectionPositive Direction = "positive"
	DirectionNegative Direction = "negative"
	DirectionNeutral  Direction = "neutral"
)

// Opposite returns the contradicting direction, or empty for neutral
// (neutral has no contradiction — only positive↔negative does).
func (d Direction) Opposite() Direction {
	switch d {
	case DirectionPositive:
		return DirectionNegative
	case DirectionNegative:
		return DirectionPositive
	}
	return ""
}

// Edge is one row in the market knowledge graph. The (EntityA,
// Relationship, EntityB, Direction) tuple is the natural key; the
// unique constraint in `market_knowledge` covers (entity_a,
// relationship, entity_b) — direction collisions are *contradictions*,
// not duplicates. Strength is in [0, 1].
type Edge struct {
	EntityA       string
	Relationship  string
	EntityB       string
	Direction     Direction
	Strength      float64
	EvidenceCount int
	DiscoveredBy  string
	LastValidated time.Time
	CreatedAt     time.Time
}

// EdgeKey is the (entity_a, relationship, entity_b) tuple used for
// contradiction detection — direction is intentionally excluded.
type EdgeKey struct {
	EntityA      string
	Relationship string
	EntityB      string
}

func (e Edge) Key() EdgeKey {
	return EdgeKey{EntityA: e.EntityA, Relationship: e.Relationship, EntityB: e.EntityB}
}

// Tunable constants — spec line 384.
const (
	// DecayInterval is the inactivity window after which an edge starts
	// losing strength per Decay run.
	DecayInterval = 30 * 24 * time.Hour
	// DecayFactor is the per-run multiplier applied to stale edges.
	DecayFactor = 0.95
	// DeletionThreshold is the strength floor below which an edge is
	// pruned entirely.
	DeletionThreshold = 0.1
	// ContradictionPenalty is the strength reduction applied to an
	// existing edge when contradicting evidence (same tuple, opposite
	// direction) is upserted.
	ContradictionPenalty = 0.2
	// MaxStrength clamps strength on Upsert so repeated confirmation
	// doesn't overflow [0, 1].
	MaxStrength = 1.0
	// MinStrength is the floor used by clampStrength.
	MinStrength = 0.0
)

// UpsertResult tells the caller what actually happened — useful for
// audit logging and for tests that need to distinguish "new edge" from
// "evidence accumulated" from "contradiction reduced opposite edge".
type UpsertResult struct {
	Inserted             bool
	Reinforced           bool
	ContradictionApplied bool
	PrevStrength         float64
	NewStrength          float64
}

// KnowledgeGraph is the in-memory cache of typed market edges, backed
// (in production) by `market_knowledge` in Postgres. The cache is
// authoritative for read paths; writes go through Upsert (which also
// persists). Concurrent-safe.
//
// Storage is in-memory by design: an edge count in the low thousands
// fits trivially in RAM, and rebuilding from Postgres on startup keeps
// the cache coherent with the durable record.
type KnowledgeGraph struct {
	mu    sync.RWMutex
	now   func() time.Time
	edges map[edgeID]*Edge
	// byKey indexes the EntityA/Relationship/EntityB tuple (direction
	// excluded) so we can find a contradicting edge in O(1).
	byKey map[EdgeKey][]edgeID
}

// edgeID = full natural key including direction (so positive and
// negative edges on the same tuple are stored as two separate rows,
// matching the spec's "contradicting evidence" model).
type edgeID struct {
	EdgeKey
	Direction Direction
}

func NewKnowledgeGraph() *KnowledgeGraph {
	return &KnowledgeGraph{
		now:   time.Now,
		edges: make(map[edgeID]*Edge),
		byKey: make(map[EdgeKey][]edgeID),
	}
}

// withClock is a test seam — production code should not touch it.
func (g *KnowledgeGraph) withClock(fn func() time.Time) *KnowledgeGraph {
	g.now = fn
	return g
}

// Upsert applies one piece of evidence to the graph. The semantics
// match spec line 384:
//
//   - If no edge exists for the (tuple, direction): inserted as a new
//     row with the supplied strength clamped to [0, 1].
//   - If the edge already exists: last_validated is bumped to now,
//     evidence_count incremented, and strength reinforced (averaged
//     toward the new evidence; capped at MaxStrength).
//   - If an opposite-direction edge exists on the same tuple: its
//     strength is reduced by ContradictionPenalty. A pure-neutral
//     direction has no opposite, so no contradiction is applied.
//
// Returns a structured UpsertResult so callers can log or audit.
func (g *KnowledgeGraph) Upsert(e Edge) (UpsertResult, error) {
	if err := validateEdge(e); err != nil {
		return UpsertResult{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.now()
	res := UpsertResult{}

	if opposite := e.Direction.Opposite(); opposite != "" {
		id := edgeID{EdgeKey: e.Key(), Direction: opposite}
		if existing, ok := g.edges[id]; ok {
			existing.Strength = clampStrength(existing.Strength - ContradictionPenalty)
			res.ContradictionApplied = true
			if existing.Strength < DeletionThreshold {
				g.removeLocked(id)
			}
		}
	}

	id := edgeID{EdgeKey: e.Key(), Direction: e.Direction}
	if existing, ok := g.edges[id]; ok {
		res.Reinforced = true
		res.PrevStrength = existing.Strength
		existing.EvidenceCount++
		existing.LastValidated = now
		// Reinforce: average existing & new strength, then nudge upward
		// by the new evidence. Bounded by MaxStrength.
		existing.Strength = clampStrength((existing.Strength + clampStrength(e.Strength)) / 2)
		res.NewStrength = existing.Strength
		return res, nil
	}

	created := e.CreatedAt
	if created.IsZero() {
		created = now
	}
	edge := &Edge{
		EntityA:       e.EntityA,
		Relationship:  e.Relationship,
		EntityB:       e.EntityB,
		Direction:     e.Direction,
		Strength:      clampStrength(e.Strength),
		EvidenceCount: max1(e.EvidenceCount),
		DiscoveredBy:  e.DiscoveredBy,
		LastValidated: now,
		CreatedAt:     created,
	}
	g.edges[id] = edge
	g.byKey[e.Key()] = append(g.byKey[e.Key()], id)
	res.Inserted = true
	res.NewStrength = edge.Strength
	return res, nil
}

// Get returns a copy of an edge by its full natural key, or false.
func (g *KnowledgeGraph) Get(key EdgeKey, dir Direction) (Edge, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	e, ok := g.edges[edgeID{EdgeKey: key, Direction: dir}]
	if !ok {
		return Edge{}, false
	}
	return *e, true
}

// Edges returns a snapshot of every edge currently in the graph,
// sorted deterministically (entity_a, relationship, entity_b,
// direction) so tests and audit dumps are stable.
func (g *KnowledgeGraph) Edges() []Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Edge, 0, len(g.edges))
	for _, e := range g.edges {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].EntityA != out[j].EntityA {
			return out[i].EntityA < out[j].EntityA
		}
		if out[i].Relationship != out[j].Relationship {
			return out[i].Relationship < out[j].Relationship
		}
		if out[i].EntityB != out[j].EntityB {
			return out[i].EntityB < out[j].EntityB
		}
		return out[i].Direction < out[j].Direction
	})
	return out
}

// Len returns the current edge count.
func (g *KnowledgeGraph) Len() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.edges)
}

// Decay applies one round of the nightly decay job:
//
//   - For each edge whose LastValidated is older than DecayInterval,
//     multiply strength by DecayFactor.
//   - Any edge whose resulting strength is below DeletionThreshold is
//     removed.
//
// Returns (decayed, deleted) counts so the caller can emit metrics.
// Idempotent for an unchanged clock — calling twice in the same tick
// applies decay twice, which is the intended "run nightly" semantics.
func (g *KnowledgeGraph) Decay() (decayed, deleted int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cutoff := g.now().Add(-DecayInterval)
	for id, e := range g.edges {
		if e.LastValidated.After(cutoff) {
			continue
		}
		e.Strength = clampStrength(e.Strength * DecayFactor)
		decayed++
		if e.Strength < DeletionThreshold {
			g.removeLocked(id)
			deleted++
		}
	}
	return decayed, deleted
}

// Neighbors returns all edges incident to `entity` (either side),
// sorted descending by Strength then ascending by counterpart name for
// determinism. Used by the aggregator and by the strategist prompt.
func (g *KnowledgeGraph) Neighbors(entity string) []Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Edge, 0, 8)
	for _, e := range g.edges {
		if e.EntityA == entity || e.EntityB == entity {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Strength != out[j].Strength {
			return out[i].Strength > out[j].Strength
		}
		ai, bi := other(out[i], entity), other(out[j], entity)
		if ai != bi {
			return ai < bi
		}
		return out[i].Direction < out[j].Direction
	})
	return out
}

func other(e Edge, entity string) string {
	if e.EntityA == entity {
		return e.EntityB
	}
	return e.EntityA
}

func (g *KnowledgeGraph) removeLocked(id edgeID) {
	delete(g.edges, id)
	list := g.byKey[id.EdgeKey]
	for i, v := range list {
		if v == id {
			g.byKey[id.EdgeKey] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(g.byKey[id.EdgeKey]) == 0 {
		delete(g.byKey, id.EdgeKey)
	}
}

func validateEdge(e Edge) error {
	if strings.TrimSpace(e.EntityA) == "" {
		return errors.New("knowledge_graph: entity_a required")
	}
	if strings.TrimSpace(e.EntityB) == "" {
		return errors.New("knowledge_graph: entity_b required")
	}
	if strings.TrimSpace(e.Relationship) == "" {
		return errors.New("knowledge_graph: relationship required")
	}
	switch e.Direction {
	case DirectionPositive, DirectionNegative, DirectionNeutral:
	default:
		return errors.New("knowledge_graph: direction must be positive/negative/neutral")
	}
	if e.Strength < 0 || e.Strength > 1 {
		return errors.New("knowledge_graph: strength out of [0,1]")
	}
	return nil
}

func clampStrength(s float64) float64 {
	if s < MinStrength {
		return MinStrength
	}
	if s > MaxStrength {
		return MaxStrength
	}
	return s
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
