package models

import (
	"strings"
	"time"
)

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
	JobKindGeneralAgent    = "general_agent"
	JobKindCodingAgent     = "coding_agent"
	JobKindStandardAgent   = "standard_agent"
	JobKindActionContainer = "action_container"
	JobKindCITask          = "ci_task"
)

const (
	// DefaultAgentTriggerToken is the literal token a PR comment must contain to
	// ask the bound agent to continue that PR. There is no agent user on the SCM
	// side (commits/PRs/comments are attributed to the connected OAuth user), so
	// a real @-mention can't resolve to a binding the way an in-Windshift mention
	// does; the PR-comment poller matches this literal token instead and resolves
	// the binding via repo→workspace→linked-item.
	DefaultAgentTriggerToken = "@agent"
	// AgentCommentMarker is embedded (as an invisible HTML comment) in every PR
	// comment the agent posts. The poller skips any comment carrying it, so the
	// agent can never re-trigger itself even if its body echoes the trigger token
	// — the first and load-bearing layer of the comment loop guard.
	AgentCommentMarker = "<!-- windshift-agent -->"
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
	JobKind  string `json:"job_kind,omitempty"`
	JobImage string `json:"job_image,omitempty"`
	// IsEphemeral marks private verification runs. Remote and local execution
	// may read repositories and call the configured model, but must not push,
	// create a PR, or invoke any post-run mutation hook.
	IsEphemeral bool `json:"is_ephemeral,omitempty"`
	// TriggeredByUserID is who caused the run: the user whose assignment
	// fired the binding trigger, or the admin who started a test run. On
	// OAuth SCM connections this user's personal token is the credential
	// for the run's git traffic and PR creation (WI-275). Nil on runs
	// queued before the column existed — those use the connection-level
	// credential.
	TriggeredByUserID *int `json:"triggered_by_user_id,omitempty"`
	// Standard-agent identity and lineage are immutable execution snapshots.
	// ActingUserID is the profile identity whose permissions every tool uses;
	// RootInitiatorUserID is the human at the start of an agent chain;
	// ImmediateTriggerUserID is the human/agent that directly queued this run.
	ActingUserID           *int   `json:"acting_user_id,omitempty"`
	RootInitiatorUserID    *int   `json:"root_initiator_user_id,omitempty"`
	ImmediateTriggerUserID *int   `json:"immediate_trigger_user_id,omitempty"`
	ParentRunID            *int   `json:"parent_run_id,omitempty"`
	ChainDepth             int    `json:"chain_depth,omitempty"`
	SessionID              string `json:"session_id,omitempty"`
	ProfileVersion         int    `json:"profile_version,omitempty"`
	// GrantsJSON contains admitted capabilities and immutable run inputs. ProfileSnapshotJSON
	// contains the immutable Standard profile inputs used by the run.
	GrantsJSON          string `json:"grants_json,omitempty"`
	ProfileSnapshotJSON string `json:"profile_snapshot_json,omitempty"`
	// Trigger is the run's trigger context + free-form instruction, persisted
	// as JSON (agent_runs.trigger_json). Holding it in one JSON blob keeps new
	// instruction shapes migration-free. Nil for runs created before the
	// column existed, or triggers that carried no extra context.
	Trigger   *RunTrigger `json:"trigger,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// RunTrigger captures why and how a run started, plus any free-form
// instruction that came with the trigger. It is persisted verbatim as JSON in
// agent_runs.trigger_json so additional instruction fields can be added
// without a schema migration.
type RunTrigger struct {
	// Kind is how the run started: "mention", "assignee", "rerun", or "test".
	Kind string `json:"kind,omitempty"`
	// Instruction is the free-form directive that triggered the run — the body
	// of the @mentioning comment. Treated as the run's primary instruction for
	// what to do, layered on after the static prompt and the binding's persona.
	Instruction string `json:"instruction,omitempty"`
	// CommentID is the comment the instruction came from (0 if none); audit only.
	CommentID int `json:"comment_id,omitempty"`
	// AuthorID is the user who wrote the triggering comment (0 if none).
	AuthorID int `json:"author_id,omitempty"`

	// Continuations reuse the resolved PR head branch, so runs extend rather than
	// compete with its PR. Persisted PR fields support post-run comments and
	// poller idempotency across local and remote claim paths.
	ContinuePRNumber   int    `json:"continue_pr_number,omitempty"`
	ContinueRepoSlug   string `json:"continue_repo_slug,omitempty"`
	ContinueHeadBranch string `json:"continue_head_branch,omitempty"`
	// ContinueCommentID is the SCM trigger comment, persisted for restart-safe
	// poller idempotency and distinct from Windshift CommentID.
	ContinueCommentID int64 `json:"continue_comment_id,omitempty"`
	// ContinueEventID provides one terminal PR response and shared deduplication.
	ContinueEventID int64 `json:"continue_event_id,omitempty"`
}

// HasInstruction reports whether the trigger carries a non-empty instruction.
func (t *RunTrigger) HasInstruction() bool {
	return t != nil && strings.TrimSpace(t.Instruction) != ""
}

// IsContinuation reports whether the run should land commits on an existing PR
// head branch instead of cutting a fresh per-run branch.
func (t *RunTrigger) IsContinuation() bool {
	return t != nil && strings.TrimSpace(t.ContinueHeadBranch) != ""
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
