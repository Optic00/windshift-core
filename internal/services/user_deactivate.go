package services

import (
	"fmt"

	"windshift/internal/database"
)

// AgentDeactivationResult captures the side-effects of deactivating an owner
// so the caller can emit audit entries for each affected row.
type AgentDeactivationResult struct {
	AgentIDs         []int // owned agent user IDs that were flipped to inactive
	OwnedAgentIDs    []int // every owned agent, including agents already inactive
	RevokedAPITokens []int // api_tokens row IDs removed (owner + agents)
}

// UserDeactivationInvalidators contains the post-commit security cache hooks
// that must run after a user and their dependants have been deactivated.
// Keeping them here gives every deactivation entry point the same behavior
// without coupling this package to the concrete auth cache implementations.
type UserDeactivationInvalidators struct {
	Tokens      func(tokenIDs []int)
	Sessions    func(userID int)
	Permissions func(userID int)
}

// UserDeactivationService owns the complete user security-offboarding path.
// LDAP, SCIM, and administrative deactivation must all call this service
// instead of independently updating users or invalidating caches.
type UserDeactivationService struct {
	db           database.Database
	invalidators UserDeactivationInvalidators
}

func NewUserDeactivationService(
	db database.Database,
	invalidators UserDeactivationInvalidators,
) *UserDeactivationService {
	return &UserDeactivationService{db: db, invalidators: invalidators}
}

// DeactivateUser atomically deactivates the owner and their agents, revokes
// all API tokens belonging to either, then invalidates security caches after
// the transaction commits. Repeated calls are safe.
func (s *UserDeactivationService) DeactivateUser(ownerID int) (AgentDeactivationResult, error) {
	result, err := deactivateUserAndOwnedAgentsAndTokens(s.db, ownerID, true)
	if err != nil {
		return result, err
	}

	if s.invalidators.Tokens != nil {
		s.invalidators.Tokens(result.RevokedAPITokens)
	}
	if s.invalidators.Sessions != nil {
		s.invalidators.Sessions(ownerID)
		for _, agentID := range result.OwnedAgentIDs {
			s.invalidators.Sessions(agentID)
		}
	}
	if s.invalidators.Permissions != nil {
		s.invalidators.Permissions(ownerID)
	}

	return result, nil
}

// ActiveSystemAdminIDs returns the user IDs of every active user who holds the
// 'system.admin' global permission. Used for baking in admin notifications on
// security-relevant SCIM events (e.g. cascaded offboarding) so operators learn
// about integration impact without having to poll the audit log.
func ActiveSystemAdminIDs(db database.Database) ([]int, error) {
	rows, err := db.Query(`
		SELECT DISTINCT ugp.user_id
		FROM user_global_permissions ugp
		JOIN permissions p ON ugp.permission_id = p.id
		JOIN users u ON ugp.user_id = u.id
		WHERE p.permission_key = 'system.admin' AND u.is_active = true
	`)
	if err != nil {
		return nil, fmt.Errorf("load system admins: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int
	for rows.Next() {
		var id int
		if scanErr := rows.Scan(&id); scanErr == nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate system admins: %w", err)
	}
	return ids, nil
}

// DeactivateOwnedAgentsAndTokens propagates an owner's deactivation to their
// agents and revokes every API token held by the owner or their agents. The
// owner's own `users.is_active` row is expected to already be flipped by the
// caller; this function only handles the cascade onto dependents.
//
// Runs in a single transaction so a partial cascade cannot leak live tokens.
func DeactivateOwnedAgentsAndTokens(db database.Database, ownerID int) (AgentDeactivationResult, error) {
	return deactivateUserAndOwnedAgentsAndTokens(db, ownerID, false)
}

func deactivateUserAndOwnedAgentsAndTokens(
	db database.Database,
	ownerID int,
	deactivateOwner bool,
) (AgentDeactivationResult, error) {
	var result AgentDeactivationResult

	tx, err := db.Begin()
	if err != nil {
		return result, fmt.Errorf("failed to begin cascade transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if deactivateOwner {
		if _, err = tx.Exec(`UPDATE users SET is_active = false, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, ownerID); err != nil {
			return result, fmt.Errorf("failed to deactivate user: %w", err)
		}
	}

	// Collect every owned agent so token revocation also covers agents that
	// were already inactive. Only newly deactivated agents are returned for
	// auditing and session invalidation.
	agentRows, err := tx.Query(`SELECT id, is_active FROM users WHERE agent_owner_user_id = ?`, ownerID)
	if err != nil {
		return result, fmt.Errorf("failed to load owned agents: %w", err)
	}
	var allAgentIDs []int
	for agentRows.Next() {
		var id int
		var active bool
		if scanErr := agentRows.Scan(&id, &active); scanErr != nil {
			_ = agentRows.Close()
			return result, fmt.Errorf("failed to scan owned agent: %w", scanErr)
		}
		allAgentIDs = append(allAgentIDs, id)
		result.OwnedAgentIDs = append(result.OwnedAgentIDs, id)
		if active {
			result.AgentIDs = append(result.AgentIDs, id)
		}
	}
	if err := agentRows.Err(); err != nil {
		_ = agentRows.Close()
		return result, fmt.Errorf("failed to iterate owned agents: %w", err)
	}
	_ = agentRows.Close()

	// Flip active agents inactive.
	if len(result.AgentIDs) > 0 {
		if _, err = tx.Exec(`UPDATE users SET is_active = false, updated_at = CURRENT_TIMESTAMP WHERE agent_owner_user_id = ? AND is_active = true`, ownerID); err != nil {
			return result, fmt.Errorf("failed to deactivate owned agents: %w", err)
		}
	}

	// Collect api_tokens row IDs before we delete them (for audit).
	// api_tokens has no is_active column, so revocation is a hard DELETE.
	userIDs := append([]int{ownerID}, allAgentIDs...)
	apiTokenRows, err := tx.Query(inClauseQuery(`SELECT id FROM api_tokens WHERE user_id IN (`, len(userIDs)), toIfaceSlice(userIDs)...)
	if err != nil {
		return result, fmt.Errorf("failed to load api_tokens: %w", err)
	}
	for apiTokenRows.Next() {
		var id int
		if scanErr := apiTokenRows.Scan(&id); scanErr != nil {
			_ = apiTokenRows.Close()
			return result, fmt.Errorf("failed to scan api_token: %w", scanErr)
		}
		result.RevokedAPITokens = append(result.RevokedAPITokens, id)
	}
	if err := apiTokenRows.Err(); err != nil {
		_ = apiTokenRows.Close()
		return result, fmt.Errorf("failed to iterate api_tokens: %w", err)
	}
	_ = apiTokenRows.Close()

	if len(result.RevokedAPITokens) > 0 {
		if _, err = tx.Exec(inClauseQuery(`DELETE FROM api_tokens WHERE user_id IN (`, len(userIDs)), toIfaceSlice(userIDs)...); err != nil {
			return result, fmt.Errorf("failed to revoke api_tokens: %w", err)
		}
	}

	if err = tx.Commit(); err != nil {
		return result, fmt.Errorf("failed to commit cascade: %w", err)
	}

	return result, nil
}

// inClauseQuery appends `?,?,?) ...trailing` sized for n placeholders.
func inClauseQuery(prefix string, n int) string {
	if n == 0 {
		return prefix + ")"
	}
	out := prefix
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += "?"
	}
	return out + ")"
}

func toIfaceSlice(ids []int) []any {
	out := make([]any, len(ids))
	for i, v := range ids {
		out[i] = v
	}
	return out
}
