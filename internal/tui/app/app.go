// Package app holds the root Bubble Tea model: a thin router/compositor
// that owns the screen stack, the dialog overlay stack and the status-bar
// notice, and delegates everything else to the active screen.
package app

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"windshift/internal/tui/components/header"
	"windshift/internal/tui/components/statusbar"
	"windshift/internal/tui/core"
	"windshift/internal/tui/data"
	"windshift/internal/tui/dialog"
	"windshift/internal/tui/styles"
)

// chromeRows is what the header (bar + rule) and status bar occupy.
const chromeRows = 3

// Model is the root model. It implements tea.Model by value but all mutable
// state lives behind pointers (ctx, screens), so Update mutates in place.
type Model struct {
	ctx     *core.Ctx
	stack   []core.Screen
	dialogs []dialog.Dialog
	notice  statusbar.Notice
}

// New builds the root model with root as the bottom (initial) screen.
func New(ctx *core.Ctx, root core.Screen) Model {
	return Model{
		ctx:   ctx,
		stack: []core.Screen{root},
	}
}

func (m Model) active() core.Screen { return m.stack[len(m.stack)-1] }

// Init starts the initial screen and kicks off the preferences load (which
// never blocks startup — failure just means defaults).
func (m Model) Init() tea.Cmd {
	return tea.Batch(data.LoadPrefs(m.ctx.Client), m.active().Init())
}

// Update routes messages: sizes fan out to the whole stack, keys go to the
// top dialog or the active screen, navigation/dialog/notice messages mutate
// root state, and everything else broadcasts to every stacked screen.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.ctx.Width = msg.Width
		m.ctx.Height = msg.Height
		for _, s := range m.stack {
			s.SetSize(msg.Width, bodyHeight(msg.Height))
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case core.PushMsg:
		msg.Screen.SetSize(m.ctx.Width, bodyHeight(m.ctx.Height))
		m.stack = append(m.stack, msg.Screen)
		return m, msg.Screen.Init()

	case core.PopMsg:
		if len(m.stack) > 1 {
			m.stack = m.stack[:len(m.stack)-1]
		}
		return m, nil

	case core.ReplaceMsg:
		msg.Screen.SetSize(m.ctx.Width, bodyHeight(m.ctx.Height))
		m.stack[len(m.stack)-1] = msg.Screen
		return m, msg.Screen.Init()

	case dialog.OpenMsg:
		m.dialogs = append(m.dialogs, msg.Dialog)
		return m, nil

	case core.NoticeMsg:
		switch msg.Kind {
		case core.NoticeSuccess:
			m.notice = statusbar.Notice{Kind: statusbar.Success, Text: msg.Text}
		case core.NoticeError:
			m.notice = statusbar.Notice{Kind: statusbar.Error, Text: msg.Text}
		default:
			m.notice = statusbar.Notice{}
		}
		return m, nil

	case data.PrefsLoadedMsg:
		if msg.OK {
			m.ctx.Prefs = msg.Prefs
			if msg.Prefs.Theme != "" && msg.Prefs.Theme != m.ctx.Theme {
				m.applyTheme(styles.ByName(msg.Prefs.Theme))
			}
		}
		return m, m.broadcast(msg)

	case data.ErrorMsg:
		m.notice = statusbar.Notice{Kind: statusbar.Error, Text: msg.Err}
		return m, m.broadcast(msg)
	}

	return m, m.broadcast(msg)
}

// broadcast delivers msg to every screen in the stack (bottom to top).
// Screens ignore messages they don't know; the stack stays shallow, so this
// is cheap and lets an underlying list refresh while a form is on top.
func (m Model) broadcast(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for _, s := range m.stack {
		if cmd := s.Update(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Success notices are transient — any key clears them. Errors stay until
	// something succeeds or explicitly clears.
	if m.notice.Kind == statusbar.Success {
		m.notice = statusbar.Notice{}
	}

	// Dialogs eat keys first.
	if len(m.dialogs) > 0 {
		top := m.dialogs[len(m.dialogs)-1]
		action := top.HandleKey(msg)
		var cmds []tea.Cmd
		if action.Cmd != nil {
			cmds = append(cmds, action.Cmd)
		}
		if action.Close {
			m.dialogs = m.dialogs[:len(m.dialogs)-1]
			if action.Selected != nil {
				if cmd := m.active().Update(dialog.ResultMsg{ID: top.ID(), Value: action.Selected}); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		return m, tea.Batch(cmds...)
	}

	// Global keys — but only when the active screen isn't editing text,
	// otherwise they'd swallow typed characters that look like
	// single-letter bindings (e.g. 'q' in a comment).
	if !m.editingText() {
		if key.Matches(msg, m.ctx.Keys.Quit) {
			return m, tea.Quit
		}
		if key.Matches(msg, m.ctx.Keys.Help) {
			m.dialogs = append(m.dialogs, dialog.NewHelp(m.helpGroups(), m.ctx.Styles))
			return m, nil
		}
		if key.Matches(msg, m.ctx.Keys.Theme) {
			return m, m.cycleTheme()
		}
	}

	return m, m.active().Update(msg)
}

// cycleTheme advances to the next registered theme and persists the choice.
func (m Model) cycleTheme() tea.Cmd {
	next := styles.Next(m.ctx.Theme)
	m.applyTheme(next)
	m.ctx.Prefs.Theme = next.Name
	return tea.Batch(
		core.NotifySuccess("Theme: "+next.Name),
		data.SavePrefs(m.ctx.Client, m.ctx.Prefs),
	)
}

// applyTheme replaces ctx.Styles wholesale — anything reading it at render
// time restyles for free; screens with baked styles are told via ThemeAware.
func (m Model) applyTheme(t styles.Theme) {
	m.ctx.Theme = t.Name
	m.ctx.Styles = styles.New(t.Palette)
	for _, s := range m.stack {
		if ta, ok := s.(core.ThemeAware); ok {
			ta.OnThemeChanged()
		}
	}
}

func (m Model) editingText() bool {
	if ed, ok := m.active().(core.TextEditor); ok {
		return ed.EditingText()
	}
	return false
}

func (m Model) helpGroups() []dialog.HelpGroup {
	k := m.ctx.Keys
	return []dialog.HelpGroup{
		{Title: "Global", Binds: []key.Binding{k.Help, k.Quit}},
		{Title: "Navigation", Binds: []key.Binding{k.Up, k.Down, k.Enter, k.Back}},
		{Title: "Item actions", Binds: []key.Binding{k.New, k.Save, k.LogTime, k.Comments, k.Refresh}},
		{Title: "Editing", Binds: []key.Binding{k.NextField, k.PrevField}},
	}
}

// View composes the screen: header on top, body in the middle, status bar at
// the bottom. Active dialog overlays everything via lipgloss.Place.
func (m Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = m.windowTitle()
	v.BackgroundColor = m.ctx.Styles.Palette.BgBase

	if m.ctx.Width == 0 || m.ctx.Height == 0 {
		v.SetContent("")
		return v
	}

	body := m.active().View()
	body = lipgloss.NewStyle().
		Width(m.ctx.Width).
		Height(bodyHeight(m.ctx.Height)).
		Background(m.ctx.Styles.Palette.BgBase).
		Foreground(m.ctx.Styles.Palette.FgBase).
		Render(body)

	content := header.Render(m.ctx.Styles, m.ctx.Width, m.workspaceLabel(), m.ctx.User) +
		"\n" + body + "\n" +
		statusbar.Render(m.ctx.Styles, m.ctx.Width, m.notice, m.active().Title(), m.active().ShortHelp())

	if len(m.dialogs) > 0 {
		content = m.overlayDialog(m.dialogs[len(m.dialogs)-1])
	}

	v.SetContent(content)
	return v
}

func (m Model) workspaceLabel() string {
	if m.ctx.Workspace != nil {
		return m.ctx.Workspace.Key + " · " + m.ctx.Workspace.Name
	}
	return ""
}

// windowTitle is what we set on tea.View.WindowTitle. Terminals that
// support OSC 0 will pick it up.
func (m Model) windowTitle() string {
	if m.ctx.Workspace != nil {
		return "Windshift · " + m.ctx.Workspace.Key
	}
	return "Windshift"
}

// overlayDialog centers the dialog frame over a blank backdrop. Matches the
// previous compositing (the body behind is not visible while a dialog is
// open) — proper alpha overlay can come later if needed.
func (m Model) overlayDialog(d dialog.Dialog) string {
	s := m.ctx.Styles

	width := 40
	if sized, ok := d.(interface{ PreferredWidth() int }); ok {
		width = sized.PreferredWidth()
	}
	if width > m.ctx.Width-8 {
		width = m.ctx.Width - 8
	}

	footerText := "↑↓ select · enter confirm · esc cancel"
	if withFooter, ok := d.(interface{ Footer() string }); ok {
		footerText = withFooter.Footer()
	}

	titleLine := s.Dialog.Title.Render(d.Title())
	stacked := titleLine + "\n" + d.View(width, m.ctx.Height-8)
	if footerText != "" {
		stacked += "\n" + s.Dialog.Footer.Render(footerText)
	}
	frame := s.Dialog.Frame.Render(stacked)
	return lipgloss.Place(
		m.ctx.Width,
		m.ctx.Height,
		lipgloss.Center,
		lipgloss.Center,
		frame,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(s.Palette.BgBase)),
	)
}

// bodyHeight subtracts the header (2 rows incl. the rule) and status bar
// (1 row) from the terminal height.
func bodyHeight(termHeight int) int {
	h := termHeight - chromeRows
	if h < 1 {
		return 1
	}
	return h
}
