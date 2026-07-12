package core

import (
	"windshift/internal/tui/data"
	"windshift/internal/tui/styles"
)

// Ctx is the shared per-SSH-session context. One *Ctx is constructed per
// connection and handed to every screen, dialog and component; screens read
// Styles at render time so a theme switch (which replaces Styles wholesale)
// propagates without plumbing.
type Ctx struct {
	// Styles is replaced atomically on theme switch. Read it at render
	// time; never copy it into retained state without implementing
	// ThemeAware.
	Styles *styles.Styles
	// Theme is the active theme name.
	Theme string

	Client *data.Client
	User   *data.UserInfo
	Keys   KeyMap

	// Workspace is the workspace the user has entered, nil while on the
	// workspace picker. Set by the workspaces screen on selection.
	Workspace *data.Workspace

	// Width and Height are the full terminal dimensions. Screens receive
	// their body-region size via SetSize; these are for overlays that
	// center against the whole terminal.
	Width  int
	Height int
}
