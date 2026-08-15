package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
	"windshift/internal/validation"
)

type itemTypeChangeRequest struct {
	TargetItemTypeID int  `json:"target_item_type_id"`
	TargetStatusID   *int `json:"target_status_id,omitempty"`
}

// AnalyzeTypeChange reports whether changing an item's item type would leave
// its current status outside the target type's effective workflow.
func (h *ItemHandler) AnalyzeTypeChange(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	targetID, err := strconvParam(r, "target_item_type_id")
	if err != nil {
		respondValidationError(w, r, "target_item_type_id is required")
		return
	}

	itemRepo := repository.NewItemRepository(h.db)
	item, err := itemRepo.FindByIDWithDetails(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	canEdit, err := h.canEditItem(user.ID, item.WorkspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canEdit {
		respondNotFound(w, r, "Item")
		return
	}

	analysis, err := h.typeChangeService().Analyze(item, targetID)
	if err != nil {
		h.respondItemTypeChangeError(w, r, err)
		return
	}

	respondJSONOK(w, analysis)
}

// ChangeType changes an item's item type. If the target type's effective
// workflow does not contain the current status, callers must provide a target
// status. The status mapping is deliberately conservative: it may not bypass a
// direct condition-gated transition or enter an approval-bound status.
func (h *ItemHandler) ChangeType(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	req, ok := decodeJSON[itemTypeChangeRequest](w, r)
	if !ok {
		return
	}
	if req.TargetItemTypeID <= 0 {
		respondValidationError(w, r, "target_item_type_id is required")
		return
	}

	itemRepo := repository.NewItemRepository(h.db)
	originalItem, err := itemRepo.FindByIDWithDetails(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	canEdit, err := h.canEditItem(user.ID, originalItem.WorkspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canEdit {
		respondNotFound(w, r, "Item")
		return
	}

	svc := h.typeChangeService()
	analysis, err := svc.Analyze(originalItem, req.TargetItemTypeID)
	if err != nil {
		h.respondItemTypeChangeError(w, r, err)
		return
	}
	if sameIntPtrValue(originalItem.ItemTypeID, req.TargetItemTypeID) && !analysis.RequiresMigration {
		// Strip names of time projects the caller has no access to (incl. the
		// inherited effective project), matching the masked read paths. Mask a
		// copy so other consumers of originalItem aren't mutated.
		maskedOriginal := []models.Item{*originalItem}
		h.maskInaccessibleProjectNames(user.ID, maskedOriginal)
		respondJSONOK(w, maskedOriginal[0])
		return
	}

	var nextStatusID *int
	if analysis.RequiresMigration {
		if req.TargetStatusID == nil {
			respondJSON(w, http.StatusConflict, map[string]any{
				"error":    "migration_required",
				"message":  "A target status is required before changing this item type",
				"analysis": analysis,
			})
			return
		}
		if analysis.TargetWorkflowID != nil {
			inWorkflow, err := svc.IsStatusInWorkflow(*req.TargetStatusID, *analysis.TargetWorkflowID)
			if err != nil {
				respondInternalError(w, r, err)
				return
			}
			if !inWorkflow {
				respondValidationError(w, r, "target_status_id is not part of the target item type workflow")
				return
			}
		}
		if err := svc.ValidateStatusMapping(r.Context(), originalItem, req.TargetItemTypeID, analysis.TargetWorkflowID, req.TargetStatusID); err != nil {
			h.respondItemTypeChangeError(w, r, err)
			return
		}
		nextStatusID = req.TargetStatusID
	}

	if h.activityTracker != nil {
		if err := h.activityTracker.TrackItemActivity(user.ID, id, services.ActivityEdit); err != nil {
			slog.Warn("failed to track item edit activity", slog.Int("user_id", user.ID), slog.Int("item_id", id), slog.Any("error", err))
		}
	}

	history, err := svc.ApplyChange(id, user.ID, req.TargetItemTypeID, nextStatusID, originalItem)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	updatedItem, err := itemRepo.FindByIDWithDetails(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	statusChanged := nextStatusID != nil && !intPtrEqual(originalItem.StatusID, updatedItem.StatusID)
	if h.eventCoordinator != nil {
		h.eventCoordinator.EmitItemUpdated(originalItem, updatedItem, statusChanged, false, user.ID, history, user.Username)
	}
	if h.issueSyncService != nil && statusChanged && updatedItem.StatusID != nil {
		go func(ctx context.Context, statusID int) {
			ctx, cancel := issueSyncContext(ctx)
			defer cancel()
			h.issueSyncService.PushStatusToGitHub(ctx, updatedItem.ID, statusID)
		}(r.Context(), *updatedItem.StatusID)
	}

	// Strip names of time projects the caller has no access to (incl. the
	// inherited effective project), matching the masked read paths. Mask a
	// copy so async consumers of updatedItem aren't mutated.
	maskedUpdated := []models.Item{*updatedItem}
	h.maskInaccessibleProjectNames(user.ID, maskedUpdated)

	respondJSONOK(w, maskedUpdated[0])
}

func (h *ItemHandler) typeChangeService() *services.ItemTypeChangeService {
	svc := services.NewItemTypeChangeService(h.db)
	if h.conditionService != nil {
		svc = svc.WithConditionService(h.conditionService)
	}
	return svc
}

func (h *ItemHandler) respondItemTypeChangeError(w http.ResponseWriter, r *http.Request, err error) {
	var valErr *validation.ValidationError
	if errors.As(err, &valErr) {
		respondValidationError(w, r, valErr.Error())
		return
	}
	respondInternalError(w, r, err)
}

func strconvParam(r *http.Request, key string) (int, error) {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return 0, fmt.Errorf("missing %s", key)
	}
	out, err := strconv.Atoi(value)
	if err != nil || out <= 0 {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return out, nil
}

func sameIntPtrValue(ptr *int, value int) bool {
	return ptr != nil && *ptr == value
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
