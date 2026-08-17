package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"windshift/internal/authz"
	"windshift/internal/database"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"
)

type WorkspaceHandler struct {
	db                database.Database
	repo              *repository.WorkspaceRepository
	workspaceService  *services.WorkspaceService
	permissionService *services.PermissionService
	authz             *authz.Authz
	activityTracker   *services.ActivityTracker
	keyCache          *WorkspaceKeyCache
}

// CreateWorkspaceRequest represents the request payload for creating a workspace
type CreateWorkspaceRequest struct {
	Name                string `json:"name" validate:"required,max=100"`
	Key                 string `json:"key" validate:"required,min=2,max=10,alphanum"`
	Description         string `json:"description" validate:"max=500"`
	Active              *bool  `json:"active,omitempty"` // Defaults to true if not specified
	TimeProjectID       *int   `json:"time_project_id,omitempty"`
	IsPersonal          bool   `json:"is_personal"`
	OwnerID             *int   `json:"owner_id,omitempty"`
	Icon                string `json:"icon,omitempty"`
	Color               string `json:"color,omitempty"`
	AvatarURL           string `json:"avatar_url,omitempty"`
	DefaultView         string `json:"default_view,omitempty"` // Default view when entering workspace (board, backlog, list, tree, map)
	TemplateWorkspaceID *int   `json:"template_workspace_id,omitempty"`
}

// UpdateWorkspaceRequest represents the request payload for updating a workspace
type UpdateWorkspaceRequest struct {
	Name                    *string                    `json:"name,omitempty" validate:"omitempty,min=1,max=100"`
	Key                     *string                    `json:"key,omitempty" validate:"omitempty,min=2,max=10,alphanum"`
	Description             *string                    `json:"description,omitempty" validate:"omitempty,max=500"`
	Active                  *bool                      `json:"active,omitempty"`
	TimeProjectID           models.NullableIntPatch    `json:"time_project_id,omitempty"`
	IsPersonal              *bool                      `json:"is_personal,omitempty"`
	OwnerID                 models.NullableIntPatch    `json:"owner_id,omitempty"`
	Icon                    *string                    `json:"icon,omitempty"`
	Color                   *string                    `json:"color,omitempty"`
	AvatarURL               models.NullableStringPatch `json:"avatar_url,omitempty"`
	DefaultView             *string                    `json:"default_view,omitempty"`
	InternalCommentsEnabled *bool                      `json:"internal_comments_enabled,omitempty"`
	TimeProjectCategories   *[]int                     `json:"time_project_categories,omitempty"`
	IsTemplate              *bool                      `json:"is_template,omitempty"`
}

func NewWorkspaceHandler(db database.Database, permissionService *services.PermissionService, activityTracker *services.ActivityTracker, keyCache *WorkspaceKeyCache) *WorkspaceHandler {
	authzService := authz.New(db, permissionService)
	return &WorkspaceHandler{
		db:                db,
		repo:              repository.NewWorkspaceRepository(db),
		workspaceService:  services.NewWorkspaceServiceWithAccess(db, authzService),
		permissionService: permissionService,
		authz:             authzService,
		activityTracker:   activityTracker,
		keyCache:          keyCache,
	}
}

func sanitizeWorkspaceUpdateField(value *string, sanitizer func(string) string) *string {
	if value == nil {
		return nil
	}
	sanitized := sanitizer(*value)
	return &sanitized
}

func nullableWorkspaceUpdate[T any](present bool, value *T) services.NullableUpdate[T] {
	return services.NullableUpdate[T]{Present: present, Value: value}
}

func (h *WorkspaceHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	// Get user from context
	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Check for is_personal query parameter
	isPersonalOnly := r.URL.Query().Get("is_personal") == "true"

	workspaces, err := h.repo.FindAll(currentUser.ID, isPersonalOnly)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Filter workspaces by permission
	filteredWorkspaces, err := h.filterWorkspacesByPermissions(currentUser.ID, workspaces)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Filter out inactive workspaces unless user can access them
	accessibleWorkspaces := []models.Workspace{}
	for _, ws := range filteredWorkspaces {
		// If workspace is active, include it
		if ws.Active {
			accessibleWorkspaces = append(accessibleWorkspaces, ws)
			continue
		}

		// If workspace is inactive, check if user can access it
		canAccess, err := h.canAccessInactiveWorkspace(currentUser, ws.ID)
		if err != nil {
			// Log error but don't fail the entire request
			// Just skip this workspace
			continue
		}

		if canAccess {
			accessibleWorkspaces = append(accessibleWorkspaces, ws)
		}
	}

	respondJSONOK(w, accessibleWorkspaces)
}

func (h *WorkspaceHandler) Get(w http.ResponseWriter, r *http.Request) {
	// Get user from context
	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	workspaceID, ok := requireWorkspaceIDParam(w, r, h.keyCache, "id")
	if !ok {
		return
	}

	workspace, err := h.loadWorkspaceForUser(currentUser, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "workspace")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	h.trackWorkspaceVisit(currentUser.ID, workspace.ID)
	respondJSONOK(w, workspace)
}

// loadWorkspaceForUser resolves a workspace and applies the same visibility
// rules used by the standalone detail endpoint. Access-denied workspaces are
// deliberately reported as not found to avoid disclosing their existence.
func (h *WorkspaceHandler) loadWorkspaceForUser(currentUser *models.User, workspaceID int) (*models.Workspace, error) {
	workspace, err := h.repo.FindByID(workspaceID)
	if err != nil {
		return nil, err
	}

	// Check permissions based on workspace state
	if !workspace.Active {
		// For inactive workspaces, check if user has admin access
		canAccess, err := h.canAccessInactiveWorkspace(currentUser, workspace.ID)
		if err != nil {
			return nil, err
		}
		if !canAccess {
			return nil, repository.ErrNotFound
		}
	} else {
		// For active workspaces, check if user has view permission
		canView, err := h.canViewWorkspace(currentUser.ID, workspace.ID)
		if err != nil {
			return nil, err
		}
		if !canView {
			return nil, repository.ErrNotFound
		}
	}

	// Load time project categories for this workspace
	timeProjectCats, err := h.repo.GetTimeProjectCategories(workspace.ID)
	if err != nil {
		slog.Error("failed to load time project categories", slog.String("component", "workspaces"), slog.Int("workspace_id", workspace.ID), slog.Any("error", err))
		// Don't fail the request, just log the error
		workspace.TimeProjectCategories = []int{} // Always include the field
	} else {
		workspace.TimeProjectCategories = timeProjectCats // Set even if empty
	}
	return workspace, nil
}

func (h *WorkspaceHandler) trackWorkspaceVisit(userID, workspaceID int) {
	if h.activityTracker == nil {
		return
	}
	if err := h.activityTracker.TrackWorkspaceVisit(userID, workspaceID); err != nil {
		slog.Error("failed to track workspace visit", slog.String("component", "workspaces"), slog.Int("user_id", userID), slog.Int("workspace_id", workspaceID), slog.Any("error", err))
	}
}

func (h *WorkspaceHandler) Create(w http.ResponseWriter, r *http.Request) {
	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Check if user has permission to create workspaces
	canCreate, err := h.canCreateWorkspace(user.ID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canCreate {
		respondForbidden(w, r)
		return
	}

	// Parse request
	req, ok := decodeJSON[CreateWorkspaceRequest](w, r)
	if !ok {
		return
	}

	// Validate input using validator
	if err = utils.Validate(req); err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	// Sanitize for defense in depth
	req.Name = sanitize.ShortIdentifier.Sanitize(req.Name)
	req.Key = sanitize.ShortIdentifier.Sanitize(req.Key)
	req.Description = sanitize.RichText.Sanitize(req.Description)

	// Post-sanitization validation: ensure name and key are not empty after sanitization
	if req.Name == "" {
		respondValidationError(w, r, "Workspace name is required")
		return
	}
	if req.Key == "" {
		respondValidationError(w, r, "Workspace key is required")
		return
	}

	// Default view to 'board' if not specified
	defaultView := req.DefaultView
	if defaultView == "" {
		defaultView = "board"
	}

	avatarURL := req.AvatarURL
	result, err := h.workspaceService.Create(r.Context(), services.CreateWorkspaceParams{
		Name:                req.Name,
		Key:                 req.Key,
		Description:         req.Description,
		Icon:                req.Icon,
		Color:               req.Color,
		CreatorID:           user.ID,
		Active:              req.Active,
		TimeProjectID:       req.TimeProjectID,
		IsPersonal:          req.IsPersonal,
		OwnerID:             req.OwnerID,
		AvatarURL:           &avatarURL,
		DefaultView:         defaultView,
		TemplateWorkspaceID: req.TemplateWorkspaceID,
	})
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateEntry) {
			respondConflict(w, r, "A workspace with this key already exists")
			return
		}
		if respondWorkspaceTemplateError(w, r, err) {
			return
		}
		respondInternalError(w, r, err)
		return
	}
	id := int64(result.Workspace.ID)
	// The transaction is committed even if response hydration below fails, so
	// invalidate access snapshots immediately after the successful mutation.
	h.keyCache.Invalidate()
	if h.permissionService != nil {
		h.permissionService.InvalidateActiveWorkspaceCache()
		h.permissionService.OnEveryoneAccessChanged()
	}

	// Create item number sequence for this workspace (PostgreSQL only, no-op for SQLite)
	if err = h.repo.CreateItemSequence(id); err != nil {
		slog.Warn("failed to create item sequence for workspace", slog.String("component", "workspaces"), slog.Int64("workspace_id", id), slog.Any("error", err))
	}

	// Return the created workspace with joined data
	workspace, err := h.repo.FindByID(int(id))
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if req.TemplateWorkspaceID != nil {
		h.logWorkspaceCloneAudit(r, workspace, result)
	} else {
		// Log audit event
		h.logWorkspaceAudit(r, logger.ActionWorkspaceCreate, &workspace.ID, workspace.Name, workspace.Key, workspace.Description, workspace.Active, workspace.IsPersonal)
	}

	respondJSONCreated(w, workspace)
}

func (h *WorkspaceHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireWorkspaceAdminAccess(w, r)
	if !ok {
		return
	}

	// Get the old workspace for audit logging
	oldWorkspace, err := h.repo.FindByID(id)
	if err == repository.ErrNotFound {
		respondNotFound(w, r, "workspace")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Parse request
	req, ok := decodeJSON[UpdateWorkspaceRequest](w, r)
	if !ok {
		return
	}

	// Validate input using validator
	if err = utils.Validate(req); err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	// Sanitize supplied user input for defense in depth.
	req.Name = sanitizeWorkspaceUpdateField(req.Name, sanitize.ShortIdentifier.Sanitize)
	req.Key = sanitizeWorkspaceUpdateField(req.Key, sanitize.ShortIdentifier.Sanitize)
	req.Description = sanitizeWorkspaceUpdateField(req.Description, sanitize.RichText.Sanitize)
	req.Icon = sanitizeWorkspaceUpdateField(req.Icon, sanitize.ShortIdentifier.Sanitize)
	req.Color = sanitizeWorkspaceUpdateField(req.Color, sanitize.ShortIdentifier.Sanitize)

	workspace, err := h.workspaceService.Update(services.UpdateWorkspaceParams{
		ID:                      id,
		Name:                    req.Name,
		Key:                     req.Key,
		Description:             req.Description,
		Active:                  req.Active,
		TimeProjectID:           nullableWorkspaceUpdate(req.TimeProjectID.Present, req.TimeProjectID.Value),
		IsPersonal:              req.IsPersonal,
		OwnerID:                 nullableWorkspaceUpdate(req.OwnerID.Present, req.OwnerID.Value),
		Icon:                    req.Icon,
		Color:                   req.Color,
		AvatarURL:               nullableWorkspaceUpdate(req.AvatarURL.Present, req.AvatarURL.Value),
		DefaultView:             req.DefaultView,
		InternalCommentsEnabled: req.InternalCommentsEnabled,
		TimeProjectCategories:   req.TimeProjectCategories,
		IsTemplate:              req.IsTemplate,
	})
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "workspace")
			return
		}
		if respondWorkspaceTemplateError(w, r, err) {
			return
		}
		respondInternalError(w, r, err)
		return
	}
	if h.permissionService != nil {
		h.permissionService.InvalidateActiveWorkspaceCache()
		h.permissionService.OnEveryoneAccessChanged()
	}

	// Load time project categories for the response
	timeProjectCats, err := h.repo.GetTimeProjectCategories(id)
	if err != nil {
		slog.Error("failed to load time project categories", slog.String("component", "workspaces"), slog.Int("workspace_id", id), slog.Any("error", err))
		// Don't fail the request, just log the error
		workspace.TimeProjectCategories = []int{} // Always include the field
	} else {
		workspace.TimeProjectCategories = timeProjectCats // Set even if empty
	}

	// Invalidate workspace key cache (key may have changed)
	h.keyCache.Invalidate()

	// Log audit event with change tracking
	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		details := make(map[string]any)

		// Track what changed
		if oldWorkspace.Name != workspace.Name {
			details["name_changed"] = map[string]any{
				"old": oldWorkspace.Name,
				"new": workspace.Name,
			}
		}
		if oldWorkspace.Key != workspace.Key {
			details["key_changed"] = map[string]any{
				"old": oldWorkspace.Key,
				"new": workspace.Key,
			}
		}
		if oldWorkspace.Description != workspace.Description {
			details["description_changed"] = map[string]any{
				"old": oldWorkspace.Description,
				"new": workspace.Description,
			}
		}
		if oldWorkspace.Active != workspace.Active {
			details["active_changed"] = map[string]any{
				"old": oldWorkspace.Active,
				"new": workspace.Active,
			}
		}
		if oldWorkspace.IsPersonal != workspace.IsPersonal {
			details["is_personal_changed"] = map[string]any{
				"old": oldWorkspace.IsPersonal,
				"new": workspace.IsPersonal,
			}
		}
		if oldWorkspace.Icon != workspace.Icon {
			details["icon_changed"] = map[string]any{
				"old": oldWorkspace.Icon,
				"new": workspace.Icon,
			}
		}
		if oldWorkspace.Color != workspace.Color {
			details["color_changed"] = map[string]any{
				"old": oldWorkspace.Color,
				"new": workspace.Color,
			}
		}
		if !workspaceStringPointersEqual(oldWorkspace.AvatarURL, workspace.AvatarURL) {
			details["avatar_url_changed"] = map[string]any{
				"old": workspaceStringPointerValue(oldWorkspace.AvatarURL),
				"new": workspaceStringPointerValue(workspace.AvatarURL),
			}
		}
		if oldWorkspace.InternalCommentsEnabled != workspace.InternalCommentsEnabled {
			details["internal_comments_enabled_changed"] = map[string]any{
				"old": oldWorkspace.InternalCommentsEnabled,
				"new": workspace.InternalCommentsEnabled,
			}
		}
		if oldWorkspace.IsTemplate != workspace.IsTemplate {
			details["is_template_changed"] = map[string]any{
				"old": oldWorkspace.IsTemplate,
				"new": workspace.IsTemplate,
			}
		}

		_ = logger.LogAudit(h.db, logger.AuditEvent{
			UserID:       currentUser.ID,
			Username:     currentUser.Username,
			IPAddress:    utils.GetClientIP(r),
			UserAgent:    r.UserAgent(),
			ActionType:   logger.ActionWorkspaceUpdate,
			ResourceType: logger.ResourceWorkspace,
			ResourceID:   &workspace.ID,
			ResourceName: workspace.Name,
			Details:      details,
			Success:      true,
		})
	}

	respondJSONOK(w, workspace)
}

func workspaceStringPointersEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func workspaceStringPointerValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func (h *WorkspaceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireWorkspaceAdminAccess(w, r)
	if !ok {
		return
	}

	// Get the workspace details for audit logging before deletion
	auditWorkspace, err := h.repo.FindByIDBasic(id)
	if err == repository.ErrNotFound {
		respondNotFound(w, r, "workspace")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Drop item number sequence for this workspace (PostgreSQL only, no-op for SQLite)
	if err = h.repo.DropItemSequence(int64(id)); err != nil {
		slog.Warn("failed to drop item sequence for workspace", slog.String("component", "workspaces"), slog.Int("workspace_id", id), slog.Any("error", err))
	}

	err = h.repo.Delete(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Invalidate workspace key cache
	h.keyCache.Invalidate()
	if h.permissionService != nil {
		h.permissionService.InvalidateActiveWorkspaceCache()
	}

	// Log audit event
	h.logWorkspaceAudit(r, logger.ActionWorkspaceDelete, &id, auditWorkspace.Name, auditWorkspace.Key, auditWorkspace.Description, auditWorkspace.Active, auditWorkspace.IsPersonal)

	w.WriteHeader(http.StatusNoContent)
}

// logWorkspaceAudit logs an audit event for workspace create/delete operations
// that share the same details structure (key, description, active, is_personal).
func (h *WorkspaceHandler) logWorkspaceAudit(r *http.Request, actionType string, resourceID *int, resourceName, key, description string, active, isPersonal bool) {
	currentUser := utils.GetCurrentUser(r)
	if currentUser == nil {
		return
	}
	_ = logger.LogAudit(h.db, logger.AuditEvent{
		UserID:       currentUser.ID,
		Username:     currentUser.Username,
		IPAddress:    utils.GetClientIP(r),
		UserAgent:    r.UserAgent(),
		ActionType:   actionType,
		ResourceType: logger.ResourceWorkspace,
		ResourceID:   resourceID,
		ResourceName: resourceName,
		Details: map[string]any{
			"key":         key,
			"description": description,
			"active":      active,
			"is_personal": isPersonal,
		},
		Success: true,
	})
}

// respondWorkspaceTemplateError maps the workspace-template sentinel errors to
// their documented HTTP contracts. Returns false when err is none of them.
func respondWorkspaceTemplateError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, services.ErrTemplateWorkspaceNotFound):
		respondError(w, r, restapi.NewAPIError(http.StatusNotFound, restapi.ErrCodeTemplateWorkspaceNotFound, "Template workspace not found or not visible"))
	case errors.Is(err, services.ErrInvalidWorkspaceTemplate):
		respondError(w, r, restapi.NewAPIError(http.StatusUnprocessableEntity, restapi.ErrCodeInvalidWorkspaceTemplate, "Workspace cannot be used as a template"))
	case errors.Is(err, services.ErrWorkspaceTemplateTooLarge):
		respondError(w, r, restapi.NewAPIError(http.StatusUnprocessableEntity, restapi.ErrCodeWorkspaceTemplateTooLarge, "Template workspace exceeds the seed item limit"))
	case errors.Is(err, services.ErrPersonalWorkspaceTemplate):
		respondError(w, r, restapi.NewAPIError(http.StatusUnprocessableEntity, restapi.ErrCodeInvalidWorkspaceTemplate, "Personal workspaces cannot be templates"))
	default:
		return false
	}
	return true
}

// logWorkspaceCloneAudit records one workspace.create_from_template event
// with the source workspace and copy counts.
func (h *WorkspaceHandler) logWorkspaceCloneAudit(r *http.Request, workspace *models.Workspace, result *services.CreateWorkspaceResult) {
	currentUser := utils.GetCurrentUser(r)
	if currentUser == nil {
		return
	}
	_ = logger.LogAudit(h.db, logger.AuditEvent{
		UserID:       currentUser.ID,
		Username:     currentUser.Username,
		IPAddress:    utils.GetClientIP(r),
		UserAgent:    r.UserAgent(),
		ActionType:   logger.ActionWorkspaceCreateFromTemplate,
		ResourceType: logger.ResourceWorkspace,
		ResourceID:   &workspace.ID,
		ResourceName: workspace.Name,
		Details: map[string]any{
			"key":                         workspace.Key,
			"source_workspace_id":         result.SourceWorkspaceID,
			"config_set_attached":         result.ConfigSetAttached,
			"templates_copied":            result.TemplatesCopied,
			"items_copied":                result.ItemsCopied,
			"omitted_custom_field_values": result.OmittedCustomFieldValues,
		},
		Success: true,
	})
}

// ListTemplates handles GET /workspace-templates. Returns the active,
// non-personal template workspaces visible to the caller as summary rows.
func (h *WorkspaceHandler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	summaries, err := h.workspaceService.ListTemplateSummaries(r.Context())
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	visible := make([]models.WorkspaceTemplateSummary, 0, len(summaries))
	for _, summary := range summaries {
		canView, err := h.canViewWorkspace(currentUser.ID, summary.ID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if canView {
			visible = append(visible, summary)
		}
	}

	respondJSONOK(w, visible)
}

// requireWorkspaceAdminAccess extracts the workspace ID from the request path,
// authenticates the user, and verifies admin permission on the workspace.
// Returns the workspace ID, authenticated user, and true on success.
// Writes an appropriate HTTP error and returns false on failure.
func (h *WorkspaceHandler) requireWorkspaceAdminAccess(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, ok := requireWorkspaceIDParam(w, r, h.keyCache, "id")
	if !ok {
		return 0, false
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return 0, false
	}

	canAdmin, permErr := h.canAdminWorkspace(user.ID, id)
	if permErr != nil {
		respondInternalError(w, r, permErr)
		return 0, false
	}
	if !canAdmin {
		respondForbidden(w, r)
		return 0, false
	}

	return id, true
}
