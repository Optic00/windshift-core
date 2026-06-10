package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"
)

type BoardConfigurationHandler struct {
	repo              *repository.BoardConfigurationRepository
	collections       *repository.CollectionRepository
	permissionService *services.PermissionService
}

func NewBoardConfigurationHandler(repo *repository.BoardConfigurationRepository, collections *repository.CollectionRepository, permissionService *services.PermissionService) *BoardConfigurationHandler {
	return &BoardConfigurationHandler{repo: repo, collections: collections, permissionService: permissionService}
}

// checkCollectionAccess verifies the user can READ the collection (public or
// owned by user). Returns true if access is granted, false if denied
// (response already written). Do NOT use this for write paths — see
// checkCollectionWriteAccess.
func (h *BoardConfigurationHandler) checkCollectionAccess(w http.ResponseWriter, r *http.Request, collectionID int) bool {
	currentUser := utils.GetCurrentUser(r)
	if currentUser == nil {
		respondUnauthorized(w, r)
		return false
	}

	coll, err := h.collections.GetByID(collectionID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "collection")
		return false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}

	if !coll.IsPublic && (coll.CreatedBy == nil || *coll.CreatedBy != currentUser.ID) {
		respondNotFound(w, r, "collection")
		return false
	}
	return true
}

// checkCollectionWriteAccess verifies the user can MUTATE board configs for
// the collection. `is_public = true` does NOT grant write access — only
// ownership (created_by == currentUser.ID) does. Returns 404 on denial to
// avoid leaking collection existence.
func (h *BoardConfigurationHandler) checkCollectionWriteAccess(w http.ResponseWriter, r *http.Request, collectionID int) bool {
	currentUser := utils.GetCurrentUser(r)
	if currentUser == nil {
		respondUnauthorized(w, r)
		return false
	}

	coll, err := h.collections.GetByID(collectionID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "collection")
		return false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}

	if coll.CreatedBy == nil || *coll.CreatedBy != currentUser.ID {
		respondNotFound(w, r, "collection")
		return false
	}
	return true
}

// checkBoardConfigWriteAccess looks up the collection/workspace associated
// with a board config and verifies the user has WRITE access.
func (h *BoardConfigurationHandler) checkBoardConfigWriteAccess(w http.ResponseWriter, r *http.Request, configID int) bool {
	collID, wsID, ok := h.loadBoardConfigScope(w, r, configID)
	if !ok {
		return false
	}
	if wsID != nil {
		return h.checkWorkspaceWriteAccess(w, r, *wsID)
	}
	if collID != nil {
		return h.checkCollectionWriteAccess(w, r, *collID)
	}
	return true
}

// loadBoardConfigScope reads the (collection_id, workspace_id) pair for a
// board config or writes the appropriate not-found response.
func (h *BoardConfigurationHandler) loadBoardConfigScope(w http.ResponseWriter, r *http.Request, configID int) (collID, wsID *int, ok bool) {
	collID, wsID, err := h.repo.GetScope(configID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "board_configuration")
		return nil, nil, false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return nil, nil, false
	}
	return collID, wsID, true
}

// checkWorkspaceAccess verifies the user has READ (`item.view`) permission on
// the workspace. Returns 404 on permission denial to prevent workspace
// existence leakage.
func (h *BoardConfigurationHandler) checkWorkspaceAccess(w http.ResponseWriter, r *http.Request, workspaceID int) bool {
	return h.checkWorkspacePerm(w, r, workspaceID, models.PermissionItemView)
}

// checkWorkspaceWriteAccess verifies the user has ADMIN (`workspace.admin`)
// permission on the workspace. The workspace-default board configuration
// reshapes the layout (columns, backlog, list/card fields, roadmap) for every
// viewer of the workspace, so write access is gated to workspace admins —
// `item.edit` is not enough. Returns 404 on permission denial.
func (h *BoardConfigurationHandler) checkWorkspaceWriteAccess(w http.ResponseWriter, r *http.Request, workspaceID int) bool {
	return h.checkWorkspacePerm(w, r, workspaceID, models.PermissionWorkspaceAdmin)
}

func (h *BoardConfigurationHandler) checkWorkspacePerm(w http.ResponseWriter, r *http.Request, workspaceID int, perm string) bool {
	currentUser := utils.GetCurrentUser(r)
	if currentUser == nil {
		respondUnauthorized(w, r)
		return false
	}
	hasPermission, err := h.permissionService.HasWorkspacePermission(currentUser.ID, workspaceID, perm)
	if err != nil || !hasPermission {
		respondNotFound(w, r, "board_configuration")
		return false
	}
	return true
}

// GetByCollection returns the board configuration for a specific collection or workspace
func (h *BoardConfigurationHandler) GetByCollection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var config *models.BoardConfiguration
	var err error

	// Check if this is a workspace-level config request
	if id == "default" {
		// Workspace-level configuration
		workspaceIDStr := r.URL.Query().Get("workspace_id")
		if workspaceIDStr == "" {
			respondValidationError(w, r, "workspace_id query parameter required for default configuration")
			return
		}

		workspaceID, parseErr := strconv.Atoi(workspaceIDStr)
		if parseErr != nil {
			respondInvalidID(w, r, "workspace_id")
			return
		}

		if !h.checkWorkspaceAccess(w, r, workspaceID) {
			return
		}

		// Get workspace board configuration
		config, err = h.repo.GetByWorkspaceID(workspaceID)

		// Every workspace logically has a default board configuration even when
		// no row has been persisted yet — return an empty config scoped to the
		// workspace so the frontend can render defaults without a 404.
		if errors.Is(err, repository.ErrNotFound) {
			wid := workspaceID
			respondJSONOK(w, models.BoardConfiguration{WorkspaceID: &wid})
			return
		}
	} else {
		// Collection-level configuration
		collectionID, parseErr := strconv.Atoi(id)
		if parseErr != nil {
			respondInvalidID(w, r, "id")
			return
		}

		if !h.checkCollectionAccess(w, r, collectionID) {
			return
		}

		config, err = h.repo.GetByCollectionID(collectionID)
	}

	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "board_configuration")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Get the columns with status mappings
	columns, err := h.repo.GetColumnsWithStatuses(config.ID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	config.Columns = columns

	respondJSONOK(w, config)
}

// CreateForCollection creates a new board configuration for a collection or workspace
func (h *BoardConfigurationHandler) CreateForCollection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	req, ok := decodeJSON[models.BoardConfigurationRequest](w, r)
	if !ok {
		return
	}
	// Each board column carries a user-facing Name + Color. Color is
	// a CSS value (hex / rgb) — ShortIdentifier matches the slice 1
	// precedent for asset types.
	sanitizeBoardColumnRequests(req.Columns)

	slog.Info("creating board configuration", "id", id, "columns_count", len(req.Columns), "backlog_status_ids", req.BacklogStatusIDs)

	var collectionID *int
	var workspaceID *int

	// Check if this is a workspace-level config request
	if id == "default" {
		// Workspace-level configuration
		workspaceIDStr := r.URL.Query().Get("workspace_id")
		if workspaceIDStr == "" {
			respondValidationError(w, r, "workspace_id query parameter required for default configuration")
			return
		}

		wsID, parseErr := strconv.Atoi(workspaceIDStr)
		if parseErr != nil {
			respondInvalidID(w, r, "workspace_id")
			return
		}

		if !h.checkWorkspaceWriteAccess(w, r, wsID) {
			return
		}
		workspaceID = &wsID
	} else {
		// Collection-level configuration
		collID, parseErr := strconv.Atoi(id)
		if parseErr != nil {
			respondInvalidID(w, r, "id")
			return
		}

		if !h.checkCollectionWriteAccess(w, r, collID) {
			return
		}
		collectionID = &collID
	}

	configID, err := h.repo.Create(collectionID, workspaceID, &req)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Return the created configuration
	config := models.BoardConfiguration{
		ID:                        configID,
		CollectionID:              collectionID,
		WorkspaceID:               workspaceID,
		ListColumns:               req.ListColumns,
		CardFields:                req.CardFields,
		ShowRightmostColumnLast50: req.ShowRightmostColumnLast50,
		CreatedAt:                 time.Now(),
		UpdatedAt:                 time.Now(),
	}
	columns, _ := h.repo.GetColumnsWithStatuses(configID)
	config.Columns = columns

	respondJSONCreated(w, config)
}

// UpdateForCollection updates the board configuration for a collection
func (h *BoardConfigurationHandler) UpdateForCollection(w http.ResponseWriter, r *http.Request) {
	configID, ok := requireIDParam(w, r, "configId")
	if !ok {
		return
	}

	// Verify WRITE access to the board config's collection or workspace
	if !h.checkBoardConfigWriteAccess(w, r, configID) {
		return
	}

	req, ok := decodeJSON[models.BoardConfigurationRequest](w, r)
	if !ok {
		return
	}
	sanitizeBoardColumnRequests(req.Columns)

	slog.Info("updating board configuration", "config_id", configID, "columns_count", len(req.Columns), "backlog_status_ids", req.BacklogStatusIDs)

	if err := h.repo.Update(configID, &req); err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Return the updated configuration
	config, err := h.repo.GetByID(configID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	columns, _ := h.repo.GetColumnsWithStatuses(configID)
	config.Columns = columns

	respondJSONOK(w, config)
}

// DeleteForCollection deletes the board configuration for a collection
func (h *BoardConfigurationHandler) DeleteForCollection(w http.ResponseWriter, r *http.Request) {
	configID, ok := requireIDParam(w, r, "configId")
	if !ok {
		return
	}

	// Verify WRITE access to the board config's collection or workspace
	if !h.checkBoardConfigWriteAccess(w, r, configID) {
		return
	}

	// Delete the configuration (cascade will handle columns and status mappings)
	if err := h.repo.Delete(configID); err != nil {
		respondInternalError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// sanitizeBoardColumnRequests scrubs the user-facing fields on each
// column in a Create/Update payload. Name is the column label; Color is
// a CSS hex/rgb value (ShortIdentifier matches the slice-1 precedent
// for asset types and statuses).
func sanitizeBoardColumnRequests(cols []models.BoardColumnRequest) {
	for i := range cols {
		sanitize.ApplyAll(
			sanitize.Pair{Target: &cols[i].Name, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &cols[i].Color, Policy: sanitize.ShortIdentifier},
		)
	}
}
