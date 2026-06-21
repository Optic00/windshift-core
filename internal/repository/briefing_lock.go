package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// briefingLockDuration is how long a generation claim is held. Briefings call
// the LLM end to end inside DefaultRequestTimeout (5m); this lease must cover
// that plus slack for the surrounding context-gathering queries, while staying
// short enough that a crashed holder's lease expires well before the next tick
// (the scheduler runs every 6h). 10 minutes bounds a wedged run to one tick.
const briefingLockDuration = 10 * time.Minute

// ErrBriefingAlreadyRunning is returned by ClaimBriefing when another instance
// already holds an unexpired generation lease for the (user, date). Callers
// treat it as "nothing to do" rather than a failure.
var ErrBriefingAlreadyRunning = errors.New("briefing generation already in progress")

// ClaimBriefing atomically acquires the generation lease for one (userID, date)
// pair. It is the cross-instance dedup gate (WI-418): the guarded UPSERT sets
// lock_until only when no unexpired lease exists, so among several app
// instances ticking at once exactly one wins the claim and the rest get
// ErrBriefingAlreadyRunning. A crashed holder self-heals: once lock_until falls
// in the past the row is claimable again on the next tick.
//
// When regenerate is false (the "daily" schedule), a successfully-generated,
// released row (content present, no error) also short-circuits as "already
// running" so we never regenerate a day that already has a good briefing. With
// regenerate true (the "every_6h" schedule) that short-circuit is skipped — we
// re-generate, but the lease still ensures only one instance does so at a time.
func (r *AIRepository) ClaimBriefing(userID int, date string, now time.Time, regenerate bool) (bool, error) {
	lockUntil := now.Add(briefingLockDuration)

	if !regenerate {
		// Guard: a successfully-generated, released row for today needs no work.
		var existingErr, existingContent sql.NullString
		err := r.db.QueryRow(
			`SELECT COALESCE(error, ''), content FROM daily_briefings WHERE user_id = ? AND date = ?`,
			userID, date,
		).Scan(&existingErr, &existingContent)
		if err == nil && existingErr.String == "" && existingContent.String != "" {
			return false, ErrBriefingAlreadyRunning
		}
	}

	// Atomic lease. INSERT for a brand-new (user, date), or UPDATE the lease on
	// an existing row — but only when no unexpired lease is held. The WHERE on
	// the UPDATE branch is what makes the claim safe across instances: two
	// instances both hitting the UPSERT see the same row, but only one's UPDATE
	// matches the free/expired-lease predicate.
	res, err := r.db.ExecWrite(`
		INSERT INTO daily_briefings (user_id, date, content, lock_until)
		VALUES (?, ?, '', ?)
		ON CONFLICT(user_id, date) DO UPDATE SET
			lock_until = excluded.lock_until,
			updated_at = CURRENT_TIMESTAMP
		WHERE daily_briefings.lock_until IS NULL
		   OR daily_briefings.lock_until < ?
	`, userID, date, lockUntil, now)
	if err != nil {
		return false, fmt.Errorf("claim briefing lock: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim briefing lock rows: %w", err)
	}
	if n == 0 {
		// Another instance holds an unexpired lease.
		return false, ErrBriefingAlreadyRunning
	}
	return true, nil
}

// ReleaseBriefingLock clears the generation lease for (userID, date). It is
// called on every completion path (success, failure, and "no context") so the
// row doesn't stay claimed. Idempotent: clearing an already-free lease is a
// no-op.
func (r *AIRepository) ReleaseBriefingLock(userID int, date string) error {
	_, err := r.db.ExecWrite(`
		UPDATE daily_briefings SET lock_until = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE user_id = ? AND date = ?
	`, userID, date)
	if err != nil {
		return fmt.Errorf("release briefing lock: %w", err)
	}
	return nil
}
