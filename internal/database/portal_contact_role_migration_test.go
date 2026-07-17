package database

import (
	"path/filepath"
	"testing"
)

func TestPortalCustomerDefaultContactRoleMigrationBackfillsExistingDatabase(t *testing.T) {
	db, err := NewSQLiteDB(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("NewSQLiteDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`
		CREATE TABLE schema_migrations (
			version TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL DEFAULT '',
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE contact_roles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			description TEXT,
			is_system BOOLEAN DEFAULT false,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	var migration Migration
	for _, candidate := range Catalog {
		if candidate.Version == "20260717_portal_customer_default_contact_role" {
			migration = candidate
			break
		}
	}
	if migration.Version == "" {
		t.Fatal("default portal customer contact role migration missing from catalog")
	}

	if err := runPendingMigrations(db, []Migration{migration}); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if err := runPendingMigrations(db, []Migration{migration}); err != nil {
		t.Fatalf("rerun migration: %v", err)
	}

	var count int
	var description string
	var isSystem bool
	if err := db.QueryRow(`
		SELECT COUNT(*), MAX(description), MAX(is_system)
		FROM contact_roles
		WHERE name = 'Portal Customer'
	`).Scan(&count, &description, &isSystem); err != nil {
		t.Fatalf("load migrated role: %v", err)
	}
	if count != 1 {
		t.Fatalf("default role count = %d, want 1", count)
	}
	if description != "Default role assigned to portal customers" {
		t.Fatalf("default role description = %q", description)
	}
	if !isSystem {
		t.Fatal("default role is_system = false, want true")
	}
}
