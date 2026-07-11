package handlers

import (
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

// ListMine handles GET /rest/api/v1/time/worklogs
//
// @Summary      List my worklogs
// @Description  Returns the authenticated user's worklogs, newest first, with optional date-range and project filters.
// @Tags         time-tracking
// @Produce      json
// @Security     BearerAuth
// @Param        date_from   query     string  false  "Inclusive start date (YYYY-MM-DD)"
// @Param        date_to     query     string  false  "Inclusive end date (YYYY-MM-DD)"
// @Param        project_id  query     int     false  "Filter by time project ID"
// @Param        page        query     int     false  "Page (1-indexed)"
// @Param        limit       query     int     false  "Page size"
// @Success      200  {object}  handlers.PaginatedResponse{data=[]handlers.worklogResponse}
// @Failure      400  {object}  restapi.ErrorResponse
// @Failure      401  {object}  restapi.ErrorResponse
// @Router       /time/worklogs [get]
func (h *TimeWorklogHandler) ListMine(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}

	pagination := h.ParsePagination(r)
	filter := repository.WorklogListFilter{
		UserID: user.ID,
		Limit:  pagination.Limit,
		Offset: pagination.Offset,
	}

	if dateFrom := r.URL.Query().Get("date_from"); dateFrom != "" {
		t, err := time.Parse("2006-01-02", dateFrom)
		if err != nil {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "invalid date_from format, use YYYY-MM-DD"))
			return
		}
		from := t.Unix()
		filter.DateFromUnix = &from
	}
	if dateTo := r.URL.Query().Get("date_to"); dateTo != "" {
		t, err := time.Parse("2006-01-02", dateTo)
		if err != nil {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "invalid date_to format, use YYYY-MM-DD"))
			return
		}
		to := t.Add(24*time.Hour - time.Second).Unix()
		filter.DateToUnix = &to
	}
	if projectIDStr := r.URL.Query().Get("project_id"); projectIDStr != "" {
		pid, err := strconv.Atoi(projectIDStr)
		if err != nil || pid <= 0 {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "invalid project_id"))
			return
		}
		filter.ProjectID = &pid
	}

	worklogs, total, err := repository.NewTimeWorklogRepository(h.DB).ListForUser(filter)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}

	out := make([]worklogResponse, 0, len(worklogs))
	for _, wl := range worklogs {
		out = append(out, mapWorklogToResponse(wl))
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

// Create handles POST /rest/api/v1/time/worklogs
//
// @Summary      Log time
// @Description  Creates a worklog for the authenticated user. Provide either `duration`/`duration_minutes` or a `start_time`+`end_time` pair; `item_id`/`item_key` optionally link the entry to a work item.
// @Tags         time-tracking
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body  body      handlers.createWorklogRequest  true  "Worklog to create"
// @Success      201   {object}  map[string]interface{}
// @Failure      400   {object}  restapi.ErrorResponse
// @Failure      401   {object}  restapi.ErrorResponse
// @Failure      403   {object}  restapi.ErrorResponse  "No permission to book time on this project"
// @Router       /time/worklogs [post]
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

	project, err := repository.NewTimeProjectRepository(h.DB).GetBookingInfo(req.ProjectID)
	if err != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusNotFound, "NOT_FOUND", "project not found"))
		return
	}
	if project.Status != "Active" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, fmt.Sprintf("project %q is not active (status: %s)", project.Name, project.Status)))
		return
	}
	if project.CustomerID == nil {
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
		CustomerID:      *project.CustomerID,
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
		"project_name":     project.Name,
		"date":             req.Date,
		"duration_minutes": durationMins,
		"description":      req.Description,
	})
}

type updateWorklogRequest struct {
	Description string `json:"description"`
}

// Update handles PUT /rest/api/v1/time/worklogs/{id}
//
// @Summary      Update a worklog description
// @Description  Changes the description of an existing worklog. Only the owning user may update; other users get 403.
// @Tags         time-tracking
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id    path      int                            true  "Worklog ID"
// @Param        body  body      handlers.updateWorklogRequest  true  "New description"
// @Success      200   {object}  map[string]interface{}
// @Failure      400   {object}  restapi.ErrorResponse
// @Failure      401   {object}  restapi.ErrorResponse
// @Failure      403   {object}  restapi.ErrorResponse  "Caller does not own the worklog"
// @Failure      404   {object}  restapi.ErrorResponse
// @Router       /time/worklogs/{id} [put]
func (h *TimeWorklogHandler) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}

	worklogID, ok := h.ParsePathID(w, r, "id", "worklog ID")
	if !ok {
		return
	}

	var req updateWorklogRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}

	worklogRepo := repository.NewTimeWorklogRepository(h.DB)
	ownerID, err := worklogRepo.GetOwnerID(worklogID)
	if err != nil {
		h.RespondNotFound(w, r)
		return
	}
	if ownerID != user.ID {
		h.RespondError(w, r, restapi.ErrForbidden)
		return
	}

	sanitize.Apply(&req.Description, sanitize.RichText)
	if err := worklogRepo.UpdateDescription(worklogID, req.Description); err != nil {
		h.RespondInternalError(w, r)
		return
	}

	h.RespondOK(w, map[string]any{"id": worklogID, "updated": true})
}

// Delete handles DELETE /rest/api/v1/time/worklogs/{id}
//
// @Summary      Delete a worklog
// @Description  Removes a worklog. Only the owning user may delete; other users get 403.
// @Tags         time-tracking
// @Security     BearerAuth
// @Param        id   path  int  true  "Worklog ID"
// @Success      204  "Deleted"
// @Failure      400  {object}  restapi.ErrorResponse
// @Failure      401  {object}  restapi.ErrorResponse
// @Failure      403  {object}  restapi.ErrorResponse  "Caller does not own the worklog"
// @Failure      404  {object}  restapi.ErrorResponse
// @Router       /time/worklogs/{id} [delete]
func (h *TimeWorklogHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}

	worklogID, ok := h.ParsePathID(w, r, "id", "worklog ID")
	if !ok {
		return
	}

	worklogRepo := repository.NewTimeWorklogRepository(h.DB)
	ownerID, err := worklogRepo.GetOwnerID(worklogID)
	if err != nil {
		h.RespondNotFound(w, r)
		return
	}
	if ownerID != user.ID {
		h.RespondError(w, r, restapi.ErrForbidden)
		return
	}

	if err := worklogRepo.Delete(worklogID); err != nil {
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
	itemNum, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid item number in key")
	}

	itemRepo := repository.NewItemRepository(db)
	itemID, err := itemRepo.FindIDByKeyAndNumber(parts[0], itemNum)
	if err != nil {
		return 0, fmt.Errorf("item not found")
	}
	wsID, err := itemRepo.GetWorkspaceID(itemID)
	if err != nil {
		return 0, fmt.Errorf("item not found")
	}

	canView, err := permService.HasWorkspacePermission(userID, wsID, models.PermissionItemView)
	if err != nil || !canView {
		return 0, fmt.Errorf("item not found")
	}

	return itemID, nil
}
