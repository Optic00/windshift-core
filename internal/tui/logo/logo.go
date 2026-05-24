// Package logo renders the Windshift mark in the terminal.
//
// The web mark is a flowing radial swirl; in the terminal we approximate it
// with box-drawing characters (concentric curves around a centered dot) and
// apply a horizontal foreground gradient so it reads as "branded" rather than
// monochrome. Two variants:
//
//   - Full     — 5×7 mark plus a WINDSHIFT wordmark for splash / empty states.
//   - Compact  — single glyph "◐" plus the word "Windshift" for the header bar.
package logo

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"windshift/internal/tui/styles"
)

// markLines is the splash-size mark. 5 rows × 7 cols. The curves wrap a
// central dot to suggest the radial swirl in the web logo without trying to
// be a faithful ANSI reproduction (which would be illegible at this size).
var markLines = []string{
	" ╭───╮ ",
	"╱ ╭─╮ ╲",
	"│ │◉│ │",
	"╲ ╰─╯ ╱",
	" ╰───╯ ",
}

// Opts controls a Full render.
type Opts struct {
	// From/To define the horizontal gradient applied to the mark and
	// wordmark. Typically Palette.GradFrom / Palette.GradTo.
	From, To color.Color
	// Wordmark is the text shown beside the mark. "" suppresses it.
	Wordmark string
	// Tagline is rendered under the wordmark in a muted style. "" suppresses.
	Tagline string
	// TaglineStyle styles the tagline; zero-value works.
	TaglineStyle lipgloss.Style
	// WordmarkStyle styles the wordmark *before* the gradient is applied.
	// Bold/Padding survive; Foreground is overridden per-rune.
	WordmarkStyle lipgloss.Style
}

// Full renders the mark + wordmark + optional tagline, with the gradient
// applied per-rune across the visible characters. Returns a multi-line
// string (no trailing newline).
func Full(opts Opts) string {
	from, to := opts.From, opts.To
	if from == nil {
		from = lipgloss.Color("#3b82f6")
	}
	if to == nil {
		to = lipgloss.Color("#8b5cf6")
	}

	gradMark := make([]string, len(markLines))
	for i, line := range markLines {
		gradMark[i] = styles.ApplyForegroundGrad(lipgloss.NewStyle(), line, from, to)
	}

	if opts.Wordmark == "" {
		return strings.Join(gradMark, "\n")
	}

	wordStyle := opts.WordmarkStyle.Bold(true)
	word := styles.ApplyForegroundGrad(wordStyle, opts.Wordmark, from, to)

	wordRow := len(markLines) / 2
	rows := make([]string, len(markLines))
	for i, l := range gradMark {
		switch i {
		case wordRow:
			rows[i] = l + "   " + word
		case wordRow + 1:
			if opts.Tagline != "" {
				rows[i] = l + "   " + opts.TaglineStyle.Render(opts.Tagline)
			} else {
				rows[i] = l
			}
		default:
			rows[i] = l
		}
	}
	return strings.Join(rows, "\n")
}

// Small renders a single-line compact mark for the header bar.
// Returns "◐ Windshift" with the dot+name colored across the gradient.
func Small(from, to color.Color) string {
	if from == nil {
		from = lipgloss.Color("#3b82f6")
	}
	if to == nil {
		to = lipgloss.Color("#8b5cf6")
	}
	return styles.ApplyForegroundGrad(
		lipgloss.NewStyle().Bold(true),
		"◐ Windshift",
		from, to,
	)
}
