package database

import (
	"fmt"
)

const checkpointRankDigits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// applyGlobalRankCheckpoint converts the legacy nullable namespace into a
// deterministic bucket-0 namespace. Existing bytewise order is preserved by
// ordering rows first and then assigning fixed-width fractional payloads; NULL
// rows have no contractual position and are placed after ranked rows by id.
// The unique index is removed only inside this atomic conversion transaction,
// and no row is ever written with a NULL rank.
func applyGlobalRankCheckpoint(db Database) error {
	return WithTx(db, func(tx Tx) error {
		if err := createGlobalRankStateTable(tx, db.GetDriverName()); err != nil {
			return err
		}
		if _, err := tx.Exec("DROP INDEX IF EXISTS idx_items_frac_index"); err != nil {
			return fmt.Errorf("drop legacy frac_index uniqueness: %w", err)
		}

		rows, err := tx.Query(`
			SELECT id
			FROM items
			ORDER BY CASE WHEN frac_index IS NULL THEN 1 ELSE 0 END,
			         frac_index ASC, id ASC`)
		if err != nil {
			return fmt.Errorf("read items for global rank checkpoint: %w", err)
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan item for global rank checkpoint: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate items for global rank checkpoint: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close items for global rank checkpoint: %w", err)
		}

		width := base62Width(len(ids) - 1)
		for index, id := range ids {
			fraction := "a1" + leftPadBase62(index, width) + "1"
			rank := "0|" + fraction
			if _, err := tx.Exec("UPDATE items SET frac_index = ? WHERE id = ?", rank, id); err != nil {
				return fmt.Errorf("write checkpoint rank for item %d: %w", id, err)
			}
		}
		if _, err := tx.Exec(`CREATE UNIQUE INDEX idx_items_frac_index ON items(frac_index)`); err != nil {
			return fmt.Errorf("recreate checkpoint frac_index uniqueness: %w", err)
		}
		if _, err := tx.Exec(`
			UPDATE global_rank_state
			SET active_bucket = 0,
			    target_bucket = NULL,
			    phase = 'stable',
			    direction = NULL,
			    frontier = NULL,
			    lease_owner = NULL,
			    lease_expires_at = NULL,
			    migrated_count = 0,
			    total_count = ?,
			    last_error = NULL,
			    updated_at = CURRENT_TIMESTAMP
			WHERE id = 1`, len(ids)); err != nil {
			return fmt.Errorf("mark global rank checkpoint stable: %w", err)
		}
		return nil
	})
}

func createGlobalRankStateTable(tx Tx, driver string) error {
	var ddl, seed string
	if driver == "postgres" {
		ddl = `
			CREATE TABLE IF NOT EXISTS global_rank_state (
				id INTEGER PRIMARY KEY CHECK (id = 1),
				active_bucket INTEGER NOT NULL CHECK (active_bucket IN (0, 1, 2)),
				target_bucket INTEGER CHECK (target_bucket IS NULL OR target_bucket IN (0, 1, 2)),
				phase TEXT NOT NULL CHECK (phase IN ('legacy', 'stable', 'migrating', 'paused', 'failed')),
				direction TEXT CHECK (direction IS NULL OR direction IN ('high_to_low', 'low_to_high')),
				frontier TEXT,
				lease_owner TEXT,
				lease_expires_at TIMESTAMPTZ,
				migrated_count BIGINT NOT NULL DEFAULT 0 CHECK (migrated_count >= 0),
				total_count BIGINT NOT NULL DEFAULT 0 CHECK (total_count >= 0),
				last_error TEXT,
				updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
				CHECK (target_bucket IS NULL OR target_bucket <> active_bucket),
				CHECK ((phase IN ('legacy', 'stable') AND target_bucket IS NULL AND direction IS NULL) OR phase IN ('migrating', 'paused', 'failed'))
			)`
		seed = `INSERT INTO global_rank_state (id, active_bucket, phase) VALUES (1, 0, 'legacy') ON CONFLICT (id) DO NOTHING`
	} else {
		ddl = `
			CREATE TABLE IF NOT EXISTS global_rank_state (
				id INTEGER PRIMARY KEY CHECK (id = 1),
				active_bucket INTEGER NOT NULL CHECK (active_bucket IN (0, 1, 2)),
				target_bucket INTEGER CHECK (target_bucket IS NULL OR target_bucket IN (0, 1, 2)),
				phase TEXT NOT NULL CHECK (phase IN ('legacy', 'stable', 'migrating', 'paused', 'failed')),
				direction TEXT CHECK (direction IS NULL OR direction IN ('high_to_low', 'low_to_high')),
				frontier TEXT,
				lease_owner TEXT,
				lease_expires_at DATETIME,
				migrated_count INTEGER NOT NULL DEFAULT 0 CHECK (migrated_count >= 0),
				total_count INTEGER NOT NULL DEFAULT 0 CHECK (total_count >= 0),
				last_error TEXT,
				updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				CHECK (target_bucket IS NULL OR target_bucket <> active_bucket),
				CHECK ((phase IN ('legacy', 'stable') AND target_bucket IS NULL AND direction IS NULL) OR phase IN ('migrating', 'paused', 'failed'))
			)`
		seed = `INSERT OR IGNORE INTO global_rank_state (id, active_bucket, phase) VALUES (1, 0, 'legacy')`
	}
	if _, err := tx.Exec(ddl); err != nil {
		return fmt.Errorf("create global rank state table: %w", err)
	}
	if _, err := tx.Exec(seed); err != nil {
		return fmt.Errorf("seed global rank state: %w", err)
	}
	return nil
}

func base62Width(value int) int {
	if value <= 0 {
		return 1
	}
	width := 0
	for value > 0 {
		width++
		value /= len(checkpointRankDigits)
	}
	return width
}

func leftPadBase62(value, width int) string {
	encoded := ""
	if value == 0 {
		encoded = "0"
	} else {
		n := value
		encoded = ""
		for n > 0 {
			encoded = string(checkpointRankDigits[n%len(checkpointRankDigits)]) + encoded
			n /= len(checkpointRankDigits)
		}
	}
	for len(encoded) < width {
		encoded = "0" + encoded
	}
	return encoded
}
