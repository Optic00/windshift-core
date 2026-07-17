package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"windshift/internal/models"
	"windshift/internal/services"
)

const (
	analyticsDefaultRangeDays = 84
	analyticsMaximumRangeDays = 366
)

// AnalyticsHandler handles analytics endpoints for workspaces.
type AnalyticsHandler struct {
	analyticsService  *services.AnalyticsService
	permissionService *services.PermissionService
	keyCache          *WorkspaceKeyCache
}

// NewAnalyticsHandler creates a new analytics handler.
func NewAnalyticsHandler(
	analyticsService *services.AnalyticsService,
	permissionService *services.PermissionService,
	keyCache *WorkspaceKeyCache,
) *AnalyticsHandler {
	return &AnalyticsHandler{
		analyticsService:  analyticsService,
		permissionService: permissionService,
		keyCache:          keyCache,
	}
}

// GetAnalytics handles GET /workspaces/{id}/analytics
// Aggregated endpoint that resolves a dataset (collection or workspace) and computes all panels.
func (h *AnalyticsHandler) GetAnalytics(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireWorkspaceIDParam(w, r, h.keyCache, "id")
	if !ok {
		return
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	if !h.hasViewPermission(w, r, user.ID, workspaceID) {
		return
	}

	startDate, endDate, err := parseAnalyticsDateRange(r.URL.Query(), time.Now())
	if err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	// Optional collection scope
	var collectionID int
	if cid := r.URL.Query().Get("collection_id"); cid != "" {
		n, err := strconv.Atoi(cid)
		if err != nil || n <= 0 {
			respondValidationError(w, r, "collection_id must be a positive integer")
			return
		}
		collectionID = n
	}

	// A caller with view on workspace X must not be able to fetch analytics
	// for a collection that lives in workspace Y by passing ?collection_id=Y.
	// Hide existence (404) rather than 403, per repo-wide convention.
	if collectionID > 0 {
		collWsID, err := h.analyticsService.GetCollectionWorkspaceIDContext(r.Context(), collectionID)
		if errors.Is(err, services.ErrAnalyticsCollectionNotFound) {
			respondNotFound(w, r, "Collection")
			return
		}
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if collWsID == 0 || collWsID != workspaceID {
			respondNotFound(w, r, "Collection")
			return
		}
	}

	// Optional direct QL query
	qlQuery := r.URL.Query().Get("ql")

	result, err := h.analyticsService.GetAnalyticsContext(r.Context(), services.ResolveDatasetParams{
		WorkspaceID:  workspaceID,
		CollectionID: collectionID,
		QLQuery:      qlQuery,
		UserID:       user.ID,
		StartDate:    startDate,
		EndDate:      endDate,
	})
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, result)
}

func parseAnalyticsDateRange(values url.Values, now time.Time) (startDate, endDate time.Time, err error) {
	year, month, day := now.Date()
	endDate = time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	startDate = endDate.AddDate(0, 0, -(analyticsDefaultRangeDays - 1))

	if raw := values.Get("start_date"); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("start_date must use YYYY-MM-DD")
		}
		startDate = parsed
	}
	if raw := values.Get("end_date"); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("end_date must use YYYY-MM-DD")
		}
		endDate = parsed
	}
	if startDate.After(endDate) {
		return time.Time{}, time.Time{}, fmt.Errorf("start_date must be on or before end_date")
	}
	inclusiveDays := int(endDate.Sub(startDate).Hours()/24) + 1
	if inclusiveDays > analyticsMaximumRangeDays {
		return time.Time{}, time.Time{}, fmt.Errorf("date range cannot exceed %d days", analyticsMaximumRangeDays)
	}
	return startDate, endDate, nil
}

func (h *AnalyticsHandler) hasViewPermission(w http.ResponseWriter, r *http.Request, userID, workspaceID int) bool {
	hasPerm, err := h.permissionService.HasWorkspacePermission(userID, workspaceID, models.PermissionItemView)
	if err != nil || !hasPerm {
		respondNotFound(w, r, "Workspace")
		return false
	}
	return true
}
