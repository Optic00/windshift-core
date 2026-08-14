package handlers

import (
	"errors"
	"net/http"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"
)

// LabelHandler handles label CRUD and item-label management endpoints
type LabelHandler struct {
	repo              *repository.LabelRepository
	itemRepo          *repository.ItemRepository
	permissionService *services.PermissionService
	auditor           *logger.Auditor
}

// NewLabelHandler creates a new LabelHandler
func NewLabelHandler(
	repo *repository.LabelRepository,
	itemRepo *repository.ItemRepository,
	permissionService *services.PermissionService,
	auditor *logger.Auditor,
) *LabelHandler {
	return &LabelHandler{
		repo:              repo,
		itemRepo:          itemRepo,
		permissionService: permissionService,
		auditor:           auditor,
	}
}

// GetAll lists the global label catalog.
func (h *LabelHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	if _, ok := RequireAuth(w, r); !ok {
		return
	}
	labels, err := h.repo.ListAll()
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, labels)
}

// Get returns a single label by ID
func (h *LabelHandler) Get(w http.ResponseWriter, r *http.Request) {
	if _, ok := RequireAuth(w, r); !ok {
		return
	}
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	label, err := h.repo.GetByID(id)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "Label")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, label)
}

// Create creates a new label
func (h *LabelHandler) Create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		WorkspaceID int    `json:"workspace_id"`
	}
	if err := newJSONDecoder(w, r).Decode(&input); err != nil {
		respondBadRequest(w, r, err.Error())
		return
	}

	input.Name = sanitize.ShortIdentifier.Sanitize(input.Name)
	if input.Name == "" {
		respondValidationError(w, r, "Label name is required")
		return
	}
	if input.WorkspaceID == 0 {
		respondValidationError(w, r, "workspace_id is required")
		return
	}
	if input.Color == "" {
		input.Color = "#3B82F6"
	}

	// Check workspace permission
	if !h.requireWorkspaceEditPermission(w, r, input.WorkspaceID) {
		return
	}

	// Check uniqueness
	exists, err := h.repo.NameExists(input.Name, 0)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if exists {
		respondConflict(w, r, "A label with this name already exists")
		return
	}

	id, _, err := h.repo.Create(input.Name, input.Color)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateEntry) {
			respondConflict(w, r, "A label with this name already exists")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	label, err := h.repo.GetByID(int(id))
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		h.auditor.Log(r, currentUser, logger.ActionLabelCreate, logger.ResourceLabel, &label.ID, label.Name)
	}

	respondJSONCreated(w, label)
}

// Update updates a label's name and/or color
func (h *LabelHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	var input struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := newJSONDecoder(w, r).Decode(&input); err != nil {
		respondBadRequest(w, r, err.Error())
		return
	}

	input.Name = sanitize.ShortIdentifier.Sanitize(input.Name)
	if input.Name == "" {
		respondValidationError(w, r, "Label name is required")
		return
	}

	if _, err := h.repo.GetByID(id); errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "Label")
		return
	} else if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Check uniqueness (excluding current)
	exists, err := h.repo.NameExists(input.Name, id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if exists {
		respondConflict(w, r, "A label with this name already exists")
		return
	}

	if input.Color == "" {
		input.Color = "#3B82F6"
	}

	if err := h.repo.Update(id, input.Name, input.Color); err != nil {
		if errors.Is(err, repository.ErrDuplicateEntry) {
			respondConflict(w, r, "A label with this name already exists")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	label, err := h.repo.GetByID(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		h.auditor.Log(r, currentUser, logger.ActionLabelUpdate, logger.ResourceLabel, &id, label.Name)
	}

	respondJSONOK(w, label)
}

// Delete deletes a label (cascade removes from items)
func (h *LabelHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	if _, err := h.repo.GetByID(id); errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "Label")
		return
	} else if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if err := h.repo.Delete(id); err != nil {
		respondInternalError(w, r, err)
		return
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		h.auditor.Log(r, currentUser, logger.ActionLabelDelete, logger.ResourceLabel, &id, "")
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetItemLabels returns labels for a specific item
func (h *LabelHandler) GetItemLabels(w http.ResponseWriter, r *http.Request) {
	itemID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	if !CheckItemPermission(w, r, h.itemRepo, h.permissionService, itemID, models.PermissionItemView) {
		return
	}

	h.respondItemLabels(w, r, itemID)
}

// checkItemEditPermission checks if the current user can edit the given item
func (h *LabelHandler) checkItemEditPermission(w http.ResponseWriter, r *http.Request, itemID int) bool {
	return CheckItemPermission(w, r, h.itemRepo, h.permissionService, itemID, models.PermissionItemEdit)
}

func (h *LabelHandler) labelsExist(w http.ResponseWriter, r *http.Request, labelIDs []int) bool {
	for _, labelID := range labelIDs {
		_, err := h.repo.GetByID(labelID)
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "Label")
			return false
		}
		if err != nil {
			respondInternalError(w, r, err)
			return false
		}
	}
	return true
}

// requireWorkspaceEditPermission verifies the current user has edit permission
// on the given workspace. Returns false (and writes an HTTP error) on failure.
func (h *LabelHandler) requireWorkspaceEditPermission(w http.ResponseWriter, r *http.Request, workspaceID int) bool {
	if h.permissionService != nil {
		user := utils.GetCurrentUser(r)
		if user == nil {
			respondUnauthorized(w, r)
			return false
		}
		hasPermission, permErr := h.permissionService.HasWorkspacePermission(user.ID, workspaceID, models.PermissionItemEdit)
		if permErr != nil || !hasPermission {
			respondNotFound(w, r, "Label")
			return false
		}
	}
	return true
}

// SetItemLabels replaces all labels on an item
func (h *LabelHandler) SetItemLabels(w http.ResponseWriter, r *http.Request) {
	itemID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	if !h.checkItemEditPermission(w, r, itemID) {
		return
	}

	var input struct {
		LabelIDs []int `json:"label_ids"`
	}
	if err := newJSONDecoder(w, r).Decode(&input); err != nil {
		respondBadRequest(w, r, err.Error())
		return
	}
	if !h.labelsExist(w, r, input.LabelIDs) {
		return
	}

	if err := h.repo.ReplaceItemLabels(itemID, input.LabelIDs); err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Return the updated labels
	h.respondItemLabels(w, r, itemID)
}

// AddItemLabel adds a single label to an item
func (h *LabelHandler) AddItemLabel(w http.ResponseWriter, r *http.Request) {
	itemID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	if !h.checkItemEditPermission(w, r, itemID) {
		return
	}

	var input struct {
		LabelID int `json:"label_id"`
	}
	if err := newJSONDecoder(w, r).Decode(&input); err != nil {
		respondBadRequest(w, r, err.Error())
		return
	}
	if input.LabelID == 0 {
		respondValidationError(w, r, "label_id is required")
		return
	}
	if !h.labelsExist(w, r, []int{input.LabelID}) {
		return
	}

	if err := h.repo.AddItemLabel(itemID, input.LabelID); err != nil {
		if errors.Is(err, repository.ErrDuplicateEntry) {
			respondConflict(w, r, "Label is already assigned to this item")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	h.respondItemLabels(w, r, itemID)
}

// RemoveItemLabel removes a label from an item
func (h *LabelHandler) RemoveItemLabel(w http.ResponseWriter, r *http.Request) {
	itemID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	if !h.checkItemEditPermission(w, r, itemID) {
		return
	}

	labelID, ok := requireIDParam(w, r, "labelId")
	if !ok {
		return
	}

	if err := h.repo.RemoveItemLabel(itemID, labelID); err != nil {
		respondInternalError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// respondItemLabels is a helper to return labels for an item
func (h *LabelHandler) respondItemLabels(w http.ResponseWriter, r *http.Request, itemID int) {
	labels, err := h.repo.ListForItem(itemID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, labels)
}
