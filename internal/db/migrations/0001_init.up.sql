-- Cambrian initial schema. Source of truth: spec lines 645-913.
-- Tables are ordered so every FK target exists before its referencing table:
--   agents -> epochs -> backtest_results -> trades -> strategist_decisions
--   -> agent_ledgers -> offspring_proposals -> postmortems -> profit_sweeps
--   -> lineage -> price_history -> intel_log -> market_knowledge
--   -> signal_outcomes
-- pgcrypto provides gen_random_uuid(); Supabase has it available but does not
-- preinstall it on a fresh project, so we enable it explicitly.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

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
