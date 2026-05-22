package handlers

import (
	"errors"
	"net/http"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/services"
	"windshift/internal/utils"
)

// PageLabelHandler exposes workspace page-label CRUD and page↔label
// attachment endpoints on the bearer-token v1 surface so the ws CLI can
// drive them. Mirrors the cookie-auth handlers/page_labels.go in shape
// but goes through bearer auth + token scopes.
type PageLabelHandler struct {
	BaseHandler
	repo     *repository.PageLabelRepository
	pageAuth *services.PagePermissionService
}

// NewPageLabelHandler constructs a v1 PageLabelHandler.
func NewPageLabelHandler(db database.Database, permissionService *services.PermissionService) *PageLabelHandler {
	return &PageLabelHandler{
		BaseHandler: NewBaseHandler(db, permissionService),
		repo:        repository.NewPageLabelRepository(db),
		pageAuth:    services.NewPagePermissionService(db, permissionService),
	}
}

// --- request payloads ---

type pageLabelCreateRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type pageLabelUpdateRequest struct {
	Name  *string `json:"name,omitempty"`
	Color *string `json:"color,omitempty"`
}

type pageLabelSetRequest struct {
	LabelIDs []int `json:"label_ids"`
}

type pageLabelAddRequest struct {
	LabelID int `json:"label_id"`
}

// --- response shapes ---

type pageLabelListResponse struct {
	Items []models.PageLabel `json:"items"`
}

type pageListWithLabelsResponse struct {
	Items []models.PageLabel `json:"items"`
}

// --- endpoints ---

// ListLabels returns every page label in the workspace.
func (h *PageLabelHandler) ListLabels(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}
	wsID, ok := h.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return
	}
	if !h.checkWorkspacePerm(w, r, user.ID, wsID, models.PermissionPageView) {
		return
	}

	labels, err := h.repo.ListByWorkspace(wsID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, pageLabelListResponse{Items: labels})
}

// GetLabel returns a single page label by id.
func (h *PageLabelHandler) GetLabel(w http.ResponseWriter, r *http.Request) {
	wsID, labelID, user, ok := h.resolveWorkspaceLabel(w, r)
	if !ok {
		return
	}
	if !h.checkWorkspacePerm(w, r, user.ID, wsID, models.PermissionPageView) {
		return
	}
	label, err := h.repo.GetByID(labelID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && label.WorkspaceID != wsID) {
		h.RespondNotFound(w, r)
		return
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, label)
}

// CreateLabel inserts a new label. Requires workspace-level page.edit.
func (h *PageLabelHandler) CreateLabel(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}
	wsID, ok := h.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return
	}
	if !h.checkWorkspacePerm(w, r, user.ID, wsID, models.PermissionPageEdit) {
		return
	}

	var req pageLabelCreateRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	name := utils.SanitizeName(req.Name)
	if !h.ValidateRequiredString(w, r, name, "name") {
		return
	}
	color := req.Color
	if color == "" {
		color = "#3B82F6"
	}

	exists, err := h.repo.NameExistsInWorkspace(wsID, name, 0)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	if exists {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "a page label with this name already exists in this workspace"))
		return
	}

	id, _, err := h.repo.Create(name, color, wsID)
	if err != nil {
		// The pre-check above is racy: a concurrent Create can squeeze
		// past NameExistsInWorkspace and only fail at the DB unique
		// constraint. Mirror the pre-check's 409 so the loser of the
		// race sees the same conflict response.
		if errors.Is(err, repository.ErrDuplicateEntry) {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "a page label with this name already exists in this workspace"))
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	label, err := h.repo.GetByID(int(id))
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondCreated(w, label)
}

// UpdateLabel changes name and/or color. Partial update: only supplied
// fields are touched.
func (h *PageLabelHandler) UpdateLabel(w http.ResponseWriter, r *http.Request) {
	wsID, labelID, user, ok := h.resolveWorkspaceLabel(w, r)
	if !ok {
		return
	}
	if !h.checkWorkspacePerm(w, r, user.ID, wsID, models.PermissionPageEdit) {
		return
	}

	existing, err := h.repo.GetByID(labelID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && existing.WorkspaceID != wsID) {
		h.RespondNotFound(w, r)
		return
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}

	var req pageLabelUpdateRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}

	name := existing.Name
	if req.Name != nil {
		name = utils.SanitizeName(*req.Name)
		if name == "" {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "name is required"))
			return
		}
	}
	color := existing.Color
	if req.Color != nil {
		color = *req.Color
		if color == "" {
			color = "#3B82F6"
		}
	}

	if name != existing.Name {
		exists, eerr := h.repo.NameExistsInWorkspace(wsID, name, labelID)
		if eerr != nil {
			h.RespondInternalError(w, r)
			return
		}
		if exists {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "a page label with this name already exists in this workspace"))
			return
		}
	}

	if err := h.repo.Update(labelID, name, color); err != nil {
		// Same racy pre-check as Create: a concurrent rename can land on
		// the workspace's UNIQUE(workspace_id, name) constraint after
		// NameExistsInWorkspace reported the name was free.
		if errors.Is(err, repository.ErrDuplicateEntry) {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "a page label with this name already exists in this workspace"))
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	updated, err := h.repo.GetByID(labelID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, updated)
}

// DeleteLabel removes a label and cascades the page assignments.
func (h *PageLabelHandler) DeleteLabel(w http.ResponseWriter, r *http.Request) {
	wsID, labelID, user, ok := h.resolveWorkspaceLabel(w, r)
	if !ok {
		return
	}
	if !h.checkWorkspacePerm(w, r, user.ID, wsID, models.PermissionPageEdit) {
		return
	}

	existing, err := h.repo.GetByID(labelID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && existing.WorkspaceID != wsID) {
		h.RespondNotFound(w, r)
		return
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}

	if err := h.repo.Delete(labelID); err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondNoContent(w)
}

// ListForPage returns the labels currently attached to a page.
func (h *PageLabelHandler) ListForPage(w http.ResponseWriter, r *http.Request) {
	_, pageID, ok := h.requireWorkspacePageOp(w, r, services.PageOpView)
	if !ok {
		return
	}
	labels, err := h.repo.ListForPage(pageID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, pageListWithLabelsResponse{Items: labels})
}

// SetForPage atomically replaces the label set on a page.
func (h *PageLabelHandler) SetForPage(w http.ResponseWriter, r *http.Request) {
	wsID, pageID, ok := h.requireWorkspacePageOp(w, r, services.PageOpEdit)
	if !ok {
		return
	}
	var req pageLabelSetRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	if !h.labelsBelongToWorkspace(w, r, req.LabelIDs, wsID) {
		return
	}
	if err := h.repo.ReplaceAssignments(pageID, req.LabelIDs); err != nil {
		h.RespondInternalError(w, r)
		return
	}
	labels, err := h.repo.ListForPage(pageID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, pageListWithLabelsResponse{Items: labels})
}

// AddToPage attaches a single label to a page.
func (h *PageLabelHandler) AddToPage(w http.ResponseWriter, r *http.Request) {
	wsID, pageID, ok := h.requireWorkspacePageOp(w, r, services.PageOpEdit)
	if !ok {
		return
	}
	var req pageLabelAddRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	if req.LabelID == 0 {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "label_id is required"))
		return
	}
	if !h.labelsBelongToWorkspace(w, r, []int{req.LabelID}, wsID) {
		return
	}
	if err := h.repo.AddAssignment(pageID, req.LabelID); err != nil {
		if errors.Is(err, repository.ErrDuplicateEntry) {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "label is already attached to this page"))
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	labels, err := h.repo.ListForPage(pageID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, pageListWithLabelsResponse{Items: labels})
}

// RemoveFromPage detaches a single label from a page.
func (h *PageLabelHandler) RemoveFromPage(w http.ResponseWriter, r *http.Request) {
	_, pageID, ok := h.requireWorkspacePageOp(w, r, services.PageOpEdit)
	if !ok {
		return
	}
	labelID, ok := h.ParsePathID(w, r, "labelId", "label ID")
	if !ok {
		return
	}
	if err := h.repo.RemoveAssignment(pageID, labelID); err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondNoContent(w)
}

// --- helpers ---

func (h *PageLabelHandler) resolveWorkspaceLabel(w http.ResponseWriter, r *http.Request) (workspaceID, labelID int, user *userCtx, ok bool) {
	var u *models.User
	u, ok = h.RequireAuth(w, r)
	if !ok {
		return 0, 0, nil, false
	}
	workspaceID, ok = h.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return 0, 0, nil, false
	}
	labelID, ok = h.ParsePathID(w, r, "labelId", "label ID")
	if !ok {
		return 0, 0, nil, false
	}
	return workspaceID, labelID, &userCtx{ID: u.ID, Username: u.Username}, true
}

// requireWorkspacePageOp runs the per-page permission check for op and
// pulls {workspaceId} + {pageId}. Page-label attachments don't need the
// authenticated user beyond the permission check, so this helper drops it.
func (h *PageLabelHandler) requireWorkspacePageOp(w http.ResponseWriter, r *http.Request, op string) (workspaceID, pageID int, ok bool) {
	var u *models.User
	u, ok = h.RequireAuth(w, r)
	if !ok {
		return 0, 0, false
	}
	workspaceID, ok = h.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return 0, 0, false
	}
	pageID, ok = h.ParsePathID(w, r, "pageId", "page ID")
	if !ok {
		return 0, 0, false
	}
	can, err := h.pageAuth.Can(u.ID, workspaceID, pageID, op)
	if err != nil {
		h.RespondInternalError(w, r)
		return 0, 0, false
	}
	if !can {
		h.RespondNotFound(w, r)
		return 0, 0, false
	}
	return workspaceID, pageID, true
}

func (h *PageLabelHandler) checkWorkspacePerm(w http.ResponseWriter, r *http.Request, userID, workspaceID int, key string) bool {
	has, err := h.pageAuth.HasWorkspacePermissionFor(userID, workspaceID, key)
	if err != nil {
		h.RespondInternalError(w, r)
		return false
	}
	if !has {
		h.RespondNotFound(w, r)
		return false
	}
	return true
}

func (h *PageLabelHandler) labelsBelongToWorkspace(w http.ResponseWriter, r *http.Request, labelIDs []int, workspaceID int) bool {
	for _, id := range labelIDs {
		ws, err := h.repo.GetWorkspaceID(id)
		if errors.Is(err, repository.ErrNotFound) || (err == nil && ws != workspaceID) {
			h.RespondNotFound(w, r)
			return false
		}
		if err != nil {
			h.RespondInternalError(w, r)
			return false
		}
	}
	return true
}
