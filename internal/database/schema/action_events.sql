-- Durable action execution identity.
-- This file is the immutable body of migration 20260827_durable_action_consumer.

ALTER TABLE action_execution_logs ADD COLUMN durable_event_key TEXT;
CREATE UNIQUE INDEX uq_action_execution_logs_durable_target
	ON action_execution_logs(durable_event_key, action_id)
	WHERE durable_event_key IS NOT NULL;
