package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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

func (r *FracIndexRepository) GetGlobalRankState() (GlobalRankState, error) {
	// Diagnostics must be able to return malformed durable markers as data;
	// worker/control paths continue to use the validated loader and refuse them.
	return loadGlobalRankStateUncheckedWithQuery(r.db, "")
}

func (r *FracIndexRepository) ControlGlobalRankMigration(ctx context.Context, action GlobalRankMigrationAction) (GlobalRankState, error) {
	return ControlGlobalRankMigration(ctx, r.db, action)
}

// FracIndexDBStats describes the persisted frac_index state.
//
// CollationMismatch is the smoking gun for a column that was created without
// COLLATE "C" — ORDER BY then returns a value that is not the byte-wise max,
// so the generator hands out successors that already exist.
//
// PredictedCollision is non-nil when the next key the generator would produce
// (computed by the caller via KeyBetween over ByteMax) is already
// present in the table — i.e. the next append would fail the UNIQUE index.
type FracIndexDBStats struct {
	ColumnCollation    *string  `json:"column_collation"`            // NULL if default DB collation
	DefaultCollation   string   `json:"default_collation,omitempty"` // datcollate of the current DB (Postgres only)
	LinguisticMax      *string  `json:"linguistic_max"`              // ORDER BY frac_index DESC LIMIT 1
	ByteMax            *string  `json:"byte_max"`                    // ORDER BY frac_index COLLATE "C" DESC LIMIT 1
	Top10ByByte        []string `json:"top_10_by_byte"`
	NotNullCount       int64    `json:"not_null_count"`
	MaxRankLength      int64    `json:"max_rank_length"`
	OverlongRankCount  int64    `json:"overlong_rank_count"`
	CollationMismatch  bool     `json:"collation_mismatch"`
	PredictedNext      *string  `json:"predicted_next,omitempty"`
	PredictedCollision *string  `json:"predicted_collision,omitempty"`
}

// GlobalRankIntegrity describes operator-facing invariants for the durable
// three-bucket migration state and all persisted item ranks.
type GlobalRankIntegrity struct {
	BucketCounts           map[string]int64 `json:"bucket_counts"`
	NullRankCount          int64            `json:"null_rank_count"`
	MalformedRankCount     int64            `json:"malformed_rank_count"`
	DuplicateRankCount     int64            `json:"duplicate_rank_count"`
	UnexpectedBucketCount  int64            `json:"unexpected_bucket_count"`
	FrontierViolationCount int64            `json:"frontier_violation_count"`
	LeaseStalled           bool             `json:"lease_stalled"`
	PopulationSplit        bool             `json:"population_split"`
	Healthy                bool             `json:"healthy"`
	Issues                 []string         `json:"issues"`
}

// GetGlobalRankIntegrity checks strict rank grammar, bucket membership,
// duplicates, frontier placement, and lease liveness. It is intentionally an
// explicit diagnostics scan rather than part of a hot request path.
func (r *FracIndexRepository) GetGlobalRankIntegrity(state GlobalRankState, now time.Time) (GlobalRankIntegrity, error) {
	out := GlobalRankIntegrity{
		BucketCounts: map[string]int64{"0": 0, "1": 0, "2": 0},
		Issues:       []string{},
	}
	if err := state.Validate(); err != nil {
		out.Issues = append(out.Issues, "invalid durable migration state: "+err.Error())
	}

	var frontierRank *GlobalRank
	if state.Frontier != nil {
		parsed, err := ParseGlobalRank(*state.Frontier)
		if err != nil || parsed.Bucket != state.ActiveBucket {
			out.Issues = append(out.Issues, "migration frontier is not a valid active-bucket rank")
		} else {
			frontierRank = &parsed
		}
	} else if state.MigratedCount > 0 && state.Phase != GlobalRankPhaseStable {
		out.Issues = append(out.Issues, "migration progress has no frontier")
	}

	rows, err := r.db.Query("SELECT frac_index FROM items")
	if err != nil {
		return out, fmt.Errorf("read ranks for global integrity: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			return out, fmt.Errorf("scan rank for global integrity: %w", err)
		}
		if !value.Valid {
			out.NullRankCount++
			continue
		}
		rank, err := ParseGlobalRank(value.String)
		if err != nil {
			out.MalformedRankCount++
			continue
		}
		out.BucketCounts[fmt.Sprintf("%d", rank.Bucket)]++
		if rank.Bucket != state.ActiveBucket && (state.TargetBucket == nil || rank.Bucket != *state.TargetBucket) {
			out.UnexpectedBucketCount++
		}
		if frontierRank != nil && state.TargetBucket != nil && state.Direction != nil {
			// The frontier constrains only the unprocessed active-bucket range.
			// Target-bucket payloads are freshly balanced into an independent
			// namespace and are intentionally not comparable with the old payload.
			switch *state.Direction {
			case GlobalRankDirectionHighToLow:
				if rank.Bucket == state.ActiveBucket && rank.Fraction >= frontierRank.Fraction {
					out.FrontierViolationCount++
				}
			case GlobalRankDirectionLowToHigh:
				if rank.Bucket == state.ActiveBucket && rank.Fraction <= frontierRank.Fraction {
					out.FrontierViolationCount++
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("iterate ranks for global integrity: %w", err)
	}
	if err := rows.Close(); err != nil {
		return out, fmt.Errorf("close ranks for global integrity: %w", err)
	}
	occupiedBuckets := 0
	for _, count := range out.BucketCounts {
		if count > 0 {
			occupiedBuckets++
		}
	}
	out.PopulationSplit = occupiedBuckets > 1

	if err := r.db.QueryRow(`
		SELECT COUNT(*)
		FROM (
			SELECT frac_index
			FROM items
			GROUP BY frac_index
			HAVING COUNT(*) > 1
		) duplicate_ranks`).Scan(&out.DuplicateRankCount); err != nil {
		return out, fmt.Errorf("count duplicate global ranks: %w", err)
	}

	if (state.LeaseOwner == nil) != (state.LeaseExpiresAt == nil) {
		out.Issues = append(out.Issues, "migration lease owner and expiry are inconsistent")
	}
	if (state.Phase == GlobalRankPhaseMigrating || state.Phase == GlobalRankPhasePaused || state.Phase == GlobalRankPhaseFailed) &&
		(state.TargetBucket == nil || state.Direction == nil) {
		out.Issues = append(out.Issues, "online migration state has no target or direction")
	}
	if state.Phase == GlobalRankPhasePaused && (state.LeaseOwner != nil || state.LeaseExpiresAt != nil) {
		out.Issues = append(out.Issues, "paused migration still owns a lease")
	}
	if (state.Phase == GlobalRankPhaseStable || state.Phase == GlobalRankPhaseLegacy) &&
		(state.Frontier != nil || state.LeaseOwner != nil || state.LeaseExpiresAt != nil) {
		out.Issues = append(out.Issues, "inactive rank state retains migration markers")
	}
	if state.Phase == GlobalRankPhaseMigrating && state.LeaseExpiresAt != nil && !state.LeaseExpiresAt.After(now) {
		out.LeaseStalled = true
		out.Issues = append(out.Issues, "migration lease is expired")
	}
	if state.Phase == GlobalRankPhaseFailed {
		out.Issues = append(out.Issues, "migration is failed")
		if state.LastError == nil || *state.LastError == "" {
			out.Issues = append(out.Issues, "failed migration has no failure reason")
		}
	}
	if state.Phase == GlobalRankPhaseLegacy {
		out.Issues = append(out.Issues, "legacy rank conversion is incomplete")
	}
	if out.NullRankCount > 0 {
		out.Issues = append(out.Issues, "NULL item ranks exist")
	}
	if out.MalformedRankCount > 0 {
		out.Issues = append(out.Issues, "malformed item ranks exist")
	}
	if out.DuplicateRankCount > 0 {
		out.Issues = append(out.Issues, "duplicate item ranks exist")
	}
	if out.UnexpectedBucketCount > 0 {
		out.Issues = append(out.Issues, "items exist outside the active and target buckets")
	}
	if out.PopulationSplit && state.Phase != GlobalRankPhaseMigrating && state.Phase != GlobalRankPhasePaused {
		out.Issues = append(out.Issues, "item ranks are split across buckets outside an active migration")
	}
	if out.FrontierViolationCount > 0 {
		out.Issues = append(out.Issues, "item ranks violate the durable migration frontier")
	}
	out.Healthy = len(out.Issues) == 0
	return out, nil
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
	isPostgres := database.IsPostgresDriver(driver)

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

	if err := r.db.QueryRow(`
		SELECT COUNT(*),
			COALESCE(MAX(LENGTH(frac_index)), 0),
			COALESCE(SUM(CASE WHEN LENGTH(frac_index) > ? THEN 1 ELSE 0 END), 0)
		FROM items
		WHERE frac_index IS NOT NULL`, fracIndexRebalanceLengthThreshold).Scan(&out.NotNullCount, &out.MaxRankLength, &out.OverlongRankCount); err != nil {
		return out, fmt.Errorf("read rank length statistics: %w", err)
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
