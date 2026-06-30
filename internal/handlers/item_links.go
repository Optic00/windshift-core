package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
	"windshift/internal/utils"
)

// assetPermissionChecker / pagePermissionChecker are kept as package-local
// aliases of the service-defined interfaces so the existing server.go
// wiring (which passes typed handlers / services) doesn't change shape.
type assetPermissionChecker = services.AssetPermissionChecker
type pagePermissionChecker = services.PagePermissionChecker

// ItemLinkHandler is the cookie-auth HTTP shim around services.ItemLinkService.
// All real orchestration (permission checks, cross-workspace gating, dup
// detection, list filtering, notification + action emission) lives in the
// service so the v1 bearer-auth handler can share it verbatim.
//
// What still lives here:
//   - HTTP decode / response shaping
//   - Custom-field-managed link pre-processing (UI-form specific, not on
//     the v1 surface): validateAndPrepareFieldLink + enforceSingleValueFieldLink
//   - Asset-link list + linkable-item search (separate flows the CLI doesn't
//     consume): GetLinkedAssets, SearchLinkableItems, search* helpers
type ItemLinkHandler struct {
	db                  database.Database
	permissionService   *services.PermissionService
	assetPerm           assetPermissionChecker
	notificationService notificationEmitter
	actionService       actionEmitter

	linkSvc *services.ItemLinkService
}

// notificationEmitter / actionEmitter mirror the existing handler-side
// interface contract so server.go can keep passing its concrete services.
type notificationEmitter interface {
	EmitEvent(event *services.NotificationEvent)
}
type actionEmitter interface {
	EmitActionEvent(event *models.ActionEvent)
}

func NewItemLinkHandler(db database.Database, notificationService notificationEmitter, permissionService *services.PermissionService) *ItemLinkHandler {
	svc := services.NewItemLinkService(db).WithPermissionService(permissionService)
	if notificationService != nil {
		svc = svc.WithNotificationEmitter(notificationService)
	}
	return &ItemLinkHandler{
		db:                  db,
		permissionService:   permissionService,
		notificationService: notificationService,
		linkSvc:             svc,
	}
}

// SetAssetPermissionChecker wires in a checker (typically *AssetHandler).
// Forwards to the underlying service so both the cookie path AND the v1
// bearer path get the same asset-permission gating.
func (h *ItemLinkHandler) SetAssetPermissionChecker(p assetPermissionChecker) {
	h.assetPerm = p
	h.linkSvc.WithAssetPermissionChecker(p)
}

// SetPagePermissionChecker wires in the page permission service. Same
// forwarding rationale as SetAssetPermissionChecker.
func (h *ItemLinkHandler) SetPagePermissionChecker(p pagePermissionChecker) {
	h.linkSvc.WithPagePermissionChecker(p)
}

// SetActionService wires the optional action-event emitter.
func (h *ItemLinkHandler) SetActionService(actionService actionEmitter) {
	h.actionService = actionService
	h.linkSvc.WithActionEmitter(actionService)
}

// LinkService exposes the underlying service so the v1 link handler can
// share a single fully-wired instance instead of re-wiring all the
// optional dependencies (asset checker, page checker, notification,
// action emitter). Returns nil when called before the handler is
// constructed (shouldn't happen in normal wiring).
func (h *ItemLinkHandler) LinkService() *services.ItemLinkService {
	return h.linkSvc
}

//nolint:unused // Kept for compatibility with helper tests that exercise service migration wrappers.
func (h *ItemLinkHandler) ensureLinkService() *services.ItemLinkService {
	if h.linkSvc == nil {
		h.linkSvc = services.NewItemLinkService(h.db).WithPermissionService(h.permissionService)
	}
	if h.assetPerm != nil {
		h.linkSvc.WithAssetPermissionChecker(h.assetPerm)
	}
	return h.linkSvc
}

// Compatibility wrappers for the helper tests that predate moving link
// orchestration into ItemLinkService. Keep these thin so handler code does not
// regain ownership of the SQL-heavy access logic.
//
//nolint:unused // Kept for compatibility with helper tests that predate the service migration.
func (h *ItemLinkHandler) resolveEntityScope(entityType string, entityID int) (wsID, setID int, found bool, err error) {
	return h.ensureLinkService().ResolveEntityScope(entityType, entityID)
}

//nolint:unused // Kept for compatibility with helper tests that predate the service migration.
func (h *ItemLinkHandler) endpointVisible(entityType string, entityID int, workspaceKey string, accessibleKeys map[string]bool, accessibleWs, accessibleSets map[int]bool) bool {
	return h.ensureLinkService().EndpointVisible(entityType, entityID, workspaceKey, accessibleKeys, accessibleWs, accessibleSets)
}

//nolint:unused // Kept for compatibility with helper tests that predate the service migration.
func (h *ItemLinkHandler) filterLinksByAccess(links []models.ItemLink, accessibleKeys map[string]bool, accessibleWs, accessibleSets map[int]bool) []models.ItemLink {
	return h.ensureLinkService().FilterLinksByAccess(links, accessibleKeys, accessibleWs, accessibleSets)
}

//nolint:unused // Kept for compatibility with helper tests that predate the service migration.
func (h *ItemLinkHandler) accessibleAssetSetIDSet(user *models.User) map[int]bool {
	if user == nil {
		return map[int]bool{}
	}
	return h.ensureLinkService().AccessibleAssetSetIDs(user.ID)
}

//nolint:unused // Kept for compatibility with helper tests that predate the service migration.
func (h *ItemLinkHandler) canUserViewEntity(_ int, entityType string, entityID int, accessibleWs, accessibleSets map[int]bool) bool {
	wsID, setID, found, err := h.resolveEntityScope(entityType, entityID)
	if err != nil || !found {
		return false
	}
	if entityType == "asset" {
		return accessibleSets[setID]
	}
	return accessibleWs[wsID]
}

// GetLinksForItem returns all links for the entity identified by the {id}
// path segment. The leading URL segment ("items" / "test-cases" / "pages")
// is mapped to the internal entity-type string before delegating to the
// service, which owns the permission + visibility filtering.
func (h *ItemLinkHandler) GetLinksForItem(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	internalType := "item"
	switch {
	case strings.Contains(r.URL.Path, "/test-cases/"):
		internalType = "test_case"
	case strings.Contains(r.URL.Path, "/pages/"):
		internalType = "page"
	}

	outgoing, incoming, err := h.linkSvc.ListLinksForEntityWithChecks(user.ID, internalType, id)
	if err != nil {
		respondLinkServiceError(w, r, internalType, err)
		return
	}
	respondJSONOK(w, map[string]interface{}{
		"outgoing": outgoing,
		"incoming": incoming,
	})
}

// maxBatchLinkItems caps how many item ids GetLinksForItemsBatch accepts in
// one request, bounding the IN-clause size. Callers (board / roadmap) chunk
// larger sets across multiple requests.
const maxBatchLinkItems = 500

// GetLinksForItemsBatch returns links for many items in a single request,
// keyed by item id. It backs the board/roadmap dependency-badge load, which
// would otherwise fire one GET /items/{id}/links per card — a burst that under
// HTTP/2 grabbed a DB connection per card and could exhaust the pool. Every
// requested id is present in the response (empty arrays when there are no
// visible links) so the client can cache misses without re-fetching.
func (h *ItemLinkHandler) GetLinksForItemsBatch(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	ids := parseIDListParam(r.URL.Query().Get("ids"))
	if len(ids) == 0 {
		respondJSONOK(w, map[int]services.EntityLinks{})
		return
	}
	if len(ids) > maxBatchLinkItems {
		respondBadRequest(w, r, fmt.Sprintf("too many ids (max %d per request)", maxBatchLinkItems))
		return
	}
	groups, err := h.linkSvc.ListLinksForItemsWithChecks(user.ID, ids)
	if err != nil {
		respondLinkServiceError(w, r, "item", err)
		return
	}
	respondJSONOK(w, groups)
}

// respondLinkServiceError maps the service's typed errors onto HTTP
// responses. Centralized so the v1 handler (added in
// internal/restapi/v1/handlers/links.go) can map the same set without
// duplicating the switch.
func respondLinkServiceError(w http.ResponseWriter, r *http.Request, fallbackResource string, err error) {
	switch {
	case errors.Is(err, services.ErrLinkSelfReference),
		errors.Is(err, services.ErrLinkInvalidEntityType):
		respondValidationError(w, r, err.Error())
	case errors.Is(err, services.ErrInvalidLinkTypeForEntities):
		respondValidationError(w, r, "The selected link type does not allow these entity types")
	case errors.Is(err, services.ErrLinkExists):
		respondConflict(w, r, "A link between these items already exists")
	case errors.Is(err, services.ErrLinkNotFound):
		respondNotFound(w, r, "link")
	case errors.Is(err, services.ErrLinkCrossWorkspacePage):
		respondNotFound(w, r, "page")
	case services.IsEntityNotAccessible(err):
		var nae *services.EntityNotAccessibleError
		errors.As(err, &nae)
		resource := fallbackResource
		if nae.EntityType != "" {
			resource = nae.EntityType
		}
		respondNotFound(w, r, resource)
	default:
		respondInternalError(w, r, err)
	}
}

// CreateLink creates a new link between items. Thin HTTP shim over
// services.ItemLinkService — does cookie-auth decode, custom-field-link
// pre-processing (the UI-form-only concern), then delegates the
// permission checks / duplicate detection / insert / notification +
// action emission to the service.
func (h *ItemLinkHandler) CreateLink(w http.ResponseWriter, r *http.Request) {
	link, ok := decodeJSON[models.ItemLink](w, r)
	if !ok {
		return
	}

	// Required-field validation kept HTTP-side so the user gets a
	// targeted 400 instead of a generic service error.
	if link.LinkTypeID == 0 || link.SourceType == "" || link.SourceID == 0 ||
		link.TargetType == "" || link.TargetID == 0 {
		respondValidationError(w, r, "link_type_id, source_type, source_id, target_type, and target_id are required")
		return
	}

	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// For custom-field-managed links, validate field config and apply
	// mirror-field source/target swap FIRST so permission checks below
	// run against the final source+target.
	var fieldPlan *fieldLinkPlan
	if link.CustomFieldID != nil {
		var fieldErr error
		fieldPlan, fieldErr = h.validateAndPrepareFieldLink(&link)
		if fieldErr != nil {
			respondValidationError(w, r, fieldErr.Error())
			return
		}

		// Pre-authorize on the rewritten source so an unauthorized caller
		// can't trip enforceSingleValueFieldLink's DELETE on its way to
		// rejection. The service will repeat this check inside
		// CreateLinkWithChecks — paying that small duplicate cost is
		// preferable to threading a callback through the service surface.
		if err := h.linkSvc.CheckEntityPermission(currentUser.ID, link.SourceType, link.SourceID, models.PermissionItemEdit, services.AssetPermissionKeyEdit); err != nil {
			respondLinkServiceError(w, r, link.SourceType, err)
			return
		}
		h.enforceSingleValueFieldLink(&link, fieldPlan)
	}

	created, err := h.linkSvc.CreateLinkWithChecks(currentUser.ID, services.CreateItemLinkParams{
		LinkTypeID:    link.LinkTypeID,
		SourceType:    link.SourceType,
		SourceID:      link.SourceID,
		TargetType:    link.TargetType,
		TargetID:      link.TargetID,
		CustomFieldID: link.CustomFieldID,
	})
	if err != nil {
		respondLinkServiceError(w, r, link.SourceType, err)
		return
	}
	respondJSONCreated(w, created)
}

// DeleteLink removes a link. Thin HTTP shim over the service — the
// service handles the source-edit permission check, the actual delete,
// and the "item unlinked" notification.
func (h *ItemLinkHandler) DeleteLink(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	if err := h.linkSvc.DeleteLinkWithChecks(user.ID, id); err != nil {
		respondLinkServiceError(w, r, "link", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetLinkedAssets returns all assets linked to a specific item
func (h *ItemLinkHandler) GetLinkedAssets(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	if !CheckItemPermission(w, r, repository.NewItemRepository(h.db), h.permissionService, id, models.PermissionItemView) {
		return
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	accessibleSets := h.linkSvc.AccessibleAssetSetIDs(user.ID)

	// Get assets where item is the source
	outgoingQuery := `
		SELECT a.id, a.title, COALESCE(a.description, '') AS description,
		       a.set_id, ams.name AS set_name,
		       COALESCE(at.name, '') AS type_name,
		       COALESCE(ac.name, '') AS category_name,
		       il.id AS link_id, lt.name AS link_type_name, lt.forward_label
		FROM item_links il
		JOIN assets a ON il.target_type = 'asset' AND il.target_id = a.id
		LEFT JOIN asset_management_sets ams ON a.set_id = ams.id
		LEFT JOIN asset_types at ON a.asset_type_id = at.id
		LEFT JOIN asset_categories ac ON a.category_id = ac.id
		JOIN link_types lt ON il.link_type_id = lt.id
		WHERE il.source_type = 'item' AND il.source_id = ?
		ORDER BY a.title
	`

	// Get assets where item is the target
	incomingQuery := `
		SELECT a.id, a.title, COALESCE(a.description, '') AS description,
		       a.set_id, ams.name AS set_name,
		       COALESCE(at.name, '') AS type_name,
		       COALESCE(ac.name, '') AS category_name,
		       il.id AS link_id, lt.name AS link_type_name, lt.reverse_label
		FROM item_links il
		JOIN assets a ON il.source_type = 'asset' AND il.source_id = a.id
		LEFT JOIN asset_management_sets ams ON a.set_id = ams.id
		LEFT JOIN asset_types at ON a.asset_type_id = at.id
		LEFT JOIN asset_categories ac ON a.category_id = ac.id
		JOIN link_types lt ON il.link_type_id = lt.id
		WHERE il.target_type = 'item' AND il.target_id = ?
		ORDER BY a.title
	`

	type LinkedAsset struct {
		ID           int    `json:"id"`
		Title        string `json:"title"`
		Description  string `json:"description"`
		SetID        int    `json:"set_id"`
		SetName      string `json:"set_name"`
		TypeName     string `json:"type_name"`
		CategoryName string `json:"category_name"`
		LinkID       int    `json:"link_id"`
		LinkTypeName string `json:"link_type_name"`
		LinkLabel    string `json:"link_label"`
		Direction    string `json:"direction"` // "outgoing" or "incoming"
	}

	var linkedAssets []LinkedAsset

	// Process outgoing links
	rows, err := h.db.Query(outgoingQuery, id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	for rows.Next() {
		var asset LinkedAsset
		var description, setName, typeName, categoryName, linkLabel sql.NullString
		err = rows.Scan(&asset.ID, &asset.Title, &description, &asset.SetID, &setName,
			&typeName, &categoryName, &asset.LinkID, &asset.LinkTypeName, &linkLabel)
		if err != nil {
			_ = rows.Close()
			respondInternalError(w, r, err)
			return
		}
		if !accessibleSets[asset.SetID] {
			continue
		}
		asset.Description = description.String
		asset.SetName = setName.String
		asset.TypeName = typeName.String
		asset.CategoryName = categoryName.String
		asset.LinkLabel = linkLabel.String
		asset.Direction = "outgoing"
		linkedAssets = append(linkedAssets, asset)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		respondInternalError(w, r, err)
		return
	}
	_ = rows.Close()

	// Process incoming links
	rows, err = h.db.Query(incomingQuery, id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	for rows.Next() {
		var asset LinkedAsset
		var description, setName, typeName, categoryName, linkLabel sql.NullString
		err := rows.Scan(&asset.ID, &asset.Title, &description, &asset.SetID, &setName,
			&typeName, &categoryName, &asset.LinkID, &asset.LinkTypeName, &linkLabel)
		if err != nil {
			_ = rows.Close()
			respondInternalError(w, r, err)
			return
		}
		if !accessibleSets[asset.SetID] {
			continue
		}
		asset.Description = description.String
		asset.SetName = setName.String
		asset.TypeName = typeName.String
		asset.CategoryName = categoryName.String
		asset.LinkLabel = linkLabel.String
		asset.Direction = "incoming"
		linkedAssets = append(linkedAssets, asset)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		respondInternalError(w, r, err)
		return
	}
	_ = rows.Close()

	respondJSONOK(w, linkedAssets)
}

// SearchLinkableItems searches for items that can be linked
func (h *ItemLinkHandler) SearchLinkableItems(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	accessibleWorkspaceIDs, err := GetAccessibleWorkspaceIDs(user, h.db, h.permissionService)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	query := r.URL.Query().Get("q")
	itemType := r.URL.Query().Get("type") // "item", "test_case", "asset", or empty for all
	limit := 20

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	// Parse optional item_type_ids filter
	var itemTypeIDFilter []int
	if itemTypeIDsStr := r.URL.Query().Get("item_type_ids"); itemTypeIDsStr != "" {
		for _, idStr := range strings.Split(itemTypeIDsStr, ",") {
			if id, err := strconv.Atoi(strings.TrimSpace(idStr)); err == nil && id > 0 {
				itemTypeIDFilter = append(itemTypeIDFilter, id)
			}
		}
	}

	var items []models.LinkableItem

	// Search work items
	if itemType == "" || itemType == "item" {
		workItems, err := h.searchWorkItems(query, limit, accessibleWorkspaceIDs, itemTypeIDFilter)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		items = append(items, workItems...)
	}

	// Search test cases
	if itemType == "" || itemType == "test_case" {
		testCases, err := h.searchTestCases(query, limit, accessibleWorkspaceIDs)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		items = append(items, testCases...)
	}

	// Search assets
	if itemType == "" || itemType == "asset" {
		assets, err := h.searchAssets(user, query, limit)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		items = append(items, assets...)
	}

	respondJSONOK(w, items)
}

// Helper functions

func (h *ItemLinkHandler) searchWorkItems(query string, limit int, accessibleWorkspaceIDs []int, itemTypeIDs ...[]int) ([]models.LinkableItem, error) {
	var typeIDs []int
	if len(itemTypeIDs) > 0 {
		typeIDs = itemTypeIDs[0]
	}
	return repository.NewItemRepository(h.db).SearchLinkableItems(query, accessibleWorkspaceIDs, typeIDs, limit)
}

func (h *ItemLinkHandler) searchTestCases(query string, limit int, accessibleWorkspaceIDs []int) ([]models.LinkableItem, error) {
	if len(accessibleWorkspaceIDs) == 0 {
		return []models.LinkableItem{}, nil
	}

	placeholders, wsArgs := BuildWorkspaceIDPlaceholders(accessibleWorkspaceIDs)
	sqlQuery := fmt.Sprintf(`
		SELECT id, title, COALESCE(preconditions, '') AS summary
		FROM test_cases
		WHERE (title LIKE ? OR preconditions LIKE ?)
		  AND workspace_id IN (%s)
		ORDER BY title
		LIMIT ?
	`, placeholders)

	searchTerm := "%" + query + "%"
	args := make([]interface{}, 0, 3+len(wsArgs))
	args = append(args, searchTerm, searchTerm)
	args = append(args, wsArgs...)
	args = append(args, limit)
	rows, err := h.db.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []models.LinkableItem
	for rows.Next() {
		var item models.LinkableItem
		var summary sql.NullString

		err := rows.Scan(&item.ID, &item.Title, &summary)
		if err != nil {
			return nil, err
		}

		item.Description = summary.String
		item.Type = "test_case"
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return items, nil
}

func (h *ItemLinkHandler) searchAssets(user *models.User, query string, limit int) ([]models.LinkableItem, error) {
	// Fail-closed: unauthenticated request or missing asset checker gets nothing.
	if user == nil || h.assetPerm == nil {
		return []models.LinkableItem{}, nil
	}

	accessibleSets := h.linkSvc.AccessibleAssetSetIDs(user.ID)
	if len(accessibleSets) == 0 {
		return []models.LinkableItem{}, nil
	}
	setIDs := make([]int, 0, len(accessibleSets))
	for id := range accessibleSets {
		setIDs = append(setIDs, id)
	}
	setPlaceholders := make([]string, len(setIDs))
	setArgs := make([]interface{}, len(setIDs))
	for i, id := range setIDs {
		setPlaceholders[i] = "?"
		setArgs[i] = id
	}

	sqlQuery := fmt.Sprintf(`
		SELECT a.id, a.title, COALESCE(a.description, '') AS description,
		       a.set_id, ams.name AS set_name,
		       COALESCE(at.name, '') AS type_name,
		       COALESCE(ac.name, '') AS category_name
		FROM assets a
		LEFT JOIN asset_management_sets ams ON a.set_id = ams.id
		LEFT JOIN asset_types at ON a.asset_type_id = at.id
		LEFT JOIN asset_categories ac ON a.category_id = ac.id
		WHERE (a.title LIKE ? OR a.description LIKE ?)
		  AND a.set_id IN (%s)
		ORDER BY a.title
		LIMIT ?
	`, strings.Join(setPlaceholders, ","))

	searchTerm := "%" + query + "%"
	args := make([]interface{}, 0, 3+len(setArgs))
	args = append(args, searchTerm, searchTerm)
	args = append(args, setArgs...)
	args = append(args, limit)
	rows, err := h.db.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []models.LinkableItem
	for rows.Next() {
		var item models.LinkableItem
		var description, setName, typeName, categoryName sql.NullString
		var setID sql.NullInt64

		err := rows.Scan(&item.ID, &item.Title, &description, &setID, &setName, &typeName, &categoryName)
		if err != nil {
			return nil, err
		}

		item.Description = description.String
		item.AssetSetID = utils.NullInt64ToPtr(setID)
		item.AssetSetName = setName.String
		item.AssetTypeName = typeName.String
		item.AssetCategoryName = categoryName.String

		item.Type = "asset"
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return items, nil
}

// GetFieldLinks returns links managed by a specific custom field for a given item
func (h *ItemLinkHandler) GetFieldLinks(w http.ResponseWriter, r *http.Request) {
	itemID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	fieldID, ok := requireIDParam(w, r, "fieldId")
	if !ok {
		return
	}

	var err error

	if !CheckItemPermission(w, r, repository.NewItemRepository(h.db), h.permissionService, itemID, models.PermissionItemView) {
		return
	}

	// Get field options to determine if this is a primary or mirror field
	var optionsJSON sql.NullString
	var fieldType string
	err = h.db.QueryRow("SELECT field_type, options FROM custom_field_definitions WHERE id = ?", fieldID).Scan(&fieldType, &optionsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		respondNotFound(w, r, "custom_field")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if fieldType != "linking" {
		respondValidationError(w, r, "Field is not a linking type")
		return
	}

	var opts struct {
		MirrorOfFieldID int `json:"mirror_of_field_id"`
	}
	if optionsJSON.Valid {
		_ = json.Unmarshal([]byte(optionsJSON.String), &opts)
	}

	// Resolve mirror fields to the primary field id; the service stores
	// links under the primary id with item as target.
	primaryFieldID, mirror := fieldID, false
	if opts.MirrorOfFieldID > 0 {
		primaryFieldID = opts.MirrorOfFieldID
		mirror = true
	}
	links, err := h.linkSvc.ListLinksByCustomField(primaryFieldID, itemID, mirror)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Filter by what the user can view — covers items (via workspace keys),
	// test_cases (workspace lookup), and assets (set lookup).
	user := utils.GetCurrentUser(r)
	if user != nil {
		accessibleKeys, err := GetAccessibleWorkspaceKeys(user, h.db, h.permissionService)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		accessibleWsIDs := h.linkSvc.AccessibleWorkspaceIDs(user.ID)
		accessibleSetIDs := h.linkSvc.AccessibleAssetSetIDs(user.ID)
		links = h.linkSvc.FilterLinksByAccess(links, accessibleKeys, accessibleWsIDs, accessibleSetIDs)
	}

	respondJSONOK(w, links)
}

// fieldLinkPlan captures the result of validateAndPrepareFieldLink so the
// caller can carry the "should enforce single-value" bit past the permission
// check and into enforceSingleValueFieldLink without re-reading field options.
type fieldLinkPlan struct {
	multi bool
}

// validateAndPrepareFieldLink validates custom field linking constraints and
// rewrites the link in place: mirror fields are resolved to their primary and
// source/target are swapped. It performs NO destructive writes — the caller
// must invoke enforceSingleValueFieldLink AFTER permission checks succeed, so
// an unauthorized request can't wipe existing field links on its way to
// being rejected.
func (h *ItemLinkHandler) validateAndPrepareFieldLink(link *models.ItemLink) (*fieldLinkPlan, error) {
	fieldID := *link.CustomFieldID

	// Get field definition
	var optionsJSON sql.NullString
	var fieldType string
	err := h.db.QueryRow("SELECT field_type, options FROM custom_field_definitions WHERE id = ?", fieldID).Scan(&fieldType, &optionsJSON)
	if err != nil {
		return nil, fmt.Errorf("custom field not found")
	}
	if fieldType != "linking" {
		return nil, fmt.Errorf("field is not a linking type")
	}

	if !optionsJSON.Valid {
		return nil, fmt.Errorf("field has no options configured")
	}

	var opts struct {
		LinkTypeID         int      `json:"link_type_id"`
		AllowedItemTypeIDs []int    `json:"allowed_item_type_ids"`
		AllowedEntityTypes []string `json:"allowed_entity_types"`
		Multi              bool     `json:"multi"`
		MirrorOfFieldID    int      `json:"mirror_of_field_id"`
		MirrorFieldID      int      `json:"mirror_field_id"`
	}
	if err := json.Unmarshal([]byte(optionsJSON.String), &opts); err != nil {
		return nil, fmt.Errorf("invalid field options")
	}

	isMirror := opts.MirrorOfFieldID > 0

	if isMirror {
		// Mirror field: resolve to primary field, swap source/target
		link.SourceType, link.TargetType = link.TargetType, link.SourceType
		link.SourceID, link.TargetID = link.TargetID, link.SourceID
		primaryID := opts.MirrorOfFieldID
		link.CustomFieldID = &primaryID

		// Get primary field options for validation
		var primaryOptsJSON sql.NullString
		if err := h.db.QueryRow("SELECT options FROM custom_field_definitions WHERE id = ?", primaryID).Scan(&primaryOptsJSON); err != nil {
			return nil, fmt.Errorf("primary field not found")
		}
		if primaryOptsJSON.Valid {
			_ = json.Unmarshal([]byte(primaryOptsJSON.String), &opts)
		}
	}

	// Validate link type matches
	if link.LinkTypeID != 0 && link.LinkTypeID != opts.LinkTypeID {
		return nil, fmt.Errorf("link type does not match field configuration")
	}
	link.LinkTypeID = opts.LinkTypeID

	// Validate target entity type
	if len(opts.AllowedEntityTypes) > 0 {
		allowed := false
		for _, et := range opts.AllowedEntityTypes {
			if et == link.TargetType {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("target entity type not allowed for this field")
		}
	}

	// Validate target item type
	if len(opts.AllowedItemTypeIDs) > 0 && link.TargetType == "item" {
		target, err := repository.NewItemRepository(h.db).FindByID(link.TargetID)
		if err == nil && target.ItemTypeID != nil {
			allowed := false
			for _, id := range opts.AllowedItemTypeIDs {
				if id == *target.ItemTypeID {
					allowed = true
					break
				}
			}
			if !allowed {
				return nil, fmt.Errorf("target item type not allowed for this field")
			}
		}
	}

	return &fieldLinkPlan{multi: opts.Multi}, nil
}

// enforceSingleValueFieldLink deletes any existing links for the field on the
// same source so the caller's INSERT becomes the sole value. Called only
// after permission checks have already authorized the caller to modify the
// field's source.
func (h *ItemLinkHandler) enforceSingleValueFieldLink(link *models.ItemLink, plan *fieldLinkPlan) {
	if plan == nil || plan.multi || link.CustomFieldID == nil {
		return
	}
	_, _ = h.db.ExecWrite("DELETE FROM item_links WHERE custom_field_id = ? AND source_type = ? AND source_id = ?",
		*link.CustomFieldID, link.SourceType, link.SourceID)
}
