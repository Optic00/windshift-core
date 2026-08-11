package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// ActionCredentialEncryptionInfo is the HKDF info label used to derive the
// action-credentials encryption key from SSO_SECRET. Domain-separated from
// the SSO secret-encryption label so a ciphertext from one realm cannot be
// decrypted with the other realm's key.
const ActionCredentialEncryptionInfo = "windshift-action-credentials-encryption-v1" //nolint:gosec // G101: HKDF domain-separation label, not a credential

// ActionCredentialType enumerates the supported credential shapes.
type ActionCredentialType string

const (
	// CredentialBearerToken stores the raw token; injected as
	// `Authorization: Bearer <token>` (or the scheme configured on the auth ref).
	CredentialBearerToken ActionCredentialType = "bearer_token"
	// CredentialAPIKey stores an opaque API key; injected as the literal
	// header value configured on the auth/secret ref.
	CredentialAPIKey ActionCredentialType = "api_key"
	// CredentialBasicAuth stores "user:password"; injected as
	// `Authorization: Basic <base64(user:password)>`.
	CredentialBasicAuth ActionCredentialType = "basic_auth"
	// CredentialCustomHeader stores an arbitrary header value with no
	// scheme/encoding transform.
	CredentialCustomHeader ActionCredentialType = "custom_header"
)

// ActionCredential represents a stored secret used by HTTP action capabilities.
// EncryptedSecret is intentionally hidden from JSON; the API returns the
// sanitized form instead.
type ActionCredential struct {
	ID             int                  `json:"id"`
	Name           string               `json:"name"`
	CredentialType ActionCredentialType `json:"credential_type"`
	// AppliesToAllWorkspaces gates whether every workspace can resolve this
	// credential. When false, only workspaces listed in WorkspaceIDs (the
	// action_credential_workspaces join table) may use it.
	AppliesToAllWorkspaces bool `json:"applies_to_all_workspaces"`
	// WorkspaceIDs is populated by the read path from the join table. Only
	// meaningful when AppliesToAllWorkspaces is false.
	WorkspaceIDs    []int     `json:"workspace_ids,omitempty"`
	CreatedBy       *int      `json:"created_by,omitempty"`
	EncryptedSecret string    `json:"-"` // never returned
	SecretPrefix    string    `json:"secret_prefix,omitempty"`
	SecretMetadata  string    `json:"secret_metadata,omitempty"` // JSON; must not contain plaintext secrets
	IsEnabled       bool      `json:"is_enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Sanitize returns a redacted view safe for any client response.
func (c *ActionCredential) Sanitize() ActionCredentialSanitized {
	metadata := c.SecretMetadata
	// Treat persisted metadata as untrusted on reads too. Validation prevents
	// new unsafe values, but legacy rows (or rows written by an older binary)
	// must not become a plaintext-secret disclosure through a list endpoint.
	if ValidateActionCredentialMetadata(metadata) != nil {
		metadata = ""
	}
	return ActionCredentialSanitized{
		ID:                     c.ID,
		Name:                   c.Name,
		CredentialType:         c.CredentialType,
		AppliesToAllWorkspaces: c.AppliesToAllWorkspaces,
		WorkspaceIDs:           c.WorkspaceIDs,
		HasSecret:              c.EncryptedSecret != "",
		SecretPrefix:           c.SecretPrefix,
		SecretMetadata:         metadata,
		IsEnabled:              c.IsEnabled,
		CreatedAt:              c.CreatedAt,
		UpdatedAt:              c.UpdatedAt,
	}
}

// ValidateActionCredentialMetadata requires metadata to be a JSON object and
// rejects sensitive-looking keys at any nesting depth. Metadata is returned to
// clients, so allowing a nested {"provider":{"token":"..."}} value would
// defeat the write-only credential contract just as surely as a top-level
// token field would.
func ValidateActionCredentialMetadata(metadata string) error {
	if metadata == "" {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(metadata), &parsed); err != nil {
		return fmt.Errorf("secret_metadata must be a JSON object: %w", err)
	}
	if parsed == nil {
		return fmt.Errorf("secret_metadata must be a JSON object")
	}
	if key, ok := findSensitiveCredentialMetadataKey(parsed); ok {
		return fmt.Errorf("secret_metadata must not contain sensitive key %q (use the secret field)", key)
	}
	return nil
}

func findSensitiveCredentialMetadataKey(value any) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if IsSensitiveActionCredentialMetadataKey(key) {
				return key, true
			}
			if nested, ok := findSensitiveCredentialMetadataKey(child); ok {
				return nested, true
			}
		}
	case []any:
		for _, child := range typed {
			if nested, ok := findSensitiveCredentialMetadataKey(child); ok {
				return nested, true
			}
		}
	}
	return "", false
}

// IsSensitiveActionCredentialMetadataKey reports whether a metadata key looks
// like it can hold secret material. It recognizes snake/kebab/space separated
// and camelCase forms so clientSecret cannot bypass a client_secret check.
func IsSensitiveActionCredentialMetadataKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}

	var compact strings.Builder
	var words strings.Builder
	var previousWasLowerOrDigit bool
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if unicode.IsUpper(r) && previousWasLowerOrDigit {
				words.WriteByte(' ')
			}
			lower := unicode.ToLower(r)
			compact.WriteRune(lower)
			words.WriteRune(lower)
			previousWasLowerOrDigit = unicode.IsLower(r) || unicode.IsDigit(r)
			continue
		}
		words.WriteByte(' ')
		previousWasLowerOrDigit = false
	}

	switch compact.String() {
	case "secret", "token", "password", "apikey", "authorization",
		"clientsecret", "privatekey", "accesstoken", "refreshtoken",
		"signingkey", "encryptionkey", "signature":
		return true
	}

	parts := strings.Fields(words.String())
	if len(parts) < 2 {
		return false
	}
	switch parts[len(parts)-1] {
	case "token", "secret", "password", "key", "signature":
		return true
	default:
		return false
	}
}

// ActionCredentialSanitized is the only shape sent to clients. has_secret +
// secret_prefix let the UI render a masked indicator without ever seeing the
// ciphertext or plaintext.
type ActionCredentialSanitized struct {
	ID                     int                  `json:"id"`
	Name                   string               `json:"name"`
	CredentialType         ActionCredentialType `json:"credential_type"`
	AppliesToAllWorkspaces bool                 `json:"applies_to_all_workspaces"`
	WorkspaceIDs           []int                `json:"workspace_ids,omitempty"`
	HasSecret              bool                 `json:"has_secret"`
	SecretPrefix           string               `json:"secret_prefix,omitempty"`
	SecretMetadata         string               `json:"secret_metadata,omitempty"`
	IsEnabled              bool                 `json:"is_enabled"`
	CreatedAt              time.Time            `json:"created_at"`
	UpdatedAt              time.Time            `json:"updated_at"`
}

// CreateActionCredentialRequest is the body for credential creation. The
// plaintext Secret travels only on this request and is discarded after
// encryption — it must not be persisted anywhere else.
type CreateActionCredentialRequest struct {
	Name           string               `json:"name"`
	CredentialType ActionCredentialType `json:"credential_type"`
	Secret         string               `json:"secret"`
	// AppliesToAllWorkspaces defaults to true when nil. The workspace-scoped
	// create endpoint forces it to false and pins WorkspaceIDs to the path
	// workspace; clients cannot smuggle a global credential through that path.
	AppliesToAllWorkspaces *bool  `json:"applies_to_all_workspaces,omitempty"`
	WorkspaceIDs           []int  `json:"workspace_ids,omitempty"`
	SecretMetadata         string `json:"secret_metadata,omitempty"`
	IsEnabled              *bool  `json:"is_enabled,omitempty"`
}

// UpdateActionCredentialRequest patches credential metadata. The plaintext
// secret cannot be set through this endpoint — use the rotate endpoint. Scope
// fields are only honored by the global admin path; the workspace-scoped path
// ignores them so a workspace admin can't widen a credential's reach.
type UpdateActionCredentialRequest struct {
	Name                   *string `json:"name,omitempty"`
	SecretMetadata         *string `json:"secret_metadata,omitempty"`
	IsEnabled              *bool   `json:"is_enabled,omitempty"`
	AppliesToAllWorkspaces *bool   `json:"applies_to_all_workspaces,omitempty"`
	WorkspaceIDs           *[]int  `json:"workspace_ids,omitempty"`
}

// RotateActionCredentialRequest carries only the new secret value.
type RotateActionCredentialRequest struct {
	Secret string `json:"secret"`
}

// SecretPrefixFor returns a non-sensitive fingerprint suitable for display.
// Short secrets are masked entirely so we don't accidentally leak material.
func SecretPrefixFor(plaintext string) string {
	const prefixLen = 4
	if len(plaintext) <= prefixLen*2 {
		return ""
	}
	return plaintext[:prefixLen] + "…"
}
