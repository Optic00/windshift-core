package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"windshift/internal/database"
	"windshift/internal/models"
)

// WorkspaceAgentBindingRepository persists workspace_agent_bindings rows.
type WorkspaceAgentBindingRepository struct {
	db database.Database
}

// NewWorkspaceAgentBindingRepository constructs a new repository.
func NewWorkspaceAgentBindingRepository(db database.Database) *WorkspaceAgentBindingRepository {
	return &WorkspaceAgentBindingRepository{db: db}
}

// ErrBindingDuplicate is returned when a caller tries to create a second
// binding for the same (workspace, acting_user). The handler layer maps
// this to a 409 Conflict.
var ErrBindingDuplicate = errors.New("workspace agent binding: a binding for this acting user already exists in this workspace")

// Insert persists a new binding and returns its id. token_ttl_minutes
// defaults to 60 when caller passes <= 0; scopes default to an empty array
// (the BindingService merges with auth.DefaultAgentScopes at trigger time
// if needed).
func (r *WorkspaceAgentBindingRepository) Insert(ctx context.Context, b *models.WorkspaceAgentBinding) (int, error) {
	if b.TokenTTLMinutes <= 0 {
		b.TokenTTLMinutes = 60
	}
	scopesJSON, err := json.Marshal(b.TokenScopes)
	if err != nil {
		return 0, fmt.Errorf("marshal token scopes: %w", err)
	}
	res, err := r.db.ExecWriteContext(ctx, `
		INSERT INTO workspace_agent_bindings
			(workspace_id, acting_user_id, acting_user_kind, repo_slug, repo_remote_url, repo_base_ref,
			 llm_connection_id, scm_connection_id, token_scopes_json, token_ttl_minutes, max_runs_per_day, created_by_user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		b.WorkspaceID, b.ActingUserID, b.ActingUserKind,
		nullStringArg(b.RepoSlug), nullStringArg(b.RepoRemoteURL), nullStringArg(b.RepoBaseRef),
		nullIntArg(b.LLMConnectionID), nullIntArg(b.SCMConnectionID),
		string(scopesJSON), b.TokenTTLMinutes, b.MaxRunsPerDay,
		b.CreatedByUserID,
	)
	if err != nil {
		if database.IsUniqueConstraintError(err) {
			return 0, ErrBindingDuplicate
		}
		return 0, fmt.Errorf("insert binding: %w", err)
	}
	id, _ := res.LastInsertId()
	return int(id), nil
}

// Get loads a single binding by id.
func (r *WorkspaceAgentBindingRepository) Get(ctx context.Context, id int) (*models.WorkspaceAgentBinding, error) {
	row := r.db.QueryRowContext(ctx, bindingSelectSQL+` WHERE id = ?`, id)
	return scanBinding(row)
}

// ListForWorkspace returns every binding configured in the workspace, oldest
// first.
func (r *WorkspaceAgentBindingRepository) ListForWorkspace(ctx context.Context, workspaceID int) ([]*models.WorkspaceAgentBinding, error) {
	rows, err := r.db.QueryContext(ctx, bindingSelectSQL+` WHERE workspace_id = ? ORDER BY created_at ASC, id ASC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list bindings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*models.WorkspaceAgentBinding
	for rows.Next() {
		b, err := scanBindingRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// FindByActingUser returns the binding for a specific (workspace, acting
// user) pair, if one exists. Returns (nil, nil) when not found — the
// assignee-change trigger calls this in the hot path and absence is the
// expected case.
func (r *WorkspaceAgentBindingRepository) FindByActingUser(ctx context.Context, workspaceID, actingUserID int) (*models.WorkspaceAgentBinding, error) {
	row := r.db.QueryRowContext(ctx, bindingSelectSQL+` WHERE workspace_id = ? AND acting_user_id = ?`, workspaceID, actingUserID)
	b, err := scanBinding(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return b, err
}

// Delete removes a binding by id. Returns the number of rows affected so
// the handler can distinguish "deleted" from "no such binding".
func (r *WorkspaceAgentBindingRepository) Delete(ctx context.Context, id int) (int64, error) {
	res, err := r.db.ExecWriteContext(ctx, `DELETE FROM workspace_agent_bindings WHERE id = ?`, id)
	if err != nil {
		return 0, fmt.Errorf("delete binding: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

const bindingSelectSQL = `
	SELECT id, workspace_id, acting_user_id, acting_user_kind,
	       repo_slug, repo_remote_url, repo_base_ref,
	       llm_connection_id, scm_connection_id,
	       token_scopes_json, token_ttl_minutes, max_runs_per_day,
	       created_by_user_id, created_at, updated_at
	FROM workspace_agent_bindings
`

type bindingRowScanner interface {
	Scan(dest ...any) error
}

func scanBinding(row bindingRowScanner) (*models.WorkspaceAgentBinding, error) {
	return scanBindingFrom(row)
}

func scanBindingRows(rows *sql.Rows) (*models.WorkspaceAgentBinding, error) {
	return scanBindingFrom(rows)
}

func scanBindingFrom(scanner bindingRowScanner) (*models.WorkspaceAgentBinding, error) {
	b := &models.WorkspaceAgentBinding{}
	var repoSlug, repoRemoteURL, repoBaseRef sql.NullString
	var llmConn, scmConn sql.NullInt64
	var scopesJSON string
	if err := scanner.Scan(
		&b.ID, &b.WorkspaceID, &b.ActingUserID, &b.ActingUserKind,
		&repoSlug, &repoRemoteURL, &repoBaseRef,
		&llmConn, &scmConn, &scopesJSON, &b.TokenTTLMinutes, &b.MaxRunsPerDay,
		&b.CreatedByUserID, &b.CreatedAt, &b.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if repoSlug.Valid {
		b.RepoSlug = repoSlug.String
	}
	if repoRemoteURL.Valid {
		b.RepoRemoteURL = repoRemoteURL.String
	}
	if repoBaseRef.Valid {
		b.RepoBaseRef = repoBaseRef.String
	}
	if llmConn.Valid {
		v := int(llmConn.Int64)
		b.LLMConnectionID = &v
	}
	if scmConn.Valid {
		v := int(scmConn.Int64)
		b.SCMConnectionID = &v
	}
	if scopesJSON != "" {
		_ = json.Unmarshal([]byte(scopesJSON), &b.TokenScopes)
	}
	return b, nil
}
