// Package timelog is the log-time form for one work item. It still talks to
// the legacy /api/* time endpoints via the session token.
package timelog

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"windshift/internal/tui/components/inputs"
	"windshift/internal/tui/core"
	"windshift/internal/tui/data"
	"windshift/internal/tui/dialog"
)

const pickerProjectID = "picker.project"

const (
	fieldDesc = iota
	fieldDuration
	fieldDate
	fieldStartTime
	fieldProject
	fieldMax = fieldProject
)

// Model is the time-logging screen.
type Model struct {
	ctx *core.Ctx

	item         data.WorkItem
	timeProjects []data.TimeProject

	descInput      textinput.Model
	durationInput  textinput.Model
	dateInput      textinput.Model
	startTimeInput textinput.Model
	projectID      *int
	projectName    string
	currentField   int
	editing        bool

	errorHint string
}

func New(ctx *core.Ctx, item data.WorkItem, timeProjects []data.TimeProject) *Model {
	now := time.Now()

	desc := inputs.New(ctx.Styles, "What did you work on?", 500)
	desc.SetWidth(inputs.Width(ctx.Width))
	dur := inputs.New(ctx.Styles, "e.g. 1h30m", 20)
	dur.SetWidth(inputs.Width(ctx.Width))
	d := inputs.New(ctx.Styles, "YYYY-MM-DD", 10)
	d.SetValue(now.Format("2006-01-02"))
	d.SetWidth(inputs.Width(ctx.Width))
	t := inputs.New(ctx.Styles, "HH:MM", 5)
	t.SetValue(now.Format("15:04"))
	t.SetWidth(inputs.Width(ctx.Width))

	m := &Model{
		ctx:            ctx,
		item:           item,
		timeProjects:   timeProjects,
		descInput:      desc,
		durationInput:  dur,
		dateInput:      d,
		startTimeInput: t,
	}

	// Pre-fill the project from the workspace's default if one is set.
	if ctx.Workspace != nil && ctx.Workspace.TimeProjectID != nil {
		m.projectID = ctx.Workspace.TimeProjectID
		for _, p := range timeProjects {
			if int(p.ID) == *ctx.Workspace.TimeProjectID {
				m.projectName = p.Name
				break
			}
		}
	}
	return m
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) SetSize(width, _ int) {
	w := inputs.Width(width)
	m.descInput.SetWidth(w)
	m.durationInput.SetWidth(w)
	m.dateInput.SetWidth(w)
	m.startTimeInput.SetWidth(w)
}

func (m *Model) Title() string { return "Log time" }

// OnThemeChanged re-applies input styles baked at construction
// (core.ThemeAware).
func (m *Model) OnThemeChanged() {
	st := inputs.Styles(m.ctx.Styles)
	m.descInput.SetStyles(st)
	m.durationInput.SetStyles(st)
	m.dateInput.SetStyles(st)
	m.startTimeInput.SetStyles(st)
}

func (m *Model) ShortHelp() []key.Binding {
	k := m.ctx.Keys
	return []key.Binding{k.Up, k.Down, k.Enter, k.Theme, k.Save, k.Back}
}

// EditingText reports whether a text field is focused (core.TextEditor).
func (m *Model) EditingText() bool { return m.editing }

func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case data.TimeLogCreatedMsg:
		return tea.Batch(core.NotifySuccess("Time logged"), core.Pop())

	case data.TimeProjectsLoadedMsg:
		m.timeProjects = msg.Projects
		return nil

	case dialog.ResultMsg:
		if msg.ID == pickerProjectID {
			if p, ok := msg.Value.(data.TimeProject); ok {
				id := int(p.ID)
				m.projectID = &id
				m.projectName = p.Name
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
		if m.currentField == fieldProject {
			return m.openProjectPicker()
		}
		m.editing = true
		m.focusField()
	case key.Matches(msg, k.Save):
		if m.projectID == nil {
			m.errorHint = "Please select a project"
			return nil
		}
		m.errorHint = ""
		return data.CreateTimeLog(
			m.ctx.Client,
			m.item.ID, *m.projectID,
			m.descInput.Value(),
			m.durationInput.Value(),
			m.dateInput.Value(),
			m.startTimeInput.Value(),
		)
	case key.Matches(msg, k.Back):
		return core.Pop()
	}
	return nil
}

func (m *Model) handleEditingKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.blurFields()
		m.editing = false
		return nil
	case "enter", "tab", "ctrl+enter":
		m.blurFields()
		m.editing = false
		if m.currentField < fieldProject {
			m.currentField++
			if m.currentField < fieldProject {
				m.editing = true
				m.focusField()
			}
		}
		return nil
	}

	var cmd tea.Cmd
	switch m.currentField {
	case fieldDesc:
		m.descInput, cmd = m.descInput.Update(msg)
	case fieldDuration:
		m.durationInput, cmd = m.durationInput.Update(msg)
	case fieldDate:
		m.dateInput, cmd = m.dateInput.Update(msg)
	case fieldStartTime:
		m.startTimeInput, cmd = m.startTimeInput.Update(msg)
	}
	return cmd
}

func (m *Model) focusField() {
	m.blurFields()
	switch m.currentField {
	case fieldDesc:
		m.descInput.Focus()
		m.descInput.CursorEnd()
	case fieldDuration:
		m.durationInput.Focus()
		m.durationInput.CursorEnd()
	case fieldDate:
		m.dateInput.Focus()
		m.dateInput.CursorEnd()
	case fieldStartTime:
		m.startTimeInput.Focus()
		m.startTimeInput.CursorEnd()
	}
}

func (m *Model) blurFields() {
	m.descInput.Blur()
	m.durationInput.Blur()
	m.dateInput.Blur()
	m.startTimeInput.Blur()
}

func (m *Model) openProjectPicker() tea.Cmd {
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
		if m.projectID != nil && int(p.ID) == *m.projectID {
			selectedIdx = i
		}
	}
	return dialog.Open(dialog.NewPicker(pickerProjectID, "Select project", options, selectedIdx, m.ctx.Styles))
}

func (m *Model) View() string {
	s := m.ctx.Styles
	heading := s.Base.Heading.Render("Log time · " + m.item.Title)
	rows := []string{heading, ""}

	w := inputs.Width(m.ctx.Width)
	type fieldRender struct {
		label, hint string
		view        string
	}
	frs := []fieldRender{
		{"Description", "", inputs.Render(s, m.descInput, m.currentField == fieldDesc, m.editing && m.currentField == fieldDesc, w)},
		{"Duration", "Examples: 1h, 30m, 1h30m", inputs.Render(s, m.durationInput, m.currentField == fieldDuration, m.editing && m.currentField == fieldDuration, w)},
		{"Date", "", inputs.Render(s, m.dateInput, m.currentField == fieldDate, m.editing && m.currentField == fieldDate, w)},
		{"Start time", "", inputs.Render(s, m.startTimeInput, m.currentField == fieldStartTime, m.editing && m.currentField == fieldStartTime, w)},
	}
	for _, fr := range frs {
		rows = append(rows, s.Form.Label.Render(fr.label))
		if fr.hint != "" {
			rows = append(rows, s.Form.Hint.Render(fr.hint))
		}
		rows = append(rows, fr.view, "")
	}

	projectLabel := m.projectName
	if projectLabel == "" {
		projectLabel = s.Base.Hint.Render("(select project)")
	}
	rows = append(rows,
		s.Form.Label.Render("Project"),
		inputs.RenderPickerCell(s, projectLabel, m.currentField == fieldProject),
	)

	if m.errorHint != "" {
		rows = append(rows, "", s.Form.Error.Render(m.errorHint))
	}

	return strings.Join(rows, "\n")
}
