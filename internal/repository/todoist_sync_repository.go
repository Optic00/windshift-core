package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
)

// TodoistSyncRepository handles persistence for the Todoist personal-task sync
// configuration (todoist_sync_config) and the item <-> Todoist-task id map
// (todoist_task_links).
type TodoistSyncRepository struct {
	db database.Database
}

// NewTodoistSyncRepository creates a TodoistSyncRepository.
func NewTodoistSyncRepository(db database.Database) *TodoistSyncRepository {
	return &TodoistSyncRepository{db: db}
}

const todoistSyncConfigColumns = "id, user_id, integration_provider_id, personal_workspace_id, enabled, scope_mode, todoist_project_id, sync_token, last_synced_at, last_error, created_at, updated_at"

// GetConfig returns the sync config for a (user, provider) pair, or ErrNotFound.
func (r *TodoistSyncRepository) GetConfig(userID, providerID string) (*models.TodoistSyncConfig, error) {
	row := r.db.QueryRow("SELECT "+todoistSyncConfigColumns+" FROM todoist_sync_config WHERE user_id = ? AND integration_provider_id = ?", userID, providerID)
	cfg, err := scanTodoistSyncConfig(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get todoist_sync_config: %w", err)
	}
	return &cfg, nil
}

// UpsertConfig creates or updates the user-facing config fields (workspace,
// enabled flag, scope). It always resets sync_token to "*" so a settings change
// (especially a scope change) triggers a full re-sync on the next run; the
// mapping table makes a full re-sync idempotent. Sync-run state (last_synced_at,
// last_error) is left untouched here and updated via UpdateSyncState.
func (r *TodoistSyncRepository) UpsertConfig(cfg models.TodoistSyncConfig) error {
	_, err := r.db.ExecWrite(`
		INSERT INTO todoist_sync_config (
			id, user_id, integration_provider_id, personal_workspace_id,
			enabled, scope_mode, todoist_project_id, sync_token
		) VALUES (?, ?, ?, ?, ?, ?, ?, '*')
		ON CONFLICT (user_id, integration_provider_id) DO UPDATE SET
			personal_workspace_id = excluded.personal_workspace_id,
			enabled = excluded.enabled,
			scope_mode = excluded.scope_mode,
			todoist_project_id = excluded.todoist_project_id,
			sync_token = '*',
			updated_at = CURRENT_TIMESTAMP
	`, cfg.ID, cfg.UserID, cfg.IntegrationProviderID, cfg.PersonalWorkspaceID,
		cfg.Enabled, string(cfg.ScopeMode), cfg.TodoistProjectID)
	if err != nil {
		return fmt.Errorf("upsert todoist_sync_config: %w", err)
	}
	return nil
}

// UpdateSyncState records the outcome of a sync run: the new Todoist cursor,
// the last-synced timestamp, and the last error ("" clears a previous error).
func (r *TodoistSyncRepository) UpdateSyncState(id, syncToken, lastError string) error {
	_, err := r.db.ExecWrite(`
		UPDATE todoist_sync_config
		SET sync_token = ?, last_synced_at = CURRENT_TIMESTAMP, last_error = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, syncToken, lastError, id)
	if err != nil {
		return fmt.Errorf("update todoist sync state: %w", err)
	}
	return nil
}

// AcquireSyncLock attempts to claim the per-config run lock so a manual "Sync
// now" and the 5-minute poller cannot reconcile the same config concurrently
// (double-creating Todoist tasks, racing deletes). It is a single guarded
// UPDATE: the lock is free when sync_lock_until is NULL or already in the past
// (a crashed holder self-heals once its lease expires). On success the lock is
// leased until lockUntil. Returns true when this caller won the lock, false
// when another run already holds an unexpired lease.
func (r *TodoistSyncRepository) AcquireSyncLock(id string, now, lockUntil time.Time) (bool, error) {
	res, err := r.db.ExecWrite(`
		UPDATE todoist_sync_config
		SET sync_lock_until = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND (sync_lock_until IS NULL OR sync_lock_until < ?)
	`, lockUntil, id, now)
	if err != nil {
		return false, fmt.Errorf("acquire todoist sync lock: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("acquire todoist sync lock rows: %w", err)
	}
	return n == 1, nil
}

// ReleaseSyncLock clears the per-config run lock. Idempotent — clearing an
// already-free lock is a no-op.
func (r *TodoistSyncRepository) ReleaseSyncLock(id string) error {
	if _, err := r.db.ExecWrite(`
		UPDATE todoist_sync_config
		SET sync_lock_until = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, id); err != nil {
		return fmt.Errorf("release todoist sync lock: %w", err)
	}
	return nil
}

// ListEnabledConfigs returns all configs with sync enabled, for the poller.
func (r *TodoistSyncRepository) ListEnabledConfigs() ([]models.TodoistSyncConfig, error) {
	rows, err := r.db.Query("SELECT " + todoistSyncConfigColumns + " FROM todoist_sync_config WHERE enabled = true")
	if err != nil {
		return nil, fmt.Errorf("list enabled todoist_sync_config: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var configs []models.TodoistSyncConfig
	for rows.Next() {
		cfg, scanErr := scanTodoistSyncConfig(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan todoist_sync_config: %w", scanErr)
		}
		configs = append(configs, cfg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate todoist_sync_config: %w", err)
	}
	return configs, nil
}

// GetEnabledTodoistProviderID resolves the single enabled Todoist integration
// provider, or ErrNotFound when none is configured/enabled. Oldest-first so the
// result is stable if more than one provider row somehow exists.
func (r *TodoistSyncRepository) GetEnabledTodoistProviderID() (string, error) {
	var id string
	err := r.db.QueryRow(`
		SELECT id FROM integration_providers
		WHERE provider_type = ? AND enabled = true
		ORDER BY created_at LIMIT 1
	`, string(models.IntegrationProviderTodoist)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("resolve todoist provider: %w", err)
	}
	return id, nil
}

// GetEncryptedToken returns the encrypted OAuth access token for a (user,
// provider) connection, or ErrNotFound when the user has not connected. The
// caller decrypts it (the repository performs no crypto).
func (r *TodoistSyncRepository) GetEncryptedToken(userID, providerID string) (string, error) {
	var enc string
	err := r.db.QueryRow(`
		SELECT oauth_access_token_encrypted FROM user_integration_tokens
		WHERE user_id = ? AND integration_provider_id = ?
	`, userID, providerID).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get integration token: %w", err)
	}
	return enc, nil
}

const todoistTaskLinkColumns = "id, user_id, item_id, todoist_task_id, todoist_project_id, last_title, last_description, last_due, last_priority, last_completed, created_at, updated_at"

// GetLinkByItemID returns the task link for a personal item, or ErrNotFound.
func (r *TodoistSyncRepository) GetLinkByItemID(userID string, itemID int) (*models.TodoistTaskLink, error) {
	row := r.db.QueryRow("SELECT "+todoistTaskLinkColumns+" FROM todoist_task_links WHERE user_id = ? AND item_id = ?", userID, itemID)
	return scanTodoistTaskLinkPtr(row)
}

// GetLinkByTodoistID returns the task link for a Todoist task, or ErrNotFound.
func (r *TodoistSyncRepository) GetLinkByTodoistID(userID, todoistTaskID string) (*models.TodoistTaskLink, error) {
	row := r.db.QueryRow("SELECT "+todoistTaskLinkColumns+" FROM todoist_task_links WHERE user_id = ? AND todoist_task_id = ?", userID, todoistTaskID)
	return scanTodoistTaskLinkPtr(row)
}

// ListLinksByUser returns all task links for a user.
func (r *TodoistSyncRepository) ListLinksByUser(userID string) ([]models.TodoistTaskLink, error) {
	rows, err := r.db.Query("SELECT "+todoistTaskLinkColumns+" FROM todoist_task_links WHERE user_id = ?", userID)
	if err != nil {
		return nil, fmt.Errorf("list todoist_task_links: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var links []models.TodoistTaskLink
	for rows.Next() {
		link, scanErr := scanTodoistTaskLink(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan todoist_task_link: %w", scanErr)
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate todoist_task_links: %w", err)
	}
	return links, nil
}

// UpsertLink creates or updates a task-link mapping, refreshing the last-synced
// snapshot. Conflict resolution is on the item_id unique key.
func (r *TodoistSyncRepository) UpsertLink(link models.TodoistTaskLink) error {
	_, err := r.db.ExecWrite(`
		INSERT INTO todoist_task_links (
			id, user_id, item_id, todoist_task_id, todoist_project_id,
			last_title, last_description, last_due, last_priority, last_completed
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (item_id) DO UPDATE SET
			todoist_task_id = excluded.todoist_task_id,
			todoist_project_id = excluded.todoist_project_id,
			last_title = excluded.last_title,
			last_description = excluded.last_description,
			last_due = excluded.last_due,
			last_priority = excluded.last_priority,
			last_completed = excluded.last_completed,
			updated_at = CURRENT_TIMESTAMP
	`, link.ID, link.UserID, link.ItemID, link.TodoistTaskID, link.TodoistProjectID,
		link.LastTitle, link.LastDescription, link.LastDue, link.LastPriority, link.LastCompleted)
	if err != nil {
		return fmt.Errorf("upsert todoist_task_link: %w", err)
	}
	return nil
}

// DeleteLink removes a task-link mapping by id.
func (r *TodoistSyncRepository) DeleteLink(id string) error {
	if _, err := r.db.ExecWrite("DELETE FROM todoist_task_links WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete todoist_task_link: %w", err)
	}
	return nil
}

func scanTodoistSyncConfig(scanner interface {
	Scan(dest ...any) error
}) (models.TodoistSyncConfig, error) {
	var cfg models.TodoistSyncConfig
	var projectID, lastError sql.NullString
	var scopeMode string
	var lastSyncedAt sql.NullTime
	if err := scanner.Scan(
		&cfg.ID, &cfg.UserID, &cfg.IntegrationProviderID, &cfg.PersonalWorkspaceID,
		&cfg.Enabled, &scopeMode, &projectID, &cfg.SyncToken,
		&lastSyncedAt, &lastError, &cfg.CreatedAt, &cfg.UpdatedAt,
	); err != nil {
		return cfg, err
	}
	cfg.ScopeMode = models.TodoistSyncScopeMode(scopeMode)
	cfg.TodoistProjectID = projectID.String
	cfg.LastError = lastError.String
	if lastSyncedAt.Valid {
		cfg.LastSyncedAt = &lastSyncedAt.Time
	}
	return cfg, nil
}

func scanTodoistTaskLink(scanner interface {
	Scan(dest ...any) error
}) (models.TodoistTaskLink, error) {
	var link models.TodoistTaskLink
	var projectID, title, description, due sql.NullString
	if err := scanner.Scan(
		&link.ID, &link.UserID, &link.ItemID, &link.TodoistTaskID, &projectID,
		&title, &description, &due, &link.LastPriority, &link.LastCompleted,
		&link.CreatedAt, &link.UpdatedAt,
	); err != nil {
		return link, err
	}
	link.TodoistProjectID = projectID.String
	link.LastTitle = title.String
	link.LastDescription = description.String
	link.LastDue = due.String
	return link, nil
}

func scanTodoistTaskLinkPtr(scanner interface {
	Scan(dest ...any) error
}) (*models.TodoistTaskLink, error) {
	link, err := scanTodoistTaskLink(scanner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get todoist_task_link: %w", err)
	}
	return &link, nil
}
