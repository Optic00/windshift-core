package services

import (
	"windshift/internal/models"
	"windshift/internal/repository"
)

// AssetPermissionKey* are the permission-key strings the asset role system
// checks against. Canonical home — both internal/handlers (cookie auth) and
// internal/restapi/v1 (bearer auth) reference these.
const (
	AssetPermissionKeyView   = "asset.view"
	AssetPermissionKeyCreate = "asset.create"
	AssetPermissionKeyEdit   = "asset.edit"
	AssetPermissionKeyDelete = "asset.delete"
	AssetPermissionKeyAdmin  = "asset.admin"
)

// AssetPermissionService owns the per-set asset role check (Viewer / Editor /
// Administrator with asset.view / create / edit / delete / admin keys) plus
// the system-admin bypass. Both the cookie-auth handler and the bearer-auth
// v1 handler depend on this service so the policy lives in one place.
type AssetPermissionService struct {
	repo  *repository.AssetRepository
	perms *PermissionService
}

// NewAssetPermissionService constructs an AssetPermissionService backed by
// the given asset repository and global PermissionService (used for the
// system-admin bypass via HasGlobalPermission).
func NewAssetPermissionService(repo *repository.AssetRepository, perms *PermissionService) *AssetPermissionService {
	return &AssetPermissionService{repo: repo, perms: perms}
}

// virtualAdminRoleName is the role-name string the system-admin bypass
// returns. Kept aligned with handlers.AssetRoleAdministrator so callers that
// switch on role-name (e.g. API response shaping) treat admins consistently.
const virtualAdminRoleName = "Administrator"

// GetUserSetRole returns the role a user has on an asset set, honoring:
//   - System Admin → virtual Administrator role (bypass).
//   - Direct user role.
//   - Group role.
//   - Set's everyone-role default.
//
// Returns (nil, nil) when the user has no role on the set.
func (s *AssetPermissionService) GetUserSetRole(userID, setID int) (*models.AssetRole, error) {
	isAdmin, err := s.perms.HasGlobalPermission(userID, "system.admin")
	if err != nil {
		return nil, err
	}
	if isAdmin {
		return &models.AssetRole{ID: -1, Name: virtualAdminRoleName}, nil
	}
	return s.repo.GetUserSetRole(userID, setID)
}

// HasAssetSetPermission reports whether userID is allowed permissionKey
// (asset.view / create / edit / delete / admin) on setID. Returns false when
// the user has no role on the set or the role doesn't grant the key.
//
// Signature matches the AssetSetPermissionChecker interface used by
// action_service.go and item_link_orchestration.go so this service is a
// drop-in replacement for the handler-shaped checker.
func (s *AssetPermissionService) HasAssetSetPermission(userID, setID int, permissionKey string) (bool, error) {
	role, err := s.GetUserSetRole(userID, setID)
	if err != nil {
		return false, err
	}
	if role == nil {
		return false, nil
	}
	return s.repo.RoleHasPermission(role.ID, permissionKey)
}

// HasAssetPermission resolves an asset to its owning set and checks the
// permission. Returns (allowed, setID, found, err) — found=false when the
// asset doesn't exist, so callers can distinguish "no such asset" from
// "asset exists but you can't act on it" if they need to.
func (s *AssetPermissionService) HasAssetPermission(userID, assetID int, permissionKey string) (allowed bool, setID int, found bool, err error) {
	asset, err := s.repo.GetAssetByID(assetID)
	if err != nil {
		return false, 0, false, err
	}
	if asset == nil {
		return false, 0, false, nil
	}
	ok, err := s.HasAssetSetPermission(userID, asset.SetID, permissionKey)
	if err != nil {
		return false, asset.SetID, true, err
	}
	return ok, asset.SetID, true, nil
}
