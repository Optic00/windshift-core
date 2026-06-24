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
	// Pages are the workspace pages referenced by this skill (WI-517). On the
	// admin surface they round-trip so the editor can render the current
	// selection; on the agent-facing `ws skill get` surface their markdown is
	// inlined into Body instead. nil/omitted when the skill references none.
	Pages []SkillPageReference `json:"pages,omitempty"`
	// CreatedByUserID is a soft audit ref; nil when the creator was deleted.
	CreatedByUserID *int      `json:"created_by_user_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// SkillPageReference is a lightweight handle on a workspace page referenced by
// a skill (WI-517): enough for the editor to render a chip and for the agent
// surface to label the inlined section, without carrying the page body around
// until it is actually needed.
type SkillPageReference struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}
