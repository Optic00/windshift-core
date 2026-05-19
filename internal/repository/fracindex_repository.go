package repository

import (
	"database/sql"
	"errors"
	"fmt"

	"windshift/internal/database"
)

// FracIndexRepository inspects the items.frac_index column for the admin
// diagnostics panel. Driver-aware queries apply explicit COLLATE "C" on
// Postgres so the byte-wise ordering used by the KeyBetween generator can
// be compared against the column's stored linguistic ordering.
type FracIndexRepository struct {
	db database.Database
}

// NewFracIndexRepository constructs a new repository.
func NewFracIndexRepository(db database.Database) *FracIndexRepository {
	return &FracIndexRepository{db: db}
}

// FracIndexDBStats describes the persisted frac_index state.
//
// CollationMismatch is the smoking gun for a column that was created without
// COLLATE "C" — ORDER BY then returns a value that is not the byte-wise max,
// so the generator hands out successors that already exist.
//
// PredictedCollision is non-nil when the next key the generator would produce
// (computed by the caller via services.KeyBetween over ByteMax) is already
// present in the table — i.e. the next append would fail the UNIQUE index.
type FracIndexDBStats struct {
	ColumnCollation    *string  `json:"column_collation"`            // NULL if default DB collation
	DefaultCollation   string   `json:"default_collation,omitempty"` // datcollate of the current DB (Postgres only)
	LinguisticMax      *string  `json:"linguistic_max"`              // ORDER BY frac_index DESC LIMIT 1
	ByteMax            *string  `json:"byte_max"`                    // ORDER BY frac_index COLLATE "C" DESC LIMIT 1
	Top10ByByte        []string `json:"top_10_by_byte"`
	NotNullCount       int64    `json:"not_null_count"`
	CollationMismatch  bool     `json:"collation_mismatch"`
	PredictedNext      *string  `json:"predicted_next,omitempty"`
	PredictedCollision *string  `json:"predicted_collision,omitempty"`
}

// GetDBStats inspects items for the diagnostics panel.
//
// Postgres applies COLLATE "C" at query time to compute the byte-wise max
// independent of the column's stored collation. SQLite stores TEXT with
// binary comparison by default; the linguistic vs byte distinction collapses
// and CollationMismatch will always be false.
func (r *FracIndexRepository) GetDBStats() (FracIndexDBStats, error) {
	out := FracIndexDBStats{Top10ByByte: []string{}}
	driver := r.db.GetDriverName()
	isPostgres := driver == "postgres" || driver == "postgresql"

	if isPostgres {
		var collation sql.NullString
		err := r.db.QueryRow(`
			SELECT collation_name FROM information_schema.columns
			WHERE table_name = 'items' AND column_name = 'frac_index'
		`).Scan(&collation)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, fmt.Errorf("read column collation: %w", err)
		}
		if collation.Valid {
			c := collation.String
			out.ColumnCollation = &c
		}

		var defaultCollation sql.NullString
		if err := r.db.QueryRow(`SELECT datcollate FROM pg_database WHERE datname = current_database()`).Scan(&defaultCollation); err == nil && defaultCollation.Valid {
			out.DefaultCollation = defaultCollation.String
		}
	}

	// Linguistic max (uses column collation as-stored)
	var lingMax sql.NullString
	if err := r.db.QueryRow(`
		SELECT frac_index FROM items
		WHERE frac_index IS NOT NULL
		ORDER BY frac_index DESC
		LIMIT 1
	`).Scan(&lingMax); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, fmt.Errorf("read linguistic max: %w", err)
	}
	if lingMax.Valid {
		v := lingMax.String
		out.LinguisticMax = &v
	}

	// Byte-wise max — Postgres needs COLLATE "C" applied at query time;
	// SQLite is already binary so the same value falls out.
	byteQuery := `
		SELECT frac_index FROM items
		WHERE frac_index IS NOT NULL
		ORDER BY frac_index DESC
		LIMIT 1
	`
	if isPostgres {
		byteQuery = `
			SELECT frac_index FROM items
			WHERE frac_index IS NOT NULL
			ORDER BY frac_index COLLATE "C" DESC
			LIMIT 1
		`
	}
	var byteMax sql.NullString
	if err := r.db.QueryRow(byteQuery).Scan(&byteMax); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, fmt.Errorf("read byte max: %w", err)
	}
	if byteMax.Valid {
		v := byteMax.String
		out.ByteMax = &v
	}

	if out.LinguisticMax != nil && out.ByteMax != nil && *out.LinguisticMax != *out.ByteMax {
		out.CollationMismatch = true
	}

	// Top 10 by byte order
	top10Query := `
		SELECT frac_index FROM items
		WHERE frac_index IS NOT NULL
		ORDER BY frac_index DESC
		LIMIT 10
	`
	if isPostgres {
		top10Query = `
			SELECT frac_index FROM items
			WHERE frac_index IS NOT NULL
			ORDER BY frac_index COLLATE "C" DESC
			LIMIT 10
		`
	}
	rows, err := r.db.Query(top10Query)
	if err != nil {
		return out, fmt.Errorf("read top 10: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return out, fmt.Errorf("scan top 10: %w", err)
		}
		out.Top10ByByte = append(out.Top10ByByte, v)
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("iterate top 10: %w", err)
	}

	if err := r.db.QueryRow(`SELECT COUNT(*) FROM items WHERE frac_index IS NOT NULL`).Scan(&out.NotNullCount); err != nil {
		return out, fmt.Errorf("count: %w", err)
	}

	return out, nil
}

// ProbePredictedKey returns the existing value when predictedNext already
// exists in items.frac_index. Equality uses the column collation, which is
// the same comparison the UNIQUE index enforces — a positive result here
// matches what a real INSERT would hit.
func (r *FracIndexRepository) ProbePredictedKey(predictedNext string) (*string, error) {
	var exists string
	err := r.db.QueryRow(`SELECT frac_index FROM items WHERE frac_index = ? LIMIT 1`, predictedNext).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("probe predicted key: %w", err)
	}
	return &exists, nil
}
