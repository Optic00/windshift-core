package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"windshift/internal/authz"
	"windshift/internal/database"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"
	"windshift/internal/validation"
	"windshift/internal/webhook"
)

type ItemHandler struct {
	db                  database.Database
	hierarchyService    *services.HierarchyService
	permissionService   *services.PermissionService
	authz               *authz.Authz
	itemCache           *services.ItemCacheService
	activityTracker     *services.ActivityTracker
	idResolver          *services.IDResolverService
	itemCRUD            *services.ItemCRUDService
	mentionService      *services.MentionService // Mention service for processing @mentions (optional, can be nil)
	notificationService interface {
		EmitEvent(event *services.NotificationEvent)
	} // Notification service for async notification processing (optional, can be nil)
	actionService interface {
		EmitActionEvent(event *models.ActionEvent)
	} // Action service for automation workflows (optional, can be nil)
	webhookSender    *webhook.WebhookSender     // Webhook sender for dispatching webhook events (optional, can be nil)
	eventCoordinator *services.EventCoordinator // Centralized event coordinator for side effects (optional, can be nil)
	issueSyncService interface {
		PushStatusToGitHub(ctx context.Context, itemID int, newStatusID int)
	} // Issue sync service for pushing status changes to GitHub (optional, can be nil)
	conditionService *services.ConditionService // Condition service for workflow transition conditions (optional, can be nil)
	approvalService  *services.ApprovalService  // Approval service for status-bound approvals (optional, can be nil)
	sseHub           *services.SSEHub           // Hub for the item event stream (optional, can be nil → /events returns 503)
}

func NewItemHandler(db database.Database, permissionService *services.PermissionService, activityTracker *services.ActivityTracker, notificationService interface {
	EmitEvent(event *services.NotificationEvent)
}) *ItemHandler {
	// Initialize item cache service
	itemCache, err := services.NewItemCacheService(db, services.DefaultItemCacheConfig())
	if err != nil {
		slog.Warn("failed to initialize item cache, continuing without cache", slog.Any("error", err))
		// Continue without cache, will fall back to direct queries
		itemCache = nil
	}

	return &ItemHandler{
		db:                  db,
		hierarchyService:    services.NewHierarchyService(db),
		permissionService:   permissionService,
		authz:               authz.New(db, permissionService),
		itemCache:           itemCache,
		activityTracker:     activityTracker,
		idResolver:          services.NewIDResolverService(db),
		itemCRUD:            services.NewItemCRUDService(db),
		notificationService: notificationService,
	}
}

// SetWebhookSender sets the webhook sender for dispatching webhook events
func (h *ItemHandler) SetWebhookSender(sender *webhook.WebhookSender) {
	h.webhookSender = sender
}

// SetMentionService sets the mention service for processing @mentions
func (h *ItemHandler) SetMentionService(mentionService *services.MentionService) {
	h.mentionService = mentionService
}

// SetActionService sets the action service for automation workflows
func (h *ItemHandler) SetActionService(actionService interface {
	EmitActionEvent(event *models.ActionEvent)
}) {
	h.actionService = actionService
}

// SetEventCoordinator sets the event coordinator for centralized side effects
func (h *ItemHandler) SetEventCoordinator(ec *services.EventCoordinator) {
	h.eventCoordinator = ec
}

// SetIssueSyncService sets the issue sync service for pushing status changes to GitHub
func (h *ItemHandler) SetIssueSyncService(svc interface {
	PushStatusToGitHub(ctx context.Context, itemID int, newStatusID int)
}) {
	h.issueSyncService = svc
}

// SetConditionService sets the condition service for workflow transition conditions
func (h *ItemHandler) SetConditionService(cs *services.ConditionService) {
	h.conditionService = cs
}

// parseIDListParam parses a comma-separated list of integer IDs from a
// query parameter. Empty/non-numeric tokens are silently dropped — a
// zero-length result means "no usable filter values supplied".
func parseIDListParam(raw string) []int {
	parts := strings.Split(raw, ",")
	ids := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if id, err := strconv.Atoi(p); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// itemCreateRequest is the legacy web/API create payload. Keep request-only
// fields here instead of adding them to models.Item, whose milestone
// representation on reads is the joined Milestones slice.
type itemCreateRequest struct {
	WorkspaceID       int                    `json:"workspace_id"`
	Title             string                 `json:"title"`
	Description       string                 `json:"description"`
	StatusID          *int                   `json:"status_id,omitempty"`
	PriorityID        *int                   `json:"priority_id,omitempty"`
	ItemTypeID        *int                   `json:"item_type_id,omitempty"`
	DueDate           *time.Time             `json:"due_date,omitempty"`
	StartDate         *time.Time             `json:"start_date,omitempty"`
	EndDate           *time.Time             `json:"end_date,omitempty"`
	IsTask            bool                   `json:"is_task"`
	IterationID       *int                   `json:"iteration_id,omitempty"`
	ProjectID         *int                   `json:"project_id,omitempty"`
	InheritProject    bool                   `json:"inherit_project"`
	TimeProjectID     *int                   `json:"time_project_id,omitempty"`
	AssigneeID        *int                   `json:"assignee_id,omitempty"`
	ParentID          *int                   `json:"parent_id"`
	RelatedWorkItemID *int                   `json:"related_work_item_id,omitempty"`
	StoryPoints       *float64               `json:"story_points,omitempty"`
	EstimateMinutes   *int                   `json:"estimate_minutes,omitempty"`
	CustomFieldValues map[string]interface{} `json:"custom_field_values,omitempty"`
	MilestoneIDs      []int                  `json:"milestone_ids,omitempty"`
}

// SetApprovalService wires the approval service so status-bound approvals gate
// transitions through this handler.
func (h *ItemHandler) SetApprovalService(ap *services.ApprovalService) {
	h.approvalService = ap
}

func (h *ItemHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	// Get user from context
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Get accessible workspace IDs (includes active workspaces and inactive ones where user has admin access)
	accessibleWorkspaceIDs, err := h.getAccessibleWorkspaceIDs(user)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// If user has no accessible workspaces, return empty list
	if len(accessibleWorkspaceIDs) == 0 {
		respondJSONOK(w, map[string]interface{}{
			"items":       []models.Item{},
			"total_count": 0,
			"page":        1,
			"limit":       50,
		})
		return
	}

	// Parse pagination parameters
	page := 1
	limit := 50
	maxLimit := 1000

	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		var p int
		if p, err = strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		var l int
		if l, err = strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
			if limit > maxLimit {
				limit = maxLimit
			}
		}
	}

	offset := (page - 1) * limit

	// Build filters from query parameters
	var filters services.ItemFilters
	qlQuery := r.URL.Query().Get("ql")
	var collectionID int
	var workspaceID int

	// Resolve collection_id
	if qlQuery == "" {
		if collectionParam := r.URL.Query().Get("collection_id"); collectionParam != "" {
			cid, err := strconv.Atoi(collectionParam)
			if err != nil {
				respondValidationError(w, r, "Invalid collection_id parameter")
				return
			}
			collectionID = cid
		}
	}

	// Apply workspace_id only when no collection_id was provided
	if collectionID == 0 {
		if wsParam := r.URL.Query().Get("workspace_id"); wsParam != "" {
			wsID, err := strconv.Atoi(wsParam)
			if err != nil {
				respondValidationError(w, r, "Invalid workspace_id parameter")
				return
			}
			workspaceID = wsID
		}
	}

	// When no QL query, apply individual filters
	if qlQuery == "" && collectionID == 0 {
		if status := r.URL.Query().Get("status"); status != "" {
			statusID, err := strconv.Atoi(status)
			if err == nil {
				filters.StatusID = &statusID
			}
		}

		if priorityParam := r.URL.Query().Get("priority_id"); priorityParam != "" {
			priorityID, err := strconv.Atoi(priorityParam)
			if err == nil {
				filters.PriorityID = &priorityID
			}
		}

		if assigneeParam := r.URL.Query().Get("assignee_id"); assigneeParam != "" {
			assigneeID, err := strconv.Atoi(assigneeParam)
			if err == nil {
				filters.AssigneeID = &assigneeID
			}
		}

		// Hierarchy filters
		if parentID := r.URL.Query().Get("parent_id"); parentID != "" {
			if parentID == "null" || parentID == "0" {
				zero := 0
				filters.ParentID = &zero
				filters.ParentIDIsSet = true
			} else {
				pid, err := strconv.Atoi(parentID)
				if err == nil {
					filters.ParentID = &pid
					filters.ParentIDIsSet = true
				}
			}
		}

		if level := r.URL.Query().Get("level"); level != "" {
			levelInt, err := strconv.Atoi(level)
			if err != nil {
				respondValidationError(w, r, "Invalid level parameter: must be an integer")
				return
			}
			filters.Level = &levelInt
		}

		if maxLevel := r.URL.Query().Get("max_level"); maxLevel != "" {
			maxLevelInt, err := strconv.Atoi(maxLevel)
			if err != nil {
				respondValidationError(w, r, "Invalid max_level parameter: must be an integer")
				return
			}
			filters.MaxLevel = &maxLevelInt
		}

		if createdSince := r.URL.Query().Get("created_since"); createdSince != "" {
			filters.CreatedSince = &createdSince
		}
	}

	// Cap completed items to those finished since this date (ISO YYYY-MM-DD or
	// RFC3339). Only items in a completed status are constrained; everything
	// else passes. Applies to both QL and non-QL queries.
	if completedSince := r.URL.Query().Get("completed_since"); completedSince != "" {
		filters.CompletedSince = &completedSince
	}

	// ID filter (applies to both QL and non-QL queries)
	if idParam := r.URL.Query().Get("id"); idParam != "" {
		itemID, err := strconv.Atoi(idParam)
		if err == nil {
			filters.ItemID = &itemID
		}
	}

	// Multi-status include/exclude filters (apply to both QL and non-QL
	// queries — the board uses these to page non-completed columns
	// separately from the capped rightmost column).
	if raw := r.URL.Query().Get("status_id"); raw != "" {
		filters.StatusIDs = parseIDListParam(raw)
	}
	if raw := r.URL.Query().Get("status_id_not"); raw != "" {
		filters.StatusIDsNot = parseIDListParam(raw)
	}

	// Sub-filter QL (ANDed with collection/direct QL)
	subQLQuery := r.URL.Query().Get("sub_ql")

	// Determine sort order
	sortBy := r.URL.Query().Get("order_by")
	sortAsc := strings.EqualFold(r.URL.Query().Get("sort_direction"), "asc")

	// Call service
	items, totalCount, err := h.itemCRUD.ListWithQL(services.ListWithQLParams{
		WorkspaceID:  workspaceID,
		CollectionID: collectionID,
		QLQuery:      qlQuery,
		SubQLQuery:   subQLQuery,
		WorkspaceIDs: accessibleWorkspaceIDs,
		UserID:       user.ID,
		Filters:      filters,
		Pagination: services.PaginationParams{
			Limit:  limit,
			Offset: offset,
		},
		SortBy:  sortBy,
		SortAsc: sortAsc,
	})
	if err != nil {
		// Check for QL-specific errors to return as validation errors
		if strings.Contains(err.Error(), "QL query error:") {
			respondValidationError(w, r, err.Error())
			return
		}
		if strings.Contains(err.Error(), "collection not found") {
			respondNotFound(w, r, "collection")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	// Filter items based on user permissions
	filteredItems, err := h.filterItemsByPermissions(user.ID, items)
	if err != nil {
		slog.Error("permission check failed", slog.Int("user_id", user.ID), slog.String("operation", "GetAll"), slog.Any("error", err))
		respondInternalError(w, r, err)
		return
	}
	items = filteredItems

	// Strip names of time projects the viewer can't access (keeps the IDs).
	h.maskInaccessibleProjectNames(user.ID, items)

	// Load labels for items
	if err := repository.NewLabelRepository(h.db).LoadForItems(items); err != nil {
		slog.Warn("failed to load labels for items", slog.Any("error", err))
	}
	if err := LoadPersonalLabelsForItems(h.db, items, user.ID); err != nil {
		slog.Warn("failed to load personal labels for items", slog.Any("error", err))
	}
	if err := repository.NewMilestoneAttachRepository(h.db).LoadForItems(items); err != nil {
		slog.Warn("failed to load milestones for items", slog.Any("error", err))
	}

	// Compute sortable fields: system fields for the workspace
	sortableFields := repository.SystemSortableFieldKeys()

	// Create paginated response
	response := models.PaginatedItemsResponse{
		Items: items,
		Pagination: models.PaginationMeta{
			Page:       page,
			Limit:      limit,
			Total:      totalCount,
			TotalPages: (totalCount + limit - 1) / limit,
		},
		SortableFields: sortableFields,
	}

	respondJSONOK(w, response)
}

func (h *ItemHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	h.respondItemByID(w, r, user, id)
}

// GetByKeyAndNumber resolves a stable display key reference, e.g.
// /api/workspaces/WI/items/123, then returns the same item shape as Get.
// This lets SPA deep links support both numeric IDs and workspace-key/item-number keys.
func (h *ItemHandler) GetByKeyAndNumber(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.PathValue("key"))
	if key == "" {
		respondValidationError(w, r, "workspace key is required")
		return
	}
	itemRef := strings.TrimSpace(r.PathValue("number"))
	if itemRef == "" {
		respondValidationError(w, r, "item number is required")
		return
	}

	lookupKey := key
	lookupNumber := 0
	if parts := strings.SplitN(itemRef, "-", 2); len(parts) == 2 {
		embeddedKey := strings.TrimSpace(parts[0])
		if embeddedKey == "" {
			respondValidationError(w, r, "invalid item key")
			return
		}
		// Allow /workspaces/1/items/WI-123 and /workspaces/WI/items/WI-123.
		// If the path workspace is itself a key and disagrees with the embedded
		// key, treat the item as not found rather than silently crossing workspaces.
		if _, numericPathWorkspace := strconv.Atoi(key); numericPathWorkspace != nil && !strings.EqualFold(key, embeddedKey) {
			respondNotFound(w, r, "item")
			return
		}
		lookupKey = embeddedKey
		var err error
		lookupNumber, err = strconv.Atoi(parts[1])
		if err != nil || lookupNumber <= 0 {
			respondValidationError(w, r, "invalid item key")
			return
		}
	} else {
		var err error
		lookupNumber, err = strconv.Atoi(itemRef)
		if err != nil || lookupNumber <= 0 {
			respondValidationError(w, r, "invalid item number")
			return
		}
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	id, err := repository.NewItemRepository(h.db).FindIDByKeyAndNumber(lookupKey, lookupNumber)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	h.respondItemByID(w, r, user, id)
}

func (h *ItemHandler) respondItemByID(w http.ResponseWriter, r *http.Request, user *models.User, id int) {
	// Get item with all details using service
	crudService := services.NewItemCRUDService(h.db)
	result, err := crudService.GetByIDWithWorkspaceStatus(id)
	if err != nil {
		if err == repository.ErrNotFound {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}
	item := result.Item

	// Check if user has permission to view this item. Active approvers without
	// workspace item.view are allowed through here so the approval inbox →
	// item navigation works; see CheckItemPermissionAsActor for the model.
	canView, err := h.canViewItemAsActor(user.ID, item.ID, item.WorkspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canView {
		respondNotFound(w, r, "Item")
		return
	}

	// Check if workspace is inactive and user has permission to access it
	if !result.WorkspaceActive {
		canAccess, err := h.canAccessInactiveWorkspace(user, item.WorkspaceID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if !canAccess {
			respondNotFound(w, r, "Item")
			return
		}
	}

	// Get effective project from cache
	if h.itemCache != nil {
		effectiveProjectID, projectInheritanceMode, err := h.itemCache.GetEffectiveProjectForItem(id, item.WorkspaceID)
		if err == nil && effectiveProjectID != nil {
			item.EffectiveProjectID = effectiveProjectID
			item.ProjectInheritanceMode = projectInheritanceMode
			var epName sql.NullString
			if err := h.db.QueryRow("SELECT name FROM time_projects WHERE id = ?", *effectiveProjectID).Scan(&epName); err != nil && !errors.Is(err, sql.ErrNoRows) {
				slog.Warn("failed to load effective project name", slog.Int("project_id", *effectiveProjectID), slog.Any("error", err))
			}
			item.EffectiveProjectName = epName.String
		}
	}

	// Load labels for item
	singleItems := []models.Item{*item}
	if err := repository.NewLabelRepository(h.db).LoadForItems(singleItems); err != nil {
		slog.Warn("failed to load labels for item", slog.Any("error", err))
	}
	if err := LoadPersonalLabelsForItems(h.db, singleItems, user.ID); err != nil {
		slog.Warn("failed to load personal labels for item", slog.Any("error", err))
	}
	if err := repository.NewMilestoneAttachRepository(h.db).LoadForItems(singleItems); err != nil {
		slog.Warn("failed to load milestones for item", slog.Any("error", err))
	}
	*item = singleItems[0]
	*item = singleItems[0]

	// Track item view activity
	if h.activityTracker != nil {
		if err := h.activityTracker.TrackItemActivity(user.ID, item.ID, services.ActivityView); err != nil {
			slog.Warn("failed to track item view activity", slog.Int("user_id", user.ID), slog.Int("item_id", item.ID), slog.Any("error", err))
		}
	}

	// Strip names of time projects the viewer has no access to (incl. the
	// cross-workspace inherited effective project), keeping the IDs.
	masked := []models.Item{*item}
	h.maskInaccessibleProjectNames(user.ID, masked)
	*item = masked[0]

	respondJSONOK(w, item)
}

// maxBatchItems caps how many item ids GetBatch accepts in one request,
// bounding the IN-clause size. The frontend chunks larger sets across multiple
// requests. Mirrors maxBatchLinkItems on the links batch endpoint.
const maxBatchItems = 500

// GetBatch returns full item-detail objects for a set of ids in a single
// request. It backs api.items.getMany(), which would otherwise fire one
// GET /items/{id} per id — a fan-out that under HTTP/2 grabbed a DB connection
// per item and could exhaust the pool during a collection delta refresh.
// Items the caller cannot view (or that no longer exist) are silently omitted;
// the consumer patches loaded rows by id and no-ops on the rest, so the
// 404-no-leak contract is preserved without per-id error signaling.
func (h *ItemHandler) GetBatch(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	ids := parseIDListParam(r.URL.Query().Get("ids"))
	if len(ids) == 0 {
		respondJSONOK(w, []models.Item{})
		return
	}
	if len(ids) > maxBatchItems {
		respondBadRequest(w, r, fmt.Sprintf("too many ids (max %d per request)", maxBatchItems))
		return
	}

	loaded, err := repository.NewItemRepository(h.db).FindByIDsWithDetails(ids)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	items := make([]models.Item, len(loaded))
	for i, it := range loaded {
		items[i] = *it
	}

	// Drop items the user can't view (workspace-memoized), matching GetAll.
	items, err = h.filterItemsByPermissions(user.ID, items)
	if err != nil {
		slog.Error("permission check failed", slog.Int("user_id", user.ID), slog.String("operation", "GetBatch"), slog.Any("error", err))
		respondInternalError(w, r, err)
		return
	}

	// Strip names of time projects the viewer can't access (keeps the IDs).
	h.maskInaccessibleProjectNames(user.ID, items)

	// Enrich with labels / personal labels (milestones are already attached by
	// the repository's batched loader).
	if err := repository.NewLabelRepository(h.db).LoadForItems(items); err != nil {
		slog.Warn("failed to load labels for batch items", slog.Any("error", err))
	}
	if err := LoadPersonalLabelsForItems(h.db, items, user.ID); err != nil {
		slog.Warn("failed to load personal labels for batch items", slog.Any("error", err))
	}

	respondJSONOK(w, items)
}

func (h *ItemHandler) Create(w http.ResponseWriter, r *http.Request) {
	slog.Debug("item create request received")
	createStart := time.Now()

	item, ok := decodeJSON[itemCreateRequest](w, r)
	if !ok {
		return
	}
	slog.Debug("item decoded", slog.Int("workspace_id", item.WorkspaceID), slog.String("title", item.Title))

	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	slog.Debug("user authenticated", slog.String("username", user.Username))

	creatorID := user.ID

	// Check if user has permission to create items in this workspace
	slog.Debug("checking permissions")
	canEdit, err := h.canEditItem(user.ID, item.WorkspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	slog.Debug("permission check complete", slog.Bool("can_edit", canEdit))
	if !canEdit {
		respondNotFound(w, r, "Item")
		return
	}

	// Sanitize user input to prevent XSS
	item.Title = sanitize.PlainTextField.Sanitize(item.Title)
	item.Description = sanitize.RichText.Sanitize(item.Description)

	// Convert item type ID to *int for validation
	var itemTypeIDPtr *int
	if item.ItemTypeID != nil {
		itemTypeIDPtr = item.ItemTypeID
	}

	// Convert parent ID to *int for validation
	var parentIDPtr *int
	if item.ParentID != nil {
		parentIDPtr = item.ParentID
	}

	// Convert related work item ID to *int
	var relatedWorkItemIDPtr *int
	if item.RelatedWorkItemID != nil {
		relatedWorkItemIDPtr = item.RelatedWorkItemID
	}

	// Use centralized validation
	validationResult := services.ValidateItemCreation(h.db, services.ItemValidationParams{
		WorkspaceID:       item.WorkspaceID,
		Title:             item.Title,
		ItemTypeID:        itemTypeIDPtr,
		ParentID:          parentIDPtr,
		StatusID:          item.StatusID,
		IsTask:            item.IsTask,
		RelatedWorkItemID: relatedWorkItemIDPtr,
		UserID:            user.ID,
		PermService:       h.permissionService,
	})

	if !validationResult.Valid {
		respondValidationError(w, r, validationResult.Error)
		return
	}

	// Set default project inheritance based on parent relationship
	if item.ProjectID == nil && !item.InheritProject {
		if item.ParentID != nil && *item.ParentID != 0 {
			// Has parent: default to inherit
			item.InheritProject = true
		}
		// If no parent: leave as NULL (none) and InheritProject = false
	}

	// Normalize parent ID (nil if 0)
	if item.ParentID != nil && *item.ParentID == 0 {
		item.ParentID = nil
	}

	validationTime := time.Since(createStart)

	// Convert custom field values to JSON. Validate + dedupe option ids
	// before marshaling so the stored JSON is canonical (no duplicate
	// multiselect entries, no out-of-range ids).
	var customFieldValuesJSON string
	if item.CustomFieldValues != nil {
		if err := validation.ValidateAndNormalizeCustomFieldValues(h.db, item.CustomFieldValues); err != nil {
			respondValidationError(w, r, err.Error())
			return
		}
		var customFieldValuesBytes []byte
		customFieldValuesBytes, err = json.Marshal(item.CustomFieldValues)
		if err != nil {
			respondValidationError(w, r, "Invalid custom field values")
			return
		}
		customFieldValuesJSON = string(customFieldValuesBytes)
	}

	// Use centralized CreateItem service
	createServiceStart := time.Now()
	id, err := services.CreateItem(h.db, services.ItemCreationParams{
		WorkspaceID:           item.WorkspaceID,
		Title:                 item.Title,
		Description:           item.Description,
		StatusID:              item.StatusID,   // Direct ID (nil = use workflow initial status)
		PriorityID:            item.PriorityID, // Direct ID (nil = use default priority)
		ItemTypeID:            itemTypeIDPtr,
		IsTask:                item.IsTask,
		ParentID:              item.ParentID,
		MilestoneIDs:          item.MilestoneIDs,
		IterationID:           item.IterationID,
		ProjectID:             item.ProjectID,
		InheritProject:        item.InheritProject,
		TimeProjectID:         item.TimeProjectID,
		AssigneeID:            item.AssigneeID,
		CreatorID:             &creatorID,
		DueDate:               item.DueDate,
		StartDate:             item.StartDate,
		EndDate:               item.EndDate,
		RelatedWorkItemID:     relatedWorkItemIDPtr,
		StoryPoints:           item.StoryPoints,
		EstimateMinutes:       item.EstimateMinutes,
		CustomFieldValuesJSON: customFieldValuesJSON,
		ValidatingUserID:      user.ID,
		PermService:           h.permissionService,
	})
	if err != nil {
		if errors.Is(err, services.ErrMissingItemType) || errors.Is(err, services.ErrInvalidItemType) || errors.Is(err, services.ErrProjectNotFound) {
			respondValidationError(w, r, err.Error())
			return
		}
		slog.Error("failed to create item", slog.Any("error", err))
		respondInternalError(w, r, err)
		return
	}
	createServiceTime := time.Since(createServiceStart)

	// Profiling: post-insert query
	postQueryStart := time.Now()

	// Return the created item with all joined details. This deliberately
	// populates the response with more fields than the original reduced SELECT
	// (e.g. AssigneeName, CreatorName, IterationName) — strictly additive for
	// API consumers, and consolidates against a single repository query.
	// effective_project is NOT calculated here for performance; clients should
	// use GET /api/items/{id} if they need effective project data.
	itemRepo := repository.NewItemRepository(h.db)
	createdPtr, err := itemRepo.FindByIDWithDetails(int(id))
	selectQueryTime := time.Since(postQueryStart)
	if err != nil {
		slog.Error("failed to query created item", slog.Int64("item_id", id), slog.Any("error", err))
		respondInternalError(w, r, err)
		return
	}
	createdItem := *createdPtr

	// Emit side effects via EventCoordinator (notifications, webhooks, action events)
	notifyStart := time.Now()
	if h.eventCoordinator != nil {
		h.eventCoordinator.EmitItemCreated(&createdItem, user.ID, user.Username)
	} else {
		// Fallback to individual services if EventCoordinator not set
		if h.notificationService != nil {
			itemKey := fmt.Sprintf("%s-%d", createdItem.WorkspaceKey, createdItem.WorkspaceItemNumber)
			h.notificationService.EmitEvent(&services.NotificationEvent{
				EventType:   models.EventItemCreated,
				WorkspaceID: createdItem.WorkspaceID,
				ActorUserID: user.ID,
				ItemID:      createdItem.ID,
				AssigneeID:  createdItem.AssigneeID,
				CreatorID:   &user.ID,
				Title:       "New Item Created",
				TemplateData: map[string]interface{}{
					"item.title":     createdItem.Title,
					"item.key":       itemKey,
					"item.id":        createdItem.ID,
					"user.name":      user.Username,
					"workspace.name": createdItem.WorkspaceName,
					"workspace.key":  createdItem.WorkspaceKey,
				},
			})
		}
		if h.actionService != nil {
			h.actionService.EmitActionEvent(&models.ActionEvent{
				EventType:   models.ActionTriggerItemCreated,
				WorkspaceID: createdItem.WorkspaceID,
				ItemID:      createdItem.ID,
				ActorUserID: user.ID,
				NewValues: map[string]interface{}{
					"title":        createdItem.Title,
					"status_id":    createdItem.StatusID,
					"item_type_id": createdItem.ItemTypeID,
					"assignee_id":  createdItem.AssigneeID,
					"creator_id":   createdItem.CreatorID,
					"priority_id":  createdItem.PriorityID,
				},
			})
		}
		if h.webhookSender != nil {
			go h.webhookSender.DispatchEvent("item.created", &createdItem)
		}
	}
	notifyTime := time.Since(notifyStart)

	// Profiling: log timing summary (all times in milliseconds for easy parsing)
	totalTime := time.Since(createStart)
	measuredTime := validationTime + createServiceTime + selectQueryTime + notifyTime
	gapTime := totalTime - measuredTime // Time spent in scheduler/unmeasured code
	slog.Debug("item creation performance",
		slog.Int("item_id", createdItem.ID),
		slog.Group("timings_ms",
			slog.Float64("validation", float64(validationTime.Microseconds())/1000.0),
			slog.Float64("create_service", float64(createServiceTime.Microseconds())/1000.0),
			slog.Float64("query", float64(selectQueryTime.Microseconds())/1000.0),
			slog.Float64("notify", float64(notifyTime.Microseconds())/1000.0),
			slog.Float64("gap", float64(gapTime.Microseconds())/1000.0),
			slog.Float64("total", float64(totalTime.Microseconds())/1000.0),
		))

	// Strip names of time projects the creator has no access to (incl. the
	// inherited effective project), matching the masked read paths. Mask a
	// copy so the webhook goroutine's view of createdItem isn't mutated.
	maskedCreated := []models.Item{createdItem}
	h.maskInaccessibleProjectNames(user.ID, maskedCreated)

	respondJSONCreated(w, maskedCreated[0])
}

func (h *ItemHandler) Update(w http.ResponseWriter, r *http.Request) {
	// Parse request and validate item ID
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Parse update data from request body
	var updateData map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updateData); err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Load item to check permissions.
	loadedItem, err := repository.NewItemRepository(h.db).FindByID(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}
	workspaceID := loadedItem.WorkspaceID

	// Check if user has permission to edit items in this workspace
	canEdit, err := h.canEditItem(user.ID, workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canEdit {
		respondNotFound(w, r, "Item")
		return
	}

	// status_id must be changed via POST /items/{id}/transition so workflow +
	// condition rules are always enforced. Accepting it here would allow
	// bypassing condition-mode checks and diverges from the dedicated
	// transition flow (which also emits the correct cascade event).
	if _, hasStatus := updateData["status_id"]; hasStatus {
		respondValidationError(w, r, "status_id may not be set via item update; use POST /items/{id}/transition")
		return
	}
	if _, hasItemType := updateData["item_type_id"]; hasItemType {
		respondValidationError(w, r, "item_type_id may not be set via item update; use POST /items/{id}/change-type")
		return
	}

	// Track item edit activity
	if h.activityTracker != nil {
		if err = h.activityTracker.TrackItemActivity(user.ID, id, services.ActivityEdit); err != nil {
			slog.Warn("failed to track item edit activity", slog.Int("user_id", user.ID), slog.Int("item_id", id), slog.Any("error", err))
			// Don't fail the request, just log the error
		}
	}

	// Call update service to handle all business logic
	updateService := services.NewItemUpdateService(h.db).WithPermissionService(h.permissionService)
	result, err := updateService.UpdateItem(services.UpdateItemRequest{
		ItemID:     id,
		UpdateData: updateData,
		UserID:     user.ID,
	})

	if err != nil {
		// Check if it's a validation error (anywhere in the wrap chain — the
		// update service wraps with `fmt.Errorf("validation failed: %w", err)`
		// so a bare type assertion would miss wrapped ValidationErrors and
		// surface them as 500s. Specifically affects parent_id moves between
		// hierarchy levels.)
		var valErr *validation.ValidationError
		if errors.As(err, &valErr) {
			respondValidationError(w, r, valErr.Error())
			return
		}
		// Generic error
		respondInternalError(w, r, err)
		return
	}

	// Get original and updated items for event emission
	originalItem := result.OriginalItem
	updatedItem := result.Item

	// Invalidate the cached effective project for this item and its descendants
	// when a project-affecting field changed. The cache keys only on item ID and
	// stores the resolved project, so a change to project_id / inherit_project /
	// parent_id here would otherwise leave this item and every descendant that
	// inherits from it serving a stale effective project for up to the cache TTL.
	if h.itemCache != nil && projectResolutionChanged(originalItem, updatedItem) {
		h.invalidateEffectiveProjectSubtree(updatedItem.ID)
	}

	w.Header().Set("Content-Type", "application/json")

	// Check if assignee changed (compare originalItem with updatedItem)
	assigneeChanged := false
	switch {
	case originalItem.AssigneeID == nil && updatedItem.AssigneeID != nil:
		assigneeChanged = true
	case originalItem.AssigneeID != nil && updatedItem.AssigneeID == nil:
		assigneeChanged = true
	case originalItem.AssigneeID != nil && updatedItem.AssigneeID != nil && *originalItem.AssigneeID != *updatedItem.AssigneeID:
		assigneeChanged = true
	}

	// Emit side effects via EventCoordinator (notifications, webhooks, action events)
	// The coding-agent binding trigger (WI-88) fires inside
	// ItemUpdateService.UpdateItem, shared by every update surface.
	if h.eventCoordinator != nil {
		h.eventCoordinator.EmitItemUpdated(originalItem, updatedItem, result.StatusChanged, assigneeChanged, user.ID, result.FieldChanges, user.Username)
	} else {
		// Fallback to individual services if EventCoordinator not set
		if h.notificationService != nil {
			var statusName string
			if result.StatusChanged && updatedItem.StatusID != nil {
				if err := h.db.QueryRow("SELECT name FROM statuses WHERE id = ?", *updatedItem.StatusID).Scan(&statusName); err != nil && !errors.Is(err, sql.ErrNoRows) {
					slog.Warn("failed to load status name", slog.Int("status_id", *updatedItem.StatusID), slog.Any("error", err))
				}
			}
			itemKey := fmt.Sprintf("%s-%d", updatedItem.WorkspaceKey, updatedItem.WorkspaceItemNumber)

			if result.StatusChanged {
				h.notificationService.EmitEvent(&services.NotificationEvent{
					EventType:   models.EventStatusChanged,
					WorkspaceID: updatedItem.WorkspaceID,
					ActorUserID: user.ID,
					ItemID:      updatedItem.ID,
					AssigneeID:  updatedItem.AssigneeID,
					CreatorID:   originalItem.CreatorID,
					Title:       "Status Changed",
					TemplateData: map[string]interface{}{
						"item.title":  updatedItem.Title,
						"item.key":    itemKey,
						"item.id":     updatedItem.ID,
						"status.name": statusName,
						"user.name":   user.Username,
					},
				})
			}
			if assigneeChanged {
				h.notificationService.EmitEvent(&services.NotificationEvent{
					EventType:   models.EventItemAssigned,
					WorkspaceID: updatedItem.WorkspaceID,
					ActorUserID: user.ID,
					ItemID:      updatedItem.ID,
					AssigneeID:  updatedItem.AssigneeID,
					CreatorID:   originalItem.CreatorID,
					Title:       "Item Assigned",
					TemplateData: map[string]interface{}{
						"item.title": updatedItem.Title,
						"item.key":   itemKey,
						"item.id":    updatedItem.ID,
						"user.name":  user.Username,
					},
				})
			}
			if !result.StatusChanged && !assigneeChanged {
				h.notificationService.EmitEvent(&services.NotificationEvent{
					EventType:   models.EventItemUpdated,
					WorkspaceID: updatedItem.WorkspaceID,
					ActorUserID: user.ID,
					ItemID:      updatedItem.ID,
					AssigneeID:  updatedItem.AssigneeID,
					CreatorID:   originalItem.CreatorID,
					Title:       "Item Updated",
					TemplateData: map[string]interface{}{
						"item.title": updatedItem.Title,
						"item.key":   itemKey,
						"item.id":    updatedItem.ID,
						"user.name":  user.Username,
					},
				})
			}
		}
		if h.actionService != nil {
			if result.StatusChanged {
				h.actionService.EmitActionEvent(&models.ActionEvent{
					EventType:   models.ActionTriggerStatusTransition,
					WorkspaceID: updatedItem.WorkspaceID,
					ItemID:      updatedItem.ID,
					ActorUserID: user.ID,
					OldValues:   map[string]interface{}{"status_id": originalItem.StatusID},
					NewValues: map[string]interface{}{
						"status_id":   updatedItem.StatusID,
						"title":       updatedItem.Title,
						"assignee_id": updatedItem.AssigneeID,
						"creator_id":  updatedItem.CreatorID,
					},
				})
			} else {
				h.actionService.EmitActionEvent(&models.ActionEvent{
					EventType:   models.ActionTriggerItemUpdated,
					WorkspaceID: updatedItem.WorkspaceID,
					ItemID:      updatedItem.ID,
					ActorUserID: user.ID,
					OldValues: map[string]interface{}{
						"status_id":   originalItem.StatusID,
						"assignee_id": originalItem.AssigneeID,
						"title":       originalItem.Title,
						"priority_id": originalItem.PriorityID,
					},
					NewValues: map[string]interface{}{
						"status_id":   updatedItem.StatusID,
						"assignee_id": updatedItem.AssigneeID,
						"title":       updatedItem.Title,
						"priority_id": updatedItem.PriorityID,
						"creator_id":  updatedItem.CreatorID,
					},
				})
			}
		}
		if h.webhookSender != nil {
			if result.StatusChanged {
				go h.webhookSender.DispatchEvent("status.changed", updatedItem)
			}
			if assigneeChanged {
				go h.webhookSender.DispatchEvent("item.assigned", updatedItem)
			}
			go h.webhookSender.DispatchEvent("item.updated", updatedItem)
		}
	}

	// Push status change to GitHub if issue sync is configured
	if h.issueSyncService != nil && result.StatusChanged && updatedItem.StatusID != nil {
		go func(ctx context.Context) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			h.issueSyncService.PushStatusToGitHub(ctx, updatedItem.ID, *updatedItem.StatusID)
		}(r.Context())
	}

	// Process @mentions in description if it changed
	if h.mentionService != nil && originalItem.Description != updatedItem.Description {
		if err := h.mentionService.ProcessMentions(services.ProcessMentionsParams{
			SourceType:  "item_description",
			SourceID:    updatedItem.ID,
			Content:     updatedItem.Description,
			ItemID:      updatedItem.ID,
			WorkspaceID: updatedItem.WorkspaceID,
			ActorUserID: user.ID,
		}); err != nil {
			slog.Warn("failed to process description mentions", slog.Int("item_id", updatedItem.ID), slog.Any("error", err))
			// Don't fail the request if mention processing fails
		}
	}

	// Strip names of time projects the editor has no access to (incl. the
	// inherited effective project), matching the masked read paths. Mask a
	// copy so async consumers of updatedItem aren't mutated.
	maskedUpdated := []models.Item{*updatedItem}
	h.maskInaccessibleProjectNames(user.ID, maskedUpdated)

	respondJSONOK(w, maskedUpdated[0])
}

// maskInaccessibleProjectNames blanks the human-readable project name fields on
// items whose project / time-project / effective-project the user is not allowed
// to view. See TimePermissionService.MaskInaccessibleProjectNames.
func (h *ItemHandler) maskInaccessibleProjectNames(userID int, items []models.Item) {
	services.NewTimePermissionService(h.db, h.permissionService).MaskInaccessibleProjectNames(userID, items)
}

// projectResolutionChanged reports whether an update touched a field that can
// change an item's (or its descendants') effective project: the direct project,
// the inherit flag, or the parent link.
func projectResolutionChanged(original, updated *models.Item) bool {
	if original.InheritProject != updated.InheritProject {
		return true
	}
	if !intPtrEqual(original.ProjectID, updated.ProjectID) {
		return true
	}
	if !intPtrEqual(original.ParentID, updated.ParentID) {
		return true
	}
	return false
}

// invalidateEffectiveProjectSubtree drops the cached hierarchy entry for an item
// and all of its descendants, since each descendant may resolve its effective
// project through this item.
func (h *ItemHandler) invalidateEffectiveProjectSubtree(itemID int) {
	_ = h.itemCache.InvalidateItemHierarchy(itemID, nil)
	if h.hierarchyService == nil {
		return
	}
	descendants, err := h.hierarchyService.GetDescendants(itemID, 0)
	if err != nil {
		slog.Warn("failed to load descendants for cache invalidation", slog.Int("item_id", itemID), slog.Any("error", err))
		return
	}
	for i := range descendants {
		_ = h.itemCache.InvalidateItemHierarchy(descendants[i].ID, nil)
	}
}

func (h *ItemHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Get item details before deletion (for permission check and notifications)
	repo := repository.NewItemRepository(h.db)
	item, err := repo.FindByID(id)
	if err != nil {
		if err == repository.ErrNotFound {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	// Check permission
	canDelete, err := h.canDeleteItem(user.ID, item.WorkspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canDelete {
		respondNotFound(w, r, "Item")
		return
	}

	// Capture ancestor IDs before deletion for cache invalidation
	var ancestorIDs []int
	if h.itemCache != nil && h.hierarchyService != nil {
		if ancestors, aErr := h.hierarchyService.GetAncestors(id); aErr == nil {
			for _, a := range ancestors {
				ancestorIDs = append(ancestorIDs, a.ID)
			}
		}
	}

	if err := services.NewItemCRUDService(h.db).DeleteSingle(id); err != nil {
		respondInternalError(w, r, err)
		return
	}

	logAuditWithDetails(h.db, r, user, logger.ActionItemDelete, logger.ResourceItem, &id, item.Title, map[string]interface{}{
		"workspace_id": item.WorkspaceID,
		"item_type_id": item.ItemTypeID,
		"parent_id":    item.ParentID,
		"status_id":    item.StatusID,
		"assignee_id":  item.AssigneeID,
		"creator_id":   item.CreatorID,
	})

	// Invalidate item hierarchy and workspace project caches
	if h.itemCache != nil {
		_ = h.itemCache.InvalidateItemHierarchy(id, ancestorIDs)
		_ = h.itemCache.InvalidateWorkspaceProjects(item.WorkspaceID)
	}

	// Emit side effects via EventCoordinator (notifications, webhooks)
	if h.eventCoordinator != nil {
		h.eventCoordinator.EmitItemDeleted(item, user.ID, 0, user.Username)
	} else {
		// Fallback to individual services if EventCoordinator not set
		if h.notificationService != nil {
			h.notificationService.EmitEvent(&services.NotificationEvent{
				EventType:   models.EventItemDeleted,
				WorkspaceID: item.WorkspaceID,
				ActorUserID: user.ID,
				ItemID:      id,
				AssigneeID:  item.AssigneeID,
				CreatorID:   item.CreatorID,
				Title:       "Item Deleted",
				TemplateData: map[string]interface{}{
					"item.title": item.Title,
					"item.id":    id,
					"user.name":  user.Username,
				},
			})
		}
		if h.webhookSender != nil {
			go h.webhookSender.DispatchEvent("item.deleted", item)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetDeleteInfo returns information needed before deleting an item (descendant count, parent info)
func (h *ItemHandler) GetDeleteInfo(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	repo := repository.NewItemRepository(h.db)
	item, err := repo.FindByID(id)
	if err != nil {
		if err == repository.ErrNotFound {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	// Check permission - need at least view access
	canEdit, err := h.canEditItem(user.ID, item.WorkspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canEdit {
		respondNotFound(w, r, "Item")
		return
	}

	// Get descendant IDs
	descendantIDs, err := repo.GetDescendantIDs(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Get hierarchy level for the item type (needed for filtering reparent candidates)
	var hierarchyLevel sql.NullInt64
	if item.ItemTypeID != nil {
		if err := h.db.QueryRow("SELECT hierarchy_level FROM item_types WHERE id = ?", *item.ItemTypeID).Scan(&hierarchyLevel); err != nil && !errors.Is(err, sql.ErrNoRows) {
			respondInternalError(w, r, err)
			return
		}
	}

	response := map[string]interface{}{
		"hasChildren":     len(descendantIDs) > 0,
		"descendantCount": len(descendantIDs),
		"parentId":        item.ParentID,
		"title":           item.Title,
		"itemTypeId":      item.ItemTypeID,
		"workspaceId":     item.WorkspaceID,
		"hierarchyLevel":  utils.NullInt64ToPtr(hierarchyLevel),
	}

	respondJSONOK(w, response)
}

// ReparentChildren moves all direct children of an item to a new parent
func (h *ItemHandler) ReparentChildren(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	var req struct {
		NewParentID *int `json:"newParentId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondValidationError(w, r, "Invalid request body: "+err.Error())
		return
	}

	repo := repository.NewItemRepository(h.db)
	item, err := repo.FindByID(id)
	if err != nil {
		if err == repository.ErrNotFound {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	// Check permission
	canEdit, err := h.canEditItem(user.ID, item.WorkspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canEdit {
		respondNotFound(w, r, "Item")
		return
	}

	// If new parent is specified, verify it exists and is in the same workspace
	if req.NewParentID != nil {
		var newParent *models.Item
		newParent, err = repo.FindByID(*req.NewParentID)
		if err != nil {
			if err == repository.ErrNotFound {
				respondNotFound(w, r, "item")
				return
			}
			respondInternalError(w, r, err)
			return
		}
		if newParent.WorkspaceID != item.WorkspaceID {
			respondValidationError(w, r, "New parent must be in the same workspace")
			return
		}
	}

	// Get direct children
	children, err := repo.GetChildren(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if len(children) == 0 {
		respondJSONOK(w, map[string]interface{}{"reparentedCount": 0})
		return
	}

	// Start transaction
	tx, err := h.db.Begin()
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	// Cycle check inside the transaction so concurrent reparents cannot race
	// past each other's individual checks. WouldCreateCycleTx locks the rows
	// it walks (FOR UPDATE on Postgres); combined with UpdateParent below in
	// the same tx, the check and the write are atomic.
	if req.NewParentID != nil {
		wouldCycle, cycleErr := h.hierarchyService.WouldCreateCycleTx(tx, id, *req.NewParentID)
		if cycleErr != nil {
			respondInternalError(w, r, cycleErr)
			return
		}
		if wouldCycle {
			respondValidationError(w, r, "Reparenting would create a hierarchy cycle")
			return
		}
	}

	// Update parent_id for all direct children
	for _, child := range children {
		if err := repo.UpdateParent(tx, child.ID, req.NewParentID); err != nil {
			respondInternalError(w, r, err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Invalidate caches for reparented children
	if h.itemCache != nil {
		_ = h.itemCache.InvalidateWorkspaceProjects(item.WorkspaceID)
		for _, child := range children {
			_ = h.itemCache.InvalidateItemHierarchy(child.ID, nil)
		}
	}

	respondJSONOK(w, map[string]interface{}{"reparentedCount": len(children)})
}

// DeleteCascade deletes an item and all its descendants
func (h *ItemHandler) DeleteCascade(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Get item details before deletion (for permission check and notifications)
	repo := repository.NewItemRepository(h.db)
	item, err := repo.FindByID(id)
	if err != nil {
		if err == repository.ErrNotFound {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	// Check permission
	canDelete, err := h.canDeleteItem(user.ID, item.WorkspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canDelete {
		respondNotFound(w, r, "Item")
		return
	}

	// Capture ancestor IDs before deletion for cache invalidation
	var ancestorIDs []int
	if h.itemCache != nil && h.hierarchyService != nil {
		if ancestors, aErr := h.hierarchyService.GetAncestors(id); aErr == nil {
			for _, a := range ancestors {
				ancestorIDs = append(ancestorIDs, a.ID)
			}
		}
	}

	// Use the CRUD service for cascade delete
	crudService := services.NewItemCRUDService(h.db)
	result, err := crudService.Delete(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	logAuditWithDetails(h.db, r, user, logger.ActionItemDeleteCascade, logger.ResourceItem, &id, item.Title, map[string]interface{}{
		"workspace_id":     item.WorkspaceID,
		"item_type_id":     item.ItemTypeID,
		"parent_id":        item.ParentID,
		"status_id":        item.StatusID,
		"assignee_id":      item.AssigneeID,
		"creator_id":       item.CreatorID,
		"deleted_count":    result.DeletedCount,
		"descendant_count": result.DeletedCount - 1,
	})

	// Invalidate item hierarchy and workspace project caches
	if h.itemCache != nil {
		_ = h.itemCache.InvalidateItemHierarchy(id, ancestorIDs)
		_ = h.itemCache.InvalidateWorkspaceProjects(item.WorkspaceID)
	}

	// Emit side effects via EventCoordinator (notifications, webhooks)
	if h.eventCoordinator != nil {
		h.eventCoordinator.EmitItemDeleted(item, user.ID, result.DeletedCount-1, user.Username)
	} else {
		// Fallback to individual services if EventCoordinator not set
		if h.notificationService != nil {
			h.notificationService.EmitEvent(&services.NotificationEvent{
				EventType:   models.EventItemDeleted,
				WorkspaceID: item.WorkspaceID,
				ActorUserID: user.ID,
				ItemID:      id,
				AssigneeID:  item.AssigneeID,
				CreatorID:   item.CreatorID,
				Title:       "Item Deleted",
				TemplateData: map[string]interface{}{
					"item.title":  item.Title,
					"item.id":     id,
					"user.name":   user.Username,
					"descendants": result.DeletedCount - 1,
				},
			})
		}
		if h.webhookSender != nil {
			go h.webhookSender.DispatchEvent("item.deleted", item)
		}
	}

	respondJSONOK(w, map[string]interface{}{
		"deletedCount": result.DeletedCount,
	})
}

func (h *ItemHandler) Copy(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Get the original item using repository
	repo := repository.NewItemRepository(h.db)
	originalItem, err := repo.FindByID(id)
	if err != nil {
		if err == repository.ErrNotFound {
			respondNotFound(w, r, "item")
		} else {
			respondInternalError(w, r, err)
		}
		return
	}

	// Check permission
	canEdit, err := h.canEditItem(user.ID, originalItem.WorkspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canEdit {
		respondNotFound(w, r, "Item")
		return
	}

	// Create copy title
	copyTitle := sanitize.PlainTextField.Sanitize(fmt.Sprintf("COPY - %s", originalItem.Title))

	result, err := services.NewItemCRUDService(h.db).Copy(id, services.CopyOptions{
		NewTitle:  copyTitle,
		CreatorID: user.ID,
	})
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	newItem, err := repo.FindByID(result.NewItemID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, newItem)
}

// GetCacheStats returns cache performance statistics
// GET /api/items/cache-stats
func (h *ItemHandler) GetCacheStats(w http.ResponseWriter, r *http.Request) {
	if h.itemCache == nil {
		respondError(w, r, &restapi.APIError{
			StatusCode: http.StatusServiceUnavailable,
			Code:       "SERVICE_UNAVAILABLE",
			Message:    "Item cache is not enabled",
		})
		return
	}

	stats := h.itemCache.GetStats()

	respondJSONOK(w, map[string]interface{}{
		"cache_enabled": true,
		"statistics":    stats,
		"timestamp":     time.Now().Format(time.RFC3339),
	})
}
