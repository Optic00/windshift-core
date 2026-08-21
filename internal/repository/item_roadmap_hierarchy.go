package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"windshift/internal/models"
)

const (
	maxRoadmapHierarchyRoots = 500
	maxRoadmapHierarchyItems = 5000
)

// ErrRoadmapHierarchyRootLimit identifies requests with too many root items.
var ErrRoadmapHierarchyRootLimit = errors.New("roadmap hierarchy root limit exceeded")

// GetRoadmapHierarchyRootWorkspaceIDs resolves the owning workspace for each
// existing root so callers can authorize roots before expanding their trees.
func (r *ItemRepository) GetRoadmapHierarchyRootWorkspaceIDs(ctx context.Context, rootIDs []int) (map[int]int, error) {
	ids := uniquePositiveInts(rootIDs)
	if len(ids) == 0 {
		return map[int]int{}, nil
	}
	if len(ids) > maxRoadmapHierarchyRoots {
		return nil, fmt.Errorf("%w (max %d)", ErrRoadmapHierarchyRootLimit, maxRoadmapHierarchyRoots)
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, workspace_id
		FROM items
		WHERE id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("query roadmap hierarchy root workspaces: %w", err)
	}
	defer func() { _ = rows.Close() }()

	workspaceIDs := make(map[int]int, len(ids))
	for rows.Next() {
		var itemID, workspaceID int
		if err := rows.Scan(&itemID, &workspaceID); err != nil {
			return nil, fmt.Errorf("scan roadmap hierarchy root workspace: %w", err)
		}
		workspaceIDs[itemID] = workspaceID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate roadmap hierarchy root workspaces: %w", err)
	}
	return workspaceIDs, nil
}

// GetRoadmapHierarchyDates returns the requested roots and their descendants.
// The result is intentionally minimal and capped for predictable roadmap loads.
func (r *ItemRepository) GetRoadmapHierarchyDates(ctx context.Context, rootIDs []int) ([]models.RoadmapHierarchyDate, bool, error) {
	ids := uniquePositiveInts(rootIDs)
	if len(ids) == 0 {
		return []models.RoadmapHierarchyDate{}, false, nil
	}
	if len(ids) > maxRoadmapHierarchyRoots {
		return nil, false, fmt.Errorf("%w (max %d)", ErrRoadmapHierarchyRootLimit, maxRoadmapHierarchyRoots)
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, maxRoadmapHierarchyItems+1)

	rows, err := r.db.QueryContext(ctx, `
		WITH RECURSIVE hierarchy(id, workspace_id, parent_id, start_date, end_date) AS (
			SELECT id, workspace_id, parent_id, start_date, end_date
			FROM items
			WHERE id IN (`+placeholders+`)
			UNION
			SELECT child.id, child.workspace_id, child.parent_id, child.start_date, child.end_date
			FROM items child
			JOIN hierarchy parent ON child.parent_id = parent.id
				AND child.workspace_id = parent.workspace_id
		)
		SELECT DISTINCT id, workspace_id, parent_id, start_date, end_date
		FROM hierarchy
		ORDER BY id
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, false, fmt.Errorf("query roadmap hierarchy dates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]models.RoadmapHierarchyDate, 0)
	for rows.Next() {
		var item models.RoadmapHierarchyDate
		var parentID sql.NullInt64
		var startDate, endDate sql.NullTime
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &parentID, &startDate, &endDate); err != nil {
			return nil, false, fmt.Errorf("scan roadmap hierarchy dates: %w", err)
		}
		assignNullableInt(&item.ParentID, parentID)
		if startDate.Valid {
			item.StartDate = startDate.Time.Format("2006-01-02")
		}
		if endDate.Valid {
			item.EndDate = endDate.Time.Format("2006-01-02")
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate roadmap hierarchy dates: %w", err)
	}

	truncated := len(items) > maxRoadmapHierarchyItems
	if truncated {
		items = items[:maxRoadmapHierarchyItems]
	}
	return items, truncated, nil
}

func uniquePositiveInts(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	result := make([]int, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
