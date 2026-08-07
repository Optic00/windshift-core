package portalwebauthn

import (
	"github.com/go-webauthn/webauthn/webauthn"

	"windshift/internal/webauthn/persistence"
)

// Database is the shared database contract used by portal persistence stores.
type Database = persistence.Database

// SessionStore handles portal-customer WebAuthn challenge storage while the
// shared persistence package owns serialization, expiry, and consumption.
type SessionStore struct {
	store *persistence.SessionStore
}

// NewSessionStore creates a store bound to the portal-customer schema.
func NewSessionStore(db Database) *SessionStore {
	return &SessionStore{store: persistence.NewPortalSessionStore(db)}
}

// SaveRegistrationSession stores registration challenge data for a customer.
func (s *SessionStore) SaveRegistrationSession(portalCustomerID int, sessionData *webauthn.SessionData) (string, error) {
	return s.store.SaveRegistrationSession(portalCustomerID, sessionData)
}

// SaveAuthenticationSession stores an authentication challenge. A nil
// customer ID is used for discoverable login.
func (s *SessionStore) SaveAuthenticationSession(portalCustomerID *int, sessionData *webauthn.SessionData) (string, error) {
	return s.store.SaveAuthenticationSession(portalCustomerID, sessionData)
}

// GetRegistrationSession retrieves and consumes a session bound to a customer.
func (s *SessionStore) GetRegistrationSession(sessionID string, portalCustomerID int) (*webauthn.SessionData, error) {
	return s.store.GetRegistrationSession(sessionID, portalCustomerID)
}

// GetAuthenticationSession retrieves and consumes an authentication session.
func (s *SessionStore) GetAuthenticationSession(sessionID string) (*webauthn.SessionData, error) {
	return s.store.GetAuthenticationSession(sessionID)
}
