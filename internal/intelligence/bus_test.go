package intelligence

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/redis"
)

func validBullSignal() Signal {
	return Signal{
		SourceAgentID: "agent-1",
		SignalType:    "momentum_breakout",
		Sentiment:     SentimentBull,
		Chain:         "solana",
		Confidence:    0.75,
		Data:          json.RawMessage(`{"token":"JTO","window":"1h"}`),
	}
}

func TestSentimentValid(t *testing.T) {
	for _, s := range []Sentiment{SentimentBull, SentimentBear, SentimentNeutral} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []Sentiment{"", "yolo"} {
		if Sentiment(s).Valid() {
			t.Errorf("%q should not be valid", s)
		}
	}
}

func TestChannelHelpers(t *testing.T) {
	if got := SignalsChannel("solana", SentimentBull); got != "intel:signals:solana:bull" {
		t.Errorf("got %q", got)
	}
	if got := StrategiesChannel("momentum"); got != "intel:strategies:momentum" {
		t.Errorf("got %q", got)
	}
	if got := LiquidationsChannel("base"); got != "intel:liquidations:base" {
		t.Errorf("got %q", got)
	}
	if got := YieldsChannel("solana"); got != "intel:yields:solana" {
		t.Errorf("got %q", got)
	}
}

func TestIntelBus_PublishSubscribeRoundtrip(t *testing.T) {
	r := redis.NewFake()
	bus := NewIntelBus(r)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := bus.Subscribe(ctx, SignalsChannel("solana", SentimentBull))
	if err != nil {
		t.Fatal(err)
	}

	sent := validBullSignal()
	if err := bus.Publish(ctx, SignalsChannel("solana", SentimentBull), sent); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-ch:
		if got.SourceAgentID != sent.SourceAgentID {
			t.Errorf("source_agent_id = %q, want %q", got.SourceAgentID, sent.SourceAgentID)
		}
		if got.Sentiment != SentimentBull {
			t.Errorf("sentiment = %q, want bull", got.Sentiment)
		}
		if got.PublishedAt.IsZero() {
			t.Error("PublishedAt should be filled in by Publish")
		}
		if !strings.Contains(string(got.Data), "JTO") {
			t.Errorf("data lost: %s", got.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

func TestIntelBus_RejectsEmptySource(t *testing.T) {
	bus := NewIntelBus(redis.NewFake())
	sig := validBullSignal()
	sig.SourceAgentID = ""
	if err := bus.Publish(context.Background(), SignalsChannel("solana", SentimentBull), sig); err == nil {
		t.Error("expected error for empty source_agent_id")
	}
}

func TestIntelBus_RejectsEmptySignalType(t *testing.T) {
	bus := NewIntelBus(redis.NewFake())
	sig := validBullSignal()
	sig.SignalType = ""
	if err := bus.Publish(context.Background(), SignalsChannel("solana", SentimentBull), sig); err == nil {
		t.Error("expected error for empty signal_type")
	}
}

func TestIntelBus_RejectsBadConfidence(t *testing.T) {
	bus := NewIntelBus(redis.NewFake())
	for _, c := range []float64{-0.01, 1.5} {
		sig := validBullSignal()
		sig.Confidence = c
		if err := bus.Publish(context.Background(), SignalsChannel("solana", SentimentBull), sig); err == nil {
			t.Errorf("expected error for confidence %v", c)
		}
	}
}

func TestIntelBus_SignalsChannelRequiresSentiment(t *testing.T) {
	bus := NewIntelBus(redis.NewFake())
	sig := validBullSignal()
	sig.Sentiment = ""
	if err := bus.Publish(context.Background(), "intel:signals:solana:bull", sig); err == nil {
		t.Error("expected error for missing sentiment on signals channel")
	}
}

func TestIntelBus_SignalsChannelSentimentMustMatchChannelSuffix(t *testing.T) {
	bus := NewIntelBus(redis.NewFake())
	sig := validBullSignal()
	sig.Sentiment = SentimentBear
	if err := bus.Publish(context.Background(), "intel:signals:solana:bull", sig); err == nil {
		t.Error("expected error when sentiment != channel suffix")
	}
}

func TestIntelBus_SignalsChannelBadShape(t *testing.T) {
	bus := NewIntelBus(redis.NewFake())
	if err := bus.Publish(context.Background(), "intel:signals:solana", validBullSignal()); err == nil {
		t.Error("expected error for missing sentiment suffix")
	}
}

func TestIntelBus_NonSignalChannelAllowsAnySentiment(t *testing.T) {
	bus := NewIntelBus(redis.NewFake())
	sig := validBullSignal()
	sig.Sentiment = "" // warnings channel doesn't need sentiment
	if err := bus.Publish(context.Background(), ChannelWarnings, sig); err != nil {
		t.Errorf("warnings should not require sentiment: %v", err)
	}
}

func TestIntelBus_NonSignalChannelRejectsBadSentiment(t *testing.T) {
	bus := NewIntelBus(redis.NewFake())
	sig := validBullSignal()
	sig.Sentiment = "yolo"
	if err := bus.Publish(context.Background(), ChannelWarnings, sig); err == nil {
		t.Error("expected error for unknown sentiment value")
	}
}

func TestIntelBus_PublishedAtPreservedIfSet(t *testing.T) {
	bus := NewIntelBus(redis.NewFake())
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sig := validBullSignal()
	sig.PublishedAt = fixed
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := bus.Subscribe(ctx, SignalsChannel("solana", SentimentBull))
	_ = bus.Publish(ctx, SignalsChannel("solana", SentimentBull), sig)
	select {
	case got := <-ch:
		if !got.PublishedAt.Equal(fixed) {
			t.Errorf("PublishedAt overwritten: got %v want %v", got.PublishedAt, fixed)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestIntelBus_SubscribeRequiresChannels(t *testing.T) {
	bus := NewIntelBus(redis.NewFake())
	if _, err := bus.Subscribe(context.Background()); err == nil {
		t.Error("expected error for empty channel list")
	}
}

func TestIntelBus_NilClient(t *testing.T) {
	bus := NewIntelBus(nil)
	if bus != nil {
		t.Error("expected nil bus for nil client")
	}
}

func TestIntelBus_MalformedPayloadDropped(t *testing.T) {
	// A non-JSON payload published directly via the raw redis client
	// should be skipped silently rather than killing the subscriber.
	r := redis.NewFake()
	bus := NewIntelBus(r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := bus.Subscribe(ctx, SignalsChannel("solana", SentimentBull))

	// Skip validation by publishing raw bytes via the underlying client.
	_ = r.Publish(ctx, SignalsChannel("solana", SentimentBull), []byte("not json"))
	// Follow up with a valid signal; the subscriber should still get it.
	_ = bus.Publish(ctx, SignalsChannel("solana", SentimentBull), validBullSignal())

	select {
	case got := <-ch:
		if got.SourceAgentID != "agent-1" {
			t.Errorf("got wrong message after bad payload: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout — bad payload may have killed the subscriber")
	}
}

func TestIntelBus_MultipleChannelFanout(t *testing.T) {
	r := redis.NewFake()
	bus := NewIntelBus(r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := bus.Subscribe(ctx,
		SignalsChannel("solana", SentimentBull),
		LiquidationsChannel("solana"),
	)
	bull := validBullSignal()
	liq := validBullSignal()
	liq.SignalType = "liquidation_at_risk"
	liq.Sentiment = "" // not a signals channel
	_ = bus.Publish(ctx, SignalsChannel("solana", SentimentBull), bull)
	_ = bus.Publish(ctx, LiquidationsChannel("solana"), liq)
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case s := <-ch:
			got[s.SignalType] = true
		case <-time.After(time.Second):
			t.Fatal("timeout")
		}
	}
	if !got["momentum_breakout"] || !got["liquidation_at_risk"] {
		t.Errorf("missed messages: %v", got)
	}
}
