package core

import "charm.land/bubbles/v2/key"

// KeyMap defines every binding the TUI listens for, in one place. Screens
// expose the subset relevant to them via Screen.ShortHelp; the help dialog
// consumes FullHelp for the complete listing.
type KeyMap struct {
	// Global
	Quit key.Binding
	Help key.Binding

	// Navigation
	Up    key.Binding
	Down  key.Binding
	Left  key.Binding
	Right key.Binding

	// Common actions
	Enter   key.Binding
	Back    key.Binding
	Refresh key.Binding

	// Item actions
	New      key.Binding
	Save     key.Binding
	LogTime  key.Binding
	Comments key.Binding

	// Form editing
	NextField key.Binding
	PrevField key.Binding
}

// DefaultKeyMap returns the bindings used by every screen.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Help:      key.NewBinding(key.WithKeys("?", "h", "f1"), key.WithHelp("?", "help")),
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Left:      key.NewBinding(key.WithKeys("left"), key.WithHelp("←", "left")),
		Right:     key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "right")),
		Enter:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		Back:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Refresh:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		New:       key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
		Save:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "save")),
		LogTime:   key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "log time")),
		Comments:  key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "comments")),
		NextField: key.NewBinding(key.WithKeys("tab", "ctrl+enter"), key.WithHelp("tab", "next field")),
		PrevField: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev field")),
	}
}

// FullHelp returns column-grouped bindings for the help dialog.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Enter, k.Back, k.NextField, k.PrevField},
		{k.New, k.Save, k.Refresh, k.Comments, k.LogTime},
		{k.Help, k.Quit},
	}
}
