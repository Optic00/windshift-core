package models

import "time"

// WorkspaceAgentSkill is one entry in a workspace's agent-skills library
// (WI-258): a markdown "knowledge pack" in the shape of the Anthropic Agent
// Skills standard (the body is a SKILL.md), attachable to workspace agent
// bindings m:n. Delivery is progressive disclosure: a run's initial prompt
// lists the attached skills' names + descriptions, and the agent fetches a
// body with `ws skill get <id>` only when it judges the skill relevant —
// so Description carries the trigger ("when to use this"), not the content.
type WorkspaceAgentSkill struct {
	ID          int    `json:"id"`
	WorkspaceID int    `json:"workspace_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body,omitempty"`
	Enabled     bool   `json:"enabled"`
	// CreatedByUserID is a soft audit ref; nil when the creator was deleted.
	CreatedByUserID *int      `json:"created_by_user_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
