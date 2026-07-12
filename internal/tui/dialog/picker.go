package dialog

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"windshift/internal/tui/styles"
)

// Option is one selectable row in a Picker. Label is rendered as-is (callers
// can pre-style it, e.g. with a colored chip for a status name). Search is
// the plain-text form used for type-to-filter — required when Label carries
// styling; falls back to Label when empty. Value is returned via
// Action.Selected on enter.
type Option struct {
	Label  string
	Search string
	Value  any
}

func (o Option) searchText() string {
	if o.Search != "" {
		return o.Search
	}
	return o.Label
}

// Picker is a vertical list dialog: title, options, ↑↓ to move, printable
// keys type-to-filter, enter to select, esc to cancel. Single concrete
// dialog reused for status, priority, assignee and project pickers.
type Picker struct {
	id       string
	title    string
	options  []Option
	filtered []int // indexes into options
	selected int   // index into filtered
	query    string
	styles   *styles.Styles
}

func NewPicker(id, title string, options []Option, selected int, s *styles.Styles) *Picker {
	p := &Picker{
		id:      id,
		title:   title,
		options: options,
		styles:  s,
	}
	p.applyFilter()
	if selected >= 0 && selected < len(options) {
		for fi, oi := range p.filtered {
			if oi == selected {
				p.selected = fi
				break
			}
		}
	}
	return p
}

func (p *Picker) ID() string    { return p.id }
func (p *Picker) Title() string { return p.title }

func (p *Picker) applyFilter() {
	prevOption := -1
	if p.selected >= 0 && p.selected < len(p.filtered) {
		prevOption = p.filtered[p.selected]
	}
	p.filtered = p.filtered[:0]
	q := strings.ToLower(strings.TrimSpace(p.query))
	for i, opt := range p.options {
		if q == "" || strings.Contains(strings.ToLower(opt.searchText()), q) {
			p.filtered = append(p.filtered, i)
		}
	}
	p.selected = 0
	if prevOption >= 0 {
		for fi, oi := range p.filtered {
			if oi == prevOption {
				p.selected = fi
				break
			}
		}
	}
}

func (p *Picker) HandleKey(msg tea.KeyPressMsg) Action {
	switch msg.String() {
	case "up":
		if p.selected > 0 {
			p.selected--
		} else if len(p.filtered) > 0 {
			p.selected = len(p.filtered) - 1
		}
		return Action{}
	case "down":
		if len(p.filtered) > 0 {
			p.selected = (p.selected + 1) % len(p.filtered)
		}
		return Action{}
	case "enter":
		if p.selected >= 0 && p.selected < len(p.filtered) {
			return Action{Close: true, Selected: p.options[p.filtered[p.selected]].Value}
		}
		return Action{Close: true}
	case "esc", "escape":
		if p.query != "" {
			p.query = ""
			p.applyFilter()
			return Action{}
		}
		return Action{Close: true}
	case "backspace":
		if p.query != "" {
			r := []rune(p.query)
			p.query = string(r[:len(r)-1])
			p.applyFilter()
		}
		return Action{}
	}

	// Printable characters extend the filter query (type-to-filter). k/j
	// deliberately filter rather than navigate — arrows navigate.
	if t := msg.String(); len([]rune(t)) == 1 && t >= " " {
		p.query += t
		p.applyFilter()
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
	if p.query != "" {
		rows = append(rows,
			p.styles.Status.KeyChord.Render("/ ")+p.styles.Dialog.Body.Render(p.query),
			"")
	}
	if len(p.filtered) == 0 {
		rows = append(rows, p.styles.Dialog.Body.Render("(no matches)"))
		return strings.Join(rows, "\n")
	}
	for fi, oi := range p.filtered {
		label := p.options[oi].Label
		if lipgloss.Width(label) > maxLabel {
			label = lipgloss.NewStyle().MaxWidth(maxLabel).Render(label) + "…"
		}
		var row string
		if fi == p.selected {
			row = p.styles.List.SelBar.Render("▎") + " " + p.styles.List.ItemSelected.Render(label)
		} else {
			row = "  " + p.styles.List.Item.Render(label)
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}
