package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"slices"
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

// ErrBindingInstructionsTooLong caps a binding's custom instructions: the
// text is appended to every run's initial prompt, so an unbounded value is
// a token-cost and context-window footgun. 8000 characters is plenty for a
// persona; longer material belongs in a skill the agent loads on demand.
var ErrBindingInstructionsTooLong = errors.New("binding service: instructions exceed 8000 characters — move detailed material into a skill")

// maxBindingInstructionsLen caps CreateBindingRequest.Instructions.
const maxBindingInstructionsLen = 8000

// ErrBindingBudgetExceeded is returned (and swallowed at log level) by
// MaybeStartRunForAssignee when a binding has already started its
// configured max_runs_per_day for the current UTC day. The binding
// remains valid; new runs simply wait until the rolling 24h window
// reopens.
var ErrBindingBudgetExceeded = errors.New("binding service: max_runs_per_day budget exceeded for today")

// Re-run trigger sentinels (the manual "Re-run" button on the item agent log).
var (
	// ErrRerunUnavailable — no RunService is wired (coding-agent harness off).
	ErrRerunUnavailable = errors.New("binding service: coding-agent harness is disabled")
	// ErrRerunNoPriorRun — the item has never had a run, so there is no agent
	// to re-run.
	ErrRerunNoPriorRun = errors.New("binding service: no prior agent run on this item")
	// ErrRerunNoBinding — the item's last run is not associated with an active
	// agent binding (manual/test run, or the binding was deleted), so its
	// configuration can't be reconstructed.
	ErrRerunNoBinding = errors.New("binding service: the last run has no active agent binding to re-run")
)

// SCMCredentialResolver is the surface BindingService needs from
// scm.CredentialResolver: given a workspace SCM connection id, return the
// access token + provider type + (for self-hosted) base URL. Kept as an
// interface so production wires scm.CredentialResolver while tests can
// supply a fake.
//
// ResolveForRunAsUser is the user-principal variant (WI-275): on
// OAuth-method connections it resolves the given user's personal token
// (ErrTriggerUserSCMNotConnected wrapped in the error chain when the user
// has none — no fallback to the workspace credential); on PAT / GitHub App
// connections it behaves exactly like ResolveForRun. ResolveForRun remains
// for callers without a user principal (legacy runs with no recorded
// triggering user).
type SCMCredentialResolver interface {
	ResolveForRun(ctx context.Context, connectionID int) (token string, providerType string, baseURL string, err error)
	ResolveForRunAsUser(ctx context.Context, connectionID, userID int) (token string, providerType string, baseURL string, err error)
}

// ErrTriggerUserSCMNotConnected is returned (wrapped) when a run on an
// OAuth-method SCM connection cannot start because the triggering user has
// not connected their own SCM account. The run is recorded as failed so the
// trigger is visible in the runs UI; there is deliberately no fallback to
// the workspace connection credential — code changes must not ride the
// connecting admin's identity (WI-275).
var ErrTriggerUserSCMNotConnected = errors.New("binding service: triggering user has no connected SCM account for this connection")

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

// RunnerPoolLister lists the runner_pool capabilities a workspace may dispatch
// to (enabled + applies-to-all OR explicitly scoped). Create uses it to
// validate a binding's chosen target_pool_id. Satisfied by
// repository.ActionRepository; an interface so tests can fake it and the
// service doesn't depend on the whole action stack.
type RunnerPoolLister interface {
	ListCapabilitiesForWorkspace(workspaceID int, capType string) ([]*models.ActionCapability, error)
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

// ErrBindingInvalidPool is returned by Create when target_pool_id is set but is
// not an enabled runner_pool capability the workspace may dispatch to. The
// handler maps it to a 400.
var ErrBindingInvalidPool = errors.New("binding service: target pool is not a runner pool available to this workspace")

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
	repo          *repository.WorkspaceAgentBindingRepository
	identity      *AgentActingIdentityService
	runs          *RunService
	scmCreds      SCMCredentialResolver
	llmRuntime    LLMRuntimeResolver
	runContext    AgentRunContextResolver
	pools         RunnerPoolLister
	skills        *repository.WorkspaceAgentSkillRepository
	continuations ItemPRContinuationResolver
	apiURL        string
	logger        *log.Logger
}

// ContinuationTarget identifies the open PR a continuation run should land on:
// its per-repo number, its repo ("owner/repo"), and its head branch.
type ContinuationTarget struct {
	PRNumber   int
	RepoSlug   string
	HeadBranch string
}

// ItemPRContinuationResolver resolves the continuation target for an item's
// most-recently-updated open linked PR, or nil when the item has none. It is the
// seam the @mention trigger uses to decide "continue the existing PR" vs "start
// a fresh run". Implemented in the server wiring because it needs both DB access
// and an scm.Provider (to read the PR's head branch), which the services package
// cannot import. Optional on BindingService — nil disables mention-continuation.
type ItemPRContinuationResolver interface {
	ContinuationForItem(ctx context.Context, itemID int) (*ContinuationTarget, error)
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
	Pools      RunnerPoolLister
	// Skills is optional: when nil, bindings carry no skill attachments and
	// run prompts get no skills index (WI-258).
	Skills *repository.WorkspaceAgentSkillRepository
	// Continuations is optional: when nil, an @mention on an item with an open
	// linked PR starts a fresh run rather than continuing that PR.
	Continuations ItemPRContinuationResolver
	APIURL        string
	Logger        *log.Logger
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
		repo:          opts.Repo,
		identity:      opts.Identity,
		runs:          opts.Runs,
		scmCreds:      opts.SCMCreds,
		llmRuntime:    opts.LLMRuntime,
		runContext:    opts.RunContext,
		pools:         opts.Pools,
		skills:        opts.Skills,
		continuations: opts.Continuations,
		apiURL:        opts.APIURL,
		logger:        logger,
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
	// TargetPoolID routes this binding's runs to a remote runner_pool instead of
	// the local in-process runner. nil = local. Validated against the pools the
	// workspace may dispatch to.
	TargetPoolID    *int
	TokenScopes     []string
	TokenTTLMinutes int
	MaxRunsPerDay   int
	// Instructions is the binding's persona/specialization, appended to the
	// run's standard initial prompt as a "Your role" section (WI-258).
	Instructions string
	// SkillIDs attaches workspace agent skills to the binding; every id must
	// belong to the binding's workspace.
	SkillIDs        []int
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
	if len(req.Instructions) > maxBindingInstructionsLen {
		return nil, ErrBindingInstructionsTooLong
	}
	if len(req.SkillIDs) > 0 && s.skills == nil {
		return nil, errors.New("binding service: skills are not configured on this server")
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
	// A target pool, when chosen, must be an enabled runner_pool capability this
	// workspace may dispatch to. nil = local in-process runner (the default).
	if req.TargetPoolID != nil {
		if err := s.validateTargetPool(req.WorkspaceID, *req.TargetPoolID); err != nil {
			return nil, err
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
		TargetPoolID:    req.TargetPoolID,
		TokenScopes:     req.TokenScopes,
		TokenTTLMinutes: req.TokenTTLMinutes,
		MaxRunsPerDay:   req.MaxRunsPerDay,
		Instructions:    req.Instructions,
		CreatedByUserID: req.CreatedByUserID,
	}
	id, err := s.repo.Insert(ctx, binding)
	if err != nil {
		return nil, err
	}
	binding.ID = id
	if len(req.SkillIDs) > 0 {
		if err := s.skills.ReplaceBindingSkills(ctx, id, req.WorkspaceID, req.SkillIDs); err != nil {
			// The binding row exists; surface the attachment failure rather
			// than rolling back — the admin can re-attach via the skills
			// endpoint. Wrapped so the handler maps it to 400.
			return nil, fmt.Errorf("binding service: attach skills: %w", err)
		}
	}
	return binding, nil
}

// UpdateAgentConfig rewrites a binding's prompt-shaping configuration —
// custom instructions and skill attachments — in place (WI-258). Bindings
// are otherwise create/delete-only; this narrow update exists so admins can
// iterate on personas and skills without recreating the binding (which
// would churn its id and history). Scoped by workspace.
func (s *BindingService) UpdateAgentConfig(ctx context.Context, workspaceID, bindingID int, instructions string, skillIDs []int) error {
	if len(instructions) > maxBindingInstructionsLen {
		return ErrBindingInstructionsTooLong
	}
	binding, err := s.repo.Get(ctx, bindingID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBindingNotFound
	}
	if err != nil {
		return fmt.Errorf("load binding: %w", err)
	}
	if binding.WorkspaceID != workspaceID {
		return ErrBindingNotFound
	}
	if err := s.repo.UpdateInstructions(ctx, bindingID, workspaceID, instructions); err != nil {
		return err
	}
	if s.skills == nil {
		if len(skillIDs) > 0 {
			return errors.New("binding service: skills are not configured on this server")
		}
		return nil
	}
	return s.skills.ReplaceBindingSkills(ctx, bindingID, workspaceID, skillIDs)
}

// validateTargetPool confirms poolID is an enabled runner_pool capability the
// workspace may dispatch to. It reuses the same workspace-scoping the action
// stack enforces (applies-to-all OR explicitly scoped), so a binding can't pin
// a pool the workspace isn't allowed to use.
func (s *BindingService) validateTargetPool(workspaceID, poolID int) error {
	if s.pools == nil {
		return ErrBindingInvalidPool
	}
	pools, err := s.pools.ListCapabilitiesForWorkspace(workspaceID, string(models.CapabilityRunnerPool))
	if err != nil {
		return fmt.Errorf("list runner pools: %w", err)
	}
	for _, p := range pools {
		if p.ID == poolID {
			return nil
		}
	}
	return ErrBindingInvalidPool
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

// ErrBindingNoRepo is returned by StartTestRun when the binding has no repo
// configured (HasRepo is false), so there is no worktree to hand the agent.
var ErrBindingNoRepo = errors.New("binding service: binding has no repo configured")

// ErrBindingRunnerNotConfigured is returned by StartTestRun when a test run is
// requested but no RunService is wired — the coding-agent harness is disabled
// on this server (CodingAgent.Enabled off).
var ErrBindingRunnerNotConfigured = errors.New("binding service: coding-agent runner not configured")

// ErrBindingTestRunRemotePool is returned by StartTestRun when the binding
// targets a remote runner pool. Test runs always execute on the local
// in-process runtime; running one for a pool-targeted binding would test the
// wrong runtime — and fail outright on hosts without git/docker, which is the
// very deployment remote pools exist for.
var ErrBindingTestRunRemotePool = errors.New("binding service: test runs are not supported for bindings that target a remote runner pool")

// DefaultTestRunPrompt is the one-shot prompt a binding "test run" hands the
// agent. It is deliberately read-only — list the project root and report a few
// entries — so simulating an assignment proves the full chain (LLM reachable +
// repo checked out + the agent can see its files) without mutating anything.
const DefaultTestRunPrompt = "This is a connectivity test, not a real task. " +
	"List the files and folders in the root of your working directory and reply " +
	"with up to 5 of their names so we can confirm the repository is checked out " +
	"correctly. Do not modify, create, commit, or push anything."

// StartTestRun provisions a real coding-agent container run for the binding —
// the same machinery a work-item assignment drives — but with no work item and
// a read-only test prompt, and marked Ephemeral so it can never push a branch
// or open a PR. It is the full-cycle counterpart of TestLLM: where TestLLM only
// proves the model answers, this proves the model is reachable through the
// run-scoped proxy, the SCM connection clones the right repo into a worktree,
// and the agent can actually read the checked-out files.
//
// Returns the new run id immediately (the run executes asynchronously); callers
// watch it via the agent-runs events endpoints. Workspace-scoped like TestLLM.
// Requires a repo-backed binding (ErrBindingNoRepo otherwise), a binding on the
// local in-process runtime (ErrBindingTestRunRemotePool otherwise), and a
// configured runner (ErrBindingRunnerNotConfigured otherwise).
//
// triggeredByUserID is the admin starting the test; on OAuth connections the
// clone authenticates with their personal token (WI-275) —
// ErrTriggerUserSCMNotConnected when they have none.
func (s *BindingService) StartTestRun(ctx context.Context, bindingID, workspaceID, triggeredByUserID int) (int, error) {
	binding, err := s.repo.Get(ctx, bindingID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrBindingNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("load binding: %w", err)
	}
	if binding.WorkspaceID != workspaceID {
		return 0, ErrBindingNotFound
	}
	if !binding.HasRepo() {
		return 0, ErrBindingNoRepo
	}
	if binding.TargetPoolID != nil {
		return 0, ErrBindingTestRunRemotePool
	}
	if s.runs == nil {
		return 0, ErrBindingRunnerNotConfigured
	}
	// Test runs execute on the local in-process runtime (they refuse remote
	// pools above). On an orchestration-only server there is no local runner,
	// so fail before doing any prep rather than queuing a run nothing claims.
	if !s.runs.LocalExecutionEnabled() {
		return 0, ErrBindingRunnerNotConfigured
	}
	if s.scmCreds == nil {
		return 0, errors.New("binding service: scm credential resolver not configured")
	}

	// itemID 0 → buildRunEnv emits the workspace context without an item join,
	// and the run is persisted with a NULL item_id.
	env, err := s.buildRunEnv(ctx, workspaceID, 0)
	if err != nil {
		return 0, err
	}

	// Repo prep inputs, derived exactly as the live trigger does: the clone URL
	// comes from the trusted SCM connection + slug, and the token rides on
	// RepoSpec for askpass injection (never embedded in the URL). The
	// credential principal is the admin starting the test (WI-275).
	token, providerType, baseURL, err := s.scmCreds.ResolveForRunAsUser(ctx, *binding.SCMConnectionID, triggeredByUserID)
	if err != nil {
		if errors.Is(err, ErrTriggerUserSCMNotConnected) {
			req := RunRequest{
				WorkspaceID:       workspaceID,
				BindingID:         binding.ID,
				TriggeredByUserID: triggeredByUserID,
			}
			if _, rerr := s.runs.RecordFailedStart(ctx, req, triggerUserNotConnectedReason); rerr != nil {
				s.logger.Printf("binding service: record failed test run for binding=%d: %v", binding.ID, rerr)
			}
			return 0, err
		}
		return 0, fmt.Errorf("resolve scm credentials: %w", err)
	}
	cloneURL, derr := deriveCloneURL(providerType, baseURL, binding.RepoSlug)
	if derr != nil {
		return 0, fmt.Errorf("derive clone url: %w", derr)
	}

	req := RunRequest{
		WorkspaceID:       workspaceID,
		ItemID:            nil,
		BindingID:         binding.ID,
		Env:               env,
		InitialPrompt:     DefaultTestRunPrompt,
		Ephemeral:         true,
		TriggeredByUserID: triggeredByUserID,
		Repo: &repoprep.RepoSpec{
			WorkspaceID: workspaceID,
			RepoSlug:    binding.RepoSlug,
			RemoteURL:   cloneURL,
			BaseRef:     binding.RepoBaseRef,
			Token:       token,
		},
	}
	req.Env["GIT_TERMINAL_PROMPT"] = "0"

	// The agent reaches the model only through the run-scoped llm-proxy, which
	// needs the per-run token + LLM grant (applied at claim from Token/Grants).
	if binding.LLMConnectionID != nil && s.llmRuntime != nil {
		llmCfg, lerr := s.llmRuntime.ConnectionRuntime(ctx, *binding.LLMConnectionID)
		if lerr != nil {
			return 0, fmt.Errorf("resolve llm runtime: %w", lerr)
		}
		applyLLMModelEnv(req.Env, llmCfg)
	}
	req.Token, req.Grants = s.bindingTokenAndGrants(binding, 0, triggeredByUserID, false)

	runID, err := s.runs.Start(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("start test run: %w", err)
	}
	s.logger.Printf("binding service: started ephemeral test run=%d for binding=%d (no item)", runID, binding.ID)
	return runID, nil
}

// triggerUserNotConnectedReason is the error recorded on a run that could
// not start because the triggering user has no SCM account connected for
// the binding's OAuth connection. Shown verbatim in the runs UI.
const triggerUserNotConnectedReason = "the user who triggered this run has no connected SCM account for the binding's OAuth connection; connect your GitHub/Gitea account under profile settings, or switch the connection to a PAT / GitHub App"

// startFailureReason renders a trigger-time resolution failure as the error
// recorded on the failed run. Every misconfiguration a run would otherwise
// hit minutes later (proxy 503, clone failure, claim enrichment error) — or
// worse, never surface at all — fails visibly in the runs panel instead.
// RecordFailedStart redacts, but redact here too so callers can also log it.
func startFailureReason(what string, err error) string {
	if errors.Is(err, ErrTriggerUserSCMNotConnected) {
		return triggerUserNotConnectedReason
	}
	return "could not resolve the binding's " + what + " at start time: " + RedactString(err.Error())
}

// MaybeStartRunForAssignee is the assignee-change trigger. Hot path: if
// the assignee did not actually change or no binding matches the new
// assignee, this is a no-op (one indexed lookup). Otherwise it builds a
// RunRequest from the binding and dispatches via RunService.Start.
//
// The signature takes *int for old/new assignee so callers don't have to
// special-case nil (item created without assignee, then assigned later).
//
// triggeredByUserID is the user performing the assignment; on OAuth SCM
// connections their personal token is the run's git credential (WI-275).
// When they have no connected account the run is recorded as failed (so
// the refusal is visible) and ErrTriggerUserSCMNotConnected is returned.
func (s *BindingService) MaybeStartRunForAssignee(ctx context.Context, workspaceID, itemID int, oldAssignee, newAssignee *int, triggeredByUserID int) error {
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
		// Human assignees land here on every assignment — stay silent for
		// them. But an AGENT assignee with no binding is a misconfiguration
		// the assigner cannot see otherwise: the assignment "succeeds" and
		// nothing ever happens.
		if s.identity.IsAgentUser(*newAssignee) {
			s.logger.Printf("binding service: item=%d assigned to agent user=%d but workspace=%d has no agent binding for that user — no run started", itemID, *newAssignee, workspaceID)
		}
		return nil
	}
	return s.startRunForBinding(ctx, binding, workspaceID, itemID, triggeredByUserID, &models.RunTrigger{Kind: "assignee"})
}

// MaybeStartRunsForMentions is the comment-@mention trigger (WI-264): every
// mentioned user that is a binding's acting user gets a run on the commented
// item — a lighter nudge than assignment, with no assignee or status change.
// Callers invoke it on comment CREATE only; comment edits never re-trigger.
//
// Per-mention guards:
//   - self-mention: a mention of the comment author themselves is skipped,
//     so an agent commenting "@itself" cannot loop. Agents mentioning OTHER
//     agents do trigger (indirect cycles are the operator's configuration
//     responsibility, mirrored from the assignee trigger's posture).
//   - dedup: a binding that already has a queued or running run on this item
//     is skipped — a repeat mention while the agent works is a nudge, not a
//     second job. A mention in a later comment, after the run finishes,
//     triggers again.
//   - budget: MaxRunsPerDay applies exactly as in the assignee trigger.
//
// Distinct agents mentioned in one comment each get their own run. Failures
// are isolated per mention (one agent's refusal must not block the others);
// they are joined into the returned error for the caller to log-and-swallow.
func (s *BindingService) MaybeStartRunsForMentions(ctx context.Context, workspaceID, itemID int, mentionedUserIDs []int, commentAuthorID int, commentBody string, commentID int) error {
	if len(mentionedUserIDs) == 0 {
		return nil
	}
	seen := make(map[int]bool, len(mentionedUserIDs))
	var errs []error
	for _, userID := range mentionedUserIDs {
		if userID <= 0 || seen[userID] {
			continue
		}
		seen[userID] = true
		if userID == commentAuthorID {
			continue
		}
		binding, err := s.repo.FindByActingUser(ctx, workspaceID, userID)
		if err != nil {
			errs = append(errs, fmt.Errorf("find binding for mention of user %d: %w", userID, err))
			continue
		}
		if binding == nil {
			// A plain human mention — the notification pipeline owns it.
			continue
		}
		if s.runs != nil {
			active, err := s.runs.CountActiveRunsForBindingItem(ctx, binding.ID, itemID)
			if err != nil {
				errs = append(errs, fmt.Errorf("count active runs for binding %d: %w", binding.ID, err))
				continue
			}
			if active > 0 {
				s.logger.Printf("binding service: mention of binding=%d on item=%d skipped — %d run(s) already queued/running", binding.ID, itemID, active)
				continue
			}
		}
		trigger := &models.RunTrigger{
			Kind:        "mention",
			Instruction: commentBody,
			CommentID:   commentID,
			AuthorID:    commentAuthorID,
		}
		// If the item already has an open linked PR in this binding's repo, the
		// mention continues that PR (adds commits to it) rather than opening a
		// competing one. Resolution failures degrade to a fresh run — a missing
		// continuation is never worse than today's behavior.
		s.applyMentionContinuation(ctx, trigger, binding, itemID)
		if err := s.startRunForBinding(ctx, binding, workspaceID, itemID, commentAuthorID, trigger); err != nil {
			errs = append(errs, fmt.Errorf("start run for mentioned binding %d: %w", binding.ID, err))
		}
	}
	return errors.Join(errs...)
}

// promptSuffixForBinding renders the per-binding addition to the run's
// initial prompt (WI-258): the binding's instructions as a "Your role"
// section, plus an index of the attached enabled skills with `ws skill get`
// pointers — progressive disclosure, so skill bodies cost no context until
// the agent decides one is relevant. Returns "" when the binding has
// neither.
func (s *BindingService) promptSuffixForBinding(binding *models.WorkspaceAgentBinding, skills []*models.WorkspaceAgentSkill) string {
	var b strings.Builder
	if strings.TrimSpace(binding.Instructions) != "" {
		b.WriteString("\n\n## Your role\n")
		b.WriteString(strings.TrimSpace(binding.Instructions))
	}
	if len(skills) > 0 {
		fmt.Fprintf(&b, "\n\n## Skills\nYou have %d skill(s) — knowledge packs curated for you. When one is relevant to the task, read its full body with `ws skill get <id>` before relying on it:\n", len(skills))
		for _, sk := range skills {
			desc := strings.TrimSpace(sk.Description)
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(&b, "- [%d] %s: %s\n", sk.ID, sk.Name, desc)
		}
	}
	return b.String()
}

// renderInstruction renders the run's free-form instruction — the body of the
// @mentioning comment that triggered the run — as a prompt section the agent
// treats as its directive for what to do. Returns "" when the trigger carries
// no instruction (e.g. a bare assignment change), so the static prompt stands
// alone. The comment is quoted verbatim and the agent is pointed at the item
// and its other comments for context, so a terse instruction ("fix the typo")
// does not strand it without the surrounding detail.
func renderInstruction(trigger *models.RunTrigger) string {
	if !trigger.HasInstruction() {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Your instruction for this run\n")
	b.WriteString("A user mentioned you in a comment on $WINDSHIFT_ITEM_ID. Treat the comment below as your primary instruction for what to do on this run — it takes precedence over any default assumption about the task. It may be terse; when it lacks detail, read the work item and its other comments (`ws task get $WINDSHIFT_ITEM_ID`, `ws comment list $WINDSHIFT_ITEM_ID`) for the surrounding context before acting.\n\n")
	for _, line := range strings.Split(strings.TrimRight(trigger.Instruction, "\n"), "\n") {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// enabledSkillsForBinding loads the binding's enabled skills; nil when the
// skills repo is not wired or the lookup fails (logged — a skills hiccup
// must not block the run).
func (s *BindingService) enabledSkillsForBinding(ctx context.Context, binding *models.WorkspaceAgentBinding) []*models.WorkspaceAgentSkill {
	if s.skills == nil {
		return nil
	}
	skills, err := s.skills.ListEnabledForBinding(ctx, binding.ID)
	if err != nil {
		s.logger.Printf("binding service: list skills for binding=%d: %v (run proceeds without skills)", binding.ID, err)
		return nil
	}
	return skills
}

// applyMentionContinuation sets the trigger's continuation fields when the
// mentioned binding's item has an open linked PR in the binding's own repo, so
// the run lands commits on that PR instead of cutting a fresh branch. It is a
// no-op (leaving a normal fresh-run trigger) when no resolver is wired, the
// binding has no repo, the item has no open PR, the PR is in a different repo
// than the binding writes to, or resolution errors — none of which should block
// the run.
func (s *BindingService) applyMentionContinuation(ctx context.Context, trigger *models.RunTrigger, binding *models.WorkspaceAgentBinding, itemID int) {
	if s.continuations == nil || !binding.HasRepo() {
		return
	}
	target, err := s.continuations.ContinuationForItem(ctx, itemID)
	if err != nil {
		s.logger.Printf("binding service: resolve continuation for item=%d binding=%d: %v (starting fresh run)", itemID, binding.ID, err)
		return
	}
	if target == nil || target.HeadBranch == "" {
		return
	}
	// Write scope: only continue a PR in the repo this binding's credentials can
	// push to. A PR in another repo would push back somewhere the binding has no
	// business writing.
	if target.RepoSlug != binding.RepoSlug {
		s.logger.Printf("binding service: item=%d open PR is in %q but binding=%d writes %q — starting fresh run", itemID, target.RepoSlug, binding.ID, binding.RepoSlug)
		return
	}
	trigger.ContinuePRNumber = target.PRNumber
	trigger.ContinueRepoSlug = target.RepoSlug
	trigger.ContinueHeadBranch = target.HeadBranch
	s.logger.Printf("binding service: mention on item=%d will continue PR #%d (%s) on binding=%d", itemID, target.PRNumber, target.HeadBranch, binding.ID)
}

// startRunForBinding admits and dispatches one run for a matched binding —
// the shared core of the assignee-change and comment-@mention triggers.
// Enforces the binding's MaxRunsPerDay budget, routes to the remote pool or
// the local in-process path, and resolves SCM credentials as the triggering
// user (WI-275).
func (s *BindingService) startRunForBinding(ctx context.Context, binding *models.WorkspaceAgentBinding, workspaceID, itemID, triggeredByUserID int, trigger *models.RunTrigger) error {
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
		remoteReq := RunRequest{
			WorkspaceID:       workspaceID,
			ItemID:            &itemID,
			BindingID:         binding.ID,
			TargetPoolID:      binding.TargetPoolID,
			JobKind:           models.JobKindCodingAgent,
			TriggeredByUserID: triggeredByUserID,
			// The instruction itself is recovered + rendered into the prompt at
			// remote claim time (ResolveRunInputs), the same place the binding
			// suffix is re-derived — so it survives the queue→claim hop.
			Trigger: trigger,
		}
		// Pre-validate the full SCM resolution now — credential principal AND
		// clone-host config — rather than letting the run sit queued until a
		// runner claims it and the git proxy 401s/503s: "fail visibly at
		// start time" (WI-275). The resolved token is discarded — remote
		// runners reach git only through the proxy — and deriveCloneURL is
		// the same base-URL validation the proxy applies at claim time.
		if binding.HasRepo() && s.scmCreds != nil {
			_, providerType, baseURL, err := s.scmCreds.ResolveForRunAsUser(ctx, *binding.SCMConnectionID, triggeredByUserID)
			if err == nil {
				_, err = deriveCloneURL(providerType, baseURL, binding.RepoSlug)
			}
			if err != nil {
				if _, rerr := s.runs.RecordFailedStart(ctx, remoteReq, startFailureReason("SCM connection", err)); rerr != nil {
					s.logger.Printf("binding service: record failed remote run for item=%d binding=%d: %v", itemID, binding.ID, rerr)
				}
				return err
			}
		}
		// Same fail-visibly treatment for the LLM connection the claim-time
		// enrichment will resolve.
		if binding.LLMConnectionID != nil && s.llmRuntime != nil {
			if _, err := s.llmRuntime.ConnectionRuntime(ctx, *binding.LLMConnectionID); err != nil {
				if _, rerr := s.runs.RecordFailedStart(ctx, remoteReq, startFailureReason("LLM connection", err)); rerr != nil {
					s.logger.Printf("binding service: record failed remote run for item=%d binding=%d: %v", itemID, binding.ID, rerr)
				}
				return err
			}
		}
		runID, err := s.runs.Start(ctx, remoteReq)
		if err != nil {
			return fmt.Errorf("start remote run: %w", err)
		}
		s.logger.Printf("binding service: queued remote run=%d for item=%d binding=%d pool=%d", runID, itemID, binding.ID, *binding.TargetPoolID)
		return nil
	}

	skills := s.enabledSkillsForBinding(ctx, binding)

	env, err := s.buildRunEnv(ctx, workspaceID, itemID)
	if err != nil {
		return err
	}
	req := RunRequest{
		WorkspaceID:       workspaceID,
		ItemID:            &itemID,
		BindingID:         binding.ID,
		Env:               env,
		TriggeredByUserID: triggeredByUserID,
		Trigger:           trigger,
		// Local path renders the instruction inline (the remote path re-derives
		// it at claim from the persisted Trigger). Order matches remote: static
		// prompt, then binding persona/skills, then the run's instruction last.
		InitialPromptSuffix: s.promptSuffixForBinding(binding, skills) + renderInstruction(trigger),
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
		token, providerType, baseURL, err := s.scmCreds.ResolveForRunAsUser(ctx, *binding.SCMConnectionID, triggeredByUserID)
		var cloneURL string
		if err == nil {
			cloneURL, err = deriveCloneURL(providerType, baseURL, binding.RepoSlug)
		}
		if err != nil {
			// Fail visibly: without a run row the trigger evaporates and the
			// assigner sees nothing at all (WI-275, extended past the
			// not-connected case after the git-proxy 503 incident).
			if _, rerr := s.runs.RecordFailedStart(ctx, req, startFailureReason("SCM connection", err)); rerr != nil {
				s.logger.Printf("binding service: record failed run for item=%d binding=%d: %v", itemID, binding.ID, rerr)
			}
			return err
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
		// A continuation run checks out the bound PR's head branch and pushes
		// back to it instead of cutting a fresh per-run branch (BaseRef ignored).
		if trigger.IsContinuation() {
			req.Repo.ContinueBranch = trigger.ContinueHeadBranch
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
			if _, rerr := s.runs.RecordFailedStart(ctx, req, startFailureReason("LLM connection", err)); rerr != nil {
				s.logger.Printf("binding service: record failed run for item=%d binding=%d: %v", itemID, binding.ID, rerr)
			}
			return err
		}
		applyLLMModelEnv(req.Env, llmCfg)
	}
	// Mint a per-run ws token + snapshot the run's access-layer grants
	// (WI-144). Shared with the remote claim path via bindingTokenAndGrants so
	// both transports derive identical inputs (WI-195). The git ref is filled
	// at claim from the prepared worktree branch.
	req.Token, req.Grants = s.bindingTokenAndGrants(binding, itemID, triggeredByUserID, len(skills) > 0)

	runID, err := s.runs.Start(ctx, req)
	if err != nil {
		return fmt.Errorf("start run: %w", err)
	}
	s.logger.Printf("binding service: started run=%d for item=%d binding=%d acting_user=%d", runID, itemID, binding.ID, binding.ActingUserID)
	return nil
}

// RerunForItem manually re-triggers the agent that last worked an item — the
// "Re-run" button on the item's agent log. It derives the agent from the most
// recent run that carried a binding and reuses that binding's full
// configuration (repo / token / grants / prompt) via startRunForBinding, the
// same path the assignee and @mention triggers use. triggeredByUserID is the
// human who clicked; they become the run's SCM principal (WI-275).
//
// started=false with a nil error means a run is already queued or in progress
// for this item — the re-run is a no-op rather than a stacked second job
// (mirrors the @mention dedup, WI-264). The caller keeps its button disabled.
func (s *BindingService) RerunForItem(ctx context.Context, itemID, triggeredByUserID int) (started bool, err error) {
	if s.runs == nil {
		return false, ErrRerunUnavailable
	}
	latest, err := s.runs.LatestRunForItem(ctx, itemID)
	if err != nil {
		return false, fmt.Errorf("find latest run: %w", err)
	}
	if latest == nil {
		return false, ErrRerunNoPriorRun
	}
	if latest.BindingID == nil {
		return false, ErrRerunNoBinding
	}
	binding, err := s.repo.Get(ctx, *latest.BindingID)
	if err != nil || binding == nil {
		// Binding deleted since the last run — nothing to reconstruct.
		return false, ErrRerunNoBinding
	}
	// Dedup: never stack a second run while one is queued/running for this
	// binding+item. The server-side backstop to the UI's disabled button.
	active, err := s.runs.CountActiveRunsForBindingItem(ctx, binding.ID, itemID)
	if err != nil {
		return false, fmt.Errorf("count active runs: %w", err)
	}
	if active > 0 {
		return false, nil
	}
	// Carry the original run's instruction forward so a re-run repeats the same
	// directive the agent first saw, not a bare context-free run.
	rerunTrigger := &models.RunTrigger{Kind: "rerun"}
	if latest.Trigger != nil {
		rerunTrigger.Instruction = latest.Trigger.Instruction
		rerunTrigger.CommentID = latest.Trigger.CommentID
		rerunTrigger.AuthorID = latest.Trigger.AuthorID
	}
	if err := s.startRunForBinding(ctx, binding, latest.WorkspaceID, itemID, triggeredByUserID, rerunTrigger); err != nil {
		return false, err
	}
	return true, nil
}

// bindingTokenAndGrants derives the per-run token spec and access-layer
// grants from a binding, shared by the local Start path and the remote claim
// enrichment (WI-195). Returns (nil, nil) when the binding mints no token (no
// acting user, or no token service configured) — grants are meaningful only
// when bound to a token. The git grant's Ref is left empty here; the claim
// path fills it (the worktree branch locally, the run-branch namespace
// remotely). triggeredByUserID is stamped into the git grant as the
// credential principal the git proxy resolves on OAuth connections (WI-275);
// 0 keeps the connection-level credential. withSkillsRead appends the
// agent-skills:read scope when the binding pins explicit scopes that predate
// the skills feature — a run whose prompt indexes skills must be able to
// fetch them (WI-258); empty scopes already expand to the default set, which
// includes it.
func (s *BindingService) bindingTokenAndGrants(b *models.WorkspaceAgentBinding, itemID, triggeredByUserID int, withSkillsRead bool) (*TokenSpec, *models.RunGrants) {
	if b.ActingUserID <= 0 || !s.runs.HasTokens() {
		return nil, nil
	}
	scopes := b.TokenScopes
	if withSkillsRead && len(scopes) > 0 && !slices.Contains(scopes, auth.ScopeAgentSkillsRead) {
		scopes = append(append([]string{}, scopes...), auth.ScopeAgentSkillsRead)
	}
	spec := &TokenSpec{
		ActingUserID: b.ActingUserID,
		Scopes:       scopes,
		TTL:          time.Duration(b.TokenTTLMinutes) * time.Minute,
		Name:         fmt.Sprintf("agent-run:item-%d:binding-%d", itemID, b.ID),
	}
	grants := &models.RunGrants{}
	if b.HasRepo() {
		grants.Git = &models.GitGrant{Repo: b.RepoSlug, ConnectionID: *b.SCMConnectionID, UserID: triggeredByUserID}
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
func (s *BindingService) ResolveRunInputs(ctx context.Context, run *models.AgentRun) (*RunInputs, error) {
	if run == nil || run.BindingID == nil {
		return nil, nil
	}
	binding, err := s.repo.Get(ctx, *run.BindingID)
	if err != nil {
		return nil, fmt.Errorf("resolve run inputs: load binding %d: %w", *run.BindingID, err)
	}
	itemID := 0
	if run.ItemID != nil {
		itemID = *run.ItemID
	}
	env, err := s.buildRunEnv(ctx, run.WorkspaceID, itemID)
	if err != nil {
		return nil, fmt.Errorf("resolve run inputs: build env: %w", err)
	}
	// Model id for the agent (same as the local path); the broker token and
	// llm-proxy base URL are layered on at claim by applyLLMProxyEnv. No raw
	// provider key travels to a remote runner — it reaches the model only
	// through the llm-proxy with its per-run token (WI-238).
	if binding.LLMConnectionID != nil && s.llmRuntime != nil {
		llmCfg, err := s.llmRuntime.ConnectionRuntime(ctx, *binding.LLMConnectionID)
		if err != nil {
			return nil, fmt.Errorf("resolve run inputs: llm runtime: %w", err)
		}
		applyLLMModelEnv(env, llmCfg)
	}
	triggeredBy := 0
	if run.TriggeredByUserID != nil {
		triggeredBy = *run.TriggeredByUserID
	}
	skills := s.enabledSkillsForBinding(ctx, binding)
	// Re-derive the binding persona/skills suffix, then append the run's own
	// instruction (the @mentioning comment, persisted on the run as Trigger) so
	// the remote claim prepares the prompt identically to the local path.
	promptSuffix := s.promptSuffixForBinding(binding, skills) + renderInstruction(run.Trigger)
	spec, grants := s.bindingTokenAndGrants(binding, itemID, triggeredBy, len(skills) > 0)

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
		// Continuation: land commits on the bound PR's head branch (resolved and
		// persisted on the trigger when the run was queued) rather than a fresh
		// per-run branch. Survives the queue→claim hop via run.Trigger.
		if run.Trigger.IsContinuation() {
			repo.ContinueBranch = run.Trigger.ContinueHeadBranch
		}
	}
	return &RunInputs{Token: spec, Grants: grants, Repo: repo, Env: env, PromptSuffix: promptSuffix}, nil
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
