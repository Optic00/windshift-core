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
		"page_attachments",
		"page_chunks",
		"page_chunk_embeddings",
	}
	for _, name := range expectedTables {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n); err != nil {
			t.Fatalf("query table %s: %v", name, err)
		}
		if n != 1 {
			t.Errorf("expected table %s to exist, found %d rows in sqlite_master", name, n)
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
		"knowledge.vector_search_enabled",
		"knowledge.embedding_model",
		"knowledge.embedding_connection_id",
		"knowledge.embedding_dimensions",
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
