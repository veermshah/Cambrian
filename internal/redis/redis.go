// Package redis is the orchestrator's thin wrapper around go-redis/v9.
// Used for the intel pub/sub bus (chunk 22) and transient state caching.
//
// Production Redis is hosted on Upstash (TLS endpoint, rediss://...).
// The free tier caps at 10,000 commands/day, so consumers should use
// Subscribe (one persistent connection per agent) rather than polling
// in a Get loop. Use the FakeRedis in tests.
package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Client is the surface every caller in this codebase uses. Both the
// real *Redis and *FakeRedis satisfy it so tests can swap in-process.
type Client interface {
	Publish(ctx context.Context, channel string, value any) error
	Subscribe(ctx context.Context, channels ...string) (<-chan Message, error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	Get(ctx context.Context, key string) (string, bool, error)
	Close() error
}

// Message is the decoded shape delivered on the Subscribe channel.
// Payload is the raw JSON bytes — the caller unmarshals into the type
// it expects.
type Message struct {
	Channel string
	Payload []byte
}

// Redis is the production wrapper around go-redis/v9.
type Redis struct {
	c *goredis.Client
}

// New connects to the URL (typically REDIS_URL). redis.ParseURL handles
// the rediss:// scheme used by Upstash — it sets up TLS automatically,
// no extra options needed. The constructor pings once so a wrong URL
// fails loudly at startup rather than on first Publish.
func New(ctx context.Context, url string) (*Redis, error) {
	if url == "" {
		return nil, fmt.Errorf("redis: empty URL")
	}
	opt, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redis: parse URL: %w", err)
	}
	// Bounded retry on transient connection failures. Each retry waits
	// 100ms * 2^attempt up to the cap, which keeps total startup wait
	// under ~2s for the default three retries.
	opt.MaxRetries = 3
	opt.MinRetryBackoff = 100 * time.Millisecond
	opt.MaxRetryBackoff = 1 * time.Second

	c := goredis.NewClient(opt)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("redis: ping: %w", err)
	}
	return &Redis{c: c}, nil
}

// Publish JSON-encodes value and PUBLISHes it on channel. Bytes-typed
// values are sent verbatim so callers that have already serialized
// don't pay the encode twice.
func (r *Redis) Publish(ctx context.Context, channel string, value any) error {
	payload, err := encodePayload(value)
	if err != nil {
		return err
	}
	return r.c.Publish(ctx, channel, payload).Err()
}

// Subscribe returns a buffered channel of incoming Messages. The
// returned channel closes when ctx is cancelled or the underlying
// PubSub is closed. One persistent connection per Subscribe — that's
// the cost-friendly path on Upstash.
func (r *Redis) Subscribe(ctx context.Context, channels ...string) (<-chan Message, error) {
	if len(channels) == 0 {
		return nil, fmt.Errorf("redis: Subscribe requires at least one channel")
	}
	ps := r.c.Subscribe(ctx, channels...)
	if _, err := ps.Receive(ctx); err != nil {
		_ = ps.Close()
		return nil, fmt.Errorf("redis: subscribe handshake: %w", err)
	}
	out := make(chan Message, 64)
	go func() {
		defer close(out)
		defer ps.Close()
		ch := ps.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- Message{Channel: m.Channel, Payload: []byte(m.Payload)}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// Set stores key=value with optional TTL (zero means no expiry).
func (r *Redis) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return r.c.Set(ctx, key, value, ttl).Err()
}

// Get fetches key. The second return value reports whether the key was
// present — a miss returns ("", false, nil), distinct from a real error.
func (r *Redis) Get(ctx context.Context, key string) (string, bool, error) {
	v, err := r.c.Get(ctx, key).Result()
	if err == goredis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// Close releases the underlying connection pool.
func (r *Redis) Close() error { return r.c.Close() }

// encodePayload turns value into the bytes we hand to PUBLISH. []byte
// and string pass through; everything else goes through encoding/json.
func encodePayload(value any) ([]byte, error) {
	switch v := value.(type) {
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("redis: marshal payload: %w", err)
		}
		return b, nil
	}
}
