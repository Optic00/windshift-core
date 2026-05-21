package database

import (
	"fmt"
	"testing"
)

// TestPagesSchemaSeedsOnFreshInstall verifies that initializing a fresh
// SQLite database creates the knowledge-pages tables and seeds the
// page.* permission rows, role grants, and knowledge.* system settings.
func TestPagesSchemaSeedsOnFreshInstall(t *testing.T) {
	dsn := fmt.Sprintf("file:%s/test.db?mode=memory&cache=shared", t.TempDir())
	db, err := NewSQLiteDBWithPoolSizes(dsn, 4, 1)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	expectedTables := []string{
		"pages",
		"page_revisions",
		"page_permissions",
		"page_chunks",
	}
	// Tables we intentionally do NOT ship: the polymorphic attachments
	// table covers page attachments, and vector search is not supported.
	removedTables := []string{"page_attachments", "page_chunk_embeddings"}
	for _, name := range expectedTables {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n); err != nil {
			t.Fatalf("query table %s: %v", name, err)
		}
		if n != 1 {
			t.Errorf("expected table %s to exist, found %d rows in sqlite_master", name, n)
		}
	}
	for _, name := range removedTables {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n); err != nil {
			t.Fatalf("query removed table %s: %v", name, err)
		}
		if n != 0 {
			t.Errorf("table %s should not be created on a fresh install (vector / dedicated attachment artifacts were removed)", name)
		}
	}

	expectedPerms := []string{"page.view", "page.create", "page.edit", "page.delete", "page.admin"}
	for _, key := range expectedPerms {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM permissions WHERE permission_key=?", key).Scan(&n); err != nil {
			t.Fatalf("query permission %s: %v", key, err)
		}
		if n != 1 {
			t.Errorf("expected permission %s to be seeded, count=%d", key, n)
		}
	}

	// Role grants: Viewer→page.view; Editor→view/create/edit; Admin→all 5.
	roleGrants := map[string]int{
		"Viewer":        1,
		"Editor":        3,
		"Administrator": 5,
	}
	for role, want := range roleGrants {
		var got int
		if err := db.QueryRow(`
			SELECT COUNT(*) FROM role_permissions rp
			JOIN workspace_roles wr ON wr.id = rp.role_id
			JOIN permissions p ON p.id = rp.permission_id
			WHERE wr.name = ? AND p.permission_key LIKE 'page.%'`, role).Scan(&got); err != nil {
			t.Fatalf("role grants for %s: %v", role, err)
		}
		if got != want {
			t.Errorf("role %s: expected %d page.* grants, got %d", role, want, got)
		}
	}

	expectedSettings := []string{
		"knowledge.full_text_search_enabled",
	}
	for _, key := range expectedSettings {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM system_settings WHERE key=?", key).Scan(&n); err != nil {
			t.Fatalf("query setting %s: %v", key, err)
		}
		if n != 1 {
			t.Errorf("expected system setting %s to be seeded, count=%d", key, n)
		}
	}
	// Vector-search settings must not be seeded.
	for _, key := range []string{
		"knowledge.vector_search_enabled",
		"knowledge.embedding_model",
		"knowledge.embedding_connection_id",
		"knowledge.embedding_dimensions",
	} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM system_settings WHERE key=?", key).Scan(&n); err != nil {
			t.Fatalf("query removed setting %s: %v", key, err)
		}
		if n != 0 {
			t.Errorf("vector setting %s should not be seeded on a fresh install", key)
		}
	}
}

// TestPagesSchemaCleanupDropsLegacyArtifacts simulates an upgrade from an
// install that ran the original Slice 1 schema (page_attachments +
// page_chunk_embeddings + vector knowledge.* settings) and verifies the
// cleanup migration removes them.
func TestPagesSchemaCleanupDropsLegacyArtifacts(t *testing.T) {
	dsn := fmt.Sprintf("file:%s/test.db?mode=memory&cache=shared", t.TempDir())
	db, err := NewSQLiteDBWithPoolSizes(dsn, 4, 1)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// First-pass initialize creates the current (post-cleanup) schema.
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	// Manually re-create the legacy artifacts that an older deploy would
	// still have on disk: the dedicated page_attachments + the embeddings
	// table + the four vector settings.
	for _, stmt := range []string{
		`CREATE TABLE page_attachments (id INTEGER PRIMARY KEY, page_id INTEGER, workspace_id INTEGER)`,
		`CREATE TABLE page_chunk_embeddings (id INTEGER PRIMARY KEY, chunk_id INTEGER)`,
		`INSERT INTO system_settings (key, value, value_type, description, category)
		 VALUES
		   ('knowledge.vector_search_enabled', 'false', 'boolean', 'legacy', 'knowledge'),
		   ('knowledge.embedding_model', '', 'string', 'legacy', 'knowledge'),
		   ('knowledge.embedding_connection_id', '', 'integer', 'legacy', 'knowledge'),
		   ('knowledge.embedding_dimensions', '1536', 'integer', 'legacy', 'knowledge')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed legacy artifact %q: %v", stmt, err)
		}
	}

	// Initialize again — this is the path an existing install hits on the
	// next server start; the migration block must drop the legacy rows.
	if err := db.Initialize(); err != nil {
		t.Fatalf("re-initialize: %v", err)
	}

	for _, name := range []string{"page_attachments", "page_chunk_embeddings"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n); err != nil {
			t.Fatalf("query %s: %v", name, err)
		}
		if n != 0 {
			t.Errorf("legacy table %s should have been dropped by the cleanup migration", name)
		}
	}
	for _, key := range []string{
		"knowledge.vector_search_enabled",
		"knowledge.embedding_model",
		"knowledge.embedding_connection_id",
		"knowledge.embedding_dimensions",
	} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM system_settings WHERE key=?", key).Scan(&n); err != nil {
			t.Fatalf("query setting %s: %v", key, err)
		}
		if n != 0 {
			t.Errorf("legacy setting %s should have been deleted", key)
		}
	}
}

// TestPagesSchemaIdempotentReRun re-applies the pages schema against an
// already-initialized DB to confirm CREATE TABLE / INSERT OR IGNORE
// statements do not duplicate rows or fail.
func TestPagesSchemaIdempotentReRun(t *testing.T) {
	dsn := fmt.Sprintf("file:%s/test.db?mode=memory&cache=shared", t.TempDir())
	db, err := NewSQLiteDBWithPoolSizes(dsn, 4, 1)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	beforePerms := countRows(t, db, "SELECT COUNT(*) FROM permissions WHERE permission_key LIKE 'page.%'")
	beforeGrants := countRows(t, db, `
		SELECT COUNT(*) FROM role_permissions rp
		JOIN permissions p ON p.id = rp.permission_id
		WHERE p.permission_key LIKE 'page.%'`)
	beforeSettings := countRows(t, db, "SELECT COUNT(*) FROM system_settings WHERE key LIKE 'knowledge.%'")

	if _, err := db.Exec(pagesSchema); err != nil {
		t.Fatalf("re-exec pages schema: %v", err)
	}

	if got := countRows(t, db, "SELECT COUNT(*) FROM permissions WHERE permission_key LIKE 'page.%'"); got != beforePerms {
		t.Errorf("permissions duplicated on re-run: before=%d after=%d", beforePerms, got)
	}
	if got := countRows(t, db, `
		SELECT COUNT(*) FROM role_permissions rp
		JOIN permissions p ON p.id = rp.permission_id
		WHERE p.permission_key LIKE 'page.%'`); got != beforeGrants {
		t.Errorf("role grants duplicated on re-run: before=%d after=%d", beforeGrants, got)
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM system_settings WHERE key LIKE 'knowledge.%'"); got != beforeSettings {
		t.Errorf("system settings duplicated on re-run: before=%d after=%d", beforeSettings, got)
	}
}
