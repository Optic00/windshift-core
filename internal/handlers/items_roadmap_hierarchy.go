package handlers

import (
	"errors"
	"net/http"

	"windshift/internal/models"
	"windshift/internal/repository"
)

type roadmapHierarchyDatesRequest struct {
	RootIDs []int `json:"root_ids"`
}

// GetRoadmapHierarchyDates returns the authorized hierarchy date projection
// needed for client-side rollup and rolldown rendering.
func (h *ItemHandler) GetRoadmapHierarchyDates(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	req, ok := decodeJSON[roadmapHierarchyDatesRequest](w, r)
	if !ok {
		return
	}
	ctx, cancel := h.requestDBContext(r)
	defer cancel()

	items, truncated, err := repository.NewItemRepository(h.db).GetRoadmapHierarchyDates(ctx, req.RootIDs)
	if err != nil {
		if errors.Is(err, repository.ErrRoadmapHierarchyRootLimit) {
			respondBadRequest(w, r, err.Error())
		} else {
			respondInternalError(w, r, err)
		}
		return
	}

	allowedByWorkspace := make(map[int]bool)
	filtered := make([]models.RoadmapHierarchyDate, 0, len(items))
	for _, item := range items {
		allowed, known := allowedByWorkspace[item.WorkspaceID]
		if !known {
			allowed, err = h.canViewItem(user.ID, item.WorkspaceID)
			if err != nil {
				respondInternalError(w, r, err)
				return
			}
			allowedByWorkspace[item.WorkspaceID] = allowed
		}
		if allowed {
			filtered = append(filtered, item)
		}
	}

	respondJSONOK(w, map[string]any{"items": filtered, "truncated": truncated})
}
