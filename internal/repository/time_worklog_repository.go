package repository

import (
	"fmt"
	"time"

	"windshift/internal/database"
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
