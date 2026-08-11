package handlers

import (
	"errors"
	"net/http"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
)

// ActionCredentialsHandler exposes CRUD for action credentials.
//
//   - Global credentials (applies_to_all_workspaces=true) and credentials
//     scoped to a workspace allowlist are managed under /admin/... and require
//     system-admin (gated at the route level).
//   - Single-workspace credentials may also be managed under
//     /workspaces/{id}/... and require PermissionActionCredentialManage in
//     that workspace; that path pins the allowlist to the path workspace and
//     ignores scope fields in the request body.
//
// The plaintext secret travels only on POST create and POST rotate; every
// response uses the sanitized DTO so ciphertext and plaintext never leave
// the server.
type ActionCredentialsHandler struct {
	service           *services.ActionCredentialService
	permissionService *services.PermissionService
	keyCache          *WorkspaceKeyCache
	auditor           *logger.Auditor
}

// NewActionCredentialsHandler builds the handler from injected services.
func NewActionCredentialsHandler(service *services.ActionCredentialService, permissionService *services.PermissionService, keyCache *WorkspaceKeyCache, auditor *logger.Auditor) *ActionCredentialsHandler {
	return &ActionCredentialsHandler{
		service:           service,
		permissionService: permissionService,
		keyCache:          keyCache,
		auditor:           auditor,
	}
}

// ListGlobal returns all credentials visible to the system-admin admin view.
// System-admin only (enforced at the route layer).
func (h *ActionCredentialsHandler) ListGlobal(w http.ResponseWriter, r *http.Request) {
	creds, err := h.service.ListAll()
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, sanitizeList(creds))
}

// ListForWorkspace returns credentials usable in this workspace: rows that
// apply to all workspaces, plus rows scoped to this workspace via the join
// table.
func (h *ActionCredentialsHandler) ListForWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireWorkspaceIDParam(w, r, h.keyCache, "workspaceId")
	if !ok {
		return
	}
	if !h.requireWorkspaceCredentialAccess(w, r, workspaceID) {
		return
	}
	creds, err := h.service.ListForWorkspace(workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, sanitizeList(creds))
}

// CreateGlobal creates a credential from the system-admin view. The request
// chooses whether the credential applies to all workspaces or is restricted
// to a workspace allowlist.
func (h *ActionCredentialsHandler) CreateGlobal(w http.ResponseWriter, r *http.Request) {
	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	req, ok := decodeJSON[models.CreateActionCredentialRequest](w, r)
	if !ok {
		return
	}
	sanitize.Apply(&req.Name, sanitize.PlainTextField)
	// SecretMetadata is a JSON blob — HTML stripping would corrupt valid
	// payloads before the service's validateSecretMetadata even sees them,
	// so it is size-capped + required to be well-formed JSON instead;
	// the service stays the semantic validator.
	if err := sanitize.ValidateJSONPayload("secret_metadata", req.SecretMetadata); err != nil {
		respondValidationError(w, r, err.Error())
		return
	}
	created, err := h.service.Create(req, &currentUser.ID)
	if err != nil {
		respondValidationError(w, r, err.Error())
		return
	}
	h.auditCredential(r, currentUser, logger.ActionActionCredentialCreate, created)
	respondJSONCreated(w, created.Sanitize())
}

// CreateForWorkspace creates a credential pinned to a single workspace.
// Requires PermissionActionCredentialManage in that workspace.
func (h *ActionCredentialsHandler) CreateForWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireWorkspaceIDParam(w, r, h.keyCache, "workspaceId")
	if !ok {
		return
	}
	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	if !h.requireWorkspaceCredentialManage(w, r, currentUser.ID, workspaceID) {
		return
	}
	req, ok := decodeJSON[models.CreateActionCredentialRequest](w, r)
	if !ok {
		return
	}
	sanitize.Apply(&req.Name, sanitize.PlainTextField)
	// SecretMetadata is a JSON blob — size-cap + well-formed-JSON gate
	// instead of HTML stripping (see CreateGlobal).
	if err := sanitize.ValidateJSONPayload("secret_metadata", req.SecretMetadata); err != nil {
		respondValidationError(w, r, err.Error())
		return
	}
	// Path scope wins — clients can't smuggle a global credential or extra
	// workspaces through a workspace endpoint.
	appliesAll := false
	req.AppliesToAllWorkspaces = &appliesAll
	req.WorkspaceIDs = []int{workspaceID}
	created, err := h.service.Create(req, &currentUser.ID)
	if err != nil {
		respondValidationError(w, r, err.Error())
		return
	}
	h.auditCredential(r, currentUser, logger.ActionActionCredentialCreate, created)
	respondJSONCreated(w, created.Sanitize())
}

// UpdateGlobal updates metadata + scope on any credential. System-admin only.
func (h *ActionCredentialsHandler) UpdateGlobal(w http.ResponseWriter, r *http.Request) {
	credentialID, ok := requireIDParam(w, r, "credentialId")
	if !ok {
		return
	}
	cred, err := h.service.Get(credentialID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "action_credential")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	h.handleUpdate(w, r, cred, true)
}

// UpdateForWorkspace updates metadata on a credential scoped to this workspace.
// Scope fields in the request body are ignored — the workspace path cannot
// widen a credential's reach.
func (h *ActionCredentialsHandler) UpdateForWorkspace(w http.ResponseWriter, r *http.Request) {
	cred, ok := h.requireWorkspaceCredential(w, r)
	if !ok {
		return
	}
	h.handleUpdate(w, r, cred, false)
}

func (h *ActionCredentialsHandler) handleUpdate(w http.ResponseWriter, r *http.Request, cred *models.ActionCredential, allowScopeChange bool) {
	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	req, ok := decodeJSON[models.UpdateActionCredentialRequest](w, r)
	if !ok {
		return
	}
	sanitize.Apply(req.Name, sanitize.PlainTextField)
	// SecretMetadata is a JSON blob — size-cap + well-formed-JSON gate
	// instead of HTML stripping (see CreateGlobal).
	if req.SecretMetadata != nil {
		if err := sanitize.ValidateJSONPayload("secret_metadata", *req.SecretMetadata); err != nil {
			respondValidationError(w, r, err.Error())
			return
		}
	}
	if !allowScopeChange {
		req.AppliesToAllWorkspaces = nil
		req.WorkspaceIDs = nil
	}
	updated, err := h.service.UpdateMetadata(cred.ID, req)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "action_credential")
			return
		}
		respondValidationError(w, r, err.Error())
		return
	}
	h.auditCredential(r, currentUser, logger.ActionActionCredentialUpdate, updated)
	respondJSONOK(w, updated.Sanitize())
}

// RotateGlobal replaces the secret on any credential. System-admin only.
func (h *ActionCredentialsHandler) RotateGlobal(w http.ResponseWriter, r *http.Request) {
	credentialID, ok := requireIDParam(w, r, "credentialId")
	if !ok {
		return
	}
	cred, err := h.service.Get(credentialID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "action_credential")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	h.handleRotate(w, r, cred)
}

// RotateForWorkspace replaces the secret on a workspace credential.
func (h *ActionCredentialsHandler) RotateForWorkspace(w http.ResponseWriter, r *http.Request) {
	cred, ok := h.requireWorkspaceCredential(w, r)
	if !ok {
		return
	}
	h.handleRotate(w, r, cred)
}

func (h *ActionCredentialsHandler) handleRotate(w http.ResponseWriter, r *http.Request, cred *models.ActionCredential) {
	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	req, ok := decodeJSON[models.RotateActionCredentialRequest](w, r)
	if !ok {
		return
	}
	rotated, err := h.service.Rotate(cred.ID, req)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "action_credential")
			return
		}
		respondValidationError(w, r, err.Error())
		return
	}
	h.auditCredential(r, currentUser, logger.ActionActionCredentialRotate, rotated)
	respondJSONOK(w, rotated.Sanitize())
}

// DeleteGlobal deletes any credential. System-admin only.
func (h *ActionCredentialsHandler) DeleteGlobal(w http.ResponseWriter, r *http.Request) {
	credentialID, ok := requireIDParam(w, r, "credentialId")
	if !ok {
		return
	}
	cred, err := h.service.Get(credentialID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "action_credential")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	h.handleDelete(w, r, cred)
}

// DeleteForWorkspace deletes a workspace credential.
func (h *ActionCredentialsHandler) DeleteForWorkspace(w http.ResponseWriter, r *http.Request) {
	cred, ok := h.requireWorkspaceCredential(w, r)
	if !ok {
		return
	}
	h.handleDelete(w, r, cred)
}

func (h *ActionCredentialsHandler) handleDelete(w http.ResponseWriter, r *http.Request, cred *models.ActionCredential) {
	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	if err := h.service.Delete(cred.ID); err != nil {
		respondInternalError(w, r, err)
		return
	}
	h.auditCredential(r, currentUser, logger.ActionActionCredentialDelete, cred)
	w.WriteHeader(http.StatusNoContent)
}

// requireWorkspaceCredential parses the workspace + credential IDs, enforces
// PermissionActionCredentialManage, and returns the credential record only
// if it belongs to that workspace. Returns 404 (not 403) when the credential
// either does not exist or does not belong to the workspace, so we don't
// leak existence of credentials in other workspaces. (Same invariant as
// CheckItemPermission in base.go.)
func (h *ActionCredentialsHandler) requireWorkspaceCredential(w http.ResponseWriter, r *http.Request) (*models.ActionCredential, bool) {
	workspaceID, ok := requireWorkspaceIDParam(w, r, h.keyCache, "workspaceId")
	if !ok {
		return nil, false
	}
	credentialID, ok := requireIDParam(w, r, "credentialId")
	if !ok {
		return nil, false
	}
	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return nil, false
	}
	if !h.requireWorkspaceCredentialManage(w, r, currentUser.ID, workspaceID) {
		return nil, false
	}
	cred, err := h.service.Get(credentialID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "action_credential")
		return nil, false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return nil, false
	}
	// Workspace path may only see credentials that are pinned to this
	// workspace (and nothing else). Credentials that apply to all workspaces,
	// or that are shared across multiple workspaces, are managed via the
	// admin path — surface as not-found here to avoid leaking those rows.
	if cred.AppliesToAllWorkspaces || len(cred.WorkspaceIDs) != 1 || cred.WorkspaceIDs[0] != workspaceID {
		respondNotFound(w, r, "action_credential")
		return nil, false
	}
	return cred, true
}

// requireWorkspaceCredentialManage gates write operations on workspace creds.
// Returns 404 on missing permission so we don't leak workspace existence to
// users with no view of it. System-admin always passes.
func (h *ActionCredentialsHandler) requireWorkspaceCredentialManage(w http.ResponseWriter, r *http.Request, userID, workspaceID int) bool {
	hasPerm, err := h.permissionService.HasWorkspacePermission(userID, workspaceID, models.PermissionActionCredentialManage)
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if !hasPerm {
		respondNotFound(w, r, "action_credential")
		return false
	}
	return true
}

// requireWorkspaceCredentialAccess gates read on workspace creds. Same as
// manage today — workspace admins are the only users who should see the list,
// since the IDs are referenced by capability config and credential selection
// is an admin task. If we later split read vs write, plumb a separate perm.
func (h *ActionCredentialsHandler) requireWorkspaceCredentialAccess(w http.ResponseWriter, r *http.Request, workspaceID int) bool {
	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return false
	}
	return h.requireWorkspaceCredentialManage(w, r, currentUser.ID, workspaceID)
}

func (h *ActionCredentialsHandler) auditCredential(r *http.Request, user *models.User, action string, cred *models.ActionCredential) {
	if user == nil || cred == nil {
		return
	}
	scope := "global"
	if !cred.AppliesToAllWorkspaces {
		scope = "scoped"
	}
	// Details intentionally hold only non-sensitive metadata. The audit
	// pipeline's sanitizeAuditDetails additionally redacts any key that
	// looks like a secret, but we don't put plaintext here either way.
	h.auditor.LogWithDetails(r, user, action, logger.ResourceActionCredential, &cred.ID, cred.Name, map[string]any{
		"credential_type": cred.CredentialType,
		"scope":           scope,
		"workspace_ids":   cred.WorkspaceIDs,
		"is_enabled":      cred.IsEnabled,
		"has_secret":      cred.EncryptedSecret != "",
	})
}

func sanitizeList(creds []*models.ActionCredential) []models.ActionCredentialSanitized {
	out := make([]models.ActionCredentialSanitized, 0, len(creds))
	for _, c := range creds {
		out = append(out, c.Sanitize())
	}
	return out
}
