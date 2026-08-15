package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"

	"windshift/internal/database"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/sanitize"
	"windshift/internal/services"
)

var slugRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

// sanitizeCollection applies field-specific text policies. FilterState is
// validated rather than scrubbed because it is saved JSON; invalid values write
// a validation error. PublicSlug is constrained separately by slugRegex.
func sanitizeCollection(w http.ResponseWriter, r *http.Request, c *models.Collection) bool {
	sanitize.ApplyAll(
		sanitize.Pair{Target: &c.Name, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &c.Description, Policy: sanitize.RichText},
		sanitize.Pair{Target: &c.QLQuery, Policy: sanitize.QueryText},
	)
	if c.FilterState != nil {
		if err := sanitize.ValidateJSONPayload("filter_state", *c.FilterState); err != nil {
			respondValidationError(w, r, err.Error())
			return false
		}
	}
	return true
}

type CollectionHandler struct {
	db                database.Database
	repo              *repository.CollectionRepository
	permissionService *services.PermissionService
	publicBoardScope  *services.PublicBoardScopeService
}

func NewCollectionHandler(db database.Database, permissionService *services.PermissionService) *CollectionHandler {
	return &CollectionHandler{
		db:                db,
		repo:              repository.NewCollectionRepository(db),
		permissionService: permissionService,
		publicBoardScope:  services.NewPublicBoardScopeService(db, permissionService),
	}
}

func (h *CollectionHandler) authorizePublicBoardScope(w http.ResponseWriter, r *http.Request, userID int, query string) bool {
	_, err := h.publicBoardScope.AuthorizePublishing(userID, query)
	switch {
	case err == nil:
		return true
	case errors.Is(err, services.ErrPublicBoardWorkspaceScopeRequired):
		respondValidationError(w, r, "Public boards require a workspace scope in the collection query")
	case errors.Is(err, services.ErrPublicBoardWorkspaceNotFound):
		respondValidationError(w, r, "Public board query references an unknown workspace")
	case errors.Is(err, services.ErrPublicBoardWorkspaceAdminRequired):
		respondForbidden(w, r)
	default:
		respondInternalError(w, r, err)
	}
	return false
}

// requireCollectionOwner authenticates and verifies creator ownership.
func (h *CollectionHandler) requireCollectionOwner(w http.ResponseWriter, r *http.Request, collectionID int) (*models.User, bool) {
	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return nil, false
	}

	ownerID, err := h.repo.GetOwnerID(collectionID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "collection")
		return nil, false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return nil, false
	}

	if ownerID == nil || *ownerID != currentUser.ID {
		respondForbidden(w, r)
		return nil, false
	}

	return currentUser, true
}

// GetAll returns all collections accessible to the user
func (h *CollectionHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	// Support filtering by workspace_id and category_id
	workspaceIDParam := r.URL.Query().Get("workspace_id")
	categoryIDParam := r.URL.Query().Get("category_id")

	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	filter := repository.CollectionListFilter{UserID: currentUser.ID}

	// Add workspace filter if provided
	if workspaceIDParam != "" {
		workspaceID, err := strconv.Atoi(workspaceIDParam)
		if err != nil {
			respondInvalidID(w, r, "workspace_id")
			return
		}
		filter.WorkspaceID = &workspaceID
	}

	// Add category filter if provided
	if categoryIDParam != "" {
		categoryID, err := strconv.Atoi(categoryIDParam)
		if err != nil {
			respondInvalidID(w, r, "category_id")
			return
		}
		filter.CategoryID = &categoryID
	}

	collections, err := h.repo.ListVisibleModels(filter)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, collections)
}

// Get returns a specific collection by ID
func (h *CollectionHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	collection, err := h.repo.GetVisibleModel(id, currentUser.ID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "collection")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, collection)
}

// Create creates a new collection
func (h *CollectionHandler) Create(w http.ResponseWriter, r *http.Request) {
	collection, ok := decodeJSON[models.Collection](w, r)
	if !ok {
		return
	}
	if !sanitizeCollection(w, r, &collection) {
		return
	}

	// Validate required fields
	if collection.Name == "" {
		respondValidationError(w, r, "Name is required")
		return
	}
	// CQL query is now optional for initial creation - can be empty for partial creation

	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Check public board permission if trying to make collection public or set slug
	if collection.IsPublic || collection.PublicSlug != nil {
		isAdmin, _ := h.permissionService.IsSystemAdmin(currentUser.ID)
		hasPerm, _ := h.permissionService.HasGlobalPermission(currentUser.ID, models.PermissionPublicBoardManage)
		if !isAdmin && !hasPerm {
			respondForbidden(w, r)
			return
		}
	}

	// Validate public_slug if provided
	if collection.PublicSlug != nil && *collection.PublicSlug != "" {
		if !slugRegex.MatchString(*collection.PublicSlug) {
			respondValidationError(w, r, "Public slug must be 3-64 characters, lowercase alphanumeric and hyphens only")
			return
		}
	}

	// Validate workspace_id if provided — check user has view permission
	if collection.WorkspaceID != nil {
		if !RequireWorkspacePermission(w, r, currentUser.ID, *collection.WorkspaceID,
			models.PermissionItemView, h.permissionService) {
			return
		}
	}

	// Validate category_id if provided (only for global collections)
	if collection.CategoryID != nil {
		if collection.WorkspaceID != nil {
			respondValidationError(w, r, "Categories can only be applied to global collections (workspace_id must be null)")
			return
		}
		exists, err := h.repo.CategoryExists(*collection.CategoryID)
		if err != nil {
			respondInternalError(w, r, fmt.Errorf("failed to validate category: %w", err))
			return
		}
		if !exists {
			respondValidationError(w, r, "Category not found")
			return
		}
	}
	if collection.IsPublic && !h.authorizePublicBoardScope(w, r, currentUser.ID, collection.QLQuery) {
		return
	}

	if err := h.repo.Create(&collection, currentUser.ID); err != nil {
		respondInternalError(w, r, err)
		return
	}

	logAudit(h.db, r, currentUser, logger.ActionCollectionCreate, logger.ResourceCollection, &collection.ID, collection.Name)

	respondJSONCreated(w, collection)
}

// Update updates an existing collection
func (h *CollectionHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	bodyBytes, err := restapi.ReadJSONBody(w, r)
	if err != nil {
		if isRequestBodyTooLarge(err) {
			respondRequestTooLarge(w, r)
			return
		}
		respondBadRequest(w, r, "Failed to read request body: "+err.Error())
		return
	}

	var payload map[string]json.RawMessage
	if err = json.Unmarshal(bodyBytes, &payload); err != nil {
		respondBadRequest(w, r, "Invalid JSON: "+err.Error())
		return
	}

	var collection models.Collection
	if err = json.Unmarshal(bodyBytes, &collection); err != nil {
		respondBadRequest(w, r, "Invalid JSON: "+err.Error())
		return
	}
	if !sanitizeCollection(w, r, &collection) {
		return
	}

	_, workspaceProvided := payload["workspace_id"]
	_, descriptionProvided := payload["description"]
	_, categoryProvided := payload["category_id"]
	_, isPublicProvided := payload["is_public"]
	_, publicSlugProvided := payload["public_slug"]
	_, filterStateProvided := payload["filter_state"]
	_, qlQueryProvided := payload["ql_query"]

	// Validate required fields
	if collection.Name == "" {
		respondValidationError(w, r, "Name is required")
		return
	}
	// CQL query validation removed - allow updating collections without CQL query set

	currentUser, ok := h.requireCollectionOwner(w, r, id)
	if !ok {
		return
	}

	// Fetch existing values for field preservation.
	existing, err := h.repo.GetModel(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Preserve is_public unless the field is explicitly sent in the payload
	if !isPublicProvided {
		collection.IsPublic = existing.IsPublic
	}
	if !descriptionProvided {
		collection.Description = existing.Description
	}

	// Public queries and workspace associations change anonymous visibility.
	changingPublic := isPublicProvided && collection.IsPublic != existing.IsPublic
	changingSlug := publicSlugProvided
	changingPublicScope := collection.IsPublic && (qlQueryProvided || workspaceProvided)
	if changingPublic || changingSlug || changingPublicScope {
		isAdmin, _ := h.permissionService.IsSystemAdmin(currentUser.ID)
		hasPerm, _ := h.permissionService.HasGlobalPermission(currentUser.ID, models.PermissionPublicBoardManage)
		if !isAdmin && !hasPerm {
			respondForbidden(w, r)
			return
		}
	}

	// Validate public_slug if provided
	if publicSlugProvided && collection.PublicSlug != nil && *collection.PublicSlug != "" {
		if !slugRegex.MatchString(*collection.PublicSlug) {
			respondValidationError(w, r, "Public slug must be 3-64 characters, lowercase alphanumeric and hyphens only")
			return
		}
	}

	// Preserve workspace association unless the field is explicitly sent in the payload
	if !workspaceProvided {
		collection.WorkspaceID = existing.WorkspaceID
	}

	// Preserve category association unless the field is explicitly sent in the payload
	if !categoryProvided {
		collection.CategoryID = existing.CategoryID
	}

	// Preserve public_slug unless the field is explicitly sent in the payload
	if !publicSlugProvided {
		collection.PublicSlug = existing.PublicSlug
	}
	if collection.IsPublic && (collection.PublicSlug == nil || *collection.PublicSlug == "") {
		respondValidationError(w, r, "Public slug is required when public sharing is enabled")
		return
	}

	// Preserve filter_state unless the field is explicitly sent in the payload.
	// An explicit null in the payload (raw mode) clears it.
	if !filterStateProvided {
		collection.FilterState = existing.FilterState
	}
	if !qlQueryProvided {
		collection.QLQuery = existing.QLQuery
	}

	// Validate workspace_id if provided — check user has view permission
	if workspaceProvided && collection.WorkspaceID != nil {
		if !RequireWorkspacePermission(w, r, currentUser.ID, *collection.WorkspaceID,
			models.PermissionItemView, h.permissionService) {
			return
		}
	}

	// Validate category_id if provided (only for global collections)
	if categoryProvided && collection.CategoryID != nil {
		if collection.WorkspaceID != nil {
			respondValidationError(w, r, "Categories can only be applied to global collections (workspace_id must be null)")
			return
		}
		exists, err := h.repo.CategoryExists(*collection.CategoryID)
		if err != nil {
			respondInternalError(w, r, fmt.Errorf("failed to validate category: %w", err))
			return
		}
		if !exists {
			respondValidationError(w, r, "Category not found")
			return
		}
	}
	requiresScopeAuthorization := collection.IsPublic && (!existing.IsPublic || qlQueryProvided || workspaceProvided)
	if requiresScopeAuthorization && !h.authorizePublicBoardScope(w, r, currentUser.ID, collection.QLQuery) {
		return
	}

	if err := h.repo.Update(id, &collection); err != nil {
		if database.IsUniqueConstraintError(err) {
			respondConflict(w, r, "This public slug is already in use")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	logAudit(h.db, r, currentUser, logger.ActionCollectionUpdate, logger.ResourceCollection, &id, collection.Name)

	// Return success
	respondJSONOK(w, map[string]string{"message": "Collection updated successfully"})
}

// UpdatePublicSharing updates only the public sharing fields of a collection
func (h *CollectionHandler) UpdatePublicSharing(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	var payload struct {
		IsPublic   bool    `json:"is_public"`
		PublicSlug *string `json:"public_slug"`
	}
	if err := newJSONDecoder(w, r).Decode(&payload); err != nil {
		respondBadRequest(w, r, "Invalid JSON: "+err.Error())
		return
	}
	// PublicSlug is only regex-validated when enabling sharing; when
	// disabling, the slug still lands in the DB, so bound it here.
	sanitize.Apply(payload.PublicSlug, sanitize.ShortIdentifier)

	currentUser, ok := h.requireCollectionOwner(w, r, id)
	if !ok {
		return
	}

	// Check public board permission
	isAdmin, _ := h.permissionService.IsSystemAdmin(currentUser.ID)
	hasPerm, _ := h.permissionService.HasGlobalPermission(currentUser.ID, models.PermissionPublicBoardManage)
	if !isAdmin && !hasPerm {
		respondForbidden(w, r)
		return
	}

	// Validate slug when enabling public sharing
	if payload.IsPublic {
		if payload.PublicSlug == nil || *payload.PublicSlug == "" {
			respondValidationError(w, r, "Public slug is required when enabling public sharing")
			return
		}
		if !slugRegex.MatchString(*payload.PublicSlug) {
			respondValidationError(w, r, "Public slug must be 3-64 characters, lowercase alphanumeric and hyphens only")
			return
		}
		collection, err := h.repo.GetModel(id)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if !h.authorizePublicBoardScope(w, r, currentUser.ID, collection.QLQuery) {
			return
		}
	}

	if err := h.repo.UpdatePublicSharing(id, payload.IsPublic, payload.PublicSlug); err != nil {
		if database.IsUniqueConstraintError(err) {
			respondConflict(w, r, "This public slug is already in use")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	logAudit(h.db, r, currentUser, logger.ActionCollectionUpdate, logger.ResourceCollection, &id, "")

	respondJSONOK(w, map[string]any{
		"is_public":   payload.IsPublic,
		"public_slug": payload.PublicSlug,
	})
}

// Delete deletes a collection
func (h *CollectionHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	currentUser, ok := h.requireCollectionOwner(w, r, id)
	if !ok {
		return
	}

	if err := h.repo.Delete(id); err != nil {
		respondInternalError(w, r, err)
		return
	}

	logAudit(h.db, r, currentUser, logger.ActionCollectionDelete, logger.ResourceCollection, &id, "")

	respondJSONOK(w, map[string]string{"message": "Collection deleted successfully"})
}
