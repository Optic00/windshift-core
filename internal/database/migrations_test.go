package database

import (
	"fmt"
	"testing"
)

// schemaMigrationsDDL mirrors the bootstrap DDL in DB.Initialize and
// PostgresDB.Initialize. Tests create the table directly so they don't
// depend on the full schema bootstrap.
const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version    TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	checksum   TEXT NOT NULL DEFAULT '',
	applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);`

func openTestDB(t *testing.T) Database {
	t.Helper()
	// Per-test on-disk temp DB. file::memory: would also work but on-disk
	// keeps the write-connection pragmas honest under WAL.
	dsn := fmt.Sprintf("file:%s/test.db?mode=memory&cache=shared", t.TempDir())
	db, err := NewSQLiteDBWithPoolSizes(dsn, 4, 1)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(schemaMigrationsDDL); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	return db
}

func countRows(t *testing.T, db Database, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func TestRunPendingMigrations_NoopWithEmptyCatalog(t *testing.T) {
	db := openTestDB(t)
	if err := runPendingMigrations(db, nil); err != nil {
		t.Fatalf("empty catalog: %v", err)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM schema_migrations"); n != 0 {
		t.Fatalf("expected 0 stamped rows, got %d", n)
	}
}

func TestRunPendingMigrations_AppliesNewMigration(t *testing.T) {
	db := openTestDB(t)

	catalog := []Migration{{
		Version: "test_001_create_widget",
		Name:    "create widget table",
		SQLite:  "CREATE TABLE widget (id INTEGER PRIMARY KEY)",
	}}

	if err := runPendingMigrations(db, catalog); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if n := countRows(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='widget'"); n != 1 {
		t.Fatalf("widget table not created (count=%d)", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM schema_migrations WHERE version='test_001_create_widget'"); n != 1 {
		t.Fatalf("stamp not recorded (count=%d)", n)
	}
}

func TestRunPendingMigrations_RetroactiveStampWhenCheckTrue(t *testing.T) {
	db := openTestDB(t)

	// Simulate an existing install: the table is already there.
	if _, err := db.Exec("CREATE TABLE widget (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("seed widget table: %v", err)
	}

	catalog := []Migration{{
		Version:     "test_001_create_widget",
		Name:        "create widget table",
		CheckSQLite: "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='widget'",
		// If the runner mistakenly re-ran this body, the second CREATE would error.
		SQLite: "CREATE TABLE widget (id INTEGER PRIMARY KEY)",
	}}

	if err := runPendingMigrations(db, catalog); err != nil {
		t.Fatalf("retroactive stamp: %v", err)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM schema_migrations WHERE version='test_001_create_widget'"); n != 1 {
		t.Fatalf("retroactive stamp not recorded (count=%d)", n)
	}
}

func TestRunPendingMigrations_IdempotentOnRerun(t *testing.T) {
	db := openTestDB(t)

	catalog := []Migration{{
		Version: "test_001_create_widget",
		Name:    "create widget table",
		// No Check: would re-run and conflict if the runner ignored
		// schema_migrations on the second pass.
		SQLite: "CREATE TABLE widget (id INTEGER PRIMARY KEY)",
	}}

	if err := runPendingMigrations(db, catalog); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := runPendingMigrations(db, catalog); err != nil {
		t.Fatalf("second run should be a no-op: %v", err)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM schema_migrations"); n != 1 {
		t.Fatalf("expected exactly 1 stamped row, got %d", n)
	}
}

// TestCatalog_FreshInstallStampsEveryEntry verifies that on a fresh SQLite
// install, every catalog entry's CheckSQLite predicate reports the effect
// is already present (so the runner stamps without re-running the body).
// Catches typos in table or column names: if a Check returns 0 on a fresh
// install, the runner would try to ALTER and fail with "duplicate column"
// (since fresh schema files already contain it).
func TestCatalog_FreshInstallStampsEveryEntry(t *testing.T) {
	dsn := fmt.Sprintf("file:%s/fresh.db?mode=memory&cache=shared", t.TempDir())
	db, err := NewSQLiteDBWithPoolSizes(dsn, 4, 1)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize fresh db: %v", err)
	}

	// Every catalog entry should be stamped exactly once. Postgres-only
	// entries (empty SQLite body) still get stamped on SQLite — the runner
	// stamps without DDL when the body for this backend is empty.
	stamped := countRows(t, db, "SELECT COUNT(*) FROM schema_migrations")
	if stamped != len(Catalog) {
		t.Fatalf("schema_migrations row count = %d, want %d (len Catalog)", stamped, len(Catalog))
	}
}

// TestMigration_ExternalKey_UniquePerWorkspace verifies the partial-unique
// invariant that `create_milestone`'s upsert logic relies on: two
// workspaces can share an external_key (e.g. both have a "2.0" milestone)
// but a single workspace cannot. NULLs are unconstrained.
func TestMigration_PortalRequestDrafts_CreatesMissingTable(t *testing.T) {
	dsn := fmt.Sprintf("file:%s/portal-drafts.db?mode=memory&cache=shared", t.TempDir())
	db, err := NewSQLiteDBWithPoolSizes(dsn, 4, 1)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize fresh db: %v", err)
	}

	if _, err := db.Exec(`DROP TABLE portal_request_drafts`); err != nil {
		t.Fatalf("drop portal_request_drafts: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = '20260528_portal_request_drafts'`); err != nil {
		t.Fatalf("delete portal drafts stamp: %v", err)
	}

	var target *Migration
	for i := range Catalog {
		if Catalog[i].Version == "20260528_portal_request_drafts" {
			target = &Catalog[i]
			break
		}
	}
	if target == nil {
		t.Fatal("portal request drafts migration entry not found in Catalog")
	}

	if err := runPendingMigrations(db, []Migration{*target}); err != nil {
		t.Fatalf("run portal drafts migration: %v", err)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='portal_request_drafts'"); n != 1 {
		t.Fatalf("portal_request_drafts table not created (count=%d)", n)
	}
	for _, idx := range []string{"uq_portal_request_drafts_pc", "uq_portal_request_drafts_user", "idx_portal_request_drafts_updated_at"} {
		if n := countRows(t, db, fmt.Sprintf("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='%s'", idx)); n != 1 {
			t.Fatalf("%s index not created (count=%d)", idx, n)
		}
	}
}

func TestMigration_ExternalKey_UniquePerWorkspace(t *testing.T) {
	dsn := fmt.Sprintf("file:%s/extkey.db?mode=memory&cache=shared", t.TempDir())
	db, err := NewSQLiteDBWithPoolSizes(dsn, 4, 1)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize fresh db: %v", err)
	}

	// Two workspaces.
	for i, key := range []string{"alpha", "beta"} {
		if _, err := db.Exec(
			`INSERT INTO workspaces(id, name, key, active) VALUES (?, ?, ?, 1)`,
			i+1, "ws-"+key, key,
		); err != nil {
			t.Fatalf("insert workspace %s: %v", key, err)
		}
	}

	// Same external_key in two different workspaces — allowed.
	for _, wsID := range []int{1, 2} {
		if _, err := db.Exec(
			`INSERT INTO milestones(name, is_global, workspace_id, external_key) VALUES (?, 0, ?, '2.0')`,
			fmt.Sprintf("Release 2.0 (ws %d)", wsID), wsID,
		); err != nil {
			t.Fatalf("insert milestone for ws %d: %v", wsID, err)
		}
	}

	// Same external_key twice in the same workspace — must fail.
	if _, err := db.Exec(
		`INSERT INTO milestones(name, is_global, workspace_id, external_key) VALUES ('Dupe', 0, 1, '2.0')`,
	); err == nil {
		t.Fatal("expected unique-constraint failure on duplicate external_key within workspace, got nil")
	}

	// NULL external_keys must not collide with each other.
	for i := 0; i < 3; i++ {
		if _, err := db.Exec(
			`INSERT INTO milestones(name, is_global, workspace_id, external_key) VALUES (?, 0, 1, NULL)`,
			fmt.Sprintf("Unkeyed %d", i),
		); err != nil {
			t.Fatalf("insert NULL-key milestone: %v", err)
		}
	}
}

// TestMigration_AgentRuns_SchemaSanity exercises the 20260529_agent_runs
// migration's table shape end-to-end on a fresh SQLite install: a workspace
// can have an agent_run, that run can have events, events cascade away with
// the run, and the status CHECK rejects bogus values.
func TestMigration_AgentRuns_SchemaSanity(t *testing.T) {
	dsn := fmt.Sprintf("file:%s/agent_runs.db?mode=memory&cache=shared", t.TempDir())
	db, err := NewSQLiteDBWithPoolSizes(dsn, 4, 1)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize fresh db: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO workspaces(id, name, key, active) VALUES (1, 'ws', 'WS', 1)`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	res, err := db.Exec(`INSERT INTO agent_runs(workspace_id, item_id) VALUES (1, NULL)`)
	if err != nil {
		t.Fatalf("insert agent_run: %v", err)
	}
	runID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO agent_run_events(run_id, type, payload_json) VALUES (?, 'lifecycle', '{"phase":"queued"}')`,
		runID,
	); err != nil {
		t.Fatalf("insert agent_run_event: %v", err)
	}
	if n := countRows(t, db, fmt.Sprintf("SELECT COUNT(*) FROM agent_run_events WHERE run_id=%d", runID)); n != 1 {
		t.Fatalf("expected 1 event for run, got %d", n)
	}

	// Status CHECK rejects bogus values.
	if _, err := db.Exec(
		`INSERT INTO agent_runs(workspace_id, status) VALUES (1, 'in_orbit')`,
	); err == nil {
		t.Fatal("expected CHECK violation on bogus status, got nil")
	}

	// ON DELETE CASCADE cleans up events.
	if _, err := db.Exec(`DELETE FROM agent_runs WHERE id = ?`, runID); err != nil {
		t.Fatalf("delete agent_run: %v", err)
	}
	if n := countRows(t, db, fmt.Sprintf("SELECT COUNT(*) FROM agent_run_events WHERE run_id=%d", runID)); n != 0 {
		t.Fatalf("expected cascade delete of events, got %d remaining", n)
	}
}

// TestMigration_AgentSecurity_SchemaSanity exercises the
// 20260529_agent_security_allowlist migration end-to-end on a fresh
// SQLite install: the seeded system_settings row is present, the
// (user_id, workspace_id) uniqueness constraint correctly treats
// NULL as distinct from any concrete workspace, and user cascade
// deletion drops the allowlist row.
func TestMigration_AgentSecurity_SchemaSanity(t *testing.T) {
	dsn := fmt.Sprintf("file:%s/agent_security.db?mode=memory&cache=shared", t.TempDir())
	db, err := NewSQLiteDBWithPoolSizes(dsn, 4, 1)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize fresh db: %v", err)
	}

	var flagValue string
	if err := db.QueryRow(`SELECT value FROM system_settings WHERE key='agents.allow_centralized_service_users'`).Scan(&flagValue); err != nil {
		t.Fatalf("read security flag: %v", err)
	}
	if flagValue != "false" {
		t.Errorf("flag default: want false, got %q", flagValue)
	}

	if _, err := db.Exec(`INSERT INTO workspaces(id, name, key, active) VALUES (1, 'WS', 'WS', 1)`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(email, username, first_name, last_name, is_agent) VALUES ('s@example.com','s','S','',1)`); err != nil {
		t.Fatalf("seed service user: %v", err)
	}
	var serviceUserID int
	if err := db.QueryRow(`SELECT id FROM users WHERE username='s'`).Scan(&serviceUserID); err != nil {
		t.Fatalf("read service user id: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(email, username, first_name, last_name) VALUES ('admin@example.com','admin','A','')`); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	var adminID int
	if err := db.QueryRow(`SELECT id FROM users WHERE username='admin'`).Scan(&adminID); err != nil {
		t.Fatalf("read admin id: %v", err)
	}

	// NULL workspace_id (= any-workspace) and a concrete workspace_id
	// must coexist for the same user — they're distinct grants.
	if _, err := db.Exec(
		`INSERT INTO global_agent_acting_user_allowlist(user_id, workspace_id, reason, created_by_user_id) VALUES (?, NULL, 'broad grant', ?)`,
		serviceUserID, adminID,
	); err != nil {
		t.Fatalf("insert any-workspace grant: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO global_agent_acting_user_allowlist(user_id, workspace_id, reason, created_by_user_id) VALUES (?, 1, 'workspace-1 grant', ?)`,
		serviceUserID, adminID,
	); err != nil {
		t.Fatalf("insert workspace-scoped grant: %v", err)
	}

	// Re-inserting the same (user_id, workspace_id) pair must fail.
	if _, err := db.Exec(
		`INSERT INTO global_agent_acting_user_allowlist(user_id, workspace_id, reason, created_by_user_id) VALUES (?, 1, 'dup', ?)`,
		serviceUserID, adminID,
	); err == nil {
		t.Fatal("expected unique-index violation on duplicate (user_id, workspace_id), got nil")
	}

	// Cascade on user delete drops the rows.
	if _, err := db.Exec(`DELETE FROM users WHERE id = ?`, serviceUserID); err != nil {
		t.Fatalf("delete service user: %v", err)
	}
	if n := countRows(t, db, fmt.Sprintf("SELECT COUNT(*) FROM global_agent_acting_user_allowlist WHERE user_id=%d", serviceUserID)); n != 0 {
		t.Fatalf("cascade delete failed: %d rows still reference user", n)
	}
}

func TestRunPendingMigrations_AbortsOnFailingMigration(t *testing.T) {
	db := openTestDB(t)

	catalog := []Migration{
		{
			Version: "test_001_ok",
			Name:    "good migration",
			SQLite:  "CREATE TABLE good (id INTEGER PRIMARY KEY)",
		},
		{
			Version: "test_002_bad",
			Name:    "bad migration",
			SQLite:  "CREATE TABLE bad (id INTEGER PRIMARY KEY)", // succeeds…
			// …but then we deliberately break it with bogus SQL.
		},
	}
	catalog[1].SQLite = "this is not valid sql"

	err := runPendingMigrations(db, catalog)
	if err == nil {
		t.Fatalf("expected error from bad migration, got nil")
	}

	// First migration stamped and applied; second is rolled back.
	if n := countRows(t, db, "SELECT COUNT(*) FROM schema_migrations"); n != 1 {
		t.Fatalf("expected 1 stamped row after partial failure, got %d", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='bad'"); n != 0 {
		t.Fatalf("expected 'bad' table to be rolled back, found %d", n)
	}
}
