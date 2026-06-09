package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
	"time"

	"windshift/internal/auth"
	"windshift/internal/llm"
	"windshift/internal/models"
	"windshift/internal/repoprep"
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

// LLMRuntimeResolver returns the provider runtime config for a connection and
// runs one-shot test prompts against it. Create uses ConnectionRuntime to
// validate a chosen llm_connection_id (it only resolves enabled connections),
// the run path uses it to derive the agent's model, and TestLLM uses
// PromptConnection to round-trip a prompt through the provider. LLM
// connections are global, not workspace-scoped: any enabled connection may
// back any workspace's binding.
type LLMRuntimeResolver interface {
	ConnectionRuntime(ctx context.Context, connectionID int) (*llm.ConnectionRuntimeConfig, error)
	PromptConnection(ctx context.Context, connectionID int, prompt string) (string, error)
}

// AgentRunContextResolver returns workspace/item identifiers the runner needs
// to render ws.toml and tell the agent which work item it owns.
type AgentRunContextResolver interface {
	AgentRunContext(ctx context.Context, workspaceID, itemID int) (repository.AgentRunContext, error)
}

// ErrLLMConnectionRequired is returned by Create when no llm_connection_id is
// supplied. A binding with no LLM can't run an agent (the llm-proxy 403s a run
// with no LLM grant), so the connection is mandatory. The handler maps it to a
// 400.
var ErrLLMConnectionRequired = errors.New("binding service: an llm connection is required")

// ErrLLMConnectionInvalid is returned by Create when the chosen
// llm_connection_id does not resolve to an enabled connection (missing or
// disabled). The handler maps it to a 400.
var ErrLLMConnectionInvalid = errors.New("binding service: llm connection not found or disabled")

// ErrBindingNotFound is returned when a binding id doesn't exist in the given
// workspace. The handler maps it to a 404.
var ErrBindingNotFound = errors.New("binding service: binding not found")

// DefaultLLMTestPrompt is the prompt TestLLM sends when the caller supplies
// none — short enough to be cheap, open enough to prove the model replies.
const DefaultLLMTestPrompt = "Reply in one short sentence to confirm you are reachable."

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
	// An LLM connection is mandatory: a binding with no LLM can't run an
	// agent (the llm-proxy 403s a run with no LLM grant). LLM connections are
	// global, not workspace-scoped — any enabled connection is fair game.
	// ConnectionRuntime only resolves enabled connections, so a successful
	// call doubles as an existence + enabled check against direct API callers.
	if req.LLMConnectionID == nil {
		return nil, ErrLLMConnectionRequired
	}
	if s.llmRuntime != nil {
		if _, err := s.llmRuntime.ConnectionRuntime(ctx, *req.LLMConnectionID); err != nil {
			return nil, ErrLLMConnectionInvalid
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

// TestLLM round-trips a prompt through the binding's LLM connection and
// returns the model's reply, so a workspace admin can confirm the agent's
// model actually responds before assigning real work. The workspace scope is
// required (an admin of workspace A must not probe a binding in workspace B by
// guessing its id). A blank prompt falls back to DefaultLLMTestPrompt.
//
// Note this exercises the provider directly, not the coding-agent's llm-proxy
// path — it validates the connection (key + model reachable), which is a
// superset of what a run needs but doesn't itself spin up a container.
func (s *BindingService) TestLLM(ctx context.Context, bindingID, workspaceID int, prompt string) (string, error) {
	binding, err := s.repo.Get(ctx, bindingID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrBindingNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load binding: %w", err)
	}
	if binding.WorkspaceID != workspaceID {
		return "", ErrBindingNotFound
	}
	if binding.LLMConnectionID == nil {
		return "", ErrLLMConnectionRequired
	}
	if s.llmRuntime == nil {
		return "", errors.New("binding service: llm runtime not configured")
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = DefaultLLMTestPrompt
	}
	return s.llmRuntime.PromptConnection(ctx, *binding.LLMConnectionID, prompt)
}

// ErrBindingNoRepo is returned by TestRepo when the binding has no repo
// configured (HasRepo is false), so there is nothing to clone. The handler
// treats it as "not applicable" rather than an error.
var ErrBindingNoRepo = errors.New("binding service: binding has no repo configured")

// ErrBindingRunnerNotConfigured is returned by TestRepo when a repo test is
// requested but no RunService (which owns the worktree preparer) is wired —
// the coding-agent harness is disabled on this server.
var ErrBindingRunnerNotConfigured = errors.New("binding service: coding-agent runner not configured")

// repoTestCheckoutTimeout bounds a single TestRepo clone+list so the admin's
// button can't hang indefinitely on a huge or unreachable repo. The first
// clone of a large repo is the slow case; later tests hit the warm bare cache.
const repoTestCheckoutTimeout = 2 * time.Minute

// RepoTestResult reports what TestRepo cloned: the repo + ref it resolved and
// the first few entries in the project root, so an admin can confirm at a
// glance that the binding points at the right project.
type RepoTestResult struct {
	RepoSlug string
	BaseRef  string
	Entries  []RepoEntry
}

// TestRepo prepares a throwaway worktree for the binding's repo — reusing the
// exact SCM-credential resolution and clone-URL derivation a real run uses
// (ResolveForRun + deriveCloneURL) and the same repoprep.Preparer — and returns
// the first max entries of the project root. This is the SCM half of the
// binding "test" chain: it proves the SCM connection decrypts, the clone URL
// resolves, and the worktree materializes against the right repo, which a bare
// LLM prompt cannot exercise.
//
// Workspace-scoped like TestLLM (an admin of one workspace can't probe
// another's binding by id). Returns ErrBindingNoRepo when the binding isn't
// repo-backed and ErrBindingRunnerNotConfigured when the harness is disabled.
func (s *BindingService) TestRepo(ctx context.Context, bindingID, workspaceID, maxEntries int) (*RepoTestResult, error) {
	binding, err := s.repo.Get(ctx, bindingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBindingNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load binding: %w", err)
	}
	if binding.WorkspaceID != workspaceID {
		return nil, ErrBindingNotFound
	}
	if !binding.HasRepo() {
		return nil, ErrBindingNoRepo
	}
	if s.scmCreds == nil {
		return nil, errors.New("binding service: scm credential resolver not configured")
	}
	if s.runs == nil {
		return nil, ErrBindingRunnerNotConfigured
	}

	ctx, cancel := context.WithTimeout(ctx, repoTestCheckoutTimeout)
	defer cancel()

	// Same derivation as the live run path (MaybeStartRunForAssignee): the
	// clone URL comes from the trusted SCM connection record + the binding's
	// slug, and the token travels on RepoSpec for askpass injection — never
	// embedded in the URL.
	token, providerType, baseURL, err := s.scmCreds.ResolveForRun(ctx, *binding.SCMConnectionID)
	if err != nil {
		return nil, fmt.Errorf("resolve scm credentials: %w", err)
	}
	cloneURL, derr := deriveCloneURL(providerType, baseURL, binding.RepoSlug)
	if derr != nil {
		return nil, fmt.Errorf("derive clone url: %w", derr)
	}

	entries, err := s.runs.InspectRepoRoot(ctx, repoprep.RepoSpec{
		WorkspaceID: workspaceID,
		RepoSlug:    binding.RepoSlug,
		RemoteURL:   cloneURL,
		BaseRef:     binding.RepoBaseRef,
		Token:       token,
	}, maxEntries)
	if err != nil {
		return nil, err
	}
	return &RepoTestResult{
		RepoSlug: binding.RepoSlug,
		BaseRef:  binding.RepoBaseRef,
		Entries:  entries,
	}, nil
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
		// it in RemoteURL. repoprep injects it via a per-clone GIT_ASKPASS
		// helper so it never appears in argv or .git/config.
		req.Repo = &repoprep.RepoSpec{
			WorkspaceID: workspaceID,
			RepoSlug:    binding.RepoSlug,
			RemoteURL:   cloneURL,
			BaseRef:     binding.RepoBaseRef,
			Token:       token,
		}
		// The SCM token stays host-side: repoprep uses it (via a per-clone
		// GIT_ASKPASS helper) to clone the worktree and, after the run, to push
		// the run branch. It is NOT injected into the container — the
		// windshift-agent holds no SCM credential and never pushes (WI-238).
		// GIT_TERMINAL_PROMPT=0 only keeps the agent's local `git commit` from
		// blocking on a credential prompt.
		req.Env["GIT_TERMINAL_PROMPT"] = "0"
	}
	if binding.LLMConnectionID != nil && s.llmRuntime != nil {
		llmCfg, err := s.llmRuntime.ConnectionRuntime(ctx, *binding.LLMConnectionID)
		if err != nil {
			return fmt.Errorf("resolve llm runtime: %w", err)
		}
		applyLLMModelEnv(req.Env, llmCfg)
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
func (s *BindingService) ResolveRunInputs(ctx context.Context, run *models.AgentRun) (*TokenSpec, *models.RunGrants, *JobRepo, map[string]string, error) {
	if run == nil || run.BindingID == nil {
		return nil, nil, nil, nil, nil
	}
	binding, err := s.repo.Get(ctx, *run.BindingID)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("resolve run inputs: load binding %d: %w", *run.BindingID, err)
	}
	itemID := 0
	if run.ItemID != nil {
		itemID = *run.ItemID
	}
	env, err := s.buildRunEnv(ctx, run.WorkspaceID, itemID)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("resolve run inputs: build env: %w", err)
	}
	// Model id for the agent (same as the local path); the broker token and
	// llm-proxy base URL are layered on at claim by applyLLMProxyEnv. No raw
	// provider key travels to a remote runner — it reaches the model only
	// through the llm-proxy with its per-run token (WI-238).
	if binding.LLMConnectionID != nil && s.llmRuntime != nil {
		llmCfg, err := s.llmRuntime.ConnectionRuntime(ctx, *binding.LLMConnectionID)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("resolve run inputs: llm runtime: %w", err)
		}
		applyLLMModelEnv(env, llmCfg)
	}
	spec, grants := s.bindingTokenAndGrants(binding, itemID)

	// Repo-prep inputs for a remote runner: only when the binding is repo-
	// backed. Unlike the local path, no SCM token travels here — the remote
	// runner clones + pushes through the git-proxy with its per-run token.
	var repo *JobRepo
	if binding.HasRepo() {
		baseRef := binding.RepoBaseRef
		if baseRef == "" {
			baseRef = "main"
		}
		repo = &JobRepo{
			WorkspaceID: run.WorkspaceID,
			Slug:        binding.RepoSlug,
			BaseRef:     baseRef,
		}
	}
	return spec, grants, repo, env, nil
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

// applyLLMModelEnv sets only the model id for the agent container. The agent
// reaches the provider exclusively through the run-scoped llm-proxy, so no raw
// provider key, base URL, provider type, or API format is ever injected into
// the (untrusted) container — those stay server-side in ProxyLLM, which the
// agent calls with its per-run token (WI-238). LLM_BASE_URL + LLM_API_KEY (the
// run token) are layered on at claim time by applyLLMProxyEnv.
func applyLLMModelEnv(env map[string]string, cfg *llm.ConnectionRuntimeConfig) {
	if cfg == nil {
		return
	}
	env["LLM_MODEL"] = cfg.Model
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
