package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/db"
	"github.com/veermshah/cambrian/internal/redis"
)

// busRecorder is a minimal redis.Client that just records publishes.
type busRecorder struct {
	mu     sync.Mutex
	events []recorded
}

type recorded struct {
	channel string
	payload []byte
}

func (b *busRecorder) Publish(_ context.Context, channel string, value any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var payload []byte
	switch v := value.(type) {
	case []byte:
		payload = append(payload, v...)
	case string:
		payload = []byte(v)
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		payload = raw
	}
	b.events = append(b.events, recorded{channel: channel, payload: payload})
	return nil
}

func (b *busRecorder) Subscribe(_ context.Context, _ ...string) (<-chan redis.Message, error) {
	ch := make(chan redis.Message)
	return ch, nil
}

func (b *busRecorder) Set(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}
func (b *busRecorder) Get(_ context.Context, _ string) (string, bool, error) {
	return "", false, nil
}
func (b *busRecorder) Close() error { return nil }

func (b *busRecorder) eventsFor(channel string) []recorded {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []recorded
	for _, e := range b.events {
		if e.channel == channel {
			out = append(out, e)
		}
	}
	return out
}

func TestLifecycle_SpawnInsertsAndPublishes(t *testing.T) {
	store := newFakeStore()
	bus := &busRecorder{}
	lm := NewLifecycleManager(store, bus, nil)

	row := db.Agent{
		Name:               "child-1",
		Chain:              "solana",
		WalletAddress:      "WALLET",
		WalletKeyEncrypted: []byte{1},
		TaskType:           "cross_chain_yield",
		NodeClass:          "shadow",
	}
	id, err := lm.Spawn(context.Background(), row, "test spawn")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if id == "" {
		t.Fatal("Spawn returned empty id")
	}
	if got := store.status(id); got != "active" {
		t.Errorf("status = %q, want active", got)
	}
	evs := bus.eventsFor(ChannelLifecycleSpawn)
	if len(evs) != 1 {
		t.Fatalf("spawn events = %d, want 1", len(evs))
	}
	var ev LifecycleEvent
	if err := json.Unmarshal(evs[0].payload, &ev); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if ev.AgentID != id || ev.Reason != "test spawn" || ev.NodeClass != "shadow" {
		t.Errorf("event = %+v", ev)
	}
}

func TestLifecycle_PauseResumeKill(t *testing.T) {
	store := newFakeStore()
	bus := &busRecorder{}
	lm := NewLifecycleManager(store, bus, nil)
	ctx := context.Background()

	id, _ := lm.Spawn(ctx, db.Agent{
		Name: "a", WalletAddress: "W", WalletKeyEncrypted: []byte{1}, TaskType: "fake",
	}, "init")

	if err := lm.Pause(ctx, id, "drawdown"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if got := store.status(id); got != "paused" {
		t.Errorf("after Pause status = %q", got)
	}
	if got := store.reason(id); got != "drawdown" {
		t.Errorf("after Pause reason = %q", got)
	}

	if err := lm.Resume(ctx, id, "recovered"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := store.status(id); got != "active" {
		t.Errorf("after Resume status = %q", got)
	}

	if err := lm.Kill(ctx, id, "insolvent"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if got := store.status(id); got != "dead" {
		t.Errorf("after Kill status = %q", got)
	}
	if got := store.reason(id); got != "insolvent" {
		t.Errorf("after Kill reason = %q", got)
	}

	for _, ch := range []string{
		ChannelLifecyclePause, ChannelLifecycleResume, ChannelLifecycleKill,
	} {
		if len(bus.eventsFor(ch)) != 1 {
			t.Errorf("expected exactly 1 event on %s", ch)
		}
	}
}

func TestLifecycle_PromoteDemote(t *testing.T) {
	store := newFakeStore()
	bus := &busRecorder{}
	lm := NewLifecycleManager(store, bus, nil)
	ctx := context.Background()

	id, _ := lm.Spawn(ctx, db.Agent{
		Name: "p", WalletAddress: "W", WalletKeyEncrypted: []byte{1}, TaskType: "fake",
		NodeClass: "shadow",
	}, "init")
	if err := lm.Promote(ctx, id, "graduated"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if got := store.class(id); got != "funded" {
		t.Errorf("after Promote class = %q", got)
	}
	if err := lm.Demote(ctx, id, "slipped"); err != nil {
		t.Fatalf("Demote: %v", err)
	}
	if got := store.class(id); got != "shadow" {
		t.Errorf("after Demote class = %q", got)
	}
}

func TestLifecycle_RequiresAgentID(t *testing.T) {
	lm := NewLifecycleManager(newFakeStore(), nil, nil)
	if err := lm.Pause(context.Background(), "", "x"); err == nil {
		t.Error("expected error for empty id")
	}
	if err := lm.Kill(context.Background(), "", "x"); err == nil {
		t.Error("expected error for empty id")
	}
}

func TestLifecycle_NilBusIsSafe(t *testing.T) {
	store := newFakeStore()
	lm := NewLifecycleManager(store, nil, nil)
	if _, err := lm.Spawn(context.Background(), db.Agent{
		Name: "a", WalletAddress: "W", WalletKeyEncrypted: []byte{1}, TaskType: "fake",
	}, ""); err != nil {
		t.Fatalf("Spawn with nil bus: %v", err)
	}
}
