package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"
)

type TimeWorklogHandler struct {
	worklogs              *repository.TimeWorklogRepository
	projects              *repository.TimeProjectRepository
	items                 *repository.ItemRepository
	permissionService     *services.PermissionService
	timePermissionService *services.TimePermissionService
}

func NewTimeWorklogHandler(db database.Database, permissionService *services.PermissionService, timePermissionService *services.TimePermissionService) *TimeWorklogHandler {
	return &TimeWorklogHandler{
		worklogs:              repository.NewTimeWorklogRepository(db),
		projects:              repository.NewTimeProjectRepository(db),
		items:                 repository.NewItemRepository(db),
		permissionService:     permissionService,
		timePermissionService: timePermissionService,
	}
}

// ParseDuration is re-exported from internal/utils for backward compat.
// New callers should import utils.ParseDuration directly.
func ParseDuration(input string) (time.Duration, error) {
	return utils.ParseDuration(input)
}

type WorklogRequest struct {
	ProjectID     int    `json:"project_id"`
	ItemID        *int   `json:"item_id,omitempty"` // Optional link to work item
	Description   string `json:"description"`
	Date          string `json:"date"`               // YYYY-MM-DD format
	StartTime     string `json:"start_time"`         // HH:MM format or empty
	EndTime       string `json:"end_time"`           // HH:MM format or empty
	DurationInput string `json:"duration"`           // "1h", "30m", "2h15m" etc
	Timezone      string `json:"timezone,omitempty"` // Optional explicit IANA timezone; defaults to acting user
}

// requireWorklogEditAccess extracts the worklog ID, authenticates the user, and verifies
// edit permission (own worklog or manager). Returns the worklog ID, user, and ok bool.
func (h *TimeWorklogHandler) requireWorklogEditAccess(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return 0, false
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return 0, false
	}

	if h.timePermissionService != nil {
		canEdit, err := h.timePermissionService.CanEditWorklog(user.ID, id)
		if err != nil {
			respondInternalError(w, r, err)
			return 0, false
		}
		if !canEdit {
			respondForbidden(w, r)
			return 0, false
		}
	}

	return id, true
}

// filterWorklogsByPermission checks permissions and hides item info if user doesn't have access
func (h *TimeWorklogHandler) filterWorklogsByPermission(worklogs []models.Worklog, userID int) []models.Worklog {
	if h.permissionService == nil {
		slog.Error("permission service unavailable, hiding all item info from worklogs", slog.String("component", "time_tracking"))
		return services.RedactInaccessibleWorklogItems(worklogs, func(int) (bool, error) {
			return false, nil
		})
	}

	return services.RedactInaccessibleWorklogItems(worklogs, func(workspaceID int) (bool, error) {
		return h.permissionService.HasWorkspacePermission(userID, workspaceID, models.PermissionItemView)
	})
}

func applyWorklogDateFilters(r *http.Request, filter *repository.WorklogDetailFilter) {
	if dateFrom := r.URL.Query().Get("date_from"); dateFrom != "" {
		if start, _, err := services.CivilDateRangeUTC(dateFrom, dateFrom, time.UTC); err == nil {
			value := start.Unix()
			filter.DateFromUnix = &value
		}
	}
	if dateTo := r.URL.Query().Get("date_to"); dateTo != "" {
		if _, endExclusive, err := services.CivilDateRangeUTC(dateTo, dateTo, time.UTC); err == nil {
			value := endExclusive.Unix()
			filter.DateToExclusiveUnix = &value
		}
	}
}

func parseWorklogIDFilter(value string) *int {
	if value == "" {
		return nil
	}
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		id = 0
	}
	return &id
}

func (h *TimeWorklogHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Gate by project membership: callers who can't book/view on a project
	// must not see its worklogs. nil = full access (admins, project.manage);
	// empty slice = no accessible projects.
	accessibleProjectIDs, err := h.timePermissionService.GetAccessibleProjects(user.ID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if accessibleProjectIDs != nil && len(accessibleProjectIDs) == 0 {
		respondJSONOK(w, []models.Worklog{})
		return
	}

	filter := repository.WorklogDetailFilter{
		AccessibleProjectIDs: accessibleProjectIDs,
		CustomerID:           parseWorklogIDFilter(r.URL.Query().Get("customer_id")),
		ProjectID:            parseWorklogIDFilter(r.URL.Query().Get("project_id")),
	}
	applyWorklogDateFilters(r, &filter)

	worklogs, err := h.worklogs.ListDetails(filter)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	worklogs = h.filterWorklogsByPermission(worklogs, user.ID)

	respondJSONOK(w, worklogs)
}

func (h *TimeWorklogHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	wl, err := h.worklogs.GetDetail(id)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "worklog")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Don't disclose worklogs on projects the caller can't view. 404 (not 403)
	// matches the item-permission convention: hide existence.
	canView, err := h.timePermissionService.CanViewProject(user.ID, wl.ProjectID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canView {
		respondNotFound(w, r, "worklog")
		return
	}

	// Filter item info by permission
	visible := *wl
	visible.UserID = nil
	visible.UserName = ""
	filtered := h.filterWorklogsByPermission([]models.Worklog{visible}, user.ID)
	respondJSONOK(w, filtered[0])
}

// validateAndParseWorklog validates a WorklogRequest and returns parsed values
//
//nolint:gocritic // tooManyResultsChecker: returns are semantically grouped
func (h *TimeWorklogHandler) validateAndParseWorklog(req WorklogRequest, userTimezone string) (customerID int, date, startTime, endTime time.Time, durationMins int, err error) {
	// Validate project exists, get customer_id, and check status
	project, projectErr := h.projects.GetBookingInfo(req.ProjectID)
	if errors.Is(projectErr, repository.ErrNotFound) {
		err = fmt.Errorf("project not found")
		return
	}
	if projectErr != nil {
		err = projectErr
		return
	}
	if project.CustomerID == nil {
		err = fmt.Errorf("project has no customer assigned")
		return
	}
	customerID = int(*project.CustomerID)

	// Only allow time logging on Active projects
	if project.Status != "Active" {
		err = fmt.Errorf("cannot log time on a project that is not active (status: %s)", project.Status)
		return
	}

	timezone := userTimezone
	if req.Timezone != "" {
		_, location, timezoneErr := services.ResolveTimezone(req.Timezone)
		if timezoneErr != nil {
			err = timezoneErr
			return
		}
		date, err = services.ParseCivilDate(req.Date, location)
	} else {
		_, location := services.ResolveTimezoneOrUTC(timezone)
		date, err = services.ParseCivilDate(req.Date, location)
	}
	if err != nil {
		return
	}

	// Handle time parsing - either explicit times or duration shorthand
	if req.StartTime != "" && req.EndTime != "" {
		var startUnix, endUnix int64
		durationMins, startUnix, endUnix, err = services.ParseWorklogTimes(date, services.WorklogTimeInput{
			StartTime: req.StartTime,
			EndTime:   req.EndTime,
		})
		if err != nil {
			return
		}
		startTime = time.Unix(startUnix, 0)
		endTime = time.Unix(endUnix, 0)
	} else if req.DurationInput != "" {
		var startUnix, endUnix int64
		durationMins, startUnix, endUnix, err = services.ParseWorklogTimes(date, services.WorklogTimeInput{Duration: req.DurationInput})
		if err != nil {
			return
		}
		startTime = time.Unix(startUnix, 0)
		endTime = time.Unix(endUnix, 0)
	} else {
		err = fmt.Errorf("either provide start_time+end_time or duration")
		return
	}

	return
}

func (h *TimeWorklogHandler) Create(w http.ResponseWriter, r *http.Request) {
	// Get user from context
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	var req WorklogRequest
	if err := newJSONDecoder(w, r).Decode(&req); err != nil {
		slog.Debug("JSON decode error", slog.String("component", "time_tracking"), slog.Any("error", err))
		respondBadRequest(w, r, fmt.Sprintf("JSON decode error: %v", err))
		return
	}

	// Check booking permission on project
	if h.timePermissionService != nil {
		canBook, err := h.timePermissionService.CanBookTimeOnProject(user.ID, req.ProjectID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if !canBook {
			respondForbidden(w, r)
			return
		}
	}

	// An item link is optional (item_id is nullable), but when one is supplied
	// the caller must be able to view that item. CheckItemPermission returns 404
	// on both not-found and no-permission so we don't disclose the item (or its
	// title / workspace key) to callers who can't see it.
	if req.ItemID != nil && *req.ItemID > 0 {
		if !CheckItemPermission(w, r, h.items, h.permissionService, *req.ItemID, models.PermissionItemView) {
			return
		}
	}

	// Debug: Log the received request
	slog.Debug("received worklog request", slog.String("component", "time_tracking"), slog.Int("project_id", req.ProjectID), slog.String("description", req.Description))

	warnings := sanitize.ApplyAllWithWarnings(
		sanitize.Pair{Target: &req.Description, Policy: sanitize.Comment, Label: "Description"},
	)

	customerID, date, startTime, endTime, durationMins, err := h.validateAndParseWorklog(req, user.Timezone)
	if err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	// No overlap validation - users should be free to log time as needed

	// Debug: Log the data being inserted
	slog.Debug("inserting worklog", slog.String("component", "time_tracking"), slog.Int("project_id", req.ProjectID), slog.Int("customer_id", customerID), slog.Any("item_id", req.ItemID), slog.String("description", req.Description), slog.Int64("date", date.Unix()), slog.Int64("start_time", startTime.Unix()), slog.Int64("end_time", endTime.Unix()), slog.Int("duration_minutes", durationMins))

	id, err := h.worklogs.Create(repository.NewWorklog{
		ProjectID:       req.ProjectID,
		CustomerID:      int64(customerID),
		UserID:          user.ID,
		ItemID:          req.ItemID,
		Description:     req.Description,
		DateUnix:        services.WorklogDateUnix(date),
		StartTimeUnix:   startTime.Unix(),
		EndTimeUnix:     endTime.Unix(),
		DurationMinutes: durationMins,
	})
	if err != nil {
		slog.Error("database insert error", slog.String("component", "time_tracking"), slog.Any("error", err))
		respondInternalError(w, r, err)
		return
	}

	// Return the created worklog with joined data
	wl, err := h.worklogs.GetDetail(int(id))
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	wl.UserID = nil
	wl.UserName = ""

	respondJSONCreated(w, struct {
		models.Worklog
		Warnings []string `json:"warnings,omitempty"`
	}{*wl, warnings})
}

func (h *TimeWorklogHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireWorklogEditAccess(w, r)
	if !ok {
		return
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	var req WorklogRequest
	if err := newJSONDecoder(w, r).Decode(&req); err != nil {
		slog.Debug("JSON decode error", slog.String("component", "time_tracking"), slog.Any("error", err))
		respondBadRequest(w, r, fmt.Sprintf("JSON decode error: %v", err))
		return
	}

	slog.Debug("received worklog update request", slog.String("component", "time_tracking"), slog.Int("id", id), slog.Int("project_id", req.ProjectID))

	// Mirror Create's guard at line 412: edit access on the existing worklog
	// is not enough — the caller must also be allowed to book on the
	// destination project. Otherwise Update is a privilege-escalation path
	// for moving time into restricted projects.
	canBook, err := h.timePermissionService.CanBookTimeOnProject(user.ID, req.ProjectID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canBook {
		respondForbidden(w, r)
		return
	}

	// The item_id being set on the worklog (it may be changing) is optional, but
	// when present the caller must be able to view it. 404 on failure hides the
	// item rather than disclosing it via the re-read joined row below.
	if req.ItemID != nil && *req.ItemID > 0 {
		if !CheckItemPermission(w, r, h.items, h.permissionService, *req.ItemID, models.PermissionItemView) {
			return
		}
	}

	warnings := sanitize.ApplyAllWithWarnings(
		sanitize.Pair{Target: &req.Description, Policy: sanitize.Comment, Label: "Description"},
	)

	customerID, date, startTime, endTime, durationMins, err := h.validateAndParseWorklog(req, user.Timezone)
	if err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	if err := h.worklogs.Update(repository.UpdateWorklog{
		ID:              id,
		ProjectID:       req.ProjectID,
		CustomerID:      customerID,
		ItemID:          req.ItemID,
		Description:     req.Description,
		DateUnix:        services.WorklogDateUnix(date),
		StartTimeUnix:   startTime.Unix(),
		EndTimeUnix:     endTime.Unix(),
		DurationMinutes: durationMins,
	}); err != nil {
		slog.Error("database update error", slog.String("component", "time_tracking"), slog.Any("error", err))
		respondInternalError(w, r, err)
		return
	}

	// Return the updated worklog with joined data
	wl, err := h.worklogs.GetDetail(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	wl.UserID = nil
	wl.UserName = ""

	respondJSONOK(w, struct {
		models.Worklog
		Warnings []string `json:"warnings,omitempty"`
	}{*wl, warnings})
}

func (h *TimeWorklogHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireWorklogEditAccess(w, r)
	if !ok {
		return
	}

	if err := h.worklogs.Delete(id); err != nil {
		respondInternalError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *TimeWorklogHandler) GetByProject(w http.ResponseWriter, r *http.Request) {
	projectID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Only project managers can view project-level worklogs (includes all users)
	if h.timePermissionService != nil {
		isManager, err := h.timePermissionService.IsTimeProjectManager(user.ID, projectID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if !isManager {
			respondForbidden(w, r)
			return
		}
	}

	filter := repository.WorklogDetailFilter{ProjectID: &projectID}
	applyWorklogDateFilters(r, &filter)
	worklogs, err := h.worklogs.ListDetails(filter)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, worklogs)
}

func (h *TimeWorklogHandler) GetByItem(w http.ResponseWriter, r *http.Request) {
	itemID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	if !CheckItemPermission(w, r, h.items, h.permissionService, itemID, models.PermissionItemView) {
		return
	}

	worklogs, err := h.worklogs.ListDetails(repository.WorklogDetailFilter{ItemID: &itemID})
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, worklogs)
}
