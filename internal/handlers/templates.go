package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"
)

// ItemTemplateHandler backs the Svelte admin UI for work item templates
// (WI-438) over cookie/session auth. Reads require workspace item-view (so the
// create-modal picker works for any item creator); catalog writes require
// workspace.admin — matching the admin settings page's canAdminWorkspace
// visibility, so workspace admins (not only system admins) can manage them.
// Workspace permission failures return 404 so template/workspace existence
// isn't leaked.
type ItemTemplateHandler struct {
	repo              *repository.TemplateRepository
	permissionService *services.PermissionService
	auditor           *logger.Auditor
}

// NewItemTemplateHandler creates a new ItemTemplateHandler.
func NewItemTemplateHandler(
	repo *repository.TemplateRepository,
	permissionService *services.PermissionService,
	auditor *logger.Auditor,
) *ItemTemplateHandler {
	return &ItemTemplateHandler{
		repo:              repo,
		permissionService: permissionService,
		auditor:           auditor,
	}
}

// GetAll lists templates for a workspace (optionally filtered to an item type).
func (h *ItemTemplateHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	workspaceIDStr := r.URL.Query().Get("workspace_id")
	if workspaceIDStr == "" {
		respondValidationError(w, r, "workspace_id is required")
		return
	}
	workspaceID, err := strconv.Atoi(workspaceIDStr)
	if err != nil {
		respondValidationError(w, r, "Invalid workspace_id")
		return
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	if !h.canViewWorkspace(w, r, user.ID, workspaceID) {
		return
	}

	if raw := r.URL.Query().Get("item_type_id"); raw != "" {
		typeID, terr := strconv.Atoi(raw)
		if terr != nil {
			respondValidationError(w, r, "Invalid item_type_id")
			return
		}
		templates, lerr := h.repo.ListForType(workspaceID, typeID)
		if lerr != nil {
			respondInternalError(w, r, lerr)
			return
		}
		respondJSONOK(w, templates)
		return
	}

	templates, err := h.repo.ListByWorkspace(workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, templates)
}

// Get returns a single template by ID.
func (h *ItemTemplateHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	tmpl, err := h.repo.GetByID(id)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "Template")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	if !h.canViewWorkspace(w, r, user.ID, tmpl.WorkspaceID) {
		return
	}
	respondJSONOK(w, tmpl)
}

type templateInput struct {
	Name            string `json:"name"`
	DescriptionBody string `json:"description_body"`
	Mode            string `json:"mode"`
	IsActive        *bool  `json:"is_active"`
	ItemTypeIDs     []int  `json:"item_type_ids"`
	WorkspaceID     int    `json:"workspace_id"`
}

// Create creates a new template.
func (h *ItemTemplateHandler) Create(w http.ResponseWriter, r *http.Request) {
	var input templateInput
	if err := newJSONDecoder(w, r).Decode(&input); err != nil {
		respondBadRequest(w, r, err.Error())
		return
	}
	input.Name = sanitize.ShortIdentifier.Sanitize(input.Name)
	if input.Name == "" {
		respondValidationError(w, r, "Template name is required")
		return
	}
	if input.WorkspaceID == 0 {
		respondValidationError(w, r, "workspace_id is required")
		return
	}
	if !h.requireWorkspaceAdminPermission(w, r, input.WorkspaceID) {
		return
	}

	mode := input.Mode
	if mode == "" {
		mode = models.TemplateModeSelectable
	}
	isActive := true
	if input.IsActive != nil {
		isActive = *input.IsActive
	}

	exists, err := h.repo.NameExistsInWorkspace(input.WorkspaceID, input.Name, 0)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if exists {
		respondConflict(w, r, "A template with this name already exists in this workspace")
		return
	}

	var actorID *int
	if u := utils.GetCurrentUser(r); u != nil {
		actorID = &u.ID
	}
	created, err := h.repo.Create(&models.ItemTemplate{
		WorkspaceID:     input.WorkspaceID,
		Name:            input.Name,
		DescriptionBody: input.DescriptionBody,
		Mode:            mode,
		IsActive:        isActive,
		ItemTypeIDs:     input.ItemTypeIDs,
		CreatedBy:       actorID,
		UpdatedBy:       actorID,
	})
	if err != nil {
		h.respondWriteError(w, r, err)
		return
	}

	if currentUser := utils.GetCurrentUser(r); currentUser != nil {
		h.auditor.Log(r, currentUser, logger.ActionTemplateCreate, logger.ResourceItemTemplate, &created.ID, created.Name)
	}
	respondJSONCreated(w, created)
}

// Update updates a template.
func (h *ItemTemplateHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	existing, err := h.repo.GetByID(id)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "Template")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !h.requireWorkspaceAdminPermission(w, r, existing.WorkspaceID) {
		return
	}

	var input templateInput
	if err := newJSONDecoder(w, r).Decode(&input); err != nil {
		respondBadRequest(w, r, err.Error())
		return
	}
	input.Name = sanitize.ShortIdentifier.Sanitize(input.Name)
	if input.Name == "" {
		respondValidationError(w, r, "Template name is required")
		return
	}

	if input.Name != existing.Name {
		exists, eerr := h.repo.NameExistsInWorkspace(existing.WorkspaceID, input.Name, id)
		if eerr != nil {
			respondInternalError(w, r, eerr)
			return
		}
		if exists {
			respondConflict(w, r, "A template with this name already exists in this workspace")
			return
		}
	}

	mode := input.Mode
	if mode == "" {
		mode = models.TemplateModeSelectable
	}
	isActive := true
	if input.IsActive != nil {
		isActive = *input.IsActive
	}
	var actorID *int
	if u := utils.GetCurrentUser(r); u != nil {
		actorID = &u.ID
	}

	updated := &models.ItemTemplate{
		ID:              id,
		WorkspaceID:     existing.WorkspaceID,
		Name:            input.Name,
		DescriptionBody: input.DescriptionBody,
		Mode:            mode,
		IsActive:        isActive,
		ItemTypeIDs:     input.ItemTypeIDs,
		UpdatedBy:       actorID,
	}
	if err := h.repo.Update(updated); err != nil {
		h.respondWriteError(w, r, err)
		return
	}

	result, err := h.repo.GetByID(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if currentUser := utils.GetCurrentUser(r); currentUser != nil {
		h.auditor.Log(r, currentUser, logger.ActionTemplateUpdate, logger.ResourceItemTemplate, &id, result.Name)
	}
	respondJSONOK(w, result)
}

// Delete deletes a template.
func (h *ItemTemplateHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	existing, err := h.repo.GetByID(id)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "Template")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !h.requireWorkspaceAdminPermission(w, r, existing.WorkspaceID) {
		return
	}
	if err := h.repo.Delete(id); err != nil {
		respondInternalError(w, r, err)
		return
	}
	if currentUser := utils.GetCurrentUser(r); currentUser != nil {
		h.auditor.Log(r, currentUser, logger.ActionTemplateDelete, logger.ResourceItemTemplate, &id, existing.Name)
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

func (h *ItemTemplateHandler) canViewWorkspace(w http.ResponseWriter, r *http.Request, userID, workspaceID int) bool {
	if h.permissionService == nil {
		return true
	}
	hasPermission, err := h.permissionService.HasWorkspacePermission(userID, workspaceID, models.PermissionItemView)
	if err != nil || !hasPermission {
		respondNotFound(w, r, "Template")
		return false
	}
	return true
}

// requireWorkspaceAdminPermission gates template catalog writes on workspace.admin
// (not item edit) — the catalog is a workspace-configuration concern, matching
// the admin settings page's canAdminWorkspace visibility. 404 on failure so the
// template/workspace existence isn't leaked.
func (h *ItemTemplateHandler) requireWorkspaceAdminPermission(w http.ResponseWriter, r *http.Request, workspaceID int) bool {
	if h.permissionService == nil {
		return true
	}
	user := utils.GetCurrentUser(r)
	if user == nil {
		respondUnauthorized(w, r)
		return false
	}
	hasPermission, err := h.permissionService.HasWorkspacePermission(user.ID, workspaceID, models.PermissionWorkspaceAdmin)
	if err != nil || !hasPermission {
		respondNotFound(w, r, "Template")
		return false
	}
	return true
}

func (h *ItemTemplateHandler) respondWriteError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, repository.ErrInvalidTemplateMode):
		respondValidationError(w, r, "mode must be 'selectable' or 'mandatory'")
	case errors.Is(err, repository.ErrMandatoryRequiresOneType):
		respondValidationError(w, r, "A mandatory template must target exactly one item type")
	case errors.Is(err, repository.ErrMandatoryConflict):
		respondConflict(w, r, "Another active mandatory template already exists for this item type")
	case errors.Is(err, repository.ErrDuplicateEntry):
		respondConflict(w, r, "A template with this name already exists in this workspace")
	default:
		respondInternalError(w, r, err)
	}
}
