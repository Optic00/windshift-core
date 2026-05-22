package repository

import (
	"database/sql"
	"strings"

	"windshift/internal/database"
)

// ItemChangeRow is a compact item change-log entry for delta polling.
type ItemChangeRow struct {
	ItemID  int
	Deleted bool
}

// ItemChangeRepository provides data access for item delta/change-log queries.
type ItemChangeRepository struct {
	db database.Database
}

// NewItemChangeRepository creates an ItemChangeRepository.
func NewItemChangeRepository(db database.Database) *ItemChangeRepository {
	return &ItemChangeRepository{db: db}
}

// CollectionExistsInWorkspace reports whether collectionID exists, and if a
// workspaceID is supplied, whether it belongs to that workspace.
func (r *ItemChangeRepository) CollectionExistsInWorkspace(collectionID, workspaceID int) (bool, error) {
	var collectionWorkspaceID sql.NullInt64
	err := r.db.QueryRow("SELECT workspace_id FROM collections WHERE id = ?", collectionID).Scan(&collectionWorkspaceID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if workspaceID > 0 && collectionWorkspaceID.Valid && int(collectionWorkspaceID.Int64) != workspaceID {
		return false, nil
	}
	return true, nil
}

// CurrentWatermark returns the maximum item_change_log id visible in scope.
func (r *ItemChangeRepository) CurrentWatermark(accessibleWorkspaceIDs []int, workspaceID int) (int64, error) {
	where, args := itemChangeScopeWhere(accessibleWorkspaceIDs, workspaceID, 0)
	var watermark sql.NullInt64
	err := r.db.QueryRow("SELECT COALESCE(MAX(id), 0) FROM item_change_log "+where, args...).Scan(&watermark)
	if err != nil {
		return 0, err
	}
	return watermark.Int64, nil
}

// QuerySince returns grouped item changes after the given watermark, capped by limit.
func (r *ItemChangeRepository) QuerySince(accessibleWorkspaceIDs []int, workspaceID int, since int64, limit int) ([]ItemChangeRow, error) {
	where, args := itemChangeScopeWhere(accessibleWorkspaceIDs, workspaceID, since)
	rows, err := r.db.Query(`
		SELECT item_id, MAX(CASE WHEN change_type = 'delete' THEN 1 ELSE 0 END) AS deleted
		FROM item_change_log
		`+where+`
		GROUP BY item_id
		ORDER BY MAX(id) ASC
		LIMIT ?
	`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	changes := []ItemChangeRow{}
	for rows.Next() {
		var change ItemChangeRow
		var deleted int
		if err := rows.Scan(&change.ItemID, &deleted); err != nil {
			return nil, err
		}
		change.Deleted = deleted > 0
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

func itemChangeScopeWhere(accessibleWorkspaceIDs []int, workspaceID int, since int64) (where string, args []interface{}) {
	clauses := []string{}
	if since > 0 {
		clauses = append(clauses, "id > ?")
		args = append(args, since)
	}
	if workspaceID > 0 {
		clauses = append(clauses, "workspace_id = ?")
		args = append(args, workspaceID)
	} else {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(accessibleWorkspaceIDs)), ",")
		clauses = append(clauses, "workspace_id IN ("+placeholders+")")
		for _, id := range accessibleWorkspaceIDs {
			args = append(args, id)
		}
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}
