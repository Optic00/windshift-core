package board

import (
	"strings"

	"charm.land/lipgloss/v2"

	"windshift/internal/tui/styles"
)

// framePane renders titled rounded pane chrome. w/h are outer dimensions;
// content uses (w-2)×(h-2), and focused panes use the focus border.
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

	// Top edge embeds the title.
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

	// Clamp without wrapping so the frame geometry remains intact.
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
