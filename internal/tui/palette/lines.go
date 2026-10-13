package palette

import (
	"strconv"
	"strings"

	"github.com/quinnovator/contactd/internal/tui/commands"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

const (
	// maxRows is the most commands listed at once.
	maxRows = 8
	// boxFrom is the narrowest terminal that gets the box and the summaries.
	// sourceFrom is the narrowest that also gets the source column.
	boxFrom, sourceFrom = 40, 60
	// chrome is the rows a boxed palette has besides its commands: the two
	// edges, the title and the keys.
	chrome = 4
	// minBoxed is the fewest commands a box is drawn around, unless fewer
	// than that match. With less room the rows go to commands, not to a box.
	minBoxed = 3
	// nameCells is the width of the name column, the pointer included, and
	// sourceCells the width of the source column.
	nameCells, sourceCells = 20, 16
	// maxRunes cuts text from outside before it is measured. Every row is
	// cut to its column after that; this only bounds the work.
	maxRunes = 256
)

// The tones the palette draws in. They are theme.Style's names.
const (
	toneDim    = "dim"
	toneAccent = "accent"
	toneMod    = "mod"
)

// Lines draws the palette in the rows it was given: exactly rows lines, each
// exactly width cells. It returns nil when the palette is closed and when
// rows is under 1.
//
// From 40 columns the commands are in a box with a title, the count of
// matches and a row of keys, and each row is a name, a summary and, from 60
// columns, where the command comes from. Under 40 columns a row is the name
// alone and there is no box.
//
// Given fewer rows than Want asks for, the palette lists fewer commands, and
// the one chosen is always among them. When the rows do not hold the box and
// three commands, or all of them where fewer match, the box, the title and
// the keys go, and every row is a command. Rows the palette has no use for
// are left blank, above it, so that it stays against the status bar.
//
// With more matches than rows the list scrolls to keep the selection in view.
// Lines remembers where the list stands, so it moves only when it must.
func (m *Model) Lines(th *theme.Theme, width, rows int) []string {
	if !m.open || rows <= 0 {
		return nil
	}
	// n is how many commands are listed. One row says that nothing matches.
	n := min(max(len(m.matches), 1), maxRows)
	boxed := width >= boxFrom && rows-chrome >= min(n, minBoxed)
	n = min(n, rows)
	used := n
	if boxed {
		n = min(n, rows-chrome)
		used = n + chrome
	}
	out := make([]string, 0, rows)
	for range rows - used {
		out = append(out, th.Pad("", width))
	}
	if !boxed {
		return append(out, m.list(th, width, n, width >= boxFrom, width >= sourceFrom)...)
	}
	// What Box leaves inside: a border and a cell of space at each side.
	inner := width - 2*th.Width(th.G.BoxV) - 2
	body := m.list(th, inner, n, true, width >= sourceFrom)
	body = append(body, tint(th, toneDim, m.keys(th, inner)))
	title, count := m.heading(th, inner)
	return append(out, th.Box(body, width, title, count)...)
}

// heading returns the box's title and count: what was typed, and how many of
// all the commands match it. The title is cut so that the count stays.
func (m *Model) heading(th *theme.Theme, inner int) (title, count string) {
	count = strconv.Itoa(len(m.matches)) + " of " + strconv.Itoa(m.total)
	title = "commands"
	if m.typed != "" {
		title += " matching /" + safe.Line(m.typed, maxRunes, ascii(th))
	}
	return th.Truncate(title, max(inner-th.Width(count)-2, 1)), count
}

// keys is the row of keys under the list, as many of them as fit in w cells.
// When nothing matches there is nothing to choose, complete or run.
func (m *Model) keys(th *theme.Theme, w int) string {
	hints := []string{"esc close"}
	if len(m.matches) > 0 {
		hints = []string{th.G.UpDown + " choose", "tab complete", "enter run", "esc close"}
	}
	line := ""
	for _, hint := range hints {
		next := hint
		if line != "" {
			next = line + " " + th.G.Mid + " " + hint
		}
		if th.Width(next) > w {
			break
		}
		line = next
	}
	return line
}

// list draws the n commands in view, each in w cells, or the one row that
// says nothing matches. n is at least 1 and no more than there is to list.
func (m *Model) list(th *theme.Theme, w, n int, summary, source bool) []string {
	if len(m.matches) == 0 {
		none := "no command matches"
		if summary {
			none += "; start with // to send it"
		}
		return []string{th.Pad(tint(th, toneDim, th.Truncate(none, w)), w)}
	}
	out := make([]string, 0, n)
	first := m.window(n)
	for i := first; i < first+n; i++ {
		out = append(out, row(th, m.matches[i], i == m.sel, w, summary, source))
	}
	return out
}

// row draws one command in exactly w cells: its name, then its summary, then
// its source at the right edge. The selected row has the pointer, its name in
// the accent tone, which is bold, its summary as body text, and the fill of a
// selected row.
//
// The name is the registry's and is plain ASCII. The summary and the source
// may be a mod's words, so they are made safe for the screen here.
func row(th *theme.Theme, s commands.Spec, selected bool, w int, summary, source bool) string {
	finish := th.Pad
	lead := strings.Repeat(" ", th.Width(th.G.Pointer)+1)
	nameStyle, textTone := th.Style(""), toneDim
	if selected {
		finish = th.Selected
		lead = th.G.Pointer + " "
		nameStyle, textTone = th.Style(toneAccent), ""
	}
	name := lead + "/" + s.Name
	if !summary {
		return finish(nameStyle.Render(th.Truncate(name, w)), w)
	}

	var b strings.Builder
	// A name that fills its column is cut one cell short, so that it never
	// runs into the summary.
	b.WriteString(th.Pad(nameStyle.Render(th.Truncate(name, nameCells-1)), nameCells))
	room := w - nameCells
	if source {
		room -= sourceCells
	}
	limit := room
	if source {
		// A cell is kept clear between the summary and the source.
		limit--
	}
	text := th.Truncate(safe.Line(s.Summary, maxRunes, ascii(th)), limit)
	b.WriteString(th.Pad(tint(th, textTone, text), room))
	if source {
		tone := toneMod
		if s.Source == commands.SourceCore {
			tone = toneDim
		}
		from := th.Truncate(safe.Line(s.Source, maxRunes, ascii(th)), sourceCells)
		b.WriteString(strings.Repeat(" ", max(sourceCells-th.Width(from), 0)))
		b.WriteString(tint(th, tone, from))
	}
	return finish(b.String(), w)
}

// tint draws s in a tone. Nothing drawn is nothing: not a pair of escape
// sequences around no text.
func tint(th *theme.Theme, tone, s string) string {
	if s == "" {
		return ""
	}
	return th.Style(tone).Render(s)
}

// ascii reports whether the theme draws with ASCII glyphs only, in which case
// text from outside is cut down to ASCII too. A Caps that was never filled in
// is drawn that way.
func ascii(th *theme.Theme) bool { return th.Caps.Glyphs != termcaps.GlyphsUnicode }
