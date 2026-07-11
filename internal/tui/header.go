package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"windshift/internal/tui/data"
	"windshift/internal/tui/logo"
)

// renderHeader paints a single line at the top of the screen:
//
//	◐ Windshift ╱╱╱╱╱╱╱╱╱╱  WI · Workspace name        user@host
//
// The diagonal fill grows/shrinks with terminal width; left and right blocks
// are pinned. Truncates the user/workspace blocks before the diagonals if
// terminal is narrow.
func (m Model) renderHeader() string {
	s := m.styles
	left := logo.Small(s.Header.GradFrom, s.Header.GradTo)

	var middle string
	if m.currentWorkspace != nil {
		middle = s.Header.Workspace.Render(m.currentWorkspace.Key + " · " + m.currentWorkspace.Name)
	}

	right := s.Header.User.Render(headerUserLabel(m.userInfo))

	leftW := lipgloss.Width(left)
	midW := lipgloss.Width(middle)
	rightW := lipgloss.Width(right)

	// Layout: left + " " + diagonals + " " + middle + "   " + right
	// Fixed gaps: 1 + 1 + 3 = 5 cells.
	const fixedGaps = 5
	diagW := m.width - leftW - midW - rightW - fixedGaps
	if diagW < 1 {
		diagW = 1
		// Truncate the middle if we're tight.
		budget := m.width - leftW - rightW - fixedGaps - diagW
		if budget < 0 {
			budget = 0
		}
		if midW > budget {
			middle = lipgloss.NewStyle().MaxWidth(budget).Render(middle)
		}
	}

	diag := s.Header.Divider.Render(strings.Repeat("╱", diagW))

	parts := []string{left, " ", diag, " ", middle, "   ", right}
	bar := strings.Join(parts, "")

	// Pad / truncate to exact width, then apply the header bar background.
	bar = lipgloss.NewStyle().
		Width(m.width).
		Background(s.Palette.BgSurface).
		Render(bar)

	// Thin underline rule below the bar uses the same width.
	rule := s.Header.BottomEdge.Render(strings.Repeat("─", m.width))
	return bar + "\n" + rule
}

// headerUserLabel collapses UserInfo to a one-token display name. Mirrors
// the precedence the old status bar used (first/last name → username → email
// local-part → credential name).
func headerUserLabel(u *data.UserInfo) string {
	if u == nil {
		return ""
	}
	switch {
	case u.FirstName != "" && u.LastName != "":
		return data.SanitizeLine(u.FirstName + " " + u.LastName)
	case u.Username != "":
		return data.SanitizeLine(u.Username)
	case u.Email != "":
		email := data.SanitizeLine(u.Email)
		if at := strings.Index(email, "@"); at > 0 {
			return email[:at]
		}
		return email
	}
	name := data.SanitizeLine(u.CredentialName)
	name = strings.TrimSuffix(name, " SSH Key")
	name = strings.TrimSuffix(name, " Key")
	return name
}
