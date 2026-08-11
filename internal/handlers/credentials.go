package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
)

type CredentialHandler struct {
	repo              *repository.CredentialRepository
	auditor           *logger.Auditor
	permissionService *services.PermissionService
	sshEnabled        bool
}

func NewCredentialHandler(repo *repository.CredentialRepository, auditor *logger.Auditor, permissionService *services.PermissionService, sshEnabled bool) *CredentialHandler {
	return &CredentialHandler{
		repo:              repo,
		auditor:           auditor,
		permissionService: permissionService,
		sshEnabled:        sshEnabled,
	}
}

// SSHKeyRequest represents the request to add an SSH key
type SSHKeyRequest struct {
	CredentialName string `json:"credential_name"`
	PublicKey      string `json:"public_key"`
}

// maxSSHPublicKeyBytes bounds the stored key material (WI-185). A sanitize
// policy would mangle the base64 blob, so this is a pure length cap that
// rejects instead of truncating: even an RSA-16384 key with a generous
// comment trailer stays under 4 KiB, so 16 KiB is pathological by
// definition.
const maxSSHPublicKeyBytes = 16 * 1024

// GetUserCredentials returns all credentials for a user (both legacy and WebAuthn)
func (h *CredentialHandler) GetUserCredentials(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireIDParam(w, r, "userId")
	if !ok {
		return
	}

	if AuthorizeUserRequest(w, r, userID, h.permissionService) == nil {
		return
	}

	credentials, err := h.repo.ListForUser(userID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if credentials == nil {
		credentials = []models.UserCredential{}
	}

	respondJSONOK(w, credentials)
}

// CreateSSHKey adds an SSH public key for a user
func (h *CredentialHandler) CreateSSHKey(w http.ResponseWriter, r *http.Request) {
	if !h.sshEnabled {
		respondBadRequest(w, r, "SSH is not enabled on this server")
		return
	}

	userID, ok := requireIDParam(w, r, "userId")
	if !ok {
		return
	}

	var err error

	currentUser := AuthorizeUserRequest(w, r, userID, h.permissionService)
	if currentUser == nil {
		return
	}

	req, ok := decodeJSON[SSHKeyRequest](w, r)
	if !ok {
		return
	}
	sanitize.Apply(&req.CredentialName, sanitize.PlainTextField)

	// Validate input
	if req.CredentialName == "" {
		respondValidationError(w, r, "Credential name is required")
		return
	}

	if req.PublicKey == "" {
		respondValidationError(w, r, "Public key is required")
		return
	}

	// Basic SSH public key validation
	req.PublicKey = strings.TrimSpace(req.PublicKey)
	if len(req.PublicKey) > maxSSHPublicKeyBytes {
		respondValidationError(w, r, "Public key is too long")
		return
	}
	if !isValidSSHPublicKey(req.PublicKey) {
		respondValidationError(w, r, "Invalid SSH public key format")
		return
	}

	// Create credential data
	credentialData := map[string]any{
		"public_key": req.PublicKey,
		"key_type":   getSSHKeyType(req.PublicKey),
	}

	credentialJSON, err := json.Marshal(credentialData)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Compute fingerprint for indexed lookup
	fingerprint := services.ComputeSSHFingerprint(req.PublicKey)

	credentialID, err := h.repo.CreateSSH(userID, req.CredentialName, string(credentialJSON), fingerprint)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	h.auditor.LogWithDetails(r, currentUser, logger.ActionCredentialCreate, logger.ResourceCredential, &credentialID, req.CredentialName, map[string]any{
		"credential_type": "ssh",
		"target_user_id":  userID,
		"key_type":        getSSHKeyType(req.PublicKey),
	})

	// Return success response
	response := map[string]any{
		"id":              credentialID,
		"credential_type": "ssh",
		"name":            req.CredentialName,
		"created_at":      time.Now().Format(time.RFC3339),
	}

	respondJSONCreated(w, response)
}

// isValidSSHPublicKey performs basic validation of SSH public key format
func isValidSSHPublicKey(key string) bool {
	parts := strings.Fields(key)
	if len(parts) < 2 {
		return false
	}

	// Check for valid key types. ssh-dss (DSA) is intentionally excluded:
	// OpenSSH deprecated DSA in 7.0 and removed it from defaults in 9.8.
	keyType := parts[0]
	validTypes := []string{
		"ssh-rsa", "ssh-ed25519",
		"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
	}

	for _, validType := range validTypes {
		if keyType == validType {
			return true
		}
	}

	return false
}

// getSSHKeyType extracts the key type from an SSH public key
func getSSHKeyType(key string) string {
	parts := strings.Fields(key)
	if len(parts) > 0 {
		return parts[0]
	}
	return "unknown"
}

// RemoveCredential removes a user credential (handles both legacy and WebAuthn credentials)
func (h *CredentialHandler) RemoveCredential(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireIDParam(w, r, "userId")
	if !ok {
		return
	}

	var err error

	currentUser := AuthorizeUserRequest(w, r, userID, h.permissionService)
	if currentUser == nil {
		return
	}

	credentialIDStr := r.PathValue("credentialId")

	// Try to parse as integer for legacy credentials
	var credentialID int
	if credentialID, err = strconv.Atoi(credentialIDStr); err == nil {
		summary, err := h.repo.GetLegacySummary(credentialID, userID)

		if err == nil {
			// Delete from legacy table
			if err := h.repo.DeleteLegacy(credentialID, userID); err != nil {
				respondInternalError(w, r, err)
				return
			}

			h.auditor.LogWithDetails(r, currentUser, logger.ActionCredentialRemove, logger.ResourceCredential, &credentialID, summary.Name, map[string]any{
				"credential_type": summary.Type,
				"target_user_id":  userID,
			})

			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !errors.Is(err, repository.ErrNotFound) {
			respondInternalError(w, r, err)
			return
		}
	}

	// Check if it's a WebAuthn credential
	credName, err := h.repo.GetWebAuthnName(credentialIDStr, userID)

	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "credential")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Delete from WebAuthn table
	if err := h.repo.DeleteWebAuthn(credentialIDStr, userID); err != nil {
		respondInternalError(w, r, err)
		return
	}

	h.auditor.LogWithDetails(r, currentUser, logger.ActionWebAuthnRemove, logger.ResourceWebAuthn, nil, credName, map[string]any{
		"credential_id":  credentialIDStr,
		"target_user_id": userID,
	})

	w.WriteHeader(http.StatusNoContent)
}
