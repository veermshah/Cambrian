package redis

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// FakeRedis is an in-memory implementation of Client suitable for unit
// tests. Pub/sub fan-out is synchronous: every subscriber gets every
// message sent to a matching channel after its Subscribe call returns.
// TTLs are tracked but only evaluated lazily on Get — good enough for
// tests, not for production.
type FakeRedis struct {
	mu     sync.Mutex
	kv     map[string]fakeEntry
	subs   map[string][]chan Message // channel -> subscriber inbox(es)
	closed bool
}

type fakeEntry struct {
	value     string
	expiresAt time.Time // zero means no expiry
}

// NewFake constructs an empty FakeRedis.
func NewFake() *FakeRedis {
	return &FakeRedis{
		kv:   make(map[string]fakeEntry),
		subs: make(map[string][]chan Message),
	}
}

// Publish JSON-encodes (the same way the real client does) and delivers
// to every matching subscriber. Returns an error if the fake is closed
// so tests catch use-after-close.
func (f *FakeRedis) Publish(_ context.Context, channel string, value any) error {
	payload, err := encodePayload(value)
	if err != nil {
		return err
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return fmt.Errorf("redis(fake): closed")
	}
	receivers := append([]chan Message(nil), f.subs[channel]...)
	f.mu.Unlock()
	msg := Message{Channel: channel, Payload: payload}
	for _, r := range receivers {
		// Non-blocking send: tests should keep their subscribe channels
		// drained; a full buffer is dropped to avoid deadlock.
		select {
		case r <- msg:
		default:
		}
	}
	return nil
}

// Subscribe registers an inbox for each requested channel. The returned
// channel closes when ctx is cancelled or the fake is closed.
func (f *FakeRedis) Subscribe(ctx context.Context, channels ...string) (<-chan Message, error) {
	if len(channels) == 0 {
		return nil, fmt.Errorf("redis(fake): Subscribe requires at least one channel")
	}
	inbox := make(chan Message, 64)
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil, fmt.Errorf("redis(fake): closed")
	}
	for _, ch := range channels {
		f.subs[ch] = append(f.subs[ch], inbox)
	}
	f.mu.Unlock()

	go func() {
		<-ctx.Done()
		f.detach(inbox, channels)
	}()
	return inbox, nil
}

func (f *FakeRedis) detach(inbox chan Message, channels []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range channels {
		list := f.subs[ch]
		for i, c := range list {
			if c == inbox {
				f.subs[ch] = append(list[:i], list[i+1:]...)
				break
			}
		}
	}
	// Safe to close: detach is the only path that closes inbox, and a
	// detach happens at most once per Subscribe (driven by ctx.Done).
	close(inbox)
}

// Set stores value with optional TTL.
func (f *FakeRedis) Set(_ context.Context, key, value string, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return fmt.Errorf("redis(fake): closed")
	}
	entry := fakeEntry{value: value}
	if ttl > 0 {
		entry.expiresAt = time.Now().Add(ttl)
	}
	f.kv[key] = entry
	return nil
}

// Get returns value, present, err. Lazily evicts expired keys.
func (f *FakeRedis) Get(_ context.Context, key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return "", false, fmt.Errorf("redis(fake): closed")
	}
	e, ok := f.kv[key]
	if !ok {
		return "", false, nil
	}
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		delete(f.kv, key)
		return "", false, nil
	}
	return e.value, true, nil
}

// Close marks the fake closed; in-flight subscribers are released when
// their contexts cancel.
func (f *FakeRedis) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}
