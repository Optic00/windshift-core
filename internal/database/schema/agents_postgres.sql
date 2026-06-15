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
    runner_id INTEGER, -- soft ref to runner_instances; NULL for the in-process local runner (WI-141)
    target_pool_id INTEGER, -- soft ref to action_capabilities (runner_pool); NULL = local in-process pool (WI-141)
    cancel_requested_at TIMESTAMPTZ, -- set when a running remote run should abort; the runner learns via heartbeat (WI-141)
    grants_json TEXT, -- RunGrants snapshot the access-layer brokers authorize against (WI-144)
    run_token_id INTEGER, -- api_tokens row that binds a presented credential to this run's grants (WI-144)
    triggered_by_user_id INTEGER, -- soft ref to users: who fired the trigger; credential principal for OAuth SCM connections (WI-275)
    job_kind TEXT NOT NULL DEFAULT 'coding_agent', -- coding_agent | action_container | ci_task (WI-146)
    job_image TEXT, -- admin image for action_container/ci_task jobs; NULL for coding_agent (fixed runner image)
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
CREATE INDEX IF NOT EXISTS idx_agent_runs_runner ON agent_runs(runner_id);
-- Supports the remote DB-as-queue claim: next queued run for a pool, oldest first.
CREATE INDEX IF NOT EXISTS idx_agent_runs_pool_claim ON agent_runs(target_pool_id, status, queued_at);

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
    target_pool_id INTEGER, -- soft ref to action_capabilities (runner_pool); NULL = local in-process run (WI-195)
    instructions TEXT NOT NULL DEFAULT '', -- appended to the run's initial prompt as the agent's role/persona (WI-258)
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

-- Workspace agent skills (WI-258): a per-workspace library of markdown
-- "knowledge packs" attachable to agent bindings. Delivery is progressive
-- disclosure through the ws CLI: the run's initial prompt lists attached
-- skills (name + description) and the agent fetches a body with
-- `ws skill get <id>` only when relevant. Modeled after the Anthropic
-- Agent Skills standard (SKILL.md), markdown-only in v1.
CREATE TABLE IF NOT EXISTS workspace_agent_skills (
    id SERIAL PRIMARY KEY,
    workspace_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_by_user_id INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (created_by_user_id) REFERENCES users(id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_agent_skills_workspace_name
    ON workspace_agent_skills(workspace_id, name);

CREATE TABLE IF NOT EXISTS workspace_agent_binding_skills (
    binding_id INTEGER NOT NULL,
    skill_id INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (binding_id, skill_id),
    FOREIGN KEY (binding_id) REFERENCES workspace_agent_bindings(id) ON DELETE CASCADE,
    FOREIGN KEY (skill_id) REFERENCES workspace_agent_skills(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_workspace_agent_binding_skills_skill
    ON workspace_agent_binding_skills(skill_id);

-- Remote runner pools (Initiative WI-141). A pool is an action_capabilities
-- row of type 'runner_pool'; these tables hang off it by soft ref (no FK,
-- mirroring the agent-table convention).
CREATE TABLE IF NOT EXISTS runner_registration_tokens (
    id SERIAL PRIMARY KEY,
    pool_capability_id INTEGER NOT NULL, -- soft ref to action_capabilities (runner_pool)
    token_hash TEXT NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_by_user_id INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_runner_registration_tokens_pool
    ON runner_registration_tokens(pool_capability_id);

CREATE TABLE IF NOT EXISTS runner_instances (
    id SERIAL PRIMARY KEY,
    pool_capability_id INTEGER NOT NULL, -- soft ref to action_capabilities (runner_pool)
    name TEXT NOT NULL DEFAULT '',
    credential_hash TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','revoked')),
    registered_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_heartbeat_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_runner_instances_pool ON runner_instances(pool_capability_id);
CREATE INDEX IF NOT EXISTS idx_runner_instances_status ON runner_instances(status);
