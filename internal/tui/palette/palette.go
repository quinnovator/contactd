// Package palette is the list of commands shown while one is being typed:
// which commands the word after the slash could mean, which of them is
// chosen, and the rows that show them between the transcript and the status
// bar.
//
// It reads no key. The chat's model tells it the draft after every change,
// moves the selection, and asks for the rows. Nothing here does I/O or reads
// a clock.
package palette

import (
	"strings"
	"unicode"

	"github.com/quinnovator/contactd/internal/tui/commands"
)

// Model is the palette of one chat. The zero value is a closed palette; it is
// not safe for concurrent use.
type Model struct {
	// reg, rev and word are what matches was filtered from. A view of
	// another registry, or of the same one after it changed, is stale.
	reg  *commands.Registry
	rev  uint64
	word string
	// typed is the word after the slash as it was typed, for the title.
	typed string
	// matches is what the word could mean, and total how many commands there
	// are to choose from.
	matches []commands.Spec
	total   int
	// sel is the chosen match. top is the first match drawn: the list
	// scrolls when there are more matches than rows.
	sel, top int
	// open says the palette is shown. dismissed says esc closed it and the
	// draft has been a command ever since.
	open, dismissed bool
}

// New returns a closed palette.
func New() *Model { return &Model{} }

// Set tells the palette what the draft is now, and reports whether the
// palette is open. It is open while the draft is one line that starts with
// one slash and has no white space yet, unless it was dismissed. It is closed
// when reg is nil.
//
// The list is filtered again only when the word after the slash changed, in
// more than its case, or the registry did. When only the registry changed
// the chosen command stays chosen if it still matches. A new word starts at
// the command it names in full, since that is the one enter runs, or at the
// first match when it names none; so does a palette that opens again.
func (m *Model) Set(reg *commands.Registry, draft string) (open bool) {
	line := commands.Parse(draft)
	command := line.Kind == commands.LineCommand
	if !command {
		m.dismissed = false
	}
	// Parse drops trailing white space, so "/st " reads the same as "/st".
	// Whether the name is still being typed is asked of the draft as it is.
	if reg == nil || m.dismissed || !command || strings.IndexFunc(draft, unicode.IsSpace) >= 0 {
		m.open = false
		return false
	}
	if reg != m.reg || reg.Rev() != m.rev || line.Name != m.word {
		m.filter(reg, line.Name, m.open && reg == m.reg && line.Name == m.word)
	} else if !m.open {
		m.sel, m.top = m.start(), 0
	}
	m.open = true
	m.typed = draft[1:]
	return true
}

// filter reads the matches for a word from the registry. With keep, the
// command that was chosen stays chosen when it is still among them.
func (m *Model) filter(reg *commands.Registry, word string, keep bool) {
	chosen := ""
	if keep && m.sel < len(m.matches) {
		chosen = m.matches[m.sel].Name
	}
	m.matches = reg.Match(word)
	m.total = reg.Len()
	m.reg, m.rev, m.word = reg, reg.Rev(), word
	for i := range m.matches {
		if chosen != "" && m.matches[i].Name == chosen {
			m.sel = i
			return
		}
	}
	m.sel, m.top = m.start(), 0
}

// start returns where the selection of a new list stands: on the command
// whose whole name is the typed word, when there is one, and on the first
// match otherwise. "/status" lists /statusline first, and enter runs /status.
func (m *Model) start() int {
	for i := range m.matches {
		if m.matches[i].Name == m.word {
			return i
		}
	}
	return 0
}

// Dismiss closes an open palette, as esc does. It stays closed until the
// draft stops being a command: until it is emptied, sent, or turned into a
// message. Dismissing a palette that is not open does nothing.
func (m *Model) Dismiss() {
	if m.open {
		m.open, m.dismissed = false, true
	}
}

// Open reports whether the palette is shown.
func (m *Model) Open() bool { return m.open }

// Move moves the selection by delta rows: negative is up. It stops at the
// first and at the last match and does not wrap around.
func (m *Model) Move(delta int) {
	last := len(m.matches) - 1
	if !m.open || last < 0 {
		return
	}
	// The step is cut to the length of the list first, so that no step, however
	// large, can overflow.
	m.sel = min(max(m.sel+min(max(delta, -last), last), 0), last)
}

// Selected returns the chosen command. ok is false when the palette is closed
// and when nothing matches.
func (m *Model) Selected() (spec commands.Spec, ok bool) {
	if !m.open || len(m.matches) == 0 {
		return commands.Spec{}, false
	}
	return m.matches[m.sel], true
}

// Want returns how many rows the palette asks for at a terminal width, its
// box included, and 0 when it is closed. That is one row for each match, up
// to 8, and from 40 columns four more: the box's two edges, its title and the
// row of keys. When nothing matches, one row says so.
func (m *Model) Want(width int) int {
	if !m.open {
		return 0
	}
	n := min(max(len(m.matches), 1), maxRows)
	if width < boxFrom {
		return n
	}
	return n + chrome
}

// window returns the first match to draw in n rows. The list moves only as
// far as it must to keep the selection in view, and never past its end.
func (m *Model) window(n int) int {
	m.top = min(m.top, m.sel)
	m.top = max(m.top, m.sel-n+1)
	m.top = max(min(m.top, len(m.matches)-n), 0)
	return m.top
}
