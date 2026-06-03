package handlers

import (
	"net/http"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
	"windshift/internal/utils"
)

// AssetHandler handles asset management operations on the cookie-auth
// surface. The per-set role check now lives on services.AssetPermissionService
// so both this handler and the bearer-auth v1 handler share one source of
// truth; this struct keeps thin delegates so existing call sites and the
// services.AssetSetPermissionChecker interface stay valid.
type AssetHandler struct {
	db                 database.Database
	repo               *repository.AssetRepository
	permissionService  *services.PermissionService
	assetPerm          *services.AssetPermissionService
	attachmentPath     string
	assetActionService *services.AssetActionService
}

// NewAssetHandler creates a new asset handler
func NewAssetHandler(db database.Database, permissionService *services.PermissionService, attachmentPath string) *AssetHandler {
	repo := repository.NewAssetRepository(db)
	return &AssetHandler{
		db:                db,
		repo:              repo,
		permissionService: permissionService,
		assetPerm:         services.NewAssetPermissionService(repo, permissionService),
		attachmentPath:    attachmentPath,
	}
}

// AssetPermissionService returns the per-set permission service this handler
// delegates to, so callers wiring up the v1 surface can share the same
// instance instead of constructing a parallel one.
func (h *AssetHandler) AssetPermissionService() *services.AssetPermissionService {
	return h.assetPerm
}

// SetAssetActionService sets the asset action service for emitting automation events
func (h *AssetHandler) SetAssetActionService(s *services.AssetActionService) {
	h.assetActionService = s
}

// Role name constants — these are response-shape strings, not used by the
// permission service. Kept here next to the only callers that need them.
const (
	AssetRoleViewer        = "Viewer"
	AssetRoleEditor        = "Editor"
	AssetRoleAdministrator = "Administrator"
)

// createDefaultStatuses creates default statuses for a new asset set.
func (h *AssetHandler) createDefaultStatuses(setID int) error {
	return h.repo.CreateDefaultStatuses(setID)
}

// getUserSetRole delegates to AssetPermissionService.
func (h *AssetHandler) getUserSetRole(userID, setID int) (*models.AssetRole, error) {
	return h.assetPerm.GetUserSetRole(userID, setID)
}

// hasAssetPermission delegates to AssetPermissionService.
func (h *AssetHandler) hasAssetPermission(userID, setID int, permissionKey string) (bool, error) {
	return h.assetPerm.HasAssetSetPermission(userID, setID, permissionKey)
}

// HasAssetSetPermission satisfies services.AssetSetPermissionChecker so the
// action service and item-link orchestration can keep accepting this handler
// as a permission source — they now transparently hit the shared service.
func (h *AssetHandler) HasAssetSetPermission(userID, setID int, permissionKey string) (bool, error) {
	return h.assetPerm.HasAssetSetPermission(userID, setID, permissionKey)
}

// getUserSetRoleName returns the role name (for API responses)
func (h *AssetHandler) getUserSetRoleName(userID, setID int) (string, error) {
	role, err := h.getUserSetRole(userID, setID)
	if err != nil {
		return "", err
	}
	if role == nil {
		return "", nil
	}
	return role.Name, nil
}

// requireSetViewAccess checks auth, parses setId, and verifies view permission.
func (h *AssetHandler) requireSetViewAccess(w http.ResponseWriter, r *http.Request) (*models.User, int, bool) {
	return h.requireSetAccess(w, r, services.AssetPermissionKeyView)
}

// requireSetEditAccess checks auth, parses setId, and verifies edit permission.
func (h *AssetHandler) requireSetEditAccess(w http.ResponseWriter, r *http.Request) (*models.User, int, bool) {
	return h.requireSetAccess(w, r, services.AssetPermissionKeyEdit)
}

// requireSetAdminAccess checks auth, parses setId, and verifies admin permission.
func (h *AssetHandler) requireSetAdminAccess(w http.ResponseWriter, r *http.Request) (*models.User, int, bool) {
	return h.requireSetAccess(w, r, services.AssetPermissionKeyAdmin)
}

// requireSetAccess checks auth, parses setId, and verifies the given permission.
func (h *AssetHandler) requireSetAccess(w http.ResponseWriter, r *http.Request, permissionKey string) (*models.User, int, bool) {
	return h.requireSetAccessByParam(w, r, "setId", permissionKey)
}

// requireSetAdminByID checks auth, parses the "id" path param, and verifies admin permission.
// Use this for routes where the set ID param is named "id" (e.g. /asset-sets/{id}/roles).
func (h *AssetHandler) requireSetAdminByID(w http.ResponseWriter, r *http.Request) (*models.User, int, bool) {
	return h.requireSetAccessByParam(w, r, "id", services.AssetPermissionKeyAdmin)
}

// requireSetAccessByParam checks auth, parses the given path param as a set ID, and verifies the given permission.
func (h *AssetHandler) requireSetAccessByParam(w http.ResponseWriter, r *http.Request, paramName, permissionKey string) (*models.User, int, bool) {
	currentUser := utils.GetCurrentUser(r)
	if currentUser == nil {
		respondUnauthorized(w, r)
		return nil, 0, false
	}
	setID, ok := requireIDParam(w, r, paramName)
	if !ok {
		return nil, 0, false
	}
	hasPerm, err := h.hasAssetPermission(currentUser.ID, setID, permissionKey)
	if err != nil {
		respondInternalError(w, r, err)
		return nil, 0, false
	}
	if !hasPerm {
		respondNotFound(w, r, "asset set")
		return nil, 0, false
	}
	return currentUser, setID, true
}

// canViewSet checks if user can view a set
func (h *AssetHandler) canViewSet(userID, setID int) (bool, error) {
	return h.hasAssetPermission(userID, setID, services.AssetPermissionKeyView)
}

// canEditSet checks if user can edit assets in a set
func (h *AssetHandler) canEditSet(userID, setID int) (bool, error) {
	return h.hasAssetPermission(userID, setID, services.AssetPermissionKeyEdit)
}

// canAdminSet checks if user can administer a set
func (h *AssetHandler) canAdminSet(userID, setID int) (bool, error) {
	return h.hasAssetPermission(userID, setID, services.AssetPermissionKeyAdmin)
}
