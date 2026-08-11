package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

// approvalActor is the polymorphic actor for an approval action — exactly one
// of UserID / PortalCustomerID is set. Internal callers use UserID; the portal
// surface uses PortalCustomerID. System-driven actions (sweeper escalations)
// pass the zero value: both nil.
type approvalActor struct {
	UserID           *int
	PortalCustomerID *int
}

func actorFromUser(userID int) approvalActor {
	return approvalActor{UserID: &userID}
}

func actorFromCustomer(customerID int) approvalActor {
	return approvalActor{PortalCustomerID: &customerID}
}

func (a approvalActor) isSet() bool {
	return a.UserID != nil || a.PortalCustomerID != nil
}

// findActiveStepForActor returns the lowest-display-order pending step instance
// where the actor (user or portal customer) has an active approver row, or nil
// if none. Branches on whichever id is set in the actor struct.
func (s *ApprovalService) findActiveStepForActor(ctx context.Context, tx database.Tx, requestID int, actor approvalActor) (*models.ApprovalStepInstance, error) {
	if !actor.isSet() {
		return nil, nil
	}
	if actor.UserID != nil {
		return s.runtimeRepo.FindActiveStepForUser(ctx, tx, requestID, *actor.UserID)
	}
	return s.runtimeRepo.FindActiveStepForCustomer(ctx, tx, requestID, *actor.PortalCustomerID)
}

// evaluateStepStatus returns the new step status based on votes vs quorum.
// The current status (passed implicitly via the step instance) is not consulted —
// caller compares to the prior status to decide whether to write an UPDATE.
func (s *ApprovalService) evaluateStepStatus(ctx context.Context, tx database.Tx, stepInstanceID int, step *models.ApprovalStep) (string, error) {
	poolSize, err := s.runtimeRepo.CountActiveApprovers(ctx, tx, stepInstanceID)
	if err != nil {
		return "", err
	}
	if poolSize == 0 {
		return models.ApprovalStepStatusPending, nil
	}

	approves, rejects, err := s.runtimeRepo.CountVotes(ctx, tx, stepInstanceID)
	if err != nil {
		return "", err
	}

	switch step.RejectionPolicy {
	case models.ApprovalRejectionPolicyAnyFails, "":
		if rejects > 0 {
			return models.ApprovalStepStatusRejected, nil
		}
	case models.ApprovalRejectionPolicyQuorumRequired:
		if rejects >= quorumThreshold(step, poolSize) {
			return models.ApprovalStepStatusRejected, nil
		}
	}

	if approves >= quorumThreshold(step, poolSize) {
		return models.ApprovalStepStatusApproved, nil
	}
	return models.ApprovalStepStatusPending, nil
}

// quorumThreshold computes the integer threshold for a step+pool-size.
func quorumThreshold(step *models.ApprovalStep, poolSize int) int {
	switch step.QuorumMode {
	case models.ApprovalQuorumModeAny, "":
		return 1
	case models.ApprovalQuorumModeAll:
		return poolSize
	case models.ApprovalQuorumModeCount:
		if step.QuorumCount == nil || *step.QuorumCount < 1 {
			return 1
		}
		if *step.QuorumCount > poolSize {
			return poolSize
		}
		return *step.QuorumCount
	case models.ApprovalQuorumModePercent:
		if step.QuorumPercent == nil || *step.QuorumPercent < 1 {
			return 1
		}
		t := (poolSize**step.QuorumPercent + 99) / 100
		if t < 1 {
			t = 1
		}
		if t > poolSize {
			t = poolSize
		}
		return t
	default:
		return 1
	}
}

// resolvedApprover identifies either an internal user or portal customer.
// SubstitutedForUserID records on-leave substitutions for users only.
type resolvedApprover struct {
	UserID               int
	PortalCustomerID     int
	SourceRoleID         *int
	SourceGroupID        *int
	SubstitutedForUserID *int
}

func (r resolvedApprover) isCustomer() bool { return r.PortalCustomerID > 0 }

// resolveAndSnapshotApprovers applies leave handling and writes approver snapshots.
// Empty pools await escalation; Decide rejects non-active approvers.
func (s *ApprovalService) resolveAndSnapshotApprovers(ctx context.Context, tx database.Tx, stepInstanceID int, step models.ApprovalStep, item *models.Item, triggeredByUserID int) error {
	rawUsers, err := s.resolveApproverSource(ctx, tx, step, item, triggeredByUserID)
	if err != nil {
		return err
	}

	finalPool := make([]resolvedApprover, 0, len(rawUsers))
	for _, ra := range rawUsers {
		if ra.isCustomer() {
			finalPool = append(finalPool, ra)
			continue
		}

		var onLeave bool
		if s.leaveRepo != nil {
			leave, err := s.leaveRepo.GetActiveForUser(ra.UserID)
			if err == nil && leave != nil {
				onLeave = true
				switch step.OnLeaveStrategy {
				case models.ApprovalOnLeaveUseSubstitute, "":
					if leave.SubstituteUserID != nil && *leave.SubstituteUserID != 0 {
						sub := *leave.SubstituteUserID
						subOrig := ra.UserID
						substitute := resolvedApprover{
							UserID:               sub,
							SourceRoleID:         ra.SourceRoleID,
							SourceGroupID:        ra.SourceGroupID,
							SubstitutedForUserID: &subOrig,
						}
						finalPool = append(finalPool, substitute)
						parentReqID, _ := s.runtimeRepo.GetRequestIDForStep(ctx, tx, stepInstanceID)
						if _, err := s.runtimeRepo.WriteDecision(ctx, tx, parentReqID, &stepInstanceID, nil, nil,
							models.ApprovalDecisionSubstitute, "", nil, map[string]any{
								"original_user_id":   subOrig,
								"substitute_user_id": sub,
								"reason":             "active_leave",
							}); err != nil {
							return err
						}
						continue
					}
					// No substitute configured: fall back to keeping the
					// original approver. Dropping them silently leaves the
					// pool potentially empty and the request unactionable.
					finalPool = append(finalPool, ra)
				case models.ApprovalOnLeaveSkip:
					// drop
				case models.ApprovalOnLeaveKeep:
					finalPool = append(finalPool, ra)
				}
			}
		}
		if !onLeave {
			finalPool = append(finalPool, ra)
		}
	}

	// Self-approval guard. Customers can't trigger an approval today
	// (triggered_by_user_id is users-only); they're never blocked here.
	if !step.AllowSelfApproval && triggeredByUserID != 0 {
		filtered := finalPool[:0]
		for _, ra := range finalPool {
			if !ra.isCustomer() && ra.UserID == triggeredByUserID {
				continue
			}
			filtered = append(filtered, ra)
		}
		finalPool = filtered
	}

	for _, ra := range finalPool {
		ai := repository.ApproverInsert{
			UserID:               ra.UserID,
			PortalCustomerID:     ra.PortalCustomerID,
			SourceRoleID:         ra.SourceRoleID,
			SourceGroupID:        ra.SourceGroupID,
			SubstitutedForUserID: ra.SubstitutedForUserID,
		}
		if err := s.runtimeRepo.InsertApprover(ctx, tx, stepInstanceID, ai); err != nil {
			return err
		}
	}
	return nil
}

// resolveApproverSource turns the configured source into a list of user IDs
// (with provenance metadata). The cross-domain reads (items, user_workspace_roles,
// group_members) intentionally stay inline rather than moving to ApprovalRepository
// — they belong to other domains and folding them into an approval repo would
// blur the boundaries.
func (s *ApprovalService) resolveApproverSource(ctx context.Context, tx database.Tx, step models.ApprovalStep, item *models.Item, triggeredByUserID int) ([]resolvedApprover, error) {
	switch step.ApproverSource {
	case models.ApprovalSourceCreator:
		return resolveCreatorApprover(item), nil

	case models.ApprovalSourceAssignee:
		return resolveUserApprover(item.AssigneeID), nil

	case models.ApprovalSourceCurrentUser:
		return resolveUserApprover(&triggeredByUserID), nil

	case models.ApprovalSourceUser:
		return resolveUserApprover(step.ApproverUserID), nil

	case models.ApprovalSourceRegularField:
		return s.resolveRegularFieldApprover(ctx, tx, step, item.ID)

	case models.ApprovalSourceCustomField:
		return s.resolveCustomFieldApprovers(ctx, tx, step, item.ID)

	case models.ApprovalSourceRole:
		return resolveRoleApprovers(ctx, tx, step, item.WorkspaceID)

	case models.ApprovalSourceGroup:
		return resolveGroupApprovers(ctx, tx, step)
	}
	return nil, fmt.Errorf("unsupported approver_source %q", step.ApproverSource)
}

func resolveCreatorApprover(item *models.Item) []resolvedApprover {
	if item.CreatorPortalCustomerID != nil && *item.CreatorPortalCustomerID != 0 {
		return []resolvedApprover{{PortalCustomerID: *item.CreatorPortalCustomerID}}
	}
	return resolveUserApprover(item.CreatorID)
}

func resolveUserApprover(userID *int) []resolvedApprover {
	if userID == nil || *userID == 0 {
		return nil
	}
	return []resolvedApprover{{UserID: *userID}}
}

func (s *ApprovalService) resolveRegularFieldApprover(ctx context.Context, tx database.Tx, step models.ApprovalStep, itemID int) ([]resolvedApprover, error) {
	if _, ok := models.AllowedRegularApproverFields[step.ApproverFieldIdentifier]; !ok {
		return nil, fmt.Errorf("regular_field %q is not in the approver whitelist", step.ApproverFieldIdentifier)
	}
	userID, err := repository.NewItemRepository(s.db).GetUserFieldTx(ctx, tx, itemID, step.ApproverFieldIdentifier)
	if err != nil {
		return nil, err
	}
	return resolveUserApprover(userID), nil
}

func (s *ApprovalService) resolveCustomFieldApprovers(ctx context.Context, tx database.Tx, step models.ApprovalStep, itemID int) ([]resolvedApprover, error) {
	if step.ApproverFieldID == nil {
		return nil, errors.New("custom_field source requires approver_field_id")
	}
	raw, err := repository.NewItemRepository(s.db).GetCustomFieldValuesRawTx(ctx, tx, itemID)
	if err != nil || !raw.Valid || raw.String == "" {
		return nil, err
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(raw.String), &values); err != nil {
		return nil, err
	}
	return userListFromValue(values[fmt.Sprintf("%d", *step.ApproverFieldID)]), nil
}

func resolveRoleApprovers(ctx context.Context, tx database.Tx, step models.ApprovalStep, workspaceID int) ([]resolvedApprover, error) {
	if step.ApproverRoleID == nil {
		return nil, errors.New("role source requires approver_role_id")
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT user_id FROM user_workspace_roles
		WHERE workspace_id = ? AND role_id = ?
		UNION
		SELECT DISTINCT gm.user_id
		FROM group_workspace_roles gwr
		JOIN group_members gm ON gm.group_id = gwr.group_id
		WHERE gwr.workspace_id = ? AND gwr.role_id = ?
	`, workspaceID, *step.ApproverRoleID, workspaceID, *step.ApproverRoleID)
	if err != nil {
		return nil, err
	}
	return scanResolvedApprovers(rows, step.ApproverRoleID, nil)
}

func resolveGroupApprovers(ctx context.Context, tx database.Tx, step models.ApprovalStep) ([]resolvedApprover, error) {
	if step.ApproverGroupID == nil {
		return nil, errors.New("group source requires approver_group_id")
	}
	rows, err := tx.QueryContext(ctx, `SELECT user_id FROM group_members WHERE group_id = ?`, *step.ApproverGroupID)
	if err != nil {
		return nil, err
	}
	return scanResolvedApprovers(rows, nil, step.ApproverGroupID)
}

func scanResolvedApprovers(rows *sql.Rows, roleID, groupID *int) ([]resolvedApprover, error) {
	defer func() { _ = rows.Close() }()
	var out []resolvedApprover
	for rows.Next() {
		var userID int
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		out = append(out, resolvedApprover{UserID: userID, SourceRoleID: roleID, SourceGroupID: groupID})
	}
	return out, rows.Err()
}

// userListFromValue interprets a custom-field value as a user id or list of user ids.
func userListFromValue(v any) []resolvedApprover {
	switch val := v.(type) {
	case float64:
		if val > 0 {
			return []resolvedApprover{{UserID: int(val)}}
		}
	case int:
		if val > 0 {
			return []resolvedApprover{{UserID: val}}
		}
	case []any:
		var out []resolvedApprover
		for _, item := range val {
			if uid, ok := toInt(item); ok && uid > 0 {
				out = append(out, resolvedApprover{UserID: uid})
			}
		}
		return out
	case string:
		var n int
		_, err := fmt.Sscanf(strings.TrimSpace(val), "%d", &n)
		if err == nil && n > 0 {
			return []resolvedApprover{{UserID: n}}
		}
	}
	return nil
}
