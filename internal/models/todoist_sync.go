package models

import "time"

// TodoistSyncScopeMode controls which Todoist tasks a user syncs.
type TodoistSyncScopeMode string

const (
	// TodoistScopeAll syncs every project, flattened into the personal workspace.
	TodoistScopeAll TodoistSyncScopeMode = "all"
	// TodoistScopeProject syncs only the tasks in TodoistProjectID.
	TodoistScopeProject TodoistSyncScopeMode = "project"
)

// TodoistSyncConfig is a user's two-way personal-task sync configuration.
// One row per (user, integration provider).
type TodoistSyncConfig struct {
	ID                    string               `json:"id"`
	UserID                string               `json:"user_id"`
	IntegrationProviderID string               `json:"integration_provider_id"`
	PersonalWorkspaceID   int                  `json:"personal_workspace_id"`
	Enabled               bool                 `json:"enabled"`
	ScopeMode             TodoistSyncScopeMode `json:"scope_mode"`
	TodoistProjectID      string               `json:"todoist_project_id,omitempty"`
	// SyncToken is the Todoist incremental cursor; "*" forces a full sync.
	SyncToken    string     `json:"-"`
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// TodoistTaskLink maps a personal-workspace item to a Todoist task and snapshots
// the agreed state at the last sync, enabling field-level change detection
// (which side changed which field since last time) for last-write-wins.
type TodoistTaskLink struct {
	ID               string
	UserID           string
	ItemID           int
	TodoistTaskID    string
	TodoistProjectID string
	// LastTitle..LastCompleted are the values both sides agreed on at the
	// previous sync. A field differing from this snapshot means that side
	// changed it.
	LastTitle       string
	LastDescription string
	LastDue         string // 'YYYY-MM-DD', RFC3339, or "" for no due date
	LastPriority    int    // Todoist scale: 1 (normal) .. 4 (urgent)
	LastCompleted   bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
