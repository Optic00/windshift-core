package data

import "time"

// UserInfo carries the authenticated identity plumbed through from SSH.
type UserInfo struct {
	UserID         int
	CredentialID   string
	CredentialName string
	RemoteAddr     string
	Email          string
	Username       string
	FirstName      string
	LastName       string
}

// Prefs is the per-user TUI preferences document persisted server-side
// (v1 /users/me/tui-preferences). Pointer fields distinguish unset.
type Prefs struct {
	Theme           string   `json:"theme,omitempty"`
	SplitRatio      *float64 `json:"split_ratio,omitempty"`
	LastWorkspaceID *int     `json:"last_workspace_id,omitempty"`
}

// ─── v1 wire mirrors ──────────────────────────────────────────────────
// These types mirror the relevant subset of internal/restapi/v1/dto. We
// duplicate them rather than import the dto package to avoid pulling the
// v1 layering dependency into the TUI. Field-for-field copies; if the
// upstream DTO grows fields we care about, mirror them here.

type v1PaginationMeta struct {
	Page       int `json:"page"`
	Limit      int `json:"limit"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

type v1WorkspacesPage struct {
	Data       []v1WorkspaceResponse `json:"data"`
	Pagination v1PaginationMeta      `json:"pagination"`
}

type v1ItemsPage struct {
	Data       []v1ItemResponse `json:"data"`
	Pagination v1PaginationMeta `json:"pagination"`
}

type v1CommentsPage struct {
	Data       []v1CommentResponse `json:"data"`
	Pagination v1PaginationMeta    `json:"pagination"`
}

type v1UserSummary struct {
	ID        int    `json:"id"`
	Email     string `json:"email"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	FullName  string `json:"full_name"`
}

type v1StatusSummary struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	CategoryID    int    `json:"category_id"`
	CategoryName  string `json:"category_name,omitempty"`
	CategoryColor string `json:"category_color,omitempty"`
}

type v1PrioritySummary struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Icon  string `json:"icon,omitempty"`
	Color string `json:"color,omitempty"`
}

type v1WorkspaceResponse struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Key         string `json:"key"`
	Description string `json:"description"`
	Active      bool   `json:"active"`
}

type v1ItemResponse struct {
	ID          int                `json:"id"`
	WorkspaceID int                `json:"workspace_id"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	ParentID    *int               `json:"parent_id,omitempty"`
	Status      *v1StatusSummary   `json:"status,omitempty"`
	Priority    *v1PrioritySummary `json:"priority,omitempty"`
	Assignee    *v1UserSummary     `json:"assignee,omitempty"`
	Creator     *v1UserSummary     `json:"creator,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

type v1CommentResponse struct {
	ID        int            `json:"id"`
	ItemID    int            `json:"item_id"`
	Content   string         `json:"content"`
	Author    *v1UserSummary `json:"author,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type v1AssignableUser struct {
	ID        int    `json:"id"`
	Username  string `json:"username"`
	FullName  string `json:"full_name"`
	IsActive  bool   `json:"is_active"`
	IsAgent   bool   `json:"is_agent"`
	AvatarURL string `json:"avatar_url"`
}

type v1AgentRunResponse struct {
	ID        int        `json:"id"`
	Status    string     `json:"status"`
	JobKind   string     `json:"job_kind"`
	QueuedAt  time.Time  `json:"queued_at"`
	StartedAt *time.Time `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	Error     string     `json:"error"`
}

// ─── TUI domain types (converters below adapt v1 wire to these) ──────

// User is an assignable user for the assignee picker.
type User struct {
	ID       int
	Username string
	FullName string
	IsAgent  bool
}

// AgentRun is one coding-agent execution against a work item.
type AgentRun struct {
	ID        int
	Status    string // queued|running|succeeded|failed|canceled|killed
	JobKind   string
	QueuedAt  string
	StartedAt string
	EndedAt   string
	Error     string
}

// Workspace represents a workspace from the Windshift API.
type Workspace struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Key           string `json:"key"`
	Description   string `json:"description"`
	Active        bool   `json:"active"`
	TimeProjectID *int   `json:"time_project_id"` // populated only by legacy callers; v1 omits it
}

// Status represents a workflow status
type Status struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	CategoryID    int    `json:"category_id"`
	CategoryName  string `json:"category_name"`
	CategoryColor string `json:"category_color"`
}

// Priority represents a priority level
type Priority struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Icon      string `json:"icon"`
	Color     string `json:"color"`
	SortOrder int    `json:"sort_order"`
}

type WorkItem struct {
	ID                int            `json:"id"`
	WorkspaceID       int            `json:"workspace_id"`
	ItemTypeID        *int           `json:"item_type_id"`
	Title             string         `json:"title"`
	Description       string         `json:"description"`
	Status            string         `json:"status"`                // Legacy text field
	Priority          string         `json:"priority"`              // Legacy text field
	StatusID          *int           `json:"status_id,omitempty"`   // ID-based status
	PriorityID        *int           `json:"priority_id,omitempty"` // ID-based priority
	MilestoneID       *int           `json:"milestone_id"`
	TimeProjectID     *int           `json:"time_project_id"`
	AssigneeID        *int           `json:"assignee_id"`
	CreatorID         *int           `json:"creator_id"`
	CustomFieldValues map[string]any `json:"custom_field_values"`
	ParentID          *int           `json:"parent_id"`
	Path              string         `json:"path"`
	Rank              *string        `json:"rank"`
	CreatedAt         string         `json:"created_at"`
	UpdatedAt         string         `json:"updated_at"`
	// Joined fields for display
	WorkspaceName   string `json:"workspace_name"`
	WorkspaceKey    string `json:"workspace_key"`
	ItemTypeName    string `json:"item_type_name"`
	ParentTitle     string `json:"parent_title"`
	MilestoneName   string `json:"milestone_name"`
	TimeProjectName string `json:"time_project_name"`
	AssigneeName    string `json:"assignee_name"`
	AssigneeEmail   string `json:"assignee_email"`
	CreatorName     string `json:"creator_name"`
	CreatorEmail    string `json:"creator_email"`
	// ID-based status/priority display fields
	StatusName          string `json:"status_name,omitempty"`
	StatusCategoryColor string `json:"category_color,omitempty"`
	PriorityName        string `json:"priority_name,omitempty"`
	PriorityIcon        string `json:"priority_icon,omitempty"`
	PriorityColor       string `json:"priority_color,omitempty"`
}

// GetLevel calculates hierarchy level from path. v1 doesn't surface a path
// string, so for v1-sourced items this returns 0; the work-item list groups
// flat unless we later expand parent chains.
func (wi *WorkItem) GetLevel() int {
	if wi.Path == "" {
		return 0
	}
	// Path format is like "/1/5/12/" - count slashes minus 1
	level := 0
	for _, char := range wi.Path {
		if char == '/' {
			level++
		}
	}
	// Subtract 1 because path starts and ends with /
	return level - 1
}

type Comment struct {
	ID          int     `json:"id"`
	ItemID      int     `json:"item_id"`
	AuthorID    int     `json:"author_id"`
	Content     string  `json:"content"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	AuthorName  *string `json:"author_name"`
	AuthorEmail *string `json:"author_email"`
}

type TimeProject struct {
	ID           int32   `json:"id"`
	CustomerID   int32   `json:"customer_id"`
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	HourlyRate   float64 `json:"hourly_rate"`
	Active       bool    `json:"active"`
	CustomerName *string `json:"customer_name"`
}

type CreateTimeLogRequest struct {
	ProjectID   int     `json:"project_id"`
	ItemID      *int    `json:"item_id"`
	Description string  `json:"description"`
	Date        string  `json:"date"`
	StartTime   string  `json:"start_time"`
	Duration    string  `json:"duration"`
	EndTime     *string `json:"end_time"`
}

// ─── v1 → TUI converters ─────────────────────────────────────────────

func workspaceFromV1(w v1WorkspaceResponse) Workspace {
	return Workspace{
		ID:          w.ID,
		Name:        SanitizeLine(w.Name),
		Key:         SanitizeLine(w.Key),
		Description: SanitizeText(w.Description),
		Active:      w.Active,
	}
}

func workItemFromV1(it v1ItemResponse) WorkItem {
	wi := WorkItem{
		ID:          it.ID,
		WorkspaceID: it.WorkspaceID,
		Title:       SanitizeLine(it.Title),
		Description: SanitizeText(it.Description),
		ParentID:    it.ParentID,
		CreatedAt:   it.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   it.UpdatedAt.Format(time.RFC3339),
	}
	if it.Status != nil {
		id := it.Status.ID
		wi.StatusID = &id
		wi.StatusName = SanitizeLine(it.Status.Name)
		wi.StatusCategoryColor = SanitizeLine(it.Status.CategoryColor)
		wi.Status = wi.StatusName
	}
	if it.Priority != nil {
		id := it.Priority.ID
		wi.PriorityID = &id
		wi.PriorityName = SanitizeLine(it.Priority.Name)
		wi.PriorityIcon = SanitizeLine(it.Priority.Icon)
		wi.PriorityColor = SanitizeLine(it.Priority.Color)
		wi.Priority = wi.PriorityName
	}
	if it.Assignee != nil {
		id := it.Assignee.ID
		wi.AssigneeID = &id
		wi.AssigneeName = SanitizeLine(it.Assignee.FullName)
		wi.AssigneeEmail = SanitizeLine(it.Assignee.Email)
	}
	if it.Creator != nil {
		id := it.Creator.ID
		wi.CreatorID = &id
		wi.CreatorName = SanitizeLine(it.Creator.FullName)
		wi.CreatorEmail = SanitizeLine(it.Creator.Email)
	}
	return wi
}

func commentFromV1(c v1CommentResponse) Comment {
	out := Comment{
		ID:        c.ID,
		ItemID:    c.ItemID,
		Content:   SanitizeText(c.Content),
		CreatedAt: c.CreatedAt.Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.Format(time.RFC3339),
	}
	if c.Author != nil {
		out.AuthorID = c.Author.ID
		name := SanitizeLine(c.Author.FullName)
		email := SanitizeLine(c.Author.Email)
		out.AuthorName = &name
		out.AuthorEmail = &email
	}
	return out
}
