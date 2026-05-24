package dialog

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"windshift/internal/tui/styles"
)

// Option is one selectable row in a Picker. Label is rendered as-is (callers
// can pre-style it, e.g. with a colored chip for a status name). Value is
// returned via Action.Selected on enter.
type Option struct {
	Label string
	Value any
}

// Picker is a vertical list dialog: title, options, ↑↓ to move, enter to
// select, esc to cancel. Single concrete dialog reused for status, priority
// and project pickers.
type Picker struct {
	id       string
	title    string
	options  []Option
	selected int
	styles   *styles.Styles
}

func NewPicker(id, title string, options []Option, selected int, s *styles.Styles) *Picker {
	if selected < 0 || selected >= len(options) {
		selected = 0
	}
	return &Picker{
		id:       id,
		title:    title,
		options:  options,
		selected: selected,
		styles:   s,
	}
}

func (p *Picker) ID() string    { return p.id }
func (p *Picker) Title() string { return p.title }

func (p *Picker) HandleKey(msg tea.KeyPressMsg) Action {
	switch msg.String() {
	case "up", "k":
		if p.selected > 0 {
			p.selected--
		} else if len(p.options) > 0 {
			p.selected = len(p.options) - 1
		}
	case "down", "j":
		if len(p.options) > 0 {
			p.selected = (p.selected + 1) % len(p.options)
		}
	case "enter":
		if p.selected >= 0 && p.selected < len(p.options) {
			return Action{Close: true, Selected: p.options[p.selected].Value}
		}
		return Action{Close: true}
	case "esc", "escape":
		return Action{Close: true}
	}
	return Action{}
}

func (p *Picker) View(width, _ int) string {
	if len(p.options) == 0 {
		return p.styles.Dialog.Body.Render("(no options)")
	}

	// Width budget: caller wraps us in styles.Dialog.Frame which adds border
	// (2) + horizontal padding (4). Cap items to width-ish so long labels
	// don't blow up the dialog.
	maxLabel := width - 10
	if maxLabel < 12 {
		maxLabel = 12
	}

	var rows []string
	for i, opt := range p.options {
		label := opt.Label
		if lipgloss.Width(label) > maxLabel {
			label = lipgloss.NewStyle().MaxWidth(maxLabel).Render(label) + "…"
		}
		var row string
		if i == p.selected {
			row = p.styles.List.SelBar.Render("▎") + " " + p.styles.List.ItemSelected.Render(label)
		} else {
			row = "  " + p.styles.List.Item.Render(label)
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}
