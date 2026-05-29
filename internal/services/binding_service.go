package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"windshift/internal/models"
	"windshift/internal/repository"
)

// SCMCredentialResolver is the surface BindingService needs from
// scm.CredentialResolver: given a workspace SCM connection id, return the
// access token + provider type + (for self-hosted) base URL. Kept as an
// interface so production wires scm.CredentialResolver while tests can
// supply a fake.
type SCMCredentialResolver interface {
	ResolveForRun(ctx context.Context, connectionID int) (token string, providerType string, baseURL string, err error)
}

// BindingService owns the workspace_agent_bindings lifecycle from the
// orchestrator's side: workspace-admin CRUD goes through Create / Delete
// (Create validates the acting identity via the WI-87 chokepoint), and
// the assignee-change trigger goes through MaybeStartRunForAssignee.
//
// Re-validating a binding's acting identity at every run start is left
// out by design: the WI-87 gate enforces at CREATE time, and flipping the
// global flag off doesn't auto-purge existing bindings. Operators who
// want stricter behavior delete the affected rows explicitly.
type BindingService struct {
	repo     *repository.WorkspaceAgentBindingRepository
	identity *AgentActingIdentityService
	runs     *RunService
	scmCreds SCMCredentialResolver
	logger   *log.Logger
}

// BindingServiceOptions wires the service. Runs is optional: when nil,
// MaybeStartRunForAssignee logs and no-ops on every call — useful for
// tests that exercise the binding CRUD path without a RunService.
type BindingServiceOptions struct {
	Repo     *repository.WorkspaceAgentBindingRepository
	Identity *AgentActingIdentityService
	Runs     *RunService
	SCMCreds SCMCredentialResolver
	Logger   *log.Logger
}

// NewBindingService constructs a BindingService. Repo + Identity are
// required; Runs may be nil to disable triggering.
func NewBindingService(opts BindingServiceOptions) (*BindingService, error) {
	if opts.Repo == nil {
		return nil, errors.New("binding service: repo is required")
	}
	if opts.Identity == nil {
		return nil, errors.New("binding service: identity service is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &BindingService{
		repo:     opts.Repo,
		identity: opts.Identity,
		runs:     opts.Runs,
		scmCreds: opts.SCMCreds,
		logger:   logger,
	}, nil
}

// CreateBindingRequest is the workspace-admin's payload, plus the
// resolved binding-creator id. The handler layer wires CreatedByUserID
// from the authenticated user; we never trust the client to set it.
type CreateBindingRequest struct {
	WorkspaceID     int
	ActingUserID    int
	RepoSlug        string
	RepoRemoteURL   string
	RepoBaseRef     string
	LLMConnectionID *int
	SCMConnectionID *int
	TokenScopes     []string
	TokenTTLMinutes int
	MaxRunsPerDay   int
	CreatedByUserID int
}

// Create validates the acting identity via the WI-87 chokepoint, then
// persists the binding with the chokepoint-resolved kind (the client's
// claim, if any, is ignored). Returns repository.ErrBindingDuplicate
// when a binding already exists for (workspace, acting_user).
func (s *BindingService) Create(ctx context.Context, req CreateBindingRequest) (*models.WorkspaceAgentBinding, error) {
	if req.WorkspaceID == 0 {
		return nil, errors.New("binding service: workspace_id is required")
	}
	if req.CreatedByUserID == 0 {
		return nil, errors.New("binding service: created_by_user_id is required")
	}
	identity, err := s.identity.Resolve(ctx, req.CreatedByUserID, req.ActingUserID, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	binding := &models.WorkspaceAgentBinding{
		WorkspaceID:     req.WorkspaceID,
		ActingUserID:    identity.UserID,
		ActingUserKind:  identity.Kind,
		RepoSlug:        req.RepoSlug,
		RepoRemoteURL:   req.RepoRemoteURL,
		RepoBaseRef:     req.RepoBaseRef,
		LLMConnectionID: req.LLMConnectionID,
		SCMConnectionID: req.SCMConnectionID,
		TokenScopes:     req.TokenScopes,
		TokenTTLMinutes: req.TokenTTLMinutes,
		MaxRunsPerDay:   req.MaxRunsPerDay,
		CreatedByUserID: req.CreatedByUserID,
	}
	id, err := s.repo.Insert(ctx, binding)
	if err != nil {
		return nil, err
	}
	binding.ID = id
	return binding, nil
}

// ListForWorkspace returns every binding configured in the workspace.
func (s *BindingService) ListForWorkspace(ctx context.Context, workspaceID int) ([]*models.WorkspaceAgentBinding, error) {
	return s.repo.ListForWorkspace(ctx, workspaceID)
}

// Delete removes a binding by id.
func (s *BindingService) Delete(ctx context.Context, id int) (int64, error) {
	return s.repo.Delete(ctx, id)
}

// MaybeStartRunForAssignee is the assignee-change trigger. Hot path: if
// the assignee did not actually change or no binding matches the new
// assignee, this is a no-op (one indexed lookup). Otherwise it builds a
// RunRequest from the binding and dispatches via RunService.Start.
//
// The signature takes *int for old/new assignee so callers don't have to
// special-case nil (item created without assignee, then assigned later).
func (s *BindingService) MaybeStartRunForAssignee(ctx context.Context, workspaceID, itemID int, oldAssignee, newAssignee *int) error {
	if newAssignee == nil {
		return nil
	}
	if oldAssignee != nil && *oldAssignee == *newAssignee {
		return nil
	}
	binding, err := s.repo.FindByActingUser(ctx, workspaceID, *newAssignee)
	if err != nil {
		return fmt.Errorf("find binding: %w", err)
	}
	if binding == nil {
		return nil
	}
	if s.runs == nil {
		s.logger.Printf("binding service: matched binding=%d for item=%d but no RunService is configured (dropping)", binding.ID, itemID)
		return nil
	}

	req := RunRequest{
		WorkspaceID: workspaceID,
		ItemID:      &itemID,
		BindingID:   binding.ID,
	}
	if binding.HasRepo() {
		remoteURL := binding.RepoRemoteURL
		// When the binding carries an SCM connection (WI-90), embed the
		// resolved OAuth access token into the remote URL so the bare
		// clone, fetches, and the agent's `git push` all authenticate
		// without needing a credential helper inside the container.
		// Works for both GitHub and Gitea because the URL form is
		// provider-agnostic.
		if binding.SCMConnectionID != nil && s.scmCreds != nil {
			token, providerType, _, err := s.scmCreds.ResolveForRun(ctx, *binding.SCMConnectionID)
			if err != nil {
				return fmt.Errorf("resolve scm credentials: %w", err)
			}
			rewritten, rewriteErr := embedTokenInRemoteURL(remoteURL, token)
			if rewriteErr != nil {
				return fmt.Errorf("embed token: %w", rewriteErr)
			}
			remoteURL = rewritten
			s.logger.Printf("binding service: embedded %s token in remote for binding=%d", providerType, binding.ID)
		}
		req.Repo = &RepoSpec{
			WorkspaceID: workspaceID,
			RepoSlug:    binding.RepoSlug,
			RemoteURL:   remoteURL,
			BaseRef:     binding.RepoBaseRef,
		}
	}
	// Mint a per-run ws token only if the RunService has a token service
	// configured; otherwise the binding still fires but the container
	// runs without WS_TOKEN. Phase 6 (WI-89) wires the full env path.
	if binding.ActingUserID > 0 && s.runs.HasTokens() {
		req.Token = &TokenSpec{
			ActingUserID: binding.ActingUserID,
			Scopes:       binding.TokenScopes,
			TTL:          time.Duration(binding.TokenTTLMinutes) * time.Minute,
			Name:         fmt.Sprintf("agent-run:item-%d:binding-%d", itemID, binding.ID),
		}
	}

	runID, err := s.runs.Start(ctx, req)
	if err != nil {
		return fmt.Errorf("start run: %w", err)
	}
	s.logger.Printf("binding service: started run=%d for item=%d binding=%d acting_user=%d", runID, itemID, binding.ID, binding.ActingUserID)
	return nil
}

// embedTokenInRemoteURL rewrites an HTTPS git remote into the OAuth-
// authenticated form. The "oauth2" username is the GitHub convention for
// PAT/access tokens; Gitea also accepts it (or "x-access-token") since
// both clone HTTPS handles any username when paired with a valid token
// in the password slot. SSH URLs are returned unchanged — the
// orchestrator only knows how to authenticate against HTTPS remotes.
func embedTokenInRemoteURL(remote, token string) (string, error) {
	if token == "" {
		return remote, nil
	}
	if !strings.HasPrefix(remote, "https://") && !strings.HasPrefix(remote, "http://") {
		return remote, nil
	}
	u, err := url.Parse(remote)
	if err != nil {
		return "", err
	}
	u.User = url.UserPassword("oauth2", token)
	return u.String(), nil
}
