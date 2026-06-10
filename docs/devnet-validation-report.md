# Devnet Validation Report

This report tracks the chunk-33 acceptance suite (spec lines 1182–1198). The suite is the gate for considering mainnet enablement.

> **Status:** scaffold landed; pure-logic suites green; resource-gated suites awaiting first live run.

## How to run

```bash
# Pure-logic suites only (default).
make e2e

# Full suite. Requires the env vars enumerated below.
make e2e-live
```

`E2E=1` is the master switch. Individual resource gates check their own env vars on top of that. Skipped tests print the missing env var in their skip message.

### Required env vars for the live run

| Suite group              | Env vars                                                          |
|--------------------------|-------------------------------------------------------------------|
| DB-backed                | `DATABASE_URL` (or `DATABASE_POOL_URL`)                           |
| Redis-backed             | `REDIS_URL`                                                       |
| LLM-backed               | `ANTHROPIC_API_KEY`                                               |
| Devnet RPC               | `HELIUS_DEVNET_URL` + `ALCHEMY_BASE_SEPOLIA_URL`                  |
| Telegram                 | `TELEGRAM_BOT_TOKEN` + `TELEGRAM_CHAT_ID`                         |

## Per-test status

| Spec ref | Test                                                | Mode      | Status          |
|----------|-----------------------------------------------------|-----------|-----------------|
| 1183     | `TestFullLifecycle_24HourSimulatedRun`              | gated     | skip — needs DB+Redis+LLM |
| 1185     | `TestCrossChainYield_RateScanAcrossChains`          | gated     | skip — needs devnet RPCs |
| 1190     | `TestEvolution_MutationAdvancesGeneration`          | pure      | **pass**        |
| 1190     | `TestEvolution_CrossoverCoherence`                  | pure      | **pass**        |
| 1191     | `TestAdversarial_RunsBullBearSynthesis`             | gated     | skip — needs LLM |
| 1192     | `TestBandit_ConvergesToBestArmOverLongHorizon`      | pure      | **pass**        |
| 1192     | `TestLearnedRules_AccumulateAndEvictAtCap`          | pure      | **pass**        |
| 1192     | `TestLearnedRules_InheritRegressesConfidence`       | pure      | **pass**        |
| 1192     | `TestLearnedRules_OutcomeMovesConfidence`           | pure      | **pass**        |
| 1193     | `TestKnowledgeGraph_EdgeAccumulationAndReinforcement` | pure    | **pass**        |
| 1193     | `TestKnowledgeGraph_ContradictionReducesStrength`   | pure      | **pass**        |
| 1193     | `TestKnowledgeGraph_DecayLeavesFreshEdgesAlone`     | pure      | **pass**        |
| 1194     | `TestSignalAccuracy_WeightingByCorrectness`         | pure      | **pass**        |
| 1195     | `TestBudget_TwentyNodesUnderHundredPerMonth`        | pure      | **pass**        |
| 1195     | `TestBudget_PostmortemPathStaysInBudget`            | pure      | **pass**        |
| 1196     | `TestHibernation_ShadowSleepSavingsAtLeast60Pct`    | pure      | **pass**        |
| 1196     | `TestHibernation_FundedAlwaysAwake`                 | pure      | **pass**        |
| 1196     | `TestBacktestValidation_ShadowVsBacktest`           | gated     | skip — needs 30-day trace |
| 1196     | `TestTelegram_EveryEventTypeDelivers`               | gated     | skip — needs Telegram |
| 1197     | `TestDiversity_MaintainedAcrossSelection`           | pure      | **pass**        |
| 1197     | `TestDiversity_BonusRewardsNovelTriples`            | pure      | **pass**        |
| 1197     | `TestHotReload_TaskRegistryAcceptsNewTypeWithoutInterruption` | pure | **pass** |
| 1197     | `TestSecurity_TxValidatorCatchesMalformed`          | pure      | **pass**        |

## Totals

- **Pure-logic, pass:** 18
- **Gated, await live run:** 5

The pure-logic tests run on every `go test ./...` invocation; the gated tests skip cleanly without `E2E=1`. Failure of any pure-logic test should block merge by default — the gated tests are operator-driven.

## Cost note

The only test that issues live LLM calls is `TestAdversarial_RunsBullBearSynthesis` — three calls per run on `claude-haiku-4-5-20251001`, ~$0.01 each. The full live suite is comfortably under $1.

## Next actions

To turn this report from "scaffold" into "ready for mainnet review":

1. Provision the env vars in the table above.
2. Run `make e2e-live` and update the per-test status table with the results.
3. After 30 days of devnet runtime, populate the backtest-validation gap by enabling `TestBacktestValidation_ShadowVsBacktest` — the test currently skips out of caution.
4. Once every row reads **pass**, this report is the artifact a reviewer reads before any mainnet flag is flipped.
