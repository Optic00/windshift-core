// Package chip renders small colored status/priority labels. Colors come
// from the API (already sanitized at ingestion); fallbacks are muted gray.
package chip

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"windshift/internal/tui/styles"
)

const fallbackHex = "#5e6c84"

type colorPair struct {
	background string
	foreground string
}

// These are the web design system's dark-mode status tokens. Keeping the
// background and foreground as a pair avoids the low-contrast combinations
// produced by placing a theme-dependent text color on a solid category color.
var webStatusColors = map[string]colorPair{ //nolint:gochecknoglobals // immutable token table
	"neutral": {background: "#374151", foreground: "#9ca3af"},
	"info":    {background: "#1e3a5f", foreground: "#60a5fa"},
	"success": {background: "#052e16", foreground: "#4ade80"},
	"warning": {background: "#422006", foreground: "#fbbf24"},
	"danger":  {background: "#450a0a", foreground: "#f87171"},
}

var categoryStatusType = map[string]string{ //nolint:gochecknoglobals // mirrors web statusColors.js
	"#6b7280": "neutral",
	"#71717a": "neutral",
	"#3b82f6": "info",
	"#2563eb": "info",
	"#10b981": "success",
	"#16a34a": "success",
	"#22c55e": "success",
	"#f59e0b": "warning",
	"#d97706": "warning",
	"#ef4444": "danger",
	"#dc2626": "danger",
}

// Status renders an API-colored chip for a status name. Falls back to muted
// gray when the API gave us no color.
func Status(s *styles.Styles, name, hex string) string {
	if name == "" {
		return s.Base.Hint.Render("(not set)")
	}
	colors := statusColorPair(name, hex)
	return s.Chip.Base.
		Background(lipgloss.Color(colors.background)).
		Foreground(lipgloss.Color(colors.foreground)).
		Render(strings.ToUpper(name))
}

func statusColorPair(name, categoryColor string) colorPair {
	categoryColor = strings.ToLower(strings.TrimSpace(categoryColor))
	if statusType, ok := categoryStatusType[categoryColor]; ok {
		return webStatusColors[statusType]
	}
	if categoryColor == "" {
		return webStatusColors[statusTypeForName(name)]
	}
	return colorPair{background: categoryColor, foreground: contrastText(categoryColor)}
}

func statusTypeForName(name string) string {
	normalized := strings.NewReplacer("_", "", " ", "", "-", "").Replace(strings.ToLower(name))
	switch normalized {
	case "open", "new", "todo", "backlog":
		return "info"
	case "inprogress", "pending", "review", "inreview", "blocked":
		return "warning"
	case "completed", "done", "closed", "resolved", "approved", "passed":
		return "success"
	case "cancelled", "canceled", "rejected", "failed": //nolint:misspell // accept both spellings
		return "danger"
	default:
		return "neutral"
	}
}

// contrastText ports the web color utility's luminance/saturation thresholds
// for custom category colors. Invalid values fall back to white.
func contrastText(hexColor string) string {
	hexColor = strings.TrimPrefix(hexColor, "#")
	if len(hexColor) != 6 {
		return "#ffffff"
	}
	value, err := strconv.ParseUint(hexColor, 16, 32)
	if err != nil {
		return "#ffffff"
	}
	r := float64(value>>16) / 255
	g := float64((value>>8)&0xff) / 255
	b := float64(value&0xff) / 255
	luminance := 0.299*r + 0.587*g + 0.114*b
	maxChannel := max(r, g, b)
	minChannel := min(r, g, b)
	saturation := 0.0
	if maxChannel > 0 {
		saturation = (maxChannel - minChannel) / maxChannel
	}
	if (saturation < 0.15 && luminance > 0.4) || (saturation >= 0.15 && luminance > 0.65) {
		return "#111827"
	}
	return "#ffffff"
}

// Priority is the same as Status but uses the priority casing.
func Priority(s *styles.Styles, name, hex string) string {
	if name == "" {
		return s.Base.Hint.Render("(not set)")
	}
	bg := hex
	if bg == "" {
		bg = fallbackHex
	}
	return s.Chip.Base.
		Background(lipgloss.Color(bg)).
		Foreground(s.Palette.OnPrimary).
		Render(name)
}

// LegacyStatus colors a free-text status when the work item has no ID-based
// status. Kept for back-compat with older payloads.
func LegacyStatus(s *styles.Styles, status string) string {
	return Status(s, status, "")
}
