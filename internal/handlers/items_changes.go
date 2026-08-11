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

const maxDeltaChanges = 500

type itemChangesResponse struct {
	ChangedItemIDs     []int `json:"changed_item_ids"`
	RemovedItemIDs     []int `json:"removed_item_ids"`
	Watermark          int64 `json:"watermark"`
	RequiresFullReload bool  `json:"requires_full_reload"`
	MembershipDirty    bool  `json:"membership_dirty"`
}

// GetChanges handles GET /api/items/changes?since=&workspace_id=&collection_id=&sub_ql=.
// It returns a cheap change-log delta for collection/workspace views. The
// watermark is the item_change_log.id; callers should pass it back as `since`.
func (h *ItemHandler) GetChanges(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	accessibleWorkspaceIDs, err := h.getAccessibleWorkspaceIDs(user)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if len(accessibleWorkspaceIDs) == 0 {
		respondJSONOK(w, itemChangesResponse{ChangedItemIDs: []int{}, RemovedItemIDs: []int{}})
		return
	}

	workspaceID, ok := parseOptionalIntParam(w, r, "workspace_id")
	if !ok {
		return
	}
	if workspaceID == 0 && strings.TrimSpace(r.PathValue("id")) != "" {
		workspaceID, ok = parseOptionalPathIntParam(w, r, "id")
		if !ok {
			return
		}
	}
	collectionID, ok := parseOptionalIntParam(w, r, "collection_id")
	if !ok {
		return
	}
	if collectionID == 0 && strings.TrimSpace(r.PathValue("collectionId")) != "" {
		collectionID, ok = parseOptionalPathIntParam(w, r, "collectionId")
		if !ok {
			return
		}
	}

	if workspaceID > 0 {
		canView, err := h.canViewItem(user.ID, workspaceID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if !canView {
			respondNotFound(w, r, "workspace")
			return
		}
	}

	changeRepo := repository.NewItemChangeRepository(h.db)

	if collectionID > 0 {
		if exists, err := changeRepo.CollectionExistsInWorkspace(collectionID, workspaceID); err != nil {
			respondInternalError(w, r, err)
			return
		} else if !exists {
			respondNotFound(w, r, "collection")
			return
		}
	}

	since := int64(0)
	sinceProvided := false
	if sinceParam := strings.TrimSpace(r.URL.Query().Get("since")); sinceParam != "" {
		sinceProvided = true
		since, err = strconv.ParseInt(sinceParam, 10, 64)
		if err != nil || since < 0 {
			respondValidationError(w, r, "Invalid since parameter")
			return
		}
	}

	watermark, err := changeRepo.CurrentWatermark(accessibleWorkspaceIDs, workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	response := itemChangesResponse{
		ChangedItemIDs: []int{},
		RemovedItemIDs: []int{},
		Watermark:      watermark,
	}

	// Omitting since primes the client after a full load without forcing it to
	// process the entire historical change log. An explicit since=0 is a real
	// low-watermark used by freshly migrated installs with an empty log.
	if !sinceProvided || since >= watermark {
		respondJSONOK(w, response)
		return
	}

	changes, err := changeRepo.QuerySince(accessibleWorkspaceIDs, workspaceID, since, maxDeltaChanges+1)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if len(changes) > maxDeltaChanges {
		response.RequiresFullReload = true
		response.MembershipDirty = true
		respondJSONOK(w, response)
		return
	}

	removedSet := make(map[int]bool)
	changedSet := make(map[int]bool)
	for _, change := range changes {
		if change.ItemID == 0 {
			response.RequiresFullReload = true
			response.MembershipDirty = true
			respondJSONOK(w, response)
			return
		}
		if change.Deleted {
			removedSet[change.ItemID] = true
			continue
		}
		visible, err := h.itemVisibleInDeltaScope(user, accessibleWorkspaceIDs, workspaceID, collectionID, change.ItemID, r.URL.Query().Get("sub_ql"))
		if err != nil {
			if errors.Is(err, services.ErrQLQuery) {
				respondValidationError(w, r, err.Error())
				return
			}
			respondInternalError(w, r, err)
			return
		}
		if visible {
			changedSet[change.ItemID] = true
		} else {
			removedSet[change.ItemID] = true
		}
	}

	for id := range removedSet {
		response.RemovedItemIDs = append(response.RemovedItemIDs, id)
	}
	for id := range changedSet {
		if !removedSet[id] {
			response.ChangedItemIDs = append(response.ChangedItemIDs, id)
		}
	}
	response.MembershipDirty = len(response.RemovedItemIDs) > 0

	respondJSONOK(w, response)
}

func parseOptionalIntParam(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return 0, true
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		respondValidationError(w, r, fmt.Sprintf("Invalid %s parameter", name))
		return 0, false
	}
	return parsed, true
}

func parseOptionalPathIntParam(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	value := strings.TrimSpace(r.PathValue(name))
	if value == "" {
		return 0, true
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		respondValidationError(w, r, fmt.Sprintf("Invalid %s path parameter", name))
		return 0, false
	}
	return parsed, true
}

func (h *ItemHandler) itemVisibleInDeltaScope(user *models.User, accessibleWorkspaceIDs []int, workspaceID, collectionID, itemID int, subQL string) (bool, error) {
	items, _, err := h.itemCRUD.ListWithQL(services.ListWithQLParams{
		WorkspaceID:  workspaceID,
		CollectionID: collectionID,
		SubQLQuery:   subQL,
		WorkspaceIDs: accessibleWorkspaceIDs,
		UserID:       user.ID,
		Filters: services.ItemFilters{
			ItemID: &itemID,
		},
		Pagination: services.PaginationParams{Limit: 1, Offset: 0},
	})
	if err != nil {
		if errors.Is(err, services.ErrCollectionNotFound) {
			return false, nil
		}
		return false, err
	}
	filteredItems, err := h.filterItemsByPermissions(user.ID, items)
	if err != nil {
		return false, err
	}
	return len(filteredItems) > 0, nil
}
