package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/restapi/v1/dto"
	"windshift/internal/restapi/v1/middleware"
	"windshift/internal/services"
)

// PageHandler exposes workspace knowledge pages on the bearer-token v1
// surface so the ws CLI can drive them. Mirrors the cookie-auth
// handlers/pages.go surface but goes through bearer auth + token scopes
// and emits the public DTO shape.
type PageHandler struct {
	BaseHandler
	service     *services.PageService
	application *services.PageApplicationService
	pageAuth    *services.PagePermissionService
}

// NewPageHandler constructs a v1 PageHandler. HATEOAS links are derived
// per-request via getBaseURL so the response surface matches the host
// the caller hit (correct behavior behind reverse proxies). Wires a
// PageLabelRepository onto the service so List/Get responses preload
// the page's labels.
func NewPageHandler(db database.Database, permissionService *services.PermissionService) *PageHandler {
	pageAuth := services.NewPagePermissionService(db, permissionService)
	svc := services.NewPageService(db)
	svc.SetPageLabelRepository(repository.NewPageLabelRepository(db))
	return &PageHandler{
		BaseHandler: NewBaseHandler(db, permissionService),
		service:     svc,
		application: services.NewPageApplicationService(svc, pageAuth),
		pageAuth:    pageAuth,
	}
}

// SetPageApplicationService wires the production instance shared with the
// cookie and MCP adapters.
func (h *PageHandler) SetPageApplicationService(application *services.PageApplicationService) {
	if application == nil {
		return
	}
	h.application = application
	h.service = application.PageService()
}

// --- request payloads ---

type pageCreateRequest struct {
	Title    string         `json:"title"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Content  string         `json:"content"`
	ParentID *int           `json:"parent_id,omitempty"`
	IsHome   bool           `json:"is_home,omitempty"`
}

// pageUpdateRequest is a partial-update payload: only fields supplied get
// touched. inherit_permissions is deliberately absent — inheritance
// changes have their own admin-gated PATCH /inheritance endpoint (not
// yet on v1; the cookie surface has it). Allowing it here would let an
// editor flip the flag via a normal save.
type pageUpdateRequest struct {
	Title               *string         `json:"title,omitempty"`
	Metadata            *map[string]any `json:"metadata,omitempty"`
	Content             *string         `json:"content,omitempty"`
	ExpectedContentHash *string         `json:"expected_content_hash,omitempty"`
}

type pageMoveRequest struct {
	DestinationWorkspaceID *int `json:"destination_workspace_id,omitempty"`
	ParentID               *int `json:"parent_id"`
	PrevSiblingID          *int `json:"prev_sibling_id,omitempty"`
	NextSiblingID          *int `json:"next_sibling_id,omitempty"`
}

type pageGrantPermissionRequest struct {
	PrincipalType   string `json:"principal_type"`
	PrincipalID     int    `json:"principal_id"`
	PermissionLevel string `json:"permission_level"`
}

type pageSetInheritanceRequest struct {
	InheritPermissions bool `json:"inherit_permissions"`
}

func marshalPageMetadata(metadata map[string]any) json.RawMessage {
	if metadata == nil {
		return nil
	}
	b, err := json.Marshal(metadata)
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}

// --- response shapes ---

type pageListResponse struct {
	Items []dto.PageResponse `json:"items"`
}

type pageHistoryListResponse struct {
	Items []dto.PageRevisionResponse `json:"items"`
}

type pagePermissionsResponse struct {
	PageID             int                     `json:"page_id"`
	InheritPermissions bool                    `json:"inherit_permissions"`
	EffectiveLevel     string                  `json:"effective_level,omitempty"`
	ACL                []models.PagePermission `json:"acl"`
}

// --- endpoints ---

// List handles GET /rest/api/v1/workspaces/{id}/pages
//
// List returns every page in the workspace the caller can view. Returns
// a flat list sorted depth-first; the CLI assembles the tree client-side.
//
// @Summary      List pages in a workspace
// @Description  Returns every page in the workspace the caller can view, flat list sorted depth-first.
// @Tags         pages
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      int  true  "Workspace ID"
// @Success      200  {object}  handlers.pageListResponse
// @Failure      400  {object}  handlers.ErrorResponse  "Invalid workspace ID"
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      404  {object}  handlers.ErrorResponse  "Workspace not found or not visible to caller"
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/pages [get]
func (h *PageHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}
	wsID, ok := h.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return
	}

	// ListTreeMeta omits bodies; callers fetch content on demand to keep large
	// workspace lists small.
	pages, err := h.service.ListTreeMeta(wsID, false)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}

	ids := make([]int, len(pages))
	for i := range pages {
		ids[i] = pages[i].ID
	}
	visible, err := h.pageAuth.ListVisiblePageIDs(user.ID, wsID, ids)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}

	// Preload labels in one batch before mapping the visible pages.
	if err := h.service.PreloadLabels(pages); err != nil {
		h.RespondInternalError(w, r)
		return
	}

	items := make([]dto.PageResponse, 0, len(pages))
	for i := range pages {
		if !visible[pages[i].ID] {
			continue
		}
		items = append(items, dto.MapPageToResponse(&pages[i], getBaseURL(r)))
	}
	h.RespondOK(w, pageListResponse{Items: items})
}

// Search handles GET /rest/api/v1/workspaces/{id}/pages/search
//
// Search returns visible, non-archived pages whose title or Markdown body
// contains the q substring. Bodies are omitted from the discovery response;
// fetch a match via GET .../pages/{pageId}.
//
// @Summary      Search pages by keyword
// @Description  Returns visible, non-archived pages whose title or Markdown body contains the q substring. Bodies are omitted; fetch via GET .../pages/{pageId}.
// @Tags         pages
// @Produce      json
// @Security     BearerAuth
// @Param        id     path      int     true   "Workspace ID"
// @Param        q      query     string  true   "Title or content search substring"
// @Param        limit  query     int     false  "Maximum results to return (default 20, max 100)"
// @Success      200    {object}  handlers.pageListResponse
// @Failure      400    {object}  handlers.ErrorResponse  "Invalid workspace ID"
// @Failure      401    {object}  handlers.ErrorResponse
// @Failure      404    {object}  handlers.ErrorResponse  "Workspace not found or not visible to caller"
// @Failure      500    {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/pages/search [get]
func (h *PageHandler) Search(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}
	wsID, ok := h.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	pages, err := h.service.SearchByKeyword(wsID, query, parseSearchLimit(r))
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}

	ids := make([]int, len(pages))
	for i := range pages {
		ids[i] = pages[i].ID
	}
	visible, err := h.pageAuth.ListVisiblePageIDs(user.ID, wsID, ids)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	if err := h.service.PreloadLabels(pages); err != nil {
		h.RespondInternalError(w, r)
		return
	}

	items := make([]dto.PageResponse, 0, len(pages))
	for i := range pages {
		if !visible[pages[i].ID] {
			continue
		}
		// Search returns metadata; fetch the body from the page endpoint.
		pages[i].Content = ""
		items = append(items, dto.MapPageToResponse(&pages[i], getBaseURL(r)))
	}
	h.RespondOK(w, pageListResponse{Items: items})
}

// Get handles GET /rest/api/v1/workspaces/{id}/pages/{pageId}
//
// Get returns a single page by id. 404 on missing or no view permission.
//
// @Summary      Get a page by ID
// @Tags         pages
// @Produce      json
// @Security     BearerAuth
// @Param        id      path      int  true  "Workspace ID"
// @Param        pageId  path      int  true  "Page ID"
// @Success      200     {object}  dto.PageResponse
// @Failure      400     {object}  handlers.ErrorResponse  "Invalid workspace or page ID"
// @Failure      401     {object}  handlers.ErrorResponse
// @Failure      404     {object}  handlers.ErrorResponse  "Page not found or you lack page.view"
// @Failure      500     {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/pages/{pageId} [get]
func (h *PageHandler) Get(w http.ResponseWriter, r *http.Request) {
	wsID, pageID, ok := h.requireWorkspacePageView(w, r)
	if !ok {
		return
	}
	page, err := h.service.GetByID(pageID)
	if err != nil || page.WorkspaceID != wsID {
		h.RespondNotFound(w, r)
		return
	}
	if err := h.service.PreloadLabelsForPage(page); err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, dto.MapPageToResponse(page, getBaseURL(r)))
}

// Create handles POST /rest/api/v1/workspaces/{id}/pages
//
// Create creates a new page. Requires pages:write scope and page.create
// on the workspace (or page.admin / workspace.admin / system.admin).
// When parent_id is set the caller must also be able to edit the parent.
//
// @Summary      Create a page
// @Description  Creates a new page in the workspace. When parent_id is set the caller must be able to edit the parent.
// @Tags         pages
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id    path      int                          true  "Workspace ID"
// @Param        body  body      handlers.pageCreateRequest   true  "Page to create"
// @Success      201   {object}  dto.PageResponse
// @Failure      400   {object}  handlers.ErrorResponse  "Invalid request body or missing required field"
// @Failure      401   {object}  handlers.ErrorResponse
// @Failure      404   {object}  handlers.ErrorResponse  "Workspace or parent page not visible to caller, or caller lacks page.create"
// @Failure      409   {object}  handlers.ErrorResponse  "Page-tree depth exceeded or a uniqueness rule rejected the write"
// @Failure      500   {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/pages [post]
func (h *PageHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}
	wsID, ok := h.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return
	}
	var req pageCreateRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	if !h.ValidateRequiredString(w, r, req.Title, "title") {
		return
	}

	page, err := h.application.Create(h.auditActor(r, user), services.CreatePageInput{
		WorkspaceID: wsID,
		ParentID:    req.ParentID,
		Title:       req.Title,
		Metadata:    marshalPageMetadata(req.Metadata),
		Content:     req.Content,
		IsHome:      req.IsHome,
	})
	if err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondCreated(w, dto.MapPageToResponse(page, getBaseURL(r)))
}

// Update handles PUT /rest/api/v1/workspaces/{id}/pages/{pageId}
//
// Update overwrites a page's title and/or content. Body is a partial:
// fields omitted are left unchanged.
//
// @Summary      Update a page
// @Description  Partial update: only fields supplied in the body are touched.
// @Tags         pages
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id      path      int                         true  "Workspace ID"
// @Param        pageId  path      int                         true  "Page ID"
// @Param        body    body      handlers.pageUpdateRequest  true  "Fields to update"
// @Success      200     {object}  dto.PageResponse
// @Failure      400     {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401     {object}  handlers.ErrorResponse
// @Failure      404     {object}  handlers.ErrorResponse  "Page not found or you lack page.edit"
// @Failure      500     {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/pages/{pageId} [put]
func (h *PageHandler) Update(w http.ResponseWriter, r *http.Request) {
	wsID, pageID, user, ok := h.requireWorkspacePageTarget(w, r)
	if !ok {
		return
	}
	var req pageUpdateRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}

	in := services.PageApplicationUpdateInput{
		ID:                  pageID,
		Title:               req.Title,
		Content:             req.Content,
		ExpectedContentHash: req.ExpectedContentHash,
	}
	if req.Metadata != nil {
		metadata := marshalPageMetadata(*req.Metadata)
		in.Metadata = &metadata
	}
	updated, err := h.application.Update(h.auditActor(r, user), wsID, in)
	if err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondOK(w, dto.MapPageToResponse(updated, getBaseURL(r)))
}

// Move handles POST /rest/api/v1/workspaces/{id}/pages/{pageId}/move
//
// Move reparents a page. parent_id=null moves it to the workspace root.
// The caller must be able to edit the moved page and the destination
// parent (when supplied).
//
// @Summary      Move (reparent) a page
// @Description  parent_id=null moves the page to the workspace root. Caller must have page.edit on both the moved page and the destination parent.
// @Tags         pages
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id      path      int                       true  "Workspace ID"
// @Param        pageId  path      int                       true  "Page ID"
// @Param        body    body      handlers.pageMoveRequest  true  "Move destination"
// @Success      200     {object}  dto.PageResponse
// @Failure      400     {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401     {object}  handlers.ErrorResponse
// @Failure      404     {object}  handlers.ErrorResponse  "Page or destination parent not found or you lack page.edit"
// @Failure      409     {object}  handlers.ErrorResponse  "Move would create a cycle or exceed depth limits"
// @Failure      500     {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/pages/{pageId}/move [post]
func (h *PageHandler) Move(w http.ResponseWriter, r *http.Request) {
	wsID, pageID, user, ok := h.requireWorkspacePageTarget(w, r)
	if !ok {
		return
	}
	var req pageMoveRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	moved, err := h.application.Move(h.auditActor(r, user), wsID, pageID, req.DestinationWorkspaceID, req.ParentID, req.PrevSiblingID, req.NextSiblingID)
	if err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondOK(w, dto.MapPageToResponse(moved, getBaseURL(r)))
}

// Archive handles DELETE /rest/api/v1/workspaces/{id}/pages/{pageId}
//
// Archive soft-deletes a page and its subtree. Requires pages:delete
// scope at the route layer plus page.admin on the page AND workspace
// page.delete. To prevent restricted descendants from being silently
// archived, we re-check PageOpAdmin on every descendant before
// cascading; see bug-hunt finding #3.
//
// @Summary      Archive (soft-delete) a page and its subtree
// @Description  Requires page.admin on the page AND workspace page.delete; re-checks page.admin on every descendant before cascading.
// @Tags         pages
// @Security     BearerAuth
// @Param        id      path  int  true  "Workspace ID"
// @Param        pageId  path  int  true  "Page ID"
// @Success      204     "Page archived"
// @Failure      400     {object}  handlers.ErrorResponse  "Invalid workspace or page ID"
// @Failure      401     {object}  handlers.ErrorResponse
// @Failure      404     {object}  handlers.ErrorResponse  "Page not found or you lack page.admin / page.delete"
// @Failure      500     {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/pages/{pageId} [delete]
func (h *PageHandler) Archive(w http.ResponseWriter, r *http.Request) {
	wsID, pageID, user, ok := h.requireWorkspacePageTarget(w, r)
	if !ok {
		return
	}
	if _, err := h.application.Archive(h.auditActor(r, user), wsID, pageID); err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondNoContent(w)
}

// GetHistory handles GET /rest/api/v1/workspaces/{id}/pages/{pageId}/history
//
// GetHistory returns revisions for a page newest-first.
//
// @Summary      List revisions of a page
// @Description  Returns revisions newest-first. Supports `limit` (default 50, max 200) and `offset` query parameters.
// @Tags         pages
// @Produce      json
// @Security     BearerAuth
// @Param        id      path      int  true   "Workspace ID"
// @Param        pageId  path      int  true   "Page ID"
// @Param        limit   query     int  false  "Maximum revisions to return (default 50, max 200)"
// @Param        offset  query     int  false  "Offset into the result set (default 0)"
// @Success      200     {object}  handlers.pageHistoryListResponse
// @Failure      400     {object}  handlers.ErrorResponse  "Invalid workspace or page ID"
// @Failure      401     {object}  handlers.ErrorResponse
// @Failure      404     {object}  handlers.ErrorResponse  "Page not found or you lack page.view"
// @Failure      500     {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/pages/{pageId}/history [get]
func (h *PageHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	_, pageID, ok := h.requireWorkspacePageView(w, r)
	if !ok {
		return
	}
	limit, offset := parseHistoryPagination(r)
	revs, err := h.service.ListRevisions(pageID, limit, offset)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	items := make([]dto.PageRevisionResponse, 0, len(revs))
	for i := range revs {
		items = append(items, dto.MapPageRevisionToResponse(&revs[i]))
	}
	h.RespondOK(w, pageHistoryListResponse{Items: items})
}

// GetRevision handles GET /rest/api/v1/workspaces/{id}/pages/{pageId}/history/{revisionId}
//
// GetRevision returns a single revision. The revision id must belong to the
// addressed page so callers cannot use a visible page as a side-channel for a
// different page's revision body.
//
// @Summary      Get a single page revision
// @Description  Returns the revision body. The revision must belong to the addressed page.
// @Tags         pages
// @Produce      json
// @Security     BearerAuth
// @Param        id          path      int  true  "Workspace ID"
// @Param        pageId      path      int  true  "Page ID"
// @Param        revisionId  path      int  true  "Revision ID"
// @Success      200         {object}  dto.PageRevisionResponse
// @Failure      400         {object}  handlers.ErrorResponse  "Invalid workspace, page, or revision ID"
// @Failure      401         {object}  handlers.ErrorResponse
// @Failure      404         {object}  handlers.ErrorResponse  "Page or revision not found or you lack page.view"
// @Failure      500         {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/pages/{pageId}/history/{revisionId} [get]
func (h *PageHandler) GetRevision(w http.ResponseWriter, r *http.Request) {
	_, pageID, ok := h.requireWorkspacePageView(w, r)
	if !ok {
		return
	}
	revisionID, ok := h.ParsePathID(w, r, "revisionId", "revision ID")
	if !ok {
		return
	}
	rev, err := h.service.GetRevision(revisionID)
	if errors.Is(err, services.ErrPageNotFound) || (err == nil && rev.PageID != pageID) {
		h.RespondNotFound(w, r)
		return
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, dto.MapPageRevisionToResponse(rev))
}

// RestoreRevision handles POST /rest/api/v1/workspaces/{id}/pages/{pageId}/history/{revisionId}/restore
//
// RestoreRevision overwrites a page's live title/content from a revision and
// unarchives the page when the target is archived. Live pages require edit;
// archived pages require the restore branch in PagePermissionService.
//
// @Summary      Restore a page revision
// @Description  Overwrites the page's live title/content from the revision; unarchives the page when the target is archived.
// @Tags         pages
// @Produce      json
// @Security     BearerAuth
// @Param        id          path      int  true  "Workspace ID"
// @Param        pageId      path      int  true  "Page ID"
// @Param        revisionId  path      int  true  "Revision ID"
// @Success      200         {object}  dto.PageResponse
// @Failure      400         {object}  handlers.ErrorResponse  "Invalid workspace, page, or revision ID"
// @Failure      401         {object}  handlers.ErrorResponse
// @Failure      404         {object}  handlers.ErrorResponse  "Page or revision not found or you lack page.edit / restore permission"
// @Failure      500         {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/pages/{pageId}/history/{revisionId}/restore [post]
func (h *PageHandler) RestoreRevision(w http.ResponseWriter, r *http.Request) {
	wsID, pageID, user, ok := h.requireWorkspacePageTarget(w, r)
	if !ok {
		return
	}
	revisionID, ok := h.ParsePathID(w, r, "revisionId", "revision ID")
	if !ok {
		return
	}
	page, err := h.application.Restore(h.auditActor(r, user), wsID, pageID, revisionID)
	if err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondOK(w, dto.MapPageToResponse(page, getBaseURL(r)))
}

// GetPermissions handles GET /rest/api/v1/workspaces/{id}/pages/{pageId}/permissions
//
// GetPermissions returns the caller's effective level plus ACL rows stored
// directly on this page. Inherited ACL rows are evaluated by the service but
// not expanded in this compact v1 payload.
//
// @Summary      Get page permissions
// @Description  Returns the caller's effective permission level and the ACL rows stored directly on the page (inherited rows are not expanded).
// @Tags         pages
// @Produce      json
// @Security     BearerAuth
// @Param        id      path      int  true  "Workspace ID"
// @Param        pageId  path      int  true  "Page ID"
// @Success      200     {object}  handlers.pagePermissionsResponse
// @Failure      400     {object}  handlers.ErrorResponse  "Invalid workspace or page ID"
// @Failure      401     {object}  handlers.ErrorResponse
// @Failure      404     {object}  handlers.ErrorResponse  "Page not found or you lack page.view"
// @Failure      500     {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/pages/{pageId}/permissions [get]
func (h *PageHandler) GetPermissions(w http.ResponseWriter, r *http.Request) {
	wsID, pageID, user, ok := h.resolveWorkspacePageOp(w, r, services.PageOpView)
	if !ok {
		return
	}
	page, err := h.service.GetByID(pageID)
	if err != nil {
		h.RespondNotFound(w, r)
		return
	}
	effective := ""
	for _, op := range []string{services.PageOpAdmin, services.PageOpEdit, services.PageOpView} {
		can, cerr := h.pageAuth.Can(user.ID, wsID, pageID, op)
		if cerr != nil {
			h.RespondInternalError(w, r)
			return
		}
		if can {
			effective = op
			break
		}
	}
	acl, err := h.service.ListOwnACL(pageID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	if acl == nil {
		acl = []models.PagePermission{}
	}
	h.RespondOK(w, pagePermissionsResponse{PageID: page.ID, InheritPermissions: page.InheritPermissions, EffectiveLevel: effective, ACL: acl})
}

// GrantPermission handles POST /rest/api/v1/workspaces/{id}/pages/{pageId}/permissions
//
// GrantPermission attaches an ACL row to a page. Requires page.admin on the
// target page via PagePermissionService and pages:write at the route layer.
//
// @Summary      Grant a page permission
// @Description  Attaches an ACL row to the page. Requires page.admin on the target page.
// @Tags         pages
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id      path      int                                   true  "Workspace ID"
// @Param        pageId  path      int                                   true  "Page ID"
// @Param        body    body      handlers.pageGrantPermissionRequest   true  "ACL row to grant"
// @Success      201     {object}  models.PagePermission
// @Failure      400     {object}  handlers.ErrorResponse  "Invalid request body, missing required field, or invalid principal/level"
// @Failure      401     {object}  handlers.ErrorResponse
// @Failure      404     {object}  handlers.ErrorResponse  "Page not found or you lack page.admin"
// @Failure      409     {object}  handlers.ErrorResponse  "Permission already granted for this principal"
// @Failure      500     {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/pages/{pageId}/permissions [post]
func (h *PageHandler) GrantPermission(w http.ResponseWriter, r *http.Request) {
	wsID, pageID, user, ok := h.requireWorkspacePageTarget(w, r)
	if !ok {
		return
	}
	var req pageGrantPermissionRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	if req.PrincipalType == "" || req.PrincipalID == 0 || req.PermissionLevel == "" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "principal_type, principal_id, and permission_level are required"))
		return
	}
	row, err := h.application.GrantPermission(h.auditActor(r, user), wsID, pageID, req.PrincipalType, req.PrincipalID, req.PermissionLevel)
	if err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondCreated(w, row)
}

// RevokePermission handles DELETE /rest/api/v1/workspaces/{id}/pages/{pageId}/permissions/{permissionId}
//
// RevokePermission deletes one ACL row from the page.
//
// @Summary      Revoke a page permission
// @Tags         pages
// @Security     BearerAuth
// @Param        id            path  int  true  "Workspace ID"
// @Param        pageId        path  int  true  "Page ID"
// @Param        permissionId  path  int  true  "Permission (ACL row) ID"
// @Success      204           "Permission revoked"
// @Failure      400           {object}  handlers.ErrorResponse  "Invalid workspace, page, or permission ID"
// @Failure      401           {object}  handlers.ErrorResponse
// @Failure      404           {object}  handlers.ErrorResponse  "Page or permission not found or you lack page.admin"
// @Failure      500           {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/pages/{pageId}/permissions/{permissionId} [delete]
func (h *PageHandler) RevokePermission(w http.ResponseWriter, r *http.Request) {
	wsID, pageID, user, ok := h.requireWorkspacePageTarget(w, r)
	if !ok {
		return
	}
	permissionID, ok := h.ParsePathID(w, r, "permissionId", "permission ID")
	if !ok {
		return
	}
	if err := h.application.RevokePermission(h.auditActor(r, user), wsID, pageID, permissionID); err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondNoContent(w)
}

// SetInheritance handles PATCH /rest/api/v1/workspaces/{id}/pages/{pageId}/inheritance
//
// SetInheritance flips the page's inherit_permissions flag. Requires admin on
// the page and pages:write on the bearer token.
//
// @Summary      Toggle ACL inheritance on a page
// @Description  Flips inherit_permissions on the page. When false, the page evaluates its own ACL only.
// @Tags         pages
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id      path      int                                  true  "Workspace ID"
// @Param        pageId  path      int                                  true  "Page ID"
// @Param        body    body      handlers.pageSetInheritanceRequest   true  "Inheritance flag"
// @Success      200     {object}  dto.PageResponse
// @Failure      400     {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401     {object}  handlers.ErrorResponse
// @Failure      404     {object}  handlers.ErrorResponse  "Page not found or you lack page.admin"
// @Failure      500     {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/pages/{pageId}/inheritance [patch]
func (h *PageHandler) SetInheritance(w http.ResponseWriter, r *http.Request) {
	wsID, pageID, user, ok := h.requireWorkspacePageTarget(w, r)
	if !ok {
		return
	}
	var req pageSetInheritanceRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	page, err := h.application.SetInheritance(h.auditActor(r, user), wsID, pageID, req.InheritPermissions)
	if err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondOK(w, dto.MapPageToResponse(page, getBaseURL(r)))
}

// parseHistoryPagination mirrors the cookie-auth GetHistory pagination
// (limit default 50, max 200; offset >= 0) so the same query params work
// against the v1 surface.
func parseHistoryPagination(r *http.Request) (limit, offset int) {
	limit = 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	return limit, offset
}

// parseSearchLimit reads the page-search result cap from the limit query
// param (default 20, max 100). A missing, non-positive, or unparseable
// value falls back to the default.
func parseSearchLimit(r *http.Request) int {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 100 {
		limit = 100
	}
	return limit
}

// --- helpers ---

func (h *PageHandler) requireWorkspacePageView(w http.ResponseWriter, r *http.Request) (workspaceID, pageID int, ok bool) {
	return h.requireWorkspacePageOp(w, r, services.PageOpView)
}

// userCtx is a tiny carrier for the auth user so the helpers can return a
// single struct alongside ids without leaking middleware types.
type userCtx struct {
	ID       int
	Username string
}

func (h *PageHandler) resolveWorkspacePageOp(w http.ResponseWriter, r *http.Request, op string) (wsID, pageID int, user *userCtx, ok bool) {
	u, authed := h.RequireAuth(w, r)
	if !authed {
		return 0, 0, nil, false
	}
	wsID, parsed := h.ParsePathID(w, r, "id", "workspace ID")
	if !parsed {
		return 0, 0, nil, false
	}
	pageID, parsed = h.ParsePathID(w, r, "pageId", "page ID")
	if !parsed {
		return 0, 0, nil, false
	}
	can, err := h.pageAuth.Can(u.ID, wsID, pageID, op)
	if err != nil {
		h.RespondInternalError(w, r)
		return 0, 0, nil, false
	}
	if !can {
		h.RespondNotFound(w, r)
		return 0, 0, nil, false
	}
	return wsID, pageID, &userCtx{ID: u.ID, Username: u.Username}, true
}

func (h *PageHandler) requireWorkspacePageOp(w http.ResponseWriter, r *http.Request, op string) (workspaceID, pageID int, ok bool) {
	wsID, pID, _, can := h.resolveWorkspacePageOp(w, r, op)
	return wsID, pID, can
}

// requireWorkspacePageTarget performs only bearer authentication and path
// parsing. PageApplicationService owns mutation-specific domain permission
// checks; this adapter owns their 404 rendering.
func (h *PageHandler) requireWorkspacePageTarget(w http.ResponseWriter, r *http.Request) (workspaceID, pageID int, user *models.User, ok bool) {
	user, ok = h.RequireAuth(w, r)
	if !ok {
		return
	}
	workspaceID, ok = h.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return
	}
	pageID, ok = h.ParsePathID(w, r, "pageId", "page ID")
	return
}

func (h *PageHandler) auditActor(r *http.Request, user *models.User) services.AuditActor {
	return services.NewAuditActorFromRequest(r, user, middleware.GetAPIToken(r.Context()), "bearer")
}

func (h *PageHandler) respondPageServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, services.ErrPageNotFound), errors.Is(err, services.ErrPageParentNotFound), errors.Is(err, services.ErrPageMutationForbidden):
		h.RespondNotFound(w, r)
	case errors.Is(err, services.ErrPageNoChanges):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "at least one page field is required"))
	case errors.Is(err, services.ErrPageTitleRequired):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "title is required"))
	case errors.Is(err, services.ErrPageParentMismatch):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "parent belongs to a different workspace"))
	case errors.Is(err, services.ErrPageCycle):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "move would create a cycle"))
	case errors.Is(err, services.ErrPageDepthExceeded):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "page tree depth limit exceeded"))
	case errors.Is(err, services.ErrPageUniqueConflict):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "page conflicts with an existing page"))
	case errors.Is(err, services.ErrPageContentConflict):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "page content changed since it was read"))
	case errors.Is(err, services.ErrPageRevisionMismatch):
		h.RespondNotFound(w, r)
	case errors.Is(err, services.ErrPageMetadataInvalid):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "metadata must be a JSON object"))
	case errors.Is(err, services.ErrPageInvalidPrincipal):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "principal_type must be user, group, or role"))
	case errors.Is(err, services.ErrPageInvalidLevel):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "permission_level must be view, edit, or admin"))
	case errors.Is(err, services.ErrPagePermissionDuplicate):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "permission already granted"))
	case errors.Is(err, services.ErrPageGrantPrincipalNotFound):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "principal does not exist"))
	default:
		h.RespondInternalError(w, r)
	}
}
