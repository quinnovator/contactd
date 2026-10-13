package draw

import (
	"path"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/safe"
)

// linkStart begins the sequence that switches a hyperlink on or off.
const linkStart = "\x1b]8;"

// clean makes text from the server safe to draw.
func (r *Renderer) clean(s string) string {
	return safe.Clean(s, r.joiners)
}

// name makes a short name from the server safe to draw on one line: a tool's
// name, a language, a file name.
func (r *Renderer) name(s string) string {
	return strings.TrimSpace(safe.Line(s, maxNameRunes, r.ascii))
}

// note makes one line of text about an image safe to draw. The image source
// writes it, from a file name the model chose.
func (r *Renderer) note(s string) string {
	return safe.Line(s, maxSpanRunes, r.ascii)
}

// tone draws s in a tone. Nothing drawn is nothing: not a pair of escape
// sequences around no text.
func (r *Renderer) tone(tone, s string) string {
	if tone == "" {
		return s
	}
	return paint(r.th.Style(tone), s)
}

// paint draws s in a style, and nothing where s is empty.
func paint(st lipgloss.Style, s string) string {
	if s == "" {
		return ""
	}
	return st.Render(s)
}

// fit is the last thing done to a block: a line that is still wider than the
// row is cut. By then only three things are: a tool's name longer than the
// row, a line in a row too narrow for its indent, and a picture wider than
// the box its source was given.
//
// A terminal that counts a character at a time is measured that way, but the
// line has to fit by the other count too: the two disagree about a few
// characters even once the joiners are gone (half a flag is one), and nothing
// says which of them the terminal agrees with. Such a line is cut until it
// fits both.
func (r *Renderer) fit(lines []string) []string {
	for i, line := range lines {
		if r.th.Width(line) > r.w {
			line = r.th.Truncate(line, r.w)
			lines[i] = line
		}
		if !r.joiners || plainASCII(line) {
			continue
		}
		for room := r.w - 1; room >= 0 && ansi.StringWidth(lines[i]) > r.w; room-- {
			lines[i] = r.th.Truncate(line, room)
		}
	}
	return lines
}

// marked draws text after a mark, wrapped, in one style. The lines after the
// first stand under the text, not under the mark.
func (r *Renderer) marked(mark string, st lipgloss.Style, text string) []string {
	lead := mark + " "
	pad := strings.Repeat(" ", r.th.Width(lead))
	rows := r.th.Wrap(text, r.w-len(pad))
	out := make([]string, len(rows))
	for i, row := range rows {
		if i == 0 {
			out[i] = st.Render(lead + row)
			continue
		}
		if row != "" {
			out[i] = pad + st.Render(row)
		}
	}
	return out
}

// indented draws rows inside the reply's indent, each in a tone. A blank row
// stays empty.
func (r *Renderer) indented(tone string, rows []string) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		if row != "" {
			out[i] = strings.Repeat(" ", indent) + r.tone(tone, row)
		}
	}
	return out
}

// written draws text as it was written: wrapped, inside the indent, with no
// blank line at either end.
func (r *Renderer) written(text string) []string {
	return r.indented("", trimBlank(r.th.Wrap(text, r.inner)))
}

// trimText drops the blank lines a text begins with and everything blank it
// ends with.
func trimText(s string) string {
	s = strings.TrimRight(s, " \n")
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 || strings.TrimSpace(s[:i]) != "" {
			return s
		}
		s = s[i+1:]
	}
}

// trimBlank drops the lines at both ends that draw nothing.
func trimBlank(lines []string) []string {
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// blank reports whether a line draws nothing: it holds spaces and escape
// sequences only.
func blank(line string) bool {
	if !strings.Contains(line, "\x1b") {
		return strings.TrimSpace(line) == ""
	}
	return strings.TrimSpace(ansi.Strip(line)) == ""
}

// trimRight drops what a line ends with and does not draw: spaces, and the
// style sequences among them. A style still in force where the line now ends
// is switched off there.
func trimRight(line string) string {
	styled := false
	for {
		line = strings.TrimRight(line, " ")
		i := strings.LastIndex(line, "\x1b[")
		if i < 0 || !isStyle(line[i:]) {
			break
		}
		line, styled = line[:i], true
	}
	if styled && strings.Contains(line, "\x1b[") {
		line += "\x1b[m"
	}
	return line
}

// isStyle reports whether s is one style sequence and nothing else.
func isStyle(s string) bool {
	if len(s) < 3 || s[len(s)-1] != 'm' {
		return false
	}
	for _, c := range []byte(s[2 : len(s)-1]) {
		if (c < '0' || c > '9') && c != ';' && c != ':' {
			return false
		}
	}
	return true
}

// plainASCII reports whether s is printable ASCII and nothing else, so that
// each of its bytes is one cell.
func plainASCII(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// hard breaks one line of text into rows of at most w cells, wherever a row
// is full. It is Theme.Hardwrap, without the measuring where every byte is
// known to be one cell.
func (r *Renderer) hard(line string, w int) []string {
	if !plainASCII(line) {
		return r.th.Hardwrap(line, w)
	}
	rows := make([]string, 0, len(line)/w+1)
	for len(line) > w {
		rows = append(rows, line[:w])
		line = line[w:]
	}
	return append(rows, line)
}

// rowsOf counts the rows hard would make of a line.
func (r *Renderer) rowsOf(line string, w int) int {
	if !plainASCII(line) {
		return len(r.th.Hardwrap(line, w))
	}
	return max((len(line)+w-1)/w, 1)
}

// folded hard-wraps text to w cells and keeps the first limit rows of it. It
// returns them with the number of rows left out. When the Renderer is
// expanded nothing is left out.
func (r *Renderer) folded(text string, w, limit int) (rows []string, hidden int) {
	w = max(w, 1)
	for line := range strings.SplitSeq(text, "\n") {
		if !r.o.Expanded && len(rows) >= limit {
			hidden += r.rowsOf(line, w)
			continue
		}
		wrapped := r.hard(line, w)
		if !r.o.Expanded && len(rows)+len(wrapped) > limit {
			hidden += len(rows) + len(wrapped) - limit
			wrapped = wrapped[:limit-len(rows)]
		}
		rows = append(rows, wrapped...)
	}
	return rows, hidden
}

// more is the line under folded rows that says how many there are still.
// Where the row has no room for all of it, the key that shows them is left
// out.
func (r *Renderer) more(hidden, room int) string {
	rows := " more rows"
	if hidden == 1 {
		rows = " more row"
	}
	line := r.th.G.Ellipsis + " " + strconv.Itoa(hidden) + rows
	if full := line + " (ctrl+o)"; r.th.Width(full) <= room {
		line = full
	}
	return r.tone(toneDim, line)
}

// spans draws one line a mod or a command printed: each span made safe, in
// its tone. In the ASCII glyph set a character outside ASCII becomes "?",
// as it does on the way from a mod.
func (r *Renderer) spans(line wire.Line) string {
	var b strings.Builder
	for _, s := range line {
		st := r.th.Style(s.Tone)
		if s.Bold {
			st = st.Bold(true)
		}
		b.WriteString(paint(st, safe.Line(s.T, maxSpanRunes, r.ascii)))
	}
	return b.String()
}

// modLines draws the lines a mod made of an item, each cut to w cells. The
// mod was told the width, so a longer line is its own doing.
func (r *Renderer) modLines(lines []wire.Line, w int) []string {
	if len(lines) > maxModLines {
		lines = lines[:maxModLines]
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = r.th.Truncate(r.spans(line), w)
	}
	return out
}

// fileName is the name an image is called by: the last element of its path.
func (r *Renderer) fileName(p string) string {
	if name := r.name(path.Base(p)); name != "" {
		return name
	}
	return "file"
}

// links makes every line stand alone where a hyperlink runs over a line
// break: the link is switched off at the end of the line and on again at the
// start of the next. The style sequences are already kept that way by the
// theme's wrapping; it does not know links.
func links(lines []string) []string {
	open := ""
	for i, line := range lines {
		if line == "" || (open == "" && !strings.Contains(line, linkStart)) {
			continue
		}
		before := open
		open = linkAfter(open, line)
		line = before + line
		if open != "" {
			line += ansi.ResetHyperlink()
		}
		lines[i] = line
	}
	return lines
}

// linkAfter returns the sequence of the hyperlink in force after line, given
// the one in force before it. "" is no link.
func linkAfter(open, line string) string {
	for {
		i := strings.Index(line, linkStart)
		if i < 0 {
			return open
		}
		body := line[i+len(linkStart):]
		end, size := stringEnd(body)
		if end < 0 {
			return open
		}
		if _, uri, _ := strings.Cut(body[:end], ";"); uri == "" {
			open = ""
		} else {
			open = line[i : i+len(linkStart)+end+size]
		}
		line = body[end+size:]
	}
}

// stringEnd finds what ends a string sequence: BEL, or ESC and a backslash.
// It returns where the terminator starts and how long it is, or -1.
func stringEnd(s string) (at, size int) {
	for i := range len(s) {
		switch {
		case s[i] == 0x07:
			return i, 1
		case s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\':
			return i, 2
		}
	}
	return -1, 0
}
