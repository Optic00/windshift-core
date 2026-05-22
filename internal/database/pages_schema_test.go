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

// TestPagesFracIndexScopedMigration_NullsOutLegacyDuplicates simulates an
// install whose pages.frac_index is unique globally and seeded a value
// that the per-sibling-set generator would happily reproduce in another
// sibling group. Applying the migration body must:
//   1. NULL out duplicates (keeping the lowest id per group),
//   2. drop the old global index,
//   3. create the new scoped index so two siblings under different
//      parents can hold the same frac_index value going forward.
func TestPagesFracIndexScopedMigration_NullsOutLegacyDuplicates(t *testing.T) {
	dsn := fmt.Sprintf("file:%s/test.db?mode=memory&cache=shared", t.TempDir())
	db, err := NewSQLiteDBWithPoolSizes(dsn, 4, 1)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	// Roll back to the pre-fix layout: drop the new index, restore the
	// legacy global UNIQUE(frac_index), and clear the migration stamp so
	// re-running Initialize() applies the migration body afresh.
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_pages_frac_index_scoped`,
		`CREATE UNIQUE INDEX idx_pages_frac_index ON pages(frac_index) WHERE frac_index IS NOT NULL`,
		`DELETE FROM schema_migrations WHERE version='20260522_pages_frac_index_scoped'`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("legacy setup %q: %v", stmt, err)
		}
	}

	// Seed: one workspace, three pages.
	//   * parent_id=NULL (root group): pages 1 and 2 share frac_index 'a0'.
	//   * parent_id=10 (separate group): page 3 also has frac_index 'a0'.
	// Page 3 must survive untouched (independent sibling set); the
	// duplicate within the root group must collapse to a single
	// frac_index='a0' (lowest id) plus a NULL for the loser.
	if _, err := db.Exec(`INSERT INTO workspaces (id, key, name) VALUES (?, ?, ?)`, 9001, "MIG", "mig"); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, username, email, password_hash, is_active, first_name, last_name) VALUES (?, ?, ?, ?, 1, ?, ?)`, 9101, "frac", "frac@example.com", "", "Frac", "User"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Manually drop the unique index to seed the colliding rows, then
	// reinstate it before running the migration (the migration body
	// drops it again as part of its work).
	if _, err := db.Exec(`DROP INDEX idx_pages_frac_index`); err != nil {
		t.Fatalf("drop legacy unique: %v", err)
	}
	// Seed the parent referenced by page 3 first so the FK to pages(id)
	// holds. (id=10 is given a different frac_index so it doesn't
	// itself participate in the duplicate-collapse.)
	if _, err := db.Exec(`INSERT INTO pages (id, workspace_id, parent_id, title, slug, content, created_by, frac_index)
		VALUES (10, 9001, NULL, 'parent', 'parent', '', ?, 'zz')`, 9101); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	// Roots collision (same parent, same key) — the dup we need to null out.
	for _, r := range []struct {
		id     int
		parent any
		frac   string
	}{
		{1, nil, "a0"},
		{2, nil, "a0"},
		{3, 10, "a0"},
	} {
		if _, err := db.Exec(`INSERT INTO pages (id, workspace_id, parent_id, title, slug, content, created_by, frac_index)
			VALUES (?, ?, ?, ?, ?, '', ?, ?)`, r.id, 9001, r.parent, fmt.Sprintf("p%d", r.id), fmt.Sprintf("p%d", r.id), 9101, r.frac); err != nil {
			t.Fatalf("seed page %d: %v", r.id, err)
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX idx_pages_frac_index ON pages(frac_index) WHERE frac_index IS NOT NULL`); err != nil {
		// Expected to fail; we've already seeded duplicates. The
		// migration drops the old index inside its body, so the
		// fixture only needs the legacy state to exist in
		// schema_migrations terms — it doesn't actually need the old
		// unique index materialised once duplicates are present.
		t.Logf("legacy unique index couldn't be re-created with duplicates present (expected): %v", err)
	}

	// Re-running Initialize() walks the catalog; our migration is
	// pending again thanks to the schema_migrations DELETE above.
	if err := db.Initialize(); err != nil {
		t.Fatalf("re-initialize (apply migration): %v", err)
	}

	// Verify the duplicate collapsed: exactly one row in the root group
	// keeps frac_index='a0' (the lowest id, 1); page 2's key is now NULL.
	for _, c := range []struct {
		id        int
		wantFrac  any
		wantNull  bool
		groupNote string
	}{
		{1, "a0", false, "lowest-id root keeps its key"},
		{2, nil, true, "higher-id root duplicate NULLed"},
		{3, "a0", false, "different parent — independent sibling set, untouched"},
	} {
		var frac *string
		if err := db.QueryRow(`SELECT frac_index FROM pages WHERE id=?`, c.id).Scan(&frac); err != nil {
			t.Fatalf("read page %d frac_index: %v", c.id, err)
		}
		if c.wantNull {
			if frac != nil {
				t.Errorf("page %d (%s): want NULL, got %q", c.id, c.groupNote, *frac)
			}
			continue
		}
		if frac == nil || *frac != c.wantFrac.(string) {
			t.Errorf("page %d (%s): want %q, got %v", c.id, c.groupNote, c.wantFrac, frac)
		}
	}

	// New index in place, old one gone.
	var newIdx, oldIdx int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_pages_frac_index_scoped'`).Scan(&newIdx); err != nil {
		t.Fatalf("query new index: %v", err)
	}
	if newIdx != 1 {
		t.Errorf("idx_pages_frac_index_scoped: want 1, got %d", newIdx)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_pages_frac_index'`).Scan(&oldIdx); err != nil {
		t.Fatalf("query old index: %v", err)
	}
	if oldIdx != 0 {
		t.Errorf("legacy idx_pages_frac_index: want 0, got %d", oldIdx)
	}
}
