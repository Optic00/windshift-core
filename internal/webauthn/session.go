package webauthn

import (
	"github.com/go-webauthn/webauthn/webauthn"

	"windshift/internal/webauthn/persistence"
)

// Database is kept as an alias for callers that used the package's original
// database contract.
type Database = persistence.Database

// SessionData is kept as an alias for compatibility with existing callers.
type SessionData = persistence.SessionData

// SessionStore handles internal-user WebAuthn challenge storage while the
// shared persistence package owns serialization, expiry, and consumption.
type SessionStore struct {
	store *persistence.SessionStore
}

// NewSessionStore creates a store bound to the internal-user schema.
func NewSessionStore(db Database) *SessionStore {
	return &SessionStore{store: persistence.NewInternalSessionStore(db)}
}

// SaveRegistrationSession stores registration session data for a user.
func (s *SessionStore) SaveRegistrationSession(userID int, sessionData *webauthn.SessionData) (string, error) {
	return s.store.SaveRegistrationSession(userID, sessionData)
}

// SaveAuthenticationSession stores authentication session data.
func (s *SessionStore) SaveAuthenticationSession(userID *int, sessionData *webauthn.SessionData) (string, error) {
	return s.store.SaveAuthenticationSession(userID, sessionData)
}

// SaveAuthenticationSessionBound binds a challenge to the restricted browser
// session that passed password verification.
func (s *SessionStore) SaveAuthenticationSessionBound(userID, authSessionID int, sessionData *webauthn.SessionData) (string, error) {
	return s.store.SaveAuthenticationSessionBound(userID, authSessionID, sessionData)
}

// GetRegistrationSession retrieves and consumes a session bound to a user.
func (s *SessionStore) GetRegistrationSession(sessionID string, userID int) (*webauthn.SessionData, error) {
	return s.store.GetRegistrationSession(sessionID, userID)
}

// GetAuthenticationSession retrieves and consumes an authentication session.
func (s *SessionStore) GetAuthenticationSession(sessionID string) (*webauthn.SessionData, error) {
	return s.store.GetAuthenticationSession(sessionID)
}

// GetAuthenticationSessionBound consumes only a challenge tied to the given
// pending browser session.
func (s *SessionStore) GetAuthenticationSessionBound(sessionID string, authSessionID int) (*webauthn.SessionData, error) {
	return s.store.GetAuthenticationSessionBound(sessionID, authSessionID)
}
