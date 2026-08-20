package database

import (
	"database/sql"
	"errors"
	"fmt"
)

// CanonicalSchemaCheckpointVersion is the release whose upgrade produces the
// schema that later releases are allowed to mutate.
const CanonicalSchemaCheckpointVersion = "0.8.5"

// ValidateCanonicalSchemaCheckpoint refuses a database that has not completed
// the canonical schema conversion. Callers should invoke it immediately after
// database initialization and before seeding, repair, or scheduler startup.
func ValidateCanonicalSchemaCheckpoint(db Database) error {
	exists, err := schemaCheckpointTableExists(db)
	if err != nil {
		return fmt.Errorf("check database schema checkpoint: %w", err)
	}
	if !exists {
		return fmt.Errorf("database schema checkpoint is missing: back up the database and upgrade it through %s before starting a later release", CanonicalSchemaCheckpointVersion)
	}

	var version string
	if err := db.QueryRow("SELECT version FROM schema_checkpoint WHERE id = 1").Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("database schema checkpoint is missing: back up the database and upgrade it through %s before starting a later release", CanonicalSchemaCheckpointVersion)
		}
		return fmt.Errorf("read database schema checkpoint: %w", err)
	}
	if version != CanonicalSchemaCheckpointVersion {
		return fmt.Errorf("database schema checkpoint %q is unsupported: back up the database and upgrade it through %s before starting a later release", version, CanonicalSchemaCheckpointVersion)
	}

	var phase string
	if err := db.QueryRow("SELECT phase FROM global_rank_state WHERE id = 1").Scan(&phase); err != nil {
		return fmt.Errorf("read global rank checkpoint state: %w", err)
	}
	switch phase {
	case "stable", "migrating", "paused", "failed":
		// These are all post-checkpoint operational states. In particular, a
		// process must be allowed to restart while migrating so the resumable
		// worker can reclaim an expired lease and continue from its frontier.
	case "legacy":
		return fmt.Errorf("database schema checkpoint %s is incomplete: global rank conversion is %s", CanonicalSchemaCheckpointVersion, phase)
	default:
		return fmt.Errorf("database schema checkpoint %s has invalid global rank phase %q", CanonicalSchemaCheckpointVersion, phase)
	}
	canonical, err := canonicalFracIndexSchemaCheck(db)
	if err != nil {
		return fmt.Errorf("validate canonical item rank schema: %w", err)
	}
	if !canonical {
		return fmt.Errorf("database schema checkpoint %s is incomplete: items.frac_index is not canonical", CanonicalSchemaCheckpointVersion)
	}
	valid, err := canonicalRankPrefixesCheck(db)
	if err != nil {
		return fmt.Errorf("validate canonical item ranks: %w", err)
	}
	if !valid {
		return fmt.Errorf("database schema checkpoint %s is incomplete: canonical item ranks are invalid or duplicated", CanonicalSchemaCheckpointVersion)
	}
	return nil
}

func canonicalFracIndexSchemaCheck(db Database) (bool, error) {
	if db.GetDriverName() == driverSQLite {
		return canonicalFracIndexSQLiteCheck(db)
	}
	return canonicalFracIndexPostgresCheck(db)
}

func schemaCheckpointTableExists(db Database) (bool, error) {
	query := "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'schema_checkpoint'"
	if db.GetDriverName() == driverSQLite {
		query = "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_checkpoint'"
	}
	var tableCount int
	if err := db.QueryRow(query).Scan(&tableCount); err != nil {
		return false, err
	}
	return tableCount == 1, nil
}

const canonicalRankPrefixValidationQuery = `
	SELECT CASE WHEN
		EXISTS (SELECT 1 FROM items WHERE frac_index < '0|' LIMIT 1)
		OR EXISTS (SELECT 1 FROM items WHERE frac_index >= '0}' AND frac_index < '1|' LIMIT 1)
		OR EXISTS (SELECT 1 FROM items WHERE frac_index >= '1}' AND frac_index < '2|' LIMIT 1)
		OR EXISTS (SELECT 1 FROM items WHERE frac_index >= '2}' LIMIT 1)
	THEN 0 ELSE 1 END`

// canonicalRankPrefixesCheck probes only the gaps outside the three bucket
// prefixes. The structural check has already guaranteed non-null unique ranks,
// and each bounded probe can use idx_items_frac_index.
func canonicalRankPrefixesCheck(db Database) (bool, error) {
	var valid int
	if err := db.QueryRow(canonicalRankPrefixValidationQuery).Scan(&valid); err != nil {
		return false, err
	}
	return valid == 1, nil
}
