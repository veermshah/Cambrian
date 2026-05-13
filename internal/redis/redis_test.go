package redis

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// All tests run against FakeRedis. A real-Upstash smoke test belongs in
// a build-tagged integration file (out of scope for chunk 8).

// FakeRedis must satisfy the same surface the real client exposes —
// this assignment fails to compile if the interfaces diverge.
var _ Client = (*FakeRedis)(nil)
var _ Client = (*Redis)(nil)

func TestFakePublishSubscribeJSONPayload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := NewFake()
	t.Cleanup(func() { _ = r.Close() })

	ch, err := r.Subscribe(ctx, "intel.market")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	payload := map[string]any{"signal": "bull", "score": 0.87}
	if err := r.Publish(ctx, "intel.market", payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case msg := <-ch:
		if msg.Channel != "intel.market" {
			t.Errorf("Channel = %q, want intel.market", msg.Channel)
		}
		var got map[string]any
		if err := json.Unmarshal(msg.Payload, &got); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if got["signal"] != "bull" {
			t.Errorf("signal = %v, want bull", got["signal"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestFakePublishRawBytesPassesThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := NewFake()
	t.Cleanup(func() { _ = r.Close() })
	ch, _ := r.Subscribe(ctx, "raw")

	// []byte and string values should not be re-JSON-encoded.
	if err := r.Publish(ctx, "raw", []byte("hello")); err != nil {
		t.Fatalf("Publish bytes: %v", err)
	}
	if err := r.Publish(ctx, "raw", "world"); err != nil {
		t.Fatalf("Publish string: %v", err)
	}

	for i, want := range []string{"hello", "world"} {
		select {
		case msg := <-ch:
			if string(msg.Payload) != want {
				t.Errorf("msg %d = %q, want %q", i, msg.Payload, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out on msg %d", i)
		}
	}
}

func TestFakeMultiSubscriberFanOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := NewFake()
	t.Cleanup(func() { _ = r.Close() })
	a, _ := r.Subscribe(ctx, "fan")
	b, _ := r.Subscribe(ctx, "fan")

	if err := r.Publish(ctx, "fan", "x"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	for i, ch := range []<-chan Message{a, b} {
		select {
		case m := <-ch:
			if string(m.Payload) != "x" {
				t.Errorf("subscriber %d: %q", i, m.Payload)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out", i)
		}
	}
}

func TestFakeSubscribeChannelClosesOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := NewFake()
	t.Cleanup(func() { _ = r.Close() })
	ch, err := r.Subscribe(ctx, "topic")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			// Drain — close may produce zero or one zero-value before EOF.
			select {
			case _, ok2 := <-ch:
				if ok2 {
					t.Error("channel still open after context cancel")
				}
			case <-time.After(time.Second):
				t.Error("channel did not close after context cancel")
			}
		}
	case <-time.After(time.Second):
		t.Error("channel did not close after context cancel")
	}
}

func TestFakeKeyValueGetSet(t *testing.T) {
	ctx := context.Background()
	r := NewFake()
	t.Cleanup(func() { _ = r.Close() })

	if _, ok, err := r.Get(ctx, "missing"); err != nil || ok {
		t.Errorf("Get missing: ok=%v err=%v", ok, err)
	}

	if err := r.Set(ctx, "k", "v", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := r.Get(ctx, "k")
	if err != nil || !ok || got != "v" {
		t.Errorf("Get k = (%q, %v, %v), want (v, true, nil)", got, ok, err)
	}
}

func TestFakeKeyValueTTLExpires(t *testing.T) {
	ctx := context.Background()
	r := NewFake()
	t.Cleanup(func() { _ = r.Close() })

	if err := r.Set(ctx, "k", "v", 20*time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, ok, _ := r.Get(ctx, "k"); !ok {
		t.Fatal("key should be present immediately after Set")
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok, _ := r.Get(ctx, "k"); ok {
		t.Error("key should have expired after TTL")
	}
}

func TestFakeSubscribeRejectsNoChannels(t *testing.T) {
	r := NewFake()
	t.Cleanup(func() { _ = r.Close() })
	if _, err := r.Subscribe(context.Background()); err == nil {
		t.Error("Subscribe() with no channels: want error")
	}
}

func TestFakeOperationsAfterCloseFail(t *testing.T) {
	r := NewFake()
	_ = r.Close()
	if err := r.Publish(context.Background(), "x", "y"); err == nil {
		t.Error("Publish after Close: want error")
	}
	if err := r.Set(context.Background(), "k", "v", 0); err == nil {
		t.Error("Set after Close: want error")
	}
}

func TestNewRedisRejectsEmptyURL(t *testing.T) {
	if _, err := New(context.Background(), ""); err == nil {
		t.Fatal("New(\"\"): want error")
	}
}

func TestNewRedisRejectsBadURL(t *testing.T) {
	if _, err := New(context.Background(), "not-a-url"); err == nil {
		t.Fatal("New(bad): want error")
	}
}
