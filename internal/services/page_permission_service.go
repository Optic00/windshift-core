package services

import (
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
	PageOpView  = "view"
	PageOpEdit  = "edit"
	PageOpAdmin = "admin"
)

// HasWorkspacePermissionFor exposes a workspace-level permission check
// through the same evaluator that handlers already hold. Used by the page
// HTTP handler for permission keys that don't depend on a specific page
// (page.create, page.delete).
func (s *PagePermissionService) HasWorkspacePermissionFor(userID, workspaceID int, key string) (bool, error) {
	return s.perm.HasWorkspacePermission(userID, workspaceID, key)
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

	if page.ArchivedAt != nil {
		// Mutations on archived pages always 404. The page is frozen until
		// an explicit unarchive op (not implemented yet) restores it; this
		// keeps Restore, Update, Move, and ACL writes from changing
		// archived rows.
		if op != PageOpView {
			return false, nil
		}
		// View on archived pages is admin-only: system.admin or workspace
		// admin. Page-level ACL grants do NOT apply to archived pages —
		// they're frozen from the user's perspective.
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
		matched, err := s.matchesACL(userID, workspaceID, acl, op)
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
func (s *PagePermissionService) ListVisiblePageIDs(userID, workspaceID int, pageIDs []int) (map[int]bool, error) {
	out := make(map[int]bool, len(pageIDs))
	for _, id := range pageIDs {
		can, err := s.Can(userID, workspaceID, id, PageOpView)
		if err != nil {
			return nil, err
		}
		out[id] = can
	}
	return out, nil
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
		var grantedBy interface{}
		if err := rows.Scan(&p.ID, &p.PageID, &p.PrincipalType, &p.PrincipalID, &p.PermissionLevel, &grantedBy, &p.GrantedAt); err != nil {
			return nil, fmt.Errorf("scan ACL row: %w", err)
		}
		if grantedBy != nil {
			if v, ok := grantedBy.(int64); ok {
				gb := int(v)
				p.GrantedBy = &gb
			}
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

// userGroupIDs returns the user's active group memberships. Mirrors the
// is_active filter applied by PermissionService.buildUserPermissionCache
// — inactive groups must not satisfy ACL grants targeted at "group N".
func (s *PagePermissionService) userGroupIDs(userID int) ([]int, error) {
	rows, err := s.db.Query(`
		SELECT gm.group_id FROM group_members gm
		JOIN groups g ON g.id = gm.group_id
		WHERE gm.user_id = ? AND g.is_active = 1
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
		WHERE gm.user_id = ? AND gwr.workspace_id = ? AND g.is_active = 1
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
	return op == PageOpView || op == PageOpEdit || op == PageOpAdmin
}

func allowedLevelsForOp(op string) []string {
	switch op {
	case PageOpView:
		return []string{models.PagePermissionLevelView, models.PagePermissionLevelEdit, models.PagePermissionLevelAdmin}
	case PageOpEdit:
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
	case PageOpEdit:
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
