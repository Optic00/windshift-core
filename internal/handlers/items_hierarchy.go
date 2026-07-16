package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"windshift/internal/models"
	"windshift/internal/repository"
)

// requireItemViewByWorkspace authenticates the user, looks up the item's workspace,
// and verifies view permission. Returns the user and true on success; writes an
// error response and returns nil/false on failure.
func (h *ItemHandler) requireItemViewByWorkspace(w http.ResponseWriter, r *http.Request, itemID int) (*models.User, bool) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return nil, false
	}

	workspaceID, err := repository.NewItemRepository(h.db).GetWorkspaceIDCtx(r.Context(), itemID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "item")
			return nil, false
		}
		respondInternalError(w, r, fmt.Errorf("failed to fetch item: %w", err))
		return nil, false
	}

	canView, permErr := h.canViewItem(user.ID, workspaceID)
	if permErr != nil {
		respondInternalError(w, r, fmt.Errorf("permission check failed: %w", permErr))
		return nil, false
	}
	if !canView {
		respondNotFound(w, r, "Item")
		return nil, false
	}

	return user, true
}

// GetAncestors returns all ancestors of an item (for breadcrumbs)
func (h *ItemHandler) GetAncestors(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.requestDBContext(r)
	defer cancel()
	r = r.WithContext(ctx)
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	user, ok := h.requireItemViewByWorkspace(w, r, id)
	if !ok {
		return
	}

	ancestors, err := h.hierarchyService.GetAncestorsContext(ctx, id)
	if err != nil {
		h.respondItemReadError(w, r, err)
		return
	}

	// Apply permission filtering to ancestors as well
	filteredAncestors, err := h.filterItemsByPermissions(user.ID, ancestors)
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("permission check failed: %w", err))
		return
	}

	// Load labels
	if err := repository.NewLabelRepository(h.db).LoadForItemsContext(ctx, filteredAncestors); err != nil {
		h.respondItemReadError(w, r, err)
		return
	}
	if err := repository.NewMilestoneAttachRepository(h.db).LoadForItemsContext(ctx, filteredAncestors); err != nil {
		h.respondItemReadError(w, r, err)
		return
	}
	if err := LoadPersonalLabelsForItemsContext(ctx, h.db, filteredAncestors, user.ID); err != nil {
		h.respondItemReadError(w, r, err)
		return
	}

	respondJSONOK(w, filteredAncestors)
}

// GetDescendantsNew returns all descendants using the new hierarchy service
func (h *ItemHandler) GetDescendantsNew(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.requestDBContext(r)
	defer cancel()
	r = r.WithContext(ctx)
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	user, ok := h.requireItemViewByWorkspace(w, r, id)
	if !ok {
		return
	}

	// Optional depth limit
	var err error
	maxDepth := 0
	if maxDepthStr := r.URL.Query().Get("max_depth"); maxDepthStr != "" {
		maxDepth, err = strconv.Atoi(maxDepthStr)
		if err != nil || maxDepth < 0 {
			respondValidationError(w, r, "Invalid max_depth parameter")
			return
		}
	}

	descendants, err := h.hierarchyService.GetDescendantsContext(ctx, id, maxDepth)
	if err != nil {
		h.respondItemReadError(w, r, err)
		return
	}

	// Apply permission filtering
	filteredDescendants, err := h.filterItemsByPermissions(user.ID, descendants)
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("permission check failed: %w", err))
		return
	}

	// Load labels
	if err := repository.NewLabelRepository(h.db).LoadForItemsContext(ctx, filteredDescendants); err != nil {
		h.respondItemReadError(w, r, err)
		return
	}
	if err := repository.NewMilestoneAttachRepository(h.db).LoadForItemsContext(ctx, filteredDescendants); err != nil {
		h.respondItemReadError(w, r, err)
		return
	}
	if err := LoadPersonalLabelsForItemsContext(ctx, h.db, filteredDescendants, user.ID); err != nil {
		h.respondItemReadError(w, r, err)
		return
	}

	respondJSONOK(w, filteredDescendants)
}

// GetTimeRollup returns the aggregated estimate + logged minutes for an item
// and its descendants (subtree). Used by the item detail Time Tracking tab's
// "Include child items" toggle.
//
// Query params:
//   - max_depth: optional, defaults to 10, clamped to [1, 30].
//
// Permission model: view permission on the root item is enforced; per-
// descendant permission filtering is skipped because the response only exposes
// aggregate totals, not per-item data.
func (h *ItemHandler) GetTimeRollup(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	if _, ok := h.requireItemViewByWorkspace(w, r, id); !ok {
		return
	}

	maxDepth := 10
	if s := r.URL.Query().Get("max_depth"); s != "" {
		parsed, err := strconv.Atoi(s)
		if err != nil || parsed < 1 {
			respondValidationError(w, r, "Invalid max_depth parameter")
			return
		}
		maxDepth = parsed
	}

	rollup, err := repository.NewItemRepository(h.db).GetTimeRollup(id, maxDepth, 0)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, rollup)
}

// GetTree returns the item and all its descendants as a nested tree structure
func (h *ItemHandler) GetTree(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.requestDBContext(r)
	defer cancel()
	r = r.WithContext(ctx)
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Get the root item
	repo := repository.NewItemRepository(h.db)
	rootItem, err := repo.FindByIDContext(ctx, id)
	if err != nil {
		if err == repository.ErrNotFound {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, fmt.Errorf("failed to fetch item: %w", err))
		return
	}

	// Check if user has permission to view item's workspace
	canView, permErr := h.canViewItem(user.ID, rootItem.WorkspaceID)
	if permErr != nil {
		respondInternalError(w, r, fmt.Errorf("permission check failed: %w", permErr))
		return
	}
	if !canView {
		respondNotFound(w, r, "Item")
		return
	}

	// Get all descendants
	descendants, err := h.hierarchyService.GetDescendantsContext(ctx, id, 0)
	if err != nil {
		h.respondItemReadError(w, r, err)
		return
	}

	// Apply permission filtering
	filteredDescendants, err := h.filterItemsByPermissions(user.ID, descendants)
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("permission check failed: %w", err))
		return
	}

	// Load labels for root item and descendants
	allItems := append([]models.Item{*rootItem}, filteredDescendants...)
	if err := repository.NewLabelRepository(h.db).LoadForItemsContext(ctx, allItems); err != nil {
		h.respondItemReadError(w, r, err)
		return
	}
	if err := LoadPersonalLabelsForItemsContext(ctx, h.db, allItems, user.ID); err != nil {
		h.respondItemReadError(w, r, err)
		return
	}
	if err := repository.NewMilestoneAttachRepository(h.db).LoadForItemsContext(ctx, allItems); err != nil {
		h.respondItemReadError(w, r, err)
		return
	}
	*rootItem = allItems[0]
	copy(filteredDescendants, allItems[1:])

	// Build tree structure
	tree := h.buildItemTree(rootItem, filteredDescendants)

	respondJSONOK(w, tree)
}

// ItemTreeNode represents an item with its children in a tree structure
type ItemTreeNode struct {
	*models.Item
	Children []*ItemTreeNode `json:"children"`
}

// buildItemTree constructs a nested tree from a root item and its descendants
func (h *ItemHandler) buildItemTree(root *models.Item, descendants []models.Item) *ItemTreeNode {
	// Create a map for quick lookup
	nodeMap := make(map[int]*ItemTreeNode)

	// Create node for root
	rootNode := &ItemTreeNode{
		Item:     root,
		Children: make([]*ItemTreeNode, 0),
	}
	nodeMap[root.ID] = rootNode

	// Create nodes for all descendants
	for i := range descendants {
		item := &descendants[i]
		nodeMap[item.ID] = &ItemTreeNode{
			Item:     item,
			Children: make([]*ItemTreeNode, 0),
		}
	}

	// Build tree by linking children to parents
	for _, item := range descendants {
		if item.ParentID != nil {
			if parentNode, exists := nodeMap[*item.ParentID]; exists {
				parentNode.Children = append(parentNode.Children, nodeMap[item.ID])
			}
		}
	}

	return rootNode
}

// GetChildrenNew returns direct children using the new hierarchy service
func (h *ItemHandler) GetChildrenNew(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.requestDBContext(r)
	defer cancel()
	r = r.WithContext(ctx)
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	user, ok := h.requireItemViewByWorkspace(w, r, id)
	if !ok {
		return
	}

	children, err := h.hierarchyService.GetChildrenContext(ctx, id)
	if err != nil {
		h.respondItemReadError(w, r, err)
		return
	}

	// Apply permission filtering
	filteredChildren, err := h.filterItemsByPermissions(user.ID, children)
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("permission check failed: %w", err))
		return
	}

	// Load labels
	if err := repository.NewLabelRepository(h.db).LoadForItemsContext(ctx, filteredChildren); err != nil {
		h.respondItemReadError(w, r, err)
		return
	}
	if err := LoadPersonalLabelsForItemsContext(ctx, h.db, filteredChildren, user.ID); err != nil {
		h.respondItemReadError(w, r, err)
		return
	}
	if err := repository.NewMilestoneAttachRepository(h.db).LoadForItemsContext(ctx, filteredChildren); err != nil {
		h.respondItemReadError(w, r, err)
		return
	}

	respondJSONOK(w, filteredChildren)
}
