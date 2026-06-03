package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/sanitize"
	"windshift/internal/services"
)

// DiagramHandler exposes item-diagram CRUD on the bearer-token v1 surface so
// MCP and the ws CLI can drive Mermaid/Excalidraw diagrams the same way the
// SPA does today. Mirrors handlers/diagram.go in shape but goes through
// bearer auth + the items:* token scopes, and gates on workspace
// view/edit (404-not-403 on permission failure to avoid leaking existence).
type DiagramHandler struct {
	BaseHandler
	repo     *repository.DiagramRepository
	itemRepo *repository.ItemRepository
}

// NewDiagramHandler constructs a v1 DiagramHandler.
func NewDiagramHandler(db database.Database, permissionService *services.PermissionService) *DiagramHandler {
	return &DiagramHandler{
		BaseHandler: NewBaseHandler(db, permissionService),
		repo:        repository.NewDiagramRepository(db),
		itemRepo:    repository.NewItemRepository(db),
	}
}

type diagramWriteRequest struct {
	Name        string `json:"name"`
	DiagramData string `json:"diagram_data"`
}

type diagramListResponse struct {
	Items []models.ItemDiagram `json:"items"`
}

// requireItemAccessByPath authenticates the user, parses the path param `id`
// or `itemId`, loads the item, and checks workspace permission. The 404
// response on permission failure mirrors the items handler so item existence
// isn't leaked through the diagrams surface either.
func (h *DiagramHandler) requireItemAccessByPath(w http.ResponseWriter, r *http.Request, param string, permCheck func(int, int) (bool, error)) (*models.Item, *models.User, bool) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return nil, nil, false
	}

	itemID, ok := h.ParsePathID(w, r, param, "item ID")
	if !ok {
		return nil, nil, false
	}

	item, err := h.itemRepo.FindByID(itemID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondError(w, r, restapi.ErrItemNotFound)
			return nil, nil, false
		}
		h.RespondInternalError(w, r)
		return nil, nil, false
	}

	allowed, err := permCheck(user.ID, item.WorkspaceID)
	if err != nil || !allowed {
		h.RespondError(w, r, restapi.ErrItemNotFound)
		return nil, nil, false
	}

	return item, user, true
}

// resolveDiagramAccess loads the diagram (by `id` path param), then checks
// that the caller can view/edit the owning item's workspace. Permission
// failures collapse to 404 so a token can't probe diagram ids it isn't
// authorized to see.
func (h *DiagramHandler) resolveDiagramAccess(w http.ResponseWriter, r *http.Request, permCheck func(int, int) (bool, error)) (*models.ItemDiagram, *models.User, bool) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return nil, nil, false
	}

	diagramID, ok := h.ParsePathID(w, r, "id", "diagram ID")
	if !ok {
		return nil, nil, false
	}

	diagram, err := h.repo.GetByID(diagramID)
	if errors.Is(err, repository.ErrNotFound) {
		h.RespondNotFound(w, r)
		return nil, nil, false
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return nil, nil, false
	}

	item, err := h.itemRepo.FindByID(diagram.ItemID)
	if err != nil {
		// Orphaned diagram (item gone) — present as 404 rather than 500.
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return nil, nil, false
		}
		h.RespondInternalError(w, r)
		return nil, nil, false
	}

	allowed, err := permCheck(user.ID, item.WorkspaceID)
	if err != nil || !allowed {
		h.RespondNotFound(w, r)
		return nil, nil, false
	}

	return diagram, user, true
}

func (h *DiagramHandler) decodeWriteRequest(w http.ResponseWriter, r *http.Request) (name, diagramData string, ok bool) {
	var req diagramWriteRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return "", "", false
	}
	name = sanitize.ShortIdentifier.Sanitize(req.Name)
	if name == "" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "name is required"))
		return "", "", false
	}
	if req.DiagramData == "" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "diagram_data is required"))
		return "", "", false
	}
	return name, req.DiagramData, true
}

// ListForItem handles GET /rest/api/v1/items/{id}/diagrams
//
// @Summary      List diagrams on an item
// @Description  Returns every diagram attached to the item, newest first. Diagrams hold the opaque Mermaid/Excalidraw payload in `diagram_data`.
// @Tags         diagrams
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      int  true  "Item ID"
// @Success      200  {object}  handlers.diagramListResponse
// @Failure      400  {object}  handlers.ErrorResponse  "Invalid item ID"
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      403  {object}  handlers.ErrorResponse  "Token lacks the items:read scope"
// @Failure      404  {object}  handlers.ErrorResponse  "Item not found or not visible to caller"
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /items/{id}/diagrams [get]
func (h *DiagramHandler) ListForItem(w http.ResponseWriter, r *http.Request) {
	item, _, ok := h.requireItemAccessByPath(w, r, "id", h.Perms.CanViewWorkspace)
	if !ok {
		return
	}

	diagrams, err := h.repo.ListByItem(item.ID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, diagramListResponse{Items: diagrams})
}

// CreateForItem handles POST /rest/api/v1/items/{id}/diagrams
//
// @Summary      Create a diagram on an item
// @Description  Creates a new diagram attached to the item. `diagram_data` is an opaque payload (e.g. Mermaid source or Excalidraw JSON) the client renders.
// @Tags         diagrams
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id    path      int                          true  "Item ID"
// @Param        body  body      handlers.diagramWriteRequest true  "Diagram to create"
// @Success      201   {object}  models.ItemDiagram
// @Failure      400   {object}  handlers.ErrorResponse  "Invalid request body or missing required field"
// @Failure      401   {object}  handlers.ErrorResponse
// @Failure      403   {object}  handlers.ErrorResponse  "Token lacks the items:write scope"
// @Failure      404   {object}  handlers.ErrorResponse  "Item not found or not visible to caller"
// @Failure      500   {object}  handlers.ErrorResponse
// @Router       /items/{id}/diagrams [post]
func (h *DiagramHandler) CreateForItem(w http.ResponseWriter, r *http.Request) {
	item, user, ok := h.requireItemAccessByPath(w, r, "id", h.Perms.CanEditWorkspace)
	if !ok {
		return
	}

	name, diagramData, ok := h.decodeWriteRequest(w, r)
	if !ok {
		return
	}

	createdBy := &user.ID

	id, _, err := h.repo.Create(item.ID, name, diagramData, createdBy)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}

	// Reload through the joined-users SELECT so the response shape matches
	// what GetByID returns elsewhere (creator_name etc.).
	created, err := h.repo.GetByID(int(id))
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}

	// Best-effort history record — same pattern the cookie handler uses.
	// We don't want a history insertion failure to fail an otherwise
	// successful create, so swallow the error.
	newValue := fmt.Sprintf("diagram:%d:%s", id, name)
	_ = h.repo.RecordHistory(item.ID, user.ID, "diagram_created", nil, newValue)

	h.RespondCreated(w, created)
}

// Get handles GET /rest/api/v1/diagrams/{id}
//
// @Summary      Get a diagram by ID
// @Description  Returns 404 (not 403) when the diagram exists but its owning item isn't visible — diagram and item existence are never leaked.
// @Tags         diagrams
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      int  true  "Diagram ID"
// @Success      200  {object}  models.ItemDiagram
// @Failure      400  {object}  handlers.ErrorResponse  "Invalid diagram ID"
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      403  {object}  handlers.ErrorResponse  "Token lacks the items:read scope"
// @Failure      404  {object}  handlers.ErrorResponse  "Diagram not found or not visible to caller"
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /diagrams/{id} [get]
func (h *DiagramHandler) Get(w http.ResponseWriter, r *http.Request) {
	diagram, _, ok := h.resolveDiagramAccess(w, r, h.Perms.CanViewWorkspace)
	if !ok {
		return
	}
	h.RespondOK(w, diagram)
}

// Update handles PUT /rest/api/v1/diagrams/{id}
//
// @Summary      Update a diagram
// @Description  Overwrites both `name` and `diagram_data`. Both fields are required — partial updates aren't supported (matches the cookie surface).
// @Tags         diagrams
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id    path      int                          true  "Diagram ID"
// @Param        body  body      handlers.diagramWriteRequest true  "Replacement diagram contents"
// @Success      200   {object}  models.ItemDiagram
// @Failure      400   {object}  handlers.ErrorResponse  "Invalid request body or missing required field"
// @Failure      401   {object}  handlers.ErrorResponse
// @Failure      403   {object}  handlers.ErrorResponse  "Token lacks the items:write scope"
// @Failure      404   {object}  handlers.ErrorResponse  "Diagram not found or not visible to caller"
// @Failure      500   {object}  handlers.ErrorResponse
// @Router       /diagrams/{id} [put]
func (h *DiagramHandler) Update(w http.ResponseWriter, r *http.Request) {
	diagram, user, ok := h.resolveDiagramAccess(w, r, h.Perms.CanEditWorkspace)
	if !ok {
		return
	}

	name, diagramData, ok := h.decodeWriteRequest(w, r)
	if !ok {
		return
	}

	updatedBy := &user.ID
	if err := h.repo.Update(diagram.ID, name, diagramData, updatedBy); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return
		}
		h.RespondInternalError(w, r)
		return
	}

	// Best-effort history; show the previous name only when it changed.
	var historyOldName *string
	if diagram.Name != name {
		historyOldName = &diagram.Name
	}
	newValue := fmt.Sprintf("diagram:%d:%s", diagram.ID, name)
	_ = h.repo.RecordHistory(diagram.ItemID, user.ID, "diagram_updated", historyOldName, newValue)

	updated, err := h.repo.GetByID(diagram.ID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, updated)
}

// Delete handles DELETE /rest/api/v1/diagrams/{id}
//
// @Summary      Delete a diagram
// @Tags         diagrams
// @Security     BearerAuth
// @Param        id   path  int  true  "Diagram ID"
// @Success      204  "Diagram deleted"
// @Failure      400  {object}  handlers.ErrorResponse  "Invalid diagram ID"
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      403  {object}  handlers.ErrorResponse  "Token lacks the items:write scope"
// @Failure      404  {object}  handlers.ErrorResponse  "Diagram not found or not visible to caller"
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /diagrams/{id} [delete]
func (h *DiagramHandler) Delete(w http.ResponseWriter, r *http.Request) {
	diagram, user, ok := h.resolveDiagramAccess(w, r, h.Perms.CanEditWorkspace)
	if !ok {
		return
	}

	// Record history before deletion so the row still exists for the
	// `old_value` snapshot.
	_ = h.repo.RecordHistory(diagram.ItemID, user.ID, "diagram_deleted", &diagram.Name, diagram.Name)

	if err := h.repo.Delete(diagram.ID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	h.RespondNoContent(w)
}
