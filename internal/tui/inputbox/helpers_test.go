package inputbox_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/inputbox"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// Text that is wide, or that the two ways of counting cells disagree about.
const (
	cjkText   = "这是一个很长的中文句子用来测试"
	zwjFamily = "👨\u200d👩\u200d👧"
	skinTone  = "👍🏽"
	heart     = "❤\ufe0f"
	placehold = "Message ada"
)

// namedTheme is a theme and what to call it in a subtest.
type namedTheme struct {
	name string
	th   *theme.Theme
}

func newTheme(c termcaps.Color, bg termcaps.Background, g termcaps.Glyphs, w termcaps.Width) *theme.Theme {
	return theme.New(termcaps.Caps{Color: c, Background: bg, Glyphs: g, Width: w}, theme.Options{})
}

// unicodeTheme is the theme of the Frame mock: 24-bit colour on a dark
// background, counting cells a character at a time.
func unicodeTheme() *theme.Theme {
	return newTheme(termcaps.ColorTrue, termcaps.BgDark, termcaps.GlyphsUnicode, termcaps.WidthWc)
}

// graphemeTheme counts cells a cluster at a time.
func graphemeTheme() *theme.Theme {
	return newTheme(termcaps.ColorTrue, termcaps.BgDark, termcaps.GlyphsUnicode, termcaps.WidthGrapheme)
}

// asciiTheme has no colour and ASCII glyphs.
func asciiTheme() *theme.Theme {
	return newTheme(termcaps.ColorNone, termcaps.BgUnknown, termcaps.GlyphsASCII, termcaps.WidthWc)
}

// lightTheme has 16 colours on a light background, where the bar has no fill.
func lightTheme() *theme.Theme {
	return newTheme(termcaps.Color16, termcaps.BgLight, termcaps.GlyphsUnicode, termcaps.WidthWc)
}

// wideGlyphTheme replaces the prompt and the box's side by glyphs two cells
// wide, so nothing in a layout can count on one cell for either.
func wideGlyphTheme() *theme.Theme {
	return theme.New(
		termcaps.Caps{Color: termcaps.Color256, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc},
		theme.Options{Glyph: map[string]string{"prompt": "=>", "boxv": "||"}},
	)
}

func themes() []namedTheme {
	return []namedTheme{
		{"truecolor dark wcwidth", unicodeTheme()},
		{"truecolor dark grapheme", graphemeTheme()},
		{"16 light", lightTheme()},
		{"none ascii", asciiTheme()},
		{"wide glyphs", wideGlyphTheme()},
	}
}

// widthThemes are the themes that differ in what a row holds: the two ways
// of counting cells, and a prompt and a border of two cells. The others
// differ from the first in colour and glyphs alone.
func widthThemes() []namedTheme {
	all := themes()
	return []namedTheme{all[0], all[1], all[4]}
}

// keyOf is a key press of a key that types nothing.
func keyOf(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

// press sends a key that types nothing and reports whether the box changed.
func press(m *inputbox.Model, code rune, mod tea.KeyMod) bool {
	_, changed := m.Update(keyOf(code, mod))
	return changed
}

// pressN sends the same key n times.
func pressN(m *inputbox.Model, n int, code rune) {
	for range n {
		press(m, code, 0)
	}
}

// typed types s a character at a time.
func typed(m *inputbox.Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// pasteOf pastes s, as the terminal does.
func pasteOf(m *inputbox.Model, s string) bool {
	_, changed := m.Update(tea.PasteMsg{Content: s})
	return changed
}

// plain is a line without its escape sequences.
func plain(s string) string { return ansi.Strip(s) }

// plainLines is every line of the box without its escape sequences.
func plainLines(m *inputbox.Model) []string {
	lines := m.Lines()
	for i, l := range lines {
		lines[i] = plain(l)
	}
	return lines
}

// notDrawn are the characters the box never draws: those that turn the
// direction of text around, and the one kitty draws an image under.
func notDrawn() []string {
	return []string{
		"\u202a", "\u202b", "\u202c", "\u202d", "\u202e",
		"\u2066", "\u2067", "\u2068", "\u2069", "\U0010eeee",
	}
}

// wideSpaces are spaces that are not U+0020. The box draws each as one.
func wideSpaces() []string {
	return []string{
		"\u00a0", "\u1680", "\u2000", "\u2001", "\u2002", "\u2003", "\u2004", "\u2005", "\u2006",
		"\u2007", "\u2008", "\u2009", "\u200a", "\u2028", "\u2029", "\u202f", "\u205f", "\u3000",
	}
}

// drawnAs is text as the box draws it under a theme: every space is a plain
// one, the characters of notDrawn are left out, and on a terminal that
// counts a character at a time so are the joiner and the emoji selector.
func drawnAs(th *theme.Theme, s string) string {
	pairs := wideSpacePairs()
	for _, c := range notDrawn() {
		pairs = append(pairs, c, "")
	}
	if th.Caps.Width != termcaps.WidthGrapheme {
		pairs = append(pairs, "\u200d", "", "\ufe0f", "")
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// frame says what theme.Box puts around a row of a box width cells wide:
// the text before the row's own, the text after it, and the cells between.
// Without a border, and where a border has no room for sides, that is
// nothing, nothing and the width.
func frame(th *theme.Theme, width int, boxed bool) (before, after string, inner int) {
	side := th.Width(th.G.BoxV)
	if !boxed || width-2*side-2 < 1 {
		return "", "", max(width, 0)
	}
	return th.G.BoxV + " ", th.G.BoxV, width - 2*side - 2
}

// inside returns the text of one row of the box, without the border and
// without the spaces that fill the row.
func inside(th *theme.Theme, line string, width int, boxed bool) string {
	before, after, _ := frame(th, width, boxed)
	s := plain(line)
	s = strings.TrimSuffix(s, after)
	s = strings.TrimPrefix(s, before)
	return strings.TrimRight(s, " ")
}

// ownCursor finds the cell the text area would draw its own cursor in: the
// row it is on, counted from the first row of the draft, and the text
// before it on that row. It reports false when no row has one, and when the
// cell is empty: the text area cut the row there, so what is before the
// cell is not all of the row.
func ownCursor(rows []string) (row int, before string, ok bool) {
	for i, r := range rows {
		if head, cell, found := strings.Cut(r, "\x1b[7m"); found {
			return i, plain(head), !strings.HasPrefix(cell, "\x1b[")
		}
	}
	return 0, "", false
}

// draftRows returns the text of every row of the draft, without what a row
// starts with: the prompt on the first and as many spaces on the others. It
// fails the test when the rows are not the draft: each character of a draft
// is on one row, in order, with every space a plain one.
func draftRows(t *testing.T, m *inputbox.Model) []string {
	t.Helper()
	rows := m.RowTexts()
	if len(rows) != m.TextRows() {
		t.Fatalf("the draft has %d rows, and TextRows is %d", len(rows), m.TextRows())
	}
	want := strings.NewReplacer(wideSpacePairs()...).Replace(strings.ReplaceAll(m.Value(), "\n", ""))
	if got := strings.Join(rows, ""); got != want {
		t.Fatalf("the rows of the draft hold %q, want %q", got, want)
	}
	return rows
}

// wideSpacePairs maps each of wideSpaces to a plain space, for a replacer.
func wideSpacePairs() []string {
	pairs := make([]string, 0, 2*len(wideSpaces()))
	for _, c := range wideSpaces() {
		pairs = append(pairs, c, " ")
	}
	return pairs
}

// check holds a box to everything that is true of it whatever was typed:
// the lines are as many as Rows says and each exactly as wide as the
// terminal, the rows shown are rows of the draft, and the cursor is inside
// the box, in the cell the text area would draw its own cursor in.
//
// ghost is the ghost text the box is expected to draw, or "".
//
// The text area cannot always be asked where it would draw its cursor: it
// draws some rows wrongly once a cursor is drawn inside a joined emoji, and
// it cuts a row it wrapped a cell too late. check reports whether it could
// be asked.
func check(t *testing.T, m *inputbox.Model, th *theme.Theme, width, maxRows int, boxed bool, ghost string) (asked bool) {
	t.Helper()
	lines := m.Lines()
	if len(lines) != m.Rows() {
		t.Fatalf("Lines returned %d lines and Rows is %d", len(lines), m.Rows())
	}
	for i, l := range lines {
		if got := th.Width(l); got != width {
			t.Errorf("line %d is %d cells wide, want %d: %q", i, got, width, l)
		}
		if strings.ContainsAny(l, "\r\n") {
			t.Errorf("line %d holds a line break: %q", i, l)
		}
		// Nothing is selected in a box that is checked, so reverse video
		// could only be a cursor, and the cursor is the terminal's.
		if strings.Contains(l, "\x1b[7m") {
			t.Errorf("line %d draws a cursor of its own: %q", i, l)
		}
		for _, c := range notDrawn() {
			if strings.Contains(l, c) {
				t.Errorf("line %d draws %+q: %q", i, c, l)
			}
		}
	}
	border := 0
	if boxed {
		border = 1
	}
	textRows := len(lines) - 2*border
	if want := min(m.TextRows(), maxRows); textRows != want {
		t.Fatalf("%d rows of text, want %d: the draft has %d and the box shows at most %d", textRows, want, m.TextRows(), maxRows)
	}
	if got, want := m.LineCount(), strings.Count(m.Value(), "\n")+1; got != want {
		t.Errorf("LineCount is %d, and the draft has %d lines", got, want)
	}

	draft := draftRows(t, m)
	top := m.Top()
	if top < 0 || top+textRows > len(draft) {
		t.Fatalf("rows %d to %d are shown, and the draft has %d", top, top+textRows-1, len(draft))
	}
	before, _, inner := frame(th, width, boxed)
	prompt := th.G.Prompt + " "
	lead := func(row int) string {
		if row == 0 {
			return prompt
		}
		return strings.Repeat(" ", th.Width(prompt))
	}

	// Every row shown is the row of the draft that belongs there. A row
	// wider than the box is cut by the theme, and is not compared; in a box
	// with no room for the prompt and a cell of text, that is every row.
	// The row of an empty draft holds the placeholder, which has its own
	// tests.
	fits := inner > th.Width(prompt)
	for i := 0; fits && m.Value() != "" && i < textRows; i++ {
		want := strings.TrimRight(lead(top+i)+drawnAs(th, draft[top+i]), " ")
		if ghost != "" {
			want = drawnAs(th, prompt+m.Value()) + ghost
		}
		if th.Width(prompt)+th.Width(strings.TrimPrefix(want, lead(top+i))) > inner {
			continue
		}
		if got := inside(th, lines[border+i], width, boxed); got != want {
			t.Errorf("row %d shows %q, want %q", i, got, want)
		}
	}

	// The cursor is inside the box, on a row of text.
	x, y := m.Cursor()
	left := th.Width(before)
	last := max(left+inner-1, 0)
	if x < min(left, last) || x > last {
		t.Errorf("the cursor is in column %d, outside the box's %d to %d", x, left, last)
	}
	if y < border || y > border+textRows-1 {
		t.Errorf("the cursor is on row %d, outside the box's %d to %d", y, border, border+textRows-1)
	}

	// And it is where the text area would draw its own. The prompt and the
	// text are measured apart, as they are drawn apart.
	own := m.OwnCursorRows()
	row, head, ok := ownCursor(own)
	if !ok || len(own) != m.TextRows() {
		return false
	}
	text, ok := strings.CutPrefix(head, lead(row))
	if !ok {
		t.Fatalf("the text area draws %q before its cursor, which does not start with %q", head, lead(row))
	}
	text = drawnAs(th, text)
	wantX := left + th.Width(prompt) + th.Width(text)
	if want := row - top + border; y != want {
		t.Errorf("the cursor is on row %d, want %d (rows %q)", y, want, lines)
	}
	if want := min(wantX, last); x != want {
		t.Errorf("the cursor is in column %d, want %d: after %q", x, want, before+head)
	}
	// The cell is the one after that text on the row as drawn, unless the
	// theme had to cut the row.
	whole := th.Width(prompt) + th.Width(strings.TrimRight(drawnAs(th, draft[row]), " "))
	if fits && y >= border && y < border+textRows && whole <= inner && wantX <= last {
		if got := plain(lines[y]); !strings.HasPrefix(got, before+lead(row)+text) {
			t.Errorf("row %d is %q and does not start with %q", y, got, before+lead(row)+text)
		}
	}
	return true
}
