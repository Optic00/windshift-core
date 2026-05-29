package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"windshift/internal/database"
)

// FlagAgentsAllowCentralizedServiceUsers is the system_settings.key the
// global-admin Security Settings toggle is stored under. Centralizing the
// constant means tests, repo, handler, and service all agree on the row.
const FlagAgentsAllowCentralizedServiceUsers = "agents.allow_centralized_service_users"

// AllowlistEntry is one row of global_agent_acting_user_allowlist. A nil
// WorkspaceID means the user is allowed across every workspace; a concrete
// WorkspaceID grants the user only when binding into that workspace.
type AllowlistEntry struct {
	UserID          int
	WorkspaceID     *int
	Reason          string
	CreatedByUserID *int
	CreatedAt       string
}

// AgentSecurityRepository wraps the system_settings flag and the acting-
// identity allowlist that gate workspace admins from picking centralized
// service users as a binding's acting identity (WI-87).
type AgentSecurityRepository struct {
	db database.Database
}

// NewAgentSecurityRepository constructs a new repository.
func NewAgentSecurityRepository(db database.Database) *AgentSecurityRepository {
	return &AgentSecurityRepository{db: db}
}

// GetAllowCentralizedServiceUsers returns the current value of the master
// flag. Returns false on the row not existing (defensive: a fresh DB will
// have the seed row, but a manually wiped settings row should not crash).
func (r *AgentSecurityRepository) GetAllowCentralizedServiceUsers(ctx context.Context) (bool, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM system_settings WHERE key = ?`, FlagAgentsAllowCentralizedServiceUsers).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read security flag: %w", err)
	}
	return strings.EqualFold(strings.TrimSpace(raw), "true"), nil
}

// SetAllowCentralizedServiceUsers writes the flag. The handler layer is
// responsible for audit-logging the change (it has the actor and the
// reason); the repo just persists the value.
func (r *AgentSecurityRepository) SetAllowCentralizedServiceUsers(ctx context.Context, enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	_, err := r.db.ExecWriteContext(ctx, `
		UPDATE system_settings
		SET value = ?, updated_at = CURRENT_TIMESTAMP
		WHERE key = ?
	`, value, FlagAgentsAllowCentralizedServiceUsers)
	if err != nil {
		return fmt.Errorf("write security flag: %w", err)
	}
	return nil
}

// AddAllowlistEntries inserts a batch of (user, workspace?) grants
// atomically: either every row lands or none do. Empty workspaceIDs is
// interpreted as a single "any workspace" grant (workspace_id NULL); a
// non-empty slice creates one grant per id. The single-row variant
// stays for the few call sites (and tests) that still need it.
func (r *AgentSecurityRepository) AddAllowlistEntries(ctx context.Context, userID int, workspaceIDs []int, createdByUserID *int, reason string) error {
	if userID <= 0 {
		return errors.New("agent security: user_id is required")
	}
	return database.WithTx(r.db, func(tx database.Tx) error {
		if len(workspaceIDs) == 0 {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO global_agent_acting_user_allowlist(user_id, workspace_id, reason, created_by_user_id)
				VALUES (?, NULL, ?, ?)
			`, userID, reason, nullIntArg(createdByUserID))
			if err != nil {
				return fmt.Errorf("insert any-workspace grant: %w", err)
			}
			return nil
		}
		for _, ws := range workspaceIDs {
			if ws <= 0 {
				return fmt.Errorf("workspace id must be positive, got %d", ws)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO global_agent_acting_user_allowlist(user_id, workspace_id, reason, created_by_user_id)
				VALUES (?, ?, ?, ?)
			`, userID, ws, reason, nullIntArg(createdByUserID)); err != nil {
				return fmt.Errorf("insert grant for workspace %d: %w", ws, err)
			}
		}
		return nil
	})
}

// AddAllowlistEntry inserts a (user, workspace?) grant. The unique index
// on (user_id, COALESCE(workspace_id, 0)) means re-inserting the same pair
// returns a constraint violation; callers should treat that as "already
// allowed" rather than an error if idempotency matters.
func (r *AgentSecurityRepository) AddAllowlistEntry(ctx context.Context, userID int, workspaceID, createdByUserID *int, reason string) error {
	if userID <= 0 {
		return errors.New("agent security: user_id is required")
	}
	_, err := r.db.ExecWriteContext(ctx, `
		INSERT INTO global_agent_acting_user_allowlist(user_id, workspace_id, reason, created_by_user_id)
		VALUES (?, ?, ?, ?)
	`, userID, nullIntArg(workspaceID), reason, nullIntArg(createdByUserID))
	if err != nil {
		return fmt.Errorf("insert allowlist entry: %w", err)
	}
	return nil
}

// RemoveAllowlistEntry deletes the matching (user, workspace?) grant.
// Returns the number of rows removed so callers can distinguish "deleted"
// from "no such entry" without a separate Get.
func (r *AgentSecurityRepository) RemoveAllowlistEntry(ctx context.Context, userID int, workspaceID *int) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if workspaceID == nil {
		res, err = r.db.ExecWriteContext(ctx,
			`DELETE FROM global_agent_acting_user_allowlist WHERE user_id = ? AND workspace_id IS NULL`,
			userID,
		)
	} else {
		res, err = r.db.ExecWriteContext(ctx,
			`DELETE FROM global_agent_acting_user_allowlist WHERE user_id = ? AND workspace_id = ?`,
			userID, *workspaceID,
		)
	}
	if err != nil {
		return 0, fmt.Errorf("delete allowlist entry: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ListAllowlist returns every grant, newest first. Used by the global-
// admin Security Settings panel; expected to be small (single-digit to
// tens of rows).
func (r *AgentSecurityRepository) ListAllowlist(ctx context.Context) ([]AllowlistEntry, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT user_id, workspace_id, reason, created_by_user_id, created_at
		FROM global_agent_acting_user_allowlist
		ORDER BY created_at DESC, user_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list allowlist: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []AllowlistEntry
	for rows.Next() {
		var e AllowlistEntry
		var workspaceID sql.NullInt64
		var createdBy sql.NullInt64
		if err := rows.Scan(&e.UserID, &workspaceID, &e.Reason, &createdBy, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan allowlist entry: %w", err)
		}
		if workspaceID.Valid {
			v := int(workspaceID.Int64)
			e.WorkspaceID = &v
		}
		if createdBy.Valid {
			v := int(createdBy.Int64)
			e.CreatedByUserID = &v
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate allowlist: %w", err)
	}
	return out, nil
}

// IsAllowed answers: "is this user an allowed acting identity for runs in
// this workspace?" Matches an explicit (user_id, workspace_id) grant or a
// workspace_id IS NULL ("any-workspace") grant. Does *not* consult the
// master flag — the caller (AgentActingIdentityService) decides whether
// to even ask in the first place.
func (r *AgentSecurityRepository) IsAllowed(ctx context.Context, userID, workspaceID int) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM global_agent_acting_user_allowlist
		WHERE user_id = ?
		  AND (workspace_id IS NULL OR workspace_id = ?)
	`, userID, workspaceID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("query allowlist match: %w", err)
	}
	return n > 0, nil
}
