// Package board is the main split-pane view: a status-grouped work-item
// list on the left and a live detail panel on the right that follows the
// selection. It replaces the separate item-list screen.
package board

import (
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"windshift/internal/tui/core"
	"windshift/internal/tui/data"
	"windshift/internal/tui/screens/comments"
	"windshift/internal/tui/screens/create"
	"windshift/internal/tui/screens/detail"
	"windshift/internal/tui/screens/timelog"
)

const (
	defaultSplitRatio = 0.45
	minSplitRatio     = 0.25
	maxSplitRatio     = 0.75
	splitStep         = 0.05
	// narrowWidth is the terminal width below which the board collapses to a
	// single pane.
	narrowWidth = 80
	// commentsDebounce delays the lazy comment fetch while the user is still
	// moving the cursor.
	commentsDebounce = 300 * time.Millisecond
)

type paneFocus int

const (
	focusList paneFocus = iota
	focusDetail
)

// debounceMsg fires after the selection has been resting for a beat; stale
// sequence numbers are dropped.
type debounceMsg struct{ seq int }

// Model is the split-pane board screen.
type Model struct {
	ctx *core.Ctx

	items        []data.WorkItem
	statuses     []data.Status
	priorities   []data.Priority
	timeProjects []data.TimeProject

	comments      map[int][]data.Comment
	commentsFresh map[int]bool
	detailSeq     int

	list   *listPane
	detail *detailPane

	collapsed  map[string]bool
	splitRatio float64
	focus      paneFocus
	// narrowDetail: in single-pane (narrow) mode, whether the detail pane is
	// the visible one.
	narrowDetail bool

	loading   bool
	truncated bool
	spinner   spinner.Model

	width  int
	height int
}

func New(ctx *core.Ctx) *Model {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(ctx.Styles.Palette.Primary)
	return &Model{
		ctx:           ctx,
		comments:      map[int][]data.Comment{},
		commentsFresh: map[int]bool{},
		list:          newListPane(ctx),
		detail:        newDetailPane(ctx),
		collapsed:     map[string]bool{},
		splitRatio:    defaultSplitRatio,
		loading:       true,
		spinner:       sp,
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
	if m.narrow() {
		m.list.setSize(width, m.paneHeight())
		m.detail.setSize(width, m.paneHeight())
		return
	}
	listW, detailW := m.paneWidths()
	m.list.setSize(listW, m.paneHeight())
	m.detail.setSize(detailW, m.paneHeight())
}

func (m *Model) narrow() bool { return m.width < narrowWidth }

func (m *Model) paneHeight() int {
	h := m.height
	if m.truncated {
		h-- // one line for the truncation notice
	}
	if h < 1 {
		h = 1
	}
	return h
}

func (m *Model) paneWidths() (listW, detailW int) {
	listW = int(float64(m.width) * m.splitRatio)
	if listW < 28 {
		listW = 28
	}
	if maxList := m.width - 42; listW > maxList && maxList >= 28 {
		listW = maxList
	}
	detailW = m.width - listW - 3 // " │ " separator
	if detailW < 10 {
		detailW = 10
	}
	return listW, detailW
}

// OnThemeChanged re-derives styles baked into retained components
// (core.ThemeAware).
func (m *Model) OnThemeChanged() {
	m.spinner.Style = lipgloss.NewStyle().Foreground(m.ctx.Styles.Palette.Primary)
	m.detail.rebuild()
}

func (m *Model) Title() string { return "Board" }

func (m *Model) ShortHelp() []key.Binding {
	k := m.ctx.Keys
	if m.focus == focusDetail {
		return []key.Binding{k.Up, k.Down, k.FocusToggle, k.Edit, k.Comments, k.Back}
	}
	return []key.Binding{k.Up, k.Down, k.NextGroup, k.Collapse, k.Edit, k.New, k.Comments, k.LogTime, k.Back, k.Help}
}

func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return cmd

	case data.WorkItemsLoadedMsg:
		m.items = msg.Items
		m.truncated = msg.Truncated
		m.loading = false
		m.SetSize(m.width, m.height) // truncation notice changes pane height
		m.rebuildRows()
		return m.syncDetail()

	case data.StatusesLoadedMsg:
		m.statuses = msg.Statuses
		m.rebuildRows()
		return m.syncDetail()

	case data.PrioritiesLoadedMsg:
		m.priorities = msg.Priorities
		m.rebuildRows()
		return nil

	case data.TimeProjectsLoadedMsg:
		m.timeProjects = msg.Projects
		return nil

	case data.WorkItemCreatedMsg, data.WorkItemUpdatedMsg:
		if m.ctx.Workspace != nil {
			return data.LoadWorkItems(m.ctx.Client, m.ctx.Workspace.ID)
		}
		return nil

	case data.CommentCreatedMsg:
		// Invalidate whatever item is selected — the composer only posts to
		// the selected item.
		if it := m.list.selectedItem(); it != nil {
			m.commentsFresh[it.ID] = false
			return data.LoadComments(m.ctx.Client, it.ID)
		}
		return nil

	case data.CommentsLoadedMsg:
		m.comments[msg.ItemID] = msg.Comments
		m.commentsFresh[msg.ItemID] = true
		if it := m.list.selectedItem(); it != nil && it.ID == msg.ItemID {
			m.detail.setComments(msg.Comments)
		}
		return nil

	case debounceMsg:
		if msg.seq != m.detailSeq {
			return nil // selection moved on
		}
		if it := m.list.selectedItem(); it != nil && !m.commentsFresh[it.ID] {
			return data.LoadComments(m.ctx.Client, it.ID)
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

// rebuildRows reflattens grouping state into the list pane.
func (m *Model) rebuildRows() {
	catByStatus := make(map[int]string, len(m.statuses))
	for _, s := range m.statuses {
		catByStatus[s.ID] = s.CategoryName
	}
	prioRank := make(map[int]int, len(m.priorities))
	for i, p := range m.priorities {
		prioRank[p.ID] = i
	}
	me := 0
	if m.ctx.User != nil {
		me = m.ctx.User.UserID
	}
	m.list.setRows(BuildRows(m.items, Grouping{
		CategoryByStatusID: catByStatus,
		PriorityRank:       prioRank,
		MeUserID:           me,
		Collapsed:          m.collapsed,
	}))
}

// syncDetail points the detail pane at the current selection and returns the
// debounced comment-fetch command when the cache is cold.
func (m *Model) syncDetail() tea.Cmd {
	it := m.list.selectedItem()
	if it == nil {
		m.detail.setItem(nil, nil, false)
		return nil
	}
	fresh := m.commentsFresh[it.ID]
	m.detail.setItem(it, m.comments[it.ID], fresh)
	if fresh {
		return nil
	}
	m.detailSeq++
	seq := m.detailSeq
	return tea.Tick(commentsDebounce, func(time.Time) tea.Msg { return debounceMsg{seq: seq} })
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.ctx.Keys

	if m.focus == focusDetail {
		switch {
		case key.Matches(msg, k.Up):
			m.detail.scroll(-1)
		case key.Matches(msg, k.Down):
			m.detail.scroll(1)
		case key.Matches(msg, k.HalfPageUp):
			m.detail.halfPage(-1)
		case key.Matches(msg, k.HalfPageDown):
			m.detail.halfPage(1)
		case key.Matches(msg, k.FocusToggle), key.Matches(msg, k.Back):
			m.focus = focusList
			m.narrowDetail = false
		case key.Matches(msg, k.Edit):
			return m.pushEdit()
		case key.Matches(msg, k.Comments):
			return m.pushComments()
		}
		return nil
	}

	switch {
	case key.Matches(msg, k.Up):
		m.list.move(-1)
		return m.syncDetail()
	case key.Matches(msg, k.Down):
		m.list.move(1)
		return m.syncDetail()
	case key.Matches(msg, k.PrevGroup):
		m.list.jumpGroup(-1)
		return m.syncDetail()
	case key.Matches(msg, k.NextGroup):
		m.list.jumpGroup(1)
		return m.syncDetail()
	case key.Matches(msg, k.HalfPageUp):
		m.list.halfPage(-1)
		return m.syncDetail()
	case key.Matches(msg, k.HalfPageDown):
		m.list.halfPage(1)
		return m.syncDetail()
	case key.Matches(msg, k.Collapse):
		if g := m.list.selectedGroupKey(); g != "" {
			m.collapsed[g] = !m.collapsed[g]
			m.rebuildRows()
			return m.syncDetail()
		}
	case key.Matches(msg, k.Enter), key.Matches(msg, k.FocusToggle):
		if m.list.selectedItem() != nil {
			m.focus = focusDetail
			m.narrowDetail = true
		}
	case key.Matches(msg, k.SplitNarrow):
		m.adjustSplit(-splitStep)
	case key.Matches(msg, k.SplitWiden):
		m.adjustSplit(splitStep)
	case key.Matches(msg, k.Edit):
		return m.pushEdit()
	case key.Matches(msg, k.New):
		if m.ctx.Workspace != nil {
			return core.Push(create.New(m.ctx, m.priorities))
		}
	case key.Matches(msg, k.Comments):
		return m.pushComments()
	case key.Matches(msg, k.LogTime):
		if it := m.list.selectedItem(); it != nil {
			return core.Push(timelog.New(m.ctx, *it, m.timeProjects))
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

func (m *Model) adjustSplit(delta float64) {
	m.splitRatio += delta
	if m.splitRatio < minSplitRatio {
		m.splitRatio = minSplitRatio
	}
	if m.splitRatio > maxSplitRatio {
		m.splitRatio = maxSplitRatio
	}
	m.SetSize(m.width, m.height)
}

func (m *Model) pushEdit() tea.Cmd {
	if it := m.list.selectedItem(); it != nil {
		return core.Push(detail.New(m.ctx, *it, m.statuses, m.priorities, m.timeProjects))
	}
	return nil
}

func (m *Model) pushComments() tea.Cmd {
	if it := m.list.selectedItem(); it != nil {
		return core.Push(comments.New(m.ctx, *it))
	}
	return nil
}

func (m *Model) View() string {
	s := m.ctx.Styles

	if m.loading && len(m.items) == 0 {
		return m.spinner.View() + " " + s.Base.Hint.Render("Loading work items…")
	}

	var notice string
	if m.truncated {
		notice = s.Base.Hint.Render("Showing the first "+strconv.Itoa(len(m.items))+" items — refine in the web UI for more.") + "\n"
	}

	if m.narrow() {
		if m.narrowDetail {
			return notice + m.detail.view()
		}
		return notice + m.list.view()
	}

	listW, detailW := m.paneWidths()
	h := m.paneHeight()

	listBlock := lipgloss.NewStyle().Width(listW).Height(h).MaxHeight(h).Render(m.list.view())
	detailBlock := lipgloss.NewStyle().Width(detailW).Height(h).MaxHeight(h).Render(m.detail.view())

	sepColor := s.Palette.Border
	if m.focus == focusDetail {
		sepColor = s.Palette.BorderFocus
	}
	sep := lipgloss.NewStyle().Foreground(sepColor).Render(strings.TrimSuffix(strings.Repeat("│\n", h), "\n"))

	return notice + lipgloss.JoinHorizontal(lipgloss.Top, listBlock, " ", sep, " ", detailBlock)
}
