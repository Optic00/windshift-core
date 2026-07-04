package repository

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
)

// TimeWorklogRepository persists rows in the time_worklogs table. The AI
// log_time tool routes through here; the HTTP TimeWorklogHandler.Create and
// the timer-stop path (ActiveTimerRepository.CreateWorklog) still inline the
// same INSERT and are follow-up candidates for migrating here.
type TimeWorklogRepository struct {
	db database.Database
}

// NewTimeWorklogRepository creates a TimeWorklogRepository.
func NewTimeWorklogRepository(db database.Database) *TimeWorklogRepository {
	return &TimeWorklogRepository{db: db}
}

// NewWorklog captures the fields needed to insert a worklog row. Date and
// start/end times are unix seconds, matching the table's storage format.
type NewWorklog struct {
	ProjectID       int
	CustomerID      int64
	UserID          int
	ItemID          *int // nil when the worklog isn't linked to a work item
	Description     string
	DateUnix        int64
	StartTimeUnix   int64
	EndTimeUnix     int64
	DurationMinutes int
}

// Create inserts a worklog row, stamping created_at/updated_at, and returns
// the new row's id.
func (r *TimeWorklogRepository) Create(in NewWorklog) (int64, error) {
	now := time.Now().Unix()
	var id int64
	err := r.db.QueryRow(`
		INSERT INTO time_worklogs (project_id, customer_id, user_id, item_id, description, date, start_time, end_time, duration_minutes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		in.ProjectID, in.CustomerID, in.UserID, in.ItemID, in.Description,
		in.DateUnix, in.StartTimeUnix, in.EndTimeUnix, in.DurationMinutes, now, now,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create worklog: %w", err)
	}
	return id, nil
}

// WorklogListFilter narrows ListForUser results. Nil pointer fields disable
// the corresponding filter; date bounds are unix seconds (inclusive).
type WorklogListFilter struct {
	UserID       int
	DateFromUnix *int64
	DateToUnix   *int64
	ProjectID    *int
	Limit        int
	Offset       int
}

// ListForUser returns a page of the user's worklogs, newest first, with the
// joined display fields (customer/project/item names, workspace reference,
// project budget figures) populated. The second return value is the total
// match count before pagination.
func (r *TimeWorklogRepository) ListForUser(f WorklogListFilter) ([]models.Worklog, int, error) {
	query := `SELECT w.id, w.project_id, w.customer_id, w.item_id, w.description, w.date, w.start_time,
	       w.end_time, w.duration_minutes, w.created_at, w.updated_at,
	       c.name, p.name, i.title, ws.id, ws.key, i.workspace_item_number,
	       p.settings as project_settings,
	       (SELECT COALESCE(SUM(duration_minutes), 0) / 60.0 FROM time_worklogs WHERE project_id = w.project_id) as project_total_hours
	FROM time_worklogs w
	JOIN customer_organisations c ON w.customer_id = c.id
	JOIN time_projects p ON w.project_id = p.id
	LEFT JOIN items i ON w.item_id = i.id
	LEFT JOIN workspaces ws ON i.workspace_id = ws.id
	WHERE w.user_id = ?`
	qa := []any{f.UserID}

	if f.DateFromUnix != nil {
		query += " AND w.date >= ?"
		qa = append(qa, *f.DateFromUnix)
	}
	if f.DateToUnix != nil {
		query += " AND w.date <= ?"
		qa = append(qa, *f.DateToUnix)
	}
	if f.ProjectID != nil {
		query += " AND w.project_id = ?"
		qa = append(qa, *f.ProjectID)
	}
	query += " ORDER BY w.date DESC"

	var total int
	if err := r.db.QueryRow("SELECT COUNT(*) FROM ("+query+")", qa...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count worklogs: %w", err)
	}

	query += " LIMIT ? OFFSET ?"
	qa = append(qa, f.Limit, f.Offset)

	rows, err := r.db.Query(query, qa...)
	if err != nil {
		return nil, 0, fmt.Errorf("list worklogs: %w", err)
	}
	defer rows.Close()

	out := make([]models.Worklog, 0)
	for rows.Next() {
		var wl models.Worklog
		var itemTitle, workspaceKey, projectSettings sql.NullString
		var workspaceID, workspaceItemNumber sql.NullInt64
		var projectTotalHours sql.NullFloat64
		if err := rows.Scan(&wl.ID, &wl.ProjectID, &wl.CustomerID, &wl.ItemID, &wl.Description,
			&wl.Date, &wl.StartTime, &wl.EndTime, &wl.DurationMins,
			&wl.CreatedAt, &wl.UpdatedAt, &wl.CustomerName, &wl.ProjectName, &itemTitle,
			&workspaceID, &workspaceKey, &workspaceItemNumber, &projectSettings, &projectTotalHours); err != nil {
			continue
		}
		wl.ItemTitle = itemTitle.String
		if workspaceID.Valid {
			id := int(workspaceID.Int64)
			wl.WorkspaceID = &id
		}
		wl.WorkspaceKey = workspaceKey.String
		wl.WorkspaceItemNumber = int(workspaceItemNumber.Int64)
		if projectTotalHours.Valid {
			wl.ProjectTotalHours = &projectTotalHours.Float64
		}
		if projectSettings.Valid && projectSettings.String != "" {
			var settings map[string]interface{}
			if err := json.Unmarshal([]byte(projectSettings.String), &settings); err == nil {
				if maxHours, ok := settings["max_hours"].(float64); ok && maxHours > 0 {
					wl.ProjectMaxHours = &maxHours
				}
			}
		}
		out = append(out, wl)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list worklogs: %w", err)
	}
	return out, total, nil
}

// GetOwnerID returns the user_id that owns a worklog. Returns ErrNotFound
// when the worklog does not exist.
func (r *TimeWorklogRepository) GetOwnerID(worklogID int) (int, error) {
	var ownerID int
	err := r.db.QueryRow("SELECT user_id FROM time_worklogs WHERE id = ?", worklogID).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("get worklog owner: %w", err)
	}
	return ownerID, nil
}

// UpdateDescription replaces a worklog's description, stamping updated_at.
func (r *TimeWorklogRepository) UpdateDescription(worklogID int, description string) error {
	_, err := r.db.ExecWrite("UPDATE time_worklogs SET description = ?, updated_at = ? WHERE id = ?",
		description, time.Now().Unix(), worklogID)
	if err != nil {
		return fmt.Errorf("update worklog description: %w", err)
	}
	return nil
}

// Delete removes a worklog row.
func (r *TimeWorklogRepository) Delete(worklogID int) error {
	if _, err := r.db.ExecWrite("DELETE FROM time_worklogs WHERE id = ?", worklogID); err != nil {
		return fmt.Errorf("delete worklog: %w", err)
	}
	return nil
}
