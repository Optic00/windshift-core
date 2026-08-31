package database

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestZammadSchemaInitializesOnSQLite(t *testing.T) {
	db, err := NewSQLiteDB(filepath.Join(t.TempDir(), "windshift-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize SQLite schema: %v", err)
	}

	for _, table := range []string{"zammad_connections", "zammad_connection_workspaces", "zammad_ticket_links", "zammad_oauth_tokens", "zammad_oauth_state"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected table %s", table)
		}
	}

	for _, version := range []string{"20260829_zammad_integration", "20260830_zammad_oauth_connections", "20260830_zammad_oauth_generation", "20260830_zammad_ticket_link_metadata", "20260830_zammad_ticket_link_completion_postgres"} {
		var migrationCount int
		if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version=?", version).Scan(&migrationCount); err != nil {
			t.Fatal(err)
		}
		if migrationCount != 1 {
			t.Fatalf("Zammad migration %s was not stamped", version)
		}
	}
	rows, err := db.Query("PRAGMA table_info(zammad_connections)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var requiredCredential, authMethod, oauthGeneration, oauthAttempt bool
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "credential_id" {
			requiredCredential = notNull == 1
		}
		if name == "auth_method" {
			authMethod = true
		}
		if name == "oauth_generation" {
			oauthGeneration = notNull == 1
		}
		if name == "oauth_attempt_id" {
			oauthAttempt = true
		}
	}
	if !requiredCredential || !authMethod || !oauthGeneration || !oauthAttempt {
		t.Fatal("Zammad OAuth connection schema must retain the legacy managed-credential constraint")
	}
	for table, columns := range map[string][]string{
		"zammad_oauth_tokens": {"oauth_generation", "refresh_claim_owner"},
		"zammad_oauth_state":  {"oauth_generation"},
	} {
		for _, column := range columns {
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("expected %s.%s", table, column)
			}
		}
	}
	var stateProviderIndex int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_zammad_oauth_state_provider'").Scan(&stateProviderIndex); err != nil || stateProviderIndex != 1 {
		t.Fatalf("expected unique per-connection OAuth state index: count=%d err=%v", stateProviderIndex, err)
	}
}

func TestZammadTicketLinkMetadataSQLiteUpgrade(t *testing.T) {
	db, err := NewSQLiteDB(filepath.Join(t.TempDir(), "legacy-zammad-links.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatal(err)
	}

	// Recreate the stamped baseline table without the metadata columns. This
	// models an existing install whose 20260829 migration already ran.
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE zammad_ticket_links_legacy (
			id TEXT PRIMARY KEY, item_id INTEGER NOT NULL, provider_id TEXT NOT NULL,
			item_integration_link_id TEXT, ticket_id INTEGER, ticket_number TEXT DEFAULT '',
			ticket_url TEXT DEFAULT '', group_id INTEGER, group_name TEXT DEFAULT '',
			correlation_key TEXT NOT NULL, sync_state TEXT NOT NULL DEFAULT 'pending',
			creating_started_at DATETIME, last_status_id INTEGER, last_status_name TEXT DEFAULT '',
			last_synced_at DATETIME, last_error TEXT DEFAULT '',
			completion_applied BOOLEAN NOT NULL DEFAULT false, sync_lock_until DATETIME,
			created_by INTEGER, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(item_id, provider_id), UNIQUE(provider_id, ticket_id),
			UNIQUE(provider_id, correlation_key)
		);
		DROP TABLE zammad_ticket_links;
		ALTER TABLE zammad_ticket_links_legacy RENAME TO zammad_ticket_links;
		DELETE FROM schema_migrations WHERE version = '20260830_zammad_ticket_link_metadata';
	`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	if err := runPendingMigrations(db, Catalog); err != nil {
		t.Fatalf("run additive ticket-link metadata migration: %v", err)
	}

	for _, column := range []string{"owner_id", "owner_name", "last_attempt_at", "next_attempt_at", "completion_applied"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('zammad_ticket_links') WHERE name = ?", column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected migrated zammad_ticket_links.%s", column)
		}
	}
}

func TestZammadTicketLinkMetadataMigrationBackendParity(t *testing.T) {
	var migration *Migration
	for i := range Catalog {
		if Catalog[i].Version == "20260830_zammad_ticket_link_metadata" {
			migration = &Catalog[i]
			break
		}
	}
	if migration == nil {
		t.Fatal("Zammad ticket-link metadata migration is missing")
	}
	for _, column := range []string{"owner_id", "owner_name", "last_attempt_at", "next_attempt_at"} {
		if !strings.Contains(migration.CheckSQLite, column) || !strings.Contains(migration.CheckPostgres, column) {
			t.Fatalf("metadata column %s is missing from one of the backend checks", column)
		}
		if !strings.Contains(migration.SQLite, "ADD COLUMN "+column) || !strings.Contains(migration.Postgres, "ADD COLUMN IF NOT EXISTS "+column) {
			t.Fatalf("metadata column %s is missing from one of the backend migration bodies", column)
		}
	}
	if !strings.Contains(zammadSchemaMigrationSQLite, "completion_applied") || !strings.Contains(zammadSchemaMigrationPostgres, "completion_applied") {
		t.Fatal("baseline Zammad migrations must include completion_applied")
	}
	var completionMigration *Migration
	for i := range Catalog {
		if Catalog[i].Version == "20260830_zammad_ticket_link_completion_postgres" {
			completionMigration = &Catalog[i]
			break
		}
	}
	if completionMigration == nil || !strings.Contains(completionMigration.Postgres, "ADD COLUMN IF NOT EXISTS completion_applied") {
		t.Fatal("PostgreSQL completion_applied backfill migration is missing")
	}
}

// This starts from the deployed 20260829 table shape. The OAuth migration is
// additive: it preserves an API-token connection and permits a new OAuth row
// with its mandatory managed-credential container.
func TestZammadOAuthSQLiteUpgradePreservesLegacyConnection(t *testing.T) {
	db, err := NewSQLiteDB(filepath.Join(t.TempDir(), "legacy-zammad.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO users(id, email, username, first_name, last_name) VALUES (9001, 'zammad-upgrade@example.test', 'zammad-upgrade', 'Zammad', 'Upgrade')`,
		`INSERT INTO action_credentials(id, name, credential_type, applies_to_all_workspaces, encrypted_secret, is_enabled) VALUES (9001, 'legacy API', 'custom_header', true, 'opaque-ciphertext', true)`,
		`INSERT INTO integration_providers(id, slug, name, provider_type, enabled, provider_config) VALUES ('legacy-zammad', 'legacy-zammad', 'Legacy Zammad', 'zammad', true, '{}')`,
		`INSERT INTO zammad_connections(provider_id, credential_id, auth_method, base_url, allowed_group_ids, default_customer, closed_state_ids, applies_to_all_workspaces, created_by) VALUES ('legacy-zammad', 9001, 'api_token', 'https://legacy.example.test', '[]', 'robot@example.test', '[]', true, 9001)`,
	} {
		if _, err := db.ExecWrite(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE zammad_connections_legacy (
			provider_id TEXT PRIMARY KEY, credential_id INTEGER NOT NULL, base_url TEXT NOT NULL, default_group_id INTEGER,
			default_group_name TEXT DEFAULT '', allowed_group_ids TEXT NOT NULL DEFAULT '[]', default_customer TEXT NOT NULL,
			correlation_field TEXT NOT NULL DEFAULT 'windshift_item_key', closed_state_ids TEXT NOT NULL DEFAULT '[]', completion_status_id INTEGER,
			applies_to_all_workspaces BOOLEAN NOT NULL DEFAULT false, last_tested_at DATETIME, last_test_error TEXT DEFAULT '', created_by INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO zammad_connections_legacy(provider_id, credential_id, base_url, default_group_id, default_group_name, allowed_group_ids, default_customer, correlation_field, closed_state_ids, completion_status_id, applies_to_all_workspaces, last_tested_at, last_test_error, created_by, created_at, updated_at)
		SELECT provider_id, credential_id, base_url, default_group_id, default_group_name, allowed_group_ids, default_customer, correlation_field, closed_state_ids, completion_status_id, applies_to_all_workspaces, last_tested_at, last_test_error, created_by, created_at, updated_at FROM zammad_connections;
		DROP TABLE zammad_connections;
		ALTER TABLE zammad_connections_legacy RENAME TO zammad_connections;
		DROP TABLE zammad_oauth_state;
		DROP TABLE zammad_oauth_tokens;
		DELETE FROM schema_migrations WHERE version = '20260830_zammad_oauth_connections';
		DELETE FROM schema_migrations WHERE version = '20260830_zammad_oauth_generation';
	`)
	if _, onErr := db.Exec("PRAGMA foreign_keys=ON"); onErr != nil {
		t.Fatal(onErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := runPendingMigrations(db, Catalog); err != nil {
		t.Fatalf("run additive OAuth migration: %v", err)
	}
	var credentialID int
	var authMethod string
	if err := db.QueryRow(`SELECT credential_id, auth_method FROM zammad_connections WHERE provider_id='legacy-zammad'`).Scan(&credentialID, &authMethod); err != nil || credentialID != 9001 || authMethod != "api_token" {
		t.Fatalf("legacy API connection was not preserved: credential=%d method=%q err=%v", credentialID, authMethod, err)
	}
	for _, statement := range []string{
		`INSERT INTO action_credentials(id, name, credential_type, applies_to_all_workspaces, encrypted_secret, is_enabled) VALUES (9002, 'OAuth pending', 'custom_header', true, 'opaque-pending-ciphertext', true)`,
		`INSERT INTO integration_providers(id, slug, name, provider_type, enabled, oauth_client_id, oauth_client_secret_encrypted, provider_config) VALUES ('oauth-zammad', 'oauth-zammad', 'OAuth Zammad', 'zammad', true, 'client', 'opaque-ciphertext', '{}')`,
		`INSERT INTO zammad_connections(provider_id, credential_id, auth_method, base_url, allowed_group_ids, default_customer, closed_state_ids, applies_to_all_workspaces, created_by) VALUES ('oauth-zammad', 9002, 'oauth', 'https://oauth.example.test', '[]', 'robot@example.test', '[]', true, 9001)`,
	} {
		if _, err := db.ExecWrite(statement); err != nil {
			t.Fatalf("upgraded schema rejected OAuth pending managed credential: %v", err)
		}
	}
}
