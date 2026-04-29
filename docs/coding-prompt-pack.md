# Coding Prompt Pack — Self-Funding Evolutionary AI Agent Swarm (v8)

**Date:** 2026-04-28
**Spec:** `evolutionary-swarm-project-description.md`
**Design:** `docs/superpowers/specs/2026-04-28-coding-prompt-pack-design.md`

This pack is the operator's queue. Each chunk below is a single PR. Feed the prompt to the listed tool, review the PR, merge, advance.

---

## How to use

1. Pick the next unblocked chunk from the DAG.
2. Copy the prompt body verbatim into the named tool.
   - **Claude Code:** paste at the project root with the listed skills available.
   - **Codex (cloud):** paste as a new task; the spec file is already in the checkout.
   - **Copilot Coding Agent:** create a GitHub issue with the prompt as the body, then assign Copilot.
3. Review the PR against the acceptance criteria. Merge with squash. Pull `main`.
4. If the agent surfaces a spec contradiction in the PR description, resolve it before merge — never let an agent silently deviate.

**Branch convention:** `feat/NN-slug` off latest `main`. Squash-merge.

**Spec line refs:** all references like `[spec:120-134]` point to `evolutionary-swarm-project-description.md`.

---

## Index

| # | Name | Tool | Depends on |
|---|---|---|---|
| 1 | Bootstrap & config | Claude Code | — |
| 2 | DB pool & migrations | Codex | 1 |
| 3 | Chain interface & registry | Codex | 1 |
| 4 | Solana client (devnet) | Codex | 3 |
| 5 | Base client (Sepolia) | Codex | 3 |
| 6 | LLM clients & registry | Codex | 1 |
| 7 | Security primitives | Codex | 1 |
| 8 | Genome model & Redis wrapper | Codex | 1 |
| 9 | Treasury init & devnet funding | Claude Code | 2,4,5,7 |
| 10 | Task interface & TickBandit | Codex | 8 |
| 11 | CrossChainYield task | Codex | 4,5,10 |
| 12 | LiquidityProvision task | Codex | 4,5,10 |
| 13 | Price monitoring & cache | Codex | 4,5,8 |
| 14 | NodeRunner, Strategist, SwarmRuntime, Lifecycle | Claude Code | 6,7,8,10,11,12,13 |
| 15 | Economics module & profit sweeps | Codex | 2,8 |
| 16 | Budget tracker & fallback | Codex | 2,6 |
| 17 | Mutation operators | Codex | 8 |
| 18 | Crossover operator | Codex | 8 |
| 19 | LLM quality check | Codex | 6,8 |
| 20 | Adversarial Bull/Bear & diversity | Codex | 6,8 |
| 21 | Root orchestrator, epochs, circuit breaker, kill/pause, postmortem | Claude Code | 14,15,16,17,18,19,20 |
| 22 | Intel bus & accuracy tracker | Codex | 8 |
| 23 | Knowledge graph & intel aggregator | Codex | 2,22 |
| 24 | Learned rules | Codex | 2,8 |
| 25 | Price collector & backtest engine | Codex | 2,10,13 |
| 26 | LiquidationHunting task | Codex | 4,5,10 |
| 27 | Momentum task & shadow hibernation | Codex | 4,5,10,14 |
| 28 | Telegram notifier | Claude Code | 8,21 |
| 29 | Gin API server & WebSocket | Claude Code | 21,22,23,24,25 |
| 30 | Dashboard scaffold & core pages | Copilot | 29 |
| 31 | Dashboard advanced pages | Copilot | 29,30 |
| 32 | Main entry & seed | Claude Code | 14,21,25,27,28,29 |
| 33 | End-to-end devnet validation | Claude Code | 32 |

---

## DAG (parallelism windows)

```
1
├── 2, 3, 6, 7, 8                              [parallel after 1]
│       ├── 4, 5                               [parallel after 3]
│       └── 10                                 [after 8]
├── 9                                          [after 2,4,5,7]
├── 11, 12, 13                                 [parallel after 4,5,10]
├── 14                                         [after 6,7,8,10,11,12,13]
├── 15, 16, 17, 18, 19, 20                     [parallel after 2,6,8]
├── 21                                         [after 14,15,16,17,18,19,20]
├── 22                                         [after 8]
├── 23, 24                                     [parallel after 22 / 2,8]
├── 25, 26, 27                                 [parallel after 10,13,14]
├── 28, 29                                     [parallel after 21–25]
├── 30                                         [after 29]
├── 31                                         [after 30]
├── 32                                         [after 14,21,25,27,28,29]
└── 33                                         [after 32]
```

Recommended cadence: 3–5 Codex tasks in flight at a time; one Claude Code chunk at a time; Copilot chunks 30–31 after API contract is stable.

---

## Conventions every prompt assumes

- Go 1.22+. One module rooted at `github.com/veermshah/cambrian`. Layout per spec lines 917–1113.
- **Hosted infra:** Postgres on Supabase, Redis on Upstash. Local Postgres / Redis are only used in unit tests via `testcontainers-go` and `miniredis`. Two Postgres URLs exist: `DATABASE_URL` (direct, used by migrations) and `DATABASE_POOL_URL` (pgBouncer pooled, used by the app's `pgx.Pool`). Always use `DATABASE_URL` for `golang-migrate`; always use `DATABASE_POOL_URL` for the app pool. `REDIS_URL` is an Upstash `rediss://...` TLS endpoint.
- All env vars added to `.env.example` in the same PR that introduces them. Never commit `.env`.
- All LLM calls go through `LLMClient.Complete` and write a row to `agent_ledgers` (chunk 15 makes this enforceable; chunk 6 lays the cost-tracking groundwork).
- All wallet bytes encrypted at rest with AES-256-GCM (chunk 7). zap field redactor must be installed before any code that handles keys or LLM output.
- `NETWORK` env var (`devnet` | `mainnet`) governs chain selection. First 30 days are devnet only [spec:112-118].
- Every logic chunk ships with unit tests in the same PR. Devnet integration tests only in chunks 4, 5, 9, 33.
- Test fakes: chunk 3 introduces a `FakeChainClient`; chunk 6 introduces a `FakeLLMClient`. All later chunks test against fakes — no live network calls in unit tests.
- If an agent finds a contradiction with the spec mid-implementation: **stop, surface in PR description, do not silently deviate.**

---

## Chunk 1 — Bootstrap & config

**Tool:** Claude Code
**Branch:** `feat/01-bootstrap`
**Depends on:** —
**Scope:** ~6 files, ~1.5 hours

**Skills to invoke:** `superpowers:test-driven-development`, `superpowers:verification-before-completion`.

### Context

The repo currently contains only `evolutionary-swarm-project-description.md`, `LICENSE`, and `README.md`. You are scaffolding the Go module and the directory layout the rest of the chunks will fill in.

### Goal

Create a buildable Go module with the directory tree from spec lines 917–1113, a typed config struct loaded from environment variables, a `.env.example` enumerating every variable used anywhere in the spec, a `Dockerfile` that produces a `FROM scratch` image, and a placeholder `cmd/swarm/main.go` that loads config and exits cleanly. `go build ./...` must succeed.

### In scope

- `go.mod` with module path `github.com/veermshah/cambrian` (lowercase per Go convention; GitHub repo URL is case-insensitive)
- `internal/config/config.go`: struct with fields for `NETWORK`, `DATABASE_URL` (Supabase direct connection — used by migrations), `DATABASE_POOL_URL` (Supabase pooled / pgBouncer connection — used by the app's `pgx.Pool`; falls back to `DATABASE_URL` if unset), `REDIS_URL` (Upstash `rediss://...` with TLS), Anthropic key, OpenAI key, Helius URL, Alchemy URL, Telegram bot token + chat id, master encryption key, monthly budget USD, log level. Use `os.Getenv` + a small required/optional helper. No third-party config libs.
- `.env.example` listing every variable from `config.go` with a one-line comment each. Include short comments noting the Supabase direct-vs-pooled distinction and that `REDIS_URL` should be the Upstash TLS endpoint (`rediss://...`).
- `Dockerfile` (multi-stage, scratch final, copies `swarm` binary).
- `cmd/swarm/main.go` that calls `config.Load()`, logs the network and exits 0.
- Empty `internal/...` directory tree per spec lines 917–1031 (use `.gitkeep` files to anchor empty dirs).
- `Makefile` with `build`, `test`, `vet`, `lint` targets.

### Out of scope

- Database schema (chunk 2)
- Any chain / LLM / Redis client (chunks 3–8)
- Logger setup beyond `log.Printf` (chunk 7 introduces zap)

### Acceptance criteria

- `go build ./...` succeeds.
- `go vet ./...` passes.
- `make build` produces a `swarm` binary.
- `docker build .` succeeds.
- Running the binary with `NETWORK=devnet` and a populated `.env` exits 0 and prints `network=devnet`.
- All directories from the spec layout exist (with `.gitkeep` where empty).

### Tests

- `internal/config/config_test.go`: table-driven tests for required vs optional, missing-required errors, default values.

### PR

- Title: `feat(01): bootstrap module, config, Dockerfile, layout`
- Body: list of files created, `go build` output, confirmation that `.env.example` lists every variable referenced in `config.go`.

---

## Chunk 2 — DB pool & migrations

**Tool:** Codex
**Branch:** `feat/02-db-migrations`
**Depends on:** 1
**Scope:** ~3 files + migrations, ~2 hours

### Context

Spec lines 645–913 contain the full SQL schema. Chunk 1 has scaffolded the module and `internal/db/` directory. Production Postgres is hosted on **Supabase**; production Redis is hosted on **Upstash**. Supabase exposes two endpoints — a direct connection (`db.<ref>.supabase.co:5432`) and a pooled pgBouncer connection (`<ref>.pooler.supabase.com:6543`) — and they have different constraints.

### Goal

Set up `pgx/v5` connection pooling, write a migration runner using `golang-migrate`, and translate the entire schema in spec lines 645–913 into versioned migration files. Provide a `db.Queries` struct with stubs for the most common reads/writes (full implementations land in later chunks). Code must work cleanly against both Supabase (production) and a local Postgres 16 in tests.

### In scope

- `internal/db/db.go`: `pgx.Pool` factory. Reads `DATABASE_POOL_URL` first (Supabase pgBouncer / pooled), falls back to `DATABASE_URL`. Pool size max 20. Because Supabase pgBouncer runs in **transaction pooling mode**, set `pgxpool.Config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec` (or use `prefer_simple_protocol=true` in the URL) — prepared statements are not safe across pgBouncer transaction boundaries.
- `internal/db/migrate.go`: runs migrations on startup using `github.com/golang-migrate/migrate/v4`. **Always uses `DATABASE_URL` (the direct connection)**, never the pooled URL — `golang-migrate` requires advisory locks and prepared statements that pgBouncer in transaction mode does not support.
- `internal/db/migrations/0001_init.up.sql` and `0001_init.down.sql`: every `CREATE TABLE`, `CREATE INDEX`, and `CONSTRAINT` from spec lines 646–913. Group logically (one migration is fine; do not split across files). All schema must work on Supabase (Postgres 15+) — avoid extensions Supabase does not enable by default; if you need `pgcrypto` for `gen_random_uuid()`, include `CREATE EXTENSION IF NOT EXISTS pgcrypto;` at the top of the up migration.
- `internal/db/queries.go`: skeleton with `type Queries struct { pool *pgxpool.Pool }` and stub method signatures (returning `errors.New("not implemented")`) for: `InsertAgent`, `GetAgent`, `ListActiveAgents`, `LogTrade`, `LogStrategistDecision`, `InsertEpoch`, `InsertOffspringProposal`, `InsertPostmortem`, `InsertLedgerRow`. Real implementations come later — for now, signatures only.
- `cmd/swarm/main.go` updated to call `db.Run()` migrations on startup.

### Out of scope

- Implementing the query bodies (chunks 9, 14, 15, 21 fill these in).
- Any seeding (chunk 32).
- Supabase row-level security / auth (we are the only client; auth handled by `API_KEY` at the API layer).

### Acceptance criteria

- `go build ./...` and `go vet ./...` pass.
- Migration runs cleanly against a Supabase project and is idempotent (running twice is a no-op).
- Migration also runs cleanly against an empty local Postgres 16 instance (proves it isn't Supabase-coupled).
- All FK constraints validate (no `REFERENCES` to a table created later).
- Down migration drops everything cleanly.
- App pool successfully executes a `SELECT 1` through the pooled URL without a "prepared statement does not exist" error.

### Tests

- `internal/db/db_test.go`: spins up a Postgres instance via `testcontainers-go`, runs up + down migrations, verifies all expected tables exist after up and are gone after down.
- Skip the test if `TESTCONTAINERS_DISABLED=1` is set.
- Optional `internal/db/supabase_smoke_test.go` (gated by `INTEGRATION=1`): connects to the configured Supabase pooled URL, runs `SELECT 1`, asserts no errors.

### PR

- Title: `feat(02): pgx pool, golang-migrate setup, full schema migration`
- Body: paste `\dt` output after migration as proof; list every table created.

---

## Chunk 3 — Chain interface & registry

**Tool:** Codex
**Branch:** `feat/03-chain-interface`
**Depends on:** 1
**Scope:** ~4 files, ~1.5 hours

### Context

Spec lines 346–360 define the `ChainClient` interface. The implementations come in chunks 4 (Solana) and 5 (Base).

### Goal

Define `ChainClient` with the exact method set from spec lines 346–360, plus the `Quote`, `Trade`, `LendingPosition`, `YieldRate`, `TxResult`, `SimResult`, and `Wallet` types referenced. Provide a registry that maps `chain` names to factory functions. Provide a `FakeChainClient` test double that all later chunks can use without hitting a network.

### In scope

- `internal/chain/interface.go`: full interface as in the spec, with godoc comments for each method.
- `internal/chain/types.go`: `Quote`, `Trade`, `LendingPosition`, `YieldRate`, `TxResult`, `SimResult`, `Wallet` structs.
- `internal/chain/registry.go`: `type Factory func(cfg Config) (ChainClient, error)`, `Register(name string, f Factory)`, `Get(name string) (Factory, error)`.
- `internal/chain/fake.go`: in-memory `FakeChainClient` with deterministic price feeds and a `WithBalance`, `WithQuote`, `WithSimResult` builder API. Used by all later unit tests.

### Out of scope

- Real Solana / Base implementations (chunks 4, 5).
- Price caching across chains (chunk 13).

### Acceptance criteria

- `go build ./...` and `go vet ./...` pass.
- `FakeChainClient` implements `ChainClient` (assert via `var _ ChainClient = (*FakeChainClient)(nil)`).
- The registry returns an error for unknown chain names.

### Tests

- `internal/chain/fake_test.go`: exercises every interface method on the fake; demonstrates the builder API.
- `internal/chain/registry_test.go`: registers a fake factory, retrieves it, asserts unknown name fails.

### PR

- Title: `feat(03): chain interface, registry, fake client`
- Body: confirm interface matches spec lines 346–360; list of fake methods.

---

## Chunk 4 — Solana client (devnet)

**Tool:** Codex
**Branch:** `feat/04-solana-client`
**Depends on:** 3
**Scope:** ~4 files, ~3 hours

### Context

Chunk 3 defined `ChainClient`. This chunk implements it for Solana on devnet. Spec lines 363–366 specify the wallet model (Ed25519). The strategy task implementations in chunks 11/12/26 will use this client via the interface.

### Goal

Implement `ChainClient` for Solana using `github.com/gagliardetto/solana-go` and the Jupiter aggregator API. Run against devnet (RPC: Helius devnet endpoint from `HELIUS_DEVNET_URL`). All trade-execution methods must support a "dry run" mode that returns a simulated result without sending.

### In scope

- `internal/chain/solana.go`: implements every `ChainClient` method.
  - `GetBalance` / `GetTokenBalance`: RPC calls.
  - `GetQuote` / `ExecuteSwap`: Jupiter v6 API.
  - `SimulateTransaction`: `simulateTransaction` RPC.
  - `SendTransaction`: signed + sent.
  - `GetLendingPositions` / `GetYieldRates`: query MarginFi and Kamino devnet programs (use their public Go SDKs if available; otherwise direct RPC + account parsing).
  - `ExecuteLiquidation`: stub returning `errors.New("not implemented for devnet")` — devnet liquidations are tested in chunk 26 against a mock chain.
- `internal/chain/solana_wallet.go`: Ed25519 keypair generation, encrypted at rest using `internal/security` (will be wired up in chunk 7 — for now, accept a `[]byte` master key and use `crypto/aes` directly).
- Register the factory in `init()` so importing the package wires it into the chain registry.

### Out of scope

- Mainnet endpoints — gated behind `if cfg.Network == "mainnet"` checks; mainnet code paths can be stubs that return `errors.New("mainnet disabled")` for now.
- Liquidation execution (chunk 26).

### Acceptance criteria

- Implements `ChainClient` (compile-time assertion).
- `go vet` passes.
- A devnet integration test successfully fetches the SOL balance of a known devnet address and gets a Jupiter quote for SOL → USDC.

### Tests

- Unit tests against `httptest.Server` for Jupiter calls.
- `internal/chain/solana_devnet_test.go`: live devnet test, gated by `INTEGRATION=1` env var. Fetches balance + quote.

### PR

- Title: `feat(04): solana client (devnet) — RPC, Jupiter, wallets`
- Body: paste integration test output; note any spec deviations.

---

## Chunk 5 — Base client (Sepolia)

**Tool:** Codex
**Branch:** `feat/05-base-client`
**Depends on:** 3
**Scope:** ~4 files, ~3 hours

### Context

Mirror image of chunk 4 for Base (Ethereum L2) on Sepolia testnet, using `github.com/ethereum/go-ethereum` and the 1inch / Paraswap aggregator APIs. Wallet keys are secp256k1.

### Goal

Implement `ChainClient` for Base on Sepolia. All trade methods support dry-run.

### In scope

- `internal/chain/base.go`: every `ChainClient` method.
  - `GetBalance` / `GetTokenBalance`: ETH RPC + ERC20 `balanceOf`.
  - `GetQuote` / `ExecuteSwap`: 1inch aggregator API (Paraswap fallback).
  - `SimulateTransaction`: `eth_call` for revert detection.
  - `SendTransaction`: legacy + EIP-1559 supported.
  - `GetLendingPositions` / `GetYieldRates`: Aave V3 + Morpho on Sepolia (use their published Sepolia deployment addresses).
  - `ExecuteLiquidation`: stub for now.
- `internal/chain/base_wallet.go`: secp256k1 keypair generation, AES-256-GCM encryption.
- Register the factory in `init()`.

### Out of scope

- Mainnet (`mainnet` returns `errors.New("mainnet disabled")` for the next 30 days).
- Flashbots Protect integration (deferred to mainnet enablement, chunk after launch).

### Acceptance criteria

- Implements `ChainClient` (compile-time assertion).
- Sepolia integration test fetches ETH balance and a USDC → ETH quote.

### Tests

- Unit tests against `httptest.Server` for 1inch.
- `internal/chain/base_sepolia_test.go`: live Sepolia, gated by `INTEGRATION=1`.

### PR

- Title: `feat(05): base client (sepolia) — go-ethereum, 1inch, wallets`
- Body: integration test output; spec deviations if any.

---

## Chunk 6 — LLM clients & registry

**Tool:** Codex
**Branch:** `feat/06-llm-clients`
**Depends on:** 1
**Scope:** ~5 files, ~2 hours

### Context

Spec lines 405–413 define the `LLMClient` interface and the model registry. The strategist (chunk 14) and the orchestrator (chunks 19, 20, 21) all call this.

### Goal

Implement `LLMClient` for Anthropic and OpenAI with strict cost accounting. Every `Complete` returns input/output tokens and a USD cost computed from the per-model rate table.

### In scope

- `internal/llm/interface.go`: `LLMClient` interface from spec lines 406–411 + `LLMResponse` struct (`Content string`, `InputTokens int`, `OutputTokens int`, `CostUSD float64`, `Model string`).
- `internal/llm/anthropic.go`: HTTP client for the Anthropic Messages API. Use `claude-haiku-4-5-20251001`, `claude-sonnet-4-6`, `claude-opus-4-7` from the model registry. Compute cost via the per-model rate table (hardcode 2026-04 rates; surface in `internal/llm/rates.go`).
- `internal/llm/openai.go`: HTTP client for OpenAI Chat Completions. Models: `gpt-4o`, `gpt-4o-mini`.
- `internal/llm/registry.go`: factory map + `Get(modelName) (LLMClient, error)`.
- `internal/llm/fake.go`: `FakeLLMClient` returning canned responses; supports a builder API and asserts that every call increments cost.

### Out of scope

- Caching (Anthropic prompt caching) — defer to a follow-up. Note in PR description.
- Streaming (not used by the strategist).

### Acceptance criteria

- All clients implement `LLMClient`.
- `FakeLLMClient` is the only thing tests should use.
- Registry returns error for unknown model.
- Each completion produces a non-zero `CostUSD`; manual sanity check the rate table against current public pricing in the PR.

### Tests

- `internal/llm/anthropic_test.go`, `internal/llm/openai_test.go`: against `httptest.Server` recordings.
- `internal/llm/rates_test.go`: known-input → known-cost table tests.

### PR

- Title: `feat(06): llm clients (anthropic, openai), registry, cost tracking`
- Body: rate table snapshot date; confirm cost computation.

---

## Chunk 7 — Security primitives

**Tool:** Codex
**Branch:** `feat/07-security`
**Depends on:** 1
**Scope:** ~4 files, ~2 hours

### Context

Spec lines 497–501 specify the security model: AES-256-GCM at rest, transaction simulation before submission, zap field redactor, no private keys in logs.

### Goal

Provide three primitives the rest of the system uses without thinking: `key_manager`, `log_redactor`, `tx_validator`.

### In scope

- `internal/security/key_manager.go`: `Encrypt(plaintext []byte, masterKey []byte) ([]byte, error)`, `Decrypt(ciphertext []byte, masterKey []byte) ([]byte, error)`. AES-256-GCM with a random nonce prepended. Master key sourced from `MASTER_ENCRYPTION_KEY` env var, must be exactly 32 bytes.
- `internal/security/log_redactor.go`: a zap `zapcore.Field` encoder hook that redacts any field whose key matches `^(.*key|.*secret|.*token|wallet.*|signature)$` (case-insensitive). Provides `NewLogger(level string) *zap.Logger` for the rest of the project.
- `internal/security/tx_validator.go`: takes a `*chain.Transaction`, requires `SimResult.WouldRevert == false`, requires `EstimatedSlippagePct < tx.MaxAllowedSlippagePct`, requires the destination address is on a per-chain allowlist. Returns a structured error if any check fails.
- `internal/security/policy.go`: per-chain destination allowlists (Jupiter program, Aave V3 router, etc.).

### Out of scope

- Mempool protection (deferred to mainnet).

### Acceptance criteria

- AES roundtrip preserves bytes; tampered ciphertext fails `Decrypt`.
- Log redactor: any of the matching field names produces `"<redacted>"` in the JSON output.
- Tx validator rejects a fixture with revert; accepts a clean fixture.

### Tests

- Roundtrip + tamper tests for AES.
- Redactor tests with multiple field name patterns.
- Validator table-driven tests (revert / slippage / unknown destination / clean).

### PR

- Title: `feat(07): security primitives — AES-256-GCM, log redactor, tx validator`
- Body: redactor regex; allowlist contents.

---

## Chunk 8 — Genome model & Redis wrapper

**Tool:** Codex
**Branch:** `feat/08-genome-redis`
**Depends on:** 1
**Scope:** ~5 files, ~2 hours

### Context

Spec lines 552–606 define `AgentGenome` and its sub-structs. The Redis wrapper is used for pub/sub on the intel bus (chunk 22) and for transient state caching. Production Redis is hosted on **Upstash** (TLS endpoint, `rediss://...`).

### Goal

Translate the entire genome model from spec lines 552–606 into Go. Provide a Redis client wrapper used by all later chunks. Wrapper must work cleanly against Upstash (production) and a local Redis 7 (tests).

### In scope

- `internal/agent/genome.go`: `AgentGenome`, `SleepSchedule`, `ReproductionPolicy`, `CostPolicy`, `CommunicationPolicy`, `LearnedRule`. JSON tags must match the spec exactly so they round-trip with the DB JSONB columns.
- `internal/agent/genome_validate.go`: `(g *AgentGenome) Validate() error` enforcing: non-empty name, generation ≥ 0, `task_type` in the registry set, `chain` in `{solana, base}`, `strategist_model` in the LLM registry, all `*_pct` fields in `[0, 100]`, all `*_usd` fields ≥ 0.
- `internal/redis/redis.go`: thin wrapper around `go-redis/v9`. Constructor parses `REDIS_URL` via `redis.ParseURL` so it picks up the `rediss://` TLS scheme used by Upstash automatically. Methods: `Publish(ctx, channel string, value any) error` (JSON-encodes), `Subscribe(ctx, channels ...string) <-chan Message`, `Set/Get` for key-value. Handles connection retry with backoff. Note in code comment that Upstash's free tier caps at 10k commands/day — every consumer should use `Subscribe` (one persistent connection) rather than per-call polling.
- `internal/redis/fake.go`: in-memory fake for tests.

### Out of scope

- Genome mutation (chunk 17), crossover (chunk 18).
- Pub/sub channel constants (chunk 22 owns the intel bus channel set).

### Acceptance criteria

- `Validate()` rejects every documented invariant violation.
- JSON round-trips a populated `AgentGenome` byte-equal.
- `FakeRedis` implements the same surface as the real client.

### Tests

- `genome_test.go`: validation table tests; round-trip test.
- `redis_test.go`: pub/sub via fake; key-value via fake.

### PR

- Title: `feat(08): genome model + redis wrapper`
- Body: confirm field tags match spec lines 577–606.

---

## Chunk 9 — Treasury init & devnet funding

**Tool:** Claude Code
**Branch:** `feat/09-treasury-devnet`
**Depends on:** 2, 4, 5, 7
**Scope:** ~3 files (commands), ~2 hours

**Skills to invoke:** `superpowers:test-driven-development`, `superpowers:verification-before-completion`.

### Context

Chunks 4 and 5 give us live devnet/Sepolia clients. Chunk 7 gives us AES key management. Chunk 2 gives us the `agents` table. We need one-time commands to (a) create treasury wallets on both chains and store them encrypted, and (b) airdrop devnet SOL and request Sepolia ETH from a faucet.

### Goal

Two `cmd/` binaries: `init-treasury` and `devnet-fund`. The treasury keypairs are stored in the `agents` table with `node_class='funded'`, `name='root_treasury_solana'` and `name='root_treasury_base'`, `wallet_key_encrypted` populated.

### In scope

- `cmd/init-treasury/main.go`: generates Ed25519 (Solana) and secp256k1 (Base) keys, encrypts with `MASTER_ENCRYPTION_KEY`, inserts two agent rows. Idempotent: if rows already exist, prints addresses and exits 0.
- `cmd/devnet-fund/main.go`:
  - Solana: calls `requestAirdrop` for 5 SOL on devnet, waits for confirmation.
  - Base Sepolia: prints the address and a curl command for the public Sepolia faucet (programmatic faucets are rate-limited and unreliable). Actually attempt `https://faucet.base.org/api/sepolia` if the env var `BASE_SEPOLIA_FAUCET_KEY` is set.
- Update `internal/db/queries.go` with `InsertAgent` real implementation (the genome JSON columns come straight from a populated `AgentGenome`).
- Update `cmd/swarm/main.go` to print whether the treasury is initialized (skip startup if not).

### Out of scope

- Funding non-treasury nodes (chunk 14 lifecycle does that).
- Mainnet treasury funding (deferred 30 days).

### Acceptance criteria

- `init-treasury` is idempotent (run twice, second run is a no-op with same addresses printed).
- `devnet-fund` increases the Solana treasury balance by ~5 SOL (verify on-chain).
- `swarm` exits with a clear message if the treasury is not initialized.

### Tests

- Use `superpowers:test-driven-development`: write a unit test for `InsertAgent` against `testcontainers-go` Postgres before implementing it.
- Manual verification: run both commands against live devnet, paste balances + addresses into the PR description.

### PR

- Title: `feat(09): init-treasury + devnet-fund commands`
- Body: paste both addresses + verified on-chain balances. Confirm `superpowers:verification-before-completion` was used to validate end-to-end.

---

## Chunk 10 — Task interface & TickBandit

**Tool:** Codex
**Branch:** `feat/10-task-interface-bandit`
**Depends on:** 8
**Scope:** ~4 files, ~2 hours

### Context

Spec lines 226–254 (Task interface) and lines 144–163 (TickBandit). All four tasks (chunks 11, 12, 26, 27) implement this interface.

### Goal

Define `Task` interface, `Trade` struct, and `TaskRegistry`. Implement `TickBandit` (Thompson Sampling over Beta distributions).

### In scope

- `internal/agent/tasks/interface.go`: `Task` interface from spec lines 227–243, `Trade` struct (fields matching the `trades` table from chunk 2), `TaskFactory`, `TaskRegistry`.
- `internal/agent/bandit.go`: `TickBandit` per spec lines 145–163. Thompson Sampling: sample from each arm's `Beta(α, β)` and pick the highest. `Update(policy string, reward float64)` increments `α` if reward > 0 else `β`. Persist/restore via JSON (`SaveState() ([]byte, error)`, `LoadState([]byte) error`).
- `internal/agent/tasks/registry.go`: empty registry skeleton with `Register(name string, factory TaskFactory)`. The four tasks register themselves via `init()`.
- `internal/agent/tasks/fake.go`: `FakeTask` for testing the `NodeRunner` (chunk 14).

### Out of scope

- Real task implementations (chunks 11, 12, 26, 27).

### Acceptance criteria

- `FakeTask` implements `Task`.
- Bandit converges to the best arm in a 10k-pull simulation (test asserts the best arm is selected ≥ 70% of the time after 1k pulls).
- Bandit state round-trips via JSON.

### Tests

- `bandit_test.go`: convergence test with 3 simulated arms (one clearly best).
- `bandit_persist_test.go`: save → load → continue gives same results as no save.

### PR

- Title: `feat(10): task interface, registry, TickBandit (Thompson Sampling)`
- Body: convergence test results; histogram of arm pulls.

---

## Chunk 11 — CrossChainYield task

**Tool:** Codex
**Branch:** `feat/11-cross-chain-yield`
**Depends on:** 4, 5, 10
**Scope:** ~3 files, ~3 hours

### Context

Spec lines 257–282 define this task. It is the highest-priority revenue source per the empirical research findings (spec lines 68–72, 257–260).

### Goal

Implement the `cross_chain_yield` task: scan yield rates across Solana (MarginFi, Kamino) and Base (Aave V3, Morpho); when the yield differential exceeds bridge + gas costs by `MinYieldDiffBps`, rebalance capital to the higher-yield venue.

### In scope

- `internal/agent/tasks/cross_chain_yield.go`: implements `Task`.
  - `RunTick`: every `CheckIntervalSecs` (default 60s) call `chain.GetYieldRates` on both chains, evaluate against current allocation, decide rebalance. Throttle actual rebalances to no more than once per `RebalanceIntervalSecs` (default 3600s).
  - `GetStateSummary`: produces a < 500-token map (current allocation, top 3 rates per chain, last rebalance timestamp, realized yield over last 24h).
  - `ApplyAdjustments`: clamps then applies `min_yield_diff_bps`, `max_single_protocol_pct`, `bridge_cost_threshold`, `rebalance_interval_secs`.
  - `GetPositionValue` / `CloseAllPositions` straightforward.
- `internal/agent/tasks/cross_chain_yield_config.go`: `CrossChainYieldConfig` per spec lines 268–280.
- Register in `init()` so importing the package wires it into the registry.

### Out of scope

- Actual cross-chain bridging (use a mock "bridge" abstraction; real bridging deferred). For now, `Rebalance` swaps within a single chain to the higher-yield venue.
- Mainnet (`mainnet` returns `errors.New("...")`).

### Acceptance criteria

- Task implements `Task`.
- Given a `FakeChainClient` with rate vectors that flip every minute, the task triggers a rebalance only when `min_yield_diff_bps` is exceeded.
- `ApplyAdjustments` clamps to legal ranges.

### Tests

- `cross_chain_yield_test.go`: scenarios — equal rates (no rebalance), diff just below threshold (no rebalance), diff above threshold but within rebalance interval (no rebalance), diff above threshold past interval (rebalance), single-protocol cap enforced.

### PR

- Title: `feat(11): cross-chain yield optimizer task`
- Body: scenario test outputs; confirm yield aggregation matches spec lines 261–267.

---

## Chunk 12 — LiquidityProvision task

**Tool:** Codex
**Branch:** `feat/12-liquidity-provision`
**Depends on:** 4, 5, 10
**Scope:** ~3 files, ~3 hours

### Context

Spec lines 284–300. Concentrated liquidity on DEX pools (Orca/Raydium on Solana, Uniswap/Aerodrome on Base).

### Goal

Implement `liquidity_provision`. Provides liquidity in a `RangeWidthPct` band around the current price; rebalances when price drifts > `RebalanceThresholdPct` from band center.

### In scope

- `internal/agent/tasks/liquidity_provision.go`: full `Task` implementation.
  - `RunTick`: check price, decide rebalance, optionally widen/narrow per strategist adjustments.
  - `GetStateSummary`: current band, fees earned 24h, impermanent loss estimate.
  - `ApplyAdjustments`: `range_width_pct`, `rebalance_threshold_pct`, plus a strategist-only `pull_liquidity` action.
- `internal/agent/tasks/liquidity_provision_config.go`: `LiquidityProvisionConfig` per spec lines 289–298.
- Register in `init()`.

### Out of scope

- Actual on-chain LP txs against a real pool — abstract through `chain.ChainClient`'s existing methods. Real LP integration tested via fake.

### Acceptance criteria

- Implements `Task`.
- Rebalances on threshold drift, not before.
- IL estimate is monotonic in price drift magnitude.

### Tests

- Scenarios: stable price (no rebalance), 1% drift (no rebalance with default 3% threshold), 5% drift (rebalance), pull-liquidity action closes the position.

### PR

- Title: `feat(12): liquidity provision task`
- Body: scenarios; IL formula reference.

---

## Chunk 13 — Price monitoring & cache

**Tool:** Codex
**Branch:** `feat/13-price-monitoring`
**Depends on:** 4, 5, 8
**Scope:** ~3 files, ~2 hours

### Context

Spec line 1140 (price monitoring with caching). Tasks call this — they should not each independently hit Jupiter / 1inch.

### Goal

A shared, in-process price cache keyed by `(chain, token_pair)`. TTL 30s. Pulls via Jupiter (Solana) and 1inch (Base) under the hood. All concurrent task ticks for the same pair share one in-flight request via `singleflight`.

### In scope

- `internal/chain/prices.go`: `PriceCache` with `Get(ctx, chain, pair) (float64, error)`. Uses `golang.org/x/sync/singleflight` to deduplicate.
- Wire `PriceCache` to read from existing `chain.ChainClient.GetQuote` for a small fixed amount (e.g., 1 unit) — quote price = output / input.
- A 60s background refresh goroutine that pre-warms common pairs (driven from active agents' configured pairs).

### Out of scope

- Persistence of prices (chunk 25 owns the price collector that writes to `price_history`).

### Acceptance criteria

- 100 concurrent `Get` calls for the same key produce exactly 1 underlying chain call (verified with a counting fake).
- TTL is honored; after 31s a second call hits the chain again.

### Tests

- `prices_test.go`: singleflight behavior; TTL behavior.

### PR

- Title: `feat(13): shared price cache with singleflight`
- Body: race test output; cache hit/miss counters from a load test.

---

## Chunk 14 — NodeRunner, Strategist, SwarmRuntime, Lifecycle

**Tool:** Claude Code
**Branch:** `feat/14-runtime-core`
**Depends on:** 6, 7, 8, 10, 11, 12, 13
**Scope:** ~6 files, ~4 hours

**Skills to invoke:** `superpowers:test-driven-development`, `superpowers:writing-plans` (this chunk has multiple coupled pieces — write an internal plan first), `superpowers:verification-before-completion`.

### Context

This is the integrative chunk that ties tasks, the bandit, the LLM strategist, and goroutine lifecycle into a working node runner. Spec lines 437–481 define the `NodeRunner`. Spec lines 165–185 define the strategist contract. Spec lines 207–217 define hibernation (light touch here; chunk 27 finalizes).

### Goal

A `SwarmRuntime` that loads active agents from Postgres, materializes a `NodeRunner` per agent in its own `errgroup`, and runs three loops per node: a fast monitor loop (calls `Task.RunTick`), a slow strategist loop (one LLM call, parses response, applies clamped adjustments), a heartbeat loop (writes `agents.last_heartbeat_at` every 30s — add this column in this chunk's migration). A `LifecycleManager` exposes `Spawn(genome)`, `Kill(id, reason)`, `Pause(id, reason)`, `Promote(id)`, `Demote(id)`.

### In scope

- `internal/agent/strategist.go`: builds the prompt (state summary + bandit data + last 2-3 postmortems + recent intel + learned rules), one LLM call, parses response into `{config_changes, action_signal, offspring_proposal?, intel_broadcasts?, new_learned_rule?}`, validates defensively (clamp every numeric field, drop unknown keys, no-op fallback on parse failure), writes `strategist_decisions` row, increments `agent_ledgers.llm_cost_usd`.
- `internal/runtime/node_runner.go`: per-spec lines 438–481, plus the strategist loop and heartbeat loop. Use `golang.org/x/sync/errgroup`.
- `internal/runtime/swarm.go`: `SwarmRuntime`. Loads active agents on startup; manages a `map[uuid.UUID]*NodeRunner`; subscribes to Redis for `lifecycle:*` events and reacts.
- `internal/runtime/lifecycle.go`: `Spawn` / `Kill` / `Pause` / `Promote` / `Demote`. Each writes the new state to DB and emits a `lifecycle:*` event.
- `internal/runtime/scheduler.go`: staggered start (offset each node's first tick by `i * 500ms` to avoid thundering herd) and channel-based backpressure on the LLM call (`semaphore.NewWeighted(N)` from `golang.org/x/sync/semaphore` to cap concurrent strategist calls).
- New migration: add `last_heartbeat_at TIMESTAMPTZ` to `agents`.

### Out of scope

- Hibernation scheduling (chunk 27 wires the sleep windows in).
- Circuit breaker (chunk 21).
- Root orchestrator epoch (chunk 21).

### Acceptance criteria

- Spawning a node with a `FakeTask` and `FakeLLMClient` produces: (a) periodic `RunTick` calls on cadence, (b) periodic strategist calls on cadence, (c) heartbeat writes, (d) clean shutdown on context cancel.
- Defensive validation: a malformed LLM response (invalid JSON, out-of-range value, unknown key) results in no-op, an error log, and a `strategist_decisions` row with `output_raw` populated.
- Backpressure: 50 simultaneous strategist firings cap at the configured concurrency.

### Tests

- `node_runner_test.go`: drives a fake task + fake LLM through several ticks; asserts cadences and DB writes.
- `strategist_test.go`: malformed-response table tests.
- `lifecycle_test.go`: spawn → pause → resume → kill flow; DB state after each transition.
- `scheduler_test.go`: backpressure cap.

### PR

- Title: `feat(14): node runner, strategist, swarm runtime, lifecycle`
- Body: per-loop cadence diagram; state-machine diagram for `Lifecycle`; `superpowers:verification-before-completion` checklist.

---

## Chunk 15 — Economics module & profit sweeps

**Tool:** Codex
**Branch:** `feat/15-economics-sweeps`
**Depends on:** 2, 8
**Scope:** ~3 files, ~2.5 hours

### Context

Spec lines 89–102 (realized net profit formula), spec lines 78–88 (three kinds of money), spec lines 514–522 (reproduction & sweep rules).

### Goal

A pure `economics.Settle(agent, epoch)` function that computes the realized net profit row for `agent_ledgers`; a `treasury.Sweep` function that computes how much profit goes to parent vs root vs retained, and writes a `profit_sweeps` row.

### In scope

- `internal/orchestrator/economics.go`: `Settle(ctx, agentID, epochID) (Ledger, error)`. Reads trades, fees, slippage, llm cost, and prorated infra/rpc cost for the epoch. Computes realized net profit per spec line 91. Writes the `agent_ledgers` row.
- `internal/orchestrator/treasury.go`: `Sweep(ctx, agentID) (*Sweep, error)`. Inputs: net profit, reproduction policy, lineage state. Outputs: amount-to-parent, amount-to-root, amount-retained per the genome's `ReproductionPolicy`. Writes `profit_sweeps`.
- `internal/orchestrator/cost_attribution.go`: prorates monthly infra cost and RPC cost across active agents by their share of strategist calls + monitor ticks.

### Out of scope

- Actually moving funds between wallets (chunk 21 wires this up via the chain clients).
- Budget tracking (chunk 16).

### Acceptance criteria

- Pure functions — `Settle` and `Sweep` are testable without a chain.
- Edge cases: zero trades, all losses, debt carried, no parent (root sweep only).

### Tests

- `economics_test.go`: P&L formula table tests, including all six negative components zeroed and combined.
- `treasury_test.go`: sweep table tests covering: solvent + healthy + parent (split per policy), solvent + no parent (all to root), insolvent (no sweep), carrying debt (no sweep).

### PR

- Title: `feat(15): economics module + profit sweeps`
- Body: confirm formula matches spec lines 89–99.

---

## Chunk 16 — Budget tracker & fallback

**Tool:** Codex
**Branch:** `feat/16-budget-tracker`
**Depends on:** 2, 6
**Scope:** ~2 files, ~1.5 hours

### Context

Spec lines 533–536. Monthly $100 cap when configured conservatively; graceful degradation when budget is tight.

### Goal

Track month-to-date spend across all agents (LLM + infra + RPC) and provide a `BudgetState` enum: `healthy`, `tight`, `breached`. The `NodeRunner` and `RootOrchestrator` query this and react.

### In scope

- `internal/orchestrator/budget.go`:
  - `MonthlySpendUSD()` aggregates `agent_ledgers` over the current calendar month + infra rent.
  - `State()` returns enum based on configured `MONTHLY_BUDGET_USD` (default 100). `tight` at ≥ 80%, `breached` at ≥ 100%.
  - `OnTight()`, `OnBreached()` hooks: `tight` → halve strategist frequencies for all nodes (write to DB), disable shadow strategists; `breached` → also freeze offspring proposals, keep deterministic loops running.
- Helper: `BudgetTracker` struct with a 60s refresh ticker.

### Out of scope

- Resetting on month boundary (handled by SQL `date_trunc('month', now())` filtering — no separate reset job needed).

### Acceptance criteria

- State transitions monotonic during the month; resets cleanly at month boundary.
- `OnTight` is idempotent (calling twice doesn't halve twice — track whether it has fired this month).

### Tests

- Table tests over fixed `(spend, budget) → state` pairs.
- Idempotency test for hooks.

### PR

- Title: `feat(16): budget tracker + graceful degradation`
- Body: state transition diagram.

---

## Chunk 17 — Mutation operators

**Tool:** Codex
**Branch:** `feat/17-mutation`
**Depends on:** 8
**Scope:** ~3 files, ~2 hours

### Context

Spec lines 419–421 (per-module mutation: numeric ±20%, prompt rewriting via LLM, model switching, chain switching, bandit policy variation, rule modification).

### Goal

A `Mutate(parent AgentGenome) AgentGenome` function. Each module has its own `Mutate*` helper. Mutations are weighted: numeric perturbation common (~50%), prompt/model/chain mutations rarer (5–15%).

### In scope

- `internal/orchestrator/evolution.go` (new file): top-level `Mutate(rng *rand.Rand, parent AgentGenome) AgentGenome`.
- Per-module helpers: `mutateTaskModule`, `mutateBrainModule`, `mutateEconomicsModule`, `mutateCommunicationModule`. Each takes the rng + parent module, returns a mutated copy.
- Numeric perturbation: `±U(0.8, 1.2)` of the field value; respect documented bounds.
- Prompt rewriting: deferred to chunk 19 (calls LLM); for now, just appends a mutation marker like `// mut-v{generation+1}` so chunk 19 can fill the rewrite step in.
- Bandit policy variation: add or drop one policy from `BanditPolicies` with low probability.

### Out of scope

- Crossover (chunk 18).
- Quality check (chunk 19).

### Acceptance criteria

- Mutated genome passes `genome.Validate()` 99%+ of the time over 1000 random trials. (Some constraint violations are acceptable; they'd be filtered by quality check.)
- Numeric perturbations respect bounds.
- Determinism: same rng seed → same mutation.

### Tests

- 1000-trial fuzz test asserting validation pass rate.
- Determinism test (seeded rng).

### PR

- Title: `feat(17): mutation operators`
- Body: per-module probability table.

---

## Chunk 18 — Crossover operator

**Tool:** Codex
**Branch:** `feat/18-crossover`
**Depends on:** 8
**Scope:** ~2 files, ~2 hours

### Context

Spec lines 423–424 (module-level crossover; child inherits task module from parent A, brain from B, etc., weighted by per-domain performance).

### Goal

`Crossover(parentA, parentB AgentGenome, performanceA, performanceB ModulePerformance) AgentGenome`. For each module independently, pick the parent whose performance for that domain is higher, with some randomness so the rare loser sometimes wins.

### In scope

- `internal/orchestrator/crossover.go`: `Crossover` function, plus a `ModulePerformance` struct (`TaskPnL float64`, `BrainEfficiency float64` (P&L per LLM dollar), `EconomicsHealth float64` (lineage solvency), `CommunicationAccuracy float64` (signal accuracy)).
- Pick rule: with 80% probability take from the better-performing parent for that module; 20% take from the other.

### Out of scope

- Calculating `ModulePerformance` from history — that's a query the root orchestrator owns (chunk 21).

### Acceptance criteria

- Child has exactly four inherited modules.
- Lineage metadata records both parents (`parent_id` and `second_parent_id` in the `lineage` table).
- Determinism with seeded rng.

### Tests

- Crossover with extreme `ModulePerformance` values (one parent strictly dominates → 80% takes from dominator).
- Module independence: shuffling the modules across many trials gives all 16 combinations.

### PR

- Title: `feat(18): crossover operator (module-level recombination)`
- Body: pick-rule justification; lineage row schema.

---

## Chunk 19 — LLM quality check

**Tool:** Codex
**Branch:** `feat/19-quality-check`
**Depends on:** 6, 8
**Scope:** ~2 files, ~1.5 hours

### Context

Spec line 425 (LLM quality check on every offspring: diversity, coherence, gap coverage). Also: chunk 17 left a placeholder for prompt rewriting — fill that in here too.

### Goal

`QualityCheck(ctx, candidate AgentGenome, swarmContext SwarmContext) (Verdict, error)` makes one LLM call. Output: `{verdict: approve|reject|revise, reasoning: string, suggested_revisions?: ...}`. Costs go to `agent_ledgers` (use chunk 15's settle path with a `system_internal` agent id, or store under the proposing agent — pick one and document).

### In scope

- `internal/orchestrator/quality_check.go`: `QualityCheck` function, prompt template, response parser, defensive validation.
- `SwarmContext`: minimal struct with current diversity score, list of existing genomes' (task_type, chain, model) tuples.
- `RewriteStrategistPrompt(ctx, parent, mutationMarker)`: fills the chunk 17 placeholder. One LLM call, returns the rewritten prompt.

### Out of scope

- Adversarial review (chunk 20).
- Backtest (chunk 25).

### Acceptance criteria

- Both functions go through `LLMClient` and produce non-zero `cost_usd`.
- Verdict parsing is strict: any deviation → `revise` with a defensive reasoning string.

### Tests

- Use `FakeLLMClient` to assert prompt structure and parse three response shapes (approve / reject / revise).

### PR

- Title: `feat(19): LLM quality check + strategist prompt rewriting`
- Body: prompt template; cost-attribution decision.

---

## Chunk 20 — Adversarial Bull/Bear & diversity

**Tool:** Codex
**Branch:** `feat/20-adversarial-diversity`
**Depends on:** 6, 8
**Scope:** ~3 files, ~2 hours

### Context

Spec lines 427–432. Two adversarial LLM calls per offspring (bull, bear), then a synthesis. Cost ~$0.01–0.03 per proposal. Diversity bonus in the fitness function (rare task_type/chain/model triples get a small boost).

### Goal

`AdversarialReview(ctx, candidate, swarmContext)` runs three LLM calls (bull, bear, synthesis) and returns a verdict with the bull case, bear case, and synthesis. `DiversityScore(swarm []AgentGenome) float64` computes a Simpson-style diversity index over (task_type, chain, model) triples.

### In scope

- `internal/orchestrator/adversarial.go`: three-call flow.
- `internal/orchestrator/diversity.go`: `DiversityScore`, plus `DiversityBonus(candidate, swarm) float64` returning a 0–1 boost based on rarity of the candidate's triple.
- Rejection tag: `adversarial_synthesis` populates `offspring_proposals.adversarial_synthesis`.

### Out of scope

- Wiring this into the root orchestrator's pipeline (chunk 21).

### Acceptance criteria

- Each function uses `FakeLLMClient` in tests.
- Diversity score is monotonically decreasing as duplicates accumulate.
- Diversity bonus is monotonically increasing as the candidate's triple is rarer.

### Tests

- Adversarial happy path (approve / reject / borderline).
- Diversity table tests with hand-picked swarms.

### PR

- Title: `feat(20): adversarial bull/bear + diversity scoring`
- Body: prompt templates; example synthesized output.

---

## Chunk 21 — Root orchestrator, epochs, circuit breaker, kill/pause, postmortem

**Tool:** Claude Code
**Branch:** `feat/21-orchestrator`
**Depends on:** 14, 15, 16, 17, 18, 19, 20
**Scope:** ~6 files, ~4–5 hours

**Skills to invoke:** `superpowers:writing-plans` (multi-step coupled changes), `superpowers:test-driven-development`, `superpowers:verification-before-completion`.

### Context

This integrates everything from the economics + evolution column (chunks 15–20) plus the runtime (chunk 14) into the per-epoch orchestration loop described in spec line 487 and the kill/pause/circuit-breaker rules in spec lines 514–528.

### Goal

`RootOrchestrator.RunEpoch(ctx)` performs the full flow from spec line 487 in order: gather state → settle economics → collect rent → sweep profits → deterministic kill/pause → process offspring proposals (solvency → quality check → adversarial review → optional backtest) → identify crossover opportunities → root LLM strategic decisions → execute → generate postmortems → log epoch → emit Telegram event → retention cleanup. Plus: circuit breaker with the spec's triggers.

### In scope

- `internal/orchestrator/parent.go`: `RootOrchestrator.RunEpoch` per spec line 487.
- `internal/orchestrator/kill_pause.go`: deterministic policy per spec lines 514–520. Returns `[]LifecycleAction`.
- `internal/orchestrator/postmortem.go`: structured postmortem generation. One LLM call per dead agent. Lessons categorized: `strategy_drift`, `regime_mismatch`, `cost_breach`, `unrecoverable_loss`.
- `internal/runtime/circuit_breaker.go`: triggers per spec lines 525–527 (SOL/ETH 15% in 1h, 50%+ funded nodes hit stops, RPC error rate ≥ 30% over 5min, manual). Auto-reset after 2h cooldown. Halts all `NodeRunner.monitorLoop` ticks (a global `atomic.Bool`).
- Wire chunk 17's mutation, chunk 18's crossover, chunk 19's quality check, chunk 20's adversarial review, into the offspring pipeline. Reject early on solvency failure.
- Add `cron`-driven scheduling for the epoch via `github.com/robfig/cron/v3`. Default 6h cadence.

### Out of scope

- Telegram delivery (chunk 28); just emit an event to Redis `events:epoch_completed`.
- API exposure (chunk 29).

### Acceptance criteria

- Full epoch runs end-to-end with `FakeChainClient` + `FakeLLMClient` against `testcontainers-go` Postgres, producing rows in `epochs`, `agent_ledgers`, `profit_sweeps`, `offspring_proposals`, `postmortems`, `lineage`.
- Circuit breaker correctly halts all monitor ticks within 1s of trigger and auto-resets.
- Offspring proposal pipeline rejects on every guardrail (insolvency, quality check reject, adversarial reject, backtest fail [stub the backtest call here, chunk 25 implements]).

### Tests

- `epoch_test.go`: end-to-end with fakes, asserting the sequence of DB writes.
- `circuit_breaker_test.go`: each trigger; auto-reset; manual override.
- `kill_pause_test.go`: every kill / pause condition.
- `postmortem_test.go`: structured output validation.

### PR

- Title: `feat(21): root orchestrator, epoch flow, circuit breaker, kill/pause, postmortem`
- Body: epoch sequence diagram; circuit breaker state machine; checklist from `superpowers:verification-before-completion`.

---

## Chunk 22 — Intel bus & accuracy tracker

**Tool:** Codex
**Branch:** `feat/22-intel-bus`
**Depends on:** 8
**Scope:** ~3 files, ~2 hours

### Context

Spec lines 372–390. Redis pub/sub channels with bull/bear tags; signal accuracy tracked over 30-day windows; consuming agents weight by accuracy → emergent authority.

### Goal

`IntelBus` over Redis pub/sub with the channel set from spec lines 374–380. `AccuracyTracker` that maintains rolling 30-day signal accuracy per source agent.

### In scope

- `internal/intelligence/bus.go`: `Publish(ctx, channel, signal Signal)`, `Subscribe(ctx, channels ...string) <-chan Signal`. `Signal` includes `source_agent_id`, `signal_type`, `sentiment` (`bull` / `bear` / `neutral`), `data`, `confidence`.
- `internal/intelligence/accuracy_tracker.go`: ingests `signal_outcomes` from chunk 21's epoch, computes 30-day rolling accuracy per source.
- Channel constants per spec lines 374–380.
- Bull/bear tag enforcement: signals on `intel:signals:*` must have `sentiment` populated; reject otherwise.

### Out of scope

- Knowledge graph (chunk 23).
- Aggregator (chunk 23).

### Acceptance criteria

- Publish + subscribe roundtrip via `FakeRedis`.
- Accuracy tracker over a 30-day fixture matches a hand-computed value.
- Untagged bull/bear signal returns error.

### Tests

- `bus_test.go`: pub/sub roundtrip; bull/bear enforcement.
- `accuracy_tracker_test.go`: rolling window computation.

### PR

- Title: `feat(22): intel bus + signal accuracy tracker`
- Body: channel list; accuracy formula.

---

## Chunk 23 — Knowledge graph & intel aggregator

**Tool:** Codex
**Branch:** `feat/23-knowledge-graph`
**Depends on:** 2, 22
**Scope:** ~3 files, ~2.5 hours

### Context

Spec lines 382–386 (knowledge graph in Postgres, edges decay if not revalidated, contradicting evidence reduces strength). Spec line 1160 (intel aggregator for strategist).

### Goal

`KnowledgeGraph` CRUD with decay + contradiction; `IntelAggregator.Summarize(agentID) IntelSummary` for the strategist's prompt.

### In scope

- `internal/intelligence/knowledge_graph.go`: `Upsert(edge Edge)` updates `last_validated`, increments `evidence_count`. `Decay` job nightly: edges not validated in 30 days have `strength *= 0.95`; below `0.1` are deleted. Contradicting evidence (same `entity_a`, `relationship`, `entity_b` but opposite `direction`) reduces strength by 0.2.
- `internal/intelligence/aggregator.go`: pulls top-N recent intel from agent's subscribed channels, weighted by source accuracy from chunk 22, dedupes, returns a < 1k-token summary.
- Cron job `internal/intelligence/decay_job.go` runs nightly.

### Out of scope

- Neo4j (explicitly excluded — Postgres only per spec line 384).

### Acceptance criteria

- Decay shrinks edges deterministically; deletion threshold respected.
- Contradiction reduces strength.
- Aggregator output is bounded in size.

### Tests

- Decay over a 60-day fixture.
- Contradiction handling.
- Aggregator size bound.

### PR

- Title: `feat(23): knowledge graph + intel aggregator`
- Body: edge lifecycle diagram; aggregator example output.

---

## Chunk 24 — Learned rules

**Tool:** Codex
**Branch:** `feat/24-learned-rules`
**Depends on:** 2, 8
**Scope:** ~2 files, ~1.5 hours

### Context

Spec lines 186–188. Up to 10 procedural rules per agent; confidence updated by outcomes; inherited by offspring; mutated during evolution; lowest-confidence evicted when full.

### Goal

`LearnedRule` CRUD with confidence updates, inheritance, mutation, eviction.

### In scope

- `internal/agent/learned_rules.go`:
  - `Add(agent, rule)` — if at cap (10), evict lowest-confidence first.
  - `RecordOutcome(agent, rule_id, success bool)` — updates confidence via `confidence = 0.9 * confidence + 0.1 * (1 if success else 0)` (EMA).
  - `Inherit(parent, child)` — child gets parent's rules, each at 0.7 × parent confidence (regression to mean).
  - `Mutate(rng, rules)` — with low probability rewrites a rule's text via LLM (delegate to chunk 19's `RewriteStrategistPrompt` or a similar helper).
- Storage: JSONB column `agents.learned_rules` (already in chunk 2 schema).

### Out of scope

- Wiring into strategist prompt (chunk 14 already loads them; just make sure the loader path works).

### Acceptance criteria

- Cap enforced; eviction picks lowest confidence.
- EMA produces expected sequence on a known input.
- Inheritance regression respected.

### Tests

- Cap + eviction.
- EMA convergence over a fixed sequence.
- Inheritance regression.

### PR

- Title: `feat(24): learned rules — confidence, inheritance, eviction`
- Body: EMA derivation; eviction policy rationale.

---

## Chunk 25 — Price collector & backtest engine

**Tool:** Codex
**Branch:** `feat/25-price-collector-backtest`
**Depends on:** 2, 10, 13
**Scope:** ~4 files, ~3 hours

### Context

Spec line 1180 (price collector) and lines 491–493 (backtest engine).

### Goal

A 1-minute price collector goroutine that writes to `price_history`. A `BacktestEngine` that runs a `Task` against historical prices using a `MockChainClient`.

### In scope

- `internal/backtesting/price_collector.go`: ticker every 60s; pulls active pairs from configured agents; uses `chain.PriceCache` (chunk 13); inserts into `price_history`.
- `internal/backtesting/mock_chain.go`: `MockChainClient` implementing `ChainClient` by replaying `price_history` rows. Supports a `slippage_bps` and `fee_bps` simulator.
- `internal/backtesting/engine.go`: `Run(ctx, genome, period) (BacktestResult, error)`. Materializes the configured task with the mock chain, runs ticks at simulated cadence, returns total P&L, max drawdown, win rate, Sharpe, equity curve.
- Wire `BacktestEngine.Run` into chunk 21's offspring pipeline (replace the chunk 21 stub).

### Out of scope

- Bridging mainnet-mock (we are devnet only).

### Acceptance criteria

- Collector writes 1 row per minute per active pair (verify with timestamps).
- Backtest result on a fixed price series matches a hand-computed P&L.
- Sharpe ratio calculated over daily returns.

### Tests

- `price_collector_test.go`: schedule + DB writes.
- `engine_test.go`: deterministic backtest result on fixture.

### PR

- Title: `feat(25): price collector + backtest engine`
- Body: backtest fixture + expected result; Sharpe calculation.

---

## Chunk 26 — LiquidationHunting task

**Tool:** Codex
**Branch:** `feat/26-liquidation-hunting`
**Depends on:** 4, 5, 10
**Scope:** ~2 files, ~3 hours

### Context

Spec lines 302–317.

### Goal

Implement the `liquidation_hunting` task. Monitor lending positions across configured protocols; when health factor drops below `HealthFactorThreshold`, attempt liquidation if estimated profit ≥ `MinProfitUSD`.

### In scope

- `internal/agent/tasks/liquidation_hunting.go`: full `Task` implementation.
  - `RunTick`: every `CheckIntervalSecs` (default 10s — fast). Iterate `chain.GetLendingPositions`. Compute health factor; if < threshold, simulate the liquidation; if profit ≥ min, call `chain.ExecuteLiquidation`.
  - `GetStateSummary`: liquidations today, success rate, top 3 at-risk positions.
  - `ApplyAdjustments`: clamp `min_profit_usd`, `health_factor_threshold`, `max_daily_liquidations`.
- `internal/agent/tasks/liquidation_hunting_config.go`: `LiquidationHuntingConfig` per spec lines 308–316.
- Implement the deferred `ChainClient.ExecuteLiquidation` in `solana.go` and `base.go` for the documented protocols (devnet — actual liquidations may be hard to trigger; leave a clear `TODO(mainnet)` if needed).

### Out of scope

- Mainnet flashbots (deferred).

### Acceptance criteria

- Task triggers liquidation on a fake position with health factor below threshold; skips if profit too low.
- `MaxDailyLiquidations` enforced.

### Tests

- Scenarios: healthy positions (no action), at-risk + profitable (action), at-risk + unprofitable (no action), daily cap reached.

### PR

- Title: `feat(26): liquidation hunting task`
- Body: scenarios; protocol-specific liquidation flow notes.

---

## Chunk 27 — Momentum task & shadow hibernation

**Tool:** Codex
**Branch:** `feat/27-momentum-hibernation`
**Depends on:** 4, 5, 10, 14
**Scope:** ~3 files, ~3 hours

### Context

Spec lines 319–338 (momentum). Spec lines 207–217 + 553–558 (hibernation). Chunk 14 left the sleep schedule unimplemented in `SwarmRuntime`; this chunk wires it in.

### Goal

Implement the `momentum` task. Wire shadow node hibernation: shadow nodes only tick during their awake window; otherwise their goroutines park.

### In scope

- `internal/agent/tasks/momentum.go`: full `Task` implementation. Lookback breakout with optional volume confirmation; stop-loss per trade; daily trade cap; max position size.
- `internal/agent/tasks/momentum_config.go`: `MomentumConfig` per spec lines 324–338.
- `internal/runtime/hibernation.go`: `HibernationScheduler.IsAwake(agent, now) bool`. The `NodeRunner` consults this in both monitor and strategist loops; if asleep, skips the tick.
- Update `SwarmRuntime` to support `HibernationScheduler`. Cost savings: shadow nodes' cumulative awake time should be ≤ 30% of wall clock at default settings.

### Out of scope

- Backtest of momentum strategy (handled generically by chunk 25).

### Acceptance criteria

- Momentum task: breakout enters, exit on threshold, stop-loss honored, daily cap honored.
- Hibernation: a shadow node configured with `AwakeWindowMinutes=120` and a 12h strategist cadence is awake ≤ 4h/day in test fixtures.

### Tests

- Momentum scenarios.
- Hibernation: simulate 24h, count awake ticks vs total.

### PR

- Title: `feat(27): momentum task + shadow node hibernation`
- Body: hibernation savings measurement; momentum scenarios.

---

## Chunk 28 — Telegram notifier

**Tool:** Claude Code
**Branch:** `feat/28-telegram`
**Depends on:** 8, 21
**Scope:** ~3 files, ~2 hours

**Skills to invoke:** `superpowers:test-driven-development`, `superpowers:verification-before-completion`.

### Context

Spec lines 503–506. Always-notify list, daily digest, throttling.

### Goal

A `TelegramNotifier` goroutine that subscribes to Redis events and pushes messages to the configured chat. Throttled: 20/hour cap, 60s cooldown between same-type messages. Daily digest at a configurable hour (default 09:00 user TZ).

### In scope

- `internal/notifications/telegram.go`: `TelegramNotifier` with `Run(ctx)`. Reads `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID`. Uses raw HTTP `sendMessage`.
- `internal/notifications/formatters.go`: per-event-type formatters: `circuit_breaker`, `agent_killed`, `agent_spawned`, `epoch_completed`, `budget_warning`, `treasury_low`, `daily_digest`.
- `internal/notifications/throttle.go`: token-bucket (20/hour) plus a per-type 60s cooldown map.
- Subscribe to channels emitted by chunk 21: `events:circuit_breaker`, `events:lifecycle`, `events:epoch_completed`, `events:budget`.

### Out of scope

- Multi-recipient (single user version per spec line 11).

### Acceptance criteria

- Real test sends a message to a configured test chat (gated by `INTEGRATION=1`).
- Throttle: a burst of 30 same-type events delivers 1, then queues nothing further until cooldown.
- Hourly cap: 25 mixed-type events deliver exactly 20.
- Daily digest fires once per day at the configured hour.

### Tests

- Unit tests against `httptest.Server` for `sendMessage`.
- Throttle table tests.
- Digest scheduling test.

### PR

- Title: `feat(28): telegram notifier — throttled events + daily digest`
- Body: confirm `superpowers:verification-before-completion` ran; live test screenshot.

---

## Chunk 29 — Gin API server & WebSocket

**Tool:** Claude Code
**Branch:** `feat/29-api-server`
**Depends on:** 21, 22, 23, 24, 25
**Scope:** ~14 handler files, ~5 hours

**Skills to invoke:** `superpowers:test-driven-development`, `superpowers:writing-plans`, `superpowers:verification-before-completion`.

### Context

Spec lines 1014–1030 (API handler list). The Next.js dashboard (chunks 30–31) consumes this.

### Goal

A Gin HTTP server exposing read-only endpoints for the dashboard, plus a WebSocket relay that forwards Redis `events:*` messages to connected clients. All endpoints are authenticated via a single `API_KEY` env var (header `X-Api-Key`).

### In scope

- `internal/api/server.go`: Gin setup, middleware (auth, structured logging via zap, recovery, CORS to `localhost:3000` in dev).
- `internal/api/handlers/*.go`: one file per spec line 1015–1029. Each handler queries Postgres directly. JSON shape documented inline.
- `internal/api/websocket.go`: subscribes to `events:*`, fans out to connected clients via `gorilla/websocket`. Heartbeat ping every 30s.
- `internal/api/types.go`: response types shared with the dashboard's TypeScript types (chunk 30 will codegen from these — keep field tags JSON-compatible).
- OpenAPI doc: `docs/api.md` enumerating every endpoint with request/response shapes (this becomes the contract for chunks 30–31).

### Out of scope

- Mutations (the dashboard is read-only; manual interventions are CLI-driven).
- Multi-tenant auth (single-user system).

### Acceptance criteria

- Every endpoint returns a documented JSON shape against a fixture DB.
- WebSocket delivers events end-to-end (Redis publish → connected client receives).
- Auth middleware rejects missing / wrong `X-Api-Key`.

### Tests

- Per-handler tests against `testcontainers-go` Postgres.
- WebSocket test: connect → publish → assert message.
- Auth test.

### PR

- Title: `feat(29): gin API server + WebSocket event relay`
- Body: link to `docs/api.md`; per-endpoint sample response; verification checklist.

---

## Chunk 30 — Dashboard scaffold & core pages

**Tool:** Copilot Coding Agent
**Branch:** `feat/30-dashboard-core`
**Depends on:** 29
**Scope:** ~25 files, ~3–5 hours

### GitHub issue body (paste into a new issue, then assign Copilot)

**Title:** `feat(30): dashboard scaffold + core pages (overview, agents, trades, epochs)`

**As the operator, I want** a working Next.js dashboard at `dashboard/` so that I can see swarm state in a browser.

**Context:**
- Backend API is ready; see `docs/api.md` for endpoint contracts.
- Backend exposes auth via `X-Api-Key` header (env var `NEXT_PUBLIC_API_KEY`).
- Backend WebSocket at `/ws` forwards Redis events.
- Spec section: `evolutionary-swarm-project-description.md` lines 1032–1085 (dashboard layout).

**Files to create:**
- `dashboard/package.json`, `dashboard/next.config.js`, `dashboard/tailwind.config.ts`, `dashboard/tsconfig.json`
- `dashboard/app/layout.tsx`: shell with sidebar + header.
- `dashboard/app/page.tsx`: overview — total agents, treasury, monthly spend, equity curve (recharts), recent epoch summary.
- `dashboard/app/agents/page.tsx`: table of agents, sortable by P&L / chain / class.
- `dashboard/app/agents/[id]/page.tsx`: per-agent detail — current state, recent trades, position value, lineage breadcrumb.
- `dashboard/app/trades/page.tsx`: paginated trade feed.
- `dashboard/app/epochs/page.tsx`: epoch timeline with summary stats.
- `dashboard/components/layout/Sidebar.tsx`, `Header.tsx`.
- `dashboard/components/shared/EquityCurve.tsx`, `StatusPill.tsx`, `ChainBadge.tsx`.
- `dashboard/hooks/useAgents.ts`, `useEpochs.ts`, `useTreasury.ts`, `useWebSocket.ts`.
- `dashboard/lib/api.ts`: typed `fetch` wrapper, react-query setup.
- `dashboard/types/index.ts`: TypeScript types matching `internal/api/types.go`.

**Acceptance criteria:**
- [ ] `npm install && npm run dev` starts the dashboard at `localhost:3000`.
- [ ] All four pages render against a running backend.
- [ ] WebSocket events update the overview live (e.g., new trade appears within 1s).
- [ ] Tailwind + shadcn/ui used per spec.
- [ ] No TypeScript errors (`tsc --noEmit` passes).
- [ ] Lighthouse perf ≥ 80 on `/` against local dev.

**Out of scope:** advanced pages (lineage, postmortems, offspring, intel, models, budget, backtests, evolution, diversity, agent brain) — those go in chunk 31.

---

## Chunk 31 — Dashboard advanced pages

**Tool:** Copilot Coding Agent
**Branch:** `feat/31-dashboard-advanced`
**Depends on:** 29, 30
**Scope:** ~12 page files, ~3–5 hours

### GitHub issue body

**Title:** `feat(31): dashboard advanced pages (lineage, postmortems, offspring, intelligence, models, budget, backtests, evolution, diversity, brain)`

**Context:**
- Chunk 30 set up the dashboard scaffold. Reuse its hooks and components.
- API contract: `docs/api.md`.
- Spec section: lines 1041–1067.

**Files to create (per page, one `app/<name>/page.tsx`):**
- `lineage/`: tree visualization of parent → child relations across generations.
- `postmortems/`: list view + detail modal showing structured lessons.
- `offspring/`: pending + historical proposals with bull/bear text panels and the synthesis verdict.
- `intelligence/`: live intel feed via WebSocket; sortable accuracy ranking; bull/bear filter.
- `models/`: per-LLM-model performance comparison (P&L per dollar, token cost, used-by count).
- `budget/`: monthly spend curve + breakdown by category (LLM / infra / RPC) and by agent.
- `backtests/`: recent backtest runs with equity curves.
- `evolution/`: mutation/crossover history visualization.
- `diversity/`: diversity score over time + current swarm composition by (task_type, chain, model).
- `agents/[id]/brain/page.tsx`: strategist prompt + last 20 decisions + bandit state + learned rules.

**Acceptance criteria:**
- [ ] Every page renders against a running backend without console errors.
- [ ] Lineage tree handles 30+ nodes legibly (use a layout lib like `dagre`).
- [ ] Intelligence page shows live updates via WebSocket.
- [ ] No TypeScript errors.
- [ ] All hyperlinks between pages work (e.g., postmortem → agent detail).

**Out of scope:** any backend changes; if a missing endpoint is needed, surface it in the PR description and STOP — do not add backend code from this chunk.

---

## Chunk 32 — Main entry & seed

**Tool:** Claude Code
**Branch:** `feat/32-main-seed`
**Depends on:** 14, 21, 25, 27, 28, 29
**Scope:** ~3 files, ~2 hours

**Skills to invoke:** `superpowers:test-driven-development`, `superpowers:verification-before-completion`.

### Context

Everything is built; now wire it together into a single binary that starts cleanly and a `seed-nodes` command that creates the initial swarm.

### Goal

`cmd/swarm/main.go` starts: DB migration → `SwarmRuntime` → `RootOrchestrator` (cron) → `PriceCollector` → `TelegramNotifier` → `api.Server`. Graceful shutdown on SIGINT (30s timeout). `cmd/seed-nodes/main.go` creates the initial set of funded + shadow nodes from a YAML config file, picking sensible default genomes for each task type and chain.

### In scope

- `cmd/swarm/main.go`: full wiring with `errgroup` so any subsystem failure surfaces. Structured startup log line per subsystem.
- `cmd/seed-nodes/main.go`: reads `seed.yaml`, validates, calls `LifecycleManager.Spawn` for each. Idempotent (skip if name already exists).
- `seed.yaml.example`: 3 funded nodes (cross_chain_yield on each chain + one liquidity_provision), 5 shadow nodes covering the other task types and the second chain.
- Update `Dockerfile` to include the seed config.

### Out of scope

- Mainnet endpoints (still 30-day devnet lock).

### Acceptance criteria

- Single `swarm` binary starts all subsystems and runs a full epoch on devnet.
- Graceful shutdown: SIGINT → all goroutines exit within 30s.
- `seed-nodes` is idempotent.
- `swarm` exits cleanly if treasury isn't initialized (chunk 9 contract).

### Tests

- `cmd/swarm/integration_test.go`: starts the binary in-process with `testcontainers-go` Postgres + Redis + a mocked chain layer; runs for 5 minutes; asserts an epoch completed, no goroutine leaks.

### PR

- Title: `feat(32): main entry + seed-nodes command`
- Body: startup log; goroutine count diagram; verification checklist.

---

## Chunk 33 — End-to-end devnet validation

**Tool:** Claude Code
**Branch:** `feat/33-devnet-validation`
**Depends on:** 32
**Scope:** ~10 test files, ~5–6 hours

**Skills to invoke:** `superpowers:test-driven-development`, `superpowers:verification-before-completion`, `superpowers:requesting-code-review` (this chunk gates merge to mainnet path).

### Context

Spec lines 1182–1198 enumerate the validation suite required before considering mainnet enablement. This chunk runs them and produces the report.

### Goal

A `tests/e2e/` directory of devnet integration tests covering every item from spec lines 1183–1197, plus a `make e2e` target that runs them and a markdown report `docs/devnet-validation-report.md` summarizing results.

### In scope

- `tests/e2e/full_lifecycle_test.go`: 24-hour simulated run (compress wall time via fast-forward of cadences); asserts spawn, mutate, kill, postmortem, sweep, epoch all happen.
- `tests/e2e/cross_chain_yield_test.go`: rate scanning across both chains, rebalance triggered.
- `tests/e2e/evolution_test.go`: crossover coherence; quality check rejects degenerate mutations.
- `tests/e2e/adversarial_test.go`: bull/bear improves offspring quality (compare with vs without).
- `tests/e2e/bandit_test.go`: convergence over a long horizon.
- `tests/e2e/learned_rules_test.go`: accumulate / evict / inherit.
- `tests/e2e/knowledge_graph_test.go`: edges accumulate, decay, contradiction reduces strength.
- `tests/e2e/signal_accuracy_test.go`: weighting works.
- `tests/e2e/hibernation_test.go`: sleep/wake savings ≥ 60%.
- `tests/e2e/budget_test.go`: 20+ nodes under $100/month at default cadences (use `FakeLLMClient` cost-only).
- `tests/e2e/backtest_validation_test.go`: shadow result vs backtest prediction.
- `tests/e2e/hot_reload_test.go`: deploy new task type without interruption.
- `tests/e2e/security_test.go`: tx validator catches malformed transactions.
- `tests/e2e/telegram_test.go`: every event type delivers (gated by `INTEGRATION=1`).
- `tests/e2e/diversity_test.go`: maintained under selection pressure.
- `Makefile` `e2e` target.
- `docs/devnet-validation-report.md`: pass/fail per item, runtime, cost incurred.

### Out of scope

- Mainnet enablement (separate change after report is reviewed).

### Acceptance criteria

- All 16 e2e tests pass against live devnet (or pass with a clearly-justified skip — flag in the report).
- Report committed.
- Total LLM cost incurred during the run ≤ $5 (verify in report).

### Tests

The deliverables ARE tests. The acceptance criterion is they pass.

### PR

- Title: `feat(33): end-to-end devnet validation suite + report`
- Body: link to `docs/devnet-validation-report.md`; total cost; any skipped items with justification. Request review via `superpowers:requesting-code-review`.

---

## Done

After chunk 33 merges, the system is feature-complete on devnet. A separate (not-in-this-pack) change flips `NETWORK=mainnet`, funds real treasury wallets, enables the deferred mainnet code paths (Flashbots Protect, mempool guards, real liquidations), and runs a parallel validation report on mainnet with strict capital limits.
