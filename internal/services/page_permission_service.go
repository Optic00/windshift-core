package services

import (
	"database/sql"
	"errors"
	"fmt"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

// PagePermissionService evaluates Confluence-style page permissions.
//
// Evaluation order (Phase 1, grant-only model):
//
//  1. system.admin → always allowed.
//  2. workspace.admin or page.admin on the workspace → admin-equivalent
//     (view + edit + admin).
//  3. Walk from the page upward, collecting ACL rows until inherit_permissions
//     = false breaks the chain. If the effective ACL is non-empty, access
//     requires a matching principal at the required level. If the ACL is
//     empty (the common case), workspace role grants decide via the
//     standard PermissionService.
//
// Phase 1 has no deny rows; the dialog UI to manage ACLs lands in Phase 2.
// Cache invalidation will land alongside the ACL editor — for now the
// evaluator is uncached, which is fast enough at workspace-tree scale.
type PagePermissionService struct {
	db    database.Database
	perm  *PermissionService
	pages *repository.PageRepository
}

// NewPagePermissionService wires the evaluator against the shared
// PermissionService (used for workspace/system permission checks).
func NewPagePermissionService(db database.Database, perm *PermissionService) *PagePermissionService {
	return &PagePermissionService{
		db:    db,
		perm:  perm,
		pages: repository.NewPageRepository(db),
	}
}

// Page operations evaluated by Can.
const (
	PageOpView    = "view"
	PageOpEdit    = "edit"
	PageOpAdmin   = "admin"
	PageOpRestore = "restore"
)

// HasWorkspacePermissionFor exposes a workspace-level permission check
// through the same evaluator that handlers already hold. Used by the page
// HTTP handler for permission keys that don't depend on a specific page
// (page.create, page.delete).
func (s *PagePermissionService) HasWorkspacePermissionFor(userID, workspaceID int, key string) (bool, error) {
	return s.perm.HasWorkspacePermission(userID, workspaceID, key)
}

// IsSystemAdmin exposes the system-admin check the page handler needs when
// gating workspace-wide admin surfaces (e.g. the archived-pages list)
// without going through Can on a specific pageID.
func (s *PagePermissionService) IsSystemAdmin(userID int) (bool, error) {
	return s.perm.IsSystemAdmin(userID)
}

// Can reports whether userID may perform op on pageID in the given
// workspace. workspaceID must match the page's workspace; cross-workspace
// calls return false (rather than ErrPageNotFound) so handlers can map to
// 404 without leaking page existence.
//
// Archived pages have a separate policy: mutations (edit, admin) always
// return false, and view is granted only to system.admin or
// workspace.admin. Live pages flow through the normal admin / ACL / role
// fallback chain.
func (s *PagePermissionService) Can(userID, workspaceID, pageID int, op string) (bool, error) {
	if !isValidPageOp(op) {
		return false, fmt.Errorf("invalid page op %q", op)
	}
	if userID == 0 {
		return false, nil
	}

	// Load the page first so the archived check can run before any admin
	// short-circuit. Cross-workspace and not-found are both 404-equivalent.
	page, err := s.pages.GetByID(pageID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if page.WorkspaceID != workspaceID {
		return false, nil
	}

	subjectID, err := s.delegatedPagePrincipalUserID(userID)
	if err != nil {
		return false, err
	}

	if page.ArchivedAt != nil {
		// Archived pages are frozen except for the explicit restore operation.
		// View and restore on archived pages are admin-only: system.admin or
		// workspace.admin. Page-level ACL grants do NOT apply to archived pages.
		if op != PageOpView && op != PageOpRestore {
			return false, nil
		}
		if isAdmin, ierr := s.perm.IsSystemAdmin(userID); ierr != nil {
			return false, ierr
		} else if isAdmin {
			return true, nil
		}
		return s.perm.HasWorkspacePermission(userID, workspaceID, models.PermissionWorkspaceAdmin)
	}

	if isAdmin, ierr := s.perm.IsSystemAdmin(userID); ierr != nil {
		return false, ierr
	} else if isAdmin {
		return true, nil
	}

	hasWsAdmin, err := s.perm.HasWorkspacePermission(userID, workspaceID, models.PermissionWorkspaceAdmin)
	if err != nil {
		return false, err
	}
	hasPageAdmin, err := s.perm.HasWorkspacePermission(userID, workspaceID, models.PermissionPageAdmin)
	if err != nil {
		return false, err
	}
	if hasWsAdmin || hasPageAdmin {
		return true, nil
	}

	acl, err := s.collectEffectiveACL(page)
	if err != nil {
		return false, err
	}

	if len(acl) > 0 {
		// Restricted page: ACL must contain a matching principal at the
		// required level. Workspace-role permissions do NOT confer the
		// requested op on a restricted page — that's the whole point of
		// breaking inheritance — but they DO establish that the caller
		// is a workspace member. An ACL grant on a user who never joined
		// the workspace (e.g. a stale row left over after removing them)
		// must not be a back door, so we require workspace.page.view as
		// the membership floor on top of the ACL match.
		matched, err := s.matchesACL(subjectID, workspaceID, acl, op)
		if err != nil || !matched {
			return matched, err
		}
		return s.perm.HasWorkspacePermission(userID, workspaceID, models.PermissionPageView)
	}

	// Inheritance broken with no explicit grants → admin-only. The admin
	// checks above already returned true for system.admin / workspace.admin
	// / page.admin, so reaching here on a deny-by-default page means the
	// caller cannot pass.
	if !page.InheritPermissions {
		return false, nil
	}

	// Open page: fall back to workspace-scoped page.* permissions.
	return s.perm.HasWorkspacePermission(userID, workspaceID, workspacePermKeyForOp(op))
}

// ListVisiblePageIDs returns the subset of pageIDs the user can view.
// Optimized for tree rendering, which checks ~tens to hundreds of pages
// per workspace per request.
//
// The batched evaluator preserves Can(..., PageOpView)'s semantics:
//
//   - userID == 0 → all false (denied).
//   - cross-workspace or missing page → false.
//   - archived page → visible only to system.admin or workspace.admin.
//   - system.admin / workspace.admin / workspace.page.admin → all live pages
//     visible.
//   - otherwise, walk inheritance chain to assemble the effective ACL; a
//     restricted page (ACL present) requires a matching ACL row AND the
//     workspace's page.view permission as a membership floor; a page with
//     inheritance broken and no ACL is admin-only; an open page falls back
//     to workspace page.view.
//
// Implementation notes — savings vs. the per-page Can loop:
//
//   - admin checks (system, workspace.admin, page.admin, page.view) are
//     evaluated once per call instead of once per page.
//   - user→groups and user→workspace-roles lookups happen once.
//   - pages, ancestor inherit flags, and page_permissions rows are each
//     bulk-loaded in a single query covering every page and every ancestor.
func (s *PagePermissionService) ListVisiblePageIDs(userID, workspaceID int, pageIDs []int) (map[int]bool, error) {
	out := make(map[int]bool, len(pageIDs))
	if len(pageIDs) == 0 {
		return out, nil
	}
	for _, id := range pageIDs {
		out[id] = false
	}
	if userID == 0 {
		return out, nil
	}

	isSysAdmin, err := s.perm.IsSystemAdmin(userID)
	if err != nil {
		return nil, err
	}
	hasWsAdmin, err := s.perm.HasWorkspacePermission(userID, workspaceID, models.PermissionWorkspaceAdmin)
	if err != nil {
		return nil, err
	}
	hasPageAdmin, err := s.perm.HasWorkspacePermission(userID, workspaceID, models.PermissionPageAdmin)
	if err != nil {
		return nil, err
	}
	hasWorkspacePageView, err := s.perm.HasWorkspacePermission(userID, workspaceID, models.PermissionPageView)
	if err != nil {
		return nil, err
	}
	adminShortcut := isSysAdmin || hasWsAdmin || hasPageAdmin

	pages, err := s.pages.GetByIDs(pageIDs)
	if err != nil {
		return nil, err
	}

	livePages := make([]models.Page, 0, len(pages))
	for _, p := range pages {
		if p.WorkspaceID != workspaceID {
			continue
		}
		if p.ArchivedAt != nil {
			// Archived pages: page-level ACLs do NOT apply; only system or
			// workspace admin can view (mirrors Can's archived branch).
			if isSysAdmin || hasWsAdmin {
				out[p.ID] = true
			}
			continue
		}
		if adminShortcut {
			out[p.ID] = true
			continue
		}
		livePages = append(livePages, p)
	}
	if len(livePages) == 0 {
		return out, nil
	}

	// Collect the union of (page + ancestor) ids so we can bulk-load
	// inheritance flags and page_permissions in one round trip each.
	candidateSet := make(map[int]struct{}, len(livePages)*2)
	for _, p := range livePages {
		candidateSet[p.ID] = struct{}{}
		if p.InheritPermissions {
			for _, aid := range splitPathIDs(p.Path) {
				candidateSet[aid] = struct{}{}
			}
		}
	}
	candidateIDs := make([]int, 0, len(candidateSet))
	for id := range candidateSet {
		candidateIDs = append(candidateIDs, id)
	}

	inheritFlags, err := s.loadAncestorInheritFlags(candidateIDs)
	if err != nil {
		return nil, err
	}
	aclsByPage, err := s.loadPagePermissionsByPage(candidateIDs)
	if err != nil {
		return nil, err
	}

	subjectID, err := s.delegatedPagePrincipalUserID(userID)
	if err != nil {
		return nil, err
	}
	groupIDs, err := s.userGroupIDs(subjectID)
	if err != nil {
		return nil, err
	}
	roleIDs, err := s.userWorkspaceRoleIDs(subjectID, workspaceID)
	if err != nil {
		return nil, err
	}

	viewLevels := map[string]bool{
		models.PagePermissionLevelView:  true,
		models.PagePermissionLevelEdit:  true,
		models.PagePermissionLevelAdmin: true,
	}

	for _, p := range livePages {
		acl := collectACLInMemory(p, inheritFlags, aclsByPage)
		if len(acl) > 0 {
			// Restricted: must match ACL AND meet workspace page.view floor.
			if !hasWorkspacePageView {
				continue
			}
			if matchesACLInMemory(subjectID, groupIDs, roleIDs, acl, viewLevels) {
				out[p.ID] = true
			}
			continue
		}
		// No effective ACL.
		if !p.InheritPermissions {
			// Inheritance broken with no grants → admin-only. Admins were
			// already short-circuited above, so deny here.
			continue
		}
		out[p.ID] = hasWorkspacePageView
	}
	return out, nil
}

// loadPagePermissionsByPage bulk-loads page_permissions rows for the given
// page ids and groups them by page_id. The result mirrors what
// collectEffectiveACL would scan per-page; pages without rows are absent
// from the map (callers must treat that as "no rows").
func (s *PagePermissionService) loadPagePermissionsByPage(ids []int) (map[int][]models.PagePermission, error) {
	if len(ids) == 0 {
		return map[int][]models.PagePermission{}, nil
	}
	args := make([]interface{}, len(ids))
	placeholders := ""
	for i, id := range ids {
		args[i] = id
		if i > 0 {
			placeholders += ", "
		}
		placeholders += "?"
	}
	rows, err := s.db.Query(`
		SELECT id, page_id, principal_type, principal_id, permission_level, granted_by, granted_at
		FROM page_permissions
		WHERE page_id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("bulk load page permissions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int][]models.PagePermission, len(ids))
	for rows.Next() {
		var p models.PagePermission
		var grantedBy sql.NullInt64
		if err := rows.Scan(&p.ID, &p.PageID, &p.PrincipalType, &p.PrincipalID, &p.PermissionLevel, &grantedBy, &p.GrantedAt); err != nil {
			return nil, fmt.Errorf("scan ACL row: %w", err)
		}
		if grantedBy.Valid {
			gb := int(grantedBy.Int64)
			p.GrantedBy = &gb
		}
		out[p.PageID] = append(out[p.PageID], p)
	}
	return out, rows.Err()
}

// collectACLInMemory mirrors collectEffectiveACL's chain-walk but operates
// against pre-loaded inheritance flags and per-page ACL rows so it issues no
// queries. The page's own ACL is always included; if the page inherits, walk
// ancestors closest-first and stop after the first ancestor that breaks
// inheritance (or is missing — fail-closed).
func collectACLInMemory(p models.Page, inheritFlags map[int]bool, aclsByPage map[int][]models.PagePermission) []models.PagePermission {
	out := append([]models.PagePermission(nil), aclsByPage[p.ID]...)
	if !p.InheritPermissions {
		return out
	}
	ancestorIDs := splitPathIDs(p.Path)
	for i := len(ancestorIDs) - 1; i >= 0; i-- {
		aid := ancestorIDs[i]
		out = append(out, aclsByPage[aid]...)
		if inh, ok := inheritFlags[aid]; !ok || !inh {
			break
		}
	}
	return out
}

// matchesACLInMemory mirrors matchesACL's principal-match loop but uses
// pre-computed group/role memberships so we don't requery per page.
func matchesACLInMemory(userID int, groupIDs, roleIDs []int, acl []models.PagePermission, wantLevels map[string]bool) bool {
	for _, row := range acl {
		if !wantLevels[row.PermissionLevel] {
			continue
		}
		switch row.PrincipalType {
		case models.PagePrincipalTypeUser:
			if row.PrincipalID == userID {
				return true
			}
		case models.PagePrincipalTypeGroup:
			for _, gid := range groupIDs {
				if gid == row.PrincipalID {
					return true
				}
			}
		case models.PagePrincipalTypeRole:
			for _, rid := range roleIDs {
				if rid == row.PrincipalID {
					return true
				}
			}
		}
	}
	return false
}

// collectEffectiveACL walks from the page upward through its ancestors
// (using the materialized path), gathering every page_permissions row
// until inherit_permissions = false breaks the chain or we reach the root.
// The walk is closest-ancestor-first; the breaking ancestor's own ACL is
// included before the chain stops.
func (s *PagePermissionService) collectEffectiveACL(page *models.Page) ([]models.PagePermission, error) {
	// Always include the page's own ACL rows.
	ids := []int{page.ID}

	if page.InheritPermissions {
		ancestorIDs := splitPathIDs(page.Path)
		if len(ancestorIDs) > 0 {
			// Load the inherit_permissions flag for every ancestor in one
			// query, then walk closest-to-furthest in memory so we can
			// stop at the first ancestor that breaks inheritance.
			ancestorInherit, err := s.loadAncestorInheritFlags(ancestorIDs)
			if err != nil {
				return nil, err
			}
			// path is "/a/b/c/" listing ancestors root-first; walk in
			// reverse so the closest parent is considered first.
			for i := len(ancestorIDs) - 1; i >= 0; i-- {
				aid := ancestorIDs[i]
				ids = append(ids, aid)
				// If the ancestor row is missing (deleted concurrently)
				// or breaks inheritance, stop walking further up. The
				// missing-row case is fail-closed: don't pretend to
				// inherit through a gap in the chain.
				if inherits, ok := ancestorInherit[aid]; !ok || !inherits {
					break
				}
			}
		}
	}

	if len(ids) == 0 {
		return nil, nil
	}

	args := make([]interface{}, len(ids))
	placeholders := ""
	for i, id := range ids {
		args[i] = id
		if i > 0 {
			placeholders += ", "
		}
		placeholders += "?"
	}

	rows, err := s.db.Query(`
		SELECT id, page_id, principal_type, principal_id, permission_level, granted_by, granted_at
		FROM page_permissions
		WHERE page_id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("load effective ACL: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []models.PagePermission
	for rows.Next() {
		var p models.PagePermission
		var grantedBy sql.NullInt64
		if err := rows.Scan(&p.ID, &p.PageID, &p.PrincipalType, &p.PrincipalID, &p.PermissionLevel, &grantedBy, &p.GrantedAt); err != nil {
			return nil, fmt.Errorf("scan ACL row: %w", err)
		}
		if grantedBy.Valid {
			gb := int(grantedBy.Int64)
			p.GrantedBy = &gb
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// loadAncestorInheritFlags fetches the inherit_permissions column for the
// given page ids in a single query. Returns a map keyed by page id so
// collectEffectiveACL can decide whether to keep walking the chain.
// Missing ids are absent from the result (collectEffectiveACL treats that
// as "stop", fail-closed).
func (s *PagePermissionService) loadAncestorInheritFlags(ids []int) (map[int]bool, error) {
	if len(ids) == 0 {
		return map[int]bool{}, nil
	}
	args := make([]interface{}, len(ids))
	placeholders := ""
	for i, id := range ids {
		args[i] = id
		if i > 0 {
			placeholders += ", "
		}
		placeholders += "?"
	}
	rows, err := s.db.Query(
		"SELECT id, inherit_permissions FROM pages WHERE id IN ("+placeholders+")",
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("load ancestor inherit flags: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int]bool, len(ids))
	for rows.Next() {
		var id int
		var inherit bool
		if err := rows.Scan(&id, &inherit); err != nil {
			return nil, fmt.Errorf("scan ancestor row: %w", err)
		}
		out[id] = inherit
	}
	return out, rows.Err()
}

// matchesACL returns true when the user has at least one effective ACL row
// (by direct user grant, group membership, or workspace role assignment)
// at a level that satisfies the requested op.
func (s *PagePermissionService) matchesACL(userID, workspaceID int, acl []models.PagePermission, op string) (bool, error) {
	wantedLevels := allowedLevelsForOp(op)
	wantSet := make(map[string]bool, len(wantedLevels))
	for _, l := range wantedLevels {
		wantSet[l] = true
	}

	// Pre-compute the user's groups and workspace roles once per call so
	// we don't requery for every ACL row.
	groupIDs, err := s.userGroupIDs(userID)
	if err != nil {
		return false, err
	}
	roleIDs, err := s.userWorkspaceRoleIDs(userID, workspaceID)
	if err != nil {
		return false, err
	}

	for _, row := range acl {
		if !wantSet[row.PermissionLevel] {
			continue
		}
		switch row.PrincipalType {
		case models.PagePrincipalTypeUser:
			if row.PrincipalID == userID {
				return true, nil
			}
		case models.PagePrincipalTypeGroup:
			for _, gid := range groupIDs {
				if gid == row.PrincipalID {
					return true, nil
				}
			}
		case models.PagePrincipalTypeRole:
			for _, rid := range roleIDs {
				if rid == row.PrincipalID {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// delegatedPagePrincipalUserID returns the human principal whose page ACLs an
// authenticated user should use. Owned agents inherit their owner's effective
// permissions everywhere else via PermissionService; restricted-page ACLs must
// follow the same delegation or an owner-visible page becomes invisible to the
// owner's API token. Service users (agents without an owner) keep their own
// principal identity and must be granted page/workspace access explicitly.
func (s *PagePermissionService) delegatedPagePrincipalUserID(userID int) (int, error) {
	var isAgent sql.NullBool
	var ownerID sql.NullInt64
	err := s.db.QueryRow(
		`SELECT COALESCE(is_agent, false), agent_owner_user_id FROM users WHERE id = ?`,
		userID,
	).Scan(&isAgent, &ownerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return userID, nil
		}
		return 0, fmt.Errorf("resolve page principal delegation: %w", err)
	}
	if isAgent.Valid && isAgent.Bool && ownerID.Valid {
		return int(ownerID.Int64), nil
	}
	return userID, nil
}

// userGroupIDs returns the user's active group memberships. Mirrors the
// is_active filter applied by PermissionService.buildUserPermissionCache
// — inactive groups must not satisfy ACL grants targeted at "group N".
func (s *PagePermissionService) userGroupIDs(userID int) ([]int, error) {
	rows, err := s.db.Query(`
		SELECT gm.group_id FROM group_members gm
		JOIN groups g ON g.id = gm.group_id
		WHERE gm.user_id = ? AND g.is_active = TRUE
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("load user groups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// userWorkspaceRoleIDs returns every role the user effectively holds in
// the workspace, from BOTH direct user→role assignments and indirect
// group→role assignments (i.e. role granted to a group the user is a
// member of). Without the group arm, an ACL row that grants a role would
// fail to match a user who reaches that role only via their group — a
// silent divergence from PermissionService's cache build.
func (s *PagePermissionService) userWorkspaceRoleIDs(userID, workspaceID int) ([]int, error) {
	rows, err := s.db.Query(`
		SELECT role_id FROM user_workspace_roles
		WHERE user_id = ? AND workspace_id = ?
		UNION
		SELECT gwr.role_id FROM group_workspace_roles gwr
		JOIN group_members gm ON gm.group_id = gwr.group_id
		JOIN groups g ON g.id = gwr.group_id
		WHERE gm.user_id = ? AND gwr.workspace_id = ? AND g.is_active = TRUE
	`, userID, workspaceID, userID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("load user workspace roles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func isValidPageOp(op string) bool {
	return op == PageOpView || op == PageOpEdit || op == PageOpAdmin || op == PageOpRestore
}

func allowedLevelsForOp(op string) []string {
	switch op {
	case PageOpView:
		return []string{models.PagePermissionLevelView, models.PagePermissionLevelEdit, models.PagePermissionLevelAdmin}
	case PageOpEdit, PageOpRestore:
		return []string{models.PagePermissionLevelEdit, models.PagePermissionLevelAdmin}
	case PageOpAdmin:
		return []string{models.PagePermissionLevelAdmin}
	default:
		return nil
	}
}

func workspacePermKeyForOp(op string) string {
	switch op {
	case PageOpView:
		return models.PermissionPageView
	case PageOpEdit, PageOpRestore:
		return models.PermissionPageEdit
	case PageOpAdmin:
		return models.PermissionPageAdmin
	default:
		return models.PermissionPageView
	}
}

// splitPathIDs parses a materialized path like "/12/45/" into [12, 45].
// Returns nil for "" or "/".
func splitPathIDs(path string) []int {
	if path == "" || path == "/" {
		return nil
	}
	var out []int
	var cur int
	hasDigit := false
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c >= '0' && c <= '9' {
			cur = cur*10 + int(c-'0')
			hasDigit = true
			continue
		}
		if hasDigit {
			out = append(out, cur)
			cur = 0
			hasDigit = false
		}
	}
	if hasDigit {
		out = append(out, cur)
	}
	return out
}
