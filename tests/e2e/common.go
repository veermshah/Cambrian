// Package e2e holds the chunk-33 devnet validation suite. Each file in
// this directory corresponds to one spec line (1183-1197) and either
// runs in-process against fakes (the pure-logic suites) or requires a
// live resource gated by an env var (the integration suites).
//
// The gate pattern is intentional: chunks 30/31 ship a real Postgres +
// Redis + Anthropic + Telegram footprint, but we don't want CI lanes
// without those creds to flag the whole suite as broken. require* helpers
// log a clear skip reason so an operator who runs `make e2e` and sees
// "skipped 8/16" knows exactly which env vars to set to unblock them.
package e2e

import (
	"os"
	"strings"
	"testing"
)

// envFlag is the marker env var that enables every gated test.
// Set E2E=1 to opt in to the integration suite as a whole; without it
// the require* helpers skip immediately even if the per-resource vars
// are set. Belt-and-suspenders so a developer with stray env vars
// doesn't accidentally rack up LLM cost via go test ./....
const envFlag = "E2E"

// e2eEnabled returns true when the operator has opted into the gated
// suite. Tests that are pure-logic don't call this — they run anywhere.
func e2eEnabled() bool {
	return strings.EqualFold(os.Getenv(envFlag), "1") || strings.EqualFold(os.Getenv(envFlag), "true")
}

// requireE2E gates a test on E2E=1. Use this at the top of any test
// that touches a real external resource, even before the per-resource
// require* helper, so a `go test` without E2E set never fires expensive
// calls.
func requireE2E(t *testing.T) {
	t.Helper()
	if !e2eEnabled() {
		t.Skip("set E2E=1 to run the devnet validation suite")
	}
}

// requireDB skips when DATABASE_URL is missing. The DB-backed e2e tests
// expect the schema from internal/db/migrations to already be applied
// against the live URL — they don't run migrations themselves to avoid
// fighting concurrent suites.
func requireDB(t *testing.T) string {
	t.Helper()
	requireE2E(t)
	url := os.Getenv("DATABASE_POOL_URL")
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" {
		t.Skip("set DATABASE_URL (or DATABASE_POOL_URL) to run DB-backed e2e tests")
	}
	return url
}

// requireRedis skips when REDIS_URL is missing.
func requireRedis(t *testing.T) string {
	t.Helper()
	requireE2E(t)
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("set REDIS_URL to run Redis-backed e2e tests")
	}
	return url
}

// requireLLM skips when neither ANTHROPIC_API_KEY nor a fake-cost flag
// is set. The chunk-33 budget test only needs cost arithmetic, so it
// accepts a fake; lifecycle / quality / adversarial / postmortem tests
// need the real call shape.
func requireLLM(t *testing.T) string {
	t.Helper()
	requireE2E(t)
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("set ANTHROPIC_API_KEY to run LLM-backed e2e tests")
	}
	return key
}

// requireTelegram skips unless both bot token and chat id are present.
// The chunk-28 notifier test uses these to send live messages.
func requireTelegram(t *testing.T) (string, string) {
	t.Helper()
	requireE2E(t)
	tok := os.Getenv("TELEGRAM_BOT_TOKEN")
	chat := os.Getenv("TELEGRAM_CHAT_ID")
	if tok == "" || chat == "" {
		t.Skip("set TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID to run Telegram e2e tests")
	}
	return tok, chat
}

// requireDevnet skips unless devnet RPC endpoints are wired. Tests that
// talk to Solana / Base directly call this.
func requireDevnet(t *testing.T) (solanaURL, baseURL string) {
	t.Helper()
	requireE2E(t)
	sol := os.Getenv("HELIUS_DEVNET_URL")
	base := os.Getenv("ALCHEMY_BASE_SEPOLIA_URL")
	if sol == "" || base == "" {
		t.Skip("set HELIUS_DEVNET_URL and ALCHEMY_BASE_SEPOLIA_URL to run devnet RPC e2e tests")
	}
	return sol, base
}
