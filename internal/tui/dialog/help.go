package dialog

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"windshift/internal/tui/styles"
)

// HelpID is the stable dialog ID of the help overlay.
const HelpID = "help"

// HelpGroup is one titled column-group of bindings in the help overlay.
type HelpGroup struct {
	Title string
	Binds []key.Binding
}

// Help renders grouped key bindings as an overlay dialog. It replaces the
// old full-screen help screen; the caller assembles the groups (typically
// from core.KeyMap) so this package stays free of a core dependency.
type Help struct {
	groups []HelpGroup
	styles *styles.Styles
}

func NewHelp(groups []HelpGroup, s *styles.Styles) *Help {
	return &Help{groups: groups, styles: s}
}

func (h *Help) ID() string    { return HelpID }
func (h *Help) Title() string { return "Help" }

func (h *Help) OnThemeChanged(s *styles.Styles) { h.styles = s }

func (h *Help) HandleKey(msg tea.KeyPressMsg) Action {
	switch msg.String() {
	case "esc", "escape", "q", "?", "enter":
		return Action{Close: true}
	}
	return Action{}
}

func (h *Help) View(_, _ int) string {
	s := h.styles
	var rows []string
	for gi, g := range h.groups {
		rows = append(rows, s.Base.Heading.Render(g.Title))
		for _, b := range g.Binds {
			if !b.Enabled() {
				continue
			}
			hp := b.Help()
			if hp.Key == "" {
				continue
			}
			rows = append(rows, "  "+s.Status.KeyChord.Render(hp.Key)+"  "+s.Status.KeyLabel.Render(hp.Desc))
		}
		if gi < len(h.groups)-1 {
			rows = append(rows, "")
		}
	}
	return strings.Join(rows, "\n")
}
