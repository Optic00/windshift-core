package repository

import (
	"database/sql"
	"errors"
	"time"

	"windshift/internal/database"
)

// NativeAuthRepository persists the short-lived, single-use codes that bridge a
// system-browser SSO login back into a native client (desktop/iOS, WI-446).
// Each code is bound to a freshly created session; the native app redeems it
// once for the encoded session cookie. Mirrors the cli_auth one-time-code
// pattern, minus the agent/token machinery — here the credential is a session.
type NativeAuthRepository struct {
	db database.Database
}

func NewNativeAuthRepository(db database.Database) *NativeAuthRepository {
	return &NativeAuthRepository{db: db}
}

// NativeAuthCode is the session binding returned when a code is consumed.
type NativeAuthCode struct {
	ID               int64
	SessionToken     string
	SessionExpiresAt time.Time
}

// Store records a valid code bound to a session token. expiresAt bounds how
// long the code may be redeemed; sessionExpiresAt is the session's own expiry,
// echoed back at exchange time so the app can size the cookie's lifetime.
func (r *NativeAuthRepository) Store(code, sessionToken string, sessionExpiresAt, expiresAt time.Time) error {
	_, err := r.db.ExecWrite(`
		INSERT INTO native_auth_codes (code, session_token, session_expires_at, status, expires_at)
		VALUES (?, ?, ?, 'valid', ?)
	`, code, sessionToken, sessionExpiresAt, expiresAt)
	return err
}

// Consume atomically redeems a valid, unexpired code and returns the session it
// is bound to. Single-use: the status guard on the UPDATE means only the first
// caller wins; replays (and expired/unknown codes) return ErrNotFound. The
// session token is cleared on consume so it can't be re-read from the row.
func (r *NativeAuthRepository) Consume(code string, now time.Time) (*NativeAuthCode, error) {
	var row NativeAuthCode
	var status string
	var consumedAt sql.NullTime
	var expiresAt time.Time
	err := r.db.QueryRow(`
		SELECT id, session_token, session_expires_at, status, expires_at, consumed_at
		FROM native_auth_codes
		WHERE code = ?
	`, code).Scan(&row.ID, &row.SessionToken, &row.SessionExpiresAt, &status, &expiresAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	if status != "valid" || consumedAt.Valid || now.After(expiresAt) {
		// Best-effort: clear the secret on an expired-but-unconsumed code so it
		// can't linger in the table. Treat every non-redeemable state as "gone".
		if now.After(expiresAt) {
			_, _ = r.db.ExecWrite(`UPDATE native_auth_codes SET status = 'expired', session_token = '' WHERE id = ? AND consumed_at IS NULL`, row.ID)
		}
		return nil, ErrNotFound
	}

	res, err := r.db.ExecWrite(`
		UPDATE native_auth_codes
		SET status = 'consumed', consumed_at = CURRENT_TIMESTAMP, session_token = ''
		WHERE id = ? AND status = 'valid' AND consumed_at IS NULL
	`, row.ID)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		// Lost the race with a concurrent exchange.
		return nil, ErrNotFound
	}

	return &row, nil
}
