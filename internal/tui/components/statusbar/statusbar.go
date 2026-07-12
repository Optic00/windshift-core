// Package statusbar renders the bottom row: a transient notice (or the
// screen's tagline) on the left, the short-help chord list on the right.
package statusbar

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"

	"windshift/internal/tui/data"
	"windshift/internal/tui/styles"
)

// Kind classifies the transient notice.
type Kind int

const (
	None Kind = iota
	Success
	Error
)

// Notice is the transient message shown on the left of the bar.
type Notice struct {
	Kind Kind
	Text string
}

// Render paints the status bar. tagline is shown when the notice is empty.
func Render(s *styles.Styles, width int, notice Notice, tagline string, bindings []key.Binding) string {
	left := renderLeft(s, notice, tagline)
	leftW := lipgloss.Width(left)
	right := renderRight(s, bindings, width-leftW-1)
	rightW := lipgloss.Width(right)

	pad := width - leftW - rightW
	if pad < 1 {
		// Drop the right side rather than wrap.
		right = ""
		pad = width - leftW
		if pad < 0 {
			left = lipgloss.NewStyle().MaxWidth(width).Render(left)
			pad = 0
		}
	}

	bar := left + strings.Repeat(" ", pad) + right
	return lipgloss.NewStyle().
		Width(width).
		Background(s.Palette.BgSurface).
		Render(bar)
}

func renderLeft(s *styles.Styles, notice Notice, tagline string) string {
	switch notice.Kind {
	case Error:
		return s.Status.Bar.Render(s.Status.Error.Render("● ") + data.SanitizeLine(notice.Text))
	case Success:
		return s.Status.Bar.Render(s.Status.Success.Render("● ") + data.SanitizeLine(notice.Text))
	}
	return s.Status.Bar.Render(s.Status.Hint.Render(tagline))
}

func renderRight(s *styles.Styles, bindings []key.Binding, maxWidth int) string {
	var parts []string
	for _, b := range bindings {
		if !b.Enabled() {
			continue
		}
		h := b.Help()
		if h.Key == "" {
			continue
		}
		part := s.Status.KeyChord.Render(h.Key) + " " + s.Status.KeyLabel.Render(h.Desc)
		candidate := strings.Join(append(parts, part), " · ")
		if maxWidth > 0 && lipgloss.Width(s.Status.Bar.Render(candidate)) > maxWidth {
			break
		}
		parts = append(parts, part)
	}
	return s.Status.Bar.Render(strings.Join(parts, " · "))
}
