package models

import "time"

// AgentTemplateCatalogEntry is a system-admin-managed override for one entry
// in the Agent Studio creation catalog (WI-922). It is global (not
// workspace-scoped) and, when enabled, wins over the hardcoded embedded
// default for the same template key. The rows are purely additive: the
// embedded defaults remain the fallback seed, so the catalog is complete
// even before any admin configures an override.
type AgentTemplateCatalogEntry struct {
	ID              int64            `json:"id"`
	TemplateKey     string           `json:"template_key"`
	Name            string           `json:"name"`
	DefaultType     AgentProfileType `json:"default_type"`
	Instructions    string           `json:"instructions"`
	Enabled         bool             `json:"enabled"`
	CreatedByUserID *int             `json:"created_by_user_id,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}
