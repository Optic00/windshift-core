// Package services hosts the coding-agent harness orchestration.
//
// RunService is the per-process owner of agent_runs lifecycle: admission
// control, async dispatch onto a goroutine worker, event persistence, and
// terminal status finalization. The actual container spawn is delegated to
// a Runner (see runner.go in later phases) so the service is testable
// without docker.
//
// This is the Phase 1 walking-skeleton implementation (WI-84). Per-user
// and per-workspace semaphores, worktree management, token minting, SSE,
// and binding-driven trigger paths are intentionally absent — they land in
// WI-85 / WI-86 / WI-88 / WI-89.
package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"windshift/internal/models"
	"windshift/internal/repoprep"
	"windshift/internal/repository"
)

// EventSink is what a Runner uses to stream events into agent_run_events
// while a run is in flight. Returning an error from the sink does not abort
// the run by itself — the runner decides what to do.
type EventSink func(eventType, payloadJSON string) error

// RunnerResult is the terminal verdict a Runner returns when it exits.
// Status must be one of the terminal agent-run states (see
// models.IsAgentRunTerminal). ContainerID is recorded for audit / forensics
// and may be empty in skeleton runners.
type RunnerResult struct {
	ContainerID string
	Status      string
	Error       string
	// Branch + BaseCommit are reported by a remote runner that prepared its
	// own worktree, so the orchestrator can create the PR on result (the
	// in-process path already has these from the worktree it prepared). Empty
	// for runs that produced no branch.
	Branch     string
	BaseCommit string
	// Summary is the agent's finish summary, surfaced as the PR note (WI-400).
	// Empty when the agent emitted no summary. Agent-generated text — bound and
	// sanitize before it reaches an SCM PR body.
	Summary string
	// Repos carries the per-repo push results of a multi-repo run (WI-449),
	// primary first. Branch is empty for a repo with no new commits. When set,
	// it supersedes the scalar Branch/BaseCommit (which mirror the primary).
	Repos []RunnerRepoResult
}

// RunnerRepoResult is one repo's push result reported by a (remote) runner that
// prepared its own checkouts (WI-449).
type RunnerRepoResult struct {
	RepoSlug   string
	Branch     string
	BaseCommit string
}

// RunInput is what RunService hands to a Runner when work is admitted:
// the run id, the host path containing the prepared worktree (empty if no
// repo was attached to the request), and any orchestrator-supplied env
// vars to forward into the container.
type RunInput struct {
	RunID         int
	WorkspacePath string
	Env           map[string]string
	InitialPrompt string
	// Kind + Image let a kind-dispatching runner pick its execution mode
	// (WI-146): coding_agent vs action_container / ci_task (run Image as
	// a plain container). Empty Kind means coding_agent.
	Kind  string
	Image string
	// Repo, when set, asks a repo-preparing runner (the remote TriageRunner)
	// to materialize its own checkout and push the run branch via the
	// git-proxy. Nil for local runs (WorkspacePath is already prepared).
	// Deprecated by Repos; mirrors Repos[0] (the primary).
	Repo *JobRepo
	// Repos is every repo a repo-preparing runner must check out (WI-449),
	// primary first. One entry → single-repo layout; many → sibling checkouts
	// under a shared workspace root, each pushed independently.
	Repos []JobRepo
}

// Runner executes the actual work of a run: spawning a container, driving
// the JSONL agent contract, and streaming events back through the sink. The skeleton
// uses a func adapter; the container-backed implementation lives in later
// phases.
type Runner interface {
	Run(ctx context.Context, input RunInput, emit EventSink) RunnerResult
}

// RunnerFunc adapts a plain function to the Runner interface.
type RunnerFunc func(ctx context.Context, input RunInput, emit EventSink) RunnerResult

// Run implements Runner for RunnerFunc.
func (f RunnerFunc) Run(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
	return f(ctx, input, emit)
}

// BindingID is the optional id stamped on PostRunInfo so the hook can
// look the binding up without re-running the assignee match. The binding
// trigger sets it; manual run starts leave it 0.

// RunRequest is the minimum payload required to start a run.
//
// Repo is optional: when set, RunService asks the repoprep.Preparer to
// prepare a per-run checkout before the runner sees the run; when nil, the runner
// runs without a /workspace mount.
//
// Token is optional: when set, RunService mints a short-lived `ws` API
// token via RunTokenService and forwards it to the container as
// $WS_TOKEN. The minted token expires by TTL; the orchestrator does not
// revoke it on run completion (cleanup happens via the token cleanup
// sweeper on api_tokens.expires_at).
//
// Env is forwarded verbatim into the runner's environment. Orchestrator-
// owned keys (WS_TOKEN, AGENT_RUN_ID) win over caller-supplied values to
// avoid mixed-identity confusion.
//
// Binding, acting-identity resolution, and llm_connection selection get
// bolted on in later phases (WI-87 / WI-88); for now the caller resolves
// those itself and hands the result in via Token + Env.
type RunRequest struct {
	WorkspaceID int
	ItemID      *int
	BindingID   int
	// Repo is the deprecated single-repo input (WI-449). Prefer Repos. When
	// Repos is empty and Repo is set, the run path treats it as a one-element
	// primary repo, keeping single-repo behavior byte-identical.
	Repo *repoprep.RepoSpec
	// Repos is the set of repositories to check out for the run (WI-449), the
	// primary first. One repo → single-repo layout (cwd = that checkout, as
	// before); more than one → each is checked out as a sibling dir under a
	// shared per-run workspace root that becomes the agent's cwd.
	Repos []*repoprep.RepoSpec
	Token *TokenSpec
	Env   map[string]string
	// Grants, when set, is snapshotted onto the run at claim time and bound
	// to the minted run-token (WI-144) so the access-layer brokers can
	// authorize the run's git/llm/secret access. The git ref is filled in at
	// claim from the prepared worktree branch. Only persisted when a token is
	// minted (the brokers authorize by the bound token).
	Grants *models.RunGrants
	// JobKind + JobImage select the runner execution mode (WI-146). Empty
	// JobKind defaults to coding_agent; action_container / ci_task run
	// JobImage as a plain container.
	JobKind  string
	JobImage string
	// InitialPrompt overrides the runner's static coding-agent prompt for
	// this one run. Empty falls back to RunService.initialPrompt. The binding
	// "test run" uses it to drive a one-shot read-only prompt instead of the
	// real work-item prompt.
	InitialPrompt string
	// Ephemeral marks a throwaway run that must not mutate the remote: the
	// host-side run-branch push and the post-run PR hook are both skipped at
	// finalize. The binding "test run" sets this so simulating an assignment
	// can never open a PR or push a branch.
	Ephemeral bool
	// TargetPoolID, when set, routes the run to a remote runner_pool instead
	// of the local in-process pool (WI-195). A remote run is persisted queued
	// for the pool and enriched (token + grants + env) at claim time by the
	// remote claim path, so Repo/Token/Grants/Env on this request are ignored
	// for remote runs — the orchestrator never sees the work locally.
	TargetPoolID *int
	// InitialPromptSuffix is appended to whichever initial prompt the run
	// uses (the service default or a per-run override): the binding's
	// custom instructions + skills index (WI-258). Never replaces the
	// operational prompt.
	InitialPromptSuffix string
	// TriggeredByUserID is the user who caused the run (the assigner whose
	// change fired the binding trigger, or the admin starting a test run).
	// Persisted on the run for audit; on OAuth SCM connections it is the
	// credential principal for the run's git traffic and PR creation
	// (WI-275). 0 = unknown (legacy callers) → connection-level credential.
	TriggeredByUserID int
	// Trigger is the run's trigger context + free-form instruction (the
	// @mentioning comment that started the run). Persisted as JSON on the run
	// and, for remote runs, recovered at claim time so the instruction reaches
	// the agent as part of its prompt. Nil for triggers carrying no extra
	// context (e.g. a bare assignment change).
	Trigger *models.RunTrigger
}

// TokenSpec is the per-run input to RunTokenService.Mint. Phase 4-5 wire
// this from a binding row; for now callers populate it directly.
type TokenSpec struct {
	ActingUserID int
	Scopes       []string
	TTL          time.Duration
	Name         string
}

// RunServiceOptions controls construction. GlobalCap caps the number of
// runs in-flight across the whole process — it sizes the in-process worker
// pool (decision #7), which replaced the old admission semaphore. Start
// enqueues onto a buffered job queue and returns without blocking the HTTP
// handler that called it. Preparer is optional and only
// required when callers actually attach Repo to a RunRequest. Tokens is
// optional and only required when callers attach a TokenSpec. PostRunHook
// is optional and fires once per run after the terminal status is
// finalized — that's where WI-90's PR creation + ItemSCMLink writeback
// live.
type RunServiceOptions struct {
	GlobalCap     int
	Runner        Runner
	Preparer      *repoprep.Preparer
	Tokens        *RunTokenService
	PostRunHook   PostRunHook
	InitialPrompt string
	Now           func() time.Time // injected for deterministic tests
	Logger        *log.Logger
}

// PostRunInfo is what RunService hands to PostRunHook.AfterRun once the
// terminal status has been finalized. Branch + BaseCommit are populated
// only when a worktree was prepared; BindingID is populated only when
// the caller attached it to the request (the binding trigger does).
type PostRunInfo struct {
	RunID       int
	WorkspaceID int
	ItemID      *int
	BindingID   int
	Status      string
	Branch      string
	BaseCommit  string
	// TriggeredByUserID is the run's triggering user (0 when unknown). The
	// PR hook uses it as the credential principal on OAuth SCM connections
	// (WI-275).
	TriggeredByUserID int
	// Summary is the agent's finish summary, rendered as the PR note (WI-400).
	// Already sanitized + bounded by the time it reaches the hook.
	Summary string
	// Trigger is the run's trigger context (nil for legacy/triggerless runs).
	// The PR hook reads it to detect a continuation run — one that pushed to an
	// existing PR's head branch — so it comments on that PR instead of opening a
	// new one.
	Trigger *models.RunTrigger
	// Repos carries the per-repo push outcome for a multi-repo run (WI-449),
	// the primary first. Branch is empty for a repo the agent left unchanged
	// (no_changes). For single-repo runs this has one entry mirroring
	// Branch/BaseCommit above; the PR hook prefers Repos when present and falls
	// back to the scalar Branch/BaseCommit otherwise.
	Repos []PostRunRepo
}

// PostRunRepo is one repo's push result handed to the PR hook (WI-449). The run
// service fills only what it knows — RepoSlug + the pushed Branch/BaseCommit;
// the SCM connection and primary flag are resolved by the hook from the binding.
type PostRunRepo struct {
	RepoSlug   string
	Branch     string // empty when the repo had no new commits
	BaseCommit string
}

// BindingInputsResolver derives a binding-backed run's per-run token spec,
// access grants, and runner env at remote claim time, so a remote claim
// prepares the run the same way the local Start path does (WI-195).
// BindingService implements it. It returns (nil, nil, env, nil) for a run
// whose binding mints no token, and (nil, nil, nil, nil) for a run with no
// binding (e.g. action_container) — neither gets token/grant enrichment.
type BindingInputsResolver interface {
	ResolveRunInputs(ctx context.Context, run *models.AgentRun) (*RunInputs, error)
}

// RunInputs bundles everything a binding-backed run needs at remote claim
// time: the per-run token spec, broker grants, repo-prep coordinates, runner
// env, and the per-binding prompt suffix (instructions + skills index,
// WI-258). Nil means "no binding" — the claim proceeds without enrichment.
type RunInputs struct {
	Token  *TokenSpec
	Grants *models.RunGrants
	// Repo is the deprecated single-repo prep input (WI-449); mirrors Repos[0]
	// (the primary). Prefer Repos.
	Repo *JobRepo
	// Repos is every repo a remote runner must check out, primary first
	// (WI-449).
	Repos        []JobRepo
	Env          map[string]string
	PromptSuffix string
}

// PostRunHook is the optional post-finalize callback. Errors are logged
// and swallowed by RunService — a misbehaving hook must not affect the
// run's recorded status.
type PostRunHook interface {
	AfterRun(ctx context.Context, info PostRunInfo)
}

// PostRunHookFunc adapts a plain function to PostRunHook.
type PostRunHookFunc func(ctx context.Context, info PostRunInfo)

// AfterRun implements PostRunHook for PostRunHookFunc.
func (f PostRunHookFunc) AfterRun(ctx context.Context, info PostRunInfo) { f(ctx, info) }

const defaultGlobalCap = 8

// ErrShuttingDown is returned from Start once Shutdown has been called.
var ErrShuttingDown = errors.New("run service is shutting down")

// ErrLocalRunnerDisabled is returned from Start for a local (non-pool) run when
// the service runs orchestration-only (no in-process runner). All execution
// happens on remote runner pools; a binding that resolves to a local run on
// such a server is a misconfiguration. The run row is left untouched.
var ErrLocalRunnerDisabled = errors.New("run service: in-process runner is disabled; route this binding to a remote runner pool")

// RunService orchestrates agent runs against the agent_runs table.
type RunService struct {
	repo          *repository.AgentRunRepository
	runner        Runner
	preparer      *repoprep.Preparer
	tokens        *RunTokenService
	postRunHook   PostRunHook
	queue         chan queuedJob
	now           func() time.Time
	logger        *log.Logger
	initialPrompt string

	mu         sync.Mutex
	shutdown   bool
	wg         sync.WaitGroup // counts runs (queued + in-flight)
	workerWG   sync.WaitGroup // counts in-process pool workers
	shutdownCh chan struct{}
	inflightMu sync.Mutex
	inflight   map[int]context.CancelFunc
	claimsMu   sync.Mutex
	claims     map[int]*claimState

	// bindingInputs derives token/grants/env for a binding-backed run at
	// remote claim time (WI-195). Optional; set via SetBindingInputsResolver
	// after construction to break the BindingService<->RunService cycle.
	bindingInputs BindingInputsResolver
}

// SetBindingInputsResolver wires the binding-input resolver used to enrich
// remote claims. Called once at boot after both services are constructed.
func (s *RunService) SetBindingInputsResolver(r BindingInputsResolver) {
	s.bindingInputs = r
}

// NewRunService constructs a RunService bound to the given repo. The
// returned service holds no background goroutines until Start is invoked.
func NewRunService(repo *repository.AgentRunRepository, opts RunServiceOptions) (*RunService, error) {
	if repo == nil {
		return nil, errors.New("run service: repo is required")
	}
	capacity := opts.GlobalCap
	if capacity <= 0 {
		capacity = defaultGlobalCap
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}
	s := &RunService{
		repo:          repo,
		runner:        opts.Runner,
		preparer:      opts.Preparer,
		tokens:        opts.Tokens,
		postRunHook:   opts.PostRunHook,
		initialPrompt: opts.InitialPrompt,
		queue:         make(chan queuedJob, queueBuffer(capacity)),
		now:           now,
		logger:        logger,
		shutdownCh:    make(chan struct{}),
		inflight:      make(map[int]context.CancelFunc),
		claims:        make(map[int]*claimState),
	}
	// Orchestration-only mode (no Runner): the service still queues runs,
	// enriches remote claims (PrepareRemoteClaim), and finalizes remote
	// results (FinalizeRemote) + fires the post-run hook — but it runs no
	// in-process worker pool, so no agent executes on this host. This is the
	// production wiring: all runs are dispatched to remote runner pools.
	if opts.Runner == nil {
		return s, nil
	}
	// In-process worker pool (decision #7): `capacity` workers each run
	// the shared RunWorker loop with RunService itself as the (local)
	// OrchestratorClient. Pool size is the concurrency cap, which replaced
	// the old global semaphore. Remote pools run the same RunWorker in the
	// agent binary against the HTTP transport. Used by tests that exercise
	// the local execution path directly.
	for i := 0; i < capacity; i++ {
		s.workerWG.Add(1)
		go func() {
			defer s.workerWG.Done()
			RunWorker(context.Background(), s, s.runner, s.logger)
		}()
	}
	return s, nil
}

// Cancel marks an in-flight run for cancellation. Returns true if the run
// was actually in flight and got its ctx canceled; false (with no error)
// if the run is no longer in flight (already terminal, never started, or
// the worker already exited). The terminal status is set by the worker's
// own canceled-by-ctx path, not here, so the DB state always reflects what
// the runner actually saw.
func (s *RunService) Cancel(runID int) bool {
	s.inflightMu.Lock()
	cancel, ok := s.inflight[runID]
	s.inflightMu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

func (s *RunService) registerCancel(runID int, cancel context.CancelFunc) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	s.inflight[runID] = cancel
}

func (s *RunService) unregisterCancel(runID int) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	delete(s.inflight, runID)
}

// Start records a new run in the queued state and dispatches it onto a
// background goroutine. The returned ID can be used to query state via the
// repository. The caller's ctx is used only for the initial DB insert; the
// run itself derives its context from the service so it survives the
// request handler returning.
func (s *RunService) Start(ctx context.Context, req RunRequest) (int, error) {
	if req.WorkspaceID == 0 {
		return 0, errors.New("run service: workspace_id is required")
	}
	// A local (non-pool) run needs the in-process worker pool to execute it.
	// In orchestration-only mode there is no runner and the queue is never
	// drained, so reject before inserting a row that would sit queued forever.
	if req.TargetPoolID == nil && s.runner == nil {
		return 0, ErrLocalRunnerDisabled
	}
	s.mu.Lock()
	if s.shutdown {
		s.mu.Unlock()
		return 0, ErrShuttingDown
	}
	s.mu.Unlock()

	run := &models.AgentRun{
		WorkspaceID: req.WorkspaceID,
		ItemID:      req.ItemID,
		Status:      models.AgentRunStatusQueued,
		Trigger:     req.Trigger,
	}
	if req.BindingID > 0 {
		bID := req.BindingID
		run.BindingID = &bID
	}
	if req.TriggeredByUserID > 0 {
		uID := req.TriggeredByUserID
		run.TriggeredByUserID = &uID
	}
	if req.TargetPoolID != nil {
		run.TargetPoolID = req.TargetPoolID
		run.JobKind = req.JobKind
		// A custom coding-agent image (or an admin container image) for a pool
		// run; empty means the remote runner uses its default image (WI-450).
		run.JobImage = req.JobImage
	}
	runID, err := s.repo.Insert(ctx, run)
	if err != nil {
		return 0, fmt.Errorf("insert agent_run: %w", err)
	}
	// Lifecycle event is best-effort: failure to record it must not block
	// the run from proceeding. Remote runs record which pool they queued for
	// so a stalled run's event log answers "where was this supposed to run?".
	queuedPayload := `{"phase":"queued"}`
	if req.TargetPoolID != nil {
		queuedPayload = fmt.Sprintf(`{"phase":"queued","target_pool_id":%d}`, *req.TargetPoolID)
	}
	if err := s.repo.AppendEvent(ctx, runID, "lifecycle", queuedPayload); err != nil {
		s.logger.Printf("run service: append queued event: %v", err)
	}

	// Remote pool: the run is now queued for a remote runner to claim. The
	// in-process worker pool must not touch it — enrichment (token, grants,
	// env) happens in PrepareRemoteClaim when a runner claims it (WI-195).
	if req.TargetPoolID != nil {
		return runID, nil
	}

	if (req.Repo != nil || len(req.Repos) > 0) && s.preparer == nil {
		return 0, errors.New("run service: request includes a Repo but no Preparer is configured")
	}
	if req.Token != nil && s.tokens == nil {
		return 0, errors.New("run service: request includes a Token but no RunTokenService is configured")
	}

	s.mu.Lock()
	if s.shutdown {
		s.mu.Unlock()
		// Lost the race with Shutdown after the row was inserted: no
		// worker will claim it, so finalize it canceled rather than
		// leave it dangling in queued.
		s.finalize(runID, models.AgentRunStatusCanceled, "shutting down")
		return 0, ErrShuttingDown
	}
	s.wg.Add(1)
	// Enqueue for the worker pool. The run row is already persisted as
	// queued; a worker claims it (admission), prepares the worktree, mints
	// the token, and drives the runner — the run outlives the caller's
	// request ctx. Holding mu across the send orders the enqueue before any
	// concurrent Shutdown so the job is never orphaned; the queue buffer is
	// sized so this send does not block under normal load.
	s.queue <- queuedJob{runID: runID, req: req}
	s.mu.Unlock()
	return runID, nil
}

// invokePostRunHook fires the post-run callback if one is configured.
// Errors are swallowed-with-log; a misbehaving hook must not affect the
// run's recorded status. A 90s ctx caps how long the hook can stall the
// worker before the worker goroutine returns — generous enough that the
// AgentPRService open-PR path can retry a transient SCM failure (a few
// bounded attempts with backoff) instead of leaving the pushed branch
// without a PR.
func (s *RunService) invokePostRunHook(info PostRunInfo) {
	if s.postRunHook == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Printf("run service: post-run hook panic run=%d: %v", info.RunID, r)
			}
		}()
		s.postRunHook.AfterRun(ctx, info)
	}()
}

func (s *RunService) finalize(runID int, status, errMsg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Scrub embedded URL credentials before persistence. errMsg
	// originates from runner output / git CombinedOutput / exec
	// failures, any of which may include a `https://user:pass@host`
	// fragment if a token slipped through somewhere upstream.
	if err := s.repo.Finalize(ctx, runID, status, RedactString(errMsg), s.now()); err != nil {
		s.logger.Printf("run service: finalize run=%d status=%s: %v", runID, status, err)
	}
}

// FinalizeRemote records the terminal verdict for a run executed by a remote
// runner (the in-process path uses Report). It normalizes + finalizes the
// status, emits the terminal event, and fires the post-run hook with the
// branch / base commit the runner reported — so remote runs get the same
// PR-creation + ItemSCMLink writeback as local ones (WI-144). Worktree
// cleanup is the runner's responsibility, so there's none here.
func (s *RunService) FinalizeRemote(ctx context.Context, runID int, result RunnerResult, branch, baseCommit string) error {
	run, err := s.repo.Get(ctx, runID)
	if err != nil {
		return fmt.Errorf("finalize remote: load run %d: %w", runID, err)
	}
	if result.ContainerID != "" {
		if err := s.repo.SetContainerID(ctx, runID, result.ContainerID); err != nil {
			s.logger.Printf("run service: set container_id run=%d: %v", runID, err)
		}
	}
	status := result.Status
	if !models.IsAgentRunTerminal(status) {
		status = models.AgentRunStatusFailed
		if result.Error == "" {
			result.Error = fmt.Sprintf("runner returned non-terminal status %q", result.Status)
		}
	}
	// Compare-and-swap finalize (WI-168): a remote runner credential must not
	// be able to rewrite a run that already finalized or was canceled. If this
	// call did not perform the running→terminal transition, treat the report
	// as a no-op and — crucially — do not re-emit the terminal event or re-run
	// the post-run hook (which would create a duplicate PR).
	transitioned, err := s.repo.FinalizeRunning(ctx, runID, status, RedactString(result.Error), s.now())
	if err != nil {
		return fmt.Errorf("finalize remote: run %d: %w", runID, err)
	}
	if !transitioned {
		s.logger.Printf("run service: ignoring remote result for run=%d (not running)", runID)
		return nil
	}
	if err := s.repo.AppendEvent(ctx, runID, "lifecycle", fmt.Sprintf(`{"phase":%q}`, status)); err != nil {
		s.logger.Printf("run service: append terminal event run=%d: %v", runID, err)
	}
	bindingID := 0
	if run.BindingID != nil {
		bindingID = *run.BindingID
	}
	// WI-197 (finding 6): a remote runner prepares its own worktree off-box and
	// reports the branch + base commit it pushed. The PR hook uses the branch as
	// the PR head ref, so these are untrusted assertions, not facts — validate
	// them here, the single point where remote-reported SCM state reaches the
	// hook (the local in-process path derives both server-side and is trusted).
	branch, baseCommit = s.validateRemoteSCMRefs(ctx, runID, branch, baseCommit)
	// Per-repo results (WI-449): each reported branch is an untrusted assertion,
	// validated the same way as the scalar primary branch before it reaches the
	// PR hook. Repos with no branch (no_changes) are dropped.
	var repos []PostRunRepo
	for _, rr := range result.Repos {
		vb, vbc := s.validateRemoteSCMRefs(ctx, runID, rr.Branch, rr.BaseCommit)
		if vb == "" {
			continue
		}
		repos = append(repos, PostRunRepo{RepoSlug: rr.RepoSlug, Branch: vb, BaseCommit: vbc})
	}
	triggeredBy := 0
	if run.TriggeredByUserID != nil {
		triggeredBy = *run.TriggeredByUserID
	}
	s.invokePostRunHook(PostRunInfo{
		RunID:             runID,
		WorkspaceID:       run.WorkspaceID,
		ItemID:            run.ItemID,
		BindingID:         bindingID,
		Status:            status,
		Branch:            branch,
		BaseCommit:        baseCommit,
		TriggeredByUserID: triggeredBy,
		Summary:           result.Summary,
		Trigger:           run.Trigger,
		Repos:             repos,
	})
	return nil
}

// RecordFailedStart persists a run that could not start at trigger time —
// e.g. the triggering user has no connected SCM account on an OAuth
// connection (WI-275) — directly in the failed state, with the queued and
// failed lifecycle events, so the refused trigger is visible in the runs
// UI instead of vanishing into a server log. Nothing is dispatched and no
// post-run hook fires. Returns the run id.
func (s *RunService) RecordFailedStart(ctx context.Context, req RunRequest, reason string) (int, error) {
	if req.WorkspaceID == 0 {
		return 0, errors.New("run service: workspace_id is required")
	}
	run := &models.AgentRun{
		WorkspaceID: req.WorkspaceID,
		ItemID:      req.ItemID,
		Status:      models.AgentRunStatusQueued,
		Trigger:     req.Trigger,
	}
	if req.BindingID > 0 {
		bID := req.BindingID
		run.BindingID = &bID
	}
	if req.TriggeredByUserID > 0 {
		uID := req.TriggeredByUserID
		run.TriggeredByUserID = &uID
	}
	if req.TargetPoolID != nil {
		run.TargetPoolID = req.TargetPoolID
		run.JobKind = req.JobKind
		// A custom coding-agent image (or an admin container image) for a pool
		// run; empty means the remote runner uses its default image (WI-450).
		run.JobImage = req.JobImage
	}
	runID, err := s.repo.Insert(ctx, run)
	if err != nil {
		return 0, fmt.Errorf("insert agent_run: %w", err)
	}
	red := RedactString(reason)
	if err := s.repo.AppendEvent(ctx, runID, "lifecycle", `{"phase":"queued"}`); err != nil {
		s.logger.Printf("run service: append queued event: %v", err)
	}
	if err := s.repo.AppendEvent(ctx, runID, "lifecycle", fmt.Sprintf(`{"phase":"failed","reason":%q}`, red)); err != nil {
		s.logger.Printf("run service: append failed event: %v", err)
	}
	if err := s.repo.Finalize(ctx, runID, models.AgentRunStatusFailed, red, s.now()); err != nil {
		return runID, fmt.Errorf("finalize failed-start run %d: %w", runID, err)
	}
	return runID, nil
}

// validateRemoteSCMRefs constrains the branch + base commit a remote runner
// reports for run runID before they reach the post-run PR hook (WI-197,
// finding 6). A non-empty branch must equal the run's canonical push ref
// (agent-runs/run-<id>) — the same ref the git-proxy gates pushes to — so a PR
// head can only ever be that branch; any other value is dropped together with
// the base commit, since opening a PR from an unverified ref is exactly what
// this guards against. A non-empty base commit must be a git object id (40- or
// 64-char hex); a malformed one is dropped on its own, leaving a valid branch
// intact. An empty branch/base is legitimate (the agent produced nothing to
// push) and passes through — the hook treats an empty branch as "no PR". Every
// rejection is logged and emitted as a warning event so the anomaly is visible
// on the run timeline.
func (s *RunService) validateRemoteSCMRefs(ctx context.Context, runID int, branch, baseCommit string) (validBranch, validBase string) {
	if branch != "" {
		expected := fmt.Sprintf("agent-runs/run-%d", runID)
		if branch != expected {
			s.logger.Printf("run service: remote run=%d reported branch %q, expected %q; dropping branch + base (no PR)",
				runID, clipForEvent(branch), expected)
			_ = s.repo.AppendEvent(ctx, runID, "warning", fmt.Sprintf(
				`{"phase":"scm_ref_rejected","field":"branch","reported":%q,"expected":%q}`,
				clipForEvent(branch), expected))
			return "", ""
		}
	}
	if baseCommit != "" && !isGitObjectID(baseCommit) {
		s.logger.Printf("run service: remote run=%d reported malformed base commit %q; dropping base",
			runID, clipForEvent(baseCommit))
		_ = s.repo.AppendEvent(ctx, runID, "warning", fmt.Sprintf(
			`{"phase":"scm_ref_rejected","field":"base_commit","reported":%q}`, clipForEvent(baseCommit)))
		return branch, ""
	}
	return branch, baseCommit
}

// isGitObjectID reports whether s is a full git object id: 40 hex chars
// (SHA-1) or 64 (SHA-256). Abbreviated ids are rejected — the runner reports
// the full rev-parse output, so anything shorter is anomalous.
func isGitObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isHex := c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
		if !isHex {
			return false
		}
	}
	return true
}

// clipForEvent bounds an untrusted runner-reported value before it is written
// to a log line or persisted as an event payload, so a hostile runner can't
// inflate either with a multi-megabyte string.
func clipForEvent(s string) string {
	const maxLen = 120
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// mintTokenAndGrants mints the per-run ws token and persists the run's
// access-layer grants bound to it, returning the plaintext token for
// $WS_TOKEN. Shared by the local claim preamble (claimNext) and the remote
// claim enrichment (PrepareRemoteClaim) so both prepare a run identically
// (WI-195, findings 1 & 7). grants may be nil (no brokered access). ref, when
// non-empty, sets the git grant's single pushable ref — the prepared worktree
// branch for local runs, the run-branch namespace for remote runs. Grant
// persistence is best-effort: a failure leaves the run without grants, which
// the brokers treat as deny — safe, just no brokered access.
func (s *RunService) mintTokenAndGrants(ctx context.Context, runID int, spec TokenSpec, grants *models.RunGrants, refByRepo map[string]string) (string, error) {
	minted, err := s.tokens.Mint(ctx, MintRequest(spec))
	if err != nil {
		return "", err
	}
	_ = s.repo.AppendEvent(ctx, runID, "lifecycle", fmt.Sprintf(
		`{"phase":"token_minted","token_id":%d,"expires_at":%q}`,
		minted.TokenID, minted.ExpiresAt.Format(time.RFC3339)))
	if grants != nil {
		// Fill each repo's push ref (the branch the run may push) from the
		// per-repo branch map, copying so the caller's grants aren't mutated
		// (WI-449). A repo with no ref stays read-only (clone/fetch, no push).
		g := *grants
		if len(g.GitRepos) > 0 {
			repos := make([]models.GitGrant, len(g.GitRepos))
			copy(repos, g.GitRepos)
			for i := range repos {
				if ref := refByRepo[repos[i].Repo]; ref != "" {
					repos[i].Ref = ref
				}
			}
			g.GitRepos = repos
		}
		if g.Git != nil {
			gg := *g.Git
			if ref := refByRepo[gg.Repo]; ref != "" {
				gg.Ref = ref
			}
			g.Git = &gg
		}
		if err := s.repo.SetGrants(ctx, runID, minted.TokenID, &g, s.now()); err != nil {
			s.logger.Printf("run service: set grants run=%d: %v", runID, err)
		}
	}
	return minted.Token, nil
}

// applyLLMProxyEnv wires the agent container to reach the model only through
// the run-scoped llm-proxy broker (WI-238): LLM_BASE_URL points at
// <WS_API_URL>/llm-proxy/<runID> (the agent appends /v1/chat/completions) and
// LLM_API_KEY carries the per-run token, which ProxyLLM validates and swaps for
// the real provider credential server-side. No raw provider key ever reaches
// the (untrusted) container. A no-op when the run has no LLM grant, or when no
// API base URL is known (the agent then fails loudly with no LLM_BASE_URL
// rather than silently falling back to a direct provider call). Shared by the
// local claim preamble and the remote claim enrichment so both transports
// build identical LLM env.
func applyLLMProxyEnv(env map[string]string, grants *models.RunGrants, runID int, token string) {
	if grants == nil || grants.LLM == nil {
		return
	}
	base := strings.TrimRight(env["WS_API_URL"], "/")
	if base == "" {
		return
	}
	env["LLM_BASE_URL"] = fmt.Sprintf("%s/llm-proxy/%d", base, runID)
	env["LLM_API_KEY"] = token
}

// FailRemoteClaim marks a just-claimed remote run failed when claim enrichment
// could not complete (e.g. PrepareRemoteClaim errored after ClaimQueued already
// moved the run to running). Without this the run would sit in `running` with no
// token or grants, holding a pool slot indefinitely (WI-238 security Phase 8).
// CAS-guarded via FinalizeRunning so it never overwrites a run that already
// reached a terminal state; the reason is redacted before it is persisted or
// emitted. No post-run hook fires — no work was produced.
func (s *RunService) FailRemoteClaim(ctx context.Context, runID int, reason string) {
	red := RedactString(reason)
	transitioned, err := s.repo.FinalizeRunning(ctx, runID, models.AgentRunStatusFailed, red, s.now())
	if err != nil {
		s.logger.Printf("run service: fail remote claim run=%d: %v", runID, err)
		return
	}
	if transitioned {
		if err := s.repo.AppendEvent(ctx, runID, "lifecycle", fmt.Sprintf(`{"phase":"failed","reason":%q}`, red)); err != nil {
			s.logger.Printf("run service: append claim-fail event run=%d: %v", runID, err)
		}
	}
}

// PrepareRemoteClaim enriches a run a remote runner just claimed: it derives
// the run's token + grants from its binding (via the resolver), mints the
// per-run token, persists the grants bound to it (git ref = the run-branch
// namespace the remote runner pushes to), and returns the JobSpec the runner
// executes — with $WS_TOKEN and run/workspace/item context in Env. A run with
// no binding (e.g. action_container) is returned with no enrichment. This is
// the remote counterpart of the local claimNext preamble (WI-195).
func (s *RunService) PrepareRemoteClaim(ctx context.Context, run *models.AgentRun) (JobSpec, error) {
	spec := JobSpec{RunID: run.ID, Kind: run.JobKind, Image: run.JobImage, InitialPrompt: s.initialPrompt}
	if s.bindingInputs == nil || s.tokens == nil || run.BindingID == nil {
		return spec, nil
	}
	inputs, err := s.bindingInputs.ResolveRunInputs(ctx, run)
	if err != nil {
		return JobSpec{}, fmt.Errorf("remote claim: resolve run inputs: %w", err)
	}
	if inputs == nil {
		inputs = &RunInputs{}
	}
	spec.InitialPrompt += inputs.PromptSuffix
	env := inputs.Env
	if env == nil {
		env = map[string]string{}
	}
	env["AGENT_RUN_ID"] = fmt.Sprintf("%d", run.ID)
	if inputs.Token != nil {
		// Per-repo push refs the remote runner will create (WI-449): the fresh
		// per-run branch for each repo, or the continuation head branch for the
		// one repo that continues a PR. Each git grant may push only its ref.
		runBranch := fmt.Sprintf("agent-runs/run-%d", run.ID)
		refByRepo := remoteRefByRepo(inputs.Grants, inputs, runBranch)
		token, err := s.mintTokenAndGrants(ctx, run.ID, *inputs.Token, inputs.Grants, refByRepo)
		if err != nil {
			return JobSpec{}, fmt.Errorf("remote claim: mint token run=%d: %w", run.ID, err)
		}
		env["WS_TOKEN"] = token
		applyLLMProxyEnv(env, inputs.Grants, run.ID, token)
	}
	spec.Env = env
	// A remote runner prepares its own checkout(s) from these; the host
	// WorkspacePath stays empty on the wire. Repo mirrors the primary for older
	// runners that read the single field.
	spec.Repo = inputs.Repo
	spec.Repos = inputs.Repos
	return spec, nil
}

// remoteRefByRepo maps each granted repo to the branch the run may push
// (WI-449): the fresh per-run branch by default, overridden by a continuation
// head branch for the one repo that continues a PR. Keyed off the grants (the
// authoritative set of repos the run can reach) so it works even when the
// resolver supplies grants without per-repo JobRepo prep inputs.
func remoteRefByRepo(grants *models.RunGrants, inputs *RunInputs, runBranch string) map[string]string {
	refs := map[string]string{}
	if grants != nil {
		for _, gg := range grants.GitRepos {
			refs[gg.Repo] = runBranch
		}
		if grants.Git != nil {
			refs[grants.Git.Repo] = runBranch
		}
	}
	// Continuation overrides: the runner lands commits on the existing PR head
	// branch for the repo that continues, not a fresh per-run branch.
	repos := inputs.Repos
	if len(repos) == 0 && inputs.Repo != nil {
		repos = []JobRepo{*inputs.Repo}
	}
	for _, jr := range repos {
		if jr.ContinueBranch != "" {
			refs[jr.Slug] = jr.ContinueBranch
		}
	}
	return refs
}

// Shutdown stops accepting new runs and waits for in-flight runs to drain.
// Cancellation of in-flight runs is propagated through their context.
func (s *RunService) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.shutdown {
		s.mu.Unlock()
		return nil
	}
	s.shutdown = true
	close(s.shutdownCh)
	s.mu.Unlock()

	// Closing shutdownCh makes the workers drain any still-queued runs as
	// canceled and then exit; in-flight runs see their ctx canceled and
	// finalize. wg drops to zero once every run (queued + in-flight) is
	// accounted for; workerWG drops once the pool has exited.
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		s.workerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait blocks until all currently-dispatched runs complete. Used by tests
// to deterministically wait for an end state; production code should use
// Shutdown.
func (s *RunService) Wait() {
	s.wg.Wait()
}

// HasTokens reports whether a RunTokenService is configured. Used by
// upstream callers (BindingService) to know whether to build a TokenSpec
// for the run.
func (s *RunService) HasTokens() bool {
	return s.tokens != nil
}

// LocalExecutionEnabled reports whether the service runs an in-process worker
// pool. It is false on an orchestration-only server, where all runs execute on
// remote runner pools. Callers that can only run locally (binding test runs)
// use it to fail fast instead of queuing a run nothing will pick up.
func (s *RunService) LocalExecutionEnabled() bool {
	return s.runner != nil
}

// CountRunsForBindingSince proxies to the repository so BindingService
// can enforce a binding's max_runs_per_day budget without taking on a
// direct dependency on the agent_runs repo.
func (s *RunService) CountRunsForBindingSince(ctx context.Context, bindingID int, since time.Time) (int, error) {
	return s.repo.CountForBindingSince(ctx, bindingID, since)
}

// CountActiveRunsForBindingItem proxies to the repository for the
// comment-@mention trigger's per-item dedup check (WI-264).
func (s *RunService) CountActiveRunsForBindingItem(ctx context.Context, bindingID, itemID int) (int, error) {
	return s.repo.CountActiveForBindingItem(ctx, bindingID, itemID)
}

// LatestRunForItem returns the most recent run on an item, or nil when the
// item has never had one. Backs the manual "Re-run" trigger, which derives the
// agent to re-run from the last run's binding.
func (s *RunService) LatestRunForItem(ctx context.Context, itemID int) (*models.AgentRun, error) {
	runs, err := s.repo.ListForItem(ctx, itemID, 1, 0)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return runs[0], nil
}
