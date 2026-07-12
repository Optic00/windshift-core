// Package comments is the comment thread + new-comment input for one work
// item.
package comments

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"windshift/internal/tui/components/inputs"
	"windshift/internal/tui/core"
	"windshift/internal/tui/data"
)

// Model is the comments screen for a single work item.
type Model struct {
	ctx *core.Ctx

	item     data.WorkItem
	comments []data.Comment

	input   textinput.Model
	editing bool
}

func New(ctx *core.Ctx, item data.WorkItem) *Model {
	in := inputs.New(ctx.Styles, "Write a comment…", 2000)
	in.SetWidth(inputs.Width(ctx.Width))
	return &Model{
		ctx:   ctx,
		item:  item,
		input: in,
	}
}

func (m *Model) Init() tea.Cmd {
	return data.LoadComments(m.ctx.Client, m.item.ID)
}

func (m *Model) SetSize(width, _ int) {
	m.input.SetWidth(inputs.Width(width))
}

func (m *Model) Title() string { return "Comments" }

// OnThemeChanged re-applies input styles baked at construction
// (core.ThemeAware).
func (m *Model) OnThemeChanged() {
	m.input.SetStyles(inputs.Styles(m.ctx.Styles))
}

func (m *Model) ShortHelp() []key.Binding {
	k := m.ctx.Keys
	return []key.Binding{k.New, k.Refresh, k.Back}
}

// EditingText reports whether the comment input is focused (core.TextEditor).
func (m *Model) EditingText() bool { return m.editing }

func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case data.CommentsLoadedMsg:
		m.comments = msg.Comments
		return nil

	case data.CommentCreatedMsg:
		m.resetInput()
		return tea.Batch(
			core.NotifySuccess("Comment added"),
			data.LoadComments(m.ctx.Client, m.item.ID),
		)

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return nil
}

func (m *Model) resetInput() {
	m.input = inputs.New(m.ctx.Styles, "Write a comment…", 2000)
	m.input.SetWidth(inputs.Width(m.ctx.Width))
	m.editing = false
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.editing {
		switch msg.String() {
		case "esc":
			m.input.Blur()
			m.editing = false
			return nil
		case "enter":
			content := m.input.Value()
			m.input.Blur()
			m.editing = false
			if content != "" {
				return data.CreateComment(m.ctx.Client, m.item.ID, content)
			}
			return nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return cmd
	}

	k := m.ctx.Keys
	switch {
	case key.Matches(msg, k.New):
		m.resetInput()
		m.editing = true
		m.input.Focus()
	case key.Matches(msg, k.Refresh):
		return data.LoadComments(m.ctx.Client, m.item.ID)
	case key.Matches(msg, k.Back):
		return core.Pop()
	}
	return nil
}

func (m *Model) View() string {
	s := m.ctx.Styles

	heading := s.Base.Heading.Render("Comments · " + m.item.Title)
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
		inputs.Render(s, m.input, true, m.editing, inputs.Width(m.ctx.Width)),
	)

	return strings.Join(rows, "\n")
}
