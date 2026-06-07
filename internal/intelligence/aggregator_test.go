package intelligence

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func aggNow(ts time.Time) func() time.Time { return func() time.Time { return ts } }

func makeSignal(source, typ string, sent Sentiment, conf float64, at time.Time) Signal {
	return Signal{
		SourceAgentID: source,
		SignalType:    typ,
		Sentiment:     sent,
		Chain:         "solana",
		Confidence:    conf,
		Data:          json.RawMessage(`{"token":"SOL"}`),
		PublishedAt:   at,
	}
}

func TestAggregator_SummarizeRanksByWeightedScore(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	feed := NewInMemorySignalFeed()
	// Trusted source with 10/10 accuracy.
	acc := NewAccuracyTracker(0).withClock(aggNow(now))
	for range 10 {
		acc.Ingest(SignalOutcome{SourceAgentID: "trusted", ObservedAt: now.Add(-time.Hour), Correct: true})
	}
	// Untrusted source with 0/10 accuracy.
	for range 10 {
		acc.Ingest(SignalOutcome{SourceAgentID: "untrusted", ObservedAt: now.Add(-time.Hour), Correct: false})
	}

	ch := SignalsChannel("solana", SentimentBull)
	feed.Append(ch, makeSignal("untrusted", "momentum", SentimentBull, 0.9, now.Add(-1*time.Hour)))
	feed.Append(ch, makeSignal("trusted", "momentum", SentimentBull, 0.7, now.Add(-1*time.Hour)))

	a := NewIntelAggregator(AggregatorConfig{Now: aggNow(now)}, feed, acc, nil)
	snap := a.Summarize("strategist-1", []string{ch}, nil)
	if len(snap.TopSignals) != 2 {
		t.Fatalf("got %d signals, want 2", len(snap.TopSignals))
	}
	if snap.TopSignals[0].SourceAgentID != "trusted" {
		t.Errorf("ranking wrong: %+v", snap.TopSignals)
	}
}

func TestAggregator_DedupesBySourceAndType(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	feed := NewInMemorySignalFeed()
	ch := SignalsChannel("solana", SentimentBull)
	// Two signals from the same source/type — only one digest should appear.
	feed.Append(ch, makeSignal("a", "momentum", SentimentBull, 0.5, now.Add(-3*time.Hour)))
	feed.Append(ch, makeSignal("a", "momentum", SentimentBull, 0.7, now.Add(-1*time.Hour)))
	feed.Append(ch, makeSignal("b", "momentum", SentimentBull, 0.5, now.Add(-1*time.Hour)))

	a := NewIntelAggregator(AggregatorConfig{Now: aggNow(now)}, feed, nil, nil)
	snap := a.Summarize("s", []string{ch}, nil)
	if len(snap.TopSignals) != 2 {
		t.Fatalf("expected 2 digests after dedup, got %d", len(snap.TopSignals))
	}
	// Should pick the higher-weighted (0.7 * 0.5 = 0.35 > 0.5 * 0.5 = 0.25)
	for _, s := range snap.TopSignals {
		if s.SourceAgentID == "a" && s.Confidence != 0.7 {
			t.Errorf("kept the wrong signal for a: %+v", s)
		}
	}
}

func TestAggregator_FreshnessExcludesOldSignals(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	feed := NewInMemorySignalFeed()
	ch := SignalsChannel("solana", SentimentBull)
	feed.Append(ch, makeSignal("old", "x", SentimentBull, 0.9, now.Add(-72*time.Hour)))
	feed.Append(ch, makeSignal("new", "x", SentimentBull, 0.4, now.Add(-1*time.Hour)))

	a := NewIntelAggregator(AggregatorConfig{Now: aggNow(now), SignalFreshness: 24 * time.Hour}, feed, nil, nil)
	snap := a.Summarize("s", []string{ch}, nil)
	if len(snap.TopSignals) != 1 || snap.TopSignals[0].SourceAgentID != "new" {
		t.Errorf("expected only the fresh signal, got %+v", snap.TopSignals)
	}
}

func TestAggregator_KnowledgeEdgesAttached(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	g := NewKnowledgeGraph().withClock(aggNow(now))
	mustUpsert(t, g, newEdge("SOL", "correlates_with", "ETH", DirectionPositive, 0.85))
	mustUpsert(t, g, newEdge("SOL", "correlates_with", "BTC", DirectionPositive, 0.4))
	mustUpsert(t, g, newEdge("USDC", "depegs_with", "USDT", DirectionPositive, 0.2)) // unrelated

	a := NewIntelAggregator(AggregatorConfig{Now: aggNow(now)}, nil, nil, g)
	snap := a.Summarize("s", nil, []string{"SOL"})
	if len(snap.KnowledgeEdges) != 2 {
		t.Fatalf("got %d edges, want 2 (SOL neighbors)", len(snap.KnowledgeEdges))
	}
	if snap.KnowledgeEdges[0].Strength != 0.85 {
		t.Errorf("edges not sorted by strength: %+v", snap.KnowledgeEdges)
	}
}

func TestAggregator_TokenBudgetEnforced(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	feed := NewInMemorySignalFeed()
	ch := SignalsChannel("solana", SentimentBull)
	for i := range 50 {
		feed.Append(ch, makeSignal(string(rune('a'+i%26)), "type"+string(rune('a'+i%26)), SentimentBull, 0.5, now.Add(-time.Duration(i)*time.Minute)))
	}
	a := NewIntelAggregator(AggregatorConfig{
		Now:         aggNow(now),
		MaxSignals:  100,
		TokenBudget: 80, // deliberately tight
	}, feed, nil, nil)
	snap := a.Summarize("s", []string{ch}, nil)
	if !snap.Truncated {
		t.Error("expected Truncated=true under tight budget")
	}
	if snap.EstimatedTokens > 80 {
		t.Errorf("snapshot exceeded budget: %d tokens", snap.EstimatedTokens)
	}
}

func TestAggregator_NilDependenciesProduceEmptySections(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	a := NewIntelAggregator(AggregatorConfig{Now: aggNow(now)}, nil, nil, nil)
	snap := a.Summarize("s", []string{"intel:signals:solana:bull"}, []string{"SOL"})
	if len(snap.TopSignals) != 0 || len(snap.KnowledgeEdges) != 0 {
		t.Errorf("expected empty snapshot, got %+v", snap)
	}
	out := snap.Render()
	if !strings.Contains(out, "(none)") {
		t.Errorf("render should note empty sections: %s", out)
	}
}

func TestAggregator_SourceWeightsAuditMatchesDigests(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	feed := NewInMemorySignalFeed()
	ch := SignalsChannel("solana", SentimentBull)
	feed.Append(ch, makeSignal("a", "x", SentimentBull, 0.6, now.Add(-time.Hour)))
	feed.Append(ch, makeSignal("b", "x", SentimentBull, 0.6, now.Add(-time.Hour)))

	a := NewIntelAggregator(AggregatorConfig{Now: aggNow(now)}, feed, nil, nil)
	snap := a.Summarize("s", []string{ch}, nil)
	if len(snap.SourceWeights) != 2 {
		t.Fatalf("audit count = %d, want 2", len(snap.SourceWeights))
	}
	for _, w := range snap.SourceWeights {
		if w.Weight != 0.5 {
			t.Errorf("weight = %v, want 0.5 (no accuracy tracker)", w.Weight)
		}
	}
}

func TestAggregator_RenderIncludesAllSections(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	feed := NewInMemorySignalFeed()
	feed.Append(SignalsChannel("solana", SentimentBull),
		makeSignal("scout-1", "breakout", SentimentBull, 0.7, now.Add(-time.Hour)))
	g := NewKnowledgeGraph().withClock(aggNow(now))
	mustUpsert(t, g, newEdge("SOL", "correlates_with", "ETH", DirectionPositive, 0.85))

	a := NewIntelAggregator(AggregatorConfig{Now: aggNow(now)}, feed, nil, g)
	snap := a.Summarize("strategist-7",
		[]string{SignalsChannel("solana", SentimentBull)},
		[]string{"SOL"},
	)
	out := snap.Render()
	for _, want := range []string{"Intel for strategist-7", "Top signals", "Known relationships", "scout-1", "correlates_with"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}
}

func TestInMemorySignalFeed_OnlyReturnsFreshSorted(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	f := NewInMemorySignalFeed()
	ch := "intel:signals:solana:bull"
	f.Append(ch, makeSignal("a", "x", SentimentBull, 0.5, now.Add(-2*time.Hour)))
	f.Append(ch, makeSignal("b", "x", SentimentBull, 0.5, now.Add(-30*time.Minute)))
	f.Append(ch, makeSignal("c", "x", SentimentBull, 0.5, now.Add(-72*time.Hour)))
	got := f.Recent(ch, now.Add(-24*time.Hour))
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (fresh only)", len(got))
	}
	if !got[0].PublishedAt.After(got[1].PublishedAt) {
		t.Error("expected most-recent-first ordering")
	}
}
