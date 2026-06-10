package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/veermshah/cambrian/internal/redis"
)

// wsHub fans Redis events out to every connected websocket client. One
// goroutine receives from the Redis subscribe channel; each client has
// its own goroutine that pushes to the socket. Slow clients are dropped
// rather than backpressuring the publisher — the dashboard can reconnect.
type wsHub struct {
	mu        sync.RWMutex
	clients   map[*wsClient]struct{}
	subscribe redis.Client
	channels  []string
	heartbeat time.Duration
	log       *zap.Logger
}

type wsClient struct {
	send chan []byte
	done chan struct{}
}

func newWSHub(sub redis.Client, channels []string, heartbeat time.Duration, log *zap.Logger) *wsHub {
	if log == nil {
		log = zap.NewNop()
	}
	return &wsHub{
		clients:   make(map[*wsClient]struct{}),
		subscribe: sub,
		channels:  channels,
		heartbeat: heartbeat,
		log:       log,
	}
}

// run subscribes to the configured channels and broadcasts every message
// to all connected clients. Exits when ctx is cancelled.
func (h *wsHub) run(ctx context.Context) {
	if h.subscribe == nil {
		return
	}
	inbox, err := h.subscribe.Subscribe(ctx, h.channels...)
	if err != nil {
		h.log.Error("api.ws.subscribe", zap.Error(err))
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-inbox:
			if !ok {
				return
			}
			h.broadcast(buildWSEvent(msg))
		}
	}
}

// buildWSEvent shapes the bytes the client sees. We always wrap the
// payload in {channel, payload, at} so the dashboard's WebSocket
// dispatcher doesn't have to sniff JSON shape per channel.
func buildWSEvent(msg redis.Message) []byte {
	env := struct {
		Channel string          `json:"channel"`
		Payload json.RawMessage `json:"payload"`
		At      time.Time       `json:"at"`
	}{
		Channel: msg.Channel,
		Payload: payloadAsJSON(msg.Payload),
		At:      time.Now().UTC(),
	}
	out, _ := json.Marshal(env)
	return out
}

// payloadAsJSON makes the WebSocket payload tolerant of producers that
// publish raw bytes instead of JSON (chunk 21 publishes epoch IDs as
// raw strings). If the bytes don't parse as JSON, we wrap them in a
// quoted JSON string so the client always receives valid JSON.
func payloadAsJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return []byte("null")
	}
	if json.Valid(b) {
		return b
	}
	quoted, _ := json.Marshal(string(b))
	return quoted
}

func (h *wsHub) broadcast(msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.send <- msg:
		default:
			// Slow client — drop and let it reconnect.
			close(c.done)
		}
	}
}

func (h *wsHub) register(c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
}

func (h *wsHub) unregister(c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
}

// ClientCount is exposed for tests so they can assert connection state.
func (h *wsHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// upgrader allows any origin — the auth middleware already validated
// the request, and origin-check is the wrong abstraction for an
// API-key-protected endpoint. The dashboard is the only intended
// client.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// handleWebsocket promotes the request to a websocket and joins the
// hub. The read loop is a discard loop — clients are listen-only — so
// we can detect a close cheaply.
func (s *Server) handleWebsocket(c *gin.Context) {
	if s.hub == nil {
		c.JSON(http.StatusServiceUnavailable, ErrorResponse{Error: "websocket disabled"})
		return
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	client := &wsClient{
		send: make(chan []byte, 32),
		done: make(chan struct{}),
	}
	s.hub.register(client)
	defer s.hub.unregister(client)

	// Drain client → server frames so close is detected promptly.
	go func() {
		defer func() {
			select {
			case <-client.done:
			default:
				close(client.done)
			}
		}()
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(s.hub.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-client.done:
			return
		case msg := <-client.send:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// PublishForTest pushes a synthetic event onto the hub without going
// through Redis. Tests use this to verify fan-out.
func (s *Server) PublishForTest(msg redis.Message) {
	if s.hub == nil {
		return
	}
	s.hub.broadcast(buildWSEvent(msg))
}

// EnsureHubForTest constructs the hub even when no Redis is wired so
// tests can call PublishForTest before Run() starts.
func (s *Server) EnsureHubForTest() {
	if s.hub == nil {
		s.hub = newWSHub(nil, s.cfg.Channels, s.cfg.HeartbeatInterval, s.log)
	}
}
