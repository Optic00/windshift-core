// Package items is the work-item list for the entered workspace. It owns the
// workspace-scoped reference data (statuses, priorities, time projects) that
// the screens it pushes need.
package items

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"windshift/internal/tui/components/chip"
	"windshift/internal/tui/core"
	"windshift/internal/tui/data"
	"windshift/internal/tui/screens/comments"
	"windshift/internal/tui/screens/create"
	"windshift/internal/tui/screens/detail"
	"windshift/internal/tui/screens/timelog"
)

// Model is the work-item list screen.
type Model struct {
	ctx *core.Ctx

	items        []data.WorkItem
	statuses     []data.Status
	priorities   []data.Priority
	timeProjects []data.TimeProject

	selected int
	loading  bool

	spinner spinner.Model
	width   int
	height  int
}

func New(ctx *core.Ctx) *Model {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(ctx.Styles.Palette.Primary)
	return &Model{
		ctx:     ctx,
		loading: true,
		spinner: sp,
	}
}

func (m *Model) Init() tea.Cmd {
	if m.ctx.Workspace == nil {
		return nil
	}
	m.loading = true
	return tea.Batch(
		data.LoadWorkItems(m.ctx.Client, m.ctx.Workspace.ID),
		data.LoadStatuses(m.ctx.Client, m.ctx.Workspace.ID),
		data.LoadPriorities(m.ctx.Client),
		data.LoadTimeProjects(m.ctx.Client),
		m.spinner.Tick,
	)
}

func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// OnThemeChanged re-derives styles baked into retained components
// (core.ThemeAware).
func (m *Model) OnThemeChanged() {
	m.spinner.Style = lipgloss.NewStyle().Foreground(m.ctx.Styles.Palette.Primary)
}

func (m *Model) Title() string { return "Work items" }

func (m *Model) ShortHelp() []key.Binding {
	k := m.ctx.Keys
	return []key.Binding{k.Up, k.Down, k.Enter, k.New, k.Comments, k.LogTime, k.Back, k.Help}
}

func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return cmd

	case data.WorkItemsLoadedMsg:
		m.items = msg.Items
		m.loading = false
		if len(m.items) > 0 && m.selected >= len(m.items) {
			m.selected = 0
		}
		return nil

	case data.StatusesLoadedMsg:
		m.statuses = msg.Statuses
		return nil

	case data.PrioritiesLoadedMsg:
		m.priorities = msg.Priorities
		return nil

	case data.TimeProjectsLoadedMsg:
		m.timeProjects = msg.Projects
		return nil

	case data.WorkItemCreatedMsg, data.WorkItemUpdatedMsg:
		// A pushed screen mutated an item — refresh the list underneath.
		if m.ctx.Workspace != nil {
			return data.LoadWorkItems(m.ctx.Client, m.ctx.Workspace.ID)
		}
		return nil

	case data.ErrorMsg:
		m.loading = false
		return nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return nil
}

func (m *Model) currentItem() *data.WorkItem {
	if len(m.items) == 0 || m.selected >= len(m.items) {
		return nil
	}
	return &m.items[m.selected]
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.ctx.Keys
	switch {
	case key.Matches(msg, k.Up):
		if m.selected > 0 {
			m.selected--
		} else if len(m.items) > 0 {
			m.selected = len(m.items) - 1
		}
	case key.Matches(msg, k.Down):
		if len(m.items) > 0 {
			m.selected = (m.selected + 1) % len(m.items)
		}
	case key.Matches(msg, k.Enter):
		if item := m.currentItem(); item != nil {
			return core.Push(detail.New(m.ctx, *item, m.statuses, m.priorities, m.timeProjects))
		}
	case key.Matches(msg, k.LogTime):
		if item := m.currentItem(); item != nil {
			return core.Push(timelog.New(m.ctx, *item, m.timeProjects))
		}
	case key.Matches(msg, k.Comments):
		if item := m.currentItem(); item != nil {
			return core.Push(comments.New(m.ctx, *item))
		}
	case key.Matches(msg, k.New):
		if m.ctx.Workspace != nil {
			return core.Push(create.New(m.ctx, m.priorities))
		}
	case key.Matches(msg, k.Refresh):
		if m.ctx.Workspace != nil {
			m.loading = true
			return tea.Batch(data.LoadWorkItems(m.ctx.Client, m.ctx.Workspace.ID), m.spinner.Tick)
		}
	case key.Matches(msg, k.Back):
		m.ctx.Workspace = nil
		return core.Pop()
	}
	return nil
}

func (m *Model) View() string {
	s := m.ctx.Styles
	workspaceName := "Unknown"
	workspaceKey := "WORK"
	if m.ctx.Workspace != nil {
		workspaceName = m.ctx.Workspace.Name
		workspaceKey = m.ctx.Workspace.Key
	}

	heading := s.Base.Heading.Render("Work items · " + workspaceName)
	count := s.List.Counter.Render(fmt.Sprintf("%d items", len(m.items)))

	if m.loading {
		return heading + "\n\n" + m.spinner.View() + " " + s.Base.Hint.Render("Loading work items…")
	}
	if len(m.items) == 0 {
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

	for i, item := range m.items {
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
			statusCell = chip.Status(s, item.StatusName, item.StatusCategoryColor)
		case item.Status != "":
			statusCell = chip.LegacyStatus(s, item.Status)
		default:
			statusCell = s.Base.Hint.Render("—")
		}

		row := fmt.Sprintf("%-*s %s%-*s %s",
			keyWidth, itemKey,
			indent, room, title,
			statusCell,
		)

		if i == m.selected {
			rows = append(rows, s.List.SelBar.Render("▎")+" "+s.List.ItemSelected.Render(row))
		} else {
			rows = append(rows, "  "+s.List.Item.Render(row))
		}
	}

	return strings.Join(rows, "\n")
}
