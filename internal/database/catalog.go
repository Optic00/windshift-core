package database

import "fmt"

// Shared check builders used by the migrations in the compact catalog. They
// build the canonical "effect already present" queries the runner uses to
// stamp a migration without re-running its DDL.

// sqliteColumnCheck and pgColumnCheck build the canonical "column exists"
// query used by column-add migrations.
func sqliteColumnCheck(table, column string) string {
	return fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name='%s'", table, column)
}

func pgColumnCheck(table, column string) string {
	return fmt.Sprintf(
		"SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='%s' AND column_name='%s'",
		table, column,
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
