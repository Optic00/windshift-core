package webauthn

import (
	"github.com/go-webauthn/webauthn/webauthn"

	"windshift/internal/webauthn/persistence"
)

// CredentialStore handles internal-user WebAuthn credentials while the shared
// persistence package owns SQL, serialization, and authenticator updates.
type CredentialStore struct {
	store *persistence.CredentialStore
}

// NewCredentialStore creates a store bound to the internal-user schema.
func NewCredentialStore(db Database) *CredentialStore {
	return &CredentialStore{store: persistence.NewInternalCredentialStore(db)}
}

// SaveCredential stores a new WebAuthn credential for a user.
func (cs *CredentialStore) SaveCredential(userID int, credentialName string, cred *webauthn.Credential) error {
	return cs.store.SaveCredential(userID, credentialName, cred)
}

// GetUserCredentials retrieves all credentials for a user.
func (cs *CredentialStore) GetUserCredentials(userID int) ([]webauthn.Credential, error) {
	return cs.store.GetCredentials(userID)
}

// UpdateCredentialCounter updates the sign count after successful
// authentication.
func (cs *CredentialStore) UpdateCredentialCounter(credentialID []byte, signCount uint32, cloneWarning bool) error {
	return cs.store.UpdateCredentialCounter(credentialID, signCount, cloneWarning)
}

// DeleteCredential removes a specific credential.
func (cs *CredentialStore) DeleteCredential(credentialID string) error {
	return cs.store.DeleteCredential(credentialID)
}

// GetUserCredentialsList retrieves credential info for display without
// sensitive key material.
func (cs *CredentialStore) GetUserCredentialsList(userID int) ([]WebAuthnCredential, error) {
	records, err := cs.store.GetCredentialRecords(userID)
	if err != nil {
		return nil, err
	}
	credentials := make([]WebAuthnCredential, 0, len(records))
	for _, record := range records {
		credentials = append(credentials, webAuthnCredentialFromRecord(record))
	}
	return credentials, nil
}

// CheckCredentialExists verifies if a credential ID already exists.
func (cs *CredentialStore) CheckCredentialExists(credentialID []byte) (bool, error) {
	return cs.store.CheckCredentialExists(credentialID)
}

// LookupUserByCredentialID returns the user that owns a credential.
func (cs *CredentialStore) LookupUserByCredentialID(credentialID string) (int, error) {
	return cs.store.LookupOwnerByCredentialID(credentialID)
}
