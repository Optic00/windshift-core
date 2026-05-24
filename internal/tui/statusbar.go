package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// renderStatusBar paints the bottom row: left side is a transient notice
// (loading / success / error) or the screen's tagline; right side is the
// short-help chord list for the current screen. Background is the same
// surface as the header, so the top and bottom frame the body.
func (m Model) renderStatusBar() string {
	s := m.styles

	left := m.statusLeft()
	right := m.statusRight()

	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)

	pad := m.width - leftW - rightW
	if pad < 1 {
		// Drop the right side rather than wrap.
		right = ""
		pad = m.width - leftW
		if pad < 0 {
			left = lipgloss.NewStyle().MaxWidth(m.width).Render(left)
			pad = 0
		}
	}

	bar := left + strings.Repeat(" ", pad) + right
	return lipgloss.NewStyle().
		Width(m.width).
		Background(s.Palette.BgSurface).
		Render(bar)
}

func (m Model) statusLeft() string {
	s := m.styles
	switch {
	case m.errorMessage != "":
		return s.Status.Bar.Render(s.Status.Error.Render("● ") + m.errorMessage)
	case m.successMessage != "":
		return s.Status.Bar.Render(s.Status.Success.Render("● ") + m.successMessage)
	case m.loading:
		return s.Status.Bar.Render(s.Status.Info.Render("● ") + "Loading…")
	}
	tag := screenTagline(m.currentScreen)
	return s.Status.Bar.Render(s.Status.Hint.Render(tag))
}

func (m Model) statusRight() string {
	s := m.styles
	bindings := m.keys.ShortHelp(m.currentScreen)
	var parts []string
	for _, b := range bindings {
		if !b.Enabled() {
			continue
		}
		h := b.Help()
		if h.Key == "" {
			continue
		}
		parts = append(parts, s.Status.KeyChord.Render(h.Key)+" "+s.Status.KeyLabel.Render(h.Desc))
	}
	return s.Status.Bar.Render(strings.Join(parts, " · "))
}

// screenTagline gives the left-side hint text for a screen when there's no
// transient notice to show. Kept short — the chord list on the right is the
// authoritative source of "what can I press."
func screenTagline(screen AppScreen) string {
	switch screen {
	case WorkspaceListScreen:
		return "Pick a workspace"
	case WorkItemListScreen:
		return "Work items"
	case WorkItemDetailScreen:
		return "Editing"
	case CreateWorkItemScreen:
		return "New work item"
	case CommentsScreen:
		return "Comments"
	case TimeLoggingScreen:
		return "Log time"
	case HelpScreen:
		return "Help"
	}
	return ""
}
