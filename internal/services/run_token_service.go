package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"windshift/internal/auth"
	"windshift/internal/models"
)

// MintRequest describes a short-lived API token to issue for a coding-agent
// run. ActingUserID is the user the run runs as (the agent or centralized
// service user per the binding's identity gate, WI-87). Scopes default to
// auth.DefaultAgentScopes when nil; TTL defaults to 1 hour. Name is a free-
// form label stored on the api_tokens row for forensics (e.g.
// "agent-run:42:WI-71").
type MintRequest struct {
	ActingUserID int
	Scopes       []string
	TTL          time.Duration
	Name         string
}

// MintResult is what Mint returns: the raw bearer string (which the
// orchestrator forwards into the container as $WS_TOKEN) and its expiry.
// The token row id is returned for debug/observability — not for revoke
// because the api_tokens.expires_at column is what bounds lifetime here.
type MintResult struct {
	Token     string
	TokenID   int
	ExpiresAt time.Time
}

// RunTokenService is the thin wrapper around auth.TokenManager that
// RunService uses to mint per-run tokens. Keeping it in the services layer
// (a) gives a single chokepoint for "every per-run token must be temporary
// and scope-validated" and (b) lets the orchestrator depend on a tiny
// interface in tests.
type RunTokenService struct {
	tm *auth.TokenManager
}

// NewRunTokenService constructs a RunTokenService over an existing
// TokenManager. The TokenManager owns the DB handle + token-validation
// cache; the run service must reuse the same instance the rest of the
// process uses so cache invalidation paths line up.
func NewRunTokenService(tm *auth.TokenManager) (*RunTokenService, error) {
	if tm == nil {
		return nil, errors.New("run token service: TokenManager is required")
	}
	return &RunTokenService{tm: tm}, nil
}

// Mint issues a short-lived, IsTemporary=true API token for the given
// acting user. Scopes default to the standard agent set; TTL defaults to
// 1 hour. The token never appears in the user-facing token list (the
// IsTemporary flag is what gates that).
func (s *RunTokenService) Mint(ctx context.Context, req MintRequest) (*MintResult, error) {
	if req.ActingUserID <= 0 {
		return nil, errors.New("run token service: ActingUserID is required")
	}
	scopes := req.Scopes
	if len(scopes) == 0 {
		scopes = append(scopes, auth.DefaultAgentScopes...)
	}
	if err := auth.ValidateScopes(scopes); err != nil {
		return nil, fmt.Errorf("run token service: %w", err)
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	name := req.Name
	if name == "" {
		name = "agent-run"
	}
	expiresAt := time.Now().UTC().Add(ttl)

	resp, err := s.tm.CreateToken(req.ActingUserID, models.APITokenCreate{
		Name:        name,
		Permissions: scopes,
		ExpiresAt:   &expiresAt,
		IsTemporary: true,
	})
	if err != nil {
		return nil, fmt.Errorf("run token service: mint token: %w", err)
	}
	return &MintResult{
		Token:     resp.Token,
		TokenID:   resp.APIToken.ID,
		ExpiresAt: expiresAt,
	}, nil
}
