package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/veermshah/cambrian/internal/redis"
)

func newWSServer(t *testing.T, auth bool) (*httptest.Server, *Server) {
	t.Helper()
	store := seedStore(t)
	srv, err := NewServer(ServerConfig{
		Store:            store,
		APIKey:           "test-key",
		AuthRequired:     auth,
		WebSocketEnabled: true,
		Subscribe:        nil, // we'll publish via PublishForTest instead of Redis
		HeartbeatInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.EnsureHubForTest()
	httpSrv := httptest.NewServer(srv.Engine())
	return httpSrv, srv
}

func wsURL(httpURL, path, apiKey string) string {
	u := "ws" + strings.TrimPrefix(httpURL, "http") + path
	if apiKey != "" {
		u += "?api_key=" + apiKey
	}
	return u
}

func TestWebsocket_DeliversBroadcast(t *testing.T) {
	httpSrv, srv := newWSServer(t, false)
	defer httpSrv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(httpSrv.URL, "/ws", ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Give the upgrade handler a moment to register with the hub.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if srv.hub.ClientCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if srv.hub.ClientCount() != 1 {
		t.Fatalf("hub clients = %d, want 1", srv.hub.ClientCount())
	}

	srv.PublishForTest(redis.Message{
		Channel: "events:circuit_breaker",
		Payload: []byte(`{"reason":"market_crash"}`),
	})

	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var env struct {
		Channel string          `json:"channel"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v (raw=%s)", err, msg)
	}
	if env.Channel != "events:circuit_breaker" {
		t.Errorf("channel = %q", env.Channel)
	}
	if !strings.Contains(string(env.Payload), "market_crash") {
		t.Errorf("payload = %s", env.Payload)
	}
}

func TestWebsocket_RawBytesPayloadWrappedAsJSONString(t *testing.T) {
	httpSrv, srv := newWSServer(t, false)
	defer httpSrv.Close()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(httpSrv.URL, "/ws", ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Wait for registration.
	for i := 0; i < 50 && srv.hub.ClientCount() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}

	srv.PublishForTest(redis.Message{Channel: "events:epoch_completed", Payload: []byte("epoch-42")})
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), `"epoch-42"`) {
		t.Errorf("raw payload not wrapped: %s", msg)
	}
}

func TestWebsocket_AuthRequiredRejects(t *testing.T) {
	httpSrv, _ := newWSServer(t, true)
	defer httpSrv.Close()
	// No key — dial should fail with 401.
	_, resp, err := websocket.DefaultDialer.Dial(wsURL(httpSrv.URL, "/ws", ""), nil)
	if err == nil {
		t.Fatal("expected dial to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %v, want 401", resp)
	}
}

func TestWebsocket_AuthQueryParamAccepts(t *testing.T) {
	httpSrv, _ := newWSServer(t, true)
	defer httpSrv.Close()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(httpSrv.URL, "/ws", "test-key"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
}

func TestWebsocket_DisabledHubReturns503(t *testing.T) {
	store := seedStore(t)
	srv, err := NewServer(ServerConfig{
		Store:            store,
		AuthRequired:     false,
		WebSocketEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	srv.Engine().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("disabled hub: status %d, want 503", rec.Code)
	}
}

func TestWebsocket_HubRunExitsOnContextCancel(t *testing.T) {
	hub := newWSHub(redis.NewFake(), []string{"events:circuit_breaker"}, 100*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		hub.run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("hub.run did not exit on context cancel")
	}
}

func TestBuildWSEvent_HandlesNilPayload(t *testing.T) {
	out := buildWSEvent(redis.Message{Channel: "events:noop", Payload: nil})
	if !strings.Contains(string(out), `"payload":null`) {
		t.Errorf("nil payload not encoded as null: %s", out)
	}
}
