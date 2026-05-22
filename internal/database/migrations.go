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
