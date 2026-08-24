package todoist

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"windshift/internal/utils"

	"uuid"
)

// syncBaseURL is the base of the Todoist Sync API (v1). The data client batches
// reads (incremental sync) and writes (commands) through the single /sync
// endpoint, which is what makes 1:1 personal-task mirroring tractable.
const syncBaseURL = "https://api.todoist.com/api/v1"

// Priority constants. Todoist priority is inverted relative to its UI: the API
// uses 1 (natural / "p4") through 4 (urgent / "p1").
const (
	PriorityNormal = 1
	PriorityUrgent = 4
)

// Client is an authenticated Todoist Sync API client. Construct it with the
// per-user access token decrypted from user_integration_tokens.
type Client struct {
	accessToken string
	httpClient  *http.Client
	baseURL     string
}

// NewClient returns a Sync API client bound to a single user's access token.
func NewClient(accessToken string) *Client {
	return &Client{
		accessToken: accessToken,
		httpClient:  utils.NewHTTPClient(requestTimeout),
		baseURL:     syncBaseURL,
	}
}

// Due mirrors Todoist's due-date object. Date is either "YYYY-MM-DD" (all-day)
// or an RFC3339 timestamp when a time component is present.
type Due struct {
	Date        string `json:"date"`
	String      string `json:"string,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
	IsRecurring bool   `json:"is_recurring,omitempty"`
}

// Item is a Todoist task as returned by the Sync API. Note: the Sync API does
// not expose a generic modification timestamp — only AddedAt and (for completed
// tasks) CompletedAt — which is why incremental sync membership, not an mtime
// comparison, is the primary signal that a task changed on Todoist's side.
type Item struct {
	ID          string  `json:"id"`
	ProjectID   string  `json:"project_id"`
	Content     string  `json:"content"`
	Description string  `json:"description"`
	Priority    int     `json:"priority"`
	Due         *Due    `json:"due"`
	ParentID    *string `json:"parent_id"`
	ChildOrder  int     `json:"child_order"`
	Checked     bool    `json:"checked"`
	IsDeleted   bool    `json:"is_deleted"`
	CompletedAt *string `json:"completed_at"`
	AddedAt     string  `json:"added_at"`
}

// Project is a Todoist project. Only the fields the sync engine needs are
// modeled; the rest of the payload is ignored.
type Project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDeleted bool   `json:"is_deleted"`
	IsInbox   bool   `json:"inbox_project"`
}

// Command is a single write operation submitted to the Sync API. Mutating
// commands that create an entity carry a TempID so the server can return the
// real id in SyncResponse.TempIDMapping.
type Command struct {
	Type   string `json:"type"`
	UUID   string `json:"uuid"`
	TempID string `json:"temp_id,omitempty"`
	Args   any    `json:"args"`
}

// SyncResponse is the unified response shape for both reads and writes.
type SyncResponse struct {
	SyncToken     string                     `json:"sync_token"`
	FullSync      bool                       `json:"full_sync"`
	Items         []Item                     `json:"items"`
	Projects      []Project                  `json:"projects"`
	TempIDMapping map[string]string          `json:"temp_id_mapping"`
	SyncStatus    map[string]json.RawMessage `json:"sync_status"`
}

// Sync performs an incremental read. Pass the stored sync token to receive only
// resources changed since the last call; pass "" (or "*") for a full sync.
func (c *Client) Sync(syncToken string, resourceTypes []string) (*SyncResponse, error) {
	if syncToken == "" {
		syncToken = "*"
	}
	rt, err := json.Marshal(resourceTypes)
	if err != nil {
		return nil, fmt.Errorf("encoding resource_types: %w", err)
	}
	return c.post(url.Values{
		"sync_token":     {syncToken},
		"resource_types": {string(rt)},
	})
}

// ExecuteCommands submits a batch of write commands. The returned response
// carries TempIDMapping (temp id -> real id) for any create commands and
// SyncStatus (command uuid -> "ok" or an error object) for per-command results.
func (c *Client) ExecuteCommands(commands []Command) (*SyncResponse, error) {
	cmds, err := json.Marshal(commands)
	if err != nil {
		return nil, fmt.Errorf("encoding commands: %w", err)
	}
	return c.post(url.Values{"commands": {string(cmds)}})
}

// ListProjects returns the user's non-deleted projects via a full project sync.
func (c *Client) ListProjects() ([]Project, error) {
	resp, err := c.Sync("*", []string{"projects"})
	if err != nil {
		return nil, err
	}
	projects := make([]Project, 0, len(resp.Projects))
	for _, p := range resp.Projects {
		if !p.IsDeleted {
			projects = append(projects, p)
		}
	}
	return projects, nil
}

func (c *Client) post(form url.Values) (*SyncResponse, error) {
	req, err := http.NewRequest("POST", c.baseURL+"/sync", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling todoist sync: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("todoist sync error (status %d): %s", resp.StatusCode, string(body))
	}

	var out SyncResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &out, nil
}

// CommandError reports the per-command failure for the given command uuid, or
// nil if the command succeeded (or is absent from the status map). The Sync API
// reports "ok" for success and an object like {"error_code":..,"error":".."}
// otherwise, so a partial failure in a batch does not surface as an HTTP error.
func (r *SyncResponse) CommandError(commandUUID string) error {
	raw, ok := r.SyncStatus[commandUUID]
	if !ok {
		return nil
	}
	var ok2 string
	if err := json.Unmarshal(raw, &ok2); err == nil {
		if ok2 == "ok" {
			return nil
		}
		return fmt.Errorf("todoist command %s failed: %s", commandUUID, ok2)
	}
	var errObj struct {
		ErrorCode int    `json:"error_code"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(raw, &errObj); err == nil && errObj.Error != "" {
		return fmt.Errorf("todoist command %s failed (code %d): %s", commandUUID, errObj.ErrorCode, errObj.Error)
	}
	return fmt.Errorf("todoist command %s failed: %s", commandUUID, string(raw))
}

// newCommandUUID returns a fresh command uuid. Isolated so tests can stub it if
// deterministic command identifiers are ever needed.
func newCommandUUID() string { return uuid.New().String() }

// AddItemArgs are the args for an "item_add" command.
type AddItemArgs struct {
	Content     string `json:"content"`
	Description string `json:"description,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	Priority    int    `json:"priority,omitempty"`
	DueDate     string `json:"-"`
	ParentID    string `json:"parent_id,omitempty"`
	Due         *Due   `json:"due,omitempty"`
}

// UpdateItemArgs are the args for an "item_update" command. Pointer fields are
// omitted when nil so an update touches only the fields that changed.
type UpdateItemArgs struct {
	ID          string  `json:"id"`
	Content     *string `json:"content,omitempty"`
	Description *string `json:"description,omitempty"`
	Priority    *int    `json:"priority,omitempty"`
	Due         *Due    `json:"due,omitempty"`
}

// NewAddItemCommand builds an item_add command and returns it alongside the
// temp id the caller should map to the resulting Windshift item.
func NewAddItemCommand(args AddItemArgs) (cmd Command, tempID string) {
	tempID = uuid.New().String()
	if args.DueDate != "" && args.Due == nil {
		args.Due = &Due{Date: args.DueDate}
	}
	return Command{Type: "item_add", UUID: newCommandUUID(), TempID: tempID, Args: args}, tempID
}

// NewUpdateItemCommand builds an item_update command.
func NewUpdateItemCommand(args UpdateItemArgs) Command {
	return Command{Type: "item_update", UUID: newCommandUUID(), Args: args}
}

// NewCompleteItemCommand marks a task complete.
func NewCompleteItemCommand(id string) Command {
	return Command{Type: "item_complete", UUID: newCommandUUID(), Args: map[string]string{"id": id}}
}

// NewUncompleteItemCommand reopens a completed task.
func NewUncompleteItemCommand(id string) Command {
	return Command{Type: "item_uncomplete", UUID: newCommandUUID(), Args: map[string]string{"id": id}}
}

// NewDeleteItemCommand deletes a task.
func NewDeleteItemCommand(id string) Command {
	return Command{Type: "item_delete", UUID: newCommandUUID(), Args: map[string]string{"id": id}}
}
