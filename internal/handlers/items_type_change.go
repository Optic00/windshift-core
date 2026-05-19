package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
	"windshift/internal/validation"
)

type itemTypeChangeAnalysisResponse struct {
	ItemID                 int                `json:"item_id"`
	CurrentItemTypeID      *int               `json:"current_item_type_id"`
	CurrentItemTypeName    string             `json:"current_item_type_name"`
	TargetItemTypeID       int                `json:"target_item_type_id"`
	TargetItemTypeName     string             `json:"target_item_type_name"`
	CurrentWorkflowID      *int               `json:"current_workflow_id"`
	TargetWorkflowID       *int               `json:"target_workflow_id"`
	CurrentStatusID        *int               `json:"current_status_id"`
	CurrentStatusName      string             `json:"current_status_name"`
	RequiresMigration      bool               `json:"requires_migration"`
	SuggestedStatusID      *int               `json:"suggested_status_id,omitempty"`
	SuggestedStatusName    string             `json:"suggested_status_name,omitempty"`
	AvailableStatuses      []statusChangeInfo `json:"available_statuses"`
	CanChangeWithoutStatus bool               `json:"can_change_without_status"`
}

type statusChangeInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type itemTypeChangeRequest struct {
	TargetItemTypeID int  `json:"target_item_type_id"`
	TargetStatusID   *int `json:"target_status_id,omitempty"`
}

// AnalyzeTypeChange reports whether changing an item's item type would leave
// its current status outside the target type's effective workflow.
func (h *ItemHandler) AnalyzeTypeChange(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	targetID, err := strconvParam(r, "target_item_type_id")
	if err != nil {
		respondValidationError(w, r, "target_item_type_id is required")
		return
	}

	itemRepo := repository.NewItemRepository(h.db)
	item, err := itemRepo.FindByIDWithDetails(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	canEdit, err := h.canEditItem(user.ID, item.WorkspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canEdit {
		respondNotFound(w, r, "Item")
		return
	}

	analysis, err := h.buildItemTypeChangeAnalysis(item, targetID)
	if err != nil {
		h.respondItemTypeChangeError(w, r, err)
		return
	}

	respondJSONOK(w, analysis)
}

// ChangeType changes an item's item type. If the target type's effective
// workflow does not contain the current status, callers must provide a target
// status. The status mapping is deliberately conservative: it may not bypass a
// direct condition-gated transition or enter an approval-bound status.
func (h *ItemHandler) ChangeType(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	req, ok := decodeJSON[itemTypeChangeRequest](w, r)
	if !ok {
		return
	}
	if req.TargetItemTypeID <= 0 {
		respondValidationError(w, r, "target_item_type_id is required")
		return
	}

	itemRepo := repository.NewItemRepository(h.db)
	originalItem, err := itemRepo.FindByIDWithDetails(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	canEdit, err := h.canEditItem(user.ID, originalItem.WorkspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canEdit {
		respondNotFound(w, r, "Item")
		return
	}

	analysis, err := h.buildItemTypeChangeAnalysis(originalItem, req.TargetItemTypeID)
	if err != nil {
		h.respondItemTypeChangeError(w, r, err)
		return
	}
	if sameIntPtrValue(originalItem.ItemTypeID, req.TargetItemTypeID) && !analysis.RequiresMigration {
		respondJSONOK(w, originalItem)
		return
	}

	var nextStatusID *int
	if analysis.RequiresMigration {
		if req.TargetStatusID == nil {
			respondJSON(w, http.StatusConflict, map[string]interface{}{
				"error":    "migration_required",
				"message":  "A target status is required before changing this item type",
				"analysis": analysis,
			})
			return
		}
		if analysis.TargetWorkflowID != nil {
			inWorkflow, err := h.statusIsInWorkflowID(*req.TargetStatusID, *analysis.TargetWorkflowID)
			if err != nil {
				respondInternalError(w, r, err)
				return
			}
			if !inWorkflow {
				respondValidationError(w, r, "target_status_id is not part of the target item type workflow")
				return
			}
		}
		if err := h.validateItemTypeStatusMapping(r.Context(), originalItem, req.TargetItemTypeID, analysis.TargetWorkflowID, req.TargetStatusID); err != nil {
			h.respondItemTypeChangeError(w, r, err)
			return
		}
		nextStatusID = req.TargetStatusID
	}

	if h.activityTracker != nil {
		if err := h.activityTracker.TrackItemActivity(user.ID, id, services.ActivityEdit); err != nil {
			slog.Warn("failed to track item edit activity", slog.Int("user_id", user.ID), slog.Int("item_id", id), slog.Any("error", err))
		}
	}

	tx, err := h.db.Begin()
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now()
	fields := map[string]interface{}{"item_type_id": req.TargetItemTypeID}
	if nextStatusID != nil {
		fields["status_id"] = *nextStatusID
	}
	if err := itemRepo.UpdateFields(tx, id, fields); err != nil {
		respondInternalError(w, r, err)
		return
	}

	history := []services.HistoryEntry{{
		ItemID:    id,
		UserID:    user.ID,
		FieldName: "item_type_id",
		OldValue:  intPtrHistoryValue(originalItem.ItemTypeID),
		NewValue:  fmt.Sprintf("%d", req.TargetItemTypeID),
		ChangedAt: now,
	}}
	if nextStatusID != nil {
		history = append(history, services.HistoryEntry{
			ItemID:    id,
			UserID:    user.ID,
			FieldName: "status_id",
			OldValue:  intPtrHistoryValue(originalItem.StatusID),
			NewValue:  fmt.Sprintf("%d", *nextStatusID),
			ChangedAt: now,
		})
	}
	if err := recordItemHistoryEntries(tx, history); err != nil {
		respondInternalError(w, r, err)
		return
	}

	if err := tx.Commit(); err != nil {
		respondInternalError(w, r, err)
		return
	}

	updatedItem, err := itemRepo.FindByIDWithDetails(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	statusChanged := nextStatusID != nil && !intPtrEqual(originalItem.StatusID, updatedItem.StatusID)
	if h.eventCoordinator != nil {
		h.eventCoordinator.EmitItemUpdated(originalItem, updatedItem, statusChanged, false, user.ID, history, user.Username)
	}
	if h.issueSyncService != nil && statusChanged && updatedItem.StatusID != nil {
		go func(statusID int) {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
			defer cancel()
			h.issueSyncService.PushStatusToGitHub(ctx, updatedItem.ID, statusID)
		}(*updatedItem.StatusID)
	}

	respondJSONOK(w, updatedItem)
}

func (h *ItemHandler) buildItemTypeChangeAnalysis(item *models.Item, targetTypeID int) (*itemTypeChangeAnalysisResponse, error) {
	target, err := h.loadItemTypeTarget(targetTypeID)
	if err != nil {
		return nil, err
	}
	if err := h.validateItemTypeAllowedForWorkspace(item.WorkspaceID, targetTypeID); err != nil {
		return nil, err
	}
	if err := h.validateItemTypeHierarchyCompatibility(item, target.HierarchyLevel); err != nil {
		return nil, err
	}

	workflowSvc := services.NewWorkflowService(h.db)
	currentWorkflowID, err := workflowSvc.GetWorkflowIDForItem(item.WorkspaceID, item.ItemTypeID)
	if err != nil {
		return nil, err
	}
	targetIDCopy := targetTypeID
	targetWorkflowID, err := workflowSvc.GetWorkflowIDForItem(item.WorkspaceID, &targetIDCopy)
	if err != nil {
		return nil, err
	}

	resp := &itemTypeChangeAnalysisResponse{
		ItemID:                 item.ID,
		CurrentItemTypeID:      item.ItemTypeID,
		CurrentItemTypeName:    item.ItemTypeName,
		TargetItemTypeID:       target.ID,
		TargetItemTypeName:     target.Name,
		CurrentWorkflowID:      currentWorkflowID,
		TargetWorkflowID:       targetWorkflowID,
		CurrentStatusID:        item.StatusID,
		CurrentStatusName:      item.StatusName,
		CanChangeWithoutStatus: true,
	}

	if targetWorkflowID == nil || item.StatusID == nil {
		return resp, nil
	}

	available, err := h.listWorkflowStatuses(*targetWorkflowID)
	if err != nil {
		return nil, err
	}
	resp.AvailableStatuses = available

	inTargetWorkflow, err := h.statusIsInWorkflowID(*item.StatusID, *targetWorkflowID)
	if err != nil {
		return nil, err
	}
	if inTargetWorkflow {
		return resp, nil
	}

	resp.RequiresMigration = true
	resp.CanChangeWithoutStatus = false
	suggestedID, suggestedName := h.suggestItemTypeChangeStatus(item.StatusName, *targetWorkflowID, available)
	resp.SuggestedStatusID = suggestedID
	resp.SuggestedStatusName = suggestedName
	return resp, nil
}

func (h *ItemHandler) validateItemTypeStatusMapping(ctx context.Context, item *models.Item, targetTypeID int, targetWorkflowID, targetStatusID *int) error {
	if targetStatusID == nil || targetWorkflowID == nil || item.StatusID == nil {
		return nil
	}
	if *targetStatusID == *item.StatusID {
		return nil
	}

	pending, err := h.itemHasPendingApproval(item.ID)
	if err != nil {
		return err
	}
	if pending {
		return &validation.ValidationError{Field: "item_type_id", Message: "Cannot change item type while an approval is pending"}
	}

	approvalBound, err := h.statusIsApprovalBound(ctx, item.WorkspaceID, targetTypeID, *targetStatusID)
	if err != nil {
		return err
	}
	if approvalBound {
		return &validation.ValidationError{Field: "target_status_id", Message: "Target status requires approval in the target item type; item type change is blocked"}
	}

	initialStatusID, err := services.NewWorkflowService(h.db).GetInitialStatusID(*targetWorkflowID)
	if err != nil {
		return err
	}
	if initialStatusID != nil && *initialStatusID == *targetStatusID {
		return nil
	}

	transitionID, err := h.findWorkflowTransitionID(*targetWorkflowID, *item.StatusID, *targetStatusID)
	if err != nil {
		return err
	}
	if transitionID == nil {
		return &validation.ValidationError{Field: "target_status_id", Message: "Target status is not reachable by a direct transition in the target item type workflow"}
	}

	conditionSetID, err := h.resolveConditionSetIDForItemType(item.WorkspaceID, targetTypeID)
	if err != nil {
		return err
	}
	if conditionSetID != nil {
		hasConditions, err := h.transitionHasConditions(*conditionSetID, *transitionID)
		if err != nil {
			return err
		}
		if hasConditions {
			return &validation.ValidationError{Field: "target_status_id", Message: "Target status transition has conditions in the target item type; item type change is blocked"}
		}
	}

	return nil
}

type itemTypeTargetDetails struct {
	ID             int
	Name           string
	HierarchyLevel int
}

func (h *ItemHandler) loadItemTypeTarget(id int) (*itemTypeTargetDetails, error) {
	var out itemTypeTargetDetails
	err := h.db.QueryRow(`SELECT id, name, COALESCE(hierarchy_level, 0) FROM item_types WHERE id = ?`, id).Scan(&out.ID, &out.Name, &out.HierarchyLevel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &validation.ValidationError{Field: "target_item_type_id", Message: "Item type not found"}
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (h *ItemHandler) validateItemTypeAllowedForWorkspace(workspaceID, targetTypeID int) error {
	var configSetID sql.NullInt64
	err := h.db.QueryRow(`SELECT configuration_set_id FROM workspace_configuration_sets WHERE workspace_id = ?`, workspaceID).Scan(&configSetID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !configSetID.Valid {
		return nil
	}

	var configuredCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM configuration_set_item_types WHERE configuration_set_id = ?`, configSetID.Int64).Scan(&configuredCount); err != nil {
		return err
	}
	if configuredCount == 0 {
		return nil
	}

	var allowed bool
	if err := h.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM configuration_set_item_types
			WHERE configuration_set_id = ? AND item_type_id = ?
		)
	`, configSetID.Int64, targetTypeID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return &validation.ValidationError{Field: "target_item_type_id", Message: "Item type is not allowed in this workspace"}
	}
	return nil
}

func (h *ItemHandler) validateItemTypeHierarchyCompatibility(item *models.Item, targetLevel int) error {
	if item.ParentID != nil {
		var parentLevel sql.NullInt64
		err := h.db.QueryRow(`
			SELECT it.hierarchy_level
			FROM items p
			LEFT JOIN item_types it ON p.item_type_id = it.id
			WHERE p.id = ?
		`, *item.ParentID).Scan(&parentLevel)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if parentLevel.Valid && targetLevel != int(parentLevel.Int64)+1 {
			return &validation.ValidationError{Field: "target_item_type_id", Message: "Item type is not compatible with the current parent hierarchy"}
		}
	}

	var incompatibleChildren int
	if err := h.db.QueryRow(`
		SELECT COUNT(*)
		FROM items c
		JOIN item_types it ON c.item_type_id = it.id
		WHERE c.parent_id = ? AND it.hierarchy_level != ?
	`, item.ID, targetLevel+1).Scan(&incompatibleChildren); err != nil {
		return err
	}
	if incompatibleChildren > 0 {
		return &validation.ValidationError{Field: "target_item_type_id", Message: "Item type is not compatible with existing child hierarchy"}
	}
	return nil
}

func (h *ItemHandler) statusIsInWorkflowID(statusID, workflowID int) (bool, error) {
	var exists bool
	err := h.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM workflow_transitions
			WHERE workflow_id = ? AND (from_status_id = ? OR to_status_id = ?)
		)
	`, workflowID, statusID, statusID).Scan(&exists)
	return exists, err
}

func (h *ItemHandler) listWorkflowStatuses(workflowID int) ([]statusChangeInfo, error) {
	rows, err := h.db.Query(`
		SELECT DISTINCT s.id, s.name
		FROM workflow_transitions wt
		JOIN statuses s ON wt.from_status_id = s.id OR wt.to_status_id = s.id
		WHERE wt.workflow_id = ?
		ORDER BY s.name
	`, workflowID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []statusChangeInfo{}
	for rows.Next() {
		var s statusChangeInfo
		if err := rows.Scan(&s.ID, &s.Name); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (h *ItemHandler) suggestItemTypeChangeStatus(currentName string, workflowID int, available []statusChangeInfo) (statusID *int, statusName string) {
	currentNorm := strings.ToLower(strings.TrimSpace(currentName))
	for _, status := range available {
		if strings.ToLower(strings.TrimSpace(status.Name)) == currentNorm && currentNorm != "" {
			id := status.ID
			return &id, status.Name
		}
	}
	initialID, err := services.NewWorkflowService(h.db).GetInitialStatusID(workflowID)
	if err == nil && initialID != nil {
		for _, status := range available {
			if status.ID == *initialID {
				id := status.ID
				return &id, status.Name
			}
		}
	}
	return nil, ""
}

func (h *ItemHandler) itemHasPendingApproval(itemID int) (bool, error) {
	var exists bool
	err := h.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM approval_requests WHERE item_id = ? AND status = 'pending')`, itemID).Scan(&exists)
	return exists, err
}

func (h *ItemHandler) statusIsApprovalBound(ctx context.Context, workspaceID, itemTypeID, statusID int) (bool, error) {
	itemTypePtr := itemTypeID
	approvalSetID, err := repository.NewApprovalSetRepository(h.db).ResolveForWorkspace(ctx, workspaceID, &itemTypePtr)
	if err != nil || approvalSetID == nil {
		return false, err
	}
	var exists bool
	err = h.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM approval_set_statuses
			WHERE approval_set_id = ? AND status_id = ? AND is_active = true
		)
	`, *approvalSetID, statusID).Scan(&exists)
	return exists, err
}

func (h *ItemHandler) resolveConditionSetIDForItemType(workspaceID, itemTypeID int) (*int, error) {
	if h.conditionService != nil {
		id := itemTypeID
		return h.conditionService.GetConditionSetIDForItem(workspaceID, &id)
	}
	conditionService := services.NewConditionService(h.db, nil, nil)
	id := itemTypeID
	return conditionService.GetConditionSetIDForItem(workspaceID, &id)
}

func (h *ItemHandler) findWorkflowTransitionID(workflowID, fromStatusID, toStatusID int) (*int, error) {
	var id int
	err := h.db.QueryRow(`
		SELECT id FROM workflow_transitions
		WHERE workflow_id = ? AND from_status_id = ? AND to_status_id = ?
	`, workflowID, fromStatusID, toStatusID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (h *ItemHandler) transitionHasConditions(conditionSetID, transitionID int) (bool, error) {
	var exists bool
	err := h.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1
			FROM condition_set_transitions cst
			JOIN conditions c ON c.condition_set_transition_id = cst.id
			WHERE cst.condition_set_id = ? AND cst.transition_id = ?
		)
	`, conditionSetID, transitionID).Scan(&exists)
	return exists, err
}

func (h *ItemHandler) respondItemTypeChangeError(w http.ResponseWriter, r *http.Request, err error) {
	var valErr *validation.ValidationError
	if errors.As(err, &valErr) {
		respondValidationError(w, r, valErr.Error())
		return
	}
	respondInternalError(w, r, err)
}

func recordItemHistoryEntries(tx repositoryTx, entries []services.HistoryEntry) error {
	for _, entry := range entries {
		if _, err := tx.Exec(`
			INSERT INTO item_history (item_id, user_id, field_name, old_value, new_value, changed_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, entry.ItemID, entry.UserID, entry.FieldName, entry.OldValue, entry.NewValue, entry.ChangedAt); err != nil {
			return fmt.Errorf("record item history: %w", err)
		}
	}
	return nil
}

type repositoryTx interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

func strconvParam(r *http.Request, key string) (int, error) {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return 0, fmt.Errorf("missing %s", key)
	}
	out, err := strconv.Atoi(value)
	if err != nil || out <= 0 {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return out, nil
}

func sameIntPtrValue(ptr *int, value int) bool {
	return ptr != nil && *ptr == value
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func intPtrHistoryValue(value *int) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%d", *value)
}
