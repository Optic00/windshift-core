package database

import (
	"context"
	"fmt"
)

func cutoverMigrations() []Migration {
	return []Migration{
		{
			Version:       "cutover_items_milestone_backfill",
			Name:          "Backfill legacy items.milestone_id into item_milestones",
			CheckSQLite:   "SELECT CASE WHEN EXISTS(SELECT 1 FROM pragma_table_info('items') WHERE name='milestone_id') THEN 0 ELSE 1 END",
			CheckPostgres: "SELECT CASE WHEN EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='items' AND column_name='milestone_id') THEN 0 ELSE 1 END",
			SQLite: `INSERT OR IGNORE INTO item_milestones (item_id, milestone_id, created_at)
				SELECT id, milestone_id, created_at FROM items WHERE milestone_id IS NOT NULL`,
			Postgres: `INSERT INTO item_milestones (item_id, milestone_id, created_at)
				SELECT id, milestone_id, created_at FROM items WHERE milestone_id IS NOT NULL
				ON CONFLICT (item_id, milestone_id) DO NOTHING`,
		},
		{
			Version:       "cutover_items_frac_index_unique",
			Name:          "Make items.frac_index unique after removing duplicates",
			CheckSQLite:   "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_items_frac_index' AND sql LIKE '%UNIQUE%'",
			CheckPostgres: "SELECT COUNT(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname='idx_items_frac_index' AND indexdef LIKE 'CREATE UNIQUE INDEX%'",
			SQLite: `
				UPDATE items SET frac_index = NULL
				WHERE frac_index IS NOT NULL
				  AND id NOT IN (SELECT MIN(id) FROM items WHERE frac_index IS NOT NULL GROUP BY frac_index);
				DROP INDEX IF EXISTS idx_items_frac_index;
				CREATE UNIQUE INDEX idx_items_frac_index ON items(frac_index) WHERE frac_index IS NOT NULL;
			`,
			Postgres: `
				UPDATE items SET frac_index = NULL
				WHERE frac_index IS NOT NULL
				  AND id NOT IN (SELECT MIN(id) FROM items WHERE frac_index IS NOT NULL GROUP BY frac_index);
				DROP INDEX IF EXISTS idx_items_frac_index;
				CREATE UNIQUE INDEX idx_items_frac_index ON items(frac_index) WHERE frac_index IS NOT NULL;
			`,
		},
		{
			Version:       "cutover_approval_set_statuses_rebuild",
			Name:          "Remove legacy approval_set_statuses inline uniqueness",
			CheckSQLite:   "SELECT CASE WHEN EXISTS(SELECT 1 FROM sqlite_master WHERE type='index' AND tbl_name='approval_set_statuses' AND name LIKE 'sqlite_autoindex_approval_set_statuses_%') THEN 0 ELSE 1 END",
			CheckPostgres: "SELECT CASE WHEN EXISTS(SELECT 1 FROM pg_constraint WHERE conname='approval_set_statuses_approval_set_id_status_id_key') THEN 0 ELSE 1 END",
			SQLite:        "custom:sqlite-rebuild-approval-set-statuses-v1",
			Postgres: `
				ALTER TABLE approval_set_statuses DROP CONSTRAINT approval_set_statuses_approval_set_id_status_id_key;
				CREATE UNIQUE INDEX IF NOT EXISTS uq_approval_set_statuses_active
					ON approval_set_statuses(approval_set_id, status_id) WHERE is_active = TRUE;
			`,
			ApplySQLite: rebuildSQLiteApprovalSetStatuses,
		},
		{
			Version:     "cutover_pages_slug_unique_rebuild",
			Name:        "Remove legacy pages inline slug uniqueness",
			CheckSQLite: "SELECT CASE WHEN EXISTS(SELECT 1 FROM sqlite_master WHERE type='index' AND tbl_name='pages' AND name LIKE 'sqlite_autoindex_pages_%') THEN 0 ELSE 1 END",
			SQLite:      "custom:sqlite-rebuild-pages-without-slug-uniqueness-v1",
			ApplySQLite: rebuildSQLitePagesWithoutSlugUniqueness,
		},
		{
			Version:       "cutover_condition_user_source",
			Name:          "Rewrite legacy condition user_source values",
			CheckSQLite:   `SELECT CASE WHEN EXISTS(SELECT 1 FROM conditions WHERE condition_type IN ('user_in_role','user_in_group') AND config LIKE '%"user_source"%') THEN 0 ELSE 1 END`,
			CheckPostgres: `SELECT CASE WHEN EXISTS(SELECT 1 FROM conditions WHERE condition_type IN ('user_in_role','user_in_group') AND config LIKE '%"user_source"%') THEN 0 ELSE 1 END`,
			SQLite:        "custom:rewrite-condition-user-source-v1",
			Postgres:      "custom:rewrite-condition-user-source-v1",
			ApplySQLite:   migrateLegacyConditionSources,
			ApplyPostgres: migrateLegacyConditionSources,
		},
		{
			Version:       "cutover_default_configuration_set",
			Name:          "Create default configuration set on legacy installs",
			CheckSQLite:   "SELECT COUNT(*) FROM configuration_sets",
			CheckPostgres: "SELECT COUNT(*) FROM configuration_sets",
			SQLite:        "custom:create-default-configuration-set-v1",
			Postgres:      "custom:create-default-configuration-set-v1",
			ApplySQLite:   migrateLegacyDefaultConfigurationSet,
			ApplyPostgres: migrateLegacyDefaultConfigurationSet,
		},
		{
			Version:       "cutover_ai_feature_config",
			Name:          "Seed ai_feature_config from legacy ai_chat_enabled",
			CheckSQLite:   "SELECT COUNT(*) FROM system_settings WHERE key='ai_feature_config'",
			CheckPostgres: "SELECT COUNT(*) FROM system_settings WHERE key='ai_feature_config'",
			SQLite: `INSERT INTO system_settings (key, value, value_type, description, category)
				SELECT 'ai_feature_config', CASE WHEN lower(value)='false' THEN '{"ai_chat":{"mode":"disabled","connection_id":0}}' ELSE '{}' END,
					'json', 'Per-feature AI LLM configuration', 'ai'
				FROM system_settings WHERE key='ai_chat_enabled'`,
			Postgres: `INSERT INTO system_settings (key, value, value_type, description, category)
				SELECT 'ai_feature_config', CASE WHEN lower(value)='false' THEN '{"ai_chat":{"mode":"disabled","connection_id":0}}' ELSE '{}' END,
					'json', 'Per-feature AI LLM configuration', 'ai'
				FROM system_settings WHERE key='ai_chat_enabled'`,
		},
		{
			Version:       "cutover_ssh_fingerprint_padding",
			Name:          "Strip legacy SSH fingerprint padding",
			CheckSQLite:   "SELECT CASE WHEN EXISTS(SELECT 1 FROM user_credentials WHERE public_key_fingerprint LIKE '%=') THEN 0 ELSE 1 END",
			CheckPostgres: "SELECT CASE WHEN EXISTS(SELECT 1 FROM user_credentials WHERE public_key_fingerprint LIKE '%=') THEN 0 ELSE 1 END",
			SQLite:        "UPDATE user_credentials SET public_key_fingerprint = rtrim(public_key_fingerprint, '=') WHERE public_key_fingerprint LIKE '%='",
			Postgres:      "UPDATE user_credentials SET public_key_fingerprint = rtrim(public_key_fingerprint, '=') WHERE public_key_fingerprint LIKE '%='",
		},
		{
			Version: "cutover_knowledge_legacy_cleanup",
			Name:    "Remove abandoned knowledge vector tables and settings",
			CheckSQLite: `SELECT CASE WHEN
				EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name IN ('page_chunk_embeddings','page_attachments'))
				OR EXISTS(SELECT 1 FROM system_settings WHERE key IN ('knowledge.vector_search_enabled','knowledge.embedding_model','knowledge.embedding_connection_id','knowledge.embedding_dimensions'))
				THEN 0 ELSE 1 END`,
			CheckPostgres: `SELECT CASE WHEN
				EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('page_chunk_embeddings','page_attachments'))
				OR EXISTS(SELECT 1 FROM system_settings WHERE key IN ('knowledge.vector_search_enabled','knowledge.embedding_model','knowledge.embedding_connection_id','knowledge.embedding_dimensions'))
				THEN 0 ELSE 1 END`,
			SQLite: `
				DROP TABLE IF EXISTS page_chunk_embeddings;
				DROP TABLE IF EXISTS page_attachments;
				DELETE FROM system_settings WHERE key IN ('knowledge.vector_search_enabled','knowledge.embedding_model','knowledge.embedding_connection_id','knowledge.embedding_dimensions');
			`,
			Postgres: `
				DROP TABLE IF EXISTS page_chunk_embeddings;
				DROP TABLE IF EXISTS page_attachments;
				DELETE FROM system_settings WHERE key IN ('knowledge.vector_search_enabled','knowledge.embedding_model','knowledge.embedding_connection_id','knowledge.embedding_dimensions');
			`,
		},
		{
			Version:       "cutover_sqlite_datetime_format",
			Name:          "Normalize legacy SQLite datetime values",
			CheckSQLite:   "SELECT 1",
			CheckSQLiteFn: sqliteDatetimeMigrationApplied,
			SQLite:        "custom:normalize-sqlite-datetime-format-v1",
			ApplySQLite:   migrateLegacySQLiteDatetimes,
		},
		{
			Version:       "cutover_postgres_timestamp_timezone",
			Name:          "Normalize legacy PostgreSQL timestamps to timestamptz",
			CheckPostgres: "SELECT CASE WHEN EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND data_type='timestamp without time zone') THEN 0 ELSE 1 END",
			Postgres:      "custom:normalize-postgres-timestamp-timezone-v1",
			ApplyPostgres: migrateLegacyPostgresTimestamps,
		},
	}
}

func migrateLegacyConditionSources(db Database) error {
	return migrateConditionUserSourceToFieldRef(db)
}

func migrateLegacyDefaultConfigurationSet(db Database) error {
	switch typed := db.(type) {
	case *SQLiteDB:
		return typed.migrateDefaultConfigurationSet()
	case *PostgresDB:
		return typed.migrateDefaultConfigurationSet()
	default:
		return fmt.Errorf("unsupported database %T", db)
	}
}

func migrateLegacySQLiteDatetimes(db Database) error {
	typed, ok := db.(*SQLiteDB)
	if !ok {
		return fmt.Errorf("SQLite datetime migration received %T", db)
	}
	return backfillLegacyDatetimeFormat(typed.writeConn)
}

func sqliteDatetimeMigrationApplied(db Database) (bool, error) {
	var users int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&users); err != nil {
		return false, err
	}
	// Fresh installs have no users while the catalog is stamped. Existing
	// installations with user data run the one-time, all-column normalizer.
	return users == 0, nil
}

func migrateLegacyPostgresTimestamps(db Database) error {
	typed, ok := db.(*PostgresDB)
	if !ok {
		return fmt.Errorf("postgres timestamp migration received %T", db)
	}
	return backfillPostgresTimestampTZ(typed.db)
}

func withSQLiteForeignKeysDisabled(db Database, name string, steps []string) (err error) {
	typed, ok := db.(*SQLiteDB)
	if !ok {
		return fmt.Errorf("%s: SQLite rebuild received %T", name, db)
	}

	ctx := context.Background()
	conn, err := typed.writeConn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("%s: acquire write connection: %w", name, err)
	}
	defer func() { _ = conn.Close() }()

	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("%s: disable foreign keys: %w", name, err)
	}
	defer func() {
		if _, enableErr := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err == nil && enableErr != nil {
			err = fmt.Errorf("%s: re-enable foreign keys: %w", name, enableErr)
		}
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: begin rebuild: %w", name, err)
	}
	defer func() { _ = tx.Rollback() }()

	for i, statement := range steps {
		if _, execErr := tx.ExecContext(ctx, statement); execErr != nil {
			return fmt.Errorf("%s step %d: %w", name, i+1, execErr)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: commit rebuild: %w", name, err)
	}
	return nil
}

func rebuildSQLiteApprovalSetStatuses(db Database) error {
	return withSQLiteForeignKeysDisabled(db, "rebuild approval_set_statuses", []string{
		`CREATE TABLE approval_set_statuses_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			approval_set_id INTEGER NOT NULL,
			status_id INTEGER NOT NULL,
			approve_transition_id INTEGER NOT NULL,
			deny_transition_id INTEGER NOT NULL,
			step_mode TEXT NOT NULL DEFAULT 'sequential',
			is_active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (approval_set_id) REFERENCES approval_sets(id) ON DELETE CASCADE,
			FOREIGN KEY (status_id) REFERENCES statuses(id) ON DELETE CASCADE,
			FOREIGN KEY (approve_transition_id) REFERENCES workflow_transitions(id) ON DELETE CASCADE,
			FOREIGN KEY (deny_transition_id) REFERENCES workflow_transitions(id) ON DELETE CASCADE
		)`,
		`INSERT INTO approval_set_statuses_new (id, approval_set_id, status_id, approve_transition_id, deny_transition_id, step_mode, is_active, created_at)
		 SELECT id, approval_set_id, status_id, approve_transition_id, deny_transition_id, step_mode, COALESCE(is_active, 1), created_at FROM approval_set_statuses`,
		`DROP TABLE approval_set_statuses`,
		`ALTER TABLE approval_set_statuses_new RENAME TO approval_set_statuses`,
		`CREATE UNIQUE INDEX uq_approval_set_statuses_active
			ON approval_set_statuses(approval_set_id, status_id) WHERE is_active = TRUE`,
	})
}

func rebuildSQLitePagesWithoutSlugUniqueness(db Database) error {
	return withSQLiteForeignKeysDisabled(db, "rebuild pages", []string{
		`CREATE TABLE pages_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace_id INTEGER NOT NULL,
			parent_id INTEGER,
			title TEXT NOT NULL,
			slug TEXT NOT NULL,
			metadata TEXT NOT NULL DEFAULT '{}',
			content TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL DEFAULT '',
			excerpt TEXT NOT NULL DEFAULT '',
			created_by INTEGER NOT NULL,
			updated_by INTEGER,
			archived_by INTEGER,
			is_home BOOLEAN NOT NULL DEFAULT FALSE,
			inherit_permissions BOOLEAN NOT NULL DEFAULT TRUE,
			rank TEXT,
			frac_index TEXT COLLATE BINARY,
			path TEXT NOT NULL DEFAULT '/',
			depth INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			archived_at DATETIME,
			FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
			FOREIGN KEY (parent_id) REFERENCES pages(id) ON DELETE CASCADE,
			FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE RESTRICT,
			FOREIGN KEY (updated_by) REFERENCES users(id) ON DELETE SET NULL,
			FOREIGN KEY (archived_by) REFERENCES users(id) ON DELETE SET NULL
		)`,
		`INSERT INTO pages_new (
			id, workspace_id, parent_id, title, slug, metadata, content, content_hash,
			excerpt, created_by, updated_by, archived_by, is_home, inherit_permissions,
			rank, frac_index, path, depth, created_at, updated_at, archived_at
		) SELECT
			id, workspace_id, parent_id, title, slug, metadata, content, content_hash,
			excerpt, created_by, updated_by, archived_by, is_home, inherit_permissions,
			rank, frac_index, path, depth, created_at, updated_at, archived_at FROM pages`,
		`DROP TABLE pages`,
		`ALTER TABLE pages_new RENAME TO pages`,
		`CREATE INDEX IF NOT EXISTS idx_pages_workspace ON pages(workspace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_pages_parent ON pages(parent_id)`,
		`CREATE INDEX IF NOT EXISTS idx_pages_workspace_parent ON pages(workspace_id, parent_id)`,
		`CREATE INDEX IF NOT EXISTS idx_pages_workspace_archived ON pages(workspace_id, archived_at)`,
		`CREATE INDEX IF NOT EXISTS idx_pages_path ON pages(path)`,
		`CREATE INDEX IF NOT EXISTS idx_pages_content_hash ON pages(content_hash) WHERE content_hash != ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_pages_workspace_home ON pages(workspace_id) WHERE is_home = TRUE AND archived_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_pages_workspace_parent_rank ON pages(workspace_id, parent_id, rank) WHERE rank IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_pages_frac_index_scoped
			ON pages(workspace_id, COALESCE(parent_id, -1), frac_index)
			WHERE frac_index IS NOT NULL AND archived_at IS NULL`,
	})
}
