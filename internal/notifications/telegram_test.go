package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/redis"
)

type sentMessage struct {
	ChatID string
	Text   string
}

func newRecorder(t *testing.T) (*httptest.Server, *[]sentMessage, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var seen []sentMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			http.Error(w, "wrong path", 404)
			return
		}
		_ = r.ParseForm()
		raw, _ := io.ReadAll(r.Body)
		_ = raw
		mu.Lock()
		seen = append(seen, sentMessage{
			ChatID: r.PostFormValue("chat_id"),
			Text:   r.PostFormValue("text"),
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	return srv, &seen, &mu
}

func newNotifier(t *testing.T, srv *httptest.Server, now func() time.Time) *TelegramNotifier {
	t.Helper()
	n, err := NewTelegramNotifier(TelegramConfig{
		BotToken:  "test-token",
		ChatID:    "test-chat",
		BaseURL:   srv.URL,
		Subscribe: redis.NewFake(),
		Now:       now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNotifier_SendMessageHitsTelegram(t *testing.T) {
	srv, seen, mu := newRecorder(t)
	defer srv.Close()
	now := func() time.Time { return time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC) }
	n := newNotifier(t, srv, now)
	if err := n.SendMessage(context.Background(), "hello *world*"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*seen) != 1 {
		t.Fatalf("seen = %d, want 1", len(*seen))
	}
	if (*seen)[0].Text != "hello *world*" {
		t.Errorf("text = %q", (*seen)[0].Text)
	}
	if (*seen)[0].ChatID != "test-chat" {
		t.Errorf("chat_id = %q", (*seen)[0].ChatID)
	}
}

func TestNotifier_SendMessageReportsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":false,"description":"bad token"}`))
	}))
	defer srv.Close()
	n, err := NewTelegramNotifier(TelegramConfig{
		BotToken: "x", ChatID: "y", BaseURL: srv.URL, Subscribe: redis.NewFake(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SendMessage(context.Background(), "hi"); err == nil {
		t.Error("expected API error to surface")
	}
}

func TestNotifier_SendMessageReportsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", 403)
	}))
	defer srv.Close()
	n, err := NewTelegramNotifier(TelegramConfig{
		BotToken: "x", ChatID: "y", BaseURL: srv.URL, Subscribe: redis.NewFake(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SendMessage(context.Background(), "hi"); err == nil {
		t.Error("expected HTTP error to surface")
	}
}

func TestNotifier_HandleEventAppliesThrottle(t *testing.T) {
	srv, seen, mu := newRecorder(t)
	defer srv.Close()
	off := &atomic.Int64{}
	start := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return start.Add(time.Duration(off.Load())) }
	n := newNotifier(t, srv, now)
	// 30 same-type events at the same instant — only one should send.
	for i := 0; i < 30; i++ {
		n.HandleEventForTest(context.Background(), redis.Message{
			Channel: "events:circuit_breaker",
			Payload: []byte(`{"reason":"market_crash"}`),
		})
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*seen) != 1 {
		t.Errorf("seen = %d, want 1 (cooldown should suppress the rest)", len(*seen))
	}
}

func TestNotifier_ConstructorRejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		cfg  TelegramConfig
	}{
		{"missing token", TelegramConfig{ChatID: "x", Subscribe: redis.NewFake()}},
		{"missing chat", TelegramConfig{BotToken: "x", Subscribe: redis.NewFake()}},
		{"missing subscribe", TelegramConfig{BotToken: "x", ChatID: "y"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewTelegramNotifier(tc.cfg); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestNotifier_LifecycleChannelInfersKilledOrSpawned(t *testing.T) {
	srv, seen, mu := newRecorder(t)
	defer srv.Close()
	now := func() time.Time { return time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC) }
	n := newNotifier(t, srv, now)

	killedPayload, _ := json.Marshal(map[string]any{
		"action":   "killed",
		"agent_id": "ag-1",
		"reason":   "max_drawdown_exceeded",
	})
	spawnedPayload, _ := json.Marshal(map[string]any{
		"action":   "spawned",
		"agent_id": "ag-2",
	})
	n.HandleEventForTest(context.Background(), redis.Message{Channel: "events:lifecycle", Payload: killedPayload})
	n.HandleEventForTest(context.Background(), redis.Message{Channel: "events:lifecycle", Payload: spawnedPayload})

	mu.Lock()
	defer mu.Unlock()
	if len(*seen) != 2 {
		t.Fatalf("seen = %d, want 2 (distinct types ⇒ cooldown doesn't block)", len(*seen))
	}
	if !strings.Contains((*seen)[0].Text, "killed") {
		t.Errorf("first message should be killed: %s", (*seen)[0].Text)
	}
	if !strings.Contains((*seen)[1].Text, "spawned") {
		t.Errorf("second message should be spawned: %s", (*seen)[1].Text)
	}
}

func TestNotifier_DailyDigestFiresOnceAtHour(t *testing.T) {
	srv, seen, mu := newRecorder(t)
	defer srv.Close()
	off := &atomic.Int64{}
	start := time.Date(2026, 6, 9, 8, 59, 0, 0, time.UTC)
	now := func() time.Time { return start.Add(time.Duration(off.Load())) }

	provider := func(_ context.Context, at time.Time) (DigestSummary, error) {
		return DigestSummary{
			Date:        at.UTC().Format("2006-01-02"),
			TotalPnLUSD: 100,
			BestAgent:   AgentScore{AgentID: "x", Name: "w", PnLUSD: 100},
			WorstAgent:  AgentScore{AgentID: "y", Name: "l", PnLUSD: -10},
		}, nil
	}
	n, err := NewTelegramNotifier(TelegramConfig{
		BotToken: "t", ChatID: "c", BaseURL: srv.URL,
		Subscribe:      redis.NewFake(),
		Now:            now,
		DigestHour:     9,
		DigestProvider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Before 09:00 → no digest.
	n.maybeFireDigest(context.Background())
	if got := snapshot(seen, mu); len(got) != 0 {
		t.Fatalf("digest fired early: %v", got)
	}
	// Advance to 09:00 → digest fires.
	off.Store(int64(time.Minute))
	n.maybeFireDigest(context.Background())
	if got := snapshot(seen, mu); len(got) != 1 {
		t.Fatalf("digest at 09:00 = %d, want 1", len(got))
	}
	// Calling again same day → no duplicate.
	n.maybeFireDigest(context.Background())
	if got := snapshot(seen, mu); len(got) != 1 {
		t.Errorf("digest re-fired same day: %d", len(got))
	}
	// Next day at 09:00 → fires again (start was 08:59 so +24h+1min lands on day 2 09:00).
	off.Store(int64(24*time.Hour + time.Minute))
	n.maybeFireDigest(context.Background())
	if got := snapshot(seen, mu); len(got) != 2 {
		t.Errorf("next-day digest = %d, want 2", len(got))
	}
}

func snapshot(seen *[]sentMessage, mu *sync.Mutex) []sentMessage {
	mu.Lock()
	defer mu.Unlock()
	out := make([]sentMessage, len(*seen))
	copy(out, *seen)
	return out
}

func TestNotifier_RunDeliversRedisEvents(t *testing.T) {
	srv, seen, mu := newRecorder(t)
	defer srv.Close()
	r := redis.NewFake()
	now := func() time.Time { return time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC) }
	n, err := NewTelegramNotifier(TelegramConfig{
		BotToken: "t", ChatID: "c", BaseURL: srv.URL,
		Subscribe: r, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan struct{})
	go func() {
		_ = n.Run(ctx)
		close(doneCh)
	}()
	// Give the subscribe a moment to register.
	time.Sleep(20 * time.Millisecond)
	if err := r.Publish(context.Background(), "events:circuit_breaker", []byte(`{"reason":"market_crash"}`)); err != nil {
		t.Fatal(err)
	}
	// Wait for delivery.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(*seen)
		mu.Unlock()
		if got > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-doneCh
	mu.Lock()
	defer mu.Unlock()
	if len(*seen) != 1 {
		t.Errorf("seen = %d, want 1", len(*seen))
	} else if !strings.Contains((*seen)[0].Text, "market_crash") {
		t.Errorf("missing payload: %s", (*seen)[0].Text)
	}
}

// TestNotifier_LiveIntegration is gated by INTEGRATION=1 and requires
// real TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID env vars. Skipped in
// normal CI; flip the env var to verify end-to-end against a real chat.
func TestNotifier_LiveIntegration(t *testing.T) {
	if os.Getenv("INTEGRATION") != "1" {
		t.Skip("set INTEGRATION=1 to run the live Telegram test")
	}
	if os.Getenv("TELEGRAM_BOT_TOKEN") == "" || os.Getenv("TELEGRAM_CHAT_ID") == "" {
		t.Skip("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID required")
	}
	n, err := NewTelegramNotifier(TelegramConfig{LoadEnv: true, Subscribe: redis.NewFake()})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SendMessage(context.Background(), "Cambrian chunk-28 integration check"); err != nil {
		t.Fatal(err)
	}
}
