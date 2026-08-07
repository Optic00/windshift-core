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

	var state GlobalRankState
	if w.db.GetDriverName() == "postgres" {
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
	if migrationLeaseBusy(state, w.owner, now) {
		if err := tx.Commit(); err != nil {
			return GlobalRankMigrationBatchResult{}, fmt.Errorf("commit global rank lease observation: %w", err)
		}
		return GlobalRankMigrationBatchResult{State: state}, nil
	}

	switch state.Phase {
	case GlobalRankPhaseStable:
		target, direction, transitionErr := GlobalRankBucketTransition(state.ActiveBucket)
		if transitionErr != nil {
			return GlobalRankMigrationBatchResult{}, transitionErr
		}
		state.TargetBucket = &target
		migrationDirection := GlobalRankDirection(direction)
		state.Direction = &migrationDirection
		state.Phase = GlobalRankPhaseMigrating
		state.Frontier = nil
		state.MigratedCount = 0
		state.TotalCount, err = countItems(tx)
		if err != nil {
			return GlobalRankMigrationBatchResult{}, err
		}
	case GlobalRankPhasePaused:
		if state.TargetBucket == nil || state.Direction == nil {
			return GlobalRankMigrationBatchResult{}, fmt.Errorf("paused global rank migration has no target or direction")
		}
		state.Phase = GlobalRankPhaseMigrating
	}

	leaseExpiry := now.Add(w.leaseDuration)
	state.LeaseOwner = stringPointer(w.owner)
	state.LeaseExpiresAt = &leaseExpiry
	state.LastError = nil
	if err := SaveGlobalRankState(tx, state); err != nil {
		return GlobalRankMigrationBatchResult{}, err
	}

	rows, err := readGlobalRankMigrationRows(tx, state, w.batchSize, w.db.GetDriverName())
	if err != nil {
		return GlobalRankMigrationBatchResult{}, err
	}
	if len(rows) == 0 {
		if err := completeGlobalRankMigration(tx, &state); err != nil {
			return GlobalRankMigrationBatchResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return GlobalRankMigrationBatchResult{}, fmt.Errorf("commit completed global rank migration: %w", err)
		}
		return GlobalRankMigrationBatchResult{State: state, LeaseAcquired: true, Completed: true}, nil
	}

	for _, row := range rows {
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
		newRank, encodeErr := EncodeGlobalRank(*state.TargetBucket, parsed.Fraction)
		if encodeErr != nil {
			return GlobalRankMigrationBatchResult{}, encodeErr
		}
		result, updateErr := tx.Exec("UPDATE items SET frac_index = ? WHERE id = ? AND frac_index = ?", newRank, row.id, row.rank)
		if updateErr != nil {
			return GlobalRankMigrationBatchResult{}, fmt.Errorf("migrate item %d rank: %w", row.id, updateErr)
		}
		if affected, affectedErr := result.RowsAffected(); affectedErr == nil && affected != 1 {
			return GlobalRankMigrationBatchResult{}, fmt.Errorf("migrate item %d rank: affected %d rows", row.id, affected)
		}
	}

	state.Frontier = stringPointer(rows[len(rows)-1].rank)
	state.MigratedCount += int64(len(rows))
	remaining, err := countRemainingGlobalRankRows(tx, state)
	if err != nil {
		return GlobalRankMigrationBatchResult{}, err
	}
	state.TotalCount, err = countItems(tx)
	if err != nil {
		return GlobalRankMigrationBatchResult{}, err
	}
	completed := remaining == 0
	if completed {
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

type globalRankMigrationRow struct {
	id   int64
	rank string
}

func loadGlobalRankStateForUpdate(tx database.Tx) (GlobalRankState, error) {
	return loadGlobalRankStateWithQuery(tx, " FOR UPDATE")
}

func migrationLeaseBusy(state GlobalRankState, owner string, now time.Time) bool {
	return state.LeaseOwner != nil && *state.LeaseOwner != owner && state.LeaseExpiresAt != nil && state.LeaseExpiresAt.After(now)
}

func readGlobalRankMigrationRows(tx database.Tx, state GlobalRankState, limit int, driver string) ([]globalRankMigrationRow, error) {
	if state.TargetBucket == nil || state.Direction == nil {
		return nil, fmt.Errorf("global rank migration has no target or direction")
	}
	where := "SUBSTR(frac_index, 1, 2) = ?"
	args := []interface{}{fmt.Sprintf("%d|", state.ActiveBucket)}
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
	query := "SELECT id, frac_index FROM items WHERE " + where + " ORDER BY frac_index " + order + ", id " + order + " LIMIT ?"
	if driver == "postgres" {
		query += " FOR UPDATE"
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

func countRemainingGlobalRankRows(tx database.Tx, state GlobalRankState) (int64, error) {
	if state.Frontier == nil || state.Direction == nil {
		return 0, nil
	}
	operator := ">"
	if *state.Direction == GlobalRankDirectionHighToLow {
		operator = "<"
	}
	var count int64
	if err := tx.QueryRow("SELECT COUNT(*) FROM items WHERE SUBSTR(frac_index, 1, 2) = ? AND frac_index "+operator+" ?", fmt.Sprintf("%d|", state.ActiveBucket), *state.Frontier).Scan(&count); err != nil {
		return 0, fmt.Errorf("count remaining global rank rows: %w", err)
	}
	return count, nil
}

func countItems(q interface {
	QueryRow(query string, args ...interface{}) *sql.Row
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
	QueryRow(query string, args ...interface{}) *sql.Row
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
	if err := state.Validate(); err != nil {
		return GlobalRankState{}, fmt.Errorf("validate loaded global rank state: %w", err)
	}
	return state, nil
}
