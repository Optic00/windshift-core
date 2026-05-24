// Package dialog defines a small overlay-dialog interface used by the TUI.
//
// Each open dialog is one entry on Model.dialogs []Dialog. The top of the
// stack receives key events; everything paints from bottom up so the most
// recent dialog appears on top. A dialog signals dismissal (and optional
// follow-up Cmd / selection) via Action.
package dialog

import (
	tea "charm.land/bubbletea/v2"
)

// Dialog is the contract every overlay implements.
type Dialog interface {
	// ID is a stable identifier used to find / replace a dialog on the
	// stack. Not displayed.
	ID() string
	// HandleKey processes a key press while this dialog is on top. Returning
	// Action.Close pops it.
	HandleKey(msg tea.KeyPressMsg) Action
	// View renders the dialog body — the framing (border, padding,
	// centering) is added by the caller via the styles.Dialog.Frame style.
	View(width, height int) string
	// Title returns the title shown above the body.
	Title() string
}

// Action is the result of a key press. Selected is type-asserted by the
// caller based on which dialog it opened.
type Action struct {
	Close    bool
	Selected any
	Cmd      tea.Cmd
}
