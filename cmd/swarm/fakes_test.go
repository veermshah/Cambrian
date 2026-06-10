package main

import (
	"context"
	"time"

	"github.com/veermshah/cambrian/internal/llm"
	"github.com/veermshah/cambrian/internal/orchestrator"
	"github.com/veermshah/cambrian/internal/redis"
)

// noopStore is the trivial EpochStore for cron tests — empty state in,
// every Persist is a no-op. The orchestrator does run end-to-end with
// this, which is all the cron test cares about.
type noopStore struct{}

func (noopStore) LoadEpochState(_ context.Context, epochID string) (orchestrator.EpochState, error) {
	return orchestrator.EpochState{EpochID: epochID, StartedAt: time.Now()}, nil
}
func (noopStore) PersistLedger(context.Context, orchestrator.Ledger) error { return nil }
func (noopStore) PersistSweep(context.Context, orchestrator.SweepDecision) error { return nil }
func (noopStore) PersistOffspringDecision(context.Context, orchestrator.OffspringDecision) error {
	return nil
}
func (noopStore) PersistPostmortem(context.Context, orchestrator.PostmortemResult, string) error {
	return nil
}
func (noopStore) LogEpoch(context.Context, orchestrator.EpochResult) error { return nil }

type noopLifecycle struct{}

func (noopLifecycle) Kill(context.Context, string, string) error   { return nil }
func (noopLifecycle) Pause(context.Context, string, string) error  { return nil }
func (noopLifecycle) Resume(context.Context, string, string) error { return nil }

type noopBus struct{}

func (noopBus) Publish(context.Context, string, []byte) error { return nil }

type noopLLM struct{}

func (noopLLM) Complete(context.Context, string, string, int) (*llm.LLMResponse, error) {
	return &llm.LLMResponse{}, nil
}
func (noopLLM) CalculateCost(int, int) float64 { return 0 }

// fakeRedis is just enough redis.Client surface for the eventBusAdapter
// test — Publish records the last (channel, payload) pair.
type fakeRedis struct {
	lastChannel string
	lastPayload []byte
}

func newFakeRedis() *fakeRedis { return &fakeRedis{} }

func (f *fakeRedis) Publish(_ context.Context, channel string, payload any) error {
	f.lastChannel = channel
	if b, ok := payload.([]byte); ok {
		f.lastPayload = b
	}
	return nil
}
func (f *fakeRedis) Subscribe(context.Context, ...string) (<-chan redis.Message, error) {
	ch := make(chan redis.Message)
	close(ch)
	return ch, nil
}
func (*fakeRedis) Set(context.Context, string, string, time.Duration) error { return nil }
func (*fakeRedis) Get(context.Context, string) (string, bool, error)        { return "", false, nil }
func (*fakeRedis) Close() error                                              { return nil }
