package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"windshift/internal/services"
)

// TimeProjectHandler provides the v1 bearer-token REST surface for time
// tracking projects. It is deployed alongside the cookie-auth time-projects
// handler (internal/handlers/time_projects.go) and uses the same
// TimePermissionService for per-project membership enforcement.
type TimeProjectHandler struct {
	BaseHandler
	timePerm *services.TimePermissionService
}

// NewTimeProjectHandler wires the handler with the shared permission pipeline.
func NewTimeProjectHandler(base BaseHandler, timePerm *services.TimePermissionService) *TimeProjectHandler {
	return &TimeProjectHandler{
		BaseHandler: base,
		timePerm:    timePerm,
	}
}

type timeProjectResponse struct {
	ID            int                    `json:"id"`
	CustomerID    *int                   `json:"customer_id,omitempty"`
	CategoryID    *int                   `json:"category_id,omitempty"`
	Name          string                 `json:"name"`
	Description   string                 `json:"description"`
	Status        string                 `json:"status"`
	Color         string                 `json:"color,omitempty"`
	HourlyRate    float64                `json:"hourly_rate"`
	Settings      map[string]interface{} `json:"settings,omitempty"`
	CustomerName  string                 `json:"customer_name,omitempty"`
	CategoryName  string                 `json:"category_name,omitempty"`
	CategoryColor string                 `json:"category_color,omitempty"`
	TotalHours    *float64               `json:"total_hours,omitempty"`
}

// List returns time projects accessible to the authenticated user.
func (h *TimeProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}

	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	accessibleIDs, err := h.timePerm.GetAccessibleProjects(user.ID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	if accessibleIDs != nil && len(accessibleIDs) == 0 {
		h.RespondOK(w, []timeProjectResponse{})
		return
	}

	query := `SELECT tp.id, tp.customer_id, tp.category_id, tp.name, COALESCE(tp.description, ''),
	       tp.status, COALESCE(tp.color, ''), tp.hourly_rate, COALESCE(tp.settings, ''),
	       COALESCE(co.name, ''), COALESCE(tpc.name, ''), COALESCE(tpc.color, ''),
	       (SELECT COALESCE(SUM(duration_minutes), 0) / 60.0 FROM time_worklogs WHERE project_id = tp.id) as total_hours
	FROM time_projects tp
	LEFT JOIN customer_organisations co ON tp.customer_id = co.id
	LEFT JOIN time_project_categories tpc ON tp.category_id = tpc.id
	WHERE 1=1`
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

	rows, err := h.DB.Query(query, qa...)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	defer rows.Close()

	out := make([]timeProjectResponse, 0)
	for rows.Next() {
		var p timeProjectResponse
		var settingsStr sql.NullString
		var totalHours sql.NullFloat64
		if err := rows.Scan(&p.ID, &p.CustomerID, &p.CategoryID, &p.Name, &p.Description,
			&p.Status, &p.Color, &p.HourlyRate, &settingsStr, &p.CustomerName,
			&p.CategoryName, &p.CategoryColor, &totalHours); err != nil {
			continue
		}
		if totalHours.Valid {
			p.TotalHours = &totalHours.Float64
		}
		if settingsStr.Valid && settingsStr.String != "" && settingsStr.String != "{}" {
			// Settings are stored as JSON; pass through as-is without
			// unmarshal → marshal round-trip.
			var m map[string]interface{}
			_ = json.Unmarshal([]byte(settingsStr.String), &m)
			if m != nil {
				p.Settings = m
			}
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		h.RespondInternalError(w, r)
		return
	}

	h.RespondOK(w, out)
}

// Get returns a single time project by ID if the user can access it.
func (h *TimeProjectHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}

	projectID, ok := h.ParsePathID(w, r, "id", "time project ID")
	if !ok {
		return
	}

	canView, err := h.timePerm.CanViewProject(user.ID, projectID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	if !canView {
		h.RespondNotFound(w, r)
		return
	}

	query := `SELECT tp.id, tp.customer_id, tp.category_id, tp.name, COALESCE(tp.description, ''),
	       tp.status, COALESCE(tp.color, ''), tp.hourly_rate, COALESCE(tp.settings, ''),
	       COALESCE(co.name, ''), COALESCE(tpc.name, ''), COALESCE(tpc.color, ''),
	       (SELECT COALESCE(SUM(duration_minutes), 0) / 60.0 FROM time_worklogs WHERE project_id = tp.id) as total_hours
	FROM time_projects tp
	LEFT JOIN customer_organisations co ON tp.customer_id = co.id
	LEFT JOIN time_project_categories tpc ON tp.category_id = tpc.id
	WHERE tp.id = ?`

	var p timeProjectResponse
	var settingsStr sql.NullString
	var totalHours sql.NullFloat64
	err = h.DB.QueryRow(query, projectID).Scan(&p.ID, &p.CustomerID, &p.CategoryID, &p.Name, &p.Description,
		&p.Status, &p.Color, &p.HourlyRate, &settingsStr, &p.CustomerName,
		&p.CategoryName, &p.CategoryColor, &totalHours)
	if err != nil {
		h.RespondNotFound(w, r)
		return
	}
	if totalHours.Valid {
		p.TotalHours = &totalHours.Float64
	}
	if settingsStr.Valid && settingsStr.String != "" && settingsStr.String != "{}" {
		var m map[string]interface{}
		_ = json.Unmarshal([]byte(settingsStr.String), &m)
		if m != nil {
			p.Settings = m
		}
	}

	h.RespondOK(w, p)
}
