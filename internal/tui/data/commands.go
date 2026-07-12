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

func LoadAssignableUsers(c *Client, workspaceID int) tea.Cmd {
	return func() tea.Msg {
		users, err := c.getAssignableUsers(workspaceID)
		if err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return UsersLoadedMsg{Users: users}
	}
}

// ReloadWorkItem refreshes one item after a mutation.
func ReloadWorkItem(c *Client, itemID int) tea.Cmd {
	return func() tea.Msg {
		item, err := c.getWorkItem(itemID)
		if err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return WorkItemLoadedMsg{Item: item}
	}
}

// SetItemStatus transitions an item, then refreshes it.
func SetItemStatus(c *Client, itemID, statusID int) tea.Cmd {
	return func() tea.Msg {
		if err := c.setItemStatus(itemID, statusID); err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return ReloadWorkItem(c, itemID)()
	}
}

// SetItemPriority sets priority_id only, then refreshes the item.
func SetItemPriority(c *Client, itemID, priorityID int) tea.Cmd {
	return func() tea.Msg {
		if err := c.setItemField(itemID, "priority_id", priorityID); err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return ReloadWorkItem(c, itemID)()
	}
}

// SetItemAssignee sets assignee_id only (0 unassigns), then refreshes.
func SetItemAssignee(c *Client, itemID, assigneeID int) tea.Cmd {
	return func() tea.Msg {
		var v interface{}
		if assigneeID > 0 {
			v = assigneeID
		} else {
			v = nil
		}
		if err := c.setItemField(itemID, "assignee_id", v); err != nil {
			return ErrorMsg{Err: err.Error()}
		}
		return ReloadWorkItem(c, itemID)()
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
