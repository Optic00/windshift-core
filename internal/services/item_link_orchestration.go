package services

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"windshift/internal/models"
	"windshift/internal/repository"
)

// ============================================
// Interfaces — set on ItemLinkService via With*
// ============================================

// AssetPermissionChecker is the minimal slice of the AssetHandler the link
// orchestration needs. Held as an interface because the implementation
// lives in the handlers package and a direct import would create a cycle.
// nil ⇒ fail-closed (asset endpoints always 404).
type AssetPermissionChecker interface {
	HasAssetSetPermission(userID, setID int, permissionKey string) (bool, error)
}

// PagePermissionChecker is satisfied by *PagePermissionService (and by
// tests). nil ⇒ fail-closed (page endpoints always 404).
type PagePermissionChecker interface {
	Can(userID, workspaceID, pageID int, op string) (bool, error)
	ListVisiblePageIDs(userID, workspaceID int, pageIDs []int) (map[int]bool, error)
}

// ItemLinkNotificationEmitter is the slot the orchestration uses to fire
// "item linked" / "item unlinked" notifications. nil ⇒ no notifications
// (the orchestration still succeeds).
type ItemLinkNotificationEmitter interface {
	EmitEvent(event *NotificationEvent)
}

// ItemLinkActionEmitter is the slot the orchestration uses to fire action
// events for automation workflows. nil ⇒ no action events.
type ItemLinkActionEmitter interface {
	EmitActionEvent(event *models.ActionEvent)
}

// ============================================
// Sentinel errors (HTTP layers map these to status codes)
// ============================================

// EntityNotAccessibleError covers both "no such entity" and "no view/edit
// permission" — they share an HTTP response (404, per the existence-leak
// policy) so the orchestration collapses them into one error type.
type EntityNotAccessibleError struct {
	EntityType string
	EntityID   int
}

func (e *EntityNotAccessibleError) Error() string {
	return fmt.Sprintf("%s %d not found or not accessible", e.EntityType, e.EntityID)
}

// IsEntityNotAccessible reports whether err is an EntityNotAccessibleError.
func IsEntityNotAccessible(err error) bool {
	var e *EntityNotAccessibleError
	return errors.As(err, &e)
}

var (
	// ErrLinkSelfReference is returned when source and target identify the
	// same entity. HTTP layer maps to 400.
	ErrLinkSelfReference = errors.New("cannot create link to self")

	// ErrLinkInvalidEntityType is returned when source_type or target_type
	// is not one of {item, test_case, asset, page}. HTTP layer maps to 400.
	ErrLinkInvalidEntityType = errors.New("invalid entity type (want item, test_case, asset, or page)")

	// ErrLinkExists is returned when an equivalent link already exists in
	// either direction. HTTP layer maps to 409.
	ErrLinkExists = errors.New("link already exists")

	// ErrLinkNotFound is returned by Delete / Get flows. HTTP layer maps
	// to 404.
	ErrLinkNotFound = errors.New("link not found")

	// ErrLinkCrossWorkspacePage is returned when an item↔page link
	// crosses workspaces. HTTP layer maps to 404 (kept opaque to match
	// per-page ACL denial shape).
	ErrLinkCrossWorkspacePage = errors.New("page link endpoints must share a workspace")
)

// ============================================
// With* dependency setters
// ============================================

// WithPermissionService wires the workspace-permission checker required
// for orchestration on item / test_case endpoints.
func (s *ItemLinkService) WithPermissionService(p *PermissionService) *ItemLinkService {
	s.perm = p
	return s
}

// WithPagePermissionChecker wires the per-page ACL checker. Without it,
// any link operation touching a page endpoint fails closed (404).
func (s *ItemLinkService) WithPagePermissionChecker(p PagePermissionChecker) *ItemLinkService {
	s.pagePerm = p
	return s
}

// WithAssetPermissionChecker wires the asset-set permission checker.
// Without it, any link operation touching an asset endpoint fails closed.
func (s *ItemLinkService) WithAssetPermissionChecker(c AssetPermissionChecker) *ItemLinkService {
	s.assetPerm = c
	return s
}

// WithNotificationEmitter wires an optional notification sink. Linked /
// unlinked events for item-source links fire only when this is set AND
// the source resolves to a real item.
func (s *ItemLinkService) WithNotificationEmitter(e ItemLinkNotificationEmitter) *ItemLinkService {
	s.notifications = e
	return s
}

// WithActionEmitter wires an optional action-event sink (automation
// workflows). Same item-source-only firing rule as notifications.
func (s *ItemLinkService) WithActionEmitter(e ItemLinkActionEmitter) *ItemLinkService {
	s.actions = e
	return s
}

// ============================================
// Public orchestration surface
// ============================================

// ListLinkTypes returns the active link-type catalog (system + custom).
// Pass includeInactive=true for admin / debug views.
func (s *ItemLinkService) ListLinkTypes(includeInactive bool) ([]models.LinkType, error) {
	repo := repository.NewLinkTypeRepository(s.db)
	return repo.List(includeInactive)
}

// ListLinksByCustomField returns the links managed by a specific custom
// linking field for one item. Primary fields store their links with
// (custom_field_id, source_type='item', source_id=item); mirror fields
// store them under the *primary* field id with target_id=item. Pass
// mirror=true to query the mirror-side view (the caller is expected to
// have resolved fieldID to the primary's id before calling).
//
// Replaces the raw getLinksWhere call sites in handlers/item_links.go's
// GetFieldLinks, so the SQL footgun stays inside the service.
func (s *ItemLinkService) ListLinksByCustomField(fieldID, itemID int, mirror bool) ([]models.ItemLink, error) {
	if mirror {
		return s.getLinksWhere(
			"il.custom_field_id = ? AND il.target_type = 'item' AND il.target_id = ?",
			fieldID, itemID,
		)
	}
	return s.getLinksWhere(
		"il.custom_field_id = ? AND il.source_type = 'item' AND il.source_id = ?",
		fieldID, itemID,
	)
}

// CreateLinkWithChecks runs the full create flow used by both the
// cookie-auth handler and the v1 bearer handler. Returns the persisted
// link with all joined display fields populated.
//
// Errors callers should expect (in order of when they may be returned):
//   - ErrLinkSelfReference / ErrLinkInvalidEntityType — bad request
//   - *EntityNotAccessibleError — 404 (missing or no permission)
//   - ErrLinkCrossWorkspacePage — 404 (page invariant)
//   - ErrLinkExists — 409
//   - ErrInvalidLinkTypeForEntities — 400 (link-type / entity-type mismatch)
//
// Notifications and action events fire only when emitters are wired AND
// the source resolves to an "item" (notifications are item-centric).
func (s *ItemLinkService) CreateLinkWithChecks(userID int, params CreateItemLinkParams) (*models.ItemLink, error) {
	if !isValidLinkEntityType(params.SourceType) || !isValidLinkEntityType(params.TargetType) {
		return nil, ErrLinkInvalidEntityType
	}
	if params.SourceType == params.TargetType && params.SourceID == params.TargetID {
		return nil, ErrLinkSelfReference
	}

	// Permission checks first so unauthorized callers never get a
	// duplicate-detection 409 leak (would oracle the existence of a link).
	if err := s.CheckEntityPermission(userID, params.SourceType, params.SourceID, models.PermissionItemEdit, AssetPermissionKeyEdit); err != nil {
		return nil, err
	}
	if err := s.CheckEntityPermission(userID, params.TargetType, params.TargetID, models.PermissionItemView, AssetPermissionKeyView); err != nil {
		return nil, err
	}

	// Item↔page links must stay in one workspace. Cross-workspace ⇒ 404.
	if params.SourceType == "page" || params.TargetType == "page" {
		ok, err := s.linkEndpointsShareWorkspace(params.SourceType, params.SourceID, params.TargetType, params.TargetID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrLinkCrossWorkspacePage
		}
	}

	// Duplicate detection (either direction).
	var existingID int
	err := s.db.QueryRow(`
		SELECT id FROM item_links
		WHERE (source_type = ? AND source_id = ? AND target_type = ? AND target_id = ?)
		   OR (source_type = ? AND source_id = ? AND target_type = ? AND target_id = ?)
	`, params.SourceType, params.SourceID, params.TargetType, params.TargetID,
		params.TargetType, params.TargetID, params.SourceType, params.SourceID).Scan(&existingID)
	if err == nil {
		return nil, ErrLinkExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to probe duplicates: %w", err)
	}

	createdBy := userID
	params.CreatedBy = &createdBy

	id, err := s.CreateLink(params)
	if err != nil {
		return nil, err
	}
	if id == 0 {
		// CreateLink returns 0 on INSERT OR IGNORE; treat as duplicate.
		return nil, ErrLinkExists
	}

	link, err := s.getLinkByID(int(id))
	if err != nil {
		return nil, fmt.Errorf("failed to load created link: %w", err)
	}

	// Notification + action events — item-source only (matches legacy
	// handler behavior). Failures here do not roll back the link.
	if params.SourceType == "item" {
		s.emitLinkedEvents(userID, params, link)
	}
	return link, nil
}

// DeleteLinkWithChecks looks up the link, requires edit permission on its
// source, then deletes. Emits an unlinked notification when the source is
// an item.
func (s *ItemLinkService) DeleteLinkWithChecks(userID, linkID int) error {
	link, err := s.getLinkByID(linkID)
	if err != nil {
		return err
	}
	if link == nil {
		return ErrLinkNotFound
	}

	if err := s.CheckEntityPermission(userID, link.SourceType, link.SourceID, models.PermissionItemEdit, AssetPermissionKeyEdit); err != nil {
		return err
	}

	result, err := s.db.ExecWrite("DELETE FROM item_links WHERE id = ?", linkID)
	if err != nil {
		return fmt.Errorf("failed to delete link: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return ErrLinkNotFound
	}

	if link.SourceType == "item" {
		s.emitUnlinkedEvents(userID, link)
	}
	return nil
}

// ListLinksForEntityWithChecks returns the (outgoing, incoming) link
// slices visible to userID for the given entity. Per-page ACLs are
// applied; entities the user cannot see have their entire link rows
// dropped from the result (matches legacy handler behavior).
//
// entityType must be one of "item", "test_case", or "page". For "page",
// callers should additionally gate the route via `pages:read`.
func (s *ItemLinkService) ListLinksForEntityWithChecks(userID int, entityType string, entityID int) (outgoing, incoming []models.ItemLink, err error) {
	if entityType != "item" && entityType != "test_case" && entityType != "page" {
		return nil, nil, ErrLinkInvalidEntityType
	}
	if err := s.CheckEntityPermission(userID, entityType, entityID, models.PermissionItemView, AssetPermissionKeyView); err != nil {
		return nil, nil, err
	}

	outgoing, err = s.getLinksWhere("source_type = ? AND source_id = ? AND il.custom_field_id IS NULL", entityType, entityID)
	if err != nil {
		return nil, nil, err
	}
	incoming, err = s.getLinksWhere("target_type = ? AND target_id = ?", entityType, entityID)
	if err != nil {
		return nil, nil, err
	}

	accessibleKeys := s.AccessibleWorkspaceKeys(userID)
	accessibleWsIDs := s.AccessibleWorkspaceIDs(userID)
	accessibleSets := s.AccessibleAssetSetIDs(userID)

	outgoing = s.FilterLinksByAccess(outgoing, accessibleKeys, accessibleWsIDs, accessibleSets)
	incoming = s.FilterLinksByAccess(incoming, accessibleKeys, accessibleWsIDs, accessibleSets)
	outgoing = s.FilterPageLinksByACL(userID, outgoing)
	incoming = s.FilterPageLinksByACL(userID, incoming)
	return outgoing, incoming, nil
}

// ============================================
// Internal helpers
// ============================================

// isValidLinkEntityType reports whether s is one of the four entity
// types the link table supports.
func isValidLinkEntityType(s string) bool {
	switch s {
	case "item", "test_case", "asset", "page":
		return true
	}
	return false
}

// pageOpForWorkspacePerm maps the workspace-permission strings the link
// orchestration uses ("item.view" / "item.edit") onto the page-op
// vocabulary expected by PagePermissionChecker.Can. Anything not
// edit-or-higher resolves to view (safe default).
func pageOpForWorkspacePerm(workspacePerm string) string {
	if workspacePerm == models.PermissionItemEdit || workspacePerm == models.PermissionItemDelete {
		return PageOpEdit
	}
	return PageOpView
}

// ResolveEntityScope returns the scoping identifier for a link endpoint:
// items / test_cases / pages → workspace_id, assets → set_id. The
// found=false branch is used so callers can issue a clean 404 without
// leaking which lookup failed.
func (s *ItemLinkService) ResolveEntityScope(entityType string, entityID int) (wsID, setID int, found bool, err error) {
	switch entityType {
	case "item":
		wsID, err := repository.NewItemRepository(s.db).GetWorkspaceID(entityID)
		if errors.Is(err, repository.ErrNotFound) {
			return 0, 0, false, nil
		}
		if err != nil {
			return 0, 0, false, err
		}
		return wsID, 0, true, nil
	case "test_case":
		var v int
		err = s.db.QueryRow("SELECT workspace_id FROM test_cases WHERE id = ?", entityID).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, false, nil
		}
		if err != nil {
			return 0, 0, false, err
		}
		return v, 0, true, nil
	case "asset":
		var v int
		err = s.db.QueryRow("SELECT set_id FROM assets WHERE id = ?", entityID).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, false, nil
		}
		if err != nil {
			return 0, 0, false, err
		}
		return 0, v, true, nil
	case "page":
		var v int
		err = s.db.QueryRow("SELECT workspace_id FROM pages WHERE id = ?", entityID).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, false, nil
		}
		if err != nil {
			return 0, 0, false, err
		}
		return v, 0, true, nil
	default:
		return 0, 0, false, fmt.Errorf("unsupported entity type %q", entityType)
	}
}

// CheckEntityPermission returns nil when userID may operate on the
// endpoint with the given workspace permission, or
// *EntityNotAccessibleError otherwise (covers "missing", "no perm
// checker wired", and "permission denied" — all 404 per policy).
func (s *ItemLinkService) CheckEntityPermission(userID int, entityType string, entityID int, workspacePerm, assetPermKey string) error {
	wsID, setID, found, err := s.ResolveEntityScope(entityType, entityID)
	if err != nil {
		return err
	}
	if !found {
		return &EntityNotAccessibleError{EntityType: entityType, EntityID: entityID}
	}

	switch entityType {
	case "item", "test_case":
		if s.perm == nil {
			return &EntityNotAccessibleError{EntityType: entityType, EntityID: entityID}
		}
		hasPerm, err := s.perm.HasWorkspacePermission(userID, wsID, workspacePerm)
		if err != nil || !hasPerm {
			return &EntityNotAccessibleError{EntityType: entityType, EntityID: entityID}
		}
		return nil
	case "asset":
		if s.assetPerm == nil {
			return &EntityNotAccessibleError{EntityType: entityType, EntityID: entityID}
		}
		hasPerm, err := s.assetPerm.HasAssetSetPermission(userID, setID, assetPermKey)
		if err != nil || !hasPerm {
			return &EntityNotAccessibleError{EntityType: entityType, EntityID: entityID}
		}
		return nil
	case "page":
		if s.pagePerm == nil {
			return &EntityNotAccessibleError{EntityType: entityType, EntityID: entityID}
		}
		op := pageOpForWorkspacePerm(workspacePerm)
		hasPerm, err := s.pagePerm.Can(userID, wsID, entityID, op)
		if err != nil || !hasPerm {
			return &EntityNotAccessibleError{EntityType: entityType, EntityID: entityID}
		}
		return nil
	}
	return ErrLinkInvalidEntityType
}

// linkEndpointsShareWorkspace verifies both ends of a link are scoped to
// the same workspace. Only relevant for item↔page links.
func (s *ItemLinkService) linkEndpointsShareWorkspace(srcType string, srcID int, tgtType string, tgtID int) (bool, error) {
	srcWs, _, srcFound, err := s.ResolveEntityScope(srcType, srcID)
	if err != nil {
		return false, err
	}
	if !srcFound {
		return false, nil
	}
	tgtWs, _, tgtFound, err := s.ResolveEntityScope(tgtType, tgtID)
	if err != nil {
		return false, err
	}
	if !tgtFound {
		return false, nil
	}
	return srcWs == tgtWs, nil
}

// AccessibleWorkspaceIDs returns workspace IDs userID can view.
func (s *ItemLinkService) AccessibleWorkspaceIDs(userID int) map[int]bool {
	out := map[int]bool{}
	if s.perm == nil {
		return out
	}
	ids, err := repository.NewWorkspaceRepository(s.db).ListActiveIDs()
	if err != nil {
		return out
	}
	for _, id := range ids {
		hasView, err := s.perm.HasWorkspacePermission(userID, id, models.PermissionItemView)
		if err != nil {
			slog.Error("link orchestration: error checking workspace view permission", slog.Int("workspace_id", id), slog.Any("error", err))
			continue
		}
		if hasView {
			out[id] = true
		}
	}
	return out
}

// AccessibleWorkspaceKeys returns the set of workspace keys userID can
// view (used as the fast path for item-endpoint visibility filtering).
func (s *ItemLinkService) AccessibleWorkspaceKeys(userID int) map[string]bool {
	out := map[string]bool{}
	if s.perm == nil {
		return out
	}
	pairs, err := repository.NewWorkspaceRepository(s.db).ListActiveIDKeys()
	if err != nil {
		return out
	}
	for _, p := range pairs {
		hasView, err := s.perm.HasWorkspacePermission(userID, p.ID, models.PermissionItemView)
		if err != nil {
			continue
		}
		if hasView {
			out[p.Key] = true
		}
	}
	return out
}

// AccessibleAssetSetIDs returns the set of asset-set IDs userID can view.
// Iterates every set and asks the asset checker (matches the pattern
// AssetHandler.canAccessEntity uses). Empty when no asset checker.
func (s *ItemLinkService) AccessibleAssetSetIDs(userID int) map[int]bool {
	out := map[int]bool{}
	if s.assetPerm == nil {
		return out
	}
	rows, err := s.db.Query("SELECT id FROM asset_management_sets")
	if err != nil {
		return out
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ok, err := s.assetPerm.HasAssetSetPermission(userID, id, AssetPermissionKeyView)
		if err == nil && ok {
			out[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		return map[int]bool{}
	}
	return out
}

// EndpointVisible reports whether a single link endpoint is accessible
// to a user, given pre-computed allow-sets. Items use the pre-joined
// workspace key for the cheap path.
func (s *ItemLinkService) EndpointVisible(entityType string, entityID int, workspaceKey string, accessibleKeys map[string]bool, accessibleWs, accessibleSets map[int]bool) bool {
	switch entityType {
	case "item":
		return workspaceKey == "" || accessibleKeys[workspaceKey]
	case "test_case", "page":
		wsID, _, found, err := s.ResolveEntityScope(entityType, entityID)
		if err != nil || !found {
			return false
		}
		return accessibleWs[wsID]
	case "asset":
		_, setID, found, err := s.ResolveEntityScope(entityType, entityID)
		if err != nil || !found {
			return false
		}
		return accessibleSets[setID]
	}
	return false
}

// FilterLinksByAccess drops links whose endpoints are in workspaces /
// asset sets the user cannot view. Counterpart of EndpointVisible.
func (s *ItemLinkService) FilterLinksByAccess(links []models.ItemLink, accessibleKeys map[string]bool, accessibleWs, accessibleSets map[int]bool) []models.ItemLink {
	out := make([]models.ItemLink, 0, len(links))
	for _, l := range links {
		if !s.EndpointVisible(l.SourceType, l.SourceID, l.SourceWorkspaceKey, accessibleKeys, accessibleWs, accessibleSets) {
			continue
		}
		if !s.EndpointVisible(l.TargetType, l.TargetID, l.TargetWorkspaceKey, accessibleKeys, accessibleWs, accessibleSets) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// FilterPageLinksByACL drops links whose page endpoint is hidden by
// per-page ACLs. No-op when there are no page endpoints in the input.
// Fail-closed when the page checker is missing.
func (s *ItemLinkService) FilterPageLinksByACL(userID int, links []models.ItemLink) []models.ItemLink {
	if len(links) == 0 {
		return links
	}
	hasPage := false
	for _, l := range links {
		if l.SourceType == "page" || l.TargetType == "page" {
			hasPage = true
			break
		}
	}
	if !hasPage {
		return links
	}
	if s.pagePerm == nil {
		out := links[:0]
		for _, l := range links {
			if l.SourceType == "page" || l.TargetType == "page" {
				continue
			}
			out = append(out, l)
		}
		return out
	}

	// Bucket page IDs by workspace so ListVisiblePageIDs can batch.
	bucket := map[int]map[int]struct{}{}
	addPage := func(wsID *int, pageID int) {
		if wsID == nil {
			return
		}
		ids, ok := bucket[*wsID]
		if !ok {
			ids = map[int]struct{}{}
			bucket[*wsID] = ids
		}
		ids[pageID] = struct{}{}
	}
	for _, l := range links {
		if l.SourceType == "page" {
			addPage(l.SourceWorkspaceID, l.SourceID)
		}
		if l.TargetType == "page" {
			addPage(l.TargetWorkspaceID, l.TargetID)
		}
	}
	visible := map[int]map[int]bool{}
	for wsID, ids := range bucket {
		flat := make([]int, 0, len(ids))
		for id := range ids {
			flat = append(flat, id)
		}
		got, err := s.pagePerm.ListVisiblePageIDs(userID, wsID, flat)
		if err != nil {
			got = map[int]bool{}
		}
		visible[wsID] = got
	}
	pageVisible := func(wsID *int, pageID int) bool {
		if wsID == nil {
			return false
		}
		return visible[*wsID][pageID]
	}
	out := make([]models.ItemLink, 0, len(links))
	for _, l := range links {
		if l.SourceType == "page" && !pageVisible(l.SourceWorkspaceID, l.SourceID) {
			continue
		}
		if l.TargetType == "page" && !pageVisible(l.TargetWorkspaceID, l.TargetID) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// getLinksWhere is the joined SELECT used by every list endpoint —
// pulls every display field the API surfaces in one round-trip.
//
// whereClause is appended to "WHERE " and may reference il (item_links),
// lt (link_types), si/sit/sw/spw (source item / type / workspace / page-
// workspace), ti/tit/tw/tpw (target equivalents), etc.
func (s *ItemLinkService) getLinksWhere(whereClause string, args ...interface{}) ([]models.ItemLink, error) {
	query := `
		SELECT il.id, il.link_type_id, il.source_type, il.source_id, il.target_type, il.target_id,
		       il.created_by, il.created_at,
		       lt.name, lt.color, lt.forward_label, lt.reverse_label,
		       COALESCE(si.title, stc.title, sa.title, sp.title, '') as source_title,
		       COALESCE(ti.title, ttc.title, ta.title, tp.title, '') as target_title,
		       COALESCE(u.username, '') as created_by_name,
		       si.status_id as source_status_id,
		       COALESCE(ss.name, '') as source_status_name,
		       COALESCE(ssc.color, '') as source_status_color,
		       si.item_type_id as source_item_type_id,
		       COALESCE(sit.name, '') as source_item_type_name,
		       COALESCE(sit.icon, '') as source_item_type_icon,
		       COALESCE(sit.color, '') as source_item_type_color,
		       COALESCE(sw.key, spw.key, '') as source_workspace_key,
		       COALESCE(si.workspace_id, sp.workspace_id) as source_workspace_id,
		       si.workspace_item_number as source_item_number,
		       ti.status_id as target_status_id,
		       COALESCE(ts.name, '') as target_status_name,
		       COALESCE(tsc.color, '') as target_status_color,
		       ti.item_type_id as target_item_type_id,
		       COALESCE(tit.name, '') as target_item_type_name,
		       COALESCE(tit.icon, '') as target_item_type_icon,
		       COALESCE(tit.color, '') as target_item_type_color,
		       COALESCE(tw.key, tpw.key, '') as target_workspace_key,
		       COALESCE(ti.workspace_id, tp.workspace_id) as target_workspace_id,
		       ti.workspace_item_number as target_item_number,
		       il.custom_field_id,
		       COALESCE(cfd.name, '') as custom_field_name
		FROM item_links il
		JOIN link_types lt ON il.link_type_id = lt.id
		LEFT JOIN items si ON il.source_type = 'item' AND il.source_id = si.id
		LEFT JOIN test_cases stc ON il.source_type = 'test_case' AND il.source_id = stc.id
		LEFT JOIN assets sa ON il.source_type = 'asset' AND il.source_id = sa.id
		LEFT JOIN pages sp ON il.source_type = 'page' AND il.source_id = sp.id
		LEFT JOIN items ti ON il.target_type = 'item' AND il.target_id = ti.id
		LEFT JOIN test_cases ttc ON il.target_type = 'test_case' AND il.target_id = ttc.id
		LEFT JOIN assets ta ON il.target_type = 'asset' AND il.target_id = ta.id
		LEFT JOIN pages tp ON il.target_type = 'page' AND il.target_id = tp.id
		LEFT JOIN users u ON il.created_by = u.id
		LEFT JOIN statuses ss ON si.status_id = ss.id
		LEFT JOIN statuses ts ON ti.status_id = ts.id
		LEFT JOIN status_categories ssc ON ss.category_id = ssc.id
		LEFT JOIN status_categories tsc ON ts.category_id = tsc.id
		LEFT JOIN item_types sit ON si.item_type_id = sit.id
		LEFT JOIN item_types tit ON ti.item_type_id = tit.id
		LEFT JOIN workspaces sw ON si.workspace_id = sw.id
		LEFT JOIN workspaces tw ON ti.workspace_id = tw.id
		LEFT JOIN workspaces spw ON sp.workspace_id = spw.id
		LEFT JOIN workspaces tpw ON tp.workspace_id = tpw.id
		LEFT JOIN custom_field_definitions cfd ON il.custom_field_id = cfd.id
		WHERE ` + whereClause + `
		ORDER BY lt.name, il.created_at DESC
	`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var links []models.ItemLink
	for rows.Next() {
		var link models.ItemLink
		if err := rows.Scan(
			&link.ID, &link.LinkTypeID, &link.SourceType, &link.SourceID,
			&link.TargetType, &link.TargetID, &link.CreatedBy, &link.CreatedAt,
			&link.LinkTypeName, &link.LinkTypeColor, &link.LinkTypeForwardLabel, &link.LinkTypeReverseLabel,
			&link.SourceTitle, &link.TargetTitle, &link.CreatedByName,
			&link.SourceStatusID, &link.SourceStatusName, &link.SourceStatusColor,
			&link.SourceItemTypeID, &link.SourceItemTypeName, &link.SourceItemTypeIcon, &link.SourceItemTypeColor,
			&link.SourceWorkspaceKey, &link.SourceWorkspaceID, &link.SourceItemNumber,
			&link.TargetStatusID, &link.TargetStatusName, &link.TargetStatusColor,
			&link.TargetItemTypeID, &link.TargetItemTypeName, &link.TargetItemTypeIcon, &link.TargetItemTypeColor,
			&link.TargetWorkspaceKey, &link.TargetWorkspaceID, &link.TargetItemNumber,
			&link.CustomFieldID, &link.CustomFieldName,
		); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return links, nil
}

// getLinkByID returns nil when no row matches (lets callers map to a
// clean ErrLinkNotFound).
func (s *ItemLinkService) getLinkByID(id int) (*models.ItemLink, error) {
	links, err := s.getLinksWhere("il.id = ?", id)
	if err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return nil, nil
	}
	return &links[0], nil
}

// emitLinkedEvents fires the "item linked" notification + action event
// for an item-source link. Best-effort; failures here do not roll back
// the create.
func (s *ItemLinkService) emitLinkedEvents(actorUserID int, params CreateItemLinkParams, link *models.ItemLink) {
	if s.notifications == nil && s.actions == nil {
		return
	}
	sourceItem, err := repository.NewItemRepository(s.db).FindByID(params.SourceID)
	if err != nil {
		return
	}
	actorName := s.lookupActorUsername(actorUserID)
	if s.notifications != nil {
		s.notifications.EmitEvent(&NotificationEvent{
			EventType:   models.EventItemLinked,
			WorkspaceID: sourceItem.WorkspaceID,
			ActorUserID: actorUserID,
			ItemID:      params.SourceID,
			AssigneeID:  sourceItem.AssigneeID,
			CreatorID:   sourceItem.CreatorID,
			Title:       "Item Linked",
			TemplateData: map[string]interface{}{
				"item.title":   sourceItem.Title,
				"item.id":      params.SourceID,
				"target.title": link.TargetTitle,
				"target.id":    params.TargetID,
				"user.name":    actorName,
			},
		})
	}
	if s.actions != nil {
		s.actions.EmitActionEvent(&models.ActionEvent{
			EventType:   models.ActionTriggerItemLinked,
			WorkspaceID: sourceItem.WorkspaceID,
			ItemID:      params.SourceID,
			ActorUserID: actorUserID,
			NewValues: map[string]interface{}{
				"link_type_id": params.LinkTypeID,
				"target_type":  params.TargetType,
				"target_id":    params.TargetID,
			},
		})
	}
}

// emitUnlinkedEvents fires the "item unlinked" notification for an
// item-source link.
func (s *ItemLinkService) emitUnlinkedEvents(actorUserID int, link *models.ItemLink) {
	if s.notifications == nil {
		return
	}
	sourceItem, err := repository.NewItemRepository(s.db).FindByID(link.SourceID)
	if err != nil {
		return
	}
	s.notifications.EmitEvent(&NotificationEvent{
		EventType:   models.EventItemUnlinked,
		WorkspaceID: sourceItem.WorkspaceID,
		ActorUserID: actorUserID,
		ItemID:      link.SourceID,
		AssigneeID:  sourceItem.AssigneeID,
		CreatorID:   sourceItem.CreatorID,
		Title:       "Item Unlinked",
		TemplateData: map[string]interface{}{
			"item.title":   sourceItem.Title,
			"item.id":      link.SourceID,
			"target.title": link.TargetTitle,
			"target.id":    link.TargetID,
			"user.name":    s.lookupActorUsername(actorUserID),
		},
	})
}

// lookupActorUsername returns the actor's username for notification
// template data. Empty on lookup failure — the template tolerates a
// missing value and we never want to block link creation on a user-row
// fetch error.
func (s *ItemLinkService) lookupActorUsername(userID int) string {
	user, err := repository.NewUserRepository(s.db).GetByID(userID)
	if err != nil || user == nil {
		return ""
	}
	return user.Username
}

// CanonicalEntityType maps user-facing path segments ("items",
// "test-cases", "pages") to internal entity-type strings. Exposed so
// HTTP layers don't reimplement the mapping.
func CanonicalEntityType(pathSegment string) (string, bool) {
	switch strings.ToLower(pathSegment) {
	case "items", "item":
		return "item", true
	case "test-cases", "test_cases", "test_case":
		return "test_case", true
	case "pages", "page":
		return "page", true
	case "assets", "asset":
		return "asset", true
	}
	return "", false
}
