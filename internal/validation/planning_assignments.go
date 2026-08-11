package validation

import (
	"database/sql"
	"errors"
	"fmt"
)

// PlanningAssignmentQueryer is implemented by both database.Database and
// database.Tx. Keeping the contract local lets create paths validate before a
// transaction while update paths validate against the locked item transaction.
type PlanningAssignmentQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

// ValidatePlanningAssignments ensures workspace-local planning objects are
// assigned only to items in the same workspace. Global objects are assignable
// everywhere. Missing, malformed, and cross-workspace IDs intentionally share
// the same not-found response so callers cannot enumerate another workspace's
// planning catalog.
func ValidatePlanningAssignments(q PlanningAssignmentQueryer, workspaceID int, milestoneIDs []int, iterationID *int) error {
	seen := make(map[int]struct{}, len(milestoneIDs))
	for _, milestoneID := range milestoneIDs {
		if _, duplicate := seen[milestoneID]; duplicate {
			continue
		}
		seen[milestoneID] = struct{}{}
		if err := validatePlanningAssignment(q, "milestones", milestoneID, workspaceID); err != nil {
			var validationErr *ValidationError
			if errors.As(err, &validationErr) {
				return &ValidationError{Field: "milestone_ids", Message: fmt.Sprintf("Milestone %d not found", milestoneID)}
			}
			return fmt.Errorf("failed to validate milestone %d: %w", milestoneID, err)
		}
	}

	if iterationID != nil {
		if err := validatePlanningAssignment(q, "iterations", *iterationID, workspaceID); err != nil {
			var validationErr *ValidationError
			if errors.As(err, &validationErr) {
				return &ValidationError{Field: "iteration_id", Message: "Iteration not found"}
			}
			return fmt.Errorf("failed to validate iteration %d: %w", *iterationID, err)
		}
	}
	return nil
}

func validatePlanningAssignment(q PlanningAssignmentQueryer, table string, id, workspaceID int) error {
	var isGlobal bool
	var assignedWorkspace sql.NullInt64
	err := q.QueryRow("SELECT is_global, workspace_id FROM "+table+" WHERE id = ?", id).Scan(&isGlobal, &assignedWorkspace)
	if errors.Is(err, sql.ErrNoRows) {
		return &ValidationError{Field: table, Message: "not found"}
	}
	if err != nil {
		return err
	}
	if isGlobal {
		if assignedWorkspace.Valid {
			return &ValidationError{Field: table, Message: "not found"}
		}
		return nil
	}
	if !assignedWorkspace.Valid || int(assignedWorkspace.Int64) != workspaceID {
		return &ValidationError{Field: table, Message: "not found"}
	}
	return nil
}
