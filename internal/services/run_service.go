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
}

// RunInput is what RunService hands to a Runner when work is admitted:
// the run id, the host path containing the prepared worktree (empty if no
// repo was attached to the request), and any orchestrator-supplied env
// vars to forward into the container.
type RunInput struct {
	RunID         int
	WorkspacePath string
	Env           map[string]string
	// Kind + Image let a kind-dispatching runner pick its execution mode
	// (WI-146): coding_agent (pi) vs action_container / ci_task (run Image as
	// a plain container). Empty Kind means coding_agent.
	Kind  string
	Image string
}

// Runner executes the actual work of a run: spawning a container, driving
// pi via RPC, and streaming events back through the sink. The skeleton
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
// Repo is optional: when set, RunService asks the WorktreeManager to
// prepare a worktree before the runner sees the run; when nil, the runner
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
	Repo        *repoprep.RepoSpec
	Token       *TokenSpec
	Env         map[string]string
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
	// TargetPoolID, when set, routes the run to a remote runner_pool instead
	// of the local in-process pool (WI-195). A remote run is persisted queued
	// for the pool and enriched (token + grants + env) at claim time by the
	// remote claim path, so Repo/Token/Grants/Env on this request are ignored
	// for remote runs — the orchestrator never sees the work locally.
	TargetPoolID *int
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
// handler that called it. Worktrees is optional and only
// required when callers actually attach Repo to a RunRequest. Tokens is
// optional and only required when callers attach a TokenSpec. PostRunHook
// is optional and fires once per run after the terminal status is
// finalized — that's where WI-90's PR creation + ItemSCMLink writeback
// live.
type RunServiceOptions struct {
	GlobalCap   int
	Runner      Runner
	Preparer    *repoprep.Preparer
	Tokens      *RunTokenService
	PostRunHook PostRunHook
	Now         func() time.Time // injected for deterministic tests
	Logger      *log.Logger
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
}

// BindingInputsResolver derives a binding-backed run's per-run token spec,
// access grants, and runner env at remote claim time, so a remote claim
// prepares the run the same way the local Start path does (WI-195).
// BindingService implements it. It returns (nil, nil, env, nil) for a run
// whose binding mints no token, and (nil, nil, nil, nil) for a run with no
// binding (e.g. action_container) — neither gets token/grant enrichment.
type BindingInputsResolver interface {
	ResolveRunInputs(ctx context.Context, run *models.AgentRun) (*TokenSpec, *models.RunGrants, map[string]string, error)
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

// RunService orchestrates agent runs against the agent_runs table.
type RunService struct {
	repo        *repository.AgentRunRepository
	runner      Runner
	preparer    *repoprep.Preparer
	tokens      *RunTokenService
	postRunHook PostRunHook
	queue       chan queuedJob
	now         func() time.Time
	logger      *log.Logger

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
	if opts.Runner == nil {
		return nil, errors.New("run service: runner is required")
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
		repo:        repo,
		runner:      opts.Runner,
		preparer:    opts.Preparer,
		tokens:      opts.Tokens,
		postRunHook: opts.PostRunHook,
		queue:       make(chan queuedJob, queueBuffer(capacity)),
		now:         now,
		logger:      logger,
		shutdownCh:  make(chan struct{}),
		inflight:    make(map[int]context.CancelFunc),
		claims:      make(map[int]*claimState),
	}
	// In-process worker pool (decision #7): `capacity` workers each run
	// the shared RunWorker loop with RunService itself as the (local)
	// OrchestratorClient. Pool size is the concurrency cap, which replaced
	// the old global semaphore. Remote pools (later phases) run the same
	// RunWorker in the agent binary against the HTTP transport.
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
	}
	if req.BindingID > 0 {
		bID := req.BindingID
		run.BindingID = &bID
	}
	if req.TargetPoolID != nil {
		run.TargetPoolID = req.TargetPoolID
		run.JobKind = req.JobKind
	}
	runID, err := s.repo.Insert(ctx, run)
	if err != nil {
		return 0, fmt.Errorf("insert agent_run: %w", err)
	}
	// Lifecycle event is best-effort: failure to record it must not block
	// the run from proceeding.
	if err := s.repo.AppendEvent(ctx, runID, "lifecycle", `{"phase":"queued"}`); err != nil {
		s.logger.Printf("run service: append queued event: %v", err)
	}

	// Remote pool: the run is now queued for a remote runner to claim. The
	// in-process worker pool must not touch it — enrichment (token, grants,
	// env) happens in PrepareRemoteClaim when a runner claims it (WI-195).
	if req.TargetPoolID != nil {
		return runID, nil
	}

	if req.Repo != nil && s.preparer == nil {
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
// run's recorded status. A 30s ctx caps how long the hook can stall the
// worker before the worker goroutine returns.
func (s *RunService) invokePostRunHook(info PostRunInfo) {
	if s.postRunHook == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	s.invokePostRunHook(PostRunInfo{
		RunID:       runID,
		WorkspaceID: run.WorkspaceID,
		ItemID:      run.ItemID,
		BindingID:   bindingID,
		Status:      status,
		Branch:      branch,
		BaseCommit:  baseCommit,
	})
	return nil
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
func (s *RunService) mintTokenAndGrants(ctx context.Context, runID int, spec TokenSpec, grants *models.RunGrants, ref string) (string, error) {
	minted, err := s.tokens.Mint(ctx, MintRequest(spec))
	if err != nil {
		return "", err
	}
	_ = s.repo.AppendEvent(ctx, runID, "lifecycle", fmt.Sprintf(
		`{"phase":"token_minted","token_id":%d,"expires_at":%q}`,
		minted.TokenID, minted.ExpiresAt.Format(time.RFC3339)))
	if grants != nil {
		g := *grants
		if g.Git != nil && ref != "" {
			gg := *g.Git
			gg.Ref = ref
			g.Git = &gg
		}
		if err := s.repo.SetGrants(ctx, runID, minted.TokenID, &g, s.now()); err != nil {
			s.logger.Printf("run service: set grants run=%d: %v", runID, err)
		}
	}
	return minted.Token, nil
}

// PrepareRemoteClaim enriches a run a remote runner just claimed: it derives
// the run's token + grants from its binding (via the resolver), mints the
// per-run token, persists the grants bound to it (git ref = the run-branch
// namespace the remote runner pushes to), and returns the JobSpec the runner
// executes — with $WS_TOKEN and run/workspace/item context in Env. A run with
// no binding (e.g. action_container) is returned with no enrichment. This is
// the remote counterpart of the local claimNext preamble (WI-195).
func (s *RunService) PrepareRemoteClaim(ctx context.Context, run *models.AgentRun) (JobSpec, error) {
	spec := JobSpec{RunID: run.ID, Kind: run.JobKind, Image: run.JobImage}
	if s.bindingInputs == nil || s.tokens == nil || run.BindingID == nil {
		return spec, nil
	}
	tokenSpec, grants, env, err := s.bindingInputs.ResolveRunInputs(ctx, run)
	if err != nil {
		return JobSpec{}, fmt.Errorf("remote claim: resolve run inputs: %w", err)
	}
	if env == nil {
		env = map[string]string{}
	}
	env["AGENT_RUN_ID"] = fmt.Sprintf("%d", run.ID)
	if tokenSpec != nil {
		token, err := s.mintTokenAndGrants(ctx, run.ID, *tokenSpec, grants, fmt.Sprintf("agent-runs/run-%d", run.ID))
		if err != nil {
			return JobSpec{}, fmt.Errorf("remote claim: mint token run=%d: %w", run.ID, err)
		}
		env["WS_TOKEN"] = token
	}
	spec.Env = env
	return spec, nil
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

// CountRunsForBindingSince proxies to the repository so BindingService
// can enforce a binding's max_runs_per_day budget without taking on a
// direct dependency on the agent_runs repo.
func (s *RunService) CountRunsForBindingSince(ctx context.Context, bindingID int, since time.Time) (int, error) {
	return s.repo.CountForBindingSince(ctx, bindingID, since)
}
