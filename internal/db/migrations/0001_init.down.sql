-- Drop in reverse FK order so dependents fall before their targets.
DROP TABLE IF EXISTS signal_outcomes;
DROP TABLE IF EXISTS market_knowledge;
DROP TABLE IF EXISTS intel_log;
DROP TABLE IF EXISTS price_history;
DROP TABLE IF EXISTS lineage;
DROP TABLE IF EXISTS profit_sweeps;
DROP TABLE IF EXISTS postmortems;
DROP TABLE IF EXISTS offspring_proposals;
DROP TABLE IF EXISTS agent_ledgers;
DROP TABLE IF EXISTS strategist_decisions;
DROP TABLE IF EXISTS trades;
DROP TABLE IF EXISTS backtest_results;
DROP TABLE IF EXISTS epochs;
DROP TABLE IF EXISTS agents;
