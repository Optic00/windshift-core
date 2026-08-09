package database

import (
	"fmt"
	"strings"
)

// canonicalRankSchemaMigrations is intentionally appended after the legacy
// catalog and cutover migrations. Some older cutovers repair duplicate ranks
// by clearing them temporarily; the canonical constraint is installed only
// after those repairs have completed.
func canonicalRankSchemaMigrations() []Migration {
	return []Migration{
		{
			Version: "20260807_items_frac_index_not_null",
			Name:    "Enforce canonical non-null item ranks",
			CheckSQLite: `SELECT CASE WHEN
				COALESCE((SELECT "notnull" FROM pragma_table_info('items') WHERE name = 'frac_index'), 0) = 1
				AND (SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_items_frac_index' AND lower(COALESCE(sql, '')) LIKE '%create unique index%' AND lower(COALESCE(sql, '')) NOT LIKE '% where %') = 1
				AND (SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_items_workspace_frac_index' AND lower(COALESCE(sql, '')) NOT LIKE '% where %') = 1
				AND (SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_items_workspace_parent_frac_index' AND lower(COALESCE(sql, '')) NOT LIKE '% where %') = 1
				THEN 1 ELSE 0 END`,
			CheckPostgres: `SELECT CASE WHEN
				(SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'items' AND column_name = 'frac_index' AND is_nullable = 'NO') = 1
				AND (SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'idx_items_frac_index' AND lower(indexdef) LIKE '%create unique index%' AND lower(indexdef) NOT LIKE '% where %') = 1
				AND (SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'idx_items_workspace_frac_index' AND lower(indexdef) NOT LIKE '% where %') = 1
				AND (SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'idx_items_workspace_parent_frac_index' AND lower(indexdef) NOT LIKE '% where %') = 1
				THEN 1 ELSE 0 END`,
			CheckSQLiteFn:   canonicalFracIndexSQLiteCheck,
			CheckPostgresFn: canonicalFracIndexPostgresCheck,
			SQLite:          "custom:sqlite-rebuild-items-frac-index-not-null-v1",
			Postgres: `
				ALTER TABLE items ALTER COLUMN frac_index SET NOT NULL;
				DROP INDEX IF EXISTS idx_items_frac_index;
				DROP INDEX IF EXISTS idx_items_workspace_frac_index;
				DROP INDEX IF EXISTS idx_items_workspace_parent_frac_index;
				CREATE UNIQUE INDEX idx_items_frac_index ON items(frac_index);
				CREATE INDEX idx_items_workspace_frac_index ON items(workspace_id, frac_index);
				CREATE INDEX idx_items_workspace_parent_frac_index ON items(workspace_id, parent_id, frac_index);
			`,
			ApplySQLite:   rebuildSQLiteItemsWithCanonicalFracIndex,
			ApplyPostgres: enforcePostgresCanonicalFracIndex,
		},
	}
}

func canonicalFracIndexSQLiteCheck(db Database) (bool, error) {
	var notNull int
	if err := db.QueryRow(`
		SELECT COALESCE((SELECT "notnull" FROM pragma_table_info('items') WHERE name = 'frac_index'), 0)
	`).Scan(&notNull); err != nil {
		return false, err
	}
	if notNull != 1 {
		return false, nil
	}

	for indexName, unique := range map[string]bool{
		"idx_items_frac_index":                  true,
		"idx_items_workspace_frac_index":        false,
		"idx_items_workspace_parent_frac_index": false,
	} {
		var count int
		var definition string
		if err := db.QueryRow("SELECT COUNT(*), COALESCE(MAX(sql), '') FROM sqlite_master WHERE type = 'index' AND name = ?", indexName).Scan(&count, &definition); err != nil {
			return false, err
		}
		if count != 1 {
			return false, nil
		}
		definition = strings.ToLower(definition)
		if definition == "" || strings.Contains(definition, " where ") {
			return false, nil
		}
		if unique != strings.Contains(definition, "create unique index") {
			return false, nil
		}
	}
	return true, nil
}

func canonicalFracIndexPostgresCheck(db Database) (bool, error) {
	var nullable string
	if err := db.QueryRow(`
		SELECT is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'items'
		  AND column_name = 'frac_index'
	`).Scan(&nullable); err != nil {
		return false, err
	}
	if nullable != "NO" {
		return false, nil
	}

	for indexName, unique := range map[string]bool{
		"idx_items_frac_index":                  true,
		"idx_items_workspace_frac_index":        false,
		"idx_items_workspace_parent_frac_index": false,
	} {
		var count int
		var definition string
		if err := db.QueryRow(`
			SELECT COUNT(*), COALESCE(MAX(indexdef), '')
			FROM pg_indexes
			WHERE schemaname = current_schema() AND indexname = ?
		`, indexName).Scan(&count, &definition); err != nil {
			return false, err
		}
		if count != 1 {
			return false, nil
		}
		definition = strings.ToLower(definition)
		if definition == "" || strings.Contains(definition, " where ") {
			return false, nil
		}
		if unique != strings.Contains(definition, "create unique index") {
			return false, nil
		}
	}
	return true, nil
}

func enforcePostgresCanonicalFracIndex(db Database) error {
	return WithTx(db, func(tx Tx) error {
		var nullCount int
		if err := tx.QueryRow("SELECT COUNT(*) FROM items WHERE frac_index IS NULL").Scan(&nullCount); err != nil {
			return fmt.Errorf("check item ranks before NOT NULL migration: %w", err)
		}
		if nullCount != 0 {
			return fmt.Errorf("cannot enforce items.frac_index NOT NULL: %d rows are NULL", nullCount)
		}
		if _, err := tx.Exec("ALTER TABLE items ALTER COLUMN frac_index SET NOT NULL"); err != nil {
			return fmt.Errorf("set items.frac_index NOT NULL: %w", err)
		}
		for _, statement := range []string{
			"DROP INDEX IF EXISTS idx_items_frac_index",
			"DROP INDEX IF EXISTS idx_items_workspace_frac_index",
			"DROP INDEX IF EXISTS idx_items_workspace_parent_frac_index",
			"CREATE UNIQUE INDEX idx_items_frac_index ON items(frac_index)",
			"CREATE INDEX idx_items_workspace_frac_index ON items(workspace_id, frac_index)",
			"CREATE INDEX idx_items_workspace_parent_frac_index ON items(workspace_id, parent_id, frac_index)",
		} {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply canonical item rank index %q: %w", statement, err)
			}
		}
		return nil
	})
}

func rebuildSQLiteItemsWithCanonicalFracIndex(db Database) error {
	var nullCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM items WHERE frac_index IS NULL").Scan(&nullCount); err != nil {
		return fmt.Errorf("check item ranks before SQLite rebuild: %w", err)
	}
	if nullCount != 0 {
		return fmt.Errorf("cannot rebuild items with canonical NOT NULL rank: %d rows are NULL", nullCount)
	}

	return withSQLiteForeignKeysDisabled(db, "rebuild items with canonical frac_index", []string{
		`CREATE TABLE items_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace_id INTEGER NOT NULL,
			workspace_item_number INTEGER NOT NULL DEFAULT 0,
			item_type_id INTEGER,
			title TEXT NOT NULL,
			description TEXT,
			is_task BOOLEAN DEFAULT false,
			iteration_id INTEGER,
			time_project_id INTEGER REFERENCES time_projects(id) ON DELETE SET NULL,
			project_id INTEGER REFERENCES time_projects(id) ON DELETE SET NULL,
			inherit_project BOOLEAN DEFAULT 0,
			assignee_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
			creator_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
			reporter_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
			creator_portal_customer_id INTEGER REFERENCES portal_customers(id) ON DELETE SET NULL,
			custom_field_values TEXT,
			virtual_field_data TEXT,
			calendar_data TEXT,
			parent_id INTEGER,
			path TEXT DEFAULT '/',
			related_work_item_id INTEGER REFERENCES items(id) ON DELETE SET NULL,
			story_points REAL,
			estimate_minutes INTEGER,
			rank TEXT,
			frac_index TEXT COLLATE BINARY NOT NULL DEFAULT ('0|a1' || lower(hex(randomblob(16))) || '1'),
			status_id INTEGER REFERENCES statuses(id) ON DELETE RESTRICT,
			channel_id INTEGER REFERENCES channels(id) ON DELETE SET NULL,
			request_type_id INTEGER REFERENCES request_types(id) ON DELETE SET NULL,
			priority_id INTEGER REFERENCES priorities(id) ON DELETE SET NULL,
			due_date DATE,
			start_date DATE,
			end_date DATE,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_active_at DATETIME,
			FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
			FOREIGN KEY (item_type_id) REFERENCES item_types(id) ON DELETE SET NULL,
			FOREIGN KEY (parent_id) REFERENCES items(id) ON DELETE CASCADE,
			FOREIGN KEY (iteration_id) REFERENCES iterations(id) ON DELETE SET NULL,
			FOREIGN KEY (time_project_id) REFERENCES time_projects(id) ON DELETE SET NULL,
			FOREIGN KEY (assignee_id) REFERENCES users(id) ON DELETE SET NULL,
			FOREIGN KEY (creator_id) REFERENCES users(id) ON DELETE SET NULL,
			FOREIGN KEY (reporter_id) REFERENCES users(id) ON DELETE SET NULL,
			FOREIGN KEY (creator_portal_customer_id) REFERENCES portal_customers(id) ON DELETE SET NULL,
			UNIQUE(workspace_id, workspace_item_number)
		)`,
		`INSERT INTO items_new (
			id, workspace_id, workspace_item_number, item_type_id, title, description,
			is_task, iteration_id, time_project_id, project_id, inherit_project,
			assignee_id, creator_id, reporter_id, creator_portal_customer_id,
			custom_field_values, virtual_field_data, calendar_data, parent_id, path,
			related_work_item_id, story_points, estimate_minutes, rank, frac_index,
			status_id, channel_id, request_type_id, priority_id, due_date, start_date,
			end_date, created_at, updated_at, last_active_at
		) SELECT
			id, workspace_id, workspace_item_number, item_type_id, title, description,
			is_task, iteration_id, time_project_id, project_id, inherit_project,
			assignee_id, creator_id, reporter_id, creator_portal_customer_id,
			custom_field_values, virtual_field_data, calendar_data, parent_id, path,
			related_work_item_id, story_points, estimate_minutes, rank, frac_index,
			status_id, channel_id, request_type_id, priority_id, due_date, start_date,
			end_date, created_at, updated_at, last_active_at
		FROM items`,
		`DROP TABLE items`,
		`ALTER TABLE items_new RENAME TO items`,
		`CREATE INDEX idx_items_workspace_id ON items(workspace_id)`,
		`CREATE INDEX idx_items_workspace_item_number ON items(workspace_id, workspace_item_number)`,
		`CREATE UNIQUE INDEX idx_items_workspace_item_number_unique ON items(workspace_id, workspace_item_number)`,
		`CREATE INDEX idx_items_item_type_id ON items(item_type_id)`,
		`CREATE INDEX idx_items_status_id ON items(status_id)`,
		`CREATE INDEX idx_items_priority_id ON items(priority_id)`,
		`CREATE INDEX idx_items_is_task ON items(is_task)`,
		`CREATE INDEX idx_items_due_date ON items(due_date) WHERE due_date IS NOT NULL`,
		`CREATE INDEX idx_items_iteration_id ON items(iteration_id)`,
		`CREATE INDEX idx_items_assignee_id ON items(assignee_id)`,
		`CREATE INDEX idx_items_creator_id ON items(creator_id)`,
		`CREATE INDEX idx_items_reporter_id ON items(reporter_id)`,
		`CREATE INDEX idx_items_creator_portal_customer_id ON items(creator_portal_customer_id)`,
		`CREATE INDEX idx_items_workspace_last_active ON items(workspace_id, last_active_at)`,
		`CREATE INDEX idx_items_time_project_id ON items(time_project_id)`,
		`CREATE INDEX idx_items_project_id ON items(project_id)`,
		`CREATE INDEX idx_items_parent_id ON items(parent_id)`,
		`CREATE INDEX idx_items_path ON items(path)`,
		`CREATE INDEX idx_items_workspace_parent ON items(workspace_id, parent_id)`,
		`CREATE INDEX idx_items_rank ON items(rank) WHERE rank IS NOT NULL`,
		`CREATE INDEX idx_items_workspace_rank ON items(workspace_id, rank) WHERE rank IS NOT NULL`,
		`CREATE INDEX idx_items_workspace_parent_rank ON items(workspace_id, parent_id, rank) WHERE rank IS NOT NULL`,
		`CREATE UNIQUE INDEX idx_items_frac_index ON items(frac_index)`,
		`CREATE INDEX idx_items_workspace_frac_index ON items(workspace_id, frac_index)`,
		`CREATE INDEX idx_items_workspace_parent_frac_index ON items(workspace_id, parent_id, frac_index)`,
		`CREATE INDEX idx_items_channel_id ON items(channel_id)`,
		`CREATE INDEX idx_items_request_type_id ON items(request_type_id)`,
		`CREATE INDEX idx_items_related_work_item_id ON items(related_work_item_id)`,
		`CREATE TRIGGER trg_items_change_insert AFTER INSERT ON items
		BEGIN
			INSERT INTO item_change_log(item_id, workspace_id, change_type) VALUES (NEW.id, NEW.workspace_id, 'upsert');
		END`,
		`CREATE TRIGGER trg_items_change_update AFTER UPDATE ON items
		BEGIN
			INSERT INTO item_change_log(item_id, workspace_id, change_type)
			SELECT OLD.id, OLD.workspace_id, 'delete' WHERE OLD.workspace_id <> NEW.workspace_id;
			INSERT INTO item_change_log(item_id, workspace_id, change_type) VALUES (NEW.id, NEW.workspace_id, 'upsert');
		END`,
		`CREATE TRIGGER trg_items_change_delete BEFORE DELETE ON items
		BEGIN
			INSERT INTO item_change_log(item_id, workspace_id, change_type) VALUES (OLD.id, OLD.workspace_id, 'delete');
		END`,
	})
}
