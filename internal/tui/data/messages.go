package data

// Message types emitted by the tea.Cmd constructors in commands.go and
// consumed by whichever screen (or the root model) cares about them.

type WorkspacesLoadedMsg struct {
	Workspaces []Workspace
}

type WorkItemsLoadedMsg struct {
	Items []WorkItem
}

type CommentsLoadedMsg struct {
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

// ErrorMsg carries a human-readable (already sanitized) error string from a
// failed loader/mutator.
type ErrorMsg struct {
	Err string
}
