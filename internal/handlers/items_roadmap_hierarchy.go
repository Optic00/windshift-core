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

	itemRepo := repository.NewItemRepository(h.db)
	rootWorkspaceIDs, err := itemRepo.GetRoadmapHierarchyRootWorkspaceIDs(ctx, req.RootIDs)
	if err != nil {
		if errors.Is(err, repository.ErrRoadmapHierarchyRootLimit) {
			respondBadRequest(w, r, err.Error())
		} else {
			respondInternalError(w, r, err)
		}
		return
	}
	authorizedRootIDs, err := authorizedRoadmapHierarchyRootIDs(req.RootIDs, rootWorkspaceIDs, func(workspaceID int) (bool, error) {
		return h.canViewItem(user.ID, workspaceID)
	})
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	items, truncated, err := itemRepo.GetRoadmapHierarchyDates(ctx, authorizedRootIDs)
	if err != nil {
		respondInternalError(w, r, err)
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

func authorizedRoadmapHierarchyRootIDs(rootIDs []int, workspaceIDs map[int]int, canView func(int) (bool, error)) ([]int, error) {
	allowedByWorkspace := make(map[int]bool)
	seen := make(map[int]struct{}, len(rootIDs))
	authorized := make([]int, 0, len(rootIDs))
	for _, itemID := range rootIDs {
		if itemID <= 0 {
			continue
		}
		if _, duplicate := seen[itemID]; duplicate {
			continue
		}
		seen[itemID] = struct{}{}
		workspaceID, exists := workspaceIDs[itemID]
		if !exists {
			continue
		}
		allowed, known := allowedByWorkspace[workspaceID]
		if !known {
			var err error
			allowed, err = canView(workspaceID)
			if err != nil {
				return nil, err
			}
			allowedByWorkspace[workspaceID] = allowed
		}
		if allowed {
			authorized = append(authorized, itemID)
		}
	}
	return authorized, nil
}
