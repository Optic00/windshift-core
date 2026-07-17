package database

import (
	"path/filepath"
	"testing"
)

func TestChannelPublicSlugMigrationBackfillsWithoutDuplicateFailure(t *testing.T) {
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
		CREATE TABLE channels (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL,
			direction TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'disabled',
			config TEXT
		);
		INSERT INTO channels(type, direction, status, config) VALUES
			('portal', 'inbound', 'enabled', '{"portal_slug":"support"}'),
			('portal', 'inbound', 'disabled', '{"portal_slug":"support"}'),
			('portal', 'inbound', 'disabled', 'malformed'),
			('form', 'inbound', 'disabled', '{"form_slug":"support"}');
	`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	var migration Migration
	for _, candidate := range Catalog {
		if candidate.Version == "20260716_channel_public_slugs" {
			migration = candidate
			break
		}
	}
	if migration.Version == "" {
		t.Fatal("public slug migration missing from catalog")
	}
	if err := runPendingMigrations(db, []Migration{migration}); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	var populated int
	if err := db.QueryRow("SELECT COUNT(*) FROM channels WHERE public_slug = 'support'").Scan(&populated); err != nil {
		t.Fatalf("count backfilled slugs: %v", err)
	}
	if populated != 2 {
		t.Fatalf("backfilled slug rows = %d, want one portal and one form", populated)
	}
	var winningPortalID int
	if err := db.QueryRow("SELECT id FROM channels WHERE type = 'portal' AND public_slug = 'support'").Scan(&winningPortalID); err != nil {
		t.Fatalf("find winning portal: %v", err)
	}
	if winningPortalID != 1 {
		t.Fatalf("winning portal ID = %d, want enabled legacy channel 1", winningPortalID)
	}
	if _, err := db.Exec(`
		UPDATE channels SET public_slug = 'support'
		WHERE id = (
			SELECT id FROM channels
			WHERE type = 'portal' AND public_slug IS NULL
			LIMIT 1
		)
	`); err == nil {
		t.Fatal("duplicate portal slug unexpectedly passed the unique index")
	}
}
