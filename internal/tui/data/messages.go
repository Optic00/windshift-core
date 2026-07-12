package data

// Message types emitted by the tea.Cmd constructors in commands.go and
// consumed by whichever screen (or the root model) cares about them.

type WorkspacesLoadedMsg struct {
	Workspaces []Workspace
}

type WorkItemsLoadedMsg struct {
	Items []WorkItem
	// Truncated is set when the workspace has more items than the client-side
	// page-accumulation cap; the board surfaces this instead of silently
	// showing a partial list.
	Truncated bool
}

type CommentsLoadedMsg struct {
	// ItemID keys the result so late responses land in the right cache slot
	// even if the selection moved on.
	ItemID   int
	Comments []Comment
}

type WorkItemUpdatedMsg struct{}

type WorkItemCreatedMsg struct{}

type CommentCreatedMsg struct{}

type TimeLogCreatedMsg struct{}

type TimeProjectsLoadedMsg struct {
	Projects []TimeProject
}

type StatusesLoadedMsg struct {
	Statuses []Status
}

type PrioritiesLoadedMsg struct {
	Priorities []Priority
}

type UsersLoadedMsg struct {
	Users []User
}

// WorkItemLoadedMsg is a single-item refresh (after a quick-set mutation).
type WorkItemLoadedMsg struct {
	Item WorkItem
}

// AgentRunsLoadedMsg delivers an item's coding-agent run history, keyed by
// ItemID like CommentsLoadedMsg.
type AgentRunsLoadedMsg struct {
	ItemID int
	Runs   []AgentRun
}

// PrefsLoadedMsg delivers the persisted TUI preferences. OK is false when
// the load failed — startup proceeds with defaults, never blocks.
type PrefsLoadedMsg struct {
	Prefs Prefs
	OK    bool
}

// ErrorMsg carries a human-readable (already sanitized) error string from a
// failed loader/mutator.
type ErrorMsg struct {
	Err string
}
