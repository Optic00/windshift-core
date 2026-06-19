package services

import (
	"context"
	"log"
)

// OrchestratorClient is the transport seam between a runner and the
// orchestrator (Initiative WI-141, decision #7: one execution path for
// local and remote).
//
// A runner depends only on this interface, never on RunService internals,
// so the same runner core serves both deployment modes:
//
//   - The in-process implementation (local pool) calls RunService /
//     AgentRunRepository directly — no loopback HTTP, no real overhead.
//   - The HTTP implementation (remote pool, later phases) speaks
//     register / claim / heartbeat / result over HTTPS, and the runner is
//     the standalone agent binary.
//
// Everything a runner needs to execute a job is delivered through Claim;
// it reports back through Emit / Report and keeps its lease alive via
// Heartbeat.
type OrchestratorClient interface {
	// Claim blocks until a job is admitted for this runner or ctx is
	// canceled. It returns (nil, nil) when the client is shutting down
	// with no further work — callers treat that as a clean stop, not an
	// error.
	Claim(ctx context.Context) (*ClaimedJob, error)

	// Emit streams one event for an in-flight run into agent_run_events.
	// Best-effort: a returned error is logged by the runner but does not
	// by itself abort the run.
	Emit(ctx context.Context, runID int, eventType, payloadJSON string) error

	// Report records the terminal verdict for a run. After Report the
	// runner must not Emit further events for that run.
	Report(ctx context.Context, runID int, result RunnerResult) error

	// Heartbeat renews the lease on an in-flight run. The in-process
	// transport no-ops (the worker holds the run for its whole lifetime);
	// remote runners call it on an interval so the orchestrator can reap
	// a dead claim and revoke its run-token.
	Heartbeat(ctx context.Context, runID int) error
}

// JobSpec is the self-contained description a runner needs to execute one
// job. It is transport-agnostic: the in-process client fills it from
// RunService admission state, while the remote client (later phases)
// receives the same shape as JSON over HTTPS. The runner never reaches
// back into orchestrator internals — if a runner needs it, it lives here.
type JobSpec struct {
	// RunID identifies the agent_runs row this job executes.
	RunID int `json:"run_id"`

	// WorkspacePath is the host path of the prepared worktree to mount as
	// /workspace, or "" when no repo is attached to the run. For remote
	// pools the runner prepares its own worktree, so this is "" on the wire.
	WorkspacePath string `json:"workspace_path,omitempty"`

	// Env is the environment to forward into the container. The
	// orchestrator has already merged caller-supplied vars with its own
	// injections (e.g. WS_TOKEN), so the runner forwards it verbatim.
	Env map[string]string `json:"env,omitempty"`

	// InitialPrompt is the server-managed prompt for coding-agent jobs. Remote
	// runners must use this instead of a runner-host default so local and remote
	// policy stays identical.
	InitialPrompt string `json:"initial_prompt,omitempty"`

	// Kind selects how the runner executes the job (WI-146): "coding_agent"
	// (default; windshift-agent harness on the fixed runner image) vs "action_container" /
	// "ci_task" (run Image as a plain container). Image is the admin image
	// for the container kinds.
	Kind  string `json:"kind,omitempty"`
	Image string `json:"image,omitempty"`

	// Repo, when set, tells a REMOTE runner to prepare its own per-run
	// checkout (via the triage binary) instead of receiving a host
	// WorkspacePath. It carries only what the runner needs to clone + push
	// through the git-proxy; the run token travels in Env (WS_TOKEN). Nil for
	// local runs (the orchestrator already prepared WorkspacePath) and for
	// runs with no repo. Deprecated by Repos; mirrors Repos[0] (the primary).
	Repo *JobRepo `json:"repo,omitempty"`

	// Repos is every repo a remote runner must check out for a multi-repo run
	// (WI-449), primary first. When more than one, the runner checks each out
	// as a sibling dir under a shared workspace root (named by repo) and pushes
	// each repo's run branch through the git-proxy. One entry → single-repo
	// layout, identical to the legacy Repo path.
	Repos []JobRepo `json:"repos,omitempty"`

	// Later phases extend JobSpec with the admin-curated image + command,
	// the grant-set / broker endpoints (git / llm / secrets / http), and
	// the sandbox spec. Keeping them here preserves the "runner is a thin
	// shim" property as the protocol grows.
}

// JobRepo is the repo-prep input a remote runner needs to materialize a
// per-run checkout and push the run branch through the git-proxy. The runner
// builds the proxy URL as {WS_API_URL}/git-proxy/{WorkspaceID}/{owner}/{repo}
// from Slug, and authenticates with the per-run token (WS_TOKEN). It holds no
// SCM credentials and no real remote URL — the git-proxy injects the cred
// server-side.
type JobRepo struct {
	WorkspaceID int    `json:"workspace_id"`
	Slug        string `json:"slug"`     // "owner/repo"
	BaseRef     string `json:"base_ref"` // branch to cut the run branch from
	// ContinueBranch, when set, makes this a continuation: the runner checks out
	// this existing PR head branch and pushes commits back to it instead of
	// cutting a fresh per-run branch from BaseRef (which is then ignored).
	ContinueBranch string `json:"continue_branch,omitempty"`
}

// ClaimedJob pairs a JobSpec with the lease the runner holds while it
// executes. For the in-process transport the lease is implicit; the
// embedded fields are reserved for the remote heartbeat/lease protocol
// (later phases) so the runner core does not change shape when the
// transport does.
type ClaimedJob struct {
	Spec JobSpec `json:"spec"`

	// Ctx is the per-run context the worker drives the runner with. The
	// client cancels it when the run should abort — in-process via
	// RunService.Cancel / shutdown, remote when a heartbeat reports the
	// run was canceled. Runtime-only; never serialized over the wire.
	Ctx context.Context `json:"-"`
}

// RunWorker is the transport-agnostic runner core (Initiative WI-141,
// decision #7). It loops claiming jobs from the orchestrator, driving the
// Runner, and reporting results — the *same* loop whether `client` is the
// in-process RunService (local pool) or the HTTPS client in the standalone
// agent binary (remote pool). It returns when Claim reports the
// orchestrator is shutting down (a nil job) or ctx is canceled.
func RunWorker(ctx context.Context, client OrchestratorClient, runner Runner, logger *log.Logger) {
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := client.Claim(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Transient claim failure (e.g. a remote network blip). The
			// in-process client never errors; remote clients back off
			// inside Claim, so a bare continue is safe here.
			logger.Printf("run worker: claim: %v", err)
			continue
		}
		if job == nil {
			return
		}
		runID := job.Spec.RunID
		jobCtx := job.Ctx
		if jobCtx == nil {
			jobCtx = ctx
		}
		emit := func(eventType, payloadJSON string) error {
			return client.Emit(jobCtx, runID, eventType, payloadJSON)
		}
		result := runner.Run(jobCtx, RunInput{
			RunID:         runID,
			WorkspacePath: job.Spec.WorkspacePath,
			Env:           job.Spec.Env,
			InitialPrompt: job.Spec.InitialPrompt,
			Kind:          job.Spec.Kind,
			Image:         job.Spec.Image,
			Repo:          job.Spec.Repo,
			Repos:         job.Spec.Repos,
		}, emit)
		if err := client.Report(jobCtx, runID, result); err != nil {
			logger.Printf("run worker: report run=%d: %v", runID, err)
		}
	}
}
