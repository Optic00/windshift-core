package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/restapi/v1/dto"
	"windshift/internal/services"
)

// PageHandler exposes workspace knowledge pages on the bearer-token v1
// surface so the ws CLI can drive them. Mirrors the cookie-auth
// handlers/pages.go surface but goes through bearer auth + token scopes
// and emits the public DTO shape.
type PageHandler struct {
	BaseHandler
	service  *services.PageService
	pageAuth *services.PagePermissionService
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
		pageAuth:    pageAuth,
	}
}

// --- request payloads ---

type pageCreateRequest struct {
	Title    string `json:"title"`
	Content  string `json:"content"`
	ParentID *int   `json:"parent_id,omitempty"`
	IsHome   bool   `json:"is_home,omitempty"`
}

// pageUpdateRequest is a partial-update payload: only fields supplied get
// touched. inherit_permissions is deliberately absent — inheritance
// changes have their own admin-gated PATCH /inheritance endpoint (not
// yet on v1; the cookie surface has it). Allowing it here would let an
// editor flip the flag via a normal save.
type pageUpdateRequest struct {
	Title   *string `json:"title,omitempty"`
	Content *string `json:"content,omitempty"`
}

type pageMoveRequest struct {
	ParentID      *int `json:"parent_id"`
	PrevSiblingID *int `json:"prev_sibling_id,omitempty"`
	NextSiblingID *int `json:"next_sibling_id,omitempty"`
}

type pageGrantPermissionRequest struct {
	PrincipalType   string `json:"principal_type"`
	PrincipalID     int    `json:"principal_id"`
	PermissionLevel string `json:"permission_level"`
}

type pageSetInheritanceRequest struct {
	InheritPermissions bool `json:"inherit_permissions"`
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

// List returns every page in the workspace the caller can view. Returns
// a flat list sorted depth-first; the CLI assembles the tree client-side.
func (h *PageHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}
	wsID, ok := h.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return
	}

	pages, err := h.service.ListTree(wsID, false)
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

	// Preload labels onto the visible slice before mapping so each DTO
	// in the response includes its labels (no per-page round-trip for the
	// CLI). Safe to call on the full slice — invisible pages just get
	// their Labels stamped and then dropped below.
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

// Get returns a single page by id. 404 on missing or no view permission.
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

// Create creates a new page. Requires pages:write scope and page.create
// on the workspace (or page.admin / workspace.admin / system.admin).
// When parent_id is set the caller must also be able to edit the parent.
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

	if !h.canCreatePage(user.ID, wsID) {
		h.RespondNotFound(w, r)
		return
	}
	if req.ParentID != nil {
		canEditParent, perr := h.pageAuth.Can(user.ID, wsID, *req.ParentID, services.PageOpEdit)
		if perr != nil {
			h.RespondInternalError(w, r)
			return
		}
		if !canEditParent {
			h.RespondNotFound(w, r)
			return
		}
	}

	page, err := h.service.Create(user.ID, services.CreatePageInput{
		WorkspaceID: wsID,
		ParentID:    req.ParentID,
		Title:       req.Title,
		Content:     req.Content,
		IsHome:      req.IsHome,
	})
	if err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondCreated(w, dto.MapPageToResponse(page, getBaseURL(r)))
}

// Update overwrites a page's title and/or content. Body is a partial:
// fields omitted are left unchanged.
func (h *PageHandler) Update(w http.ResponseWriter, r *http.Request) {
	wsID, pageID, user, ok := h.requireWorkspacePageEdit(w, r)
	if !ok {
		return
	}
	_ = wsID
	var req pageUpdateRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}

	existing, err := h.service.GetByID(pageID)
	if err != nil {
		h.RespondNotFound(w, r)
		return
	}

	in := services.UpdatePageInput{
		ID:      pageID,
		Title:   existing.Title,
		Content: existing.Content,
	}
	if req.Title != nil {
		in.Title = *req.Title
	}
	if req.Content != nil {
		in.Content = *req.Content
	}

	updated, err := h.service.Update(user.ID, in)
	if err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondOK(w, dto.MapPageToResponse(updated, getBaseURL(r)))
}

// Move reparents a page. parent_id=null moves it to the workspace root.
// The caller must be able to edit the moved page and the destination
// parent (when supplied).
func (h *PageHandler) Move(w http.ResponseWriter, r *http.Request) {
	wsID, pageID, user, ok := h.requireWorkspacePageEdit(w, r)
	if !ok {
		return
	}
	var req pageMoveRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	if req.ParentID != nil {
		canEditParent, err := h.pageAuth.Can(user.ID, wsID, *req.ParentID, services.PageOpEdit)
		if err != nil {
			h.RespondInternalError(w, r)
			return
		}
		if !canEditParent {
			h.RespondNotFound(w, r)
			return
		}
	}
	moved, err := h.service.Move(user.ID, pageID, req.ParentID, req.PrevSiblingID, req.NextSiblingID)
	if err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondOK(w, dto.MapPageToResponse(moved, getBaseURL(r)))
}

// Archive soft-deletes a page and its subtree. Requires pages:delete
// scope at the route layer plus page.admin on the page AND workspace
// page.delete. To prevent restricted descendants from being silently
// archived, we re-check PageOpAdmin on every descendant before
// cascading; see bug-hunt finding #3.
func (h *PageHandler) Archive(w http.ResponseWriter, r *http.Request) {
	wsID, pageID, user, ok := h.requireWorkspacePageAdmin(w, r)
	if !ok {
		return
	}
	hasDelete, err := h.PermissionService.HasWorkspacePermission(user.ID, wsID, "page.delete")
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	if !hasDelete {
		h.RespondNotFound(w, r)
		return
	}
	if err := h.service.ArchiveChecked(user.ID, pageID, func(subtree []models.Page) error {
		for _, d := range subtree {
			can, cerr := h.pageAuth.Can(user.ID, wsID, d.ID, services.PageOpAdmin)
			if cerr != nil {
				return cerr
			}
			if !can {
				return services.ErrPageNotFound
			}
		}
		return nil
	}); err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondNoContent(w)
}

// GetHistory returns revisions for a page newest-first.
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

// GetRevision returns a single revision. The revision id must belong to the
// addressed page so callers cannot use a visible page as a side-channel for a
// different page's revision body.
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

// RestoreRevision overwrites a page's live title/content from a revision and
// unarchives the page when the target is archived. Live pages require edit;
// archived pages require the restore branch in PagePermissionService.
func (h *PageHandler) RestoreRevision(w http.ResponseWriter, r *http.Request) {
	_, pageID, user, ok := h.resolveWorkspacePageOp(w, r, services.PageOpRestore)
	if !ok {
		return
	}
	revisionID, ok := h.ParsePathID(w, r, "revisionId", "revision ID")
	if !ok {
		return
	}
	page, err := h.service.Restore(user.ID, pageID, revisionID)
	if err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondOK(w, dto.MapPageToResponse(page, getBaseURL(r)))
}

// GetPermissions returns the caller's effective level plus ACL rows stored
// directly on this page. Inherited ACL rows are evaluated by the service but
// not expanded in this compact v1 payload.
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

// GrantPermission attaches an ACL row to a page. Requires page.admin on the
// target page via PagePermissionService and pages:write at the route layer.
func (h *PageHandler) GrantPermission(w http.ResponseWriter, r *http.Request) {
	_, pageID, user, ok := h.resolveWorkspacePageOp(w, r, services.PageOpAdmin)
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
	row, err := h.service.GrantPermission(user.ID, pageID, req.PrincipalType, req.PrincipalID, req.PermissionLevel)
	if err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondCreated(w, row)
}

// RevokePermission deletes one ACL row from the page.
func (h *PageHandler) RevokePermission(w http.ResponseWriter, r *http.Request) {
	_, pageID, user, ok := h.resolveWorkspacePageOp(w, r, services.PageOpAdmin)
	if !ok {
		return
	}
	permissionID, ok := h.ParsePathID(w, r, "permissionId", "permission ID")
	if !ok {
		return
	}
	if err := h.service.RevokePermission(user.ID, pageID, permissionID); err != nil {
		h.respondPageServiceError(w, r, err)
		return
	}
	h.RespondNoContent(w)
}

// SetInheritance flips the page's inherit_permissions flag. Requires admin on
// the page and pages:write on the bearer token.
func (h *PageHandler) SetInheritance(w http.ResponseWriter, r *http.Request) {
	_, pageID, user, ok := h.resolveWorkspacePageOp(w, r, services.PageOpAdmin)
	if !ok {
		return
	}
	var req pageSetInheritanceRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	page, err := h.service.SetInheritPermissions(user.ID, pageID, req.InheritPermissions)
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

// --- helpers ---

func (h *PageHandler) requireWorkspacePageView(w http.ResponseWriter, r *http.Request) (workspaceID, pageID int, ok bool) {
	return h.requireWorkspacePageOp(w, r, services.PageOpView)
}

func (h *PageHandler) requireWorkspacePageEdit(w http.ResponseWriter, r *http.Request) (workspaceID, pageID int, user *userCtx, ok bool) {
	wsID, pID, u, can := h.resolveWorkspacePageOp(w, r, services.PageOpEdit)
	return wsID, pID, u, can
}

func (h *PageHandler) requireWorkspacePageAdmin(w http.ResponseWriter, r *http.Request) (workspaceID, pageID int, user *userCtx, ok bool) {
	wsID, pID, u, can := h.resolveWorkspacePageOp(w, r, services.PageOpAdmin)
	return wsID, pID, u, can
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

func (h *PageHandler) canCreatePage(userID, workspaceID int) bool {
	for _, key := range []string{"page.create", "page.admin", "workspace.admin"} {
		if has, err := h.PermissionService.HasWorkspacePermission(userID, workspaceID, key); err == nil && has {
			return true
		}
	}
	return false
}

func (h *PageHandler) respondPageServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, services.ErrPageNotFound):
		h.RespondNotFound(w, r)
	case errors.Is(err, services.ErrPageTitleRequired):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "title is required"))
	case errors.Is(err, services.ErrPageParentMismatch):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "parent belongs to a different workspace"))
	case errors.Is(err, services.ErrPageCycle):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "move would create a cycle"))
	case errors.Is(err, services.ErrPageDepthExceeded):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "page tree depth limit exceeded"))
	case errors.Is(err, services.ErrPageSlugConflict):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "slug conflicts with an existing sibling page"))
	case errors.Is(err, services.ErrPageRevisionMismatch):
		h.RespondNotFound(w, r)
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
