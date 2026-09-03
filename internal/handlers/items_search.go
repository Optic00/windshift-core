package handlers

import (
	"fmt"
	"net/http"
	"strconv"

	"windshift/internal/models"
	"windshift/internal/services"
)

const (
	maxSearchQueryLength = 500
	maxWorkspaceFilters  = 50
	maxStatusFilters     = 20
	maxPriorityFilters   = 10
)

func (h *ItemHandler) Search(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	ctx, cancel := h.requestDBContext(r)
	defer cancel()
	accessibleWorkspaceIDs, err := h.getAccessibleWorkspaceIDs(user)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if len(accessibleWorkspaceIDs) == 0 {
		respondJSONOK(w, []models.Item{})
		return
	}
	textQuery := r.URL.Query().Get("q")
	workspaceIDs := r.URL.Query()["workspace_id"]
	statuses := r.URL.Query()["status"]
	priorities := r.URL.Query()["priority"]
	if len(textQuery) > maxSearchQueryLength {
		respondValidationError(w, r, fmt.Sprintf("Search query too long (max %d characters)", maxSearchQueryLength))
		return
	}
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
	requested := make(map[int]struct{}, len(workspaceIDs))
	for _, value := range workspaceIDs {
		if value == "" {
			continue
		}
		id, err := strconv.Atoi(value)
		if err != nil {
			respondValidationError(w, r, "Invalid workspace ID format")
			return
		}
		requested[id] = struct{}{}
	}
	finalWorkspaceIDs := accessibleWorkspaceIDs
	if len(requested) > 0 {
		finalWorkspaceIDs = nil
		for _, id := range accessibleWorkspaceIDs {
			if _, ok := requested[id]; ok {
				finalWorkspaceIDs = append(finalWorkspaceIDs, id)
			}
		}
	}
	parseIDs := func(values []string) []int {
		ids := make([]int, 0, len(values))
		for _, value := range values {
			if id, err := strconv.Atoi(value); err == nil {
				ids = append(ids, id)
			}
		}
		return ids
	}
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			respondValidationError(w, r, "Invalid limit format")
			return
		}
		limit = max(1, min(parsed, 1000))
	}
	items, _, err := h.itemCRUD.SearchWithFiltersContext(ctx, services.SearchParams{
		TextQuery: textQuery, WorkspaceIDs: finalWorkspaceIDs,
		StatusIDs: parseIDs(statuses), PriorityIDs: parseIDs(priorities),
		Pagination: services.PaginationParams{Limit: limit},
	})
	if err != nil {
		h.respondItemReadError(w, r, err)
		return
	}
	items, err = h.filterItemsByPermissions(user.ID, items)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	h.maskInaccessibleProjectNamesContext(ctx, user.ID, items)
	if ctx.Err() != nil {
		h.respondItemReadError(w, r, ctx.Err())
		return
	}
	respondJSONOK(w, items)
}
