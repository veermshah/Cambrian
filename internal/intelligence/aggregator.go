package intelligence

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// IntelSnapshot is what the strategist sees. Strings are pre-formatted
// markdown bullets so the prompt builder can drop them straight into
// the LLM call without further templating. Sized to fit comfortably
// under a 1k-token budget (spec line 1160).
type IntelSnapshot struct {
	AgentID         string
	GeneratedAt     time.Time
	TopSignals      []SignalDigest // strongest weighted signals
	KnowledgeEdges  []EdgeDigest   // graph edges incident to the agent's focus entities
	SourceWeights   []SourceWeight // per-source accuracy weights actually applied
	EstimatedTokens int            // rough char/4 estimate
	Truncated       bool           // true if we had to drop items to meet the budget
}

// SignalDigest is one signal as the strategist will see it.
type SignalDigest struct {
	SourceAgentID  string
	SignalType     string
	Sentiment      Sentiment
	Chain          string
	Confidence     float64 // publisher-reported
	SourceAccuracy float64 // from AccuracyTracker.Weight at digest time
	Weighted       float64 // Confidence * SourceAccuracy — the field the strategist should rank by
	PublishedAt    time.Time
	Summary        string // <= 200 chars; first non-empty field of Data, else SignalType+sentiment
}

// EdgeDigest is one knowledge-graph edge in human-readable form.
type EdgeDigest struct {
	EntityA      string
	Relationship string
	EntityB      string
	Direction    Direction
	Strength     float64
}

// SourceWeight is one entry in the audit list of who-was-weighted-how
// for this snapshot.
type SourceWeight struct {
	SourceAgentID string
	Weight        float64
}

// AggregatorConfig parameterises the aggregator.
type AggregatorConfig struct {
	// MaxSignals caps the number of signal digests included. Defaults
	// to 10.
	MaxSignals int
	// MaxEdges caps the number of knowledge-graph edges included.
	// Defaults to 8.
	MaxEdges int
	// TokenBudget is the soft upper bound (in tokens, char/4). When the
	// snapshot would exceed it, the aggregator drops the lowest-ranked
	// signals first, then the lowest-strength edges. Defaults to 1000
	// per spec line 1160.
	TokenBudget int
	// SignalFreshness is the lookback window for signals — older items
	// are excluded from consideration. Defaults to 24h.
	SignalFreshness time.Duration
	// Now is injected for tests. Defaults to time.Now.
	Now func() time.Time
}

func (c AggregatorConfig) withDefaults() AggregatorConfig {
	if c.MaxSignals <= 0 {
		c.MaxSignals = 10
	}
	if c.MaxEdges <= 0 {
		c.MaxEdges = 8
	}
	if c.TokenBudget <= 0 {
		c.TokenBudget = 1000
	}
	if c.SignalFreshness <= 0 {
		c.SignalFreshness = 24 * time.Hour
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// SignalFeed is the read side of the intel bus, abstracted so the
// aggregator can be unit-tested without standing up Redis. Recent
// returns signals on `channel` newer than `since`, most-recent first.
type SignalFeed interface {
	Recent(channel string, since time.Time) []Signal
}

// InMemorySignalFeed is a tests-and-glue helper that satisfies
// SignalFeed from a slice of pre-recorded signals. The production
// wiring will replace this with a Redis-backed reader hitting
// `intel_log`.
type InMemorySignalFeed struct {
	mu sync.RWMutex
	// channel -> signals in arrival order
	byChannel map[string][]Signal
}

func NewInMemorySignalFeed() *InMemorySignalFeed {
	return &InMemorySignalFeed{byChannel: make(map[string][]Signal)}
}

// Append records one signal. Threadsafe.
func (f *InMemorySignalFeed) Append(channel string, sig Signal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byChannel[channel] = append(f.byChannel[channel], sig)
}

// Recent returns signals newer than `since`, most recent first.
func (f *InMemorySignalFeed) Recent(channel string, since time.Time) []Signal {
	f.mu.RLock()
	defer f.mu.RUnlock()
	src := f.byChannel[channel]
	out := make([]Signal, 0, len(src))
	for _, s := range src {
		if s.PublishedAt.After(since) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].PublishedAt.After(out[j].PublishedAt)
	})
	return out
}

// IntelAggregator builds per-agent intel snapshots for the strategist
// prompt. Spec line 1160: pulls top-N recent intel from the agent's
// subscribed channels, weighted by source accuracy (chunk 22), deduped,
// returns a <1k-token summary.
type IntelAggregator struct {
	cfg      AggregatorConfig
	feed     SignalFeed
	accuracy *AccuracyTracker
	graph    *KnowledgeGraph
}

// NewIntelAggregator constructs an aggregator. accuracy and graph are
// optional — nil means "skip weighting" / "skip knowledge edges".
func NewIntelAggregator(cfg AggregatorConfig, feed SignalFeed, accuracy *AccuracyTracker, graph *KnowledgeGraph) *IntelAggregator {
	return &IntelAggregator{
		cfg:      cfg.withDefaults(),
		feed:     feed,
		accuracy: accuracy,
		graph:    graph,
	}
}

// Summarize builds an IntelSnapshot for the given agent. Channels are
// the bus channels this agent subscribes to; entities are the tokens /
// chains the agent cares about for the knowledge-graph slice.
//
// Dedupe rule: at most one digest per (source_agent_id, signal_type)
// — we keep the most recent and accumulate confidence by max, not sum,
// to avoid one chatty source dominating the slot count.
func (a *IntelAggregator) Summarize(agentID string, channels []string, entities []string) IntelSnapshot {
	now := a.cfg.Now()
	since := now.Add(-a.cfg.SignalFreshness)
	snap := IntelSnapshot{
		AgentID:     agentID,
		GeneratedAt: now,
	}
	if a.feed != nil {
		snap.TopSignals = a.collectSignals(channels, since)
	}
	if a.graph != nil {
		snap.KnowledgeEdges = a.collectEdges(entities)
	}
	snap.SourceWeights = a.weightsAudit(snap.TopSignals)
	snap.EstimatedTokens = estimateTokens(snap)
	snap = a.enforceBudget(snap)
	return snap
}

func (a *IntelAggregator) collectSignals(channels []string, since time.Time) []SignalDigest {
	type key struct {
		Source, Type string
	}
	best := map[key]SignalDigest{}
	for _, ch := range channels {
		for _, s := range a.feed.Recent(ch, since) {
			d := a.digest(s)
			k := key{Source: d.SourceAgentID, Type: d.SignalType}
			cur, ok := best[k]
			// Keep the digest with the higher weighted score; ties broken
			// by most recent.
			if !ok || d.Weighted > cur.Weighted ||
				(d.Weighted == cur.Weighted && d.PublishedAt.After(cur.PublishedAt)) {
				best[k] = d
			}
		}
	}
	out := make([]SignalDigest, 0, len(best))
	for _, d := range best {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Weighted != out[j].Weighted {
			return out[i].Weighted > out[j].Weighted
		}
		if !out[i].PublishedAt.Equal(out[j].PublishedAt) {
			return out[i].PublishedAt.After(out[j].PublishedAt)
		}
		return out[i].SourceAgentID < out[j].SourceAgentID
	})
	if len(out) > a.cfg.MaxSignals {
		out = out[:a.cfg.MaxSignals]
	}
	return out
}

func (a *IntelAggregator) digest(s Signal) SignalDigest {
	weight := 0.5
	if a.accuracy != nil {
		weight = a.accuracy.Weight(s.SourceAgentID)
	}
	return SignalDigest{
		SourceAgentID:  s.SourceAgentID,
		SignalType:     s.SignalType,
		Sentiment:      s.Sentiment,
		Chain:          s.Chain,
		Confidence:     s.Confidence,
		SourceAccuracy: weight,
		Weighted:       s.Confidence * weight,
		PublishedAt:    s.PublishedAt,
		Summary:        summarise(s),
	}
}

func (a *IntelAggregator) collectEdges(entities []string) []EdgeDigest {
	if len(entities) == 0 {
		return nil
	}
	seen := map[edgeID]struct{}{}
	out := make([]EdgeDigest, 0, a.cfg.MaxEdges*2)
	for _, ent := range entities {
		for _, e := range a.graph.Neighbors(ent) {
			id := edgeID{EdgeKey: e.Key(), Direction: e.Direction}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, EdgeDigest{
				EntityA:      e.EntityA,
				Relationship: e.Relationship,
				EntityB:      e.EntityB,
				Direction:    e.Direction,
				Strength:     e.Strength,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Strength != out[j].Strength {
			return out[i].Strength > out[j].Strength
		}
		if out[i].EntityA != out[j].EntityA {
			return out[i].EntityA < out[j].EntityA
		}
		return out[i].EntityB < out[j].EntityB
	})
	if len(out) > a.cfg.MaxEdges {
		out = out[:a.cfg.MaxEdges]
	}
	return out
}

func (a *IntelAggregator) weightsAudit(sigs []SignalDigest) []SourceWeight {
	seen := map[string]float64{}
	for _, s := range sigs {
		seen[s.SourceAgentID] = s.SourceAccuracy
	}
	out := make([]SourceWeight, 0, len(seen))
	for k, v := range seen {
		out = append(out, SourceWeight{SourceAgentID: k, Weight: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].SourceAgentID < out[j].SourceAgentID
	})
	return out
}

// enforceBudget drops the lowest-weighted signals (and then the
// lowest-strength edges) until estimateTokens fits the configured
// budget. Sets Truncated when items were dropped.
func (a *IntelAggregator) enforceBudget(snap IntelSnapshot) IntelSnapshot {
	for estimateTokens(snap) > a.cfg.TokenBudget {
		// Prefer dropping a signal first — they're chattier per item.
		if len(snap.TopSignals) > 0 {
			snap.TopSignals = snap.TopSignals[:len(snap.TopSignals)-1]
			snap.Truncated = true
			continue
		}
		if len(snap.KnowledgeEdges) > 0 {
			snap.KnowledgeEdges = snap.KnowledgeEdges[:len(snap.KnowledgeEdges)-1]
			snap.Truncated = true
			continue
		}
		break
	}
	if snap.Truncated {
		snap.SourceWeights = a.weightsAudit(snap.TopSignals)
	}
	snap.EstimatedTokens = estimateTokens(snap)
	return snap
}

// Render produces the markdown the strategist actually sees. Stable
// formatting so prompt-level cache reuse holds across runs with the
// same underlying intel.
func (s IntelSnapshot) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Intel for %s (generated %s)\n",
		s.AgentID, s.GeneratedAt.UTC().Format(time.RFC3339))
	if s.Truncated {
		b.WriteString("_(truncated to fit token budget)_\n")
	}
	b.WriteString("\n## Top signals\n")
	if len(s.TopSignals) == 0 {
		b.WriteString("(none)\n")
	}
	for _, sig := range s.TopSignals {
		fmt.Fprintf(&b, "- [%s] %s/%s from=%s w=%.2f (conf=%.2f × src=%.2f) %s\n",
			sig.Sentiment, sig.Chain, sig.SignalType, sig.SourceAgentID, sig.Weighted, sig.Confidence, sig.SourceAccuracy, sig.Summary)
	}
	b.WriteString("\n## Known relationships\n")
	if len(s.KnowledgeEdges) == 0 {
		b.WriteString("(none)\n")
	}
	for _, e := range s.KnowledgeEdges {
		fmt.Fprintf(&b, "- %s --%s(%s,%.2f)--> %s\n",
			e.EntityA, e.Relationship, e.Direction, e.Strength, e.EntityB)
	}
	return b.String()
}

func summarise(s Signal) string {
	if len(s.Data) > 0 {
		const max = 160
		raw := string(s.Data)
		if len(raw) > max {
			raw = raw[:max] + "…"
		}
		return raw
	}
	return fmt.Sprintf("%s/%s", s.SignalType, s.Sentiment)
}

// estimateTokens uses the standard char/4 approximation. Errs on the
// generous side: tokenizers usually emit fewer tokens than chars/4 for
// English text, so a budget-passing snapshot will fit in real tokens.
func estimateTokens(s IntelSnapshot) int {
	return len(s.Render()) / 4
}
