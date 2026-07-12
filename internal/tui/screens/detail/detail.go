// Package detail is the work-item edit screen: title, description, status
// and priority, with pickers for the latter two.
package detail

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"windshift/internal/tui/components/chip"
	"windshift/internal/tui/components/inputs"
	"windshift/internal/tui/core"
	"windshift/internal/tui/data"
	"windshift/internal/tui/dialog"
	"windshift/internal/tui/screens/comments"
	"windshift/internal/tui/screens/timelog"
)

const (
	pickerStatusID   = "picker.status"
	pickerPriorityID = "picker.priority"
)

const (
	fieldTitle = iota
	fieldDescription
	fieldStatus
	fieldPriority
	fieldMax = fieldPriority
)

// Model is the work-item edit screen.
type Model struct {
	ctx *core.Ctx

	item         data.WorkItem
	statuses     []data.Status
	priorities   []data.Priority
	timeProjects []data.TimeProject

	titleInput          textinput.Model
	descriptionTextarea textarea.Model
	statusID            *int
	statusName          string
	statusColor         string
	priorityID          *int
	priorityName        string
	priorityColor       string
	currentField        int
	editing             bool

	width  int
	height int
}

func New(ctx *core.Ctx, item data.WorkItem, statuses []data.Status, priorities []data.Priority, timeProjects []data.TimeProject) *Model {
	title := inputs.New(ctx.Styles, "Title", 200)
	title.SetValue(item.Title)
	title.SetWidth(inputs.Width(ctx.Width))

	ta := textarea.New()
	ta.SetValue(item.Description)
	w, h := textareaDimensions(ctx.Width, ctx.Height)
	ta.SetWidth(w)
	ta.SetHeight(h)
	ta.ShowLineNumbers = false
	ta.CharLimit = 5000
	ta.Placeholder = "Enter description…"

	return &Model{
		ctx:                 ctx,
		item:                item,
		statuses:            statuses,
		priorities:          priorities,
		timeProjects:        timeProjects,
		titleInput:          title,
		descriptionTextarea: ta,
		statusID:            item.StatusID,
		statusName:          item.StatusName,
		statusColor:         item.StatusCategoryColor,
		priorityID:          item.PriorityID,
		priorityName:        item.PriorityName,
		priorityColor:       item.PriorityColor,
	}
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
	taW, taH := textareaDimensions(width, height)
	m.descriptionTextarea.SetWidth(taW)
	m.descriptionTextarea.SetHeight(taH)
	m.titleInput.SetWidth(inputs.Width(width))
}

func (m *Model) Title() string { return "Editing" }

// OnThemeChanged re-applies input styles baked at construction
// (core.ThemeAware).
func (m *Model) OnThemeChanged() {
	m.titleInput.SetStyles(inputs.Styles(m.ctx.Styles))
}

func (m *Model) ShortHelp() []key.Binding {
	k := m.ctx.Keys
	return []key.Binding{k.Up, k.Down, k.Enter, k.Save, k.Comments, k.LogTime, k.Back}
}

// EditingText reports whether a text field is focused (core.TextEditor).
func (m *Model) EditingText() bool { return m.editing }

func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case data.WorkItemUpdatedMsg:
		return core.NotifySuccess("Work item saved")

	case data.StatusesLoadedMsg:
		m.statuses = msg.Statuses
		return nil

	case data.PrioritiesLoadedMsg:
		m.priorities = msg.Priorities
		return nil

	case data.TimeProjectsLoadedMsg:
		m.timeProjects = msg.Projects
		return nil

	case dialog.ResultMsg:
		m.applyPickerResult(msg)
		return nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return nil
}

func (m *Model) applyPickerResult(msg dialog.ResultMsg) {
	switch msg.ID {
	case pickerStatusID:
		if s, ok := msg.Value.(data.Status); ok {
			m.statusID = &s.ID
			m.statusName = s.Name
			m.statusColor = s.CategoryColor
		}
	case pickerPriorityID:
		if p, ok := msg.Value.(data.Priority); ok {
			m.priorityID = &p.ID
			m.priorityName = p.Name
			m.priorityColor = p.Color
		}
	}
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.editing {
		return m.handleEditingKey(msg)
	}
	k := m.ctx.Keys
	switch {
	case key.Matches(msg, k.Up):
		if m.currentField > 0 {
			m.currentField--
		}
	case key.Matches(msg, k.Down):
		if m.currentField < fieldMax {
			m.currentField++
		}
	case key.Matches(msg, k.Enter):
		switch m.currentField {
		case fieldTitle:
			m.editing = true
			m.titleInput.Focus()
			m.titleInput.CursorEnd()
		case fieldDescription:
			m.editing = true
			return m.descriptionTextarea.Focus()
		case fieldStatus:
			return m.openStatusPicker()
		case fieldPriority:
			return m.openPriorityPicker()
		}
	case key.Matches(msg, k.Save):
		return data.UpdateWorkItem(m.ctx.Client, m.item.ID, m.titleInput.Value(), m.descriptionTextarea.Value(), m.statusID, m.priorityID)
	case key.Matches(msg, k.LogTime):
		return core.Push(timelog.New(m.ctx, m.item, m.timeProjects))
	case key.Matches(msg, k.Comments):
		return core.Push(comments.New(m.ctx, m.item))
	case key.Matches(msg, k.Back):
		return core.Pop()
	}
	return nil
}

func (m *Model) handleEditingKey(msg tea.KeyPressMsg) tea.Cmd {
	// Description (textarea, multi-line). Esc stops editing; tab/ctrl+enter
	// moves to next field; everything else (incl. enter) goes to the
	// textarea so it can insert newlines.
	if m.currentField == fieldDescription {
		switch msg.String() {
		case "esc":
			m.descriptionTextarea.Blur()
			m.editing = false
			return nil
		case "tab", "ctrl+enter":
			m.descriptionTextarea.Blur()
			m.editing = false
			if m.currentField < fieldMax {
				m.currentField++
			}
			return nil
		}
		var cmd tea.Cmd
		m.descriptionTextarea, cmd = m.descriptionTextarea.Update(msg)
		return cmd
	}

	// Title (single-line textinput). Esc / enter / tab leave editing.
	if m.currentField == fieldTitle {
		switch msg.String() {
		case "esc":
			m.titleInput.Blur()
			m.editing = false
			return nil
		case "enter", "tab", "ctrl+enter":
			m.titleInput.Blur()
			m.editing = false
			if m.currentField < fieldMax {
				m.currentField++
			}
			return nil
		}
		var cmd tea.Cmd
		m.titleInput, cmd = m.titleInput.Update(msg)
		return cmd
	}
	return nil
}

func (m *Model) openStatusPicker() tea.Cmd {
	options := make([]dialog.Option, len(m.statuses))
	selectedIdx := 0
	for i, s := range m.statuses {
		options[i] = dialog.Option{
			Label:  chip.Status(m.ctx.Styles, s.Name, s.CategoryColor),
			Search: s.Name,
			Value:  s,
		}
		if m.statusID != nil && *m.statusID == s.ID {
			selectedIdx = i
		}
	}
	return dialog.Open(dialog.NewPicker(pickerStatusID, "Select status", options, selectedIdx, m.ctx.Styles))
}

func (m *Model) openPriorityPicker() tea.Cmd {
	options := make([]dialog.Option, len(m.priorities))
	selectedIdx := 0
	for i, p := range m.priorities {
		options[i] = dialog.Option{
			Label:  chip.Priority(m.ctx.Styles, p.Name, p.Color),
			Search: p.Name,
			Value:  p,
		}
		if m.priorityID != nil && *m.priorityID == p.ID {
			selectedIdx = i
		}
	}
	return dialog.Open(dialog.NewPicker(pickerPriorityID, "Select priority", options, selectedIdx, m.ctx.Styles))
}

func (m *Model) View() string {
	s := m.ctx.Styles
	workspaceKey := "WORK"
	if m.ctx.Workspace != nil {
		workspaceKey = m.ctx.Workspace.Key
	}
	itemKey := fmt.Sprintf("%s-%d", workspaceKey, m.item.ID)

	heading := s.Base.Heading.Render("Edit · " + itemKey)

	var descRow string
	if m.currentField == fieldDescription && m.editing {
		descRow = m.descriptionTextarea.View()
	} else {
		descPreview := m.descriptionTextarea.Value()
		if descPreview == "" {
			descPreview = s.Base.Hint.Render("(empty)")
		}
		descFrame := s.Form.Input
		if m.currentField == fieldDescription {
			descFrame = s.Form.InputFocused
		}
		descRow = descFrame.Width(inputs.Width(m.width)).Render(descPreview)
	}

	rows := make([]string, 0, 16)
	rows = append(rows,
		heading, "",
		s.Form.Label.Render("Title"),
		inputs.Render(s, m.titleInput, m.currentField == fieldTitle, m.editing && m.currentField == fieldTitle, inputs.Width(m.width)),
		"",
		s.Form.Label.Render("Description"),
		descRow,
		"",
		s.Form.Label.Render("Status"),
		inputs.RenderPickerCell(s, chip.Status(s, m.statusName, m.statusColor), m.currentField == fieldStatus),
		"",
		s.Form.Label.Render("Priority"),
		inputs.RenderPickerCell(s, chip.Priority(s, m.priorityName, m.priorityColor), m.currentField == fieldPriority),
		"",
	)

	if m.editing && m.currentField == fieldDescription {
		rows = append(rows, s.Form.Hint.Render("esc save · tab next field · enter newline"))
	}
	return strings.Join(rows, "\n")
}

// textareaDimensions clamps the description textarea size to sensible bounds
// (need to be > 0 to avoid a v2 viewport nil-deref before we know the
// terminal width).
func textareaDimensions(winW, winH int) (w, h int) {
	w = winW - 6
	if w < 40 {
		w = 40
	}
	h = winH / 3
	if h < 6 {
		h = 6
	}
	if h > 20 {
		h = 20
	}
	return w, h
}
