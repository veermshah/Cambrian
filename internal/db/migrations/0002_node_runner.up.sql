-- Chunk 14: NodeRunner heartbeat column.
-- The heartbeat loop writes agents.last_heartbeat_at every 30s so the
-- root orchestrator and dashboard can tell which goroutines are alive
-- after a hot-reload / restart.

ALTER TABLE agents
    ADD COLUMN last_heartbeat_at TIMESTAMPTZ;

CREATE INDEX idx_agents_heartbeat ON agents(last_heartbeat_at)
    WHERE status = 'active';
