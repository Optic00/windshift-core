package repository

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"windshift/internal/database"
)

// TimeProjectRepository reads rows from the time_projects table. The v1 REST
// handlers route through here; the cookie-auth TimeProjectsHandler still
// inlines its own SQL and is a follow-up candidate for migrating here.
type TimeProjectRepository struct {
	db database.Database
}

// NewTimeProjectRepository creates a TimeProjectRepository.
func NewTimeProjectRepository(db database.Database) *TimeProjectRepository {
	return &TimeProjectRepository{db: db}
}

// TimeProjectDetail is a time project joined with its customer/category names
// and the project's booked total hours.
type TimeProjectDetail struct {
	ID            int
	CustomerID    *int
	CategoryID    *int
	Name          string
	Description   string
	Status        string
	Color         string
	HourlyRate    float64
	Settings      map[string]interface{} // parsed settings JSON; nil when empty
	CustomerName  string
	CategoryName  string
	CategoryColor string
	TotalHours    *float64
}

const timeProjectDetailSelect = `SELECT tp.id, tp.customer_id, tp.category_id, tp.name, COALESCE(tp.description, ''),
       tp.status, COALESCE(tp.color, ''), tp.hourly_rate, COALESCE(tp.settings, ''),
       COALESCE(co.name, ''), COALESCE(tpc.name, ''), COALESCE(tpc.color, ''),
       (SELECT COALESCE(SUM(duration_minutes), 0) / 60.0 FROM time_worklogs WHERE project_id = tp.id) as total_hours
FROM time_projects tp
LEFT JOIN customer_organisations co ON tp.customer_id = co.id
LEFT JOIN time_project_categories tpc ON tp.category_id = tpc.id`

func scanTimeProjectDetail(scan func(dest ...any) error) (TimeProjectDetail, error) {
	var p TimeProjectDetail
	var settingsStr sql.NullString
	var totalHours sql.NullFloat64
	err := scan(&p.ID, &p.CustomerID, &p.CategoryID, &p.Name, &p.Description,
		&p.Status, &p.Color, &p.HourlyRate, &settingsStr, &p.CustomerName,
		&p.CategoryName, &p.CategoryColor, &totalHours)
	if err != nil {
		return p, err
	}
	if totalHours.Valid {
		p.TotalHours = &totalHours.Float64
	}
	if settingsStr.Valid && settingsStr.String != "" && settingsStr.String != "{}" {
		var m map[string]interface{}
		_ = json.Unmarshal([]byte(settingsStr.String), &m)
		p.Settings = m
	}
	return p, nil
}

// ListDetails returns project detail rows ordered by name. A nil
// accessibleIDs slice means no access restriction; a non-nil slice limits the
// result to those project IDs. An empty statusFilter disables status
// filtering.
func (r *TimeProjectRepository) ListDetails(accessibleIDs []int, statusFilter string) ([]TimeProjectDetail, error) {
	query := timeProjectDetailSelect + "\nWHERE 1=1"
	var qa []any

	if accessibleIDs != nil {
		ph := make([]string, len(accessibleIDs))
		for i, id := range accessibleIDs {
			ph[i] = "?"
			qa = append(qa, id)
		}
		query += " AND tp.id IN (" + strings.Join(ph, ",") + ")"
	}
	if statusFilter != "" {
		query += " AND tp.status = ?"
		qa = append(qa, statusFilter)
	}
	query += " ORDER BY tp.name"

	rows, err := r.db.Query(query, qa...)
	if err != nil {
		return nil, fmt.Errorf("list time projects: %w", err)
	}
	defer rows.Close()

	out := make([]TimeProjectDetail, 0)
	for rows.Next() {
		p, err := scanTimeProjectDetail(rows.Scan)
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list time projects: %w", err)
	}
	return out, nil
}

// GetDetail returns a single project detail row. Returns ErrNotFound when the
// project does not exist.
func (r *TimeProjectRepository) GetDetail(projectID int) (*TimeProjectDetail, error) {
	row := r.db.QueryRow(timeProjectDetailSelect+"\nWHERE tp.id = ?", projectID)
	p, err := scanTimeProjectDetail(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get time project: %w", err)
	}
	return &p, nil
}

// TimeProjectBookingInfo carries the fields needed to validate logging time
// on a project.
type TimeProjectBookingInfo struct {
	Name       string
	Status     string
	CustomerID *int64 // nil when the project has no customer assigned
}

// GetBookingInfo returns the name, status, and customer of a project.
// Returns ErrNotFound when the project does not exist.
func (r *TimeProjectRepository) GetBookingInfo(projectID int) (*TimeProjectBookingInfo, error) {
	var info TimeProjectBookingInfo
	var customerID sql.NullInt64
	err := r.db.QueryRow("SELECT name, status, customer_id FROM time_projects WHERE id = ?", projectID).
		Scan(&info.Name, &info.Status, &customerID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get time project booking info: %w", err)
	}
	if customerID.Valid {
		info.CustomerID = &customerID.Int64
	}
	return &info, nil
}
