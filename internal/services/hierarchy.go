package services

import (
	"database/sql"
	"errors"
	"fmt"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

// maxHierarchyDepth caps every recursive hierarchy walk — both the CTE-based
// traversals below and the cycle-detection in WouldCreateCycle. 30 comfortably
// covers realistic roadmap/epic/story/subtask trees while capping worst-case
// scan cost and preventing a stored cycle from looping the DB forever.
const maxHierarchyDepth = 30

// HierarchyService handles all hierarchy-related operations using only parent_id
type HierarchyService struct {
	db database.Database
}

// NewHierarchyService creates a new hierarchy service
func NewHierarchyService(db database.Database) *HierarchyService {
	return &HierarchyService{db: db}
}

func itemPtrsToValues(items []*models.Item) []models.Item {
	out := make([]models.Item, 0, len(items))
	for _, item := range items {
		if item != nil {
			out = append(out, *item)
		}
	}
	return out
}

// WouldCreateCycle reports whether assigning newParentID as the parent of
// something in ancestorCandidateID's subtree (or of ancestorCandidateID itself)
// would create a cycle. It walks parent_id upward from newParentID; if
// ancestorCandidateID is encountered — or equals newParentID — a cycle would
// result. Self-parent (newParentID == ancestorCandidateID) is reported as a
// cycle. If the walk exhausts maxHierarchyDepth without reaching a root, the
// hierarchy is either already cyclic or deeper than our ceiling, so we
// fail-closed and return (true, nil).
//
// This overload reads outside of a transaction. Callers that are about to
// mutate parent_id must use WouldCreateCycleTx so the check and the write
// are atomic.
func (h *HierarchyService) WouldCreateCycle(ancestorCandidateID, newParentID int) (bool, error) {
	itemRepo := repository.NewItemRepository(h.db)
	return walkForCycle(ancestorCandidateID, newParentID, itemRepo.GetParentID)
}

// WouldCreateCycleTx is the transaction-scoped variant of WouldCreateCycle.
// It walks using SELECT ... FOR UPDATE (on Postgres) so the rows being
// examined are locked for the rest of the transaction; paired with writing
// the new parent_id in the same transaction this closes the TOCTOU window
// where two concurrent reparents could each pass their cycle check and
// together create a cycle.
func (h *HierarchyService) WouldCreateCycleTx(tx database.Tx, ancestorCandidateID, newParentID int) (bool, error) {
	itemRepo := repository.NewItemRepository(h.db)
	return walkForCycle(ancestorCandidateID, newParentID, func(id int) (*int, error) {
		return itemRepo.GetParentIDTx(tx, id)
	})
}

// last review: ser, 190526, NOTE: Comments are lacking
func walkForCycle(ancestorCandidateID, newParentID int, getParent func(int) (*int, error)) (bool, error) {
	current := newParentID
	for i := 0; i < maxHierarchyDepth; i++ {
		if current == ancestorCandidateID {
			return true, nil
		}
		parent, err := getParent(current)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return false, nil
			}
			return false, fmt.Errorf("failed to walk hierarchy: %w", err)
		}
		if parent == nil {
			return false, nil
		}
		current = *parent
	}
	return true, nil
}

// GetAncestors returns all ancestors of an item (from root to direct parent).
// The recursive walk is capped at maxHierarchyDepth so a stored cycle can't
// loop the DB.
func (h *HierarchyService) GetAncestors(itemID int) ([]models.Item, error) {
	return repository.NewItemRepository(h.db).GetAncestorsForHierarchy(itemID, maxHierarchyDepth)
}

// GetDescendants returns all descendants of an item
func (h *HierarchyService) GetDescendants(itemID, maxDepth int) ([]models.Item, error) {
	if maxDepth <= 0 || maxDepth > maxHierarchyDepth {
		maxDepth = maxHierarchyDepth
	}
	items, err := repository.NewItemRepository(h.db).GetDescendantsWithMaxDepth(itemID, maxDepth)
	if err != nil {
		return nil, err
	}
	return itemPtrsToValues(items), nil
}

// CountDescendants returns the total number of descendants for an item.
// The recursive walk is capped at maxHierarchyDepth so a stored cycle can't
// loop the DB.
func (h *HierarchyService) CountDescendants(itemID int) (int, error) {
	return repository.NewItemRepository(h.db).CountDescendants(itemID)
}

// GetChildren returns direct children of an item
func (h *HierarchyService) GetChildren(itemID int) ([]models.Item, error) {
	items, err := repository.NewItemRepository(h.db).GetChildren(itemID)
	if err != nil {
		return nil, err
	}
	return itemPtrsToValues(items), nil
}

// GetRoot returns the root item for a given item (walks up to top level).
// The walk is capped at maxHierarchyDepth so a stored cycle can't loop the
// DB; exhaustion surfaces as an error rather than a silent nil so callers
// can't confuse it with "no parent". A non-existent input id returns
// (nil, nil) — the recursive query alone cannot distinguish that case from a
// cap-hit, so a cheap existence probe runs first.
func (h *HierarchyService) GetRoot(itemID int) (*models.Item, error) {
	repo := repository.NewItemRepository(h.db)
	current := itemID
	for depth := 0; depth < maxHierarchyDepth; depth++ {
		parentID, err := repo.GetParentID(current)
		if errors.Is(err, repository.ErrNotFound) {
			if depth == 0 {
				return nil, nil
			}
			return nil, fmt.Errorf("hierarchy parent %d referenced by item %d was not found", current, itemID)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to walk hierarchy: %w", err)
		}
		if parentID == nil {
			return repo.FindByIDWithDetails(current)
		}
		current = *parentID
	}
	return nil, fmt.Errorf("hierarchy walk exceeded %d levels without finding a root (item %d, likely cyclic)", maxHierarchyDepth, itemID)
}

// GetEffectiveProject returns the effective project_id for an item by walking up the hierarchy
// Returns: (effective_project_id, inheritance_mode, error)
// inheritance_mode: "none" (NULL), "inherit" (-1), "direct" (>0)
func (h *HierarchyService) GetEffectiveProject(itemID int) (projectID *int, inheritanceMode string, err error) {
	query := `
		WITH RECURSIVE project_chain AS (
			-- Base case: get the item itself
			SELECT id, project_id, parent_id, 0 as depth
			FROM items
			WHERE id = ?

			UNION ALL

			-- Recursive case: walk up to parent if current has inherit (-1)
			SELECT i.id, i.project_id, i.parent_id, pc.depth + 1
			FROM items i
			JOIN project_chain pc ON i.id = pc.parent_id
			WHERE pc.project_id = -1 AND pc.depth < 10
		)
		SELECT
			id,
			project_id,
			CASE
				WHEN project_id IS NULL THEN 'none'
				WHEN project_id = -1 THEN 'inherit'
				ELSE 'direct'
			END as mode,
			depth
		FROM project_chain
		WHERE project_id IS NOT NULL AND project_id != -1
		ORDER BY depth ASC
		LIMIT 1
	`

	var id, depth int
	var nullProjectID sql.NullInt64
	var mode string

	err = h.db.QueryRow(query, itemID).Scan(&id, &nullProjectID, &mode, &depth)
	if errors.Is(err, sql.ErrNoRows) {
		// No effective project found (all ancestors have NULL or -1)
		return nil, "none", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("failed to get effective project: %w", err)
	}

	if nullProjectID.Valid {
		val := int(nullProjectID.Int64)
		return &val, mode, nil
	}

	return nil, "none", nil
}
