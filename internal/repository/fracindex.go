package repository

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/lib/pq"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"windshift/internal/database"
)

// FracIndexMaxRetries caps transaction retries on the item INSERT and reorder
// paths. Reorders retry unique collisions and PostgreSQL concurrency aborts.
const FracIndexMaxRetries = 5

// fracIndexJitterLen is the number of random base62 digits appended to a
// freshly generated append key. The append key from KeyBetween(max, "") is
// DETERMINISTIC, so two concurrent appends that read the same MAX(frac_index)
// would otherwise compute the identical key and collide on idx_items_frac_index.
// Appending random fractional digits (the well-known fractional-indexing
// "jitter") makes the two keys differ with probability 1 - 62^-len ≈ 1, so
// concurrent appends proceed without any global serialization. The
// unique-violation retry in the create paths remains as the backstop for the
// astronomically rare genuine collision. Four digits give 62^4 ≈ 1.5e7
// distinct suffixes per position — empirically zero retries across tens of
// thousands of concurrent appends. This replaces the global advisory lock that
// previously serialized every append.
const fracIndexJitterLen = 4

// fracIndexRebalanceLengthThreshold is the point where a generated key is
// considered pathologically long for an interactive reorder. A local window
// rebalance is attempted before writing keys above this size. Normal keys are
// usually 2-5 bytes; the threshold leaves ample headroom while avoiding index
// bloat from repeated insertion into the same hot gap.
const fracIndexRebalanceLengthThreshold = 128

// fracIndexLocalRebalanceWindowSize caps the number of neighboring rows that a
// synchronous hot-gap rebalance rewrites. It is intentionally small enough for
// drag-and-drop latency, but large enough that balanced midpoint assignment
// restores plenty of space around the insertion point.
const fracIndexLocalRebalanceWindowSize = 128

// globalRankHotGapTriggerTimeout bounds the best-effort state transition that
// schedules full normalization after a canonical local hot-gap rebalance.
const globalRankHotGapTriggerTimeout = 2 * time.Second

// IsFracIndexUniqueViolation reports whether err is specifically a
// UNIQUE-constraint violation on idx_items_frac_index. Other unique
// violations (e.g. workspace_item_number) must not trigger the retry,
// so a generic check would be too broad. Exported for use by handlers
// that wrap reorder writes in their own retry loop.
func IsFracIndexUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505" && pqErr.Constraint == "idx_items_frac_index"
	}
	return isSQLiteUniqueViolation(err, "items.frac_index")
}

func isFracIndexRetryableTransactionError(err error) bool {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		return false
	}
	return pqErr.Code == "40P01" || pqErr.Code == "40001"
}

// IsSerializationAbort reports PostgreSQL serialization failures and
// deadlocks — the cases where retrying the whole transaction on a fresh
// snapshot is the documented recovery.
func IsSerializationAbort(err error) bool {
	return isFracIndexRetryableTransactionError(err)
}

// IsWorkspaceItemNumberUniqueViolation reports whether err is specifically a
// UNIQUE-constraint violation on (workspace_id, workspace_item_number).
// GetNextWorkspaceItemNumber now serializes allocation per workspace with an
// advisory lock, so this should not fire in normal operation; callers keep the
// whole-transaction retry as a defensive backstop (e.g. a number assigned
// outside that path).
func IsWorkspaceItemNumberUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505" && pqErr.Constraint == "items_workspace_id_workspace_item_number_key"
	}
	return isSQLiteUniqueViolation(err, "items.workspace_id, items.workspace_item_number")
}

func isSQLiteUniqueViolation(err error, columns string) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) &&
		(sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE ||
			sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) &&
		strings.Contains(sqliteErr.Error(), "UNIQUE constraint failed: "+columns)
}

// Fractional indexing implementation based on https://github.com/rocicorp/fracdex
// This provides lexicographically sortable keys for ordering items

const base62Digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
const smallestInt = "A00000000000000000000000000"
const zero = "a0"

// KeyBetween returns a key that sorts lexicographically between a and b.
// Either a or b can be empty strings. If a is empty it indicates smallest key,
// If b is empty it indicates largest key.
// b must be empty string or > a.
func KeyBetween(a, b string) (string, error) {
	if a != "" {
		err := validateOrderKey(a)
		if err != nil {
			return "", err
		}
	}
	if b != "" {
		err := validateOrderKey(b)
		if err != nil {
			return "", err
		}
	}
	if a != "" && b != "" && a >= b {
		return "", fmt.Errorf("%s >= %s", a, b)
	}
	if a == "" {
		if b == "" {
			return zero, nil
		}

		ib, err := getIntPart(b)
		if err != nil {
			return "", err
		}
		fb := b[len(ib):]
		if ib == smallestInt {
			return ib + midpoint("", fb), nil
		}
		if ib < b {
			return ib, nil
		}
		res, err := decrementInt(ib)
		if err != nil {
			return "", err
		}
		if res == "" {
			return "", errors.New("range underflow")
		}
		return res, nil
	}

	if b == "" {
		ia, err := getIntPart(a)
		if err != nil {
			return "", err
		}
		fa := a[len(ia):]
		i, err := incrementInt(ia)
		if err != nil {
			return "", err
		}
		if i == "" {
			return ia + midpoint(fa, ""), nil
		}
		return i, nil
	}

	ia, err := getIntPart(a)
	if err != nil {
		return "", err
	}
	fa := a[len(ia):]
	ib, err := getIntPart(b)
	if err != nil {
		return "", err
	}
	fb := b[len(ib):]
	if ia == ib {
		return ia + midpoint(fa, fb), nil
	}
	i, err := incrementInt(ia)
	if err != nil {
		return "", err
	}
	if i == "" {
		return "", errors.New("range overflow")
	}
	if i < b {
		return i, nil
	}
	return ia + midpoint(fa, ""), nil
}

// `a < b` lexicographically if `b` is non-empty.
// a == "" means first possible string.
// b == "" means last possible string.
func midpoint(a, b string) string {
	if b != "" {
		// remove longest common prefix.  pad `a` with 0s as we
		// go.  note that we don't need to pad `b`, because it can't
		// end before `a` while traversing the common prefix.
		i := 0
		for ; i < len(b); i++ {
			c := byte('0')
			if len(a) > i {
				c = a[i]
			}
			if c != b[i] {
				break
			}
		}
		if i > 0 {
			if i > len(a) {
				return b[0:i] + midpoint("", b[i:])
			}
			return b[0:i] + midpoint(a[i:], b[i:])
		}
	}

	// first digits (or lack of digit) are different
	digitA := 0
	if a != "" {
		digitA = strings.Index(base62Digits, string(a[0]))
	}
	digitB := len(base62Digits)
	if b != "" {
		digitB = strings.Index(base62Digits, string(b[0]))
	}
	if digitB-digitA > 1 {
		midDigit := int(math.Round(0.5 * float64(digitA+digitB)))
		return string(base62Digits[midDigit])
	}

	// first digits are consecutive
	if len(b) > 1 {
		return b[0:1]
	}

	// `b` is empty or has length 1 (a single digit).
	// the first digit of `a` is the previous digit to `b`,
	// or 9 if `b` is null.
	// given, for example, midpoint('49', '5'), return
	// '4' + midpoint('9', null), which will become
	// '4' + '9' + midpoint('', null), which is '495'
	sa := ""
	if a != "" {
		sa = a[1:]
	}
	return string(base62Digits[digitA]) + midpoint(sa, "")
}

func validateInt(i string) error {
	exp, err := getIntLen(i[0])
	if err != nil {
		return err
	}
	if len(i) != exp {
		return fmt.Errorf("invalid integer part of order key: %s", i)
	}
	return nil
}

func getIntLen(head byte) (int, error) {
	switch {
	case head >= 'a' && head <= 'z':
		return int(head - 'a' + 2), nil
	case head >= 'A' && head <= 'Z':
		return int('Z' - head + 2), nil
	default:
		return 0, fmt.Errorf("invalid order key head: %s", string(head))
	}
}

func getIntPart(key string) (string, error) {
	intPartLen, err := getIntLen(key[0])
	if err != nil {
		return "", err
	}
	if intPartLen > len(key) {
		return "", fmt.Errorf("invalid order key: %s", key)
	}
	return key[0:intPartLen], nil
}

func validateOrderKey(key string) error {
	if key == smallestInt {
		return fmt.Errorf("invalid order key: %s", key)
	}
	// getIntPart will return error if the first character is bad,
	// or the key is too short.  we'd call it to check these things
	// even if we didn't need the result
	i, err := getIntPart(key)
	if err != nil {
		return err
	}
	f := key[len(i):]
	if strings.HasSuffix(f, "0") {
		return fmt.Errorf("invalid order key: %s", key)
	}
	return nil
}

// returns error if x is invalid, or if range is exceeded
func incrementInt(x string) (string, error) {
	err := validateInt(x)
	if err != nil {
		return "", err
	}
	digs := strings.Split(x, "")
	head := digs[0]
	digs = digs[1:]
	carry := true
	for i := len(digs) - 1; carry && i >= 0; i-- {
		d := strings.Index(base62Digits, digs[i]) + 1
		if d == len(base62Digits) {
			digs[i] = "0"
		} else {
			digs[i] = string(base62Digits[d])
			carry = false
		}
	}
	if carry {
		if head == "Z" {
			return "a0", nil
		}
		if head == "z" {
			return "", nil
		}
		h := string(head[0] + 1)
		if h > "a" {
			digs = append(digs, "0")
		} else {
			digs = digs[1:]
		}
		return h + strings.Join(digs, ""), nil
	}
	return head + strings.Join(digs, ""), nil
}

func decrementInt(x string) (string, error) {
	err := validateInt(x)
	if err != nil {
		return "", err
	}
	digs := strings.Split(x, "")
	head := digs[0]
	digs = digs[1:]
	borrow := true
	for i := len(digs) - 1; borrow && i >= 0; i-- {
		d := strings.Index(base62Digits, digs[i]) - 1
		if d == -1 {
			digs[i] = string(base62Digits[len(base62Digits)-1])
		} else {
			digs[i] = string(base62Digits[d])
			borrow = false
		}
	}

	if borrow {
		if head == "a" {
			return "Z" + string(base62Digits[len(base62Digits)-1]), nil
		}
		if head == "A" {
			return "", nil
		}
		h := head[0] - 1
		if h < 'Z' {
			digs = append(digs, string(base62Digits[len(base62Digits)-1]))
		} else {
			digs = digs[1:]
		}
		return string(h) + strings.Join(digs, ""), nil
	}

	return head + strings.Join(digs, ""), nil
}

// ===== Integration functions for the windshift application =====

// GenerateFracIndexForNewItem returns the next frac_index for an append
// (new item at the end of the global ordering). It reads MAX(frac_index)
// inside the caller's transaction, computes the deterministic successor with
// KeyBetween, and appends a random jitter suffix (see fracIndexJitterLen) so
// concurrent appends that read the same MAX still produce distinct keys
// without any cross-transaction lock.
//
// Callers must (a) be inside a transaction whose subsequent INSERT writes
// the returned key, (b) pass the database driver when available so PostgreSQL
// can coordinate the mutation with a bounded global migration batch, and
// (c) retry the whole transaction on
// IsFracIndexUniqueViolation — jitter makes collisions astronomically rare
// but the retry is the correctness backstop (jitter collision, or a
// non-generator writer racing in).
func GenerateFracIndexForNewItem(tx database.Tx, drivers ...string) (string, error) {
	driver := ""
	if len(drivers) > 0 {
		driver = drivers[0]
	}
	if err := acquireGlobalRankMutationLock(tx, driver); err != nil {
		return "", err
	}
	var last sql.NullString
	err := tx.QueryRow(`SELECT frac_index
		FROM items
		WHERE frac_index IS NOT NULL
		ORDER BY frac_index DESC
		LIMIT 1`).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read max frac_index: %w", err)
	}

	var base string
	var bucket *GlobalRankBucket
	if !last.Valid {
		base, err = KeyBetween("", "")
		if state, stateErr := loadGlobalRankState(tx); stateErr == nil && state.Phase != GlobalRankPhaseLegacy {
			activeBucket := state.ActiveBucket
			bucket = &activeBucket
		}
	} else {
		if parsed, parseErr := ParseGlobalRank(last.String); parseErr == nil {
			base, err = KeyBetween(parsed.Fraction, "")
			parsedBucket := parsed.Bucket
			bucket = &parsedBucket
		} else {
			// The canonical schema only permits bucketed ranks. A legacy or
			// hand-written unbucketed value should not leak into a newly created
			// row once the global rank state is stable; start a fresh valid key
			// in the active bucket instead.
			base, err = KeyBetween("", "")
			if state, stateErr := loadGlobalRankState(tx); stateErr == nil && state.Phase != GlobalRankPhaseLegacy {
				activeBucket := state.ActiveBucket
				bucket = &activeBucket
			}
		}
	}
	if err != nil {
		return "", err
	}
	base += fracIndexJitter()
	if bucket == nil {
		return base, nil
	}
	return EncodeGlobalRank(*bucket, base)
}

// GenerateFracIndexesForBatch returns count strictly increasing frac_index
// keys for one bulk append (workspace-template cloning). It takes the global
// rank mutation lock once, reads MAX(frac_index) once, then chains KeyBetween
// successors with jitter, so the relative order of the batch is preserved and
// every key is globally unique without per-item locking. Callers write the
// returned keys inside the same transaction and retry the whole transaction
// on IsFracIndexUniqueViolation, exactly like the single-item path.
func GenerateFracIndexesForBatch(tx database.Tx, count int, drivers ...string) ([]string, error) {
	if count <= 0 {
		return nil, nil
	}
	driver := ""
	if len(drivers) > 0 {
		driver = drivers[0]
	}
	if err := acquireGlobalRankMutationLock(tx, driver); err != nil {
		return nil, err
	}
	var last sql.NullString
	err := tx.QueryRow(`SELECT frac_index
		FROM items
		WHERE frac_index IS NOT NULL
		ORDER BY frac_index DESC
		LIMIT 1`).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read max frac_index: %w", err)
	}

	prev := ""
	var bucket *GlobalRankBucket
	if last.Valid {
		if parsed, parseErr := ParseGlobalRank(last.String); parseErr == nil {
			prev = parsed.Fraction
			parsedBucket := parsed.Bucket
			bucket = &parsedBucket
		}
	}
	if bucket == nil {
		if state, stateErr := loadGlobalRankState(tx); stateErr == nil && state.Phase != GlobalRankPhaseLegacy {
			activeBucket := state.ActiveBucket
			bucket = &activeBucket
		}
	}

	keys := make([]string, 0, count)
	for i := 0; i < count; i++ {
		base, err := KeyBetween(prev, "")
		if err != nil {
			return nil, err
		}
		base += fracIndexJitter()
		key := base
		if bucket != nil {
			key, err = EncodeGlobalRank(*bucket, base)
			if err != nil {
				return nil, err
			}
		}
		keys = append(keys, key)
		if parsed, parseErr := ParseGlobalRank(key); parseErr == nil {
			prev = parsed.Fraction
		} else {
			prev = key
		}
	}
	return keys, nil
}

// fracIndexJitter returns fracIndexJitterLen random base62 digits, with the
// last digit forced non-zero so the suffix is a valid order-key tail
// (validateOrderKey rejects a trailing '0'). Appending it to a generated
// append key keeps the key strictly greater than the previous max while making
// concurrent appends collision-resistant. crypto/rand never realistically
// fails; if it did, the unique-violation retry would still preserve
// correctness, just with more contention.
func fracIndexJitter() string {
	b := make([]byte, fracIndexJitterLen)
	if _, err := crand.Read(b); err != nil {
		return "z" // valid single-digit, non-zero fallback
	}
	for i, v := range b {
		b[i] = base62Digits[int(v)%len(base62Digits)]
	}
	if b[len(b)-1] == '0' {
		b[len(b)-1] = base62Digits[1]
	}
	return string(b)
}

// MoveItemBetween updates an item's frac_index to a value between the
// frac_index of its prev and next neighbors. It reads the neighbor
// frac_indexes inside a transaction (with FOR UPDATE on Postgres so
// concurrent moves involving the same neighbors block at the DB rather
// than racing on idx_items_frac_index), computes KeyBetween in Go, and
// writes the UPDATE — all atomically. The retry loop handles unique collisions
// and PostgreSQL deadlock or serialization aborts. Each retry starts a fresh
// transaction and re-reads the neighbors, so it never reuses stale bounds.
//
// prevID / nextID may be nil to indicate "start of list" / "end of list".
func MoveItemBetween(db database.Database, itemID int, prevID, nextID *int) (string, error) {
	driver := db.GetDriverName()
	var lastErr error
	for attempt := 0; attempt < FracIndexMaxRetries; attempt++ {
		requestGlobalMigration := false
		key, err := database.WithTxResult(db, func(tx database.Tx) (string, error) {
			if err := acquireGlobalRankMutationLock(tx, driver); err != nil {
				return "", err
			}
			prev, perr := readFracIndexForUpdate(tx, prevID, driver)
			if perr != nil {
				return "", perr
			}
			next, nerr := readFracIndexForUpdate(tx, nextID, driver)
			if nerr != nil {
				return "", nerr
			}
			newKey, kerr := chooseMoveFracIndex(tx, itemID, prev, next, driver)
			if kerr != nil {
				return "", fmt.Errorf("compute key between %q and %q: %w", prev, next, kerr)
			}
			if len(newKey) > fracIndexRebalanceLengthThreshold {
				rebalanced := false
				if rank, parseErr := ParseGlobalRank(newKey); parseErr == nil {
					if globalRankBoundsWithinBucket(prev, next, rank.Bucket) {
						if rerr := rebalanceLocalGlobalRankWindow(tx, itemID, prev, next, rank.Bucket, driver); rerr != nil {
							return "", rerr
						}
						rebalanced = true
						requestGlobalMigration = true
					}
				} else {
					if rerr := rebalanceLocalFracIndexWindow(tx, itemID, prev, next, driver); rerr != nil {
						return "", rerr
					}
					rebalanced = true
				}
				if rebalanced {
					// Re-read explicit neighbors because the local rebalance may have
					// rewritten their frac_index values while preserving order.
					prev, perr = readFracIndexForUpdate(tx, prevID, driver)
					if perr != nil {
						return "", perr
					}
					next, nerr = readFracIndexForUpdate(tx, nextID, driver)
					if nerr != nil {
						return "", nerr
					}
					newKey, kerr = chooseMoveFracIndex(tx, itemID, prev, next, driver)
					if kerr != nil {
						return "", fmt.Errorf("compute key after local rebalance between %q and %q: %w", prev, next, kerr)
					}
				}
				if len(newKey) > fracIndexRebalanceLengthThreshold {
					slog.Warn("frac_index local rebalance left a long move key",
						slog.Int("item_id", itemID),
						slog.Int("key_length", len(newKey)),
						slog.String("component", "fracindex"))
				}
			}
			if _, eerr := tx.Exec("UPDATE items SET frac_index = ? WHERE id = ?", newKey, itemID); eerr != nil {
				return "", eerr
			}
			return newKey, nil
		})
		if err == nil {
			if requestGlobalMigration {
				requestGlobalRankMigrationAfterHotGap(db, itemID)
			}
			return key, nil
		}
		if !IsFracIndexUniqueViolation(err) && !isFracIndexRetryableTransactionError(err) {
			return "", err
		}
		lastErr = err
		slog.Warn("frac_index move transaction aborted, retrying",
			slog.Int("attempt", attempt+1),
			slog.Int("item_id", itemID),
			slog.Any("error", err),
			slog.String("component", "fracindex"))
	}
	return "", fmt.Errorf("move item %d failed after %d transaction retries: %w", itemID, FracIndexMaxRetries, lastErr)
}

func requestGlobalRankMigrationAfterHotGap(db database.Database, itemID int) {
	ctx, cancel := context.WithTimeout(context.Background(), globalRankHotGapTriggerTimeout)
	defer cancel()
	state, err := ControlGlobalRankMigration(ctx, db, GlobalRankMigrationStart)
	if errors.Is(err, ErrGlobalRankMigrationConflict) {
		// A migration is already active, paused, or failed. The durable state is
		// already operator-visible and duplicate scheduling is unnecessary.
		return
	}
	if err != nil {
		slog.Warn("failed to schedule global rank migration after hot gap",
			slog.Int("item_id", itemID),
			slog.Any("error", err),
			slog.String("component", "fracindex"))
		return
	}
	slog.Info("scheduled global rank migration after canonical hot gap",
		slog.Int("item_id", itemID),
		slog.Int("active_bucket", int(state.ActiveBucket)),
		slog.Any("target_bucket", state.TargetBucket),
		slog.String("component", "fracindex"))
}

// chooseMoveFracIndex finds a globally unique key within filtered-view bounds.
// It uses the nearest global neighbor to avoid deterministic collisions with
// items outside the view while preserving the requested open interval.
func chooseMoveFracIndex(tx database.Tx, itemID int, prev, next, driver string) (string, error) {
	if hasGlobalRankPrefix(prev) || hasGlobalRankPrefix(next) {
		return chooseMoveGlobalRank(tx, itemID, prev, next, driver)
	}
	if prev == "" && next == "" {
		maxKey, found, err := readGlobalBoundaryFracIndexForUpdate(tx, itemID, "DESC", driver)
		if err != nil {
			return "", err
		}
		if !found {
			fraction, err := KeyBetween("", "")
			if err != nil {
				return "", err
			}
			state, stateErr := loadGlobalRankState(tx)
			if stateErr == nil && state.Phase != GlobalRankPhaseLegacy {
				return EncodeGlobalRank(state.ActiveBucket, fraction)
			}
			return fraction, nil
		}
		if rank, parseErr := ParseGlobalRank(maxKey); parseErr == nil {
			fraction, err := KeyBetween(rank.Fraction, "")
			if err != nil {
				return "", err
			}
			return EncodeGlobalRank(rank.Bucket, fraction)
		}
		return KeyBetween(maxKey, "")
	}

	if prev == "" {
		lower := ""
		maxBelowNext, found, err := readBoundedFracIndexForUpdate(tx, itemID, "frac_index < ?", []any{next}, "DESC", driver)
		if err != nil {
			return "", err
		}
		if found {
			lower = maxBelowNext
		}
		return KeyBetween(lower, next)
	}

	upper := next
	where := "frac_index > ?"
	args := []any{prev}
	if next != "" {
		where += " AND frac_index < ?"
		args = append(args, next)
	}
	minAbovePrev, found, err := readBoundedFracIndexForUpdate(tx, itemID, where, args, "ASC", driver)
	if err != nil {
		return "", err
	}
	if found {
		upper = minAbovePrev
	}
	return KeyBetween(prev, upper)
}

func chooseMoveGlobalRank(tx database.Tx, itemID int, prev, next, driver string) (string, error) {
	// Narrow filtered-view bounds to the nearest actual global neighbor. This
	// prevents deterministic collisions with an item hidden by the view.
	effectivePrev := prev
	effectiveNext := next
	if prev == "" {
		maxBelowNext, found, err := readBoundedFracIndexForUpdate(tx, itemID, "frac_index < ?", []any{next}, "DESC", driver)
		if err != nil {
			return "", err
		}
		if found {
			effectivePrev = maxBelowNext
		}
	} else {
		where := "frac_index > ?"
		args := []any{prev}
		if next != "" {
			where += " AND frac_index < ?"
			args = append(args, next)
		}
		minAbovePrev, found, err := readBoundedFracIndexForUpdate(tx, itemID, where, args, "ASC", driver)
		if err != nil {
			return "", err
		}
		if found {
			effectiveNext = minAbovePrev
		}
	}

	return globalRankBetween(tx, effectivePrev, effectiveNext)
}

func globalRankBetween(tx database.Tx, lowerValue, upperValue string) (string, error) {
	lowerFraction, lowerBucket, err := splitGlobalRankBound(lowerValue)
	if err != nil {
		return "", err
	}
	upperFraction, upperBucket, err := splitGlobalRankBound(upperValue)
	if err != nil {
		return "", err
	}

	var bucket GlobalRankBucket
	switch {
	case lowerBucket == nil && upperBucket == nil:
		return "", fmt.Errorf("global rank move has no bounds")
	case lowerBucket == nil:
		bucket = *upperBucket
	case upperBucket == nil:
		bucket = *lowerBucket
	case *lowerBucket == *upperBucket:
		bucket = *lowerBucket
	default:
		state, err := loadGlobalRankState(tx)
		if err != nil {
			return "", err
		}
		if state.Phase != GlobalRankPhaseMigrating && state.Phase != GlobalRankPhasePaused {
			return "", fmt.Errorf("global rank bounds use different buckets outside an active migration: %d and %d", *lowerBucket, *upperBucket)
		}
		if state.TargetBucket == nil || state.Direction == nil {
			return "", fmt.Errorf("global rank migration has no target or direction")
		}
		validFrontierPair := *state.Direction == GlobalRankDirectionHighToLow &&
			*lowerBucket == state.ActiveBucket && *upperBucket == *state.TargetBucket
		validFrontierPair = validFrontierPair || (*state.Direction == GlobalRankDirectionLowToHigh &&
			*lowerBucket == *state.TargetBucket && *upperBucket == state.ActiveBucket)
		if !validFrontierPair {
			return "", fmt.Errorf("global rank bounds %d and %d do not match the migration frontier", *lowerBucket, *upperBucket)
		}
		if state.Frontier == nil {
			return "", fmt.Errorf("global rank migration has mixed buckets without a frontier")
		}
		frontier, err := ParseGlobalRank(*state.Frontier)
		if err != nil || frontier.Bucket != state.ActiveBucket {
			return "", fmt.Errorf("global rank migration has invalid frontier %q", *state.Frontier)
		}
		// Keep the new row in the active bucket so the worker will migrate it.
		// Target-bucket payloads are freshly balanced and therefore are not
		// directly comparable with active-bucket payloads. The durable frontier
		// supplies the opposite fractional bound and ensures the new rank remains
		// inside the worker's unprocessed active-bucket range.
		bucket = state.ActiveBucket
		var fraction string
		if *state.Direction == GlobalRankDirectionHighToLow {
			fraction, err = KeyBetween(lowerFraction, frontier.Fraction)
		} else {
			fraction, err = KeyBetween(frontier.Fraction, upperFraction)
		}
		if err != nil {
			return "", err
		}
		return EncodeGlobalRank(bucket, fraction)
	}

	fraction, err := KeyBetween(lowerFraction, upperFraction)
	if err != nil {
		return "", err
	}
	return EncodeGlobalRank(bucket, fraction)
}

func hasGlobalRankPrefix(value string) bool {
	return len(value) >= 2 && value[1] == '|' && value[0] >= '0' && value[0] <= '2'
}

func splitGlobalRankBound(value string) (string, *GlobalRankBucket, error) {
	if value == "" {
		return "", nil, nil
	}
	rank, err := ParseGlobalRank(value)
	if err != nil {
		return "", nil, err
	}
	bucket := rank.Bucket
	return rank.Fraction, &bucket, nil
}

type fracIndexWindowRow struct {
	id  int
	key string
}

// rebalanceLocalFracIndexWindow resequences a small contiguous global window
// around the intended insertion point. It preserves the relative order of every
// existing row in the window, but assigns balanced midpoint keys between the
// rows just outside the window. This is the cheap hot-gap escape hatch: repeated
// insertion into the same gap can make the immediate midpoint very long, and a
// full-table rebalance would be excessive for an interactive drag.
func rebalanceLocalFracIndexWindow(tx database.Tx, movingItemID int, prev, next, driver string) error {
	rows, err := readLocalRebalanceWindowForUpdate(tx, movingItemID, prev, next, driver)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}

	left, right, err := readWindowOutsideBoundsForUpdate(tx, movingItemID, rows[0].key, rows[len(rows)-1].key, driver)
	if err != nil {
		return err
	}
	keys, err := generateEvenlySpacedFracKeys(left, right, len(rows))
	if err != nil {
		return fmt.Errorf("generate local rebalance keys: %w", err)
	}

	// Temporarily move the moving row and the window rows out of the UNIQUE
	// index. Temporary non-null keys keep this path compatible with the
	// canonical items.frac_index NOT NULL constraint; the final rewrite can
	// otherwise fail when a new key equals another window row's old key. The
	// transaction restores final keys before commit, and the temporary prefix
	// is outside the validated fractional/bucket key grammar.
	ids := make([]int, 0, len(rows)+1)
	ids = append(ids, movingItemID)
	for _, row := range rows {
		ids = append(ids, row.id)
	}
	if err := setFracIndexTemporaryForIDs(tx, ids); err != nil {
		return err
	}

	updates := make([]fracIndexUpdate, 0, len(rows))
	for index, row := range rows {
		updates = append(updates, fracIndexUpdate{id: int64(row.id), key: keys[index]})
	}
	if err := updateFracIndexes(tx, updates); err != nil {
		return fmt.Errorf("write local rebalance keys: %w", err)
	}

	slog.Info("rebalanced local frac_index window",
		slog.Int("rows", len(rows)),
		slog.Int("moving_item_id", movingItemID),
		slog.String("component", "fracindex"))
	return nil
}

// rebalanceLocalGlobalRankWindow is the canonical bucket-aware equivalent of
// rebalanceLocalFracIndexWindow. It scopes every boundary query to one bucket,
// balances only the fractional payloads, then restores the bucket prefix.
func rebalanceLocalGlobalRankWindow(tx database.Tx, movingItemID int, prev, next string, bucket GlobalRankBucket, driver string) error {
	rows, err := readLocalGlobalRankWindowForUpdate(tx, movingItemID, prev, next, bucket, driver)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}

	left, right, err := readGlobalRankWindowOutsideBoundsForUpdate(tx, movingItemID, rows[0].key, rows[len(rows)-1].key, bucket, driver)
	if err != nil {
		return err
	}
	leftFraction, err := globalRankFractionForBucket(left, bucket)
	if err != nil {
		return err
	}
	rightFraction, err := globalRankFractionForBucket(right, bucket)
	if err != nil {
		return err
	}
	fractions, err := generateEvenlySpacedFracKeys(leftFraction, rightFraction, len(rows))
	if err != nil {
		return fmt.Errorf("generate canonical local rebalance keys: %w", err)
	}

	updates := make([]fracIndexUpdate, 0, len(rows))
	ids := make([]int, 0, len(rows)+1)
	ids = append(ids, movingItemID)
	for index, row := range rows {
		ids = append(ids, row.id)
		key, err := EncodeGlobalRank(bucket, fractions[index])
		if err != nil {
			return err
		}
		updates = append(updates, fracIndexUpdate{id: int64(row.id), key: key})
	}
	if err := setFracIndexTemporaryForIDs(tx, ids); err != nil {
		return err
	}
	if err := updateFracIndexes(tx, updates); err != nil {
		return fmt.Errorf("write canonical local rebalance keys: %w", err)
	}

	slog.Info("rebalanced canonical local frac_index window",
		slog.Int("rows", len(rows)),
		slog.Int("moving_item_id", movingItemID),
		slog.Int("bucket", int(bucket)),
		slog.String("component", "fracindex"))
	return nil
}

func globalRankBoundsWithinBucket(prev, next string, bucket GlobalRankBucket) bool {
	for _, value := range []string{prev, next} {
		if value == "" {
			continue
		}
		rank, err := ParseGlobalRank(value)
		if err != nil || rank.Bucket != bucket {
			return false
		}
	}
	return true
}

func readLocalGlobalRankWindowForUpdate(tx database.Tx, movingItemID int, prev, next string, bucket GlobalRankBucket, driver string) ([]fracIndexWindowRow, error) {
	beforeLimit := fracIndexLocalRebalanceWindowSize / 2
	afterLimit := fracIndexLocalRebalanceWindowSize - beforeLimit

	var before, after []fracIndexWindowRow
	var err error
	switch {
	case prev != "":
		before, err = readGlobalRankWindowRowsForUpdate(tx, "frac_index <= ?", []any{prev}, "DESC", beforeLimit, movingItemID, bucket, driver)
		if err != nil {
			return nil, err
		}
		after, err = readGlobalRankWindowRowsForUpdate(tx, "frac_index > ?", []any{prev}, "ASC", afterLimit, movingItemID, bucket, driver)
		if err != nil {
			return nil, err
		}
		reverseWindowRows(before)
	case next != "":
		before, err = readGlobalRankWindowRowsForUpdate(tx, "frac_index < ?", []any{next}, "DESC", beforeLimit, movingItemID, bucket, driver)
		if err != nil {
			return nil, err
		}
		after, err = readGlobalRankWindowRowsForUpdate(tx, "frac_index >= ?", []any{next}, "ASC", afterLimit, movingItemID, bucket, driver)
		if err != nil {
			return nil, err
		}
		reverseWindowRows(before)
	default:
		before, err = readGlobalRankWindowRowsForUpdate(tx, "1 = 1", nil, "DESC", fracIndexLocalRebalanceWindowSize, movingItemID, bucket, driver)
		if err != nil {
			return nil, err
		}
		reverseWindowRows(before)
	}
	rows := make([]fracIndexWindowRow, 0, len(before)+len(after))
	rows = append(rows, before...)
	rows = append(rows, after...)
	return rows, nil
}

func readGlobalRankWindowRowsForUpdate(tx database.Tx, where string, args []any, direction string, limit, movingItemID int, bucket GlobalRankBucket, driver string) ([]fracIndexWindowRow, error) {
	lower, upper := globalRankBucketBounds(bucket)
	queryArgs := make([]any, 0, 2+len(args)+2)
	queryArgs = append(queryArgs, lower, upper)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, movingItemID, limit)
	query := `SELECT id, frac_index FROM items
		WHERE frac_index >= ? AND frac_index < ? AND (` + where + `) AND id <> ?
		ORDER BY frac_index ` + direction + `
		LIMIT ?`
	if database.IsPostgresDriver(driver) {
		query += " FOR UPDATE"
	}
	rows, err := tx.Query(query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("read canonical local rebalance window: %w", err)
	}
	defer rows.Close()
	out := make([]fracIndexWindowRow, 0, limit)
	for rows.Next() {
		var row fracIndexWindowRow
		if err := rows.Scan(&row.id, &row.key); err != nil {
			return nil, fmt.Errorf("scan canonical local rebalance window: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate canonical local rebalance window: %w", err)
	}
	return out, nil
}

func readGlobalRankWindowOutsideBoundsForUpdate(tx database.Tx, movingItemID int, firstKey, lastKey string, bucket GlobalRankBucket, driver string) (left, right string, err error) {
	lower, upper := globalRankBucketBounds(bucket)
	left, _, err = readBoundedFracIndexForUpdate(tx, movingItemID, "frac_index >= ? AND frac_index < ? AND frac_index < ?", []any{lower, upper, firstKey}, "DESC", driver)
	if err != nil {
		return "", "", err
	}
	right, _, err = readBoundedFracIndexForUpdate(tx, movingItemID, "frac_index >= ? AND frac_index < ? AND frac_index > ?", []any{lower, upper, lastKey}, "ASC", driver)
	if err != nil {
		return "", "", err
	}
	return left, right, nil
}

func globalRankFractionForBucket(value string, bucket GlobalRankBucket) (string, error) {
	if value == "" {
		return "", nil
	}
	rank, err := ParseGlobalRank(value)
	if err != nil {
		return "", err
	}
	if rank.Bucket != bucket {
		return "", fmt.Errorf("global rank %q is outside local rebalance bucket %d", value, bucket)
	}
	return rank.Fraction, nil
}

func readLocalRebalanceWindowForUpdate(tx database.Tx, movingItemID int, prev, next, driver string) ([]fracIndexWindowRow, error) {
	beforeLimit := fracIndexLocalRebalanceWindowSize / 2
	afterLimit := fracIndexLocalRebalanceWindowSize - beforeLimit

	var before, after []fracIndexWindowRow
	var err error
	switch {
	case prev != "":
		before, err = readWindowRowsForUpdate(tx, `frac_index <= ?`, []any{prev}, "DESC", beforeLimit, movingItemID, driver)
		if err != nil {
			return nil, err
		}
		after, err = readWindowRowsForUpdate(tx, `frac_index > ?`, []any{prev}, "ASC", afterLimit, movingItemID, driver)
		if err != nil {
			return nil, err
		}
		reverseWindowRows(before)
	case next != "":
		before, err = readWindowRowsForUpdate(tx, `frac_index < ?`, []any{next}, "DESC", beforeLimit, movingItemID, driver)
		if err != nil {
			return nil, err
		}
		after, err = readWindowRowsForUpdate(tx, `frac_index >= ?`, []any{next}, "ASC", afterLimit, movingItemID, driver)
		if err != nil {
			return nil, err
		}
		reverseWindowRows(before)
	default:
		before, err = readWindowRowsForUpdate(tx, `frac_index IS NOT NULL`, nil, "DESC", fracIndexLocalRebalanceWindowSize, movingItemID, driver)
		if err != nil {
			return nil, err
		}
		reverseWindowRows(before)
	}

	rows := make([]fracIndexWindowRow, 0, len(before)+len(after))
	rows = append(rows, before...)
	rows = append(rows, after...)
	return rows, nil
}

func readWindowRowsForUpdate(tx database.Tx, where string, args []any, direction string, limit, movingItemID int, driver string) ([]fracIndexWindowRow, error) {
	q := `SELECT id, frac_index FROM items
		WHERE ` + where + ` AND id <> ?
		ORDER BY frac_index ` + direction + `
		LIMIT ?`
	args = append(args, movingItemID, limit)
	if database.IsPostgresDriver(driver) {
		q += " FOR UPDATE"
	}
	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("read local rebalance window: %w", err)
	}
	defer rows.Close()

	out := make([]fracIndexWindowRow, 0, limit)
	for rows.Next() {
		var row fracIndexWindowRow
		if err := rows.Scan(&row.id, &row.key); err != nil {
			return nil, fmt.Errorf("scan local rebalance window: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate local rebalance window: %w", err)
	}
	return out, nil
}

func readWindowOutsideBoundsForUpdate(tx database.Tx, movingItemID int, firstKey, lastKey, driver string) (left, right string, err error) {
	left, _, err = readBoundedFracIndexForUpdate(tx, movingItemID, "frac_index < ?", []any{firstKey}, "DESC", driver)
	if err != nil {
		return "", "", err
	}
	right, _, err = readBoundedFracIndexForUpdate(tx, movingItemID, "frac_index > ?", []any{lastKey}, "ASC", driver)
	if err != nil {
		return "", "", err
	}
	return left, right, nil
}

func setFracIndexTemporaryForIDs(tx database.Tx, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	updates := make([]fracIndexUpdate, 0, len(ids))
	for _, id := range ids {
		// Generated fractional keys use only the order-key alphabet and
		// canonical ranks use bucket|fraction. The prefix therefore cannot
		// collide with a valid application rank, while the item ID makes each
		// in-flight key unique within this transaction.
		updates = append(updates, fracIndexUpdate{id: int64(id), key: fmt.Sprintf("~rebalance-%d", id)})
	}
	if err := updateFracIndexes(tx, updates); err != nil {
		return fmt.Errorf("set temporary local rebalance keys: %w", err)
	}
	return nil
}

type fracIndexUpdate struct {
	id  int64
	key string
}

// updateFracIndexes rewrites one bounded set of item ranks in one statement.
// Callers lock or serialize the rows before invoking it.
func updateFracIndexes(tx database.Tx, updates []fracIndexUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	var query strings.Builder
	query.WriteString("UPDATE items SET frac_index = CASE id")
	args := make([]any, 0, len(updates)*3)
	for _, update := range updates {
		query.WriteString(" WHEN ? THEN ?")
		args = append(args, update.id, update.key)
	}
	query.WriteString(" ELSE frac_index END WHERE id IN (")
	for index, update := range updates {
		if index > 0 {
			query.WriteByte(',')
		}
		query.WriteByte('?')
		args = append(args, update.id)
	}
	query.WriteByte(')')

	result, err := tx.Exec(query.String(), args...)
	if err != nil {
		return fmt.Errorf("update frac_index batch: %w", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr == nil && affected != int64(len(updates)) {
		return fmt.Errorf("update frac_index batch: affected %d rows, want %d", affected, len(updates))
	}
	return nil
}

func generateEvenlySpacedFracKeys(left, right string, n int) ([]string, error) {
	keys := make([]string, n)
	if err := fillEvenlySpacedFracKeys(keys, 0, n, left, right); err != nil {
		return nil, err
	}
	return keys, nil
}

func fillEvenlySpacedFracKeys(keys []string, lo, hi int, left, right string) error {
	if lo >= hi {
		return nil
	}
	mid := lo + (hi-lo)/2
	key, err := KeyBetween(left, right)
	if err != nil {
		return err
	}
	keys[mid] = key
	if err := fillEvenlySpacedFracKeys(keys, lo, mid, left, key); err != nil {
		return err
	}
	return fillEvenlySpacedFracKeys(keys, mid+1, hi, key, right)
}

func reverseWindowRows(rows []fracIndexWindowRow) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func readGlobalBoundaryFracIndexForUpdate(tx database.Tx, itemID int, direction, driver string) (key string, found bool, err error) {
	q := `SELECT frac_index FROM items
		WHERE frac_index IS NOT NULL AND id <> ?
		ORDER BY frac_index ` + direction + `
		LIMIT 1`
	return scanBoundaryFracIndexForUpdate(tx, q, []any{itemID}, driver)
}

func readBoundedFracIndexForUpdate(tx database.Tx, itemID int, where string, args []any, direction, driver string) (key string, found bool, err error) {
	q := `SELECT frac_index FROM items
		WHERE ` + where + ` AND id <> ?
		ORDER BY frac_index ` + direction + `
		LIMIT 1`
	args = append(args, itemID)
	return scanBoundaryFracIndexForUpdate(tx, q, args, driver)
}

func scanBoundaryFracIndexForUpdate(tx database.Tx, q string, args []any, driver string) (key string, found bool, err error) {
	if database.IsPostgresDriver(driver) {
		q += " FOR UPDATE"
	}
	var k sql.NullString
	if err := tx.QueryRow(q, args...).Scan(&k); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read frac_index boundary: %w", err)
	}
	if !k.Valid {
		return "", false, nil
	}
	return k.String, true, nil
}

// readFracIndexForUpdate reads the frac_index of a neighbor row. On Postgres
// it appends FOR UPDATE so the row is locked for the duration of the tx;
// on SQLite the global writer lock already serializes the read-compute-write
// cycle, so the clause is omitted (SQLite's parser would reject it).
// A nil id returns "" — the caller's signal for "no neighbor on this side".
func readFracIndexForUpdate(tx database.Tx, id *int, driver string) (string, error) {
	if id == nil {
		return "", nil
	}
	q := "SELECT frac_index FROM items WHERE id = ?"
	if database.IsPostgresDriver(driver) {
		q += " FOR UPDATE"
	}
	var k sql.NullString
	if err := tx.QueryRow(q, *id).Scan(&k); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("neighbor %d not found", *id)
		}
		return "", fmt.Errorf("read neighbor %d: %w", *id, err)
	}
	if !k.Valid {
		return "", fmt.Errorf("neighbor %d has null frac_index", *id)
	}
	return k.String, nil
}
