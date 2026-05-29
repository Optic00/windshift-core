package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"windshift/internal/database"
	"windshift/internal/repository"
)

// Acting-identity kinds. ActingIdentityKindAgent covers agents owned by
// the binding creator (allowed by default); ActingIdentityKindCentralized
// covers service users that the global admin has enabled + allowlisted.
const (
	ActingIdentityKindAgent       = "agent"
	ActingIdentityKindCentralized = "centralized_service"
)

// ActingIdentity is the chokepoint's verdict: the kind and the canonical
// name/email the orchestrator stamps onto the run env (GIT_AUTHOR_*,
// GIT_COMMITTER_*) and the run row (acting_user_id, acting_user_kind).
type ActingIdentity struct {
	UserID int
	Kind   string // ActingIdentityKindAgent or ActingIdentityKindCentralized
	Name   string
	Email  string
}

// Typed errors so callers can render the right HTTP status. The handler
// layer maps these onto 403 (rejected) vs 404 (not found) without leaking
// "this user exists but you can't use it" to non-admins.
var (
	ErrActingIdentityNotFound         = errors.New("agent acting identity: candidate user not found")
	ErrActingIdentityNotAgent         = errors.New("agent acting identity: candidate is not an agent user")
	ErrActingIdentityInactive         = errors.New("agent acting identity: candidate user is inactive")
	ErrActingIdentityNotOwned         = errors.New("agent acting identity: agent is not owned by the binding creator")
	ErrActingIdentityCentralizedGated = errors.New("agent acting identity: centralized service users are not allowed (security flag is off)")
	ErrActingIdentityNotInAllowlist   = errors.New("agent acting identity: centralized service user is not allowlisted for this workspace")
)

// AgentActingIdentityService is the single chokepoint that decides whether
// a given (bindingCreator, actingUser, workspace) triple is valid. It is
// consulted both at binding-create time (WI-88) and at run-start time
// (defense in depth — never trust the client to carry the result through).
type AgentActingIdentityService struct {
	db       database.Database
	security *repository.AgentSecurityRepository
}

// NewAgentActingIdentityService wires the service to the DB handle and the
// security repository (the chokepoint reads both the master flag and the
// allowlist).
func NewAgentActingIdentityService(db database.Database, security *repository.AgentSecurityRepository) (*AgentActingIdentityService, error) {
	if db == nil {
		return nil, errors.New("agent acting identity service: db is required")
	}
	if security == nil {
		return nil, errors.New("agent acting identity service: security repository is required")
	}
	return &AgentActingIdentityService{db: db, security: security}, nil
}

// Resolve validates a candidate acting user against the gate rules and
// returns the canonical identity payload to stamp on the run. Returns one
// of the typed errors above when the candidate is not eligible; the
// caller should surface a generic 403 to non-admins (per the design plan
// the existence of an inactive or non-allowlisted user must not leak).
func (s *AgentActingIdentityService) Resolve(ctx context.Context, bindingCreatorID, actingUserID, workspaceID int) (*ActingIdentity, error) {
	if actingUserID <= 0 {
		return nil, ErrActingIdentityNotFound
	}

	var (
		email         string
		username      string
		firstName     string
		lastName      string
		isActive      bool
		isAgent       bool
		ownerNullable sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT email, username, first_name, last_name,
		       COALESCE(is_active, 1), COALESCE(is_agent, 0),
		       agent_owner_user_id
		FROM users WHERE id = ?
	`, actingUserID).Scan(&email, &username, &firstName, &lastName, &isActive, &isAgent, &ownerNullable)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrActingIdentityNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load candidate user: %w", err)
	}
	if !isActive {
		return nil, ErrActingIdentityInactive
	}
	if !isAgent {
		return nil, ErrActingIdentityNotAgent
	}

	identity := &ActingIdentity{
		UserID: actingUserID,
		Name:   gitDisplayName(firstName, lastName, username),
		Email:  email,
	}

	if ownerNullable.Valid {
		// Owned agent: must be owned by the binding creator. Anyone else
		// asking to bind an agent owned by a third party is rejected;
		// agents inherit the owner's permissions and would otherwise
		// surface as a privilege-escalation surface.
		ownerID := int(ownerNullable.Int64)
		if ownerID != bindingCreatorID {
			return nil, ErrActingIdentityNotOwned
		}
		identity.Kind = ActingIdentityKindAgent
		return identity, nil
	}

	// Centralized service user: requires the global flag + an allowlist
	// match. Both checks run in this order so the flag-off case never
	// touches the allowlist (cheaper + a flag-off setup with stale
	// allowlist rows does not surprise anyone).
	enabled, err := s.security.GetAllowCentralizedServiceUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("read security flag: %w", err)
	}
	if !enabled {
		return nil, ErrActingIdentityCentralizedGated
	}
	allowed, err := s.security.IsAllowed(ctx, actingUserID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("check allowlist: %w", err)
	}
	if !allowed {
		return nil, ErrActingIdentityNotInAllowlist
	}
	identity.Kind = ActingIdentityKindCentralized
	return identity, nil
}

// gitDisplayName produces a `user.name`-shaped string. Prefer
// "First Last" when both are set; fall back to username so commits never
// land with an empty author.
func gitDisplayName(first, last, username string) string {
	first = strings.TrimSpace(first)
	last = strings.TrimSpace(last)
	switch {
	case first != "" && last != "":
		return first + " " + last
	case first != "":
		return first
	case last != "":
		return last
	default:
		return username
	}
}
