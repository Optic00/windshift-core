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
}

// RunInput is what RunService hands to a Runner when work is admitted:
// the run id, the host path containing the prepared worktree (empty if no
// repo was attached to the request), and any orchestrator-supplied env
// vars to forward into the container.
type RunInput struct {
	RunID         int
	WorkspacePath string
	Env           map[string]string
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
	Repo        *RepoSpec
	Token       *TokenSpec
	Env         map[string]string
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
// runs in-flight across the whole process; the queueing semaphore admits
// goroutines past Start, so a backed-up queue parks here without blocking
// the HTTP handler that called Start. Worktrees is optional and only
// required when callers actually attach Repo to a RunRequest. Tokens is
// optional and only required when callers attach a TokenSpec. PostRunHook
// is optional and fires once per run after the terminal status is
// finalized — that's where WI-90's PR creation + ItemSCMLink writeback
// live.
type RunServiceOptions struct {
	GlobalCap   int
	Runner      Runner
	Worktrees   *WorktreeManager
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
	worktrees   *WorktreeManager
	tokens      *RunTokenService
	postRunHook PostRunHook
	sem         chan struct{}
	now         func() time.Time
	logger      *log.Logger

	mu         sync.Mutex
	shutdown   bool
	wg         sync.WaitGroup
	shutdownCh chan struct{}
	inflightMu sync.Mutex
	inflight   map[int]context.CancelFunc
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
	return &RunService{
		repo:        repo,
		runner:      opts.Runner,
		worktrees:   opts.Worktrees,
		tokens:      opts.Tokens,
		postRunHook: opts.PostRunHook,
		sem:         make(chan struct{}, capacity),
		now:         now,
		logger:      logger,
		shutdownCh:  make(chan struct{}),
		inflight:    make(map[int]context.CancelFunc),
	}, nil
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
	runID, err := s.repo.Insert(ctx, run)
	if err != nil {
		return 0, fmt.Errorf("insert agent_run: %w", err)
	}
	// Lifecycle event is best-effort: failure to record it must not block
	// the run from proceeding.
	if err := s.repo.AppendEvent(ctx, runID, "lifecycle", `{"phase":"queued"}`); err != nil {
		s.logger.Printf("run service: append queued event: %v", err)
	}

	if req.Repo != nil && s.worktrees == nil {
		return 0, errors.New("run service: request includes a Repo but no WorktreeManager is configured")
	}
	if req.Token != nil && s.tokens == nil {
		return 0, errors.New("run service: request includes a Token but no RunTokenService is configured")
	}

	s.wg.Add(1)
	// The caller's ctx is request-scoped; the run must outlive it.
	// execute() derives a service-scoped ctx wired to shutdownCh so
	// cancellation flows from process shutdown instead of HTTP request
	// teardown.
	go s.execute(runID, req) //nolint:gosec // G118: intentional Background ctx; see comment above.
	return runID, nil
}

// execute is the per-run worker. Acquires the global semaphore, marks the
// run running, invokes the runner with an event sink wired to the repo,
// then finalizes the run with the runner's verdict.
func (s *RunService) execute(runID int, req RunRequest) {
	defer s.wg.Done()

	// Detach from the request ctx but honor service shutdown via cancel
	// derived from the shutdown channel. The same cancel is registered
	// in the inflight map so RunService.Cancel can reach the worker.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.registerCancel(runID, cancel)
	defer s.unregisterCancel(runID)
	go func() {
		select {
		case <-s.shutdownCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		s.finalize(runID, models.AgentRunStatusCanceled, "shutdown before admission")
		return
	}
	defer func() { <-s.sem }()

	now := s.now()
	if err := s.repo.MarkRunning(ctx, runID, "", now); err != nil {
		s.logger.Printf("run service: mark running run=%d: %v", runID, err)
		s.finalize(runID, models.AgentRunStatusFailed, fmt.Sprintf("mark running: %v", err))
		return
	}
	if err := s.repo.AppendEvent(ctx, runID, "lifecycle", `{"phase":"running"}`); err != nil {
		s.logger.Printf("run service: append running event run=%d: %v", runID, err)
	}

	sink := func(eventType, payloadJSON string) error {
		// Sink writes are best-effort but visible: the orchestrator may
		// want them in audit even if a downstream subscriber failed.
		return s.repo.AppendEvent(ctx, runID, eventType, payloadJSON)
	}

	var (
		workspacePath string
		runBranch     string
		runBaseCommit string
	)
	if req.Repo != nil {
		pw, err := s.worktrees.Prepare(ctx, *req.Repo, runID)
		if err != nil {
			s.logger.Printf("run service: prepare worktree run=%d: %v", runID, err)
			s.finalize(runID, models.AgentRunStatusFailed, fmt.Sprintf("prepare worktree: %v", err))
			s.invokePostRunHook(PostRunInfo{
				RunID: runID, WorkspaceID: req.WorkspaceID, ItemID: req.ItemID,
				BindingID: req.BindingID, Status: models.AgentRunStatusFailed,
			})
			return
		}
		workspacePath = pw.Path
		runBranch = pw.Branch
		runBaseCommit = pw.BaseCommit
		_ = sink("lifecycle", fmt.Sprintf(`{"phase":"worktree_ready","path":%q,"branch":%q,"base_commit":%q}`,
			pw.Path, pw.Branch, pw.BaseCommit))
		defer func() {
			if err := s.worktrees.Cleanup(context.Background(), pw); err != nil {
				s.logger.Printf("run service: cleanup worktree run=%d: %v", runID, err)
			}
		}()
	}

	// Build the env the runner will see. Caller-supplied keys come
	// first; the orchestrator's own injections (WS_TOKEN) overwrite on
	// conflict so a confused caller can't smuggle in their own token.
	env := make(map[string]string, len(req.Env)+1)
	for k, v := range req.Env {
		env[k] = v
	}
	if req.Token != nil {
		minted, err := s.tokens.Mint(ctx, MintRequest{
			ActingUserID: req.Token.ActingUserID,
			Scopes:       req.Token.Scopes,
			TTL:          req.Token.TTL,
			Name:         req.Token.Name,
		})
		if err != nil {
			s.logger.Printf("run service: mint ws token run=%d: %v", runID, err)
			s.finalize(runID, models.AgentRunStatusFailed, fmt.Sprintf("mint ws token: %v", err))
			return
		}
		env["WS_TOKEN"] = minted.Token
		_ = sink("lifecycle", fmt.Sprintf(`{"phase":"token_minted","token_id":%d,"expires_at":%q}`,
			minted.TokenID, minted.ExpiresAt.Format(time.RFC3339)))
	}

	result := s.runner.Run(ctx, RunInput{RunID: runID, WorkspacePath: workspacePath, Env: env}, sink)
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
	s.finalize(runID, status, result.Error)
	_ = sink("lifecycle", fmt.Sprintf(`{"phase":%q}`, status))

	s.invokePostRunHook(PostRunInfo{
		RunID:       runID,
		WorkspaceID: req.WorkspaceID,
		ItemID:      req.ItemID,
		BindingID:   req.BindingID,
		Status:      status,
		Branch:      runBranch,
		BaseCommit:  runBaseCommit,
	})
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
	if err := s.repo.Finalize(ctx, runID, status, errMsg, s.now()); err != nil {
		s.logger.Printf("run service: finalize run=%d status=%s: %v", runID, status, err)
	}
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

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
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
