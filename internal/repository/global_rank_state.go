package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"windshift/internal/database"
)

const globalRankStateRowID = 1

// globalRankAdvisoryLockClass namespaces the PostgreSQL transaction lock that
// coordinates normal rank mutations with a global migration batch. Mutations
// take the shared form (so creates/reorders remain concurrent); the worker
// takes the exclusive form for its bounded transaction. SQLite already has a
// single writer and does not need an additional lock.
const globalRankAdvisoryLockClass = 0x4752 // 'GR'

func acquireGlobalRankMutationLock(tx database.Tx, driver string) error {
	if !database.IsPostgresDriver(driver) {
		return nil
	}
	if _, err := tx.Exec("SELECT pg_advisory_xact_lock_shared(?, ?)", globalRankAdvisoryLockClass, globalRankStateRowID); err != nil {
		return fmt.Errorf("acquire shared global rank lock: %w", err)
	}
	return nil
}

func acquireGlobalRankMigrationLock(tx database.Tx, driver string) error {
	if !database.IsPostgresDriver(driver) {
		return nil
	}
	if _, err := tx.Exec("SELECT pg_advisory_xact_lock(?, ?)", globalRankAdvisoryLockClass, globalRankStateRowID); err != nil {
		return fmt.Errorf("acquire exclusive global rank lock: %w", err)
	}
	return nil
}

// GlobalRankPhase describes the durable lifecycle of the singleton rank
// state. Legacy is used until the 0.8.5 checkpoint converter has rewritten
// existing unprefixed ranks into bucketed form.
type GlobalRankPhase string

const (
	GlobalRankPhaseLegacy    GlobalRankPhase = "legacy"
	GlobalRankPhaseStable    GlobalRankPhase = "stable"
	GlobalRankPhaseMigrating GlobalRankPhase = "migrating"
	GlobalRankPhasePaused    GlobalRankPhase = "paused"
	GlobalRankPhaseFailed    GlobalRankPhase = "failed"
)

type GlobalRankDirection string

const (
	GlobalRankDirectionHighToLow GlobalRankDirection = "high_to_low"
	GlobalRankDirectionLowToHigh GlobalRankDirection = "low_to_high"
)

// GlobalRankState is the durable singleton coordination record for online
// normalization. Nullable fields are pointers so callers can distinguish an
// absent target/frontier/lease from an empty value.
type GlobalRankState struct {
	ActiveBucket   GlobalRankBucket     `json:"active_bucket"`
	TargetBucket   *GlobalRankBucket    `json:"target_bucket,omitempty"`
	Phase          GlobalRankPhase      `json:"phase"`
	Direction      *GlobalRankDirection `json:"direction,omitempty"`
	Frontier       *string              `json:"frontier,omitempty"`
	LeaseOwner     *string              `json:"lease_owner,omitempty"`
	LeaseExpiresAt *time.Time           `json:"lease_expires_at,omitempty"`
	MigratedCount  int64                `json:"migrated_count"`
	TotalCount     int64                `json:"total_count"`
	LastError      *string              `json:"last_error,omitempty"`
}

type GlobalRankMigrationAction string

const (
	GlobalRankMigrationStart  GlobalRankMigrationAction = "start"
	GlobalRankMigrationPause  GlobalRankMigrationAction = "pause"
	GlobalRankMigrationResume GlobalRankMigrationAction = "resume"
	GlobalRankMigrationReset  GlobalRankMigrationAction = "reset"
)

var ErrGlobalRankMigrationConflict = errors.New("global rank migration state conflict")

func (s GlobalRankState) Validate() error {
	if err := validateGlobalRankBucket(s.ActiveBucket); err != nil {
		return err
	}
	switch s.Phase {
	case GlobalRankPhaseLegacy, GlobalRankPhaseStable, GlobalRankPhaseMigrating, GlobalRankPhasePaused, GlobalRankPhaseFailed:
	default:
		return fmt.Errorf("invalid global rank phase %q", s.Phase)
	}
	if s.TargetBucket != nil {
		if err := validateGlobalRankBucket(*s.TargetBucket); err != nil {
			return fmt.Errorf("invalid global rank target: %w", err)
		}
		if *s.TargetBucket == s.ActiveBucket {
			return fmt.Errorf("global rank target bucket must differ from active bucket")
		}
	}
	if s.Direction != nil && *s.Direction != GlobalRankDirectionHighToLow && *s.Direction != GlobalRankDirectionLowToHigh {
		return fmt.Errorf("invalid global rank direction %q", *s.Direction)
	}
	if s.Phase == GlobalRankPhaseLegacy || s.Phase == GlobalRankPhaseStable {
		if s.TargetBucket != nil || s.Direction != nil {
			return fmt.Errorf("%s global rank state cannot have a target or direction", s.Phase)
		}
	}
	if s.Phase == GlobalRankPhaseMigrating && (s.TargetBucket == nil || s.Direction == nil) {
		return fmt.Errorf("migrating global rank state requires target and direction")
	}
	if s.TargetBucket != nil && s.Direction != nil {
		expectedTarget, expectedDirection, err := GlobalRankBucketTransition(s.ActiveBucket)
		if err != nil {
			return err
		}
		if *s.TargetBucket != expectedTarget || string(*s.Direction) != expectedDirection {
			return fmt.Errorf("global rank target %d and direction %q do not match active bucket %d", *s.TargetBucket, *s.Direction, s.ActiveBucket)
		}
	}
	if s.MigratedCount < 0 || s.TotalCount < 0 {
		return fmt.Errorf("global rank progress cannot be negative")
	}
	if s.MigratedCount > s.TotalCount {
		return fmt.Errorf("global rank migrated count %d exceeds total %d", s.MigratedCount, s.TotalCount)
	}
	return nil
}

// LoadGlobalRankState reads the singleton state row.
func LoadGlobalRankState(db database.Database) (GlobalRankState, error) {
	return loadGlobalRankState(db)
}

// ControlGlobalRankMigration applies an explicit operator lifecycle action.
// The state row and the PostgreSQL global-rank advisory lock serialize the
// action with worker batches and normal rank mutations.
func ControlGlobalRankMigration(ctx context.Context, db database.Database, action GlobalRankMigrationAction) (GlobalRankState, error) {
	if ctx == nil {
		return GlobalRankState{}, errors.New("global rank migration control requires a context")
	}
	if db == nil {
		return GlobalRankState{}, errors.New("global rank migration control requires a database")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return GlobalRankState{}, fmt.Errorf("begin global rank migration control: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	driver := db.GetDriverName()
	if err := acquireGlobalRankMigrationLock(tx, driver); err != nil {
		return GlobalRankState{}, err
	}

	var state GlobalRankState
	if database.IsPostgresDriver(driver) {
		state, err = loadGlobalRankStateForUpdate(tx)
	} else {
		state, err = loadGlobalRankState(tx)
	}
	if err != nil {
		return GlobalRankState{}, err
	}

	switch action {
	case GlobalRankMigrationStart:
		if state.Phase != GlobalRankPhaseStable {
			return GlobalRankState{}, globalRankControlConflict(action, state.Phase)
		}
		state, err = startGlobalRankMigration(tx, state)
	case GlobalRankMigrationPause:
		if state.Phase != GlobalRankPhaseMigrating {
			return GlobalRankState{}, globalRankControlConflict(action, state.Phase)
		}
		state.Phase = GlobalRankPhasePaused
		state.LeaseOwner = nil
		state.LeaseExpiresAt = nil
	case GlobalRankMigrationResume:
		if state.Phase != GlobalRankPhasePaused && state.Phase != GlobalRankPhaseFailed {
			return GlobalRankState{}, globalRankControlConflict(action, state.Phase)
		}
		if state.Phase == GlobalRankPhaseFailed {
			if err := validateGlobalRankResumePopulation(tx, state); err != nil {
				return GlobalRankState{}, err
			}
			state.LastError = nil
		}
		state.Phase = GlobalRankPhaseMigrating
		state.LeaseOwner = nil
		state.LeaseExpiresAt = nil
	case GlobalRankMigrationReset:
		if state.Phase != GlobalRankPhaseFailed {
			return GlobalRankState{}, globalRankControlConflict(action, state.Phase)
		}
		if err := validateGlobalRankResetPopulation(tx, state); err != nil {
			return GlobalRankState{}, err
		}
		state.Phase = GlobalRankPhaseStable
		state.TargetBucket = nil
		state.Direction = nil
		state.Frontier = nil
		state.LeaseOwner = nil
		state.LeaseExpiresAt = nil
		state.MigratedCount = 0
		state.TotalCount = 0
		state.LastError = nil
	default:
		return GlobalRankState{}, fmt.Errorf("unsupported global rank migration action %q", action)
	}
	if err != nil {
		return GlobalRankState{}, err
	}
	if err := SaveGlobalRankState(tx, state); err != nil {
		return GlobalRankState{}, err
	}
	if err := tx.Commit(); err != nil {
		return GlobalRankState{}, fmt.Errorf("commit global rank migration control: %w", err)
	}
	return state, nil
}

func globalRankControlConflict(action GlobalRankMigrationAction, phase GlobalRankPhase) error {
	return fmt.Errorf("%w: cannot %s while phase is %s", ErrGlobalRankMigrationConflict, action, phase)
}

type globalRankPopulation struct {
	bucketCounts   [3]int64
	nullCount      int64
	malformedCount int64
}

func inspectGlobalRankPopulation(q interface {
	Query(query string, args ...any) (*sql.Rows, error)
}) (globalRankPopulation, error) {
	var population globalRankPopulation
	rows, err := q.Query("SELECT frac_index FROM items")
	if err != nil {
		return population, fmt.Errorf("inspect global rank population: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			return population, fmt.Errorf("scan global rank population: %w", err)
		}
		if !value.Valid {
			population.nullCount++
			continue
		}
		rank, err := ParseGlobalRank(value.String)
		if err != nil {
			population.malformedCount++
			continue
		}
		population.bucketCounts[rank.Bucket]++
	}
	if err := rows.Err(); err != nil {
		return population, fmt.Errorf("iterate global rank population: %w", err)
	}
	return population, nil
}

func validateGlobalRankResetPopulation(tx database.Tx, state GlobalRankState) error {
	population, err := inspectGlobalRankPopulation(tx)
	if err != nil {
		return err
	}
	invalidCount := population.nullCount + population.malformedCount
	outsideActive := int64(0)
	for bucket, count := range population.bucketCounts {
		if GlobalRankBucket(bucket) != state.ActiveBucket {
			outsideActive += count
		}
	}
	if outsideActive > 0 {
		return fmt.Errorf("%w: cannot reset failed global rank migration while %d items remain outside active bucket %d and %d malformed or NULL ranks remain; repair invalid ranks and resume the migration to completion",
			ErrGlobalRankMigrationConflict, outsideActive, state.ActiveBucket, invalidCount)
	}
	if invalidCount > 0 {
		return fmt.Errorf("%w: cannot reset failed global rank migration while %d malformed or NULL item ranks remain; repair them, then retry reset or resume if target-bucket ranks remain",
			ErrGlobalRankMigrationConflict, invalidCount)
	}
	return nil
}

func validateGlobalRankResumePopulation(tx database.Tx, state GlobalRankState) error {
	population, err := inspectGlobalRankPopulation(tx)
	if err != nil {
		return err
	}
	if population.nullCount > 0 || population.malformedCount > 0 {
		return fmt.Errorf("%w: cannot resume failed global rank migration while %d malformed or NULL item ranks remain; repair them and resume again",
			ErrGlobalRankMigrationConflict, population.nullCount+population.malformedCount)
	}
	for bucket, count := range population.bucketCounts {
		globalBucket := GlobalRankBucket(bucket)
		if count > 0 && globalBucket != state.ActiveBucket && (state.TargetBucket == nil || globalBucket != *state.TargetBucket) {
			return fmt.Errorf("%w: cannot resume failed global rank migration while %d items remain in unexpected bucket %d; move them into the active or target bucket first",
				ErrGlobalRankMigrationConflict, count, bucket)
		}
	}
	return nil
}

func startGlobalRankMigration(tx database.Tx, state GlobalRankState) (GlobalRankState, error) {
	target, direction, err := GlobalRankBucketTransition(state.ActiveBucket)
	if err != nil {
		return GlobalRankState{}, err
	}
	migrationDirection := GlobalRankDirection(direction)
	state.TargetBucket = &target
	state.Direction = &migrationDirection
	state.Phase = GlobalRankPhaseMigrating
	state.Frontier = nil
	state.LeaseOwner = nil
	state.LeaseExpiresAt = nil
	state.MigratedCount = 0
	state.TotalCount, err = countItems(tx)
	state.LastError = nil
	if err != nil {
		return GlobalRankState{}, err
	}
	return state, nil
}

func loadGlobalRankState(q interface {
	QueryRow(query string, args ...any) *sql.Row
}) (GlobalRankState, error) {
	return loadGlobalRankStateWithQuery(q, "")
}

// SaveGlobalRankState persists a validated state inside the caller's
// transaction. Keeping this operation transaction-scoped lets the future
// balancer advance its frontier atomically with each bounded item batch.
func SaveGlobalRankState(tx database.Tx, state GlobalRankState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	result, err := tx.Exec(`
		UPDATE global_rank_state
		SET active_bucket = ?, target_bucket = ?, phase = ?, direction = ?,
		    frontier = ?, lease_owner = ?, lease_expires_at = ?,
		    migrated_count = ?, total_count = ?, last_error = ?,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		state.ActiveBucket,
		nullableGlobalRankBucket(state.TargetBucket),
		state.Phase,
		nullableGlobalRankDirection(state.Direction),
		state.Frontier,
		state.LeaseOwner,
		state.LeaseExpiresAt,
		state.MigratedCount,
		state.TotalCount,
		state.LastError,
		globalRankStateRowID,
	)
	if err != nil {
		return fmt.Errorf("save global rank state: %w", err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected != 1 {
		return fmt.Errorf("save global rank state: affected %d rows", affected)
	}
	return nil
}

func nullableGlobalRankBucket(bucket *GlobalRankBucket) any {
	if bucket == nil {
		return nil
	}
	return *bucket
}

func nullableGlobalRankDirection(direction *GlobalRankDirection) any {
	if direction == nil {
		return nil
	}
	return *direction
}
