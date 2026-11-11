# Cambrian

A self-evolving swarm of DeFi trading agents on Solana and Base, written in Go.

Each agent is a **genome**: a task (yield rotation, liquidity provision,
liquidation hunting, momentum), a chain, an LLM strategist with its own prompt
and model, economic parameters, and learned rules. Agents trade on a fast
deterministic loop and are tuned by a slow LLM loop. Once an epoch, an
orchestrator settles every agent's books, kills or pauses the ones that lose
money, and lets profitable ones reproduce by mutating or crossing over their
genomes. Offspring are screened by an LLM quality check, an adversarial
bull/bear review, and a backtest before they get capital.

The system targets **devnet / testnet only** (Solana devnet, Base Sepolia).
Mainnet is gated behind the validation suite described below.

## Architecture

```mermaid
flowchart LR
    subgraph boot["cmd/swarm (single process)"]
        CFG[config] --> RT
        RT["runtime.SwarmRuntime<br/>one NodeRunner per agent"]
        ORCH["orchestrator.RootOrchestrator<br/>epoch cron"]
        PC["backtesting.PriceCollector"]
        API["api.Server<br/>REST + WebSocket"]
        TG["notifications.Telegram"]
    end

    subgraph node["NodeRunner (per agent)"]
        MON["monitor loop<br/>Task.RunTick + TickBandit"]
        STRAT["strategist loop<br/>agent.Strategist (LLM)"]
        STRAT -- config adjustments --> MON
    end
    RT --> node

    MON --> TASKS["agent/tasks<br/>cross_chain_yield · liquidity_provision<br/>liquidation_hunting · momentum"]
    TASKS --> SEC["security<br/>tx validator · allowlists · AES-GCM keys"]
    SEC --> CHAIN["chain<br/>Solana: Jupiter, Kamino, marginfi<br/>Base: 1inch, Aave, Morpho"]
    TASKS --> PRICES["chain.PriceCache<br/>singleflight, 30s TTL"]
    PC --> PRICES
    STRAT --> LLM["llm<br/>Anthropic · OpenAI · cost tracking"]

    ORCH --> EVO["evolution · crossover<br/>quality check · adversarial review<br/>economics · treasury sweeps · budget<br/>kill/pause · postmortems · diversity"]
    EVO --> BT["backtesting.Engine<br/>mock chain replay"]
    EVO --> LLM

    MON & STRAT & ORCH --> PG[("Postgres<br/>agents, trades, ledgers,<br/>lineage, epochs, prices")]
    RT & ORCH & TG & API <--> RD[("Redis<br/>lifecycle events,<br/>intel bus")]
    RD --- INTEL["intelligence<br/>signal bus · accuracy tracker<br/>knowledge graph"]
    API --> PG
```

### Packages

| Package | Role |
|---|---|
| `cmd/swarm` | Boots every subsystem: migrations, DB pool, Redis, LLM clients, chain clients, runtime, price collector, Telegram, API server, epoch cron. Graceful shutdown on SIGINT/SIGTERM. |
| `cmd/init-treasury` | Creates the root Solana and Base treasury wallets (keys encrypted at rest). Idempotent. |
| `cmd/devnet-fund` | Airdrops devnet SOL to the treasury; prints Base Sepolia faucet instructions. |
| `cmd/seed-nodes` | Inserts the first generation of agents from `seed.yaml`. Idempotent. |
| `internal/agent` | Genome model and validation, Thompson-sampling `TickBandit`, LLM strategist contract, learned rules with confidence and eviction. |
| `internal/agent/tasks` | The four strategies behind a common `Task` interface and a hot-reloadable registry. |
| `internal/runtime` | `SwarmRuntime`, per-agent `NodeRunner` (monitor + strategist loops), shared LLM scheduler, shadow-node hibernation, global circuit breaker. |
| `internal/orchestrator` | Per-epoch flow: settle ledgers, deterministic kill/pause, offspring pipeline (solvency, quality, adversarial, backtest), mutation and crossover, profit sweeps, monthly budget fallback ladder, postmortems, diversity scoring. |
| `internal/chain` | `ChainClient` interface with Solana and Base implementations, DEX and lending adapters, shared price cache. |
| `internal/llm` | Anthropic and OpenAI clients, model registry, per-call cost accounting, fake client for tests. |
| `internal/intelligence` | Typed Redis pub/sub for agent signals, per-agent signal accuracy (agents that are right get more weight), market knowledge graph with decay and contradiction handling. |
| `internal/backtesting` | Price history collector and a replay engine over a mock chain. |
| `internal/security` | AES-256-GCM key manager, log redactor, pre-flight transaction validator with per-chain destination allowlists. |
| `internal/api` | Read-only Gin REST API and WebSocket event relay, gated by `X-Api-Key`. See [`docs/api.md`](docs/api.md). |
| `internal/db` | pgx pool, golang-migrate migrations, queries. |
| `internal/notifications` | Throttled Telegram alerts and a daily digest. |

### One epoch

1. Each agent's ledger is settled: realized P&L minus its own LLM, RPC and infra costs.
2. A deterministic policy kills or pauses agents that breach loss or health limits.
3. Healthy, solvent agents whose reproduction policy allows it propose offspring.
4. Each offspring is a mutation (numeric config perturbation, rarer prompt, model, chain or task swaps) or a module-level crossover of two parents.
5. Offspring go through a solvency check, an LLM quality check, a bull/bear/synthesis review, and a backtest before they are spawned.
6. Killed agents get an LLM postmortem that categorizes the failure.
7. Profits are swept up to the parent and the root treasury; the epoch is logged and published.

## Running it

### Prerequisites

- Go 1.25+
- Postgres 16 and Redis 7 (local, or hosted: the code was developed against Supabase and Upstash)
- Optional: Anthropic and/or OpenAI API keys, Helius (Solana devnet) and Alchemy (Base Sepolia) RPC URLs, a Telegram bot

### Local Postgres and Redis

```sh
docker compose up -d           # Postgres on :5432, Redis on :6379
```

### Configure

```sh
cp .env.example .env
# Required: NETWORK, DATABASE_URL, REDIS_URL, MASTER_ENCRYPTION_KEY, API_KEY
#   MASTER_ENCRYPTION_KEY=$(openssl rand -hex 32)
#   API_KEY=$(openssl rand -hex 24)
# For the compose services:
#   DATABASE_URL=postgres://cambrian:cambrian@localhost:5432/cambrian?sslmode=disable
#   REDIS_URL=redis://localhost:6379/0
set -a; source .env; set +a
```

| Variable | Required | Purpose |
|---|---|---|
| `NETWORK` | yes | `devnet` or `mainnet` |
| `DATABASE_URL` | yes | Direct Postgres connection, used for migrations |
| `DATABASE_POOL_URL` | no | Pooled connection for the app (falls back to `DATABASE_URL`) |
| `REDIS_URL` | yes | `redis://` or `rediss://` (TLS) |
| `MASTER_ENCRYPTION_KEY` | yes | 64 hex chars; encrypts wallet keys at rest |
| `API_KEY` | yes | Required in `X-Api-Key` on every API request |
| `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` | no | LLM strategists and reviews; LLM paths are skipped when unset |
| `HELIUS_DEVNET_URL`, `ALCHEMY_BASE_SEPOLIA_URL` | no | Chain RPCs; defaults to public devnet endpoints |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | no | Alerts and daily digest |
| `MONTHLY_BUDGET_USD` | no | Spend cap for LLM + infra + RPC (default 100) |
| `LOG_LEVEL` | no | `debug`, `info` (default), `warn`, `error` |
| `API_ADDR` | no | API bind address (default `:8080`) |
| `EPOCH_INTERVAL` | no | Epoch cadence as a Go duration (default `1h`) |
| `BASE_SEPOLIA_FAUCET_KEY` | no | Used only by `cmd/devnet-fund` |

### Boot the swarm

```sh
go run ./cmd/swarm                    # first run applies migrations, then exits: treasury not initialized
go run ./cmd/init-treasury            # create root wallets
go run ./cmd/devnet-fund              # airdrop devnet SOL, print Base faucet steps
cp seed.yaml.example seed.yaml
go run ./cmd/seed-nodes seed.yaml     # 3 funded + 5 shadow agents across all tasks and both chains
go run ./cmd/swarm                    # boots everything; API on :8080
```

```sh
curl localhost:8080/healthz
curl -H "X-Api-Key: $API_KEY" localhost:8080/api/agents
```

### Docker image

```sh
make docker                           # builds cambrian:dev (static binaries on scratch)
docker run --env-file .env -p 8080:8080 cambrian:dev
```

## Testing

```sh
go vet ./...
go test ./...
```

Current result: `go vet` clean; `go test ./...` passes in all 17 packages that have tests, with
637 passing tests and subtests and 17 skips. The skips are tests that need a
running Docker daemon (Postgres via testcontainers) or live devnet, LLM and
Telegram credentials; they print the missing requirement. With credentials:

```sh
make e2e-live                         # E2E=1 go test -v ./tests/e2e/...
INTEGRATION=1 go test ./internal/chain/...
```

`tests/e2e` holds the acceptance suite tracked in
[`docs/devnet-validation-report.md`](docs/devnet-validation-report.md): 18
pure-logic suites pass (evolution, bandit convergence, learned rules,
knowledge graph, signal accuracy, budget, hibernation, diversity, hot reload,
transaction validation); 5 live suites are waiting on a full devnet run.

CI runs `go vet` and `go test` on every push and pull request.

## Design decisions

- **The LLM never builds transactions.** Strategists only return bounded config adjustments, which each task clamps. Every outbound transaction is simulated and checked against slippage limits and a per-chain destination allowlist before signing. This limits what a prompt-injected or confused model can do.
- **Two loops per agent.** Trading runs on a cheap deterministic monitor loop with a Thompson-sampling bandit over micro-policies; the expensive LLM runs on a slow loop. Most ticks cost zero tokens.
- **Agents pay for their own thinking.** Each agent's ledger is charged for its LLM, RPC and infra costs, so selection favors strategies that are profitable net of inference, not just on gross P&L.
- **Shadow nodes and hibernation.** New offspring paper-trade before receiving capital. Shadow nodes sleep between strategist windows; the test suite checks this saves at least 60% versus always-on.
- **Budget fallback ladder.** As monthly spend approaches the cap, the swarm reduces strategist frequency, then disables shadow strategists, then freezes reproduction, while deterministic trading keeps running.
- **Diversity pressure.** A Gini-Simpson score over (task, chain, model) triples gives rare genomes a small boost, and crossover lets the weaker parent win a module 20% of the time, so the swarm does not collapse onto one lineage.
- **Single binary, explicit dependencies.** Everything runs in one process under an `errgroup`; subsystems receive their dependencies through interfaces, so each has fakes (`chain/fake.go`, `llm/fake.go`, `redis/fake.go`) and the suite runs offline.

## Limitations

- **Not yet run end to end on devnet.** The live acceptance suites (24-hour lifecycle, cross-chain rate scan, adversarial review, backtest-vs-shadow comparison, Telegram delivery) have not been run, so there are no trading results yet.
- **No dashboard.** The API and WebSocket relay are built for one, but there is no frontend in this repo.
- `cmd/backtest` is a placeholder; backtests run inside the offspring pipeline only. `docs/api.md` mentions a `cmd/kill-agent` command that does not exist yet.
- Testnet DEX and lending liquidity is thin and unlike mainnet, so devnet P&L will not say much about real-market fitness.
- Single process: no horizontal scaling or leader election for the epoch cron.

## License

MIT
