package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"windshift/internal/database"
	"windshift/internal/models"
)

// ErrSkillDuplicateName is returned when a skill with the same name already
// exists in the workspace. The handler maps it to 409.
var ErrSkillDuplicateName = errors.New("workspace agent skill: a skill with this name already exists in this workspace")

// WorkspaceAgentSkillRepository persists the per-workspace agent-skills
// library (WI-258) and the binding↔skill attachments.
type WorkspaceAgentSkillRepository struct {
	db database.Database
}

// NewWorkspaceAgentSkillRepository constructs a new repository.
func NewWorkspaceAgentSkillRepository(db database.Database) *WorkspaceAgentSkillRepository {
	return &WorkspaceAgentSkillRepository{db: db}
}

const skillSelectSQL = `
	SELECT id, workspace_id, name, description, body, enabled,
	       created_by_user_id, created_at, updated_at
	FROM workspace_agent_skills
`

func scanSkill(scanner interface{ Scan(...any) error }) (*models.WorkspaceAgentSkill, error) {
	s := &models.WorkspaceAgentSkill{}
	var createdBy sql.NullInt64
	if err := scanner.Scan(
		&s.ID, &s.WorkspaceID, &s.Name, &s.Description, &s.Body, &s.Enabled,
		&createdBy, &s.CreatedAt, &s.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if createdBy.Valid {
		v := int(createdBy.Int64)
		s.CreatedByUserID = &v
	}
	return s, nil
}

// Insert persists a new skill and returns its id.
func (r *WorkspaceAgentSkillRepository) Insert(ctx context.Context, s *models.WorkspaceAgentSkill) (int, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO workspace_agent_skills (workspace_id, name, description, body, enabled, created_by_user_id)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id
	`, s.WorkspaceID, s.Name, s.Description, s.Body, s.Enabled, nullIntArg(s.CreatedByUserID)).Scan(&id)
	if err != nil {
		if database.IsUniqueConstraintError(err) {
			return 0, ErrSkillDuplicateName
		}
		return 0, fmt.Errorf("insert skill: %w", err)
	}
	return int(id), nil
}

// Update rewrites a skill's editable fields, scoped by workspace so a
// workspace admin cannot reach another workspace's skill by guessing ids.
// Returns rows affected (0 = not found / wrong workspace).
func (r *WorkspaceAgentSkillRepository) Update(ctx context.Context, s *models.WorkspaceAgentSkill) (int64, error) {
	res, err := r.db.ExecWriteContext(ctx, `
		UPDATE workspace_agent_skills
		SET name = ?, description = ?, body = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND workspace_id = ?
	`, s.Name, s.Description, s.Body, s.Enabled, s.ID, s.WorkspaceID)
	if err != nil {
		if database.IsUniqueConstraintError(err) {
			return 0, ErrSkillDuplicateName
		}
		return 0, fmt.Errorf("update skill: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Delete removes a skill by (id, workspace). Attachments cascade.
func (r *WorkspaceAgentSkillRepository) Delete(ctx context.Context, id, workspaceID int) (int64, error) {
	res, err := r.db.ExecWriteContext(ctx, `
		DELETE FROM workspace_agent_skills WHERE id = ? AND workspace_id = ?
	`, id, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("delete skill: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Get loads a single skill by (id, workspace). Returns ErrNotFound when absent,
// so callers do not have to reach into database/sql to detect a miss.
func (r *WorkspaceAgentSkillRepository) Get(ctx context.Context, id, workspaceID int) (*models.WorkspaceAgentSkill, error) {
	row := r.db.QueryRowContext(ctx, skillSelectSQL+` WHERE id = ? AND workspace_id = ?`, id, workspaceID)
	skill, err := scanSkill(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get skill: %w", err)
	}
	return skill, nil
}

// ListForWorkspace returns the workspace's skill library, name order.
func (r *WorkspaceAgentSkillRepository) ListForWorkspace(ctx context.Context, workspaceID int) ([]*models.WorkspaceAgentSkill, error) {
	rows, err := r.db.QueryContext(ctx, skillSelectSQL+` WHERE workspace_id = ? ORDER BY name ASC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*models.WorkspaceAgentSkill
	for rows.Next() {
		s, err := scanSkill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListEnabledForBinding returns the enabled skills attached to a binding,
// name order — what the run prompt indexes and `ws skill ls` reports.
func (r *WorkspaceAgentSkillRepository) ListEnabledForBinding(ctx context.Context, bindingID int) ([]*models.WorkspaceAgentSkill, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT s.id, s.workspace_id, s.name, s.description, s.body, s.enabled,
		       s.created_by_user_id, s.created_at, s.updated_at
		FROM workspace_agent_skills s
		JOIN workspace_agent_binding_skills bs ON bs.skill_id = s.id
		WHERE bs.binding_id = ? AND s.enabled = true
		ORDER BY s.name ASC
	`, bindingID)
	if err != nil {
		return nil, fmt.Errorf("list skills for binding: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*models.WorkspaceAgentSkill
	for rows.Next() {
		s, err := scanSkill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SkillIDsForBinding returns the attached skill ids (enabled or not), for
// rendering the binding's configuration.
func (r *WorkspaceAgentSkillRepository) SkillIDsForBinding(ctx context.Context, bindingID int) ([]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT skill_id FROM workspace_agent_binding_skills WHERE binding_id = ? ORDER BY skill_id ASC
	`, bindingID)
	if err != nil {
		return nil, fmt.Errorf("list binding skill ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ReplaceBindingSkills sets the binding's attachments to exactly skillIDs.
// Every id must belong to workspaceID — ids from other workspaces are
// rejected, so an admin cannot attach a foreign workspace's skill by
// guessing ids.
func (r *WorkspaceAgentSkillRepository) ReplaceBindingSkills(ctx context.Context, bindingID, workspaceID int, skillIDs []int) error {
	if len(skillIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(skillIDs)), ",")
		args := make([]any, 0, len(skillIDs)+1)
		args = append(args, workspaceID)
		for _, id := range skillIDs {
			args = append(args, id)
		}
		var n int
		//nolint:gosec // G201: placeholders is built from a fixed "?," pattern, never user input
		if err := r.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM workspace_agent_skills WHERE workspace_id = ? AND id IN (`+placeholders+`)`,
			args...).Scan(&n); err != nil {
			return fmt.Errorf("validate skill ids: %w", err)
		}
		if n != len(uniqueInts(skillIDs)) {
			return fmt.Errorf("workspace agent skill: one or more skill ids do not exist in this workspace")
		}
	}
	if _, err := r.db.ExecWriteContext(ctx, `DELETE FROM workspace_agent_binding_skills WHERE binding_id = ?`, bindingID); err != nil {
		return fmt.Errorf("clear binding skills: %w", err)
	}
	for _, id := range uniqueInts(skillIDs) {
		if _, err := r.db.ExecWriteContext(ctx, `
			INSERT INTO workspace_agent_binding_skills (binding_id, skill_id) VALUES (?, ?)
		`, bindingID, id); err != nil {
			return fmt.Errorf("attach skill %d: %w", id, err)
		}
	}
	return nil
}

func uniqueInts(in []int) []int {
	seen := make(map[int]bool, len(in))
	out := make([]int, 0, len(in))
	for _, v := range in {
		if v > 0 && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
