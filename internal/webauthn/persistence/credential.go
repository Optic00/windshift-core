package persistence

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

type credentialSchema struct {
	table       string
	ownerColumn string
}

var (
	internalCredentialSchema = credentialSchema{
		table:       "webauthn_credentials",
		ownerColumn: "user_id",
	}
	portalCredentialSchema = credentialSchema{
		table:       "portal_webauthn_credentials",
		ownerColumn: "portal_customer_id",
	}
)

// CredentialRecord is the schema-neutral representation of a stored
// WebAuthn credential. OwnerID is a user ID for internal credentials and a
// portal-customer ID for portal credentials.
type CredentialRecord struct {
	ID                  string
	OwnerID             int
	CredentialName      string
	PublicKey           []byte
	AttestationType     string
	AAGUID              []byte
	SignCount           uint32
	CloneWarning        bool
	Transport           []string
	FlagsUserPresent    bool
	FlagsUserVerified   bool
	FlagsBackupEligible bool
	FlagsBackupState    bool
	CreatedAt           time.Time
	UpdatedAt           time.Time
	LastUsedAt          *time.Time
}

// CredentialStore contains the shared credential persistence mechanics. Use
// NewInternalCredentialStore or NewPortalCredentialStore to bind it to one of
// the two compile-time-owned schemas.
type CredentialStore struct {
	db     Database
	schema credentialSchema
}

// NewInternalCredentialStore creates a store for internal-user credentials.
func NewInternalCredentialStore(db Database) *CredentialStore {
	return &CredentialStore{db: db, schema: internalCredentialSchema}
}

// NewPortalCredentialStore creates a store for portal-customer credentials.
func NewPortalCredentialStore(db Database) *CredentialStore {
	return &CredentialStore{db: db, schema: portalCredentialSchema}
}

// SaveCredential stores a newly completed WebAuthn registration.
func (cs *CredentialStore) SaveCredential(ownerID int, credentialName string, cred *webauthn.Credential) error {
	record := FromWebAuthnCredential(ownerID, credentialName, cred)
	transportJSON, err := json.Marshal(record.Transport)
	if err != nil {
		return fmt.Errorf("failed to marshal transport: %w", err)
	}

	query := fmt.Sprintf(`
		INSERT INTO %s (
			id, %s, credential_name, public_key, attestation_type,
			aaguid, sign_count, clone_warning, transport,
			flags_user_present, flags_user_verified,
			flags_backup_eligible, flags_backup_state,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, cs.schema.table, cs.schema.ownerColumn)
	now := time.Now()
	_, err = cs.db.ExecWrite(query,
		record.ID, ownerID, credentialName, record.PublicKey, record.AttestationType,
		record.AAGUID, record.SignCount, record.CloneWarning, transportJSON,
		record.FlagsUserPresent, record.FlagsUserVerified,
		record.FlagsBackupEligible, record.FlagsBackupState,
		now, now,
	)
	if err != nil {
		return fmt.Errorf("failed to save credential: %w", err)
	}
	return nil
}

// GetCredentials loads credentials in the format expected by go-webauthn.
func (cs *CredentialStore) GetCredentials(ownerID int) ([]webauthn.Credential, error) {
	records, err := cs.GetCredentialRecords(ownerID)
	if err != nil {
		return nil, err
	}
	credentials := make([]webauthn.Credential, 0, len(records))
	for _, record := range records {
		credential, err := record.ToWebAuthnCredential()
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, credential)
	}
	return credentials, nil
}

// GetCredentialRecords loads complete rows for an owner, including fields
// needed by the credential-management endpoints.
func (cs *CredentialStore) GetCredentialRecords(ownerID int) ([]CredentialRecord, error) {
	query := fmt.Sprintf(`
		SELECT id, %s, credential_name, public_key, attestation_type, aaguid, sign_count,
		       clone_warning, transport, flags_user_present, flags_user_verified,
		       flags_backup_eligible, flags_backup_state,
		       created_at, updated_at, last_used_at
		FROM %s
		WHERE %s = ?
		ORDER BY created_at DESC`, cs.schema.ownerColumn, cs.schema.table, cs.schema.ownerColumn)
	rows, err := cs.db.Query(query, ownerID)
	if err != nil {
		return nil, fmt.Errorf("failed to query credentials: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var records []CredentialRecord
	for rows.Next() {
		var record CredentialRecord
		var transportJSON string
		var lastUsedAt sql.NullTime
		if err := rows.Scan(
			&record.ID, &record.OwnerID, &record.CredentialName, &record.PublicKey, &record.AttestationType,
			&record.AAGUID, &record.SignCount, &record.CloneWarning, &transportJSON,
			&record.FlagsUserPresent, &record.FlagsUserVerified,
			&record.FlagsBackupEligible, &record.FlagsBackupState,
			&record.CreatedAt, &record.UpdatedAt, &lastUsedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan credential: %w", err)
		}
		if err := json.Unmarshal([]byte(transportJSON), &record.Transport); err != nil {
			return nil, fmt.Errorf("failed to unmarshal transport: %w", err)
		}
		if lastUsedAt.Valid {
			record.LastUsedAt = &lastUsedAt.Time
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate credentials: %w", err)
	}
	return records, nil
}

// UpdateCredentialCounter persists the authenticator counter and clone flag.
func (cs *CredentialStore) UpdateCredentialCounter(credentialID []byte, signCount uint32, cloneWarning bool) error {
	credID := base64.RawURLEncoding.EncodeToString(credentialID)
	query := fmt.Sprintf(`
		UPDATE %s
		SET sign_count = ?, clone_warning = ?, last_used_at = ?, updated_at = ?
		WHERE id = ?`, cs.schema.table)
	now := time.Now()
	if _, err := cs.db.ExecWrite(query, signCount, cloneWarning, now, now, credID); err != nil {
		return fmt.Errorf("failed to update credential counter: %w", err)
	}
	return nil
}

// DeleteCredential removes a credential by its encoded ID.
func (cs *CredentialStore) DeleteCredential(credentialID string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, cs.schema.table)
	if _, err := cs.db.ExecWrite(query, credentialID); err != nil {
		return fmt.Errorf("failed to delete credential: %w", err)
	}
	return nil
}

// CheckCredentialExists reports whether a credential ID is already stored.
func (cs *CredentialStore) CheckCredentialExists(credentialID []byte) (bool, error) {
	credID := base64.RawURLEncoding.EncodeToString(credentialID)
	query := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE id = ?)`, cs.schema.table)
	var exists bool
	if err := cs.db.QueryRow(query, credID).Scan(&exists); err != nil {
		return false, fmt.Errorf("failed to check credential existence: %w", err)
	}
	return exists, nil
}

// LookupOwnerByCredentialID returns the owner ID for a stored credential.
func (cs *CredentialStore) LookupOwnerByCredentialID(credentialID string) (int, error) {
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE id = ?`, cs.schema.ownerColumn, cs.schema.table)
	var ownerID int
	if err := cs.db.QueryRow(query, credentialID).Scan(&ownerID); err != nil {
		return 0, err
	}
	return ownerID, nil
}

// CountCredentials returns the number of credentials for an owner.
func (cs *CredentialStore) CountCredentials(ownerID int) (int, error) {
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s = ?`, cs.schema.table, cs.schema.ownerColumn)
	var count int
	if err := cs.db.QueryRow(query, ownerID).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count credentials: %w", err)
	}
	return count, nil
}

// FromWebAuthnCredential converts a library credential into a schema-neutral
// database record.
func FromWebAuthnCredential(ownerID int, name string, cred *webauthn.Credential) CredentialRecord {
	transports := make([]string, 0, len(cred.Transport))
	for _, transport := range cred.Transport {
		transports = append(transports, string(transport))
	}
	return CredentialRecord{
		ID:                  base64.RawURLEncoding.EncodeToString(cred.ID),
		OwnerID:             ownerID,
		CredentialName:      name,
		PublicKey:           cred.PublicKey,
		AttestationType:     cred.AttestationType,
		AAGUID:              cred.Authenticator.AAGUID,
		SignCount:           cred.Authenticator.SignCount,
		CloneWarning:        cred.Authenticator.CloneWarning,
		Transport:           transports,
		FlagsUserPresent:    cred.Flags.UserPresent,
		FlagsUserVerified:   cred.Flags.UserVerified,
		FlagsBackupEligible: cred.Flags.BackupEligible,
		FlagsBackupState:    cred.Flags.BackupState,
	}
}

// ToWebAuthnCredential converts a stored record into the library type.
func (record CredentialRecord) ToWebAuthnCredential() (webauthn.Credential, error) {
	credentialID, err := base64.RawURLEncoding.DecodeString(record.ID)
	if err != nil {
		return webauthn.Credential{}, fmt.Errorf("failed to decode credential ID: %w", err)
	}
	transports := make([]protocol.AuthenticatorTransport, 0, len(record.Transport))
	for _, transport := range record.Transport {
		transports = append(transports, protocol.AuthenticatorTransport(transport))
	}
	return webauthn.Credential{
		ID:              credentialID,
		PublicKey:       record.PublicKey,
		AttestationType: record.AttestationType,
		Transport:       transports,
		Flags: webauthn.CredentialFlags{
			UserPresent:    record.FlagsUserPresent,
			UserVerified:   record.FlagsUserVerified,
			BackupEligible: record.FlagsBackupEligible,
			BackupState:    record.FlagsBackupState,
		},
		Authenticator: webauthn.Authenticator{
			AAGUID:       record.AAGUID,
			SignCount:    record.SignCount,
			CloneWarning: record.CloneWarning,
		},
	}, nil
}

// CredentialWireFields is the API-facing subset of a stored credential
// shared by the internal-user and portal-customer wire types. Each wire type
// embeds it and adds its owner ID field under the JSON key its API contract
// requires.
type CredentialWireFields struct {
	ID                  string   `json:"id"` // Base64 encoded credential ID
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

// WireFields converts the record to the shared wire fields, formatting
// timestamps as RFC3339.
func (record CredentialRecord) WireFields() CredentialWireFields {
	fields := CredentialWireFields{
		ID:                  record.ID,
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
		fields.LastUsedAt = &lastUsedAt
	}
	return fields
}

// RecordFromWireFields rebuilds the storage record from the shared wire
// fields. Timestamps are not parsed back; the record's times stay zero.
func RecordFromWireFields(fields CredentialWireFields, ownerID int) CredentialRecord {
	return CredentialRecord{
		ID:                  fields.ID,
		OwnerID:             ownerID,
		CredentialName:      fields.CredentialName,
		PublicKey:           fields.PublicKey,
		AttestationType:     fields.AttestationType,
		AAGUID:              fields.AAGUID,
		SignCount:           fields.SignCount,
		CloneWarning:        fields.CloneWarning,
		Transport:           fields.Transport,
		FlagsUserPresent:    fields.FlagsUserPresent,
		FlagsUserVerified:   fields.FlagsUserVerified,
		FlagsBackupEligible: fields.FlagsBackupEligible,
		FlagsBackupState:    fields.FlagsBackupState,
	}
}
