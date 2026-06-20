package repository

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"windshift/internal/database"
)

// newBriefingTestDB stands up an initialized SQLite DB. daily_briefings is
// created by Initialize() via the schema runner, including the lock_until
// column added by the migration.
func newBriefingTestDB(t *testing.T) database.Database {
	t.Helper()
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "briefing-lock.db"))
	if err != nil {
		t.Fatalf("create sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize db: %v", err)
	}
	// daily_briefings.lock_until is created by the embedded schema (fresh
	// installs) or by the 20260620_daily_briefings_lock_until migration
	// (existing installs). The schema runner above covers the fresh-install
	// path this test exercises.
	return db
}

func TestClaimBriefingFirstCallerWins(t *testing.T) {
	db := newBriefingTestDB(t)
	repo := NewAIRepository(db)
	now := time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC)

	// First claim on a fresh (user, date) succeeds.
	claimed, err := repo.ClaimBriefing(1, "2026-06-20", now, false)
	if err != nil {
		t.Fatalf("first claim: unexpected error %v", err)
	}
	if !claimed {
		t.Fatalf("first claim: want true (claim granted), got false")
	}

	// A second concurrent caller (another instance) must NOT win: the first
	// holds an unexpired lease.
	claimed2, err := repo.ClaimBriefing(1, "2026-06-20", now, false)
	if !errors.Is(err, ErrBriefingAlreadyRunning) {
		t.Fatalf("second claim: want ErrBriefingAlreadyRunning, got claimed=%v err=%v", claimed2, err)
	}
	if claimed2 {
		t.Fatalf("second claim: want false, got true")
	}
}

func TestClaimBriefingAlreadyGeneratedShortCircuits(t *testing.T) {
	db := newBriefingTestDB(t)
	repo := NewAIRepository(db)
	now := time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC)

	// Seed a successfully-generated, released briefing for today.
	if _, err := db.Exec(
		`INSERT INTO daily_briefings (user_id, date, content, error, lock_until) VALUES (?, ?, ?, NULL, NULL)`,
		1, "2026-06-20", "Good morning!",
	); err != nil {
		t.Fatalf("seed briefing: %v", err)
	}

	// With regenerate=false, the existing successful briefing means "nothing
	// to do" — the claim reports already-running without invoking generation.
	claimed, err := repo.ClaimBriefing(1, "2026-06-20", now, false)
	if !errors.Is(err, ErrBriefingAlreadyRunning) {
		t.Fatalf("claim on completed row: want ErrBriefingAlreadyRunning, got claimed=%v err=%v", claimed, err)
	}

	// With regenerate=true (every_6h schedule), we *can* claim again to
	// re-generate — but the lease still protects against concurrent claims.
	claimed2, err := repo.ClaimBriefing(1, "2026-06-20", now, true)
	if err != nil || !claimed2 {
		t.Fatalf("regenerate claim: want claim granted, got claimed=%v err=%v", claimed2, err)
	}

	// And a third caller while the lease is live is blocked even under
	// regenerate, proving cross-instance dedup holds regardless of schedule.
	claimed3, err := repo.ClaimBriefing(1, "2026-06-20", now, true)
	if !errors.Is(err, ErrBriefingAlreadyRunning) {
		t.Fatalf("regenerate concurrent claim: want ErrBriefingAlreadyRunning, got claimed=%v err=%v", claimed3, err)
	}
}

func TestClaimBriefingExpiredLeaseSelfHeals(t *testing.T) {
	db := newBriefingTestDB(t)
	repo := NewAIRepository(db)
	now := time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC)

	// Simulate a crashed holder: a row with a stale (past) lease and an error
	// from the failed attempt. The next tick must be able to reclaim it.
	stale := now.Add(-2 * briefingLockDuration)
	if _, err := db.Exec(
		`INSERT INTO daily_briefings (user_id, date, content, error, lock_until) VALUES (?, ?, '', 'boom', ?)`,
		1, "2026-06-20", stale,
	); err != nil {
		t.Fatalf("seed stale row: %v", err)
	}

	claimed, err := repo.ClaimBriefing(1, "2026-06-20", now, false)
	if err != nil || !claimed {
		t.Fatalf("reclaim after stale lease: want claim granted, got claimed=%v err=%v", claimed, err)
	}
}

func TestReleaseBriefingLockClearsLease(t *testing.T) {
	db := newBriefingTestDB(t)
	repo := NewAIRepository(db)
	now := time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC)

	if _, err := repo.ClaimBriefing(1, "2026-06-20", now, false); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := repo.ReleaseBriefingLock(1, "2026-06-20"); err != nil {
		t.Fatalf("release: %v", err)
	}

	// After release the row is claimable again immediately (no lease held).
	claimed, err := repo.ClaimBriefing(1, "2026-06-20", now, false)
	if err != nil || !claimed {
		t.Fatalf("re-claim after release: want claim granted, got claimed=%v err=%v", claimed, err)
	}
}
