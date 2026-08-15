package services

import (
	"windshift/internal/models"
	"windshift/internal/validation"
)

// MaskInaccessibleRelatedWorkItems removes related-item identifiers and joined
// metadata when the viewer cannot access the referenced workspace.
func MaskInaccessibleRelatedWorkItems(
	userID int,
	items []models.Item,
	checker validation.WorkspacePermissionChecker,
) {
	access := make(map[int]bool)
	for i := range items {
		item := &items[i]
		if item.RelatedWorkItemID == nil {
			continue
		}

		workspaceID := item.RelatedWorkItemWorkspaceID
		allowed, checked := access[workspaceID]
		if !checked {
			if checker != nil && workspaceID > 0 {
				var err error
				allowed, err = checker.HasWorkspacePermission(userID, workspaceID, models.PermissionItemView)
				if err != nil {
					allowed = false
				}
			}
			access[workspaceID] = allowed
		}
		if allowed {
			continue
		}

		item.RelatedWorkItemID = nil
		item.RelatedWorkItemTitle = ""
		item.RelatedWorkItemWorkspaceKey = ""
		item.RelatedWorkItemWorkspaceID = 0
		item.RelatedWorkItemNumber = 0
	}
}
