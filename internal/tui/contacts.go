package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/inputbox"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// The list of contacts and its two forms: a new contact, and a new name for
// one. Every row is built for the width of the terminal, and on a short
// terminal the rows that matter least give way first.

const (
	// nameLimit is the most characters a contact's name may have.
	nameLimit = 64
	// The widest a contact's label and its route are drawn in the list.
	labelColumn = 24
	routeColumn = 34
	// systemRows is the most rows the system prompt's box shows.
	systemRows = 12
)

// How soon a row of the list or of a form gives way on a short terminal: the
// highest first, and keep never.
const (
	keep = iota
	dropOtherBox
	dropNote
	dropLabel
	dropTitle
	dropHints
	dropTip
	dropBlank
)

// line is one row of the list or of a form.
type line struct {
	text string
	// drop says when the row gives way to a short terminal: see keep.
	drop int
	// box marks the first row of the box being typed in, which the cursor's
	// row is counted from. The box's rows are already as wide as the
	// terminal.
	box bool
	// boxed marks every row of an input box.
	boxed bool
	// slot stands for the rows of the list, which take what room is left.
	slot bool
}

// fit drops rows until no more than height are left: all the rows that give
// way first, then the next, and never one marked keep. If the rows that
// stay are still too many, the last ones are cut.
func fit(lines []line, height int) []line {
	lines = slices.Clone(lines)
	for len(lines) > height {
		first := keep
		for _, l := range lines {
			first = max(first, l.drop)
		}
		if first == keep {
			break
		}
		lines = slices.DeleteFunc(lines, func(l line) bool { return l.drop == first })
	}
	return lines[:max(min(len(lines), height), 0)]
}

// clean makes text from the server safe to draw on this terminal.
func clean(th *theme.Theme, s string) string {
	return safe.Clean(s, th.Caps.Width == termcaps.WidthWc)
}

// hintLine joins as many of the hints as fit in width, the first ones first,
// and draws them dim.
func hintLine(th *theme.Theme, hints []string, width int) string {
	sep := " " + th.G.Mid + " "
	var out string
	for _, h := range hints {
		next := h
		if out != "" {
			next = out + sep + h
		}
		if th.Width(next) > width {
			break
		}
		out = next
	}
	return th.Style("dim").Render(out)
}

// wrapped draws text over as many rows as it needs at this width, in a tone.
func wrapped(th *theme.Theme, text, tone string, width, drop int) []line {
	rows := th.Wrap(clean(th, text), max(width, 1))
	out := make([]line, 0, len(rows))
	for _, row := range rows {
		out = append(out, line{text: th.Style(tone).Render(row), drop: drop})
	}
	return out
}

// newLineKey names the key that starts a new line in a box, as the hint
// line of the conversation does.
func newLineKey(th *theme.Theme) string {
	if th.Caps.ShiftEnter {
		return "shift+enter"
	}
	return "ctrl+j"
}

func (m *model) contactsKey(msg tea.KeyPressMsg) tea.Cmd {
	m.note, m.stale = "", true
	switch msg.String() {
	case "ctrl+c", "q", "esc":
		return tea.Quit
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, max(len(m.contacts)-1, 0))
	case "enter":
		if m.cursor < len(m.contacts) {
			return m.open(m.contacts[m.cursor])
		}
	case "n":
		m.newForm()
	case "r":
		if m.cursor < len(m.contacts) {
			m.renameForm(m.contacts[m.cursor])
		}
	case "g":
		return m.showContacts()
	case "ctrl+l":
		return m.clear()
	}
	return nil
}

// newForm shows the new-contact form, empty.
func (m *model) newForm() {
	m.screen, m.field, m.stale = screenNew, 0, true
	m.name.Reset()
	m.system.Reset()
}

// renameForm shows the rename prompt for a contact, holding its name.
func (m *model) renameForm(c client.Contact) {
	m.screen, m.target, m.stale = screenRename, c, true
	m.name.SetValue(c.Name)
	m.oneLineName()
}

// leaveForm goes back to where the form was asked for: the conversation,
// when a rename was asked for there and it is still open, and the list
// otherwise.
func (m *model) leaveForm() tea.Cmd {
	if m.screen == screenRename && m.chat != nil {
		m.screen = screenChat
		return nil
	}
	return m.showContacts()
}

func (m *model) formKey(msg tea.KeyPressMsg) tea.Cmd {
	m.note, m.stale = "", true
	// While the server is asked to create or rename, the form waits for its
	// answer. Leaving does not: the server may take its time, or never answer.
	if msg.String() == "ctrl+c" {
		return tea.Quit
	}
	if m.busy {
		return nil
	}
	onName := m.screen == screenRename || m.field == 0
	switch msg.String() {
	case "esc":
		return m.leaveForm()
	case "ctrl+l":
		return m.clear()
	case "tab", "shift+tab":
		if m.screen == screenNew {
			m.field = 1 - m.field
		}
		return nil
	case "ctrl+j", "alt+enter", "shift+enter":
		if onName {
			return nil // a name is one line
		}
	case "enter":
		name := strings.TrimSpace(m.name.Value())
		if m.screen == screenRename {
			m.busy = true
			return m.rename(m.target.ID, name)
		}
		if m.field == 0 {
			m.field = 1
			return nil
		}
		// Enter moves on, as it does in the chat, so a new line needs
		// another key, or a backslash before this one.
		system, done := m.system.Enter()
		switch system = strings.TrimSpace(system); {
		case !done:
		case system == "":
			m.note = "say what this contact is: a system prompt"
		default:
			m.busy = true
			return m.create(client.NewContact{Name: name, Agent: core.AgentConfig{System: system}})
		}
		return nil
	}
	return m.toInput(msg)
}

// nameInput hands a message to the box a name is typed in, and keeps what
// it holds one line of at most nameLimit characters.
func (m *model) nameInput(msg tea.Msg) tea.Cmd {
	cmd, _ := m.name.Update(msg)
	m.oneLineName()
	return cmd
}

// oneLineName makes what the name's box holds one line of at most nameLimit
// characters, whether it was typed, pasted, or is the name the server has.
func (m *model) oneLineName() {
	m.stale = true
	v := m.name.Value()
	one := strings.ReplaceAll(v, "\n", " ")
	if utf8.RuneCountInString(one) > nameLimit {
		one = string([]rune(one)[:nameLimit])
	}
	if one != v {
		m.name.SetValue(one)
	}
}

// contactsView draws the list. The cursor's row is kept on screen.
func (m *model) contactsView() []line {
	th, dim := m.th, m.th.Style("dim")
	rows := []line{
		{text: th.Spread(th.Title().Render("Contacts"), dim.Render(clean(th, m.opt.Backend.Where())), m.width), drop: dropTitle},
		{drop: dropBlank},
		{slot: true},
		{drop: dropBlank},
	}
	if m.note != "" {
		rows = append(rows, wrapped(th, m.note, "warn", m.width, dropNote)...)
	}
	rows = append(rows, line{
		text: hintLine(th, []string{"enter open", "n new", "r rename", "g reload", "q quit"}, m.width),
		drop: dropHints,
	})
	rows = fit(rows, m.height)
	at := slices.IndexFunc(rows, func(l line) bool { return l.slot })
	if at < 0 {
		return rows
	}
	return slices.Replace(rows, at, at+1, m.contactRows(max(m.height-len(rows)+1, 1))...)
}

// contactRows draws at most room rows of the list, scrolled so that the
// cursor is among them: a contact's label, then its route, then how long
// ago it was last active. The route and the time are left out where the
// terminal is too narrow for them.
func (m *model) contactRows(room int) []line {
	th, dim := m.th, m.th.Style("dim")
	switch {
	case len(m.contacts) == 0 && m.loading:
		return []line{{text: dim.Render("  loading" + th.G.Ellipsis)}}
	case len(m.contacts) == 0 && m.note == "":
		return []line{{text: dim.Render("  No contacts yet. Press n to make one.")}}
	}
	now := m.opt.Now()
	type row struct{ label, route, ago string }
	all := make([]row, len(m.contacts))
	var labelW, routeW, agoW int
	for i := range m.contacts {
		c := &m.contacts[i]
		r := row{label: clean(th, c.Label()), route: clean(th, c.Provider+"/"+c.Model), ago: ago(now, c.LastActive)}
		labelW, routeW, agoW = max(labelW, th.Width(r.label)), max(routeW, th.Width(r.route)), max(agoW, th.Width(r.ago))
		all[i] = r
	}
	mark := th.G.Pointer + " "
	markW := th.Width(mark)
	// The label's column is never wider than the row: on a terminal narrower
	// than the column a long label is cut, and a short one is not cut after
	// the spaces that would have filled the column.
	labelW, routeW = min(labelW, labelColumn, max(m.width-markW, 1)), min(routeW, routeColumn)
	withRoute := markW+labelW+2+routeW <= m.width
	withAgo := withRoute && agoW > 0 && markW+labelW+2+routeW+2+agoW <= m.width

	first := max(min(m.cursor-room/2, len(all)-room), 0)
	out := make([]line, 0, room)
	for i := first; i < len(all) && i < first+room; i++ {
		r, selected := all[i], i == m.cursor
		var b strings.Builder
		label := th.Pad(r.label, labelW)
		if selected {
			b.WriteString(mark + th.Style("accent").Render(label))
		} else {
			b.WriteString(strings.Repeat(" ", markW) + label)
		}
		if withRoute {
			b.WriteString("  " + dim.Render(th.Pad(r.route, routeW)))
		}
		if withAgo {
			b.WriteString("  " + dim.Render(r.ago))
		}
		text := b.String()
		if selected {
			text = th.Selected(text, m.width)
		}
		out = append(out, line{text: text})
	}
	return out
}

// newView draws the new-contact form, and returns the box being typed in.
// A long system prompt has the rows the terminal can spare for it: all but
// the rows that say what the form is and what its keys are. The blank rows
// and the tip give way to it; they are there for a short prompt.
func (m *model) newView() ([]line, *inputbox.Model) {
	rows, _ := m.newRows(1)
	others := -1 // the rows that stay, without the prompt's own
	for _, l := range rows {
		if l.drop < dropTip {
			others++
		}
	}
	return m.newRows(min(max(m.height-others, 1), systemRows))
}

// newRows is the new-contact form with at most most rows of the system
// prompt shown.
func (m *model) newRows(most int) ([]line, *inputbox.Model) {
	th, dim := m.th, m.th.Style("dim")
	m.name.SetSize(m.width, 1, false)
	m.system.SetSize(m.width, most, false)
	focus := m.name
	if m.field == 1 {
		focus = m.system
	}
	rows := []line{
		{text: th.Title().Render("New contact"), drop: dropTitle},
		{drop: dropBlank},
		{text: "Name " + dim.Render("(optional; what you will call it)"), drop: dropLabel},
	}
	rows = append(rows, boxRows(m.name, focus == m.name)...)
	rows = append(rows,
		line{drop: dropBlank},
		line{text: "System prompt " + dim.Render("(who this contact is)"), drop: dropLabel},
	)
	rows = append(rows, boxRows(m.system, focus == m.system)...)
	rows = append(rows, line{drop: dropBlank})
	if m.note != "" {
		rows = append(rows, wrapped(th, m.note, "warn", m.width, dropNote)...)
	}
	if m.busy {
		rows = append(rows, line{text: dim.Render("creating" + th.G.Ellipsis), drop: dropNote})
	}
	rows = append(rows, line{
		text: hintLine(th, []string{"tab switch field", "enter next / create", newLineKey(th) + " new line", "esc cancel"}, m.width),
		drop: dropHints,
	})
	tip := "Tools, model and effort are set with: contactd chat -new NAME -system ... -tools python"
	return append(rows, wrapped(th, tip, "dim", m.width, dropTip)...), focus
}

// renameView draws the rename prompt, and returns its box.
func (m *model) renameView() ([]line, *inputbox.Model) {
	th := m.th
	m.name.SetSize(m.width, 1, false)
	rows := []line{
		{text: th.Title().Render("Rename " + clean(th, m.target.Label())), drop: dropTitle},
		{drop: dropBlank},
	}
	rows = append(rows, boxRows(m.name, true)...)
	rows = append(rows, line{drop: dropBlank})
	if m.note != "" {
		rows = append(rows, wrapped(th, m.note, "warn", m.width, dropNote)...)
	}
	return append(rows, line{
		text: hintLine(th, []string{"enter rename", "esc cancel", "an empty name removes it"}, m.width),
		drop: dropHints,
	}), m.name
}

// boxRows returns the rows of an input box. The one being typed in never
// gives way; the other goes last.
func boxRows(box *inputbox.Model, focused bool) []line {
	drop := dropOtherBox
	if focused {
		drop = keep
	}
	rows := box.Lines()
	out := make([]line, 0, len(rows))
	for i, row := range rows {
		out = append(out, line{text: row, drop: drop, boxed: true, box: focused && i == 0})
	}
	return out
}

// listView makes the screen of the list or of a form from its rows: no more
// of them than the terminal has, none wider than it, and the cursor in the
// box being typed in.
func (m *model) listView(lines []line, box *inputbox.Model) tea.View {
	lines = fit(lines, m.height)
	rows := make([]string, len(lines))
	var cursor *tea.Cursor
	for i, l := range lines {
		rows[i] = l.text
		if !l.boxed {
			rows[i] = m.th.Truncate(l.text, m.width)
		}
		if l.box && box != nil {
			x, y := box.Cursor()
			if i+y < len(lines) {
				cursor = tea.NewCursor(x, i+y)
				cursor.Blink = false
			}
		}
	}
	v := tea.NewView(strings.Join(rows, "\n"))
	v.AltScreen = true
	v.WindowTitle = "contactd"
	v.Cursor = cursor
	return v
}

// ago says how long before now a moment was, the way a contact list does.
func ago(now time.Time, ts int64) string {
	if ts == 0 {
		return ""
	}
	d := now.Sub(time.UnixMilli(ts))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return time.UnixMilli(ts).In(now.Location()).Format("2 Jan 2006")
	}
}
