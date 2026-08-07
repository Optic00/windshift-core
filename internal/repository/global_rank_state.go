package repository

import (
	"database/sql"
	"fmt"
	"time"

	"windshift/internal/database"
)

const globalRankStateRowID = 1

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
	ActiveBucket   GlobalRankBucket
	TargetBucket   *GlobalRankBucket
	Phase          GlobalRankPhase
	Direction      *GlobalRankDirection
	Frontier       *string
	LeaseOwner     *string
	LeaseExpiresAt *time.Time
	MigratedCount  int64
	TotalCount     int64
	LastError      *string
}

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

func loadGlobalRankState(q interface {
	QueryRow(query string, args ...interface{}) *sql.Row
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

func nullableGlobalRankBucket(bucket *GlobalRankBucket) interface{} {
	if bucket == nil {
		return nil
	}
	return *bucket
}

func nullableGlobalRankDirection(direction *GlobalRankDirection) interface{} {
	if direction == nil {
		return nil
	}
	return *direction
}
