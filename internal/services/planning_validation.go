package services

import "fmt"

// CategoryExists reports whether a milestone category exists by ID.
func (s *PlanningService) CategoryExists(categoryID int) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM milestone_categories WHERE id = ?", categoryID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check category: %w", err)
	}
	return count > 0, nil
}

// WorkspaceExists reports whether a workspace exists by ID.
func (s *PlanningService) WorkspaceExists(workspaceID int) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE id = ?", workspaceID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check workspace: %w", err)
	}
	return count > 0, nil
}

// IterationTypeExists reports whether an iteration type exists by ID.
func (s *PlanningService) IterationTypeExists(typeID int) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM iteration_types WHERE id = ?", typeID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check iteration type: %w", err)
	}
	return count > 0, nil
}
