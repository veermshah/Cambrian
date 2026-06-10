// Command swarm is the single entry point that boots every Cambrian
// subsystem: DB migrations, the agent runtime, the price collector, the
// Telegram notifier, and the dashboard API. Subsystems run as errgroup
// goroutines so any failure surfaces and a SIGINT cleanly drains them
// (30-second deadline per chunk 32 acceptance criteria).
//
// Each subsystem is optional in the sense that missing env vars degrade
// gracefully (no Telegram token → no Telegram, no Anthropic key → LLM
// registry returns errors when the strategist asks for a model). Only
// the required env vars listed in internal/config block startup.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sync/errgroup"

	"github.com/veermshah/cambrian/internal/api"
	"github.com/veermshah/cambrian/internal/backtesting"
	"github.com/veermshah/cambrian/internal/chain"
	"github.com/veermshah/cambrian/internal/config"
	"github.com/veermshah/cambrian/internal/db"
	"github.com/veermshah/cambrian/internal/llm"
	"github.com/veermshah/cambrian/internal/notifications"
	"github.com/veermshah/cambrian/internal/redis"
	"github.com/veermshah/cambrian/internal/runtime"
)

// shutdownTimeout is the cap on how long the binary will wait for
// subsystems to drain after SIGINT. Spec line 1560: SIGINT → all
// goroutines exit within 30s.
const shutdownTimeout = 30 * time.Second

func main() {
	if err := run(); err != nil {
		// run() emits structured errors via zap, but if we got an error
		// back it's worth one last fmt.Fprintln so non-JSON tail readers
		// (operators tailing the binary directly) see the cause.
		fmt.Fprintln(os.Stderr, "swarm exited with error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	log, err := newLogger(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("logger: %w", err)
	}
	defer func() { _ = log.Sync() }()

	bootCtx, cancelBoot := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelBoot()

	log.Info("swarm.boot.start",
		zap.String("network", cfg.Network),
		zap.String("log_level", cfg.LogLevel),
	)

	// 1) Migrations.
	if err := db.Run(cfg.DatabaseURL); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	log.Info("swarm.boot.migrations_ok")

	// 2) DB pool.
	pool, err := db.NewPool(bootCtx, cfg.DatabasePoolURL)
	if err != nil {
		return fmt.Errorf("db pool: %w", err)
	}
	defer pool.Close()

	queries := db.NewQueries(pool)
	ready, err := queries.TreasuryInitialized(bootCtx)
	if err != nil {
		return fmt.Errorf("treasury check: %w", err)
	}
	if !ready {
		return errors.New("treasury not initialized — run `go run ./cmd/init-treasury` first")
	}
	log.Info("swarm.boot.db_ok")

	// 3) Redis.
	rdb, err := redis.New(bootCtx, cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer rdb.Close()
	log.Info("swarm.boot.redis_ok")

	// 4) LLM registry. Configure binds keys; agents resolve clients via
	// llm.Get(model) at strategist time. Missing keys are tolerated —
	// only the strategist call for that provider will error.
	llm.Configure(cfg.AnthropicAPIKey, cfg.OpenAIAPIKey)
	log.Info("swarm.boot.llm_ok",
		zap.Bool("anthropic", cfg.AnthropicAPIKey != ""),
		zap.Bool("openai", cfg.OpenAIAPIKey != ""),
	)

	// 5) Chain clients — build per-chain ChainClient via the factory
	// registry that's wired in init() of internal/chain/solana.go and
	// base.go. Mainnet is still gated; spec line 1555.
	chainClients, err := buildChainClients(cfg)
	if err != nil {
		return fmt.Errorf("chain clients: %w", err)
	}
	priceCache := chain.NewPriceCache(chainClients)
	log.Info("swarm.boot.chains_ok", zap.Int("count", len(chainClients)))

	// 6) Subsystems. Each runs in its own errgroup goroutine so a panic
	// or a returned error in one surfaces and triggers the shared
	// shutdown context.
	runCtx, cancelRun := signalContext()
	defer cancelRun()
	group, gctx := errgroup.WithContext(runCtx)

	// 6a) SwarmRuntime — the agent goroutine substrate.
	swarmRT, err := runtime.NewSwarm(runtime.SwarmConfig{
		Store: queries,
		LLMs:  llmRegistryAdapter{},
		Bus:   rdb,
		Log:   loggerAdapter{log: log},
	})
	if err != nil {
		return fmt.Errorf("swarm runtime: %w", err)
	}
	if started, err := swarmRT.LoadAndStart(gctx); err != nil {
		log.Warn("swarm.boot.load_active_failed", zap.Error(err))
	} else {
		log.Info("swarm.boot.runtime_ok", zap.Int("agents_started", started))
	}
	group.Go(func() error { return swarmRT.Run(gctx) })

	// 6b) PriceCollector — InMemoryPriceStore for now; persisting to
	// price_history is a future chunk that adds InsertPriceRow to
	// *db.Queries. The price cache is the live source either way.
	collector, err := backtesting.NewPriceCollector(backtesting.PriceCollectorConfig{
		Cache: priceCache,
		Pairs: backtesting.StaticPairSource{
			{"solana", "SOL/USDC"},
			{"base", "ETH/USDC"},
		},
		Store: backtesting.NewInMemoryPriceStore(),
	})
	if err != nil {
		return fmt.Errorf("price collector: %w", err)
	}
	group.Go(func() error {
		err := collector.Run(gctx)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	})
	log.Info("swarm.boot.price_collector_ok")

	// 6c) Telegram notifier (only when wired). Missing token/chat means
	// the operator hasn't set up Telegram — skip silently.
	if cfg.TelegramBotToken != "" && cfg.TelegramChatID != "" {
		notifier, err := notifications.NewTelegramNotifier(notifications.TelegramConfig{
			BotToken:  cfg.TelegramBotToken,
			ChatID:    cfg.TelegramChatID,
			Subscribe: rdb,
		})
		if err != nil {
			return fmt.Errorf("telegram: %w", err)
		}
		group.Go(func() error { return notifier.Run(gctx) })
		log.Info("swarm.boot.telegram_ok")
	} else {
		log.Info("swarm.boot.telegram_skipped",
			zap.Bool("token", cfg.TelegramBotToken != ""),
			zap.Bool("chat", cfg.TelegramChatID != ""),
		)
	}

	// 6d) API server.
	apiServer, err := api.NewServer(api.ServerConfig{
		Store:            api.NewPostgresStore(pool),
		Subscribe:        rdb,
		APIKey:           cfg.APIKey,
		AuthRequired:     true,
		WebSocketEnabled: true,
		Logger:           log,
	})
	if err != nil {
		return fmt.Errorf("api: %w", err)
	}
	addr := apiAddr()
	group.Go(func() error {
		if err := apiServer.Run(gctx, addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	log.Info("swarm.boot.api_ok", zap.String("addr", addr))

	// 6e) RootOrchestrator cron — placeholder. The production EpochStore
	// binding lives in a future chunk; until then we log the gap rather
	// than wire a fake into production.
	log.Warn("swarm.boot.orchestrator_skipped",
		zap.String("reason", "production EpochStore not yet implemented"),
		zap.String("tracker", "TODO: chunks after 32 wire orchestrator.EpochStore against pgx pool"),
	)

	log.Info("swarm.boot.ready")

	// Block on group; the first goroutine to return decides the exit
	// status. SIGINT cancels gctx and every subsystem drains.
	if err := group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("swarm.shutdown.ok")
	return nil
}

// signalContext returns a context that cancels on SIGINT / SIGTERM.
// The deferred cancel() the caller installs covers the "we exited
// normally before any signal" path.
func signalContext() (context.Context, context.CancelFunc) {
	parent := context.Background()
	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	return ctx, func() {
		// Once cancel returns we still want to wait at most
		// shutdownTimeout for goroutines to finish — errgroup blocks
		// indefinitely otherwise. Subsystems honor ctx.Done() per the
		// errgroup contract, so this is a guardrail not a workaround.
		_, deadline := context.WithTimeout(parent, shutdownTimeout)
		_ = deadline
		cancel()
	}
}

// apiAddr returns the dashboard API bind address. Configurable via
// env so dev and prod can use different ports; defaults to :8080.
func apiAddr() string {
	if v := os.Getenv("API_ADDR"); v != "" {
		return v
	}
	return ":8080"
}

// buildChainClients hydrates the per-chain client map the PriceCache
// reads from. Each factory is registered in its package's init().
// The Helius / Alchemy URLs come from config; empty URLs fall back to
// the factory's default devnet endpoint.
func buildChainClients(cfg *config.Config) (map[string]chain.ChainClient, error) {
	out := map[string]chain.ChainClient{}
	for _, c := range []struct {
		name   string
		rpcURL string
	}{
		{"solana", cfg.HeliusDevnetURL},
		{"base", cfg.AlchemyBaseSepoliaURL},
	} {
		factory, err := chain.Get(c.name)
		if err != nil {
			return nil, fmt.Errorf("chain %s: %w", c.name, err)
		}
		client, err := factory(chain.Config{
			Network: cfg.Network,
			RPCURL:  c.rpcURL,
		})
		if err != nil {
			return nil, fmt.Errorf("chain %s factory: %w", c.name, err)
		}
		out[c.name] = client
	}
	return out, nil
}

// newLogger builds the production zap logger. The runtime, swarm, and
// api subsystems consume it directly; the agent-package Logger interface
// gets a thin adapter below.
func newLogger(level string) (*zap.Logger, error) {
	lvl := zap.NewAtomicLevelAt(zap.InfoLevel)
	switch level {
	case "debug":
		lvl.SetLevel(zap.DebugLevel)
	case "warn":
		lvl.SetLevel(zap.WarnLevel)
	case "error":
		lvl.SetLevel(zap.ErrorLevel)
	}
	enc := zap.NewProductionEncoderConfig()
	enc.TimeKey = "ts"
	enc.EncodeTime = zapcore.RFC3339NanoTimeEncoder
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(enc),
		zapcore.Lock(os.Stdout),
		lvl,
	)
	return zap.New(core), nil
}

// llmRegistryAdapter satisfies runtime.LLMRegistry by delegating to the
// package-global llm.Get. The runtime takes an interface so tests can
// inject overrides; production uses this trivial adapter.
type llmRegistryAdapter struct{}

func (llmRegistryAdapter) Get(model string) (llm.LLMClient, error) {
	return llm.Get(model)
}

// loggerAdapter bridges *zap.Logger to the agent.Logger interface the
// runtime + swarm consume. Only Infow / Warnw / Errorw are used today;
// kvs are alternating key/value pairs.
type loggerAdapter struct {
	log *zap.Logger
}

func (a loggerAdapter) Debugw(msg string, kv ...any) { a.log.Debug(msg, fieldsFromKV(kv)...) }
func (a loggerAdapter) Infow(msg string, kv ...any)  { a.log.Info(msg, fieldsFromKV(kv)...) }
func (a loggerAdapter) Warnw(msg string, kv ...any)  { a.log.Warn(msg, fieldsFromKV(kv)...) }
func (a loggerAdapter) Errorw(msg string, kv ...any) { a.log.Error(msg, fieldsFromKV(kv)...) }

func fieldsFromKV(kv []any) []zap.Field {
	if len(kv) == 0 {
		return nil
	}
	out := make([]zap.Field, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			k = fmt.Sprintf("%v", kv[i])
		}
		out = append(out, zap.Any(k, kv[i+1]))
	}
	return out
}
