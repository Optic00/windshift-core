package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"windshift/internal/database"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"
)

type TimeProjectHandler struct {
	db                    database.Database
	projects              *repository.TimeProjectRepository
	customers             *repository.CustomerOrganisationRepository
	categories            *repository.TimeProjectCategoryRepository
	timePermissionService *services.TimePermissionService
	customerOrgPermission *services.CustomerOrganisationPermissionService
	keyCache              *WorkspaceKeyCache
}

func NewTimeProjectHandler(db database.Database, timePermissionService *services.TimePermissionService, customerOrgPermission *services.CustomerOrganisationPermissionService, keyCache *WorkspaceKeyCache) *TimeProjectHandler {
	return &TimeProjectHandler{
		db:                    db,
		projects:              repository.NewTimeProjectRepository(db),
		customers:             repository.NewCustomerOrganisationRepository(db),
		categories:            repository.NewTimeProjectCategoryRepository(db),
		timePermissionService: timePermissionService,
		customerOrgPermission: customerOrgPermission,
		keyCache:              keyCache,
	}
}

func timeProjectFromDetail(detail repository.TimeProjectDetail) models.TimeProject {
	return models.TimeProject{
		ID:            detail.ID,
		CustomerID:    detail.CustomerID,
		CategoryID:    detail.CategoryID,
		Name:          detail.Name,
		Description:   detail.Description,
		Status:        detail.Status,
		Color:         detail.Color,
		HourlyRate:    detail.HourlyRate,
		Settings:      detail.Settings,
		CreatedAt:     detail.CreatedAt,
		UpdatedAt:     detail.UpdatedAt,
		CustomerName:  detail.CustomerName,
		CategoryName:  detail.CategoryName,
		CategoryColor: detail.CategoryColor,
		TotalHours:    detail.TotalHours,
	}
}

// validateTimeProjectReferences checks that the referenced customer and category exist.
// Returns true if validation passes, false if a response has already been written.
func (h *TimeProjectHandler) validateTimeProjectReferences(w http.ResponseWriter, r *http.Request, customerID, categoryID *int) bool {
	// Customer is required
	if customerID == nil {
		respondValidationError(w, r, "Customer is required")
		return false
	}

	if _, err := h.customers.GetByID(*customerID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondValidationError(w, r, "Customer not found")
		} else {
			respondInternalError(w, r, err)
		}
		return false
	}

	// Validate category exists (if provided)
	if categoryID != nil {
		categoryExists, err := h.categories.Exists(*categoryID)
		if err != nil {
			respondInternalError(w, r, err)
			return false
		}
		if !categoryExists {
			respondValidationError(w, r, "Category not found")
			return false
		}
	}

	return true
}

func (h *TimeProjectHandler) respondTimeProjects(w http.ResponseWriter, r *http.Request, userID int, filter repository.TimeProjectListFilter) {
	var accessibleIDs []int
	if h.timePermissionService != nil {
		var err error
		accessibleIDs, err = h.timePermissionService.GetAccessibleProjects(userID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
	}
	filter.AccessibleIDs = accessibleIDs

	details, err := h.projects.ListDetailsFiltered(filter)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	projects := make([]models.TimeProject, 0, len(details))
	for _, detail := range details {
		projects = append(projects, timeProjectFromDetail(detail))
	}

	respondJSONOK(w, projects)
}

func (h *TimeProjectHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	// Get user from context
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Get accessible project IDs (nil means all accessible)
	var accessibleIDs []int
	if h.timePermissionService != nil {
		var err error
		accessibleIDs, err = h.timePermissionService.GetAccessibleProjects(user.ID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
	}

	if accessibleIDs != nil && len(accessibleIDs) == 0 {
		// User has no access to any projects (slice is non-nil but empty)
		respondJSONOK(w, []models.TimeProject{})
		return
	}

	details, err := h.projects.ListDetails(accessibleIDs, "")
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	projects := make([]models.TimeProject, 0, len(details))
	for _, detail := range details {
		projects = append(projects, timeProjectFromDetail(detail))
	}

	// Set IsManager flag for each project
	if h.timePermissionService != nil {
		for i := range projects {
			isManager, err := h.timePermissionService.IsTimeProjectManager(user.ID, projects[i].ID)
			if err != nil {
				slog.Warn("failed to check manager status", slog.Int("project_id", projects[i].ID), slog.Any("error", err))
				continue
			}
			projects[i].IsManager = isManager
		}
	}

	respondJSONOK(w, projects)
}

func (h *TimeProjectHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Get user from context
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Check view permission
	if h.timePermissionService != nil {
		canView, err := h.timePermissionService.CanViewProject(user.ID, id)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if !canView {
			// Hide existence: return 404 (matching the not-found branch below) rather than
			// 403, so a caller can't distinguish a project they lack access to from one that
			// doesn't exist (WI-293), matching Worklog.Get.
			respondNotFound(w, r, "project")
			return
		}
	}

	detail, err := h.projects.GetDetail(id)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "project")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, timeProjectFromDetail(*detail))
}

func (h *TimeProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	// Get user from context
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Check project.manage permission (required to create new projects)
	if h.timePermissionService != nil {
		hasPermission, err := h.timePermissionService.HasProjectManagePermission(user.ID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if !hasPermission {
			respondForbidden(w, r)
			return
		}
	}

	p, ok := decodeJSON[models.TimeProject](w, r)
	if !ok {
		return
	}
	warnings := sanitize.ApplyAllWithWarnings(
		sanitize.Pair{Target: &p.Name, Policy: sanitize.PlainTextField, Label: "Name"},
		sanitize.Pair{Target: &p.Description, Policy: sanitize.RichText, Label: "Description"},
		sanitize.Pair{Target: &p.Color, Policy: sanitize.ShortIdentifier, Label: "Color"},
	)

	// Set default status if not provided
	if p.Status == "" {
		p.Status = "Active"
	}

	// Validate customer and category references
	if !h.validateTimeProjectReferences(w, r, p.CustomerID, p.CategoryID) {
		return
	}

	if err := h.projects.Create(&p); err != nil {
		respondInternalError(w, r, err)
		return
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		projectID := p.ID
		logAudit(h.db, r, currentUser, logger.ActionTimeProjectCreate, logger.ResourceTimeProject, &projectID, p.Name)
	}

	respondJSONCreated(w, struct {
		models.TimeProject
		Warnings []string `json:"warnings,omitempty"`
	}{p, warnings})
}

func (h *TimeProjectHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Get user from context
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Check manager permission (required to update project)
	if h.timePermissionService != nil {
		isManager, err := h.timePermissionService.IsTimeProjectManager(user.ID, id)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if !isManager {
			respondForbidden(w, r)
			return
		}
	}

	p, ok := decodeJSON[models.TimeProject](w, r)
	if !ok {
		return
	}
	warnings := sanitize.ApplyAllWithWarnings(
		sanitize.Pair{Target: &p.Name, Policy: sanitize.PlainTextField, Label: "Name"},
		sanitize.Pair{Target: &p.Description, Policy: sanitize.RichText, Label: "Description"},
		sanitize.Pair{Target: &p.Color, Policy: sanitize.ShortIdentifier, Label: "Color"},
	)

	// Validate customer and category references
	if !h.validateTimeProjectReferences(w, r, p.CustomerID, p.CategoryID) {
		return
	}

	if err := h.projects.Update(id, &p); err != nil {
		respondInternalError(w, r, err)
		return
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		logAudit(h.db, r, currentUser, logger.ActionTimeProjectUpdate, logger.ResourceTimeProject, &id, p.Name)
	}

	respondJSONOK(w, struct {
		models.TimeProject
		Warnings []string `json:"warnings,omitempty"`
	}{p, warnings})
}

func (h *TimeProjectHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Get user from context
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Check project.manage permission (only global permission can delete projects)
	if h.timePermissionService != nil {
		hasPermission, err := h.timePermissionService.HasProjectManagePermission(user.ID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if !hasPermission {
			respondForbidden(w, r)
			return
		}
	}

	if err := h.projects.Delete(id); err != nil {
		respondInternalError(w, r, err)
		return
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		logAudit(h.db, r, currentUser, logger.ActionTimeProjectDelete, logger.ResourceTimeProject, &id, "")
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *TimeProjectHandler) GetByCustomer(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	customerID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	if h.customerOrgPermission != nil {
		canView, err := h.customerOrgPermission.CanView(user.ID, customerID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if !canView {
			respondForbidden(w, r)
			return
		}
	}

	h.respondTimeProjects(w, r, user.ID, repository.TimeProjectListFilter{CustomerID: &customerID})
}

func (h *TimeProjectHandler) GetByWorkspace(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	workspaceID, ok := requireWorkspaceIDParam(w, r, h.keyCache, "id")
	if !ok {
		return
	}

	allowedCategories, err := h.categories.ListIDsForWorkspace(workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	h.respondTimeProjects(w, r, user.ID, repository.TimeProjectListFilter{CategoryIDs: allowedCategories})
}
