package services

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"windshift/internal/constants"
	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

// ItemValidationParams contains parameters for validating item creation
type ItemValidationParams struct {
	WorkspaceID       int
	Title             string
	ItemTypeID        *int
	ParentID          *int
	StatusID          *int
	IsTask            bool
	RelatedWorkItemID *int
	UserID            int // User creating the item (for personal workspace validation)
	// PermService, when set, enforces that the caller has view permission on a
	// cross-workspace parent's workspace before allowing the link — otherwise a
	// user could discover (and attach to) items in workspaces they can't see.
	// User-facing create handlers set it; internal callers may omit it.
	PermService *PermissionService
}

// ItemValidationResult contains the result of validation
type ItemValidationResult struct {
	Valid bool
	Error string
}

// IsItemTypeAllowedInWorkspace checks whether the given item type is allowed
// in the workspace's configuration set. Returns true when:
//   - the workspace has no configuration set (all types allowed), or
//   - the item type appears in configuration_set_item_types for that config set.
func IsItemTypeAllowedInWorkspace(db database.Database, workspaceID, itemTypeID int) (bool, error) {
	var configSetID *int
	err := db.QueryRow(
		"SELECT configuration_set_id FROM workspace_configuration_sets WHERE workspace_id = ?",
		workspaceID,
	).Scan(&configSetID)
	if errors.Is(err, sql.ErrNoRows) || configSetID == nil {
		return true, nil // no config set → all types allowed
	}
	if err != nil {
		return false, fmt.Errorf("failed to query workspace config set: %w", err)
	}

	var exists bool
	err = db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM configuration_set_item_types WHERE configuration_set_id = ? AND item_type_id = ?)",
		*configSetID, itemTypeID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check item type in config set: %w", err)
	}
	return exists, nil
}

// IsPriorityAllowedInWorkspace verifies that the priority exists and is
// available through the workspace's configuration set. Workspaces without a
// configuration set, or whose configuration set has no explicit priority
// assignments, use the default global priority catalog.
func IsPriorityAllowedInWorkspace(db database.Database, workspaceID, priorityID int) (bool, error) {
	var priorityExists bool
	if err := db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM priorities WHERE id = ?)",
		priorityID,
	).Scan(&priorityExists); err != nil {
		return false, fmt.Errorf("failed to check priority existence: %w", err)
	}
	if !priorityExists {
		return false, nil
	}

	var configSetID *int
	err := db.QueryRow(
		"SELECT configuration_set_id FROM workspace_configuration_sets WHERE workspace_id = ?",
		workspaceID,
	).Scan(&configSetID)
	if errors.Is(err, sql.ErrNoRows) || configSetID == nil {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to query workspace config set: %w", err)
	}

	var allowed bool
	if err := db.QueryRow(
		`SELECT
			NOT EXISTS(SELECT 1 FROM configuration_set_priorities WHERE configuration_set_id = ?)
			OR EXISTS(SELECT 1 FROM configuration_set_priorities WHERE configuration_set_id = ? AND priority_id = ?)`,
		*configSetID, *configSetID, priorityID,
	).Scan(&allowed); err != nil {
		return false, fmt.Errorf("failed to check priority in config set: %w", err)
	}
	return allowed, nil
}

// ValidateItemCreation validates all parameters for creating an item
// Returns a validation result indicating success or failure with error message
func ValidateItemCreation(db database.Database, params ItemValidationParams) *ItemValidationResult {
	// Validate required fields
	if strings.TrimSpace(params.Title) == "" {
		return &ItemValidationResult{Valid: false, Error: "Title is required"}
	}

	// Validate workspace exists
	var workspaceExists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM workspaces WHERE id = ?)", params.WorkspaceID).Scan(&workspaceExists)
	if err != nil {
		return &ItemValidationResult{Valid: false, Error: fmt.Sprintf("Failed to validate workspace: %v", err)}
	}
	if !workspaceExists {
		return &ItemValidationResult{Valid: false, Error: "Workspace not found"}
	}

	// Task-specific validation
	if params.IsTask {
		// Tasks can only have status_id Open or Done
		if params.StatusID != nil && *params.StatusID != constants.StatusIDOpen && *params.StatusID != constants.StatusIDDone {
			return &ItemValidationResult{Valid: false, Error: "Tasks can only have status 'Open' or 'Done'"}
		}
	}

	// Validate parent item if specified
	if params.ParentID != nil && *params.ParentID != 0 {
		result := validateParentHierarchy(db, params.ParentID, params.ItemTypeID, params.WorkspaceID, params.UserID, params.PermService)
		if !result.Valid {
			return result
		}
	}

	// Validate related_work_item_id if provided
	if params.RelatedWorkItemID != nil {
		result := validateRelatedWorkItem(db, params.WorkspaceID, params.UserID, *params.RelatedWorkItemID)
		if !result.Valid {
			return result
		}
	}

	return &ItemValidationResult{Valid: true}
}

// validateParentHierarchy validates the parent-child hierarchy relationship.
// When permService is non-nil and the parent lives in a different workspace
// than the new item, the caller must hold view permission on the parent's
// workspace; otherwise the parent is reported as "not found" so its existence
// (and hierarchy level) isn't leaked across a workspace boundary.
func validateParentHierarchy(db database.Database, parentID, itemTypeID *int, workspaceID, userID int, permService *PermissionService) *ItemValidationResult {
	repo := repository.NewItemRepository(db)

	// Cross-workspace parent: gate on view permission before revealing anything
	// about the parent (mirrors the update-path check in the field validator).
	if permService != nil {
		parentWorkspaceID, wsErr := repo.GetWorkspaceID(*parentID)
		if errors.Is(wsErr, repository.ErrNotFound) {
			return &ItemValidationResult{Valid: false, Error: "Parent item not found"}
		}
		if wsErr != nil {
			return &ItemValidationResult{Valid: false, Error: fmt.Sprintf("Failed to validate parent: %v", wsErr)}
		}
		if parentWorkspaceID != workspaceID {
			hasView, permErr := permService.HasWorkspacePermission(userID, parentWorkspaceID, models.PermissionItemView)
			if permErr != nil {
				return &ItemValidationResult{Valid: false, Error: fmt.Sprintf("Failed to validate parent: %v", permErr)}
			}
			if !hasView {
				return &ItemValidationResult{Valid: false, Error: "Parent item not found"}
			}
		}
	}

	_, parentItemTypeHierarchyLevel, err := repo.GetItemTypeAndHierarchyLevel(*parentID)
	if errors.Is(err, repository.ErrNotFound) {
		return &ItemValidationResult{Valid: false, Error: "Parent item not found"}
	}
	if err != nil {
		return &ItemValidationResult{Valid: false, Error: fmt.Sprintf("Failed to validate parent: %v", err)}
	}

	// Validate hierarchy relationship if item type is specified
	if itemTypeID != nil && *itemTypeID != 0 {
		var itemTypeHierarchyLevel int
		var itemTypeName string
		err := db.QueryRow(`
			SELECT hierarchy_level, name FROM item_types
			WHERE id = ?
		`, *itemTypeID).Scan(&itemTypeHierarchyLevel, &itemTypeName)

		if errors.Is(err, sql.ErrNoRows) {
			return &ItemValidationResult{Valid: false, Error: "Item type not found"}
		}
		if err != nil {
			return &ItemValidationResult{Valid: false, Error: fmt.Sprintf("Failed to validate item type: %v", err)}
		}

		// Check if child hierarchy level is exactly one more than parent
		if itemTypeHierarchyLevel != parentItemTypeHierarchyLevel+1 {
			return &ItemValidationResult{
				Valid: false,
				Error: fmt.Sprintf("Item type '%s' (hierarchy level %d) cannot be a child of an item at hierarchy level %d",
					itemTypeName, itemTypeHierarchyLevel, parentItemTypeHierarchyLevel),
			}
		}
	}

	return &ItemValidationResult{Valid: true}
}

// validateRelatedWorkItem validates that the related work item exists and is in a valid workspace
func validateRelatedWorkItem(db database.Database, workspaceID, userID, relatedWorkItemID int) *ItemValidationResult {
	// Verify workspace is personal and belongs to the user
	var isPersonal bool
	var ownerID *int
	err := db.QueryRow(`
		SELECT is_personal, owner_id FROM workspaces WHERE id = ?
	`, workspaceID).Scan(&isPersonal, &ownerID)

	if err != nil {
		return &ItemValidationResult{Valid: false, Error: fmt.Sprintf("Failed to validate workspace: %v", err)}
	}

	if !isPersonal || ownerID == nil || *ownerID != userID {
		return &ItemValidationResult{Valid: false, Error: "Personal tasks must be created in your own personal workspace"}
	}

	// Verify the related work item exists.
	if _, err := repository.NewItemRepository(db).GetWorkspaceID(relatedWorkItemID); err != nil {
		return &ItemValidationResult{Valid: false, Error: "Related work item not found or access denied"}
	}

	return &ItemValidationResult{Valid: true}
}
