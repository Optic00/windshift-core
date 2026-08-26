package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"windshift/internal/logger"
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
	items             *services.ItemCRUDService
	workspaces        *services.WorkspaceService
	auditor           *logger.Auditor
}

const maxCompletedItemRetentionDays = 3650

func NewBoardConfigurationHandler(
	repo *repository.BoardConfigurationRepository,
	collections *repository.CollectionRepository,
	permissionService *services.PermissionService,
	items *services.ItemCRUDService,
	workspaces *services.WorkspaceService,
	auditor *logger.Auditor,
) *BoardConfigurationHandler {
	return &BoardConfigurationHandler{
		repo: repo, collections: collections, permissionService: permissionService,
		items: items, workspaces: workspaces, auditor: auditor,
	}
}

// checkCollectionAccess verifies the user can READ the collection (public or
// owned by user). Returns true if access is granted, false if denied
// (response already written). Do NOT use this for write paths — see
// checkCollectionWriteAccess.
func (h *BoardConfigurationHandler) checkCollectionAccess(w http.ResponseWriter, r *http.Request, collectionID int) bool {
	_, ok := h.loadReadableCollection(w, r, collectionID)
	return ok
}

func (h *BoardConfigurationHandler) loadReadableCollection(w http.ResponseWriter, r *http.Request, collectionID int) (*repository.CollectionRecord, bool) {
	currentUser := utils.GetCurrentUser(r)
	if currentUser == nil {
		respondUnauthorized(w, r)
		return nil, false
	}

	coll, err := h.collections.GetByID(collectionID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "collection")
		return nil, false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return nil, false
	}

	if !coll.IsPublic && (coll.CreatedBy == nil || *coll.CreatedBy != currentUser.ID) {
		respondNotFound(w, r, "collection")
		return nil, false
	}
	return coll, true
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

func (h *BoardConfigurationHandler) validatePublicCollectionCardFields(w http.ResponseWriter, r *http.Request, collectionID int, fields []models.ListColumn) bool {
	collection, err := h.collections.GetByID(collectionID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "collection")
		return false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if !collection.IsPublic {
		return true
	}
	if err := validatePublicBoardCardFields(fields); err != nil {
		respondValidationError(w, r, err.Error())
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

type boardConfigurationCollection struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	QLQuery     string  `json:"ql_query"`
	IsPublic    bool    `json:"is_public"`
	WorkspaceID *int    `json:"workspace_id,omitempty"`
	CreatedBy   *int    `json:"created_by,omitempty"`
	PublicSlug  *string `json:"public_slug,omitempty"`
}

type boardConfigurationBootstrapResponse struct {
	Collection             *boardConfigurationCollection `json:"collection,omitempty"`
	BoardConfiguration     *models.BoardConfiguration    `json:"board_configuration"`
	Statuses               []models.Status               `json:"statuses"`
	ReferencedWorkspaceIDs []int                         `json:"referenced_workspace_ids"`
}

// GetBootstrap returns the configuration editor's collection metadata,
// persisted board layout and status catalog together. Collection CQL is
// evaluated as a DISTINCT workspace projection, then every workspace's
// available statuses are unioned in one query; matching item rows never cross
// the API boundary and browser request count does not grow with workspace count.
func (h *BoardConfigurationHandler) GetBootstrap(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	if id == "default" {
		workspaceID, ok := boardConfigurationWorkspaceID(w, r, true)
		if !ok {
			return
		}
		if !h.checkWorkspaceAccess(w, r, workspaceID) {
			return
		}

		config, err := h.loadBootstrapConfiguration(nil, &workspaceID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		statuses, err := h.workspaces.GetStatusesForWorkspaces([]int{workspaceID})
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		respondJSONOK(w, boardConfigurationBootstrapResponse{
			BoardConfiguration: config, Statuses: statuses,
			ReferencedWorkspaceIDs: []int{workspaceID},
		})
		return
	}

	collectionID, err := strconv.Atoi(id)
	if err != nil {
		respondInvalidID(w, r, "id")
		return
	}
	collection, ok := h.loadReadableCollection(w, r, collectionID)
	if !ok {
		return
	}
	fallbackWorkspaceID, ok := boardConfigurationWorkspaceID(w, r, false)
	if !ok {
		return
	}

	config, err := h.loadBootstrapConfiguration(&collectionID, nil)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	accessibleWorkspaceIDs, err := h.permissionService.AccessibleWorkspaceIDs(user.ID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	referencedWorkspaceIDs := []int{}
	if collection.QLQuery != "" {
		referencedWorkspaceIDs, err = h.items.ListDistinctWorkspaceIDsWithQLContext(
			r.Context(), collection.QLQuery, accessibleWorkspaceIDs, user.ID,
		)
		if err != nil {
			// A configuration editor must remain usable for a temporarily invalid
			// saved query. Match the previous frontend behavior by falling back to
			// its route/global status scope instead of failing the whole bootstrap.
			slog.Warn("board configuration bootstrap: collection CQL workspace projection failed",
				"collection_id", collectionID, "error", err)
			referencedWorkspaceIDs = []int{}
		}
	}
	if len(referencedWorkspaceIDs) == 0 {
		candidate := fallbackWorkspaceID
		if candidate == 0 && collection.WorkspaceID != nil {
			candidate = *collection.WorkspaceID
		}
		if candidate != 0 && containsInt(accessibleWorkspaceIDs, candidate) {
			referencedWorkspaceIDs = []int{candidate}
		}
	}

	statuses, err := h.workspaces.GetStatusesForWorkspaces(referencedWorkspaceIDs)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, boardConfigurationBootstrapResponse{
		Collection:             boardConfigurationCollectionFromRecord(collection),
		BoardConfiguration:     config,
		Statuses:               statuses,
		ReferencedWorkspaceIDs: referencedWorkspaceIDs,
	})
}

func boardConfigurationWorkspaceID(w http.ResponseWriter, r *http.Request, required bool) (int, bool) {
	value := r.URL.Query().Get("workspace_id")
	if value == "" {
		if required {
			respondValidationError(w, r, "workspace_id query parameter required for default configuration")
			return 0, false
		}
		return 0, true
	}
	workspaceID, err := strconv.Atoi(value)
	if err != nil || workspaceID <= 0 {
		respondInvalidID(w, r, "workspace_id")
		return 0, false
	}
	return workspaceID, true
}

func (h *BoardConfigurationHandler) loadBootstrapConfiguration(collectionID, workspaceID *int) (*models.BoardConfiguration, error) {
	var config *models.BoardConfiguration
	var err error
	if collectionID != nil {
		config, err = h.repo.GetByCollectionID(*collectionID)
	} else {
		config, err = h.repo.GetByWorkspaceID(*workspaceID)
	}
	if errors.Is(err, repository.ErrNotFound) {
		if workspaceID != nil {
			return &models.BoardConfiguration{WorkspaceID: workspaceID}, nil
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	config.Columns, err = h.repo.GetColumnsWithStatuses(config.ID)
	return config, err
}

func boardConfigurationCollectionFromRecord(collection *repository.CollectionRecord) *boardConfigurationCollection {
	result := &boardConfigurationCollection{
		ID: collection.ID, Name: collection.Name, Description: collection.Description,
		QLQuery: collection.QLQuery, IsPublic: collection.IsPublic,
		WorkspaceID: collection.WorkspaceID, CreatedBy: collection.CreatedBy,
	}
	if collection.Slug != "" {
		result.PublicSlug = &collection.Slug
	}
	return result
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
	if message := validateBoardDisplayOptions(req); message != "" {
		respondValidationError(w, r, message)
		return
	}

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
		if !h.validatePublicCollectionCardFields(w, r, collID, req.CardFields) {
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
		ID:                         configID,
		CollectionID:               collectionID,
		WorkspaceID:                workspaceID,
		ListColumns:                req.ListColumns,
		CardFields:                 req.CardFields,
		ShowRightmostColumnLast50:  req.ShowRightmostColumnLast50,
		CompletedItemRetentionDays: req.CompletedItemRetentionDays,
		CreatedAt:                  time.Now(),
		UpdatedAt:                  time.Now(),
	}
	columns, _ := h.repo.GetColumnsWithStatuses(configID)
	config.Columns = columns

	user := utils.GetCurrentUser(r)
	if user != nil {
		h.auditor.Log(r, user, logger.ActionBoardConfigCreate, logger.ResourceBoardConfiguration, &configID, "")
	}
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
	collectionID, _, ok := h.loadBoardConfigScope(w, r, configID)
	if !ok {
		return
	}

	req, ok := decodeJSON[models.BoardConfigurationRequest](w, r)
	if !ok {
		return
	}
	sanitizeBoardColumnRequests(req.Columns)
	if message := validateBoardDisplayOptions(req); message != "" {
		respondValidationError(w, r, message)
		return
	}
	if collectionID != nil && !h.validatePublicCollectionCardFields(w, r, *collectionID, req.CardFields) {
		return
	}

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

	user := utils.GetCurrentUser(r)
	if user != nil {
		h.auditor.Log(r, user, logger.ActionBoardConfigUpdate, logger.ResourceBoardConfiguration, &configID, "")
	}
	respondJSONOK(w, config)
}

func validateBoardDisplayOptions(req models.BoardConfigurationRequest) string {
	if req.CompletedItemRetentionDays == nil {
		return ""
	}
	if req.ShowRightmostColumnLast50 {
		return "show_rightmost_column_last_50 and completed_item_retention_days cannot both be enabled"
	}
	days := *req.CompletedItemRetentionDays
	if days < 1 || days > maxCompletedItemRetentionDays {
		return "completed_item_retention_days must be between 1 and 3650"
	}
	return ""
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

	user := utils.GetCurrentUser(r)
	if user != nil {
		h.auditor.Log(r, user, logger.ActionBoardConfigDelete, logger.ResourceBoardConfiguration, &configID, "")
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
