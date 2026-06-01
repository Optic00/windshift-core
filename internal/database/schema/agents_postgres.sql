-- Coding-agent harness state (Postgres). See schema/agents.sql for the
-- SQLite version + rationale.

CREATE TABLE IF NOT EXISTS agent_runs (
    id SERIAL PRIMARY KEY,
    workspace_id INTEGER NOT NULL,
    item_id INTEGER,
    binding_id INTEGER, -- soft ref to workspace_agent_bindings; agent_runs must outlive bindings for audit
    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued','running','succeeded','failed','canceled','killed')),
    queued_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMPTZ,
    ended_at TIMESTAMPTZ,
    container_id TEXT,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (item_id) REFERENCES items(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_runs_workspace_queued ON agent_runs(workspace_id, queued_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_runs_status ON agent_runs(status);
CREATE INDEX IF NOT EXISTS idx_agent_runs_item_id ON agent_runs(item_id);
CREATE INDEX IF NOT EXISTS idx_agent_runs_binding_created ON agent_runs(binding_id, created_at DESC);

CREATE TABLE IF NOT EXISTS agent_run_events (
    id BIGSERIAL PRIMARY KEY,
    run_id INTEGER NOT NULL,
    ts TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    type TEXT NOT NULL,
    payload_json JSONB NOT NULL DEFAULT '{}'::JSONB,
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_agent_run_events_run ON agent_run_events(run_id, id);

CREATE TABLE IF NOT EXISTS global_agent_acting_user_allowlist (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id INTEGER REFERENCES workspaces(id) ON DELETE CASCADE,
    reason TEXT NOT NULL DEFAULT '',
    created_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_global_agent_acting_user_allowlist_unique
    ON global_agent_acting_user_allowlist(user_id, COALESCE(workspace_id, 0));
CREATE INDEX IF NOT EXISTS idx_global_agent_acting_user_allowlist_workspace
    ON global_agent_acting_user_allowlist(workspace_id) WHERE workspace_id IS NOT NULL;

INSERT INTO system_settings(key, value, value_type, description, category)
VALUES (
    'agents.allow_centralized_service_users',
    'false',
    'boolean',
    'Allow workspace admins to bind coding-agent runs to centralized service users (impersonation gate, WI-87).',
    'security'
) ON CONFLICT (key) DO NOTHING;

INSERT INTO system_settings(key, value, value_type, description, category)
VALUES (
    'allow_user_managed_agents',
    'false',
    'boolean',
    'Allow non-admin users to create and manage their own agent users from their profile',
    'security'
) ON CONFLICT (key) DO NOTHING;

INSERT INTO system_settings(key, value, value_type, description, category)
VALUES (
    'max_agents_per_user',
    '5',
    'integer',
    'Maximum number of owned agents a single non-admin user may create',
    'security'
) ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS workspace_agent_bindings (
    id SERIAL PRIMARY KEY,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    acting_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    acting_user_kind TEXT NOT NULL
        CHECK (acting_user_kind IN ('agent','centralized_service')),
    repo_slug TEXT,
    repo_base_ref TEXT,
    llm_connection_id INTEGER,
    token_scopes_json JSONB NOT NULL DEFAULT '[]'::JSONB,
    token_ttl_minutes INTEGER NOT NULL DEFAULT 60,
    max_runs_per_day INTEGER NOT NULL DEFAULT 0,
    scm_connection_id INTEGER REFERENCES workspace_scm_connections(id) ON DELETE SET NULL,
    created_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_agent_bindings_workspace_acting
    ON workspace_agent_bindings(workspace_id, acting_user_id);
CREATE INDEX IF NOT EXISTS idx_workspace_agent_bindings_workspace
    ON workspace_agent_bindings(workspace_id);
CREATE INDEX IF NOT EXISTS idx_workspace_agent_bindings_scm_connection
    ON workspace_agent_bindings(scm_connection_id);
