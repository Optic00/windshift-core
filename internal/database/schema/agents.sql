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
    runner_id INTEGER, -- soft ref to runner_instances; NULL for the in-process local runner (WI-141)
    target_pool_id INTEGER, -- soft ref to action_capabilities (runner_pool); NULL = local in-process pool (WI-141)
    cancel_requested_at DATETIME, -- set when a running remote run should abort; the runner learns via heartbeat (WI-141)
    grants_json TEXT, -- RunGrants snapshot the access-layer brokers authorize against (WI-144)
    run_token_id INTEGER, -- api_tokens row that binds a presented credential to this run's grants (WI-144)
    triggered_by_user_id INTEGER, -- soft ref to users: who fired the trigger; credential principal for OAuth SCM connections (WI-275)
    job_kind TEXT NOT NULL DEFAULT 'coding_agent', -- coding_agent | action_container | ci_task (WI-146)
    job_image TEXT, -- admin image for action_container/ci_task jobs; NULL for coding_agent (fixed runner image)
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
CREATE INDEX IF NOT EXISTS idx_agent_runs_runner ON agent_runs(runner_id);
-- Supports the remote DB-as-queue claim: next queued run for a pool, oldest first.
CREATE INDEX IF NOT EXISTS idx_agent_runs_pool_claim ON agent_runs(target_pool_id, status, queued_at);

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

INSERT OR IGNORE INTO system_settings(key, value, value_type, description, category)
VALUES (
    'allow_user_managed_agents',
    'false',
    'boolean',
    'Allow non-admin users to create and manage their own agent users from their profile',
    'security'
);

INSERT OR IGNORE INTO system_settings(key, value, value_type, description, category)
VALUES (
    'max_agents_per_user',
    '5',
    'integer',
    'Maximum number of owned agents a single non-admin user may create',
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
    target_pool_id INTEGER, -- soft ref to action_capabilities (runner_pool); NULL = local in-process run (WI-195)
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

-- Remote runner pools (Initiative WI-141). A pool is an action_capabilities
-- row of type 'runner_pool'; these tables hang off it by soft ref (no FK,
-- mirroring the agent-table convention) so pool deletion is handled in the
-- service layer and runs/instances can outlive a pool for audit.

-- runner_registration_tokens: reusable, pool-scoped, revocable tokens an
-- operator bakes into a runner deployment. A runner presents one to register
-- and exchanges it for a per-instance credential (runner_instances).
CREATE TABLE IF NOT EXISTS runner_registration_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pool_capability_id INTEGER NOT NULL, -- soft ref to action_capabilities (runner_pool)
    token_hash TEXT NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_by_user_id INTEGER,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME, -- NULL = no expiry
    revoked_at DATETIME  -- NULL = active
);
CREATE INDEX IF NOT EXISTS idx_runner_registration_tokens_pool
    ON runner_registration_tokens(pool_capability_id);

-- runner_instances: one registered runner. credential_hash is the
-- per-instance control-plane credential it received at registration;
-- last_heartbeat_at drives lease reaping.
CREATE TABLE IF NOT EXISTS runner_instances (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pool_capability_id INTEGER NOT NULL, -- soft ref to action_capabilities (runner_pool)
    name TEXT NOT NULL DEFAULT '',
    credential_hash TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','revoked')),
    registered_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_heartbeat_at DATETIME,
    revoked_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_runner_instances_pool ON runner_instances(pool_capability_id);
CREATE INDEX IF NOT EXISTS idx_runner_instances_status ON runner_instances(status);
