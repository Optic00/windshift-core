package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
// All user-table reads are delegated to UserReadService so the rules
// about how an "agent user" or "centralized service user" is identified
// live in one place; this service only composes those lookups with the
// security-flag and allowlist gates.
type AgentActingIdentityService struct {
	users    *UserReadService
	security *repository.AgentSecurityRepository
}

// NewAgentActingIdentityService wires the service to the user-read
// service and the security repository.
func NewAgentActingIdentityService(users *UserReadService, security *repository.AgentSecurityRepository) (*AgentActingIdentityService, error) {
	if users == nil {
		return nil, errors.New("agent acting identity service: user-read service is required")
	}
	if security == nil {
		return nil, errors.New("agent acting identity service: security repository is required")
	}
	return &AgentActingIdentityService{users: users, security: security}, nil
}

// IsAgentUser reports whether the user exists, is active, and is an agent.
// Triggers use it to decide whether a "no binding matched" outcome is worth
// logging (an agent assignee with no binding is a silent misconfiguration;
// a human assignee is just a normal assignment). It deliberately skips the
// ownership/allowlist gates Resolve enforces — this is observability, not
// authorization.
func (s *AgentActingIdentityService) IsAgentUser(userID int) bool {
	if userID <= 0 {
		return false
	}
	u, err := s.users.GetByID(userID)
	return err == nil && u.IsActive && u.IsAgent
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

	u, err := s.users.GetByID(actingUserID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrActingIdentityNotFound
		}
		return nil, fmt.Errorf("load candidate user: %w", err)
	}
	if !u.IsActive {
		return nil, ErrActingIdentityInactive
	}
	if !u.IsAgent {
		return nil, ErrActingIdentityNotAgent
	}

	identity := &ActingIdentity{
		UserID: actingUserID,
		Name:   gitDisplayName(u.FirstName, u.LastName, u.Username),
		Email:  u.Email,
	}

	if u.AgentOwnerUserID != nil {
		// Owned agent: must be owned by the binding creator. Anyone else
		// asking to bind an agent owned by a third party is rejected;
		// agents inherit the owner's permissions and would otherwise
		// surface as a privilege-escalation surface.
		if *u.AgentOwnerUserID != bindingCreatorID {
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

// CandidateActingIdentity is what ListCandidatesForBinding returns — one
// per acting identity the workspace admin is allowed to pick. The kind
// determines whether it's an agent they own or a centralized service
// user reachable only because the WI-87 master flag + allowlist let it
// through.
type CandidateActingIdentity struct {
	UserID    int    `json:"user_id"`
	Kind      string `json:"kind"` // ActingIdentityKindAgent or ActingIdentityKindCentralized
	Username  string `json:"username"`
	Email     string `json:"email"`
	Name      string `json:"name"` // git-style display name
	OwnerID   *int   `json:"owner_id,omitempty"`
	OwnerName string `json:"owner_name,omitempty"`
}

// ListCandidatesForBinding returns the acting identities the given
// workspace admin may pick when creating a binding in this workspace:
// centralized service users (is_agent + agent_owner_user_id NULL +
// active) that the WI-87 allowlist reaches for this workspace. These
// only surface when the master flag is on, matching what Resolve()
// would accept at create time.
//
// Agents an admin personally owns are deliberately NOT offered — a
// binding's acting identity is a workspace-shared, admin-provisioned
// global service user, never a personal agent. (Resolve() still accepts
// owned agents for backwards compatibility with bindings created before
// this rule, so existing runs don't break; the picker simply won't
// surface them as new options.)
//
// The handler layer surfaces this to the picker so admins can't even
// see options the server is going to refuse.
func (s *AgentActingIdentityService) ListCandidatesForBinding(ctx context.Context, bindingCreatorID, workspaceID int) ([]CandidateActingIdentity, error) {
	if bindingCreatorID <= 0 || workspaceID <= 0 {
		return nil, errors.New("agent acting identity service: bindingCreatorID and workspaceID are required")
	}

	// Centralized service users only surface when the master flag is on
	// — Resolve() would reject them otherwise.
	flagEnabled, err := s.security.GetAllowCentralizedServiceUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("read security flag: %w", err)
	}
	if !flagEnabled {
		return []CandidateActingIdentity{}, nil
	}
	centralized, err := s.users.ListAllowlistedCentralizedServiceUsers(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]CandidateActingIdentity, 0, len(centralized))
	for i := range centralized {
		u := centralized[i]
		out = append(out, CandidateActingIdentity{
			UserID:   u.ID,
			Kind:     ActingIdentityKindCentralized,
			Username: u.Username,
			Email:    u.Email,
			Name:     gitDisplayName(u.FirstName, u.LastName, u.Username),
		})
	}
	return out, nil
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
