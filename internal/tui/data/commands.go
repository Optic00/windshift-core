package data

import (
	tea "charm.land/bubbletea/v2"
)

// tea.Cmd constructors. Each wraps one Client call and emits either its
// typed *Msg or an ErrorMsg. They are package functions (not methods on a
// model) so any screen can fire them with just a *Client.

func LoadWorkspaces(c *Client) tea.Cmd {
	return func() tea.Msg {
		workspaces, err := c.getWorkspaces()
		if err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return WorkspacesLoadedMsg{Workspaces: workspaces}
	}
}

func LoadWorkItems(c *Client, workspaceID int) tea.Cmd {
	return func() tea.Msg {
		items, truncated, err := c.getWorkItems(workspaceID)
		if err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return WorkItemsLoadedMsg{Items: items, Truncated: truncated}
	}
}

func LoadComments(c *Client, itemID int) tea.Cmd {
	return func() tea.Msg {
		comments, err := c.getComments(itemID)
		if err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return CommentsLoadedMsg{ItemID: itemID, Comments: comments}
	}
}

func LoadStatuses(c *Client, workspaceID int) tea.Cmd {
	return func() tea.Msg {
		statuses, err := c.getStatuses(workspaceID)
		if err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return StatusesLoadedMsg{Statuses: statuses}
	}
}

func LoadPriorities(c *Client) tea.Cmd {
	return func() tea.Msg {
		priorities, err := c.getPriorities()
		if err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return PrioritiesLoadedMsg{Priorities: priorities}
	}
}

func LoadTimeProjects(c *Client) tea.Cmd {
	return func() tea.Msg {
		projects, err := c.getTimeProjects()
		if err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return TimeProjectsLoadedMsg{Projects: projects}
	}
}

func UpdateWorkItem(c *Client, itemID int, title, description string, statusID, priorityID *int) tea.Cmd {
	return func() tea.Msg {
		if err := c.updateWorkItem(itemID, title, description, statusID, priorityID); err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return WorkItemUpdatedMsg{}
	}
}

func CreateWorkItem(c *Client, workspaceID int, title, description string, priorityID *int) tea.Cmd {
	return func() tea.Msg {
		if err := c.createWorkItem(workspaceID, title, description, priorityID); err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return WorkItemCreatedMsg{}
	}
}

func CreateComment(c *Client, itemID int, content string) tea.Cmd {
	return func() tea.Msg {
		if err := c.createComment(itemID, content); err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return CommentCreatedMsg{}
	}
}

func CreateTimeLog(c *Client, itemID, projectID int, description, duration, date, startTime string) tea.Cmd {
	return func() tea.Msg {
		if err := c.createTimeLog(itemID, projectID, description, duration, date, startTime); err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return TimeLogCreatedMsg{}
	}
}
