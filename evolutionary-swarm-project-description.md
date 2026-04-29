# Self-Funding Evolutionary AI Agent Swarm (v8)

## Project overview

Build a production-ready autonomous agent swarm that operates on Solana and Base (Ethereum L2), evolves through self-funding lineages, communicates strategies between agents, and operates with strict economic guardrails — all managed through a Next.js dashboard with Telegram push notifications.

**Go backend.** The entire backend is written in Go — the swarm runtime, API server, orchestrator, and all agent logic. Go's goroutines and channels are a natural fit for managing 10-30 concurrent node loops in a single process with low memory overhead and high reliability.

**Next.js dashboard.** The frontend is a Next.js (App Router) application with TypeScript and Tailwind CSS, communicating with the Go API server via REST and WebSocket.

This is the personal / single-user version, architected for future multi-tenant SaaS conversion.

### System targets
- 10–30 total nodes/genomes
- a smaller subset of funded live-trading nodes
- shadow / paper-trading nodes for cheap mutation testing
- monthly non-capital operating expense under $100 when configured conservatively
- **devnet-only for the first 30 days** — no mainnet trading until the system proves stable
- live feature additions without interrupting running agents

### What makes this different
- Profitable nodes survive, weak nodes die. Natural selection, not manual management.
- Nodes design their own descendants with mutated reasoning styles.
- Agents share intelligence — successful strategies propagate across the swarm.
- Multiple blockchains, multiple LLM models, four task types — all competing for capital.
- Task types are selected based on empirical evidence: yield optimization and liquidity management are the proven revenue generators for AI agents, not pure trading.
- The operator receives Telegram alerts and only intervenes when they choose to.
- Architecture is informed by recent academic research (EvoAgent, TradingAgents, AEL, Lark) and production agent systems (Hermes, MiroFish).

---

## Research foundations

### EvoAgent (Yuan et al., 2024 — arXiv 2406.14228)
Evolutionary operators (mutation, crossover, selection) automatically generate diverse multi-agent systems from a single initial agent framework, with LLM-based quality checks.

**Applied to:** Evolution engine, offspring quality validation, genome representation.

### TradingAgents (Xiao et al., UCLA/MIT, 2024 — arXiv 2412.20138)
Multi-agent trading framework with specialized roles and structured Bull/Bear debates between agents with opposing views.

**Applied to:** Adversarial offspring evaluation, Bull/Bear signal channels, structured communication protocol.

### AEL: Agent Evolving Learning (Xu et al., 2026 — arXiv 2604.21725)
Two-timescale self-improvement: fast Thompson Sampling bandit for policy selection, slow LLM-driven reflection for failure diagnosis. Critical finding: memory plus reflection produced 58% improvement, but every additional mechanism degraded performance. The bottleneck is self-diagnosing how to use experience, not architectural complexity.

**Applied to:** Strategist layer (simple reflective diagnosis), two-timescale architecture, postmortem-driven learning. This is the most important architectural constraint.

### Lark (NeurIPS 2025 ER Workshop)
Biologically inspired neuroevolution with plasticity (intra-lifetime adaptation) and modular duplication.

**Applied to:** Intra-epoch adaptation via bandit, modular genome design.

### Hermes Agent (Nous Research, 2026)
Autonomous skill creation: after completing tasks, the agent creates reusable procedural rules. Three-tier memory: prompt, episodic, procedural. Serverless hibernation for idle agents.

**Applied to:** Learned rules layer, shadow node hibernation.

### MiroFish (CAMEL-AI, 2026)
Swarm intelligence prediction via emergent behavior. GraphRAG for knowledge grounding. Emergent authority through accuracy tracking.

**Applied to:** Market knowledge graph, signal accuracy tracking with emergent authority.

### Autonomous Agents on Blockchains (arXiv 2601.04583, 2026)
Threat model for LLM agents on blockchains: instruction hijacking, tool spoofing, mempool exploitation.

**Applied to:** Mainnet security model, transaction simulation, wallet architecture.

### DWF/Cornell Research on AI Agent Revenue (2026)
Key empirical finding: AI agents outperform humans in DeFi yield optimization by 12.3% higher annualized returns, but autonomous trading lags behind by 5x. Cross-chain yield optimization and liquidity management are the proven revenue generators. Pure trading (arbitrage, momentum) is the hardest, most competitive category.

**Applied to:** Task type selection — yield optimization and liquidity management prioritized over pure trading strategies.

---

## Core design principles

### 1. Separate three kinds of money
1. **Trading capital** on Solana and Base
2. **Off-chain operating expenses** — Anthropic, OpenAI, Render, Helius, Alchemy, Redis, Postgres
3. **Internal swarm accounting** that attributes costs and profit to each node

### 2. Root treasury owns real capital custody
Central treasury wallet holds most capital, allocates working balances, receives profit sweeps, enforces solvency and budget rules.

### 3. Off-chain vendor bills modeled as internal debt
The system maintains a virtual cost ledger. Node-level profitability is evaluated as if each node paid its own costs.

### 4. Net profitability only
```go
realizedNetProfit :=
    realizedTradingPnL -
    tradingFees -
    slippageCost -
    llmCostAttributed -
    infraRentAttributed -
    rpcCostAttributed -
    upstreamObligationsDue
```

### 5. Economic guardrails are the main anti-explosion mechanism
Reproduction bonds, descendant reserves, upstream tax, operating debt, lineage solvency checks, monthly budget limits, shadow-node promotion. Hard caps are a safety valve, not the primary design.

### 6. Simplicity over complexity in self-improvement (AEL principle)
The strategist does simple reflective self-diagnosis. Every proposed addition must justify itself against the baseline of "simple reflection + memory." More complex ≠ better.

### 7. Yield optimization over pure trading (empirical principle)
Research shows AI agents generate 12.3% higher returns through yield optimization vs manual strategies, while pure autonomous trading significantly underperforms. Task types are selected accordingly.

---

## Devnet-first mandate

**First 30 days: devnet only.**

Solana uses devnet. Base uses Sepolia testnet. Treasury funded via faucets. LLM costs ARE real and count against the $100/month budget. After 30 days, switching to mainnet requires only changing the `NETWORK` environment variable and funding real wallets. No code changes.

---

## Why Go

Go is the right language for this project for several reasons:

**Concurrency model.** The swarm runtime manages 10-30 nodes, each with 2-3 concurrent loops, plus background tasks (price collection, epoch evaluation, circuit breaker, Telegram notifier, retention cleanup). Go's goroutines (~2KB each) handle this with trivial memory overhead. Python's asyncio works but is more fragile — one blocking call in any coroutine stalls the entire event loop. Go's goroutine scheduler handles this transparently.

**Performance.** The monitor layer runs hundreds of ticks per minute. Go's compiled performance means lower CPU usage per tick, which matters when running 30 nodes on a single $12/month server.

**Reliability.** Go binaries are statically compiled with no runtime dependencies. Deploy a single binary — no virtual environments, no pip installs, no Python version mismatches. The process starts in milliseconds and runs for months without memory leaks.

**Ecosystem.** `solana-go` (Gagliardetto) for Solana, `go-ethereum` for Base/EVM, `pgx` for PostgreSQL, `go-redis` for Redis, standard `net/http` or `gin` for the API server, `gorilla/websocket` for WebSocket — all mature, production-grade libraries.

**Deployment.** Build a Docker image with a single `FROM scratch` layer containing just the binary. Smaller image, faster deploys, fewer attack surfaces.

---

## The four-layer node architecture

### 1. Monitor layer (fast, deterministic, no LLM)

Runs every 15-60 seconds as a goroutine. Reads prices, tracks positions, enforces risk checks, executes trades, emits state deltas and trade logs.

**Includes a Thompson Sampling bandit (from AEL):** Lightweight bandit over available micro-policies. Explores variations each tick, costs zero LLM calls, provides the strategist with data about what's actually working.

```go
type TickBandit struct {
    Alphas map[string]float64 // success counts per policy
    Betas  map[string]float64 // failure counts per policy
}

func (b *TickBandit) Select() string {
    // Sample from each arm's Beta distribution, pick highest
    best := ""
    bestSample := -1.0
    for policy := range b.Alphas {
        sample := betaSample(b.Alphas[policy], b.Betas[policy])
        if sample > bestSample {
            bestSample = sample
            best = policy
        }
    }
    return best
}
```

### 2. Strategist layer (slow, LLM-powered — reflective, not complex)

Runs every 2-6 hours for funded nodes, 12-24 hours for shadow nodes. As a goroutine with a ticker.

**Critical constraint (AEL):** One LLM call. Reflective self-diagnosis. One adjustment. No multi-step reasoning chains.

The strategist receives:
- compact state summary (under 500 tokens)
- bandit performance data
- last 2-3 lineage postmortems (institutional memory)
- recent intel from subscribed bus channels
- learned rules (procedural memory)

The strategist produces:
- brief failure diagnosis
- a single config adjustment dict (or empty)
- action signal: continue / pause / aggressive / defensive
- optionally: offspring proposal
- optionally: intel broadcasts
- optionally: a new learned rule

### Learned rules layer (from Hermes)
Each node maintains up to 10 persistent procedural rules discovered during operation. Rules have confidence scores updated based on outcomes. Inherited by offspring, mutated during evolution, lowest-confidence evicted when full. Stored as JSONB on the agent record.

### 3. Genome layer (modular, serializable DNA)

Independent modules that mutate and recombine independently:
- **Identity module:** name, lineage metadata, generation
- **Task module:** task type, chain assignment, strategy config
- **Brain module:** strategist prompt, strategist model, strategist cadence, bandit policies, learned rules
- **Economics module:** reproduction policy, cost policy, profit sharing
- **Communication module:** intel subscriptions, publish permissions

### 4. Economics layer (ledger + policy)
Tracks solvency, kill/pause triggers, profit sweep eligibility, reproduction eligibility, lineage health.

---

## Node classes

### Funded nodes
Real wallet, working capital, live trading, full strategist cadence from goroutine.

### Shadow nodes
Paper-trade with simulated slippage (0.1%) and fees. Less LLM budget.

**Hibernation (from Hermes):** Shadow nodes operate on a sleep/wake cycle. Awake for a configurable window around each strategist call (default 2 hours), fully suspended otherwise. The SwarmRuntime goroutine scheduler simply doesn't tick sleeping shadows. Reduces compute cost by ~70%.

### Paused nodes
No new positions, may manage exits, no reproduction.

### Dead nodes
Removed from scheduling, positions closed, funds swept, postmortem generated.

---

## Task types (4 — empirically selected)

Only task types with evidence of profitable AI agent operation are included. Pure trading strategies (arbitrage, mean reversion, token sniping) were evaluated and removed — research shows AI agents significantly underperform in autonomous trading compared to yield optimization and liquidity management.

### Task interface

```go
type Task interface {
    // Fast loop: check conditions, execute if warranted.
    RunTick(ctx context.Context) ([]Trade, error)

    // Package state for strategist (under 500 tokens).
    GetStateSummary(ctx context.Context) (map[string]interface{}, error)

    // Apply bounded config changes from strategist.
    ApplyAdjustments(adjustments map[string]interface{}) error

    // Total value of current positions.
    GetPositionValue(ctx context.Context) (float64, error)

    // Gracefully close all positions.
    CloseAllPositions(ctx context.Context) ([]Trade, error)
}
```

### Task registry

```go
var TaskRegistry = map[string]TaskFactory{
    "cross_chain_yield":    NewCrossChainYieldTask,
    "liquidity_provision":  NewLiquidityProvisionTask,
    "liquidation_hunting":  NewLiquidationHuntingTask,
    "momentum":             NewMomentumTask,
}
```

### 1. Cross-chain yield optimizer (HIGHEST PRIORITY)

The most proven money-maker for AI agents. Research shows 12.3% higher annualized returns vs manual strategies, and $15k/month risk-free yield arbitrage on $100k capital.

The agent continuously scans yield sources across Solana and Base:
- Lending rates: MarginFi, Kamino (Solana), Aave V3, Morpho (Base)
- Staking yields: Jito (Solana), Lido derivatives (Base)
- LP fee rates: Orca, Raydium (Solana), Uniswap, Aerodrome (Base)

When rate differentials exceed bridge + gas costs, the agent moves capital to the highest risk-adjusted yield. Rebalances daily or when differentials justify it — not every tick.

```go
type CrossChainYieldConfig struct {
    PrimaryChain          string   `json:"primary_chain"`   // "solana" or "base"
    AllowedProtocols      []string `json:"allowed_protocols"` // ["marginfi", "kamino", "aave_v3", ...]
    MinYieldDiffBps       float64  `json:"min_yield_diff_bps"` // minimum yield difference to trigger rebalance
    MaxSingleProtocolPct  float64  `json:"max_single_protocol_pct"` // max % in one protocol
    RebalanceIntervalSecs int      `json:"rebalance_interval_secs"` // default 3600 (1 hour)
    BridgeCostThreshold   float64  `json:"bridge_cost_threshold"` // max acceptable bridge cost in USD
    MaxDrawdownPct        float64  `json:"max_drawdown_pct"`
    MinCapitalToOperate   float64  `json:"min_capital_to_operate"`
    CheckIntervalSecs     float64  `json:"check_interval_secs"` // 60 seconds
}
```

The strategist layer adds genuine value here: it reasons about which yield sources are sustainable vs temporary, when to pull liquidity before a rate drops, and when bridge costs are worth paying for a yield differential.

### 2. Liquidity provision

Provides concentrated liquidity on DEX pools. Earns trading fees. Strategist decides when to rebalance, widen/narrow range, or pull liquidity during high volatility.

```go
type LiquidityProvisionConfig struct {
    Chain                string  `json:"chain"`
    TokenPair            string  `json:"token_pair"`
    PoolAddress          string  `json:"pool_address"`
    FeeTier              string  `json:"fee_tier"`
    RangeWidthPct        float64 `json:"range_width_pct"`
    RebalanceThresholdPct float64 `json:"rebalance_threshold_pct"`
    CheckIntervalSecs    float64 `json:"check_interval_secs"`
    MaxDrawdownPct       float64 `json:"max_drawdown_pct"`
    MinCapitalToOperate  float64 `json:"min_capital_to_operate"`
}
```

### 3. Liquidation hunting

Monitors lending protocol positions approaching liquidation. Executes liquidations for the bonus (5-10%).

```go
type LiquidationHuntingConfig struct {
    Chain                 string   `json:"chain"`
    Protocols             []string `json:"protocols"` // solana: ["marginfi","kamino"], base: ["aave_v3","morpho"]
    MinProfitUSD          float64  `json:"min_profit_usd"`
    HealthFactorThreshold float64  `json:"health_factor_threshold"`
    CheckIntervalSecs     float64  `json:"check_interval_secs"` // 10 seconds — fast
    MaxPositionSizeUSD    float64  `json:"max_position_size_usd"`
    MaxDailyLiquidations  int      `json:"max_daily_liquidations"`
    MinCapitalToOperate   float64  `json:"min_capital_to_operate"`
}
```

### 4. Momentum

Follows price trends on longer timeframes (1-4 hour lookbacks). Enters on breakouts with volume confirmation. The one trading strategy where LLM reasoning adds a genuine edge — interpreting context beyond simple moving averages.

```go
type MomentumConfig struct {
    Chain              string  `json:"chain"`
    TokenPair          string  `json:"token_pair"`
    LookbackMinutes    int     `json:"lookback_minutes"`
    EntryThresholdPct  float64 `json:"entry_threshold_pct"`
    ExitThresholdPct   float64 `json:"exit_threshold_pct"`
    VolumeConfirmation bool    `json:"volume_confirmation"`
    CheckIntervalSecs  float64 `json:"check_interval_secs"` // 30 seconds
    MaxPositionSizePct float64 `json:"max_position_size_pct"`
    MaxDrawdownPct     float64 `json:"max_drawdown_pct"`
    StopLossPerTradePct float64 `json:"stop_loss_per_trade_pct"`
    MaxDailyTrades     int     `json:"max_daily_trades"`
    MinCapitalToOperate float64 `json:"min_capital_to_operate"`
}
```

---

## Multi-chain architecture

### Chain abstraction

```go
type ChainClient interface {
    GetBalance(ctx context.Context, address string) (float64, error)
    GetTokenBalance(ctx context.Context, address string, tokenAddr string) (float64, error)
    GetQuote(ctx context.Context, tokenIn, tokenOut string, amount float64) (*Quote, error)
    ExecuteSwap(ctx context.Context, quote *Quote, wallet *Wallet) (*TxResult, error)
    SimulateTransaction(ctx context.Context, tx *Transaction) (*SimResult, error)
    SendTransaction(ctx context.Context, tx *Transaction, wallet *Wallet) (*TxResult, error)
    GetLendingPositions(ctx context.Context, protocol string) ([]LendingPosition, error)
    GetYieldRates(ctx context.Context, protocols []string) ([]YieldRate, error) // new for yield optimizer
    ExecuteLiquidation(ctx context.Context, pos *LendingPosition, wallet *Wallet) (*TxResult, error)
    ChainName() string
    NativeToken() string
}
```

Implementations: `SolanaClient` (solana-go, Jupiter API) and `BaseClient` (go-ethereum, 1inch/Paraswap API).

### Wallet management
Solana: Ed25519 keypair. Base: secp256k1 keypair. Both encrypted at rest with AES-256-GCM using a master key from environment variables.

---

## Inter-agent communication and strategy sharing

### Intelligence bus (Redis pub/sub)

```
intel:signals:{chain}:bull       — bullish signals
intel:signals:{chain}:bear       — bearish signals
intel:strategies:{task_type}     — strategy insights
intel:warnings                   — risk warnings
intel:liquidations:{chain}       — at-risk positions
intel:yields:{chain}             — yield rate changes and opportunities
```

### Market knowledge graph (from MiroFish)

Stored in Postgres (not Neo4j). Typed entity-relationship edges: `SOL → correlates_with (positive, 0.85) → ETH`. Strength decays if not revalidated in 30 days. Contradicting evidence reduces strength.

### Signal accuracy tracking and emergent authority (from MiroFish)

Each source node's signal accuracy tracked over rolling 30-day windows. Consuming agents weight signals by source accuracy. High-accuracy, low-trading-profit agents may evolve into natural "scouts."

### Communication policy in genome

```go
type CommunicationPolicy struct {
    SubscribeChannels     []string `json:"subscribe_channels"`
    PublishChannels       []string `json:"publish_channels"`
    MaxBroadcastsPerCycle int      `json:"max_broadcasts_per_cycle"`
    IntelSummaryMaxItems  int      `json:"intel_summary_max_items"`
    RequireBullBearTag    bool     `json:"require_bull_bear_tag"`
}
```

---

## LLM model experimentation

```go
type LLMClient interface {
    Complete(ctx context.Context, system, user string, maxTokens int) (*LLMResponse, error)
    CalculateCost(inputTokens, outputTokens int) float64
}
```

Registry: `claude-haiku-4-5-20251001`, `claude-sonnet-4-20250514`, `gpt-4o-mini`, `gpt-4o`. Evolutionary pressure selects for best profit-per-LLM-dollar.

---

## Formalized evolution engine (from EvoAgent)

### Mutation operators
Per-module mutation: numeric perturbation (±20%), strategist prompt rewriting via LLM, model switching weighted by cost-adjusted performance, chain switching (low probability), bandit policy variation, learned rule modification.

### Crossover operator
Module-level crossover between two profitable nodes. Child inherits task module from parent A, brain module from parent B, etc. Weighted by which parent performs better in each domain.

### Selection with LLM quality check
Deterministic validation → LLM quality check (diversity, coherence, gap coverage) → materialize.

### Adversarial Bull/Bear evaluation (from TradingAgents)
Two adversarial LLM calls per offspring proposal — one arguing for, one against. Root synthesizes both perspectives. Cost: ~$0.01-0.03 per proposal, negligible.

### Diversity maintenance
Diversity bonus in fitness function. Rare task_type/chain/model combinations get a small boost. Prevents convergence on a single strategy.

---

## Node runner (two-timescale, goroutine-based)

```go
type NodeRunner struct {
    AgentID    string
    Genome     AgentGenome
    Task       Task
    Bandit     *TickBandit
    DB         *Database
    Redis      *RedisClient
    Chain      ChainClient
    LLM        LLMClient
    Running    atomic.Bool
    Postmortems []PostmortemLesson // loaded at startup
    LearnedRules []LearnedRule
}

func (n *NodeRunner) Run(ctx context.Context) {
    g, ctx := errgroup.WithContext(ctx)
    g.Go(func() error { return n.monitorLoop(ctx) })
    g.Go(func() error { return n.strategistLoop(ctx) })
    g.Go(func() error { return n.heartbeatLoop(ctx) })
    g.Wait()
}

func (n *NodeRunner) monitorLoop(ctx context.Context) error {
    ticker := time.NewTicker(time.Duration(n.Genome.StrategyConfig.CheckIntervalSecs) * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return nil
        case <-ticker.C:
            trades, err := n.Task.RunTick(ctx)
            if err != nil {
                log.Error("monitor_tick_failed", "agent_id", n.AgentID, "error", err)
                continue
            }
            for _, trade := range trades {
                n.DB.LogTrade(ctx, n.AgentID, trade)
                n.Redis.Publish(ctx, "events:trades", trade)
            }
        }
    }
}
```

---

## Root orchestrator

Epoch evaluation flow: gather state → settle economics → collect rent → sweep profits → deterministic kill/pause → process offspring proposals (solvency → quality check → Bull/Bear evaluation → optional backtest) → identify crossover opportunities → root LLM strategic decisions → execute → generate postmortems → log epoch → Telegram notifications → retention cleanup.

---

## Backtesting engine

Runs task strategy against historical price data using same `Task` interface with a mock `ChainClient`. Price collection starts day 1 on devnet. Shadow promotion requires passing backtest. Returns: total P&L, max drawdown, win rate, Sharpe ratio, equity curve.

---

## Mainnet security model (from blockchain security survey)

Transaction simulation before every mainnet submission. Instruction hijacking defense (strategist outputs config adjustments only, never constructs transactions). Mempool protection on Base (Flashbots Protect). Key encryption at rest (AES-256-GCM). Value-at-risk limits enforced in monitor layer. Shared RPC rate limiter.

---

## Telegram notifications

Always notify: circuit breaker, agent killed/spawned, epoch completed, budget 80%+, treasury below 30% reserve. Daily digest: best/worst agents, total P&L, promotions pending. Throttled: max 20/hour, 60s cooldown between same-type messages.

---

## Hot-reload

All state in Postgres. No in-memory-only state. Go binary restarts cleanly — goroutines shut down via context cancellation, new process loads active agents from DB and resumes. On Railway/Render: push to main → build → graceful shutdown (30s) → new container → resume. ~30-60s downtime.

---

## Kill, pause, survival, reproduction rules

**Kill:** balance below minimum, drawdown exceeded, consecutive losing epochs, operating debt exceeded, heartbeat missing.

**Pause:** strategy/regime mismatch, solvent but weak, budget tight.

**Reproduce:** active and healthy, positive realized net profit, no unpaid debt, can fully pre-fund offspring, lineage above reserve threshold, budget has room.

---

## Global circuit breaker

Triggers: SOL or ETH moves 15%+ in 1 hour, 50%+ funded nodes hit stop-losses in same epoch, RPC error rate 30%+ over 5 minutes, manual override. Auto-reset after 2-hour cooldown.

---

## Budget and cost controls

Infrastructure: 1 shared Go worker, 1 Go API server, 1 Postgres, 1 Redis. Default cadences: funded monitor 15-60s, funded strategist 2-6h, shadow strategist 12-24h, root epoch 6-12h, price collection 60s. Budget fallback: reduce strategist frequency → disable shadow strategists → freeze offspring → keep deterministic running.

---

## Data retention

- `trades`: 90 days full, then daily summaries
- `strategist_decisions`: 30 days full, then reasoning + config_changes only
- `price_history`: 90 days 1-minute, then downsample to 1-hour
- `postmortems`: forever
- `agent_ledgers`: forever
- `offspring_proposals`: forever
- `backtest_results`: 90 days
- `intel_log`: 30 days

---

## Genome model

```go
type SleepSchedule struct {
    AwakeWindowMinutes int  `json:"awake_window_minutes"`
    SleepBetween       bool `json:"sleep_between_windows"`
    WakeForBacktest    bool `json:"wake_for_backtest"`
}

type ReproductionPolicy struct {
    MinProfitableEpochs      int     `json:"min_profitable_epochs"`
    MinRealizedNetProfitUSD  float64 `json:"min_realized_net_profit_usd"`
    OffspringSeedCapitalUSD  float64 `json:"offspring_seed_capital_usd"`
    OffspringAPIReserveUSD   float64 `json:"offspring_api_reserve_usd"`
    OffspringFailureBufferUSD float64 `json:"offspring_failure_buffer_usd"`
    MaxDescendantsPerEpoch   int     `json:"max_descendants_per_epoch"`
    AllowTaskTypeMutation    bool    `json:"allow_task_type_mutation"`
    AllowChainMutation       bool    `json:"allow_chain_mutation"`
    AllowModelMutation       bool    `json:"allow_model_mutation"`
}

type CostPolicy struct {
    MonthlyLLMBudgetUSD       float64 `json:"monthly_llm_budget_usd"`
    MonthlyInfraRentBudgetUSD float64 `json:"monthly_infra_rent_budget_usd"`
    PauseOnBudgetBreach       bool    `json:"pause_on_budget_breach"`
}

type AgentGenome struct {
    // Identity module
    Name         string `json:"name"`
    Generation   int    `json:"generation"`
    LineageDepth int    `json:"lineage_depth"`

    // Task module
    TaskType       string                 `json:"task_type"` // cross_chain_yield, liquidity_provision, liquidation_hunting, momentum
    Chain          string                 `json:"chain"`     // solana, base
    StrategyConfig map[string]interface{} `json:"strategy_config"`

    // Brain module
    StrategistPrompt          string       `json:"strategist_prompt"`
    StrategistModel           string       `json:"strategist_model"`
    StrategistIntervalSeconds int          `json:"strategist_interval_seconds"`
    BanditPolicies            []string     `json:"bandit_policies"`
    LearnedRules              []LearnedRule `json:"learned_rules"`

    // Economics module
    CapitalAllocation  float64            `json:"capital_allocation"`
    ReproductionPolicy ReproductionPolicy `json:"reproduction_policy"`
    CostPolicy         CostPolicy         `json:"cost_policy"`

    // Communication module
    CommunicationPolicy CommunicationPolicy `json:"communication_policy"`

    // Shadow scheduling (from Hermes)
    SleepSchedule SleepSchedule `json:"sleep_schedule"`
}
```

---

## Tech stack

### Go backend
- Go 1.22+
- `github.com/gagliardetto/solana-go` — Solana interactions
- `github.com/ethereum/go-ethereum` — Base/EVM interactions
- `github.com/jackc/pgx/v5` — PostgreSQL (async, connection pooling)
- `github.com/redis/go-redis/v9` — Redis pub/sub and key-value
- `github.com/gin-gonic/gin` — HTTP API server
- `github.com/gorilla/websocket` — WebSocket for dashboard real-time events
- `net/http` — HTTP client for LLM APIs, Jupiter, 1inch, Telegram
- `github.com/robfig/cron/v3` — Epoch scheduling, retention cleanup
- `go.uber.org/zap` — Structured logging
- `golang.org/x/sync/errgroup` — Goroutine lifecycle management
- `github.com/golang-migrate/migrate` — Database migrations
- `encoding/json` — Genome serialization, LLM response parsing

### Next.js dashboard
- Next.js 14+ (App Router) + TypeScript + Tailwind CSS
- `recharts` — P&L charts, equity curves, cost tracking
- `@tanstack/react-query` — data fetching with polling
- Native WebSocket — real-time events from Go API server
- `shadcn/ui` — component library

### Infrastructure
- Render or Railway: 1 Go web service, 1 Go background worker (or single combined binary), 1 Postgres, 1 Redis
- Helius — Solana RPC (free tier)
- Alchemy — Base RPC (free tier)
- Anthropic + OpenAI API keys
- Telegram Bot token

---

## Database schema

```sql
CREATE TABLE agents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_id UUID REFERENCES agents(id),
    name TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 0,
    chain TEXT NOT NULL DEFAULT 'solana',
    wallet_address TEXT NOT NULL UNIQUE,
    wallet_key_encrypted BYTEA NOT NULL,
    task_type TEXT NOT NULL,
    strategy_config JSONB NOT NULL,
    strategist_prompt TEXT NOT NULL,
    strategist_model TEXT NOT NULL DEFAULT 'claude-haiku-4-5-20251001',
    strategist_interval_seconds INTEGER NOT NULL DEFAULT 14400,
    bandit_policies JSONB NOT NULL DEFAULT '["default"]',
    bandit_state JSONB NOT NULL DEFAULT '{}',
    learned_rules JSONB NOT NULL DEFAULT '[]',
    sleep_schedule JSONB NOT NULL DEFAULT '{}',
    reproduction_policy JSONB NOT NULL DEFAULT '{}',
    cost_policy JSONB NOT NULL DEFAULT '{}',
    communication_policy JSONB NOT NULL DEFAULT '{}',
    node_class TEXT NOT NULL DEFAULT 'funded',
    health_state TEXT NOT NULL DEFAULT 'healthy',
    capital_allocated NUMERIC(20, 9) NOT NULL DEFAULT 0,
    current_balance NUMERIC(20, 9) NOT NULL DEFAULT 0,
    peak_balance NUMERIC(20, 9) NOT NULL DEFAULT 0,
    lineage_id UUID,
    lineage_depth INTEGER NOT NULL DEFAULT 0,
    unpaid_operating_debt_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    retained_earnings_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    descendant_reserve_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    max_allowed_operating_debt_usd NUMERIC(20, 9) NOT NULL DEFAULT 10.0,
    reproduction_eligibility TEXT NOT NULL DEFAULT 'ineligible',
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    killed_at TIMESTAMPTZ,
    kill_reason TEXT,
    total_trades INTEGER NOT NULL DEFAULT 0,
    total_pnl NUMERIC(20, 9) NOT NULL DEFAULT 0,
    consecutive_negative_epochs INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT valid_node_class CHECK (node_class IN ('funded', 'shadow', 'paused', 'dead')),
    CONSTRAINT valid_chain CHECK (chain IN ('solana', 'base')),
    CONSTRAINT valid_task CHECK (task_type IN (
        'cross_chain_yield', 'liquidity_provision', 'liquidation_hunting', 'momentum'
    ))
);

CREATE TABLE trades (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id UUID NOT NULL REFERENCES agents(id),
    epoch_id UUID REFERENCES epochs(id),
    chain TEXT NOT NULL DEFAULT 'solana',
    trade_type TEXT NOT NULL,
    token_pair TEXT NOT NULL,
    dex TEXT NOT NULL,
    amount_in NUMERIC(20, 9) NOT NULL,
    amount_out NUMERIC(20, 9) NOT NULL,
    fee_paid NUMERIC(20, 9) NOT NULL DEFAULT 0,
    pnl NUMERIC(20, 9),
    tx_signature TEXT,
    is_paper_trade BOOLEAN NOT NULL DEFAULT false,
    bandit_policy_used TEXT,
    executed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata JSONB
);

CREATE TABLE strategist_decisions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id UUID NOT NULL REFERENCES agents(id),
    input_summary JSONB NOT NULL,
    output_raw TEXT NOT NULL,
    config_changes JSONB,
    reasoning TEXT,
    intel_broadcasts JSONB,
    offspring_proposal_submitted BOOLEAN NOT NULL DEFAULT false,
    new_learned_rule JSONB,
    model_used TEXT NOT NULL,
    input_tokens INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    cost_usd NUMERIC(10, 6) NOT NULL,
    decided_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE agent_ledgers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id UUID NOT NULL REFERENCES agents(id),
    epoch_id UUID REFERENCES epochs(id),
    realized_trading_pnl_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    trading_fees_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    slippage_cost_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    llm_cost_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    infra_rent_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    rpc_cost_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    upstream_paid_to_parent_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    upstream_paid_to_root_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    realized_net_profit_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE epochs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    epoch_number INTEGER NOT NULL UNIQUE,
    started_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    total_agents INTEGER NOT NULL,
    funded_agents INTEGER NOT NULL DEFAULT 0,
    shadow_agents INTEGER NOT NULL DEFAULT 0,
    agents_spawned INTEGER NOT NULL DEFAULT 0,
    agents_killed INTEGER NOT NULL DEFAULT 0,
    agents_promoted INTEGER NOT NULL DEFAULT 0,
    crossovers_performed INTEGER NOT NULL DEFAULT 0,
    treasury_balance NUMERIC(20, 9) NOT NULL,
    total_pnl NUMERIC(20, 9) NOT NULL,
    total_llm_cost_usd NUMERIC(10, 6) NOT NULL DEFAULT 0,
    monthly_spend_to_date_usd NUMERIC(10, 6) NOT NULL DEFAULT 0,
    swarm_diversity_score NUMERIC(10, 4),
    market_regime TEXT,
    parent_reasoning TEXT,
    circuit_breaker_triggered BOOLEAN NOT NULL DEFAULT false,
    metadata JSONB
);

CREATE TABLE offspring_proposals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    proposing_agent_id UUID NOT NULL REFERENCES agents(id),
    epoch_id UUID REFERENCES epochs(id),
    proposed_genome JSONB NOT NULL,
    requested_seed_capital_usd NUMERIC(20, 9) NOT NULL,
    requested_api_reserve_usd NUMERIC(20, 9) NOT NULL,
    requested_failure_buffer_usd NUMERIC(20, 9) NOT NULL,
    rationale TEXT NOT NULL,
    quality_check_verdict TEXT,
    quality_check_reasoning TEXT,
    bull_case TEXT,
    bear_case TEXT,
    adversarial_synthesis TEXT,
    backtest_result_id UUID REFERENCES backtest_results(id),
    status TEXT NOT NULL DEFAULT 'pending',
    rejection_reason TEXT,
    created_child_id UUID REFERENCES agents(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT valid_status CHECK (status IN (
        'pending', 'quality_check', 'adversarial_review',
        'approved', 'rejected', 'materialized'
    ))
);

CREATE TABLE postmortems (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id UUID NOT NULL REFERENCES agents(id),
    agent_name TEXT NOT NULL,
    lifespan_epochs INTEGER NOT NULL,
    total_trades INTEGER NOT NULL,
    total_pnl NUMERIC(20, 9) NOT NULL,
    total_llm_cost_usd NUMERIC(10, 6) NOT NULL,
    strategy_config_snapshot JSONB NOT NULL,
    strategist_prompt_snapshot TEXT NOT NULL,
    bandit_final_state JSONB,
    analysis TEXT NOT NULL,
    lessons_summary TEXT NOT NULL,
    lessons JSONB,
    failure_category TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE profit_sweeps (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id UUID NOT NULL REFERENCES agents(id),
    parent_agent_id UUID REFERENCES agents(id),
    amount_to_parent_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    amount_to_root_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    amount_retained_usd NUMERIC(20, 9) NOT NULL DEFAULT 0,
    executed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE lineage (
    child_id UUID PRIMARY KEY REFERENCES agents(id),
    parent_id UUID NOT NULL REFERENCES agents(id),
    second_parent_id UUID REFERENCES agents(id),
    evolution_method TEXT NOT NULL DEFAULT 'mutation',
    mutations_applied JSONB NOT NULL,
    spawn_reasoning TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE price_history (
    id BIGSERIAL PRIMARY KEY,
    chain TEXT NOT NULL,
    token_pair TEXT NOT NULL,
    price NUMERIC(20, 9) NOT NULL,
    volume_24h NUMERIC(20, 9),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE backtest_results (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    genome_snapshot JSONB NOT NULL,
    chain TEXT NOT NULL,
    token_pair TEXT NOT NULL,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    initial_capital NUMERIC(20, 9) NOT NULL,
    final_capital NUMERIC(20, 9) NOT NULL,
    total_pnl NUMERIC(20, 9) NOT NULL,
    max_drawdown_pct NUMERIC(10, 4) NOT NULL,
    total_trades INTEGER NOT NULL,
    win_rate NUMERIC(10, 4),
    sharpe_ratio NUMERIC(10, 4),
    equity_curve JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE intel_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_agent_id UUID NOT NULL REFERENCES agents(id),
    channel TEXT NOT NULL,
    signal_type TEXT NOT NULL,
    sentiment TEXT,
    data JSONB NOT NULL,
    confidence NUMERIC(3, 2),
    consumed_by_agents UUID[] DEFAULT '{}',
    source_accuracy_30d NUMERIC(5, 4),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ
);

CREATE TABLE market_knowledge (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_a TEXT NOT NULL,
    relationship TEXT NOT NULL,
    entity_b TEXT NOT NULL,
    direction TEXT NOT NULL,
    strength NUMERIC(3, 2) NOT NULL,
    evidence_count INTEGER NOT NULL DEFAULT 1,
    discovered_by UUID REFERENCES agents(id),
    last_validated TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT unique_edge UNIQUE (entity_a, relationship, entity_b)
);

CREATE TABLE signal_outcomes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    signal_id UUID NOT NULL REFERENCES intel_log(id),
    source_agent_id UUID NOT NULL REFERENCES agents(id),
    consuming_agent_id UUID NOT NULL REFERENCES agents(id),
    trade_id UUID REFERENCES trades(id),
    trade_pnl NUMERIC(20, 9),
    was_profitable BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes
CREATE INDEX idx_agents_status ON agents(status);
CREATE INDEX idx_agents_node_class ON agents(node_class);
CREATE INDEX idx_agents_chain ON agents(chain);
CREATE INDEX idx_agents_lineage ON agents(lineage_id);
CREATE INDEX idx_trades_agent ON trades(agent_id);
CREATE INDEX idx_trades_executed ON trades(executed_at);
CREATE INDEX idx_strategist_agent ON strategist_decisions(agent_id);
CREATE INDEX idx_ledgers_agent ON agent_ledgers(agent_id);
CREATE INDEX idx_epochs_number ON epochs(epoch_number);
CREATE INDEX idx_offspring_status ON offspring_proposals(status);
CREATE INDEX idx_price_history_lookup ON price_history(chain, token_pair, recorded_at);
CREATE INDEX idx_intel_channel ON intel_log(channel, created_at);
CREATE INDEX idx_postmortems_agent ON postmortems(agent_id);
CREATE INDEX idx_lineage_parent ON lineage(parent_id);
CREATE INDEX idx_knowledge_entities ON market_knowledge(entity_a, entity_b);
CREATE INDEX idx_signal_outcomes_source ON signal_outcomes(source_agent_id);
```

---

## Project structure

```
swarm/
├── go.mod
├── go.sum
├── .env.example
├── Dockerfile
├── cmd/
│   ├── swarm/
│   │   └── main.go                 # Main entry: SwarmRuntime + API server + Telegram + PriceCollector
│   ├── init-treasury/
│   │   └── main.go                 # One-time: create + encrypt treasury wallets (both chains)
│   ├── seed-nodes/
│   │   └── main.go                 # One-time: create initial funded + shadow nodes
│   ├── devnet-fund/
│   │   └── main.go                 # Helper: airdrop devnet SOL + Base Sepolia ETH
│   └── backtest/
│       └── main.go                 # Manual backtest runner
│
├── internal/
│   ├── config/
│   │   └── config.go               # Env var loading, typed config struct
│   │
│   ├── db/
│   │   ├── db.go                   # pgx pool setup
│   │   ├── queries.go              # All database query functions
│   │   └── migrations/             # SQL migration files
│   │
│   ├── chain/
│   │   ├── interface.go            # ChainClient interface
│   │   ├── solana.go               # Solana implementation (solana-go, Jupiter)
│   │   ├── base.go                 # Base implementation (go-ethereum, 1inch)
│   │   ├── prices.go               # Shared price cache across chains
│   │   └── registry.go             # Chain registry + factory
│   │
│   ├── llm/
│   │   ├── interface.go            # LLMClient interface
│   │   ├── anthropic.go            # Claude models
│   │   ├── openai.go               # GPT models
│   │   └── registry.go             # LLM registry + factory
│   │
│   ├── agent/
│   │   ├── genome.go               # AgentGenome + all config structs
│   │   ├── wallet.go               # Multi-chain keypair gen + AES encryption
│   │   ├── strategist.go           # LLM call, response parsing, cost tracking
│   │   ├── bandit.go               # TickBandit (Thompson Sampling)
│   │   ├── learned_rules.go        # Rule CRUD, inheritance, eviction
│   │   └── tasks/
│   │       ├── interface.go        # Task interface + Trade struct
│   │       ├── registry.go         # TaskRegistry + factory
│   │       ├── cross_chain_yield.go
│   │       ├── liquidity_provision.go
│   │       ├── liquidation_hunting.go
│   │       └── momentum.go
│   │
│   ├── runtime/
│   │   ├── swarm.go                # SwarmRuntime — shared goroutine manager
│   │   ├── node_runner.go          # NodeRunner — per-node goroutine loops
│   │   ├── lifecycle.go            # LifecycleManager — spawn/kill/promote/demote
│   │   ├── circuit_breaker.go      # Global halt mechanism
│   │   ├── hibernation.go          # Shadow node sleep/wake scheduling
│   │   └── scheduler.go            # Staggered scheduling + backpressure
│   │
│   ├── orchestrator/
│   │   ├── parent.go               # Root orchestrator + epoch evaluation
│   │   ├── evolution.go            # Mutation/crossover/selection operators
│   │   ├── adversarial.go          # Bull/Bear evaluation
│   │   ├── quality_check.go        # LLM quality validation of offspring
│   │   ├── diversity.go            # Diversity scoring and maintenance
│   │   ├── treasury.go             # Capital allocation, rent, sweeps
│   │   ├── budget.go               # Monthly budget tracking + fallback
│   │   ├── economics.go            # Per-node P&L settlement
│   │   ├── postmortem.go           # Structured postmortem generation
│   │   └── retention.go            # Data retention cleanup
│   │
│   ├── backtesting/
│   │   ├── engine.go               # BacktestEngine — mock chain, task replay
│   │   ├── mock_chain.go           # Mock ChainClient replaying price history
│   │   └── price_collector.go      # Background price data collection goroutine
│   │
│   ├── intelligence/
│   │   ├── bus.go                   # Intel pub/sub with Bull/Bear channels
│   │   ├── aggregator.go           # Summarize recent intel for strategist
│   │   ├── accuracy_tracker.go     # Source accuracy + emergent authority
│   │   └── knowledge_graph.go      # Market relationship graph
│   │
│   ├── notifications/
│   │   ├── telegram.go             # Telegram bot notifier
│   │   └── formatters.go           # Event → message formatting
│   │
│   ├── security/
│   │   ├── tx_validator.go         # Pre-submission simulation + validation
│   │   ├── key_manager.go          # AES-256-GCM encrypt/decrypt
│   │   └── log_redactor.go         # zap field redactor
│   │
│   └── api/
│       ├── server.go               # Gin router setup + middleware
│       ├── handlers/
│       │   ├── agents.go
│       │   ├── trades.go
│       │   ├── epochs.go
│       │   ├── lineage.go
│       │   ├── treasury.go
│       │   ├── postmortems.go
│       │   ├── offspring.go
│       │   ├── budget.go
│       │   ├── circuit_breaker.go
│       │   ├── backtests.go
│       │   ├── intelligence.go
│       │   ├── models.go           # LLM model performance stats
│       │   ├── evolution.go
│       │   └── dashboard.go
│       └── websocket.go            # WebSocket event relay from Redis
│
├── dashboard/                       # Next.js app
│   ├── package.json
│   ├── next.config.js
│   ├── tailwind.config.ts
│   ├── tsconfig.json
│   ├── app/
│   │   ├── layout.tsx
│   │   ├── page.tsx                # Overview dashboard
│   │   ├── agents/
│   │   │   ├── page.tsx            # Agent list
│   │   │   └── [id]/
│   │   │       ├── page.tsx        # Agent detail
│   │   │       └── brain/
│   │   │           └── page.tsx    # Strategist prompt + decisions + bandit
│   │   ├── lineage/
│   │   │   └── page.tsx
│   │   ├── trades/
│   │   │   └── page.tsx
│   │   ├── epochs/
│   │   │   └── page.tsx
│   │   ├── postmortems/
│   │   │   └── page.tsx
│   │   ├── offspring/
│   │   │   └── page.tsx            # Proposals with bull/bear arguments
│   │   ├── intelligence/
│   │   │   └── page.tsx            # Intel feed + accuracy rankings
│   │   ├── models/
│   │   │   └── page.tsx            # LLM model performance comparison
│   │   ├── budget/
│   │   │   └── page.tsx
│   │   ├── backtests/
│   │   │   └── page.tsx
│   │   ├── evolution/
│   │   │   └── page.tsx            # Mutation/crossover genealogy
│   │   └── diversity/
│   │       └── page.tsx
│   ├── components/
│   │   ├── layout/
│   │   │   ├── Sidebar.tsx
│   │   │   └── Header.tsx
│   │   └── shared/
│   │       ├── EquityCurve.tsx
│   │       ├── StatusPill.tsx
│   │       └── ChainBadge.tsx
│   ├── hooks/
│   │   ├── useAgents.ts
│   │   ├── useWebSocket.ts
│   │   ├── useTreasury.ts
│   │   └── useEpochs.ts
│   ├── lib/
│   │   └── api.ts                  # API client + react-query config
│   └── types/
│       └── index.ts
│
└── tests/
    ├── genome_test.go
    ├── evolution_test.go
    ├── quality_check_test.go
    ├── adversarial_test.go
    ├── bandit_test.go
    ├── economics_test.go
    ├── budget_test.go
    ├── circuit_breaker_test.go
    ├── strategist_test.go
    ├── intel_bus_test.go
    ├── chain_clients_test.go
    ├── llm_clients_test.go
    ├── backtesting_test.go
    ├── tx_validator_test.go
    ├── tasks/
    │   ├── cross_chain_yield_test.go
    │   ├── liquidity_provision_test.go
    │   ├── liquidation_hunting_test.go
    │   └── momentum_test.go
    ├── lifecycle_test.go
    ├── profit_sweeps_test.go
    ├── telegram_test.go
    ├── diversity_test.go
    ├── learned_rules_test.go
    ├── knowledge_graph_test.go
    └── epoch_test.go
```

---

## Build order

### Phase 1: Foundation
1. Go module init, config loading, .env.example with all keys
2. Database: pgx pool setup, SQL migrations for all tables
3. Chain abstraction: ChainClient interface, chain registry
4. Solana client: solana-go wrapper, Jupiter quotes/swaps, wallet management
5. Base client: go-ethereum wrapper, 1inch quotes/swaps, EVM wallet management
6. LLM abstraction: LLMClient interface, Anthropic client, OpenAI client, registry
7. Security: AES-256-GCM key manager, zap log redactor, transaction validator
8. Genome model: all Go structs for genome, configs, policies
9. Redis client: pub/sub wrapper, key-value state helpers
10. Treasury init + devnet funding commands

### Phase 2: Runtime core
11. Task interface, Trade struct, task registry
12. TickBandit (Thompson Sampling)
13. CrossChainYieldTask — highest priority, most proven revenue
14. LiquidityProvisionTask
15. Price monitoring via Jupiter + 1inch with Redis caching
16. NodeRunner: goroutine-based two-timescale loops (bandit monitor + reflective strategist + heartbeat)
17. Strategist module: simple reflective LLM call, response validation, cost tracking
18. SwarmRuntime: goroutine manager, staggered scheduling, backpressure via channels
19. LifecycleManager: materialize/kill/pause/promote/demote nodes

### Phase 3: Economics + evolution
20. Economics module: per-node cost attribution, realized net P&L
21. Treasury: capital allocation, rent, profit sweeps
22. Budget tracker: monthly spend, fallback degradation
23. Formal mutation operators (per-module, from EvoAgent)
24. Crossover operator (module-level recombination)
25. LLM quality check for offspring
26. Adversarial Bull/Bear evaluation for offspring
27. Diversity scoring and maintenance
28. Kill/pause policy enforcement
29. Postmortem generation with structured lessons
30. Root orchestrator: epoch evaluation with crossover identification
31. Circuit breaker

### Phase 4: Intelligence + backtesting
32. Intelligence bus: Bull/Bear channels, structured signals
33. Signal accuracy tracker + emergent authority
34. Market knowledge graph
35. Intel aggregator for strategist context
36. Learned rules module: CRUD, confidence tracking, eviction, inheritance
37. Price collector goroutine: 1-minute candles
38. BacktestEngine: mock chain client, task replay
39. Backtest integration: pre-promotion and offspring validation

### Phase 5: Remaining tasks
40. LiquidationHuntingTask (Solana + Base)
41. MomentumTask
42. Shadow node hibernation scheduler

### Phase 6: Notifications + API + dashboard
43. Telegram notifier: event subscription, formatting, throttling, daily digest
44. Gin API server with all handlers
45. WebSocket event relay
46. Next.js dashboard: scaffolding, layout, overview page
47. Core pages: agents list, agent detail, trade feed
48. Advanced pages: lineage tree, evolution history, diversity dashboard
49. Research-informed pages: offspring log (with bull/bear), postmortems (with failure categories), model performance, intelligence feed (with accuracy scores), budget dashboard

### Phase 7: Integration + validation
50. Main cmd/swarm entry point — starts SwarmRuntime + API + Telegram + PriceCollector
51. Seed command: initial nodes on both chains
52. End-to-end devnet test: full lifecycle
53. Cross-chain yield test: verify rate scanning across Solana + Base lending protocols
54. Evolution validation: crossover coherence, quality check catches degenerate mutations
55. Adversarial evaluation test: bull/bear improves offspring quality
56. Bandit validation: Thompson Sampling converges
57. Learned rules validation: accumulate, evict, inherit correctly
58. Knowledge graph validation: edges accumulate, decay, contradictions reduce strength
59. Signal accuracy test: consuming agents weight by source accuracy
60. Shadow hibernation test: sleep/wake works, compute savings match
61. Budget compliance: 20+ nodes under $100/month
62. Backtest validation: compare shadow results against backtest predictions
63. Hot-reload test: deploy new task type without interruption
64. Security test: transaction simulation catches malformed transactions
65. Telegram test: all event types deliver
66. Diversity test: swarm maintains diversity under selection pressure

---

## Critical implementation constraints

- **Devnet only for first 30 days.** `NETWORK` env var controls everything.
- **Go for the entire backend.** Single compiled binary. Goroutines for all concurrency.
- **Next.js for the dashboard.** App Router, TypeScript, Tailwind, shadcn/ui.
- **Yield optimization is the primary revenue strategy.** Cross-chain yield and LP are the first tasks built and tested.
- **Simplicity over complexity in the strategist (AEL).** One LLM call, reflective diagnosis, one adjustment.
- **Formal evolution operators (EvoAgent).** Mutation, crossover, selection — explicit, not ad-hoc.
- **Adversarial offspring evaluation (TradingAgents).** Bull/Bear debate for every proposal.
- **Postmortems are institutional memory.** Structured lessons propagate to descendants.
- **Learned rules are procedural memory (Hermes).** Max 10 per node, inherited, lowest-confidence evicted.
- **Shadow nodes hibernate (Hermes).** Sleep/wake scheduling reduces compute ~70%.
- **Knowledge graph edges validated (MiroFish).** Decay unvalidated, reduce contradicted.
- **Signal authority emerges, never programmed (MiroFish).** Weight by tracked accuracy.
- **Diversity is a first-class metric.** Resist convergence.
- **Every LLM call cost-attributed.** Including quality checks, adversarial evaluations.
- **Transaction simulation before every mainnet submission.**
- **Never log private keys.** zap field redactor on all output.
- **Every strategist response validated defensively.** Clamp, reject, no-op fallback.
- **Every descendant must pass: solvency → quality check → adversarial review → optional backtest.**
- **No reproduction from unrealized gains or while carrying debt.**
- **Economic guardrails are primary.** Hard caps are safety valves only.
- **One shared goroutine runtime.** No per-node OS processes.
- **Graceful degradation on budget breach.**
- **Multi-tenant ready.** Dependency injection, no package-level globals.
- **Chain-agnostic and model-agnostic interfaces.**
- **Hot-reload safe.** All state in Postgres. Bandit state and learned rules persisted.
- **Price collection starts day 1.**
- **Shadow slippage simulation.** Paper trading must not be over-optimistic.
- **Bull/Bear tagging on all intel signals.**

---

## Research references

1. Yuan et al. "EvoAgent: Towards Automatic Multi-Agent Generation via Evolutionary Algorithms." arXiv:2406.14228, 2024.
2. Xiao et al. "TradingAgents: Multi-Agents LLM Financial Trading Framework." arXiv:2412.20138, UCLA/MIT, 2024.
3. Xu et al. "AEL: Agent Evolving Learning for Open-Ended Environments." arXiv:2604.21725, 2026.
4. "Lark: Biologically Inspired Neuroevolution for Multi-Stakeholder LLM Agents." NeurIPS 2025 ER Workshop.
5. "Autonomous Agents on Blockchains: Standards, Execution Models, and Trust Boundaries." arXiv:2601.04583, 2026.
6. "A Survey of Self-Evolving Agents: On Path to Artificial Super Intelligence." arXiv:2507.21046, 2025.
7. Luo et al. "LLM-Powered Multi-Agent System for Automated Crypto Portfolio Management." arXiv:2501.00826, 2025.
8. Nous Research. "Hermes Agent." github.com/NousResearch/hermes-agent, 2026.
9. Guo Hangjiang. "MiroFish: A Simple and Universal Swarm Intelligence Engine." github.com/666ghj/MiroFish, 2026.
10. DWF Labs / Cornell. "AI Outperforms Humans in DeFi Yield Optimization, Autonomous Trading Lags Behind by 5x." Odaily, 2026.
11. Cobo. "AI DeFi: Autonomous Agents Revolutionizing Yield Optimization & Liquidity Management." Q1 2026 data.

---

## Final build goal

A working system where a root treasury funds initial nodes across Solana and Base, nodes generate revenue primarily through cross-chain yield optimization and liquidity provision (the empirically proven strategies for AI agents), a Thompson Sampling bandit handles fast-timescale policy exploration while a simple reflective strategist handles slow-timescale adaptation, agents accumulate learned rules as procedural memory, agents share bull/bear-tagged intelligence with emergent authority based on tracked signal accuracy, the swarm collectively builds a market knowledge graph, shadow nodes hibernate to minimize compute, profitable nodes sweep value upward and design economically justified descendants, offspring pass formal quality checks and adversarial Bull/Bear evaluation, crossover between successful nodes recombines the best traits, weak nodes receive structured postmortems that become institutional memory, backtesting validates shadows before promotion, diversity is maintained across task types and chains and models, the Go backend runs reliably as a single compiled binary managing all nodes via goroutines, the Next.js dashboard clearly shows lineage trees, evolution history, knowledge graph, signal authority, diversity metrics, and why each node lived, reproduced, or died, and the operator receives Telegram alerts and only intervenes when they choose to.
