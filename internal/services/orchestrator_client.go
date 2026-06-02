package services

import "context"

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
	RunID int

	// WorkspacePath is the host path of the prepared worktree to mount as
	// /workspace, or "" when no repo is attached to the run.
	WorkspacePath string

	// Env is the environment to forward into the container. The
	// orchestrator has already merged caller-supplied vars with its own
	// injections (e.g. WS_TOKEN), so the runner forwards it verbatim.
	Env map[string]string

	// Later phases extend JobSpec with the admin-curated image + command,
	// the grant-set / broker endpoints (git / llm / secrets / http), and
	// the sandbox spec. Keeping them here preserves the "runner is a thin
	// shim" property as the protocol grows.
}

// ClaimedJob pairs a JobSpec with the lease the runner holds while it
// executes. For the in-process transport the lease is implicit; the
// embedded fields are reserved for the remote heartbeat/lease protocol
// (later phases) so the runner core does not change shape when the
// transport does.
type ClaimedJob struct {
	Spec JobSpec
}
