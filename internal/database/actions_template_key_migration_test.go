package database

import (
	"strings"
	"testing"
)

func TestActionsTemplateKeyPostgresRepairMigration(t *testing.T) {
	var migration Migration
	for _, candidate := range Catalog {
		if candidate.Version == "20260717_actions_template_key_postgres" {
			migration = candidate
			break
		}
	}
	if migration.Version == "" {
		t.Fatal("actions.template_key Postgres repair migration missing from catalog")
	}
	if migration.CheckPostgres != pgColumnCheck("actions", "template_key") {
		t.Fatalf("unexpected Postgres check: %q", migration.CheckPostgres)
	}
	if !strings.Contains(migration.Postgres, "ALTER TABLE actions ADD COLUMN IF NOT EXISTS template_key TEXT") {
		t.Fatalf("unexpected Postgres migration body: %q", migration.Postgres)
	}
	if migration.SQLite != "" {
		t.Fatalf("repair migration must be a SQLite no-op, got %q", migration.SQLite)
	}
}
