package database

import "fmt"

// Catalog entries are ported 1:1 from the legacy migrations and
// pgMigrations slices in database.go and postgres.go. Each entry pairs
// the SQLite and Postgres equivalents of the same logical schema change.
// Backend-specific changes (e.g. Postgres JSONB conversions, the Postgres
// CHECK constraints that SQLite enforces via triggers) leave the other
// backend's SQL empty — the runner still stamps the version so both
// backends share a single ordered catalog.
//
// During the porting window (PR 2a → PR 2d), the legacy migration loops
// still run first; runPendingMigrations finds Check returns true for
// every catalog entry on existing installs and stamps without re-running
// the DDL. PR 2d deletes the legacy loops once parity is verified.

// sqliteColumnCheck and pgColumnCheck build the canonical "column exists"
// query used by virtually every column-add migration.
func sqliteColumnCheck(table, column string) string {
	return fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name='%s'", table, column)
}

func pgColumnCheck(table, column string) string {
	return fmt.Sprintf(
		"SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='%s' AND column_name='%s'",
		table, column,
	)
}

// pgConstraintCheck builds the "named constraint exists" query for the
// Postgres-only CHECK constraints on the users table.
func pgConstraintCheck(table, constraint string) string {
	return fmt.Sprintf(
		"SELECT COUNT(*) FROM information_schema.table_constraints WHERE table_schema=current_schema() AND table_name='%s' AND constraint_name='%s'",
		table, constraint,
	)
}

// pgCustomFieldValuesJSONBCheck returns 1 when custom_field_values on
// the given table is already JSONB. Used by the TEXT → JSONB conversion
// migrations across the four tables that carry that column.
func pgCustomFieldValuesJSONBCheck(table string) string {
	return fmt.Sprintf(
		"SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='%s' AND column_name='custom_field_values' AND data_type='jsonb'",
		table,
	)
}

// sqliteMilestonesColumnAbsentCheck and pgMilestonesColumnAbsentCheck
// invert the column-exists check for the milestone SCM column drops.
// They return 1 when the column is gone (migration applied), 0 when
// it still exists (DROP needs to run). SQLite has no DROP COLUMN IF
// EXISTS, so the runner must skip the body when the column is already
// gone. Hard-coded to the milestones table because that's the only
// table currently using inverted checks; if we add more drop-style
// migrations, generalize then.
func sqliteMilestonesColumnAbsentCheck(column string) string {
	return fmt.Sprintf(
		"SELECT CASE WHEN EXISTS(SELECT 1 FROM pragma_table_info('milestones') WHERE name='%s') THEN 0 ELSE 1 END",
		column,
	)
}

func pgMilestonesColumnAbsentCheck(column string) string {
	return fmt.Sprintf(
		"SELECT CASE WHEN EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='milestones' AND column_name='%s') THEN 0 ELSE 1 END",
		column,
	)
}

// sqliteTableCheck and pgTableCheck build the canonical "table exists"
// query used by inline-block migrations.
func sqliteTableCheck(table string) string {
	return fmt.Sprintf("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='%s'", table)
}

func pgTableCheck(table string) string {
	return fmt.Sprintf(
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='%s'",
		table,
	)
}

// sqliteIndexCheck / pgIndexCheck for index-only migrations. The Postgres
// variant is pinned to current_schema() — matching an identically-named
// index in an unrelated schema would falsely stamp the migration as applied.
func sqliteIndexCheck(idx string) string {
	return fmt.Sprintf("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='%s'", idx)
}

func pgIndexCheck(idx string) string {
	return fmt.Sprintf(
		"SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind='i' AND c.relname='%s' AND n.nspname=current_schema()",
		idx,
	)
}

// sqliteTriggerCheck for SQLite-only trigger migrations. Postgres uses
// functions+triggers and is checked via pgFunctionCheck.
func sqliteTriggerCheck(trigger string) string {
	return fmt.Sprintf("SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='%s'", trigger)
}

func pgTriggerCheck(trigger string) string {
	return fmt.Sprintf(
		"SELECT COUNT(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE t.tgname='%s' AND n.nspname=current_schema()",
		trigger,
	)
}

// init appends migrations in dependency order: baseline, tables, then dependent
// columns and indexes. Do not reset Catalog: migrations.go initializes dated
// entries before init, and append-only preserves both declaration styles.
func init() {
	Catalog = append(Catalog, baselineMigration())
	Catalog = append(Catalog, inlineTableMigrations()...)
	Catalog = append(Catalog, columnAddMigrations()...)
	Catalog = append(Catalog, indexMigrations()...)
	Catalog = append(Catalog, triggerMigrations()...)
	Catalog = append(Catalog, postSliceColumnAddMigrations()...)
	Catalog = append(Catalog, samlMigrations()...)
	Catalog = append(Catalog, milestoneScmDropMigrations()...)
	Catalog = append(Catalog, seedMigrations()...)
	Catalog = append(Catalog, miscMigrations()...)
	Catalog = append(Catalog, schemaRerunMigrations()...)
	Catalog = append(Catalog, cutoverMigrations()...)
	Catalog = append(Catalog, driftFixMigrations()...)
}

// baselineMigration stamps a fresh install without DDL. Bootstrap is not yet
// atomic, so operators must drop a database left by a failed first install.
func baselineMigration() Migration {
	return Migration{
		Version: "0000_baseline",
		Name:    "fresh-install baseline marker",
	}
}

// columnAddMigrations preserves legacy column-add ordering.
func columnAddMigrations() []Migration {
	return []Migration{
		{
			Version:       "0001_workspaces_display_mode",
			Name:          "workspaces.display_mode",
			CheckSQLite:   sqliteColumnCheck("workspaces", "display_mode"),
			CheckPostgres: pgColumnCheck("workspaces", "display_mode"),
			SQLite:        "ALTER TABLE workspaces ADD COLUMN display_mode TEXT DEFAULT 'default'",
			Postgres:      "ALTER TABLE workspaces ADD COLUMN display_mode TEXT DEFAULT 'default'",
		},
		{
			Version:       "0002_active_timers_user_id",
			Name:          "active_timers.user_id",
			CheckSQLite:   sqliteColumnCheck("active_timers", "user_id"),
			CheckPostgres: pgColumnCheck("active_timers", "user_id"),
			SQLite:        "ALTER TABLE active_timers ADD COLUMN user_id INTEGER REFERENCES users(id) ON DELETE CASCADE",
			Postgres:      "ALTER TABLE active_timers ADD COLUMN user_id INTEGER REFERENCES users(id) ON DELETE CASCADE",
		},
		// Postgres-only: convert TEXT custom_field_values to JSONB so the
		// repository's @> and -> operators can use GIN indexes.
		{
			Version:       "0003_items_custom_field_values_jsonb",
			Name:          "items.custom_field_values → JSONB (Postgres)",
			CheckPostgres: pgCustomFieldValuesJSONBCheck("items"),
			Postgres:      "ALTER TABLE items ALTER COLUMN custom_field_values TYPE JSONB USING custom_field_values::jsonb",
		},
		{
			Version:       "0004_assets_custom_field_values_jsonb",
			Name:          "assets.custom_field_values → JSONB (Postgres)",
			CheckPostgres: pgCustomFieldValuesJSONBCheck("assets"),
			Postgres:      "ALTER TABLE assets ALTER COLUMN custom_field_values TYPE JSONB USING custom_field_values::jsonb",
		},
		{
			Version:       "0005_portal_customers_custom_field_values_jsonb",
			Name:          "portal_customers.custom_field_values → JSONB (Postgres)",
			CheckPostgres: pgCustomFieldValuesJSONBCheck("portal_customers"),
			Postgres:      "ALTER TABLE portal_customers ALTER COLUMN custom_field_values TYPE JSONB USING custom_field_values::jsonb",
		},
		{
			Version:       "0006_customer_organisations_custom_field_values_jsonb",
			Name:          "customer_organisations.custom_field_values → JSONB (Postgres)",
			CheckPostgres: pgCustomFieldValuesJSONBCheck("customer_organisations"),
			Postgres:      "ALTER TABLE customer_organisations ALTER COLUMN custom_field_values TYPE JSONB USING custom_field_values::jsonb", //nolint:misspell // actual table name
		},
		{
			Version:       "0007_items_start_date",
			Name:          "items.start_date",
			CheckSQLite:   sqliteColumnCheck("items", "start_date"),
			CheckPostgres: pgColumnCheck("items", "start_date"),
			SQLite:        "ALTER TABLE items ADD COLUMN start_date DATE",
			Postgres:      "ALTER TABLE items ADD COLUMN start_date DATE",
		},
		{
			Version:       "0008_items_end_date",
			Name:          "items.end_date",
			CheckSQLite:   sqliteColumnCheck("items", "end_date"),
			CheckPostgres: pgColumnCheck("items", "end_date"),
			SQLite:        "ALTER TABLE items ADD COLUMN end_date DATE",
			Postgres:      "ALTER TABLE items ADD COLUMN end_date DATE",
		},
		{
			Version:       "0009_board_configurations_roadmap_config",
			Name:          "board_configurations.roadmap_config",
			CheckSQLite:   sqliteColumnCheck("board_configurations", "roadmap_config"),
			CheckPostgres: pgColumnCheck("board_configurations", "roadmap_config"),
			SQLite:        "ALTER TABLE board_configurations ADD COLUMN roadmap_config TEXT",
			Postgres:      "ALTER TABLE board_configurations ADD COLUMN roadmap_config TEXT",
		},
		{
			Version:       "0010_board_configurations_card_fields",
			Name:          "board_configurations.card_fields",
			CheckSQLite:   sqliteColumnCheck("board_configurations", "card_fields"),
			CheckPostgres: pgColumnCheck("board_configurations", "card_fields"),
			SQLite:        "ALTER TABLE board_configurations ADD COLUMN card_fields TEXT",
			Postgres:      "ALTER TABLE board_configurations ADD COLUMN card_fields TEXT",
		},
		{
			Version:       "0011_workspaces_internal_comments_enabled",
			Name:          "workspaces.internal_comments_enabled",
			CheckSQLite:   sqliteColumnCheck("workspaces", "internal_comments_enabled"),
			CheckPostgres: pgColumnCheck("workspaces", "internal_comments_enabled"),
			SQLite:        "ALTER TABLE workspaces ADD COLUMN internal_comments_enabled BOOLEAN DEFAULT FALSE",
			Postgres:      "ALTER TABLE workspaces ADD COLUMN internal_comments_enabled BOOLEAN DEFAULT false",
		},
		{
			Version:       "0012_request_types_workspace_id",
			Name:          "request_types.workspace_id",
			CheckSQLite:   sqliteColumnCheck("request_types", "workspace_id"),
			CheckPostgres: pgColumnCheck("request_types", "workspace_id"),
			SQLite:        "ALTER TABLE request_types ADD COLUMN workspace_id INTEGER DEFAULT NULL REFERENCES workspaces(id) ON DELETE SET NULL",
			Postgres:      "ALTER TABLE request_types ADD COLUMN workspace_id INTEGER DEFAULT NULL REFERENCES workspaces(id) ON DELETE SET NULL",
		},
		{
			Version:       "0013_assets_import_job_id",
			Name:          "assets.import_job_id",
			CheckSQLite:   sqliteColumnCheck("assets", "import_job_id"),
			CheckPostgres: pgColumnCheck("assets", "import_job_id"),
			SQLite:        "ALTER TABLE assets ADD COLUMN import_job_id TEXT",
			Postgres:      "ALTER TABLE assets ADD COLUMN import_job_id TEXT",
		},
		{
			Version:       "0014_users_is_agent",
			Name:          "users.is_agent",
			CheckSQLite:   sqliteColumnCheck("users", "is_agent"),
			CheckPostgres: pgColumnCheck("users", "is_agent"),
			SQLite:        "ALTER TABLE users ADD COLUMN is_agent BOOLEAN DEFAULT 0",
			Postgres:      "ALTER TABLE users ADD COLUMN is_agent BOOLEAN DEFAULT false",
		},
		{
			Version:       "0015_users_agent_owner_user_id",
			Name:          "users.agent_owner_user_id",
			CheckSQLite:   sqliteColumnCheck("users", "agent_owner_user_id"),
			CheckPostgres: pgColumnCheck("users", "agent_owner_user_id"),
			SQLite:        "ALTER TABLE users ADD COLUMN agent_owner_user_id INTEGER REFERENCES users(id) ON DELETE CASCADE",
			Postgres:      "ALTER TABLE users ADD COLUMN agent_owner_user_id INTEGER REFERENCES users(id) ON DELETE CASCADE",
		},
		{
			Version:       "0016_email_channel_state_uid_validity",
			Name:          "email_channel_state.uid_validity",
			CheckSQLite:   sqliteColumnCheck("email_channel_state", "uid_validity"),
			CheckPostgres: pgColumnCheck("email_channel_state", "uid_validity"),
			SQLite:        "ALTER TABLE email_channel_state ADD COLUMN uid_validity INTEGER DEFAULT 0",
			Postgres:      "ALTER TABLE email_channel_state ADD COLUMN uid_validity BIGINT DEFAULT 0",
		},
		{
			Version:       "0017_actions_actor_user_id",
			Name:          "actions.actor_user_id",
			CheckSQLite:   sqliteColumnCheck("actions", "actor_user_id"),
			CheckPostgres: pgColumnCheck("actions", "actor_user_id"),
			SQLite:        "ALTER TABLE actions ADD COLUMN actor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL",
			Postgres:      "ALTER TABLE actions ADD COLUMN actor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL",
		},
		{
			Version:       "0018_action_execution_logs_trigger_user_id",
			Name:          "action_execution_logs.trigger_user_id",
			CheckSQLite:   sqliteColumnCheck("action_execution_logs", "trigger_user_id"),
			CheckPostgres: pgColumnCheck("action_execution_logs", "trigger_user_id"),
			SQLite:        "ALTER TABLE action_execution_logs ADD COLUMN trigger_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL",
			Postgres:      "ALTER TABLE action_execution_logs ADD COLUMN trigger_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL",
		},
		{
			Version:       "0019_action_execution_logs_effective_actor_user_id",
			Name:          "action_execution_logs.effective_actor_user_id",
			CheckSQLite:   sqliteColumnCheck("action_execution_logs", "effective_actor_user_id"),
			CheckPostgres: pgColumnCheck("action_execution_logs", "effective_actor_user_id"),
			SQLite:        "ALTER TABLE action_execution_logs ADD COLUMN effective_actor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL",
			Postgres:      "ALTER TABLE action_execution_logs ADD COLUMN effective_actor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL",
		},
		{
			Version:       "0020_item_scm_links_smart_commits_applied_at",
			Name:          "item_scm_links.smart_commits_applied_at",
			CheckSQLite:   sqliteColumnCheck("item_scm_links", "smart_commits_applied_at"),
			CheckPostgres: pgColumnCheck("item_scm_links", "smart_commits_applied_at"),
			SQLite:        "ALTER TABLE item_scm_links ADD COLUMN smart_commits_applied_at DATETIME",
			Postgres:      "ALTER TABLE item_scm_links ADD COLUMN smart_commits_applied_at TIMESTAMPTZ",
		},
		{
			Version:       "0021_workspace_scm_connections_smart_commits_enabled",
			Name:          "workspace_scm_connections.smart_commits_enabled",
			CheckSQLite:   sqliteColumnCheck("workspace_scm_connections", "smart_commits_enabled"),
			CheckPostgres: pgColumnCheck("workspace_scm_connections", "smart_commits_enabled"),
			SQLite:        "ALTER TABLE workspace_scm_connections ADD COLUMN smart_commits_enabled BOOLEAN DEFAULT 0",
			Postgres:      "ALTER TABLE workspace_scm_connections ADD COLUMN smart_commits_enabled BOOLEAN DEFAULT false",
		},
		// SQLite-only: user_sessions.enrollment_required has no Postgres
		// counterpart in pgMigrations. Likely covered by Postgres user_sessions
		// table DDL in users_postgres.sql; we stamp on Postgres without DDL.
		{
			Version:     "0022_user_sessions_enrollment_required_sqlite",
			Name:        "user_sessions.enrollment_required (SQLite)",
			CheckSQLite: sqliteColumnCheck("user_sessions", "enrollment_required"),
			SQLite:      "ALTER TABLE user_sessions ADD COLUMN enrollment_required BOOLEAN DEFAULT 0",
		},
		{
			Version:     "0023_time_projects_active_sqlite",
			Name:        "time_projects.active (SQLite)",
			CheckSQLite: sqliteColumnCheck("time_projects", "active"),
			SQLite:      "ALTER TABLE time_projects ADD COLUMN active BOOLEAN DEFAULT 1",
		},
		// Postgres-only: notification_templates.subject doesn't appear in the
		// SQLite legacy migrations; SQLite's notifications schema presumably
		// has it natively.
		{
			Version:       "0024_notification_templates_subject_postgres",
			Name:          "notification_templates.subject (Postgres)",
			CheckPostgres: pgColumnCheck("notification_templates", "subject"),
			Postgres:      "ALTER TABLE notification_templates ADD COLUMN subject TEXT",
		},
		{
			Version:       "0025_notification_templates_text_body",
			Name:          "notification_templates.text_body",
			CheckSQLite:   sqliteColumnCheck("notification_templates", "text_body"),
			CheckPostgres: pgColumnCheck("notification_templates", "text_body"),
			SQLite:        "ALTER TABLE notification_templates ADD COLUMN text_body TEXT",
			Postgres:      "ALTER TABLE notification_templates ADD COLUMN text_body TEXT",
		},
		{
			Version:     "0026_notification_templates_is_system_sqlite",
			Name:        "notification_templates.is_system (SQLite)",
			CheckSQLite: sqliteColumnCheck("notification_templates", "is_system"),
			SQLite:      "ALTER TABLE notification_templates ADD COLUMN is_system BOOLEAN DEFAULT 0",
		},
		{
			Version:       "0027_teams_icon",
			Name:          "teams.icon",
			CheckSQLite:   sqliteColumnCheck("teams", "icon"),
			CheckPostgres: pgColumnCheck("teams", "icon"),
			SQLite:        "ALTER TABLE teams ADD COLUMN icon TEXT",
			Postgres:      "ALTER TABLE teams ADD COLUMN icon TEXT",
		},
		{
			Version:       "0028_teams_color",
			Name:          "teams.color",
			CheckSQLite:   sqliteColumnCheck("teams", "color"),
			CheckPostgres: pgColumnCheck("teams", "color"),
			SQLite:        "ALTER TABLE teams ADD COLUMN color TEXT",
			Postgres:      "ALTER TABLE teams ADD COLUMN color TEXT",
		},
		{
			Version:       "0029_teams_avatar_url",
			Name:          "teams.avatar_url",
			CheckSQLite:   sqliteColumnCheck("teams", "avatar_url"),
			CheckPostgres: pgColumnCheck("teams", "avatar_url"),
			SQLite:        "ALTER TABLE teams ADD COLUMN avatar_url TEXT",
			Postgres:      "ALTER TABLE teams ADD COLUMN avatar_url TEXT",
		},
		{
			Version:       "0030_approval_set_statuses_is_active",
			Name:          "approval_set_statuses.is_active",
			CheckSQLite:   sqliteColumnCheck("approval_set_statuses", "is_active"),
			CheckPostgres: pgColumnCheck("approval_set_statuses", "is_active"),
			SQLite:        "ALTER TABLE approval_set_statuses ADD COLUMN is_active INTEGER NOT NULL DEFAULT 1",
			Postgres:      "ALTER TABLE approval_set_statuses ADD COLUMN is_active BOOLEAN NOT NULL DEFAULT TRUE",
		},
		{
			Version:     "0031_actions_template_key_sqlite",
			Name:        "actions.template_key (SQLite)",
			CheckSQLite: sqliteColumnCheck("actions", "template_key"),
			SQLite:      "ALTER TABLE actions ADD COLUMN template_key TEXT",
		},
		{
			Version:       "0032_action_capabilities_applies_to_all_workspaces",
			Name:          "action_capabilities.applies_to_all_workspaces",
			CheckSQLite:   sqliteColumnCheck("action_capabilities", "applies_to_all_workspaces"),
			CheckPostgres: pgColumnCheck("action_capabilities", "applies_to_all_workspaces"),
			SQLite:        "ALTER TABLE action_capabilities ADD COLUMN applies_to_all_workspaces BOOLEAN DEFAULT 1",
			Postgres:      "ALTER TABLE action_capabilities ADD COLUMN applies_to_all_workspaces BOOLEAN DEFAULT TRUE",
		},
		{
			Version:       "0033_notifications_last_send_failed",
			Name:          "notifications.last_send_failed",
			CheckSQLite:   sqliteColumnCheck("notifications", "last_send_failed"),
			CheckPostgres: pgColumnCheck("notifications", "last_send_failed"),
			SQLite:        "ALTER TABLE notifications ADD COLUMN last_send_failed BOOLEAN DEFAULT 0",
			Postgres:      "ALTER TABLE notifications ADD COLUMN last_send_failed BOOLEAN DEFAULT FALSE",
		},
		{
			Version:       "0034_users_agent_provenance",
			Name:          "users.agent_provenance",
			CheckSQLite:   sqliteColumnCheck("users", "agent_provenance"),
			CheckPostgres: pgColumnCheck("users", "agent_provenance"),
			SQLite:        "ALTER TABLE users ADD COLUMN agent_provenance TEXT NOT NULL DEFAULT 'user'",
			Postgres:      "ALTER TABLE users ADD COLUMN agent_provenance TEXT NOT NULL DEFAULT 'user'",
		},
		{
			Version:       "0035_users_oauth_client_id",
			Name:          "users.oauth_client_id",
			CheckSQLite:   sqliteColumnCheck("users", "oauth_client_id"),
			CheckPostgres: pgColumnCheck("users", "oauth_client_id"),
			SQLite:        "ALTER TABLE users ADD COLUMN oauth_client_id INTEGER REFERENCES oauth_clients(id) ON DELETE CASCADE",
			Postgres:      "ALTER TABLE users ADD COLUMN oauth_client_id INTEGER REFERENCES oauth_clients(id) ON DELETE CASCADE",
		},
		// Postgres-only CHECK constraints. SQLite enforces the same invariants
		// via triggers ported in PR 2c.
		{
			Version:       "0036_users_oauth_provenance_requires_client_check",
			Name:          "users CHECK users_oauth_provenance_requires_client (Postgres)",
			CheckPostgres: pgConstraintCheck("users", "users_oauth_provenance_requires_client"),
			Postgres:      "ALTER TABLE users ADD CONSTRAINT users_oauth_provenance_requires_client CHECK (agent_provenance != 'oauth' OR oauth_client_id IS NOT NULL)",
		},
		{
			Version:       "0037_users_oauth_client_requires_oauth_agent_check",
			Name:          "users CHECK users_oauth_client_requires_oauth_agent (Postgres)",
			CheckPostgres: pgConstraintCheck("users", "users_oauth_client_requires_oauth_agent"),
			Postgres:      "ALTER TABLE users ADD CONSTRAINT users_oauth_client_requires_oauth_agent CHECK (oauth_client_id IS NULL OR (is_agent = true AND agent_provenance = 'oauth'))",
		},
		{
			Version:       "0038_attachments_entity_type",
			Name:          "attachments.entity_type",
			CheckSQLite:   sqliteColumnCheck("attachments", "entity_type"),
			CheckPostgres: pgColumnCheck("attachments", "entity_type"),
			SQLite:        "ALTER TABLE attachments ADD COLUMN entity_type TEXT DEFAULT 'item'",
			Postgres:      "ALTER TABLE attachments ADD COLUMN entity_type TEXT DEFAULT 'item'",
		},
		{
			Version:       "0039_attachments_category",
			Name:          "attachments.category",
			CheckSQLite:   sqliteColumnCheck("attachments", "category"),
			CheckPostgres: pgColumnCheck("attachments", "category"),
			SQLite:        "ALTER TABLE attachments ADD COLUMN category TEXT DEFAULT ''",
			Postgres:      "ALTER TABLE attachments ADD COLUMN category TEXT DEFAULT ''",
		},
		{
			Version:     "0040_portal_customers_dismissed_passkey_prompt_at_sqlite",
			Name:        "portal_customers.dismissed_passkey_prompt_at (SQLite)",
			CheckSQLite: sqliteColumnCheck("portal_customers", "dismissed_passkey_prompt_at"),
			SQLite:      "ALTER TABLE portal_customers ADD COLUMN dismissed_passkey_prompt_at DATETIME",
		},
		{
			Version:       "0041_request_types_title_template",
			Name:          "request_types.title_template",
			CheckSQLite:   sqliteColumnCheck("request_types", "title_template"),
			CheckPostgres: pgColumnCheck("request_types", "title_template"),
			SQLite:        "ALTER TABLE request_types ADD COLUMN title_template TEXT NOT NULL DEFAULT ''",
			Postgres:      "ALTER TABLE request_types ADD COLUMN title_template TEXT NOT NULL DEFAULT ''",
		},
		{
			Version:       "0042_portal_customer_sessions_channel_id",
			Name:          "portal_customer_sessions.channel_id",
			CheckSQLite:   sqliteColumnCheck("portal_customer_sessions", "channel_id"),
			CheckPostgres: pgColumnCheck("portal_customer_sessions", "channel_id"),
			SQLite:        "ALTER TABLE portal_customer_sessions ADD COLUMN channel_id INTEGER REFERENCES channels(id) ON DELETE SET NULL",
			Postgres:      "ALTER TABLE portal_customer_sessions ADD COLUMN channel_id INTEGER REFERENCES channels(id) ON DELETE SET NULL",
		},
		{
			Version:       "0043_audit_logs_user_agent",
			Name:          "audit_logs.user_agent",
			CheckSQLite:   sqliteColumnCheck("audit_logs", "user_agent"),
			CheckPostgres: pgColumnCheck("audit_logs", "user_agent"),
			SQLite:        "ALTER TABLE audit_logs ADD COLUMN user_agent TEXT",
			Postgres:      "ALTER TABLE audit_logs ADD COLUMN user_agent TEXT",
		},
		{
			Version:       "0044_audit_logs_error_message",
			Name:          "audit_logs.error_message",
			CheckSQLite:   sqliteColumnCheck("audit_logs", "error_message"),
			CheckPostgres: pgColumnCheck("audit_logs", "error_message"),
			SQLite:        "ALTER TABLE audit_logs ADD COLUMN error_message TEXT",
			Postgres:      "ALTER TABLE audit_logs ADD COLUMN error_message TEXT",
		},
	}
}

// inlineTableMigrations are the standalone CREATE TABLE IF NOT EXISTS
// blocks that live directly in the legacy Initialize() bodies (separate
// from the migrations / pgMigrations slices). Each block uses fully
// idempotent DDL so re-running is a no-op on installs that already have
// the tables.
func inlineTableMigrations() []Migration {
	return []Migration{
		{
			Version:       "inline_item_key_reservations",
			Name:          "item key reservations",
			CheckSQLite:   sqliteTableCheck("item_key_reservations"),
			CheckPostgres: pgTableCheck("item_key_reservations"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS item_key_reservations (
					workspace_id INTEGER NOT NULL,
					workspace_item_number INTEGER NOT NULL,
					moved_item_id INTEGER,
					destination_workspace_id INTEGER,
					destination_workspace_item_number INTEGER,
					moved_by INTEGER,
					created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
					PRIMARY KEY (workspace_id, workspace_item_number),
					FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
					FOREIGN KEY (destination_workspace_id) REFERENCES workspaces(id) ON DELETE SET NULL,
					FOREIGN KEY (moved_by) REFERENCES users(id) ON DELETE SET NULL
				);
				CREATE INDEX IF NOT EXISTS idx_item_key_reservations_moved_item ON item_key_reservations(moved_item_id);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS item_key_reservations (
					workspace_id INTEGER NOT NULL,
					workspace_item_number INTEGER NOT NULL,
					moved_item_id INTEGER,
					destination_workspace_id INTEGER,
					destination_workspace_item_number INTEGER,
					moved_by INTEGER,
					created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					PRIMARY KEY (workspace_id, workspace_item_number),
					FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
					FOREIGN KEY (destination_workspace_id) REFERENCES workspaces(id) ON DELETE SET NULL,
					FOREIGN KEY (moved_by) REFERENCES users(id) ON DELETE SET NULL
				);
				CREATE INDEX IF NOT EXISTS idx_item_key_reservations_moved_item ON item_key_reservations(moved_item_id);
			`,
		},
		{
			Version: "inline_action_capabilities",
			Name:    "action_capabilities",
			// Superseded: this body was edited on 2026-05-17 (TIMESTAMP to TIMESTAMPTZ)
			// after it had already shipped and been applied, so databases stamped
			// before that edit carry the checksum below. The edit never reached those
			// databases (CREATE TABLE IF NOT EXISTS no-ops on an existing table);
			// 20260804_pg_timestamptz_convergence is what actually converges them.
			Superseded:    []string{"e7eb501e6df75a7545a4038b76d7724b6dcbb35f542813c7306571736aa12023"},
			CheckSQLite:   sqliteTableCheck("action_capabilities"),
			CheckPostgres: pgTableCheck("action_capabilities"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS action_capabilities (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					name TEXT NOT NULL,
					capability_type TEXT NOT NULL,
					config TEXT NOT NULL,
					is_enabled BOOLEAN DEFAULT true,
					applies_to_all_workspaces BOOLEAN DEFAULT true,
					created_by INTEGER,
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL
				);
				CREATE INDEX IF NOT EXISTS idx_action_capabilities_type ON action_capabilities(capability_type);
				CREATE INDEX IF NOT EXISTS idx_action_capabilities_enabled ON action_capabilities(is_enabled);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS action_capabilities (
					id SERIAL PRIMARY KEY,
					name TEXT NOT NULL,
					capability_type TEXT NOT NULL,
					config TEXT NOT NULL,
					is_enabled BOOLEAN DEFAULT true,
					applies_to_all_workspaces BOOLEAN DEFAULT true,
					created_by INTEGER,
					created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
					updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
					FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL
				);
				CREATE INDEX IF NOT EXISTS idx_action_capabilities_type ON action_capabilities(capability_type);
				CREATE INDEX IF NOT EXISTS idx_action_capabilities_enabled ON action_capabilities(is_enabled);
			`,
		},
		{
			Version:       "inline_action_capability_workspaces",
			Name:          "action_capability_workspaces",
			CheckSQLite:   sqliteTableCheck("action_capability_workspaces"),
			CheckPostgres: pgTableCheck("action_capability_workspaces"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS action_capability_workspaces (
					capability_id INTEGER NOT NULL,
					workspace_id INTEGER NOT NULL,
					PRIMARY KEY (capability_id, workspace_id),
					FOREIGN KEY (capability_id) REFERENCES action_capabilities(id) ON DELETE CASCADE,
					FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
				);
				CREATE INDEX IF NOT EXISTS idx_action_capability_workspaces_workspace ON action_capability_workspaces(workspace_id);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS action_capability_workspaces (
					capability_id INTEGER NOT NULL,
					workspace_id INTEGER NOT NULL,
					PRIMARY KEY (capability_id, workspace_id),
					FOREIGN KEY (capability_id) REFERENCES action_capabilities(id) ON DELETE CASCADE,
					FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
				);
				CREATE INDEX IF NOT EXISTS idx_action_capability_workspaces_workspace ON action_capability_workspaces(workspace_id);
			`,
		},
		{
			Version: "inline_oauth_server_tables",
			Name:    "oauth_clients, oauth_authorization_codes, oauth_refresh_tokens",
			// Superseded: this body was edited on 2026-05-17 (TIMESTAMP to TIMESTAMPTZ)
			// after it had already shipped and been applied, so databases stamped
			// before that edit carry the checksum below. The edit never reached those
			// databases (CREATE TABLE IF NOT EXISTS no-ops on an existing table);
			// 20260804_pg_timestamptz_convergence is what actually converges them.
			Superseded:    []string{"7cc7702f41804b6734d32106144f3ed3ea3d3d96615556b334589fb815836d5c"},
			CheckSQLite:   sqliteTableCheck("oauth_clients"),
			CheckPostgres: pgTableCheck("oauth_clients"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS oauth_clients (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					slug TEXT NOT NULL UNIQUE,
					display_name TEXT NOT NULL,
					client_id TEXT NOT NULL UNIQUE,
					client_type TEXT NOT NULL,
					client_secret_hash TEXT,
					redirect_uris TEXT NOT NULL DEFAULT '[]',
					allowed_scopes TEXT NOT NULL DEFAULT '[]',
					enabled BOOLEAN NOT NULL DEFAULT 1,
					created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
				);
				CREATE INDEX IF NOT EXISTS idx_oauth_clients_client_id ON oauth_clients(client_id);
				CREATE INDEX IF NOT EXISTS idx_oauth_clients_enabled ON oauth_clients(enabled);

				CREATE TABLE IF NOT EXISTS oauth_authorization_codes (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					code TEXT NOT NULL UNIQUE,
					client_id TEXT NOT NULL,
					user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
					agent_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
					redirect_uri TEXT NOT NULL,
					scopes TEXT NOT NULL DEFAULT '[]',
					code_challenge TEXT,
					code_challenge_method TEXT,
					state TEXT,
					expires_at DATETIME NOT NULL,
					consumed_at DATETIME,
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP
				);
				CREATE INDEX IF NOT EXISTS idx_oauth_authorization_codes_code ON oauth_authorization_codes(code);
				CREATE INDEX IF NOT EXISTS idx_oauth_authorization_codes_expires_at ON oauth_authorization_codes(expires_at);

				CREATE TABLE IF NOT EXISTS oauth_refresh_tokens (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					token_hash TEXT NOT NULL UNIQUE,
					api_token_id INTEGER NOT NULL REFERENCES api_tokens(id) ON DELETE CASCADE,
					client_id TEXT NOT NULL,
					user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
					agent_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
					scopes TEXT NOT NULL DEFAULT '[]',
					expires_at DATETIME NOT NULL,
					revoked_at DATETIME,
					rotated_to_id INTEGER REFERENCES oauth_refresh_tokens(id) ON DELETE SET NULL,
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP
				);
				CREATE INDEX IF NOT EXISTS idx_oauth_refresh_tokens_token_hash ON oauth_refresh_tokens(token_hash);
				CREATE INDEX IF NOT EXISTS idx_oauth_refresh_tokens_api_token_id ON oauth_refresh_tokens(api_token_id);
				CREATE INDEX IF NOT EXISTS idx_oauth_refresh_tokens_expires_at ON oauth_refresh_tokens(expires_at);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS oauth_clients (
					id SERIAL PRIMARY KEY,
					slug TEXT NOT NULL UNIQUE,
					display_name TEXT NOT NULL,
					client_id TEXT NOT NULL UNIQUE,
					client_type TEXT NOT NULL,
					client_secret_hash TEXT,
					redirect_uris TEXT NOT NULL DEFAULT '[]',
					allowed_scopes TEXT NOT NULL DEFAULT '[]',
					enabled BOOLEAN NOT NULL DEFAULT true,
					created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
					created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
					updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
				);
				CREATE INDEX IF NOT EXISTS idx_oauth_clients_client_id ON oauth_clients(client_id);
				CREATE INDEX IF NOT EXISTS idx_oauth_clients_enabled ON oauth_clients(enabled);

				CREATE TABLE IF NOT EXISTS oauth_authorization_codes (
					id SERIAL PRIMARY KEY,
					code TEXT NOT NULL UNIQUE,
					client_id TEXT NOT NULL,
					user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
					agent_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
					redirect_uri TEXT NOT NULL,
					scopes TEXT NOT NULL DEFAULT '[]',
					code_challenge TEXT,
					code_challenge_method TEXT,
					state TEXT,
					expires_at TIMESTAMPTZ NOT NULL,
					consumed_at TIMESTAMPTZ,
					created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
				);
				CREATE INDEX IF NOT EXISTS idx_oauth_authorization_codes_code ON oauth_authorization_codes(code);
				CREATE INDEX IF NOT EXISTS idx_oauth_authorization_codes_expires_at ON oauth_authorization_codes(expires_at);

				CREATE TABLE IF NOT EXISTS oauth_refresh_tokens (
					id SERIAL PRIMARY KEY,
					token_hash TEXT NOT NULL UNIQUE,
					api_token_id INTEGER NOT NULL REFERENCES api_tokens(id) ON DELETE CASCADE,
					client_id TEXT NOT NULL,
					user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
					agent_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
					scopes TEXT NOT NULL DEFAULT '[]',
					expires_at TIMESTAMPTZ NOT NULL,
					revoked_at TIMESTAMPTZ,
					rotated_to_id INTEGER REFERENCES oauth_refresh_tokens(id) ON DELETE SET NULL,
					created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
				);
				CREATE INDEX IF NOT EXISTS idx_oauth_refresh_tokens_token_hash ON oauth_refresh_tokens(token_hash);
				CREATE INDEX IF NOT EXISTS idx_oauth_refresh_tokens_api_token_id ON oauth_refresh_tokens(api_token_id);
				CREATE INDEX IF NOT EXISTS idx_oauth_refresh_tokens_expires_at ON oauth_refresh_tokens(expires_at);
			`,
		},
		{
			Version: "inline_audit_logs",
			Name:    "audit_logs",
			// Superseded: this body was edited on 2026-05-17 (TIMESTAMP to TIMESTAMPTZ)
			// after it had already shipped and been applied, so databases stamped
			// before that edit carry the checksum below. The edit never reached those
			// databases (CREATE TABLE IF NOT EXISTS no-ops on an existing table);
			// 20260804_pg_timestamptz_convergence is what actually converges them.
			Superseded:    []string{"b4f37a5439649cdc991b1531428c629f58ef40ef6a8339b12b9f2168531aad7e"},
			CheckSQLite:   sqliteTableCheck("audit_logs"),
			CheckPostgres: pgTableCheck("audit_logs"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS audit_logs (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
					user_id INTEGER,
					username TEXT NOT NULL,
					ip_address TEXT,
					user_agent TEXT,
					action_type TEXT NOT NULL,
					resource_type TEXT NOT NULL,
					resource_id INTEGER,
					resource_name TEXT,
					details TEXT,
					success BOOLEAN NOT NULL DEFAULT 1,
					error_message TEXT,
					FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL
				);
				CREATE INDEX IF NOT EXISTS idx_audit_logs_timestamp ON audit_logs(timestamp);
				CREATE INDEX IF NOT EXISTS idx_audit_logs_user_id ON audit_logs(user_id);
				CREATE INDEX IF NOT EXISTS idx_audit_logs_action_type ON audit_logs(action_type);
				CREATE INDEX IF NOT EXISTS idx_audit_logs_resource_type ON audit_logs(resource_type);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS audit_logs (
					id SERIAL PRIMARY KEY,
					timestamp TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					user_id INTEGER,
					username TEXT NOT NULL,
					ip_address TEXT,
					user_agent TEXT,
					action_type TEXT NOT NULL,
					resource_type TEXT NOT NULL,
					resource_id INTEGER,
					resource_name TEXT,
					details TEXT,
					success BOOLEAN NOT NULL DEFAULT TRUE,
					error_message TEXT,
					FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL
				);
				CREATE INDEX IF NOT EXISTS idx_audit_logs_timestamp ON audit_logs(timestamp);
				CREATE INDEX IF NOT EXISTS idx_audit_logs_user_id ON audit_logs(user_id);
				CREATE INDEX IF NOT EXISTS idx_audit_logs_action_type ON audit_logs(action_type);
				CREATE INDEX IF NOT EXISTS idx_audit_logs_resource_type ON audit_logs(resource_type);
			`,
		},
		{
			Version: "inline_scheduler_runs",
			Name:    "scheduler_runs",
			// Superseded: this body was edited on 2026-05-17 (TIMESTAMP to TIMESTAMPTZ)
			// after it had already shipped and been applied, so databases stamped
			// before that edit carry the checksum below. The edit never reached those
			// databases (CREATE TABLE IF NOT EXISTS no-ops on an existing table);
			// 20260804_pg_timestamptz_convergence is what actually converges them.
			Superseded:    []string{"bc0cc96b1cf0cbf24f49e06b98147dbb2e830185901275c5aff0b9a35ed4a768"},
			CheckSQLite:   sqliteTableCheck("scheduler_runs"),
			CheckPostgres: pgTableCheck("scheduler_runs"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS scheduler_runs (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					scheduler_name TEXT NOT NULL,
					started_at DATETIME NOT NULL,
					completed_at DATETIME,
					duration_ms INTEGER,
					items_processed INTEGER,
					success BOOLEAN NOT NULL DEFAULT 0,
					error_message TEXT
				);
				CREATE INDEX IF NOT EXISTS idx_scheduler_runs_name_started ON scheduler_runs(scheduler_name, started_at DESC);
				CREATE INDEX IF NOT EXISTS idx_scheduler_runs_started_at ON scheduler_runs(started_at);
				CREATE INDEX IF NOT EXISTS idx_scheduler_runs_success ON scheduler_runs(success);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS scheduler_runs (
					id SERIAL PRIMARY KEY,
					scheduler_name TEXT NOT NULL,
					started_at TIMESTAMPTZ NOT NULL,
					completed_at TIMESTAMPTZ,
					duration_ms INTEGER,
					items_processed INTEGER,
					success BOOLEAN NOT NULL DEFAULT FALSE,
					error_message TEXT
				);
				CREATE INDEX IF NOT EXISTS idx_scheduler_runs_name_started ON scheduler_runs(scheduler_name, started_at DESC);
				CREATE INDEX IF NOT EXISTS idx_scheduler_runs_started_at ON scheduler_runs(started_at);
				CREATE INDEX IF NOT EXISTS idx_scheduler_runs_success ON scheduler_runs(success);
			`,
		},
		{
			Version: "inline_pending_custom_field_cleanups",
			Name:    "pending_custom_field_cleanups",
			// Superseded: this body was edited on 2026-05-17 (TIMESTAMP to TIMESTAMPTZ)
			// after it had already shipped and been applied, so databases stamped
			// before that edit carry the checksum below. The edit never reached those
			// databases (CREATE TABLE IF NOT EXISTS no-ops on an existing table);
			// 20260804_pg_timestamptz_convergence is what actually converges them.
			Superseded:    []string{"6fd4c1beadd4afec1882bb99d9255a427643bbe289e26d57f9719f6660e1ff87"},
			CheckSQLite:   sqliteTableCheck("pending_custom_field_cleanups"),
			CheckPostgres: pgTableCheck("pending_custom_field_cleanups"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS pending_custom_field_cleanups (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					field_id INTEGER NOT NULL,
					status TEXT NOT NULL DEFAULT 'pending',
					created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
					started_at DATETIME,
					completed_at DATETIME,
					items_processed INTEGER NOT NULL DEFAULT 0,
					error_message TEXT
				);
				CREATE INDEX IF NOT EXISTS idx_pending_cfv_cleanups_status ON pending_custom_field_cleanups(status, created_at);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS pending_custom_field_cleanups (
					id SERIAL PRIMARY KEY,
					field_id INTEGER NOT NULL,
					status TEXT NOT NULL DEFAULT 'pending',
					created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					started_at TIMESTAMPTZ,
					completed_at TIMESTAMPTZ,
					items_processed INTEGER NOT NULL DEFAULT 0,
					error_message TEXT
				);
				CREATE INDEX IF NOT EXISTS idx_pending_cfv_cleanups_status ON pending_custom_field_cleanups(status, created_at);
			`,
		},
		{
			Version: "inline_user_invitations",
			Name:    "user_invitations",
			// Superseded: this body was edited on 2026-05-17 (TIMESTAMP to TIMESTAMPTZ)
			// after it had already shipped and been applied, so databases stamped
			// before that edit carry the checksum below. The edit never reached those
			// databases (CREATE TABLE IF NOT EXISTS no-ops on an existing table);
			// 20260804_pg_timestamptz_convergence is what actually converges them.
			Superseded:    []string{"632a419374009fedfa5e685aa8efa20664ce1cd910bb3f21c625ecdc925e2a9e"},
			CheckSQLite:   sqliteTableCheck("user_invitations"),
			CheckPostgres: pgTableCheck("user_invitations"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS user_invitations (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					user_id INTEGER NOT NULL,
					token TEXT UNIQUE NOT NULL,
					expires_at DATETIME NOT NULL,
					used_at DATETIME,
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
				);
				CREATE INDEX IF NOT EXISTS idx_user_invitations_token ON user_invitations(token);
				CREATE INDEX IF NOT EXISTS idx_user_invitations_user_id ON user_invitations(user_id);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS user_invitations (
					id SERIAL PRIMARY KEY,
					user_id INTEGER NOT NULL,
					token TEXT UNIQUE NOT NULL,
					expires_at TIMESTAMPTZ NOT NULL,
					used_at TIMESTAMPTZ,
					created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
					FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
				);
				CREATE INDEX IF NOT EXISTS idx_user_invitations_token ON user_invitations(token);
				CREATE INDEX IF NOT EXISTS idx_user_invitations_user_id ON user_invitations(user_id);
			`,
		},
		{
			Version: "inline_custom_field_indexes",
			Name:    "custom_field_indexes",
			// Superseded: this body was edited on 2026-05-17 (TIMESTAMP to TIMESTAMPTZ)
			// after it had already shipped and been applied, so databases stamped
			// before that edit carry the checksum below. The edit never reached those
			// databases (CREATE TABLE IF NOT EXISTS no-ops on an existing table);
			// 20260804_pg_timestamptz_convergence is what actually converges them.
			Superseded:    []string{"95f1cf74f4cf3901dd5541f6cd22382477784a20112ddccb80af6fdf18e062a9"},
			CheckSQLite:   sqliteTableCheck("custom_field_indexes"),
			CheckPostgres: pgTableCheck("custom_field_indexes"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS custom_field_indexes (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					custom_field_id INTEGER NOT NULL,
					target_table TEXT NOT NULL,
					index_name TEXT NOT NULL,
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					FOREIGN KEY (custom_field_id) REFERENCES custom_field_definitions(id) ON DELETE CASCADE,
					UNIQUE(custom_field_id, target_table)
				)
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS custom_field_indexes (
					id SERIAL PRIMARY KEY,
					custom_field_id INTEGER NOT NULL,
					target_table TEXT NOT NULL,
					index_name TEXT NOT NULL,
					created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
					FOREIGN KEY (custom_field_id) REFERENCES custom_field_definitions(id) ON DELETE CASCADE,
					UNIQUE(custom_field_id, target_table)
				)
			`,
		},
		{
			Version:     "inline_asset_report_fields_sqlite",
			Name:        "asset_report_fields (SQLite)",
			CheckSQLite: sqliteTableCheck("asset_report_fields"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS asset_report_fields (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					asset_report_id INTEGER NOT NULL,
					field_identifier TEXT NOT NULL,
					field_type TEXT NOT NULL,
					is_required BOOLEAN DEFAULT false,
					display_order INTEGER DEFAULT 0,
					options TEXT,
					display_name TEXT,
					description TEXT,
					step_number INTEGER DEFAULT 1,
					virtual_field_type TEXT,
					virtual_field_options TEXT,
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					FOREIGN KEY (asset_report_id) REFERENCES asset_reports(id) ON DELETE CASCADE
				);
				CREATE INDEX IF NOT EXISTS idx_asset_report_fields_asset_report_id ON asset_report_fields(asset_report_id);
			`,
		},
		{
			Version: "inline_api_tokens",
			Name:    "api_tokens",
			// Superseded: this body was edited on 2026-05-17 (TIMESTAMP to TIMESTAMPTZ)
			// after it had already shipped and been applied, so databases stamped
			// before that edit carry the checksum below. The edit never reached those
			// databases (CREATE TABLE IF NOT EXISTS no-ops on an existing table);
			// 20260804_pg_timestamptz_convergence is what actually converges them.
			Superseded:    []string{"be8df54e71a11f71eb67617324db48229f3905b1db4ffe4a1ab7f4b8ece32f32"},
			CheckSQLite:   sqliteTableCheck("api_tokens"),
			CheckPostgres: pgTableCheck("api_tokens"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS api_tokens (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
					name TEXT NOT NULL,
					token_hash TEXT NOT NULL UNIQUE,
					token_prefix TEXT NOT NULL,
					permissions TEXT DEFAULT '["read"]',
					expires_at DATETIME NULL,
					last_used_at DATETIME NULL,
					is_temporary BOOLEAN DEFAULT false,
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
				);
				CREATE INDEX IF NOT EXISTS idx_api_tokens_user_id ON api_tokens(user_id);
				CREATE INDEX IF NOT EXISTS idx_api_tokens_token_hash ON api_tokens(token_hash);
				CREATE INDEX IF NOT EXISTS idx_api_tokens_expires_at ON api_tokens(expires_at);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS api_tokens (
					id SERIAL PRIMARY KEY,
					user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
					name TEXT NOT NULL,
					token_hash TEXT NOT NULL UNIQUE,
					token_prefix TEXT NOT NULL,
					permissions TEXT DEFAULT '["read"]',
					expires_at TIMESTAMPTZ NULL,
					last_used_at TIMESTAMPTZ NULL,
					is_temporary BOOLEAN DEFAULT false,
					created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
					updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
				);
				CREATE INDEX IF NOT EXISTS idx_api_tokens_user_id ON api_tokens(user_id);
				CREATE INDEX IF NOT EXISTS idx_api_tokens_token_hash ON api_tokens(token_hash);
				CREATE INDEX IF NOT EXISTS idx_api_tokens_expires_at ON api_tokens(expires_at);
			`,
		},
		{
			Version: "inline_scm_processed_commits",
			Name:    "scm_processed_commits",
			// Superseded: this body was edited on 2026-05-17 (TIMESTAMP to TIMESTAMPTZ)
			// after it had already shipped and been applied, so databases stamped
			// before that edit carry the checksum below. The edit never reached those
			// databases (CREATE TABLE IF NOT EXISTS no-ops on an existing table);
			// 20260804_pg_timestamptz_convergence is what actually converges them.
			Superseded:    []string{"757968d42a47c9cab14d492e49204f7b4063d50e2039b655340b0e4f401b0924"},
			CheckSQLite:   sqliteTableCheck("scm_processed_commits"),
			CheckPostgres: pgTableCheck("scm_processed_commits"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS scm_processed_commits (
					commit_sha              TEXT NOT NULL,
					workspace_repository_id INTEGER NOT NULL,
					processed_at            DATETIME DEFAULT CURRENT_TIMESTAMP,
					actions_applied         INTEGER NOT NULL DEFAULT 0,
					PRIMARY KEY (commit_sha, workspace_repository_id),
					FOREIGN KEY (workspace_repository_id) REFERENCES workspace_repositories(id) ON DELETE CASCADE
				);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS scm_processed_commits (
					commit_sha              TEXT NOT NULL,
					workspace_repository_id INTEGER NOT NULL,
					processed_at            TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
					actions_applied         INTEGER NOT NULL DEFAULT 0,
					PRIMARY KEY (commit_sha, workspace_repository_id),
					FOREIGN KEY (workspace_repository_id) REFERENCES workspace_repositories(id) ON DELETE CASCADE
				);
			`,
		},
		{
			Version: "inline_cli_auth_codes",
			Name:    "cli_auth_codes",
			// Superseded: this body was edited on 2026-05-17 (TIMESTAMP to TIMESTAMPTZ)
			// after it had already shipped and been applied, so databases stamped
			// before that edit carry the checksum below. The edit never reached those
			// databases (CREATE TABLE IF NOT EXISTS no-ops on an existing table);
			// 20260804_pg_timestamptz_convergence is what actually converges them.
			Superseded:    []string{"5154e09fe27ccc3010da4bd038d275aa3c338858f1b8f3ee365e07d9be93f6b1"},
			CheckSQLite:   sqliteTableCheck("cli_auth_codes"),
			CheckPostgres: pgTableCheck("cli_auth_codes"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS cli_auth_codes (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					code TEXT NOT NULL UNIQUE,
					state TEXT NOT NULL,
					callback_url TEXT NOT NULL,
					hostname TEXT NOT NULL,
					agent_name TEXT NOT NULL,
					requested_scopes TEXT NOT NULL,
					status TEXT NOT NULL DEFAULT 'pending',
					approved_by_user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
					agent_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
					token_id INTEGER REFERENCES api_tokens(id) ON DELETE SET NULL,
					token_plaintext TEXT,
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					expires_at DATETIME NOT NULL,
					consumed_at DATETIME
				);
				CREATE INDEX IF NOT EXISTS idx_cli_auth_codes_code ON cli_auth_codes(code);
				CREATE INDEX IF NOT EXISTS idx_cli_auth_codes_expires_at ON cli_auth_codes(expires_at);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS cli_auth_codes (
					id SERIAL PRIMARY KEY,
					code TEXT NOT NULL UNIQUE,
					state TEXT NOT NULL,
					callback_url TEXT NOT NULL,
					hostname TEXT NOT NULL,
					agent_name TEXT NOT NULL,
					requested_scopes TEXT NOT NULL,
					status TEXT NOT NULL DEFAULT 'pending',
					approved_by_user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
					agent_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
					token_id INTEGER REFERENCES api_tokens(id) ON DELETE SET NULL,
					token_plaintext TEXT,
					created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
					expires_at TIMESTAMPTZ NOT NULL,
					consumed_at TIMESTAMPTZ
				);
				CREATE INDEX IF NOT EXISTS idx_cli_auth_codes_code ON cli_auth_codes(code);
				CREATE INDEX IF NOT EXISTS idx_cli_auth_codes_expires_at ON cli_auth_codes(expires_at);
			`,
		},
		{
			// native_auth_codes: short-lived single-use codes bridging a
			// system-browser SSO login back into a native client (desktop/iOS,
			// WI-446). Each code is bound to a session and redeemed once at
			// /api/auth/native/exchange for the encoded session cookie.
			Version:       "inline_native_auth_codes",
			Name:          "native_auth_codes",
			CheckSQLite:   sqliteTableCheck("native_auth_codes"),
			CheckPostgres: pgTableCheck("native_auth_codes"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS native_auth_codes (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					code TEXT NOT NULL UNIQUE,
					session_token TEXT NOT NULL,
					session_expires_at DATETIME NOT NULL,
					status TEXT NOT NULL DEFAULT 'valid',
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					expires_at DATETIME NOT NULL,
					consumed_at DATETIME
				);
				CREATE INDEX IF NOT EXISTS idx_native_auth_codes_code ON native_auth_codes(code);
				CREATE INDEX IF NOT EXISTS idx_native_auth_codes_expires_at ON native_auth_codes(expires_at);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS native_auth_codes (
					id SERIAL PRIMARY KEY,
					code TEXT NOT NULL UNIQUE,
					session_token TEXT NOT NULL,
					session_expires_at TIMESTAMPTZ NOT NULL,
					status TEXT NOT NULL DEFAULT 'valid',
					created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
					expires_at TIMESTAMPTZ NOT NULL,
					consumed_at TIMESTAMPTZ
				);
				CREATE INDEX IF NOT EXISTS idx_native_auth_codes_code ON native_auth_codes(code);
				CREATE INDEX IF NOT EXISTS idx_native_auth_codes_expires_at ON native_auth_codes(expires_at);
			`,
		},
		{
			// pr_comment_cursors: per-PR high-water mark of the last SCM comment
			// id the continuation poller has processed, so each sync tick only
			// looks at comments newer than this and never re-fires an old one
			// (the load-bearing idempotency layer of the comment loop guard).
			Version:       "inline_pr_comment_cursors",
			Name:          "pr_comment_cursors",
			CheckSQLite:   sqliteTableCheck("pr_comment_cursors"),
			CheckPostgres: pgTableCheck("pr_comment_cursors"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS pr_comment_cursors (
					workspace_repository_id INTEGER NOT NULL,
					pr_number INTEGER NOT NULL,
					last_comment_id INTEGER NOT NULL,
					updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					PRIMARY KEY (workspace_repository_id, pr_number),
					FOREIGN KEY (workspace_repository_id) REFERENCES workspace_repositories(id) ON DELETE CASCADE
				);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS pr_comment_cursors (
					workspace_repository_id INTEGER NOT NULL,
					pr_number INTEGER NOT NULL,
					last_comment_id BIGINT NOT NULL,
					updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
					PRIMARY KEY (workspace_repository_id, pr_number),
					FOREIGN KEY (workspace_repository_id) REFERENCES workspace_repositories(id) ON DELETE CASCADE
				);
			`,
		},
		{
			Version:       "20260711_agent_pr_review_events",
			Name:          "agent_pr_review_events",
			CheckSQLite:   sqliteTableCheck("agent_pr_review_events"),
			CheckPostgres: pgTableCheck("agent_pr_review_events"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS agent_pr_review_events (
					id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_repository_id INTEGER NOT NULL,
					workspace_id INTEGER NOT NULL, item_id INTEGER NOT NULL, pr_number INTEGER NOT NULL,
					event_kind TEXT NOT NULL, external_id INTEGER NOT NULL, author_id TEXT, author_login TEXT,
					author_association TEXT, body TEXT NOT NULL, context_json TEXT, status TEXT NOT NULL DEFAULT 'pending',
					agent_run_id INTEGER, ack_comment_id INTEGER, terminal_comment_id INTEGER, terminal_body TEXT,
					attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					UNIQUE(workspace_repository_id, pr_number, event_kind, external_id),
					FOREIGN KEY (workspace_repository_id) REFERENCES workspace_repositories(id) ON DELETE CASCADE,
					FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
					FOREIGN KEY (item_id) REFERENCES items(id) ON DELETE CASCADE
				);
				CREATE INDEX IF NOT EXISTS idx_agent_pr_review_events_status ON agent_pr_review_events(status, updated_at);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS agent_pr_review_events (
					id SERIAL PRIMARY KEY, workspace_repository_id INTEGER NOT NULL, workspace_id INTEGER NOT NULL,
					item_id INTEGER NOT NULL, pr_number INTEGER NOT NULL, event_kind TEXT NOT NULL, external_id BIGINT NOT NULL,
					author_id TEXT, author_login TEXT, author_association TEXT, body TEXT NOT NULL, context_json JSONB,
					status TEXT NOT NULL DEFAULT 'pending', agent_run_id INTEGER, ack_comment_id BIGINT,
					terminal_comment_id BIGINT, terminal_body TEXT, attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT,
					created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
					UNIQUE(workspace_repository_id, pr_number, event_kind, external_id),
					FOREIGN KEY (workspace_repository_id) REFERENCES workspace_repositories(id) ON DELETE CASCADE,
					FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
					FOREIGN KEY (item_id) REFERENCES items(id) ON DELETE CASCADE
				);
				CREATE INDEX IF NOT EXISTS idx_agent_pr_review_events_status ON agent_pr_review_events(status, updated_at);
			`,
		},
		{
			Version:       "20260711_agent_pr_ownerships",
			Name:          "agent_pr_ownerships",
			CheckSQLite:   sqliteTableCheck("agent_pr_ownerships"),
			CheckPostgres: pgTableCheck("agent_pr_ownerships"),
			SQLite: `CREATE TABLE IF NOT EXISTS agent_pr_ownerships (
				workspace_repository_id INTEGER NOT NULL, pr_number INTEGER NOT NULL, item_id INTEGER NOT NULL,
				agent_run_id INTEGER NOT NULL, binding_id INTEGER NOT NULL, triggered_by_user_id INTEGER,
				head_repo TEXT NOT NULL, head_branch TEXT NOT NULL, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (workspace_repository_id, pr_number),
				FOREIGN KEY (workspace_repository_id) REFERENCES workspace_repositories(id) ON DELETE CASCADE,
				FOREIGN KEY (item_id) REFERENCES items(id) ON DELETE CASCADE,
				FOREIGN KEY (triggered_by_user_id) REFERENCES users(id) ON DELETE SET NULL);`,
			Postgres: `CREATE TABLE IF NOT EXISTS agent_pr_ownerships (
				workspace_repository_id INTEGER NOT NULL, pr_number INTEGER NOT NULL, item_id INTEGER NOT NULL,
				agent_run_id INTEGER NOT NULL, binding_id INTEGER NOT NULL, triggered_by_user_id INTEGER,
				head_repo TEXT NOT NULL, head_branch TEXT NOT NULL, created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (workspace_repository_id, pr_number),
				FOREIGN KEY (workspace_repository_id) REFERENCES workspace_repositories(id) ON DELETE CASCADE,
				FOREIGN KEY (item_id) REFERENCES items(id) ON DELETE CASCADE,
				FOREIGN KEY (triggered_by_user_id) REFERENCES users(id) ON DELETE SET NULL);`,
		},
	}
}

// indexMigrations are standalone CREATE INDEX statements that the legacy
// code runs after the column-add slice.
func indexMigrations() []Migration {
	return []Migration{
		{
			Version:       "idx_users_is_agent",
			Name:          "idx_users_is_agent",
			CheckSQLite:   sqliteIndexCheck("idx_users_is_agent"),
			CheckPostgres: pgIndexCheck("idx_users_is_agent"),
			SQLite:        "CREATE INDEX IF NOT EXISTS idx_users_is_agent ON users(is_agent)",
			Postgres:      "CREATE INDEX IF NOT EXISTS idx_users_is_agent ON users(is_agent)",
		},
		{
			Version:       "idx_users_agent_owner",
			Name:          "idx_users_agent_owner",
			CheckSQLite:   sqliteIndexCheck("idx_users_agent_owner"),
			CheckPostgres: pgIndexCheck("idx_users_agent_owner"),
			SQLite:        "CREATE INDEX IF NOT EXISTS idx_users_agent_owner ON users(agent_owner_user_id) WHERE agent_owner_user_id IS NOT NULL",
			Postgres:      "CREATE INDEX IF NOT EXISTS idx_users_agent_owner ON users(agent_owner_user_id) WHERE agent_owner_user_id IS NOT NULL",
		},
		{
			Version:       "idx_portal_sessions_channel_id",
			Name:          "idx_portal_sessions_channel_id",
			CheckSQLite:   sqliteIndexCheck("idx_portal_sessions_channel_id"),
			CheckPostgres: pgIndexCheck("idx_portal_sessions_channel_id"),
			SQLite:        "CREATE INDEX IF NOT EXISTS idx_portal_sessions_channel_id ON portal_customer_sessions(channel_id)",
			Postgres:      "CREATE INDEX IF NOT EXISTS idx_portal_sessions_channel_id ON portal_customer_sessions(channel_id)",
		},
		{
			Version:       "idx_users_agent_provenance",
			Name:          "idx_users_agent_provenance",
			CheckSQLite:   sqliteIndexCheck("idx_users_agent_provenance"),
			CheckPostgres: pgIndexCheck("idx_users_agent_provenance"),
			SQLite:        "CREATE INDEX IF NOT EXISTS idx_users_agent_provenance ON users(agent_provenance) WHERE is_agent = true",
			Postgres:      "CREATE INDEX IF NOT EXISTS idx_users_agent_provenance ON users(agent_provenance) WHERE is_agent = true",
		},
		{
			Version:       "idx_users_oauth_client_id",
			Name:          "idx_users_oauth_client_id",
			CheckSQLite:   sqliteIndexCheck("idx_users_oauth_client_id"),
			CheckPostgres: pgIndexCheck("idx_users_oauth_client_id"),
			SQLite:        "CREATE INDEX IF NOT EXISTS idx_users_oauth_client_id ON users(oauth_client_id) WHERE oauth_client_id IS NOT NULL",
			Postgres:      "CREATE INDEX IF NOT EXISTS idx_users_oauth_client_id ON users(oauth_client_id) WHERE oauth_client_id IS NOT NULL",
		},
		{
			Version:       "idx_item_watches_user_active",
			Name:          "idx_item_watches_user_active",
			CheckSQLite:   sqliteIndexCheck("idx_item_watches_user_active"),
			CheckPostgres: pgIndexCheck("idx_item_watches_user_active"),
			SQLite:        "CREATE INDEX IF NOT EXISTS idx_item_watches_user_active ON item_watches(user_id, is_active)",
			Postgres:      "CREATE INDEX IF NOT EXISTS idx_item_watches_user_active ON item_watches(user_id, is_active)",
		},
	}
}

// triggerMigrations are the SQLite triggers and matching Postgres
// functions+triggers that enforce immutability and invariant rules on
// the users table.
//
// SQLite uses one trigger per invariant. Postgres uses two functions, each
// driving one trigger (CREATE OR REPLACE FUNCTION; DROP TRIGGER IF EXISTS;
// CREATE TRIGGER) — the legacy code recreates them on every startup which
// is fine since `CREATE OR REPLACE FUNCTION` is idempotent and the trigger
// is dropped first.
func triggerMigrations() []Migration {
	return []Migration{
		{
			Version:     "trig_users_is_agent_immutable_sqlite",
			Name:        "users_is_agent_immutable (SQLite trigger)",
			CheckSQLite: sqliteTriggerCheck("users_is_agent_immutable"),
			SQLite: `
				CREATE TRIGGER IF NOT EXISTS users_is_agent_immutable
				BEFORE UPDATE OF is_agent ON users
				FOR EACH ROW
				WHEN IFNULL(NEW.is_agent, 0) IS NOT IFNULL(OLD.is_agent, 0)
				BEGIN
					SELECT RAISE(ABORT, 'is_agent is immutable');
				END
			`,
		},
		{
			Version:     "trig_users_agent_owner_immutable_sqlite",
			Name:        "users_agent_owner_immutable (SQLite trigger)",
			CheckSQLite: sqliteTriggerCheck("users_agent_owner_immutable"),
			SQLite: `
				CREATE TRIGGER IF NOT EXISTS users_agent_owner_immutable
				BEFORE UPDATE OF agent_owner_user_id ON users
				FOR EACH ROW
				WHEN NEW.agent_owner_user_id IS NOT OLD.agent_owner_user_id
				BEGIN
					SELECT RAISE(ABORT, 'agent_owner_user_id is immutable');
				END
			`,
		},
		{
			Version:     "trig_users_agent_owner_requires_agent_insert_sqlite",
			Name:        "users_agent_owner_requires_agent_insert (SQLite trigger)",
			CheckSQLite: sqliteTriggerCheck("users_agent_owner_requires_agent_insert"),
			SQLite: `
				CREATE TRIGGER IF NOT EXISTS users_agent_owner_requires_agent_insert
				BEFORE INSERT ON users
				FOR EACH ROW
				WHEN NEW.agent_owner_user_id IS NOT NULL AND IFNULL(NEW.is_agent, 0) = 0
				BEGIN
					SELECT RAISE(ABORT, 'agent_owner_user_id requires is_agent');
				END
			`,
		},
		{
			Version:     "trig_users_oauth_provenance_requires_client_sqlite",
			Name:        "users_oauth_provenance_requires_client (SQLite trigger)",
			CheckSQLite: sqliteTriggerCheck("users_oauth_provenance_requires_client"),
			SQLite: `
				CREATE TRIGGER IF NOT EXISTS users_oauth_provenance_requires_client
				BEFORE INSERT ON users
				FOR EACH ROW
				WHEN NEW.agent_provenance = 'oauth' AND NEW.oauth_client_id IS NULL
				BEGIN
					SELECT RAISE(ABORT, 'agent_provenance=oauth requires oauth_client_id');
				END
			`,
		},
		{
			Version:     "trig_users_oauth_client_requires_oauth_agent_sqlite",
			Name:        "users_oauth_client_requires_oauth_agent (SQLite trigger)",
			CheckSQLite: sqliteTriggerCheck("users_oauth_client_requires_oauth_agent"),
			SQLite: `
				CREATE TRIGGER IF NOT EXISTS users_oauth_client_requires_oauth_agent
				BEFORE INSERT ON users
				FOR EACH ROW
				WHEN NEW.oauth_client_id IS NOT NULL
				  AND (IFNULL(NEW.is_agent, 0) = 0 OR NEW.agent_provenance != 'oauth')
				BEGIN
					SELECT RAISE(ABORT, 'oauth_client_id requires is_agent and agent_provenance=oauth');
				END
			`,
		},
		{
			Version:     "trig_users_agent_provenance_immutable_sqlite",
			Name:        "users_agent_provenance_immutable (SQLite trigger)",
			CheckSQLite: sqliteTriggerCheck("users_agent_provenance_immutable"),
			SQLite: `
				CREATE TRIGGER IF NOT EXISTS users_agent_provenance_immutable
				BEFORE UPDATE OF agent_provenance ON users
				FOR EACH ROW
				WHEN IFNULL(NEW.agent_provenance, '') IS NOT IFNULL(OLD.agent_provenance, '')
				BEGIN
					SELECT RAISE(ABORT, 'agent_provenance is immutable');
				END
			`,
		},
		{
			Version:     "trig_users_oauth_client_id_immutable_sqlite",
			Name:        "users_oauth_client_id_immutable (SQLite trigger)",
			CheckSQLite: sqliteTriggerCheck("users_oauth_client_id_immutable"),
			SQLite: `
				CREATE TRIGGER IF NOT EXISTS users_oauth_client_id_immutable
				BEFORE UPDATE OF oauth_client_id ON users
				FOR EACH ROW
				WHEN NEW.oauth_client_id IS NOT OLD.oauth_client_id
				BEGIN
					SELECT RAISE(ABORT, 'oauth_client_id is immutable');
				END
			`,
		},
		{
			Version:       "trig_users_is_agent_immutable_postgres",
			Name:          "users_is_agent_immutable (Postgres function+trigger)",
			CheckPostgres: pgTriggerCheck("users_is_agent_immutable_trigger"),
			Postgres: `
				CREATE OR REPLACE FUNCTION users_is_agent_immutable() RETURNS TRIGGER AS $fn$
				BEGIN
					IF COALESCE(NEW.is_agent, false) IS DISTINCT FROM COALESCE(OLD.is_agent, false) THEN
						RAISE EXCEPTION 'is_agent is immutable';
					END IF;
					IF NEW.agent_owner_user_id IS DISTINCT FROM OLD.agent_owner_user_id THEN
						RAISE EXCEPTION 'agent_owner_user_id is immutable';
					END IF;
					RETURN NEW;
				END;
				$fn$ LANGUAGE plpgsql;
				DROP TRIGGER IF EXISTS users_is_agent_immutable_trigger ON users;
				CREATE TRIGGER users_is_agent_immutable_trigger BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION users_is_agent_immutable();
			`,
		},
		{
			Version:       "trig_users_oauth_provenance_immutable_postgres",
			Name:          "users_oauth_provenance_immutable (Postgres function+trigger)",
			CheckPostgres: pgTriggerCheck("users_oauth_provenance_immutable_trigger"),
			Postgres: `
				CREATE OR REPLACE FUNCTION users_oauth_provenance_immutable() RETURNS TRIGGER AS $fn$
				BEGIN
					IF COALESCE(NEW.agent_provenance, '') IS DISTINCT FROM COALESCE(OLD.agent_provenance, '') THEN
						RAISE EXCEPTION 'agent_provenance is immutable';
					END IF;
					IF NEW.oauth_client_id IS DISTINCT FROM OLD.oauth_client_id THEN
						RAISE EXCEPTION 'oauth_client_id is immutable';
					END IF;
					RETURN NEW;
				END;
				$fn$ LANGUAGE plpgsql;
				DROP TRIGGER IF EXISTS users_oauth_provenance_immutable_trigger ON users;
				CREATE TRIGGER users_oauth_provenance_immutable_trigger BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION users_oauth_provenance_immutable();
			`,
		},
		{
			Version:       "trig_users_agent_owner_requires_agent_check_postgres",
			Name:          "users_agent_owner_requires_agent CHECK (Postgres)",
			CheckPostgres: pgConstraintCheck("users", "users_agent_owner_requires_agent"),
			Postgres:      "ALTER TABLE users ADD CONSTRAINT users_agent_owner_requires_agent CHECK (agent_owner_user_id IS NULL OR is_agent = true)",
		},
	}
}

// postSliceColumnAddMigrations covers column-add migrations that live as
// inline `if column-exists then ALTER` blocks AFTER the legacy migrations
// slice. They use the same pattern as the slice, just open-coded.
func postSliceColumnAddMigrations() []Migration {
	return []Migration{
		{
			Version:       "col_link_types_allowed_entity_types",
			Name:          "link_types.allowed_entity_types",
			CheckSQLite:   sqliteColumnCheck("link_types", "allowed_entity_types"),
			CheckPostgres: pgColumnCheck("link_types", "allowed_entity_types"),
			SQLite:        "ALTER TABLE link_types ADD COLUMN allowed_entity_types TEXT DEFAULT NULL",
			Postgres:      "ALTER TABLE link_types ADD COLUMN allowed_entity_types TEXT DEFAULT NULL",
		},
		{
			Version:       "col_item_links_custom_field_id",
			Name:          "item_links.custom_field_id (+ index)",
			CheckSQLite:   sqliteColumnCheck("item_links", "custom_field_id"),
			CheckPostgres: pgColumnCheck("item_links", "custom_field_id"),
			SQLite: `
				ALTER TABLE item_links ADD COLUMN custom_field_id INTEGER REFERENCES custom_field_definitions(id) ON DELETE CASCADE;
				CREATE INDEX IF NOT EXISTS idx_item_links_custom_field ON item_links(custom_field_id);
			`,
			Postgres: `
				ALTER TABLE item_links ADD COLUMN custom_field_id INTEGER REFERENCES custom_field_definitions(id) ON DELETE CASCADE;
				CREATE INDEX IF NOT EXISTS idx_item_links_custom_field ON item_links(custom_field_id);
			`,
		},
		{
			Version:       "col_configuration_sets_approval_set_id",
			Name:          "configuration_sets.approval_set_id",
			CheckSQLite:   sqliteColumnCheck("configuration_sets", "approval_set_id"),
			CheckPostgres: pgColumnCheck("configuration_sets", "approval_set_id"),
			SQLite:        "ALTER TABLE configuration_sets ADD COLUMN approval_set_id INTEGER",
			Postgres:      "ALTER TABLE configuration_sets ADD COLUMN approval_set_id INTEGER",
		},
		{
			Version:       "col_configuration_set_item_types_approval_set_id",
			Name:          "configuration_set_item_types.approval_set_id",
			CheckSQLite:   sqliteColumnCheck("configuration_set_item_types", "approval_set_id"),
			CheckPostgres: pgColumnCheck("configuration_set_item_types", "approval_set_id"),
			SQLite:        "ALTER TABLE configuration_set_item_types ADD COLUMN approval_set_id INTEGER",
			Postgres:      "ALTER TABLE configuration_set_item_types ADD COLUMN approval_set_id INTEGER",
		},
		{
			Version:       "col_workspace_roles_permissions_enabled",
			Name:          "workspace_roles.permissions_enabled",
			CheckSQLite:   sqliteColumnCheck("workspace_roles", "permissions_enabled"),
			CheckPostgres: pgColumnCheck("workspace_roles", "permissions_enabled"),
			SQLite:        "ALTER TABLE workspace_roles ADD COLUMN permissions_enabled BOOLEAN DEFAULT 1",
			Postgres:      "ALTER TABLE workspace_roles ADD COLUMN permissions_enabled BOOLEAN DEFAULT true",
		},
		{
			Version:       "col_approval_step_approvers_portal_customer_id",
			Name:          "approval_step_approvers.portal_customer_id",
			CheckSQLite:   sqliteColumnCheck("approval_step_approvers", "portal_customer_id"),
			CheckPostgres: pgColumnCheck("approval_step_approvers", "portal_customer_id"),
			SQLite:        "ALTER TABLE approval_step_approvers ADD COLUMN portal_customer_id INTEGER REFERENCES portal_customers(id) ON DELETE RESTRICT",
			Postgres:      "ALTER TABLE approval_step_approvers ADD COLUMN portal_customer_id INTEGER REFERENCES portal_customers(id) ON DELETE RESTRICT",
		},
		{
			Version:       "col_approval_decisions_actor_portal_customer_id",
			Name:          "approval_decisions.actor_portal_customer_id",
			CheckSQLite:   sqliteColumnCheck("approval_decisions", "actor_portal_customer_id"),
			CheckPostgres: pgColumnCheck("approval_decisions", "actor_portal_customer_id"),
			SQLite:        "ALTER TABLE approval_decisions ADD COLUMN actor_portal_customer_id INTEGER REFERENCES portal_customers(id) ON DELETE SET NULL",
			Postgres:      "ALTER TABLE approval_decisions ADD COLUMN actor_portal_customer_id INTEGER REFERENCES portal_customers(id) ON DELETE SET NULL",
		},
		{
			Version:       "col_conditions_error_message",
			Name:          "conditions.error_message",
			CheckSQLite:   sqliteColumnCheck("conditions", "error_message"),
			CheckPostgres: pgColumnCheck("conditions", "error_message"),
			SQLite:        "ALTER TABLE conditions ADD COLUMN error_message TEXT",
			Postgres:      "ALTER TABLE conditions ADD COLUMN error_message TEXT",
		},
		{
			Version:       "col_conditions_mode",
			Name:          "conditions.mode",
			CheckSQLite:   sqliteColumnCheck("conditions", "mode"),
			CheckPostgres: pgColumnCheck("conditions", "mode"),
			SQLite:        "ALTER TABLE conditions ADD COLUMN mode TEXT NOT NULL DEFAULT 'condition'",
			Postgres:      "ALTER TABLE conditions ADD COLUMN mode TEXT NOT NULL DEFAULT 'condition'",
		},
		{
			Version:       "col_configuration_sets_condition_set_id",
			Name:          "configuration_sets.condition_set_id",
			CheckSQLite:   sqliteColumnCheck("configuration_sets", "condition_set_id"),
			CheckPostgres: pgColumnCheck("configuration_sets", "condition_set_id"),
			SQLite:        "ALTER TABLE configuration_sets ADD COLUMN condition_set_id INTEGER REFERENCES condition_sets(id) ON DELETE SET NULL",
			Postgres:      "ALTER TABLE configuration_sets ADD COLUMN condition_set_id INTEGER REFERENCES condition_sets(id) ON DELETE SET NULL",
		},
		{
			Version:       "col_configuration_set_item_types_condition_set_id",
			Name:          "configuration_set_item_types.condition_set_id",
			CheckSQLite:   sqliteColumnCheck("configuration_set_item_types", "condition_set_id"),
			CheckPostgres: pgColumnCheck("configuration_set_item_types", "condition_set_id"),
			SQLite:        "ALTER TABLE configuration_set_item_types ADD COLUMN condition_set_id INTEGER REFERENCES condition_sets(id) ON DELETE SET NULL",
			Postgres:      "ALTER TABLE configuration_set_item_types ADD COLUMN condition_set_id INTEGER REFERENCES condition_sets(id) ON DELETE SET NULL",
		},
		{
			Version:       "col_collections_public_slug",
			Name:          "collections.public_slug (+ unique index)",
			CheckSQLite:   sqliteColumnCheck("collections", "public_slug"),
			CheckPostgres: pgColumnCheck("collections", "public_slug"),
			SQLite: `
				ALTER TABLE collections ADD COLUMN public_slug TEXT;
				CREATE UNIQUE INDEX IF NOT EXISTS idx_collections_public_slug ON collections(public_slug);
			`,
			Postgres: `
				ALTER TABLE collections ADD COLUMN public_slug TEXT;
				CREATE UNIQUE INDEX IF NOT EXISTS idx_collections_public_slug ON collections(public_slug);
			`,
		},
		{
			Version:       "col_collections_filter_state",
			Name:          "collections.filter_state",
			CheckSQLite:   sqliteColumnCheck("collections", "filter_state"),
			CheckPostgres: pgColumnCheck("collections", "filter_state"),
			SQLite:        "ALTER TABLE collections ADD COLUMN filter_state TEXT",
			Postgres:      "ALTER TABLE collections ADD COLUMN filter_state TEXT",
		},
		{
			Version:       "col_items_story_points",
			Name:          "items.story_points",
			CheckSQLite:   sqliteColumnCheck("items", "story_points"),
			CheckPostgres: pgColumnCheck("items", "story_points"),
			SQLite:        "ALTER TABLE items ADD COLUMN story_points REAL",
			Postgres:      "ALTER TABLE items ADD COLUMN story_points REAL",
		},
		{
			Version:       "col_items_estimate_minutes",
			Name:          "items.estimate_minutes",
			CheckSQLite:   sqliteColumnCheck("items", "estimate_minutes"),
			CheckPostgres: pgColumnCheck("items", "estimate_minutes"),
			SQLite:        "ALTER TABLE items ADD COLUMN estimate_minutes INTEGER",
			Postgres:      "ALTER TABLE items ADD COLUMN estimate_minutes INTEGER",
		},
		{
			Version:       "col_approval_requests_from_status_id",
			Name:          "approval_requests.from_status_id",
			CheckSQLite:   sqliteColumnCheck("approval_requests", "from_status_id"),
			CheckPostgres: pgColumnCheck("approval_requests", "from_status_id"),
			SQLite:        "ALTER TABLE approval_requests ADD COLUMN from_status_id INTEGER REFERENCES statuses(id) ON DELETE SET NULL",
			Postgres:      "ALTER TABLE approval_requests ADD COLUMN from_status_id INTEGER REFERENCES statuses(id) ON DELETE SET NULL",
		},
		{
			Version:       "col_request_types_config",
			Name:          "request_types.config",
			CheckSQLite:   sqliteColumnCheck("request_types", "config"),
			CheckPostgres: pgColumnCheck("request_types", "config"),
			SQLite:        "ALTER TABLE request_types ADD COLUMN config TEXT DEFAULT NULL",
			Postgres:      "ALTER TABLE request_types ADD COLUMN config TEXT DEFAULT NULL",
		},
		{
			Version:       "col_asset_reports_run_mode",
			Name:          "asset_reports.run_mode",
			CheckSQLite:   sqliteColumnCheck("asset_reports", "run_mode"),
			CheckPostgres: pgColumnCheck("asset_reports", "run_mode"),
			SQLite:        "ALTER TABLE asset_reports ADD COLUMN run_mode TEXT NOT NULL DEFAULT 'direct'",
			Postgres:      "ALTER TABLE asset_reports ADD COLUMN run_mode TEXT NOT NULL DEFAULT 'direct'",
		},
		{
			Version:       "col_asset_reports_item_type_id",
			Name:          "asset_reports.item_type_id",
			CheckSQLite:   sqliteColumnCheck("asset_reports", "item_type_id"),
			CheckPostgres: pgColumnCheck("asset_reports", "item_type_id"),
			SQLite:        "ALTER TABLE asset_reports ADD COLUMN item_type_id INTEGER DEFAULT NULL REFERENCES item_types(id) ON DELETE SET NULL",
			Postgres:      "ALTER TABLE asset_reports ADD COLUMN item_type_id INTEGER DEFAULT NULL REFERENCES item_types(id) ON DELETE SET NULL",
		},
		{
			Version:       "col_asset_reports_workspace_id",
			Name:          "asset_reports.workspace_id",
			CheckSQLite:   sqliteColumnCheck("asset_reports", "workspace_id"),
			CheckPostgres: pgColumnCheck("asset_reports", "workspace_id"),
			SQLite:        "ALTER TABLE asset_reports ADD COLUMN workspace_id INTEGER DEFAULT NULL REFERENCES workspaces(id) ON DELETE SET NULL",
			Postgres:      "ALTER TABLE asset_reports ADD COLUMN workspace_id INTEGER DEFAULT NULL REFERENCES workspaces(id) ON DELETE SET NULL",
		},
		{
			Version:       "col_asset_reports_config",
			Name:          "asset_reports.config",
			CheckSQLite:   sqliteColumnCheck("asset_reports", "config"),
			CheckPostgres: pgColumnCheck("asset_reports", "config"),
			SQLite:        "ALTER TABLE asset_reports ADD COLUMN config TEXT DEFAULT NULL",
			Postgres:      "ALTER TABLE asset_reports ADD COLUMN config TEXT DEFAULT NULL",
		},
	}
}

// samlMigrations are the SAML column adds on sso_providers (Postgres
// pgMigrations array has them too, but in the SQLite path they live
// outside the legacy migrations slice).
func samlMigrations() []Migration {
	return []Migration{
		{
			Version:       "saml_sso_providers_idp_metadata_url",
			Name:          "sso_providers.saml_idp_metadata_url",
			CheckSQLite:   sqliteColumnCheck("sso_providers", "saml_idp_metadata_url"),
			CheckPostgres: pgColumnCheck("sso_providers", "saml_idp_metadata_url"),
			SQLite:        "ALTER TABLE sso_providers ADD COLUMN saml_idp_metadata_url TEXT",
			Postgres:      "ALTER TABLE sso_providers ADD COLUMN saml_idp_metadata_url TEXT",
		},
		{
			Version:       "saml_sso_providers_idp_sso_url",
			Name:          "sso_providers.saml_idp_sso_url",
			CheckSQLite:   sqliteColumnCheck("sso_providers", "saml_idp_sso_url"),
			CheckPostgres: pgColumnCheck("sso_providers", "saml_idp_sso_url"),
			SQLite:        "ALTER TABLE sso_providers ADD COLUMN saml_idp_sso_url TEXT",
			Postgres:      "ALTER TABLE sso_providers ADD COLUMN saml_idp_sso_url TEXT",
		},
		{
			Version:       "saml_sso_providers_idp_certificate",
			Name:          "sso_providers.saml_idp_certificate",
			CheckSQLite:   sqliteColumnCheck("sso_providers", "saml_idp_certificate"),
			CheckPostgres: pgColumnCheck("sso_providers", "saml_idp_certificate"),
			SQLite:        "ALTER TABLE sso_providers ADD COLUMN saml_idp_certificate TEXT",
			Postgres:      "ALTER TABLE sso_providers ADD COLUMN saml_idp_certificate TEXT",
		},
		{
			Version:       "saml_sso_providers_sp_entity_id",
			Name:          "sso_providers.saml_sp_entity_id",
			CheckSQLite:   sqliteColumnCheck("sso_providers", "saml_sp_entity_id"),
			CheckPostgres: pgColumnCheck("sso_providers", "saml_sp_entity_id"),
			SQLite:        "ALTER TABLE sso_providers ADD COLUMN saml_sp_entity_id TEXT",
			Postgres:      "ALTER TABLE sso_providers ADD COLUMN saml_sp_entity_id TEXT",
		},
		{
			Version:       "saml_sso_providers_sign_requests",
			Name:          "sso_providers.saml_sign_requests",
			CheckSQLite:   sqliteColumnCheck("sso_providers", "saml_sign_requests"),
			CheckPostgres: pgColumnCheck("sso_providers", "saml_sign_requests"),
			SQLite:        "ALTER TABLE sso_providers ADD COLUMN saml_sign_requests BOOLEAN DEFAULT 0",
			Postgres:      "ALTER TABLE sso_providers ADD COLUMN saml_sign_requests BOOLEAN DEFAULT FALSE",
		},
	}
}

// milestoneScmDropMigrations drop the legacy SCM columns from milestones,
// which moved to milestone_releases. Uses sqliteMilestonesColumnAbsentCheck so the
// runner stamps without running when the column is already gone (which
// is the case on fresh installs whose schema files never had these
// columns to begin with).
func milestoneScmDropMigrations() []Migration {
	return []Migration{
		{
			Version:       "drop_milestones_scm_connection_id",
			Name:          "DROP milestones.scm_connection_id",
			CheckSQLite:   sqliteMilestonesColumnAbsentCheck("scm_connection_id"),
			CheckPostgres: pgMilestonesColumnAbsentCheck("scm_connection_id"),
			SQLite:        "ALTER TABLE milestones DROP COLUMN scm_connection_id",
			Postgres:      "ALTER TABLE milestones DROP COLUMN scm_connection_id",
		},
		{
			Version:       "drop_milestones_scm_repository",
			Name:          "DROP milestones.scm_repository",
			CheckSQLite:   sqliteMilestonesColumnAbsentCheck("scm_repository"),
			CheckPostgres: pgMilestonesColumnAbsentCheck("scm_repository"),
			SQLite:        "ALTER TABLE milestones DROP COLUMN scm_repository",
			Postgres:      "ALTER TABLE milestones DROP COLUMN scm_repository",
		},
		{
			Version:       "drop_milestones_scm_release_id",
			Name:          "DROP milestones.scm_release_id",
			CheckSQLite:   sqliteMilestonesColumnAbsentCheck("scm_release_id"),
			CheckPostgres: pgMilestonesColumnAbsentCheck("scm_release_id"),
			SQLite:        "ALTER TABLE milestones DROP COLUMN scm_release_id",
			Postgres:      "ALTER TABLE milestones DROP COLUMN scm_release_id",
		},
		{
			Version:       "drop_milestones_scm_release_url",
			Name:          "DROP milestones.scm_release_url",
			CheckSQLite:   sqliteMilestonesColumnAbsentCheck("scm_release_url"),
			CheckPostgres: pgMilestonesColumnAbsentCheck("scm_release_url"),
			SQLite:        "ALTER TABLE milestones DROP COLUMN scm_release_url",
			Postgres:      "ALTER TABLE milestones DROP COLUMN scm_release_url",
		},
	}
}

// seedMigrations are the legacy "INSERT row if missing" seeds for
// system_settings, permissions, and screen_fields.
func seedMigrations() []Migration {
	return []Migration{
		{
			Version:       "seed_allow_user_managed_agents",
			Name:          "system_settings.allow_user_managed_agents",
			CheckSQLite:   "SELECT COUNT(*) FROM system_settings WHERE key = 'allow_user_managed_agents'",
			CheckPostgres: "SELECT COUNT(*) FROM system_settings WHERE key = 'allow_user_managed_agents'",
			SQLite:        "INSERT INTO system_settings (key, value, value_type, description, category) VALUES ('allow_user_managed_agents', 'false', 'boolean', 'Allow non-admin users to create and manage their own agent users from their profile', 'security')",
			Postgres:      "INSERT INTO system_settings (key, value, value_type, description, category) VALUES ('allow_user_managed_agents', 'false', 'boolean', 'Allow non-admin users to create and manage their own agent users from their profile', 'security')",
		},
		{
			Version:       "seed_max_agents_per_user",
			Name:          "system_settings.max_agents_per_user",
			CheckSQLite:   "SELECT COUNT(*) FROM system_settings WHERE key = 'max_agents_per_user'",
			CheckPostgres: "SELECT COUNT(*) FROM system_settings WHERE key = 'max_agents_per_user'",
			SQLite:        "INSERT INTO system_settings (key, value, value_type, description, category) VALUES ('max_agents_per_user', '5', 'integer', 'Maximum number of owned agents a single non-admin user may create', 'security')",
			Postgres:      "INSERT INTO system_settings (key, value, value_type, description, category) VALUES ('max_agents_per_user', '5', 'integer', 'Maximum number of owned agents a single non-admin user may create', 'security')",
		},
		{
			Version:       "seed_max_custom_field_indexes_per_table",
			Name:          "system_settings.max_custom_field_indexes_per_table",
			CheckSQLite:   "SELECT COUNT(*) FROM system_settings WHERE key = 'max_custom_field_indexes_per_table'",
			CheckPostgres: "SELECT COUNT(*) FROM system_settings WHERE key = 'max_custom_field_indexes_per_table'",
			SQLite:        "INSERT INTO system_settings (key, value, value_type, description, category) VALUES ('max_custom_field_indexes_per_table', '20', 'integer', 'Maximum number of custom field indexes per table', 'performance')",
			Postgres:      "INSERT INTO system_settings (key, value, value_type, description, category) VALUES ('max_custom_field_indexes_per_table', '20', 'integer', 'Maximum number of custom field indexes per table', 'performance')",
		},
		{
			Version:       "seed_ai_chat_enabled",
			Name:          "system_settings.ai_chat_enabled",
			CheckSQLite:   "SELECT COUNT(*) FROM system_settings WHERE key = 'ai_chat_enabled'",
			CheckPostgres: "SELECT COUNT(*) FROM system_settings WHERE key = 'ai_chat_enabled'",
			SQLite:        "INSERT INTO system_settings (key, value, value_type, description, category) VALUES ('ai_chat_enabled', 'true', 'boolean', 'Enable AI chat functionality', 'modules')",
			Postgres:      "INSERT INTO system_settings (key, value, value_type, description, category) VALUES ('ai_chat_enabled', 'true', 'boolean', 'Enable AI chat functionality', 'modules')",
		},
		{
			Version:       "seed_permission_public_board_manage",
			Name:          "permissions.public_board.manage",
			CheckSQLite:   "SELECT COUNT(*) FROM permissions WHERE permission_key = 'public_board.manage'",
			CheckPostgres: "SELECT COUNT(*) FROM permissions WHERE permission_key = 'public_board.manage'",
			SQLite:        "INSERT OR IGNORE INTO permissions (permission_key, permission_name, description, scope, is_system) VALUES ('public_board.manage', 'Manage Public Boards', 'Can make collections public and configure public board sharing', 'global', 0)",
			Postgres:      "INSERT INTO permissions (permission_key, permission_name, description, scope, is_system) VALUES ('public_board.manage', 'Manage Public Boards', 'Can make collections public and configure public board sharing', 'global', false) ON CONFLICT (permission_key) DO NOTHING",
		},
		{
			Version:       "seed_permission_teams_manage",
			Name:          "permissions.teams.manage",
			CheckSQLite:   "SELECT COUNT(*) FROM permissions WHERE permission_key = 'teams.manage'",
			CheckPostgres: "SELECT COUNT(*) FROM permissions WHERE permission_key = 'teams.manage'",
			SQLite:        "INSERT OR IGNORE INTO permissions (permission_key, permission_name, description, scope, is_system) VALUES ('teams.manage', 'Manage Teams', 'Can create, edit, and delete teams', 'global', 0)",
			Postgres:      "INSERT INTO permissions (permission_key, permission_name, description, scope, is_system) VALUES ('teams.manage', 'Manage Teams', 'Can create, edit, and delete teams', 'global', false) ON CONFLICT (permission_key) DO NOTHING",
		},
		{
			Version:       "seed_screen_field_due_date",
			Name:          "screen_fields.due_date (screen 1)",
			CheckSQLite:   "SELECT COUNT(*) FROM screen_fields WHERE screen_id = 1 AND field_identifier = 'due_date'",
			CheckPostgres: "SELECT COUNT(*) FROM screen_fields WHERE screen_id = 1 AND field_identifier = 'due_date'",
			SQLite:        "INSERT INTO screen_fields (screen_id, field_type, field_identifier, display_order, is_required, field_width) VALUES (1, 'system', 'due_date', 6, false, 'half')",
			Postgres:      "INSERT INTO screen_fields (screen_id, field_type, field_identifier, display_order, is_required, field_width) VALUES (1, 'system', 'due_date', 6, false, 'half')",
		},
		{
			Version:     "seed_screen_field_labels_sqlite",
			Name:        "screen_fields.labels (screen 1, SQLite)",
			CheckSQLite: "SELECT COUNT(*) FROM screen_fields WHERE screen_id = 1 AND field_identifier = 'labels'",
			SQLite:      "INSERT INTO screen_fields (screen_id, field_type, field_identifier, display_order, is_required, field_width) VALUES (1, 'system', 'labels', 11, false, 'full')",
		},
	}
}

// miscMigrations cover small one-shot legacy operations: dropping
// workspace_everyone_roles, seeding link_types Tests row, and replacing
// the approval_set_statuses inline UNIQUE with a partial unique index.
//
// The approval_set_statuses table rebuild on SQLite (handled by the
// legacy code via CREATE NEW / INSERT / DROP / RENAME inside an explicit
// tx with PRAGMA foreign_keys toggling) is deliberately NOT ported here;
// it remains in the legacy branch until PR 2d.
func miscMigrations() []Migration {
	return []Migration{
		{
			Version: "drop_workspace_everyone_roles",
			Name:    "DROP TABLE workspace_everyone_roles",
			// Check returns 1 when the legacy table is already gone (fresh
			// installs never had it; older installs that already ran this
			// migration). Returns 0 only on a pre-migration install that
			// still carries the table, in which case the body runs.
			CheckSQLite:   "SELECT CASE WHEN EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='workspace_everyone_roles') THEN 0 ELSE 1 END",
			CheckPostgres: "SELECT CASE WHEN EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='workspace_everyone_roles') THEN 0 ELSE 1 END",
			SQLite:        "DROP TABLE IF EXISTS workspace_everyone_roles",
			Postgres:      "DROP TABLE IF EXISTS workspace_everyone_roles",
		},
		{
			Version:       "seed_link_types_tests_allowed_entity_types",
			Name:          "link_types Tests allowed_entity_types seed",
			CheckSQLite:   "SELECT COUNT(*) FROM link_types WHERE name = 'Tests' AND is_system = true AND allowed_entity_types IS NOT NULL",
			CheckPostgres: "SELECT COUNT(*) FROM link_types WHERE name = 'Tests' AND is_system = true AND allowed_entity_types IS NOT NULL",
			SQLite:        `UPDATE link_types SET allowed_entity_types = '["item","test_case"]' WHERE name = 'Tests' AND is_system = true AND allowed_entity_types IS NULL`,
			Postgres:      `UPDATE link_types SET allowed_entity_types = '["item","test_case"]' WHERE name = 'Tests' AND is_system = true AND allowed_entity_types IS NULL`,
		},
		{
			Version:       "idx_uq_approval_set_statuses_active",
			Name:          "uq_approval_set_statuses_active (partial unique index)",
			CheckSQLite:   sqliteIndexCheck("uq_approval_set_statuses_active"),
			CheckPostgres: pgIndexCheck("uq_approval_set_statuses_active"),
			SQLite:        "CREATE UNIQUE INDEX IF NOT EXISTS uq_approval_set_statuses_active ON approval_set_statuses(approval_set_id, status_id) WHERE is_active = 1",
			Postgres:      "CREATE UNIQUE INDEX IF NOT EXISTS uq_approval_set_statuses_active ON approval_set_statuses(approval_set_id, status_id) WHERE is_active = TRUE",
		},
		{
			// Bughunt #6: collapse duplicate notification-setting assignments
			// per configuration set. Keep the most recent (highest id) row;
			// the follow-up migration adds the UNIQUE(configuration_set_id)
			// index. No Check — the DELETE is idempotent (zero rows match
			// once dedup is done) and the runner stamps after first run.
			Version: "dedupe_configuration_set_notification_settings",
			Name:    "dedupe configuration_set_notification_settings to one row per config set",
			// Check on the named unique index that the next migration creates
			// (and schema/notifications{,_postgres}.sql carries on fresh
			// installs). If the index exists, duplicates already cannot
			// exist, so this dedupe is unreachable and the body is stamped
			// without running.
			CheckSQLite:   sqliteIndexCheck("uq_config_set_notification_setting_one_per_set"),
			CheckPostgres: pgIndexCheck("uq_config_set_notification_setting_one_per_set"),
			SQLite:        "DELETE FROM configuration_set_notification_settings WHERE id NOT IN (SELECT MAX(id) FROM configuration_set_notification_settings GROUP BY configuration_set_id)",
			Postgres:      "DELETE FROM configuration_set_notification_settings WHERE id NOT IN (SELECT MAX(id) FROM configuration_set_notification_settings GROUP BY configuration_set_id)",
		},
		{
			Version:       "idx_uq_config_set_notification_setting_one_per_set",
			Name:          "uq_config_set_notification_setting_one_per_set (one notification setting per config set)",
			CheckSQLite:   sqliteIndexCheck("uq_config_set_notification_setting_one_per_set"),
			CheckPostgres: pgIndexCheck("uq_config_set_notification_setting_one_per_set"),
			SQLite:        "CREATE UNIQUE INDEX IF NOT EXISTS uq_config_set_notification_setting_one_per_set ON configuration_set_notification_settings(configuration_set_id)",
			Postgres:      "CREATE UNIQUE INDEX IF NOT EXISTS uq_config_set_notification_setting_one_per_set ON configuration_set_notification_settings(configuration_set_id)",
		},
		{
			// Bughunt #11: split "seen in tray" from "read/acknowledged" so
			// auto-mark-as-seen on tray view doesn't suppress email batching
			// (which fires only on read = false).
			Version:       "notifications_seen_at",
			Name:          "notifications.seen_at",
			CheckSQLite:   sqliteColumnCheck("notifications", "seen_at"),
			CheckPostgres: pgColumnCheck("notifications", "seen_at"),
			SQLite:        "ALTER TABLE notifications ADD COLUMN seen_at DATETIME",
			Postgres:      "ALTER TABLE notifications ADD COLUMN seen_at TIMESTAMPTZ",
		},
		{
			// WI-46: branding attachments (workspace/portal/hub backgrounds
			// and logos) historically stored the *owner* id (workspace_id,
			// portal_id, hub_id) in attachments.item_id. The items-scoped
			// list query filters by item_id only and so leaked those rows
			// onto unrelated work items whose numeric id collided with the
			// owner id. CreateRecord now writes NULL for these entity_types;
			// this migration nulls the existing rows so the symptom clears
			// without a manual SQL fix-up.
			Version: "wi46_null_branding_attachment_item_id",
			Name:    "WI-46 null item_id for workspace/portal/hub branding attachments",
			// Check returns 1 when no branding attachments need scrubbing
			// (fresh installs with no rows, or older installs that already
			// ran this migration). Otherwise the body runs and nulls
			// item_id on the offending rows.
			CheckSQLite:   "SELECT CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE entity_type IN ('workspace_background','portal_background','portal_logo','hub_logo') AND item_id IS NOT NULL) THEN 0 ELSE 1 END",
			CheckPostgres: "SELECT CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE entity_type IN ('workspace_background','portal_background','portal_logo','hub_logo') AND item_id IS NOT NULL) THEN 0 ELSE 1 END",
			SQLite:        "UPDATE attachments SET item_id = NULL WHERE entity_type IN ('workspace_background','portal_background','portal_logo','hub_logo') AND item_id IS NOT NULL",
			Postgres:      "UPDATE attachments SET item_id = NULL WHERE entity_type IN ('workspace_background','portal_background','portal_logo','hub_logo') AND item_id IS NOT NULL",
		},
		// Milestone-from-tag automation: stable upsert key on milestones,
		// per-repo glob filters, and the tag/branch idempotency ledger.
		{
			Version:       "col_milestones_external_key",
			Name:          "milestones.external_key",
			CheckSQLite:   sqliteColumnCheck("milestones", "external_key"),
			CheckPostgres: pgColumnCheck("milestones", "external_key"),
			SQLite:        "ALTER TABLE milestones ADD COLUMN external_key TEXT",
			Postgres:      "ALTER TABLE milestones ADD COLUMN external_key TEXT",
		},
		{
			Version:       "idx_uq_milestones_workspace_external_key",
			Name:          "uq_milestones_workspace_external_key (partial unique)",
			CheckSQLite:   sqliteIndexCheck("uq_milestones_workspace_external_key"),
			CheckPostgres: pgIndexCheck("uq_milestones_workspace_external_key"),
			SQLite:        "CREATE UNIQUE INDEX IF NOT EXISTS uq_milestones_workspace_external_key ON milestones(workspace_id, external_key) WHERE external_key IS NOT NULL",
			Postgres:      "CREATE UNIQUE INDEX IF NOT EXISTS uq_milestones_workspace_external_key ON milestones(workspace_id, external_key) WHERE external_key IS NOT NULL",
		},
		{
			Version:       "col_workspace_repositories_milestone_tag_pattern",
			Name:          "workspace_repositories.milestone_tag_pattern",
			CheckSQLite:   sqliteColumnCheck("workspace_repositories", "milestone_tag_pattern"),
			CheckPostgres: pgColumnCheck("workspace_repositories", "milestone_tag_pattern"),
			SQLite:        "ALTER TABLE workspace_repositories ADD COLUMN milestone_tag_pattern TEXT NOT NULL DEFAULT 'v*'",
			Postgres:      "ALTER TABLE workspace_repositories ADD COLUMN milestone_tag_pattern TEXT NOT NULL DEFAULT 'v*'",
		},
		{
			Version:       "col_workspace_repositories_milestone_branch_pattern",
			Name:          "workspace_repositories.milestone_branch_pattern",
			CheckSQLite:   sqliteColumnCheck("workspace_repositories", "milestone_branch_pattern"),
			CheckPostgres: pgColumnCheck("workspace_repositories", "milestone_branch_pattern"),
			SQLite:        "ALTER TABLE workspace_repositories ADD COLUMN milestone_branch_pattern TEXT NOT NULL DEFAULT 'release/*'",
			Postgres:      "ALTER TABLE workspace_repositories ADD COLUMN milestone_branch_pattern TEXT NOT NULL DEFAULT 'release/*'",
		},
		{
			Version:       "inline_scm_processed_refs",
			Name:          "scm_processed_refs (tag/branch idempotency ledger)",
			CheckSQLite:   sqliteTableCheck("scm_processed_refs"),
			CheckPostgres: pgTableCheck("scm_processed_refs"),
			SQLite: `
				CREATE TABLE IF NOT EXISTS scm_processed_refs (
					workspace_repository_id INTEGER NOT NULL,
					ref_type                TEXT NOT NULL,
					ref_name                TEXT NOT NULL,
					sha                     TEXT,
					processed_at            DATETIME DEFAULT CURRENT_TIMESTAMP,
					PRIMARY KEY (workspace_repository_id, ref_type, ref_name),
					FOREIGN KEY (workspace_repository_id) REFERENCES workspace_repositories(id) ON DELETE CASCADE
				);
			`,
			Postgres: `
				CREATE TABLE IF NOT EXISTS scm_processed_refs (
					workspace_repository_id INTEGER NOT NULL,
					ref_type                TEXT NOT NULL,
					ref_name                TEXT NOT NULL,
					sha                     TEXT,
					processed_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
					PRIMARY KEY (workspace_repository_id, ref_type, ref_name),
					FOREIGN KEY (workspace_repository_id) REFERENCES workspace_repositories(id) ON DELETE CASCADE
				);
			`,
		},
		{
			// Older comment.created rules disabled new commenter watches; enable them
			// so follow-up replies reach participants. Check skips once all are fixed.
			Version:       "comment_created_notify_watchers",
			Name:          "comment.created notify_watchers = true (follow-on-comment)",
			CheckSQLite:   "SELECT CASE WHEN EXISTS(SELECT 1 FROM notification_event_rules WHERE event_type = 'comment.created' AND notify_watchers = 0) THEN 0 ELSE 1 END",
			CheckPostgres: "SELECT CASE WHEN EXISTS(SELECT 1 FROM notification_event_rules WHERE event_type = 'comment.created' AND notify_watchers = false) THEN 0 ELSE 1 END",
			SQLite:        "UPDATE notification_event_rules SET notify_watchers = 1 WHERE event_type = 'comment.created' AND notify_watchers = 0",
			Postgres:      "UPDATE notification_event_rules SET notify_watchers = true WHERE event_type = 'comment.created' AND notify_watchers = false",
		},
	}
}

// driftFixMigrations close gaps where the base schema/default data had moved
// ahead of the upgrade path. They intentionally run after the schema re-runs
// and legacy column-add catalog entries so their checks see the final shape of
// an upgraded database.
func driftFixMigrations() []Migration {
	return []Migration{
		{
			// A 2026-05-17 edit switched these Postgres columns from TIMESTAMP to
			// TIMESTAMPTZ inside CREATE TABLE IF NOT EXISTS bodies that had already
			// been applied. Those bodies no-op against an existing table, so only
			// databases created after that date ever got the timezone-aware type.
			// This converges the ones created before it. The matching Superseded
			// entries on those migrations are what let such databases boot at all.
			//
			// Postgres-only: SQLite has no timezone-aware type and stores these as
			// DATETIME text, so the SQLite body is empty and the row is stamped
			// without running anything.
			//
			// The DO block is self-guarding: it alters only columns that are still
			// "timestamp without time zone", so it no-ops on already-converged
			// databases and tolerates an absent table. USING ... AT TIME ZONE UTC
			// is explicit so stored values are read as UTC rather than reinterpreted
			// through the session TimeZone, which would shift every timestamp.
			Version: "20260804_pg_timestamptz_convergence",
			Name:    "Converge pre-2026-05-17 Postgres timestamp columns to TIMESTAMPTZ",
			Postgres: `
				DO $$
				DECLARE
					target record;
				BEGIN
					FOR target IN
						SELECT c.table_name::text AS table_name, c.column_name::text AS column_name
						FROM information_schema.columns c
						WHERE c.table_schema = current_schema()
						  AND c.data_type = 'timestamp without time zone'
						  AND (c.table_name::text, c.column_name::text) IN (
							('action_capabilities','created_at'),
							('action_capabilities','updated_at'),
							('oauth_clients','created_at'),
							('oauth_clients','updated_at'),
							('oauth_authorization_codes','expires_at'),
							('oauth_authorization_codes','consumed_at'),
							('oauth_authorization_codes','created_at'),
							('oauth_refresh_tokens','expires_at'),
							('oauth_refresh_tokens','revoked_at'),
							('oauth_refresh_tokens','created_at'),
							('audit_logs','timestamp'),
							('scheduler_runs','started_at'),
							('scheduler_runs','completed_at'),
							('pending_custom_field_cleanups','created_at'),
							('pending_custom_field_cleanups','started_at'),
							('pending_custom_field_cleanups','completed_at'),
							('user_invitations','expires_at'),
							('user_invitations','used_at'),
							('user_invitations','created_at'),
							('api_tokens','expires_at'),
							('api_tokens','last_used_at'),
							('api_tokens','created_at'),
							('api_tokens','updated_at'),
							('scm_processed_commits','processed_at'),
							('cli_auth_codes','created_at'),
							('cli_auth_codes','expires_at'),
							('cli_auth_codes','consumed_at')
						  )
					LOOP
						EXECUTE format(
							'ALTER TABLE %I ALTER COLUMN %I TYPE TIMESTAMPTZ USING %I AT TIME ZONE ''UTC''',
							target.table_name, target.column_name, target.column_name
						);
					END LOOP;
				END $$;
			`,
		},
		{
			Version:       "drift_email_message_tracking_direction",
			Name:          "email_message_tracking.direction",
			CheckSQLite:   sqliteColumnCheck("email_message_tracking", "direction"),
			CheckPostgres: pgColumnCheck("email_message_tracking", "direction"),
			SQLite:        "ALTER TABLE email_message_tracking ADD COLUMN direction TEXT DEFAULT 'inbound' CHECK(direction IN ('inbound', 'outbound'))",
			Postgres:      "ALTER TABLE email_message_tracking ADD COLUMN direction TEXT DEFAULT 'inbound' CHECK(direction IN ('inbound', 'outbound'))",
		},
		{
			Version: "drift_email_message_tracking_attachments_status_check",
			Name:    "email_message_tracking.attachments_status CHECK",
			CheckSQLite: `SELECT CASE WHEN EXISTS (
				SELECT 1 FROM sqlite_master
				WHERE type='table'
				  AND name='email_message_tracking'
				  AND sql LIKE '%attachments_status TEXT CHECK(attachments_status IN%'
			) THEN 1 ELSE 0 END`,
			CheckPostgres: "SELECT COUNT(*) FROM pg_constraint WHERE conname = 'email_message_tracking_attachments_status_check'",
			SQLite: `
				UPDATE email_message_tracking
				SET attachments_status = NULL
				WHERE attachments_status IS NOT NULL
				  AND attachments_status NOT IN ('ok','partial','failed');

				CREATE TABLE email_message_tracking_new (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					channel_id INTEGER NOT NULL,
					message_id TEXT NOT NULL,
					dedup_key TEXT NOT NULL DEFAULT '',
					in_reply_to TEXT,
					from_email TEXT NOT NULL,
					from_name TEXT,
					subject TEXT,
					item_id INTEGER,
					comment_id INTEGER,
					attachments_status TEXT CHECK(attachments_status IN ('ok','partial','failed') OR attachments_status IS NULL),
					direction TEXT DEFAULT 'inbound' CHECK(direction IN ('inbound', 'outbound')),
					processed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
					FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE,
					FOREIGN KEY (item_id) REFERENCES items(id) ON DELETE SET NULL,
					FOREIGN KEY (comment_id) REFERENCES comments(id) ON DELETE SET NULL
				);
				INSERT INTO email_message_tracking_new
					(id, channel_id, message_id, dedup_key, in_reply_to, from_email, from_name, subject,
					 item_id, comment_id, attachments_status, direction, processed_at)
				SELECT id, channel_id, message_id, dedup_key, in_reply_to, from_email, from_name, subject,
					   item_id, comment_id, attachments_status, COALESCE(direction, 'inbound'), processed_at
				FROM email_message_tracking;
				DROP TABLE email_message_tracking;
				ALTER TABLE email_message_tracking_new RENAME TO email_message_tracking;
				CREATE INDEX IF NOT EXISTS idx_email_message_tracking_channel_id ON email_message_tracking(channel_id);
				CREATE INDEX IF NOT EXISTS idx_email_message_tracking_message_id ON email_message_tracking(message_id);
				CREATE INDEX IF NOT EXISTS idx_email_message_tracking_in_reply_to ON email_message_tracking(in_reply_to);
				CREATE UNIQUE INDEX IF NOT EXISTS idx_email_message_tracking_dedup ON email_message_tracking(channel_id, dedup_key);
			`,
			Postgres: `
				UPDATE email_message_tracking
				SET attachments_status = NULL
				WHERE attachments_status IS NOT NULL
				  AND attachments_status NOT IN ('ok','partial','failed');
				ALTER TABLE email_message_tracking
					ADD CONSTRAINT email_message_tracking_attachments_status_check
					CHECK (attachments_status IN ('ok','partial','failed') OR attachments_status IS NULL);
			`,
		},
		{
			Version: "drift_active_timers_user_id_not_null",
			Name:    "active_timers.user_id NOT NULL",
			CheckSQLite: `SELECT CASE WHEN EXISTS (
				SELECT 1 FROM pragma_table_info('active_timers')
				WHERE name='user_id' AND [notnull] = 1
			) THEN 1 ELSE 0 END`,
			CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='active_timers' AND column_name='user_id' AND is_nullable='NO'",
			SQLite: `
				DELETE FROM active_timers WHERE user_id IS NULL;
				CREATE TABLE active_timers_new (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					workspace_id INTEGER NOT NULL,
					item_id INTEGER,
					project_id INTEGER NOT NULL,
					user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
					description TEXT NOT NULL,
					start_time_utc INTEGER NOT NULL,
					created_at INTEGER NOT NULL,
					FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
					FOREIGN KEY (item_id) REFERENCES items(id) ON DELETE SET NULL,
					FOREIGN KEY (project_id) REFERENCES time_projects(id) ON DELETE CASCADE
				);
				INSERT INTO active_timers_new
					(id, workspace_id, item_id, project_id, user_id, description, start_time_utc, created_at)
				SELECT id, workspace_id, item_id, project_id, user_id, description, start_time_utc, created_at
				FROM active_timers;
				DROP TABLE active_timers;
				ALTER TABLE active_timers_new RENAME TO active_timers;
				CREATE INDEX IF NOT EXISTS idx_active_timers_workspace_id ON active_timers(workspace_id);
				CREATE INDEX IF NOT EXISTS idx_active_timers_item_id ON active_timers(item_id);
				CREATE INDEX IF NOT EXISTS idx_active_timers_project_id ON active_timers(project_id);
				CREATE INDEX IF NOT EXISTS idx_active_timers_user_id ON active_timers(user_id);
			`,
			Postgres: `
				DELETE FROM active_timers WHERE user_id IS NULL;
				ALTER TABLE active_timers ALTER COLUMN user_id SET NOT NULL;
			`,
		},
		{
			Version:       "drift_screen_field_labels_postgres",
			Name:          "screen_fields.labels (screen 1, Postgres)",
			CheckSQLite:   "SELECT COUNT(*) FROM screen_fields WHERE screen_id = 1 AND field_identifier = 'labels'",
			CheckPostgres: "SELECT COUNT(*) FROM screen_fields WHERE screen_id = 1 AND field_identifier = 'labels'",
			SQLite:        "INSERT INTO screen_fields (screen_id, field_type, field_identifier, display_order, is_required, field_width) VALUES (1, 'system', 'labels', 11, false, 'full')",
			Postgres:      "INSERT INTO screen_fields (screen_id, field_type, field_identifier, display_order, is_required, field_width) VALUES (1, 'system', 'labels', 11, false, 'full')",
		},
	}
}

// schemaRerunMigrations are embedded schema files that the legacy code
// re-executes on every existing-install startup. Each schema is fully
// idempotent (CREATE TABLE IF NOT EXISTS, CREATE INDEX IF NOT EXISTS,
// CREATE OR REPLACE FUNCTION, DO $$ guard blocks) so re-running is safe.
//
// Every entry pins a sentinel table name — the first CREATE TABLE in the
// schema file. The Check predicates assert that table is present, so on
// a fresh install (where the same schema is already in DB.Initialize's
// concat string) the migration stamps without re-executing the body.
// schema/*.sql is canonical for fresh installs; this slice covers the
// upgrade path only.
func schemaRerunMigrations() []Migration {
	type rerun struct {
		version  string
		name     string
		sentinel string // first CREATE TABLE name in the schema body
		sqlite   string
		postgres string
	}
	entries := []rerun{
		{"schema_labels", "labels schema", "labels", labelsSchema, labelsSchemaPostgres},
		{"schema_llm", "llm schema", "llm_connections", llmSchema, llmSchemaPostgres},
		{"schema_auth_policy", "auth_policy schema", "auth_policy_audit", authPolicySchema, authPolicySchemaPostgres},
		{"schema_milestones", "milestones schema", "milestone_categories", milestonesSchema, milestonesSchemaPostgres},
		{"schema_channels", "channels schema", "channel_categories", channelsSchema, channelsSchemaPostgres},
		{"schema_assets", "assets schema", "asset_management_sets", assetsSchema, assetsSchemaPostgres},
		{"schema_ldap", "ldap schema", "ldap_configs", ldapSchema, ldapSchemaPostgres},
		{"schema_daily_briefings", "daily_briefings schema", "daily_briefings", dailyBriefingsSchema, dailyBriefingsSchemaPostgres},
		{"schema_asset_actions", "asset_actions schema", "asset_actions", assetActionsSchema, assetActionsSchemaPostgres},
		{"schema_teams", "teams schema", "teams", teamsSchema, teamsSchemaPostgres},
		{"schema_condition_sets", "condition_sets schema", "condition_sets", conditionSetsSchema, conditionSetsSchemaPostgres},
		{"schema_approvals", "approvals schema", "approval_sets", approvalsSchema, approvalsSchemaPostgres},
		{"schema_integrations", "integrations schema", "integration_providers", integrationsSchema, integrationsSchemaPostgres},
		{"schema_actions", "actions schema", "actions", actionsSchema, actionsSchemaPostgres},
		{"schema_scm", "scm schema", "scm_providers", scmSchema, scmSchemaPostgres},
		// Postgres-only — there's no asset_reports.sql for SQLite (it lives
		// in the items.sql / scm.sql composite for SQLite). Empty SQLite
		// body so the runner stamps without doing anything on that backend.
		{"schema_asset_reports_postgres", "asset_reports schema (Postgres)", "asset_reports", "", assetReportsSchemaPostgres},
	}

	out := make([]Migration, 0, len(entries))
	for _, e := range entries {
		migration := Migration{
			Version:       e.version,
			Name:          e.name,
			CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='" + e.sentinel + "'",
			CheckPostgres: "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='" + e.sentinel + "'",
			SQLite:        e.sqlite,
			Postgres:      e.postgres,
		}
		if e.version == "schema_llm" {
			// The base schema gained the token-class columns alongside the
			// append-only migration that converges existing tables. Accept and
			// re-stamp both deployed backend checksums after that migration runs.
			migration.Superseded = []string{
				"ce3303166028080e7d00e27ae22f4da9f6931284d60862030a70ee4cc43bd0f7",
				"2bfa5f49b77f9ba706cfab9ee7917038c9aad8868e3cdf4c819ba7802e4019e8",
			}
		}
		out = append(out, migration)
	}
	return out
}
