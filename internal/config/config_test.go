package config

import (
	"os"
	"strings"
	"testing"
)

var allConfigVars = []string{
	"NETWORK", "DATABASE_URL", "DATABASE_POOL_URL", "REDIS_URL",
	"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "HELIUS_DEVNET_URL",
	"ALCHEMY_BASE_SEPOLIA_URL", "TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID",
	"MASTER_ENCRYPTION_KEY", "MONTHLY_BUDGET_USD", "LOG_LEVEL", "API_KEY",
}

// applyEnv sets the given vars (using t.Setenv so they're restored after the test)
// and unsets every other config var the loader might inspect.
func applyEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	set := make(map[string]bool, len(vars))
	for k, v := range vars {
		t.Setenv(k, v)
		set[k] = true
	}
	for _, k := range allConfigVars {
		if !set[k] {
			// t.Setenv with empty string still sets the var; we need it actually unset
			// so a "missing required" test sees absence. Save+restore manually.
			prior, had := os.LookupEnv(k)
			if had {
				_ = os.Unsetenv(k)
				t.Cleanup(func() { _ = os.Setenv(k, prior) })
			}
		}
	}
}

func minimalEnv() map[string]string {
	return map[string]string{
		"NETWORK":               "devnet",
		"DATABASE_URL":          "postgresql://user:pw@host:5432/db",
		"REDIS_URL":             "rediss://default:pw@host:6379",
		"MASTER_ENCRYPTION_KEY": strings.Repeat("a", 64),
		"API_KEY":               "test-api-key",
	}
}

func TestLoad_AllRequiredPresent_Succeeds(t *testing.T) {
	applyEnv(t, minimalEnv())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Network != "devnet" {
		t.Errorf("Network = %q, want %q", cfg.Network, "devnet")
	}
	if cfg.DatabaseURL == "" {
		t.Error("DatabaseURL is empty")
	}
	if cfg.RedisURL == "" {
		t.Error("RedisURL is empty")
	}
}

func TestLoad_MissingRequired_ReturnsError(t *testing.T) {
	required := []string{
		"NETWORK", "DATABASE_URL", "REDIS_URL", "MASTER_ENCRYPTION_KEY", "API_KEY",
	}
	for _, missing := range required {
		t.Run("missing_"+missing, func(t *testing.T) {
			env := minimalEnv()
			delete(env, missing)
			applyEnv(t, env)
			_, err := Load()
			if err == nil {
				t.Fatalf("Load() with missing %s returned nil error", missing)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("error %q does not mention missing var %q", err.Error(), missing)
			}
		})
	}
}

func TestLoad_NetworkValidation(t *testing.T) {
	env := minimalEnv()
	env["NETWORK"] = "mainnet-but-not-really"
	applyEnv(t, env)
	_, err := Load()
	if err == nil {
		t.Fatal("Load() with bogus NETWORK returned nil error")
	}
}

func TestLoad_DatabasePoolURLDefaultsToDatabaseURL(t *testing.T) {
	applyEnv(t, minimalEnv())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.DatabasePoolURL != cfg.DatabaseURL {
		t.Errorf("DatabasePoolURL = %q, want fallback to DatabaseURL %q", cfg.DatabasePoolURL, cfg.DatabaseURL)
	}
}

func TestLoad_DatabasePoolURLOverride(t *testing.T) {
	env := minimalEnv()
	env["DATABASE_POOL_URL"] = "postgresql://user:pw@pooler:6543/db"
	applyEnv(t, env)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.DatabasePoolURL != "postgresql://user:pw@pooler:6543/db" {
		t.Errorf("DatabasePoolURL = %q, want pooled URL", cfg.DatabasePoolURL)
	}
	if cfg.DatabaseURL == cfg.DatabasePoolURL {
		t.Error("DatabaseURL should differ from DatabasePoolURL when pool URL is set explicitly")
	}
}

func TestLoad_DefaultMonthlyBudget(t *testing.T) {
	applyEnv(t, minimalEnv())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.MonthlyBudgetUSD != 100 {
		t.Errorf("MonthlyBudgetUSD = %v, want default 100", cfg.MonthlyBudgetUSD)
	}
}

func TestLoad_OverrideMonthlyBudget(t *testing.T) {
	env := minimalEnv()
	env["MONTHLY_BUDGET_USD"] = "50"
	applyEnv(t, env)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.MonthlyBudgetUSD != 50 {
		t.Errorf("MonthlyBudgetUSD = %v, want 50", cfg.MonthlyBudgetUSD)
	}
}

func TestLoad_DefaultLogLevel(t *testing.T) {
	applyEnv(t, minimalEnv())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want default %q", cfg.LogLevel, "info")
	}
}

func TestLoad_MasterKeyMustBe32Bytes(t *testing.T) {
	env := minimalEnv()
	env["MASTER_ENCRYPTION_KEY"] = "tooshort"
	applyEnv(t, env)
	_, err := Load()
	if err == nil {
		t.Fatal("Load() with short master key returned nil error")
	}
}

func TestLoad_OptionalsCanBeUnset(t *testing.T) {
	applyEnv(t, minimalEnv())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.AnthropicAPIKey != "" {
		t.Errorf("AnthropicAPIKey = %q, want empty (optional)", cfg.AnthropicAPIKey)
	}
	if cfg.TelegramBotToken != "" {
		t.Errorf("TelegramBotToken = %q, want empty (optional)", cfg.TelegramBotToken)
	}
}
