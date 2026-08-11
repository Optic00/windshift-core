package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"windshift/internal/models"
	"windshift/internal/restapi"
	"windshift/internal/services"
)

type pageDiagramCreateRequest struct {
	Name                string          `json:"name"`
	Mermaid             string          `json:"mermaid,omitempty"`
	Excalidraw          json.RawMessage `json:"excalidraw,omitempty"`
	Placement           string          `json:"placement"`
	ExpectedContentHash *string         `json:"expected_content_hash,omitempty"`
}

type pageDiagramUpdateRequest struct {
	Name                string          `json:"name,omitempty"`
	Mermaid             string          `json:"mermaid,omitempty"`
	Excalidraw          json.RawMessage `json:"excalidraw,omitempty"`
	ExpectedContentHash *string         `json:"expected_content_hash,omitempty"`
}

// ListDiagrams returns the diagrams referenced exactly once by the current
// Page body.
func (h *PageHandler) ListDiagrams(w http.ResponseWriter, r *http.Request) {
	pageID, user, ok := h.requirePageDiagramTarget(w, r)
	if !ok {
		return
	}
	items, err := h.pageDiagrams.List(services.NewAuditActorFromRequest(r, user, nil, "cookie"), pageID)
	if err != nil {
		h.respondPageDiagramError(w, r, err)
		return
	}
	respondJSONOK(w, map[string]any{"items": items})
}

// GetDiagram returns one Page-owned diagram attachment.
func (h *PageHandler) GetDiagram(w http.ResponseWriter, r *http.Request) {
	pageID, user, ok := h.requirePageDiagramTarget(w, r)
	if !ok {
		return
	}
	attachmentID, ok := requireIDParam(w, r, "attachmentId")
	if !ok {
		return
	}
	diagram, err := h.pageDiagrams.Get(
		services.NewAuditActorFromRequest(r, user, nil, "cookie"),
		pageID,
		attachmentID,
	)
	if err != nil {
		h.respondPageDiagramError(w, r, err)
		return
	}
	respondJSONOK(w, diagram)
}

// CreateDiagram atomically uploads a diagram attachment and inserts its Page
// fence. A failed Page mutation compensates by deleting the new attachment.
func (h *PageHandler) CreateDiagram(w http.ResponseWriter, r *http.Request) {
	pageID, user, ok := h.requirePageDiagramTarget(w, r)
	if !ok {
		return
	}
	req, ok := decodeJSON[pageDiagramCreateRequest](w, r)
	if !ok {
		return
	}
	diagram, err := h.pageDiagrams.Create(
		services.NewAuditActorFromRequest(r, user, nil, "cookie"),
		services.CreatePageDiagramInput{
			PageID:              pageID,
			Name:                req.Name,
			Mermaid:             req.Mermaid,
			Excalidraw:          req.Excalidraw,
			Placement:           req.Placement,
			ExpectedContentHash: req.ExpectedContentHash,
		},
	)
	if err != nil {
		h.respondPageDiagramError(w, r, err)
		return
	}
	respondJSONCreated(w, diagram)
}

// UpdateDiagram creates an immutable replacement attachment and atomically
// swaps the Page fence that references the current attachment.
func (h *PageHandler) UpdateDiagram(w http.ResponseWriter, r *http.Request) {
	pageID, user, ok := h.requirePageDiagramTarget(w, r)
	if !ok {
		return
	}
	attachmentID, ok := requireIDParam(w, r, "attachmentId")
	if !ok {
		return
	}
	req, ok := decodeJSON[pageDiagramUpdateRequest](w, r)
	if !ok {
		return
	}
	diagram, err := h.pageDiagrams.Update(
		services.NewAuditActorFromRequest(r, user, nil, "cookie"),
		services.UpdatePageDiagramInput{
			PageID:              pageID,
			AttachmentID:        attachmentID,
			Name:                req.Name,
			Mermaid:             req.Mermaid,
			Excalidraw:          req.Excalidraw,
			ExpectedContentHash: req.ExpectedContentHash,
		},
	)
	if err != nil {
		h.respondPageDiagramError(w, r, err)
		return
	}
	respondJSONOK(w, diagram)
}

func (h *PageHandler) requirePageDiagramTarget(
	w http.ResponseWriter,
	r *http.Request,
) (int, *models.User, bool) {
	workspaceID, pageID, user, ok := h.requireWorkspacePageTarget(w, r)
	if !ok {
		return 0, nil, false
	}
	if h.pageDiagrams == nil {
		respondInternalError(w, r, errors.New("page diagram service is unavailable"))
		return 0, nil, false
	}
	page, err := h.service.GetByID(pageID)
	if err != nil || page.WorkspaceID != workspaceID {
		respondNotFound(w, r, "Page")
		return 0, nil, false
	}
	return pageID, user, true
}

func (h *PageHandler) respondPageDiagramError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, services.ErrPageDiagramNotFound),
		errors.Is(err, services.ErrPageNotFound),
		errors.Is(err, services.ErrPageAttachmentUploadNotFound):
		respondNotFound(w, r, "Page diagram")
	case errors.Is(err, services.ErrPageContentConflict),
		errors.Is(err, services.ErrPageDiagramReferenceConflict):
		respondConflict(w, r, err.Error())
	case errors.Is(err, services.ErrDiagramPayloadTooLarge):
		respondRequestTooLarge(w, r)
	case errors.Is(err, services.ErrPageDiagramNameRequired),
		errors.Is(err, services.ErrPageDiagramPlacementInvalid),
		errors.Is(err, services.ErrDiagramPayloadRequired),
		errors.Is(err, services.ErrDiagramPayloadConflict),
		errors.Is(err, services.ErrDiagramPayloadInvalid),
		errors.Is(err, services.ErrPageAttachmentUploadInvalid):
		respondValidationError(w, r, err.Error())
	case errors.Is(err, services.ErrPageAttachmentUploadDisabled):
		respondError(w, r, restapi.NewAPIError(
			http.StatusServiceUnavailable,
			restapi.ErrCodeValidationFailed,
			"page diagram storage is disabled",
		))
	default:
		respondInternalError(w, r, err)
	}
}
