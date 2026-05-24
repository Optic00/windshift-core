package tui

import (
	"charm.land/lipgloss/v2"

	"windshift/internal/tui/logo"
)

// renderSplash centers the logo + tagline + a loading line vertically. Used
// while initial workspaces fetch — replaces the bare "Loading…" string.
func (m Model) renderSplash() string {
	s := m.styles
	body := logo.Full(logo.Opts{
		From:          s.Header.GradFrom,
		To:            s.Header.GradTo,
		Wordmark:      "WINDSHIFT",
		Tagline:       "self-hostable work tracking",
		TaglineStyle:  s.Splash.Tagline,
		WordmarkStyle: s.Splash.Wordmark,
	})

	loadLine := s.Splash.Loading.Render("Loading workspaces…")
	stacked := body + "\n\n" + loadLine

	return lipgloss.Place(
		m.width,
		bodyHeight(m.height),
		lipgloss.Center,
		lipgloss.Center,
		stacked,
	)
}

// bodyHeight subtracts the header (2 rows incl. the rule) and status bar
// (1 row) from the terminal height. Used by both renderSplash and the main
// screen dispatcher so they pick the same vertical region.
func bodyHeight(termHeight int) int {
	h := termHeight - 3
	if h < 1 {
		return 1
	}
	return h
}
