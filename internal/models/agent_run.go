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
// Docker container that mounts a worktree, runs the pi coding agent, and
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
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// AgentRunEvent is one entry on the NDJSON-style stream pi emits to stdout
// during a run, plus orchestrator-emitted lifecycle entries (queued,
// running, succeeded, …). PayloadJSON is stored verbatim so future readers
// can interpret newer pi event shapes without a schema migration.
type AgentRunEvent struct {
	ID          int       `json:"id"`
	RunID       int       `json:"run_id"`
	Timestamp   time.Time `json:"ts"`
	Type        string    `json:"type"`
	PayloadJSON string    `json:"payload_json"`
}
