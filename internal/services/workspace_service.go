package services

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

// WorkspaceService encapsulates workspace business logic used by both HTTP handlers
// and other services.
type WorkspaceService struct {
	db   database.Database
	repo *repository.WorkspaceRepository
}

// NewWorkspaceService creates a new WorkspaceService.
func NewWorkspaceService(db database.Database) *WorkspaceService {
	return &WorkspaceService{
		db:   db,
		repo: repository.NewWorkspaceRepository(db),
	}
}

// WorkspaceListParams contains the parameters for listing workspaces.
type WorkspaceListParams struct {
	UserID int
	Limit  int
	Offset int
}

// WorkspaceListResult contains a workspace with minimal fields for list views.
type WorkspaceListResult struct {
	ID          int
	Name        string
	Key         string
	Description string
	Active      bool
	IsPersonal  bool
	Icon        string
	Color       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// List retrieves all workspaces accessible to a user with pagination.
// This checks both direct user workspace roles and group workspace roles.
func (s *WorkspaceService) List(params WorkspaceListParams) ([]WorkspaceListResult, int, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT w.id, w.name, w.key, w.description, w.active, w.is_personal,
		       w.icon, w.color, w.created_at, w.updated_at
		FROM workspaces w
		LEFT JOIN user_workspace_roles uwr ON w.id = uwr.workspace_id AND uwr.user_id = ?
		LEFT JOIN (
			SELECT DISTINCT gwr.workspace_id
			FROM group_workspace_roles gwr
			JOIN group_members gm ON gwr.group_id = gm.group_id
			WHERE gm.user_id = ?
		) grp ON w.id = grp.workspace_id
		WHERE (w.active = true AND (w.is_personal = false OR w.is_personal IS NULL))
		   OR (w.active = false AND uwr.role_id IS NOT NULL)
		   OR (w.active = false AND grp.workspace_id IS NOT NULL)
		   OR (w.is_personal = true AND w.owner_id = ?)
		ORDER BY w.name
		LIMIT ? OFFSET ?
	`, params.UserID, params.UserID, params.UserID, params.Limit, params.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list workspaces: %w", err)
	}
	defer rows.Close()

	var workspaces []WorkspaceListResult
	for rows.Next() {
		var ws WorkspaceListResult
		var icon, color sql.NullString
		err = rows.Scan(&ws.ID, &ws.Name, &ws.Key, &ws.Description, &ws.Active, &ws.IsPersonal,
			&icon, &color, &ws.CreatedAt, &ws.UpdatedAt)
		if err != nil {
			continue
		}
		ws.Icon = icon.String
		ws.Color = color.String
		workspaces = append(workspaces, ws)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to iterate workspaces: %w", err)
	}

	if workspaces == nil {
		workspaces = []WorkspaceListResult{}
	}

	// Get total count
	var total int
	err = s.db.QueryRow(`
		SELECT COUNT(DISTINCT w.id)
		FROM workspaces w
		LEFT JOIN user_workspace_roles uwr ON w.id = uwr.workspace_id AND uwr.user_id = ?
		LEFT JOIN (
			SELECT DISTINCT gwr.workspace_id
			FROM group_workspace_roles gwr
			JOIN group_members gm ON gwr.group_id = gm.group_id
			WHERE gm.user_id = ?
		) grp ON w.id = grp.workspace_id
		WHERE (w.active = true AND (w.is_personal = false OR w.is_personal IS NULL))
		   OR (w.active = false AND uwr.role_id IS NOT NULL)
		   OR (w.active = false AND grp.workspace_id IS NOT NULL)
		   OR (w.is_personal = true AND w.owner_id = ?)
	`, params.UserID, params.UserID, params.UserID).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count workspaces: %w", err)
	}

	return workspaces, total, nil
}

// GetByID retrieves a workspace by ID with minimal fields.
func (s *WorkspaceService) GetByID(id int) (*WorkspaceListResult, error) {
	var ws WorkspaceListResult
	var icon, color sql.NullString
	err := s.db.QueryRow(`
		SELECT id, name, key, description, active, is_personal, icon, color, created_at, updated_at
		FROM workspaces WHERE id = ?
	`, id).Scan(&ws.ID, &ws.Name, &ws.Key, &ws.Description, &ws.Active, &ws.IsPersonal,
		&icon, &color, &ws.CreatedAt, &ws.UpdatedAt)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("workspace not found: %d: %w", id, repository.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace: %w", err)
	}

	ws.Icon = icon.String
	ws.Color = color.String

	return &ws, nil
}

// CreateWorkspaceParams contains the parameters for creating a workspace.
type CreateWorkspaceParams struct {
	Name        string
	Key         string
	Description string
	Icon        string
	Color       string
	CreatorID   int
}

// CreateWorkspaceResult contains the result of creating a workspace.
type CreateWorkspaceResult struct {
	Workspace *WorkspaceListResult
}

// Create creates a new workspace and grants admin permission to the creator.
func (s *WorkspaceService) Create(params CreateWorkspaceParams) (*CreateWorkspaceResult, error) {
	// Normalize key to uppercase
	key := strings.ToUpper(params.Key)

	// Check for duplicate key
	exists, err := s.repo.KeyExists(key)
	if err != nil {
		return nil, fmt.Errorf("failed to check key existence: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("workspace key already exists: %s", key)
	}

	// Create workspace
	var id int64
	err = s.db.QueryRow(`
		INSERT INTO workspaces (name, key, description, icon, color, active)
		VALUES (?, ?, ?, ?, ?, true) RETURNING id
	`, params.Name, key, params.Description, params.Icon, params.Color).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}

	// Grant admin permission to creator
	_, err = s.db.ExecWrite(`
		INSERT INTO user_workspace_roles (workspace_id, user_id, role_id, granted_by, granted_at)
		SELECT ?, ?, id, ?, CURRENT_TIMESTAMP FROM workspace_roles WHERE name = 'Administrator'
	`, id, params.CreatorID, params.CreatorID)
	if err != nil {
		slog.Warn("failed to grant admin permission to workspace creator", "error", err, "workspace_id", id)
	}

	// Return created workspace
	ws, err := s.GetByID(int(id))
	if err != nil {
		return nil, fmt.Errorf("workspace created but failed to retrieve: %w", err)
	}

	return &CreateWorkspaceResult{Workspace: ws}, nil
}

// UpdateWorkspaceParams contains the parameters for updating a workspace.
type UpdateWorkspaceParams struct {
	ID          int
	Name        *string
	Description *string
	Active      *bool
	Icon        *string
	Color       *string
}

// Update updates an existing workspace.
func (s *WorkspaceService) Update(params UpdateWorkspaceParams) (*WorkspaceListResult, error) {
	// Load existing workspace
	var ws struct {
		ID          int
		Name        string
		Description string
		Active      bool
		Icon        sql.NullString
		Color       sql.NullString
	}
	err := s.db.QueryRow("SELECT id, name, description, active, icon, color FROM workspaces WHERE id = ?", params.ID).
		Scan(&ws.ID, &ws.Name, &ws.Description, &ws.Active, &ws.Icon, &ws.Color)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("workspace not found: %d: %w", params.ID, repository.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load workspace: %w", err)
	}

	// Apply updates
	if params.Name != nil {
		ws.Name = *params.Name
	}
	if params.Description != nil {
		ws.Description = *params.Description
	}
	if params.Active != nil {
		ws.Active = *params.Active
	}
	if params.Icon != nil {
		ws.Icon = sql.NullString{String: *params.Icon, Valid: true}
	}
	if params.Color != nil {
		ws.Color = sql.NullString{String: *params.Color, Valid: true}
	}

	_, err = s.db.ExecWrite(`
		UPDATE workspaces SET name = ?, description = ?, active = ?, icon = ?, color = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, ws.Name, ws.Description, ws.Active, ws.Icon.String, ws.Color.String, params.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to update workspace: %w", err)
	}

	// Return updated workspace
	return s.GetByID(params.ID)
}

// Delete removes a workspace by ID.
func (s *WorkspaceService) Delete(id int) error {
	// Check workspace exists
	exists, err := s.repo.Exists(id)
	if err != nil {
		return fmt.Errorf("failed to check workspace existence: %w", err)
	}
	if !exists {
		return fmt.Errorf("workspace not found: %d: %w", id, repository.ErrNotFound)
	}

	// Delete workspace (cascade will handle related records)
	err = s.repo.Delete(id)
	if err != nil {
		return fmt.Errorf("failed to delete workspace: %w", err)
	}

	return nil
}

// Exists checks if a workspace exists.
// deadcode-keep: called by core-tests/internal/services/workspace_service_test.go
func (s *WorkspaceService) Exists(id int) (bool, error) {
	return s.repo.Exists(id)
}

// KeyExists checks if a workspace key exists.
func (s *WorkspaceService) KeyExists(key string) (bool, error) {
	return s.repo.KeyExists(strings.ToUpper(key))
}

// GetStatuses retrieves statuses available through the workspace's effective
// workflows. This follows the same fallback chain used for item transitions:
// item-type override, configuration-set workflow, then the global default
// workflow. A status is returned only when at least one applicable workflow
// references it. Personal workspaces are not workflow-bound and retain access
// to the full status catalog.
func (s *WorkspaceService) GetStatuses(workspaceID int) ([]models.Status, error) {
	statuses, err := s.GetStatusesForWorkspaces([]int{workspaceID})
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace statuses: %w", err)
	}
	return statuses, nil
}

// GetStatusesForWorkspaces returns the union of statuses available to any of
// the supplied workspaces in one query. An empty workspace list means global
// status context and therefore returns the complete status catalog.
func (s *WorkspaceService) GetStatusesForWorkspaces(workspaceIDs []int) ([]models.Status, error) {
	if len(workspaceIDs) == 0 {
		return repository.NewStatusRepository(s.db).List()
	}

	placeholders := make([]string, len(workspaceIDs))
	args := make([]any, len(workspaceIDs))
	for i, workspaceID := range workspaceIDs {
		placeholders[i] = "?"
		args[i] = workspaceID
	}

	rows, err := s.db.Query(fmt.Sprintf(`
		WITH target_workspaces AS (
			SELECT id, is_personal
			FROM workspaces
			WHERE id IN (%s)
		), effective_workflows AS (
			SELECT target.id AS workspace_id,
			       target.is_personal,
			       COALESCE(
			         csit.workflow_id,
			         cs.workflow_id,
			         (SELECT id FROM workflows WHERE is_default = true ORDER BY id LIMIT 1)
			       ) AS workflow_id
			FROM target_workspaces target
			LEFT JOIN workspace_configuration_sets wcs ON wcs.workspace_id = target.id
			LEFT JOIN configuration_sets cs ON cs.id = wcs.configuration_set_id
			LEFT JOIN configuration_set_item_types csit ON csit.configuration_set_id = cs.id
		), available_statuses AS (
			SELECT wt.from_status_id AS status_id
			FROM effective_workflows ew
			JOIN workflow_transitions wt ON wt.workflow_id = ew.workflow_id
			WHERE wt.from_status_id IS NOT NULL
			UNION
			SELECT wt.to_status_id AS status_id
			FROM effective_workflows ew
			JOIN workflow_transitions wt ON wt.workflow_id = ew.workflow_id
		)
		SELECT DISTINCT s.id, s.name, s.description, s.category_id, s.is_default,
		       sc.name as category_name, sc.color as category_color, sc.is_completed
		FROM statuses s
		JOIN status_categories sc ON s.category_id = sc.id
		WHERE s.id IN (SELECT status_id FROM available_statuses)
		   OR EXISTS (SELECT 1 FROM target_workspaces WHERE is_personal = true)
		ORDER BY s.category_id, s.name
	`, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get statuses for workspaces: %w", err)
	}
	defer rows.Close()
	return scanWorkspaceStatuses(rows)
}

func scanWorkspaceStatuses(rows *sql.Rows) ([]models.Status, error) {
	statuses := []models.Status{}
	for rows.Next() {
		var status models.Status
		var description sql.NullString
		err := rows.Scan(
			&status.ID, &status.Name, &description, &status.CategoryID, &status.IsDefault,
			&status.CategoryName, &status.CategoryColor, &status.IsCompleted,
		)
		if err != nil {
			continue
		}
		status.Description = description.String
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate workspace statuses: %w", err)
	}
	return statuses, nil
}

// GetItemTypes retrieves item types available for a workspace via its configuration set.
// If the workspace has a config set with item types defined, only those are returned.
// If no config set exists, all item types are returned.
func (s *WorkspaceService) GetItemTypes(workspaceID int) ([]ItemTypeResult, error) {
	rows, err := s.db.Query(`
		SELECT it.id, it.name, it.description, it.icon, it.color,
		       it.hierarchy_level, it.sort_order, it.is_default
		FROM item_types it
		WHERE NOT EXISTS (
			SELECT 1 FROM workspace_configuration_sets wcs
			JOIN configuration_set_item_types csit ON wcs.configuration_set_id = csit.configuration_set_id
			WHERE wcs.workspace_id = ?
		)
		OR EXISTS (
			SELECT 1 FROM workspace_configuration_sets wcs
			JOIN configuration_set_item_types csit ON wcs.configuration_set_id = csit.configuration_set_id
			WHERE wcs.workspace_id = ? AND csit.item_type_id = it.id
		)
		ORDER BY CASE WHEN it.hierarchy_level = -1 THEN 1 ELSE 0 END, it.hierarchy_level, it.sort_order, it.name
	`, workspaceID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace item types: %w", err)
	}

	return ScanItemTypes(rows)
}

// GetPriorities returns the priorities enabled for a workspace's configuration
// set. When the workspace has no configuration set (or no priorities mapped to
// it), all priorities are returned — mirroring GetItemTypes/GetStatuses.
func (s *WorkspaceService) GetPriorities(workspaceID int) ([]PriorityResult, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT p.id, p.name, p.description, p.icon, p.color,
		       p.sort_order, p.is_default
		FROM priorities p
		WHERE NOT EXISTS (
			SELECT 1 FROM workspace_configuration_sets wcs
			JOIN configuration_set_priorities csp ON wcs.configuration_set_id = csp.configuration_set_id
			WHERE wcs.workspace_id = ?
		)
		OR EXISTS (
			SELECT 1 FROM workspace_configuration_sets wcs
			JOIN configuration_set_priorities csp ON wcs.configuration_set_id = csp.configuration_set_id
			WHERE wcs.workspace_id = ? AND csp.priority_id = p.id
		)
		ORDER BY p.sort_order, p.name
	`, workspaceID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace priorities: %w", err)
	}

	return ScanPriorities(rows)
}
