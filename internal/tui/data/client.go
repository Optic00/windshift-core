// Package data is the TUI's only gateway to the Windshift API: the HTTP
// client, the wire/domain types, the tea.Cmd loaders and the message types
// they emit.
//
// Sanitization rule: no string from the API reaches a renderer except
// through a converter in types.go (or an explicit Sanitize* call at the
// ingestion point). See sanitize.go.
package data

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// authMode picks which header doGet/doMutate attaches.
type authMode int

const (
	authBearer authMode = iota
	authSession
)

// Client handles communication with the Windshift API.
//
// The TUI's endpoints split across two surfaces:
//   - /rest/api/v1/... uses bearer auth (Authorization: Bearer crw_*),
//     populated from an SSH-minted temp API token.
//   - /api/...           uses session auth (X-Session-Token), populated from
//     an SSH-minted session row.
//
// Once v1 grows /time/projects and /time/worklogs endpoints, the legacy
// session path can be removed entirely.
type Client struct {
	baseURL      string
	httpClient   *http.Client
	sessionToken string
	bearerToken  string
}

// NewClient creates a new API client.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// SetSessionToken sets the session token used by legacy /api/* calls.
func (c *Client) SetSessionToken(token string) {
	c.sessionToken = token
}

// SetBearerToken sets the API token used by /rest/api/v1/* calls.
func (c *Client) SetBearerToken(token string) {
	c.bearerToken = token
}

func (c *Client) setAuth(req *http.Request, mode authMode) {
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
func (c *Client) doGet(path string, mode authMode, result interface{}) error {
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
		return fmt.Errorf("API error: %s - %s", resp.Status, SanitizeText(string(body)))
	}

	if result == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(result)
}

// doMutate performs a mutating HTTP request (POST, PUT, etc.) with a JSON body.
// If result is non-nil, the response body is decoded into it.
func (c *Client) doMutate(method, path string, mode authMode, body, result interface{}) error { //nolint:unparam // result is wired for callers that will decode bodies; all current call sites pass nil
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
		return fmt.Errorf("API error: %s - %s", resp.Status, SanitizeText(string(body)))
	}

	if result == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(result)
}

// ─── HTTP API methods ─────────────────────────────────────────────────

func (c *Client) getWorkspaces() ([]Workspace, error) {
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

// maxWorkItems caps how many items getWorkItems accumulates across pages —
// beyond this the board truncates (and says so) rather than hammering the
// API with dozens of page fetches.
const maxWorkItems = 500

// getWorkItems fetches all pages of a workspace's items (the v1 endpoint
// caps limit at 100) up to maxWorkItems. The bool result reports truncation.
func (c *Client) getWorkItems(workspaceID int) ([]WorkItem, bool, error) {
	out := make([]WorkItem, 0, 64)
	for page := 1; ; page++ {
		var resp v1ItemsPage
		// expand= populates the nested status/priority/assignee/creator the
		// list view chips need; without it those come back nil.
		path := fmt.Sprintf("/rest/api/v1/workspaces/%d/items?expand=status,priority,assignee,creator&page=%d&limit=100", workspaceID, page)
		if err := c.doGet(path, authBearer, &resp); err != nil {
			return nil, false, err
		}
		for _, it := range resp.Data {
			out = append(out, workItemFromV1(it))
		}
		if len(resp.Data) == 0 || page >= resp.Pagination.TotalPages {
			return out, false, nil
		}
		if len(out) >= maxWorkItems {
			return out, true, nil
		}
	}
}

func (c *Client) getComments(itemID int) ([]Comment, error) {
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

func (c *Client) getStatuses(workspaceID int) ([]Status, error) {
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
			Name:          SanitizeLine(s.Name),
			CategoryID:    s.CategoryID,
			CategoryName:  SanitizeLine(s.CategoryName),
			CategoryColor: SanitizeLine(s.CategoryColor),
		})
	}
	return out, nil
}

func (c *Client) getPriorities() ([]Priority, error) {
	var raw []v1PrioritySummary
	if err := c.doGet("/rest/api/v1/priorities", authBearer, &raw); err != nil {
		return nil, err
	}
	out := make([]Priority, 0, len(raw))
	for _, p := range raw {
		out = append(out, Priority{
			ID:    p.ID,
			Name:  SanitizeLine(p.Name),
			Icon:  SanitizeLine(p.Icon),
			Color: SanitizeLine(p.Color),
		})
	}
	return out, nil
}

func (c *Client) getTimeProjects() ([]TimeProject, error) {
	// Legacy /api/* + session auth — v1 doesn't yet expose /time/projects.
	var projects []TimeProject
	if err := c.doGet("/api/time/projects", authSession, &projects); err != nil {
		return nil, err
	}
	for i := range projects {
		projects[i].Name = SanitizeLine(projects[i].Name)
		projects[i].Description = SanitizeStringPtr(projects[i].Description, false)
		projects[i].CustomerName = SanitizeStringPtr(projects[i].CustomerName, true)
	}
	return projects, nil
}

// updateWorkItem updates title/description/priority via PUT, then if statusID
// changed, drives the workflow transition through POST /items/{id}/transition.
// v1's ItemUpdateRequest deliberately rejects status_id to keep workflow
// validator/condition rules in the hot path.
func (c *Client) updateWorkItem(itemID int, title, description string, statusID, priorityID *int) error {
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

func (c *Client) createWorkItem(workspaceID int, title, description string, priorityID *int) error {
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

func (c *Client) createComment(itemID int, content string) error {
	// v1's request shape drops the author_id field — the user is identified
	// from the bearer token. Less to forge.
	body := map[string]interface{}{"content": content}
	return c.doMutate("POST", fmt.Sprintf("/rest/api/v1/items/%d/comments", itemID), authBearer, body, nil)
}

func (c *Client) createTimeLog(itemID, projectID int, description, duration, date, startTime string) error {
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
