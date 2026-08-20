package database

import (
	"strings"
)

// canonicalFracIndexSQLiteCheck verifies the canonical items.frac_index
// shape on SQLite: the column is NOT NULL and the three rank indexes exist
// unfiltered, with idx_items_frac_index unique. Startup validation calls
// this through canonicalFracIndexSchemaCheck in schema_checkpoint.go.
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

// canonicalFracIndexPostgresCheck verifies the canonical items.frac_index
// shape on PostgreSQL. See canonicalFracIndexSQLiteCheck.
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
