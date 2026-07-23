package wscli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client provides methods for calling the Windshift API
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient creates a new API client
func NewClient() (*Client, error) {
	if cfg.Server.URL == "" {
		return nil, fmt.Errorf("server URL not configured. Set WS_URL, use --url, or run 'ws config init'")
	}
	if cfg.Server.Token == "" {
		return nil, fmt.Errorf("API token not configured. Set WS_TOKEN, use --token, or run 'ws config init'")
	}

	return &Client{
		baseURL: strings.TrimSuffix(cfg.Server.URL, "/"),
		token:   cfg.Server.Token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// APIError represents an error response from the API. Status carries
// the HTTP status code so callers can branch on 404/403/etc. without
// pattern-matching the message string. Zero means "unknown" (e.g.
// transport failure before a response arrived).
type APIError struct {
	Status int    `json:"-"`
	Code   string `json:"code"`
	// Message is the human-readable error. The cookie-auth surface puts
	// it under "message"; the v1 REST surface (restapi.ErrorResponse)
	// puts it under "error". Accept both so we don't fall back to the
	// machine-readable Code on v1 responses.
	Message      string      `json:"message"`
	ErrorMessage string      `json:"error"`
	Details      interface{} `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.ErrorMessage != "" {
		return e.ErrorMessage
	}
	return e.Code
}

// doRequest executes an HTTP request with authentication. WS_DEBUG_HTTP=1
// in the env enables one-line request/response logging on stderr — useful
// when triaging server-side errors from the CLI.
func (c *Client) doRequest(method, path string, body, result interface{}) error {
	var bodyReader io.Reader
	var jsonBody []byte
	if body != nil {
		var err error
		jsonBody, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}
	if debugHTTP {
		_, _ = fmt.Fprintf(stderr, "[ws-debug] %s %s body=%s\n", method, path, string(jsonBody))
	}

	reqURL := c.baseURL + path
	req, err := http.NewRequest(method, reqURL, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req) //nolint:gosec // G704: URL from server config, not user input
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if debugHTTP {
		// #nosec G705 -- writing to a CLI terminal, not HTML; G705 is checking for an XSS sink that doesn't exist here
		_, _ = fmt.Fprintf(stderr, "[ws-debug] -> status=%d body=%s\n", resp.StatusCode, string(respBody))
	}
	if resp.StatusCode >= 400 {
		var apiErr APIError
		if err := json.Unmarshal(respBody, &apiErr); err == nil && (apiErr.Code != "" || apiErr.Message != "") {
			apiErr.Status = resp.StatusCode
			return &apiErr
		}
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}
	}

	return nil
}

// GET performs a GET request
func (c *Client) GET(path string, result interface{}) error {
	return c.doRequest("GET", path, nil, result)
}

// POST performs a POST request
func (c *Client) POST(path string, body, result interface{}) error {
	return c.doRequest("POST", path, body, result)
}

// PUT performs a PUT request
func (c *Client) PUT(path string, body, result interface{}) error {
	return c.doRequest("PUT", path, body, result)
}

// PATCH performs a PATCH request
func (c *Client) PATCH(path string, body, result interface{}) error {
	return c.doRequest("PATCH", path, body, result)
}

// DELETE performs a DELETE request
func (c *Client) DELETE(path string) error {
	return c.doRequest("DELETE", path, nil, nil)
}

// ============================================
// REST API v1 Methods
// ============================================

// GetCurrentUser returns the authenticated user
func (c *Client) GetCurrentUser() (*User, error) {
	var user User
	if err := c.GET("/rest/api/v1/users/me", &user); err != nil {
		return nil, err
	}
	return &user, nil
}

// ListItems lists items with optional filters
func (c *Client) ListItems(filters map[string]string) (*PaginatedResponse[Item], error) {
	path := "/rest/api/v1/items"
	if len(filters) > 0 {
		params := url.Values{}
		for k, v := range filters {
			params.Set(k, v)
		}
		path += "?" + params.Encode()
	}

	var resp PaginatedResponse[Item]
	if err := c.GET(path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetItem gets a single item by ID with optional expansions
func (c *Client) GetItem(id int, expand string) (*Item, error) {
	path := fmt.Sprintf("/rest/api/v1/items/%d", id)
	if expand != "" {
		path += "?expand=" + url.QueryEscape(expand)
	}

	var item Item
	if err := c.GET(path, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

// GetItemByKeyAndNumber gets an item by its (workspace_key, workspace_item_number) pair
// via the direct-lookup endpoint, avoiding paginating over /rest/api/v1/items when
// resolving a KEY-NUMBER identifier.
func (c *Client) GetItemByKeyAndNumber(wsKey string, number int) (*Item, error) {
	path := fmt.Sprintf("/rest/api/v1/workspaces/%s/items/%d", url.PathEscape(wsKey), number)
	var item Item
	if err := c.GET(path, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

// GetItemChildren gets child items of a given item
func (c *Client) GetItemChildren(id int) ([]Item, error) {
	var items []Item
	if err := c.GET(fmt.Sprintf("/rest/api/v1/items/%d/children", id), &items); err != nil {
		return nil, err
	}
	return items, nil
}

// CreateItem creates a new item
func (c *Client) CreateItem(req ItemCreateRequest) (*Item, error) {
	var item Item
	if err := c.POST("/rest/api/v1/items", req, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

// UpdateItem updates an item
func (c *Client) UpdateItem(id int, req ItemUpdateRequest) (*Item, error) {
	var item Item
	if err := c.PUT(fmt.Sprintf("/rest/api/v1/items/%d", id), req, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

// GetItemTransitions gets valid status transitions for an item
func (c *Client) GetItemTransitions(id int) ([]Transition, error) {
	var transitions []Transition
	if err := c.GET(fmt.Sprintf("/rest/api/v1/items/%d/transitions", id), &transitions); err != nil {
		return nil, err
	}
	return transitions, nil
}

// TransitionItem performs a workflow status transition. Use this instead of
// setting status_id via UpdateItem — the update endpoint rejects status_id
// because transitions must run through the workflow + condition pipeline.
func (c *Client) TransitionItem(id, toStatusID int) (*TransitionResult, error) {
	var result TransitionResult
	req := TransitionRequest{ToStatusID: toStatusID}
	if err := c.POST(fmt.Sprintf("/rest/api/v1/items/%d/transition", id), req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ChangeItemType changes an item's item type through the dedicated endpoint.
func (c *Client) ChangeItemType(id, targetItemTypeID int, targetStatusID *int) (*Item, error) {
	var item Item
	req := ItemTypeChangeRequest{TargetItemTypeID: targetItemTypeID, TargetStatusID: targetStatusID}
	if err := c.POST(fmt.Sprintf("/rest/api/v1/items/%d/change-type", id), req, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

// ListWorkspaces lists all accessible workspaces
func (c *Client) ListWorkspaces() (*PaginatedResponse[Workspace], error) {
	var resp PaginatedResponse[Workspace]
	if err := c.GET("/rest/api/v1/workspaces", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetWorkspace gets a workspace by ID
func (c *Client) GetWorkspace(id int) (*Workspace, error) {
	var ws Workspace
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d", id), &ws); err != nil {
		return nil, err
	}
	return &ws, nil
}

// GetWorkspaceStatuses gets statuses for a workspace
func (c *Client) GetWorkspaceStatuses(workspaceID int) ([]Status, error) {
	var statuses []Status
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/statuses", workspaceID), &statuses); err != nil {
		return nil, err
	}
	return statuses, nil
}

// GetCompletedStatuses gets statuses where is_completed = true for a workspace
func (c *Client) GetCompletedStatuses(workspaceID int) ([]Status, error) {
	var statuses []Status
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/statuses/completed", workspaceID), &statuses); err != nil {
		return nil, err
	}
	return statuses, nil
}

// ListStatuses lists all statuses
func (c *Client) ListStatuses() ([]Status, error) {
	var statuses []Status
	if err := c.GET("/rest/api/v1/statuses", &statuses); err != nil {
		return nil, err
	}
	return statuses, nil
}

// ListItemTypes lists all item types
func (c *Client) ListItemTypes() ([]ItemType, error) {
	var types []ItemType
	if err := c.GET("/rest/api/v1/item-types", &types); err != nil {
		return nil, err
	}
	return types, nil
}

// GetWorkspaceItemTypes lists the item types enabled for a workspace's
// configuration set.
func (c *Client) GetWorkspaceItemTypes(workspaceID int) ([]ItemType, error) {
	var types []ItemType
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/item-types", workspaceID), &types); err != nil {
		return nil, err
	}
	return types, nil
}

// ListPriorities lists all priorities
func (c *Client) ListPriorities() ([]Priority, error) {
	var priorities []Priority
	if err := c.GET("/rest/api/v1/priorities", &priorities); err != nil {
		return nil, err
	}
	return priorities, nil
}

// GetWorkspacePriorities lists the priorities enabled for a workspace's configuration set
func (c *Client) GetWorkspacePriorities(workspaceID int) ([]Priority, error) {
	var priorities []Priority
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/priorities", workspaceID), &priorities); err != nil {
		return nil, err
	}
	return priorities, nil
}

// ListWorkflows lists all workflows
func (c *Client) ListWorkflows() ([]Workflow, error) {
	var workflows []Workflow
	if err := c.GET("/rest/api/v1/workflows", &workflows); err != nil {
		return nil, err
	}
	return workflows, nil
}

// GetWorkspaceWorkflows lists the distinct workflows effective for a
// workspace's configured item types.
func (c *Client) GetWorkspaceWorkflows(workspaceID int) ([]Workflow, error) {
	var workflows []Workflow
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/workflows", workspaceID), &workflows); err != nil {
		return nil, err
	}
	return workflows, nil
}

// GetWorkflowTransitions gets transitions for a workflow
func (c *Client) GetWorkflowTransitions(workflowID int) ([]Transition, error) {
	var transitions []Transition
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workflows/%d/transitions", workflowID), &transitions); err != nil {
		return nil, err
	}
	return transitions, nil
}

// ============================================
// Test Management API Methods
// ============================================
//
// Test routes live on /rest/api/v1 (gated by tests:read / tests:write)
// since WI-68 mirrored the read + run-lifecycle slice off the legacy
// cookie surface. Full catalog CRUD (folders, case writes, set writes,
// labels) is still cookie-only; reach it through the SPA / admin tools
// until a follow-up ticket lifts it to v1.

// ListTestCases lists test cases in a workspace
func (c *Client) ListTestCases(workspaceID int, folderID string) ([]TestCase, error) {
	path := fmt.Sprintf("/rest/api/v1/workspaces/%d/test-cases", workspaceID)
	if folderID != "" {
		path += "?folder_id=" + url.QueryEscape(folderID)
	} else {
		path += "?all=true"
	}

	var cases []TestCase
	if err := c.GET(path, &cases); err != nil {
		return nil, err
	}
	return cases, nil
}

// GetTestCase gets a test case by ID
func (c *Client) GetTestCase(workspaceID, id int) (*TestCase, error) {
	var tc TestCase
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/test-cases/%d", workspaceID, id), &tc); err != nil {
		return nil, err
	}
	return &tc, nil
}

// GetTestSteps gets steps for a test case
func (c *Client) GetTestSteps(workspaceID, testCaseID int) ([]TestStep, error) {
	var steps []TestStep
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/test-cases/%d/steps", workspaceID, testCaseID), &steps); err != nil {
		return nil, err
	}
	return steps, nil
}

// ListTestRuns lists test runs in a workspace
func (c *Client) ListTestRuns(workspaceID int, assigneeID string) ([]TestRun, error) {
	path := fmt.Sprintf("/rest/api/v1/workspaces/%d/test-runs", workspaceID)
	if assigneeID != "" {
		path += "?assignee_id=" + url.QueryEscape(assigneeID)
	}

	var runs []TestRun
	if err := c.GET(path, &runs); err != nil {
		return nil, err
	}
	return runs, nil
}

// GetTestRun gets a test run by ID
func (c *Client) GetTestRun(workspaceID, id int) (*TestRun, error) {
	var run TestRun
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/test-runs/%d", workspaceID, id), &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// CreateTestRun creates a new test run
func (c *Client) CreateTestRun(workspaceID int, req TestRunCreateRequest) (*TestRun, error) {
	var run TestRun
	if err := c.POST(fmt.Sprintf("/rest/api/v1/workspaces/%d/test-runs", workspaceID), req, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// EndTestRun ends a test run
func (c *Client) EndTestRun(workspaceID, id int) error {
	return c.POST(fmt.Sprintf("/rest/api/v1/workspaces/%d/test-runs/%d/end", workspaceID, id), nil, nil)
}

// GetTestRunResults gets results for a test run
func (c *Client) GetTestRunResults(workspaceID, runID int) ([]TestResult, error) {
	var results []TestResult
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/test-runs/%d/results", workspaceID, runID), &results); err != nil {
		return nil, err
	}
	return results, nil
}

// UpdateTestResult updates a test result
func (c *Client) UpdateTestResult(workspaceID, runID, resultID int, req TestResultUpdateRequest) error {
	return c.PUT(fmt.Sprintf("/rest/api/v1/workspaces/%d/test-runs/%d/results/%d", workspaceID, runID, resultID), req, nil)
}

// ListTestSets lists test sets in a workspace
func (c *Client) ListTestSets(workspaceID int) ([]TestSet, error) {
	var sets []TestSet
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/test-sets", workspaceID), &sets); err != nil {
		return nil, err
	}
	return sets, nil
}

// GetTestSet gets a test set by ID
func (c *Client) GetTestSet(workspaceID, id int) (*TestSet, error) {
	var set TestSet
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/test-sets/%d", workspaceID, id), &set); err != nil {
		return nil, err
	}
	return &set, nil
}

// GetTestSetTestCases gets test cases in a test set
func (c *Client) GetTestSetTestCases(workspaceID, setID int) ([]TestCase, error) {
	var cases []TestCase
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/test-sets/%d/test-cases", workspaceID, setID), &cases); err != nil {
		return nil, err
	}
	return cases, nil
}

// ExecuteRunTemplate executes a test run template
func (c *Client) ExecuteRunTemplate(workspaceID, templateID int) (*TestRun, error) {
	var run TestRun
	if err := c.POST(fmt.Sprintf("/rest/api/v1/workspaces/%d/test-run-templates/%d/execute", workspaceID, templateID), nil, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// ============================================
// Comment Methods
// ============================================

// GetComments lists comments on an item
func (c *Client) GetComments(itemID int) ([]Comment, error) {
	var comments []Comment
	if err := c.GET(fmt.Sprintf("/rest/api/v1/items/%d/comments", itemID), &comments); err != nil {
		return nil, err
	}
	return comments, nil
}

// CreateComment adds a comment to an item
func (c *Client) CreateComment(itemID int, content string) (*Comment, error) {
	req := map[string]string{"content": content}
	var comment Comment
	if err := c.POST(fmt.Sprintf("/rest/api/v1/items/%d/comments", itemID), req, &comment); err != nil {
		return nil, err
	}
	return &comment, nil
}

// UpdateComment edits an existing comment
func (c *Client) UpdateComment(commentID int, content string) (*Comment, error) {
	req := map[string]string{"content": content}
	var comment Comment
	if err := c.PUT(fmt.Sprintf("/rest/api/v1/comments/%d", commentID), req, &comment); err != nil {
		return nil, err
	}
	return &comment, nil
}

// DeleteComment removes a comment
func (c *Client) DeleteComment(commentID int) error {
	return c.DELETE(fmt.Sprintf("/rest/api/v1/comments/%d", commentID))
}

// ============================================
// Diagram Methods
// ============================================
//
// Diagram routes live on /rest/api/v1 (gated by items:read / items:write)
// since WI-71 mirrored them off the legacy cookie surface. The handler
// accepts {name, diagram_data} where diagram_data is opaque text —
// either an Excalidraw scene JSON or a {type:"mermaid",source:...} seed
// wrapper that the frontend expands on first open.

// ListDiagrams returns all diagrams for an item.
func (c *Client) ListDiagrams(itemID int) ([]Diagram, error) {
	// v1 list endpoints wrap the array in {"items":[...]} for forward
	// compatibility with pagination metadata; unwrap that here so callers
	// keep receiving a plain slice.
	var envelope struct {
		Items []Diagram `json:"items"`
	}
	if err := c.GET(fmt.Sprintf("/rest/api/v1/items/%d/diagrams", itemID), &envelope); err != nil {
		return nil, err
	}
	return envelope.Items, nil
}

// GetDiagram fetches a single diagram by ID.
func (c *Client) GetDiagram(id int) (*Diagram, error) {
	var d Diagram
	if err := c.GET(fmt.Sprintf("/rest/api/v1/diagrams/%d", id), &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// CreateDiagram attaches a new diagram to an item. diagramData is the raw
// payload to persist — callers building from mermaid should pass the
// JSON-encoded {"type":"mermaid","source":...} wrapper.
func (c *Client) CreateDiagram(itemID int, name, diagramData string) (*Diagram, error) {
	req := map[string]string{"name": name, "diagram_data": diagramData}
	var d Diagram
	if err := c.POST(fmt.Sprintf("/rest/api/v1/items/%d/diagrams", itemID), req, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// UpdateDiagram overwrites a diagram's name and data.
func (c *Client) UpdateDiagram(id int, name, diagramData string) (*Diagram, error) {
	req := map[string]string{"name": name, "diagram_data": diagramData}
	var d Diagram
	if err := c.PUT(fmt.Sprintf("/rest/api/v1/diagrams/%d", id), req, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// DeleteDiagram removes a diagram.
func (c *Client) DeleteDiagram(id int) error {
	return c.DELETE(fmt.Sprintf("/rest/api/v1/diagrams/%d", id))
}

// ============================================
// Attachment Methods
// ============================================
//
// Both endpoints live on the public REST v1 surface. The legacy
// /api/attachments/{id}/download route explicitly rejects bearer tokens
// (cookie-auth only), so the CLI must use /rest/api/v1/*.

// UploadPageAttachment uploads a file as an attachment on a workspace
// knowledge page via POST /rest/api/v1/workspaces/{wsID}/pages/{pageID}/attachments.
// The response is the legacy attachment-upload envelope: a JSON object
// with `attachment.{id,filename,...}`. Returns the parsed attachment so
// callers can build the embed URL `/api/attachments/{id}/download`.
//
// The server gate is `pages:write` + per-page `page.edit` (Editor role
// on the workspace satisfies that by default). A 404 from the server
// means either the page id does not exist or the caller lacks edit
// permission — page handlers never distinguish the two on purpose.
func (c *Client) UploadPageAttachment(workspaceID, pageID int, originalFilename string, body io.Reader) (*Attachment, error) {
	var buf bytes.Buffer
	mp := multipart.NewWriter(&buf)
	part, err := mp.CreateFormFile("file", originalFilename)
	if err != nil {
		return nil, fmt.Errorf("multipart create part: %w", err)
	}
	if _, err := io.Copy(part, body); err != nil {
		return nil, fmt.Errorf("multipart copy body: %w", err)
	}
	if err := mp.Close(); err != nil {
		return nil, fmt.Errorf("multipart close: %w", err)
	}

	reqURL := fmt.Sprintf("%s/rest/api/v1/workspaces/%d/pages/%d/attachments", c.baseURL, workspaceID, pageID)
	if debugHTTP {
		_, _ = fmt.Fprintf(stderr, "[ws-debug] POST %s upload=%s bytes=%d\n", reqURL, originalFilename, buf.Len())
	}

	req, err := http.NewRequest("POST", reqURL, &buf)
	if err != nil {
		return nil, fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", mp.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req) //nolint:gosec // G704: URL from server config
	if err != nil {
		return nil, fmt.Errorf("upload request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read upload response: %w", err)
	}
	if debugHTTP {
		// #nosec G705 -- writing to a CLI terminal, not HTML
		_, _ = fmt.Fprintf(stderr, "[ws-debug] -> status=%d body=%s\n", resp.StatusCode, string(respBody))
	}

	if resp.StatusCode >= 400 {
		// Try v1's APIError shape first, then fall back to the legacy
		// {success:false,message:"..."} envelope the wrapped handler
		// emits for its own validation errors.
		var apiErr APIError
		if jerr := json.Unmarshal(respBody, &apiErr); jerr == nil && (apiErr.Code != "" || apiErr.Message != "") {
			apiErr.Status = resp.StatusCode
			return nil, &apiErr
		}
		var legacy struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if jerr := json.Unmarshal(respBody, &legacy); jerr == nil && (legacy.Message != "" || legacy.Error != "") {
			msg := legacy.Message
			if msg == "" {
				msg = legacy.Error
			}
			return nil, &APIError{Status: resp.StatusCode, Message: msg}
		}
		return nil, fmt.Errorf("upload failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	// Legacy envelope: {"success":true,"message":"...","attachment":{...}}.
	var envelope struct {
		Success    bool        `json:"success"`
		Message    string      `json:"message"`
		Attachment *Attachment `json:"attachment"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return nil, fmt.Errorf("parse upload response: %w", err)
	}
	if envelope.Attachment == nil || envelope.Attachment.ID == 0 {
		return nil, fmt.Errorf("upload response missing attachment id: %s", string(respBody))
	}
	return envelope.Attachment, nil
}

// ListAttachments returns all attachments on an item.
func (c *Client) ListAttachments(itemID int) ([]Attachment, error) {
	var atts []Attachment
	if err := c.GET(fmt.Sprintf("/rest/api/v1/items/%d/attachments", itemID), &atts); err != nil {
		return nil, err
	}
	return atts, nil
}

// DownloadAttachment streams the attachment bytes for the given id into w
// and returns the filename suggested by the server's Content-Disposition
// header. Falls back to "attachment-<id>" if no filename is advertised.
func (c *Client) DownloadAttachment(id int, w io.Writer) (string, error) {
	reqURL := c.baseURL + fmt.Sprintf("/rest/api/v1/attachments/%d/download", id)
	if debugHTTP {
		_, _ = fmt.Fprintf(stderr, "[ws-debug] GET %s\n", reqURL)
	}

	req, err := http.NewRequest("GET", reqURL, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req) //nolint:gosec // G704: URL from server config
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if debugHTTP {
		_, _ = fmt.Fprintf(stderr, "[ws-debug] -> status=%d content-type=%s\n", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	if resp.StatusCode >= 400 {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return "", fmt.Errorf("API error (status %d); failed to read response body: %w", resp.StatusCode, readErr)
		}
		var apiErr APIError
		if jerr := json.Unmarshal(body, &apiErr); jerr == nil && (apiErr.Code != "" || apiErr.Message != "") {
			apiErr.Status = resp.StatusCode
			return "", &apiErr
		}
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	filename := parseContentDispositionFilename(resp.Header.Get("Content-Disposition"))
	if filename == "" {
		filename = fmt.Sprintf("attachment-%d", id)
	}

	if _, err := io.Copy(w, resp.Body); err != nil {
		return filename, fmt.Errorf("failed to stream file: %w", err)
	}
	return filename, nil
}

// parseContentDispositionFilename extracts the filename from a
// Content-Disposition header. Returns "" if the header is missing,
// malformed, or has no filename parameter.
func parseContentDispositionFilename(header string) string {
	if header == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	return params["filename"]
}

// ============================================
// Helper Methods
// ============================================

// ============================================
// Milestone API Methods
// ============================================

// ListMilestones lists milestones with optional filters
func (c *Client) ListMilestones(filters map[string]string) (*PaginatedResponse[Milestone], error) {
	path := "/rest/api/v1/milestones"
	if len(filters) > 0 {
		params := url.Values{}
		for k, v := range filters {
			params.Set(k, v)
		}
		path += "?" + params.Encode()
	}

	var resp PaginatedResponse[Milestone]
	if err := c.GET(path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetMilestone gets a milestone by ID
func (c *Client) GetMilestone(id int) (*Milestone, error) {
	var milestone Milestone
	if err := c.GET(fmt.Sprintf("/rest/api/v1/milestones/%d", id), &milestone); err != nil {
		return nil, err
	}
	return &milestone, nil
}

// GetMilestoneProgress gets progress report for a milestone.
func (c *Client) GetMilestoneProgress(id int) (*MilestoneProgress, error) {
	var progress MilestoneProgress
	if err := c.GET(fmt.Sprintf("/rest/api/v1/milestones/%d/progress", id), &progress); err != nil {
		return nil, err
	}
	return &progress, nil
}

// CreateMilestone creates a new milestone
func (c *Client) CreateMilestone(req MilestoneCreateRequest) (*Milestone, error) {
	var milestone Milestone
	if err := c.POST("/rest/api/v1/milestones", req, &milestone); err != nil {
		return nil, err
	}
	return &milestone, nil
}

// UpdateMilestone updates an existing milestone
func (c *Client) UpdateMilestone(id int, req MilestoneUpdateRequest) (*Milestone, error) {
	var milestone Milestone
	if err := c.PUT(fmt.Sprintf("/rest/api/v1/milestones/%d", id), req, &milestone); err != nil {
		return nil, err
	}
	return &milestone, nil
}

// DeleteMilestone deletes a milestone
func (c *Client) DeleteMilestone(id int) error {
	return c.DELETE(fmt.Sprintf("/rest/api/v1/milestones/%d", id))
}

// Workspace-scoped milestone methods. These hit /rest/api/v1/workspaces/{id}/milestones[...]
// instead of the global routes; tokens scoped to one workspace can use them
// without needing global milestone access. The workspace is encoded in the
// URL — request bodies should not also carry workspace_id (the server ignores
// it on these routes).

// ListMilestonesInWorkspace lists milestones belonging to a single workspace.
func (c *Client) ListMilestonesInWorkspace(workspaceID int, filters map[string]string) (*PaginatedResponse[Milestone], error) {
	path := fmt.Sprintf("/rest/api/v1/workspaces/%d/milestones", workspaceID)
	if len(filters) > 0 {
		params := url.Values{}
		for k, v := range filters {
			params.Set(k, v)
		}
		path += "?" + params.Encode()
	}

	var resp PaginatedResponse[Milestone]
	if err := c.GET(path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetMilestoneInWorkspace fetches a milestone scoped to a workspace.
func (c *Client) GetMilestoneInWorkspace(workspaceID, milestoneID int) (*Milestone, error) {
	var milestone Milestone
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/milestones/%d", workspaceID, milestoneID), &milestone); err != nil {
		return nil, err
	}
	return &milestone, nil
}

// GetMilestoneProgressInWorkspace fetches a milestone's progress report scoped
// to a workspace.
func (c *Client) GetMilestoneProgressInWorkspace(workspaceID, milestoneID int) (*MilestoneProgress, error) {
	var progress MilestoneProgress
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/milestones/%d/progress", workspaceID, milestoneID), &progress); err != nil {
		return nil, err
	}
	return &progress, nil
}

// CreateMilestoneInWorkspace creates a milestone in a workspace. The body's
// WorkspaceID is cleared because the URL already carries it.
func (c *Client) CreateMilestoneInWorkspace(workspaceID int, req MilestoneCreateRequest) (*Milestone, error) {
	req.WorkspaceID = nil
	var milestone Milestone
	if err := c.POST(fmt.Sprintf("/rest/api/v1/workspaces/%d/milestones", workspaceID), req, &milestone); err != nil {
		return nil, err
	}
	return &milestone, nil
}

// UpdateMilestoneInWorkspace updates a workspace-scoped milestone.
func (c *Client) UpdateMilestoneInWorkspace(workspaceID, milestoneID int, req MilestoneUpdateRequest) (*Milestone, error) {
	var milestone Milestone
	if err := c.PUT(fmt.Sprintf("/rest/api/v1/workspaces/%d/milestones/%d", workspaceID, milestoneID), req, &milestone); err != nil {
		return nil, err
	}
	return &milestone, nil
}

// DeleteMilestoneInWorkspace deletes a workspace-scoped milestone.
func (c *Client) DeleteMilestoneInWorkspace(workspaceID, milestoneID int) error {
	return c.DELETE(fmt.Sprintf("/rest/api/v1/workspaces/%d/milestones/%d", workspaceID, milestoneID))
}

// ResolveMilestoneID resolves a milestone name or ID to an ID. When workspaceID
// is non-nil the lookup uses the workspace-scoped list endpoint; otherwise it
// falls back to the global list (which only callers with global access can use).
func (c *Client) ResolveMilestoneID(nameOrID string, workspaceID *int) (int, error) {
	// Try parsing as integer first. Use Atoi so malformed inputs like
	// "123abc" do not accidentally resolve as ID 123.
	if id, err := strconv.Atoi(nameOrID); err == nil {
		return id, nil
	}

	// Otherwise, look up by name (fuzzy match)
	var resp *PaginatedResponse[Milestone]
	var err error
	if workspaceID != nil {
		resp, err = c.ListMilestonesInWorkspace(*workspaceID, nil)
	} else {
		resp, err = c.ListMilestones(nil)
	}
	if err != nil {
		return 0, err
	}

	nameLower := strings.ToLower(nameOrID)
	var bestMatch *Milestone

	for i := range resp.Data {
		m := &resp.Data[i]
		mNameLower := strings.ToLower(m.Name)

		// Exact match (case-insensitive)
		if mNameLower == nameLower {
			return m.ID, nil
		}
		// Partial match - prefer first match
		if bestMatch == nil && strings.Contains(mNameLower, nameLower) {
			bestMatch = m
		}
	}

	if bestMatch != nil {
		return bestMatch.ID, nil
	}

	return 0, fmt.Errorf("milestone not found: %s", nameOrID)
}

// SearchItems performs a full-text search over items the caller can view
// via GET /rest/api/v1/search/items. limit <= 0 falls back to the server
// default page size.
// SearchItems searches items via the v1 search endpoint. When asCQL is true the
// query is sent as an explicit CQL filter (`ql`), so the server reports parse
// errors instead of falling back to full-text; otherwise it is sent as a
// full-text term (`q`) that the server auto-detects as CQL when it parses as a
// structured filter.
func (c *Client) SearchItems(query string, limit int, asCQL bool) (*PaginatedResponse[Item], error) {
	params := url.Values{}
	if asCQL {
		params.Set("ql", query)
	} else {
		params.Set("q", query)
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}

	var resp PaginatedResponse[Item]
	if err := c.GET("/rest/api/v1/search/items?"+params.Encode(), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetItemHistory returns the change history of an item. The endpoint
// returns the full history as a plain array.
func (c *Client) GetItemHistory(itemID int) ([]History, error) {
	var history []History
	if err := c.GET(fmt.Sprintf("/rest/api/v1/items/%d/history", itemID), &history); err != nil {
		return nil, err
	}
	return history, nil
}

// ============================================
// Item Label Methods
// ============================================
//
// Workspace-scoped work-item labels (catalog under /workspaces/{id}/labels,
// per-item attachments under /items/{id}/labels). Fully separate from the
// page-label system. Gated by items:read / items:write.

// ListLabels returns every item label defined in a workspace.
func (c *Client) ListLabels(workspaceID int) ([]Label, error) {
	var resp LabelListResponse
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/labels", workspaceID), &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// ListItemLabels returns the labels attached to a single item.
func (c *Client) ListItemLabels(itemID int) ([]Label, error) {
	var resp LabelListResponse
	if err := c.GET(fmt.Sprintf("/rest/api/v1/items/%d/labels", itemID), &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// SetItemLabels atomically replaces the label set on an item.
func (c *Client) SetItemLabels(itemID int, labelIDs []int) ([]Label, error) {
	var resp LabelListResponse
	if err := c.PUT(
		fmt.Sprintf("/rest/api/v1/items/%d/labels", itemID),
		ItemLabelSetRequest{LabelIDs: labelIDs},
		&resp,
	); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// AddItemLabel attaches a single label to an item.
func (c *Client) AddItemLabel(itemID, labelID int) ([]Label, error) {
	var resp LabelListResponse
	if err := c.POST(
		fmt.Sprintf("/rest/api/v1/items/%d/labels", itemID),
		ItemLabelAddRequest{LabelID: labelID},
		&resp,
	); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// RemoveItemLabel detaches a single label from an item.
func (c *Client) RemoveItemLabel(itemID, labelID int) error {
	return c.DELETE(fmt.Sprintf("/rest/api/v1/items/%d/labels/%d", itemID, labelID))
}

// ============================================
//
// Work item templates (WI-438): workspace-scoped reusable description bodies.
// Read surface gated by item-templates:read so agents can discover the scaffold
// a type enforces.

// ListItemTemplates returns the templates defined in a workspace. When
// itemTypeID > 0, the result is filtered to templates valid for that type
// (type-targeted + global) and MandatoryTemplateID flags the enforced one.
func (c *Client) ListItemTemplates(workspaceID, itemTypeID int) (ItemTemplateListResponse, error) {
	var resp ItemTemplateListResponse
	path := fmt.Sprintf("/rest/api/v1/workspaces/%d/templates", workspaceID)
	if itemTypeID > 0 {
		path += fmt.Sprintf("?item_type_id=%d", itemTypeID)
	}
	if err := c.GET(path, &resp); err != nil {
		return ItemTemplateListResponse{}, err
	}
	return resp, nil
}

// GetItemTemplate returns a single template (with its full description_body).
func (c *Client) GetItemTemplate(workspaceID, templateID int) (*ItemTemplate, error) {
	var tmpl ItemTemplate
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/templates/%d", workspaceID, templateID), &tmpl); err != nil {
		return nil, err
	}
	return &tmpl, nil
}

// ============================================
// Custom Field Methods
// ============================================

// ListCustomFields lists all custom field definitions. Gated by the
// custom-fields:read scope (part of the default agent mint).
func (c *Client) ListCustomFields() ([]CustomField, error) {
	var fields []CustomField
	if err := c.GET("/rest/api/v1/custom-fields", &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// ============================================
// Iteration API Methods
// ============================================

// ListIterations lists iterations across all scopes (global + workspace).
// Requires the iterations:read scope.
func (c *Client) ListIterations(filters map[string]string) (*PaginatedResponse[Iteration], error) {
	path := "/rest/api/v1/iterations"
	if len(filters) > 0 {
		params := url.Values{}
		for k, v := range filters {
			params.Set(k, v)
		}
		path += "?" + params.Encode()
	}

	var resp PaginatedResponse[Iteration]
	if err := c.GET(path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListIterationsInWorkspace lists iterations belonging to a single workspace
// via the items:read-gated workspace route.
func (c *Client) ListIterationsInWorkspace(workspaceID int, filters map[string]string) (*PaginatedResponse[Iteration], error) {
	path := fmt.Sprintf("/rest/api/v1/workspaces/%d/iterations", workspaceID)
	if len(filters) > 0 {
		params := url.Values{}
		for k, v := range filters {
			params.Set(k, v)
		}
		path += "?" + params.Encode()
	}

	var resp PaginatedResponse[Iteration]
	if err := c.GET(path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ResolveIterationID resolves an iteration name or ID to an ID. Mirrors
// ResolveMilestoneID: numeric input passes through; otherwise the lookup is
// fuzzy (exact case-insensitive first, then first substring match) against
// the workspace-scoped list when workspaceID is non-nil, else the global list.
func (c *Client) ResolveIterationID(nameOrID string, workspaceID *int) (int, error) {
	// Use Atoi so malformed inputs like "123abc" do not resolve as ID 123.
	if id, err := strconv.Atoi(nameOrID); err == nil {
		return id, nil
	}

	var resp *PaginatedResponse[Iteration]
	var err error
	if workspaceID != nil {
		resp, err = c.ListIterationsInWorkspace(*workspaceID, nil)
	} else {
		resp, err = c.ListIterations(nil)
	}
	if err != nil {
		return 0, err
	}

	nameLower := strings.ToLower(nameOrID)
	var bestMatch *Iteration

	for i := range resp.Data {
		it := &resp.Data[i]
		itNameLower := strings.ToLower(it.Name)

		// Exact match (case-insensitive)
		if itNameLower == nameLower {
			return it.ID, nil
		}
		// Partial match - prefer first match
		if bestMatch == nil && strings.Contains(itNameLower, nameLower) {
			bestMatch = it
		}
	}

	if bestMatch != nil {
		return bestMatch.ID, nil
	}

	return 0, fmt.Errorf("iteration not found: %s", nameOrID)
}

// ============================================
// Helper Methods
// ============================================

// ResolveWorkspaceID resolves a workspace key to an ID
func (c *Client) ResolveWorkspaceID(keyOrID string) (int, error) {
	// Try parsing as integer first. Use Atoi so malformed inputs like
	// "123abc" do not accidentally resolve as ID 123.
	if id, err := strconv.Atoi(keyOrID); err == nil {
		return id, nil
	}

	// Look up by key from workspace list
	workspaces, err := c.ListWorkspaces()
	if err != nil {
		return 0, fmt.Errorf("failed to list workspaces: %w", err)
	}

	for _, ws := range workspaces.Data {
		if strings.EqualFold(ws.Key, keyOrID) {
			return ws.ID, nil
		}
	}

	return 0, fmt.Errorf("workspace not found: %s", keyOrID)
}

// ResolveItemID resolves an item key (e.g., PROJ-123) or ID to an item ID
func (c *Client) ResolveItemID(keyOrID string) (int, error) {
	// Try parsing as integer first. Use Atoi so malformed inputs like
	// "123abc" do not accidentally resolve as ID 123.
	if id, err := strconv.Atoi(keyOrID); err == nil {
		return id, nil
	}

	// Parse as workspace key + item number (e.g., PROJ-123). Split on
	// the last dash so workspace keys that themselves contain dashes (notably
	// personal workspace keys) still resolve correctly.
	dash := strings.LastIndex(keyOrID, "-")
	if dash <= 0 || dash == len(keyOrID)-1 {
		return 0, fmt.Errorf("invalid item identifier: %s (expected ID or KEY-NUMBER format)", keyOrID)
	}

	wsKey := keyOrID[:dash]
	itemNum, err := strconv.Atoi(keyOrID[dash+1:])
	if err != nil {
		return 0, fmt.Errorf("invalid item number in: %s", keyOrID)
	}

	item, err := c.GetItemByKeyAndNumber(wsKey, itemNum)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return 0, fmt.Errorf("item not found: %s", keyOrID)
		}
		return 0, err
	}
	return item.ID, nil
}

// ============================================
// Pages API Methods
// ============================================

// AgentSkill mirrors the v1 agent-skills payloads (WI-258).
type AgentSkill struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body,omitempty"`
	Enabled     bool   `json:"enabled"`
}

type agentSkillListResponse struct {
	Items []AgentSkill `json:"items"`
}

// ListAgentSkills lists the workspace's enabled agent skills (no bodies).
func (c *Client) ListAgentSkills(workspaceID int) ([]AgentSkill, error) {
	var resp agentSkillListResponse
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/agent-skills", workspaceID), &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// GetAgentSkill fetches one skill including its markdown body.
func (c *Client) GetAgentSkill(workspaceID, skillID int) (*AgentSkill, error) {
	var skill AgentSkill
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/agent-skills/%d", workspaceID, skillID), &skill); err != nil {
		return nil, err
	}
	return &skill, nil
}

// ListPages returns every page in the workspace the caller can view,
// sorted depth-first by the server.
func (c *Client) ListPages(workspaceID int) ([]Page, error) {
	var resp PageListResponse
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages", workspaceID), &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// SearchPages performs a title-and-content search over pages the caller can view in a
// workspace via GET /rest/api/v1/workspaces/{id}/pages/search. limit <= 0
// falls back to the server default. Results omit the page body.
func (c *Client) SearchPages(workspaceID int, query string, limit int) ([]Page, error) {
	params := url.Values{}
	params.Set("q", query)
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	var resp PageListResponse
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/search?%s", workspaceID, params.Encode()), &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// GetPage fetches a single page by id.
func (c *Client) GetPage(workspaceID, pageID int) (*Page, error) {
	var page Page
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d", workspaceID, pageID), &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// CreatePage creates a new page under the given workspace.
func (c *Client) CreatePage(workspaceID int, req PageCreateRequest) (*Page, error) {
	var page Page
	if err := c.POST(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages", workspaceID), req, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// UpdatePage applies a partial update to a page. Pass nil for fields
// that should remain unchanged.
func (c *Client) UpdatePage(workspaceID, pageID int, req PageUpdateRequest) (*Page, error) {
	var page Page
	if err := c.PUT(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d", workspaceID, pageID), req, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// ListPageDiagrams returns every diagram fence currently embedded in a Page.
func (c *Client) ListPageDiagrams(workspaceID, pageID int) ([]PageDiagram, error) {
	var envelope struct {
		Items []PageDiagram `json:"items"`
	}
	path := fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/diagrams", workspaceID, pageID)
	if err := c.GET(path, &envelope); err != nil {
		return nil, err
	}
	return envelope.Items, nil
}

// GetPageDiagram fetches an embedded Page diagram by attachment ID.
func (c *Client) GetPageDiagram(workspaceID, pageID, attachmentID int) (*PageDiagram, error) {
	var diagram PageDiagram
	path := fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/diagrams/%d", workspaceID, pageID, attachmentID)
	if err := c.GET(path, &diagram); err != nil {
		return nil, err
	}
	return &diagram, nil
}

// CreatePageDiagram uploads an immutable diagram attachment and inserts its
// Markdown fence into the Page.
func (c *Client) CreatePageDiagram(workspaceID, pageID int, req PageDiagramCreateRequest) (*PageDiagram, error) {
	var diagram PageDiagram
	path := fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/diagrams", workspaceID, pageID)
	if err := c.POST(path, req, &diagram); err != nil {
		return nil, err
	}
	return &diagram, nil
}

// UpdatePageDiagram creates a replacement attachment and atomically replaces
// the matching fence in the Page.
func (c *Client) UpdatePageDiagram(workspaceID, pageID, attachmentID int, req PageDiagramUpdateRequest) (*PageDiagram, error) {
	var diagram PageDiagram
	path := fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/diagrams/%d", workspaceID, pageID, attachmentID)
	if err := c.PUT(path, req, &diagram); err != nil {
		return nil, err
	}
	return &diagram, nil
}

// MovePage reparents a page. Pass parentID=nil to move to the workspace
// root. prevSiblingID / nextSiblingID place the page at a specific position
// among its siblings; pass nil for both to let the server pick.
func (c *Client) MovePage(workspaceID, pageID int, parentID, prevSiblingID, nextSiblingID *int) (*Page, error) {
	var page Page
	req := PageMoveRequest{
		ParentID:      parentID,
		PrevSiblingID: prevSiblingID,
		NextSiblingID: nextSiblingID,
	}
	if err := c.POST(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/move", workspaceID, pageID), req, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// ArchivePage soft-deletes a page and its entire subtree.
func (c *Client) ArchivePage(workspaceID, pageID int) error {
	return c.DELETE(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d", workspaceID, pageID))
}

// GetPageHistory returns revisions for a page newest-first. Optional
// pagination arguments are limit, offset (kept variadic for compatibility with
// existing call sites/tests that used the original two-argument form).
func (c *Client) GetPageHistory(workspaceID, pageID int, pagination ...int) ([]PageRevision, error) {
	var resp PageHistoryResponse
	endpoint := fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/history", workspaceID, pageID)
	limit, offset := 0, 0
	if len(pagination) > 0 {
		limit = pagination[0]
	}
	if len(pagination) > 1 {
		offset = pagination[1]
	}
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	if offset > 0 {
		q.Set("offset", fmt.Sprintf("%d", offset))
	}
	if encoded := q.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	if err := c.GET(endpoint, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

func (c *Client) GetPageRevision(workspaceID, pageID, revisionID int) (*PageRevision, error) {
	var rev PageRevision
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/history/%d", workspaceID, pageID, revisionID), &rev); err != nil {
		return nil, err
	}
	return &rev, nil
}

func (c *Client) RestorePageRevision(workspaceID, pageID, revisionID int) (*Page, error) {
	var page Page
	if err := c.POST(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/history/%d/restore", workspaceID, pageID, revisionID), map[string]any{}, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

func (c *Client) GetPagePermissions(workspaceID, pageID int) (*PagePermissions, error) {
	var perms PagePermissions
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/permissions", workspaceID, pageID), &perms); err != nil {
		return nil, err
	}
	return &perms, nil
}

func (c *Client) GrantPagePermission(workspaceID, pageID int, req PageGrantPermissionRequest) (*PagePermission, error) {
	var perm PagePermission
	if err := c.POST(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/permissions", workspaceID, pageID), req, &perm); err != nil {
		return nil, err
	}
	return &perm, nil
}

func (c *Client) RevokePagePermission(workspaceID, pageID, permissionID int) error {
	return c.DELETE(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/permissions/%d", workspaceID, pageID, permissionID))
}

func (c *Client) SetPageInheritance(workspaceID, pageID int, inherit bool) (*Page, error) {
	var page Page
	if err := c.PATCH(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/inheritance", workspaceID, pageID), PageSetInheritanceRequest{InheritPermissions: inherit}, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// ============================================
// Page Labels API Methods
// ============================================
//
// Workspace-scoped labels that attach to pages only. Fully separate from
// the work-item label system; never share rows or endpoints.

// ListPageLabels returns every page label in the workspace.
func (c *Client) ListPageLabels(workspaceID int) ([]PageLabel, error) {
	var resp PageLabelListResponse
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/page-labels", workspaceID), &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// GetPageLabel fetches a single page label by id.
func (c *Client) GetPageLabel(workspaceID, labelID int) (*PageLabel, error) {
	var label PageLabel
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/page-labels/%d", workspaceID, labelID), &label); err != nil {
		return nil, err
	}
	return &label, nil
}

// CreatePageLabel inserts a new page label in the workspace.
func (c *Client) CreatePageLabel(workspaceID int, req PageLabelCreateRequest) (*PageLabel, error) {
	var label PageLabel
	if err := c.POST(fmt.Sprintf("/rest/api/v1/workspaces/%d/page-labels", workspaceID), req, &label); err != nil {
		return nil, err
	}
	return &label, nil
}

// UpdatePageLabel applies a partial update to a label.
func (c *Client) UpdatePageLabel(workspaceID, labelID int, req PageLabelUpdateRequest) (*PageLabel, error) {
	var label PageLabel
	if err := c.PUT(fmt.Sprintf("/rest/api/v1/workspaces/%d/page-labels/%d", workspaceID, labelID), req, &label); err != nil {
		return nil, err
	}
	return &label, nil
}

// DeletePageLabel removes a label and cascades the page assignments.
func (c *Client) DeletePageLabel(workspaceID, labelID int) error {
	return c.DELETE(fmt.Sprintf("/rest/api/v1/workspaces/%d/page-labels/%d", workspaceID, labelID))
}

// ListPageLabelsForPage returns the labels attached to a single page.
func (c *Client) ListPageLabelsForPage(workspaceID, pageID int) ([]PageLabel, error) {
	var resp PageLabelListResponse
	if err := c.GET(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/labels", workspaceID, pageID), &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// SetPageLabelsForPage atomically replaces the label set on a page.
func (c *Client) SetPageLabelsForPage(workspaceID, pageID int, labelIDs []int) ([]PageLabel, error) {
	var resp PageLabelListResponse
	if err := c.PUT(
		fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/labels", workspaceID, pageID),
		PageLabelSetRequest{LabelIDs: labelIDs},
		&resp,
	); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// AddPageLabelToPage attaches a single label to a page.
func (c *Client) AddPageLabelToPage(workspaceID, pageID, labelID int) ([]PageLabel, error) {
	var resp PageLabelListResponse
	if err := c.POST(
		fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/labels", workspaceID, pageID),
		PageLabelAddRequest{LabelID: labelID},
		&resp,
	); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// RemovePageLabelFromPage detaches a single label from a page.
func (c *Client) RemovePageLabelFromPage(workspaceID, pageID, labelID int) error {
	return c.DELETE(fmt.Sprintf("/rest/api/v1/workspaces/%d/pages/%d/labels/%d", workspaceID, pageID, labelID))
}

// ============================================
// Links API Methods (item ↔ item / item ↔ page / item ↔ test_case)
// ============================================

// ListLinkTypes returns every active link type plus the system catalog.
// AllowedEntityTypes on each entry constrains which source/target pairs
// are valid (nil means any).
func (c *Client) ListLinkTypes() ([]LinkType, error) {
	var types []LinkType
	if err := c.GET("/rest/api/v1/link-types", &types); err != nil {
		return nil, err
	}
	return types, nil
}

// CreateLink creates a cross-entity link. The server enforces the
// link-type / entity-type compatibility check; the CLI front-loads an
// obvious-mismatch check for a friendlier error.
func (c *Client) CreateLink(req LinkCreateRequest) (*ItemLink, error) {
	var link ItemLink
	if err := c.POST("/rest/api/v1/links", req, &link); err != nil {
		return nil, err
	}
	return &link, nil
}

// ListLinksForEntity returns outgoing and incoming links for a single
// entity. The route prefix depends on entityType — items, pages, and
// test cases each get their own list endpoint that funnels into the
// same handler.
func (c *Client) ListLinksForEntity(entityType string, id int) (*LinkListResponse, error) {
	var route string
	switch entityType {
	case "item":
		route = fmt.Sprintf("/rest/api/v1/items/%d/links", id)
	case "page":
		route = fmt.Sprintf("/rest/api/v1/pages/%d/links", id)
	case "test_case":
		route = fmt.Sprintf("/rest/api/v1/test-cases/%d/links", id)
	default:
		return nil, fmt.Errorf("unsupported entity type %q (want item, page, or test_case)", entityType)
	}
	var resp LinkListResponse
	if err := c.GET(route, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DeleteLink removes a link by its numeric id. The server enforces edit
// permission on the source entity.
func (c *Client) DeleteLink(id int) error {
	return c.DELETE(fmt.Sprintf("/rest/api/v1/links/%d", id))
}

// ----------------------------------------------------------------------
// Assets — v1 surface
// ----------------------------------------------------------------------

// ListAssets returns a page of assets in setID, filtered by ?type_id /
// ?category_id / ?status_id / ?q. Pagination flows through the standard
// PaginatedResponse envelope.
func (c *Client) ListAssets(setID int, filters map[string]string) (*PaginatedResponse[Asset], error) {
	path := fmt.Sprintf("/rest/api/v1/asset-sets/%d/assets", setID)
	if len(filters) > 0 {
		params := url.Values{}
		for k, v := range filters {
			if v != "" {
				params.Set(k, v)
			}
		}
		if encoded := params.Encode(); encoded != "" {
			path += "?" + encoded
		}
	}
	var resp PaginatedResponse[Asset]
	if err := c.GET(path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetAsset fetches a single asset by id.
func (c *Client) GetAsset(id int) (*Asset, error) {
	var a Asset
	if err := c.GET(fmt.Sprintf("/rest/api/v1/assets/%d", id), &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// CreateAsset creates a new asset in setID.
func (c *Client) CreateAsset(setID int, req AssetCreateRequest) (*Asset, error) {
	var a Asset
	if err := c.POST(fmt.Sprintf("/rest/api/v1/asset-sets/%d/assets", setID), req, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// UpdateAsset partial-updates an asset. Only non-nil pointer fields in
// req are written; everything else is preserved.
func (c *Client) UpdateAsset(id int, req AssetUpdateRequest) (*Asset, error) {
	var a Asset
	if err := c.PUT(fmt.Sprintf("/rest/api/v1/assets/%d", id), req, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// DeleteAsset removes an asset and any item↔asset links pointing at it.
// Requires assets:delete scope on the token.
func (c *Client) DeleteAsset(id int) error {
	return c.DELETE(fmt.Sprintf("/rest/api/v1/assets/%d", id))
}

// ListAssetSets lists asset sets visible to the caller.
func (c *Client) ListAssetSets() ([]AssetSet, error) {
	var sets []AssetSet
	if err := c.GET("/rest/api/v1/asset-sets", &sets); err != nil {
		return nil, err
	}
	return sets, nil
}

// GetAssetSet fetches an asset set by id.
func (c *Client) GetAssetSet(id int) (*AssetSet, error) {
	var s AssetSet
	if err := c.GET(fmt.Sprintf("/rest/api/v1/asset-sets/%d", id), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ListAssetTypes lists the asset types defined on setID.
func (c *Client) ListAssetTypes(setID int) ([]AssetType, error) {
	var types []AssetType
	if err := c.GET(fmt.Sprintf("/rest/api/v1/asset-sets/%d/types", setID), &types); err != nil {
		return nil, err
	}
	return types, nil
}

// GetAssetType fetches an asset type by id (including its field definitions).
func (c *Client) GetAssetType(id int) (*AssetType, error) {
	var t AssetType
	if err := c.GET(fmt.Sprintf("/rest/api/v1/asset-types/%d", id), &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// ListAssetCategories lists categories defined on setID.
func (c *Client) ListAssetCategories(setID int) ([]AssetCategory, error) {
	var cats []AssetCategory
	if err := c.GET(fmt.Sprintf("/rest/api/v1/asset-sets/%d/categories", setID), &cats); err != nil {
		return nil, err
	}
	return cats, nil
}

// ListAssetStatuses lists statuses defined on setID.
func (c *Client) ListAssetStatuses(setID int) ([]AssetStatus, error) {
	var statuses []AssetStatus
	if err := c.GET(fmt.Sprintf("/rest/api/v1/asset-sets/%d/statuses", setID), &statuses); err != nil {
		return nil, err
	}
	return statuses, nil
}

// ImportAssetsCSV uploads a CSV to /asset-sets/{setID}/assets/import. assetTypeID
// is required; statusID and categoryID are optional defaults for every row.
// Returns a synthetic AssetImportJob summarizing the run (the v1 endpoint is
// synchronous one-shot, not the cookie-auth async flow).
func (c *Client) ImportAssetsCSV(setID, assetTypeID int, statusID, categoryID *int, filename string, body io.Reader) (*AssetImportJob, error) {
	var buf bytes.Buffer
	mp := multipart.NewWriter(&buf)
	if err := mp.WriteField("asset_type_id", fmt.Sprintf("%d", assetTypeID)); err != nil {
		return nil, fmt.Errorf("multipart write asset_type_id: %w", err)
	}
	if statusID != nil {
		if err := mp.WriteField("status_id", fmt.Sprintf("%d", *statusID)); err != nil {
			return nil, fmt.Errorf("multipart write status_id: %w", err)
		}
	}
	if categoryID != nil {
		if err := mp.WriteField("category_id", fmt.Sprintf("%d", *categoryID)); err != nil {
			return nil, fmt.Errorf("multipart write category_id: %w", err)
		}
	}
	part, err := mp.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("multipart create file: %w", err)
	}
	if _, err := io.Copy(part, body); err != nil {
		return nil, fmt.Errorf("multipart copy file: %w", err)
	}
	if err := mp.Close(); err != nil {
		return nil, fmt.Errorf("multipart close: %w", err)
	}

	endpoint := fmt.Sprintf("%s/rest/api/v1/asset-sets/%d/assets/import", c.baseURL, setID)
	req, err := http.NewRequest(http.MethodPost, endpoint, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", mp.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("import failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	var job AssetImportJob
	if err := json.Unmarshal(respBody, &job); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &job, nil
}

// ============================================
// Time tracking
// ============================================

// ListTimeProjects returns time projects accessible to the authenticated user.
func (c *Client) ListTimeProjects() ([]TimeProject, error) {
	var projects []TimeProject
	if err := c.GET("/rest/api/v1/time/projects", &projects); err != nil {
		return nil, err
	}
	return projects, nil
}

// ListTimeWorklogs returns worklogs for the authenticated user with optional filters.
func (c *Client) ListTimeWorklogs(filters map[string]string) (*PaginatedResponse[TimeWorklog], error) {
	path := "/rest/api/v1/time/worklogs"
	if len(filters) > 0 {
		params := make([]string, 0, len(filters))
		for k, v := range filters {
			params = append(params, k+"="+v)
		}
		path += "?" + strings.Join(params, "&")
	}
	var resp PaginatedResponse[TimeWorklog]
	if err := c.GET(path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateTimeWorklog logs a new time entry.
func (c *Client) CreateTimeWorklog(req TimeWorklogCreateRequest) (map[string]any, error) {
	var out map[string]any
	if err := c.POST("/rest/api/v1/time/worklogs", req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateTimeWorklog updates the description of an existing worklog.
func (c *Client) UpdateTimeWorklog(id int, description string) error {
	body := map[string]string{"description": description}
	var out map[string]any
	return c.PUT(fmt.Sprintf("/rest/api/v1/time/worklogs/%d", id), body, &out)
}

// DeleteTimeWorklog deletes a worklog.
func (c *Client) DeleteTimeWorklog(id int) error {
	return c.DELETE(fmt.Sprintf("/rest/api/v1/time/worklogs/%d", id))
}

// StartTimer starts a new active timer.
func (c *Client) StartTimer(req TimerStartRequest) (map[string]any, error) {
	var out map[string]any
	if err := c.POST("/rest/api/v1/timer/start", req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetActiveTimer returns the user's currently running timer.
func (c *Client) GetActiveTimer() (map[string]any, error) {
	var out map[string]any
	if err := c.GET("/rest/api/v1/timer/active", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// StopTimer stops the user's active timer and creates a worklog.
func (c *Client) StopTimer() (map[string]any, error) {
	var out map[string]any
	// DELETE on /timer/stop returns a JSON body; use a custom request so we
	// can pass a result target (the convenience Delete method discards the body).
	if err := c.doRequest("DELETE", "/rest/api/v1/timer/stop", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
