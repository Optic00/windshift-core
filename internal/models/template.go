package models

import "time"

// Work item template modes (WI-438).
const (
	// TemplateModeSelectable templates are offered in the create-modal picker
	// for the creator to choose. They may target zero (global) or many item
	// types.
	TemplateModeSelectable = "selectable"
	// TemplateModeMandatory templates are bound to exactly one item type and
	// applied automatically to every new item of that type (when the caller
	// left the description empty); the picker is suppressed.
	TemplateModeMandatory = "mandatory"
)

// ItemTemplate is a workspace-scoped reusable body that pre-fills a work
// item's description at creation time. ItemTypeIDs lists the item types the
// template targets: empty means global/untyped (selectable only), and a
// mandatory template targets exactly one type. v1 covers the description field
// only; the model is forward-compatible with other pre-filled fields.
type ItemTemplate struct {
	ID              int       `json:"id"`
	WorkspaceID     int       `json:"workspace_id"`
	Name            string    `json:"name"`
	DescriptionBody string    `json:"description_body"`
	Mode            string    `json:"mode"`
	IsActive        bool      `json:"is_active"`
	ItemTypeIDs     []int     `json:"item_type_ids"`
	CreatedBy       *int      `json:"created_by,omitempty"`
	UpdatedBy       *int      `json:"updated_by,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
