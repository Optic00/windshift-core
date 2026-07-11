// Package splash renders the centered logo + tagline shown while the very
// first workspace list loads.
package splash

import (
	"charm.land/lipgloss/v2"

	"windshift/internal/tui/logo"
	"windshift/internal/tui/styles"
)

// Render centers the logo + tagline + a loading line in a width×height box.
func Render(s *styles.Styles, width, height int) string {
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
		width,
		height,
		lipgloss.Center,
		lipgloss.Center,
		stacked,
	)
}
