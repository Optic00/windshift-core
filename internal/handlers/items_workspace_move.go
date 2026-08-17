package handlers

import (
	"errors"
	"net/http"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
	"windshift/internal/validation"
)

func (h *ItemHandler) PreviewWorkspaceMove(w http.ResponseWriter, r *http.Request) {
	itemID, input, _, ok := h.requireWorkspaceMove(w, r)
	if !ok {
		return
	}
	preview, err := h.itemWorkspaceMove.Preview(itemID, input)
	if err != nil {
		h.respondWorkspaceMoveError(w, r, err)
		return
	}
	respondJSONOK(w, preview)
}

func (h *ItemHandler) MoveWorkspace(w http.ResponseWriter, r *http.Request) {
	itemID, input, user, ok := h.requireWorkspaceMove(w, r)
	if !ok {
		return
	}
	result, err := h.itemWorkspaceMove.MoveContext(r.Context(), itemID, user.ID, input)
	if err != nil {
		h.respondWorkspaceMoveError(w, r, err)
		return
	}

	h.invalidateEffectiveProjectSubtree(itemID)
	for _, childID := range result.DetachedChildIDs {
		h.invalidateEffectiveProjectSubtree(childID)
	}
	logAuditWithDetails(h.db, r, user, logger.ActionItemMoveWorkspace, logger.ResourceItem, &itemID, result.Item.Title, map[string]any{
		"old_key":               result.OldKey,
		"new_key":               result.NewKey,
		"fields":                result.Preview.Fields,
		"labels_kept":           result.Preview.LabelsKept,
		"labels_dropped":        result.Preview.LabelsDropped,
		"custom_fields_kept":    result.Preview.CustomFieldsKept,
		"custom_fields_dropped": result.Preview.CustomFieldsDropped,
	})
	respondJSONOK(w, result)
}

func (h *ItemHandler) requireWorkspaceMove(w http.ResponseWriter, r *http.Request) (int, services.ItemWorkspaceMoveInput, *models.User, bool) {
	itemID, ok := requireIDParam(w, r, "id")
	if !ok {
		return 0, services.ItemWorkspaceMoveInput{}, nil, false
	}
	input, ok := decodeJSON[services.ItemWorkspaceMoveInput](w, r)
	if !ok {
		return 0, services.ItemWorkspaceMoveInput{}, nil, false
	}
	if input.DestinationWorkspaceID <= 0 {
		respondValidationError(w, r, "destination_workspace_id is required")
		return 0, input, nil, false
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return 0, input, nil, false
	}

	sourceWorkspaceID, err := repository.NewItemRepository(h.db).GetWorkspaceID(itemID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "item")
		return 0, input, nil, false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return 0, input, nil, false
	}
	canEdit, err := h.canEditItem(user.ID, sourceWorkspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return 0, input, nil, false
	}
	canCreate := false
	if canEdit && h.permissionService != nil {
		canCreate, err = h.permissionService.HasWorkspacePermission(user.ID, input.DestinationWorkspaceID, models.PermissionItemCreate)
		if err != nil {
			respondInternalError(w, r, err)
			return 0, input, nil, false
		}
	}
	if !canEdit || !canCreate {
		respondNotFound(w, r, "item")
		return 0, input, nil, false
	}
	return itemID, input, user, true
}

func (h *ItemHandler) respondWorkspaceMoveError(w http.ResponseWriter, r *http.Request, err error) {
	var validationErr *validation.ValidationError
	switch {
	case errors.Is(err, repository.ErrNotFound):
		respondNotFound(w, r, "item or destination workspace")
	case errors.Is(err, services.ErrItemWorkspaceMoveSameWorkspace),
		errors.Is(err, services.ErrItemWorkspaceMoveInvalidType),
		errors.Is(err, services.ErrItemWorkspaceMoveInvalidStatus),
		errors.Is(err, services.ErrItemWorkspaceMoveInvalidPriority):
		respondValidationError(w, r, err.Error())
	case errors.As(err, &validationErr):
		respondValidationError(w, r, validationErr.Error())
	default:
		respondInternalError(w, r, err)
	}
}
