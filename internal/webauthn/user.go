package webauthn

import (
	"fmt"
	"strconv"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"windshift/internal/models"
	"windshift/internal/webauthn/persistence"
)

// User wraps the application User model to implement webauthn.User interface
type User struct {
	*models.User
	credentials []webauthn.Credential
}

// NewUser creates a new WebAuthn user wrapper
func NewUser(user *models.User) *User {
	return &User{
		User:        user,
		credentials: []webauthn.Credential{},
	}
}

// WebAuthnID returns the user's unique identifier for WebAuthn
// Max 64 bytes as per WebAuthn spec
func (u *User) WebAuthnID() []byte {
	// Use user ID as bytes
	idStr := strconv.Itoa(u.ID)
	return []byte(idStr)
}

// WebAuthnName returns the user's username for WebAuthn
// This is the user-visible identifier
func (u *User) WebAuthnName() string {
	// Use email as the primary identifier since it's unique and memorable
	return u.Email
}

// WebAuthnDisplayName returns the user's display name for WebAuthn
func (u *User) WebAuthnDisplayName() string {
	// Use full name if available, otherwise username
	if u.FirstName != "" || u.LastName != "" {
		return fmt.Sprintf("%s %s", u.FirstName, u.LastName)
	}
	return u.Username
}

// WebAuthnCredentials returns the user's credentials for WebAuthn
func (u *User) WebAuthnCredentials() []webauthn.Credential {
	return u.credentials
}

// WebAuthnIcon returns the user's icon URL for WebAuthn (optional)
func (u *User) WebAuthnIcon() string {
	return u.AvatarURL
}

// AddCredential adds a credential to the user's credential list
func (u *User) AddCredential(cred webauthn.Credential) {
	u.credentials = append(u.credentials, cred)
}

// SetCredentials sets the user's complete credential list
func (u *User) SetCredentials(creds []webauthn.Credential) {
	u.credentials = creds
}

// CredentialExcludeList returns a list of credential descriptors for exclusion
// Used during registration to prevent duplicate registrations
func (u *User) CredentialExcludeList() []protocol.CredentialDescriptor {
	excludeList := make([]protocol.CredentialDescriptor, 0, len(u.credentials))
	for _, cred := range u.credentials {
		descriptor := protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: cred.ID,
			Transport:    cred.Transport,
		}
		excludeList = append(excludeList, descriptor)
	}
	return excludeList
}

// WebAuthnCredential represents a stored WebAuthn credential in the database
type WebAuthnCredential struct {
	ID                  string   `json:"id"` // Base64 encoded credential ID
	UserID              int      `json:"user_id"`
	CredentialName      string   `json:"credential_name"`
	PublicKey           []byte   `json:"-"` // COSE encoded public key (not sent to client)
	AttestationType     string   `json:"attestation_type"`
	AAGUID              []byte   `json:"-"` // Authenticator GUID
	SignCount           uint32   `json:"sign_count"`
	CloneWarning        bool     `json:"clone_warning"`
	Transport           []string `json:"transport"` // ['usb', 'nfc', 'ble', 'internal']
	FlagsUserPresent    bool     `json:"flags_user_present"`
	FlagsUserVerified   bool     `json:"flags_user_verified"`
	FlagsBackupEligible bool     `json:"flags_backup_eligible"`
	FlagsBackupState    bool     `json:"flags_backup_state"`
	CreatedAt           string   `json:"created_at"`
	UpdatedAt           string   `json:"updated_at"`
	LastUsedAt          *string  `json:"last_used_at,omitempty"`
}

// ToWebAuthnCredential converts database credential to webauthn.Credential
func (wc *WebAuthnCredential) ToWebAuthnCredential() (webauthn.Credential, error) {
	return credentialRecordFromWebAuthnCredential(wc).ToWebAuthnCredential()
}

// FromWebAuthnCredential creates a WebAuthnCredential from webauthn.Credential
func FromWebAuthnCredential(userID int, name string, cred *webauthn.Credential) *WebAuthnCredential {
	credential := webAuthnCredentialFromRecord(persistence.FromWebAuthnCredential(userID, name, cred))
	return &credential
}

func webAuthnCredentialFromRecord(record persistence.CredentialRecord) WebAuthnCredential {
	credential := WebAuthnCredential{
		ID:                  record.ID,
		UserID:              record.OwnerID,
		CredentialName:      record.CredentialName,
		PublicKey:           record.PublicKey,
		AttestationType:     record.AttestationType,
		AAGUID:              record.AAGUID,
		SignCount:           record.SignCount,
		CloneWarning:        record.CloneWarning,
		Transport:           record.Transport,
		FlagsUserPresent:    record.FlagsUserPresent,
		FlagsUserVerified:   record.FlagsUserVerified,
		FlagsBackupEligible: record.FlagsBackupEligible,
		FlagsBackupState:    record.FlagsBackupState,
		CreatedAt:           record.CreatedAt.Format(time.RFC3339),
		UpdatedAt:           record.UpdatedAt.Format(time.RFC3339),
	}
	if record.LastUsedAt != nil {
		lastUsedAt := record.LastUsedAt.Format(time.RFC3339)
		credential.LastUsedAt = &lastUsedAt
	}
	return credential
}

func credentialRecordFromWebAuthnCredential(credential *WebAuthnCredential) persistence.CredentialRecord {
	return persistence.CredentialRecord{
		ID:                  credential.ID,
		OwnerID:             credential.UserID,
		CredentialName:      credential.CredentialName,
		PublicKey:           credential.PublicKey,
		AttestationType:     credential.AttestationType,
		AAGUID:              credential.AAGUID,
		SignCount:           credential.SignCount,
		CloneWarning:        credential.CloneWarning,
		Transport:           credential.Transport,
		FlagsUserPresent:    credential.FlagsUserPresent,
		FlagsUserVerified:   credential.FlagsUserVerified,
		FlagsBackupEligible: credential.FlagsBackupEligible,
		FlagsBackupState:    credential.FlagsBackupState,
	}
}
