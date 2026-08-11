package validation

import (
	"database/sql"
	"errors"
	"fmt"

	"windshift/internal/models"
)

// HierarchyQueryer is the common read surface shared by database.Database and
// database.Tx. Placement checks that accompany a write should receive the
// transaction so validation and mutation observe the same hierarchy state.
type HierarchyQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

type hierarchyTypeDetails struct {
	level int
	name  string
}

func loadHierarchyType(q HierarchyQueryer, itemTypeID int) (hierarchyTypeDetails, error) {
	var details hierarchyTypeDetails
	err := q.QueryRow(`
		SELECT hierarchy_level, name
		FROM item_types
		WHERE id = ?
	`, itemTypeID).Scan(&details.level, &details.name)
	if errors.Is(err, sql.ErrNoRows) {
		return details, &ValidationError{Field: "item_type_id", Message: "Item type not found"}
	}
	if err != nil {
		return details, fmt.Errorf("failed to get item type hierarchy level: %w", err)
	}
	return details, nil
}

func loadItemHierarchyType(q HierarchyQueryer, itemID int) (hierarchyTypeDetails, error) {
	var details hierarchyTypeDetails
	err := q.QueryRow(`
		SELECT it.hierarchy_level, it.name
		FROM items i
		JOIN item_types it ON i.item_type_id = it.id
		WHERE i.id = ?
	`, itemID).Scan(&details.level, &details.name)
	if errors.Is(err, sql.ErrNoRows) {
		return details, &ValidationError{Field: "parent_id", Message: "Parent item not found or has no item type"}
	}
	if err != nil {
		return details, fmt.Errorf("failed to get parent item hierarchy level: %w", err)
	}
	return details, nil
}

// ValidateParentForItemType enforces the hierarchy contract for one item
// placement:
//   - regular types may be roots or children exactly one level below a regular
//     parent;
//   - hierarchy level -1 is a generic subtask sentinel which requires any
//     regular parent;
//   - generic subtasks are terminal and therefore cannot be parents.
func ValidateParentForItemType(q HierarchyQueryer, itemTypeID int, parentID *int) error {
	child, err := loadHierarchyType(q, itemTypeID)
	if err != nil {
		return err
	}

	if parentID == nil || *parentID == 0 {
		if child.level == models.HierarchyLevelGenericSubtask {
			return &ValidationError{
				Field:   "parent_id",
				Message: fmt.Sprintf("Generic sub-task item type '%s' requires a parent", child.name),
			}
		}
		return nil
	}

	parent, err := loadItemHierarchyType(q, *parentID)
	if err != nil {
		return err
	}
	if parent.level == models.HierarchyLevelGenericSubtask {
		return &ValidationError{
			Field:   "parent_id",
			Message: "Generic sub-tasks are terminal and cannot have child items",
		}
	}
	if child.level == models.HierarchyLevelGenericSubtask {
		return nil
	}
	if child.level != parent.level+1 {
		return &ValidationError{
			Field: "parent_id",
			Message: fmt.Sprintf(
				"Item type '%s' (hierarchy level %d) cannot be a child of an item at hierarchy level %d",
				child.name, child.level, parent.level,
			),
		}
	}
	return nil
}

// ValidateGenericSubtaskBoundary enforces only the sentinel-specific part of
// the hierarchy contract. Low-level creation paths use this guard because some
// legacy/internal callers intentionally stage regular items before repairing
// their fixed hierarchy, while no caller may treat a generic subtask as a root
// or parent. Interactive API paths use ValidateParentForItemType for the full
// adjacent-level contract.
func ValidateGenericSubtaskBoundary(
	q HierarchyQueryer,
	itemTypeID int,
	parentID *int,
	allowUnparentedGeneric bool,
) error {
	child, err := loadHierarchyType(q, itemTypeID)
	if err != nil {
		return err
	}

	if parentID == nil || *parentID == 0 {
		if child.level == models.HierarchyLevelGenericSubtask && !allowUnparentedGeneric {
			return &ValidationError{
				Field:   "parent_id",
				Message: fmt.Sprintf("Generic sub-task item type '%s' requires a parent", child.name),
			}
		}
		return nil
	}

	var parentLevel sql.NullInt64
	err = q.QueryRow(`
		SELECT it.hierarchy_level
		FROM items parent
		LEFT JOIN item_types it ON parent.item_type_id = it.id
		WHERE parent.id = ?
	`, *parentID).Scan(&parentLevel)
	if errors.Is(err, sql.ErrNoRows) {
		return &ValidationError{Field: "parent_id", Message: "Parent item not found"}
	}
	if err != nil {
		return fmt.Errorf("failed to get parent item hierarchy level: %w", err)
	}
	if parentLevel.Valid && int(parentLevel.Int64) == models.HierarchyLevelGenericSubtask {
		return &ValidationError{
			Field:   "parent_id",
			Message: "Generic sub-tasks are terminal and cannot have child items",
		}
	}
	if child.level == models.HierarchyLevelGenericSubtask && !parentLevel.Valid {
		return &ValidationError{
			Field:   "parent_id",
			Message: "Generic sub-tasks require a parent with a regular item type",
		}
	}
	return nil
}

// ValidateItemTypePlacement checks whether an existing item can use itemTypeID
// without invalidating either its parent edge or any direct child edge. It is
// used by the item-type change workflow and bulk migration paths.
func ValidateItemTypePlacement(q HierarchyQueryer, itemID, itemTypeID int, parentID *int) error {
	if err := ValidateParentForItemType(q, itemTypeID, parentID); err != nil {
		return err
	}

	target, err := loadHierarchyType(q, itemTypeID)
	if err != nil {
		return err
	}

	var incompatibleChildren int
	if target.level == models.HierarchyLevelGenericSubtask {
		err = q.QueryRow(`SELECT COUNT(*) FROM items WHERE parent_id = ?`, itemID).Scan(&incompatibleChildren)
	} else {
		err = q.QueryRow(`
			SELECT COUNT(*)
			FROM items child
			JOIN item_types child_type ON child.item_type_id = child_type.id
			WHERE child.parent_id = ?
			  AND child_type.hierarchy_level NOT IN (?, ?)
		`, itemID, models.HierarchyLevelGenericSubtask, target.level+1).Scan(&incompatibleChildren)
	}
	if err != nil {
		return fmt.Errorf("failed to validate existing child hierarchy: %w", err)
	}
	if incompatibleChildren > 0 {
		return &ValidationError{
			Field:   "item_type_id",
			Message: "Item type is not compatible with existing child hierarchy",
		}
	}
	return nil
}
