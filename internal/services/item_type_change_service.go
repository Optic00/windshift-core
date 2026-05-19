package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/validation"
)

// ItemTypeChangeService owns the DB-touching logic for the item-type change
// workflow. The HTTP handler only orchestrates auth and response shaping;
// every direct query lives here so the handler boundary guard stays clean.
type ItemTypeChangeService struct {
	db               database.Database
	workflowService  *WorkflowService
	conditionService *ConditionService
	itemRepo         *repository.ItemRepository
	approvalSetRepo  *repository.ApprovalSetRepository
}

// NewItemTypeChangeService constructs a service. The optional condition service
// can be wired via WithConditionService so workspace-level overrides apply.
func NewItemTypeChangeService(db database.Database) *ItemTypeChangeService {
	return &ItemTypeChangeService{
		db:              db,
		workflowService: NewWorkflowService(db),
		itemRepo:        repository.NewItemRepository(db),
		approvalSetRepo: repository.NewApprovalSetRepository(db),
	}
}

// WithConditionService wires a shared ConditionService instance so cached
// condition-set lookups stay consistent across handlers.
func (s *ItemTypeChangeService) WithConditionService(cs *ConditionService) *ItemTypeChangeService {
	s.conditionService = cs
	return s
}

// ItemTypeChangeAnalysis describes the impact of changing an item's type, and
// is returned to clients to drive the migration UI.
type ItemTypeChangeAnalysis struct {
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
	AvailableStatuses      []StatusChangeInfo `json:"available_statuses"`
	CanChangeWithoutStatus bool               `json:"can_change_without_status"`
}

// StatusChangeInfo is the minimal status descriptor surfaced in an analysis.
type StatusChangeInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Analyze produces the analysis for an item against a target type.
func (s *ItemTypeChangeService) Analyze(item *models.Item, targetTypeID int) (*ItemTypeChangeAnalysis, error) {
	target, err := s.loadItemTypeTarget(targetTypeID)
	if err != nil {
		return nil, err
	}
	if err := s.validateItemTypeAllowedForWorkspace(item.WorkspaceID, targetTypeID); err != nil {
		return nil, err
	}
	if err := s.validateItemTypeHierarchyCompatibility(item, target.HierarchyLevel); err != nil {
		return nil, err
	}

	currentWorkflowID, err := s.workflowService.GetWorkflowIDForItem(item.WorkspaceID, item.ItemTypeID)
	if err != nil {
		return nil, err
	}
	targetIDCopy := targetTypeID
	targetWorkflowID, err := s.workflowService.GetWorkflowIDForItem(item.WorkspaceID, &targetIDCopy)
	if err != nil {
		return nil, err
	}

	resp := &ItemTypeChangeAnalysis{
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

	available, err := s.listWorkflowStatuses(*targetWorkflowID)
	if err != nil {
		return nil, err
	}
	resp.AvailableStatuses = available

	inTargetWorkflow, err := s.IsStatusInWorkflow(*item.StatusID, *targetWorkflowID)
	if err != nil {
		return nil, err
	}
	if inTargetWorkflow {
		return resp, nil
	}

	resp.RequiresMigration = true
	resp.CanChangeWithoutStatus = false
	suggestedID, suggestedName := s.suggestStatus(item.StatusName, *targetWorkflowID, available)
	resp.SuggestedStatusID = suggestedID
	resp.SuggestedStatusName = suggestedName
	return resp, nil
}

// IsStatusInWorkflow returns true when statusID participates in any transition
// of workflowID (either as origin or destination).
func (s *ItemTypeChangeService) IsStatusInWorkflow(statusID, workflowID int) (bool, error) {
	var exists bool
	err := s.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM workflow_transitions
			WHERE workflow_id = ? AND (from_status_id = ? OR to_status_id = ?)
		)
	`, workflowID, statusID, statusID).Scan(&exists)
	return exists, err
}

// ValidateStatusMapping rejects type changes that would bypass approval-bound
// or condition-gated transitions in the target workflow.
func (s *ItemTypeChangeService) ValidateStatusMapping(ctx context.Context, item *models.Item, targetTypeID int, targetWorkflowID, targetStatusID *int) error {
	if targetStatusID == nil || targetWorkflowID == nil || item.StatusID == nil {
		return nil
	}
	if *targetStatusID == *item.StatusID {
		return nil
	}

	pending, err := s.itemHasPendingApproval(item.ID)
	if err != nil {
		return err
	}
	if pending {
		return &validation.ValidationError{Field: "item_type_id", Message: "Cannot change item type while an approval is pending"}
	}

	approvalBound, err := s.statusIsApprovalBound(ctx, item.WorkspaceID, targetTypeID, *targetStatusID)
	if err != nil {
		return err
	}
	if approvalBound {
		return &validation.ValidationError{Field: "target_status_id", Message: "Target status requires approval in the target item type; item type change is blocked"}
	}

	initialStatusID, err := s.workflowService.GetInitialStatusID(*targetWorkflowID)
	if err != nil {
		return err
	}
	if initialStatusID != nil && *initialStatusID == *targetStatusID {
		return nil
	}

	transitionID, err := s.findWorkflowTransitionID(*targetWorkflowID, *item.StatusID, *targetStatusID)
	if err != nil {
		return err
	}
	if transitionID == nil {
		return &validation.ValidationError{Field: "target_status_id", Message: "Target status is not reachable by a direct transition in the target item type workflow"}
	}

	conditionSetID, err := s.resolveConditionSetIDForItemType(item.WorkspaceID, targetTypeID)
	if err != nil {
		return err
	}
	if conditionSetID != nil {
		hasConditions, err := s.transitionHasConditions(*conditionSetID, *transitionID)
		if err != nil {
			return err
		}
		if hasConditions {
			return &validation.ValidationError{Field: "target_status_id", Message: "Target status transition has conditions in the target item type; item type change is blocked"}
		}
	}

	return nil
}

// ApplyChange runs the item-type change atomically: it updates the item fields
// and records the history rows. Callers must have already validated inputs via
// Analyze + ValidateStatusMapping.
func (s *ItemTypeChangeService) ApplyChange(itemID, userID, targetTypeID int, nextStatusID *int, original *models.Item) ([]HistoryEntry, error) {
	now := time.Now()
	fields := map[string]interface{}{"item_type_id": targetTypeID}
	if nextStatusID != nil {
		fields["status_id"] = *nextStatusID
	}

	history := []HistoryEntry{{
		ItemID:    itemID,
		UserID:    userID,
		FieldName: "item_type_id",
		OldValue:  intPtrHistoryValue(original.ItemTypeID),
		NewValue:  fmt.Sprintf("%d", targetTypeID),
		ChangedAt: now,
	}}
	if nextStatusID != nil {
		history = append(history, HistoryEntry{
			ItemID:    itemID,
			UserID:    userID,
			FieldName: "status_id",
			OldValue:  intPtrHistoryValue(original.StatusID),
			NewValue:  fmt.Sprintf("%d", *nextStatusID),
			ChangedAt: now,
		})
	}

	err := database.WithTx(s.db, func(tx database.Tx) error {
		if err := s.itemRepo.UpdateFields(tx, itemID, fields); err != nil {
			return err
		}
		repoEntries := make([]repository.HistoryEntry, len(history))
		for i, h := range history {
			repoEntries[i] = repository.HistoryEntry{
				ItemID:    h.ItemID,
				UserID:    h.UserID,
				FieldName: h.FieldName,
				OldValue:  h.OldValue,
				NewValue:  h.NewValue,
				ChangedAt: h.ChangedAt,
			}
		}
		if err := s.itemRepo.RecordHistoryBatch(tx, repoEntries); err != nil {
			return fmt.Errorf("record item history: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return history, nil
}

type itemTypeTargetDetails struct {
	ID             int
	Name           string
	HierarchyLevel int
}

func (s *ItemTypeChangeService) loadItemTypeTarget(id int) (*itemTypeTargetDetails, error) {
	var out itemTypeTargetDetails
	err := s.db.QueryRow(`SELECT id, name, COALESCE(hierarchy_level, 0) FROM item_types WHERE id = ?`, id).Scan(&out.ID, &out.Name, &out.HierarchyLevel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &validation.ValidationError{Field: "target_item_type_id", Message: "Item type not found"}
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *ItemTypeChangeService) validateItemTypeAllowedForWorkspace(workspaceID, targetTypeID int) error {
	var configSetID sql.NullInt64
	err := s.db.QueryRow(`SELECT configuration_set_id FROM workspace_configuration_sets WHERE workspace_id = ?`, workspaceID).Scan(&configSetID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !configSetID.Valid {
		return nil
	}

	var configuredCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM configuration_set_item_types WHERE configuration_set_id = ?`, configSetID.Int64).Scan(&configuredCount); err != nil {
		return err
	}
	if configuredCount == 0 {
		return nil
	}

	var allowed bool
	if err := s.db.QueryRow(`
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

func (s *ItemTypeChangeService) validateItemTypeHierarchyCompatibility(item *models.Item, targetLevel int) error {
	if item.ParentID != nil {
		var parentLevel sql.NullInt64
		err := s.db.QueryRow(`
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
	if err := s.db.QueryRow(`
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

func (s *ItemTypeChangeService) listWorkflowStatuses(workflowID int) ([]StatusChangeInfo, error) {
	rows, err := s.db.Query(`
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

	out := []StatusChangeInfo{}
	for rows.Next() {
		var info StatusChangeInfo
		if err := rows.Scan(&info.ID, &info.Name); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

func (s *ItemTypeChangeService) suggestStatus(currentName string, workflowID int, available []StatusChangeInfo) (statusID *int, statusName string) {
	currentNorm := strings.ToLower(strings.TrimSpace(currentName))
	for _, status := range available {
		if strings.ToLower(strings.TrimSpace(status.Name)) == currentNorm && currentNorm != "" {
			id := status.ID
			return &id, status.Name
		}
	}
	initialID, err := s.workflowService.GetInitialStatusID(workflowID)
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

func (s *ItemTypeChangeService) itemHasPendingApproval(itemID int) (bool, error) {
	var exists bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM approval_requests WHERE item_id = ? AND status = 'pending')`, itemID).Scan(&exists)
	return exists, err
}

func (s *ItemTypeChangeService) statusIsApprovalBound(ctx context.Context, workspaceID, itemTypeID, statusID int) (bool, error) {
	itemTypePtr := itemTypeID
	approvalSetID, err := s.approvalSetRepo.ResolveForWorkspace(ctx, workspaceID, &itemTypePtr)
	if err != nil || approvalSetID == nil {
		return false, err
	}
	var exists bool
	err = s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM approval_set_statuses
			WHERE approval_set_id = ? AND status_id = ? AND is_active = true
		)
	`, *approvalSetID, statusID).Scan(&exists)
	return exists, err
}

func (s *ItemTypeChangeService) resolveConditionSetIDForItemType(workspaceID, itemTypeID int) (*int, error) {
	id := itemTypeID
	if s.conditionService != nil {
		return s.conditionService.GetConditionSetIDForItem(workspaceID, &id)
	}
	return NewConditionService(s.db, nil, nil).GetConditionSetIDForItem(workspaceID, &id)
}

func (s *ItemTypeChangeService) findWorkflowTransitionID(workflowID, fromStatusID, toStatusID int) (*int, error) {
	var id int
	err := s.db.QueryRow(`
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

func (s *ItemTypeChangeService) transitionHasConditions(conditionSetID, transitionID int) (bool, error) {
	var exists bool
	err := s.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1
			FROM condition_set_transitions cst
			JOIN conditions c ON c.condition_set_transition_id = cst.id
			WHERE cst.condition_set_id = ? AND cst.transition_id = ?
		)
	`, conditionSetID, transitionID).Scan(&exists)
	return exists, err
}

func intPtrHistoryValue(value *int) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%d", *value)
}
