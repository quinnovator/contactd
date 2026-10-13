package sledit

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/tui/statusline"
)

// maxInput is the most characters a field's input takes: the longest value
// any key of the file may have.
const maxInput = 2048

// field is one setting of a segment that the editor changes.
type field uint8

const (
	fieldName field = iota + 1
	fieldCommand
	fieldMatch
	fieldFormat
	fieldShort
	fieldWarn
	fieldDrop
	fieldInterval
	fieldTimeout
)

// fieldsOf lists the fields of a segment in the order they are edited. A
// shell segment's name comes first, so that a new one is named first.
func fieldsOf(k kind) []field {
	switch k {
	case kindShell:
		return []field{
			fieldName, fieldCommand, fieldMatch, fieldFormat, fieldShort,
			fieldWarn, fieldDrop, fieldInterval, fieldTimeout,
		}
	case kindCore, kindMod:
		return []field{fieldFormat, fieldShort, fieldWarn, fieldDrop}
	}
	return nil
}

// label is the field's name beside its input.
func (f field) label() string {
	switch f {
	case fieldName:
		return "name"
	case fieldCommand:
		return "command"
	case fieldMatch:
		return "match"
	case fieldFormat:
		return "format"
	case fieldShort:
		return "short"
	case fieldWarn:
		return "warn below"
	case fieldDrop:
		return "drop"
	case fieldInterval:
		return "interval"
	case fieldTimeout:
		return "timeout"
	}
	return ""
}

// help says what the field takes, for the row under its input.
func (f field) help(seg statusline.Segment, vars string) string {
	switch f {
	case fieldName:
		return "a short name with no space in it"
	case fieldCommand:
		return "run with /bin/sh -c; $out is its first line"
	case fieldMatch:
		return "an optional expression; its groups are $1 to $9"
	case fieldFormat:
		if vars == "" {
			return "what it shows; empty for the default"
		}
		return "what it shows, with " + vars + "; empty for the default"
	case fieldShort:
		if kindOf(seg) == kindShell {
			return "what it shows on a narrow bar; empty for none"
		}
		return "what it shows on a narrow bar; empty for the default"
	case fieldWarn:
		return "a number: the warn tone below it; empty for none"
	case fieldDrop:
		return "its turn to give way: 1 goes first, 0 never"
	case fieldInterval:
		return "how often it runs, like 10s; at least 1s"
	case fieldTimeout:
		return "how long a run may take; at most the interval"
	}
	return ""
}

// get returns the field's value as its input shows it.
func (f field) get(seg statusline.Segment) string {
	switch f {
	case fieldName:
		return seg.Name
	case fieldCommand:
		return seg.Command
	case fieldMatch:
		return seg.Match
	case fieldFormat:
		return seg.Format
	case fieldShort:
		return seg.Short
	case fieldWarn:
		if seg.HasWarn {
			return strconv.Itoa(seg.WarnBelow)
		}
	case fieldDrop:
		return strconv.Itoa(seg.Drop)
	case fieldInterval:
		return seg.Interval.String()
	case fieldTimeout:
		return seg.Timeout.String()
	}
	return ""
}

// set returns the segment with the field changed to what was typed, or why
// the text is not a value for it.
func (f field) set(seg statusline.Segment, text string) (statusline.Segment, string) {
	trimmed := strings.TrimSpace(text)
	switch f {
	case fieldName:
		seg.Name = trimmed
	case fieldCommand:
		seg.Command = trimmed
	case fieldMatch:
		seg.Match = text
	case fieldFormat:
		seg.Format = text
	case fieldShort:
		seg.Short = text
	case fieldWarn:
		seg.WarnBelow, seg.HasWarn = 0, false
		if trimmed == "" {
			break
		}
		n, err := strconv.Atoi(trimmed)
		if err != nil {
			return seg, "warn below takes a whole number, or nothing for no warning"
		}
		seg.WarnBelow, seg.HasWarn = n, true
	case fieldDrop:
		n, err := strconv.Atoi(trimmed)
		if err != nil {
			return seg, "drop takes a whole number: 1 goes first, 0 never"
		}
		seg.Drop = n
	case fieldInterval:
		d, ok := duration(trimmed)
		if !ok {
			return seg, "interval takes a time like 10s or 1m30s"
		}
		seg.Interval = d
		// A run may not take longer than the wait between two runs, so
		// a shorter interval takes the timeout down with it.
		seg.Timeout = min(seg.Timeout, d)
	case fieldTimeout:
		d, ok := duration(trimmed)
		if !ok {
			return seg, "timeout takes a time like 1s or 500ms"
		}
		seg.Timeout = d
	}
	return seg, ""
}

// duration reads a time as Go writes one, "10s" or "1m30s". A bare number is
// taken as seconds.
func duration(s string) (time.Duration, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 || n > int(time.Duration(1<<62)/time.Second) {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	d, err := time.ParseDuration(s)
	return d, err == nil
}

// editing is the field being edited.
type editing struct {
	input  textinput.Model
	fields []field
	at     int
	// initial is the field's value as the input first showed it. Text that
	// was not changed is not written back: the input drops characters it
	// cannot show, and a field that was only looked at must stay as it was.
	initial string
	// off is the first character of the value on screen, when the value
	// is longer than its row.
	off int
	// added says the segment was added just now and no field of it was
	// accepted yet, so that esc takes it out again. back is the selection
	// to return to then.
	added bool
	back  int
	// problem says why the text was refused.
	problem string
}

// field returns the field being edited.
func (e *editing) field() field { return e.fields[e.at] }

// edit opens the fields of the selected segment. A shell segment opens at
// its command; its name is one field up.
func (m *Model) edit() {
	seg := m.selected()
	if seg == nil {
		return
	}
	m.open(max(slices.Index(fieldsOf(kindOf(*seg)), fieldCommand), 0))
}

// open starts editing the selected segment at one of its fields.
func (m *Model) open(at int) {
	input := textinput.New()
	input.Prompt = ""
	input.CharLimit = maxInput
	// The screen draws the value and the terminal's own cursor itself.
	input.SetVirtualCursor(false)
	// ctrl+v would read the system clipboard. A paste from the terminal
	// arrives as a message and is taken.
	input.KeyMap.Paste.SetEnabled(false)
	input.Focus()
	m.ed = &editing{input: input, fields: fieldsOf(kindOf(*m.selected())), at: at}
	m.load()
}

// load puts the current field's value in the input.
func (m *Model) load() {
	e := m.ed
	e.input.SetValue(e.field().get(*m.selected()))
	e.input.CursorEnd()
	e.initial, e.off, e.problem = e.input.Value(), 0, ""
}

// updateField handles a message while a field is edited.
func (m *Model) updateField(msg tea.Msg) (Result, tea.Cmd) {
	e := m.ed
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch keyName(key) {
		case "esc":
			m.cancel()
			return Result{}, nil
		case "enter":
			return m.step(1, true), nil
		case "down", "tab":
			return m.step(1, false), nil
		case "up", "shift+tab":
			return m.step(-1, false), nil
		case "":
			// Several characters in one press are text, whatever they
			// spell. The input takes them the way it takes a paste, and
			// not as the key they may name.
			msg = tea.PasteMsg{Content: key.Text}
		}
		e.problem = ""
	}
	var cmd tea.Cmd
	e.input, cmd = e.input.Update(msg)
	return Result{}, cmd
}

// cancel leaves the field as it was and stops editing. A segment that was
// added and never named is taken out again.
func (m *Model) cancel() {
	e := m.ed
	m.ed = nil
	if e.added {
		m.cfg.Segments = slices.Delete(m.cfg.Segments, m.sel, m.sel+1)
		m.sel = e.back
		m.clamp()
	}
}

// step accepts the field and moves to the next one, or the one before. Past
// the last field editing is over when finish is set. A value that was
// refused keeps the field open.
func (m *Model) step(by int, finish bool) Result {
	e := m.ed
	if problem := m.accept(); problem != "" {
		e.problem = problem
		return Result{Note: problem}
	}
	e.added = false
	next := e.at + by
	if next >= len(e.fields) && finish {
		m.ed = nil
		return Result{}
	}
	e.at = max(min(next, len(e.fields)-1), 0)
	m.load()
	return Result{}
}

// accept writes the input's text to the field. It returns why the text was
// refused, or "".
func (m *Model) accept() string {
	e, seg := m.ed, m.selected()
	f, text := e.field(), e.input.Value()
	if text == e.initial && f != fieldName {
		return ""
	}
	changed, problem := f.set(*seg, text)
	if problem != "" {
		return problem
	}
	if err := changed.Validate(); err != nil {
		return err.Error()
	}
	if f == fieldName {
		for i, other := range m.cfg.Segments {
			if i != m.sel && other.ID() == changed.ID() {
				return "there is a shell segment named " + changed.Name + " already"
			}
		}
	}
	*seg = changed
	return ""
}

// window returns the part of the input's value that fits in avail cells with
// the cursor inside it, and the cursor's place in cells from its start. It
// moves the first character shown only as far as the cursor needs.
func (e *editing) window(avail int, width func(string) int) (shown string, cursor int) {
	value := []rune(e.input.Value())
	pos := min(max(e.input.Position(), 0), len(value))
	e.off = max(min(e.off, pos), 0)
	// Width grows with the text, so the least offset that brings the
	// cursor into view can be searched for.
	e.off += sort.Search(pos-e.off, func(skip int) bool {
		return width(string(value[e.off+skip:pos])) <= avail
	})
	// A value that got shorter is pulled back into view, so the row is
	// never left half empty with text hidden at its left.
	if width(string(value[e.off:])) < avail {
		e.off = sort.Search(e.off, func(skip int) bool {
			return width(string(value[skip:])) <= avail
		})
	}
	rest := value[e.off:]
	fits := sort.Search(len(rest), func(n int) bool {
		return width(string(rest[:n+1])) > avail
	})
	return string(rest[:fits]), width(string(value[e.off:pos]))
}
