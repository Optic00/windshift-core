package handlers

import (
	"errors"
	"net/http"
	"time"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/sanitize"
	"windshift/internal/services"
)

// KnowledgeSearchHandler serves the unified knowledge search endpoint.
type KnowledgeSearchHandler struct {
	retrieval *services.KnowledgeRetrievalService
}

// NewKnowledgeSearchHandler constructs a KnowledgeSearchHandler.
func NewKnowledgeSearchHandler(retrieval *services.KnowledgeRetrievalService) *KnowledgeSearchHandler {
	return &KnowledgeSearchHandler{retrieval: retrieval}
}

// Search runs full-text search over pages (and, in a later slice, other
// knowledge sources). Workspace membership is enforced by the underlying
// permission evaluator; no-permission yields an empty result rather than
// 404 because the workspace itself is the lookup scope.
func (h *KnowledgeSearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	query := r.URL.Query().Get("q")
	limit, _ := parseOffsetPagination(r, 25, 100)
	results, err := h.retrieval.Search(services.SearchInput{
		UserID:      user.ID,
		WorkspaceID: workspaceID,
		Query:       query,
		Limit:       limit,
	})
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if results == nil {
		results = []services.KnowledgeResult{}
	}
	respondJSONOK(w, map[string]interface{}{"results": results, "query": query})
}

// PageHandler serves the workspace knowledge-pages API.
//
// Authorization model (Phase 1):
//   - Every workspace-scoped endpoint resolves {workspaceId} first.
//   - Permission failures return 404 (memory: workspace-resource access
//     failures must not leak existence).
//   - View / edit / admin operations route through PagePermissionService
//     which combines workspace page.* role grants with per-page ACL rows
//     walked along the materialized path.
type PageHandler struct {
	service  *services.PageService
	pageAuth *services.PagePermissionService
	auditor  *logger.Auditor
}

// NewPageHandler constructs a PageHandler.
func NewPageHandler(service *services.PageService, pageAuth *services.PagePermissionService, auditor *logger.Auditor) *PageHandler {
	return &PageHandler{
		service:  service,
		pageAuth: pageAuth,
		auditor:  auditor,
	}
}

// --- response payloads ---

type pageTreeResponse struct {
	Pages []models.Page      `json:"pages"`
	Tree  []*models.PageNode `json:"tree"`
}

type pageHistoryResponse struct {
	Revisions []models.PageRevision `json:"revisions"`
}

type pageEffectivePermissionsResponse struct {
	PageID             int                     `json:"page_id"`
	InheritPermissions bool                    `json:"inherit_permissions"`
	EffectiveLevel     string                  `json:"effective_level,omitempty"`
	ACL                []models.PagePermission `json:"acl"`
}

type archivedPageResponse struct {
	ID             int       `json:"id"`
	Title          string    `json:"title"`
	Slug           string    `json:"slug"`
	Path           string    `json:"path"`
	Depth          int       `json:"depth"`
	ArchivedAt     time.Time `json:"archived_at"`
	ArchivedBy     *int      `json:"archived_by,omitempty"`
	ArchivedByName string    `json:"archived_by_name,omitempty"`
}

// --- request payloads ---

type createPageRequest struct {
	ParentID *int   `json:"parent_id"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	IsHome   bool   `json:"is_home"`
}

// updatePageRequest covers only title + content. Inheritance toggles go
// through PATCH /inheritance (PageOpAdmin) — accepting the field here
// would let editors break inheritance via a normal save, and Go's zero
// value would set it to false whenever clients omitted it.
type updatePageRequest struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type movePageRequest struct {
	ParentID      *int `json:"parent_id"`
	PrevSiblingID *int `json:"prev_sibling_id,omitempty"`
	NextSiblingID *int `json:"next_sibling_id,omitempty"`
}

type grantPagePermissionRequest struct {
	PrincipalType   string `json:"principal_type"`
	PrincipalID     int    `json:"principal_id"`
	PermissionLevel string `json:"permission_level"`
}

type setInheritanceRequest struct {
	InheritPermissions bool `json:"inherit_permissions"`
}

// --- endpoints ---

// GetTree returns every page in the workspace that the user can view,
// plus the assembled tree shape for direct client-side rendering.
func (h *PageHandler) GetTree(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	pages, err := h.service.ListTree(workspaceID, false)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Filter to visible pages. With no per-page ACL set (the default), this
	// reduces to a single page.view workspace-permission check.
	ids := make([]int, len(pages))
	for i, p := range pages {
		ids[i] = p.ID
	}
	visible, err := h.pageAuth.ListVisiblePageIDs(user.ID, workspaceID, ids)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	filtered := pages[:0]
	for _, p := range pages {
		if visible[p.ID] {
			filtered = append(filtered, p)
		}
	}

	// The sidebar renders titles only and PageMoveDialog needs id/parent
	// only; neither consumer touches the body. Stripping the heavy TEXT
	// fields here keeps the tree payload KB-sized instead of MB-sized at
	// ~1000 pages (content is shipped twice over: flat Pages + nested
	// Tree, since PageNode embeds Page). Mutating the shared slice before
	// BuildPageTree copies each Page into a PageNode strips both shapes
	// in one pass. (WI-407.)
	for i := range filtered {
		filtered[i].Content = ""
		filtered[i].ContentHash = ""
		filtered[i].Excerpt = ""
	}

	// Preload labels onto each visible page before BuildPageTree copies
	// them into PageNodes — the copy inherits the slice header.
	if err := h.service.PreloadLabels(filtered); err != nil {
		respondInternalError(w, r, err)
		return
	}

	tree := services.BuildPageTree(filtered)
	respondJSONOK(w, pageTreeResponse{Pages: filtered, Tree: tree})
}

// Search returns workspace pages whose title matches q (substring,
// case-insensitive). Drives the page-picker in the link dialog. Returns
// only pages the user may view; per-page ACLs are honored. The response
// shape stays minimal (id, title, workspace_id, parent_id, path) — the
// picker doesn't need the full page payload.
func (h *PageHandler) Search(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	query := r.URL.Query().Get("q")
	limit, _ := parseOffsetPagination(r, 20, 50)

	pages, err := h.service.SearchByTitle(workspaceID, query, limit)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	ids := make([]int, len(pages))
	for i, p := range pages {
		ids[i] = p.ID
	}
	visible, err := h.pageAuth.ListVisiblePageIDs(user.ID, workspaceID, ids)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	type result struct {
		ID          int    `json:"id"`
		Title       string `json:"title"`
		WorkspaceID int    `json:"workspace_id"`
		ParentID    *int   `json:"parent_id,omitempty"`
		Path        string `json:"path,omitempty"`
	}
	results := make([]result, 0, len(pages))
	for _, p := range pages {
		if !visible[p.ID] {
			continue
		}
		results = append(results, result{
			ID:          p.ID,
			Title:       p.Title,
			WorkspaceID: p.WorkspaceID,
			ParentID:    p.ParentID,
			Path:        p.Path,
		})
	}
	respondJSONOK(w, map[string]interface{}{"results": results, "query": query})
}

// Get returns a single page after authorizing view access.
func (h *PageHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID, pageID, user, ok := h.requireWorkspacePageAuth(w, r, services.PageOpView)
	if !ok {
		return
	}
	_ = user

	page, err := h.service.GetByID(pageID)
	if err != nil {
		if errors.Is(err, services.ErrPageNotFound) {
			respondNotFound(w, r, "Page")
			return
		}
		respondInternalError(w, r, err)
		return
	}
	if page.WorkspaceID != workspaceID {
		respondNotFound(w, r, "Page")
		return
	}
	if err := h.service.PreloadLabelsForPage(page); err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, page)
}

// Create creates a new page. workspace page.create must be held, and when
// a parent is supplied the user must also have edit on the parent (so we
// can't insert under a page they can't otherwise see).
func (h *PageHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	req, ok := decodeJSON[createPageRequest](w, r)
	if !ok {
		return
	}
	// Sanitization happens in PageService.Create — the documented choke
	// point. Re-sanitizing here would double-decode HTML entities and
	// corrupt escaped-HTML content (e.g. code samples) in a single save.
	if req.Title == "" {
		respondValidationError(w, r, "title is required")
		return
	}

	// Workspace-level create permission.
	if !h.workspacePermissionAllowsCreate(user.ID, workspaceID) {
		respondNotFound(w, r, "Pages")
		return
	}

	// If a parent is supplied, the user must be able to edit the parent.
	if req.ParentID != nil {
		canEditParent, err := h.pageAuth.Can(user.ID, workspaceID, *req.ParentID, services.PageOpEdit)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if !canEditParent {
			respondNotFound(w, r, "Page")
			return
		}
	}

	page, err := h.service.Create(user.ID, services.CreatePageInput{
		WorkspaceID: workspaceID,
		ParentID:    req.ParentID,
		Title:       req.Title,
		Content:     req.Content,
		IsHome:      req.IsHome,
	})
	if err != nil {
		h.respondServiceError(w, r, err)
		return
	}

	h.auditor.Log(r, user, "create", "page", &page.ID, page.Title)
	respondJSONCreated(w, page)
}

// Update overwrites a page's title/content/inheritance.
func (h *PageHandler) Update(w http.ResponseWriter, r *http.Request) {
	workspaceID, pageID, user, ok := h.requireWorkspacePageAuth(w, r, services.PageOpEdit)
	if !ok {
		return
	}
	_ = workspaceID

	req, ok := decodeJSON[updatePageRequest](w, r)
	if !ok {
		return
	}
	// Sanitization happens in PageService.Update (choke point); see Create.
	if req.Title == "" {
		respondValidationError(w, r, "title is required")
		return
	}

	updated, err := h.service.Update(user.ID, services.UpdatePageInput{
		ID:      pageID,
		Title:   req.Title,
		Content: req.Content,
	})
	if err != nil {
		h.respondServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, "update", "page", &updated.ID, updated.Title)
	respondJSONOK(w, updated)
}

// Delete archives the page (and its entire subtree). page.delete is
// required at the workspace level AND the user must be able to admin the
// page subtree to prevent restricted descendants from being silently
// archived by an otherwise-eligible editor.
//
// Bug-hunt finding #3 fix: the previous version checked PageOpAdmin only
// on the root page, but Archive cascades via materialized path. We now
// enumerate the whole subtree and re-check PageOpAdmin on every
// descendant before triggering the cascade. A denied descendant blocks
// the whole archive — admins must restructure (move or grant) before
// archiving from above.
func (h *PageHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID, pageID, user, ok := h.requireWorkspacePageAuth(w, r, services.PageOpAdmin)
	if !ok {
		return
	}

	hasDelete, err := h.workspacePermissionAllowsDelete(user.ID, workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !hasDelete {
		respondNotFound(w, r, "Page")
		return
	}

	if err := h.service.ArchiveChecked(user.ID, pageID, func(subtree []models.Page) error {
		for _, d := range subtree {
			can, cerr := h.pageAuth.Can(user.ID, workspaceID, d.ID, services.PageOpAdmin)
			if cerr != nil {
				return cerr
			}
			if !can {
				// A restricted descendant blocks the whole archive. 404 keeps
				// existence-not-leaked — caller learns "no" without learning
				// which descendant denied.
				return services.ErrPageNotFound
			}
		}
		return nil
	}); err != nil {
		h.respondServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, "archive", "page", &pageID, "")
	respondJSONOK(w, map[string]bool{"archived": true})
}

// Move reparents a page. The user must be able to edit the moved page
// (already checked) and the destination parent (or be allowed at the
// workspace root). Cycle detection lives in the service.
func (h *PageHandler) Move(w http.ResponseWriter, r *http.Request) {
	workspaceID, pageID, user, ok := h.requireWorkspacePageAuth(w, r, services.PageOpEdit)
	if !ok {
		return
	}
	req, ok := decodeJSON[movePageRequest](w, r)
	if !ok {
		return
	}
	if req.ParentID != nil {
		canEditParent, err := h.pageAuth.Can(user.ID, workspaceID, *req.ParentID, services.PageOpEdit)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if !canEditParent {
			respondNotFound(w, r, "Page")
			return
		}
	}

	moved, err := h.service.Move(user.ID, pageID, req.ParentID, req.PrevSiblingID, req.NextSiblingID)
	if err != nil {
		h.respondServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, "move", "page", &moved.ID, moved.Title)
	respondJSONOK(w, moved)
}

// GetHistory returns paginated revision history for a page.
func (h *PageHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	_, pageID, _, ok := h.requireWorkspacePageAuth(w, r, services.PageOpView)
	if !ok {
		return
	}
	limit, offset := parseOffsetPagination(r, 50, 200)
	revs, err := h.service.ListRevisions(pageID, limit, offset)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, pageHistoryResponse{Revisions: revs})
}

// GetRevision returns a single revision; the revision must belong to the
// addressed page so we don't leak content across page boundaries.
func (h *PageHandler) GetRevision(w http.ResponseWriter, r *http.Request) {
	_, pageID, _, ok := h.requireWorkspacePageAuth(w, r, services.PageOpView)
	if !ok {
		return
	}
	revisionID, ok := requireIDParam(w, r, "revisionId")
	if !ok {
		return
	}
	rev, err := h.service.GetRevision(revisionID)
	if err != nil {
		if errors.Is(err, services.ErrPageNotFound) {
			respondNotFound(w, r, "Revision")
			return
		}
		respondInternalError(w, r, err)
		return
	}
	if rev.PageID != pageID {
		respondNotFound(w, r, "Revision")
		return
	}
	respondJSONOK(w, rev)
}

// RestoreRevision overwrites a page's live content from a revision.
// Requires edit permission on the target page.
func (h *PageHandler) RestoreRevision(w http.ResponseWriter, r *http.Request) {
	_, pageID, user, ok := h.requireWorkspacePageAuth(w, r, services.PageOpRestore)
	if !ok {
		return
	}
	revisionID, ok := requireIDParam(w, r, "revisionId")
	if !ok {
		return
	}
	restored, err := h.service.Restore(user.ID, pageID, revisionID)
	if err != nil {
		h.respondServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, "restore", "page", &restored.ID, restored.Title)
	respondJSONOK(w, restored)
}

// GetPermissions returns the page's inherit flag, the user's effective
// level, and the raw ACL rows attached to this page (NOT inherited rows).
// Phase 1 is read-only; the dialog edit affordance is Phase 2.
func (h *PageHandler) GetPermissions(w http.ResponseWriter, r *http.Request) {
	workspaceID, pageID, user, ok := h.requireWorkspacePageAuth(w, r, services.PageOpView)
	if !ok {
		return
	}

	page, err := h.service.GetByID(pageID)
	if err != nil {
		if errors.Is(err, services.ErrPageNotFound) {
			respondNotFound(w, r, "Page")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	effective := ""
	for _, op := range []string{services.PageOpAdmin, services.PageOpEdit, services.PageOpView} {
		can, _ := h.pageAuth.Can(user.ID, workspaceID, pageID, op)
		if can {
			effective = op
			break
		}
	}

	acl, err := h.service.ListOwnACL(pageID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	// Make sure the client always sees an array — a nil slice would
	// serialize as JSON `null`, which the frontend DataTable can't
	// handle (it does `data.length` unconditionally).
	if acl == nil {
		acl = []models.PagePermission{}
	}

	respondJSONOK(w, pageEffectivePermissionsResponse{
		PageID:             page.ID,
		InheritPermissions: page.InheritPermissions,
		EffectiveLevel:     effective,
		ACL:                acl,
	})
}

// GrantPermission attaches an ACL row to a page. Requires page.admin on
// the target page (system.admin / workspace.admin also satisfy via the
// evaluator).
func (h *PageHandler) GrantPermission(w http.ResponseWriter, r *http.Request) {
	_, pageID, user, ok := h.requireWorkspacePageAuth(w, r, services.PageOpAdmin)
	if !ok {
		return
	}
	req, ok := decodeJSON[grantPagePermissionRequest](w, r)
	if !ok {
		return
	}
	sanitize.ApplyAll(
		sanitize.Pair{Target: &req.PrincipalType, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &req.PermissionLevel, Policy: sanitize.ShortIdentifier},
	)
	if req.PrincipalType == "" || req.PrincipalID == 0 || req.PermissionLevel == "" {
		respondValidationError(w, r, "principal_type, principal_id, and permission_level are required")
		return
	}

	row, err := h.service.GrantPermission(user.ID, pageID, req.PrincipalType, req.PrincipalID, req.PermissionLevel)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrPageInvalidPrincipal):
			respondValidationError(w, r, "principal_type must be user, group, or role")
			return
		case errors.Is(err, services.ErrPageInvalidLevel):
			respondValidationError(w, r, "permission_level must be view, edit, or admin")
			return
		case errors.Is(err, services.ErrPagePermissionDuplicate):
			respondConflict(w, r, "permission already granted")
			return
		case errors.Is(err, services.ErrPageGrantPrincipalNotFound):
			respondValidationError(w, r, "principal does not exist")
			return
		}
		h.respondServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, "grant_permission", "page", &pageID, "")
	respondJSONCreated(w, row)
}

// RevokePermission deletes a single ACL row from a page. {permissionId}
// must belong to {pageId}; cross-page revoke attempts return 404.
func (h *PageHandler) RevokePermission(w http.ResponseWriter, r *http.Request) {
	_, pageID, user, ok := h.requireWorkspacePageAuth(w, r, services.PageOpAdmin)
	if !ok {
		return
	}
	permissionID, ok := requireIDParam(w, r, "permissionId")
	if !ok {
		return
	}
	if err := h.service.RevokePermission(user.ID, pageID, permissionID); err != nil {
		h.respondServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, "revoke_permission", "page", &pageID, "")
	respondJSONOK(w, map[string]bool{"revoked": true})
}

// SetInheritance flips the inherit_permissions flag on a page. Breaking
// inheritance is the mechanism the ACL UI uses to restrict a subtree.
func (h *PageHandler) SetInheritance(w http.ResponseWriter, r *http.Request) {
	_, pageID, user, ok := h.requireWorkspacePageAuth(w, r, services.PageOpAdmin)
	if !ok {
		return
	}
	req, ok := decodeJSON[setInheritanceRequest](w, r)
	if !ok {
		return
	}
	page, err := h.service.SetInheritPermissions(user.ID, pageID, req.InheritPermissions)
	if err != nil {
		h.respondServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, "set_inheritance", "page", &pageID, "")
	respondJSONOK(w, page)
}

// ListArchived returns every archived page in the workspace for the
// admin UI. Admin-only (system.admin or workspace.admin) — mirrors the
// archived-page view policy in PagePermissionService.Can.
func (h *PageHandler) ListArchived(w http.ResponseWriter, r *http.Request) {
	workspaceID, _, ok := h.requireWorkspaceAdmin(w, r)
	if !ok {
		return
	}
	rows, err := h.service.ListArchived(workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	out := make([]archivedPageResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, archivedPageResponse{
			ID:             row.ID,
			Title:          row.Title,
			Slug:           row.Slug,
			Path:           row.Path,
			Depth:          row.Depth,
			ArchivedAt:     row.ArchivedAt,
			ArchivedBy:     row.ArchivedBy,
			ArchivedByName: row.ArchivedByName,
		})
	}
	respondJSONOK(w, out)
}

// Unarchive flips a single archived page back to active without
// overwriting its content. Admin-only. Does not cascade — if the
// page's ancestor is still archived the page remains hidden from the
// tree until that ancestor is also unarchived (matches the existing
// Restore behavior).
func (h *PageHandler) Unarchive(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireWorkspaceAdmin(w, r)
	if !ok {
		return
	}
	pageID, ok := requireIDParam(w, r, "pageId")
	if !ok {
		return
	}
	page, err := h.service.GetByID(pageID)
	if err != nil {
		if errors.Is(err, services.ErrPageNotFound) {
			respondNotFound(w, r, "Page")
			return
		}
		respondInternalError(w, r, err)
		return
	}
	if page.WorkspaceID != workspaceID {
		respondNotFound(w, r, "Page")
		return
	}
	restored, err := h.service.Unarchive(user.ID, pageID)
	if err != nil {
		h.respondServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, "unarchive", "page", &restored.ID, restored.Title)
	respondJSONOK(w, restored)
}

// --- helpers ---

// requireWorkspaceAdmin enforces system.admin OR workspace.admin on the
// path's {workspaceId}. Used for workspace-wide admin surfaces where
// there's no specific pageID to feed PagePermissionService.Can —
// mirrors the gating that Can applies to archived pages
// (page_permission_service.go:96-109).
//
// Returns 404 (not 403) on failure to keep workspace existence
// unleakable, consistent with the project-wide policy captured in
// project_workspace_permissions_open_default.
func (h *PageHandler) requireWorkspaceAdmin(w http.ResponseWriter, r *http.Request) (workspaceID int, user *models.User, ok bool) {
	workspaceID, ok = requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	user, ok = RequireAuth(w, r)
	if !ok {
		return
	}
	isAdmin, err := h.pageAuth.IsSystemAdmin(user.ID)
	if err != nil {
		respondInternalError(w, r, err)
		ok = false
		return
	}
	if isAdmin {
		return
	}
	wsAdmin, err := h.pageAuth.HasWorkspacePermissionFor(user.ID, workspaceID, models.PermissionWorkspaceAdmin)
	if err != nil {
		respondInternalError(w, r, err)
		ok = false
		return
	}
	if !wsAdmin {
		respondNotFound(w, r, "Workspace")
		ok = false
		return
	}
	return
}

// requireWorkspacePageAuth pulls {workspaceId} + {pageId} + the current
// user, then runs the page permission check for the requested op. On
// failure it writes the appropriate 404/401 and returns ok=false.
func (h *PageHandler) requireWorkspacePageAuth(w http.ResponseWriter, r *http.Request, op string) (workspaceID, pageID int, user *models.User, ok bool) {
	workspaceID, ok = requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	pageID, ok = requireIDParam(w, r, "pageId")
	if !ok {
		return
	}
	user, ok = RequireAuth(w, r)
	if !ok {
		return
	}
	can, err := h.pageAuth.Can(user.ID, workspaceID, pageID, op)
	if err != nil {
		respondInternalError(w, r, err)
		ok = false
		return
	}
	if !can {
		respondNotFound(w, r, "Page")
		ok = false
		return
	}
	return
}

func (h *PageHandler) workspacePermissionAllowsCreate(userID, workspaceID int) bool {
	// Any of: page.create, page.admin, workspace.admin, system.admin unlock create.
	for _, key := range []string{
		models.PermissionPageCreate,
		models.PermissionPageAdmin,
		models.PermissionWorkspaceAdmin,
	} {
		if has, err := h.permFromService(userID, workspaceID, key); err == nil && has {
			return true
		}
	}
	return false
}

func (h *PageHandler) workspacePermissionAllowsDelete(userID, workspaceID int) (bool, error) {
	return h.permFromService(userID, workspaceID, models.PermissionPageDelete)
}

// permFromService is a small helper that re-uses the underlying
// PermissionService via PagePermissionService for direct workspace-permission
// lookups (page.create, page.delete).
func (h *PageHandler) permFromService(userID, workspaceID int, key string) (bool, error) {
	return h.pageAuth.HasWorkspacePermissionFor(userID, workspaceID, key)
}

func (h *PageHandler) respondServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, services.ErrPageNotFound):
		respondNotFound(w, r, "Page")
	case errors.Is(err, services.ErrPageTitleRequired):
		respondValidationError(w, r, "title is required")
	case errors.Is(err, services.ErrPageParentMismatch):
		respondValidationError(w, r, "parent belongs to a different workspace")
	case errors.Is(err, services.ErrPageCycle):
		respondConflict(w, r, "move would create a cycle")
	case errors.Is(err, services.ErrPageDepthExceeded):
		respondConflict(w, r, "page tree depth limit exceeded")
	case errors.Is(err, services.ErrPageSlugConflict):
		respondConflict(w, r, "slug conflicts with an existing sibling page")
	case errors.Is(err, services.ErrPageRevisionMismatch):
		respondNotFound(w, r, "Revision")
	default:
		respondInternalError(w, r, err)
	}
}
