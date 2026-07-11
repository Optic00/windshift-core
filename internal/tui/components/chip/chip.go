// Package chip renders small colored status/priority labels. Colors come
// from the API (already sanitized at ingestion); fallbacks are muted gray.
package chip

import (
	"strings"

	"charm.land/lipgloss/v2"

	"windshift/internal/tui/styles"
)

const fallbackHex = "#5e6c84"

// Status renders an API-colored chip for a status name. Falls back to muted
// gray when the API gave us no color.
func Status(s *styles.Styles, name, hex string) string {
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
		Render(strings.ToUpper(name))
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
	hex := fallbackHex
	switch strings.ToLower(status) {
	case "open", "to_do", "todo":
		hex = "#3b82f6"
	case "in_progress", "in progress", "progress":
		hex = "#ca8a04"
	case "completed", "done", "closed":
		hex = "#2874bb"
	case "cancelled", "canceled": //nolint:misspell // accept both spellings
		hex = "#dc2626"
	}
	return s.Chip.Base.
		Background(lipgloss.Color(hex)).
		Foreground(s.Palette.OnPrimary).
		Render(strings.ToUpper(status))
}
