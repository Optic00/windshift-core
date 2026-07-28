package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"windshift/internal/repository"
	"windshift/internal/restapi"
)

const (
	defaultItemChangeLimit = 100
	maxItemChangeLimit     = 500
)

type itemChangeResponse struct {
	ItemID     int    `json:"item_id"`
	ChangeType string `json:"change_type"`
}

type itemChangesResponse struct {
	Changes       []itemChangeResponse `json:"changes"`
	NextCursor    string               `json:"next_cursor"`
	Watermark     string               `json:"watermark"`
	HasMore       bool                 `json:"has_more"`
	ResetRequired bool                 `json:"reset_required"`
}

// ListChanges handles GET /rest/api/v1/items/changes.
//
// @Summary      List ordered item changes
// @Description  Returns item upsert/delete events for one visible workspace. Omit since to obtain a cursor before a full load. Keep passing the first page's watermark as through while paging.
// @Tags         items
// @Produce      json
// @Security     BearerAuth
// @Param        workspace_id query int    true  "Workspace ID"
// @Param        since        query string false "Exclusive change cursor"
// @Param        through      query string false "Inclusive snapshot watermark"
// @Param        limit        query int    false "Changes per page (max 500)"
// @Success      200 {object} itemChangesResponse
// @Failure      400 {object} handlers.ErrorResponse
// @Failure      401 {object} handlers.ErrorResponse
// @Failure      403 {object} handlers.ErrorResponse
// @Failure      404 {object} handlers.ErrorResponse
// @Failure      500 {object} handlers.ErrorResponse
// @Router       /items/changes [get]
func (h *ItemHandler) ListChanges(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}

	workspaceID, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("workspace_id")))
	if err != nil || workspaceID <= 0 {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "workspace_id must be a positive integer"))
		return
	}
	canView, err := h.Perms.CanViewWorkspace(user.ID, workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	if !canView {
		h.RespondError(w, r, restapi.ErrWorkspaceNotFound)
		return
	}

	sinceRaw := strings.TrimSpace(r.URL.Query().Get("since"))
	since, valid := parseItemChangeCursor(sinceRaw)
	if !valid {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "since must be a non-negative integer"))
		return
	}
	limit := defaultItemChangeLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit <= 0 || limit > maxItemChangeLimit {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "limit must be between 1 and 500"))
			return
		}
	}

	changeRepo := repository.NewItemChangeRepository(h.DB)
	throughRaw := strings.TrimSpace(r.URL.Query().Get("through"))
	var currentWatermark int64
	if throughRaw == "" {
		currentWatermark, err = changeRepo.StableCurrentWatermark([]int{workspaceID}, workspaceID)
	} else {
		currentWatermark, err = changeRepo.CurrentWatermark([]int{workspaceID}, workspaceID)
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	response := itemChangesResponse{
		Changes:    []itemChangeResponse{},
		NextCursor: strconv.FormatInt(currentWatermark, 10),
		Watermark:  strconv.FormatInt(currentWatermark, 10),
	}
	if sinceRaw == "" {
		h.RespondOK(w, response)
		return
	}
	if since > currentWatermark {
		response.ResetRequired = true
		h.RespondOK(w, response)
		return
	}

	through := currentWatermark
	if throughRaw != "" {
		through, valid = parseItemChangeCursor(throughRaw)
		if !valid || through < since {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "through must be an integer greater than or equal to since"))
			return
		}
		if through > currentWatermark {
			response.ResetRequired = true
			h.RespondOK(w, response)
			return
		}
	}
	response.Watermark = strconv.FormatInt(through, 10)

	changes, err := changeRepo.QueryPage([]int{workspaceID}, workspaceID, since, through, limit+1)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	if len(changes) > limit {
		response.HasMore = true
		changes = changes[:limit]
	}
	for _, change := range changes {
		response.Changes = append(response.Changes, itemChangeResponse{
			ItemID:     change.ItemID,
			ChangeType: change.ChangeType,
		})
	}
	if response.HasMore {
		response.NextCursor = strconv.FormatInt(changes[len(changes)-1].Cursor, 10)
	} else {
		response.NextCursor = response.Watermark
	}
	h.RespondOK(w, response)
}

func parseItemChangeCursor(raw string) (int64, bool) {
	if raw == "" {
		return 0, true
	}
	cursor, err := strconv.ParseInt(raw, 10, 64)
	return cursor, err == nil && cursor >= 0
}
