package services

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"

	"github.com/lib/pq"

	"windshift/internal/database"
)

// fracIndexMaxRetries caps the number of unique-violation retries on the
// item INSERT / reorder UPDATE paths. The retry path only fires when a
// concurrent writer wins the race on idx_items_frac_index between two
// transactions that read the same neighbor keys before either committed.
const fracIndexMaxRetries = 5

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
		return pqErr.Code == "23505" &&
			(pqErr.Constraint == "idx_items_frac_index" ||
				strings.Contains(pqErr.Message, "idx_items_frac_index"))
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed: items.frac_index")
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
// inside the caller's transaction, optionally locking that row on Postgres
// via FOR UPDATE so two concurrent appends serialize on the current max.
// SQLite ignores the clause; its global writer lock already serializes
// writing transactions.
//
// Callers must (a) be inside a transaction whose subsequent INSERT writes
// the returned key, and (b) retry the whole transaction on
// IsFracIndexUniqueViolation — the lock prevents most collisions but not
// all (e.g. a non-generator writer running concurrently).
func GenerateFracIndexForNewItem(tx database.Tx, driverName string) (string, error) {
	q := `SELECT frac_index
		FROM items
		WHERE frac_index IS NOT NULL
		ORDER BY frac_index DESC
		LIMIT 1`
	if driverName == "postgres" {
		q += " FOR UPDATE"
	}
	var last sql.NullString
	err := tx.QueryRow(q).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read max frac_index: %w", err)
	}
	if !last.Valid {
		return KeyBetween("", "")
	}
	return KeyBetween(last.String, "")
}

// MoveItemBetween updates an item's frac_index to a value between the
// frac_index of its prev and next neighbors. It reads the neighbor
// frac_indexes inside a transaction (with FOR UPDATE on Postgres so
// concurrent moves involving the same neighbors block at the DB rather
// than racing on idx_items_frac_index), computes KeyBetween in Go, and
// writes the UPDATE — all atomically. The unique-violation retry is the
// backstop for cases the locks don't cover (non-generator writers, brief
// partitions). Each retry re-reads the neighbors so a concurrent reorder
// that moved them is naturally accounted for.
//
// prevID / nextID may be nil to indicate "start of list" / "end of list".
func MoveItemBetween(db database.Database, itemID int, prevID, nextID *int) (string, error) {
	driver := db.GetDriverName()
	var lastErr error
	for attempt := 0; attempt < fracIndexMaxRetries; attempt++ {
		key, err := database.WithTxResult(db, func(tx database.Tx) (string, error) {
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
				if rerr := rebalanceLocalFracIndexWindow(tx, itemID, prev, next, driver); rerr != nil {
					return "", rerr
				}
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
			return key, nil
		}
		if !IsFracIndexUniqueViolation(err) {
			return "", err
		}
		lastErr = err
		slog.Warn("frac_index unique violation on move, retrying",
			slog.Int("attempt", attempt+1),
			slog.Int("item_id", itemID),
			slog.String("component", "fracindex"))
	}
	return "", fmt.Errorf("move item %d failed after %d frac_index retries: %w", itemID, fracIndexMaxRetries, lastErr)
}

// chooseMoveFracIndex returns a globally unique frac_index that still sorts
// within the caller's requested view-local bounds. Board and backlog reorders
// pass neighbors from a filtered subset (status column, iteration section,
// etc.), while items.frac_index is globally unique. A naive KeyBetween(prev,
// next) can therefore deterministically produce a key already held by an item
// outside that subset (for example prev=a0, next=nil -> a1, but a1 is in a
// different board column). In that case retrying the same bounds will collide
// forever.
//
// To avoid that, use the immediate global row just inside the requested bound
// as the opposite bound. The open interval between an item and its immediate
// global neighbor contains no existing frac_index, so KeyBetween cannot collide
// deterministically. The chosen key remains valid for the filtered view because
// it is still inside the caller's original open interval.
func chooseMoveFracIndex(tx database.Tx, itemID int, prev, next, driver string) (string, error) {
	if prev == "" && next == "" {
		maxKey, found, err := readGlobalBoundaryFracIndexForUpdate(tx, itemID, "DESC", driver)
		if err != nil {
			return "", err
		}
		if !found {
			return KeyBetween("", "")
		}
		return KeyBetween(maxKey, "")
	}

	if prev == "" {
		lower := ""
		maxBelowNext, found, err := readBoundedFracIndexForUpdate(tx, itemID, "frac_index < ?", []interface{}{next}, "DESC", driver)
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
	args := []interface{}{prev}
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

	// Temporarily remove the moving row and the window rows from the UNIQUE
	// partial index. Without this, sequential rewrites can fail when a new key
	// equals another window row's old key. The transaction restores final keys
	// before commit; readers never observe the temporary NULLs on Postgres.
	ids := make([]int, 0, len(rows)+1)
	ids = append(ids, movingItemID)
	for _, row := range rows {
		ids = append(ids, row.id)
	}
	if err := setFracIndexNullForIDs(tx, ids); err != nil {
		return err
	}

	for i, row := range rows {
		if _, err := tx.Exec("UPDATE items SET frac_index = ? WHERE id = ?", keys[i], row.id); err != nil {
			return fmt.Errorf("write local rebalance key for item %d: %w", row.id, err)
		}
	}

	slog.Info("rebalanced local frac_index window",
		slog.Int("rows", len(rows)),
		slog.Int("moving_item_id", movingItemID),
		slog.String("component", "fracindex"))
	return nil
}

func readLocalRebalanceWindowForUpdate(tx database.Tx, movingItemID int, prev, next, driver string) ([]fracIndexWindowRow, error) {
	beforeLimit := fracIndexLocalRebalanceWindowSize / 2
	afterLimit := fracIndexLocalRebalanceWindowSize - beforeLimit

	var before, after []fracIndexWindowRow
	var err error
	switch {
	case prev != "":
		before, err = readWindowRowsForUpdate(tx, `frac_index <= ?`, []interface{}{prev}, "DESC", beforeLimit, movingItemID, driver)
		if err != nil {
			return nil, err
		}
		after, err = readWindowRowsForUpdate(tx, `frac_index > ?`, []interface{}{prev}, "ASC", afterLimit, movingItemID, driver)
		if err != nil {
			return nil, err
		}
		reverseWindowRows(before)
	case next != "":
		before, err = readWindowRowsForUpdate(tx, `frac_index < ?`, []interface{}{next}, "DESC", beforeLimit, movingItemID, driver)
		if err != nil {
			return nil, err
		}
		after, err = readWindowRowsForUpdate(tx, `frac_index >= ?`, []interface{}{next}, "ASC", afterLimit, movingItemID, driver)
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

func readWindowRowsForUpdate(tx database.Tx, where string, args []interface{}, direction string, limit, movingItemID int, driver string) ([]fracIndexWindowRow, error) {
	q := `SELECT id, frac_index FROM items
		WHERE ` + where + ` AND id <> ?
		ORDER BY frac_index ` + direction + `
		LIMIT ?`
	args = append(args, movingItemID, limit)
	if driver == "postgres" {
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
	left, _, err = readBoundedFracIndexForUpdate(tx, movingItemID, "frac_index < ?", []interface{}{firstKey}, "DESC", driver)
	if err != nil {
		return "", "", err
	}
	right, _, err = readBoundedFracIndexForUpdate(tx, movingItemID, "frac_index > ?", []interface{}{lastKey}, "ASC", driver)
	if err != nil {
		return "", "", err
	}
	return left, right, nil
}

func setFracIndexNullForIDs(tx database.Tx, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := "UPDATE items SET frac_index = NULL WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	if _, err := tx.Exec(q, args...); err != nil {
		return fmt.Errorf("clear local rebalance keys: %w", err)
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
	return scanBoundaryFracIndexForUpdate(tx, q, []interface{}{itemID}, driver)
}

func readBoundedFracIndexForUpdate(tx database.Tx, itemID int, where string, args []interface{}, direction, driver string) (key string, found bool, err error) {
	q := `SELECT frac_index FROM items
		WHERE ` + where + ` AND id <> ?
		ORDER BY frac_index ` + direction + `
		LIMIT 1`
	args = append(args, itemID)
	return scanBoundaryFracIndexForUpdate(tx, q, args, driver)
}

func scanBoundaryFracIndexForUpdate(tx database.Tx, q string, args []interface{}, driver string) (key string, found bool, err error) {
	if driver == "postgres" {
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
	if driver == "postgres" {
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
