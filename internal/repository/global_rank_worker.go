package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"windshift/internal/database"
)

const (
	DefaultGlobalRankMigrationBatchSize = 128
	DefaultGlobalRankMigrationLease     = 30 * time.Second
)

// GlobalRankMigrationBatchResult describes one bounded worker transaction.
// LeaseAcquired is false when another live owner currently holds the lease.
type GlobalRankMigrationBatchResult struct {
	State         GlobalRankState
	Migrated      int
	Remaining     int64
	LeaseAcquired bool
	Completed     bool
}

// GlobalRankMigrationWorker advances one bounded, resumable global-rank batch.
// The worker starts a migration from stable state, resumes an expired lease,
// and commits item updates together with the durable frontier.
type GlobalRankMigrationWorker struct {
	db            database.Database
	owner         string
	batchSize     int
	leaseDuration time.Duration
	now           func() time.Time
	// beforeCompletion is a deterministic white-box test barrier. Production
	// constructors leave it nil.
	beforeCompletion func()
}

func NewGlobalRankMigrationWorker(db database.Database, owner string, batchSize int, leaseDuration time.Duration) *GlobalRankMigrationWorker {
	if batchSize <= 0 {
		batchSize = DefaultGlobalRankMigrationBatchSize
	}
	if leaseDuration <= 0 {
		leaseDuration = DefaultGlobalRankMigrationLease
	}
	return &GlobalRankMigrationWorker{
		db:            db,
		owner:         owner,
		batchSize:     batchSize,
		leaseDuration: leaseDuration,
		now:           func() time.Time { return time.Now().UTC() },
	}
}

// Run executes one bounded migration transaction. A process killed after the
// commit resumes from the saved frontier; a process killed before commit leaves
// the prior frontier intact and the lease expires for another owner.
func (w *GlobalRankMigrationWorker) Run(ctx context.Context) (GlobalRankMigrationBatchResult, error) {
	if w == nil || w.db == nil {
		return GlobalRankMigrationBatchResult{}, errors.New("global rank migration worker requires a database")
	}
	if w.owner == "" {
		return GlobalRankMigrationBatchResult{}, errors.New("global rank migration worker requires an owner")
	}
	if ctx == nil {
		return GlobalRankMigrationBatchResult{}, errors.New("global rank migration worker requires a context")
	}
	now := w.now().UTC()
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return GlobalRankMigrationBatchResult{}, fmt.Errorf("begin global rank migration batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	driver := w.db.GetDriverName()
	if err := acquireGlobalRankMigrationLock(tx, driver); err != nil {
		return GlobalRankMigrationBatchResult{}, err
	}

	var state GlobalRankState
	if database.IsPostgresDriver(driver) {
		// PostgreSQL needs an explicit row lock so two balancers cannot both
		// claim a lease; SQLite's write transaction already serializes them.
		state, err = loadGlobalRankStateForUpdate(tx)
	} else {
		state, err = loadGlobalRankState(tx)
	}
	if err != nil {
		return GlobalRankMigrationBatchResult{}, err
	}

	if state.Phase == GlobalRankPhaseLegacy {
		return GlobalRankMigrationBatchResult{}, fmt.Errorf("global rank migration requires the canonical checkpoint")
	}
	if state.Phase == GlobalRankPhaseFailed {
		return GlobalRankMigrationBatchResult{}, fmt.Errorf("global rank migration is failed: %s", globalRankLastError(state))
	}
	if state.Phase == GlobalRankPhasePaused {
		return GlobalRankMigrationBatchResult{State: state}, nil
	}
	if migrationLeaseBusy(state, w.owner, now) {
		if err := tx.Commit(); err != nil {
			return GlobalRankMigrationBatchResult{}, fmt.Errorf("commit global rank lease observation: %w", err)
		}
		return GlobalRankMigrationBatchResult{State: state}, nil
	}

	if state.Phase == GlobalRankPhaseStable {
		state, err = startGlobalRankMigration(tx, state)
		if err != nil {
			return GlobalRankMigrationBatchResult{}, err
		}
	}

	leaseExpiry := now.Add(w.leaseDuration)
	state.LeaseOwner = stringPointer(w.owner)
	state.LeaseExpiresAt = &leaseExpiry
	state.LastError = nil
	if err := SaveGlobalRankState(tx, state); err != nil {
		return GlobalRankMigrationBatchResult{}, err
	}

	rows, err := readGlobalRankMigrationRows(tx, state, w.batchSize, driver)
	if err != nil {
		return GlobalRankMigrationBatchResult{}, err
	}
	if len(rows) == 0 {
		if w.beforeCompletion != nil {
			w.beforeCompletion()
		}
		if err := completeGlobalRankMigration(tx, &state); err != nil {
			return GlobalRankMigrationBatchResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return GlobalRankMigrationBatchResult{}, fmt.Errorf("commit completed global rank migration: %w", err)
		}
		return GlobalRankMigrationBatchResult{State: state, LeaseAcquired: true, Completed: true}, nil
	}

	fractions, err := generateGlobalRankMigrationFractions(tx, state, len(rows), driver)
	if err != nil {
		return GlobalRankMigrationBatchResult{}, err
	}
	updates := make([]fracIndexUpdate, 0, len(rows))
	for index, row := range rows {
		parsed, parseErr := ParseGlobalRank(row.rank)
		if parseErr != nil || parsed.Bucket != state.ActiveBucket {
			failure := fmt.Errorf("item %d has invalid active-bucket rank %q", row.id, row.rank)
			state.Phase = GlobalRankPhaseFailed
			state.LastError = stringPointer(failure.Error())
			state.LeaseOwner = nil
			state.LeaseExpiresAt = nil
			if saveErr := SaveGlobalRankState(tx, state); saveErr != nil {
				return GlobalRankMigrationBatchResult{}, saveErr
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return GlobalRankMigrationBatchResult{}, fmt.Errorf("commit failed global rank migration state: %w", commitErr)
			}
			return GlobalRankMigrationBatchResult{State: state, LeaseAcquired: true}, failure
		}
		newRank, encodeErr := EncodeGlobalRank(*state.TargetBucket, fractions[index])
		if encodeErr != nil {
			return GlobalRankMigrationBatchResult{}, encodeErr
		}
		updates = append(updates, fracIndexUpdate{id: row.id, key: newRank})
	}
	if err := updateGlobalRankMigrationRows(tx, updates); err != nil {
		return GlobalRankMigrationBatchResult{}, err
	}

	state.Frontier = stringPointer(rows[len(rows)-1].rank)
	remaining, err := countRemainingGlobalRankRows(tx, state)
	if err != nil {
		return GlobalRankMigrationBatchResult{}, err
	}
	state.TotalCount, err = countItems(tx)
	if err != nil {
		return GlobalRankMigrationBatchResult{}, err
	}
	// Recompute progress from current durable membership rather than adding the
	// batch size. Concurrent creates/deletes between batches can change the
	// population; derived progress stays bounded and includes rows born directly
	// in the target bucket.
	state.MigratedCount = state.TotalCount - remaining
	completed := remaining == 0
	if completed {
		if w.beforeCompletion != nil {
			w.beforeCompletion()
		}
		if err := completeGlobalRankMigration(tx, &state); err != nil {
			return GlobalRankMigrationBatchResult{}, err
		}
	} else if err := SaveGlobalRankState(tx, state); err != nil {
		return GlobalRankMigrationBatchResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return GlobalRankMigrationBatchResult{}, fmt.Errorf("commit global rank migration batch: %w", err)
	}
	return GlobalRankMigrationBatchResult{
		State:         state,
		Migrated:      len(rows),
		Remaining:     remaining,
		LeaseAcquired: true,
		Completed:     completed,
	}, nil
}

// generateGlobalRankMigrationFractions assigns a fresh, balanced fractional
// payload to every migrated row. The target bucket is an independent namespace,
// so each batch can extend the already-migrated target range without colliding
// with active-bucket ranks. High-to-low transitions prepend below the target
// minimum; low-to-high transitions append above the target maximum.
func generateGlobalRankMigrationFractions(tx database.Tx, state GlobalRankState, count int, driver string) ([]string, error) {
	if count <= 0 {
		return nil, nil
	}
	if state.TargetBucket == nil || state.Direction == nil {
		return nil, fmt.Errorf("global rank migration has no target or direction")
	}

	left, right := "", ""
	boundary, found, err := readGlobalRankMigrationTargetBoundary(tx, *state.TargetBucket, *state.Direction, driver)
	if err != nil {
		return nil, err
	}
	if found {
		parsed, err := ParseGlobalRank(boundary)
		if err != nil || parsed.Bucket != *state.TargetBucket {
			return nil, fmt.Errorf("invalid target-bucket migration boundary %q", boundary)
		}
		if *state.Direction == GlobalRankDirectionHighToLow {
			right = parsed.Fraction
		} else {
			left = parsed.Fraction
		}
	}

	fractions, err := generateEvenlySpacedFracKeys(left, right, count)
	if err != nil {
		return nil, fmt.Errorf("generate balanced global rank batch: %w", err)
	}
	if *state.Direction == GlobalRankDirectionHighToLow {
		// Migration rows are read from highest to lowest, while the generated
		// fractional keys are ascending.
		for i, j := 0, len(fractions)-1; i < j; i, j = i+1, j-1 {
			fractions[i], fractions[j] = fractions[j], fractions[i]
		}
	}
	return fractions, nil
}

func readGlobalRankMigrationTargetBoundary(tx database.Tx, bucket GlobalRankBucket, direction GlobalRankDirection, driver string) (boundary string, found bool, err error) {
	lower, upper := globalRankBucketBounds(bucket)
	order := "DESC"
	if direction == GlobalRankDirectionHighToLow {
		order = "ASC"
	}
	query := `SELECT frac_index FROM items
		WHERE frac_index >= ? AND frac_index < ?
		ORDER BY frac_index ` + order + ` LIMIT 1`
	if database.IsPostgresDriver(driver) {
		query += " FOR UPDATE"
	}
	if err := tx.QueryRow(query, lower, upper).Scan(&boundary); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read global rank target boundary: %w", err)
	}
	return boundary, true, nil
}

type globalRankMigrationRow struct {
	id   int64
	rank string
}

// updateGlobalRankMigrationRows rewrites one bounded batch with a single SQL
// statement. The prior one-statement-per-row loop made a 128-row batch hold
// the migration transaction across 128 database round trips, which caused
// visible latency spikes for deep-page list scans at 100k rows. The worker has
// already locked/serialized the selected IDs, and RowsAffected preserves the
// all-or-nothing guard against a row disappearing unexpectedly.
func updateGlobalRankMigrationRows(tx database.Tx, updates []fracIndexUpdate) error {
	if err := updateFracIndexes(tx, updates); err != nil {
		return fmt.Errorf("migrate global rank batch: %w", err)
	}
	return nil
}

func loadGlobalRankStateForUpdate(tx database.Tx) (GlobalRankState, error) {
	return loadGlobalRankStateWithQuery(tx, " FOR UPDATE")
}

func migrationLeaseBusy(state GlobalRankState, owner string, now time.Time) bool {
	return state.LeaseOwner != nil && *state.LeaseOwner != owner && state.LeaseExpiresAt != nil && state.LeaseExpiresAt.After(now)
}

func readGlobalRankMigrationRows(tx database.Tx, state GlobalRankState, limit int, driver string) ([]globalRankMigrationRow, error) {
	query, args, err := globalRankMigrationRowsQuery(state, limit, driver)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("read global rank migration batch: %w", err)
	}
	defer rows.Close()
	out := make([]globalRankMigrationRow, 0, limit)
	for rows.Next() {
		var row globalRankMigrationRow
		if err := rows.Scan(&row.id, &row.rank); err != nil {
			return nil, fmt.Errorf("scan global rank migration batch: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate global rank migration batch: %w", err)
	}
	return out, nil
}

func globalRankMigrationRowsQuery(state GlobalRankState, limit int, driver string) (query string, args []any, err error) {
	if state.TargetBucket == nil || state.Direction == nil {
		return "", nil, fmt.Errorf("global rank migration has no target or direction")
	}
	lowerBucketBound, upperBucketBound := globalRankBucketBounds(state.ActiveBucket)
	// A direct byte range keeps the unique frac_index index usable. Applying
	// SUBSTR to every row forced a table/index scan for each bounded batch.
	where := "frac_index >= ? AND frac_index < ?"
	args = []any{lowerBucketBound, upperBucketBound}
	order := "ASC"
	if *state.Direction == GlobalRankDirectionHighToLow {
		order = "DESC"
	}
	if state.Frontier != nil {
		operator := ">"
		if *state.Direction == GlobalRankDirectionHighToLow {
			operator = "<"
		}
		where += " AND frac_index " + operator + " ?"
		args = append(args, *state.Frontier)
	}
	args = append(args, limit)
	query = "SELECT id, frac_index FROM items WHERE " + where + " ORDER BY frac_index " + order + ", id " + order + " LIMIT ?"
	if database.IsPostgresDriver(driver) {
		query += " FOR UPDATE"
	}
	return query, args, nil
}

func countRemainingGlobalRankRows(tx database.Tx, state GlobalRankState) (int64, error) {
	if state.Frontier == nil || state.Direction == nil {
		return 0, nil
	}
	operator := ">"
	if *state.Direction == GlobalRankDirectionHighToLow {
		operator = "<"
	}
	lowerBucketBound, upperBucketBound := globalRankBucketBounds(state.ActiveBucket)
	var count int64
	if err := tx.QueryRow("SELECT COUNT(*) FROM items WHERE frac_index >= ? AND frac_index < ? AND frac_index "+operator+" ?", lowerBucketBound, upperBucketBound, *state.Frontier).Scan(&count); err != nil {
		return 0, fmt.Errorf("count remaining global rank rows: %w", err)
	}
	return count, nil
}

func globalRankBucketBounds(bucket GlobalRankBucket) (lower, upper string) {
	return fmt.Sprintf("%d|", bucket), fmt.Sprintf("%d|", int(bucket)+1)
}

func countItems(q interface {
	QueryRow(query string, args ...any) *sql.Row
}) (int64, error) {
	var count int64
	if err := q.QueryRow("SELECT COUNT(*) FROM items").Scan(&count); err != nil {
		return 0, fmt.Errorf("count global rank items: %w", err)
	}
	return count, nil
}

func completeGlobalRankMigration(tx database.Tx, state *GlobalRankState) error {
	if state.TargetBucket == nil {
		return fmt.Errorf("complete global rank migration has no target bucket")
	}
	state.ActiveBucket = *state.TargetBucket
	state.TargetBucket = nil
	state.Phase = GlobalRankPhaseStable
	state.Direction = nil
	state.Frontier = nil
	state.LeaseOwner = nil
	state.LeaseExpiresAt = nil
	state.MigratedCount = 0
	return SaveGlobalRankState(tx, *state)
}

func globalRankLastError(state GlobalRankState) string {
	if state.LastError == nil || *state.LastError == "" {
		return "no failure reason recorded"
	}
	return *state.LastError
}

func stringPointer(value string) *string {
	return &value
}

func loadGlobalRankStateWithQuery(q interface {
	QueryRow(query string, args ...any) *sql.Row
}, suffix string) (GlobalRankState, error) {
	state, err := loadGlobalRankStateUncheckedWithQuery(q, suffix)
	if err != nil {
		return GlobalRankState{}, err
	}
	if err := state.Validate(); err != nil {
		return GlobalRankState{}, fmt.Errorf("validate loaded global rank state: %w", err)
	}
	return state, nil
}

func loadGlobalRankStateUncheckedWithQuery(q interface {
	QueryRow(query string, args ...any) *sql.Row
}, suffix string) (GlobalRankState, error) {
	var state GlobalRankState
	var targetBucket sql.NullInt64
	var direction, frontier, leaseOwner, lastError sql.NullString
	var leaseExpiresAt sql.NullTime
	if err := q.QueryRow(`
		SELECT active_bucket, target_bucket, phase, direction, frontier,
		       lease_owner, lease_expires_at, migrated_count, total_count, last_error
		FROM global_rank_state
		WHERE id = ?`+suffix, globalRankStateRowID).Scan(
		&state.ActiveBucket,
		&targetBucket,
		&state.Phase,
		&direction,
		&frontier,
		&leaseOwner,
		&leaseExpiresAt,
		&state.MigratedCount,
		&state.TotalCount,
		&lastError,
	); err != nil {
		return GlobalRankState{}, fmt.Errorf("load global rank state: %w", err)
	}
	if targetBucket.Valid {
		if targetBucket.Int64 < int64(GlobalRankBucket0) || targetBucket.Int64 > int64(GlobalRankBucket2) {
			return GlobalRankState{}, fmt.Errorf("load global rank state: invalid target bucket %d", targetBucket.Int64)
		}
		bucket := GlobalRankBucket(targetBucket.Int64)
		state.TargetBucket = &bucket
	}
	if direction.Valid {
		value := GlobalRankDirection(direction.String)
		state.Direction = &value
	}
	if frontier.Valid {
		state.Frontier = &frontier.String
	}
	if leaseOwner.Valid {
		state.LeaseOwner = &leaseOwner.String
	}
	if leaseExpiresAt.Valid {
		state.LeaseExpiresAt = &leaseExpiresAt.Time
	}
	if lastError.Valid {
		state.LastError = &lastError.String
	}
	return state, nil
}
