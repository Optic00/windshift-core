package dialog

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"windshift/internal/tui/styles"
)

// FormField is one labeled input in a Form. Exactly one of Input/Area is
// used, selected by Multiline.
type FormField struct {
	Key       string // key in the submitted values map
	Label     string
	Multiline bool
	Input     textinput.Model
	Area      textarea.Model
}

// FormResult is delivered through ResultMsg.Value on submit.
type FormResult struct {
	Values map[string]string
}

// Form is a modal form dialog: tab/shift+tab (and enter on single-line
// fields) move focus, ctrl+s submits, esc cancels. Construct it at open
// time so the textarea is born with a real size (bubbletea v2 sizing
// hazard).
type Form struct {
	id     string
	title  string
	fields []FormField
	focus  int
	styles *styles.Styles
	width  int
}

// NewForm builds a form dialog. width is the inner content width the
// fields are sized to.
func NewForm(id, title string, fields []FormField, s *styles.Styles, width int) *Form {
	if width < 30 {
		width = 30
	}
	f := &Form{
		id:     id,
		title:  title,
		fields: fields,
		styles: s,
		width:  width,
	}
	for i := range f.fields {
		if f.fields[i].Multiline {
			f.fields[i].Area.SetWidth(width)
		} else {
			f.fields[i].Input.SetWidth(width)
		}
	}
	f.setFocus(0)
	return f
}

func (f *Form) ID() string    { return f.id }
func (f *Form) Title() string { return f.title }

// PreferredWidth lets the app's overlay compositor size the frame to the
// form instead of the default picker width.
func (f *Form) PreferredWidth() int { return f.width + 4 }

// Footer suppresses the default picker footer — the form renders its own
// hint line.
func (f *Form) Footer() string { return "" }

func (f *Form) setFocus(i int) {
	if len(f.fields) == 0 {
		return
	}
	if i < 0 {
		i = len(f.fields) - 1
	}
	i %= len(f.fields)
	for j := range f.fields {
		if f.fields[j].Multiline {
			f.fields[j].Area.Blur()
		} else {
			f.fields[j].Input.Blur()
		}
	}
	f.focus = i
	if f.fields[i].Multiline {
		f.fields[i].Area.Focus()
	} else {
		f.fields[i].Input.Focus()
		f.fields[i].Input.CursorEnd()
	}
}

func (f *Form) values() FormResult {
	vals := make(map[string]string, len(f.fields))
	for i := range f.fields {
		if f.fields[i].Multiline {
			vals[f.fields[i].Key] = f.fields[i].Area.Value()
		} else {
			vals[f.fields[i].Key] = f.fields[i].Input.Value()
		}
	}
	return FormResult{Values: vals}
}

func (f *Form) HandleKey(msg tea.KeyPressMsg) Action {
	cur := &f.fields[f.focus]
	switch msg.String() {
	case "esc":
		return Action{Close: true}
	case "ctrl+s":
		return Action{Close: true, Selected: f.values()}
	case "tab":
		f.setFocus(f.focus + 1)
		return Action{}
	case "shift+tab":
		f.setFocus(f.focus - 1)
		return Action{}
	case "enter":
		if !cur.Multiline {
			if f.focus == len(f.fields)-1 {
				return Action{Close: true, Selected: f.values()}
			}
			f.setFocus(f.focus + 1)
			return Action{}
		}
		// Multiline: enter inserts a newline — fall through to the field.
	}

	var cmd tea.Cmd
	if cur.Multiline {
		cur.Area, cmd = cur.Area.Update(msg)
	} else {
		cur.Input, cmd = cur.Input.Update(msg)
	}
	return Action{Cmd: cmd}
}

func (f *Form) View(_, _ int) string {
	s := f.styles
	var rows []string
	for i := range f.fields {
		fld := &f.fields[i]
		label := s.Form.Label.Render(fld.Label)
		if i == f.focus {
			label = s.List.SelBar.Render("▎") + " " + label
		} else {
			label = "  " + label
		}
		rows = append(rows, label)
		if fld.Multiline {
			rows = append(rows, fld.Area.View())
		} else {
			rows = append(rows, fld.Input.View())
		}
		rows = append(rows, "")
	}
	rows = append(rows, s.Form.Hint.Render("ctrl+s save · tab next field · esc cancel"))
	return strings.Join(rows, "\n")
}
