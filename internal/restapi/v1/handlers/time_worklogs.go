package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"
)

// TimeWorklogHandler provides the v1 bearer-token REST surface for time
// tracking worklogs. Worklogs are user-scoped: listing returns the
// authenticated user's entries; create/update/delete target their own.
type TimeWorklogHandler struct {
	BaseHandler
	timePerm *services.TimePermissionService
}

// NewTimeWorklogHandler wires the handler with the shared permission pipeline.
func NewTimeWorklogHandler(base BaseHandler, timePerm *services.TimePermissionService) *TimeWorklogHandler {
	return &TimeWorklogHandler{
		BaseHandler: base,
		timePerm:    timePerm,
	}
}

type worklogResponse struct {
	ID                  int      `json:"id"`
	ProjectID           int      `json:"project_id"`
	CustomerID          int      `json:"customer_id"`
	ItemID              *int     `json:"item_id,omitempty"`
	Description         string   `json:"description"`
	Date                int64    `json:"date"`
	StartTime           int64    `json:"start_time"`
	EndTime             int64    `json:"end_time"`
	DurationMinutes     int      `json:"duration_minutes"`
	CreatedAt           int64    `json:"created_at"`
	UpdatedAt           int64    `json:"updated_at"`
	CustomerName        string   `json:"customer_name,omitempty"`
	ProjectName         string   `json:"project_name,omitempty"`
	ItemTitle           string   `json:"item_title,omitempty"`
	WorkspaceID         *int     `json:"workspace_id,omitempty"`
	WorkspaceKey        string   `json:"workspace_key,omitempty"`
	WorkspaceItemNumber int      `json:"workspace_item_number,omitempty"`
	ProjectMaxHours     *float64 `json:"project_max_hours,omitempty"`
	ProjectTotalHours   *float64 `json:"project_total_hours,omitempty"`
}

func mapWorklogToResponse(wl models.Worklog) worklogResponse {
	return worklogResponse{
		ID:                  wl.ID,
		ProjectID:           wl.ProjectID,
		CustomerID:          wl.CustomerID,
		ItemID:              wl.ItemID,
		Description:         wl.Description,
		Date:                wl.Date,
		StartTime:           wl.StartTime,
		EndTime:             wl.EndTime,
		DurationMinutes:     wl.DurationMins,
		CreatedAt:           wl.CreatedAt,
		UpdatedAt:           wl.UpdatedAt,
		CustomerName:        wl.CustomerName,
		ProjectName:         wl.ProjectName,
		ItemTitle:           wl.ItemTitle,
		WorkspaceID:         wl.WorkspaceID,
		WorkspaceKey:        wl.WorkspaceKey,
		WorkspaceItemNumber: wl.WorkspaceItemNumber,
		ProjectMaxHours:     wl.ProjectMaxHours,
		ProjectTotalHours:   wl.ProjectTotalHours,
	}
}

// ListMine returns worklogs for the authenticated user.
func (h *TimeWorklogHandler) ListMine(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}

	pagination := h.ParsePagination(r)
	dateFrom := r.URL.Query().Get("date_from")
	dateTo := r.URL.Query().Get("date_to")
	projectIDStr := r.URL.Query().Get("project_id")

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
	var qa []any
	qa = append(qa, user.ID)

	if dateFrom != "" {
		t, err := time.Parse("2006-01-02", dateFrom)
		if err != nil {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "invalid date_from format, use YYYY-MM-DD"))
			return
		}
		query += " AND w.date >= ?"
		qa = append(qa, t.Unix())
	}
	if dateTo != "" {
		t, err := time.Parse("2006-01-02", dateTo)
		if err != nil {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "invalid date_to format, use YYYY-MM-DD"))
			return
		}
		query += " AND w.date <= ?"
		qa = append(qa, t.Add(24*time.Hour-time.Second).Unix())
	}
	if projectIDStr != "" {
		pid, err := strconv.Atoi(projectIDStr)
		if err != nil || pid <= 0 {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "invalid project_id"))
			return
		}
		query += " AND w.project_id = ?"
		qa = append(qa, pid)
	}
	query += " ORDER BY w.date DESC"

	// Count total
	countQuery := "SELECT COUNT(*) FROM (" + query + ")"
	var total int
	if err := h.DB.QueryRow(countQuery, qa...).Scan(&total); err != nil {
		h.RespondInternalError(w, r)
		return
	}

	// Apply pagination
	query += " LIMIT ? OFFSET ?"
	qa = append(qa, pagination.Limit, pagination.Offset)

	rows, err := h.DB.Query(query, qa...)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	defer rows.Close()

	out := make([]worklogResponse, 0)
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
		wl.WorkspaceID = utils.NullInt64ToPtr(workspaceID)
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
		out = append(out, mapWorklogToResponse(wl))
	}
	if err := rows.Err(); err != nil {
		h.RespondInternalError(w, r)
		return
	}

	h.RespondPaginated(w, out, pagination, total)
}

type createWorklogRequest struct {
	ProjectID       int    `json:"project_id"`
	Description     string `json:"description"`
	Date            string `json:"date"`
	Duration        string `json:"duration,omitempty"`
	DurationMinutes int    `json:"duration_minutes,omitempty"`
	StartTime       string `json:"start_time,omitempty"`
	EndTime         string `json:"end_time,omitempty"`
	ItemID          *int   `json:"item_id,omitempty"`
	ItemKey         string `json:"item_key,omitempty"`
}

// Create logs a new time entry for the authenticated user.
func (h *TimeWorklogHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}

	var req createWorklogRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}

	if req.ProjectID == 0 || req.Description == "" || req.Date == "" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "project_id, description, and date are required"))
		return
	}

	canBook, err := h.timePerm.CanBookTimeOnProject(user.ID, req.ProjectID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	if !canBook {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusForbidden, "FORBIDDEN", "no permission to book time on this project"))
		return
	}

	sanitize.Apply(&req.Description, sanitize.RichText)

	var projectName, projectStatus string
	var customerID sql.NullInt64
	err = h.DB.QueryRow("SELECT name, status, customer_id FROM time_projects WHERE id = ?", req.ProjectID).
		Scan(&projectName, &projectStatus, &customerID)
	if err != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusNotFound, "NOT_FOUND", "project not found"))
		return
	}
	if projectStatus != "Active" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, fmt.Sprintf("project %q is not active (status: %s)", projectName, projectStatus)))
		return
	}
	if !customerID.Valid {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "project has no customer assigned, cannot log time"))
		return
	}

	date, err := time.Parse("2006-01-02", req.Date)
	if err != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "invalid date format, use YYYY-MM-DD"))
		return
	}

	var durationMins int
	var startUnix, endUnix int64
	switch {
	case req.Duration != "":
		dur, err := utils.ParseDuration(req.Duration)
		if err != nil {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, fmt.Sprintf("invalid duration: %s", err.Error())))
			return
		}
		durationMins = int(dur.Minutes())
		startUnix = date.Unix()
		endUnix = date.Add(dur).Unix()
	case req.DurationMinutes > 0:
		durationMins = req.DurationMinutes
		startUnix = date.Unix()
		endUnix = date.Add(time.Duration(req.DurationMinutes) * time.Minute).Unix()
	case req.StartTime != "" && req.EndTime != "":
		sp := strings.SplitN(req.StartTime, ":", 2)
		ep := strings.SplitN(req.EndTime, ":", 2)
		if len(sp) != 2 || len(ep) != 2 {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "start_time and end_time must be in HH:MM format"))
			return
		}
		sh, e1 := strconv.Atoi(sp[0])
		sm, e2 := strconv.Atoi(sp[1])
		eh, e3 := strconv.Atoi(ep[0])
		em, e4 := strconv.Atoi(ep[1])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "start_time and end_time must be in HH:MM format"))
			return
		}
		st := date.Add(time.Duration(sh)*time.Hour + time.Duration(sm)*time.Minute)
		et := date.Add(time.Duration(eh)*time.Hour + time.Duration(em)*time.Minute)
		if !et.After(st) {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "end_time must be after start_time"))
			return
		}
		durationMins = int(et.Sub(st).Minutes())
		startUnix, endUnix = st.Unix(), et.Unix()
	default:
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "provide duration, duration_minutes, or start_time and end_time"))
		return
	}

	if durationMins <= 0 {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "duration must be positive"))
		return
	}

	// Resolve optional item link
	var itemID *int
	if req.ItemKey != "" {
		id, err := resolveItemByKey(h.DB, h.PermissionService, user.ID, req.ItemKey)
		if err != nil {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusNotFound, "NOT_FOUND", "item not found"))
			return
		}
		itemID = &id
	} else if req.ItemID != nil && *req.ItemID > 0 {
		itemID = req.ItemID
	}

	id, err := repository.NewTimeWorklogRepository(h.DB).Create(repository.NewWorklog{
		ProjectID:       req.ProjectID,
		CustomerID:      customerID.Int64,
		UserID:          user.ID,
		ItemID:          itemID,
		Description:     req.Description,
		DateUnix:        date.Unix(),
		StartTimeUnix:   startUnix,
		EndTimeUnix:     endUnix,
		DurationMinutes: durationMins,
	})
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}

	h.RespondCreated(w, map[string]any{
		"id":               id,
		"project_id":       req.ProjectID,
		"project_name":     projectName,
		"date":             req.Date,
		"duration_minutes": durationMins,
		"description":      req.Description,
	})
}

// Update changes the description of an existing worklog. Only the owning user can update.
func (h *TimeWorklogHandler) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}

	worklogID, ok := h.ParsePathID(w, r, "id", "worklog ID")
	if !ok {
		return
	}

	var req struct {
		Description string `json:"description"`
	}
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}

	var ownerID int
	err := h.DB.QueryRow("SELECT user_id FROM time_worklogs WHERE id = ?", worklogID).Scan(&ownerID)
	if err != nil {
		h.RespondNotFound(w, r)
		return
	}
	if ownerID != user.ID {
		h.RespondError(w, r, restapi.ErrForbidden)
		return
	}

	sanitize.Apply(&req.Description, sanitize.RichText)
	now := time.Now().Unix()
	_, err = h.DB.Exec("UPDATE time_worklogs SET description = ?, updated_at = ? WHERE id = ?", req.Description, now, worklogID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}

	h.RespondOK(w, map[string]any{"id": worklogID, "updated": true})
}

// Delete removes a worklog. Only the owning user can delete.
func (h *TimeWorklogHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}

	worklogID, ok := h.ParsePathID(w, r, "id", "worklog ID")
	if !ok {
		return
	}

	var ownerID int
	err := h.DB.QueryRow("SELECT user_id FROM time_worklogs WHERE id = ?", worklogID).Scan(&ownerID)
	if err != nil {
		h.RespondNotFound(w, r)
		return
	}
	if ownerID != user.ID {
		h.RespondError(w, r, restapi.ErrForbidden)
		return
	}

	_, err = h.DB.Exec("DELETE FROM time_worklogs WHERE id = ?", worklogID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}

	h.RespondNoContent(w)
}

// resolveItemByKey looks up an item by its key (e.g. "PROJ-42") and verifies
// the user can view the item's workspace. Returns the item's numeric ID.
func resolveItemByKey(db database.Database, permService *services.PermissionService, userID int, itemKey string) (int, error) {
	parts := strings.SplitN(itemKey, "-", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid item key format")
	}
	wsKey := parts[0]
	itemNumStr := parts[1]
	itemNum, err := strconv.Atoi(itemNumStr)
	if err != nil {
		return 0, fmt.Errorf("invalid item number in key")
	}

	var itemID, wsID int
	err = db.QueryRow(`
		SELECT i.id, i.workspace_id
		FROM items i
		JOIN workspaces w ON i.workspace_id = w.id
		WHERE w.key = ? AND i.workspace_item_number = ?`,
		wsKey, itemNum,
	).Scan(&itemID, &wsID)
	if err != nil {
		return 0, fmt.Errorf("item not found")
	}

	canView, err := permService.HasWorkspacePermission(userID, wsID, models.PermissionItemView)
	if err != nil || !canView {
		return 0, fmt.Errorf("item not found")
	}

	return itemID, nil
}
