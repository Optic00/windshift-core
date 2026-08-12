package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"
)

type TestFolderHandler struct {
	service *services.TestFolderService
	auditor *logger.Auditor
}

func NewTestFolderHandler(service *services.TestFolderService, auditor *logger.Auditor) *TestFolderHandler {
	return &TestFolderHandler{
		service: service,
		auditor: auditor,
	}
}

func (h *TestFolderHandler) writeFolderServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		respondNotFound(w, r, "test_folder")
	case errors.Is(err, services.ErrTestFolderNameRequired):
		respondValidationError(w, r, "Folder name is required")
	case errors.Is(err, services.ErrTestFolderParentNotFound),
		errors.Is(err, services.ErrTestFolderNestedDepth),
		errors.Is(err, services.ErrTestFolderParentSelf),
		errors.Is(err, services.ErrTestFolderParentHasChildren):
		respondValidationError(w, r, err.Error())
	default:
		respondInternalError(w, r, err)
	}
}

// GetAllFolders returns all test folders with test case counts
func (h *TestFolderHandler) GetAllFolders(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	folders, err := h.service.List(workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, folders)
}

// GetFolder returns a single test folder
func (h *TestFolderHandler) GetFolder(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	folder, err := h.service.Get(workspaceID, id)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "test_folder")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, folder)
}

// CreateFolder creates a new test folder
func (h *TestFolderHandler) CreateFolder(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	user := utils.GetCurrentUser(r)

	folder, ok := decodeJSON[models.TestFolder](w, r)
	if !ok {
		return
	}

	folder.Name = sanitize.PlainTextField.Sanitize(folder.Name)
	folder.Description = sanitize.RichText.Sanitize(folder.Description)

	created, err := h.service.Create(workspaceID, folder)
	if err != nil {
		h.writeFolderServiceError(w, r, err)
		return
	}

	h.auditor.Log(r, user, logger.ActionTestFolderCreate, logger.ResourceTestFolder, &created.ID, created.Name)

	respondJSONCreated(w, created)
}

// UpdateFolder updates an existing test folder
func (h *TestFolderHandler) UpdateFolder(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	user := utils.GetCurrentUser(r)

	body, err := restapi.ReadJSONBody(w, r)
	if err != nil {
		if isRequestBodyTooLarge(err) {
			respondRequestTooLarge(w, r)
			return
		}
		respondBadRequest(w, r, "Invalid request body")
		return
	}

	var folder models.TestFolder
	if err = json.Unmarshal(body, &folder); err != nil {
		respondBadRequest(w, r, "Invalid request body")
		return
	}

	folder.Name = sanitize.PlainTextField.Sanitize(folder.Name)
	folder.Description = sanitize.RichText.Sanitize(folder.Description)

	var rawPayload map[string]json.RawMessage
	if err = json.Unmarshal(body, &rawPayload); err != nil {
		respondBadRequest(w, r, "Invalid request body")
		return
	}

	_, parentProvided := rawPayload["parent_id"]
	_, sortOrderProvided := rawPayload["sort_order"]

	updated, err := h.service.Update(workspaceID, id, services.TestFolderUpdateInput{
		Folder:            folder,
		ParentProvided:    parentProvided,
		SortOrderProvided: sortOrderProvided,
	})
	if err != nil {
		h.writeFolderServiceError(w, r, err)
		return
	}

	h.auditor.Log(r, user, logger.ActionTestFolderUpdate, logger.ResourceTestFolder, &id, updated.Name)

	respondJSONOK(w, updated)
}

// DeleteFolder deletes a test folder (test cases will be moved to no folder)
func (h *TestFolderHandler) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	workspaceID, id, user, ok := requireWorkspaceIDAndID(w, r)
	if !ok {
		return
	}

	if err := h.service.Delete(workspaceID, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "test_folder")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	h.auditor.Log(r, user, logger.ActionTestFolderDelete, logger.ResourceTestFolder, &id, "")

	w.WriteHeader(http.StatusNoContent)
}

// ReorderFolders updates the sort order of multiple folders
func (h *TestFolderHandler) ReorderFolders(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	var reorderData struct {
		FolderIDs []int `json:"folder_ids"`
	}

	if err := newJSONDecoder(w, r).Decode(&reorderData); err != nil {
		respondBadRequest(w, r, "Invalid request body")
		return
	}

	if err := h.service.Reorder(workspaceID, reorderData.FolderIDs); err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, map[string]bool{"success": true})
}
