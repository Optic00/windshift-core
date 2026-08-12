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

// PageLabelHandler serves the workspace page-label API and the
// page↔label attachment endpoints. Mirrors LabelHandler in shape but
// gates everything through PagePermissionService instead of the
// item-edit permission used by work-item labels.
//
// Authorization model (matches the pages handler):
//   - Label CRUD (Create/Update/Delete + List/Get) is gated on workspace-level
//     `page.edit` via PagePermissionService.HasWorkspacePermissionFor.
//   - Attach / Detach / List for a page is gated per-page via
//     PagePermissionService.Can(...) with view (for read) or edit (for write).
//   - Permission failures return 404 (workspace-resource access failures
//     must not leak existence).
type PageLabelHandler struct {
	repo     *repository.PageLabelRepository
	pageAuth *services.PagePermissionService
	auditor  *logger.Auditor
}

// NewPageLabelHandler constructs a PageLabelHandler.
func NewPageLabelHandler(
	repo *repository.PageLabelRepository,
	pageAuth *services.PagePermissionService,
	auditor *logger.Auditor,
) *PageLabelHandler {
	return &PageLabelHandler{repo: repo, pageAuth: pageAuth, auditor: auditor}
}

// --- workspace-scoped label CRUD ---

// List returns all page labels in the workspace, ordered by name.
func (h *PageLabelHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireWorkspaceUser(w, r)
	if !ok {
		return
	}
	if !h.requirePageView(w, r, user.ID, workspaceID) {
		return
	}

	labels, err := h.repo.ListByWorkspace(workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, labels)
}

// Get returns a single page label by id, scoped to the workspace in the URL.
func (h *PageLabelHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID, labelID, user, ok := h.requireWorkspaceLabelUser(w, r)
	if !ok {
		return
	}
	if !h.requirePageView(w, r, user.ID, workspaceID) {
		return
	}

	label, err := h.repo.GetByID(labelID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && label.WorkspaceID != workspaceID) {
		respondNotFound(w, r, "Page label")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, label)
}

// Create inserts a new page label. Requires workspace-level page.edit.
func (h *PageLabelHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireWorkspaceUser(w, r)
	if !ok {
		return
	}
	if !h.requirePageEdit(w, r, user.ID, workspaceID) {
		return
	}

	var input struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := newJSONDecoder(w, r).Decode(&input); err != nil {
		respondBadRequest(w, r, err.Error())
		return
	}

	input.Name = sanitize.ShortIdentifier.Sanitize(input.Name)
	if input.Name == "" {
		respondValidationError(w, r, "Label name is required")
		return
	}
	if input.Color == "" {
		input.Color = "#3B82F6"
	}

	exists, err := h.repo.NameExistsInWorkspace(workspaceID, input.Name, 0)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if exists {
		respondConflict(w, r, "A page label with this name already exists in this workspace")
		return
	}

	id, _, err := h.repo.Create(input.Name, input.Color, workspaceID)
	if err != nil {
		// The pre-check above is racy; a concurrent Create can squeeze
		// past it and only fail at the DB unique constraint. Translate
		// to the same 409 the pre-check returns so the API stays
		// consistent regardless of which path catches the collision.
		if errors.Is(err, repository.ErrDuplicateEntry) {
			respondConflict(w, r, "A page label with this name already exists in this workspace")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	label, err := h.repo.GetByID(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if h.auditor != nil {
		h.auditor.Log(r, user, logger.ActionPageLabelCreate, logger.ResourcePageLabel, &label.ID, label.Name)
	}

	respondJSONCreated(w, label)
}

// Update overwrites name + color. Requires workspace-level page.edit.
func (h *PageLabelHandler) Update(w http.ResponseWriter, r *http.Request) {
	workspaceID, labelID, user, ok := h.requireWorkspaceLabelUser(w, r)
	if !ok {
		return
	}
	if !h.requirePageEdit(w, r, user.ID, workspaceID) {
		return
	}

	existing, err := h.repo.GetByID(labelID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && existing.WorkspaceID != workspaceID) {
		respondNotFound(w, r, "Page label")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	var input struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := newJSONDecoder(w, r).Decode(&input); err != nil {
		respondBadRequest(w, r, err.Error())
		return
	}

	input.Name = sanitize.ShortIdentifier.Sanitize(input.Name)
	if input.Name == "" {
		respondValidationError(w, r, "Label name is required")
		return
	}
	if input.Color == "" {
		input.Color = "#3B82F6"
	}

	exists, err := h.repo.NameExistsInWorkspace(workspaceID, input.Name, labelID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if exists {
		respondConflict(w, r, "A page label with this name already exists in this workspace")
		return
	}

	if err := h.repo.Update(labelID, input.Name, input.Color); err != nil {
		// Same racy pre-check as Create: a concurrent rename can land
		// on the workspace's UNIQUE(workspace_id, name) constraint after
		// NameExistsInWorkspace reported the name was free.
		if errors.Is(err, repository.ErrDuplicateEntry) {
			respondConflict(w, r, "A page label with this name already exists in this workspace")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	label, err := h.repo.GetByID(labelID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if h.auditor != nil {
		h.auditor.Log(r, user, logger.ActionPageLabelUpdate, logger.ResourcePageLabel, &label.ID, label.Name)
	}

	respondJSONOK(w, label)
}

// Delete removes a page label (cascade removes all assignments via FK).
func (h *PageLabelHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID, labelID, user, ok := h.requireWorkspaceLabelUser(w, r)
	if !ok {
		return
	}
	if !h.requirePageEdit(w, r, user.ID, workspaceID) {
		return
	}

	existing, err := h.repo.GetByID(labelID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && existing.WorkspaceID != workspaceID) {
		respondNotFound(w, r, "Page label")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if err := h.repo.Delete(labelID); err != nil {
		respondInternalError(w, r, err)
		return
	}

	if h.auditor != nil {
		h.auditor.Log(r, user, logger.ActionPageLabelDelete, logger.ResourcePageLabel, &labelID, existing.Name)
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- page-scoped attachment endpoints ---

// ListForPage returns the labels currently attached to a page.
func (h *PageLabelHandler) ListForPage(w http.ResponseWriter, r *http.Request) {
	_, pageID, ok := h.requirePageOp(w, r, services.PageOpView)
	if !ok {
		return
	}
	h.respondPageLabels(w, r, pageID)
}

// SetForPage replaces the full set of labels on a page atomically.
// Body: {"label_ids": [int, ...]}.
func (h *PageLabelHandler) SetForPage(w http.ResponseWriter, r *http.Request) {
	workspaceID, pageID, ok := h.requirePageOp(w, r, services.PageOpEdit)
	if !ok {
		return
	}

	var input struct {
		LabelIDs []int `json:"label_ids"`
	}
	if err := newJSONDecoder(w, r).Decode(&input); err != nil {
		respondBadRequest(w, r, err.Error())
		return
	}

	if !h.labelsBelongToWorkspace(w, r, input.LabelIDs, workspaceID) {
		return
	}

	if err := h.repo.ReplaceAssignments(pageID, input.LabelIDs); err != nil {
		respondInternalError(w, r, err)
		return
	}

	h.respondPageLabels(w, r, pageID)
}

// AddToPage attaches a single label to a page.
// Body: {"label_id": int}.
func (h *PageLabelHandler) AddToPage(w http.ResponseWriter, r *http.Request) {
	workspaceID, pageID, ok := h.requirePageOp(w, r, services.PageOpEdit)
	if !ok {
		return
	}

	var input struct {
		LabelID int `json:"label_id"`
	}
	if err := newJSONDecoder(w, r).Decode(&input); err != nil {
		respondBadRequest(w, r, err.Error())
		return
	}
	if input.LabelID == 0 {
		respondValidationError(w, r, "label_id is required")
		return
	}

	if !h.labelsBelongToWorkspace(w, r, []int{input.LabelID}, workspaceID) {
		return
	}

	if err := h.repo.AddAssignment(pageID, input.LabelID); err != nil {
		if errors.Is(err, repository.ErrDuplicateEntry) {
			respondConflict(w, r, "Label is already attached to this page")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	h.respondPageLabels(w, r, pageID)
}

// RemoveFromPage detaches a single label from a page.
func (h *PageLabelHandler) RemoveFromPage(w http.ResponseWriter, r *http.Request) {
	_, pageID, ok := h.requirePageOp(w, r, services.PageOpEdit)
	if !ok {
		return
	}

	labelID, ok := requireIDParam(w, r, "labelId")
	if !ok {
		return
	}

	if err := h.repo.RemoveAssignment(pageID, labelID); err != nil {
		respondInternalError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

func (h *PageLabelHandler) requireWorkspaceUser(w http.ResponseWriter, r *http.Request) (int, *models.User, bool) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return 0, nil, false
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return 0, nil, false
	}
	return workspaceID, user, true
}

func (h *PageLabelHandler) requireWorkspaceLabelUser(w http.ResponseWriter, r *http.Request) (workspaceID, labelID int, user *models.User, ok bool) {
	workspaceID, user, ok = h.requireWorkspaceUser(w, r)
	if !ok {
		return 0, 0, nil, false
	}
	labelID, ok = requireIDParam(w, r, "labelId")
	if !ok {
		return 0, 0, nil, false
	}
	return workspaceID, labelID, user, true
}

// requirePageOp pulls {workspaceId} + {pageId} + the current user, runs the
// per-page permission check for the requested op. On failure writes the
// appropriate 404/401 and returns ok=false.
func (h *PageLabelHandler) requirePageOp(w http.ResponseWriter, r *http.Request, op string) (workspaceID, pageID int, ok bool) {
	workspaceID, ok = requireIDParam(w, r, "workspaceId")
	if !ok {
		return 0, 0, false
	}
	pageID, ok = requireIDParam(w, r, "pageId")
	if !ok {
		return 0, 0, false
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return 0, 0, false
	}
	can, err := h.pageAuth.Can(user.ID, workspaceID, pageID, op)
	if err != nil {
		respondInternalError(w, r, err)
		return 0, 0, false
	}
	if !can {
		respondNotFound(w, r, "Page")
		return 0, 0, false
	}
	return workspaceID, pageID, true
}

func (h *PageLabelHandler) requirePageView(w http.ResponseWriter, r *http.Request, userID, workspaceID int) bool {
	has, err := h.pageAuth.HasWorkspacePermissionFor(userID, workspaceID, models.PermissionPageView)
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if !has {
		respondNotFound(w, r, "Page labels")
		return false
	}
	return true
}

func (h *PageLabelHandler) requirePageEdit(w http.ResponseWriter, r *http.Request, userID, workspaceID int) bool {
	has, err := h.pageAuth.HasWorkspacePermissionFor(userID, workspaceID, models.PermissionPageEdit)
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if !has {
		respondNotFound(w, r, "Page labels")
		return false
	}
	return true
}

// labelsBelongToWorkspace prevents cross-workspace label assignment by
// confirming every supplied label id lives in the workspace from the URL.
func (h *PageLabelHandler) labelsBelongToWorkspace(w http.ResponseWriter, r *http.Request, labelIDs []int, workspaceID int) bool {
	for _, id := range labelIDs {
		ws, err := h.repo.GetWorkspaceID(id)
		if errors.Is(err, repository.ErrNotFound) || (err == nil && ws != workspaceID) {
			respondNotFound(w, r, "Page label")
			return false
		}
		if err != nil {
			respondInternalError(w, r, err)
			return false
		}
	}
	return true
}

func (h *PageLabelHandler) respondPageLabels(w http.ResponseWriter, r *http.Request, pageID int) {
	labels, err := h.repo.ListForPage(pageID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, labels)
}
