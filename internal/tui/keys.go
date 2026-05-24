package tui

import (
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"windshift/internal/tui/dialog"
)

// handleKey is the top-level key router. Order matters: dialogs eat keys
// first, then global bindings, then per-screen dispatch.
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.successMessage = ""

	if len(m.dialogs) > 0 {
		return m.handleDialogKey(msg)
	}

	// Global keys — but only when we're not editing a text field, otherwise
	// they'd swallow typed characters that look like single-letter bindings
	// (e.g. 'q' in a comment).
	if !m.isEditing() {
		if key.Matches(msg, m.keys.Quit) {
			return m, tea.Quit
		}
		if key.Matches(msg, m.keys.Help) {
			return m.toggleHelp(), nil
		}
	}

	switch m.currentScreen {
	case WorkspaceListScreen:
		return m.handleWorkspaceKeys(msg)
	case WorkItemListScreen:
		return m.handleWorkItemKeys(msg)
	case WorkItemDetailScreen:
		return m.handleWorkItemDetailKeys(msg)
	case CreateWorkItemScreen:
		return m.handleCreateWorkItemKeys(msg)
	case CommentsScreen:
		return m.handleCommentsKeys(msg)
	case TimeLoggingScreen:
		return m.handleTimeLoggingKeys(msg)
	case HelpScreen:
		return m.handleHelpKeys(msg)
	}
	return m, nil
}

// toggleHelp flips between the help screen and the previous screen.
func (m Model) toggleHelp() Model {
	if m.currentScreen == HelpScreen {
		if m.currentWorkspace != nil {
			m.currentScreen = WorkItemListScreen
		} else {
			m.currentScreen = WorkspaceListScreen
		}
	} else {
		m.currentScreen = HelpScreen
	}
	return m
}

func (m Model) isEditing() bool {
	switch m.currentScreen {
	case WorkItemDetailScreen:
		return m.editForm.editing
	case CreateWorkItemScreen:
		return m.createForm.editing
	case CommentsScreen:
		return m.commentForm.editing
	case TimeLoggingScreen:
		return m.timeForm.editing
	}
	return false
}

func (m Model) handleDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	top := m.dialogs[len(m.dialogs)-1]
	action := top.HandleKey(msg)
	if action.Close {
		m.applyDialogSelection(top.ID(), action.Selected)
		m.dialogs = m.dialogs[:len(m.dialogs)-1]
	}
	return m, action.Cmd
}

// applyDialogSelection routes a picker result back to the right form field.
func (m *Model) applyDialogSelection(id string, sel any) {
	if sel == nil {
		return
	}
	switch id {
	case pickerStatusID:
		s, ok := sel.(Status)
		if !ok {
			return
		}
		if m.currentScreen == WorkItemDetailScreen {
			m.editForm.statusID = &s.ID
			m.editForm.statusName = s.Name
			m.editForm.statusColor = s.CategoryColor
		}
	case pickerPriorityID:
		p, ok := sel.(Priority)
		if !ok {
			return
		}
		switch m.currentScreen {
		case WorkItemDetailScreen:
			m.editForm.priorityID = &p.ID
			m.editForm.priorityName = p.Name
			m.editForm.priorityColor = p.Color
		case CreateWorkItemScreen:
			m.createForm.priorityID = &p.ID
			m.createForm.priorityName = p.Name
			m.createForm.priorityColor = p.Color
		}
	case pickerProjectID:
		p, ok := sel.(TimeProject)
		if !ok {
			return
		}
		if m.currentScreen == TimeLoggingScreen {
			id := int(p.ID)
			m.timeForm.projectID = &id
			m.timeForm.projectName = p.Name
		}
	}
}

// ---------- per-screen handlers ----------

func (m Model) handleWorkspaceKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		if m.selectedWorkspaceIdx > 0 {
			m.selectedWorkspaceIdx--
		} else if len(m.workspaces) > 0 {
			m.selectedWorkspaceIdx = len(m.workspaces) - 1
		}
	case key.Matches(msg, m.keys.Down):
		if len(m.workspaces) > 0 {
			m.selectedWorkspaceIdx = (m.selectedWorkspaceIdx + 1) % len(m.workspaces)
		}
	case key.Matches(msg, m.keys.Enter):
		if len(m.workspaces) > 0 && m.selectedWorkspaceIdx < len(m.workspaces) {
			m.currentWorkspace = &m.workspaces[m.selectedWorkspaceIdx]
			m.currentScreen = WorkItemListScreen
			m.loading = true
			return m, tea.Batch(
				m.loadWorkItems(m.currentWorkspace.ID),
				m.loadStatuses(m.currentWorkspace.ID),
				m.loadPriorities(),
				m.loadTimeProjects(),
			)
		}
	case key.Matches(msg, m.keys.Refresh):
		m.loading = true
		m.errorMessage = ""
		return m, m.loadWorkspaces()
	}
	return m, nil
}

func (m Model) handleWorkItemKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		if m.selectedItemIdx > 0 {
			m.selectedItemIdx--
		} else if len(m.workItems) > 0 {
			m.selectedItemIdx = len(m.workItems) - 1
		}
	case key.Matches(msg, m.keys.Down):
		if len(m.workItems) > 0 {
			m.selectedItemIdx = (m.selectedItemIdx + 1) % len(m.workItems)
		}
	case key.Matches(msg, m.keys.Enter):
		if len(m.workItems) > 0 && m.selectedItemIdx < len(m.workItems) {
			item := m.workItems[m.selectedItemIdx]
			m.editForm = m.resetEditForm(item)
			m.currentScreen = WorkItemDetailScreen
		}
	case key.Matches(msg, m.keys.LogTime):
		if len(m.workItems) > 0 {
			m.enterTimeLoggingForCurrentWorkspace()
		}
	case key.Matches(msg, m.keys.Comments):
		if len(m.workItems) > 0 && m.selectedItemIdx < len(m.workItems) {
			item := m.workItems[m.selectedItemIdx]
			m.currentScreen = CommentsScreen
			m.commentForm = m.resetCommentForm()
			return m, m.loadComments(item.ID)
		}
	case key.Matches(msg, m.keys.New):
		if m.currentWorkspace != nil {
			m.createForm = m.resetCreateForm()
			m.currentScreen = CreateWorkItemScreen
		}
	case key.Matches(msg, m.keys.Refresh):
		if m.currentWorkspace != nil {
			m.loading = true
			m.errorMessage = ""
			return m, m.loadWorkItems(m.currentWorkspace.ID)
		}
	case key.Matches(msg, m.keys.Back):
		m.currentScreen = WorkspaceListScreen
		m.currentWorkspace = nil
	}
	return m, nil
}

func (m Model) handleWorkItemDetailKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.editForm.editing {
		return m.handleEditFormEditingKeys(msg)
	}
	switch {
	case key.Matches(msg, m.keys.Up):
		if m.editForm.currentField > 0 {
			m.editForm.currentField--
		}
	case key.Matches(msg, m.keys.Down):
		if m.editForm.currentField < 3 {
			m.editForm.currentField++
		}
	case key.Matches(msg, m.keys.Enter):
		switch m.editForm.currentField {
		case 0:
			m.editForm.editing = true
			m.editForm.titleInput.Focus()
			m.editForm.titleInput.CursorEnd()
		case 1:
			m.editForm.editing = true
			return m, m.editForm.descriptionTextarea.Focus()
		case 2:
			m.openStatusPicker()
		case 3:
			m.openPriorityPicker(m.editForm.priorityID)
		}
	case key.Matches(msg, m.keys.Save):
		if len(m.workItems) > 0 && m.selectedItemIdx < len(m.workItems) {
			item := m.workItems[m.selectedItemIdx]
			return m, m.updateWorkItem(item.ID, m.editForm.titleInput.Value(), m.editForm.descriptionTextarea.Value(), m.editForm.statusID, m.editForm.priorityID)
		}
	case key.Matches(msg, m.keys.LogTime):
		m.enterTimeLoggingForCurrentWorkspace()
	case key.Matches(msg, m.keys.Comments):
		if len(m.workItems) > 0 && m.selectedItemIdx < len(m.workItems) {
			item := m.workItems[m.selectedItemIdx]
			m.currentScreen = CommentsScreen
			m.commentForm = m.resetCommentForm()
			return m, m.loadComments(item.ID)
		}
	case key.Matches(msg, m.keys.Back):
		m.currentScreen = WorkItemListScreen
	}
	return m, nil
}

func (m Model) handleEditFormEditingKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Description (textarea, multi-line). Esc stops editing; tab/ctrl+enter
	// moves to next field; everything else (incl. enter) goes to the
	// textarea so it can insert newlines.
	if m.editForm.currentField == 1 {
		switch msg.String() {
		case "esc":
			m.editForm.descriptionTextarea.Blur()
			m.editForm.editing = false
			return m, nil
		case "tab", "ctrl+enter":
			m.editForm.descriptionTextarea.Blur()
			m.editForm.editing = false
			if m.editForm.currentField < 3 {
				m.editForm.currentField++
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.editForm.descriptionTextarea, cmd = m.editForm.descriptionTextarea.Update(msg)
		return m, cmd
	}

	// Title (single-line textinput). Esc / enter / tab leave editing.
	if m.editForm.currentField == 0 {
		switch msg.String() {
		case "esc":
			m.editForm.titleInput.Blur()
			m.editForm.editing = false
			return m, nil
		case "enter", "tab", "ctrl+enter":
			m.editForm.titleInput.Blur()
			m.editForm.editing = false
			if m.editForm.currentField < 3 {
				m.editForm.currentField++
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.editForm.titleInput, cmd = m.editForm.titleInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) handleCreateWorkItemKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.createForm.editing {
		return m.handleCreateFormEditingKeys(msg)
	}
	switch {
	case key.Matches(msg, m.keys.Up):
		if m.createForm.currentField > 0 {
			m.createForm.currentField--
		}
	case key.Matches(msg, m.keys.Down):
		if m.createForm.currentField < 2 {
			m.createForm.currentField++
		}
	case key.Matches(msg, m.keys.Enter):
		switch m.createForm.currentField {
		case 0:
			m.createForm.editing = true
			m.createForm.titleInput.Focus()
			m.createForm.titleInput.CursorEnd()
		case 1:
			m.createForm.editing = true
			m.createForm.descInput.Focus()
			m.createForm.descInput.CursorEnd()
		case 2:
			m.openPriorityPicker(m.createForm.priorityID)
		}
	case key.Matches(msg, m.keys.Save):
		if m.currentWorkspace != nil {
			return m, m.createWorkItem(m.currentWorkspace.ID, m.createForm.titleInput.Value(), m.createForm.descInput.Value(), m.createForm.priorityID)
		}
	case key.Matches(msg, m.keys.Back):
		m.currentScreen = WorkItemListScreen
	}
	return m, nil
}

func (m Model) handleCreateFormEditingKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.createForm.titleInput.Blur()
		m.createForm.descInput.Blur()
		m.createForm.editing = false
		return m, nil
	case "enter", "tab", "ctrl+enter":
		m.createForm.titleInput.Blur()
		m.createForm.descInput.Blur()
		m.createForm.editing = false
		if m.createForm.currentField < 2 {
			m.createForm.currentField++
		}
		return m, nil
	}

	var cmd tea.Cmd
	switch m.createForm.currentField {
	case 0:
		m.createForm.titleInput, cmd = m.createForm.titleInput.Update(msg)
	case 1:
		m.createForm.descInput, cmd = m.createForm.descInput.Update(msg)
	}
	return m, cmd
}

func (m Model) handleCommentsKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.commentForm.editing {
		switch msg.String() {
		case "esc":
			m.commentForm.input.Blur()
			m.commentForm.editing = false
			return m, nil
		case "enter":
			content := m.commentForm.input.Value()
			if content != "" && len(m.workItems) > 0 && m.selectedItemIdx < len(m.workItems) {
				item := m.workItems[m.selectedItemIdx]
				m.commentForm.input.Blur()
				m.commentForm.editing = false
				return m, m.createComment(item.ID, content)
			}
			m.commentForm.input.Blur()
			m.commentForm.editing = false
			return m, nil
		}
		var cmd tea.Cmd
		m.commentForm.input, cmd = m.commentForm.input.Update(msg)
		return m, cmd
	}
	switch {
	case key.Matches(msg, m.keys.New):
		m.commentForm = m.resetCommentForm()
		m.commentForm.editing = true
		m.commentForm.input.Focus()
	case key.Matches(msg, m.keys.Refresh):
		if len(m.workItems) > 0 && m.selectedItemIdx < len(m.workItems) {
			item := m.workItems[m.selectedItemIdx]
			return m, m.loadComments(item.ID)
		}
	case key.Matches(msg, m.keys.Back):
		m.currentScreen = WorkItemListScreen
	}
	return m, nil
}

func (m Model) handleTimeLoggingKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.timeForm.editing {
		return m.handleTimeFormEditingKeys(msg)
	}
	switch {
	case key.Matches(msg, m.keys.Up):
		if m.timeForm.currentField > 0 {
			m.timeForm.currentField--
		}
	case key.Matches(msg, m.keys.Down):
		if m.timeForm.currentField < 4 {
			m.timeForm.currentField++
		}
	case key.Matches(msg, m.keys.Enter):
		if m.timeForm.currentField == 4 {
			m.openProjectPicker(m.timeForm.projectID)
		} else {
			m.timeForm.editing = true
			m.focusTimeField()
		}
	case key.Matches(msg, m.keys.Save):
		if len(m.workItems) > 0 && m.selectedItemIdx < len(m.workItems) {
			if m.timeForm.projectID == nil {
				m.errorMessage = "Please select a project"
				return m, nil
			}
			item := m.workItems[m.selectedItemIdx]
			return m, m.createTimeLog(
				item.ID, *m.timeForm.projectID,
				m.timeForm.descInput.Value(),
				m.timeForm.durationInput.Value(),
				m.timeForm.dateInput.Value(),
				m.timeForm.startTimeInput.Value(),
			)
		}
	case key.Matches(msg, m.keys.Back):
		m.currentScreen = WorkItemListScreen
	}
	return m, nil
}

func (m Model) handleTimeFormEditingKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.blurTimeFields()
		m.timeForm.editing = false
		return m, nil
	case "enter", "tab", "ctrl+enter":
		m.blurTimeFields()
		m.timeForm.editing = false
		if m.timeForm.currentField < 4 {
			m.timeForm.currentField++
			if m.timeForm.currentField < 4 {
				m.timeForm.editing = true
				m.focusTimeField()
			}
		}
		return m, nil
	}

	var cmd tea.Cmd
	switch m.timeForm.currentField {
	case 0:
		m.timeForm.descInput, cmd = m.timeForm.descInput.Update(msg)
	case 1:
		m.timeForm.durationInput, cmd = m.timeForm.durationInput.Update(msg)
	case 2:
		m.timeForm.dateInput, cmd = m.timeForm.dateInput.Update(msg)
	case 3:
		m.timeForm.startTimeInput, cmd = m.timeForm.startTimeInput.Update(msg)
	}
	return m, cmd
}

func (m *Model) focusTimeField() {
	m.blurTimeFields()
	switch m.timeForm.currentField {
	case 0:
		m.timeForm.descInput.Focus()
		m.timeForm.descInput.CursorEnd()
	case 1:
		m.timeForm.durationInput.Focus()
		m.timeForm.durationInput.CursorEnd()
	case 2:
		m.timeForm.dateInput.Focus()
		m.timeForm.dateInput.CursorEnd()
	case 3:
		m.timeForm.startTimeInput.Focus()
		m.timeForm.startTimeInput.CursorEnd()
	}
}

func (m *Model) blurTimeFields() {
	m.timeForm.descInput.Blur()
	m.timeForm.durationInput.Blur()
	m.timeForm.dateInput.Blur()
	m.timeForm.startTimeInput.Blur()
}

func (m Model) handleHelpKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Back) || key.Matches(msg, m.keys.Help) {
		return m.toggleHelp(), nil
	}
	return m, nil
}

// ---------- picker dialog openers ----------

func (m *Model) openStatusPicker() {
	options := make([]dialog.Option, len(m.statuses))
	selectedIdx := 0
	for i, s := range m.statuses {
		options[i] = dialog.Option{
			Label: m.chipForStatus(s.Name, s.CategoryColor),
			Value: s,
		}
		if m.editForm.statusID != nil && *m.editForm.statusID == s.ID {
			selectedIdx = i
		}
	}
	m.dialogs = append(m.dialogs, dialog.NewPicker(pickerStatusID, "Select status", options, selectedIdx, m.styles))
}

func (m *Model) openPriorityPicker(currentID *int) {
	options := make([]dialog.Option, len(m.priorities))
	selectedIdx := 0
	for i, p := range m.priorities {
		options[i] = dialog.Option{
			Label: m.chipForPriority(p.Name, p.Color),
			Value: p,
		}
		if currentID != nil && *currentID == p.ID {
			selectedIdx = i
		}
	}
	m.dialogs = append(m.dialogs, dialog.NewPicker(pickerPriorityID, "Select priority", options, selectedIdx, m.styles))
}

func (m *Model) openProjectPicker(currentID *int) {
	options := make([]dialog.Option, len(m.timeProjects))
	selectedIdx := 0
	for i, p := range m.timeProjects {
		label := p.Name
		if p.CustomerName != nil && *p.CustomerName != "" {
			label += " (" + *p.CustomerName + ")"
		}
		options[i] = dialog.Option{
			Label: label,
			Value: p,
		}
		if currentID != nil && int(p.ID) == *currentID {
			selectedIdx = i
		}
	}
	m.dialogs = append(m.dialogs, dialog.NewPicker(pickerProjectID, "Select project", options, selectedIdx, m.styles))
}

// enterTimeLoggingForCurrentWorkspace resets the time-log form and pre-fills
// the project from the workspace's default if one is set.
func (m *Model) enterTimeLoggingForCurrentWorkspace() {
	m.currentScreen = TimeLoggingScreen
	now := timeNow()
	m.timeForm = m.resetTimeLogForm(now.date, now.startTime)

	if m.currentWorkspace != nil && m.currentWorkspace.TimeProjectID != nil {
		m.timeForm.projectID = m.currentWorkspace.TimeProjectID
		for _, p := range m.timeProjects {
			if int(p.ID) == *m.currentWorkspace.TimeProjectID {
				m.timeForm.projectName = p.Name
				break
			}
		}
	}
}

type nowParts struct{ date, startTime string }

func timeNow() nowParts {
	t := time.Now()
	return nowParts{
		date:      t.Format("2006-01-02"),
		startTime: t.Format("15:04"),
	}
}
