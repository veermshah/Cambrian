package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/veermshah/cambrian/internal/redis"
)

// ServerConfig wires the API server. Store is required; APIKey is
// required when AuthRequired is true (production default). Subscribe is
// only required when WebSocketEnabled is true.
type ServerConfig struct {
	Store        Store
	Subscribe    redis.Client
	APIKey       string
	AuthRequired bool

	// WebSocketEnabled gates the /ws relay. Default true; tests turn it
	// off when they only exercise REST handlers.
	WebSocketEnabled bool

	// CORSOrigins is the list of origins allowed by the CORS middleware.
	// Production passes the dashboard URL; dev defaults to localhost:3000.
	CORSOrigins []string

	// Channels is the list of redis channels the websocket relay fans
	// out to clients. Defaults to "events:*".
	Channels []string

	// HeartbeatInterval is the websocket ping interval. Default 30s.
	HeartbeatInterval time.Duration

	Logger *zap.Logger
}

// Server holds the gin engine and the resources every handler shares.
type Server struct {
	cfg    ServerConfig
	engine *gin.Engine
	log    *zap.Logger
	hub    *wsHub
}

// NewServer validates config and returns a ready-to-serve Server.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Store == nil {
		return nil, errors.New("api: store required")
	}
	if cfg.AuthRequired && cfg.APIKey == "" {
		return nil, errors.New("api: api key required when auth enabled")
	}
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	if len(cfg.CORSOrigins) == 0 {
		cfg.CORSOrigins = []string{"http://localhost:3000"}
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if len(cfg.Channels) == 0 {
		cfg.Channels = []string{
			"events:circuit_breaker",
			"events:lifecycle",
			"events:epoch_completed",
			"events:budget",
			"events:intel",
			"events:trade",
		}
	}

	gin.SetMode(gin.ReleaseMode)
	eng := gin.New()
	eng.Use(gin.Recovery())

	s := &Server{cfg: cfg, engine: eng, log: cfg.Logger}
	s.use(loggingMiddleware(cfg.Logger))
	s.use(corsMiddleware(cfg.CORSOrigins))
	s.routes()
	return s, nil
}

// Engine returns the underlying *gin.Engine — used by tests that need
// to feed it to httptest, and by cmd/swarm to mount additional routes.
func (s *Server) Engine() *gin.Engine { return s.engine }

// Run starts the HTTP listener on addr and blocks until ctx is
// cancelled. If WebSocketEnabled is true it also starts the redis
// relay goroutine; both stop on shutdown. A bare HTTP server (no TLS)
// is intentional — the dashboard runs behind a reverse proxy that
// terminates TLS in production.
func (s *Server) Run(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.engine,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if s.cfg.WebSocketEnabled && s.cfg.Subscribe != nil {
		s.hub = newWSHub(s.cfg.Subscribe, s.cfg.Channels, s.cfg.HeartbeatInterval, s.log)
		go s.hub.run(ctx)
	}

	errCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Server) use(mw gin.HandlerFunc) { s.engine.Use(mw) }

// loggingMiddleware records one zap line per request. The body is small
// so we keep the default mode (no body logging) — it would be a leaky
// abstraction for the dashboard's auth header dance.
func loggingMiddleware(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Info("api.request",
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("dur", time.Since(start)),
		)
	}
}

// corsMiddleware allows the dashboard origin. Browser preflight (OPTIONS)
// short-circuits with 204 so the actual handler never runs.
func corsMiddleware(origins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[o] = struct{}{}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if _, ok := allowed[origin]; ok {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Headers", "Content-Type, X-Api-Key")
			c.Header("Access-Control-Allow-Methods", "GET, OPTIONS")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// authMiddleware enforces the X-Api-Key header. The websocket upgrade
// path uses the same check so an unauthenticated client can't subscribe
// to the event firehose. Returns 401 with the canonical JSON body.
func authMiddleware(key string) gin.HandlerFunc {
	return func(c *gin.Context) {
		got := c.GetHeader("X-Api-Key")
		if got == "" {
			// Allow the api key in a query param too — useful for the
			// browser-side websocket which can't set custom headers.
			got = c.Query("api_key")
		}
		if got != key {
			c.AbortWithStatusJSON(http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
			return
		}
		c.Next()
	}
}

func (s *Server) routes() {
	api := s.engine.Group("/api")
	if s.cfg.AuthRequired {
		api.Use(authMiddleware(s.cfg.APIKey))
	}

	api.GET("/agents", s.listAgents)
	api.GET("/agents/:id", s.getAgent)
	api.GET("/trades", s.listTrades)
	api.GET("/epochs", s.listEpochs)
	api.GET("/lineage", s.getLineage)
	api.GET("/treasury", s.getTreasury)
	api.GET("/postmortems", s.listPostmortems)
	api.GET("/offspring", s.listOffspring)
	api.GET("/budget", s.getBudget)
	api.GET("/circuit-breaker", s.getCircuitBreaker)
	api.GET("/backtests", s.listBacktests)
	api.GET("/intelligence", s.listIntel)
	api.GET("/models", s.listModels)
	api.GET("/evolution", s.listEvolution)
	api.GET("/dashboard", s.getDashboardSnapshot)

	// /healthz is unauthenticated so load balancers don't have to know
	// the API key — kept out of the /api group on purpose.
	s.engine.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	// /ws upgrades to a websocket. Auth runs at the same wall as the
	// REST endpoints so an unauthenticated client can't subscribe to
	// the event firehose.
	wsRoute := s.engine.Group("/ws")
	if s.cfg.AuthRequired {
		wsRoute.Use(authMiddleware(s.cfg.APIKey))
	}
	wsRoute.GET("", s.handleWebsocket)
}

// queryInt reads the named query param and clamps to [0, max].
// 0 means the handler picks its own default.
func queryInt(c *gin.Context, name string, max int) int {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	if n > max {
		return max
	}
	return n
}

// respondError centralizes the not-found / server-error branching so
// every handler renders the same JSON shape.
func respondError(c *gin.Context, err error) {
	if errors.Is(err, ErrNotFound) {
		c.JSON(http.StatusNotFound, ErrorResponse{Error: "not found"})
		return
	}
	c.JSON(http.StatusInternalServerError, ErrorResponse{Error: fmt.Sprintf("internal: %v", err)})
}
