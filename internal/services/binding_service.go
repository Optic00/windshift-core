package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"time"

	"windshift/internal/auth"
	"windshift/internal/llm"
	"windshift/internal/models"
	"windshift/internal/repository"
)

// ErrBindingRepoNeedsSCMConnection is returned when a binding sets a
// RepoSlug but no SCMConnectionID. Bindings can no longer carry a
// free-form remote URL (a workspace admin could otherwise point runs
// at arbitrary hosts via SSRF or git remote helpers); the clone URL
// is always derived server-side from a trusted SCM connection record.
var ErrBindingRepoNeedsSCMConnection = errors.New("binding service: repo_slug requires scm_connection_id; the clone URL is derived from the trusted SCM connection")

// ErrBindingInvalidRepoSlug is returned when a binding's RepoSlug is
// not of the canonical owner/repo shape (no path traversal, no
// schemes, no leading slashes).
var ErrBindingInvalidRepoSlug = errors.New("binding service: repo_slug must be owner/repo, alphanumerics + . _ - only")

// validRepoSlug is the canonical owner/repo shape. Two segments
// separated by a single /. No "..", no leading slashes, no schemes —
// the regex on its own rejects all of those because none of the
// allowed characters can produce them.
var validRepoSlug = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// ErrBindingTokenTTLOverCap is returned when a binding is created with a
// TokenTTLMinutes value above the per-run-token ceiling (see
// MaxAgentTokenTTL). Surfaced as 400 by the handler so the admin sees
// the bad config at create time rather than getting silently clamped at
// every run start.
var ErrBindingTokenTTLOverCap = errors.New("binding service: token_ttl_minutes exceeds the agent-token ceiling")

// ErrBindingBudgetExceeded is returned (and swallowed at log level) by
// MaybeStartRunForAssignee when a binding has already started its
// configured max_runs_per_day for the current UTC day. The binding
// remains valid; new runs simply wait until the rolling 24h window
// reopens.
var ErrBindingBudgetExceeded = errors.New("binding service: max_runs_per_day budget exceeded for today")

// SCMCredentialResolver is the surface BindingService needs from
// scm.CredentialResolver: given a workspace SCM connection id, return the
// access token + provider type + (for self-hosted) base URL. Kept as an
// interface so production wires scm.CredentialResolver while tests can
// supply a fake.
type SCMCredentialResolver interface {
	ResolveForRun(ctx context.Context, connectionID int) (token string, providerType string, baseURL string, err error)
}

// LLMCapabilityResolver reports which LLM connections a workspace is
// allowed to bind to: the connection ids exposed to it as enabled
// "llm_connection" action capabilities. Create validates a chosen
// llm_connection_id against this list so the limit holds even when a
// caller bypasses the UI and POSTs directly. Kept as an interface so
// production wires the action repository while tests supply a fake.
type LLMCapabilityResolver interface {
	WorkspaceLLMConnectionIDs(ctx context.Context, workspaceID int) ([]int, error)
}

// LLMRuntimeResolver returns the provider runtime config for a connection that
// Create has already validated against the workspace's exposed capabilities.
type LLMRuntimeResolver interface {
	ConnectionRuntime(ctx context.Context, connectionID int) (*llm.ConnectionRuntimeConfig, error)
}

// AgentRunContextResolver returns workspace/item identifiers the runner needs
// to render ws.toml and tell the agent which work item it owns.
type AgentRunContextResolver interface {
	AgentRunContext(ctx context.Context, workspaceID, itemID int) (repository.AgentRunContext, error)
}

// ErrLLMConnectionNotExposed is returned by Create when the chosen
// llm_connection_id is not exposed to the workspace as an action
// capability. The handler maps it to a 400.
var ErrLLMConnectionNotExposed = errors.New("binding service: llm connection is not available to this workspace")

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
	repo       *repository.WorkspaceAgentBindingRepository
	identity   *AgentActingIdentityService
	runs       *RunService
	scmCreds   SCMCredentialResolver
	llmCaps    LLMCapabilityResolver
	llmRuntime LLMRuntimeResolver
	runContext AgentRunContextResolver
	apiURL     string
	logger     *log.Logger
}

// BindingServiceOptions wires the service. Runs is optional: when nil,
// MaybeStartRunForAssignee logs and no-ops on every call — useful for
// tests that exercise the binding CRUD path without a RunService.
type BindingServiceOptions struct {
	Repo       *repository.WorkspaceAgentBindingRepository
	Identity   *AgentActingIdentityService
	Runs       *RunService
	SCMCreds   SCMCredentialResolver
	LLMCaps    LLMCapabilityResolver
	LLMRuntime LLMRuntimeResolver
	RunContext AgentRunContextResolver
	APIURL     string
	Logger     *log.Logger
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
		repo:       opts.Repo,
		identity:   opts.Identity,
		runs:       opts.Runs,
		scmCreds:   opts.SCMCreds,
		llmCaps:    opts.LLMCaps,
		llmRuntime: opts.LLMRuntime,
		runContext: opts.RunContext,
		apiURL:     opts.APIURL,
		logger:     logger,
	}, nil
}

// CreateBindingRequest is the workspace-admin's payload, plus the
// resolved binding-creator id. The handler layer wires CreatedByUserID
// from the authenticated user; we never trust the client to set it.
//
// RepoRemoteURL is intentionally absent: a workspace admin must not
// be able to make the orchestrator clone arbitrary URLs. A binding
// that wants per-run worktree preparation must reference an
// SCMConnectionID; the clone URL is derived from the connection's
// provider host and the binding's RepoSlug.
type CreateBindingRequest struct {
	WorkspaceID     int
	ActingUserID    int
	RepoSlug        string
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
//
// Scopes and TTL are validated up front so a workspace admin gets a
// 400 at create time instead of having their config silently clamped
// (TTL) or runs failing at mint time (scopes).
func (s *BindingService) Create(ctx context.Context, req CreateBindingRequest) (*models.WorkspaceAgentBinding, error) {
	if req.WorkspaceID == 0 {
		return nil, errors.New("binding service: workspace_id is required")
	}
	if req.CreatedByUserID == 0 {
		return nil, errors.New("binding service: created_by_user_id is required")
	}
	if len(req.TokenScopes) > 0 {
		if err := auth.ValidateAgentScopes(req.TokenScopes); err != nil {
			return nil, fmt.Errorf("binding service: %w", err)
		}
	}
	if req.TokenTTLMinutes > 0 {
		if time.Duration(req.TokenTTLMinutes)*time.Minute > MaxAgentTokenTTL {
			return nil, ErrBindingTokenTTLOverCap
		}
	}
	if req.RepoSlug != "" {
		if !validRepoSlug.MatchString(req.RepoSlug) {
			return nil, ErrBindingInvalidRepoSlug
		}
		if req.SCMConnectionID == nil {
			return nil, ErrBindingRepoNeedsSCMConnection
		}
	}
	identity, err := s.identity.Resolve(ctx, req.CreatedByUserID, req.ActingUserID, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	// An LLM, when chosen, must be one the workspace was granted via an
	// "llm_connection" action capability. The UI already limits the
	// picker to these; re-check here so a direct API call can't bind to a
	// connection the workspace was never exposed to.
	if req.LLMConnectionID != nil && s.llmCaps != nil {
		allowed, err := s.llmCaps.WorkspaceLLMConnectionIDs(ctx, req.WorkspaceID)
		if err != nil {
			return nil, fmt.Errorf("resolve llm capabilities: %w", err)
		}
		if !containsInt(allowed, *req.LLMConnectionID) {
			return nil, ErrLLMConnectionNotExposed
		}
	}
	binding := &models.WorkspaceAgentBinding{
		WorkspaceID:     req.WorkspaceID,
		ActingUserID:    identity.UserID,
		ActingUserKind:  identity.Kind,
		RepoSlug:        req.RepoSlug,
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

// Delete removes a binding by (id, workspaceID). The workspace scope is
// required so an admin of workspace A cannot delete a binding that lives
// in workspace B by guessing its id.
func (s *BindingService) Delete(ctx context.Context, id, workspaceID int) (int64, error) {
	return s.repo.Delete(ctx, id, workspaceID)
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

	if binding.MaxRunsPerDay > 0 {
		// Use a rolling 24h window rather than calendar day: simpler to
		// reason about, no time-zone surprises, and aligns with how the
		// per-binding budget is typically meant ("at most N in any 24h
		// stretch"). 0 means unlimited.
		since := time.Now().UTC().Add(-24 * time.Hour)
		count, err := s.runs.CountRunsForBindingSince(ctx, binding.ID, since)
		if err != nil {
			return fmt.Errorf("count recent runs: %w", err)
		}
		if count >= binding.MaxRunsPerDay {
			s.logger.Printf("binding service: budget exceeded for binding=%d (max=%d, recent=%d) — dropping item=%d", binding.ID, binding.MaxRunsPerDay, count, itemID)
			return ErrBindingBudgetExceeded
		}
	}

	// Remote pool binding: persist a queued run for the pool and stop. The
	// per-run token, grants, and runner env are derived at claim time by the
	// remote claim path (RunService.PrepareRemoteClaim → ResolveRunInputs);
	// none of the local worktree/clone-URL/secret resolution below applies,
	// since a remote runner reaches git/llm/secrets through the brokers, not
	// host-side credentials (WI-195).
	if binding.TargetPoolID != nil {
		runID, err := s.runs.Start(ctx, RunRequest{
			WorkspaceID:  workspaceID,
			ItemID:       &itemID,
			BindingID:    binding.ID,
			TargetPoolID: binding.TargetPoolID,
			JobKind:      models.JobKindCodingAgent,
		})
		if err != nil {
			return fmt.Errorf("start remote run: %w", err)
		}
		s.logger.Printf("binding service: queued remote run=%d for item=%d binding=%d pool=%d", runID, itemID, binding.ID, *binding.TargetPoolID)
		return nil
	}

	env, err := s.buildRunEnv(ctx, workspaceID, itemID)
	if err != nil {
		return err
	}
	req := RunRequest{
		WorkspaceID: workspaceID,
		ItemID:      &itemID,
		BindingID:   binding.ID,
		Env:         env,
	}
	if binding.HasRepo() {
		// HasRepo guarantees SCMConnectionID is set; this is the only
		// path that resolves a clone URL. The orchestrator derives the
		// URL from the trusted SCM connection record + the binding's
		// slug — the binding cannot carry a free-form URL.
		if s.scmCreds == nil {
			s.logger.Printf("binding service: binding=%d wants repo prep but no SCMCredentialResolver is configured (dropping)", binding.ID)
			return nil
		}
		token, providerType, baseURL, err := s.scmCreds.ResolveForRun(ctx, *binding.SCMConnectionID)
		if err != nil {
			return fmt.Errorf("resolve scm credentials: %w", err)
		}
		cloneURL, derr := deriveCloneURL(providerType, baseURL, binding.RepoSlug)
		if derr != nil {
			return fmt.Errorf("derive clone url: %w", derr)
		}
		s.logger.Printf("binding service: derived %s clone url for binding=%d slug=%s", providerType, binding.ID, binding.RepoSlug)
		// Token travels on RepoSpec as a separate field — never embed
		// it in RemoteURL. WorktreeManager injects it via a per-clone
		// GIT_ASKPASS helper so it never appears in argv or .git/config.
		req.Repo = &RepoSpec{
			WorkspaceID: workspaceID,
			RepoSlug:    binding.RepoSlug,
			RemoteURL:   cloneURL,
			BaseRef:     binding.RepoBaseRef,
			Token:       token,
		}
		// Forward the same token to the sandbox through the env-file path so
		// the agent can push its run branch. The entrypoint turns it into a
		// per-container GIT_ASKPASS helper; it never appears in docker argv or
		// in .git/config.
		req.Env["AGENT_GIT_TOKEN"] = token
		req.Env["GIT_TERMINAL_PROMPT"] = "0"
	}
	if binding.LLMConnectionID != nil && s.llmRuntime != nil {
		llmCfg, err := s.llmRuntime.ConnectionRuntime(ctx, *binding.LLMConnectionID)
		if err != nil {
			return fmt.Errorf("resolve llm runtime: %w", err)
		}
		applyLLMRuntimeEnv(req.Env, llmCfg)
	}
	// Mint a per-run ws token + snapshot the run's access-layer grants
	// (WI-144). Shared with the remote claim path via bindingTokenAndGrants so
	// both transports derive identical inputs (WI-195). The git ref is filled
	// at claim from the prepared worktree branch.
	req.Token, req.Grants = s.bindingTokenAndGrants(binding, itemID)

	runID, err := s.runs.Start(ctx, req)
	if err != nil {
		return fmt.Errorf("start run: %w", err)
	}
	s.logger.Printf("binding service: started run=%d for item=%d binding=%d acting_user=%d", runID, itemID, binding.ID, binding.ActingUserID)
	return nil
}

// bindingTokenAndGrants derives the per-run token spec and access-layer
// grants from a binding, shared by the local Start path and the remote claim
// enrichment (WI-195). Returns (nil, nil) when the binding mints no token (no
// acting user, or no token service configured) — grants are meaningful only
// when bound to a token. The git grant's Ref is left empty here; the claim
// path fills it (the worktree branch locally, the run-branch namespace
// remotely).
func (s *BindingService) bindingTokenAndGrants(b *models.WorkspaceAgentBinding, itemID int) (*TokenSpec, *models.RunGrants) {
	if b.ActingUserID <= 0 || !s.runs.HasTokens() {
		return nil, nil
	}
	spec := &TokenSpec{
		ActingUserID: b.ActingUserID,
		Scopes:       b.TokenScopes,
		TTL:          time.Duration(b.TokenTTLMinutes) * time.Minute,
		Name:         fmt.Sprintf("agent-run:item-%d:binding-%d", itemID, b.ID),
	}
	grants := &models.RunGrants{}
	if b.HasRepo() {
		grants.Git = &models.GitGrant{Repo: b.RepoSlug, ConnectionID: *b.SCMConnectionID}
	}
	if b.LLMConnectionID != nil {
		grants.LLM = &models.LLMGrant{ConnectionID: *b.LLMConnectionID}
	}
	if grants.Git == nil && grants.LLM == nil {
		return spec, nil
	}
	return spec, grants
}

// ResolveRunInputs implements RunService.BindingInputsResolver: it derives a
// binding-backed run's token spec, access grants, and runner context env at
// remote claim time, mirroring the local Start derivation. Secrets are NOT
// injected into env — a remote runner reaches git/llm/secrets through the
// brokers using its per-run token (WI-195). Returns (nil, nil, nil, nil) for
// a run with no binding (e.g. action_container).
func (s *BindingService) ResolveRunInputs(ctx context.Context, run *models.AgentRun) (*TokenSpec, *models.RunGrants, map[string]string, error) {
	if run == nil || run.BindingID == nil {
		return nil, nil, nil, nil
	}
	binding, err := s.repo.Get(ctx, *run.BindingID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve run inputs: load binding %d: %w", *run.BindingID, err)
	}
	itemID := 0
	if run.ItemID != nil {
		itemID = *run.ItemID
	}
	env, err := s.buildRunEnv(ctx, run.WorkspaceID, itemID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve run inputs: build env: %w", err)
	}
	spec, grants := s.bindingTokenAndGrants(binding, itemID)
	return spec, grants, env, nil
}

func (s *BindingService) buildRunEnv(ctx context.Context, workspaceID, itemID int) (map[string]string, error) {
	env := map[string]string{
		"WS_WORKSPACE_ID":      fmt.Sprintf("%d", workspaceID),
		"WINDSHIFT_ITEM_DB_ID": fmt.Sprintf("%d", itemID),
	}
	if s.apiURL != "" {
		env["WS_API_URL"] = s.apiURL
	}
	if s.runContext == nil {
		env["WINDSHIFT_ITEM_ID"] = fmt.Sprintf("%d", itemID)
		return env, nil
	}
	runCtx, err := s.runContext.AgentRunContext(ctx, workspaceID, itemID)
	if err != nil {
		return nil, err
	}
	if runCtx.WorkspaceKey != "" {
		env["WS_WORKSPACE_KEY"] = runCtx.WorkspaceKey
	}
	if runCtx.ItemNumber > 0 {
		env["WS_ITEM_NUMBER"] = fmt.Sprintf("%d", runCtx.ItemNumber)
	}
	if runCtx.ItemKey != "" {
		env["WINDSHIFT_ITEM_ID"] = runCtx.ItemKey
		env["WINDSHIFT_ITEM_KEY"] = runCtx.ItemKey
	} else {
		env["WINDSHIFT_ITEM_ID"] = fmt.Sprintf("%d", itemID)
	}
	return env, nil
}

func applyLLMRuntimeEnv(env map[string]string, cfg *llm.ConnectionRuntimeConfig) {
	if cfg == nil {
		return
	}
	env["LLM_PROVIDER"] = cfg.ProviderType
	env["LLM_PROVIDER_TYPE"] = cfg.ProviderType
	env["LLM_API_FORMAT"] = cfg.APIFormat
	env["LLM_MODEL"] = cfg.Model
	if cfg.APIKey != "" {
		env["LLM_API_KEY"] = cfg.APIKey
	}
	if cfg.BaseURL != "" {
		env["LLM_BASE_URL"] = cfg.BaseURL
	}
}

// containsInt reports whether xs contains v.
func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// deriveCloneURL constructs an https git remote from the trusted SCM
// connection record and the binding's slug. GitHub bindings use
// github.com unless the connection's baseURL declares a GitHub
// Enterprise host. Gitea bindings always derive the host from the
// connection's baseURL.
//
// The returned URL has no embedded credentials; auth is layered on
// later via a per-clone GIT_ASKPASS helper so the token never appears
// in argv or .git/config (WI-137).
func deriveCloneURL(providerType, baseURL, slug string) (string, error) {
	if !validRepoSlug.MatchString(slug) {
		return "", ErrBindingInvalidRepoSlug
	}
	host := ""
	switch providerType {
	case "github":
		host = "github.com"
		if baseURL != "" {
			h, err := hostFromURL(baseURL)
			if err != nil {
				return "", fmt.Errorf("github base url: %w", err)
			}
			host = h
		}
	case "gitea":
		if baseURL == "" {
			return "", errors.New("gitea connection is missing base_url")
		}
		h, err := hostFromURL(baseURL)
		if err != nil {
			return "", fmt.Errorf("gitea base url: %w", err)
		}
		host = h
	default:
		return "", fmt.Errorf("unsupported scm provider type %q", providerType)
	}
	return "https://" + host + "/" + slug + ".git", nil
}

// hostFromURL parses a base URL ("https://gitea.example.com/" or
// "https://github.example-corp.com") and returns just the host. The
// scheme is dropped; the resulting clone URL is always https.
func hostFromURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("base url %q has no host", raw)
	}
	return u.Host, nil
}
