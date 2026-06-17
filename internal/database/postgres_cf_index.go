package database

import (
	"fmt"
	"strconv"
)

// BuildPostgresCustomFieldIndexSQL generates the CREATE INDEX SQL for a custom
// field expression index on Postgres. The index extracts the field's value from
// the custom_field_values JSON and casts it per the field type so equality and
// range queries on the field can use the index.
//
// When concurrently is true it emits CREATE INDEX CONCURRENTLY — non-blocking
// on large tables but it must run outside a transaction. The async index-build
// worker (CFVCleanupScheduler) uses the concurrent form so the build never
// blocks an admin request or holds a write lock on items/assets (WI-416).
//
// Only number, date, and text fields are indexable (indexableFieldTypes in the
// handler); any other fieldType falls back to the text expression.
func BuildPostgresCustomFieldIndexSQL(fieldID int, fieldType, targetTable, indexName string, concurrently bool) string {
	create := "CREATE INDEX"
	if concurrently {
		create = "CREATE INDEX CONCURRENTLY"
	}
	fieldIDStr := strconv.Itoa(fieldID)
	switch fieldType {
	case "number":
		return fmt.Sprintf(`%s %s ON %s(CAST(custom_field_values->>'%s' AS NUMERIC))`,
			create, indexName, targetTable, fieldIDStr)
	case "date":
		return fmt.Sprintf(`%s %s ON %s(CAST(custom_field_values->>'%s' AS TEXT))`,
			create, indexName, targetTable, fieldIDStr)
	default: // text
		return fmt.Sprintf(`%s %s ON %s((custom_field_values->>'%s'))`,
			create, indexName, targetTable, fieldIDStr)
	}
}
