package portalwebauthn

import (
	"strconv"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"windshift/internal/auth"
	"windshift/internal/webauthn/persistence"
)

// Subject adapts auth.PortalCustomer to the webauthn.User interface.
type Subject struct {
	*auth.PortalCustomer
	credentials []webauthn.Credential
}

// NewSubject creates a Subject for the given portal customer with no
// credentials loaded; call SetCredentials before BeginRegistration/BeginLogin.
func NewSubject(c *auth.PortalCustomer) *Subject {
	return &Subject{PortalCustomer: c, credentials: []webauthn.Credential{}}
}

// WebAuthnID returns the user-handle bytes the authenticator stores. We use
// the portal customer ID as ASCII digits — at most 19 bytes for INT64, well
// under the WebAuthn 64-byte limit.
func (s *Subject) WebAuthnID() []byte { return []byte(strconv.Itoa(s.ID)) }

// WebAuthnName is the user-visible identifier (email).
func (s *Subject) WebAuthnName() string { return s.Email }

// WebAuthnDisplayName is the customer's full name; falls back to email.
func (s *Subject) WebAuthnDisplayName() string {
	if s.Name != "" {
		return s.Name
	}
	return s.Email
}

// WebAuthnCredentials returns currently-loaded credentials.
func (s *Subject) WebAuthnCredentials() []webauthn.Credential { return s.credentials }

// WebAuthnIcon is unused.
func (s *Subject) WebAuthnIcon() string { return "" }

// SetCredentials loads the customer's stored credentials onto the subject.
func (s *Subject) SetCredentials(creds []webauthn.Credential) { s.credentials = creds }

// CredentialExcludeList builds the exclusion list used during registration so
// the same authenticator cannot be enrolled twice.
func (s *Subject) CredentialExcludeList() []protocol.CredentialDescriptor {
	excludeList := make([]protocol.CredentialDescriptor, 0, len(s.credentials))
	for _, cred := range s.credentials {
		excludeList = append(excludeList, protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: cred.ID,
			Transport:    cred.Transport,
		})
	}
	return excludeList
}

// Credential is the on-the-wire / database shape for a stored portal
// credential. Mirrors webauthn.WebAuthnCredential but keyed on
// portal_customer_id.
type Credential struct {
	PortalCustomerID int `json:"portal_customer_id"`
	persistence.CredentialWireFields
}

// ToWebAuthnCredential converts the database row to a go-webauthn credential.
func (wc *Credential) ToWebAuthnCredential() (webauthn.Credential, error) {
	return credentialRecordFromPortalCredential(wc).ToWebAuthnCredential()
}

// FromWebAuthnCredential builds a database row from a go-webauthn credential.
func FromWebAuthnCredential(portalCustomerID int, name string, cred *webauthn.Credential) *Credential {
	record := persistence.FromWebAuthnCredential(portalCustomerID, name, cred)
	credential := portalCredentialFromRecord(record)
	return &credential
}

func portalCredentialFromRecord(record persistence.CredentialRecord) Credential {
	return Credential{
		PortalCustomerID:     record.OwnerID,
		CredentialWireFields: record.WireFields(),
	}
}

func credentialRecordFromPortalCredential(credential *Credential) persistence.CredentialRecord {
	return persistence.RecordFromWireFields(credential.CredentialWireFields, credential.PortalCustomerID)
}
