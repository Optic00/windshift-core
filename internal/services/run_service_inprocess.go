package services

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"windshift/internal/models"
	"windshift/internal/repoprep"
)

// RunService is the local OrchestratorClient: its workers run the same
// claim/execute/report loop as remote agents through direct calls. claimNext
// prepares a run; Report finalizes it and cleans up.
var _ OrchestratorClient = (*RunService)(nil)

// queuedJob is an admitted run sent from Start to a local worker.
type queuedJob struct {
	runID int
	req   RunRequest
}

// claimState retains claim-to-report bookkeeping. Repos and checkouts are
// primary-first; scalar refs mirror the primary checkout for legacy paths.
type claimState struct {
	req           RunRequest
	repos         []*repoprep.RepoSpec
	checkouts     []*repoprep.Prepared
	path          string
	branch        string
	baseCommit    string
	workspaceRoot string
	ephemeral     bool
	cancel        context.CancelFunc
}

// runRepos returns Repos or the legacy single Repo, primary first.
func runRepos(req RunRequest) []*repoprep.RepoSpec {
	if len(req.Repos) > 0 {
		return req.Repos
	}
	if req.Repo != nil {
		return []*repoprep.RepoSpec{req.Repo}
	}
	return nil
}

// repoDirNames maps each repo to a unique on-disk subdir name for the multi-repo
// workspace layout (WI-449): the slug's last segment ("owner/core-tests" ->
// "core-tests"), disambiguated with a numeric suffix if two repos share a name.
func repoDirNames(repos []*repoprep.RepoSpec) []string {
	names := make([]string, len(repos))
	seen := make(map[string]int, len(repos))
	for i, r := range repos {
		base := r.RepoSlug
		if idx := strings.LastIndex(base, "/"); idx >= 0 && idx < len(base)-1 {
			base = base[idx+1:]
		}
		if base == "" {
			base = fmt.Sprintf("repo%d", i)
		}
		name := base
		if n, ok := seen[base]; ok {
			name = fmt.Sprintf("%s-%d", base, n+1)
		}
		seen[base]++
		names[i] = name
	}
	return names
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

// claimNext admits queued work; preamble failures finalize in place, and shutdown drains queued runs.
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
		transitioned, err := s.repo.MarkRunningIfQueued(runCtx, job.runID, "", now)
		if err != nil {
			s.logger.Printf("run service: mark running run=%d: %v", job.runID, err)
			s.failClaim(job, cancel, fmt.Sprintf("mark running: %v", err), false)
			continue
		}
		if !transitioned {
			// The row left 'queued' while the job sat on the in-memory
			// channel — canceled via the API (WI-341) or otherwise already
			// terminal. The terminal status (and its lifecycle event) is
			// recorded by whoever made that transition; just release the
			// run's accounting and move on instead of executing it anyway.
			s.logger.Printf("run service: skipping run=%d: no longer queued at dequeue", job.runID)
			cancel()
			s.unregisterCancel(job.runID)
			s.wg.Done()
			continue
		}
		if err := s.repo.AppendEvent(runCtx, job.runID, "lifecycle", `{"phase":"running"}`); err != nil {
			s.logger.Printf("run service: append running event run=%d: %v", job.runID, err)
		}

		st := claimState{req: job.req, ephemeral: job.req.Ephemeral, cancel: cancel}

		repos := runRepos(job.req)
		if len(repos) > 0 {
			// Single repo → the checkout dir itself is the agent's cwd (the
			// pre-WI-449 layout, unchanged). Multiple repos → each is checked
			// out as a sibling dir under a shared per-run workspace root that
			// becomes the cwd, so the agent sees every bound repo at once.
			multi := len(repos) > 1
			if multi {
				st.workspaceRoot = s.preparer.RunWorkspaceDir(job.runID)
			}
			dirNames := repoDirNames(repos)
			prepFailed := false
			for i, rspec := range repos {
				spec := *rspec
				if multi {
					spec.DestDir = filepath.Join(st.workspaceRoot, dirNames[i])
				}
				pw, err := s.preparer.Prepare(runCtx, spec, job.runID)
				if err != nil {
					s.logger.Printf("run service: prepare checkout run=%d repo=%s: %v", job.runID, rspec.RepoSlug, err)
					prepFailed = true
					break
				}
				st.repos = append(st.repos, rspec)
				st.checkouts = append(st.checkouts, pw)
				_ = s.repo.AppendEvent(runCtx, job.runID, "lifecycle", fmt.Sprintf(
					`{"phase":"worktree_ready","repo":%q,"path":%q,"branch":%q,"base_commit":%q}`,
					rspec.RepoSlug, pw.Path, pw.Branch, pw.BaseCommit))
			}
			if prepFailed {
				// A partial multi-repo checkout is unusable; clean up what we
				// prepared and fail visibly. Checkout-prep failure fires the
				// post-run hook (matches the prior inline behavior).
				for _, pw := range st.checkouts {
					_ = s.preparer.Cleanup(context.Background(), pw)
				}
				if st.workspaceRoot != "" {
					s.preparer.CleanupWorkspaceDir(job.runID)
				}
				s.failClaim(job, cancel, "prepare checkout failed", true)
				continue
			}
			// The primary checkout (index 0) drives the single-repo-compatible
			// path: its branch is the grant ref, and it's the cwd for a
			// single-repo run.
			primary := st.checkouts[0]
			st.branch = primary.Branch
			st.baseCommit = primary.BaseCommit
			if multi {
				st.path = st.workspaceRoot
			} else {
				st.path = primary.Path
			}
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
			// Per-repo push refs: each grant may push only its prepared branch
			// (WI-449). Built from the parallel repos/checkouts slices.
			refByRepo := make(map[string]string, len(st.checkouts))
			for i, pw := range st.checkouts {
				refByRepo[st.repos[i].RepoSlug] = pw.Branch
			}
			token, err := s.mintTokenAndGrants(runCtx, job.runID, *job.req.Token, job.req.Grants, refByRepo)
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

	// The orchestrator pushes because agents lack SCM credentials. Each repo is
	// delivered independently; no-commit repos are skipped, while a push failure
	// fails the run so the PR hook cannot open an unpushed branch.
	var pushedRepos []PostRunRepo
	if status == models.AgentRunStatusSucceeded && st != nil && !st.ephemeral && len(st.checkouts) > 0 && s.preparer != nil {
		for i, pw := range st.checkouts {
			rspec := st.repos[i]
			rr := PostRunRepo{RepoSlug: rspec.RepoSlug, Branch: pw.Branch, BaseCommit: pw.BaseCommit}
			switch err := s.preparer.Push(context.Background(), pw, rspec.Token); {
			case errors.Is(err, repoprep.ErrNoNewCommits):
				rr.Branch = "" // nothing delivered for this repo
				if err := s.repo.AppendEvent(ctx, runID, "lifecycle", fmt.Sprintf(`{"phase":"no_changes","repo":%q}`, rspec.RepoSlug)); err != nil {
					s.logger.Printf("run service: append no_changes event run=%d: %v", runID, err)
				}
			case err != nil:
				s.logger.Printf("run service: push run branch run=%d repo=%s: %v", runID, rspec.RepoSlug, err)
				status = models.AgentRunStatusFailed
				result.Error = fmt.Sprintf("push run branch %s: %v", rspec.RepoSlug, err)
			}
			pushedRepos = append(pushedRepos, rr)
		}
	}
	// Legacy scalar branch fields mirror the primary repo's no-change outcome.
	noChanges := len(pushedRepos) > 0 && pushedRepos[0].Branch == ""

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
		if !noChanges {
			branch = st.branch
			baseCommit = st.baseCommit
		}
		cancel = st.cancel
		for _, pw := range st.checkouts {
			if err := s.preparer.Cleanup(context.Background(), pw); err != nil {
				s.logger.Printf("run service: cleanup checkout run=%d: %v", runID, err)
			}
		}
		if st.workspaceRoot != "" {
			s.preparer.CleanupWorkspaceDir(runID)
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
			Summary:           result.Summary,
			Trigger:           req.Trigger,
			Repos:             pushedRepos,
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
