package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

func (s *ApprovalService) Escalate(ctx context.Context, stepInstanceID, actorUserID int, reason string) error {
	itemRepo := repository.NewItemRepository(s.db)

	type escalateOutcome struct {
		ran                        bool
		action                     string
		newApproverIDs             []int
		newlyStartedStepInstanceID *int
		stepInstanceID             int
		requestID                  int
	}

	outcome, err := database.WithTxResult(s.db, func(tx database.Tx) (escalateOutcome, error) {
		var out escalateOutcome
		si, err := s.runtimeRepo.LoadStepInstanceByIDInTx(ctx, tx, stepInstanceID)
		if errors.Is(err, repository.ErrNotFound) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		if si.Status != models.ApprovalStepStatusPending {
			return out, nil
		}

		step, err := s.templateRepo.FindStepByIDInTx(ctx, tx, si.ApprovalStepID)
		if err != nil {
			return out, err
		}

		req, err := s.runtimeRepo.LoadRequestByIDInTx(ctx, tx, si.ApprovalRequestID)
		if err != nil {
			return out, err
		}
		if req.Status != models.ApprovalRequestStatusPending {
			return out, nil
		}

		action := step.EscalationAction
		if action == "" {
			action = models.ApprovalEscalationActionReassign
		}

		var actor *int
		if actorUserID > 0 {
			actor = &actorUserID
		}

		switch action {
		case models.ApprovalEscalationActionReassign:
			newApproverIDs, err := s.escalateReassign(ctx, tx, si, step, req, actor, reason)
			if err != nil {
				return out, err
			}
			out.ran = true
			out.action = action
			out.newApproverIDs = newApproverIDs
			out.stepInstanceID = si.ID
			out.requestID = req.ID
			return out, nil

		case models.ApprovalEscalationActionSkipStep:
			if err := s.runtimeRepo.MarkStepEscalated(ctx, tx, si.ID, models.ApprovalStepStatusEscalated); err != nil {
				return out, err
			}
			if _, err := s.runtimeRepo.WriteDecision(ctx, tx, req.ID, &si.ID, actor, nil, models.ApprovalDecisionEscalate, "", nil, map[string]any{
				"reason":     reason,
				"action":     action,
				"resolution": "skip_step",
			}); err != nil {
				return out, err
			}
			newID, err := s.advanceRequestAfterStep(ctx, tx, req, si, models.ApprovalStepStatusApproved, actorUserID, itemRepo)
			if err != nil {
				return out, err
			}
			out.ran = true
			out.action = action
			out.newlyStartedStepInstanceID = newID
			out.requestID = req.ID
			return out, nil

		case models.ApprovalEscalationActionAutoReject:
			if err := s.runtimeRepo.MarkStepEscalated(ctx, tx, si.ID, models.ApprovalStepStatusRejected); err != nil {
				return out, err
			}
			if _, err := s.runtimeRepo.WriteDecision(ctx, tx, req.ID, &si.ID, actor, nil, models.ApprovalDecisionEscalate, "", nil, map[string]any{
				"reason":     reason,
				"action":     action,
				"resolution": "auto_reject",
			}); err != nil {
				return out, err
			}
			if _, err := s.advanceRequestAfterStep(ctx, tx, req, si, models.ApprovalStepStatusRejected, actorUserID, itemRepo); err != nil {
				return out, err
			}
			out.ran = true
			out.action = action
			out.requestID = req.ID
			return out, nil
		}
		return out, fmt.Errorf("unknown escalation_action %q", action)
	})
	if err != nil {
		return err
	}
	if !outcome.ran {
		return nil
	}

	if s.eventCoordinator == nil {
		return nil
	}
	switch outcome.action {
	case models.ApprovalEscalationActionReassign:
		req, _ := s.GetRequest(ctx, outcome.requestID)
		if req == nil {
			return nil
		}
		item, _ := itemRepo.FindByIDWithDetails(req.ItemID)
		if item != nil {
			for i := range req.StepInstances {
				if req.StepInstances[i].ID == outcome.stepInstanceID {
					s.eventCoordinator.EmitApprovalEscalated(req, &req.StepInstances[i], outcome.action, outcome.newApproverIDs, item, actorUserID)
					break
				}
			}
		}
	default:
		s.emitEscalationCompletion(ctx, outcome.requestID, outcome.action, outcome.newlyStartedStepInstanceID, actorUserID)
	}
	return nil
}

// escalateReassign swaps the approver pool to the configured escalation target.
// Returns the list of newly-active approver user IDs.
func (s *ApprovalService) escalateReassign(ctx context.Context, tx database.Tx, si *models.ApprovalStepInstance, step *models.ApprovalStep, req *models.ApprovalRequest, actor *int, reason string) ([]int, error) {
	if step.EscalationTargetSource == "" {
		return nil, fmt.Errorf("escalation_action=reassign requires escalation_target_source")
	}

	priorPool, err := s.runtimeRepo.LoadActiveApproverUserIDs(ctx, tx, si.ID)
	if err != nil {
		return nil, err
	}

	if err := s.runtimeRepo.DeactivateApprovers(ctx, tx, si.ID); err != nil {
		return nil, err
	}

	// Resolve the escalation target as if it were the approver_source. Reuse
	// resolveApproverSource by rewriting the step into a target-shaped probe.
	probe := *step
	probe.ApproverSource = step.EscalationTargetSource
	probe.ApproverFieldIdentifier = step.EscalationTargetFieldIdentifier
	probe.ApproverFieldID = step.EscalationTargetFieldID
	probe.ApproverRoleID = step.EscalationTargetRoleID
	probe.ApproverGroupID = step.EscalationTargetGroupID
	probe.ApproverUserID = step.EscalationTargetUserID

	itemRepo := repository.NewItemRepository(s.db)
	item, err := itemRepo.FindByID(req.ItemID)
	if err != nil {
		return nil, fmt.Errorf("reload item: %w", err)
	}
	if err := s.resolveAndSnapshotApprovers(ctx, tx, si.ID, probe, item, req.TriggeredByUserID); err != nil {
		return nil, fmt.Errorf("resolve escalation target: %w", err)
	}

	newPool, err := s.runtimeRepo.LoadActiveApproverUserIDs(ctx, tx, si.ID)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	newCount := si.EscalationCount + 1
	var newDue sql.NullTime
	if step.EscalationAfterHours != nil {
		if step.MaxEscalations == nil || newCount < *step.MaxEscalations {
			newDue = sql.NullTime{Time: now.Add(time.Duration(*step.EscalationAfterHours) * time.Hour), Valid: true}
		}
	}
	if err := s.runtimeRepo.UpdateEscalationCounters(ctx, tx, si.ID, newCount, now, newDue); err != nil {
		return nil, err
	}
	si.EscalationCount = newCount

	if _, err := s.runtimeRepo.WriteDecision(ctx, tx, req.ID, &si.ID, actor, nil, models.ApprovalDecisionEscalate, "", nil, map[string]any{
		"reason":           reason,
		"action":           models.ApprovalEscalationActionReassign,
		"prior_pool":       priorPool,
		"new_pool":         newPool,
		"escalation_count": newCount,
		"max_escalations":  step.MaxEscalations,
	}); err != nil {
		return nil, err
	}
	return newPool, nil
}

// emitEscalationCompletion fires the post-tx event for skip_step / auto_reject
// escalations. For reassign, EmitApprovalEscalated is called inline with the
// new pool; here we fire the request-level Completed event when applicable.
func (s *ApprovalService) emitEscalationCompletion(ctx context.Context, requestID int, action string, newlyStartedStepInstanceID *int, actorUserID int) {
	if s.eventCoordinator == nil {
		return
	}
	out, err := s.GetRequest(ctx, requestID)
	if err != nil || out == nil {
		return
	}
	item, err := repository.NewItemRepository(s.db).FindByIDWithDetails(out.ItemID)
	if err != nil || item == nil {
		return
	}
	for i := range out.StepInstances {
		si := &out.StepInstances[i]
		if si.Status == models.ApprovalStepStatusEscalated || si.Status == models.ApprovalStepStatusRejected {
			s.eventCoordinator.EmitApprovalEscalated(out, si, action, nil, item, actorUserID)
			break
		}
	}
	if newlyStartedStepInstanceID != nil {
		for i := range out.StepInstances {
			si := &out.StepInstances[i]
			if si.ID == *newlyStartedStepInstanceID {
				s.eventCoordinator.EmitApprovalStepStarted(out, si, approverUserIDs(si.Approvers), approverPortalCustomerIDs(si.Approvers), item, actorUserID)
				break
			}
		}
	}
	if out.Status == models.ApprovalRequestStatusApproved || out.Status == models.ApprovalRequestStatusRejected {
		s.eventCoordinator.EmitApprovalCompleted(out, item, actorUserID)
	}
}

// Delegate hands the actor's seat in the active step pool to another user.
// Mirrors the on-leave substitute flow but driven by the user.
func (s *ApprovalService) Delegate(ctx context.Context, requestID, actorUserID, toUserID int, comment string) error {
	if toUserID == 0 || toUserID == actorUserID {
		return errors.New("delegate target must be a different user")
	}

	stepInstanceID, err := database.WithTxResult(s.db, func(tx database.Tx) (int, error) {
		stepInstance, err := s.runtimeRepo.FindActiveStepForUser(ctx, tx, requestID, actorUserID)
		if err != nil {
			return 0, err
		}
		if stepInstance == nil {
			return 0, fmt.Errorf("user %d is not an active approver of request %d", actorUserID, requestID)
		}
		if err := s.runtimeRepo.DeactivateApproverByUser(ctx, tx, stepInstance.ID, actorUserID); err != nil {
			return 0, err
		}
		if err := s.runtimeRepo.InsertDelegatedApprover(ctx, tx, stepInstance.ID, toUserID, actorUserID); err != nil {
			return 0, err
		}
		delegated := toUserID
		if _, err := s.runtimeRepo.WriteDecision(ctx, tx, requestID, &stepInstance.ID, &actorUserID, nil,
			models.ApprovalDecisionDelegate, comment, &delegated, map[string]any{
				"from_user_id": actorUserID,
				"to_user_id":   toUserID,
			}); err != nil {
			return 0, err
		}
		return stepInstance.ID, nil
	})
	if err != nil {
		return err
	}

	if s.eventCoordinator != nil {
		out, _ := s.GetRequest(ctx, requestID)
		if out != nil {
			item, _ := repository.NewItemRepository(s.db).FindByIDWithDetails(out.ItemID)
			if item != nil {
				for i := range out.StepInstances {
					if out.StepInstances[i].ID == stepInstanceID {
						s.eventCoordinator.EmitApprovalStepStarted(out, &out.StepInstances[i], []int{toUserID}, nil, item, actorUserID)
						break
					}
				}
			}
		}
	}
	return nil
}

// RefreshApprovers re-resolves the configured approver_source for a pending
// step instance, applies on-leave handling, and replaces the snapshot. Admin
// path — useful when a source field was edited mid-flow and the admin wants
// the change to take effect.
func (s *ApprovalService) RefreshApprovers(ctx context.Context, stepInstanceID, actorUserID int, comment string) error {
	type refreshOutcome struct {
		stepInstanceID  int
		newPool         []int
		newCustomerPool []int
		requestID       int
	}

	outcome, err := database.WithTxResult(s.db, func(tx database.Tx) (refreshOutcome, error) {
		var out refreshOutcome
		si, err := s.runtimeRepo.LoadStepInstanceByIDInTx(ctx, tx, stepInstanceID)
		if errors.Is(err, repository.ErrNotFound) {
			return out, errors.New("step instance not found")
		}
		if err != nil {
			return out, err
		}
		if si.Status != models.ApprovalStepStatusPending {
			return out, errors.New("step instance is not pending")
		}

		req, err := s.runtimeRepo.LoadRequestByIDInTx(ctx, tx, si.ApprovalRequestID)
		if err != nil {
			return out, err
		}

		step, err := s.templateRepo.FindStepByIDInTx(ctx, tx, si.ApprovalStepID)
		if err != nil {
			return out, err
		}

		priorPool, err := s.runtimeRepo.LoadActiveApproverUserIDs(ctx, tx, si.ID)
		if err != nil {
			return out, err
		}

		if err := s.runtimeRepo.DeactivateApprovers(ctx, tx, si.ID); err != nil {
			return out, err
		}

		item, err := repository.NewItemRepository(s.db).FindByID(req.ItemID)
		if err != nil {
			return out, err
		}
		if err := s.resolveAndSnapshotApprovers(ctx, tx, si.ID, *step, item, req.TriggeredByUserID); err != nil {
			return out, err
		}

		newPool, err := s.runtimeRepo.LoadActiveApproverUserIDs(ctx, tx, si.ID)
		if err != nil {
			return out, err
		}
		newCustomerPool, err := s.runtimeRepo.LoadActiveApproverCustomerIDs(ctx, tx, si.ID)
		if err != nil {
			return out, err
		}

		actor := &actorUserID
		if actorUserID == 0 {
			actor = nil
		}
		if _, err := s.runtimeRepo.WriteDecision(ctx, tx, req.ID, &si.ID, actor, nil, models.ApprovalDecisionReassign, comment, nil, map[string]any{
			"reason":             "refresh_approvers",
			"prior_pool":         priorPool,
			"new_pool":           newPool,
			"new_pool_customers": newCustomerPool,
		}); err != nil {
			return out, err
		}
		out.stepInstanceID = si.ID
		out.newPool = newPool
		out.newCustomerPool = newCustomerPool
		out.requestID = req.ID
		return out, nil
	})
	if err != nil {
		return err
	}

	if s.eventCoordinator != nil {
		req, _ := s.GetRequest(ctx, outcome.requestID)
		if req == nil {
			return nil
		}
		itm, _ := repository.NewItemRepository(s.db).FindByIDWithDetails(req.ItemID)
		if itm != nil {
			for i := range req.StepInstances {
				if req.StepInstances[i].ID == outcome.stepInstanceID {
					s.eventCoordinator.EmitApprovalStepStarted(req, &req.StepInstances[i], outcome.newPool, outcome.newCustomerPool, itm, actorUserID)
					break
				}
			}
		}
	}
	return nil
}

// ----------------------------------------------------------------------------
// Gating helpers used by WorkflowService.PerformTransition
// ----------------------------------------------------------------------------
