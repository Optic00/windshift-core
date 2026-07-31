package handlers

import (
	"errors"
	"net/http"

	"windshift/internal/database"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/restapi/v1/middleware"
	"windshift/internal/sanitize"
	"windshift/internal/services"
)

// LabelHandler exposes workspace item-label CRUD and item↔label attachment
// endpoints on the bearer-token v1 surface. Mirrors the cookie-auth
// handlers/labels.go in shape but uses bearer auth + the items:* token
// scopes. Workspace view/edit is enforced in-handler (404-not-403 on
// permission failure to avoid leaking label / workspace existence).
type LabelHandler struct {
	BaseHandler
	repo     *repository.LabelRepository
	itemRepo *repository.ItemRepository
}

// NewLabelHandler constructs a v1 LabelHandler.
func NewLabelHandler(db database.Database, permissionService *services.PermissionService) *LabelHandler {
	return &LabelHandler{
		BaseHandler: NewBaseHandler(db, permissionService),
		repo:        repository.NewLabelRepository(db),
		itemRepo:    repository.NewItemRepository(db),
	}
}

// --- request payloads ---

type labelCreateRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type labelUpdateRequest struct {
	Name  *string `json:"name,omitempty"`
	Color *string `json:"color,omitempty"`
}

type itemLabelSetRequest struct {
	LabelIDs []int `json:"label_ids"`
}

type itemLabelAddRequest struct {
	LabelID int `json:"label_id"`
}

// --- response shapes ---

type labelListResponse struct {
	Items []models.Label `json:"items"`
}

const defaultLabelColor = "#3B82F6"

// --- workspace catalog ---

// ListForWorkspace handles GET /rest/api/v1/workspaces/{id}/labels
//
// @Summary      List workspace labels
// @Description  Returns every item label defined in the workspace, alphabetically.
// @Tags         labels
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      int  true  "Workspace ID"
// @Success      200  {object}  handlers.labelListResponse
// @Failure      400  {object}  handlers.ErrorResponse  "Invalid workspace ID"
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      403  {object}  handlers.ErrorResponse  "Token lacks the items:read scope"
// @Failure      404  {object}  handlers.ErrorResponse  "Workspace not found or not visible to caller"
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/labels [get]
func (h *LabelHandler) ListForWorkspace(w http.ResponseWriter, r *http.Request) {
	wsID, ok := h.RequireWorkspaceViewAccess(w, r)
	if !ok {
		return
	}
	labels, err := h.repo.ListByWorkspace(wsID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, labelListResponse{Items: labels})
}

// CreateInWorkspace handles POST /rest/api/v1/workspaces/{id}/labels
//
// @Summary      Create a workspace label
// @Description  Creates a new item label in the workspace. `color` defaults to a neutral blue if omitted.
// @Tags         labels
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id    path      int                       true  "Workspace ID"
// @Param        body  body      handlers.labelCreateRequest true  "Label to create"
// @Success      201   {object}  models.Label
// @Failure      400   {object}  handlers.ErrorResponse  "Invalid request body or missing required field"
// @Failure      401   {object}  handlers.ErrorResponse
// @Failure      403   {object}  handlers.ErrorResponse  "Token lacks the items:write scope"
// @Failure      404   {object}  handlers.ErrorResponse  "Workspace not found or not editable by caller"
// @Failure      409   {object}  handlers.ErrorResponse  "A label with this name already exists in the workspace"
// @Failure      500   {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/labels [post]
func (h *LabelHandler) CreateInWorkspace(w http.ResponseWriter, r *http.Request) {
	wsID, ok := h.RequireWorkspaceEditAccess(w, r)
	if !ok {
		return
	}
	var req labelCreateRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	name := sanitize.ShortIdentifier.Sanitize(req.Name)
	if !h.ValidateRequiredString(w, r, name, "name") {
		return
	}
	color := req.Color
	if color == "" {
		color = defaultLabelColor
	}

	exists, err := h.repo.NameExistsInWorkspace(wsID, name, 0)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	if exists {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "a label with this name already exists in this workspace"))
		return
	}

	id, _, err := h.repo.Create(name, color, wsID)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateEntry) {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "a label with this name already exists in this workspace"))
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
	if user := middleware.GetUser(r.Context()); user != nil {
		labelID := label.ID
		h.Auditor.Log(r, user, logger.ActionLabelCreate, logger.ResourceLabel, &labelID, label.Name)
	}
	h.RespondCreated(w, label)
}

// GetInWorkspace handles GET /rest/api/v1/workspaces/{id}/labels/{labelId}
//
// @Summary      Get a workspace label by ID
// @Tags         labels
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      int  true  "Workspace ID"
// @Param        labelId  path      int  true  "Label ID"
// @Success      200      {object}  models.Label
// @Failure      400      {object}  handlers.ErrorResponse  "Invalid workspace or label ID"
// @Failure      401      {object}  handlers.ErrorResponse
// @Failure      403      {object}  handlers.ErrorResponse  "Token lacks the items:read scope"
// @Failure      404      {object}  handlers.ErrorResponse  "Label not found in this workspace or not visible to caller"
// @Failure      500      {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/labels/{labelId} [get]
func (h *LabelHandler) GetInWorkspace(w http.ResponseWriter, r *http.Request) {
	wsID, labelID, ok := h.resolveWorkspaceLabelAccess(w, r, h.Perms.CanViewWorkspace)
	if !ok {
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

// UpdateInWorkspace handles PUT /rest/api/v1/workspaces/{id}/labels/{labelId}
//
// @Summary      Update a workspace label
// @Description  Partial update: only fields supplied in the body are touched. Pass `color` as `""` to reset to the default neutral blue.
// @Tags         labels
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      int                         true  "Workspace ID"
// @Param        labelId  path      int                         true  "Label ID"
// @Param        body     body      handlers.labelUpdateRequest true  "Fields to update"
// @Success      200      {object}  models.Label
// @Failure      400      {object}  handlers.ErrorResponse  "Invalid request body or invalid field"
// @Failure      401      {object}  handlers.ErrorResponse
// @Failure      403      {object}  handlers.ErrorResponse  "Token lacks the items:write scope"
// @Failure      404      {object}  handlers.ErrorResponse  "Label not found in this workspace or not editable by caller"
// @Failure      409      {object}  handlers.ErrorResponse  "Another label in this workspace already has the new name"
// @Failure      500      {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/labels/{labelId} [put]
func (h *LabelHandler) UpdateInWorkspace(w http.ResponseWriter, r *http.Request) {
	wsID, labelID, ok := h.resolveWorkspaceLabelAccess(w, r, h.Perms.CanEditWorkspace)
	if !ok {
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

	var req labelUpdateRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}

	name := existing.Name
	if req.Name != nil {
		name = sanitize.ShortIdentifier.Sanitize(*req.Name)
		if name == "" {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "name is required"))
			return
		}
	}
	color := existing.Color
	if req.Color != nil {
		color = *req.Color
		if color == "" {
			color = defaultLabelColor
		}
	}

	if name != existing.Name {
		exists, eerr := h.repo.NameExistsInWorkspace(wsID, name, labelID)
		if eerr != nil {
			h.RespondInternalError(w, r)
			return
		}
		if exists {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "a label with this name already exists in this workspace"))
			return
		}
	}

	if err := h.repo.Update(labelID, name, color); err != nil {
		if errors.Is(err, repository.ErrDuplicateEntry) {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "a label with this name already exists in this workspace"))
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
	if user := middleware.GetUser(r.Context()); user != nil {
		h.Auditor.Log(r, user, logger.ActionLabelUpdate, logger.ResourceLabel, &labelID, updated.Name)
	}
	h.RespondOK(w, updated)
}

// DeleteInWorkspace handles DELETE /rest/api/v1/workspaces/{id}/labels/{labelId}
//
// @Summary      Delete a workspace label
// @Description  Cascade-removes the label from every item it was attached to.
// @Tags         labels
// @Security     BearerAuth
// @Param        id       path      int  true  "Workspace ID"
// @Param        labelId  path      int  true  "Label ID"
// @Success      204      "Label deleted"
// @Failure      400      {object}  handlers.ErrorResponse  "Invalid workspace or label ID"
// @Failure      401      {object}  handlers.ErrorResponse
// @Failure      403      {object}  handlers.ErrorResponse  "Token lacks the items:write scope"
// @Failure      404      {object}  handlers.ErrorResponse  "Label not found in this workspace or not editable by caller"
// @Failure      500      {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/labels/{labelId} [delete]
func (h *LabelHandler) DeleteInWorkspace(w http.ResponseWriter, r *http.Request) {
	wsID, labelID, ok := h.resolveWorkspaceLabelAccess(w, r, h.Perms.CanEditWorkspace)
	if !ok {
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
	if user := middleware.GetUser(r.Context()); user != nil {
		h.Auditor.Log(r, user, logger.ActionLabelDelete, logger.ResourceLabel, &labelID, existing.Name)
	}
	h.RespondNoContent(w)
}

// --- item attachments ---

// ListForItem handles GET /rest/api/v1/items/{id}/labels
//
// @Summary      List labels attached to an item
// @Tags         labels
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      int  true  "Item ID"
// @Success      200  {object}  handlers.labelListResponse
// @Failure      400  {object}  handlers.ErrorResponse  "Invalid item ID"
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      403  {object}  handlers.ErrorResponse  "Token lacks the items:read scope"
// @Failure      404  {object}  handlers.ErrorResponse  "Item not found or not visible to caller"
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /items/{id}/labels [get]
func (h *LabelHandler) ListForItem(w http.ResponseWriter, r *http.Request) {
	item, ok := h.resolveItemAccess(w, r, h.Perms.CanViewWorkspace)
	if !ok {
		return
	}
	h.respondItemLabels(w, r, item.ID)
}

// SetForItem handles PUT /rest/api/v1/items/{id}/labels
//
// @Summary      Replace the label set on an item
// @Description  Atomically replaces every label attached to the item with the supplied IDs. Labels must belong to the item's workspace.
// @Tags         labels
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id    path      int                          true  "Item ID"
// @Param        body  body      handlers.itemLabelSetRequest true  "Replacement label set"
// @Success      200   {object}  handlers.labelListResponse
// @Failure      400   {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401   {object}  handlers.ErrorResponse
// @Failure      403   {object}  handlers.ErrorResponse  "Token lacks the items:write scope"
// @Failure      404   {object}  handlers.ErrorResponse  "Item not found, not editable, or label doesn't belong to the workspace"
// @Failure      500   {object}  handlers.ErrorResponse
// @Router       /items/{id}/labels [put]
func (h *LabelHandler) SetForItem(w http.ResponseWriter, r *http.Request) {
	item, ok := h.resolveItemAccess(w, r, h.Perms.CanEditWorkspace)
	if !ok {
		return
	}
	var req itemLabelSetRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	if !h.labelsBelongToWorkspace(w, r, req.LabelIDs, item.WorkspaceID) {
		return
	}
	if err := h.repo.ReplaceItemLabels(item.ID, req.LabelIDs); err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.respondItemLabels(w, r, item.ID)
}

// AddToItem handles POST /rest/api/v1/items/{id}/labels
//
// @Summary      Attach a single label to an item
// @Tags         labels
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id    path      int                          true  "Item ID"
// @Param        body  body      handlers.itemLabelAddRequest true  "Label to attach"
// @Success      200   {object}  handlers.labelListResponse
// @Failure      400   {object}  handlers.ErrorResponse  "Invalid request body or missing required field"
// @Failure      401   {object}  handlers.ErrorResponse
// @Failure      403   {object}  handlers.ErrorResponse  "Token lacks the items:write scope"
// @Failure      404   {object}  handlers.ErrorResponse  "Item not found, not editable, or label doesn't belong to the workspace"
// @Failure      409   {object}  handlers.ErrorResponse  "Label is already attached to this item"
// @Failure      500   {object}  handlers.ErrorResponse
// @Router       /items/{id}/labels [post]
func (h *LabelHandler) AddToItem(w http.ResponseWriter, r *http.Request) {
	item, ok := h.resolveItemAccess(w, r, h.Perms.CanEditWorkspace)
	if !ok {
		return
	}
	var req itemLabelAddRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	if req.LabelID == 0 {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "label_id is required"))
		return
	}
	if !h.labelsBelongToWorkspace(w, r, []int{req.LabelID}, item.WorkspaceID) {
		return
	}
	if err := h.repo.AddItemLabel(item.ID, req.LabelID); err != nil {
		if errors.Is(err, repository.ErrDuplicateEntry) {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "label is already attached to this item"))
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	h.respondItemLabels(w, r, item.ID)
}

// RemoveFromItem handles DELETE /rest/api/v1/items/{id}/labels/{labelId}
//
// @Summary      Detach a label from an item
// @Tags         labels
// @Security     BearerAuth
// @Param        id       path  int  true  "Item ID"
// @Param        labelId  path  int  true  "Label ID"
// @Success      204      "Label detached"
// @Failure      400      {object}  handlers.ErrorResponse  "Invalid item or label ID"
// @Failure      401      {object}  handlers.ErrorResponse
// @Failure      403      {object}  handlers.ErrorResponse  "Token lacks the items:write scope"
// @Failure      404      {object}  handlers.ErrorResponse  "Item not found or not editable by caller"
// @Failure      500      {object}  handlers.ErrorResponse
// @Router       /items/{id}/labels/{labelId} [delete]
func (h *LabelHandler) RemoveFromItem(w http.ResponseWriter, r *http.Request) {
	item, ok := h.resolveItemAccess(w, r, h.Perms.CanEditWorkspace)
	if !ok {
		return
	}
	labelID, ok := h.ParsePathID(w, r, "labelId", "label ID")
	if !ok {
		return
	}
	if err := h.repo.RemoveItemLabel(item.ID, labelID); err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondNoContent(w)
}

// --- helpers ---

// resolveWorkspaceLabelAccess parses {id} (workspace) + {labelId} from the
// path and verifies the caller has the given permission on the workspace.
// Permission failure returns 404 so the caller can't probe workspace IDs.
func (h *LabelHandler) resolveWorkspaceLabelAccess(w http.ResponseWriter, r *http.Request, permCheck func(int, int) (bool, error)) (workspaceID, labelID int, ok bool) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return 0, 0, false
	}
	workspaceID, ok = h.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return 0, 0, false
	}
	labelID, ok = h.ParsePathID(w, r, "labelId", "label ID")
	if !ok {
		return 0, 0, false
	}
	allowed, err := permCheck(user.ID, workspaceID)
	if err != nil || !allowed {
		h.RespondNotFound(w, r)
		return 0, 0, false
	}
	return workspaceID, labelID, true
}

// resolveItemAccess loads the item from path param `id` and applies the
// workspace permission check. 404 on permission failure so item existence
// isn't leaked through the labels surface.
func (h *LabelHandler) resolveItemAccess(w http.ResponseWriter, r *http.Request, permCheck func(int, int) (bool, error)) (*models.Item, bool) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return nil, false
	}
	itemID, ok := h.ParsePathID(w, r, "id", "item ID")
	if !ok {
		return nil, false
	}
	item, err := h.itemRepo.FindByID(itemID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondError(w, r, restapi.ErrItemNotFound)
			return nil, false
		}
		h.RespondInternalError(w, r)
		return nil, false
	}
	allowed, err := permCheck(user.ID, item.WorkspaceID)
	if err != nil || !allowed {
		h.RespondError(w, r, restapi.ErrItemNotFound)
		return nil, false
	}
	return item, true
}

// labelsBelongToWorkspace ensures every supplied label ID lives in the
// expected workspace. Missing or cross-workspace IDs surface as 404 so an
// agent can't learn labels exist in other workspaces by trying them here.
func (h *LabelHandler) labelsBelongToWorkspace(w http.ResponseWriter, r *http.Request, labelIDs []int, workspaceID int) bool {
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

func (h *LabelHandler) respondItemLabels(w http.ResponseWriter, r *http.Request, itemID int) {
	labels, err := h.repo.ListForItem(itemID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, labelListResponse{Items: labels})
}
