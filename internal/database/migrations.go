package database

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Driver name constants match the values returned by GetDriverName on each
// backend (SQLiteDB.GetDriverName → "sqlite", PostgresDB.GetDriverName →
// "postgres"). The Database interface comment claims "sqlite3" but that's
// stale; the actual returns are below.
const (
	driverSQLite   = "sqlite"
	driverPostgres = "postgres"
)

// Migration is one entry in the schema_migrations catalog. Version is a
// stable slug used as the schema_migrations primary key; Name is a human
// label. CheckSQLite / CheckPostgres are queries that return COUNT >= 1
// when the migration's effect is already present, used for retroactive
// backfill on existing installs upgrading past the introduction of the
// schema_migrations table. SQLite / Postgres carry the backend-specific
// DDL to apply when the check reports the effect is missing.
//
// An empty Check on a backend means the migration body always runs when
// the version isn't already stamped. An empty body on a backend means
// the migration is skipped on that backend — the row is still stamped
// so the catalog stays consistent across backends.
type Migration struct {
	Version       string
	Name          string
	CheckSQLite   string
	CheckPostgres string
	SQLite        string
	Postgres      string
}

// Catalog is the ordered list of migrations applied via runPendingMigrations.
// New migrations append with a date-prefixed Version slug such as
// "20260514_widgets_archived_at". Order matters only between migrations
// with row dependencies; otherwise entries may be reordered freely.
//
// Currently empty: the legacy migration arrays in database.go and
// postgres.go still own existing-install migrations. They are ported into
// this Catalog in subsequent commits.
var Catalog = []Migration{
	{
		Version:       "20260618_push_subscriptions",
		Name:          "Create Web Push subscriptions table",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='push_subscriptions'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='push_subscriptions'",
		SQLite: `
			CREATE TABLE IF NOT EXISTS push_subscriptions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL,
				endpoint TEXT NOT NULL,
				auth_key TEXT NOT NULL,
				p256dh_key TEXT NOT NULL,
				user_agent TEXT NOT NULL DEFAULT '',
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				last_used_at DATETIME,
				revoked_at DATETIME,
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
			);
			CREATE UNIQUE INDEX IF NOT EXISTS uq_push_subscriptions_endpoint ON push_subscriptions(endpoint);
			CREATE INDEX IF NOT EXISTS idx_push_subscriptions_user_id ON push_subscriptions(user_id);
		`,
		Postgres: `
			CREATE TABLE IF NOT EXISTS push_subscriptions (
				id SERIAL PRIMARY KEY,
				user_id INTEGER NOT NULL,
				endpoint TEXT NOT NULL,
				auth_key TEXT NOT NULL,
				p256dh_key TEXT NOT NULL,
				user_agent TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
				last_used_at TIMESTAMPTZ,
				revoked_at TIMESTAMPTZ,
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
			);
			CREATE UNIQUE INDEX IF NOT EXISTS uq_push_subscriptions_endpoint ON push_subscriptions(endpoint);
			CREATE INDEX IF NOT EXISTS idx_push_subscriptions_user_id ON push_subscriptions(user_id);
		`,
	},
	{
		Version:       "20260617_llm_connections_provider_config",
		Name:          "Add provider-specific JSON config to LLM connections",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('llm_connections') WHERE name='provider_config'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='llm_connections' AND column_name='provider_config'",
		SQLite:        "ALTER TABLE llm_connections ADD COLUMN provider_config TEXT",
		Postgres:      "ALTER TABLE llm_connections ADD COLUMN provider_config JSONB",
	},
	{
		Version:       "20260528_portal_request_drafts",
		Name:          "Create portal request form drafts table",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='portal_request_drafts'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='portal_request_drafts'",
		SQLite: `
			CREATE TABLE IF NOT EXISTS portal_request_drafts (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				channel_id INTEGER NOT NULL,
				request_type_id INTEGER NOT NULL,
				portal_customer_id INTEGER,
				user_id INTEGER,
				title TEXT NOT NULL DEFAULT '',
				description TEXT NOT NULL DEFAULT '',
				custom_field_values TEXT,
				current_step INTEGER NOT NULL DEFAULT 1,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE,
				FOREIGN KEY (request_type_id) REFERENCES request_types(id) ON DELETE CASCADE,
				FOREIGN KEY (portal_customer_id) REFERENCES portal_customers(id) ON DELETE CASCADE,
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
				CHECK (portal_customer_id IS NOT NULL OR user_id IS NOT NULL)
			);
			CREATE UNIQUE INDEX IF NOT EXISTS uq_portal_request_drafts_pc
				ON portal_request_drafts(portal_customer_id, request_type_id)
				WHERE portal_customer_id IS NOT NULL;
			CREATE UNIQUE INDEX IF NOT EXISTS uq_portal_request_drafts_user
				ON portal_request_drafts(user_id, request_type_id)
				WHERE user_id IS NOT NULL;
			CREATE INDEX IF NOT EXISTS idx_portal_request_drafts_updated_at
				ON portal_request_drafts(updated_at DESC);
		`,
		Postgres: `
			CREATE TABLE IF NOT EXISTS portal_request_drafts (
				id SERIAL PRIMARY KEY,
				channel_id INTEGER NOT NULL,
				request_type_id INTEGER NOT NULL,
				portal_customer_id INTEGER,
				user_id INTEGER,
				title TEXT NOT NULL DEFAULT '',
				description TEXT NOT NULL DEFAULT '',
				custom_field_values JSONB,
				current_step INTEGER NOT NULL DEFAULT 1,
				created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE,
				FOREIGN KEY (request_type_id) REFERENCES request_types(id) ON DELETE CASCADE,
				FOREIGN KEY (portal_customer_id) REFERENCES portal_customers(id) ON DELETE CASCADE,
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
				CHECK (portal_customer_id IS NOT NULL OR user_id IS NOT NULL)
			);
			CREATE UNIQUE INDEX IF NOT EXISTS uq_portal_request_drafts_pc
				ON portal_request_drafts(portal_customer_id, request_type_id)
				WHERE portal_customer_id IS NOT NULL;
			CREATE UNIQUE INDEX IF NOT EXISTS uq_portal_request_drafts_user
				ON portal_request_drafts(user_id, request_type_id)
				WHERE user_id IS NOT NULL;
			CREATE INDEX IF NOT EXISTS idx_portal_request_drafts_updated_at
				ON portal_request_drafts(updated_at DESC);
		`,
	},
	{
		Version:       "20260520_item_change_log",
		Name:          "Create item change log for collection delta polling",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='item_change_log'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='item_change_log'",
		SQLite: `
			CREATE TABLE IF NOT EXISTS item_change_log (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				item_id INTEGER NOT NULL,
				workspace_id INTEGER NOT NULL,
				change_type TEXT NOT NULL CHECK (change_type IN ('upsert', 'delete')),
				changed_at DATETIME DEFAULT CURRENT_TIMESTAMP
			);
			CREATE INDEX IF NOT EXISTS idx_item_change_log_workspace_id ON item_change_log(workspace_id, id);
			CREATE INDEX IF NOT EXISTS idx_item_change_log_item_id ON item_change_log(item_id, id);
			CREATE TRIGGER IF NOT EXISTS trg_items_change_insert AFTER INSERT ON items
			BEGIN
				INSERT INTO item_change_log(item_id, workspace_id, change_type) VALUES (NEW.id, NEW.workspace_id, 'upsert');
			END;
			CREATE TRIGGER IF NOT EXISTS trg_items_change_update AFTER UPDATE ON items
			BEGIN
				INSERT INTO item_change_log(item_id, workspace_id, change_type)
				SELECT OLD.id, OLD.workspace_id, 'delete' WHERE OLD.workspace_id <> NEW.workspace_id;
				INSERT INTO item_change_log(item_id, workspace_id, change_type) VALUES (NEW.id, NEW.workspace_id, 'upsert');
			END;
			CREATE TRIGGER IF NOT EXISTS trg_items_change_delete BEFORE DELETE ON items
			BEGIN
				INSERT INTO item_change_log(item_id, workspace_id, change_type) VALUES (OLD.id, OLD.workspace_id, 'delete');
			END;
			CREATE TRIGGER IF NOT EXISTS trg_collections_change_update AFTER UPDATE OF ql_query, filter_state, workspace_id ON collections
			BEGIN
				INSERT INTO item_change_log(item_id, workspace_id, change_type) VALUES (0, COALESCE(NEW.workspace_id, OLD.workspace_id, 0), 'upsert');
			END;
		`,
		Postgres: `
			CREATE TABLE IF NOT EXISTS item_change_log (
				id BIGSERIAL PRIMARY KEY,
				item_id INTEGER NOT NULL,
				workspace_id INTEGER NOT NULL,
				change_type TEXT NOT NULL CHECK (change_type IN ('upsert', 'delete')),
				changed_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
			);
			CREATE INDEX IF NOT EXISTS idx_item_change_log_workspace_id ON item_change_log(workspace_id, id);
			CREATE INDEX IF NOT EXISTS idx_item_change_log_item_id ON item_change_log(item_id, id);
			CREATE OR REPLACE FUNCTION log_item_change() RETURNS trigger AS $$
			BEGIN
				IF TG_OP = 'DELETE' THEN
					INSERT INTO item_change_log(item_id, workspace_id, change_type) VALUES (OLD.id, OLD.workspace_id, 'delete');
					RETURN OLD;
				END IF;
				IF TG_OP = 'UPDATE' AND OLD.workspace_id <> NEW.workspace_id THEN
					INSERT INTO item_change_log(item_id, workspace_id, change_type) VALUES (OLD.id, OLD.workspace_id, 'delete');
				END IF;
				INSERT INTO item_change_log(item_id, workspace_id, change_type) VALUES (NEW.id, NEW.workspace_id, 'upsert');
				RETURN NEW;
			END;
			$$ LANGUAGE plpgsql;
			CREATE OR REPLACE FUNCTION log_collection_change() RETURNS trigger AS $$
			BEGIN
				INSERT INTO item_change_log(item_id, workspace_id, change_type) VALUES (0, COALESCE(NEW.workspace_id, OLD.workspace_id, 0), 'upsert');
				RETURN NEW;
			END;
			$$ LANGUAGE plpgsql;
			DROP TRIGGER IF EXISTS trg_items_change_insert ON items;
			DROP TRIGGER IF EXISTS trg_items_change_update ON items;
			DROP TRIGGER IF EXISTS trg_items_change_delete ON items;
			DROP TRIGGER IF EXISTS trg_collections_change_update ON collections;
			CREATE TRIGGER trg_items_change_insert AFTER INSERT ON items FOR EACH ROW EXECUTE FUNCTION log_item_change();
			CREATE TRIGGER trg_items_change_update AFTER UPDATE ON items FOR EACH ROW EXECUTE FUNCTION log_item_change();
			CREATE TRIGGER trg_items_change_delete BEFORE DELETE ON items FOR EACH ROW EXECUTE FUNCTION log_item_change();
			CREATE TRIGGER trg_collections_change_update AFTER UPDATE OF ql_query, filter_state, workspace_id ON collections FOR EACH ROW EXECUTE FUNCTION log_collection_change();
		`,
	},
	{
		Version: "20260514_email_message_tracking_dedup_key",
		Name:    "Add dedup_key to email_message_tracking",
		// Idempotency check: column already present means the migration ran
		// previously (the legacy unique index gets swapped inside the body).
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('email_message_tracking') WHERE name='dedup_key'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='email_message_tracking' AND column_name='dedup_key'",
		SQLite: `
			ALTER TABLE email_message_tracking ADD COLUMN dedup_key TEXT NOT NULL DEFAULT '';
			UPDATE email_message_tracking SET dedup_key = CASE
				WHEN message_id IS NOT NULL AND message_id <> '' THEN message_id
				ELSE 'legacy:' || CAST(id AS TEXT)
			END WHERE dedup_key = '';
			DROP INDEX IF EXISTS idx_email_message_tracking_unique;
			CREATE UNIQUE INDEX IF NOT EXISTS idx_email_message_tracking_dedup ON email_message_tracking(channel_id, dedup_key);
		`,
		Postgres: `
			ALTER TABLE email_message_tracking ADD COLUMN dedup_key TEXT NOT NULL DEFAULT '';
			UPDATE email_message_tracking SET dedup_key = CASE
				WHEN message_id IS NOT NULL AND message_id <> '' THEN message_id
				ELSE 'legacy:' || id::text
			END WHERE dedup_key = '';
			DROP INDEX IF EXISTS idx_email_message_tracking_unique;
			CREATE UNIQUE INDEX IF NOT EXISTS idx_email_message_tracking_dedup ON email_message_tracking(channel_id, dedup_key);
		`,
	},
	{
		Version:       "20260514_email_message_tracking_attachments_status",
		Name:          "Add attachments_status to email_message_tracking",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('email_message_tracking') WHERE name='attachments_status'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='email_message_tracking' AND column_name='attachments_status'",
		SQLite:        `ALTER TABLE email_message_tracking ADD COLUMN attachments_status TEXT`,
		Postgres:      `ALTER TABLE email_message_tracking ADD COLUMN attachments_status TEXT`,
	},
	{
		Version:       "20260514_webhook_deliveries_response_preview",
		Name:          "Add response_preview to webhook_deliveries",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('webhook_deliveries') WHERE name='response_preview'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='webhook_deliveries' AND column_name='response_preview'",
		SQLite:        `ALTER TABLE webhook_deliveries ADD COLUMN response_preview TEXT`,
		Postgres:      `ALTER TABLE webhook_deliveries ADD COLUMN response_preview TEXT`,
	},
	{
		Version:       "20260520_board_config_rightmost_column_limit",
		Name:          "Add rightmost board column display limit setting",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('board_configurations') WHERE name='show_rightmost_column_last_50'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='board_configurations' AND column_name='show_rightmost_column_last_50'",
		SQLite:        `ALTER TABLE board_configurations ADD COLUMN show_rightmost_column_last_50 BOOLEAN DEFAULT false`,
		Postgres:      `ALTER TABLE board_configurations ADD COLUMN show_rightmost_column_last_50 BOOLEAN DEFAULT false`,
	},
	{
		Version: "20260520_time_tracking_permission_tables",
		Name:    "Create time tracking and customer organisation permission tables",
		CheckSQLite: `SELECT CASE WHEN
			EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name='time_project_managers')
			AND EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name='time_project_members')
			AND EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name='customer_organisation_managers')
			AND EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name='customer_organisation_members')
			THEN 1 ELSE 0 END`,
		CheckPostgres: `SELECT CASE WHEN
			EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='time_project_managers')
			AND EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='time_project_members')
			AND EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='customer_organisation_managers')
			AND EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='customer_organisation_members')
			THEN 1 ELSE 0 END`,
		SQLite: `
			CREATE TABLE IF NOT EXISTS time_project_managers (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				project_id INTEGER NOT NULL,
				manager_type TEXT NOT NULL CHECK (manager_type IN ('user', 'group')),
				manager_id INTEGER NOT NULL,
				granted_by INTEGER,
				granted_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (project_id) REFERENCES time_projects(id) ON DELETE CASCADE,
				FOREIGN KEY (granted_by) REFERENCES users(id) ON DELETE SET NULL,
				UNIQUE(project_id, manager_type, manager_id)
			);

			CREATE TABLE IF NOT EXISTS time_project_members (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				project_id INTEGER NOT NULL,
				member_type TEXT NOT NULL CHECK (member_type IN ('user', 'group')),
				member_id INTEGER NOT NULL,
				granted_by INTEGER,
				granted_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (project_id) REFERENCES time_projects(id) ON DELETE CASCADE,
				FOREIGN KEY (granted_by) REFERENCES users(id) ON DELETE SET NULL,
				UNIQUE(project_id, member_type, member_id)
			);

			CREATE INDEX IF NOT EXISTS idx_time_project_managers_project ON time_project_managers(project_id);
			CREATE INDEX IF NOT EXISTS idx_time_project_members_project ON time_project_members(project_id);

			CREATE TABLE IF NOT EXISTS customer_organisation_managers (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				customer_organisation_id INTEGER NOT NULL,
				manager_type TEXT NOT NULL CHECK (manager_type IN ('user', 'group')),
				manager_id INTEGER NOT NULL,
				granted_by INTEGER,
				granted_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (customer_organisation_id) REFERENCES customer_organisations(id) ON DELETE CASCADE,
				FOREIGN KEY (granted_by) REFERENCES users(id) ON DELETE SET NULL,
				UNIQUE(customer_organisation_id, manager_type, manager_id)
			);

			CREATE TABLE IF NOT EXISTS customer_organisation_members (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				customer_organisation_id INTEGER NOT NULL,
				member_type TEXT NOT NULL CHECK (member_type IN ('user', 'group')),
				member_id INTEGER NOT NULL,
				granted_by INTEGER,
				granted_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (customer_organisation_id) REFERENCES customer_organisations(id) ON DELETE CASCADE,
				FOREIGN KEY (granted_by) REFERENCES users(id) ON DELETE SET NULL,
				UNIQUE(customer_organisation_id, member_type, member_id)
			);

			CREATE INDEX IF NOT EXISTS idx_customer_organisation_managers_org ON customer_organisation_managers(customer_organisation_id);
			CREATE INDEX IF NOT EXISTS idx_customer_organisation_members_org ON customer_organisation_members(customer_organisation_id);
		`,
		Postgres: `
			CREATE TABLE IF NOT EXISTS time_project_managers (
				id SERIAL PRIMARY KEY,
				project_id INTEGER NOT NULL,
				manager_type TEXT NOT NULL CHECK (manager_type IN ('user', 'group')),
				manager_id INTEGER NOT NULL,
				granted_by INTEGER,
				granted_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (project_id) REFERENCES time_projects(id) ON DELETE CASCADE,
				FOREIGN KEY (granted_by) REFERENCES users(id) ON DELETE SET NULL,
				UNIQUE(project_id, manager_type, manager_id)
			);

			CREATE TABLE IF NOT EXISTS time_project_members (
				id SERIAL PRIMARY KEY,
				project_id INTEGER NOT NULL,
				member_type TEXT NOT NULL CHECK (member_type IN ('user', 'group')),
				member_id INTEGER NOT NULL,
				granted_by INTEGER,
				granted_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (project_id) REFERENCES time_projects(id) ON DELETE CASCADE,
				FOREIGN KEY (granted_by) REFERENCES users(id) ON DELETE SET NULL,
				UNIQUE(project_id, member_type, member_id)
			);

			CREATE INDEX IF NOT EXISTS idx_time_project_managers_project ON time_project_managers(project_id);
			CREATE INDEX IF NOT EXISTS idx_time_project_members_project ON time_project_members(project_id);

			CREATE TABLE IF NOT EXISTS customer_organisation_managers (
				id SERIAL PRIMARY KEY,
				customer_organisation_id INTEGER NOT NULL,
				manager_type TEXT NOT NULL CHECK (manager_type IN ('user', 'group')),
				manager_id INTEGER NOT NULL,
				granted_by INTEGER,
				granted_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (customer_organisation_id) REFERENCES customer_organisations(id) ON DELETE CASCADE,
				FOREIGN KEY (granted_by) REFERENCES users(id) ON DELETE SET NULL,
				UNIQUE(customer_organisation_id, manager_type, manager_id)
			);

			CREATE TABLE IF NOT EXISTS customer_organisation_members (
				id SERIAL PRIMARY KEY,
				customer_organisation_id INTEGER NOT NULL,
				member_type TEXT NOT NULL CHECK (member_type IN ('user', 'group')),
				member_id INTEGER NOT NULL,
				granted_by INTEGER,
				granted_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (customer_organisation_id) REFERENCES customer_organisations(id) ON DELETE CASCADE,
				FOREIGN KEY (granted_by) REFERENCES users(id) ON DELETE SET NULL,
				UNIQUE(customer_organisation_id, member_type, member_id)
			);

			CREATE INDEX IF NOT EXISTS idx_customer_organisation_managers_org ON customer_organisation_managers(customer_organisation_id);
			CREATE INDEX IF NOT EXISTS idx_customer_organisation_members_org ON customer_organisation_members(customer_organisation_id);
		`,
	},
	{
		// Legacy SQLite installs declared notification_templates with
		// `template_type TEXT NOT NULL` and `content TEXT NOT NULL`. The
		// modernized seed in emailutil.SeedTemplates doesn't supply
		// template_type, so the INSERT trips the legacy NOT NULL constraint
		// and no built-in templates land. Rebuild the table to match the
		// current schema (notifications.sql), which makes both columns
		// nullable. Postgres never had the NOT NULL on either column.
		//
		// Check: COUNT > 0 when template_type is already nullable (or the
		// column is missing — pragma returns no rows, COUNT = 0 falls through,
		// but the WHEN branch evaluating notnull = 0 also returns 1 when the
		// column exists and is nullable). The body is a single multi-statement
		// rebuild; no FK toggling needed because nothing FK-references
		// notification_templates.
		Version: "20260515_notification_templates_drop_legacy_notnull",
		Name:    "Drop legacy NOT NULL on notification_templates.template_type/content",
		CheckSQLite: `SELECT CASE
			WHEN NOT EXISTS (SELECT 1 FROM pragma_table_info('notification_templates') WHERE name='template_type') THEN 1
			WHEN (SELECT [notnull] FROM pragma_table_info('notification_templates') WHERE name='template_type') = 0 THEN 1
			ELSE 0
		END`,
		SQLite: `
			CREATE TABLE notification_templates_new (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL UNIQUE,
				subject TEXT,
				content TEXT,
				text_body TEXT,
				description TEXT,
				is_system BOOLEAN DEFAULT 0,
				is_active BOOLEAN DEFAULT 1,
				template_type TEXT,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
			);
			INSERT INTO notification_templates_new
				(id, name, subject, content, text_body, description, is_system, is_active, template_type, created_at, updated_at)
			SELECT id, name, subject, content, text_body, description, is_system, is_active, template_type, created_at, updated_at
			FROM notification_templates;
			DROP TABLE notification_templates;
			ALTER TABLE notification_templates_new RENAME TO notification_templates;
			CREATE INDEX IF NOT EXISTS idx_notification_templates_active ON notification_templates(is_active);
		`,
	},
	{
		// Commit 90edd5a reshaped action_credentials: dropped workspace_id (+ its
		// FK and idx_action_credentials_workspace), added applies_to_all_workspaces,
		// and introduced the action_credential_workspaces join table. Schema files
		// use CREATE TABLE IF NOT EXISTS, so existing installs never picked up the
		// column rename and the admin /api/admin/action-credentials handler dies
		// with `column "applies_to_all_workspaces" does not exist`.
		//
		// Backfill: rows with workspace_id set become applies_to_all_workspaces=false
		// with a join-table row; rows with workspace_id IS NULL keep the column
		// default of true (global). Postgres can drop the legacy column inline.
		// SQLite rejects DROP COLUMN on a FK-bearing column, so we table-rebuild
		// the same way as the notification_templates entry above.
		Version:       "20260519_action_credentials_workspace_scope",
		Name:          "Reshape action_credentials to applies_to_all_workspaces + join table",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('action_credentials') WHERE name='applies_to_all_workspaces'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='action_credentials' AND column_name='applies_to_all_workspaces'",
		Postgres: `
			CREATE TABLE IF NOT EXISTS action_credential_workspaces (
				credential_id INTEGER NOT NULL,
				workspace_id INTEGER NOT NULL,
				PRIMARY KEY (credential_id, workspace_id),
				FOREIGN KEY (credential_id) REFERENCES action_credentials(id) ON DELETE CASCADE,
				FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
			);
			CREATE INDEX IF NOT EXISTS idx_action_credential_workspaces_workspace ON action_credential_workspaces(workspace_id);

			ALTER TABLE action_credentials ADD COLUMN applies_to_all_workspaces BOOLEAN NOT NULL DEFAULT true;

			INSERT INTO action_credential_workspaces (credential_id, workspace_id)
				SELECT id, workspace_id FROM action_credentials WHERE workspace_id IS NOT NULL;
			UPDATE action_credentials SET applies_to_all_workspaces = false WHERE workspace_id IS NOT NULL;

			DROP INDEX IF EXISTS idx_action_credentials_workspace;
			ALTER TABLE action_credentials DROP COLUMN workspace_id;
		`,
		SQLite: `
			-- Stash the legacy workspace_id mappings before the rebuild — we can't
			-- create the action_credential_workspaces join table up-front because
			-- its ON DELETE CASCADE FK would wipe the backfill rows when we DROP
			-- the legacy action_credentials table below.
			CREATE TEMP TABLE _cred_ws_backfill AS
				SELECT id AS credential_id, workspace_id
				FROM action_credentials
				WHERE workspace_id IS NOT NULL;

			CREATE TABLE action_credentials_new (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL,
				credential_type TEXT NOT NULL,
				applies_to_all_workspaces BOOLEAN NOT NULL DEFAULT 1,
				created_by INTEGER,
				encrypted_secret TEXT NOT NULL,
				secret_prefix TEXT,
				secret_metadata TEXT,
				is_enabled BOOLEAN DEFAULT 1,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL
			);
			INSERT INTO action_credentials_new
				(id, name, credential_type, applies_to_all_workspaces, created_by,
				 encrypted_secret, secret_prefix, secret_metadata, is_enabled,
				 created_at, updated_at)
			SELECT id, name, credential_type,
				   CASE WHEN workspace_id IS NULL THEN 1 ELSE 0 END,
				   created_by, encrypted_secret, secret_prefix, secret_metadata,
				   is_enabled, created_at, updated_at
			FROM action_credentials;

			DROP INDEX IF EXISTS idx_action_credentials_workspace;
			DROP TABLE action_credentials;
			ALTER TABLE action_credentials_new RENAME TO action_credentials;
			CREATE INDEX IF NOT EXISTS idx_action_credentials_enabled ON action_credentials(is_enabled);

			CREATE TABLE IF NOT EXISTS action_credential_workspaces (
				credential_id INTEGER NOT NULL,
				workspace_id INTEGER NOT NULL,
				PRIMARY KEY (credential_id, workspace_id),
				FOREIGN KEY (credential_id) REFERENCES action_credentials(id) ON DELETE CASCADE,
				FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
			);
			CREATE INDEX IF NOT EXISTS idx_action_credential_workspaces_workspace ON action_credential_workspaces(workspace_id);

			INSERT INTO action_credential_workspaces (credential_id, workspace_id)
				SELECT credential_id, workspace_id FROM _cred_ws_backfill;
			DROP TABLE _cred_ws_backfill;
		`,
	},
	{
		// Seed the "Page" system link type retroactively so existing installs
		// can link work items to knowledge pages. Skips the insert if a row
		// with that name already exists (e.g. a fresh install that already
		// got it via the database.go seed loop).
		Version:       "20260522_link_type_page",
		Name:          "Seed Page system link type",
		CheckSQLite:   "SELECT COUNT(*) FROM link_types WHERE name='Page'",
		CheckPostgres: "SELECT COUNT(*) FROM link_types WHERE name='Page'",
		SQLite: `INSERT INTO link_types (name, description, forward_label, reverse_label, color, is_system, active, allowed_entity_types, created_at, updated_at)
			VALUES ('Page', 'Work item references a knowledge page', 'references page', 'referenced by', '#0ea5e9', 1, 1, '["item","page"]', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		Postgres: `INSERT INTO link_types (name, description, forward_label, reverse_label, color, is_system, active, allowed_entity_types, created_at, updated_at)
			VALUES ('Page', 'Work item references a knowledge page', 'references page', 'referenced by', '#0ea5e9', true, true, '["item","page"]', NOW(), NOW())`,
	},
	{
		// frac_index was originally declared UNIQUE globally, but the
		// generator (KeyBetween) only intends per-sibling-set
		// uniqueness — KeyBetween("","") deterministically returns "a0",
		// so two independent sibling sets would both attempt that key
		// on first reorder and collide. Scope the index by
		// (workspace_id, parent_id) instead. Before swapping the index
		// we resolve any duplicate keys that may have crept in: keep
		// the lowest-id row per (workspace_id, parent_id, frac_index)
		// and NULL the others so the next drag-and-drop in that group
		// backfills cleanly. COALESCE(parent_id,-1) treats root pages
		// as their own sibling set (both backends consider NULL=NULL
		// false inside a UNIQUE index).
		Version:       "20260522_pages_frac_index_scoped",
		Name:          "Scope pages.frac_index uniqueness to (workspace_id, parent_id)",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_pages_frac_index_scoped'",
		CheckPostgres: "SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND indexname='idx_pages_frac_index_scoped'",
		SQLite: `
			UPDATE pages SET frac_index = NULL
			WHERE frac_index IS NOT NULL
			  AND id IN (
				SELECT p1.id FROM pages p1
				WHERE p1.frac_index IS NOT NULL
				  AND EXISTS (
					SELECT 1 FROM pages p2
					WHERE p2.frac_index = p1.frac_index
					  AND p2.workspace_id = p1.workspace_id
					  AND COALESCE(p2.parent_id, -1) = COALESCE(p1.parent_id, -1)
					  AND p2.id < p1.id
				  )
			  );
			DROP INDEX IF EXISTS idx_pages_frac_index;
			CREATE UNIQUE INDEX IF NOT EXISTS idx_pages_frac_index_scoped
				ON pages(workspace_id, COALESCE(parent_id, -1), frac_index)
				WHERE frac_index IS NOT NULL;
		`,
		Postgres: `
			UPDATE pages SET frac_index = NULL
			WHERE frac_index IS NOT NULL
			  AND id IN (
				SELECT p1.id FROM pages p1
				WHERE p1.frac_index IS NOT NULL
				  AND EXISTS (
					SELECT 1 FROM pages p2
					WHERE p2.frac_index = p1.frac_index
					  AND p2.workspace_id = p1.workspace_id
					  AND COALESCE(p2.parent_id, -1) = COALESCE(p1.parent_id, -1)
					  AND p2.id < p1.id
				  )
			  );
			DROP INDEX IF EXISTS idx_pages_frac_index;
			CREATE UNIQUE INDEX IF NOT EXISTS idx_pages_frac_index_scoped
				ON pages(workspace_id, COALESCE(parent_id, -1), frac_index)
				WHERE frac_index IS NOT NULL;
		`,
	},
	{
		Version:       "20260520_llm_provider_model_cache",
		Name:          "Add llm_provider_model_cache for dynamic model lists",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='llm_provider_model_cache'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='llm_provider_model_cache'",
		SQLite: `CREATE TABLE llm_provider_model_cache (
			provider_type     TEXT PRIMARY KEY,
			models_json       TEXT NOT NULL,
			last_refreshed_at DATETIME,
			last_error        TEXT,
			updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		Postgres: `CREATE TABLE llm_provider_model_cache (
			provider_type     TEXT PRIMARY KEY,
			models_json       TEXT NOT NULL,
			last_refreshed_at TIMESTAMPTZ,
			last_error        TEXT,
			updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
	},
	{
		// page_label_assignments is the junction table behind ws/page-label
		// attach/detach (page_label_repository.AddAssignment / ReplaceAssignments /
		// ListForPage / LoadLabelsForPages). The legacy upgrade path in
		// postgres.go:672 re-runs the embedded page_labels schema for
		// existing installs, but at least one production DB ended up with
		// page_labels present and page_label_assignments missing (tree-load
		// 500: "relation \"page_label_assignments\" does not exist"). Stamp
		// it through the catalog so the table is guaranteed regardless of
		// which path the install took.
		//
		// Both backends use CREATE TABLE IF NOT EXISTS + indexes that are
		// also IF NOT EXISTS, so re-running this on a healthy install is a
		// no-op even when the Check happens to be skipped.
		Version:       "20260522_page_label_assignments",
		Name:          "Ensure page_label_assignments table exists",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='page_label_assignments'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='page_label_assignments'",
		SQLite: `
			CREATE TABLE IF NOT EXISTS page_label_assignments (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				page_id INTEGER NOT NULL,
				page_label_id INTEGER NOT NULL,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (page_id) REFERENCES pages(id) ON DELETE CASCADE,
				FOREIGN KEY (page_label_id) REFERENCES page_labels(id) ON DELETE CASCADE,
				UNIQUE(page_id, page_label_id)
			);
			CREATE INDEX IF NOT EXISTS idx_page_label_assignments_page_id ON page_label_assignments(page_id);
			CREATE INDEX IF NOT EXISTS idx_page_label_assignments_label_id ON page_label_assignments(page_label_id);
		`,
		Postgres: `
			CREATE TABLE IF NOT EXISTS page_label_assignments (
				id SERIAL PRIMARY KEY,
				page_id INTEGER NOT NULL,
				page_label_id INTEGER NOT NULL,
				created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (page_id) REFERENCES pages(id) ON DELETE CASCADE,
				FOREIGN KEY (page_label_id) REFERENCES page_labels(id) ON DELETE CASCADE,
				UNIQUE(page_id, page_label_id)
			);
			CREATE INDEX IF NOT EXISTS idx_page_label_assignments_page_id ON page_label_assignments(page_id);
			CREATE INDEX IF NOT EXISTS idx_page_label_assignments_label_id ON page_label_assignments(page_label_id);
		`,
	},
	{
		// agent_runs records one execution of the coding-agent harness:
		// admission → container spawn → exit. agent_run_events captures the
		// per-run stdio / lifecycle stream that the orchestrator reads from
		// the agent's JSONL RPC mode and forwards to the SSE hub. binding_id is the FK
		// back to the workspace_agent_binding that triggered the run (NULL
		// for manually-started runs); the (binding_id, created_at) index
		// supports per-binding budget enforcement (WI-134).
		Version:       "20260529_agent_runs",
		Name:          "Create agent_runs + agent_run_events for the coding-agent harness",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='agent_runs'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='agent_runs'",
		SQLite: `
			CREATE TABLE IF NOT EXISTS agent_runs (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				workspace_id INTEGER NOT NULL,
				item_id INTEGER,
				binding_id INTEGER, -- soft ref to workspace_agent_bindings; that table is created in a later migration so no FK constraint, and agent_runs must outlive bindings for audit anyway
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
		`,
		Postgres: `
			CREATE TABLE IF NOT EXISTS agent_runs (
				id SERIAL PRIMARY KEY,
				workspace_id INTEGER NOT NULL,
				item_id INTEGER,
				binding_id INTEGER, -- soft ref to workspace_agent_bindings; that table is created in a later migration so no FK constraint, and agent_runs must outlive bindings for audit anyway
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
		`,
	},
	{
		// Agent acting-identity gate (see Coding Agent Harness — Design §7
		// and WI-87). Two pieces:
		//   1) system_settings row toggling whether workspace admins may
		//      bind agent runs to centralized service users at all.
		//   2) An allowlist of (user_id, workspace_id) pairs that *can*
		//      be picked when the flag is on. workspace_id=NULL grants
		//      the user as an acting identity across every workspace.
		// The chokepoint that consults both lives in
		// internal/services/agent_acting_identity_service.go; mutations
		// flow through the global-admin handlers (audit-logged).
		Version:       "20260529_agent_security_allowlist",
		Name:          "Create agent acting-identity allowlist + security setting",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='global_agent_acting_user_allowlist'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='global_agent_acting_user_allowlist'",
		SQLite: `
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
		`,
		Postgres: `
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
		`,
	},
	{
		// workspace_agent_bindings is the workspace-admin-managed link from
		// an acting user (the binding's identity, validated by the WI-87
		// chokepoint at create time) to the run-shape RunService needs
		// when an item is assigned to that user. See WI-88 / the Coding
		// Agent Harness — Design plan.
		//
		// One binding per (workspace, acting_user) — the lookup BindingService
		// does at assignee-change time is intentionally O(1) on this index.
		Version:       "20260529_workspace_agent_bindings",
		Name:          "Create workspace_agent_bindings",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='workspace_agent_bindings'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='workspace_agent_bindings'",
		SQLite: `
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
		`,
		Postgres: `
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
				created_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
			);
			CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_agent_bindings_workspace_acting
				ON workspace_agent_bindings(workspace_id, acting_user_id);
			CREATE INDEX IF NOT EXISTS idx_workspace_agent_bindings_workspace
				ON workspace_agent_bindings(workspace_id);
		`,
	},
	{
		// WI-90 broadens bindings to know which SCM connection the
		// orchestrator should authenticate against when fetching the
		// repo and opening PRs. workspace_scm_connections rows are
		// shared with the rest of the SCM machinery; the FK lets the
		// connection go away (ON DELETE SET NULL) without orphaning
		// the binding row — the trigger just skips the SCM step when
		// the column is NULL.
		Version:       "20260529_workspace_agent_bindings_scm_connection",
		Name:          "Add scm_connection_id to workspace_agent_bindings",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('workspace_agent_bindings') WHERE name='scm_connection_id'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='workspace_agent_bindings' AND column_name='scm_connection_id'",
		SQLite: `
			ALTER TABLE workspace_agent_bindings
				ADD COLUMN scm_connection_id INTEGER
					REFERENCES workspace_scm_connections(id) ON DELETE SET NULL;
			CREATE INDEX IF NOT EXISTS idx_workspace_agent_bindings_scm_connection
				ON workspace_agent_bindings(scm_connection_id);
		`,
		Postgres: `
			ALTER TABLE workspace_agent_bindings
				ADD COLUMN scm_connection_id INTEGER
					REFERENCES workspace_scm_connections(id) ON DELETE SET NULL;
			CREATE INDEX IF NOT EXISTS idx_workspace_agent_bindings_scm_connection
				ON workspace_agent_bindings(scm_connection_id);
		`,
	},
	{
		// workspace_agent_bindings.target_pool_id routes a binding's
		// coding-agent runs to a runner_pool capability instead of the local
		// in-process pool (WI-195). Soft ref to action_capabilities (no FK),
		// mirroring agent_runs.target_pool_id: NULL = local. Fresh installs get
		// it from schema/agents{,_postgres}.sql; this upgrades existing DBs.
		Version:       "20260604_workspace_agent_bindings_target_pool_id",
		Name:          "Add target_pool_id to workspace_agent_bindings",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('workspace_agent_bindings') WHERE name='target_pool_id'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='workspace_agent_bindings' AND column_name='target_pool_id'",
		SQLite:        "ALTER TABLE workspace_agent_bindings ADD COLUMN target_pool_id INTEGER",
		Postgres:      "ALTER TABLE workspace_agent_bindings ADD COLUMN target_pool_id INTEGER",
	},
	{
		// workspace_agent_bindings.runner_image lets a remote (pool) binding run
		// its coding-agent on a custom container image instead of the runner's
		// fixed default windshift-agent image (WI-450) — e.g. a Node+Chrome image
		// for Playwright e2e. NULL = the runner's default. Fresh installs get it
		// from schema/agents{,_postgres}.sql; this upgrades existing DBs.
		Version:       "20260623_workspace_agent_bindings_runner_image",
		Name:          "Add runner_image to workspace_agent_bindings",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('workspace_agent_bindings') WHERE name='runner_image'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='workspace_agent_bindings' AND column_name='runner_image'",
		SQLite:        "ALTER TABLE workspace_agent_bindings ADD COLUMN runner_image TEXT",
		Postgres:      "ALTER TABLE workspace_agent_bindings ADD COLUMN runner_image TEXT",
	},
	{
		// Remote runner pools (Initiative WI-141). A pool is an
		// action_capabilities row of type 'runner_pool'; these tables hang
		// off it by soft ref (no FK), mirroring the agent-table convention.
		// runner_registration_tokens: single-use (consumed on first
		// registration), revocable, pool-scoped tokens a runner presents to
		// register; runner_instances: one registered runner with its
		// per-instance credential + heartbeat.
		// Fresh installs get these from schema/agents{,_postgres}.sql; this
		// entry upgrades existing DBs (its Check stamps without re-running
		// once runner_instances exists).
		Version:       "20260602_runner_pool_tables",
		Name:          "Create runner_registration_tokens + runner_instances",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='runner_instances'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='runner_instances'",
		SQLite: `
			CREATE TABLE IF NOT EXISTS runner_registration_tokens (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				pool_capability_id INTEGER NOT NULL,
				token_hash TEXT NOT NULL UNIQUE,
				token_prefix TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				created_by_user_id INTEGER,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				expires_at DATETIME,
				revoked_at DATETIME
			);
			CREATE INDEX IF NOT EXISTS idx_runner_registration_tokens_pool
				ON runner_registration_tokens(pool_capability_id);
			CREATE TABLE IF NOT EXISTS runner_instances (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				pool_capability_id INTEGER NOT NULL,
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
		`,
		Postgres: `
			CREATE TABLE IF NOT EXISTS runner_registration_tokens (
				id SERIAL PRIMARY KEY,
				pool_capability_id INTEGER NOT NULL,
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
				pool_capability_id INTEGER NOT NULL,
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
		`,
	},
	{
		// agent_runs.runner_id records which remote runner executed a run
		// (NULL for the in-process local runner). Soft ref to
		// runner_instances; runs outlive instances for audit.
		Version:       "20260602_agent_runs_runner_id",
		Name:          "Add runner_id to agent_runs",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('agent_runs') WHERE name='runner_id'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='agent_runs' AND column_name='runner_id'",
		SQLite: `
			ALTER TABLE agent_runs ADD COLUMN runner_id INTEGER;
			CREATE INDEX IF NOT EXISTS idx_agent_runs_runner ON agent_runs(runner_id);
		`,
		Postgres: `
			ALTER TABLE agent_runs ADD COLUMN runner_id INTEGER;
			CREATE INDEX IF NOT EXISTS idx_agent_runs_runner ON agent_runs(runner_id);
		`,
	},
	{
		// agent_runs.target_pool_id routes a run to a runner_pool capability
		// (NULL = local in-process pool). Remote runners claim queued runs
		// scoped by this value; the index supports that DB-as-queue claim.
		Version:       "20260602_agent_runs_target_pool_id",
		Name:          "Add target_pool_id to agent_runs",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('agent_runs') WHERE name='target_pool_id'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='agent_runs' AND column_name='target_pool_id'",
		SQLite: `
			ALTER TABLE agent_runs ADD COLUMN target_pool_id INTEGER;
			CREATE INDEX IF NOT EXISTS idx_agent_runs_pool_claim ON agent_runs(target_pool_id, status, queued_at);
		`,
		Postgres: `
			ALTER TABLE agent_runs ADD COLUMN target_pool_id INTEGER;
			CREATE INDEX IF NOT EXISTS idx_agent_runs_pool_claim ON agent_runs(target_pool_id, status, queued_at);
		`,
	},
	{
		// agent_runs.cancel_requested_at signals that a running remote run
		// should abort. The orchestrator sets it (POST /agent-runs/{id}/cancel
		// for a remote run); the runner learns via its heartbeat response and
		// cancels the job, then reports canceled.
		Version:       "20260602_agent_runs_cancel_requested_at",
		Name:          "Add cancel_requested_at to agent_runs",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('agent_runs') WHERE name='cancel_requested_at'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='agent_runs' AND column_name='cancel_requested_at'",
		SQLite:        `ALTER TABLE agent_runs ADD COLUMN cancel_requested_at DATETIME;`,
		Postgres:      `ALTER TABLE agent_runs ADD COLUMN cancel_requested_at TIMESTAMPTZ;`,
	},
	{
		// agent_runs.grants_json + run_token_id back the secretless access
		// layer (WI-144): grants_json is the RunGrants snapshot the brokers
		// authorize against; run_token_id binds a presented credential to
		// this run's grants. Both nullable/additive.
		Version:       "20260602_agent_runs_grants",
		Name:          "Add grants_json + run_token_id to agent_runs",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('agent_runs') WHERE name='grants_json'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='agent_runs' AND column_name='grants_json'",
		SQLite: `
			ALTER TABLE agent_runs ADD COLUMN grants_json TEXT;
			ALTER TABLE agent_runs ADD COLUMN run_token_id INTEGER;
		`,
		Postgres: `
			ALTER TABLE agent_runs ADD COLUMN grants_json TEXT;
			ALTER TABLE agent_runs ADD COLUMN run_token_id INTEGER;
		`,
	},
	{
		// agent_runs.job_kind + job_image let non-coding-agent jobs
		// (action_container, ci_task) ride the same runner substrate (WI-146):
		// the runner picks its execution mode by kind and runs job_image for
		// container jobs (the fixed runner image is used for coding_agent).
		Version:       "20260602_agent_runs_job_kind",
		Name:          "Add job_kind + job_image to agent_runs",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('agent_runs') WHERE name='job_kind'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='agent_runs' AND column_name='job_kind'",
		SQLite: `
			ALTER TABLE agent_runs ADD COLUMN job_kind TEXT NOT NULL DEFAULT 'coding_agent';
			ALTER TABLE agent_runs ADD COLUMN job_image TEXT;
		`,
		Postgres: `
			ALTER TABLE agent_runs ADD COLUMN job_kind TEXT NOT NULL DEFAULT 'coding_agent';
			ALTER TABLE agent_runs ADD COLUMN job_image TEXT;
		`,
	},
	{
		// agent_runs.binding_id is a soft ref to workspace_agent_bindings (a run
		// outlives its binding for audit). It has always been in the CREATE TABLE
		// but never had an ADD-COLUMN migration, so a database created before
		// binding_id entered the schema never got the column and the run insert
		// fails. This backfills it; on a DB that already has the column the Check
		// matches and the migration is stamped without re-running the DDL.
		Version:       "20260609_agent_runs_binding_id",
		Name:          "Add binding_id to agent_runs",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('agent_runs') WHERE name='binding_id'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='agent_runs' AND column_name='binding_id'",
		SQLite: `
			ALTER TABLE agent_runs ADD COLUMN binding_id INTEGER;
			CREATE INDEX IF NOT EXISTS idx_agent_runs_binding_created ON agent_runs(binding_id, created_at DESC);
		`,
		Postgres: `
			ALTER TABLE agent_runs ADD COLUMN binding_id INTEGER;
			CREATE INDEX IF NOT EXISTS idx_agent_runs_binding_created ON agent_runs(binding_id, created_at DESC);
		`,
	},
	{
		// agent_runs.triggered_by_user_id records who caused the run (the
		// user whose assignment fired the binding trigger, or the admin who
		// started a test run). On OAuth SCM connections this user's personal
		// token is the credential for the run's git traffic and PR creation
		// (WI-275). Soft ref to users: runs must outlive users for audit.
		Version:       "20260610_agent_runs_triggered_by",
		Name:          "Add triggered_by_user_id to agent_runs",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('agent_runs') WHERE name='triggered_by_user_id'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='agent_runs' AND column_name='triggered_by_user_id'",
		SQLite: `
			ALTER TABLE agent_runs ADD COLUMN triggered_by_user_id INTEGER;
		`,
		Postgres: `
			ALTER TABLE agent_runs ADD COLUMN triggered_by_user_id INTEGER;
		`,
	},
	{
		// WI-258: per-workspace agent skills library + per-binding custom
		// instructions. Skills are markdown knowledge packs (Anthropic Agent
		// Skills shape) attached to bindings m:n; the run's initial prompt
		// indexes them and the agent fetches bodies via `ws skill get`.
		// binding.instructions is appended to the initial prompt as the
		// agent's role/persona.
		Version:       "20260610_workspace_agent_skills",
		Name:          "Agent skills library + binding instructions",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('workspace_agent_bindings') WHERE name='instructions'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='workspace_agent_bindings' AND column_name='instructions'",
		SQLite: `
			ALTER TABLE workspace_agent_bindings ADD COLUMN instructions TEXT NOT NULL DEFAULT '';
			CREATE TABLE IF NOT EXISTS workspace_agent_skills (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				workspace_id INTEGER NOT NULL,
				name TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				body TEXT NOT NULL DEFAULT '',
				enabled BOOLEAN NOT NULL DEFAULT 1,
				created_by_user_id INTEGER,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
				FOREIGN KEY (created_by_user_id) REFERENCES users(id) ON DELETE SET NULL
			);
			CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_agent_skills_workspace_name
				ON workspace_agent_skills(workspace_id, name);
			CREATE TABLE IF NOT EXISTS workspace_agent_binding_skills (
				binding_id INTEGER NOT NULL,
				skill_id INTEGER NOT NULL,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (binding_id, skill_id),
				FOREIGN KEY (binding_id) REFERENCES workspace_agent_bindings(id) ON DELETE CASCADE,
				FOREIGN KEY (skill_id) REFERENCES workspace_agent_skills(id) ON DELETE CASCADE
			);
			CREATE INDEX IF NOT EXISTS idx_workspace_agent_binding_skills_skill
				ON workspace_agent_binding_skills(skill_id);
		`,
		Postgres: `
			ALTER TABLE workspace_agent_bindings ADD COLUMN instructions TEXT NOT NULL DEFAULT '';
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
		`,
	},
	{
		// active_timers "only one running timer per user" was enforced solely by
		// a TOCTOU check (HasActiveTimerForUser → CreateTimer, two statements, no
		// txn) in TimerService.StartTimer, so concurrent starts could create
		// multiple running timers. Make the user_id index UNIQUE so the DB is the
		// backstop; the repo maps the resulting constraint violation to
		// ErrTimerAlreadyRunning (WI-298). Before swapping the index we delete all
		// but the latest active timer per user so any pre-existing duplicates
		// don't block the UNIQUE index creation.
		//
		// The schema (system{,_postgres}.sql) creates idx_active_timers_user_id as
		// a plain index; this migration drops it and recreates it UNIQUE. The
		// Check reports the effect present once the index is already unique, so it
		// is idempotent on installs that already have it (incl. fresh installs
		// whose schema bootstrap will adopt the UNIQUE form on SQLite but not on
		// Postgres — this migration unifies both).
		Version: "20260610_active_timers_unique_user",
		Name:    "Enforce one active timer per user via UNIQUE(user_id)",
		CheckSQLite: `SELECT COUNT(*) FROM pragma_index_list('active_timers')
			WHERE name='idx_active_timers_user_id' AND "unique"=1`,
		CheckPostgres: `SELECT COUNT(*) FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			WHERE c.relname='idx_active_timers_user_id' AND i.indisunique`,
		SQLite: `
			DELETE FROM active_timers
			WHERE id NOT IN (
				SELECT MAX(id) FROM active_timers GROUP BY user_id
			);
			DROP INDEX IF EXISTS idx_active_timers_user_id;
			CREATE UNIQUE INDEX IF NOT EXISTS idx_active_timers_user_id ON active_timers(user_id);
		`,
		Postgres: `
			DELETE FROM active_timers
			WHERE id NOT IN (
				SELECT MAX(id) FROM active_timers GROUP BY user_id
			);
			DROP INDEX IF EXISTS idx_active_timers_user_id;
			CREATE UNIQUE INDEX IF NOT EXISTS idx_active_timers_user_id ON active_timers(user_id);
		`,
	},
	{
		// agent_runs.trigger_json holds the run's trigger context + free-form
		// instruction (the body of the @mentioning comment that started the
		// run) as a single JSON blob, so new instruction shapes need no
		// further schema migration. JSONB on Postgres (queryable), TEXT on
		// SQLite (which has no jsonb type). Nullable — runs created before the
		// column, and triggers with no extra context, leave it NULL.
		Version:       "20260615_agent_runs_trigger_json",
		Name:          "Add trigger_json to agent_runs",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('agent_runs') WHERE name='trigger_json'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='agent_runs' AND column_name='trigger_json'",
		SQLite: `
			ALTER TABLE agent_runs ADD COLUMN trigger_json TEXT;
		`,
		Postgres: `
			ALTER TABLE agent_runs ADD COLUMN trigger_json JSONB;
		`,
	},
	{
		// WI-402 Todoist personal-task sync. todoist_sync_config holds the
		// per-(user, provider) sync configuration; todoist_task_links is the
		// item <-> Todoist-task id map with a last-synced snapshot for
		// field-level last-write-wins. Both reuse integration_providers /
		// user_integration_tokens for the connection. Fresh installs get these
		// from schema/integrations{,_postgres}.sql; this entry upgrades existing
		// DBs (its Check stamps without re-running once todoist_task_links exists).
		Version:       "20260615_todoist_sync_tables",
		Name:          "Create todoist_sync_config + todoist_task_links for personal-task sync",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='todoist_task_links'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='todoist_task_links'",
		SQLite: `
			CREATE TABLE IF NOT EXISTS todoist_sync_config (
				id TEXT PRIMARY KEY,
				user_id TEXT NOT NULL,
				integration_provider_id TEXT NOT NULL,
				personal_workspace_id INTEGER NOT NULL,
				enabled BOOLEAN DEFAULT 0,
				scope_mode TEXT NOT NULL DEFAULT 'all',
				todoist_project_id TEXT DEFAULT '',
				sync_token TEXT DEFAULT '*',
				last_synced_at DATETIME,
				last_error TEXT DEFAULT '',
				sync_lock_until DATETIME,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (integration_provider_id) REFERENCES integration_providers(id) ON DELETE CASCADE,
				UNIQUE(user_id, integration_provider_id)
			);
			CREATE INDEX IF NOT EXISTS idx_todoist_sync_config_user ON todoist_sync_config(user_id);
			CREATE INDEX IF NOT EXISTS idx_todoist_sync_config_enabled ON todoist_sync_config(enabled);

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
				last_completed BOOLEAN DEFAULT 0,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				UNIQUE(user_id, todoist_task_id),
				UNIQUE(item_id)
			);
			CREATE INDEX IF NOT EXISTS idx_todoist_task_links_user ON todoist_task_links(user_id);
			CREATE INDEX IF NOT EXISTS idx_todoist_task_links_item ON todoist_task_links(item_id);
			CREATE INDEX IF NOT EXISTS idx_todoist_task_links_todoist ON todoist_task_links(todoist_task_id);
		`,
		Postgres: `
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
		`,
	},
	{
		// todoist_sync_config.sync_lock_until backs the per-config sync
		// admission lock (security fix, WI-402): SyncConfig sets it to a future
		// lease while a run holds the config and clears it on completion, so a
		// manual "Sync now" and the 5-minute poller cannot reconcile the same
		// config concurrently and double-create Todoist tasks. The base table
		// migration above carries the column in its CREATE TABLE for installs
		// that hadn't created the table yet; this entry adds it to installs that
		// created todoist_sync_config before the column existed.
		Version:       "20260615_todoist_sync_lock_until",
		Name:          "Add sync_lock_until to todoist_sync_config",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('todoist_sync_config') WHERE name='sync_lock_until'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='todoist_sync_config' AND column_name='sync_lock_until'",
		SQLite:        "ALTER TABLE todoist_sync_config ADD COLUMN sync_lock_until DATETIME",
		Postgres:      "ALTER TABLE todoist_sync_config ADD COLUMN sync_lock_until TIMESTAMPTZ",
	},
	{
		// Generalize the cfv-cleanup queue into a multi-purpose custom-field
		// maintenance queue: job_type selects field_scrub / option_removal /
		// index_build, payload carries the per-job detail. Existing in-flight
		// rows default to 'field_scrub', their original meaning.
		Version:       "20260616_cfv_cleanup_job_type_payload",
		Name:          "Add job_type + payload to pending_custom_field_cleanups",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('pending_custom_field_cleanups') WHERE name='job_type'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='pending_custom_field_cleanups' AND column_name='job_type'",
		SQLite: `
			ALTER TABLE pending_custom_field_cleanups ADD COLUMN job_type TEXT NOT NULL DEFAULT 'field_scrub';
			ALTER TABLE pending_custom_field_cleanups ADD COLUMN payload TEXT;
		`,
		Postgres: `
			ALTER TABLE pending_custom_field_cleanups ADD COLUMN job_type TEXT NOT NULL DEFAULT 'field_scrub';
			ALTER TABLE pending_custom_field_cleanups ADD COLUMN payload TEXT;
		`,
	},
	{
		// Recency timestamp powering the board's "Bubble Mode" sort: cards
		// ordered by most-recently-active first. Bumped on comments, edits and
		// transitions (never on manual frac_index reorder) so it stays distinct
		// from updated_at's "last edited"/change-log semantics. Backfilled from
		// updated_at so existing rows have a sensible initial order.
		Version:       "20260619_items_last_active_at",
		Name:          "Add items.last_active_at for board Bubble Mode sort",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('items') WHERE name='last_active_at'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='items' AND column_name='last_active_at'",
		SQLite: `
			ALTER TABLE items ADD COLUMN last_active_at DATETIME;
			UPDATE items SET last_active_at = COALESCE(updated_at, created_at) WHERE last_active_at IS NULL;
			CREATE INDEX IF NOT EXISTS idx_items_workspace_last_active ON items(workspace_id, last_active_at);
		`,
		Postgres: `
			ALTER TABLE items ADD COLUMN last_active_at TIMESTAMPTZ;
			UPDATE items SET last_active_at = COALESCE(updated_at, created_at) WHERE last_active_at IS NULL;
			CREATE INDEX IF NOT EXISTS idx_items_workspace_last_active ON items(workspace_id, last_active_at);
		`,
	},
	{
		// WI-449: multi-repo agent bindings. A binding may bind N repos; this
		// child table holds them (one is_primary row per binding). Backfill
		// each existing binding's single scalar repo as its primary repo. The
		// legacy scalar columns on workspace_agent_bindings are kept one
		// release as a dormant rollback net and dropped in a later migration.
		Version:       "20260620_workspace_agent_binding_repos",
		Name:          "Multi-repo bindings: child table + backfill primary repo",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='workspace_agent_binding_repos'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='workspace_agent_binding_repos'",
		SQLite: `
			CREATE TABLE workspace_agent_binding_repos (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				binding_id INTEGER NOT NULL,
				scm_connection_id INTEGER,
				repo_slug TEXT NOT NULL,
				repo_base_ref TEXT NOT NULL DEFAULT '',
				is_primary BOOLEAN NOT NULL DEFAULT 0,
				position INTEGER NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (binding_id) REFERENCES workspace_agent_bindings(id) ON DELETE CASCADE,
				FOREIGN KEY (scm_connection_id) REFERENCES workspace_scm_connections(id) ON DELETE SET NULL
			);
			CREATE UNIQUE INDEX idx_wab_repos_binding_slug ON workspace_agent_binding_repos(binding_id, repo_slug);
			CREATE UNIQUE INDEX idx_wab_repos_one_primary ON workspace_agent_binding_repos(binding_id) WHERE is_primary;
			CREATE INDEX idx_wab_repos_binding ON workspace_agent_binding_repos(binding_id);
			INSERT INTO workspace_agent_binding_repos
				(binding_id, scm_connection_id, repo_slug, repo_base_ref, is_primary, position)
			SELECT id, scm_connection_id, repo_slug, COALESCE(repo_base_ref, ''), 1, 0
			FROM workspace_agent_bindings
			WHERE repo_slug IS NOT NULL AND repo_slug <> '';
		`,
		Postgres: `
			CREATE TABLE workspace_agent_binding_repos (
				id SERIAL PRIMARY KEY,
				binding_id INTEGER NOT NULL,
				scm_connection_id INTEGER,
				repo_slug TEXT NOT NULL,
				repo_base_ref TEXT NOT NULL DEFAULT '',
				is_primary BOOLEAN NOT NULL DEFAULT FALSE,
				position INTEGER NOT NULL DEFAULT 0,
				created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (binding_id) REFERENCES workspace_agent_bindings(id) ON DELETE CASCADE,
				FOREIGN KEY (scm_connection_id) REFERENCES workspace_scm_connections(id) ON DELETE SET NULL
			);
			CREATE UNIQUE INDEX idx_wab_repos_binding_slug ON workspace_agent_binding_repos(binding_id, repo_slug);
			CREATE UNIQUE INDEX idx_wab_repos_one_primary ON workspace_agent_binding_repos(binding_id) WHERE is_primary;
			CREATE INDEX idx_wab_repos_binding ON workspace_agent_binding_repos(binding_id);
			INSERT INTO workspace_agent_binding_repos
				(binding_id, scm_connection_id, repo_slug, repo_base_ref, is_primary, position)
			SELECT id, scm_connection_id, repo_slug, COALESCE(repo_base_ref, ''), TRUE, 0
			FROM workspace_agent_bindings
			WHERE repo_slug IS NOT NULL AND repo_slug <> '';
		`,
	},
	{
		// Backfill: the migration above adds last_active_at without a DEFAULT,
		// and the CreateItem insert path originally omitted the column, so items
		// created via the API after the first migration landed with a NULL
		// last_active_at. Scanning NULL into the non-nullable time.Time field
		// 500s the entire item list. The insert path now writes last_active_at;
		// this re-run mops up rows stranded in the window before that fix. No
		// Check — the UPDATE is idempotent, so it is safe to always run.
		Version: "20260619_items_last_active_at_backfill",
		Name:    "Backfill NULL items.last_active_at left by the insert path",
		// Skip on a clean DB (no NULL rows) — that's a fresh install (the schema
		// + insert path never leave last_active_at NULL) or an already-backfilled
		// one. Returns 1 → stamp without running; 0 (NULL rows present) → run the
		// idempotent backfill. Keeps the catalog upgrade-only for fresh installs.
		CheckSQLite:   "SELECT CASE WHEN EXISTS(SELECT 1 FROM items WHERE last_active_at IS NULL) THEN 0 ELSE 1 END",
		CheckPostgres: "SELECT CASE WHEN EXISTS(SELECT 1 FROM items WHERE last_active_at IS NULL) THEN 0 ELSE 1 END",
		SQLite: `
			UPDATE items SET last_active_at = COALESCE(updated_at, created_at) WHERE last_active_at IS NULL;
		`,
		Postgres: `
			UPDATE items SET last_active_at = COALESCE(updated_at, created_at) WHERE last_active_at IS NULL;
		`,
	},
	{
		// daily_briefings.lock_until is the cross-instance generation lock
		// (WI-418). With multiple app instances the per-user briefing run is a
		// check-then-act race: both read "no briefing for today", both invoke
		// the LLM, and the UNIQUE(user_id, date) ON CONFLICT UPDATE lets one
		// silently overwrite the other — wasted LLM spend. A guarded
		// UPDATE/UPSERT on lock_until is the atomic claim: the holder leases
		// until lock_until and clears it on completion, and a crashed holder
		// self-heals once the lease expires. The base schema
		// (daily_briefings.sql) carries the column for fresh installs; this
		// adds it to installs created before the column existed.
		Version:       "20260620_daily_briefings_lock_until",
		Name:          "Add lock_until to daily_briefings",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('daily_briefings') WHERE name='lock_until'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='daily_briefings' AND column_name='lock_until'",
		SQLite:        "ALTER TABLE daily_briefings ADD COLUMN lock_until DATETIME",
		Postgres:      "ALTER TABLE daily_briefings ADD COLUMN lock_until TIMESTAMPTZ",
	},
	{
		// Work item templates (WI-438): workspace-scoped reusable bodies that
		// pre-fill a new item's description. item_templates holds the body +
		// mode (selectable|mandatory) + is_active; item_template_item_types is
		// the optional N:N target-type filter (mandatory templates target
		// exactly one type, enforced in the service layer). The covering index
		// idx_item_template_item_types_type and idx_item_templates_ws_mode_active
		// back the (workspace, item_type) mandatory-template lookup that
		// CreateItem runs on every create. No schema/*.sql counterpart — this
		// catalog entry is canonical for both fresh and upgrading installs (the
		// existence check stamps without re-running where the tables exist).
		Version:       "20260619_work_item_templates",
		Name:          "Add item_templates + item_template_item_types tables",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='item_templates'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='item_templates'",
		SQLite: `
			CREATE TABLE IF NOT EXISTS item_templates (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				workspace_id INTEGER NOT NULL,
				name TEXT NOT NULL,
				description_body TEXT NOT NULL DEFAULT '',
				mode TEXT NOT NULL DEFAULT 'selectable',
				is_active BOOLEAN NOT NULL DEFAULT true,
				created_by INTEGER,
				updated_by INTEGER,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
				FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL,
				FOREIGN KEY (updated_by) REFERENCES users(id) ON DELETE SET NULL,
				UNIQUE(name, workspace_id)
			);
			CREATE INDEX IF NOT EXISTS idx_item_templates_workspace_id ON item_templates(workspace_id);
			CREATE INDEX IF NOT EXISTS idx_item_templates_ws_mode_active ON item_templates(workspace_id, mode, is_active);

			CREATE TABLE IF NOT EXISTS item_template_item_types (
				template_id INTEGER NOT NULL,
				item_type_id INTEGER NOT NULL,
				PRIMARY KEY (template_id, item_type_id),
				FOREIGN KEY (template_id) REFERENCES item_templates(id) ON DELETE CASCADE,
				FOREIGN KEY (item_type_id) REFERENCES item_types(id) ON DELETE CASCADE
			);
			CREATE INDEX IF NOT EXISTS idx_item_template_item_types_type ON item_template_item_types(item_type_id, template_id);
		`,
		Postgres: `
			CREATE TABLE IF NOT EXISTS item_templates (
				id SERIAL PRIMARY KEY,
				workspace_id INTEGER NOT NULL,
				name TEXT NOT NULL,
				description_body TEXT NOT NULL DEFAULT '',
				mode TEXT NOT NULL DEFAULT 'selectable',
				is_active BOOLEAN NOT NULL DEFAULT true,
				created_by INTEGER,
				updated_by INTEGER,
				created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
				FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL,
				FOREIGN KEY (updated_by) REFERENCES users(id) ON DELETE SET NULL,
				UNIQUE(name, workspace_id)
			);
			CREATE INDEX IF NOT EXISTS idx_item_templates_workspace_id ON item_templates(workspace_id);
			CREATE INDEX IF NOT EXISTS idx_item_templates_ws_mode_active ON item_templates(workspace_id, mode, is_active);

			CREATE TABLE IF NOT EXISTS item_template_item_types (
				template_id INTEGER NOT NULL,
				item_type_id INTEGER NOT NULL,
				PRIMARY KEY (template_id, item_type_id),
				FOREIGN KEY (template_id) REFERENCES item_templates(id) ON DELETE CASCADE,
				FOREIGN KEY (item_type_id) REFERENCES item_types(id) ON DELETE CASCADE
			);
			CREATE INDEX IF NOT EXISTS idx_item_template_item_types_type ON item_template_item_types(item_type_id, template_id);
		`,
	},
	{
		// Per-call LLM token usage + cost, metered at the broker. Foundation
		// for the (still unenforced) RunGrants.LLM.QuotaTokens follow-up.
		Version:       "20260623_llm_usage",
		Name:          "Add llm_usage table for broker token/cost metering",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='llm_usage'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='llm_usage'",
		SQLite: `
			CREATE TABLE IF NOT EXISTS llm_usage (
				id                INTEGER PRIMARY KEY AUTOINCREMENT,
				run_id            INTEGER NOT NULL,
				model             TEXT NOT NULL,
				prompt_tokens     INTEGER NOT NULL DEFAULT 0,
				completion_tokens INTEGER NOT NULL DEFAULT 0,
				total_tokens      INTEGER NOT NULL DEFAULT 0,
				cost_usd          REAL,
				cost_source       TEXT NOT NULL DEFAULT '',
				created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			);
			CREATE INDEX IF NOT EXISTS idx_llm_usage_run_id ON llm_usage(run_id);
		`,
		Postgres: `
			CREATE TABLE IF NOT EXISTS llm_usage (
				id                SERIAL PRIMARY KEY,
				run_id            INTEGER NOT NULL,
				model             TEXT NOT NULL,
				prompt_tokens     INTEGER NOT NULL DEFAULT 0,
				completion_tokens INTEGER NOT NULL DEFAULT 0,
				total_tokens      INTEGER NOT NULL DEFAULT 0,
				cost_usd          DOUBLE PRECISION,
				cost_source       TEXT NOT NULL DEFAULT '',
				created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
			);
			CREATE INDEX IF NOT EXISTS idx_llm_usage_run_id ON llm_usage(run_id);
		`,
	},
	{
		// WI-517: reference-pages picker on agent skills. A skill may
		// reference N workspace pages; their markdown is inlined into the
		// body the agent fetches via `ws skill get`. The matching
		// schema/{agents,agents_postgres}.sql has the table since the WI-517
		// PR shipped the table in the fresh-install concat, but the catalog
		// entry was never added, so existing installs (the prod DB that hit
		// this) upgrade past the PR without the table and 500 on
		// GET /api/workspaces/<ws>/agent-skills (list skill pages). No data
		// to backfill; pure CREATE.
		Version:       "20260624_workspace_agent_skill_pages",
		Name:          "Add workspace_agent_skill_pages reference-pages table",
		CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='workspace_agent_skill_pages'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='workspace_agent_skill_pages'",
		SQLite: `
			CREATE TABLE IF NOT EXISTS workspace_agent_skill_pages (
				skill_id INTEGER NOT NULL,
				page_id INTEGER NOT NULL,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (skill_id, page_id),
				FOREIGN KEY (skill_id) REFERENCES workspace_agent_skills(id) ON DELETE CASCADE,
				FOREIGN KEY (page_id) REFERENCES pages(id) ON DELETE CASCADE
			);
			CREATE INDEX IF NOT EXISTS idx_workspace_agent_skill_pages_page
				ON workspace_agent_skill_pages(page_id);
		`,
		Postgres: `
			CREATE TABLE IF NOT EXISTS workspace_agent_skill_pages (
				skill_id INTEGER NOT NULL,
				page_id INTEGER NOT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (skill_id, page_id),
				FOREIGN KEY (skill_id) REFERENCES workspace_agent_skills(id) ON DELETE CASCADE,
				FOREIGN KEY (page_id) REFERENCES pages(id) ON DELETE CASCADE
			);
			CREATE INDEX IF NOT EXISTS idx_workspace_agent_skill_pages_page
				ON workspace_agent_skill_pages(page_id);
		`,
	},
	{
		// WI-515: manual sort order for milestones (drag-and-drop reorder).
		// position is scoped per (is_global, workspace_id, category_id);
		// new milestones get MaxPosition(scope) + 1000 (mirrors test
		// folders). The column-add is guarded by a column-exists check, but
		// because the body also backfills, we additionally guard the backfill
		// on position = 0 so a re-run after a manual reorder (which would
		// leave some rows at 0 only if they were never reordered) is a no-op
		// for already-ordered scopes. Fresh installs get the column from the
		// schema concat and stamp without running the body.
		Version:       "20260624_milestones_position",
		Name:          "Add milestones.position for drag-and-drop reorder",
		CheckSQLite:   sqliteColumnCheck("milestones", "position"),
		CheckPostgres: pgColumnCheck("milestones", "position"),
		SQLite: `
			ALTER TABLE milestones ADD COLUMN position INTEGER NOT NULL DEFAULT 0;
			-- Backfill: assign (row_number * 1000) within each scope, ordered by
			-- the legacy target_date, name sort. Only touches rows still at the
			-- default 0 so a post-reorder re-run is a no-op. Use a ranked derived
			-- table keyed by id so ROW_NUMBER() sees the full scope, not only the
			-- current outer row.
			UPDATE milestones
			SET position = (
				SELECT ranked.new_position
				FROM (
					SELECT id, (ROW_NUMBER() OVER (
						PARTITION BY is_global, COALESCE(workspace_id, 0), COALESCE(category_id, 0)
						ORDER BY target_date IS NULL, target_date, name
					) * 1000) AS new_position
					FROM milestones
				) ranked
				WHERE ranked.id = milestones.id
			)
			WHERE position = 0;
			CREATE INDEX IF NOT EXISTS idx_milestones_position
				ON milestones(is_global, workspace_id, category_id, position);
		`,
		Postgres: `
			ALTER TABLE milestones ADD COLUMN IF NOT EXISTS position INTEGER NOT NULL DEFAULT 0;
			UPDATE milestones
			SET position = sub.new_position
			FROM (
				SELECT id, (ROW_NUMBER() OVER (
					PARTITION BY is_global, COALESCE(workspace_id, 0), COALESCE(category_id, 0)
					ORDER BY target_date NULLS LAST, name
				) * 1000) AS new_position
				FROM milestones
			) sub
			WHERE milestones.id = sub.id AND milestones.position = 0;
			CREATE INDEX IF NOT EXISTS idx_milestones_position
				ON milestones(is_global, workspace_id, category_id, position);
		`,
	},
	{
		Version:       "20260706_pages_metadata",
		Name:          "Add pages.metadata JSON object",
		CheckSQLite:   sqliteColumnCheck("pages", "metadata"),
		CheckPostgres: pgColumnCheck("pages", "metadata"),
		SQLite: `
			ALTER TABLE pages ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}';
		`,
		Postgres: `
			ALTER TABLE pages ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
		`,
	},
	{
		Version:       "20260710_sso_state_request_id",
		Name:          "Add sso_state_tokens.request_id for SAML AuthnRequest binding",
		CheckSQLite:   sqliteColumnCheck("sso_state_tokens", "request_id"),
		CheckPostgres: pgColumnCheck("sso_state_tokens", "request_id"),
		SQLite: `
			ALTER TABLE sso_state_tokens ADD COLUMN request_id TEXT;
		`,
		Postgres: `
			ALTER TABLE sso_state_tokens ADD COLUMN IF NOT EXISTS request_id TEXT;
		`,
	},
	{
		Version:       "20260710_user_sessions_enrollment_required_postgres",
		Name:          "Add missing Postgres user session enrollment state",
		CheckSQLite:   sqliteColumnCheck("user_sessions", "enrollment_required"),
		CheckPostgres: pgColumnCheck("user_sessions", "enrollment_required"),
		Postgres:      "ALTER TABLE user_sessions ADD COLUMN enrollment_required BOOLEAN DEFAULT false",
	},
	{
		Version:       "20260710_user_sessions_auth_pending_type",
		Name:          "Add typed pending authentication state to user sessions",
		CheckSQLite:   sqliteColumnCheck("user_sessions", "auth_pending_type"),
		CheckPostgres: pgColumnCheck("user_sessions", "auth_pending_type"),
		SQLite:        "ALTER TABLE user_sessions ADD COLUMN auth_pending_type TEXT",
		Postgres:      "ALTER TABLE user_sessions ADD COLUMN auth_pending_type TEXT",
	},
}

func (m Migration) checksum(driver string) string {
	var body string
	switch driver {
	case driverSQLite:
		body = m.SQLite
	case driverPostgres:
		body = m.Postgres
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// runPendingMigrations applies catalog entries that aren't yet stamped in
// schema_migrations. For each pending migration: if its backend-specific
// Check predicate reports the effect is already present, the row is stamped
// without re-running the DDL (retroactive backfill); otherwise the DDL runs
// inside a transaction that ends with the stamp INSERT so the pair is
// atomic.
//
// Errors abort startup. There is no log-and-continue.
func runPendingMigrations(db Database, catalog []Migration) error {
	driver := db.GetDriverName()

	applied, err := loadAppliedMigrations(db)
	if err != nil {
		return fmt.Errorf("load applied migrations: %w", err)
	}

	for _, m := range catalog {
		if _, ok := applied[m.Version]; ok {
			continue
		}
		if err := applyMigration(db, driver, m); err != nil {
			return fmt.Errorf("migration %s (%s): %w", m.Version, m.Name, err)
		}
	}
	return nil
}

func loadAppliedMigrations(db Database) (map[string]struct{}, error) {
	rows, err := db.Query("SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]struct{}{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = struct{}{}
	}
	return out, rows.Err()
}

func applyMigration(db Database, driver string, m Migration) error {
	var checkSQL, body string
	switch driver {
	case driverSQLite:
		checkSQL, body = m.CheckSQLite, m.SQLite
	case driverPostgres:
		checkSQL, body = m.CheckPostgres, m.Postgres
	default:
		return fmt.Errorf("unknown driver %q", driver)
	}

	// Migration is a no-op on this backend — stamp without running anything.
	if body == "" {
		return stampMigration(db, m, driver)
	}

	// Retroactive backfill: if the effect is already present, stamp without
	// re-running. Migrations with no Check always run.
	if checkSQL != "" {
		var count int
		if err := db.QueryRow(checkSQL).Scan(&count); err != nil {
			return fmt.Errorf("check: %w", err)
		}
		if count > 0 {
			return stampMigration(db, m, driver)
		}
	}

	return WithTx(db, func(tx Tx) error {
		if _, err := tx.Exec(body); err != nil {
			return fmt.Errorf("apply: %w", err)
		}
		_, err := tx.Exec(
			"INSERT INTO schema_migrations(version, name, checksum) VALUES(?, ?, ?)",
			m.Version, m.Name, m.checksum(driver),
		)
		return err
	})
}

func stampMigration(db Database, m Migration, driver string) error {
	_, err := db.Exec(
		"INSERT INTO schema_migrations(version, name, checksum) VALUES(?, ?, ?)",
		m.Version, m.Name, m.checksum(driver),
	)
	return err
}
