package board

import (
	"strings"

	"charm.land/lipgloss/v2"

	"windshift/internal/tui/styles"
)

// framePane wraps pre-sized pane content in a rounded border with the title
// embedded in the top edge — the board's ltui-style pane chrome:
//
//	╭─ Work items · 12 ────────╮
//	│ …                        │
//	╰──────────────────────────╯
//
// w and h are the OUTER dimensions; content is rendered into (w-2)×(h-2).
// The focused pane's border uses the focus color.
func framePane(s *styles.Styles, title, content string, w, h int, focused bool) string {
	if w < 4 || h < 3 {
		return content
	}
	innerW := w - 2
	innerH := h - 2

	borderColor := s.Palette.Border
	titleStyle := lipgloss.NewStyle().Foreground(s.Palette.FgSubtle)
	if focused {
		borderColor = s.Palette.BorderFocus
		titleStyle = lipgloss.NewStyle().Foreground(s.Palette.PrimaryHovered).Bold(true)
	}
	bc := lipgloss.NewStyle().Foreground(borderColor)

	// Top edge with embedded title: ╭─ title ─…─╮
	var top string
	if title != "" {
		t := " " + title + " "
		maxTitle := innerW - 3
		if lipgloss.Width(t) > maxTitle {
			t = lipgloss.NewStyle().MaxWidth(maxTitle).Render(t)
		}
		top = bc.Render("╭─") + titleStyle.Render(t)
		fill := innerW - 2 - lipgloss.Width(t)
		if fill < 0 {
			fill = 0
		}
		top += bc.Render(strings.Repeat("─", fill) + "╮")
	} else {
		top = bc.Render("╭" + strings.Repeat("─", innerW) + "╮")
	}

	// Body rows, clamped/padded to the inner box. MaxWidth truncates (it
	// never wraps — wrapping would break the frame); padding is manual.
	lines := strings.Split(content, "\n")
	if len(lines) > innerH {
		lines = lines[:innerH]
	}
	clamp := lipgloss.NewStyle().MaxWidth(innerW)
	side := bc.Render("│")
	body := make([]string, 0, innerH)
	for i := 0; i < innerH; i++ {
		line := ""
		if i < len(lines) {
			line = clamp.Render(lines[i])
		}
		if pad := innerW - lipgloss.Width(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		body = append(body, side+line+side)
	}

	bottom := bc.Render("╰" + strings.Repeat("─", innerW) + "╯")

	return top + "\n" + strings.Join(body, "\n") + "\n" + bottom
}
