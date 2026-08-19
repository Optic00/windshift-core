package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"windshift/internal/cacheutil"
	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"

	"github.com/allegro/bigcache/v3"
)

// PermissionService handles cached permission resolution
type PermissionService struct {
	cache           *bigcache.BigCache
	db              database.Database
	mu              sync.RWMutex
	cacheCommitMu   sync.RWMutex
	cacheGeneration atomic.Uint64
	workspaceAccess *workspaceAccessCache

	hits                      int64
	misses                    int64
	errors                    int64
	permissionCheckCount      int64
	permissionCheckNanos      int64
	permissionSnapshotDecodes atomic.Uint64

	ttl       time.Duration
	batchSize int

	allPermissionKeys []string
}

// PermissionCacheConfig represents configuration for the permission cache
type PermissionCacheConfig struct {
	TTL             time.Duration `json:"ttl"`               // Default: 15min
	MaxCacheSize    int           `json:"max_cache_size"`    // Default: 123MB
	WarmupOnStartup bool          `json:"warmup_on_startup"` // Default: true
	PreWarmActive   bool          `json:"pre_warm_active"`   // Default: true
	BatchSize       int           `json:"batch_size"`        // Default: 100
}

// DefaultPermissionCacheConfig returns default configuration
// deadcode-keep: called by core-tests test fixtures (invitations_test.go, items_test.go, iterations_test.go and others)
func DefaultPermissionCacheConfig() PermissionCacheConfig {
	return PermissionCacheConfig{
		TTL:             15 * time.Minute,
		MaxCacheSize:    123,
		WarmupOnStartup: true,
		PreWarmActive:   true,
		BatchSize:       100,
	}
}

// NewPermissionService creates a new permission service with caching
func NewPermissionService(db database.Database, config PermissionCacheConfig) (*PermissionService, error) {
	cache, err := cacheutil.New("permissions", cacheutil.BigCacheOptions{
		TTL:               config.TTL,
		MaxCacheMB:        config.MaxCacheSize,
		MaxEntrySize:      8192, // 8KB per entry (larger for permission data)
		Shards:            64,
		InitialCapacityMB: 4,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create BigCache for permissions: %w", err)
	}

	service := &PermissionService{
		cache:           cache,
		db:              db,
		workspaceAccess: newWorkspaceAccessCache(),
		ttl:             config.TTL,
		batchSize:       config.BatchSize,
	}

	if err := service.loadAllPermissionKeys(); err != nil {
		slog.Warn("Failed to pre-load permission keys; will lazy-load on first cache build",
			slog.String("component", "permissions"),
			slog.Any("error", err))
	}

	if config.WarmupOnStartup {
		go service.WarmCache()
	}

	return service, nil
}

// getCacheKey generates a cache key for a user's permissions
func (ps *PermissionService) getCacheKey(userID int) string {
	return fmt.Sprintf("permissions:user:%d", userID)
}

// HasWorkspacePermission checks if user has a specific workspace permission
// Returns true if:
// 1. User is system admin, OR
// 2. User has the specified permission on the workspace, OR
// 3. Workspace has NO permission restrictions (accessible to all logged-in users)
func (ps *PermissionService) HasWorkspacePermission(userID, workspaceID int, permission string) (bool, error) {
	startTime := time.Now()
	defer func() {
		atomic.AddInt64(&ps.permissionCheckCount, 1)
		atomic.AddInt64(&ps.permissionCheckNanos, time.Since(startTime).Nanoseconds())
	}()

	cached, err := ps.effectivePermissionSnapshot(userID)
	if err != nil {
		return false, err
	}
	return workspacePermissionFromSnapshot(cached, workspaceID, permission), nil
}

func workspacePermissionFromSnapshot(cached *models.UserPermissionCache, workspaceID int, permission string) bool {
	if cached == nil {
		return false
	}
	if cached.IsSystemAdmin {
		return true
	}
	if everyonePerms := cached.WorkspaceEveryone[workspaceID]; everyonePerms[permission] {
		return true
	}
	return cached.WorkspacePermissions[workspaceID][permission]
}

func (ps *PermissionService) effectivePermissionSnapshot(userID int) (*models.UserPermissionCache, error) {
	cached, err := ps.getUserPermissionCache(userID)
	if err == nil {
		atomic.AddInt64(&ps.hits, 1)
		return cached, nil
	}

	atomic.AddInt64(&ps.misses, 1)
	cached, err = ps.buildAndStoreUserPermissionCache(userID)
	if err != nil {
		atomic.AddInt64(&ps.errors, 1)
		return nil, err
	}
	return cached, nil
}

// buildAndStoreUserPermissionCache prevents a snapshot that started before an
// invalidation from being written back after the invalidation completed.
// Permission builds intentionally happen outside the commit lock so unrelated
// reads remain concurrent; the generation check makes the final write linear.
func (ps *PermissionService) buildAndStoreUserPermissionCache(userID int) (*models.UserPermissionCache, error) {
	for {
		generation := ps.cacheGeneration.Load()
		cached, err := ps.buildUserPermissionCache(userID)
		if err != nil {
			return nil, err
		}
		stored, err := ps.storeUserPermissionCacheIfCurrent(userID, cached, generation)
		if err != nil {
			slog.Warn("failed to store effective permission snapshot",
				slog.String("component", "permissions"),
				slog.Int("user_id", userID),
				slog.Any("error", err))
			return cached, nil
		}
		if stored {
			return cached, nil
		}
	}
}

func (ps *PermissionService) storeUserPermissionCacheIfCurrent(
	userID int,
	cached *models.UserPermissionCache,
	generation uint64,
) (bool, error) {
	ps.cacheCommitMu.RLock()
	defer ps.cacheCommitMu.RUnlock()
	if generation != ps.cacheGeneration.Load() {
		return false, nil
	}
	return true, ps.storeUserPermissionCache(userID, cached)
}

// HasGlobalPermission checks if user has a specific global permission
func (ps *PermissionService) HasGlobalPermission(userID int, permission string) (bool, error) {
	// Cache misses build a complete snapshot so later checks stay in memory.
	cached, err := ps.getUserPermissionCache(userID)
	if err == nil {
		atomic.AddInt64(&ps.hits, 1)

		if cached.IsSystemAdmin {
			return true, nil
		}

		return cached.GlobalPermissions[permission], nil
	}

	atomic.AddInt64(&ps.misses, 1)
	return ps.loadUserPermissionAndCheckGlobal(userID, permission)
}

// HasGlobalPermissionContext is the request-aware form used by hot read paths.
// Cache hits remain allocation-free; a miss uses one cancellable SQL probe
// instead of building the full permission snapshot after the request is gone.
func (ps *PermissionService) HasGlobalPermissionContext(ctx context.Context, userID int, permission string) (bool, error) {
	if cached, err := ps.getUserPermissionCache(userID); err == nil {
		atomic.AddInt64(&ps.hits, 1)
		return cached.IsSystemAdmin || cached.GlobalPermissions[permission], nil
	}
	atomic.AddInt64(&ps.misses, 1)
	var allowed bool
	err := ps.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM user_global_permissions ugp
			JOIN permissions p ON p.id = ugp.permission_id
			WHERE ugp.user_id = ? AND p.permission_key IN (?, 'system.admin')
			UNION
			SELECT 1 FROM group_members gm
			JOIN groups g ON g.id = gm.group_id AND g.is_active = true
			JOIN group_global_permissions ggp ON ggp.group_id = gm.group_id
			JOIN permissions p ON p.id = ggp.permission_id
			WHERE gm.user_id = ? AND p.permission_key IN (?, 'system.admin')
		)
	`, userID, permission, userID, permission).Scan(&allowed)
	if err != nil {
		atomic.AddInt64(&ps.errors, 1)
		return false, fmt.Errorf("error checking global permission: %w", err)
	}
	return allowed, nil
}

// HasWorkspacePermissions checks multiple permissions in single operation
func (ps *PermissionService) HasWorkspacePermissions(userID, workspaceID int, permissions []string) (map[string]bool, error) {
	result := make(map[string]bool)

	// Merge implicit Everyone permissions with explicit workspace assignments.
	cached, err := ps.getUserPermissionCache(userID)
	if err == nil {
		atomic.AddInt64(&ps.hits, 1)

		if cached.IsSystemAdmin {
			for _, perm := range permissions {
				result[perm] = true
			}
			return result, nil
		}

		if everyonePerms, exists := cached.WorkspaceEveryone[workspaceID]; exists {
			for _, perm := range permissions {
				if everyonePerms[perm] {
					result[perm] = true
				}
			}
		}

		if workspacePerms, exists := cached.WorkspacePermissions[workspaceID]; exists {
			for _, perm := range permissions {
				if workspacePerms[perm] {
					result[perm] = true
				}
			}
		}
		return result, nil
	}

	atomic.AddInt64(&ps.misses, 1)
	return ps.loadUserPermissionAndCheckMultiple(userID, workspaceID, permissions)
}

// IsSystemAdmin checks if user is system administrator
func (ps *PermissionService) IsSystemAdmin(userID int) (bool, error) {
	cached, err := ps.getUserPermissionCache(userID)
	if err == nil {
		atomic.AddInt64(&ps.hits, 1)
		return cached.IsSystemAdmin, nil
	}

	// Keep this probe aligned with auth_policy.go when the snapshot is absent.
	atomic.AddInt64(&ps.misses, 1)
	var hasPermission bool
	err = ps.db.QueryRow(repository.SystemAdminGrantQuery, userID, userID).Scan(&hasPermission)
	if err != nil {
		atomic.AddInt64(&ps.errors, 1)
		return false, fmt.Errorf("error checking system admin permission: %w", err)
	}

	return hasPermission, nil
}

// IsSystemAdminContext is the request-aware form of IsSystemAdmin.
func (ps *PermissionService) IsSystemAdminContext(ctx context.Context, userID int) (bool, error) {
	if cached, err := ps.getUserPermissionCache(userID); err == nil {
		atomic.AddInt64(&ps.hits, 1)
		return cached.IsSystemAdmin, nil
	}
	atomic.AddInt64(&ps.misses, 1)
	var hasPermission bool
	err := ps.db.QueryRowContext(ctx, repository.SystemAdminGrantQuery, userID, userID).Scan(&hasPermission)
	if err != nil {
		atomic.AddInt64(&ps.errors, 1)
		return false, fmt.Errorf("error checking system admin permission: %w", err)
	}
	return hasPermission, nil
}

// GetItemWorkspaceID returns the workspace ID for a given item ID using lazy-loaded cache
// This method is thread-safe and will populate the cache on first access
func (ps *PermissionService) GetItemWorkspaceID(userID, itemID int) (int, error) {
	cached, err := ps.getUserPermissionCache(userID)
	if err == nil {
		if workspaceID, exists := cached.ItemWorkspaceMap[itemID]; exists {
			atomic.AddInt64(&ps.hits, 1)
			return workspaceID, nil
		}
	}

	atomic.AddInt64(&ps.misses, 1)

	workspaceID, err := repository.NewItemRepository(ps.db).GetWorkspaceID(itemID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return 0, fmt.Errorf("item not found: %d", itemID)
		}
		atomic.AddInt64(&ps.errors, 1)
		return 0, fmt.Errorf("error querying item workspace: %w", err)
	}

	if cached != nil {
		ps.mu.Lock()
		cached.ItemWorkspaceMap[itemID] = workspaceID
		ps.mu.Unlock()

		if err := ps.storeUserPermissionCache(userID, cached); err != nil {
			slog.Warn("Failed to update cache with item workspace mapping",
				slog.String("component", "permissions"),
				slog.Int("user_id", userID),
				slog.Int("item_id", itemID),
				slog.Any("error", err))
		}
	}

	return workspaceID, nil
}

// getUserPermissionCache retrieves cached permission data for a user
func (ps *PermissionService) getUserPermissionCache(userID int) (*models.UserPermissionCache, error) {
	cacheKey := ps.getCacheKey(userID)

	entry, err := ps.cache.Get(cacheKey)
	if err != nil {
		return nil, err
	}

	var cached models.UserPermissionCache
	if err := json.Unmarshal(entry, &cached); err != nil {
		// Remove corrupted cache entry
		_ = ps.cache.Delete(cacheKey)
		return nil, err
	}
	ps.permissionSnapshotDecodes.Add(1)

	if time.Now().After(cached.ExpiresAt) {
		_ = ps.cache.Delete(cacheKey)
		return nil, fmt.Errorf("cache entry expired")
	}

	return &cached, nil
}

// loadUserPermissionAndCheckGlobal loads user permissions from DB and checks global permission
func (ps *PermissionService) loadUserPermissionAndCheckGlobal(userID int, permission string) (bool, error) {
	cached, err := ps.buildAndStoreUserPermissionCache(userID)
	if err != nil {
		atomic.AddInt64(&ps.errors, 1)
		return false, err
	}

	if cached.IsSystemAdmin {
		return true, nil
	}

	return cached.GlobalPermissions[permission], nil
}

// loadUserPermissionAndCheckMultiple loads user permissions and checks multiple permissions
func (ps *PermissionService) loadUserPermissionAndCheckMultiple(userID, workspaceID int, permissions []string) (map[string]bool, error) {
	result := make(map[string]bool)

	cached, err := ps.buildAndStoreUserPermissionCache(userID)
	if err != nil {
		atomic.AddInt64(&ps.errors, 1)
		return result, err
	}

	if cached.IsSystemAdmin {
		for _, perm := range permissions {
			result[perm] = true
		}
		return result, nil
	}

	if everyonePerms, exists := cached.WorkspaceEveryone[workspaceID]; exists {
		for _, perm := range permissions {
			if everyonePerms[perm] {
				result[perm] = true
			}
		}
	}

	if workspacePerms, exists := cached.WorkspacePermissions[workspaceID]; exists {
		for _, perm := range permissions {
			result[perm] = workspacePerms[perm]
		}
	}

	return result, nil
}

// GetGroupMemberships returns the group IDs for a user, leveraging the permission cache.
// Falls back to a direct DB query on cache miss.
func (ps *PermissionService) GetGroupMemberships(userID int) ([]int, error) {
	cached, err := ps.getUserPermissionCache(userID)
	if err == nil {
		atomic.AddInt64(&ps.hits, 1)
		return cached.GroupMemberships, nil
	}

	atomic.AddInt64(&ps.misses, 1)
	cached, err = ps.buildAndStoreUserPermissionCache(userID)
	if err != nil {
		atomic.AddInt64(&ps.errors, 1)
		return nil, err
	}
	return cached.GroupMemberships, nil
}

// HasWorkspaceRole checks whether a user has a specific role in a workspace.
func (ps *PermissionService) HasWorkspaceRole(userID, workspaceID, roleID int) (bool, error) {
	cache, err := ps.GetUserEffectivePermissions(userID)
	if err != nil {
		return false, err
	}
	for _, rid := range cache.RoleAssignments[workspaceID] {
		if rid == roleID {
			return true, nil
		}
	}
	return false, nil
}

// GetUserEffectivePermissions returns the full effective permission cache for a user,
// including explicit roles, group-based roles, and "Everyone" implicit permissions.
func (ps *PermissionService) GetUserEffectivePermissions(userID int) (*models.UserPermissionCache, error) {
	return ps.effectivePermissionSnapshot(userID)
}

// InvalidateUserCache removes a user's permission cache. If the user owns
// any agents, their caches are invalidated as well so the delegation stays
// consistent after a permission mutation on the owner.
func (ps *PermissionService) InvalidateUserCache(userID int) error {
	ps.cacheCommitMu.Lock()
	defer ps.cacheCommitMu.Unlock()
	ps.cacheGeneration.Add(1)
	cacheKey := ps.getCacheKey(userID)
	err := ps.cache.Delete(cacheKey)
	ps.invalidateOwnedAgents(userID)
	return err
}

// invalidateOwnedAgents clears the permission cache for every agent owned by
// the given user. Best-effort: failures are logged but don't surface.
func (ps *PermissionService) invalidateOwnedAgents(ownerID int) {
	rows, err := ps.db.Query(
		"SELECT id FROM users WHERE agent_owner_user_id = ?",
		ownerID,
	)
	if err != nil {
		slog.Warn("failed to enumerate owned agents for cache invalidation",
			slog.String("component", "permissions"),
			slog.Int("owner_id", ownerID),
			slog.Any("error", err))
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var agentID int
		if err := rows.Scan(&agentID); err != nil {
			continue
		}
		_ = ps.cache.Delete(ps.getCacheKey(agentID))
	}
	if err := rows.Err(); err != nil {
		slog.Warn("failed to iterate owned agents for cache invalidation",
			slog.String("component", "permissions"),
			slog.Int("owner_id", ownerID),
			slog.Any("error", err))
	}
}

// InvalidateMultipleUserCaches removes permission caches for multiple users
func (ps *PermissionService) InvalidateMultipleUserCaches(userIDs []int) error {
	for _, userID := range userIDs {
		if err := ps.InvalidateUserCache(userID); err != nil {
			slog.Warn("Failed to invalidate cache for user",
				slog.String("component", "permissions"),
				slog.Int("user_id", userID),
				slog.Any("error", err))
		}
	}
	return nil
}

// InvalidateGroupMemberCaches invalidates caches for all members of a group
func (ps *PermissionService) InvalidateGroupMemberCaches(groupID int) error {
	// Invalidation reaches inactive groups too; reactivation must not reuse stale snapshots.
	userIDs, err := ps.getGroupMembers(groupID)
	if err != nil {
		return fmt.Errorf("error getting group members for cache invalidation: %w", err)
	}

	return ps.InvalidateMultipleUserCaches(userIDs)
}

// InvalidateWorkspaceMemberCaches invalidates caches for all members of a workspace
func (ps *PermissionService) InvalidateWorkspaceMemberCaches(workspaceID int) error {
	rows, err := ps.db.Query(`
		SELECT DISTINCT user_id FROM user_workspace_roles WHERE workspace_id = ?
		UNION
		SELECT DISTINCT gm.user_id FROM group_members gm
		JOIN group_workspace_roles gwr ON gm.group_id = gwr.group_id
		WHERE gwr.workspace_id = ?
	`, workspaceID, workspaceID)
	if err != nil {
		return fmt.Errorf("error getting workspace members for cache invalidation: %w", err)
	}
	defer rows.Close()

	userIDs, err := scanIntColumn(rows)
	if err != nil {
		return fmt.Errorf("error iterating workspace members for cache invalidation: %w", err)
	}

	return ps.InvalidateMultipleUserCaches(userIDs)
}

// getGroupMembers returns all user IDs in a group. Used by cache invalidation
// helpers when group permissions or membership change. Not filtered by
// groups.is_active: invalidation must reach members of inactive groups too,
// otherwise reactivating a group leaves stale "no perm" caches in place.
func (ps *PermissionService) getGroupMembers(groupID int) ([]int, error) {
	rows, err := ps.db.Query(`
		SELECT user_id FROM group_members WHERE group_id = ?
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanIntColumn(rows)
}

// OnUserPermissionChanged should be called when user permissions are modified
func (ps *PermissionService) OnUserPermissionChanged(userID int) error {
	if err := ps.InvalidateUserCache(userID); err != nil {
		slog.Warn("Failed to invalidate cache for user after permission change",
			slog.String("component", "permissions"),
			slog.Int("user_id", userID),
			slog.Any("error", err))
	}
	return nil
}

// OnGroupPermissionChanged should be called when group permissions are modified
func (ps *PermissionService) OnGroupPermissionChanged(groupID int) error {
	if err := ps.InvalidateGroupMemberCaches(groupID); err != nil {
		slog.Warn("Failed to invalidate group member caches",
			slog.String("component", "permissions"),
			slog.Int("group_id", groupID),
			slog.Any("error", err))
	}
	return nil
}

// OnUserGroupMembershipChanged should be called when user is added/removed from group
func (ps *PermissionService) OnUserGroupMembershipChanged(userID, groupID int) error {
	return ps.OnUserPermissionChanged(userID)
}

// OnWorkspacePermissionChanged should be called when workspace-level permissions change
func (ps *PermissionService) OnWorkspacePermissionChanged(workspaceID int) error {
	if err := ps.InvalidateWorkspaceMemberCaches(workspaceID); err != nil {
		slog.Warn("Failed to invalidate workspace member caches",
			slog.String("component", "permissions"),
			slog.Int("workspace_id", workspaceID),
			slog.Any("error", err))
	}

	return nil
}

// OnRoleChanged should be called when a role's permissions are modified
func (ps *PermissionService) OnRoleChanged(roleID int) error {
	userIDs, err := ps.getUsersWithRole(roleID)
	if err != nil {
		slog.Error("Failed to get users with role",
			slog.String("component", "permissions"),
			slog.Int("role_id", roleID),
			slog.Any("error", err))
		return err
	}

	if err = ps.InvalidateMultipleUserCaches(userIDs); err != nil {
		slog.Warn("Failed to invalidate user caches for role",
			slog.String("component", "permissions"),
			slog.Int("role_id", roleID),
			slog.Any("error", err))
	}

	groupUserIDs, err := ps.getUsersInGroupsWithRole(roleID)
	if err != nil {
		slog.Warn("Failed to get users in groups with role",
			slog.String("component", "permissions"),
			slog.Int("role_id", roleID),
			slog.Any("error", err))
	} else if len(groupUserIDs) > 0 {
		if err := ps.InvalidateMultipleUserCaches(groupUserIDs); err != nil {
			slog.Warn("Failed to invalidate group user caches for role",
				slog.String("component", "permissions"),
				slog.Int("role_id", roleID),
				slog.Any("error", err))
		}
	}

	return nil
}

// OnPermissionSetChanged should be called when a permission set's permissions are modified
func (ps *PermissionService) OnPermissionSetChanged(permissionSetID int) error {
	configSetIDs, err := ps.getConfigurationSetsUsingPermissionSet(permissionSetID)
	if err != nil {
		slog.Error("Failed to get configuration sets using permission set",
			slog.String("component", "permissions"),
			slog.Int("permission_set_id", permissionSetID),
			slog.Any("error", err))
		return err
	}

	for _, configSetID := range configSetIDs {
		workspaceIDs, err := ps.getWorkspacesUsingConfigurationSet(configSetID)
		if err != nil {
			slog.Warn("Failed to get workspaces for configuration set",
				slog.String("component", "permissions"),
				slog.Int("configuration_set_id", configSetID),
				slog.Any("error", err))
			continue
		}

		for _, workspaceID := range workspaceIDs {
			if err := ps.InvalidateWorkspaceMemberCaches(workspaceID); err != nil {
				slog.Warn("Failed to invalidate workspace member caches",
					slog.String("component", "permissions"),
					slog.Int("workspace_id", workspaceID),
					slog.Any("error", err))
			}
		}
	}

	return nil
}

// OnEveryoneAccessChanged resets the entire permission cache when the implicit
// "everyone" access level changes (i.e., a role's first assignment is added or
// its last assignment is removed for a workspace).
func (ps *PermissionService) OnEveryoneAccessChanged() {
	if ps.cache != nil {
		ps.cacheCommitMu.Lock()
		defer ps.cacheCommitMu.Unlock()
		ps.cacheGeneration.Add(1)
		if err := ps.cache.Reset(); err != nil {
			slog.Error("Failed to reset permission cache after everyone-access change",
				slog.String("component", "permissions"),
				slog.Any("error", err))
		}
	}
}

// OnConfigurationSetChanged should be called when a configuration set is modified or reassigned
func (ps *PermissionService) OnConfigurationSetChanged(configurationSetID int) error {
	workspaceIDs, err := ps.getWorkspacesUsingConfigurationSet(configurationSetID)
	if err != nil {
		slog.Error("Failed to get workspaces for configuration set",
			slog.String("component", "permissions"),
			slog.Int("configuration_set_id", configurationSetID),
			slog.Any("error", err))
		return err
	}

	for _, workspaceID := range workspaceIDs {
		if err := ps.InvalidateWorkspaceMemberCaches(workspaceID); err != nil {
			slog.Warn("Failed to invalidate workspace member caches",
				slog.String("component", "permissions"),
				slog.Int("workspace_id", workspaceID),
				slog.Any("error", err))
		}
	}

	return nil
}

// Helper functions for cache invalidation

// getUsersWithRole returns all user IDs that have been assigned a specific role
func (ps *PermissionService) getUsersWithRole(roleID int) ([]int, error) {
	rows, err := ps.db.Query(`
		SELECT DISTINCT user_id
		FROM user_workspace_roles
		WHERE role_id = ?
	`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanIntColumn(rows)
}

// getUsersInGroupsWithRole returns all user IDs in groups that have been assigned a specific role
func (ps *PermissionService) getUsersInGroupsWithRole(roleID int) ([]int, error) {
	rows, err := ps.db.Query(`
		SELECT DISTINCT gm.user_id
		FROM group_workspace_roles gwr
		JOIN group_members gm ON gwr.group_id = gm.group_id
		WHERE gwr.role_id = ?
	`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanIntColumn(rows)
}

// getConfigurationSetsUsingPermissionSet returns all configuration set IDs using a specific permission set
func (ps *PermissionService) getConfigurationSetsUsingPermissionSet(permissionSetID int) ([]int, error) {
	rows, err := ps.db.Query(`
		SELECT id
		FROM configuration_sets
		WHERE permission_set_id = ?
	`, permissionSetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanIntColumn(rows)
}

// getWorkspacesUsingConfigurationSet returns all workspace IDs using a specific configuration set
func (ps *PermissionService) getWorkspacesUsingConfigurationSet(configSetID int) ([]int, error) {
	rows, err := ps.db.Query(`
		SELECT workspace_id
		FROM workspace_configuration_sets
		WHERE configuration_set_id = ?
	`, configSetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanIntColumn(rows)
}

// GetCacheStats returns current cache performance statistics
func (ps *PermissionService) GetCacheStats() models.CacheStats {
	hits := atomic.LoadInt64(&ps.hits)
	misses := atomic.LoadInt64(&ps.misses)
	errCount := atomic.LoadInt64(&ps.errors)
	total := hits + misses

	hitRatio := 0.0
	if total > 0 {
		hitRatio = float64(hits) / float64(total)
	}

	// Calculate the average workspace-permission check time without taking a
	// process-wide lock on the permission hot path.
	avgLoadTime := int64(0)
	checkCount := atomic.LoadInt64(&ps.permissionCheckCount)
	if checkCount > 0 {
		avgLoadTime = atomic.LoadInt64(&ps.permissionCheckNanos) / checkCount / int64(time.Millisecond)
	}

	// Get cache info - BigCache Stats doesn't have Entries field
	// We'll track total users differently or estimate it
	totalUsers := int64(0) // For now, we don't track this precisely
	workspaceAccessStats := ps.GetWorkspaceAccessStats()

	return models.CacheStats{
		Hits:                       hits,
		Misses:                     misses,
		Errors:                     errCount,
		HitRatio:                   hitRatio,
		AvgLoadTime:                avgLoadTime,
		TotalUsers:                 totalUsers,
		PermissionSnapshotDecodes:  workspaceAccessStats.PermissionSnapshotDecodes,
		ActiveWorkspaceCacheHits:   workspaceAccessStats.ActiveWorkspaceCacheHits,
		ActiveWorkspaceCacheMisses: workspaceAccessStats.ActiveWorkspaceCacheMisses,
	}
}

// buildUserPermissionCache loads complete permission profile from database
func (ps *PermissionService) buildUserPermissionCache(userID int) (*models.UserPermissionCache, error) {
	// Owned agents inherit their owner's permissions. Resolve the owner up front
	// and build the owner's cache; return it keyed under the agent's ID so the
	// permission-check hot path is unchanged.
	var ownerID sql.NullInt64
	var isAgent sql.NullBool
	err := ps.db.QueryRow(
		"SELECT COALESCE(is_agent, false), agent_owner_user_id FROM users WHERE id = ?",
		userID,
	).Scan(&isAgent, &ownerID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("error loading user for permission resolution: %w", err)
	}
	if isAgent.Valid && isAgent.Bool && ownerID.Valid {
		ownerCache, err := ps.buildUserPermissionCache(int(ownerID.Int64))
		if err != nil {
			return nil, err
		}
		agentCache := *ownerCache
		agentCache.UserID = userID
		return &agentCache, nil
	}

	now := time.Now()

	cached := &models.UserPermissionCache{
		UserID:               userID,
		IsSystemAdmin:        false,
		GlobalPermissions:    make(map[string]bool),
		WorkspacePermissions: make(map[int]map[string]bool),
		WorkspaceEveryone:    make(map[int]map[string]bool),
		GroupMemberships:     make([]int, 0),
		RoleAssignments:      make(map[int][]int),
		DirectPermissions:    make(map[int][]string),
		PermissionSources:    make(map[int]map[string]string),
		ItemWorkspaceMap:     make(map[int]int),
		CachedAt:             now,
		ExpiresAt:            now.Add(ps.ttl),
	}

	// Check if user has system.admin permission, either directly or via an
	// active group. Mirrors auth_policy.go's display SQL — see IsSystemAdmin.
	var hasSystemAdmin bool
	err = ps.db.QueryRow(repository.SystemAdminGrantQuery, userID, userID).Scan(&hasSystemAdmin)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cached, nil // User not found, return empty permissions
		}
		return nil, fmt.Errorf("error checking system admin permission: %w", err)
	}
	cached.IsSystemAdmin = hasSystemAdmin

	// If system admin, no need to load specific permissions
	if cached.IsSystemAdmin {
		return cached, nil
	}

	// Cache for role permissions (lazy-loaded per role ID)
	rolePermissionCache := make(map[int]map[string]bool)

	// Load workspace active flags once
	activeWorkspaces, err := ps.getWorkspaceActiveMap()
	if err != nil {
		return nil, fmt.Errorf("error loading workspace states: %w", err)
	}

	// Derive Everyone permissions from the absence of explicit role assignments.
	// Hierarchy: Viewer → Editor → Tester (each requires the previous to be open).
	// If a role has NO explicit assignments (user or group), everyone gets those permissions.
	// Admin always requires explicit assignment.

	var viewerRoleID, editorRoleID, testerRoleID int
	_ = ps.db.QueryRow(`SELECT id FROM workspace_roles WHERE name = ? LIMIT 1`, models.RoleViewer).Scan(&viewerRoleID)
	_ = ps.db.QueryRow(`SELECT id FROM workspace_roles WHERE name = ? LIMIT 1`, models.RoleEditor).Scan(&editorRoleID)
	_ = ps.db.QueryRow(`SELECT id FROM workspace_roles WHERE name = ? LIMIT 1`, models.RoleTester).Scan(&testerRoleID)

	explicitAssignments := make(map[int]map[int]bool) // workspace_id -> role_id -> true
	explicitRows, err := ps.db.Query(`
		SELECT DISTINCT workspace_id, role_id FROM user_workspace_roles
		UNION
		SELECT DISTINCT workspace_id, role_id FROM group_workspace_roles
	`)
	if err != nil {
		slog.Error("failed to load explicit role assignments",
			slog.String("component", "permissions"),
			slog.Int("user_id", userID),
			slog.Any("error", err))
	} else {
		defer func() { _ = explicitRows.Close() }()
		for explicitRows.Next() {
			var wsID, roleID int
			if err = explicitRows.Scan(&wsID, &roleID); err != nil {
				continue
			}
			if explicitAssignments[wsID] == nil {
				explicitAssignments[wsID] = make(map[int]bool)
			}
			explicitAssignments[wsID][roleID] = true
		}
		if err := explicitRows.Err(); err != nil {
			slog.Error("failed to iterate explicit role assignments", slog.String("component", "permissions"), slog.Int("user_id", userID), slog.Any("error", err))
		}
	}

	loadRolePerms := func(roleID int) map[string]bool {
		if roleID == 0 {
			return nil
		}
		perms, ok := rolePermissionCache[roleID]
		if !ok {
			perms, err = ps.getRolePermissions(roleID)
			if err == nil {
				rolePermissionCache[roleID] = perms
			}
		}
		return perms
	}

	viewerPerms := loadRolePerms(viewerRoleID)
	editorPerms := loadRolePerms(editorRoleID)
	testerPerms := loadRolePerms(testerRoleID)

	for wsID, active := range activeWorkspaces {
		if !active {
			continue
		}
		wsExplicit := explicitAssignments[wsID]

		if wsExplicit[viewerRoleID] {
			cached.WorkspaceEveryone[wsID] = map[string]bool{}
			continue
		}

		everyonePerms := clonePermissionSet(viewerPerms)

		editorOpen := !wsExplicit[editorRoleID]
		if editorOpen {
			mergePerms(everyonePerms, editorPerms)
		}

		testerOpen := editorOpen && !wsExplicit[testerRoleID]
		if testerOpen {
			mergePerms(everyonePerms, testerPerms)
		}

		cached.WorkspaceEveryone[wsID] = everyonePerms
	}

	globalRows, err := ps.db.Query(`
		SELECT p.permission_key
		FROM user_global_permissions ugp
		JOIN permissions p ON ugp.permission_id = p.id
		WHERE ugp.user_id = ?
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("error loading global permissions: %w", err)
	}
	defer func() { _ = globalRows.Close() }()

	for globalRows.Next() {
		var permissionKey string
		if err = globalRows.Scan(&permissionKey); err != nil {
			continue
		}
		cached.GlobalPermissions[permissionKey] = true
	}
	if err := globalRows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating global permissions: %w", err)
	}

	// Load global permissions inherited via active group membership. The
	// admin handlers and auth_policy display already treat these as real;
	// the cache must too or middleware denies what the UI promises.
	groupGlobalRows, err := ps.db.Query(`
		SELECT DISTINCT p.permission_key
		FROM group_members gm
		JOIN groups g ON g.id = gm.group_id
		JOIN group_global_permissions ggp ON ggp.group_id = gm.group_id
		JOIN permissions p ON p.id = ggp.permission_id
		WHERE gm.user_id = ? AND g.is_active = true
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("error loading group global permissions: %w", err)
	}
	defer func() { _ = groupGlobalRows.Close() }()

	for groupGlobalRows.Next() {
		var permissionKey string
		if err = groupGlobalRows.Scan(&permissionKey); err != nil {
			continue
		}
		cached.GlobalPermissions[permissionKey] = true
	}
	if err := groupGlobalRows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating group global permissions: %w", err)
	}

	// Load group memberships, scoped to active groups only. Inactive groups
	// must not contribute permissions; filtering here means every downstream
	// pass that keys off cached.GroupMemberships is automatically scoped.
	groupRows, err := ps.db.Query(`
		SELECT gm.group_id
		FROM group_members gm
		JOIN groups g ON g.id = gm.group_id
		WHERE gm.user_id = ? AND g.is_active = true
	`, userID)
	if err == nil {
		defer func() { _ = groupRows.Close() }()
		for groupRows.Next() {
			var groupID int
			if err = groupRows.Scan(&groupID); err == nil {
				cached.GroupMemberships = append(cached.GroupMemberships, groupID)
			}
		}
		if err := groupRows.Err(); err != nil {
			return nil, fmt.Errorf("error iterating group memberships: %w", err)
		}
	}

	// Load user's role assignments — independent of permissions_enabled. Even
	// label-only (custom) roles are tracked here so HasWorkspaceRole and
	// downstream checks (conditions, approval routing) see them.
	roleAssignRows, err := ps.db.Query(`
		SELECT workspace_id, role_id FROM user_workspace_roles WHERE user_id = ?
	`, userID)
	if err == nil {
		for roleAssignRows.Next() {
			var workspaceID, roleID int
			if err = roleAssignRows.Scan(&workspaceID, &roleID); err != nil {
				continue
			}
			if cached.RoleAssignments[workspaceID] == nil {
				cached.RoleAssignments[workspaceID] = []int{}
			}
			roleExists := false
			for _, rid := range cached.RoleAssignments[workspaceID] {
				if rid == roleID {
					roleExists = true
					break
				}
			}
			if !roleExists {
				cached.RoleAssignments[workspaceID] = append(cached.RoleAssignments[workspaceID], roleID)
			}
		}
		if err := roleAssignRows.Err(); err != nil {
			_ = roleAssignRows.Close()
			return nil, fmt.Errorf("error iterating role assignments: %w", err)
		}
		_ = roleAssignRows.Close()
	}

	// Derive permissions from those roles, filtered to permission-bearing only.
	// Custom (label-only) roles never contribute to a user's permission set,
	// even if a permission row gets attached to them via direct DB access.
	roleRows, err := ps.db.Query(`
		SELECT uwr.workspace_id, p.permission_key
		FROM user_workspace_roles uwr
		JOIN workspace_roles wr ON wr.id = uwr.role_id AND wr.permissions_enabled = true
		JOIN role_permissions rp ON uwr.role_id = rp.role_id
		JOIN permissions p ON rp.permission_id = p.id
		WHERE uwr.user_id = ?
	`, userID)
	if err == nil {
		defer func() { _ = roleRows.Close() }()
		for roleRows.Next() {
			var workspaceID int
			var permissionKey string
			if err = roleRows.Scan(&workspaceID, &permissionKey); err != nil {
				continue
			}

			if cached.WorkspacePermissions[workspaceID] == nil {
				cached.WorkspacePermissions[workspaceID] = make(map[string]bool)
			}
			cached.WorkspacePermissions[workspaceID][permissionKey] = true

			if cached.PermissionSources[workspaceID] == nil {
				cached.PermissionSources[workspaceID] = make(map[string]string)
			}
			if cached.PermissionSources[workspaceID][permissionKey] == "" {
				cached.PermissionSources[workspaceID][permissionKey] = "role"
			}
		}
		if err := roleRows.Err(); err != nil {
			return nil, fmt.Errorf("error iterating role permissions: %w", err)
		}
	}

	// Load group role assignments (permissions granted via group membership)
	if len(cached.GroupMemberships) > 0 {
		// Build group ID list for query
		groupIDList := ""
		for i, gid := range cached.GroupMemberships {
			if i > 0 {
				groupIDList += ","
			}
			groupIDList += fmt.Sprintf("%d", gid)
		}

		// Same filter as the user-role pass: label-only roles assigned to a
		// group are honored for membership tracking elsewhere but never
		// contribute permissions.
		groupRoleQuery := fmt.Sprintf(`
			SELECT gwr.workspace_id, p.permission_key
			FROM group_workspace_roles gwr
			JOIN workspace_roles wr ON wr.id = gwr.role_id AND wr.permissions_enabled = true
			JOIN role_permissions rp ON gwr.role_id = rp.role_id
			JOIN permissions p ON rp.permission_id = p.id
			WHERE gwr.group_id IN (%s)
		`, groupIDList)

		var groupRoleRows *sql.Rows
		groupRoleRows, err = ps.db.Query(groupRoleQuery)
		if err == nil {
			defer func() { _ = groupRoleRows.Close() }()
			for groupRoleRows.Next() {
				var workspaceID int
				var permissionKey string
				if err = groupRoleRows.Scan(&workspaceID, &permissionKey); err != nil {
					continue
				}

				// Add permission from group
				if cached.WorkspacePermissions[workspaceID] == nil {
					cached.WorkspacePermissions[workspaceID] = make(map[string]bool)
				}
				cached.WorkspacePermissions[workspaceID][permissionKey] = true

				// Track source (only if not already set by role or direct)
				if cached.PermissionSources[workspaceID] == nil {
					cached.PermissionSources[workspaceID] = make(map[string]string)
				}
				if cached.PermissionSources[workspaceID][permissionKey] == "" {
					cached.PermissionSources[workspaceID][permissionKey] = "group"
				}
			}
			if err := groupRoleRows.Err(); err != nil {
				return nil, fmt.Errorf("error iterating group role permissions: %w", err)
			}
		}
	}

	// Grant all permissions for personal workspaces owned by this user
	personalRows, err := ps.db.Query(`
		SELECT w.id FROM workspaces w WHERE w.is_personal = true AND w.owner_id = ? AND w.active = true
	`, userID)
	if err == nil {
		defer func() { _ = personalRows.Close() }()

		// Lazy-load if startup pre-load failed
		if len(ps.allPermissionKeys) == 0 {
			if err := ps.loadAllPermissionKeys(); err != nil {
				slog.Warn("Failed to lazy-load permission keys for personal workspace grant",
					slog.String("component", "permissions"),
					slog.Int("user_id", userID),
					slog.Any("error", err))
			}
		}

		if len(ps.allPermissionKeys) > 0 {
			for personalRows.Next() {
				var wsID int
				if err := personalRows.Scan(&wsID); err != nil {
					continue
				}
				if cached.WorkspacePermissions[wsID] == nil {
					cached.WorkspacePermissions[wsID] = make(map[string]bool)
				}
				for _, key := range ps.allPermissionKeys {
					cached.WorkspacePermissions[wsID][key] = true
				}
				if cached.PermissionSources[wsID] == nil {
					cached.PermissionSources[wsID] = make(map[string]string)
				}
				cached.PermissionSources[wsID]["_source"] = "personal_owner"
			}
			if err := personalRows.Err(); err != nil {
				return nil, fmt.Errorf("error iterating personal workspaces: %w", err)
			}
		}
	}

	return cached, nil
}

// storeUserPermissionCache stores permission cache data
func (ps *PermissionService) storeUserPermissionCache(userID int, cached *models.UserPermissionCache) error {
	cacheKey := ps.getCacheKey(userID)

	data, err := json.Marshal(cached)
	if err != nil {
		return fmt.Errorf("error marshaling cache data: %w", err)
	}

	return ps.cache.Set(cacheKey, data)
}

// getWorkspaceActiveMap returns a map of workspace_id -> active flag
func (ps *PermissionService) getWorkspaceActiveMap() (map[int]bool, error) {
	rows, err := ps.db.Query(`SELECT id, active FROM workspaces WHERE is_personal = false OR is_personal IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int]bool)
	for rows.Next() {
		var id int
		var active bool
		if err := rows.Scan(&id, &active); err != nil {
			return nil, err
		}
		result[id] = active
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// getRolePermissions loads permission keys for a given workspace role id
func (ps *PermissionService) getRolePermissions(roleID int) (map[string]bool, error) {
	rows, err := ps.db.Query(`
		SELECT p.permission_key
		FROM role_permissions rp
		JOIN permissions p ON rp.permission_id = p.id
		WHERE rp.role_id = ?
	`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	perms := make(map[string]bool)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err == nil {
			perms[key] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return perms, nil
}

// loadAllPermissionKeys fetches all permission keys from the database and
// stores them on the service. The permissions table is static, so this only
// needs to run once (at startup or lazily on first cache build).
func (ps *PermissionService) loadAllPermissionKeys() error {
	rows, err := ps.db.Query(`SELECT permission_key FROM permissions`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err == nil {
			keys = append(keys, key)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	ps.allPermissionKeys = keys
	return nil
}

// scanIntColumn collects a single-int-column result set into a slice. Rows
// that fail to scan are skipped (matching the lenient behavior the cache
// invalidation queries have always relied on); any iteration error surfaces.
func scanIntColumn(rows *sql.Rows) ([]int, error) {
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func clonePermissionSet(src map[string]bool) map[string]bool {
	dst := make(map[string]bool, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func mergePerms(dst, src map[string]bool) {
	for k, v := range src {
		if v {
			dst[k] = true
		}
	}
}

// WarmCache pre-loads permissions for recently active users
func (ps *PermissionService) WarmCache() {
	slog.Info("Starting permission cache warm-up",
		slog.String("component", "permissions"))

	// Get recently active users (last 24 hours)
	activeUsers, err := ps.getRecentlyActiveUsers(24 * time.Hour)
	if err != nil {
		slog.Error("Error getting recently active users for cache warm-up",
			slog.String("component", "permissions"),
			slog.Any("error", err))
		return
	}

	warmedCount := 0
	for _, userID := range activeUsers {
		if err := ps.preWarmUserCache(userID); err != nil {
			slog.Warn("Error warming cache for user",
				slog.String("component", "permissions"),
				slog.Int("user_id", userID),
				slog.Any("error", err))
			continue
		}
		warmedCount++

		// Add small delay to prevent overwhelming the database
		if warmedCount%ps.batchSize == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	slog.Info("Permission cache warm-up completed",
		slog.String("component", "permissions"),
		slog.Int("users_cached", warmedCount))
}

// preWarmUserCache loads and caches permissions for a specific user
func (ps *PermissionService) preWarmUserCache(userID int) error {
	_, err := ps.buildAndStoreUserPermissionCache(userID)
	return err
}

// getRecentlyActiveUsers returns user IDs who were active in the specified duration
func (ps *PermissionService) getRecentlyActiveUsers(duration time.Duration) ([]int, error) {
	since := time.Now().Add(-duration)

	rows, err := ps.db.Query(`
		SELECT user_id
		FROM user_sessions
		WHERE is_active = true AND expires_at > CURRENT_TIMESTAMP AND created_at > ?
		GROUP BY user_id
		ORDER BY MAX(created_at) DESC
		LIMIT ?
	`, since, ps.batchSize*2) // Limit to prevent excessive warm-up

	if err != nil {
		// If session table doesn't exist or has issues, fall back to basic user list
		rows, err = ps.db.Query(`
			SELECT id FROM users 
			WHERE is_active = true
			ORDER BY updated_at DESC
			LIMIT ?
		`, ps.batchSize)

		if err != nil {
			return nil, err
		}
	}
	defer rows.Close()

	return scanIntColumn(rows)
}

// Close gracefully shuts down the permission service
func (ps *PermissionService) Close() error {
	return ps.cache.Close()
}
