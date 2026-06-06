// Package intelligence is the swarm's communication substrate: a typed
// Redis pub/sub bus (chunk 22), the signal accuracy tracker that drives
// emergent authority (chunk 22), and — when it lands — the market
// knowledge graph (chunk 23).
package intelligence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/veermshah/cambrian/internal/redis"
)

// Sentiment is the bull/bear/neutral tag every signal on
// `intel:signals:*` must carry. Spec lines 374-375: signals are routed
// by sentiment as well as chain so consumers can subscribe to one side
// of the order book without filtering downstream.
type Sentiment string

const (
	SentimentBull    Sentiment = "bull"
	SentimentBear    Sentiment = "bear"
	SentimentNeutral Sentiment = "neutral"
)

// Valid reports whether s is one of the closed-set values. Used by the
// publish-side validator and by the accuracy tracker's outcome
// classifier.
func (s Sentiment) Valid() bool {
	switch s {
	case SentimentBull, SentimentBear, SentimentNeutral:
		return true
	}
	return false
}

// Signal is the canonical message body on every intelligence channel.
// Encoded as JSON; consumers decode and dispatch on SignalType.
//
// Confidence is the publisher's self-reported probability the signal is
// accurate, in [0, 1]. Consumers should re-weight by source accuracy
// (AccuracyTracker.Score) — Confidence alone is not authoritative.
type Signal struct {
	SourceAgentID string          `json:"source_agent_id"`
	SignalType    string          `json:"signal_type"`
	Sentiment     Sentiment       `json:"sentiment,omitempty"`
	Chain         string          `json:"chain,omitempty"`
	TaskType      string          `json:"task_type,omitempty"`
	Confidence    float64         `json:"confidence"`
	Data          json.RawMessage `json:"data,omitempty"`
	PublishedAt   time.Time       `json:"published_at"`
}

// Channel constants — spec lines 374-380.
const (
	ChannelSignalsPrefix     = "intel:signals:"      // intel:signals:{chain}:{sentiment}
	ChannelStrategiesPrefix  = "intel:strategies:"   // intel:strategies:{task_type}
	ChannelWarnings          = "intel:warnings"
	ChannelLiquidationsPrefix = "intel:liquidations:" // intel:liquidations:{chain}
	ChannelYieldsPrefix      = "intel:yields:"       // intel:yields:{chain}
)

// SignalsChannel returns the canonical signals channel for a given
// (chain, sentiment). Both arguments are required; the validator at
// Publish time rejects empty sentiment on this prefix.
func SignalsChannel(chain string, sentiment Sentiment) string {
	return ChannelSignalsPrefix + chain + ":" + string(sentiment)
}

// StrategiesChannel returns the canonical strategies channel for a task
// type.
func StrategiesChannel(taskType string) string {
	return ChannelStrategiesPrefix + taskType
}

// LiquidationsChannel returns the canonical liquidations channel for a
// chain.
func LiquidationsChannel(chain string) string { return ChannelLiquidationsPrefix + chain }

// YieldsChannel returns the canonical yields channel for a chain.
func YieldsChannel(chain string) string { return ChannelYieldsPrefix + chain }

// IntelBus is the typed wrapper around redis.Client that every agent
// uses to communicate. Producers call Publish; consumers call
// Subscribe. The bus enforces channel-specific validation rules — most
// notably the bull/bear tag requirement on `intel:signals:*`.
type IntelBus struct {
	client redis.Client
	now    func() time.Time
}

// NewIntelBus constructs a bus over the given redis.Client. The
// production constructor passes redis.New(...); tests pass
// redis.NewFakeRedis().
func NewIntelBus(client redis.Client) *IntelBus {
	if client == nil {
		return nil
	}
	return &IntelBus{client: client, now: time.Now}
}

// Publish validates the signal against the channel's rules, fills in
// PublishedAt if zero, and routes the encoded payload to Redis.
//
// Validation:
//   - SourceAgentID required.
//   - SignalType required.
//   - Confidence must be in [0, 1].
//   - On `intel:signals:*`: Sentiment must be non-empty and one of
//     bull / bear / neutral. The trailing channel suffix must match the
//     sentiment so consumers can rely on the channel name alone.
//   - On any other prefix: Sentiment is optional but rejected if set
//     to an unknown value.
func (b *IntelBus) Publish(ctx context.Context, channel string, sig Signal) error {
	if b == nil || b.client == nil {
		return errors.New("intelbus: nil client")
	}
	if err := validateSignal(channel, sig); err != nil {
		return err
	}
	if sig.PublishedAt.IsZero() {
		sig.PublishedAt = b.now()
	}
	payload, err := json.Marshal(sig)
	if err != nil {
		return fmt.Errorf("intelbus: marshal: %w", err)
	}
	return b.client.Publish(ctx, channel, payload)
}

// Subscribe joins the requested channels and returns a buffered
// channel of decoded Signal values. Decode errors are dropped silently
// after the first one is logged at the warning level — a malformed
// message must not stall a consumer. The returned channel closes when
// ctx is cancelled.
func (b *IntelBus) Subscribe(ctx context.Context, channels ...string) (<-chan Signal, error) {
	if b == nil || b.client == nil {
		return nil, errors.New("intelbus: nil client")
	}
	if len(channels) == 0 {
		return nil, errors.New("intelbus: at least one channel required")
	}
	raw, err := b.client.Subscribe(ctx, channels...)
	if err != nil {
		return nil, fmt.Errorf("intelbus: subscribe: %w", err)
	}
	out := make(chan Signal, 64)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-raw:
				if !ok {
					return
				}
				var sig Signal
				if err := json.Unmarshal(m.Payload, &sig); err != nil {
					continue
				}
				select {
				case out <- sig:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

func validateSignal(channel string, sig Signal) error {
	if sig.SourceAgentID == "" {
		return errors.New("intelbus: source_agent_id required")
	}
	if sig.SignalType == "" {
		return errors.New("intelbus: signal_type required")
	}
	if sig.Confidence < 0 || sig.Confidence > 1 {
		return fmt.Errorf("intelbus: confidence %v out of [0,1]", sig.Confidence)
	}
	if strings.HasPrefix(channel, ChannelSignalsPrefix) {
		if !sig.Sentiment.Valid() || sig.Sentiment == "" {
			return fmt.Errorf("intelbus: signals channel requires bull/bear/neutral sentiment, got %q", sig.Sentiment)
		}
		// Channel suffix must match sentiment: intel:signals:{chain}:{sentiment}
		tail := strings.TrimPrefix(channel, ChannelSignalsPrefix)
		parts := strings.Split(tail, ":")
		if len(parts) != 2 || parts[0] == "" {
			return fmt.Errorf("intelbus: signals channel must be intel:signals:{chain}:{sentiment}, got %q", channel)
		}
		if parts[1] != string(sig.Sentiment) {
			return fmt.Errorf("intelbus: channel sentiment %q does not match signal sentiment %q", parts[1], sig.Sentiment)
		}
		return nil
	}
	if sig.Sentiment != "" && !sig.Sentiment.Valid() {
		return fmt.Errorf("intelbus: unknown sentiment %q", sig.Sentiment)
	}
	return nil
}
