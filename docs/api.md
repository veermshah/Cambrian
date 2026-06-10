# Cambrian API

Read-only HTTP + WebSocket surface for the dashboard.
The swarm runtime has no public mutations — manual interventions are
CLI-driven (`cmd/init-treasury`, `cmd/seed-nodes`, `cmd/kill-agent`).

## Conventions

- **Base URL**: `http://localhost:8080` in dev. Production runs behind a TLS
  reverse proxy.
- **Auth**: every `/api/*` and `/ws` request must carry `X-Api-Key: <key>`.
  WebSocket clients that can't set headers may pass `?api_key=<key>` instead.
  `/healthz` is unauthenticated.
- **CORS**: dashboard origin (`http://localhost:3000` by default) is allowed.
- **List shape**: every list endpoint returns `{ "items": [...], "count": N }`.
  The wrapper makes it trivial to add pagination later without breaking the
  dashboard.
- **Times**: ISO-8601 UTC with millisecond precision.
- **Money**: numbers in USD unless the field name says otherwise. Field
  names follow `_usd` / `_pct` suffix conventions.
- **Errors**: non-2xx responses return `{ "error": "<short reason>" }`.

## REST endpoints

### `GET /healthz`
Liveness probe. `200 {"ok": true}`. No auth.

### `GET /api/agents?chain=&node_class=&status=&task_type=`
List agents, ordered by `total_pnl_usd` descending. Returns `AgentSummary` rows.

Sample:

```json
{
  "items": [
    {
      "id": "ag-1",
      "name": "scout-7",
      "chain": "solana",
      "task_type": "momentum",
      "node_class": "funded",
      "status": "active",
      "health_state": "healthy",
      "generation": 3,
      "lineage_depth": 2,
      "capital_usd": 100,
      "current_usd": 142.5,
      "total_pnl_usd": 42.5,
      "total_trades": 27,
      "strategy_model": "claude-haiku-4-5-20251001",
      "created_at": "2026-06-01T00:00:00Z"
    }
  ],
  "count": 1
}
```

### `GET /api/agents/:id`
Per-agent detail: summary + strategist prompt + strategy_config + learned
rules + last 25 trades. Returns `AgentDetail` or `404 {"error":"not found"}`.

### `GET /api/trades?agent_id=&chain=&limit=`
Most recent trades, default limit 100, max 1000. Joined to agent so the
table can show the agent name without a second round-trip.

### `GET /api/epochs?limit=`
Epoch timeline, newest first. Max 500.

### `GET /api/lineage`
Every agent + parent edges, used to draw the family tree.

### `GET /api/treasury`
Root reserve + total capital allocated + per-chain breakdown.

### `GET /api/postmortems?limit=`
Killed-agent retrospectives, newest first.

### `GET /api/offspring?status=`
Pending and historical proposals with bull/bear and synthesis text.
`status` is one of `pending`, `quality_check`, `adversarial_review`,
`approved`, `rejected`, `materialized`.

### `GET /api/budget`
Monthly spend curve + per-category (llm/infra/rpc) and per-agent breakdown.

### `GET /api/circuit-breaker`
Whether the global breaker is currently armed or tripped. Derived from the
most recent epoch row.

### `GET /api/backtests?limit=`
Recent backtest runs with equity curves and Sharpe / drawdown / win-rate.

### `GET /api/intelligence?channel=&sentiment=&limit=`
Recent intel-bus messages. The dashboard uses this for the historical feed
and the WebSocket for live updates.

### `GET /api/models`
Per-LLM-model performance comparison: P&L per dollar spent, used-by
agent count, average token usage.

### `GET /api/evolution?limit=`
Mutation / crossover events as parent → child rows.

### `GET /api/dashboard`
Single snapshot for the overview page: totals + treasury + equity curve
(last 50 epochs) + most recent epoch + open-proposal count.

## WebSocket `GET /ws`

Upgrade to WebSocket. Auth: `X-Api-Key` header or `?api_key=` query.

The server pushes every Redis `events:*` message to every connected
client, wrapped as:

```json
{
  "channel": "events:circuit_breaker",
  "payload": { "reason": "market_crash", "at": "2026-06-09T12:00:00Z" },
  "at": "2026-06-09T12:00:00.123Z"
}
```

- Client → server frames are ignored (listen-only).
- 30-second ping heartbeat; client must respond with pong.
- Slow clients are disconnected — reconnect with backoff.

Channels currently relayed:

| Channel                  | Notes                                    |
|--------------------------|------------------------------------------|
| `events:circuit_breaker` | Global breaker trip / reset              |
| `events:lifecycle`       | Spawn / kill / promote events            |
| `events:epoch_completed` | One per epoch close                      |
| `events:budget`          | Monthly spend warnings                   |
| `events:intel`           | Live intel-bus mirror                    |
| `events:trade`           | Live trade fills                         |

## Error codes

| Code | Meaning                                       |
|------|-----------------------------------------------|
| 200  | OK                                            |
| 204  | Successful preflight (CORS OPTIONS)           |
| 401  | Missing / wrong `X-Api-Key`                   |
| 404  | Resource not found (`/api/agents/:id`)        |
| 500  | Internal — the body carries the error string  |
| 503  | WebSocket disabled in server config           |
