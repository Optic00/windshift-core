package services

import (
	"context"
	"database/sql"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

// ErrApprovalNotFound is returned when an approval request or related approval resource is not found.
var ErrApprovalNotFound = sql.ErrNoRows

// ApprovalService is the asynchronous sibling of ConditionService. Where
// conditions/validators evaluate at transition time, approvals open a stateful
// request that one or more approvers decide over time.
//
// The lifecycle:
//
//  1. An item enters a status that has an approval_set_status row.
//  2. PerformTransition (after CommitTransition) calls RequestApproval, which
//     creates an approval_requests row, materializes step instances per the
//     approval_steps template, and snapshots the approver pool for the active
//     step (with on-leave handling via LeaveRepository).
//  3. Each approver POSTs /approvals/{id}/decide. ApprovalService.Decide records
//     a decision, advances/completes the step based on the configured quorum,
//     and on final outcome calls WorkflowService.CommitTransition with the
//     configured approve/deny transition's to_status_id.
//  4. If the user transitions out of the approval-bound status via a non-gated
//     transition, the pending request is canceled with reason "left_status".
//
// The configured approve/deny transitions cannot be invoked directly by users —
// PerformTransition rejects those attempts with code "approval_must_decide".
type ApprovalService struct {
	db              database.Database
	leaveRepo       *repository.LeaveRepository
	workflowService *WorkflowService

	runtimeRepo  *repository.ApprovalRepository
	templateRepo *repository.ApprovalSetRepository

	// eventCoordinator is set via SetEventCoordinator at startup; nil in tests
	// that exercise gating only and don't care about notifications.
	eventCoordinator *EventCoordinator
}

// NewApprovalService constructs an ApprovalService. EventCoordinator is wired
// via SetEventCoordinator after construction (mirrors CommentService pattern).
func NewApprovalService(db database.Database, leaveRepo *repository.LeaveRepository, workflowService *WorkflowService) *ApprovalService {
	return &ApprovalService{
		db:              db,
		leaveRepo:       leaveRepo,
		workflowService: workflowService,
		runtimeRepo:     repository.NewApprovalRepository(db),
		templateRepo:    repository.NewApprovalSetRepository(db),
	}
}

// SetEventCoordinator wires the EventCoordinator for emitting approval events.
func (s *ApprovalService) SetEventCoordinator(ec *EventCoordinator) {
	s.eventCoordinator = ec
}

// ----------------------------------------------------------------------------
// Resolution: which approval-set-status applies to an item?
// ----------------------------------------------------------------------------

// GetApprovalSetIDForItem mirrors ConditionService.GetConditionSetIDForItem:
// item-type override → workspace config-set default → global default.
// Returns (nil, nil) for personal workspaces or when no approval set is configured.
func (s *ApprovalService) GetApprovalSetIDForItem(ctx context.Context, workspaceID int, itemTypeID *int) (*int, error) {
	isPersonal, err := s.templateRepo.IsWorkspacePersonal(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if isPersonal {
		return nil, nil
	}
	return s.templateRepo.ResolveForWorkspace(ctx, workspaceID, itemTypeID)
}

// GetApprovalSetStatusForItem returns the approval_set_status (template row)
// that applies to an item entering statusID, or nil if no approval gates the entry.
func (s *ApprovalService) GetApprovalSetStatusForItem(ctx context.Context, workspaceID int, itemTypeID *int, statusID int) (*models.ApprovalSetStatus, error) {
	approvalSetID, err := s.GetApprovalSetIDForItem(ctx, workspaceID, itemTypeID)
	if err != nil {
		return nil, err
	}
	if approvalSetID == nil {
		return nil, nil
	}
	return s.templateRepo.FindActiveStatusBySetAndStatus(ctx, *approvalSetID, statusID)
}

// ----------------------------------------------------------------------------
// RequestApproval: open a new pending approval request.
// ----------------------------------------------------------------------------

// RequestApproval opens a new approval request for the item. The caller (typically
// PerformTransition's post-commit hook) is responsible for ensuring no pending
// request already exists; the unique partial index uq_approval_requests_one_open_per_item
// enforces this at the DB layer as defense in depth.
