// Package header renders the single-line top bar plus its underline rule.
package header

import (
	"strings"

	"charm.land/lipgloss/v2"

	"windshift/internal/tui/data"
	"windshift/internal/tui/styles"
)

// Render paints the header:
//
//	◐ Windshift   /   WI · Workspace name                         user@host
//
// Left and right blocks are pinned; the workspace breadcrumb truncates when
// the terminal is narrow. workspaceLabel may be empty (workspace picker).
func Render(s *styles.Styles, width int, workspaceLabel string, u *data.UserInfo) string {
	left := s.Header.Logo.Foreground(s.Palette.Primary).Render("◐ Windshift")

	var middle string
	if workspaceLabel != "" {
		middle = s.Header.Workspace.Render(workspaceLabel)
	}

	right := s.Header.User.Render(UserLabel(u))

	leftW := lipgloss.Width(left)
	midW := lipgloss.Width(middle)
	rightW := lipgloss.Width(right)

	// Keep the header calm and breadcrumb-like. The previous diagonal gradient
	// fill competed with the board and was the strongest visual element.
	const fixedGaps = 9 // outer padding plus "   /   "
	budget := width - leftW - rightW - fixedGaps
	if budget < 0 {
		budget = 0
	}
	if midW > budget {
		middle = lipgloss.NewStyle().MaxWidth(budget).Render(middle)
		midW = lipgloss.Width(middle)
	}
	pad := width - leftW - midW - rightW - fixedGaps
	if pad < 0 {
		pad = 0
	}

	parts := []string{" ", left, s.Header.Divider.Render("   /   "), middle, strings.Repeat(" ", pad), right, " "}
	bar := strings.Join(parts, "")

	// Pad / truncate to exact width, then apply the header bar background.
	bar = lipgloss.NewStyle().
		Width(width).
		Background(s.Palette.BgSurface).
		Render(bar)

	// Thin underline rule below the bar uses the same width.
	rule := s.Header.BottomEdge.Render(strings.Repeat("─", width))
	return bar + "\n" + rule
}

// UserLabel collapses UserInfo to a one-token display name (first/last name
// → username → email local-part → credential name).
func UserLabel(u *data.UserInfo) string {
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
