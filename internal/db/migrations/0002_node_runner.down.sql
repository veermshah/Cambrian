DROP INDEX IF EXISTS idx_agents_heartbeat;
ALTER TABLE agents DROP COLUMN IF EXISTS last_heartbeat_at;
