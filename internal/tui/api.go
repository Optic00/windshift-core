// Package tui provides a terminal user interface for Windshift.
package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	tea "charm.land/bubbletea/v2"
)

// authMode picks which header doGet/doMutate attaches.
type authMode int

const (
	authBearer authMode = iota
	authSession
)

// APIClient handles communication with the Windshift API.
//
// The TUI's endpoints split across two surfaces:
//   - /rest/api/v1/... uses bearer auth (Authorization: Bearer crw_*),
//     populated from an SSH-minted temp API token.
//   - /api/...           uses session auth (X-Session-Token), populated from
//     an SSH-minted session row.
//
// Once v1 grows /time/projects and /time/worklogs endpoints, the legacy
// session path can be removed entirely.
type APIClient struct {
	baseURL      string
	httpClient   *http.Client
	sessionToken string
	bearerToken  string
}

// NewAPIClient creates a new API client
func NewAPIClient(baseURL string) *APIClient {
	return &APIClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// SetSessionToken sets the session token used by legacy /api/* calls.
func (c *APIClient) SetSessionToken(token string) {
	c.sessionToken = token
}

// SetBearerToken sets the API token used by /rest/api/v1/* calls.
func (c *APIClient) SetBearerToken(token string) {
	c.bearerToken = token
}

func (c *APIClient) setAuth(req *http.Request, mode authMode) {
	switch mode {
	case authBearer:
		if c.bearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.bearerToken)
		}
	case authSession:
		if c.sessionToken != "" {
			req.Header.Set("X-Session-Token", c.sessionToken)
		}
	}
}

// doGet performs a GET request to the given path and decodes the JSON response into result.
func (c *APIClient) doGet(path string, mode authMode, result interface{}) error {
	req, err := http.NewRequest("GET", c.baseURL+path, http.NoBody)
	if err != nil {
		return err
	}
	c.setAuth(req, mode)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("API error: %s; failed to read response body: %w", resp.Status, readErr)
		}
		return fmt.Errorf("API error: %s - %s", resp.Status, sanitizeTerminalText(string(body)))
	}

	if result == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(result)
}

// doMutate performs a mutating HTTP request (POST, PUT, etc.) with a JSON body.
// If result is non-nil, the response body is decoded into it.
func (c *APIClient) doMutate(method, path string, mode authMode, body, result interface{}) error { //nolint:unparam // result is wired for callers that will decode bodies; all current call sites pass nil
	jsonData, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(method, c.baseURL+path, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req, mode)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("API error: %s; failed to read response body: %w", resp.Status, readErr)
		}
		return fmt.Errorf("API error: %s - %s", resp.Status, sanitizeTerminalText(string(body)))
	}

	if result == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(result)
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

// ─── existing TUI types (kept; converters below adapt v1 wire to these) ──

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
	ID                int                    `json:"id"`
	WorkspaceID       int                    `json:"workspace_id"`
	ItemTypeID        *int                   `json:"item_type_id"`
	Title             string                 `json:"title"`
	Description       string                 `json:"description"`
	Status            string                 `json:"status"`                // Legacy text field
	Priority          string                 `json:"priority"`              // Legacy text field
	StatusID          *int                   `json:"status_id,omitempty"`   // ID-based status
	PriorityID        *int                   `json:"priority_id,omitempty"` // ID-based priority
	MilestoneID       *int                   `json:"milestone_id"`
	TimeProjectID     *int                   `json:"time_project_id"`
	AssigneeID        *int                   `json:"assignee_id"`
	CreatorID         *int                   `json:"creator_id"`
	CustomFieldValues map[string]interface{} `json:"custom_field_values"`
	ParentID          *int                   `json:"parent_id"`
	Path              string                 `json:"path"`
	Rank              *string                `json:"rank"`
	CreatedAt         string                 `json:"created_at"`
	UpdatedAt         string                 `json:"updated_at"`
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

type CreateWorkItemRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    string `json:"priority"`
}

type CreateCommentRequest struct {
	Content  string `json:"content"`
	AuthorID int    `json:"author_id"`
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
		Name:        sanitizeTerminalLine(w.Name),
		Key:         sanitizeTerminalLine(w.Key),
		Description: sanitizeTerminalText(w.Description),
		Active:      w.Active,
	}
}

func workItemFromV1(it v1ItemResponse) WorkItem {
	wi := WorkItem{
		ID:          it.ID,
		WorkspaceID: it.WorkspaceID,
		Title:       sanitizeTerminalLine(it.Title),
		Description: sanitizeTerminalText(it.Description),
		ParentID:    it.ParentID,
		CreatedAt:   it.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   it.UpdatedAt.Format(time.RFC3339),
	}
	if it.Status != nil {
		id := it.Status.ID
		wi.StatusID = &id
		wi.StatusName = sanitizeTerminalLine(it.Status.Name)
		wi.StatusCategoryColor = sanitizeTerminalLine(it.Status.CategoryColor)
		wi.Status = wi.StatusName
	}
	if it.Priority != nil {
		id := it.Priority.ID
		wi.PriorityID = &id
		wi.PriorityName = sanitizeTerminalLine(it.Priority.Name)
		wi.PriorityIcon = sanitizeTerminalLine(it.Priority.Icon)
		wi.PriorityColor = sanitizeTerminalLine(it.Priority.Color)
		wi.Priority = wi.PriorityName
	}
	if it.Assignee != nil {
		id := it.Assignee.ID
		wi.AssigneeID = &id
		wi.AssigneeName = sanitizeTerminalLine(it.Assignee.FullName)
		wi.AssigneeEmail = sanitizeTerminalLine(it.Assignee.Email)
	}
	if it.Creator != nil {
		id := it.Creator.ID
		wi.CreatorID = &id
		wi.CreatorName = sanitizeTerminalLine(it.Creator.FullName)
		wi.CreatorEmail = sanitizeTerminalLine(it.Creator.Email)
	}
	return wi
}

func commentFromV1(c v1CommentResponse) Comment {
	out := Comment{
		ID:        c.ID,
		ItemID:    c.ItemID,
		Content:   sanitizeTerminalText(c.Content),
		CreatedAt: c.CreatedAt.Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.Format(time.RFC3339),
	}
	if c.Author != nil {
		out.AuthorID = c.Author.ID
		name := sanitizeTerminalLine(c.Author.FullName)
		email := sanitizeTerminalLine(c.Author.Email)
		out.AuthorName = &name
		out.AuthorEmail = &email
	}
	return out
}

// ─── Message types for tea.Cmd ────────────────────────────────────────

type workspacesLoadedMsg struct {
	workspaces []Workspace
}

type workItemsLoadedMsg struct {
	items []WorkItem
}

type commentsLoadedMsg struct {
	comments []Comment
}

type workItemUpdatedMsg struct{}

type workItemCreatedMsg struct{}

type commentCreatedMsg struct{}

type timeLogCreatedMsg struct{}

type timeProjectsLoadedMsg struct {
	projects []TimeProject
}

type statusesLoadedMsg struct {
	statuses []Status
}

type prioritiesLoadedMsg struct {
	priorities []Priority
}

type errorMsg struct {
	error string
}

// ─── API methods that return tea.Cmd ─────────────────────────────────

func (m Model) loadWorkspaces() tea.Cmd {
	return tea.Cmd(func() tea.Msg {
		workspaces, err := m.apiClient.getWorkspaces()
		if err != nil {
			return errorMsg{error: err.Error()}
		}
		return workspacesLoadedMsg{workspaces: workspaces}
	})
}

func (m Model) loadWorkItems(workspaceID int) tea.Cmd {
	return tea.Cmd(func() tea.Msg {
		items, err := m.apiClient.getWorkItems(workspaceID)
		if err != nil {
			return errorMsg{error: err.Error()}
		}
		return workItemsLoadedMsg{items: items}
	})
}

func (m Model) loadComments(itemID int) tea.Cmd {
	return tea.Cmd(func() tea.Msg {
		comments, err := m.apiClient.getComments(itemID)
		if err != nil {
			return errorMsg{error: err.Error()}
		}
		return commentsLoadedMsg{comments: comments}
	})
}

func (m Model) loadStatuses(workspaceID int) tea.Cmd {
	return tea.Cmd(func() tea.Msg {
		statuses, err := m.apiClient.getStatuses(workspaceID)
		if err != nil {
			return errorMsg{error: err.Error()}
		}
		return statusesLoadedMsg{statuses: statuses}
	})
}

func (m Model) loadPriorities() tea.Cmd {
	return tea.Cmd(func() tea.Msg {
		priorities, err := m.apiClient.getPriorities()
		if err != nil {
			return errorMsg{error: err.Error()}
		}
		return prioritiesLoadedMsg{priorities: priorities}
	})
}

func (m Model) loadTimeProjects() tea.Cmd {
	return tea.Cmd(func() tea.Msg {
		projects, err := m.apiClient.getTimeProjects()
		if err != nil {
			return errorMsg{error: err.Error()}
		}
		return timeProjectsLoadedMsg{projects: projects}
	})
}

func (m Model) updateWorkItem(itemID int, title, description string, statusID, priorityID *int) tea.Cmd {
	return tea.Cmd(func() tea.Msg {
		err := m.apiClient.updateWorkItem(itemID, title, description, statusID, priorityID)
		if err != nil {
			return errorMsg{error: err.Error()}
		}
		return workItemUpdatedMsg{}
	})
}

func (m Model) createWorkItem(workspaceID int, title, description string, priorityID *int) tea.Cmd {
	return tea.Cmd(func() tea.Msg {
		err := m.apiClient.createWorkItem(workspaceID, title, description, priorityID)
		if err != nil {
			return errorMsg{error: err.Error()}
		}
		return workItemCreatedMsg{}
	})
}

func (m Model) createComment(itemID int, content string) tea.Cmd {
	return tea.Cmd(func() tea.Msg {
		err := m.apiClient.createComment(itemID, content)
		if err != nil {
			return errorMsg{error: err.Error()}
		}
		return commentCreatedMsg{}
	})
}

func (m Model) createTimeLog(itemID, projectID int, description, duration, date, startTime string) tea.Cmd {
	return tea.Cmd(func() tea.Msg {
		err := m.apiClient.createTimeLog(itemID, projectID, description, duration, date, startTime)
		if err != nil {
			return errorMsg{error: err.Error()}
		}
		return timeLogCreatedMsg{}
	})
}

// ─── HTTP API methods ─────────────────────────────────────────────────

func (c *APIClient) getWorkspaces() ([]Workspace, error) {
	var resp v1WorkspacesPage
	if err := c.doGet("/rest/api/v1/workspaces", authBearer, &resp); err != nil {
		return nil, err
	}
	out := make([]Workspace, 0, len(resp.Data))
	for _, w := range resp.Data {
		out = append(out, workspaceFromV1(w))
	}
	return out, nil
}

func (c *APIClient) getWorkItems(workspaceID int) ([]WorkItem, error) {
	var resp v1ItemsPage
	// expand= populates the nested status/priority/assignee/creator the
	// list view chips need; without it those come back nil.
	path := fmt.Sprintf("/rest/api/v1/workspaces/%d/items?expand=status,priority,assignee,creator", workspaceID)
	if err := c.doGet(path, authBearer, &resp); err != nil {
		return nil, err
	}
	out := make([]WorkItem, 0, len(resp.Data))
	for _, it := range resp.Data {
		out = append(out, workItemFromV1(it))
	}
	return out, nil
}

func (c *APIClient) getComments(itemID int) ([]Comment, error) {
	// v1's comments list endpoint isn't paginated for items; it returns a bare
	// array. If a future v1 release wraps it in {data,pagination} we'll need
	// to swap the decoder here.
	var raw []v1CommentResponse
	if err := c.doGet(fmt.Sprintf("/rest/api/v1/items/%d/comments?expand=author", itemID), authBearer, &raw); err != nil {
		return nil, err
	}
	out := make([]Comment, 0, len(raw))
	for _, c2 := range raw {
		out = append(out, commentFromV1(c2))
	}
	return out, nil
}

func (c *APIClient) getStatuses(workspaceID int) ([]Status, error) {
	// Workspace-scoped: /workspaces/{id}/statuses requires workspaces:read
	// (the global /statuses route would need a separate statuses:read scope).
	var raw []v1StatusSummary
	if err := c.doGet(fmt.Sprintf("/rest/api/v1/workspaces/%d/statuses", workspaceID), authBearer, &raw); err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(raw))
	for _, s := range raw {
		out = append(out, Status{
			ID:            s.ID,
			Name:          sanitizeTerminalLine(s.Name),
			CategoryID:    s.CategoryID,
			CategoryName:  sanitizeTerminalLine(s.CategoryName),
			CategoryColor: sanitizeTerminalLine(s.CategoryColor),
		})
	}
	return out, nil
}

func (c *APIClient) getPriorities() ([]Priority, error) {
	var raw []v1PrioritySummary
	if err := c.doGet("/rest/api/v1/priorities", authBearer, &raw); err != nil {
		return nil, err
	}
	out := make([]Priority, 0, len(raw))
	for _, p := range raw {
		out = append(out, Priority{
			ID:    p.ID,
			Name:  sanitizeTerminalLine(p.Name),
			Icon:  sanitizeTerminalLine(p.Icon),
			Color: sanitizeTerminalLine(p.Color),
		})
	}
	return out, nil
}

func (c *APIClient) getTimeProjects() ([]TimeProject, error) {
	// Legacy /api/* + session auth — v1 doesn't yet expose /time/projects.
	var projects []TimeProject
	if err := c.doGet("/api/time/projects", authSession, &projects); err != nil {
		return nil, err
	}
	for i := range projects {
		projects[i].Name = sanitizeTerminalLine(projects[i].Name)
		projects[i].Description = sanitizeTerminalStringPtr(projects[i].Description, false)
		projects[i].CustomerName = sanitizeTerminalStringPtr(projects[i].CustomerName, true)
	}
	return projects, nil
}

// updateWorkItem updates title/description/priority via PUT, then if statusID
// changed, drives the workflow transition through POST /items/{id}/transition.
// v1's ItemUpdateRequest deliberately rejects status_id to keep workflow
// validator/condition rules in the hot path.
func (c *APIClient) updateWorkItem(itemID int, title, description string, statusID, priorityID *int) error {
	body := map[string]interface{}{
		"title":       title,
		"description": description,
	}
	if priorityID != nil {
		body["priority_id"] = *priorityID
	}
	if err := c.doMutate("PUT", fmt.Sprintf("/rest/api/v1/items/%d", itemID), authBearer, body, nil); err != nil {
		return err
	}
	if statusID != nil {
		transition := map[string]interface{}{"to_status_id": *statusID}
		if err := c.doMutate("POST", fmt.Sprintf("/rest/api/v1/items/%d/transition", itemID), authBearer, transition, nil); err != nil {
			return fmt.Errorf("status transition: %w", err)
		}
	}
	return nil
}

func (c *APIClient) createWorkItem(workspaceID int, title, description string, priorityID *int) error {
	body := map[string]interface{}{
		"workspace_id": workspaceID,
		"title":        title,
		"description":  description,
	}
	if priorityID != nil {
		body["priority_id"] = *priorityID
	}
	return c.doMutate("POST", "/rest/api/v1/items", authBearer, body, nil)
}

func (c *APIClient) createComment(itemID int, content string) error {
	// v1's request shape drops the author_id field — the user is identified
	// from the bearer token. Less to forge.
	body := map[string]interface{}{"content": content}
	return c.doMutate("POST", fmt.Sprintf("/rest/api/v1/items/%d/comments", itemID), authBearer, body, nil)
}

func (c *APIClient) createTimeLog(itemID, projectID int, description, duration, date, startTime string) error {
	// Legacy /api/* + session auth — v1 doesn't yet expose /time/worklogs.
	data := CreateTimeLogRequest{
		ProjectID:   projectID,
		ItemID:      &itemID,
		Description: description,
		Date:        date,
		StartTime:   startTime,
		Duration:    duration,
	}
	return c.doMutate("POST", "/api/time/worklogs", authSession, data, nil)
}
