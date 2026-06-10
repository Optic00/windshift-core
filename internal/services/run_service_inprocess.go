package services

import (
	"context"
	"fmt"

	"windshift/internal/models"
	"windshift/internal/repoprep"
)

// This file is the in-process (local) transport for the unified runner
// model (Initiative WI-141, decision #7). RunService itself implements
// OrchestratorClient: the worker pool started in NewRunService runs the
// same claim -> execute -> report loop a remote agent runs, but Claim /
// Emit / Report resolve to direct calls on RunService rather than HTTPS.
//
// The orchestrator-side state machine that used to live inline in
// RunService.execute (admission, mark-running, worktree prep, token mint,
// finalize, worktree cleanup, post-run hook) lives here now, split across
// claimNext (everything up to handing the job to the runner) and Report
// (everything after the runner returns).
var _ OrchestratorClient = (*RunService)(nil)

// queuedJob is one admitted-pending run handed from Start to the worker
// pool through the in-process queue.
type queuedJob struct {
	runID int
	req   RunRequest
}

// claimState is the per-run bookkeeping kept between claim and Report so
// Report can finalize, clean up the worktree, and build PostRunInfo
// without re-deriving anything.
type claimState struct {
	req        RunRequest
	checkout   *repoprep.Prepared
	path       string
	branch     string
	baseCommit string
	ephemeral  bool
	cancel     context.CancelFunc
}

// queueBuffer sizes the in-process job queue. It is generous relative to
// the concurrency cap so Start does not block under normal load; a queue
// this deep only fills under pathological backpressure.
func queueBuffer(capacity int) int {
	b := capacity * 128
	if b < 1024 {
		b = 1024
	}
	return b
}

// claimNext pulls the next admitted job off the queue and runs the
// orchestrator-side preamble (mark running, worktree prep, token mint),
// returning a ClaimedJob whose Ctx is the per-run context the worker
// drives the runner with. A job whose preamble fails is finalized in place
// and the loop moves on to the next. It returns nil only when the service
// is shutting down, at which point any still-queued runs are drained as
// canceled. This is the in-process realization of OrchestratorClient.Claim.
func (s *RunService) claimNext() *ClaimedJob {
	for {
		var job queuedJob
		select {
		case job = <-s.queue:
		case <-s.shutdownCh:
			// Drain still-queued runs as canceled, then stop. Channel
			// receive is safe across workers; whichever worker wins
			// finalizes the run.
			for {
				select {
				case j := <-s.queue:
					s.finalize(j.runID, models.AgentRunStatusCanceled, "shutdown before admission")
					s.wg.Done()
				default:
					return nil
				}
			}
		}

		// Per-run context, wired to shutdown so RunService.Cancel and
		// process shutdown both reach the in-flight runner.
		runCtx, cancel := context.WithCancel(context.Background())
		s.registerCancel(job.runID, cancel)
		go func() {
			select {
			case <-s.shutdownCh:
				cancel()
			case <-runCtx.Done():
			}
		}()

		now := s.now()
		if err := s.repo.MarkRunning(runCtx, job.runID, "", now); err != nil {
			s.logger.Printf("run service: mark running run=%d: %v", job.runID, err)
			s.failClaim(job, cancel, fmt.Sprintf("mark running: %v", err), false)
			continue
		}
		if err := s.repo.AppendEvent(runCtx, job.runID, "lifecycle", `{"phase":"running"}`); err != nil {
			s.logger.Printf("run service: append running event run=%d: %v", job.runID, err)
		}

		st := claimState{req: job.req, ephemeral: job.req.Ephemeral, cancel: cancel}

		if job.req.Repo != nil {
			pw, err := s.preparer.Prepare(runCtx, *job.req.Repo, job.runID)
			if err != nil {
				s.logger.Printf("run service: prepare checkout run=%d: %v", job.runID, err)
				// Checkout-prep failure fires the post-run hook (matches
				// the prior inline behavior).
				s.failClaim(job, cancel, fmt.Sprintf("prepare checkout: %v", err), true)
				continue
			}
			st.checkout = pw
			st.path = pw.Path
			st.branch = pw.Branch
			st.baseCommit = pw.BaseCommit
			_ = s.repo.AppendEvent(runCtx, job.runID, "lifecycle", fmt.Sprintf(
				`{"phase":"worktree_ready","path":%q,"branch":%q,"base_commit":%q}`,
				pw.Path, pw.Branch, pw.BaseCommit))
		}

		// Caller-supplied env first; the orchestrator's own injections
		// (WS_TOKEN) overwrite on conflict so a confused caller cannot
		// smuggle in its own token. The token mint + grant snapshot (bound to
		// the minted token, git ref = the prepared worktree branch) is the
		// shared preamble the remote claim path also runs (WI-195).
		env := make(map[string]string, len(job.req.Env)+1)
		for k, v := range job.req.Env {
			env[k] = v
		}
		if job.req.Token != nil {
			token, err := s.mintTokenAndGrants(runCtx, job.runID, *job.req.Token, job.req.Grants, st.branch)
			if err != nil {
				s.logger.Printf("run service: mint ws token run=%d: %v", job.runID, err)
				// Token-mint failure does not fire the hook (matches the
				// prior inline behavior).
				s.failClaim(job, cancel, fmt.Sprintf("mint ws token: %v", err), false)
				continue
			}
			env["WS_TOKEN"] = token
			applyLLMProxyEnv(env, job.req.Grants, job.runID, token)
		}

		s.claimsMu.Lock()
		s.claims[job.runID] = &st
		s.claimsMu.Unlock()

		initialPrompt := s.initialPrompt
		if job.req.InitialPrompt != "" {
			initialPrompt = job.req.InitialPrompt
		}
		// Per-binding instructions + skills index ride as a suffix on top of
		// whichever base prompt won (WI-258).
		initialPrompt += job.req.InitialPromptSuffix
		return &ClaimedJob{Spec: JobSpec{RunID: job.runID, WorkspacePath: st.path, Env: env, InitialPrompt: initialPrompt, Kind: job.req.JobKind, Image: job.req.JobImage}, Ctx: runCtx}
	}
}

// failClaim records a terminal failed status for a run whose preamble
// failed, releases its accounting, and (for the cases that warrant it)
// fires the post-run hook. It mirrors the early-return finalize paths the
// old inline execute used.
func (s *RunService) failClaim(job queuedJob, cancel context.CancelFunc, msg string, hook bool) {
	s.finalize(job.runID, models.AgentRunStatusFailed, msg)
	if hook {
		s.invokePostRunHook(PostRunInfo{
			RunID:             job.runID,
			WorkspaceID:       job.req.WorkspaceID,
			ItemID:            job.req.ItemID,
			BindingID:         job.req.BindingID,
			Status:            models.AgentRunStatusFailed,
			TriggeredByUserID: job.req.TriggeredByUserID,
		})
	}
	cancel()
	s.unregisterCancel(job.runID)
	s.wg.Done()
}

// Claim implements OrchestratorClient: the in-process transport for the
// shared RunWorker loop. It blocks on the in-memory queue (honoring
// shutdown) and returns (nil, nil) when the service is shutting down. The
// per-run abort context rides on ClaimedJob.Ctx.
func (s *RunService) Claim(_ context.Context) (*ClaimedJob, error) {
	return s.claimNext(), nil
}

// Emit implements OrchestratorClient: it appends one event to the run's
// agent_run_events stream.
func (s *RunService) Emit(ctx context.Context, runID int, eventType, payloadJSON string) error {
	return s.repo.AppendEvent(ctx, runID, eventType, payloadJSON)
}

// Report implements OrchestratorClient: it records the runner's terminal
// verdict, emits the terminal lifecycle event, cleans up the worktree,
// fires the post-run hook, and releases the run's accounting.
func (s *RunService) Report(ctx context.Context, runID int, result RunnerResult) error {
	s.claimsMu.Lock()
	st := s.claims[runID]
	delete(s.claims, runID)
	s.claimsMu.Unlock()

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

	// Host-side push of the run branch (WI-238). The windshift-agent holds no
	// SCM credential and does not push; the orchestrator owns delivery, the
	// same way the remote TriageRunner pushes runner-side before reporting. A
	// push failure downgrades the run to failed so the PR hook does not try to
	// open a PR for a branch that never reached the remote.
	if status == models.AgentRunStatusSucceeded && st != nil && !st.ephemeral && st.checkout != nil && st.req.Repo != nil && s.preparer != nil {
		if err := s.preparer.Push(context.Background(), st.checkout, st.req.Repo.Token); err != nil {
			s.logger.Printf("run service: push run branch run=%d: %v", runID, err)
			status = models.AgentRunStatusFailed
			result.Error = fmt.Sprintf("push run branch: %v", err)
		}
	}

	s.finalize(runID, status, result.Error)
	if err := s.repo.AppendEvent(ctx, runID, "lifecycle", fmt.Sprintf(`{"phase":%q}`, status)); err != nil {
		s.logger.Printf("run service: append terminal event run=%d: %v", runID, err)
	}

	var (
		req        RunRequest
		branch     string
		baseCommit string
		cancel     context.CancelFunc
	)
	if st != nil {
		req = st.req
		branch = st.branch
		baseCommit = st.baseCommit
		cancel = st.cancel
		if st.checkout != nil {
			if err := s.preparer.Cleanup(context.Background(), st.checkout); err != nil {
				s.logger.Printf("run service: cleanup checkout run=%d: %v", runID, err)
			}
		}
	}

	// Ephemeral (binding "test") runs never feed the PR hook: there is no item
	// to link and no branch should reach the remote (the push above is skipped
	// too), so opening a PR would be wrong.
	if st == nil || !st.ephemeral {
		s.invokePostRunHook(PostRunInfo{
			RunID:             runID,
			WorkspaceID:       req.WorkspaceID,
			ItemID:            req.ItemID,
			BindingID:         req.BindingID,
			Status:            status,
			Branch:            branch,
			BaseCommit:        baseCommit,
			TriggeredByUserID: req.TriggeredByUserID,
		})
	}

	if cancel != nil {
		cancel()
	}
	s.unregisterCancel(runID)
	s.wg.Done()
	return nil
}

// Heartbeat implements OrchestratorClient. The in-process worker holds the
// run for its whole lifetime, so there is nothing to renew; remote runners
// override this to keep their lease alive.
func (s *RunService) Heartbeat(_ context.Context, _ int) error { return nil }
