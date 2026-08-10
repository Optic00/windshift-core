package handlers

import (
	"net/http"

	"windshift/internal/repository"
)

// StatusQueryHandler serves status queries outside the generic CRUD surface.
type StatusQueryHandler struct {
	repo *repository.StatusRepository
}

// NewStatusQueryHandler creates a StatusQueryHandler.
func NewStatusQueryHandler(repo *repository.StatusRepository) *StatusQueryHandler {
	return &StatusQueryHandler{repo: repo}
}

// GetNonDoneStatusIDs returns status IDs outside completed categories.
func (h *StatusQueryHandler) GetNonDoneStatusIDs(w http.ResponseWriter, r *http.Request) {
	ids, err := h.repo.ListNonDoneIDs()
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, ids)
}
