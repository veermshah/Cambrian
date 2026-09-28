# Cambrian Arena — pivot plan

**Goal.** Turn Cambrian from a devnet DeFi swarm into an open-ended
*agent-evolution benchmark*: a reproducible harness where populations of
LLM agents mutate, cross over, inherit learned rules and compete under a
hard dollar budget on verifiable tasks — and where the *selection
algorithm itself* is the thing being measured. The headline result we
want: "quality-diversity archive selection with an economics layer
reaches X% on task suite T at Y% of the LLM cost of greedy hill-climbing
/ random restarts." That is squarely in the DGM / AlphaEvolve /
ShinkaEvolve / OpenEvolve research line and is what makes this a hiring
artifact rather than a hobby bot.

## 1. What Cambrian is today (and what carries over)

| Layer | Today (DeFi) | Arena reuse |
|---|---|---|
| `agent.AgentGenome` | task type, chain, strategist prompt/model, bandit policies, learned rules, economics/comms/sleep policies | **Keep.** `Chain` becomes the *task suite*, `TaskType` the *task family*; economics stay as-is. |
| `evolution` (`Mutate`, `Crossover`, `Inherit`) | module-wise mutation, performance-weighted crossover, rule inheritance with confidence regression | **Keep verbatim.** |
| `orchestrator/reproduction.go` (chunk 34) | QD niche archive, DGM-style fitness × 1/(1+children) × novelty parent sampling, budget freeze | **Keep verbatim** — this is the paper's contribution. |
| Offspring pipeline | solvency → LLM quality check → Bull/Bear adversarial → backtest | **Keep.** Backtest becomes "held-out eval split". |
| Ledger / rent / kill-pause / circuit breaker | USD P&L from devnet trades | **Keep shape**, replace P&L with *task reward − LLM cost*. |
| `runtime` (NodeRunner, lifecycle, hibernation) | goroutine per agent, Redis lifecycle events | **Keep.** |
| `chain/*` clients, wallets, tx validator | Solana/Base devnet | **Drop from Arena build tag.** Wallet becomes a no-op "identity". |
| Postgres schema | agents, ledger, lineage, offspring_proposals, epochs | **Keep**, add `arena_runs`, `task_results`. |
| Dashboard / API | P&L, agents, lineage tree | **Keep**, add leaderboard + archive heatmap. |

Net: the pivot is mostly *deleting* the chain layer and adding a
`Task` adapter interface + a `Fitness` function. No rewrite.

## 2. Architecture delta

```
internal/arena/
  task.go        // Task, Episode, Verdict interfaces
  adapters/      // one package per task suite
    humaneval/   // verifiable code tasks (tests as oracle)
    swebench/    // SWE-bench-Lite subset via docker harness
    arc/         // ARC-AGI style grid puzzles (exact-match)
    kernels/     // (later) Triton kernel speedup via dryft
  fitness.go     // reward, cost, net "USD", per-niche normalization
  runner.go      // Episode loop: genome → agent → task → verdict → ledger row
  baselines.go   // greedy, random-restart, no-archive, no-crossover ablations
  report.go      // lineage tree, archive heatmap, cost curves → markdown/json
cmd/arena/       // `arena run --suite humaneval --selector qd --budget 20`
```

### Task adapter contract

```go
type Task interface {
    ID() string
    Family() string                        // maps to genome.TaskType
    Prompt(ctx, g agent.AgentGenome) (string, error)
    Verify(ctx, output string) (Verdict, error) // deterministic oracle
}

type Verdict struct {
    Score   float64 // [0,1]; 1 = all hidden tests pass
    CostUSD float64 // LLM tokens billed via llm.RateFor
    Trace   json.RawMessage
}
```

### Fitness → ledger mapping

`RealizedNetProfit = Σ(Score × RewardUSDPerTask) − LLMCost − Rent`.
`RewardUSDPerTask` is a run parameter (e.g. $0.50) so every existing
economics gate — kill at insolvency, pause on stop-loss, pre-funded
reproduction, `BudgetTight` freeze — works unchanged. This is the
differentiator vs. DGM/OpenEvolve: **selection under an explicit
cost ceiling**, not just max-score.

### Selector variants (the experiment axis)

Wire `RootOrchestratorConfig.Reproduction` + a `Selector` enum:

| Selector | Description |
|---|---|
| `qd` (ours) | niche archive + fitness/children/novelty weights (chunk 34) |
| `greedy` | always mutate the top-k by fitness |
| `random` | uniform parent sampling |
| `no_crossover` | qd with `CrossoverProbability = 0` |
| `no_inherit` | qd without learned-rule inheritance |
| `no_economics` | qd, rent = 0, no kill/pause (pure DGM) |

## 3. Benchmark protocol

1. **Seed population**: 8 genomes per (family × model) niche, models
   drawn from the existing registry (Haiku 4.5, GPT-4o-mini …).
2. **Epoch**: every agent attempts `N` tasks sampled without replacement
   from the *train* split; ledger settles; offspring pipeline runs;
   proposer generates children. Fixed RNG seed per run.
3. **Budget**: run stops when cumulative LLM spend hits `B` dollars (e.g.
   $20) — cost, not wall clock, is the x-axis.
4. **Eval**: every 5 epochs, evaluate the archive's elites on a *held-out*
   split (this reuses the `Backtest` hook). Report best-of-archive and
   mean-of-archive.
5. **Metrics**: pass@1 on held-out vs. cumulative $; archive coverage
   (filled niches / total); lineage depth of the best agent; fraction of
   improvements from crossover vs. mutation; learned-rule survival.
6. **Repeats**: 5 seeds per selector; report mean ± std.

Reproducibility: `arena run` writes `runs/<id>/config.json`, the full
proposal + decision log, and a `report.md`. Everything replays from
Postgres + seed; the LLM transcripts are cached by (model, prompt hash).

## 4. Milestones (each ≈ one Devin session)

1. **Arena skeleton** — `internal/arena` interfaces, `humaneval` adapter
   (164 problems, local pytest oracle), `cmd/arena run` on a
   `FakeLLMClient`, ledger mapping, chain layer behind `//go:build defi`.
2. **Selectors + baselines** — selector enum, ablations, deterministic
   run harness, `report.md` generator with archive heatmap + lineage tree.
3. **Real LLM runs** — 5 seeds × 6 selectors × $20 on HumanEval+/MBPP;
   first results table; dashboard leaderboard page.
4. **Harder suite** — SWE-bench-Lite (30-problem subset) docker adapter;
   or the `dryft` Triton-kernel adapter (fitness = measured speedup).
5. **Write-up** — 6–8 page paper-style report + blog post + 2-min demo;
   publish runs; README rewrite around Arena.

## 5. Hosting / deployment

The Arena is *batch*, not always-on, so hosting is cheaper than the swarm:

- **Runs**: a single VM (Hetzner CX22 / DO 2 vCPU, ~$6–12/mo) or your
  laptop; `docker compose up postgres redis` + `go run ./cmd/arena`.
  SWE-bench adapter needs Docker-in-Docker → prefer a 4-vCPU VM for that
  milestone.
- **Public leaderboard + dashboard**: Fly.io / Railway (~$10–15/mo for
  the Go API + managed Postgres), Next.js dashboard on Vercel free tier.
- **Secrets needed**: `ANTHROPIC_API_KEY` (and optionally
  `OPENAI_API_KEY`), `DATABASE_URL`, `REDIS_URL`, `MASTER_ENCRYPTION_KEY`
  (still required by config; wallets become identities). No RPC keys.
- **LLM spend** dominates: the milestone-3 grid is ≈ 30 runs × $20 =
  $600 at list price; shrink to 3 seeds × 4 selectors × $10 ≈ $120 for a
  first pass, and use Haiku-class models.

What I need from you when we get to milestone 3: an Anthropic key (and
a spend cap on it), and a decision on the first VM/provider. Nothing is
needed for milestones 1–2 — they run entirely on `FakeLLMClient`.
