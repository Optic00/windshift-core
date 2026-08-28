-- Shared durable action cutover and frozen-target state.
CREATE TABLE IF NOT EXISTS action_event_cutovers (
	cutover_key TEXT PRIMARY KEY,
	start_event_id BIGINT NOT NULL CHECK (start_event_id > 0),
	recorded_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS action_event_batches (
	event_key TEXT PRIMARY KEY,
	event_id BIGINT NOT NULL UNIQUE,
	consumer_key TEXT NOT NULL,
	workspace_id INTEGER,
	trigger_event TEXT NOT NULL,
	materialized_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS action_event_targets (
	event_key TEXT NOT NULL,
	action_id INTEGER NOT NULL,
	state TEXT NOT NULL DEFAULT 'pending'
		CHECK (state IN ('pending', 'running', 'failed', 'completed', 'skipped')),
	attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
	last_error TEXT,
	completed_at TIMESTAMPTZ,
	skipped_by_kind TEXT,
	skipped_by_ref TEXT,
	skip_reason TEXT,
	skipped_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (event_key, action_id),
	FOREIGN KEY (event_key) REFERENCES action_event_batches(event_key) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_action_event_targets_state ON action_event_targets(state, updated_at);
CREATE INDEX IF NOT EXISTS idx_action_event_targets_action ON action_event_targets(action_id, created_at);
