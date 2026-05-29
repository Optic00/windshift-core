package models

import "time"

// WorkspaceAgentBinding links a workspace + acting user to the run-shape
// RunService needs when an item is assigned to that user. The acting-
// identity kind is stamped at create time by the WI-87 chokepoint and
// stored verbatim so the trigger path can render audit info without
// re-running the gate.
//
// One binding per (workspace_id, acting_user_id). The Repo* fields are
// nullable so a workspace can ship a "fall through to whatever the
// orchestrator picks" binding while still gating identity; the LLM
// connection + budget fields land here but aren't enforced until
// WI-89 / Phase 8.
type WorkspaceAgentBinding struct {
	ID              int       `json:"id"`
	WorkspaceID     int       `json:"workspace_id"`
	ActingUserID    int       `json:"acting_user_id"`
	ActingUserKind  string    `json:"acting_user_kind"`
	RepoSlug        string    `json:"repo_slug,omitempty"`
	RepoRemoteURL   string    `json:"repo_remote_url,omitempty"`
	RepoBaseRef     string    `json:"repo_base_ref,omitempty"`
	LLMConnectionID *int      `json:"llm_connection_id,omitempty"`
	TokenScopes     []string  `json:"token_scopes,omitempty"`
	TokenTTLMinutes int       `json:"token_ttl_minutes"`
	MaxRunsPerDay   int       `json:"max_runs_per_day"`
	CreatedByUserID int       `json:"created_by_user_id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// HasRepo reports whether the binding is configured with enough source-
// control info to ask the WorktreeManager for a prepared worktree.
func (b *WorkspaceAgentBinding) HasRepo() bool {
	return b != nil && b.RepoSlug != "" && b.RepoRemoteURL != ""
}
