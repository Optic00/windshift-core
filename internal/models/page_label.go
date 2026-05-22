package models

import "time"

// PageLabel is a workspace-scoped label that attaches to pages only.
// Separate system from work-item Label (no shared table or join key).
type PageLabel struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Color       string    `json:"color"`
	WorkspaceID int       `json:"workspace_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
