package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"windshift/internal/tui/styles"
)

// WorkItemEditForm backs the WorkItemDetailScreen. Title uses a real
// textinput; description uses a textarea; status and priority open dialogs.
type WorkItemEditForm struct {
	titleInput          textinput.Model
	descriptionTextarea textarea.Model
	statusID            *int
	statusName          string
	statusColor         string
	priorityID          *int
	priorityName        string
	priorityColor       string
	currentField        int // 0=title, 1=description, 2=status, 3=priority
	editing             bool
}

// CreateWorkItemForm backs the CreateWorkItemScreen. Single-line description
// to match existing UX (textarea felt heavy for a quick-create flow).
type CreateWorkItemForm struct {
	titleInput    textinput.Model
	descInput     textinput.Model
	priorityID    *int
	priorityName  string
	priorityColor string
	currentField  int // 0=title, 1=description, 2=priority
	editing       bool
}

// CommentForm backs the CommentsScreen. Single text input.
type CommentForm struct {
	input   textinput.Model
	editing bool
}

// TimeLogForm backs the TimeLoggingScreen. Four text inputs + a project picker.
type TimeLogForm struct {
	descInput      textinput.Model
	durationInput  textinput.Model
	dateInput      textinput.Model
	startTimeInput textinput.Model
	projectID      *int
	projectName    string
	currentField   int // 0=desc, 1=duration, 2=date, 3=startTime, 4=project
	editing        bool
}

// newInput builds a styled single-line textinput configured for our forms.
// We override the default prompt ("> ") to be empty since the form labels
// already announce the field.
func newInput(s *styles.Styles, placeholder string, charLimit int) textinput.Model {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = placeholder
	in.CharLimit = charLimit
	in.SetStyles(inputStyles(s))
	return in
}

// inputStyles configures textinput.Styles for both focused and blurred
// states. The cursor color is brand primary; focused text gets a soft
// surface-hovered background to make the active field obvious.
func inputStyles(s *styles.Styles) textinput.Styles {
	style := textinput.Styles{}
	style.Focused.Text = lipgloss.NewStyle().Foreground(s.Palette.FgBase)
	style.Focused.Placeholder = lipgloss.NewStyle().Foreground(s.Palette.FgMuted)
	style.Focused.Suggestion = lipgloss.NewStyle().Foreground(s.Palette.FgMuted)
	style.Focused.Prompt = lipgloss.NewStyle()

	style.Blurred.Text = lipgloss.NewStyle().Foreground(s.Palette.FgBase)
	style.Blurred.Placeholder = lipgloss.NewStyle().Foreground(s.Palette.FgMuted)
	style.Blurred.Suggestion = lipgloss.NewStyle().Foreground(s.Palette.FgMuted)
	style.Blurred.Prompt = lipgloss.NewStyle()

	style.Cursor.Color = s.Palette.Primary
	style.Cursor.Shape = tea.CursorBlock
	style.Cursor.Blink = true
	return style
}

// renderInput wraps a textinput's view in the form input frame. When the
// field is "selected but not editing" we still show the focused background
// so the user knows which row is active; the cursor disappears because
// textinput.Blur was called.
func renderInput(s *styles.Styles, in textinput.Model, selected, editing bool, width int) string {
	view := in.View()
	frame := s.Form.Input
	if editing {
		frame = s.Form.InputFocused
	} else if selected {
		frame = s.Form.InputFocused
	}
	if width > 0 {
		frame = frame.Width(width)
	}
	return frame.Render(view)
}

// resetEditForm builds a fresh WorkItemEditForm seeded from a WorkItem and
// sized to the current terminal.
func (m Model) resetEditForm(item WorkItem) WorkItemEditForm {
	title := newInput(m.styles, "Title", 200)
	title.SetValue(item.Title)
	title.SetWidth(inputWidth(m.width))

	ta := textarea.New()
	ta.SetValue(item.Description)
	w, h := textareaDimensions(m.width, m.height)
	ta.SetWidth(w)
	ta.SetHeight(h)
	ta.ShowLineNumbers = false
	ta.CharLimit = 5000
	ta.Placeholder = "Enter description…"

	return WorkItemEditForm{
		titleInput:          title,
		descriptionTextarea: ta,
		statusID:            item.StatusID,
		statusName:          item.StatusName,
		statusColor:         item.StatusCategoryColor,
		priorityID:          item.PriorityID,
		priorityName:        item.PriorityName,
		priorityColor:       item.PriorityColor,
	}
}

// resetCreateForm builds an empty CreateWorkItemForm.
func (m Model) resetCreateForm() CreateWorkItemForm {
	title := newInput(m.styles, "Title", 200)
	title.SetWidth(inputWidth(m.width))
	desc := newInput(m.styles, "Description (optional)", 1000)
	desc.SetWidth(inputWidth(m.width))
	return CreateWorkItemForm{
		titleInput: title,
		descInput:  desc,
	}
}

// resetCommentForm builds an empty CommentForm.
func (m Model) resetCommentForm() CommentForm {
	in := newInput(m.styles, "Write a comment…", 2000)
	in.SetWidth(inputWidth(m.width))
	return CommentForm{input: in}
}

// resetTimeLogForm builds an empty TimeLogForm with date/startTime pre-filled.
func (m Model) resetTimeLogForm(date, startTime string) TimeLogForm {
	desc := newInput(m.styles, "What did you work on?", 500)
	desc.SetWidth(inputWidth(m.width))
	dur := newInput(m.styles, "e.g. 1h30m", 20)
	dur.SetWidth(inputWidth(m.width))
	d := newInput(m.styles, "YYYY-MM-DD", 10)
	d.SetValue(date)
	d.SetWidth(inputWidth(m.width))
	t := newInput(m.styles, "HH:MM", 5)
	t.SetValue(startTime)
	t.SetWidth(inputWidth(m.width))
	return TimeLogForm{
		descInput:      desc,
		durationInput:  dur,
		dateInput:      d,
		startTimeInput: t,
	}
}

// inputWidth picks a reasonable form-field width given the terminal width.
func inputWidth(winW int) int {
	w := winW - 8
	if w < 30 {
		w = 30
	}
	if w > 80 {
		w = 80
	}
	return w
}

// chipForStatus renders an API-colored chip for a status name. Falls back to
// muted gray when the API gave us no color.
func (m Model) chipForStatus(name, hex string) string {
	if name == "" {
		return m.styles.Base.Hint.Render("(not set)")
	}
	bg := hex
	if bg == "" {
		bg = "#5e6c84"
	}
	return m.styles.Chip.Base.
		Background(lipgloss.Color(bg)).
		Foreground(m.styles.Palette.OnPrimary).
		Render(strings.ToUpper(name))
}

// chipForPriority is the same as chipForStatus but uses the priority casing
// and a slightly different fallback color (matches the existing UI).
func (m Model) chipForPriority(name, hex string) string {
	if name == "" {
		return m.styles.Base.Hint.Render("(not set)")
	}
	bg := hex
	if bg == "" {
		bg = "#5e6c84"
	}
	return m.styles.Chip.Base.
		Background(lipgloss.Color(bg)).
		Foreground(m.styles.Palette.OnPrimary).
		Render(name)
}

// chipForLegacyStatus colors a free-text status when the work item has no
// ID-based status. Kept for back-compat with older payloads.
func (m Model) chipForLegacyStatus(status string) string {
	hex := "#5e6c84"
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
	return m.styles.Chip.Base.
		Background(lipgloss.Color(hex)).
		Foreground(m.styles.Palette.OnPrimary).
		Render(strings.ToUpper(status))
}
