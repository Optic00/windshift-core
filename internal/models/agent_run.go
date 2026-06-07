package models

import "time"

// Agent-run lifecycle states. The agent_runs.status CHECK constraint
// enforces this set in the database — keep both lists in sync.
const (
	AgentRunStatusQueued    = "queued"
	AgentRunStatusRunning   = "running"
	AgentRunStatusSucceeded = "succeeded"
	AgentRunStatusFailed    = "failed"
	AgentRunStatusCanceled  = "canceled"
	AgentRunStatusKilled    = "killed"
)

// Agent-run job kinds. coding_agent is the default (the windshift-agent harness
// on the fixed runner image); action_container + ci_task run an admin-chosen
// image on the same runner substrate (WI-146).
const (
	JobKindCodingAgent     = "coding_agent"
	JobKindActionContainer = "action_container"
	JobKindCITask          = "ci_task"
)

// IsAgentRunTerminal reports whether the status represents a final state
// (no further transitions will be made by the orchestrator).
func IsAgentRunTerminal(status string) bool {
	switch status {
	case AgentRunStatusSucceeded,
		AgentRunStatusFailed,
		AgentRunStatusCanceled,
		AgentRunStatusKilled:
		return true
	}
	return false
}

// AgentRun records one execution of the coding-agent harness: a per-run
// Docker container that mounts a worktree, runs the windshift-agent, and
// produces a PR. BindingID is optional — set when the run was triggered
// by an assignee change matching a workspace_agent_binding, nil for
// manually-started runs.
type AgentRun struct {
	ID          int        `json:"id"`
	WorkspaceID int        `json:"workspace_id"`
	ItemID      *int       `json:"item_id,omitempty"`
	BindingID   *int       `json:"binding_id,omitempty"`
	Status      string     `json:"status"`
	QueuedAt    time.Time  `json:"queued_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	ContainerID string     `json:"container_id,omitempty"`
	Error       string     `json:"error,omitempty"`
	// TargetPoolID is the runner_pool capability this run is dispatched to,
	// or nil for the local in-process pool (Initiative WI-141). Remote
	// runners claim queued runs scoped by this value.
	TargetPoolID *int `json:"target_pool_id,omitempty"`
	// RunnerID is the runner_instances row that executed this run, or nil
	// for the in-process local runner. Audit only; soft ref.
	RunnerID *int `json:"runner_id,omitempty"`
	// JobKind selects how the runner executes this run (WI-146); defaults to
	// JobKindCodingAgent. JobImage is the admin image for container jobs
	// (action_container / ci_task), empty for coding_agent.
	JobKind   string    `json:"job_kind,omitempty"`
	JobImage  string    `json:"job_image,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AgentRunEvent is one entry on the NDJSON-style stream the agent emits to
// stdout during a run, plus orchestrator-emitted lifecycle entries (queued,
// running, succeeded, …). PayloadJSON is stored verbatim so future readers
// can interpret newer agent event shapes without a schema migration.
type AgentRunEvent struct {
	ID          int       `json:"id"`
	RunID       int       `json:"run_id"`
	Timestamp   time.Time `json:"ts"`
	Type        string    `json:"type"`
	PayloadJSON string    `json:"payload_json"`
}
