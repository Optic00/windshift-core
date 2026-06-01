-- Coding-agent harness state.
--
-- Folded into the fresh-install schema concat so a brand-new DB has these
-- tables without needing the catalog migrations to run. The matching catalog
-- entries (20260529_agent_runs, _agent_security_allowlist,
-- _workspace_agent_bindings, _workspace_agent_bindings_scm_connection) keep
-- their CheckSQLite predicates so they stamp without re-running on a fresh
-- install and still upgrade older DBs that predate this file.
--
-- See migrations.go for the WI references behind each table:
--   - agent_runs / agent_run_events: run-lifecycle + SSE event stream (WI-134)
--   - global_agent_acting_user_allowlist: acting-identity gate (WI-87)
--   - workspace_agent_bindings: workspace-admin-managed binding (WI-88)
--   - workspace_agent_bindings.scm_connection_id: SCM connection FK (WI-90)

CREATE TABLE IF NOT EXISTS agent_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id INTEGER NOT NULL,
    item_id INTEGER,
    binding_id INTEGER, -- soft ref to workspace_agent_bindings; agent_runs must outlive bindings for audit
    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued','running','succeeded','failed','canceled','killed')),
    queued_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at DATETIME,
    ended_at DATETIME,
    container_id TEXT,
    error TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (item_id) REFERENCES items(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_runs_workspace_queued ON agent_runs(workspace_id, queued_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_runs_status ON agent_runs(status);
CREATE INDEX IF NOT EXISTS idx_agent_runs_item_id ON agent_runs(item_id);
CREATE INDEX IF NOT EXISTS idx_agent_runs_binding_created ON agent_runs(binding_id, created_at DESC);

CREATE TABLE IF NOT EXISTS agent_run_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL,
    ts DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    type TEXT NOT NULL,
    payload_json TEXT NOT NULL DEFAULT '{}',
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_agent_run_events_run ON agent_run_events(run_id, id);

CREATE TABLE IF NOT EXISTS global_agent_acting_user_allowlist (
    user_id INTEGER NOT NULL,
    workspace_id INTEGER,
    reason TEXT NOT NULL DEFAULT '',
    created_by_user_id INTEGER,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (created_by_user_id) REFERENCES users(id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_global_agent_acting_user_allowlist_unique
    ON global_agent_acting_user_allowlist(user_id, COALESCE(workspace_id, 0));
CREATE INDEX IF NOT EXISTS idx_global_agent_acting_user_allowlist_workspace
    ON global_agent_acting_user_allowlist(workspace_id);

INSERT OR IGNORE INTO system_settings(key, value, value_type, description, category)
VALUES (
    'agents.allow_centralized_service_users',
    'false',
    'boolean',
    'Allow workspace admins to bind coding-agent runs to centralized service users (impersonation gate, WI-87).',
    'security'
);

CREATE TABLE IF NOT EXISTS workspace_agent_bindings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id INTEGER NOT NULL,
    acting_user_id INTEGER NOT NULL,
    acting_user_kind TEXT NOT NULL
        CHECK (acting_user_kind IN ('agent','centralized_service')),
    repo_slug TEXT,
    repo_base_ref TEXT,
    llm_connection_id INTEGER,
    token_scopes_json TEXT NOT NULL DEFAULT '[]',
    token_ttl_minutes INTEGER NOT NULL DEFAULT 60,
    max_runs_per_day INTEGER NOT NULL DEFAULT 0,
    scm_connection_id INTEGER REFERENCES workspace_scm_connections(id) ON DELETE SET NULL,
    created_by_user_id INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (acting_user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (created_by_user_id) REFERENCES users(id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_agent_bindings_workspace_acting
    ON workspace_agent_bindings(workspace_id, acting_user_id);
CREATE INDEX IF NOT EXISTS idx_workspace_agent_bindings_workspace
    ON workspace_agent_bindings(workspace_id);
CREATE INDEX IF NOT EXISTS idx_workspace_agent_bindings_scm_connection
    ON workspace_agent_bindings(scm_connection_id);
