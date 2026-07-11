// Package create is the new-work-item form.
package create

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"windshift/internal/tui/components/chip"
	"windshift/internal/tui/components/inputs"
	"windshift/internal/tui/core"
	"windshift/internal/tui/data"
	"windshift/internal/tui/dialog"
)

const pickerPriorityID = "picker.priority"

const (
	fieldTitle = iota
	fieldDescription
	fieldPriority
	fieldMax = fieldPriority
)

// Model is the create-work-item screen. Single-line description to match
// existing UX (a textarea felt heavy for a quick-create flow).
type Model struct {
	ctx *core.Ctx

	priorities []data.Priority

	titleInput    textinput.Model
	descInput     textinput.Model
	priorityID    *int
	priorityName  string
	priorityColor string
	currentField  int
	editing       bool

	width int
}

func New(ctx *core.Ctx, priorities []data.Priority) *Model {
	title := inputs.New(ctx.Styles, "Title", 200)
	title.SetWidth(inputs.Width(ctx.Width))
	desc := inputs.New(ctx.Styles, "Description (optional)", 1000)
	desc.SetWidth(inputs.Width(ctx.Width))
	return &Model{
		ctx:        ctx,
		priorities: priorities,
		titleInput: title,
		descInput:  desc,
	}
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) SetSize(width, _ int) {
	m.width = width
	m.titleInput.SetWidth(inputs.Width(width))
	m.descInput.SetWidth(inputs.Width(width))
}

func (m *Model) Title() string { return "New work item" }

func (m *Model) ShortHelp() []key.Binding {
	k := m.ctx.Keys
	return []key.Binding{k.Up, k.Down, k.Enter, k.Save, k.Back}
}

// EditingText reports whether a text field is focused (core.TextEditor).
func (m *Model) EditingText() bool { return m.editing }

func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case data.WorkItemCreatedMsg:
		return tea.Batch(core.NotifySuccess("Work item created"), core.Pop())

	case data.PrioritiesLoadedMsg:
		m.priorities = msg.Priorities
		return nil

	case dialog.ResultMsg:
		if msg.ID == pickerPriorityID {
			if p, ok := msg.Value.(data.Priority); ok {
				m.priorityID = &p.ID
				m.priorityName = p.Name
				m.priorityColor = p.Color
			}
		}
		return nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return nil
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
			m.descInput.Focus()
			m.descInput.CursorEnd()
		case fieldPriority:
			return m.openPriorityPicker()
		}
	case key.Matches(msg, k.Save):
		if m.ctx.Workspace != nil {
			return data.CreateWorkItem(m.ctx.Client, m.ctx.Workspace.ID, m.titleInput.Value(), m.descInput.Value(), m.priorityID)
		}
	case key.Matches(msg, k.Back):
		return core.Pop()
	}
	return nil
}

func (m *Model) handleEditingKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.titleInput.Blur()
		m.descInput.Blur()
		m.editing = false
		return nil
	case "enter", "tab", "ctrl+enter":
		m.titleInput.Blur()
		m.descInput.Blur()
		m.editing = false
		if m.currentField < fieldMax {
			m.currentField++
		}
		return nil
	}

	var cmd tea.Cmd
	switch m.currentField {
	case fieldTitle:
		m.titleInput, cmd = m.titleInput.Update(msg)
	case fieldDescription:
		m.descInput, cmd = m.descInput.Update(msg)
	}
	return cmd
}

func (m *Model) openPriorityPicker() tea.Cmd {
	options := make([]dialog.Option, len(m.priorities))
	selectedIdx := 0
	for i, p := range m.priorities {
		options[i] = dialog.Option{
			Label: chip.Priority(m.ctx.Styles, p.Name, p.Color),
			Value: p,
		}
		if m.priorityID != nil && *m.priorityID == p.ID {
			selectedIdx = i
		}
	}
	return dialog.Open(dialog.NewPicker(pickerPriorityID, "Select priority", options, selectedIdx, m.ctx.Styles))
}

func (m *Model) View() string {
	s := m.ctx.Styles
	workspaceName := "Unknown"
	if m.ctx.Workspace != nil {
		workspaceName = m.ctx.Workspace.Name
	}
	heading := s.Base.Heading.Render("New work item · " + workspaceName)
	rows := []string{
		heading, "",
		s.Form.Label.Render("Title"),
		inputs.Render(s, m.titleInput, m.currentField == fieldTitle, m.editing && m.currentField == fieldTitle, inputs.Width(m.width)),
		"",
		s.Form.Label.Render("Description"),
		inputs.Render(s, m.descInput, m.currentField == fieldDescription, m.editing && m.currentField == fieldDescription, inputs.Width(m.width)),
		"",
		s.Form.Label.Render("Priority"),
		inputs.RenderPickerCell(s, chip.Priority(s, m.priorityName, m.priorityColor), m.currentField == fieldPriority),
		"",
	}
	return strings.Join(rows, "\n")
}
