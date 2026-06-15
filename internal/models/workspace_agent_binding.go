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
// orchestrator picks" binding while still gating identity.
//
// A binding that wants per-run worktree preparation must reference an
// SCMConnectionID. The clone URL is derived server-side from the
// trusted SCM provider record + RepoSlug — the binding does not store
// a free-form remote URL, so a workspace admin cannot point runs at
// arbitrary hosts (SSRF) or git remote helpers (RCE via ext::).
type WorkspaceAgentBinding struct {
	ID              int      `json:"id"`
	WorkspaceID     int      `json:"workspace_id"`
	ActingUserID    int      `json:"acting_user_id"`
	ActingUserKind  string   `json:"acting_user_kind"`
	RepoSlug        string   `json:"repo_slug,omitempty"`
	RepoBaseRef     string   `json:"repo_base_ref,omitempty"`
	LLMConnectionID *int     `json:"llm_connection_id,omitempty"`
	SCMConnectionID *int     `json:"scm_connection_id,omitempty"`
	TokenScopes     []string `json:"token_scopes,omitempty"`
	TokenTTLMinutes int      `json:"token_ttl_minutes"`
	MaxRunsPerDay   int      `json:"max_runs_per_day"`
	// TargetPoolID routes this binding's coding-agent runs to a runner_pool
	// capability (a remote pool) instead of the local in-process pool. NULL =
	// local. The pool's per-run token + grants are derived at claim (WI-195).
	TargetPoolID *int `json:"target_pool_id,omitempty"`
	// Instructions is the binding's persona/specialization, appended to the
	// run's standard initial prompt as a "Your role" section (WI-258). It
	// never replaces the operational prompt.
	Instructions    string    `json:"instructions,omitempty"`
	CreatedByUserID int       `json:"created_by_user_id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// HasRepo reports whether the binding is configured with enough source-
// control info to ask the repoprep.Preparer for a prepared checkout.
// Requires both a RepoSlug and an SCMConnectionID: the connection
// supplies the trusted provider host that the clone URL is derived
// from.
func (b *WorkspaceAgentBinding) HasRepo() bool {
	return b != nil && b.RepoSlug != "" && b.SCMConnectionID != nil
}
