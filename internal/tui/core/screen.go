// Package core holds the screen contract, the shared per-session context and
// the navigation messages that connect screens to the root app model. It sits
// below screens/dialogs/components so nothing here may import them.
package core

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// Screen is the contract every foreground view implements. Screens are
// pointer receivers — Update mutates in place and returns only the follow-up
// command. The root model routes key presses and broadcast messages here and
// composes View() output between the header and status bar.
type Screen interface {
	// Init fires when the screen becomes active (after SetSize).
	Init() tea.Cmd
	// Update receives key presses (when no dialog is open) and every
	// broadcast message. Unknown messages should be ignored.
	Update(msg tea.Msg) tea.Cmd
	// View renders the body region only — the root adds header, status bar
	// and background.
	View() string
	// SetSize informs the screen of its body region size.
	SetSize(width, height int)
	// Title is the status-bar tagline for the screen (e.g. "Work items").
	Title() string
	// ShortHelp lists the key bindings shown in the status bar, left to
	// right.
	ShortHelp() []key.Binding
}

// TextEditor is an optional capability: screens that contain focusable text
// inputs return true while one is focused so the root suppresses global
// single-key bindings (q, ?, …) that would otherwise swallow typed
// characters.
type TextEditor interface {
	EditingText() bool
}

// ThemeAware is an optional capability: screens/components that bake styles
// into retained state (textinput styles, cached rendered rows, markdown
// renderers) re-derive them here after a theme switch. Anything that reads
// ctx.Styles at render time picks the new theme up for free.
type ThemeAware interface {
	OnThemeChanged()
}
