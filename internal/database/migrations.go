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
		// pi's RPC mode and forwards to the SSE hub. binding_id is the FK
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
		// Remote runner pools (Initiative WI-141). A pool is an
		// action_capabilities row of type 'runner_pool'; these tables hang
		// off it by soft ref (no FK), mirroring the agent-table convention.
		// runner_registration_tokens: reusable, revocable, pool-scoped
		// tokens a runner presents to register; runner_instances: one
		// registered runner with its per-instance credential + heartbeat.
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
