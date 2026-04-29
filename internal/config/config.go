// Package config loads runtime configuration from environment variables.
//
// One Config is loaded at startup; later subsystems consume it via
// dependency injection rather than calling os.Getenv themselves.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config is the typed view of every environment variable the swarm reads.
type Config struct {
	// Network selects which chain endpoints + behavior the system uses.
	// Must be "devnet" or "mainnet". First 30 days are devnet only.
	Network string

	// DatabaseURL is the Supabase direct connection (port 5432). Used by
	// golang-migrate, which requires advisory locks + prepared statements
	// that pgBouncer in transaction mode does not support.
	DatabaseURL string

	// DatabasePoolURL is the Supabase pgBouncer pooled connection (port 6543).
	// Used by the app's pgx.Pool. Falls back to DatabaseURL when unset.
	DatabasePoolURL string

	// RedisURL is the Upstash TLS endpoint (rediss://...).
	RedisURL string

	AnthropicAPIKey       string
	OpenAIAPIKey          string
	HeliusDevnetURL       string
	AlchemyBaseSepoliaURL string
	TelegramBotToken      string
	TelegramChatID        string

	// MasterEncryptionKey is a 32-byte key encoded as 64 hex chars.
	// Wallet keys are encrypted at rest with AES-256-GCM under this key.
	MasterEncryptionKey string

	// MonthlyBudgetUSD caps total LLM + infra + RPC spend per calendar month.
	// Defaults to 100.
	MonthlyBudgetUSD float64

	// LogLevel is one of debug | info | warn | error. Defaults to "info".
	LogLevel string

	// APIKey gates the dashboard API via the X-Api-Key header.
	APIKey string
}

// Load reads, validates, and returns the configuration. Returns an error
// naming any missing required variable or invalid value.
func Load() (*Config, error) {
	cfg := &Config{}

	required := map[string]*string{
		"NETWORK":               &cfg.Network,
		"DATABASE_URL":          &cfg.DatabaseURL,
		"REDIS_URL":             &cfg.RedisURL,
		"MASTER_ENCRYPTION_KEY": &cfg.MasterEncryptionKey,
		"API_KEY":               &cfg.APIKey,
	}
	for name, dest := range required {
		v := os.Getenv(name)
		if v == "" {
			return nil, fmt.Errorf("missing required environment variable %s", name)
		}
		*dest = v
	}

	switch cfg.Network {
	case "devnet", "mainnet":
		// ok
	default:
		return nil, fmt.Errorf("invalid NETWORK %q: must be devnet or mainnet", cfg.Network)
	}

	if len(cfg.MasterEncryptionKey) != 64 {
		return nil, fmt.Errorf("MASTER_ENCRYPTION_KEY must be 64 hex chars (32 bytes), got %d chars", len(cfg.MasterEncryptionKey))
	}

	cfg.DatabasePoolURL = os.Getenv("DATABASE_POOL_URL")
	if cfg.DatabasePoolURL == "" {
		cfg.DatabasePoolURL = cfg.DatabaseURL
	}

	cfg.AnthropicAPIKey = os.Getenv("ANTHROPIC_API_KEY")
	cfg.OpenAIAPIKey = os.Getenv("OPENAI_API_KEY")
	cfg.HeliusDevnetURL = os.Getenv("HELIUS_DEVNET_URL")
	cfg.AlchemyBaseSepoliaURL = os.Getenv("ALCHEMY_BASE_SEPOLIA_URL")
	cfg.TelegramBotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	cfg.TelegramChatID = os.Getenv("TELEGRAM_CHAT_ID")

	cfg.LogLevel = os.Getenv("LOG_LEVEL")
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}

	cfg.MonthlyBudgetUSD = 100
	if v := os.Getenv("MONTHLY_BUDGET_USD"); v != "" {
		parsed, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("MONTHLY_BUDGET_USD %q: %w", v, err)
		}
		cfg.MonthlyBudgetUSD = parsed
	}

	return cfg, nil
}
