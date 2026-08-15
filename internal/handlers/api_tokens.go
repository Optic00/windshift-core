package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"windshift/internal/auth"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"
)

// defaultAPITokenLifetime is the expiry applied to non-admin tokens that omit
// expires_on. Caps the credential's lifetime so a forgotten token in CI logs
// or a stale dev machine eventually stops working without admin intervention.
// Admins keep the never-expiring option for service tokens that need it.
const defaultAPITokenLifetime = 90 * 24 * time.Hour

// authorizedViaKey tags which authorization rule unlocked a cross-user
// token operation (empty = self, "admin" = system admin, "owner" = the
// target's agent owner). Threaded on the request context only to flow into
// the audit event that fires a few lines later in the same handler.
type authorizedViaKey struct{}

func setAuthorizedVia(r *http.Request, via string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authorizedViaKey{}, via))
}

func getAuthorizedVia(r *http.Request) string {
	if v, ok := r.Context().Value(authorizedViaKey{}).(string); ok {
		return v
	}
	return ""
}

// APITokenHandler handles API token management
type APITokenHandler struct {
	tokenManager      *auth.TokenManager
	policyRepo        *repository.APITokenPolicyRepository
	workspaceRepo     *repository.WorkspaceRepository
	auditor           *logger.Auditor
	permissionService *services.PermissionService
}

// NewAPITokenHandler creates a new API token handler
func NewAPITokenHandler(tokenManager *auth.TokenManager, policyRepo *repository.APITokenPolicyRepository, workspaceRepo *repository.WorkspaceRepository, auditor *logger.Auditor, permissionService *services.PermissionService) *APITokenHandler {
	return &APITokenHandler{
		tokenManager:      tokenManager,
		policyRepo:        policyRepo,
		workspaceRepo:     workspaceRepo,
		auditor:           auditor,
		permissionService: permissionService,
	}
}

// CreateToken creates a new API token for a user
func (ath *APITokenHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	// Get user from context (set by auth middleware)
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	request, ok := decodeJSON[models.APITokenCreate](w, r)
	if !ok {
		return
	}
	if request.ExpiresAt != nil {
		respondValidationError(w, r, "expires_at is not accepted; use expires_on in YYYY-MM-DD format")
		return
	}
	if request.ExpiresOn != nil {
		_, location := services.ResolveTimezoneOrUTC(user.Timezone)
		date, err := services.ParseCivilDate(*request.ExpiresOn, location)
		if err != nil {
			respondValidationError(w, r, "expires_on must use YYYY-MM-DD format")
			return
		}
		// The token expires when the next day starts for the caller.
		expiry := date.AddDate(0, 0, 1).UTC()
		request.ExpiresAt = &expiry
	}
	sanitize.Apply(&request.Name, sanitize.PlainTextField)

	// Validate required fields
	if request.Name == "" {
		respondValidationError(w, r, "Token name is required")
		return
	}

	// Set default permissions if none provided
	if len(request.Permissions) == 0 {
		request.Permissions = auth.DefaultAgentScopes
	}

	// Validate all scopes
	if err := auth.ValidateScopes(request.Permissions); err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	// Reject admin scopes for non-admin users
	for _, scope := range request.Permissions {
		if auth.IsAdminScope(scope) {
			isAdmin, err := ath.permissionService.IsSystemAdmin(user.ID)
			if err != nil || !isAdmin {
				respondForbidden(w, r)
				return
			}
			break
		}
	}

	// Enforce API key creation policy
	if policyErr := ath.checkCreationPolicy(user.ID); policyErr != nil {
		respondForbidden(w, r)
		return
	}

	// Default expiry for non-admin callers when omitted. Admins can still mint
	// never-expiring tokens by omitting expires_on — needed for some service
	// integrations.
	if request.ExpiresAt == nil {
		isAdmin, _ := ath.permissionService.IsSystemAdmin(user.ID)
		if !isAdmin {
			expiry := time.Now().Add(defaultAPITokenLifetime)
			request.ExpiresAt = &expiry
		}
	}

	// Callers mint for themselves; admins may mint for any agent, and non-admins
	// only for owned agents. Other requests are forbidden and audited.
	targetUserID := user.ID
	if request.UserID != nil && *request.UserID != user.ID {
		isSystemAdmin, err := ath.permissionService.IsSystemAdmin(user.ID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}

		ownership, lookupErr := ath.loadAgentOwnership(*request.UserID)
		if lookupErr != nil {
			respondInternalError(w, r, lookupErr)
			return
		}
		if !ownership.Exists {
			respondNotFound(w, r, "user")
			return
		}
		if !ownership.IsActive {
			respondValidationError(w, r, "Cannot create token for inactive user")
			return
		}

		authorized := false
		via := ""
		switch {
		case isSystemAdmin && ownership.IsAgent:
			authorized = true
			via = "admin"
		case ownership.IsAgent && ownership.OwnerID != nil && *ownership.OwnerID == user.ID:
			authorized = true
			via = "owner"
		}

		if !authorized {
			// Distinguish reasons in the audit log so a genuine impersonation
			// attempt stands out from an owner-scope mismatch.
			reason := "target_not_agent"
			if ownership.IsAgent && !isSystemAdmin {
				reason = "not_agent_owner"
			}
			ath.auditor.LogFailure(r, user, logger.ActionAPITokenCreate, logger.ResourceAPIToken, nil, request.Name, "not authorized to create token for target user", map[string]any{
				"reason":         reason,
				"target_user_id": *request.UserID,
			})
			respondForbidden(w, r)
			return
		}

		if ownership.IsAgent && ownership.OwnerID == nil && tokenRequestsPageScope(request.Permissions) {
			hasPageAccess, accessErr := ath.userHasAnyPageWorkspaceAccess(*request.UserID)
			if accessErr != nil {
				respondInternalError(w, r, accessErr)
				return
			}
			if !hasPageAccess {
				respondValidationError(w, r, "Agent has no workspace page access; grant it a workspace role/page permission or remove page scopes")
				return
			}
		}

		targetUserID = *request.UserID
		r = setAuthorizedVia(r, via)
	}

	// Create token
	tokenResponse, err := ath.tokenManager.CreateToken(targetUserID, request)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	details := map[string]any{
		"token_prefix": tokenResponse.APIToken.TokenPrefix,
	}
	if targetUserID != user.ID {
		details["target_user_id"] = targetUserID
		// By this point the target has been verified as an agent; recording
		// it makes the impersonation guarantee auditable from the log alone.
		details["target_is_agent"] = true
		if via := getAuthorizedVia(r); via != "" {
			details["via"] = via
		}
	}
	ath.auditor.LogWithDetails(r, user, logger.ActionAPITokenCreate, logger.ResourceAPIToken, &tokenResponse.APIToken.ID, tokenResponse.APIToken.Name, details)

	respondJSONOK(w, tokenResponse)
}

// GetUserTokens retrieves all tokens for the current user. An optional
// ?user_id=<id> query parameter lets an admin or an agent's owner list
// tokens for that target, reusing the same canActOnUserTokens rule as the
// create/revoke paths.
func (ath *APITokenHandler) GetUserTokens(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	targetUserID := user.ID
	if q := r.URL.Query().Get("user_id"); q != "" {
		parsed, err := strconv.Atoi(q)
		if err != nil {
			respondBadRequest(w, r, "Invalid user_id")
			return
		}
		if !ath.canActOnUserTokens(user, parsed) {
			respondForbidden(w, r)
			return
		}
		targetUserID = parsed
	}

	tokens, err := ath.tokenManager.GetUserTokens(targetUserID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, tokens)
}

// resolveActionableToken parses the token ID from the URL, loads the token, and
// verifies the caller can act on it. onOwnershipFail writes the not-found or
// forbidden response — GetToken returns 403 to admins who aren't owners, while
// RevokeToken returns 404 to avoid leaking token existence.
func (ath *APITokenHandler) resolveActionableToken(
	w http.ResponseWriter, r *http.Request,
	onOwnershipFail func(http.ResponseWriter, *http.Request),
) (*models.APIToken, *models.User, bool) {
	tokenIDStr := r.PathValue("id")
	tokenID, err := strconv.Atoi(tokenIDStr)
	if err != nil {
		respondInvalidID(w, r, "token ID")
		return nil, nil, false
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return nil, nil, false
	}

	token, err := ath.tokenManager.GetTokenByID(tokenID)
	if err != nil {
		respondNotFound(w, r, "token")
		return nil, nil, false
	}
	if !ath.canActOnUserTokens(user, token.UserID) {
		onOwnershipFail(w, r)
		return nil, nil, false
	}
	return token, user, true
}

// GetToken retrieves a specific token by ID (for current user)
func (ath *APITokenHandler) GetToken(w http.ResponseWriter, r *http.Request) {
	token, _, ok := ath.resolveActionableToken(w, r, respondForbidden)
	if !ok {
		return
	}
	respondJSONOK(w, token)
}

// RevokeToken deletes/revokes a token
func (ath *APITokenHandler) RevokeToken(w http.ResponseWriter, r *http.Request) {
	token, user, ok := ath.resolveActionableToken(w, r, func(w http.ResponseWriter, r *http.Request) {
		respondNotFound(w, r, "token")
	})
	if !ok {
		return
	}

	if err := ath.tokenManager.RevokeToken(token.ID, token.UserID); err != nil {
		if err.Error() == "token not found or not owned by user" {
			respondNotFound(w, r, "token")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	ath.auditor.Log(r, user, logger.ActionAPITokenRevoke, logger.ResourceAPIToken, &token.ID, token.Name)

	w.WriteHeader(http.StatusNoContent)
}

// canActOnUserTokens returns true if the caller may list/get/revoke tokens
// belonging to targetUserID. Kept in one place so every surface enforces the
// same rule: self, or system admin on any agent, or caller owns the target agent.
func tokenRequestsPageScope(scopes []string) bool {
	for _, scope := range scopes {
		switch scope {
		case auth.ScopePagesRead, auth.ScopePagesWrite, auth.ScopePagesDelete:
			return true
		}
	}
	return false
}

func (ath *APITokenHandler) userHasAnyPageWorkspaceAccess(userID int) (bool, error) {
	workspaceIDs, err := ath.workspaceRepo.ListActiveIDs()
	if err != nil {
		return false, fmt.Errorf("load workspaces for agent page-access check: %w", err)
	}
	for _, workspaceID := range workspaceIDs {
		hasView, err := ath.permissionService.HasWorkspacePermission(userID, workspaceID, models.PermissionPageView)
		if err != nil {
			return false, err
		}
		if hasView {
			return true, nil
		}
	}
	return false, nil
}

func (ath *APITokenHandler) canActOnUserTokens(caller *models.User, targetUserID int) bool {
	if caller == nil {
		return false
	}
	if caller.ID == targetUserID {
		return true
	}
	isAdmin, _ := ath.permissionService.IsSystemAdmin(caller.ID)
	ownership, err := ath.loadAgentOwnership(targetUserID)
	if err != nil || !ownership.Exists {
		return false
	}
	if isAdmin && ownership.IsAgent {
		return true
	}
	if ownership.IsAgent && ownership.OwnerID != nil && *ownership.OwnerID == caller.ID {
		return true
	}
	return false
}

// loadAgentOwnership reads the agent flag + owner link for a user ID.
func (ath *APITokenHandler) loadAgentOwnership(userID int) (repository.AgentOwnership, error) {
	return ath.policyRepo.LoadAgentOwnership(userID)
}

// ValidateToken endpoint for testing token validity
func (ath *APITokenHandler) ValidateToken(w http.ResponseWriter, r *http.Request) {
	// This endpoint is useful for API clients to test their tokens
	// If they can call this successfully, their token is valid

	// Get user and token info from context (set by auth middleware)
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	apiToken, _ := r.Context().Value("api_token").(*models.APIToken)
	authMethod, _ := r.Context().Value("auth_method").(string)

	response := map[string]any{
		"valid":       true,
		"user_id":     user.ID,
		"username":    user.Username,
		"auth_method": authMethod,
	}

	if apiToken != nil {
		response["token_id"] = apiToken.ID
		response["token_name"] = apiToken.Name
		response["permissions"] = apiToken.Permissions
		if apiToken.ExpiresAt != nil {
			response["expires_at"] = apiToken.ExpiresAt
		}
	}

	respondJSONOK(w, response)
}

// CleanupExpiredTokens removes expired tokens (admin endpoint)
func (ath *APITokenHandler) CleanupExpiredTokens(w http.ResponseWriter, r *http.Request) {
	// This should be protected by admin middleware
	count, err := ath.tokenManager.CleanupExpiredTokens()
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		ath.auditor.LogWithDetails(r, currentUser, logger.ActionAPITokenCleanup, logger.ResourceAPIToken, nil, "", map[string]any{
			"cleaned_count": count,
		})
	}

	response := map[string]any{
		"cleaned_count": count,
		"message":       "Successfully cleaned up expired tokens",
	}

	respondJSONOK(w, response)
}

// ListAllTokens lists all non-temporary API tokens across users (admin endpoint).
// Supports ?user_id= filter and ?page=&per_page= pagination.
func (ath *APITokenHandler) ListAllTokens(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	var userIDFilter *int
	if uid := query.Get("user_id"); uid != "" {
		id, err := strconv.Atoi(uid)
		if err != nil {
			respondBadRequest(w, r, "Invalid user_id")
			return
		}
		userIDFilter = &id
	}

	page := 1
	perPage := 50
	if p := query.Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}
	if pp := query.Get("per_page"); pp != "" {
		if v, err := strconv.Atoi(pp); err == nil && v > 0 && v <= 100 {
			perPage = v
		}
	}

	offset := (page - 1) * perPage
	tokens, total, err := ath.tokenManager.ListAllTokens(userIDFilter, perPage, offset)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	totalPages := total / perPage
	if total%perPage > 0 {
		totalPages++
	}

	respondJSONOK(w, map[string]any{
		"tokens":      tokens,
		"total":       total,
		"page":        page,
		"per_page":    perPage,
		"total_pages": totalPages,
	})
}

// AdminRevokeToken revokes any token by ID (admin endpoint).
func (ath *APITokenHandler) AdminRevokeToken(w http.ResponseWriter, r *http.Request) {
	tokenID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		respondInvalidID(w, r, "token ID")
		return
	}

	// Get the token first for audit logging
	token, err := ath.tokenManager.GetTokenByID(tokenID)
	if err != nil {
		respondNotFound(w, r, "token")
		return
	}

	if err := ath.tokenManager.AdminRevokeToken(tokenID); err != nil {
		if errors.Is(err, auth.ErrTokenNotFound) {
			respondNotFound(w, r, "token")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	// Audit log
	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		tid := tokenID
		ath.auditor.LogWithDetails(r, currentUser, logger.ActionAPITokenAdminRevoke, logger.ResourceAPIToken, &tid, token.Name, map[string]any{
			"revoked_user_id": token.UserID,
		})
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetScopeCatalog returns the full catalog of grantable token scopes, with the
// label, description and grouping each picker renders. Serving it from
// auth.ScopeCatalog keeps the token UI, the agent-token UI, the OAuth client
// admin UI and the consent screens in step with the server allowlist instead
// of each maintaining a hand-written copy that drifts (WI-961).
func (ath *APITokenHandler) GetScopeCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := RequireAuth(w, r); !ok {
		return
	}
	respondJSONOK(w, auth.ScopeCatalog())
}

// GetAPIKeyPolicy returns whether the current user can create API keys.
func (ath *APITokenHandler) GetAPIKeyPolicy(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	policy := ath.getCreationPolicy()
	canCreate := true

	switch policy {
	case "disabled":
		// System admins can always create
		isAdmin, err := ath.permissionService.IsSystemAdmin(user.ID)
		if err != nil || !isAdmin {
			canCreate = false
		}
	case "groups_only":
		isAdmin, _ := ath.permissionService.IsSystemAdmin(user.ID)
		if !isAdmin {
			canCreate = ath.userInAllowedGroups(user.ID)
		}
	}

	respondJSONOK(w, map[string]any{
		"can_create": canCreate,
		"policy":     policy,
	})
}

// checkCreationPolicy enforces the API key creation policy for a user.
// Returns nil if the user is allowed to create tokens, or an error if not.
func (ath *APITokenHandler) checkCreationPolicy(userID int) error {
	// System admins always bypass
	isAdmin, _ := ath.permissionService.IsSystemAdmin(userID)
	if isAdmin {
		return nil
	}

	policy := ath.getCreationPolicy()

	switch policy {
	case "disabled":
		return fmt.Errorf("API key creation is disabled")
	case "groups_only":
		if !ath.userInAllowedGroups(userID) {
			return fmt.Errorf("API key creation is restricted to specific groups")
		}
	}

	return nil
}

// getCreationPolicy reads the api_key_creation_policy setting.
func (ath *APITokenHandler) getCreationPolicy() string {
	value, ok, err := ath.policyRepo.GetSystemSetting("api_key_creation_policy")
	if err != nil || !ok {
		return "all_users" // default
	}
	return value
}

// userInAllowedGroups checks if the user is a member of any group in the allowed list.
func (ath *APITokenHandler) userInAllowedGroups(userID int) bool {
	groupIDsJSON, ok, err := ath.policyRepo.GetSystemSetting("api_key_allowed_group_ids")
	if err != nil || !ok {
		return false
	}

	var groupIDs []int
	if err := json.Unmarshal([]byte(groupIDsJSON), &groupIDs); err != nil || len(groupIDs) == 0 {
		return false
	}

	inGroup, err := ath.policyRepo.UserInAnyGroup(userID, groupIDs)
	return err == nil && inGroup
}
