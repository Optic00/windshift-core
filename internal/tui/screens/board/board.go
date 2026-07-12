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
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"windshift/internal/tui/components/chip"
	"windshift/internal/tui/components/inputs"
	"windshift/internal/tui/core"
	"windshift/internal/tui/data"
	"windshift/internal/tui/dialog"
	"windshift/internal/tui/screens/comments"
	"windshift/internal/tui/screens/create"
	"windshift/internal/tui/screens/detail"
	"windshift/internal/tui/screens/timelog"
)

const (
	pickerStatusID   = "board.picker.status"
	pickerPriorityID = "board.picker.priority"
	pickerAssignID   = "board.picker.assign"
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
	users        []data.User

	filter      Filter
	filterInput textinput.Model
	filtering   bool // filter input focused

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
		filterInput:   inputs.New(ctx.Styles, "filter…", 100),
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
		data.LoadAssignableUsers(m.ctx.Client, m.ctx.Workspace.ID),
		m.spinner.Tick,
	)
}

func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
	if m.narrow() {
		m.list.setSize(width, m.listHeight())
		m.detail.setSize(width, m.paneHeight())
		m.filterInput.SetWidth(width - 4)
		return
	}
	listW, detailW := m.paneWidths()
	m.list.setSize(listW, m.listHeight())
	m.detail.setSize(detailW, m.paneHeight())
	m.filterInput.SetWidth(listW - 4)
}

// listHeight reserves a line for the filter input when it's visible.
func (m *Model) listHeight() int {
	h := m.paneHeight()
	if m.filterVisible() {
		h--
	}
	if h < 1 {
		h = 1
	}
	return h
}

func (m *Model) filterVisible() bool { return m.filtering || m.filter.Active() }

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
	m.detail.resetRenderer()
	m.detail.rebuild()
}

func (m *Model) Title() string { return "Board" }

func (m *Model) ShortHelp() []key.Binding {
	k := m.ctx.Keys
	if m.filtering {
		return []key.Binding{k.Enter, k.Back}
	}
	if m.focus == focusDetail {
		return []key.Binding{k.Up, k.Down, k.FocusToggle, k.Edit, k.Comments, k.Back}
	}
	return []key.Binding{k.Up, k.Down, k.Filter, k.Status, k.Priority, k.Assign, k.Edit, k.New, k.Help}
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

	case data.UsersLoadedMsg:
		m.users = msg.Users
		return nil

	case data.WorkItemLoadedMsg:
		for i := range m.items {
			if m.items[i].ID == msg.Item.ID {
				m.items[i] = msg.Item
				break
			}
		}
		m.rebuildRows()
		return m.syncDetail()

	case dialog.ResultMsg:
		return m.applyPickerResult(msg)

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
	wsKey := ""
	if m.ctx.Workspace != nil {
		wsKey = m.ctx.Workspace.Key
	}
	m.list.setRows(BuildRows(m.items, Grouping{
		CategoryByStatusID: catByStatus,
		PriorityRank:       prioRank,
		MeUserID:           me,
		Collapsed:          m.collapsed,
		Filter:             m.filter,
		WorkspaceKey:       wsKey,
	}))
}

// EditingText reports whether the filter input is focused (core.TextEditor)
// so global single-key bindings don't swallow typed filter characters.
func (m *Model) EditingText() bool { return m.filtering }

// applyPickerResult patches the selected item optimistically and fires the
// matching mutation + targeted refresh.
func (m *Model) applyPickerResult(msg dialog.ResultMsg) tea.Cmd {
	it := m.list.selectedItem()
	if it == nil {
		return nil
	}
	switch msg.ID {
	case pickerStatusID:
		s, ok := msg.Value.(data.Status)
		if !ok {
			return nil
		}
		id := s.ID
		it.StatusID = &id
		it.StatusName = s.Name
		it.StatusCategoryColor = s.CategoryColor
		it.Status = s.Name
		m.rebuildRows()
		return tea.Batch(m.syncDetail(), data.SetItemStatus(m.ctx.Client, it.ID, s.ID))
	case pickerPriorityID:
		p, ok := msg.Value.(data.Priority)
		if !ok {
			return nil
		}
		id := p.ID
		it.PriorityID = &id
		it.PriorityName = p.Name
		it.PriorityColor = p.Color
		it.Priority = p.Name
		m.rebuildRows()
		return tea.Batch(m.syncDetail(), data.SetItemPriority(m.ctx.Client, it.ID, p.ID))
	case pickerAssignID:
		u, ok := msg.Value.(data.User)
		if !ok {
			return nil
		}
		if u.ID > 0 {
			id := u.ID
			it.AssigneeID = &id
			it.AssigneeName = u.FullName
		} else {
			it.AssigneeID = nil
			it.AssigneeName = ""
		}
		m.rebuildRows()
		return tea.Batch(m.syncDetail(), data.SetItemAssignee(m.ctx.Client, it.ID, u.ID))
	}
	return nil
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

	if m.filtering {
		return m.handleFilterKey(msg)
	}

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
	case key.Matches(msg, k.Filter):
		m.filtering = true
		m.filterInput.SetValue(m.filter.Query)
		m.filterInput.CursorEnd()
		m.SetSize(m.width, m.height) // reserve the filter line
		return m.filterInput.Focus()
	case key.Matches(msg, k.Status):
		return m.openStatusPicker()
	case key.Matches(msg, k.Priority):
		return m.openPriorityPicker()
	case key.Matches(msg, k.Assign):
		return m.openAssignPicker()
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
		if m.filter.Active() {
			m.clearFilter()
			return m.syncDetail()
		}
		m.ctx.Workspace = nil
		return core.Pop()
	}
	return nil
}

// handleFilterKey routes keys while the filter input is focused: the filter
// applies live on every keystroke; enter keeps it and returns focus to the
// list; esc clears it.
func (m *Model) handleFilterKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.clearFilter()
		return m.syncDetail()
	case "enter":
		m.filtering = false
		m.filterInput.Blur()
		if !m.filter.Active() {
			m.SetSize(m.width, m.height) // release the filter line
		}
		return nil
	}
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(msg)
	if q := m.filterInput.Value(); q != m.filter.Query {
		m.filter.Query = q
		m.rebuildRows()
		return tea.Batch(cmd, m.syncDetail())
	}
	return cmd
}

func (m *Model) clearFilter() {
	m.filtering = false
	m.filter.Query = ""
	m.filterInput.SetValue("")
	m.filterInput.Blur()
	m.SetSize(m.width, m.height)
	m.rebuildRows()
}

func (m *Model) openStatusPicker() tea.Cmd {
	it := m.list.selectedItem()
	if it == nil || len(m.statuses) == 0 {
		return nil
	}
	options := make([]dialog.Option, len(m.statuses))
	selectedIdx := 0
	for i, s := range m.statuses {
		options[i] = dialog.Option{
			Label:  chip.Status(m.ctx.Styles, s.Name, s.CategoryColor),
			Search: s.Name,
			Value:  s,
		}
		if it.StatusID != nil && *it.StatusID == s.ID {
			selectedIdx = i
		}
	}
	return dialog.Open(dialog.NewPicker(pickerStatusID, "Set status", options, selectedIdx, m.ctx.Styles))
}

func (m *Model) openPriorityPicker() tea.Cmd {
	it := m.list.selectedItem()
	if it == nil || len(m.priorities) == 0 {
		return nil
	}
	options := make([]dialog.Option, len(m.priorities))
	selectedIdx := 0
	for i, p := range m.priorities {
		options[i] = dialog.Option{
			Label:  chip.Priority(m.ctx.Styles, p.Name, p.Color),
			Search: p.Name,
			Value:  p,
		}
		if it.PriorityID != nil && *it.PriorityID == p.ID {
			selectedIdx = i
		}
	}
	return dialog.Open(dialog.NewPicker(pickerPriorityID, "Set priority", options, selectedIdx, m.ctx.Styles))
}

func (m *Model) openAssignPicker() tea.Cmd {
	it := m.list.selectedItem()
	if it == nil || len(m.users) == 0 {
		return nil
	}
	s := m.ctx.Styles
	options := make([]dialog.Option, 0, len(m.users)+1)
	options = append(options, dialog.Option{
		Label:  s.Base.Hint.Render("(unassign)"),
		Search: "unassign",
		Value:  data.User{},
	})
	selectedIdx := 0
	for _, u := range m.users {
		label := u.FullName
		if label == "" {
			label = u.Username
		}
		if u.IsAgent {
			label += " " + s.List.Muted.Render("· agent")
		}
		if m.ctx.User != nil && u.ID == m.ctx.User.UserID {
			label += " " + s.List.Muted.Render("· me")
		}
		options = append(options, dialog.Option{Label: label, Search: u.FullName + " " + u.Username, Value: u})
		if it.AssigneeID != nil && *it.AssigneeID == u.ID {
			selectedIdx = len(options) - 1
		}
	}
	return dialog.Open(dialog.NewPicker(pickerAssignID, "Assign to", options, selectedIdx, m.ctx.Styles))
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
		return notice + m.listColumn()
	}

	listW, detailW := m.paneWidths()
	h := m.paneHeight()

	listBlock := lipgloss.NewStyle().Width(listW).Height(h).MaxHeight(h).Render(m.listColumn())
	detailBlock := lipgloss.NewStyle().Width(detailW).Height(h).MaxHeight(h).Render(m.detail.view())

	sepColor := s.Palette.Border
	if m.focus == focusDetail {
		sepColor = s.Palette.BorderFocus
	}
	sep := lipgloss.NewStyle().Foreground(sepColor).Render(strings.TrimSuffix(strings.Repeat("│\n", h), "\n"))

	return notice + lipgloss.JoinHorizontal(lipgloss.Top, listBlock, " ", sep, " ", detailBlock)
}

// listColumn stacks the list rows and, when active, the filter input line.
func (m *Model) listColumn() string {
	out := m.list.view()
	if m.filterVisible() {
		out += "\n" + m.ctx.Styles.Status.KeyChord.Render("/") + " " + m.filterInput.View()
	}
	return out
}
