package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"windshift/internal/repository"
	"windshift/internal/services"
)

// GetAvailableStatusTransitions returns the valid status transitions for a work item
func (h *ItemHandler) GetAvailableStatusTransitions(w http.ResponseWriter, r *http.Request) {
	itemID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Get the item to find its current status, workspace, and item type
	item, err := repository.NewItemRepository(h.db).FindByID(itemID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}
	workspaceID := item.WorkspaceID
	currentStatusID := item.StatusID
	itemTypeIDPtr := item.ItemTypeID

	// Check if user has permission to view this item's workspace
	canView, permErr := h.canViewItem(user.ID, workspaceID)
	if permErr != nil {
		respondInternalError(w, r, permErr)
		return
	}
	if !canView {
		respondNotFound(w, r, "Item")
		return
	}

	workflowService := services.NewWorkflowService(h.db)

	// Get current status name for response
	currentStatusName := ""
	if currentStatusID != nil {
		currentStatusName, err = workflowService.GetStatusName(int64(*currentStatusID))
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
	}

	// Get the workflow using WorkflowService (considers item type override)
	workflowID, err := workflowService.GetWorkflowIDForItem(workspaceID, itemTypeIDPtr)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// No workflow configured - return empty transitions
	if workflowID == nil {
		response := map[string]interface{}{
			"current_status":        currentStatusName,
			"available_transitions": []map[string]interface{}{},
		}
		respondJSONOK(w, response)
		return
	}

	// Build the list of available transitions
	availableTransitions := []map[string]interface{}{}
	var pendingApproval *services.PendingApprovalSummary

	// Always include current status first
	if currentStatusID != nil {
		currentOption, optionErr := workflowService.GetStatusTransitionOption(int64(*currentStatusID))
		if optionErr != nil {
			respondInternalError(w, r, optionErr)
			return
		}
		if currentOption != nil {
			availableTransitions = append(availableTransitions, transitionOptionResponse(*currentOption))
		}
	}

	// Get valid transitions from current status
	if currentStatusID != nil {
		rawTransitions, listErr := workflowService.ListAvailableTransitionOptions(*workflowID, int64(*currentStatusID))
		if listErr != nil {
			respondInternalError(w, r, listErr)
			return
		}

		// Apply approval gating: drop transitions whose ID is the approve or
		// deny target of an in-flight approval on this item.
		if h.approvalService != nil {
			gatedIDs, summary, gErr := h.approvalService.GetGatedTransitionsForItem(r.Context(), itemID, user.ID)
			if gErr != nil {
				slog.Warn("approval gating lookup failed, returning unfiltered transitions",
					slog.Int("item_id", itemID),
					slog.Any("error", gErr))
			} else if len(gatedIDs) > 0 {
				gated := map[int]bool{}
				for _, id := range gatedIDs {
					gated[id] = true
				}
				kept := rawTransitions[:0]
				for _, rt := range rawTransitions {
					if !gated[rt.TransitionID] {
						kept = append(kept, rt)
					}
				}
				rawTransitions = kept
			}
			pendingApproval = summary
		}

		// Apply condition filtering if condition service is available
		if h.conditionService != nil {
			conditionSetID, csErr := h.conditionService.GetConditionSetIDForItem(workspaceID, itemTypeIDPtr)
			if csErr == nil && conditionSetID != nil {
				// Build item context for condition evaluation
				itemCtx := services.BuildItemContextFromIDs(h.db, itemID, workspaceID, currentStatusID, itemTypeIDPtr)

				// Convert to TransitionWithID for filtering
				var twids []services.TransitionWithID
				for _, rt := range rawTransitions {
					color := ""
					if rt.CategoryColor != nil {
						color = *rt.CategoryColor
					}
					twids = append(twids, services.TransitionWithID{
						TransitionID:  rt.TransitionID,
						StatusID:      rt.StatusID,
						StatusName:    rt.StatusName,
						CategoryColor: color,
					})
				}

				filtered, filterErr := h.conditionService.FilterTransitionsByConditions(
					r.Context(), *conditionSetID, twids, user.ID, itemCtx,
				)
				if filterErr != nil {
					slog.Warn("condition filtering failed, returning unfiltered transitions",
						slog.Int("item_id", itemID),
						slog.Int("condition_set_id", *conditionSetID),
						slog.Any("error", filterErr))
				} else {
					// Rebuild rawTransitions from filtered results
					rawTransitions = nil
					for _, f := range filtered {
						var categoryColor *string
						if f.CategoryColor != "" {
							color := f.CategoryColor
							categoryColor = &color
						}
						rawTransitions = append(rawTransitions, services.StatusTransitionOption{
							TransitionID:  f.TransitionID,
							StatusID:      f.StatusID,
							StatusName:    f.StatusName,
							CategoryColor: categoryColor,
						})
					}
				}
			}
		}

		// Track IDs we've already added to avoid duplicates.
		// currentStatusID is non-nil in this block.
		addedIDs := map[int]bool{*currentStatusID: true}

		for _, rt := range rawTransitions {
			if !addedIDs[rt.StatusID] {
				availableTransitions = append(availableTransitions, transitionOptionResponse(rt))
				addedIDs[rt.StatusID] = true
			}
		}
	}

	response := map[string]interface{}{
		"current_status":        currentStatusName,
		"available_transitions": availableTransitions,
		"pending_approval":      pendingApproval,
	}

	respondJSONOK(w, response)
}

// GetWorkspaceTransitionMatrix returns the allowed status transitions for every
// (item_type_id, status_id) pair in a workspace, keyed "<itemTypeID>:<statusID>".
// It backs the board's transition preload, which otherwise fired one
// GET /items/{id}/available-status-transitions per unique (item type, status)
// pair — many concurrent requests on a board spanning several types/statuses.
//
// The matrix is for DISPLAY of candidate transitions only: it deliberately
// omits per-item approval gating and condition filtering (both item-specific),
// which still apply when an actual transition is performed. Each pair's value
// matches the per-item endpoint's available_transitions shape (current status
// first, then reachable statuses).
func (h *ItemHandler) GetWorkspaceTransitionMatrix(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	canView, permErr := h.canViewItem(user.ID, workspaceID)
	if permErr != nil {
		respondInternalError(w, r, permErr)
		return
	}
	if !canView {
		respondNotFound(w, r, "Workspace")
		return
	}

	itemTypes, err := services.NewConfigReadService(h.db).ListItemTypes()
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	statuses, err := services.NewWorkspaceService(h.db).GetStatuses(workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	workflowService := services.NewWorkflowService(h.db)

	// Transitions depend only on (workflow, fromStatus), so compute once per
	// workflow and replicate across every item type that resolves to it.
	perWorkflow := map[int]map[int][]map[string]interface{}{}
	computeForWorkflow := func(workflowID int) map[int][]map[string]interface{} {
		if cached, ok := perWorkflow[workflowID]; ok {
			return cached
		}
		byStatus := make(map[int][]map[string]interface{}, len(statuses))
		for _, st := range statuses {
			options := []map[string]interface{}{}
			added := map[int]bool{st.ID: true}
			// Current status first (matches the per-item endpoint).
			if current, optErr := workflowService.GetStatusTransitionOption(int64(st.ID)); optErr == nil && current != nil {
				options = append(options, transitionOptionResponse(*current))
			}
			raw, listErr := workflowService.ListAvailableTransitionOptions(workflowID, int64(st.ID))
			if listErr == nil {
				for _, rt := range raw {
					if !added[rt.StatusID] {
						options = append(options, transitionOptionResponse(rt))
						added[rt.StatusID] = true
					}
				}
			}
			byStatus[st.ID] = options
		}
		perWorkflow[workflowID] = byStatus
		return byStatus
	}

	transitions := map[string][]map[string]interface{}{}
	for _, it := range itemTypes {
		itemTypeID := it.ID
		workflowID, wfErr := workflowService.GetWorkflowIDForItem(workspaceID, &itemTypeID)
		if wfErr != nil || workflowID == nil {
			continue
		}
		for statusID, options := range computeForWorkflow(*workflowID) {
			transitions[strconv.Itoa(itemTypeID)+":"+strconv.Itoa(statusID)] = options
		}
	}

	respondJSONOK(w, map[string]interface{}{"transitions": transitions})
}

func transitionOptionResponse(option services.StatusTransitionOption) map[string]interface{} {
	transition := map[string]interface{}{
		"id":    option.StatusID,
		"name":  option.StatusName,
		"value": strings.ToLower(strings.ReplaceAll(option.StatusName, " ", "_")),
	}
	if option.CategoryColor != nil {
		transition["category_color"] = *option.CategoryColor
	}
	return transition
}
