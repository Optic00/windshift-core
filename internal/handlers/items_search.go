package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
)

// Search filter limits to prevent abuse
const (
	maxSearchQueryLength = 500 // Maximum characters for search query
	maxWorkspaceFilters  = 50  // Maximum number of workspace IDs in filter
	maxStatusFilters     = 20  // Maximum number of statuses in filter
	maxPriorityFilters   = 10  // Maximum number of priorities in filter
)

// Search items across workspaces with advanced filtering
func (h *ItemHandler) Search(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Get user from context
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	ctx, cancel := h.requestDBContext(r)
	defer cancel()

	// Get accessible workspace IDs (includes active workspaces and inactive ones where user has admin access)
	accessibleWorkspaceIDs, err := h.getAccessibleWorkspaceIDs(user)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// If user has no accessible workspaces, return empty list
	if len(accessibleWorkspaceIDs) == 0 {
		respondJSONOK(w, []models.Item{})
		return
	}

	// Get search parameters with input validation
	textQuery := r.URL.Query().Get("q")
	workspaceIDs := r.URL.Query()["workspace_id"] // Allow multiple workspace IDs
	statuses := r.URL.Query()["status"]           // Allow multiple statuses
	priorities := r.URL.Query()["priority"]       // Allow multiple priorities

	// Validate and sanitize inputs
	if len(textQuery) > maxSearchQueryLength {
		respondValidationError(w, r, fmt.Sprintf("Search query too long (max %d characters)", maxSearchQueryLength))
		return
	}

	// Validate workspace IDs are numeric
	for _, workspaceID := range workspaceIDs {
		if workspaceID == "" {
			continue
		}
		if _, err = strconv.Atoi(workspaceID); err != nil {
			respondValidationError(w, r, "Invalid workspace ID format")
			return
		}
	}

	// Limit array sizes to prevent abuse
	if len(workspaceIDs) > maxWorkspaceFilters {
		respondValidationError(w, r, fmt.Sprintf("Too many workspace filters (max %d)", maxWorkspaceFilters))
		return
	}
	if len(statuses) > maxStatusFilters {
		respondValidationError(w, r, fmt.Sprintf("Too many status filters (max %d)", maxStatusFilters))
		return
	}
	if len(priorities) > maxPriorityFilters {
		respondValidationError(w, r, fmt.Sprintf("Too many priority filters (max %d)", maxPriorityFilters))
		return
	}

	// Intersect requested workspace IDs with accessible ones
	finalWorkspaceIDs := accessibleWorkspaceIDs
	if len(workspaceIDs) > 0 {
		requestedIDs := make(map[int]bool)
		for _, wsID := range workspaceIDs {
			if wsID != "" {
				var id int
				if id, err = strconv.Atoi(wsID); err == nil {
					requestedIDs[id] = true
				}
			}
		}
		finalWorkspaceIDs = []int{}
		for _, id := range accessibleWorkspaceIDs {
			if requestedIDs[id] {
				finalWorkspaceIDs = append(finalWorkspaceIDs, id)
			}
		}
	}

	// Parse status IDs (numeric)
	var statusIDs []int
	for _, s := range statuses {
		if s != "" {
			if id, err := strconv.Atoi(s); err == nil {
				statusIDs = append(statusIDs, id)
			}
		}
	}

	// Parse priority IDs (numeric)
	var priorityIDs []int
	for _, p := range priorities {
		if p != "" {
			if id, err := strconv.Atoi(p); err == nil {
				priorityIDs = append(priorityIDs, id)
			}
		}
	}

	// Parse limit
	limit := 100
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		parsedLimit, err := strconv.Atoi(limitStr)
		if err != nil {
			respondValidationError(w, r, "Invalid limit format")
			return
		}
		switch {
		case parsedLimit < 1:
			limit = 1
		case parsedLimit > 1000:
			limit = 1000
		default:
			limit = parsedLimit
		}
	}

	// Call service
	items, _, err := h.itemCRUD.SearchWithFiltersContext(ctx, services.SearchParams{
		TextQuery:    textQuery,
		WorkspaceIDs: finalWorkspaceIDs,
		StatusIDs:    statusIDs,
		PriorityIDs:  priorityIDs,
		Pagination: services.PaginationParams{
			Limit: limit,
		},
	})
	if err != nil {
		h.respondItemReadError(w, r, err)
		return
	}

	// Filter items based on user permissions
	filteredItems, err := h.filterItemsByPermissions(user.ID, items)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if ctx.Err() != nil {
		h.respondItemReadError(w, r, ctx.Err())
		return
	}

	// Strip names of time projects the viewer has no access to, keeping the IDs.
	h.maskInaccessibleProjectNamesContext(ctx, user.ID, filteredItems)
	if ctx.Err() != nil {
		h.respondItemReadError(w, r, ctx.Err())
		return
	}

	respondJSONOK(w, filteredItems)
}

// UpdateFracIndex updates the frac_index of an item for fractional indexing ordering
func (h *ItemHandler) UpdateFracIndex(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Parse the request body
	var fracIndexRequest struct {
		// Item ID-based ranking (uses prev/next item IDs to calculate frac_index)
		PrevItemID *int `json:"prev_item_id"` // ID of item before in current view
		NextItemID *int `json:"next_item_id"` // ID of item after in current view
	}

	if err := newJSONDecoder(w, r).Decode(&fracIndexRequest); err != nil {
		respondBadRequest(w, r, "Invalid request body")
		return
	}

	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Get workspace_id for permission check
	workspaceID, err := repository.NewItemRepository(h.db).GetWorkspaceID(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	// Check if user has permission to edit items in this workspace
	canEdit, permErr := h.canEditItem(user.ID, workspaceID)
	if permErr != nil {
		respondInternalError(w, r, permErr)
		return
	}
	if !canEdit {
		respondNotFound(w, r, "Item")
		return
	}

	// Pre-flight: resolve neighbor frac_indexes for the 409-on-missing
	// behavior (the frontend relies on this to distinguish "neighbor
	// deleted" from a generic failure) and short-circuit when the
	// current position is already strictly between prev and next.
	// The real atomic move is delegated to repository.MoveItemBetween,
	// which re-reads the neighbors inside its own tx with FOR UPDATE
	// (Postgres) and retries on idx_items_frac_index violations.
	itemRepo := repository.NewItemRepository(h.db)

	var prevFracIndex, nextFracIndex string
	if fracIndexRequest.PrevItemID != nil {
		frac, ferr := itemRepo.GetFracIndex(*fracIndexRequest.PrevItemID)
		if ferr != nil || frac == nil {
			respondConflict(w, r, "previous neighbor is no longer available; please refresh")
			return
		}
		prevFracIndex = *frac
	}
	if fracIndexRequest.NextItemID != nil {
		frac, ferr := itemRepo.GetFracIndex(*fracIndexRequest.NextItemID)
		if ferr != nil || frac == nil {
			respondConflict(w, r, "next neighbor is no longer available; please refresh")
			return
		}
		nextFracIndex = *frac
	}

	// Defensive check: if prev and next have the same frac_index there's no room between.
	if prevFracIndex != "" && nextFracIndex != "" && prevFracIndex == nextFracIndex {
		h.Get(w, r)
		return
	}

	// Short-circuit: if the item is already strictly between prev and next, no write needed.
	currentFrac, ferr := itemRepo.GetFracIndex(id)
	if ferr != nil {
		respondInternalError(w, r, ferr)
		return
	}
	if currentFrac != nil {
		current := *currentFrac
		isAfterPrev := prevFracIndex == "" || current > prevFracIndex
		isBeforeNext := nextFracIndex == "" || current < nextFracIndex
		if isAfterPrev && isBeforeNext {
			h.Get(w, r)
			return
		}
	}

	if _, err := repository.MoveItemBetween(h.db, id, fracIndexRequest.PrevItemID, fracIndexRequest.NextItemID); err != nil {
		if repository.IsFracIndexUniqueViolation(err) {
			respondConflict(w, r, "could not reorder; please refresh and try again")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	h.Get(w, r)
}

// GetBacklogItems returns items whose statuses are not marked as completed for a workspace
func (h *ItemHandler) GetBacklogItems(w http.ResponseWriter, r *http.Request) {
	// Get user from context
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	ctx, cancel := h.requestDBContext(r)
	defer cancel()

	workspaceIDParam := r.URL.Query().Get("workspace_id")
	collectionIDParam := r.URL.Query().Get("collection_id")

	// workspace_id is required when no collection_id is provided
	if workspaceIDParam == "" && collectionIDParam == "" {
		respondValidationError(w, r, "workspace_id parameter is required")
		return
	}

	var wsID int
	if workspaceIDParam != "" {
		var err error
		wsID, err = strconv.Atoi(workspaceIDParam)
		if err != nil {
			respondValidationError(w, r, "Invalid workspace_id format")
			return
		}
	}

	var collectionID int
	if collectionIDParam != "" {
		var err error
		collectionID, err = strconv.Atoi(collectionIDParam)
		if err != nil {
			respondValidationError(w, r, "Invalid collection_id parameter")
			return
		}
	}

	// Get accessible workspace IDs
	accessibleWorkspaceIDs, err := h.getAccessibleWorkspaceIDs(user)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if len(accessibleWorkspaceIDs) == 0 {
		respondJSONOK(w, []models.Item{})
		return
	}

	qlQuery := r.URL.Query().Get("ql")
	subQLQuery := r.URL.Query().Get("sub_ql")

	// Parse pagination parameters
	page := 1
	limit := 50
	maxLimit := 1000

	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if p, parseErr := strconv.Atoi(pageStr); parseErr == nil && p > 0 {
			page = p
		}
	}

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, parseErr := strconv.Atoi(limitStr); parseErr == nil && l > 0 {
			limit = l
			if limit > maxLimit {
				limit = maxLimit
			}
		}
	}

	offset := (page - 1) * limit

	omitDescriptions := strings.EqualFold(r.URL.Query().Get("omit_descriptions"), "true") ||
		strings.EqualFold(r.URL.Query().Get("fields"), "summary")
	var watermark int64
	if strings.EqualFold(r.URL.Query().Get("include_watermark"), "true") {
		watermark, err = repository.NewItemChangeRepository(h.db).CurrentWatermark(accessibleWorkspaceIDs, wsID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
	}

	// Call service
	items, totalCount, err := h.itemCRUD.GetBacklogItemsContext(ctx, services.BacklogParams{
		WorkspaceID:      wsID,
		CollectionID:     collectionID,
		QLQuery:          qlQuery,
		SubQLQuery:       subQLQuery,
		WorkspaceIDs:     accessibleWorkspaceIDs,
		UserID:           user.ID,
		Pagination:       services.PaginationParams{Limit: limit, Offset: offset, Page: page},
		OmitDescriptions: omitDescriptions,
	})
	if err != nil {
		if errors.Is(err, services.ErrQLQuery) {
			respondValidationError(w, r, err.Error())
			return
		}
		if errors.Is(err, services.ErrCollectionNotFound) {
			respondNotFound(w, r, "collection")
			return
		}
		h.respondItemReadError(w, r, err)
		return
	}

	// Filter items based on user permissions
	filteredItems, err := h.filterItemsByPermissions(user.ID, items)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if ctx.Err() != nil {
		h.respondItemReadError(w, r, ctx.Err())
		return
	}

	// Strip names of time projects the viewer has no access to, keeping the IDs.
	h.maskInaccessibleProjectNamesContext(ctx, user.ID, filteredItems)
	if ctx.Err() != nil {
		h.respondItemReadError(w, r, ctx.Err())
		return
	}

	totalPages := 0
	if limit > 0 {
		totalPages = (totalCount + limit - 1) / limit
	}

	response := models.PaginatedItemsResponse{
		Items: filteredItems,
		Pagination: models.PaginationMeta{
			Page:       page,
			Limit:      limit,
			Total:      totalCount,
			TotalPages: totalPages,
		},
		Watermark: watermark,
	}
	respondJSONOK(w, response)
}
