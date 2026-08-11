package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/scheduler"
	"windshift/internal/services"
)

// RecurrenceHandler adapts the cookie-auth API to the shared recurrence service.
type RecurrenceHandler struct {
	service           *services.RecurrenceService
	itemRepo          *repository.ItemRepository
	permissionService *services.PermissionService
}

// NewRecurrenceHandler creates a recurrence handler.
func NewRecurrenceHandler(recurrenceRepo *repository.RecurrenceRepository, itemRepo *repository.ItemRepository, sched *scheduler.RecurrenceScheduler, permissionService *services.PermissionService, auditor *logger.Auditor) *RecurrenceHandler {
	return &RecurrenceHandler{
		service:           services.NewRecurrenceService(recurrenceRepo, sched, auditor),
		itemRepo:          itemRepo,
		permissionService: permissionService,
	}
}

// requireItem keeps cookie-auth and existence-leak policy in the transport
// adapter while returning the item metadata needed by shared operations.
func (h *RecurrenceHandler) requireItem(w http.ResponseWriter, r *http.Request, permission string) (*models.Item, *models.User, bool) {
	itemID, ok := requireIDParam(w, r, "id")
	if !ok {
		return nil, nil, false
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return nil, nil, false
	}
	item, err := h.itemRepo.FindByID(itemID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "item")
		return nil, nil, false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return nil, nil, false
	}
	hasPermission, err := h.permissionService.HasWorkspacePermission(user.ID, item.WorkspaceID, permission)
	if err != nil || !hasPermission {
		respondNotFound(w, r, "item")
		return nil, nil, false
	}
	return item, user, true
}

func respondRecurrenceValidation(w http.ResponseWriter, r *http.Request, err error) bool {
	validationErr, ok := services.AsRecurrenceValidationError(err)
	if !ok {
		return false
	}
	respondValidationError(w, r, validationErr.Message)
	return true
}

// GetRecurrence gets the recurrence rule for an item. Missing recurrence is a
// normal state represented by JSON null.
func (h *RecurrenceHandler) GetRecurrence(w http.ResponseWriter, r *http.Request) {
	item, _, ok := h.requireItem(w, r, models.PermissionItemView)
	if !ok {
		return
	}
	rule, err := h.service.Get(item.ID)
	if errors.Is(err, repository.ErrNotFound) {
		respondJSONOK(w, nil)
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, rule)
}

// CreateRecurrence creates a recurrence rule for an item.
func (h *RecurrenceHandler) CreateRecurrence(w http.ResponseWriter, r *http.Request) {
	item, user, ok := h.requireItem(w, r, models.PermissionItemEdit)
	if !ok {
		return
	}
	req, ok := decodeJSON[models.CreateRecurrenceRequest](w, r)
	if !ok {
		return
	}
	rule, err := h.service.Create(item.ID, item.WorkspaceID, user.ID, req, services.NewAuditActorFromRequest(r, user, nil, "cookie"))
	if errors.Is(err, services.ErrRecurrenceConflict) {
		respondConflict(w, r, "Recurrence rule already exists for this item")
		return
	}
	if errors.Is(err, services.ErrRecurrenceWorkspaceLimit) {
		respondConflict(w, r, services.RecurrenceWorkspaceLimitMessage())
		return
	}
	if respondRecurrenceValidation(w, r, err) {
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONCreated(w, rule)
}

// UpdateRecurrence updates a recurrence rule.
func (h *RecurrenceHandler) UpdateRecurrence(w http.ResponseWriter, r *http.Request) {
	item, user, ok := h.requireItem(w, r, models.PermissionItemEdit)
	if !ok {
		return
	}
	req, ok := decodeJSON[models.UpdateRecurrenceRequest](w, r)
	if !ok {
		return
	}
	rule, err := h.service.Update(item.ID, req, services.NewAuditActorFromRequest(r, user, nil, "cookie"))
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "recurrence_rule")
		return
	}
	if respondRecurrenceValidation(w, r, err) {
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, rule)
}

// DeleteRecurrence deletes a recurrence rule.
func (h *RecurrenceHandler) DeleteRecurrence(w http.ResponseWriter, r *http.Request) {
	item, user, ok := h.requireItem(w, r, models.PermissionItemEdit)
	if !ok {
		return
	}
	err := h.service.Delete(item.ID, services.NewAuditActorFromRequest(r, user, nil, "cookie"))
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "recurrence_rule")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListInstances lists generated instances for a recurrence rule.
func (h *RecurrenceHandler) ListInstances(w http.ResponseWriter, r *http.Request) {
	item, _, ok := h.requireItem(w, r, models.PermissionItemView)
	if !ok {
		return
	}
	limit, offset := cookieRecurrencePagination(r)
	result, err := h.service.ListInstances(item.ID, limit, offset)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "recurrence_rule")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, map[string]any{
		"instances":  result.Items,
		"pagination": map[string]int{"limit": limit, "offset": offset, "total": result.Total},
	})
}

func cookieRecurrencePagination(r *http.Request) (limit, offset int) {
	limit, offset = 20, 0
	if value := r.URL.Query().Get("limit"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			limit = min(parsed, 100)
		}
	}
	if value := r.URL.Query().Get("offset"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	return limit, offset
}

// ForceGenerate forces immediate generation for a rule.
func (h *RecurrenceHandler) ForceGenerate(w http.ResponseWriter, r *http.Request) {
	item, user, ok := h.requireItem(w, r, models.PermissionItemEdit)
	if !ok {
		return
	}
	count, err := h.service.ForceGenerate(item.ID, services.NewAuditActorFromRequest(r, user, nil, "cookie"))
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "recurrence_rule")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, map[string]any{"instances_generated": count})
}

// ListByWorkspace lists all recurrence rules for a workspace.
func (h *RecurrenceHandler) ListByWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	hasPermission, err := h.permissionService.HasWorkspacePermission(user.ID, workspaceID, models.PermissionItemView)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !hasPermission {
		respondNotFound(w, r, "workspace")
		return
	}
	rules, err := h.service.ListByWorkspace(workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, rules)
}

// PreviewRRule previews RRULE occurrences.
func (h *RecurrenceHandler) PreviewRRule(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeJSON[models.RRulePreviewRequest](w, r)
	if !ok {
		return
	}
	preview, err := h.service.Preview(req)
	if respondRecurrenceValidation(w, r, err) {
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	dates := make([]string, len(preview.Occurrences))
	for i, occurrence := range preview.Occurrences {
		dates[i] = occurrence.Format(time.RFC3339)
	}
	respondJSONOK(w, map[string]any{
		"rrule": preview.RRule, "dtstart": preview.DtStart.Format(time.RFC3339), "occurrences": dates,
	})
}
