package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"windshift/internal/tui/styles"
)

// renderWorkspaceList paints the workspace picker.
func (m Model) renderWorkspaceList() string {
	s := m.styles
	heading := s.Base.Heading.Render("Workspaces")
	count := s.List.Counter.Render(fmt.Sprintf("%d total", len(m.workspaces)))

	if m.loading {
		return heading + "\n\n" + m.spinner.View() + " " + s.Base.Hint.Render("Loading workspaces…")
	}
	if len(m.workspaces) == 0 {
		return heading + "\n\n" + s.List.Empty.Render("No workspaces available.")
	}

	rows := []string{heading + "   " + count, ""}
	for i, w := range m.workspaces {
		label := fmt.Sprintf("%s · %s", w.Key, w.Name)
		if i == m.selectedWorkspaceIdx {
			rows = append(rows, s.List.SelBar.Render("▎")+" "+s.List.ItemSelected.Render(label))
		} else {
			rows = append(rows, "  "+s.List.Item.Render(label))
		}
	}
	if m.errorMessage != "" {
		rows = append(rows, "", s.Status.Error.Render("● ")+sanitizeTerminalLine(m.errorMessage))
	}
	return strings.Join(rows, "\n")
}

// renderWorkItemList paints the work-item table for the current workspace.
func (m Model) renderWorkItemList() string {
	s := m.styles
	workspaceName := "Unknown"
	workspaceKey := "WORK"
	if m.currentWorkspace != nil {
		workspaceName = m.currentWorkspace.Name
		workspaceKey = m.currentWorkspace.Key
	}

	heading := s.Base.Heading.Render("Work items · " + workspaceName)
	count := s.List.Counter.Render(fmt.Sprintf("%d items", len(m.workItems)))

	if m.loading {
		return heading + "\n\n" + m.spinner.View() + " " + s.Base.Hint.Render("Loading work items…")
	}
	if len(m.workItems) == 0 {
		return heading + "\n\n" + s.List.Empty.Render("No work items yet. Press 'n' to create one.")
	}

	availableWidth := m.width - 6
	keyWidth := 12
	statusWidth := 18
	titleWidth := availableWidth - keyWidth - statusWidth - 6
	if titleWidth < 20 {
		titleWidth = 20
	}

	header := s.List.Header.Render(fmt.Sprintf("%-*s %-*s %s",
		keyWidth, "KEY",
		titleWidth, "TITLE",
		"STATUS",
	))
	rule := s.List.Rule.Render(strings.Repeat("─", availableWidth))

	rows := []string{heading + "   " + count, "", header, rule}

	for i, item := range m.workItems {
		indent := strings.Repeat("  ", item.GetLevel())
		itemKey := fmt.Sprintf("%s-%d", workspaceKey, item.ID)

		title := item.Title
		room := titleWidth - len(indent)
		if room < 4 {
			room = 4
		}
		if len(title) > room {
			title = title[:room-1] + "…"
		}

		var statusCell string
		switch {
		case item.StatusName != "":
			statusCell = m.chipForStatus(item.StatusName, item.StatusCategoryColor)
		case item.Status != "":
			statusCell = m.chipForLegacyStatus(item.Status)
		default:
			statusCell = s.Base.Hint.Render("—")
		}

		row := fmt.Sprintf("%-*s %s%-*s %s",
			keyWidth, itemKey,
			indent, room, title,
			statusCell,
		)

		if i == m.selectedItemIdx {
			rows = append(rows, s.List.SelBar.Render("▎")+" "+s.List.ItemSelected.Render(row))
		} else {
			rows = append(rows, "  "+s.List.Item.Render(row))
		}
	}

	if m.errorMessage != "" {
		rows = append(rows, "", s.Status.Error.Render("● ")+sanitizeTerminalLine(m.errorMessage))
	}
	return strings.Join(rows, "\n")
}

// renderWorkItemDetail paints the edit form for a single work item.
func (m Model) renderWorkItemDetail() string {
	if m.selectedItemIdx >= len(m.workItems) {
		return m.styles.Base.Hint.Render("No work item selected")
	}
	s := m.styles
	item := m.workItems[m.selectedItemIdx]
	workspaceKey := "WORK"
	if m.currentWorkspace != nil {
		workspaceKey = m.currentWorkspace.Key
	}
	itemKey := fmt.Sprintf("%s-%d", workspaceKey, item.ID)

	heading := s.Base.Heading.Render("Edit · " + itemKey)

	var descRow string
	if m.editForm.currentField == 1 && m.editForm.editing {
		descRow = m.editForm.descriptionTextarea.View()
	} else {
		descPreview := m.editForm.descriptionTextarea.Value()
		if descPreview == "" {
			descPreview = s.Base.Hint.Render("(empty)")
		}
		descFrame := s.Form.Input
		if m.editForm.currentField == 1 {
			descFrame = s.Form.InputFocused
		}
		descRow = descFrame.Width(inputWidth(m.width)).Render(descPreview)
	}

	rows := make([]string, 0, 16)
	rows = append(rows,
		heading, "",
		s.Form.Label.Render("Title"),
		renderInput(s, m.editForm.titleInput, m.editForm.currentField == 0, m.editForm.editing && m.editForm.currentField == 0, inputWidth(m.width)),
		"",
		s.Form.Label.Render("Description"),
		descRow,
		"",
		s.Form.Label.Render("Status"),
		renderPickerCell(s, m.chipForStatus(m.editForm.statusName, m.editForm.statusColor), m.editForm.currentField == 2),
		"",
		s.Form.Label.Render("Priority"),
		renderPickerCell(s, m.chipForPriority(m.editForm.priorityName, m.editForm.priorityColor), m.editForm.currentField == 3),
		"",
	)

	if m.editForm.editing && m.editForm.currentField == 1 {
		rows = append(rows, s.Form.Hint.Render("esc save · tab next field · enter newline"))
	}
	return strings.Join(rows, "\n")
}

// renderCreateWorkItem paints the new-work-item form.
func (m Model) renderCreateWorkItem() string {
	s := m.styles
	workspaceName := "Unknown"
	if m.currentWorkspace != nil {
		workspaceName = m.currentWorkspace.Name
	}
	heading := s.Base.Heading.Render("New work item · " + workspaceName)
	rows := make([]string, 0, 11)
	rows = append(rows,
		heading, "",
		s.Form.Label.Render("Title"),
		renderInput(s, m.createForm.titleInput, m.createForm.currentField == 0, m.createForm.editing && m.createForm.currentField == 0, inputWidth(m.width)),
		"",
		s.Form.Label.Render("Description"),
		renderInput(s, m.createForm.descInput, m.createForm.currentField == 1, m.createForm.editing && m.createForm.currentField == 1, inputWidth(m.width)),
		"",
		s.Form.Label.Render("Priority"),
		renderPickerCell(s, m.chipForPriority(m.createForm.priorityName, m.createForm.priorityColor), m.createForm.currentField == 2),
		"",
	)
	return strings.Join(rows, "\n")
}

// renderComments paints the comments thread + the new-comment input.
func (m Model) renderComments() string {
	if m.selectedItemIdx >= len(m.workItems) {
		return m.styles.Base.Hint.Render("No work item selected")
	}
	s := m.styles
	item := m.workItems[m.selectedItemIdx]

	heading := s.Base.Heading.Render("Comments · " + item.Title)
	rows := []string{heading, ""}

	if len(m.comments) == 0 {
		rows = append(rows, s.List.Empty.Render("No comments yet. Press 'n' to add one."))
	} else {
		for _, c := range m.comments {
			author := "Unknown"
			if c.AuthorName != nil {
				author = *c.AuthorName
			}
			byline := s.Base.Heading.Render(author) + " " + s.Base.Hint.Render("· "+c.CreatedAt)
			rows = append(rows, byline, c.Content, "")
		}
	}

	rows = append(rows,
		s.Form.Label.Render("New comment"),
		renderInput(s, m.commentForm.input, true, m.commentForm.editing, inputWidth(m.width)),
	)

	return strings.Join(rows, "\n")
}

// renderTimeLogging paints the time-log form.
func (m Model) renderTimeLogging() string {
	if m.selectedItemIdx >= len(m.workItems) {
		return m.styles.Base.Hint.Render("No work item selected")
	}
	s := m.styles
	item := m.workItems[m.selectedItemIdx]
	heading := s.Base.Heading.Render("Log time · " + item.Title)
	rows := []string{heading, ""}

	type fieldRender struct {
		label, hint string
		view        string
		idx         int
	}
	frs := []fieldRender{
		{"Description", "", renderInput(s, m.timeForm.descInput, m.timeForm.currentField == 0, m.timeForm.editing && m.timeForm.currentField == 0, inputWidth(m.width)), 0},
		{"Duration", "Examples: 1h, 30m, 1h30m", renderInput(s, m.timeForm.durationInput, m.timeForm.currentField == 1, m.timeForm.editing && m.timeForm.currentField == 1, inputWidth(m.width)), 1},
		{"Date", "", renderInput(s, m.timeForm.dateInput, m.timeForm.currentField == 2, m.timeForm.editing && m.timeForm.currentField == 2, inputWidth(m.width)), 2},
		{"Start time", "", renderInput(s, m.timeForm.startTimeInput, m.timeForm.currentField == 3, m.timeForm.editing && m.timeForm.currentField == 3, inputWidth(m.width)), 3},
	}
	for _, fr := range frs {
		rows = append(rows, s.Form.Label.Render(fr.label))
		if fr.hint != "" {
			rows = append(rows, s.Form.Hint.Render(fr.hint))
		}
		rows = append(rows, fr.view, "")
	}

	projectLabel := m.timeForm.projectName
	if projectLabel == "" {
		projectLabel = s.Base.Hint.Render("(select project)")
	}
	rows = append(rows,
		s.Form.Label.Render("Project"),
		renderPickerCell(s, projectLabel, m.timeForm.currentField == 4),
	)

	return strings.Join(rows, "\n")
}

// renderHelp paints the static help screen with bindings rendered from KeyMap.
func (m Model) renderHelp() string {
	s := m.styles
	heading := s.Base.Heading.Render("Help")
	rows := []string{heading, ""}

	groups := []struct {
		title string
		binds []struct{ key, desc string }
	}{
		{"Global", []struct{ key, desc string }{
			{m.keys.Help.Help().Key, m.keys.Help.Help().Desc},
			{m.keys.Quit.Help().Key, m.keys.Quit.Help().Desc},
		}},
		{"Navigation", []struct{ key, desc string }{
			{m.keys.Up.Help().Key, m.keys.Up.Help().Desc},
			{m.keys.Down.Help().Key, m.keys.Down.Help().Desc},
			{m.keys.Enter.Help().Key, m.keys.Enter.Help().Desc},
			{m.keys.Back.Help().Key, m.keys.Back.Help().Desc},
		}},
		{"Item actions", []struct{ key, desc string }{
			{m.keys.New.Help().Key, m.keys.New.Help().Desc},
			{m.keys.Save.Help().Key, m.keys.Save.Help().Desc},
			{m.keys.LogTime.Help().Key, m.keys.LogTime.Help().Desc},
			{m.keys.Comments.Help().Key, m.keys.Comments.Help().Desc},
			{m.keys.Refresh.Help().Key, m.keys.Refresh.Help().Desc},
		}},
		{"Editing", []struct{ key, desc string }{
			{m.keys.NextField.Help().Key, m.keys.NextField.Help().Desc},
			{m.keys.PrevField.Help().Key, m.keys.PrevField.Help().Desc},
		}},
	}

	for _, g := range groups {
		rows = append(rows, s.Base.Heading.Render(g.title))
		for _, b := range g.binds {
			rows = append(rows, "  "+s.Status.KeyChord.Render(b.key)+"  "+s.Status.KeyLabel.Render(b.desc))
		}
		rows = append(rows, "")
	}
	return strings.Join(rows, "\n")
}

// renderPickerCell draws a chip/string inside the form input frame (focused
// styling when selected). Used for the status/priority/project rows where
// the value comes from a picker dialog rather than an inline input.
func renderPickerCell(s *Styles, label string, selected bool) string {
	frame := s.Form.Input
	if selected {
		frame = s.Form.InputFocused
	}
	// MaxWidth so the chip's background color doesn't span the whole frame.
	return frame.Render(label + "  " + lipgloss.NewStyle().Foreground(s.Palette.FgMuted).Render("[enter to change]"))
}

// Styles aliases *styles.Styles so renderPickerCell can keep a shorter
// signature without re-exporting the styles package here.
type Styles = styles.Styles
