package database

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
)

// Migration is one entry in the schema_migrations catalog. Version is a
// stable slug used as the schema_migrations primary key; Name is a human
// label. CheckSQLite / CheckPostgres are queries that return COUNT >= 1
// when the migration's effect is already present, used for retroactive
// backfill on existing installs upgrading past the introduction of the
// schema_migrations table. SQLite / Postgres carry the backend-specific
// DDL to apply when the check reports the effect is missing. ApplySQLite /
// ApplyPostgres are reserved for migrations that cannot be expressed as one
// transactional SQL body (notably SQLite table rebuilds that must toggle
// foreign_keys outside their transaction). Their matching SQL field contains
// a stable implementation marker that participates in checksum validation.
//
// An empty Check on a backend means the migration body always runs when
// the version isn't already stamped. An empty body on a backend means
// the migration is skipped on that backend — the row is still stamped
// so the catalog stays consistent across backends.
type Migration struct {
	Version         string
	Name            string
	CheckSQLite     string
	CheckPostgres   string
	CheckSQLiteFn   func(Database) (bool, error)
	CheckPostgresFn func(Database) (bool, error)
	SQLite          string
	Postgres        string
	ApplySQLite     func(Database) error
	ApplyPostgres   func(Database) error

	// ReconcileChecksum permits intentional edits to schema_* compatibility
	// wrappers. Applied wrappers are not rerun; their checksum is advanced.
	ReconcileChecksum bool

	// Superseded accepts checksums from before validation was enforced and
	// restamps them once. New schema changes still require a new migration.
	Superseded []string
}

// acceptsSuperseded reports whether stored is a checksum this migration's body
// carried in an earlier release.
func (m Migration) acceptsSuperseded(stored string) bool {
	return slices.Contains(m.Superseded, stored)
}

// Catalog is the ordered list of migrations applied via runPendingMigrations.
// New migrations append with a date-prefixed Version slug such as
// "20260514_widgets_archived_at". Order matters only between migrations
// with row dependencies; otherwise entries may be reordered freely.
//
// 0.8.6 squashed the historical catalog: 0.8.5 is the minimum supported
// schema, the compact catalog only carries the upgrades introduced after
// v0.8.5, and the Initialize implementations refuse databases without a
// valid canonical schema checkpoint before any migration runs. Retired
// schema_migrations rows on upgraded databases are ignored because the
// runner iterates the catalog, never the stored rows.
var Catalog = []Migration{
	{
		Version: "0000_baseline",
		Name:    "fresh-install baseline marker",
	},
	{
		Version:       "20260814_workflow_transitions_from_all",
		Name:          "Allow workflow transitions from every other status",
		CheckSQLite:   "SELECT COUNT(*) FROM pragma_table_info('workflow_transitions') WHERE name='from_all_statuses'",
		CheckPostgres: "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='workflow_transitions' AND column_name='from_all_statuses'",
		SQLite:        "ALTER TABLE workflow_transitions ADD COLUMN from_all_statuses BOOLEAN NOT NULL DEFAULT false",
		Postgres:      "ALTER TABLE workflow_transitions ADD COLUMN IF NOT EXISTS from_all_statuses BOOLEAN NOT NULL DEFAULT false",
	},
	{
		Version:       "20260816_portal_approval_vote_uniqueness",
		Name:          "Enforce one portal-customer vote per approval step",
		CheckSQLite:   sqliteIndexCheck("uq_approval_decisions_one_vote_per_portal_customer"),
		CheckPostgres: pgIndexCheck("uq_approval_decisions_one_vote_per_portal_customer"),
		SQLite: `
			DELETE FROM approval_decisions
			WHERE id IN (
				SELECT id FROM (
					SELECT id, ROW_NUMBER() OVER (
						PARTITION BY approval_step_instance_id, actor_portal_customer_id
						ORDER BY created_at, id
					) AS duplicate_rank
					FROM approval_decisions
					WHERE actor_portal_customer_id IS NOT NULL
					  AND decision IN ('approve', 'reject')
				) duplicate_votes
				WHERE duplicate_rank > 1
			);
			CREATE UNIQUE INDEX uq_approval_decisions_one_vote_per_portal_customer
				ON approval_decisions(approval_step_instance_id, actor_portal_customer_id)
				WHERE actor_portal_customer_id IS NOT NULL AND decision IN ('approve', 'reject');
		`,
		Postgres: `
			DELETE FROM approval_decisions
			WHERE id IN (
				SELECT id FROM (
					SELECT id, ROW_NUMBER() OVER (
						PARTITION BY approval_step_instance_id, actor_portal_customer_id
						ORDER BY created_at, id
					) AS duplicate_rank
					FROM approval_decisions
					WHERE actor_portal_customer_id IS NOT NULL
					  AND decision IN ('approve', 'reject')
				) duplicate_votes
				WHERE duplicate_rank > 1
			);
			CREATE UNIQUE INDEX uq_approval_decisions_one_vote_per_portal_customer
				ON approval_decisions(approval_step_instance_id, actor_portal_customer_id)
				WHERE actor_portal_customer_id IS NOT NULL AND decision IN ('approve', 'reject');
		`,
	},
	{
		Version:       "20260815_workspaces_is_template",
		Name:          "Mark workspaces as reusable templates",
		CheckSQLite:   sqliteColumnCheck("workspaces", "is_template"),
		CheckPostgres: pgColumnCheck("workspaces", "is_template"),
		// The body originally added the column NOT NULL while the fresh
		// schema files declared it nullable. The canonical contract is
		// nullable; Superseded advances databases stamped by unreleased
		// main builds that ran the NOT NULL body.
		Superseded: []string{"ea8a11f5aff9de67107eaaa4a23a1519397546f69d693b77beb1dd53c9478054"},
		SQLite: `
			ALTER TABLE workspaces ADD COLUMN is_template BOOLEAN DEFAULT false;
			CREATE INDEX IF NOT EXISTS idx_workspaces_template_active
				ON workspaces(is_template, active)
				WHERE is_template = true;
		`,
		Postgres: `
			ALTER TABLE workspaces ADD COLUMN is_template BOOLEAN DEFAULT false;
			CREATE INDEX IF NOT EXISTS idx_workspaces_template_active
				ON workspaces(is_template, active)
				WHERE is_template = true;
		`,
	},
	{
		Version:       "20260823_cfv_cleanup_retries",
		Name:          "Add retry scheduling to custom field maintenance jobs",
		CheckSQLite:   sqliteColumnCheck("pending_custom_field_cleanups", "attempt_count"),
		CheckPostgres: pgColumnCheck("pending_custom_field_cleanups", "attempt_count"),
		SQLite: `
			ALTER TABLE pending_custom_field_cleanups ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE pending_custom_field_cleanups ADD COLUMN next_attempt_at DATETIME;
		`,
		Postgres: `
			ALTER TABLE pending_custom_field_cleanups ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE pending_custom_field_cleanups ADD COLUMN next_attempt_at TIMESTAMPTZ;
		`,
	},
	{
		Version:       "20260824_agent_skill_page_snapshots",
		Name:          "Snapshot pages referenced by agent skills",
		CheckSQLite:   sqliteColumnCheck("workspace_agent_skill_pages", "content_snapshot"),
		CheckPostgres: pgColumnCheck("workspace_agent_skill_pages", "content_snapshot"),
		SQLite: `
			ALTER TABLE workspace_agent_skill_pages ADD COLUMN title_snapshot TEXT NOT NULL DEFAULT '';
			ALTER TABLE workspace_agent_skill_pages ADD COLUMN content_snapshot TEXT NOT NULL DEFAULT '';
			ALTER TABLE workspace_agent_skill_pages ADD COLUMN page_updated_at_snapshot DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00';
			ALTER TABLE workspace_agent_skill_pages ADD COLUMN snapshot_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00';
			UPDATE workspace_agent_skill_pages
			SET title_snapshot = COALESCE((SELECT title FROM pages WHERE pages.id = page_id), ''),
			    content_snapshot = COALESCE((SELECT content FROM pages WHERE pages.id = page_id), ''),
			    page_updated_at_snapshot = COALESCE((SELECT updated_at FROM pages WHERE pages.id = page_id), CURRENT_TIMESTAMP),
			    snapshot_at = CURRENT_TIMESTAMP;
		`,
		Postgres: `
			ALTER TABLE workspace_agent_skill_pages ADD COLUMN title_snapshot TEXT NOT NULL DEFAULT '';
			ALTER TABLE workspace_agent_skill_pages ADD COLUMN content_snapshot TEXT NOT NULL DEFAULT '';
			ALTER TABLE workspace_agent_skill_pages ADD COLUMN page_updated_at_snapshot TIMESTAMPTZ;
			ALTER TABLE workspace_agent_skill_pages ADD COLUMN snapshot_at TIMESTAMPTZ;
			UPDATE workspace_agent_skill_pages sp
			SET title_snapshot = p.title,
			    content_snapshot = p.content,
			    page_updated_at_snapshot = p.updated_at,
			    snapshot_at = CURRENT_TIMESTAMP
			FROM pages p WHERE p.id = sp.page_id;
			ALTER TABLE workspace_agent_skill_pages ALTER COLUMN page_updated_at_snapshot SET NOT NULL;
			ALTER TABLE workspace_agent_skill_pages ALTER COLUMN snapshot_at SET NOT NULL;
		`,
	},
	{
		Version:       "20260826_board_completed_item_retention",
		Name:          "Add completed item retention to board configurations",
		CheckSQLite:   sqliteColumnCheck("board_configurations", "completed_item_retention_days"),
		CheckPostgres: pgColumnCheck("board_configurations", "completed_item_retention_days"),
		SQLite:        "ALTER TABLE board_configurations ADD COLUMN completed_item_retention_days INTEGER",
		Postgres:      "ALTER TABLE board_configurations ADD COLUMN completed_item_retention_days INTEGER",
	},
	{
		Version:       "20260827_notification_provenance",
		Name:          "Add authorization provenance to notifications",
		CheckSQLite:   sqliteColumnCheck("notifications", "authorization_scope"),
		CheckPostgres: pgColumnCheck("notifications", "authorization_scope"),
		SQLite: `
			ALTER TABLE notifications ADD COLUMN authorization_scope TEXT NOT NULL DEFAULT 'legacy';
			ALTER TABLE notifications ADD COLUMN workspace_id INTEGER;
			ALTER TABLE notifications ADD COLUMN item_id INTEGER;
			ALTER TABLE notifications ADD COLUMN source_type TEXT;
			ALTER TABLE notifications ADD COLUMN source_id INTEGER;
			CREATE INDEX idx_notifications_workspace_id ON notifications(workspace_id);
		`,
		Postgres: `
			ALTER TABLE notifications ADD COLUMN authorization_scope TEXT NOT NULL DEFAULT 'legacy';
			ALTER TABLE notifications ADD COLUMN workspace_id INTEGER;
			ALTER TABLE notifications ADD COLUMN item_id INTEGER;
			ALTER TABLE notifications ADD COLUMN source_type TEXT;
			ALTER TABLE notifications ADD COLUMN source_id INTEGER;
			CREATE INDEX idx_notifications_workspace_id ON notifications(workspace_id);
		`,
	},
}

func (m Migration) checksum(driver string) string {
	var body string
	switch driver {
	case driverSQLite:
		body = m.SQLite
	case driverPostgres:
		body = m.Postgres
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// runPendingMigrations applies catalog entries that aren't yet stamped in
// schema_migrations. For each pending migration: if its backend-specific
// Check predicate reports the effect is already present, the row is stamped
// without re-running the DDL (retroactive backfill); otherwise the DDL runs
// inside a transaction that ends with the stamp INSERT so the pair is
// atomic.
//
// Errors abort startup. There is no log-and-continue.
func runPendingMigrations(db Database, catalog []Migration) error {
	driver := db.GetDriverName()

	applied, err := loadAppliedMigrations(db)
	if err != nil {
		return fmt.Errorf("load applied migrations: %w", err)
	}

	for _, m := range catalog {
		if checksum, ok := applied[m.Version]; ok {
			expected := m.checksum(driver)
			if checksum != "" && checksum != expected && !m.ReconcileChecksum && !m.acceptsSuperseded(checksum) {
				return fmt.Errorf(
					"migration %s (%s): checksum mismatch: stored %s, expected %s",
					m.Version, m.Name, checksum, expected,
				)
			}
			// Backfill an unstamped row and bring recognized historical or
			// intentionally mutable checksums forward to the current value.
			if checksum != expected {
				if _, err := db.Exec(
					"UPDATE schema_migrations SET name = ?, checksum = ? WHERE version = ?",
					m.Name, expected, m.Version,
				); err != nil {
					return fmt.Errorf("migration %s (%s): restamp checksum: %w", m.Version, m.Name, err)
				}
			}
			continue
		}
		if err := applyMigration(db, driver, m); err != nil {
			return fmt.Errorf("migration %s (%s): %w", m.Version, m.Name, err)
		}
	}
	return nil
}

func loadAppliedMigrations(db Database) (map[string]string, error) {
	rows, err := db.Query("SELECT version, checksum FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]string{}
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, err
		}
		out[version] = checksum
	}
	return out, rows.Err()
}

func applyMigration(db Database, driver string, m Migration) error {
	var checkSQL, body string
	var check func(Database) (bool, error)
	var apply func(Database) error
	switch driver {
	case driverSQLite:
		checkSQL, check, body, apply = m.CheckSQLite, m.CheckSQLiteFn, m.SQLite, m.ApplySQLite
	case driverPostgres:
		checkSQL, check, body, apply = m.CheckPostgres, m.CheckPostgresFn, m.Postgres, m.ApplyPostgres
	default:
		return fmt.Errorf("unknown driver %q", driver)
	}

	// Migration is a no-op on this backend — stamp without running anything.
	if body == "" {
		return stampMigration(db, m, driver)
	}

	// Retroactive backfill: if the effect is already present, stamp without
	// re-running. Migrations with no Check always run.
	if check != nil {
		alreadyApplied, err := check(db)
		if err != nil {
			return fmt.Errorf("check: %w", err)
		}
		if alreadyApplied {
			return stampMigration(db, m, driver)
		}
	} else if checkSQL != "" {
		var count int
		if err := db.QueryRow(checkSQL).Scan(&count); err != nil {
			return fmt.Errorf("check: %w", err)
		}
		if count > 0 {
			return stampMigration(db, m, driver)
		}
	}
	if apply != nil {
		if err := apply(db); err != nil {
			return fmt.Errorf("apply: %w", err)
		}
		return stampMigration(db, m, driver)
	}

	return WithTx(db, func(tx Tx) error {
		if _, err := tx.Exec(body); err != nil {
			return fmt.Errorf("apply: %w", err)
		}
		_, err := tx.Exec(
			"INSERT INTO schema_migrations(version, name, checksum) VALUES(?, ?, ?)",
			m.Version, m.Name, m.checksum(driver),
		)
		return err
	})
}

func stampMigration(db Database, m Migration, driver string) error {
	_, err := db.Exec(
		"INSERT INTO schema_migrations(version, name, checksum) VALUES(?, ?, ?)",
		m.Version, m.Name, m.checksum(driver),
	)
	return err
}
