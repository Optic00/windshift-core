package database

import (
	"fmt"
	"testing"
)

// TestActionCredentialsReshape_OldShapeUpgrade seeds the pre-90edd5a shape
// (workspace_id column + FK, idx_action_credentials_workspace, no
// applies_to_all_workspaces, no join table), runs runPendingMigrations,
// then asserts the post-migration shape and that backfill preserved data.
func TestActionCredentialsReshape_OldShapeUpgrade(t *testing.T) {
	db := openTestDB(t)

	// Minimal dependencies for the FK in the legacy schema.
	for _, ddl := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT)`,
		`CREATE TABLE workspaces (id INTEGER PRIMARY KEY AUTOINCREMENT)`,
		`CREATE TABLE action_credentials (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			credential_type TEXT NOT NULL,
			workspace_id INTEGER,
			created_by INTEGER,
			encrypted_secret TEXT NOT NULL,
			secret_prefix TEXT,
			secret_metadata TEXT,
			is_enabled BOOLEAN DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
			FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL
		)`,
		`CREATE INDEX idx_action_credentials_workspace ON action_credentials(workspace_id)`,
		`INSERT INTO workspaces (id) VALUES (1), (2)`,
		`INSERT INTO action_credentials (id, name, credential_type, workspace_id, encrypted_secret)
			VALUES (1, 'global', 'bearer_token', NULL, 'enc-global'),
			       (2, 'ws1', 'api_key', 1, 'enc-ws1'),
			       (3, 'ws2', 'basic_auth', 2, 'enc-ws2')`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("seed %q: %v", ddl, err)
		}
	}

	// Run only the new entry against the seeded DB.
	var target *Migration
	for i := range Catalog {
		if Catalog[i].Version == "20260519_action_credentials_workspace_scope" {
			target = &Catalog[i]
		}
	}
	if target == nil {
		t.Fatalf("migration entry not found in Catalog")
	}
	if err := runPendingMigrations(db, []Migration{*target}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Column shape.
	if n := countRows(t, db, "SELECT COUNT(*) FROM pragma_table_info('action_credentials') WHERE name='applies_to_all_workspaces'"); n != 1 {
		t.Fatalf("applies_to_all_workspaces column missing (count=%d)", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM pragma_table_info('action_credentials') WHERE name='workspace_id'"); n != 0 {
		t.Fatalf("legacy workspace_id column still present (count=%d)", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_action_credentials_workspace'"); n != 0 {
		t.Fatalf("legacy index still present")
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='action_credential_workspaces'"); n != 1 {
		t.Fatalf("join table missing")
	}

	// Backfill: row 1 stays global, rows 2 and 3 become scoped.
	cases := []struct {
		id       int
		appliesA int
	}{{1, 1}, {2, 0}, {3, 0}}
	for _, c := range cases {
		var a int
		if err := db.QueryRow(fmt.Sprintf("SELECT applies_to_all_workspaces FROM action_credentials WHERE id=%d", c.id)).Scan(&a); err != nil {
			t.Fatalf("scan row %d: %v", c.id, err)
		}
		if a != c.appliesA {
			t.Fatalf("row %d: applies_to_all_workspaces=%d want %d", c.id, a, c.appliesA)
		}
	}
	// Join table rows from backfill.
	if n := countRows(t, db, "SELECT COUNT(*) FROM action_credential_workspaces WHERE credential_id=2 AND workspace_id=1"); n != 1 {
		t.Fatalf("join row for cred 2 missing")
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM action_credential_workspaces WHERE credential_id=3 AND workspace_id=2"); n != 1 {
		t.Fatalf("join row for cred 3 missing")
	}
	// Secrets preserved.
	var sec string
	if err := db.QueryRow("SELECT encrypted_secret FROM action_credentials WHERE id=2").Scan(&sec); err != nil {
		t.Fatalf("scan secret: %v", err)
	}
	if sec != "enc-ws1" {
		t.Fatalf("encrypted_secret not preserved: %q", sec)
	}
}
