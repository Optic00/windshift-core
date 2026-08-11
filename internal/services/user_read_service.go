package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

// UserReadService provides read operations for users
type UserReadService struct {
	db database.Database
}

// AdminUserUpdate holds optional user fields for system-admin updates. Empty
// string values are ignored to preserve the existing REST v1 semantics.
type AdminUserUpdate struct {
	FirstName *string
	LastName  *string
	Email     *string
	IsActive  *bool
}

// IsEmpty reports whether the update would touch no persisted fields.
func (u AdminUserUpdate) IsEmpty() bool {
	return (u.FirstName == nil || *u.FirstName == "") &&
		(u.LastName == nil || *u.LastName == "") &&
		(u.Email == nil || *u.Email == "") &&
		u.IsActive == nil
}

// NewUserReadService creates a new user read service
func NewUserReadService(db database.Database) *UserReadService {
	return &UserReadService{db: db}
}

// hydrateUser populates nullable fields and the computed FullName on a User.
func hydrateUser(u *models.User, avatarURL, timezone, language sql.NullString) {
	u.FullName = u.FirstName + " " + u.LastName
	if avatarURL.Valid {
		u.AvatarURL = avatarURL.String
	}
	if timezone.Valid {
		u.Timezone = timezone.String
	}
	if language.Valid {
		u.Language = language.String
	}
}

// scanUserRow scans a single user row from the standard column set
// (id, email, username, first_name, last_name, is_active, avatar_url, timezone, language, is_agent, agent_owner_user_id, created_at)
// and returns a fully hydrated User.
func scanUserRow(scanner interface{ Scan(dest ...any) error }) (models.User, error) {
	var u models.User
	var avatarURL, timezone, language sql.NullString
	var agentOwnerUserID sql.NullInt64
	err := scanner.Scan(&u.ID, &u.Email, &u.Username, &u.FirstName, &u.LastName, &u.IsActive,
		&avatarURL, &timezone, &language, &u.IsAgent, &agentOwnerUserID, &u.CreatedAt)
	if err != nil {
		return u, err
	}
	hydrateUser(&u, avatarURL, timezone, language)
	if agentOwnerUserID.Valid {
		owner := int(agentOwnerUserID.Int64)
		u.AgentOwnerUserID = &owner
	}
	return u, nil
}

// List retrieves active users with pagination
func (s *UserReadService) List(pagination PaginationParams) ([]models.User, int, error) {
	rows, err := s.db.Query(`
		SELECT id, email, username, first_name, last_name, is_active, avatar_url, timezone, language, COALESCE(is_agent, false), agent_owner_user_id, created_at
		FROM users
		WHERE is_active = true
		ORDER BY first_name, last_name
		LIMIT ? OFFSET ?
	`, pagination.Limit, pagination.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list users: %w", err)
	}
	defer rows.Close()

	var users []models.User
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			continue
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to iterate users: %w", err)
	}

	if users == nil {
		users = []models.User{}
	}

	// Get total count
	var total int
	err = s.db.QueryRow("SELECT COUNT(*) FROM users WHERE is_active = true").Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count users: %w", err)
	}

	return users, total, nil
}

// GetByID retrieves a user by ID
func (s *UserReadService) GetByID(id int) (*models.User, error) {
	row := s.db.QueryRow(`
		SELECT id, email, username, first_name, last_name, is_active, avatar_url, timezone, language, COALESCE(is_agent, false), agent_owner_user_id, created_at
		FROM users WHERE id = ?
	`, id)

	u, err := scanUserRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("user not found: %d: %w", id, repository.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	return &u, nil
}

// UpdateAdmin applies a partial system-admin update to a user. Callers should
// check AdminUserUpdate.IsEmpty before invoking it. ErrUserNotFound is returned
// when no row is updated.
func (s *UserReadService) UpdateAdmin(id int, update AdminUserUpdate) error {
	sets := []string{}
	args := []any{}
	if update.FirstName != nil && *update.FirstName != "" {
		sets = append(sets, "first_name = ?")
		args = append(args, *update.FirstName)
	}
	if update.LastName != nil && *update.LastName != "" {
		sets = append(sets, "last_name = ?")
		args = append(args, *update.LastName)
	}
	if update.Email != nil && *update.Email != "" {
		sets = append(sets, "email = ?")
		args = append(args, *update.Email)
	}
	if update.IsActive != nil {
		sets = append(sets, "is_active = ?")
		args = append(args, *update.IsActive)
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, id)

	result, err := s.db.ExecWrite("UPDATE users SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update user rows affected: %w", err)
	}
	if rows == 0 {
		return ErrUserNotFound
	}
	return nil
}

// GetGroupIDs returns active group membership IDs for a user.
func (s *UserReadService) GetGroupIDs(userID int) ([]int, error) {
	rows, err := s.db.Query("SELECT group_id FROM group_members WHERE user_id = ?", userID)
	if err != nil {
		return nil, fmt.Errorf("list user groups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	ids := []int{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan user group: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user groups: %w", err)
	}
	return ids, nil
}

// ListAll retrieves all active users without pagination.
func (s *UserReadService) ListAll() ([]models.User, error) {
	rows, err := s.db.Query(`
		SELECT id, email, username, first_name, last_name, is_active, avatar_url, timezone, language, COALESCE(is_agent, false), agent_owner_user_id, created_at
		FROM users
		WHERE is_active = true
		ORDER BY first_name, last_name
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to list users: %w", err)
	}
	defer rows.Close()

	var users []models.User
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			continue
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate users: %w", err)
	}

	if users == nil {
		users = []models.User{}
	}

	return users, nil
}

// CountActive returns the number of active users.
func (s *UserReadService) CountActive() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM users WHERE is_active = true").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count active users: %w", err)
	}
	return count, nil
}

// Exists checks if a user exists by ID
func (s *UserReadService) Exists(id int) (bool, error) {
	var exists bool
	err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM users WHERE id = ?)", id).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check user existence: %w", err)
	}
	return exists, nil
}

// ListAllowlistedCentralizedServiceUsers returns the active, unowned
// agent users (is_agent + agent_owner_user_id IS NULL) that the WI-87
// global allowlist makes reachable from this workspace — either via a
// workspace-scoped grant or a workspace_id IS NULL "any workspace"
// grant. Does NOT consult the master flag; the caller is responsible
// for gating that.
func (s *UserReadService) ListAllowlistedCentralizedServiceUsers(ctx context.Context, workspaceID int) ([]models.User, error) {
	if workspaceID <= 0 {
		return []models.User{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.id, u.email, u.username, u.first_name, u.last_name, u.is_active,
		       u.avatar_url, u.timezone, u.language,
		       COALESCE(u.is_agent, false), u.agent_owner_user_id, u.created_at
		FROM users u
		INNER JOIN global_agent_acting_user_allowlist a ON a.user_id = u.id
		WHERE COALESCE(u.is_agent, false) = true
		  AND u.agent_owner_user_id IS NULL
		  AND COALESCE(u.is_active, true) = true
		  AND (a.workspace_id IS NULL OR a.workspace_id = ?)
		GROUP BY u.id, u.email, u.username, u.first_name, u.last_name, u.is_active,
		         u.avatar_url, u.timezone, u.language, u.is_agent, u.agent_owner_user_id, u.created_at
		ORDER BY u.first_name, u.last_name, u.username
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list allowlisted centralized service users: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []models.User
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []models.User{}
	}
	return out, nil
}

// IsCentralizedServiceUser reports whether the user row exists, is an
// agent identity (is_agent = true), and is *not* owned by anyone
// (agent_owner_user_id IS NULL). The WI-87 admin allowlist editor uses
// this to refuse non-service users at the boundary — owned agents reach
// bindings through the chokepoint directly without needing a grant, and
// regular humans must never be impersonated by the harness. Returns
// (false, nil) when the user row does not exist so callers can render
// a 400 without leaking row existence.
func (s *UserReadService) IsCentralizedServiceUser(ctx context.Context, userID int) (bool, error) {
	if userID <= 0 {
		return false, nil
	}
	var (
		isAgent  bool
		hasOwner bool
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(is_agent, false), agent_owner_user_id IS NOT NULL
		FROM users WHERE id = ?
	`, userID).Scan(&isAgent, &hasOwner)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read user for service-user check: %w", err)
	}
	return isAgent && !hasOwner, nil
}
