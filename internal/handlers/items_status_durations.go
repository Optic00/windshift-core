package handlers

import (
	"errors"
	"net/http"
	"time"

	"windshift/internal/repository"
)

// GetStatusDurations returns accumulated wall-clock time for every status the
// item has occupied. It is separate from the normal item response because the
// history scan can be comparatively expensive.
func (h *ItemHandler) GetStatusDurations(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.requestDBContext(r)
	defer cancel()
	r = r.WithContext(ctx)

	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	workspaceID, err := h.itemRepo.GetWorkspaceIDCtx(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}
	canView, err := h.canViewItemAsActor(ctx, user.ID, id, workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canView {
		respondNotFound(w, r, "Item")
		return
	}

	durations, err := h.itemRepo.GetStatusDurations(ctx, id, time.Now())
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, durations)
}
