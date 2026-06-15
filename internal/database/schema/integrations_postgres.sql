-- Integration provider tables (Notion, Confluence, Google Docs, etc.)
-- Generalized system for linking external pages/documents to work items

-- Integration Providers (system-level configuration, admin sets up)
CREATE TABLE IF NOT EXISTS integration_providers (
	id TEXT PRIMARY KEY,
	slug TEXT UNIQUE NOT NULL,
	name TEXT NOT NULL,
	provider_type TEXT NOT NULL,
	enabled BOOLEAN DEFAULT true,
	oauth_client_id TEXT,
	oauth_client_secret_encrypted TEXT,
	provider_config TEXT DEFAULT '{}',
	created_at TIMESTAMPTZ DEFAULT NOW(),
	updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_integration_providers_slug ON integration_providers(slug);
CREATE INDEX IF NOT EXISTS idx_integration_providers_type ON integration_providers(provider_type);
CREATE INDEX IF NOT EXISTS idx_integration_providers_enabled ON integration_providers(enabled);

-- Integration OAuth State (temporary storage for OAuth flow, 5-min expiry)
CREATE TABLE IF NOT EXISTS integration_oauth_state (
	id TEXT PRIMARY KEY,
	provider_id TEXT NOT NULL REFERENCES integration_providers(id) ON DELETE CASCADE,
	state TEXT UNIQUE NOT NULL,
	user_id TEXT NOT NULL,
	expires_at TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_integration_oauth_state_state ON integration_oauth_state(state);
CREATE INDEX IF NOT EXISTS idx_integration_oauth_state_expires ON integration_oauth_state(expires_at);

-- User Integration Tokens (per-user OAuth tokens for integration providers)
CREATE TABLE IF NOT EXISTS user_integration_tokens (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	integration_provider_id TEXT NOT NULL REFERENCES integration_providers(id) ON DELETE CASCADE,
	oauth_access_token_encrypted TEXT NOT NULL,
	provider_metadata TEXT DEFAULT '{}',
	connected_at TIMESTAMPTZ DEFAULT NOW(),
	created_at TIMESTAMPTZ DEFAULT NOW(),
	updated_at TIMESTAMPTZ DEFAULT NOW(),
	UNIQUE(user_id, integration_provider_id)
);

CREATE INDEX IF NOT EXISTS idx_user_integration_tokens_user ON user_integration_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_user_integration_tokens_provider ON user_integration_tokens(integration_provider_id);

-- Item Integration Links (links between items and external pages/documents)
CREATE TABLE IF NOT EXISTS item_integration_links (
	id TEXT PRIMARY KEY,
	item_id TEXT NOT NULL,
	integration_provider_id TEXT NOT NULL REFERENCES integration_providers(id) ON DELETE CASCADE,
	external_id TEXT NOT NULL,
	external_url TEXT NOT NULL,
	title TEXT NOT NULL,
	icon TEXT DEFAULT '',
	link_type TEXT NOT NULL,
	link_metadata TEXT DEFAULT '{}',
	linked_by TEXT NOT NULL,
	created_at TIMESTAMPTZ DEFAULT NOW(),
	updated_at TIMESTAMPTZ DEFAULT NOW(),
	UNIQUE(item_id, integration_provider_id, external_id)
);

CREATE INDEX IF NOT EXISTS idx_item_integration_links_item ON item_integration_links(item_id);
CREATE INDEX IF NOT EXISTS idx_item_integration_links_provider ON item_integration_links(integration_provider_id);
CREATE INDEX IF NOT EXISTS idx_item_integration_links_external ON item_integration_links(external_id);

-- ============================================================================
-- Todoist personal-task sync (WI-402)
-- Two-way 1:1 mirror between a user's personal workspace and their Todoist
-- account. Reuses integration_providers + user_integration_tokens for the
-- connection; these tables hold the per-user sync configuration and the
-- item <-> Todoist-task id mapping.
-- ============================================================================

-- Per-user sync configuration. One row per (user, provider).
CREATE TABLE IF NOT EXISTS todoist_sync_config (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	integration_provider_id TEXT NOT NULL REFERENCES integration_providers(id) ON DELETE CASCADE,
	personal_workspace_id INTEGER NOT NULL,
	enabled BOOLEAN DEFAULT false,
	scope_mode TEXT NOT NULL DEFAULT 'all',
	todoist_project_id TEXT DEFAULT '',
	sync_token TEXT DEFAULT '*',
	last_synced_at TIMESTAMPTZ,
	last_error TEXT DEFAULT '',
	sync_lock_until TIMESTAMPTZ,
	created_at TIMESTAMPTZ DEFAULT NOW(),
	updated_at TIMESTAMPTZ DEFAULT NOW(),
	UNIQUE(user_id, integration_provider_id)
);

CREATE INDEX IF NOT EXISTS idx_todoist_sync_config_user ON todoist_sync_config(user_id);
CREATE INDEX IF NOT EXISTS idx_todoist_sync_config_enabled ON todoist_sync_config(enabled);

-- Item <-> Todoist-task id map. One row per synced task pair.
CREATE TABLE IF NOT EXISTS todoist_task_links (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	item_id INTEGER NOT NULL,
	todoist_task_id TEXT NOT NULL,
	todoist_project_id TEXT DEFAULT '',
	last_title TEXT DEFAULT '',
	last_description TEXT DEFAULT '',
	last_due TEXT DEFAULT '',
	last_priority INTEGER DEFAULT 1,
	last_completed BOOLEAN DEFAULT false,
	created_at TIMESTAMPTZ DEFAULT NOW(),
	updated_at TIMESTAMPTZ DEFAULT NOW(),
	UNIQUE(user_id, todoist_task_id),
	UNIQUE(item_id)
);

CREATE INDEX IF NOT EXISTS idx_todoist_task_links_user ON todoist_task_links(user_id);
CREATE INDEX IF NOT EXISTS idx_todoist_task_links_item ON todoist_task_links(item_id);
CREATE INDEX IF NOT EXISTS idx_todoist_task_links_todoist ON todoist_task_links(todoist_task_id);
