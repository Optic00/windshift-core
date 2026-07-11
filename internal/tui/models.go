package tui

import (
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"windshift/internal/tui/data"
	"windshift/internal/tui/dialog"
	"windshift/internal/tui/styles"
)

// AppScreen names the foreground view. The picker overlay is no longer a
// screen — it lives as a dialog on Model.dialogs.
type AppScreen int

const (
	WorkspaceListScreen AppScreen = iota
	WorkItemListScreen
	WorkItemDetailScreen
	CreateWorkItemScreen
	CommentsScreen
	TimeLoggingScreen
	HelpScreen
)

// Picker IDs used to disambiguate which form to apply a selection to when
// the dialog closes.
const (
	pickerStatusID   = "picker.status"
	pickerPriorityID = "picker.priority"
	pickerProjectID  = "picker.project"
)

// Model is the root Bubble Tea model. Visual styling is delegated to
// *styles.Styles; component sub-styles are reached through it. Modal
// overlays live on Model.dialogs (top of stack is what the user sees /
// interacts with).
type Model struct {
	// State
	currentScreen AppScreen
	workspaces    []data.Workspace
	workItems     []data.WorkItem
	comments      []data.Comment
	timeProjects  []data.TimeProject
	statuses      []data.Status
	priorities    []data.Priority

	// Selections
	currentWorkspace     *data.Workspace
	selectedWorkspaceIdx int
	selectedItemIdx      int

	// Forms
	editForm    WorkItemEditForm
	commentForm CommentForm
	createForm  CreateWorkItemForm
	timeForm    TimeLogForm

	// Overlay stack (last entry is on top and receives key events).
	dialogs []dialog.Dialog

	// UI state
	loading        bool
	errorMessage   string
	successMessage string

	// API client + auth
	apiClient    *data.Client
	userInfo     *data.UserInfo
	sessionToken string
	bearerToken  string

	// Window size
	width  int
	height int

	// Theme + keys + spinner
	styles  *styles.Styles
	keys    KeyMap
	spinner spinner.Model
}

// NewModelWithUserAndTokens builds the root Model for a given SSH session.
// Either token may be empty — handlers that need one and don't have it will
// surface a 401 to the user.
func NewModelWithUserAndTokens(apiURL string, userInfo *data.UserInfo, sessionToken, bearerToken string) Model {
	theme := styles.New(styles.WindshiftDark())

	apiClient := data.NewClient(apiURL)
	if sessionToken != "" {
		apiClient.SetSessionToken(sessionToken)
	}
	if bearerToken != "" {
		apiClient.SetBearerToken(bearerToken)
	}

	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(theme.Palette.Primary)

	now := time.Now()
	m := Model{
		currentScreen:        WorkspaceListScreen,
		workspaces:           []data.Workspace{},
		workItems:            []data.WorkItem{},
		comments:             []data.Comment{},
		timeProjects:         []data.TimeProject{},
		selectedWorkspaceIdx: 0,
		selectedItemIdx:      0,
		apiClient:            apiClient,
		userInfo:             userInfo,
		sessionToken:         sessionToken,
		bearerToken:          bearerToken,
		styles:               theme,
		keys:                 DefaultKeyMap(),
		spinner:              sp,
		loading:              true,
	}
	// timeForm gets seeded on entry via resetTimeLogForm so the textinputs
	// pick up the current terminal width. The bare date/time strings here
	// just survive the first render before that reset happens.
	m.timeForm.dateInput = newInput(theme, "YYYY-MM-DD", 10)
	m.timeForm.dateInput.SetValue(now.Format("2006-01-02"))
	m.timeForm.startTimeInput = newInput(theme, "HH:MM", 5)
	m.timeForm.startTimeInput.SetValue(now.Format("15:04"))
	m.timeForm.descInput = newInput(theme, "What did you work on?", 500)
	m.timeForm.durationInput = newInput(theme, "e.g. 1h30m", 20)
	return m
}

// Init kicks off the initial workspace load and the spinner tick.
func (m Model) Init() tea.Cmd {
	return tea.Batch(data.LoadWorkspaces(m.apiClient), m.spinner.Tick)
}

// Update is the central message router.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Spinner ticks come back here; let the spinner consume them.
	if _, ok := msg.(spinner.TickMsg); ok {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Resize the description textarea only when we know it's initialized
		// — otherwise SetWidth nil-derefs the unset internal viewport.
		if m.currentScreen == WorkItemDetailScreen {
			taW, taH := textareaDimensions(m.width, m.height)
			m.editForm.descriptionTextarea.SetWidth(taW)
			m.editForm.descriptionTextarea.SetHeight(taH)
		}
		w := inputWidth(m.width)
		m.editForm.titleInput.SetWidth(w)
		m.createForm.titleInput.SetWidth(w)
		m.createForm.descInput.SetWidth(w)
		m.commentForm.input.SetWidth(w)
		m.timeForm.descInput.SetWidth(w)
		m.timeForm.durationInput.SetWidth(w)
		m.timeForm.dateInput.SetWidth(w)
		m.timeForm.startTimeInput.SetWidth(w)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case data.WorkspacesLoadedMsg:
		m.workspaces = msg.Workspaces
		m.loading = false
		if len(m.workspaces) > 0 {
			m.selectedWorkspaceIdx = 0
		}
		return m, nil

	case data.WorkItemsLoadedMsg:
		m.workItems = msg.Items
		m.loading = false
		if len(m.workItems) > 0 {
			m.selectedItemIdx = 0
		}
		return m, nil

	case data.CommentsLoadedMsg:
		m.comments = msg.Comments
		m.loading = false
		return m, nil

	case data.CommentCreatedMsg:
		m.commentForm = m.resetCommentForm()
		m.successMessage = "Comment added"
		m.errorMessage = ""
		if len(m.workItems) > 0 && m.selectedItemIdx < len(m.workItems) {
			item := m.workItems[m.selectedItemIdx]
			return m, data.LoadComments(m.apiClient, item.ID)
		}
		return m, nil

	case data.WorkItemCreatedMsg:
		m.createForm = m.resetCreateForm()
		m.currentScreen = WorkItemListScreen
		m.successMessage = "Work item created"
		m.errorMessage = ""
		if m.currentWorkspace != nil {
			return m, data.LoadWorkItems(m.apiClient, m.currentWorkspace.ID)
		}
		return m, nil

	case data.StatusesLoadedMsg:
		m.statuses = msg.Statuses
		return m, nil

	case data.PrioritiesLoadedMsg:
		m.priorities = msg.Priorities
		return m, nil

	case data.TimeProjectsLoadedMsg:
		m.timeProjects = msg.Projects
		return m, nil

	case data.WorkItemUpdatedMsg:
		m.successMessage = "Work item saved"
		m.errorMessage = ""
		if m.currentWorkspace != nil {
			return m, data.LoadWorkItems(m.apiClient, m.currentWorkspace.ID)
		}
		return m, nil

	case data.TimeLogCreatedMsg:
		m.currentScreen = WorkItemDetailScreen
		m.successMessage = "Time logged"
		m.errorMessage = ""
		return m, nil

	case data.ErrorMsg:
		m.errorMessage = msg.Err
		m.loading = false
		return m, nil
	}
	return m, nil
}

// View composes the screen: header on top, body in the middle, status bar at
// the bottom. Active dialog overlays everything via lipgloss.Place.
func (m Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = m.windowTitle()
	v.BackgroundColor = m.styles.Palette.BgBase

	if m.width == 0 || m.height == 0 {
		v.SetContent("")
		return v
	}

	body := m.renderBody()
	body = lipgloss.NewStyle().
		Width(m.width).
		Height(bodyHeight(m.height)).
		Background(m.styles.Palette.BgBase).
		Foreground(m.styles.Palette.FgBase).
		Render(body)

	content := m.renderHeader() + "\n" + body + "\n" + m.renderStatusBar()

	if len(m.dialogs) > 0 {
		top := m.dialogs[len(m.dialogs)-1]
		content = m.overlayDialog(top)
	}

	v.SetContent(content)
	return v
}

// renderBody picks the screen renderer, falling back to the splash if we're
// loading the very first list of workspaces (no data yet).
func (m Model) renderBody() string {
	if m.loading && m.currentScreen == WorkspaceListScreen && len(m.workspaces) == 0 {
		return m.renderSplash()
	}
	switch m.currentScreen {
	case WorkspaceListScreen:
		return m.renderWorkspaceList()
	case WorkItemListScreen:
		return m.renderWorkItemList()
	case WorkItemDetailScreen:
		return m.renderWorkItemDetail()
	case CreateWorkItemScreen:
		return m.renderCreateWorkItem()
	case CommentsScreen:
		return m.renderComments()
	case TimeLoggingScreen:
		return m.renderTimeLogging()
	case HelpScreen:
		return m.renderHelp()
	}
	return ""
}

// windowTitle is what we set on tea.View.WindowTitle. Terminals that
// support OSC 0 will pick it up.
func (m Model) windowTitle() string {
	if m.currentWorkspace != nil {
		return "Windshift · " + m.currentWorkspace.Key
	}
	return "Windshift"
}

// overlayDialog centers the dialog frame over a blank backdrop. Matches the
// existing picker's compositing (the body behind is not visible while a
// dialog is open) — proper alpha overlay can come later if needed.
func (m Model) overlayDialog(d dialog.Dialog) string {
	s := m.styles
	titleLine := s.Dialog.Title.Render(d.Title())
	body := d.View(40, m.height-8)
	footer := s.Dialog.Footer.Render("↑↓ select · enter confirm · esc cancel")
	stacked := titleLine + "\n" + body + "\n" + footer
	frame := s.Dialog.Frame.Render(stacked)
	return lipgloss.Place(
		m.width,
		m.height,
		lipgloss.Center,
		lipgloss.Center,
		frame,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(s.Palette.BgBase)),
	)
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
