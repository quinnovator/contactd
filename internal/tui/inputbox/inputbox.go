// Package inputbox is the box a message is written in: a text area with a
// prompt, drawn inside a border where the terminal has room for one.
//
// The text area is the one from bubbles. It holds the draft, takes the keys,
// and says where each line wraps. Everything that reaches the screen is made
// here: the rows, from the text area's account of them and never from its
// own drawing; the border, by the theme, so that its right edge is measured
// the way the terminal counts cells; and the place of the cursor, which is
// the terminal's own and is drawn by nobody.
//
// The text area's drawing is left alone because it is not safe to call. It
// panics when the cursor stands between an emoji with a selector (U+FE0F)
// that ends a row and the space after it. It draws nothing for a wide
// character that follows a narrow one in the last cell of a row, which it
// wraps a cell too late. And it breaks some rows in two. So a key is handed
// to the text area at a width where no line wraps, which is where its
// drawing is sound.
//
// What is drawn is not always what is written. Every kind of space is drawn
// as one plain space, which is how the text area counts it. Characters that
// turn the direction of text around are not drawn at all, and neither, on a
// terminal that counts a character at a time, are the joiner (U+200D) and
// the emoji selector (U+FE0F). Value and Enter return the draft as it was
// written.
//
// A row can come out wider than the box. The text area wraps by its own
// count of cells, a cluster at a time, and a terminal that counts a
// character at a time finds a row with joined emoji or skin tones wider than
// that; and there is the row the text area wraps a cell too late. A draft
// with such a row is wrapped narrower, until every row fits the box. So the
// box shows all of a draft, short of a single cluster that is wider than the
// box.
package inputbox

import (
	"reflect"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"

	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// The size of a box that was never given one.
const (
	defaultWidth   = 80
	defaultMaxRows = 8
)

// firstWide is the first character that may take other than one cell: the
// first combining mark. Everything before it is one cell wide by every
// count.
const firstWide = 0x300

// Model is the input box. Make one with New. It is not safe for use from
// several goroutines.
type Model struct {
	th *theme.Theme
	ta textarea.Model
	// placeholder is the placeholder as given, and hint what is drawn of
	// it: made safe for the screen, on one line.
	placeholder, hint string
	// ghost is the ghost text, made safe for the screen.
	ghost string

	width, maxRows int
	boxed          bool
	// inner is the number of cells a row has inside the border, and left
	// the number of cells the border puts before it. Without a border they
	// are the width and zero.
	inner, left int
	// wrap is the width the text area is given, prompt included: inner, or
	// less where a row of the draft would not fit. fitted and fitValue say
	// for which draft it was worked out.
	wrap     int
	fitted   bool
	fitValue string
	// prompt starts the first row of the draft and blank starts the others.
	// Both are as wide as each other.
	prompt, blank string
	// row is the row of the draft the cursor is on, counting each row of a
	// wrapped line, and top the first row that is shown.
	row, top int

	// lines and the cursor are drawn once and kept until something changes.
	drawn  bool
	lines  []string
	cx, cy int
}

// state is everything about the text area that shows on the screen.
type state struct {
	value            string
	row, col, height int
	selected         bool
	from, to         textarea.Position
}

// New makes an empty input box that has the focus. The placeholder is shown,
// dim, while the box is empty: on one row, cut with the theme's ellipsis
// where it is too long, and without any control character it holds. Until
// SetSize is called the box is 80 cells wide, has no border, and shows at
// most 8 rows.
func New(th *theme.Theme, placeholder string) *Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	// The text area is as tall as the draft and as wide as it is told.
	// Which of its rows are shown is decided here, in scroll: a height
	// limit of its own would also be a limit on the number of lines that
	// can be written.
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = 0
	ta.MaxWidth = 0
	// Enter sends, so a new line needs another key.
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j", "alt+enter", "shift+enter"))
	ta.SetVirtualCursor(false)
	ta.SetStyles(th.Textarea())
	ta.Focus()

	m := &Model{th: th, ta: ta, placeholder: placeholder, width: defaultWidth, maxRows: defaultMaxRows}
	m.layout()
	return m
}

// SetTheme draws the box with another theme from now on. The draft and the
// cursor stay as they are.
func (m *Model) SetTheme(th *theme.Theme) {
	m.th = th
	m.ta.SetStyles(th.Textarea())
	m.layout()
}

// SetSize gives the box its room: the width of the terminal, the most rows
// of text it may show, and whether it has a border. With a border the text
// has four cells less. A draft with more rows than fit is scrolled so that
// the row with the cursor is shown.
//
// The draft is wrapped again only when the width or the border changes, so
// SetSize can be called after every key with the rows the screen has to
// spare.
func (m *Model) SetSize(width, maxRows int, boxed bool) {
	width, maxRows = max(width, 0), max(maxRows, 1)
	switch {
	case width != m.width || boxed != m.boxed:
		m.width, m.maxRows, m.boxed = width, maxRows, boxed
		m.layout()
	case maxRows != m.maxRows:
		m.maxRows = maxRows
		m.changed()
	}
}

// SetGhost sets the text shown, dim, after the draft: the rest of a command
// and its usage. It is drawn only while the draft is one line that is not
// empty, the cursor is at its end, nothing is selected, and prompt, draft
// and ghost fit in one row. "" removes it. The ghost never becomes part of
// the draft. It is drawn on one line and without any control character it
// holds.
func (m *Model) SetGhost(s string) {
	// A tab is four spaces once it is clean, so four times the bytes is
	// room for all of it.
	s = visible(safe.Line(s, 4*len(s), false), true)
	if s == m.ghost {
		return
	}
	m.ghost = s
	m.drawn = false
}

// Focus gives the box the focus, which New has given it already. The command
// is nil: the cursor is the terminal's, so nothing has to blink.
func (m *Model) Focus() tea.Cmd {
	return m.ta.Focus()
}

// Update hands a message to the box: typing, editing keys, the keys that
// start a new line (ctrl+j, alt+enter, shift+enter), and pasted text.
// In pasted text CRLF and CR become LF, a tab becomes four spaces, and any
// other control character is dropped. The enter key is not handled here: the
// screen calls Enter. Page up and page down do nothing: they scroll the
// screen, not the draft.
//
// changed reports that the box has to be drawn again: its text, its cursor,
// its selection or its number of rows is different. It is not a sign that
// something was typed: compare Value for that.
//
// cmd is nil for all but two keys. ctrl+v reads the system clipboard, and
// its text comes back as a tea.PasteMsg for Update. ctrl+shift+c copies what
// is selected.
func (m *Model) Update(msg tea.Msg) (cmd tea.Cmd, changed bool) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		msg.Content = newlines(msg.Content)
		return m.update(msg)
	case tea.KeyPressMsg:
		if key.Matches(msg, m.ta.KeyMap.Paste) {
			// The clipboard's text comes back as a tea.PasteMsg, so it
			// takes the same way in as text the terminal pasted.
			return clipboard, false
		}
		// A key's text is what it types, and is taken as pasted text is.
		msg.Text = newlines(msg.Text)
		return m.update(msg)
	}
	return m.update(msg)
}

// update hands a message to the text area and reports whether anything that
// is drawn changed.
func (m *Model) update(msg tea.Msg) (tea.Cmd, bool) {
	before := m.state()
	cmd := m.edit(msg)
	if m.state() == before {
		return cmd, false
	}
	m.changed()
	return cmd, true
}

// edit hands a message to the text area in a way that cannot bring the
// program down.
//
// The text area draws itself whenever it is handed a message, and its
// drawing panics over some wrapped rows (see the package comment). So it is
// handed the message at a width where no line wraps, and given its own width
// back afterwards. The keys that move the cursor a row up or down need the
// rows: they are done here, by the text area's own moves, which draw
// nothing.
func (m *Model) edit(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		keys := m.ta.KeyMap
		switch {
		case key.Matches(k, keys.LinePrevious):
			m.ta.ClearSelection()
			m.ta.CursorUp()
			return nil
		case key.Matches(k, keys.LineNext):
			m.ta.ClearSelection()
			m.ta.CursorDown()
			return nil
		case key.Matches(k, keys.PageUp, keys.PageDown):
			return nil
		case key.Matches(k, keys.SelectLineUp, keys.SelectLineDown):
			// Only the text area can extend a selection, and it has to
			// do it over the rows as they are.
			return m.guarded(msg)
		}
	}
	m.ta.SetWidth(m.flat(msg))
	cmd := m.guarded(msg)
	m.ta.SetWidth(m.wrap)
	return cmd
}

// guarded hands a message to the text area and survives a panic in it: the
// draft and the cursor are then put back as they were, and the message has
// done nothing.
func (m *Model) guarded(msg tea.Msg) (cmd tea.Cmd) {
	value, row, col := m.ta.Value(), m.ta.Line(), m.ta.Column()
	defer func() {
		if recover() == nil {
			return
		}
		// The text area changes its lines in place, so what is left of
		// them is not to be trusted.
		m.ta.SetValue(value)
		m.ta.MoveToBegin()
		for range row {
			m.ta.CursorEnd()
			m.ta.CursorDown()
		}
		m.ta.SetCursorColumn(col)
		cmd = nil
	}()
	m.ta, cmd = m.ta.Update(msg)
	return cmd
}

// flat returns a width at which no line of the draft is wrapped, before or
// after the text area has taken msg: room for the longest line twice over,
// since two lines can be joined, and for what msg adds.
func (m *Model) flat(msg tea.Msg) int {
	cells := 2 * longest(m.ta.Value())
	switch msg := msg.(type) {
	case tea.PasteMsg:
		cells += longest(msg.Content)
	case tea.KeyPressMsg:
		cells += longest(msg.Text)
	}
	return len(m.blank) + cells + 2
}

// longest returns the most cells a line of s can take: two for a character
// that may be wide, and four for a tab, which becomes four spaces.
func longest(s string) int {
	most, n := 0, 0
	for _, r := range s {
		switch {
		case r == '\n':
			n = 0
		case r == '\t':
			n += 4
		case r < firstWide:
			n++
		default:
			n += 2
		}
		most = max(most, n)
	}
	return most
}

// Enter is the enter key. A backslash before the cursor becomes a newline
// and send is false. Otherwise text is the draft, trimmed of trailing space,
// and send is true, also when the text is empty. The draft stays in the box:
// the screen calls Reset once the message is on its way.
func (m *Model) Enter() (text string, send bool) {
	line, col := m.line(), m.ta.Column()
	if col > 0 && col <= len(line) && line[col-1] == '\\' {
		m.ta.ClearSelection()
		m.edit(tea.KeyPressMsg{Code: tea.KeyBackspace})
		m.ta.InsertRune('\n')
		m.changed()
		return "", false
	}
	return strings.TrimRightFunc(m.ta.Value(), unicode.IsSpace), true
}

// Value returns the draft.
func (m *Model) Value() string {
	return m.ta.Value()
}

// SetValue replaces the draft and puts the cursor at its end. CRLF and CR
// become LF, a tab four spaces, and other control characters are dropped.
func (m *Model) SetValue(s string) {
	m.ta.SetValue(newlines(s))
	m.changed()
}

// Reset empties the box.
func (m *Model) Reset() {
	m.ta.Reset()
	m.changed()
}

// LineCount returns the number of lines in the draft, as written: a long
// line that is wrapped over several rows is one line. An empty draft has one.
func (m *Model) LineCount() int {
	return m.ta.LineCount()
}

// TextRows returns the number of rows the draft takes at the width the box
// has: one for each line, and a line that is wrapped counts as many as it
// takes. It is not held to the most rows SetSize allows, so it is what the
// screen asks for when it shares out its rows. An empty draft takes one.
func (m *Model) TextRows() int {
	return max(m.ta.Height(), 1)
}

// Rows returns the number of screen rows the box takes, border included:
// TextRows, up to the most SetSize allows, and two more with a border.
func (m *Model) Rows() int {
	if m.boxed {
		return m.shownRows() + 2
	}
	return m.shownRows()
}

// Lines draws the box: exactly Rows lines, each exactly as wide as the
// terminal. No line holds a cursor.
func (m *Model) Lines() []string {
	m.draw()
	return slices.Clone(m.lines)
}

// Cursor returns where the terminal's cursor goes: the cell after the text
// before the cursor, as that text is drawn and as the terminal counts cells.
// x and y are relative to the first cell of the first line that Lines
// returns, so the screen adds the box's first row to y. x counts the border.
func (m *Model) Cursor() (x, y int) {
	m.draw()
	return m.cx, m.cy
}

// layout fits the text area to the theme and the size, and is called when
// either changes.
func (m *Model) layout() {
	th := m.th
	m.inner, m.left = m.width, 0
	if m.boxed {
		// The room inside a border, as theme.Box leaves it: a border glyph
		// and a space on both sides. Where that leaves no room Box draws no
		// sides, and the text has the whole width.
		side := th.Width(th.G.BoxV)
		corners := max(th.Width(th.G.BoxTL)+th.Width(th.G.BoxTR), th.Width(th.G.BoxBL)+th.Width(th.G.BoxBR))
		if inner := m.width - 2*side - 2; inner >= 1 && corners <= m.width {
			m.inner, m.left = inner, side+1
		}
	}

	m.prompt = th.G.Prompt + " "
	m.blank = strings.Repeat(" ", th.Width(m.prompt))
	m.ta.SetPromptFunc(len(m.blank), m.promptFor)
	m.fitted = false
	// The placeholder is drawn here, not by the text area, which wraps it
	// by its own count of cells and shows the first row of what it gets.
	m.hint = visible(safe.Clean(m.placeholder, m.wc()), m.wc())
	m.changed()
}

// wc reports whether the terminal counts cells a character at a time, and
// not a cluster at a time.
func (m *Model) wc() bool {
	return m.th.Caps.Width != termcaps.WidthGrapheme
}

// promptFor marks the first row of the draft, and only the first.
func (m *Model) promptFor(info textarea.PromptInfo) string {
	if info.LineNumber == 0 {
		return m.prompt
	}
	return m.blank
}

// changed is called after anything that is drawn may have changed.
func (m *Model) changed() {
	m.drawn = false
	m.fit()
	m.scroll()
}

// scroll moves the rows shown as little as it takes to show the row with the
// cursor, and never leaves a shown row empty while the draft has a row for
// it.
func (m *Model) scroll() {
	rows := m.shownRows()
	m.row = m.cursorRow()
	m.top = min(m.top, m.row)
	m.top = max(m.top, m.row-rows+1)
	m.top = max(min(m.top, m.ta.Height()-rows), 0)
}

// shownRows is the number of rows of text shown: every row of the draft,
// up to the most the box may show.
func (m *Model) shownRows() int {
	return min(m.TextRows(), m.maxRows)
}

// cursorRow is the row of the draft the cursor is on, counting each row of a
// wrapped line.
func (m *Model) cursorRow() int {
	c := m.ta.Cursor()
	if c == nil {
		return 0
	}
	return c.Y + m.ta.ScrollYOffset()
}

// state returns what the text area shows now.
func (m *Model) state() state {
	s := state{value: m.ta.Value(), row: m.ta.Line(), col: m.ta.Column(), height: m.ta.Height()}
	s.from, s.to, s.selected = m.ta.Selection()
	return s
}

// line returns the line of the draft the cursor is on.
func (m *Model) line() []rune {
	lines := strings.Split(m.ta.Value(), "\n")
	row := m.ta.Line()
	if row < 0 || row >= len(lines) {
		return nil
	}
	return []rune(lines[row])
}

// draw makes the lines and finds the cursor, unless nothing changed since
// the last time.
func (m *Model) draw() {
	if m.drawn {
		return
	}
	m.drawn = true

	rows := m.shownRows()
	value := m.ta.Value()
	text := make([]string, 0, rows)
	switch ghost, ok := m.ghostRow(value); {
	case value == "":
		text = append(text, m.promptText(0)+m.placeholderText())
	case ok:
		text = append(text, ghost)
	default:
		text = m.rows(m.top, m.top+rows)
	}
	for len(text) < rows {
		text = append(text, "")
	}
	// The characters that are never drawn are taken out here, for every
	// kind of row.
	for i, row := range text {
		text[i] = visible(row, m.wc())
	}

	if m.boxed {
		m.lines = m.th.Box(text, m.width, "", "")
	} else {
		for i, row := range text {
			text[i] = m.th.Pad(row, m.width)
		}
		m.lines = text
	}
	m.cx, m.cy = m.cursor()
}

// promptText draws what a row of the draft starts with: the prompt on the
// first row and as many spaces on the others.
func (m *Model) promptText(row int) string {
	prompt := m.blank
	if row == 0 {
		prompt = m.prompt
	}
	return m.ta.Styles().Focused.Prompt.Inline(true).Render(prompt)
}

// placeholderText draws the placeholder in the room the first row has after
// the prompt.
func (m *Model) placeholderText() string {
	room := m.inner - m.th.Width(m.prompt)
	return m.ta.Styles().Focused.Placeholder.Inline(true).Render(m.th.Truncate(m.hint, room))
}

// segment is one row of the draft: the characters from from up to to of a
// line. A row with no text has the two equal.
type segment struct {
	line     []rune
	number   int // of the line, from zero
	from, to int
}

// text returns the row's text with every space a plain one.
func (s segment) text() string {
	return spaces(string(s.line[s.from:s.to]))
}

// segments returns the rows of the draft from row from up to, not
// including, row to, counting each row of a wrapped line.
//
// Where a line wraps is the text area's account, asked a row at a time, so
// a row made here holds all of its text and is the row the cursor is counted
// in.
func (m *Model) segments(from, to int) []segment {
	// The copy's cursor is walked through the draft; the text area's own
	// stays where it is.
	walk := m.ta
	walk.MoveToBegin()
	lines := strings.Split(walk.Value(), "\n")
	out := make([]segment, 0, max(to-from, 0))
	first := 0 // the row the line in hand starts on
	for i, text := range lines {
		line := []rune(text)
		walk.SetCursorColumn(0)
		height := max(walk.LineInfo().Height, 1)
		// A line's first row can be empty: the text area leaves it so
		// when the first word fills the row and ends in a character of no
		// width. The cursor cannot be put on such a row, so each row is
		// filed under the number the text area gives it.
		rows := make([]segment, height)
		for n := range rows {
			rows[n] = segment{line: line, number: i}
		}
		for at, n := 0, 0; n < height && first+height > from; n++ {
			walk.SetCursorColumn(at)
			info := walk.LineInfo()
			row := min(max(info.RowOffset, n), height-1)
			a := min(max(info.StartColumn, 0), len(line))
			b := min(max(a+info.Width, a), len(line))
			if row == height-1 {
				// Whatever is left of the line is on its last row.
				b = len(line)
			}
			rows[row].from, rows[row].to = a, b
			at, n = b, row
		}
		for n, row := range rows {
			if at := first + n; at >= from && at < to {
				out = append(out, row)
			}
		}
		first += height
		if first >= to || i == len(lines)-1 {
			break
		}
		// From the end of a line, down is the start of the next.
		walk.CursorEnd()
		walk.CursorDown()
	}
	return out
}

// rows draws the rows of the draft from row from up to, not including, row
// to. A row is its prompt, its text with every space a plain one, and the
// selection on what is selected.
func (m *Model) rows(from, to int) []string {
	selection := m.ta.Styles().Focused.Selection.Inline(true)
	start, end, selected := m.ta.Selection()
	room := m.inner - m.th.Width(m.prompt)
	segments := m.segments(from, to)
	out := make([]string, len(segments))
	for i, s := range segments {
		// A row ends in the spaces it was wrapped at, and a full row has no
		// room for them. They are left out, so that one that is selected
		// does not push the row over the edge.
		for s.to > s.from && unicode.IsSpace(s.line[s.to-1]) && m.th.Width(visible(s.text(), m.wc())) > room {
			s.to--
		}
		// The part of the row that is selected, if any is.
		a, b := s.from, s.from
		if selected && s.number >= start.Row && s.number <= end.Row {
			a, b = s.from, s.to
			if s.number == start.Row {
				a = min(max(start.Col, s.from), s.to)
			}
			if s.number == end.Row {
				b = min(max(end.Col, a), s.to)
			}
			// Whole clusters, as far as the row holds them: the text area
			// can wrap a line inside one.
			a, b = wholeClusters(s.line, a, b)
			a, b = max(a, s.from), min(b, s.to)
		}
		row := m.promptText(from+i) + spaces(string(s.line[s.from:a]))
		if a < b {
			row += selection.Render(spaces(string(s.line[a:b])))
		}
		out[i] = row + spaces(string(s.line[b:s.to]))
	}
	return out
}

// fit gives the text area the widest width at which every row of the draft
// fits the box.
//
// The text area wraps by its own count of cells, a cluster at a time, and
// the box is as wide as the theme counts. Two kinds of row come out too
// wide. On a terminal that counts a character at a time, a row with joined
// emoji or skin tones holds more cells than the text area thinks. And the
// text area wraps a line a cell too late when a wide character follows a
// narrow one in a row's last cell. A draft with such a row is wrapped
// narrower until no row is over: no character of a draft is cut. Only a
// cluster wider than the whole box still is.
func (m *Model) fit() {
	value := m.ta.Value()
	if m.fitted && value == m.fitValue {
		return
	}
	m.fitted, m.fitValue = true, value
	m.wrap = m.inner
	m.ta.SetWidth(m.wrap)
	if strings.IndexFunc(value, func(r rune) bool { return r >= firstWide }) < 0 {
		// Text of one cell to a character is counted alike by all.
		return
	}
	for m.ta.Width() > 1 {
		over := m.over()
		if over <= 0 {
			return
		}
		// Half of what the widest row is over, since a narrower row often
		// loses its last word, and that is more cells than asked for.
		m.wrap -= max(over/2, 1)
		m.ta.SetWidth(m.wrap)
	}
}

// over returns the number of cells the widest row of the draft has too many
// for the box, or less than one when every row fits.
func (m *Model) over() int {
	room := m.inner - m.th.Width(m.prompt)
	most := 0
	cells := func(row string) {
		most = max(most, m.th.Width(visible(strings.TrimRight(row, " "), m.wc())))
	}
	if value := m.ta.Value(); m.ta.Height() == m.ta.LineCount() {
		// No line is wrapped, so the rows are the lines.
		for line := range strings.SplitSeq(value, "\n") {
			cells(spaces(line))
		}
	} else {
		for _, s := range m.segments(0, m.ta.Height()) {
			cells(s.text())
		}
	}
	return most - room
}

// wholeClusters widens the part of a line from a up to b so that it starts
// and ends between clusters. The text area selects a character at a time,
// and a selection drawn from the middle of a joined emoji would cut the
// emoji in two: a terminal that counts a cluster at a time, and the theme
// with it, counts each half a cluster of its own, and the row is wider.
func wholeClusters(line []rune, a, b int) (int, int) {
	if a >= b {
		return a, b
	}
	at := 0
	for g := uniseg.NewGraphemes(string(line)); g.Next() && at < b; {
		end := at + len(g.Runes())
		if a > at && a < end {
			a = at
		}
		if b > at && b < end {
			b = end
		}
		at = end
	}
	return a, b
}

// spaces draws every kind of space as one plain space, which is what the
// text area takes each for when it wraps a line: one cell.
func spaces(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= utf8.RuneSelf && unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
}

// visible returns text as the box draws it, which is also the text the
// cursor is counted after.
//
// Every kind of space is one plain space. The characters that turn the
// direction of text around (U+202A to U+202E, U+2066 to U+2069) are left
// out, since a terminal that obeys them would move the border; so is the
// character kitty draws an image under (U+10EEEE). These are the characters
// safe.Clean takes out of a message.
//
// With wc, for a terminal that counts a character at a time, the joiner
// (U+200D) and the emoji selector (U+FE0F) are left out as well, as they are
// left out of every message drawn there: some such terminals draw a joined
// emoji in two cells and some in six, and without the joiner all draw what
// the theme counts.
func visible(s string, wc bool) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < utf8.RuneSelf:
			return r
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x10eeee:
			return -1
		case wc && (r == 0x200d || r == 0xfe0f):
			return -1
		case unicode.IsSpace(r):
			return ' '
		}
		return r
	}, s)
}

// ghostRow draws the row of a one-line draft with the ghost text after it,
// and reports whether the ghost is shown at all.
func (m *Model) ghostRow(value string) (string, bool) {
	// One row is one line that is not wrapped.
	if m.ghost == "" || value == "" || m.ta.Height() != 1 || m.ta.HasSelection() {
		return "", false
	}
	if m.ta.Column() != utf8.RuneCountInString(value) {
		return "", false
	}
	draft := visible(value, m.wc())
	if m.th.Width(m.prompt)+m.th.Width(draft+m.ghost) > m.inner {
		return "", false
	}
	return m.promptText(0) + draft + m.th.Style("dim").Render(m.ghost), true
}

// cursor finds the cell the terminal's cursor goes to. The row comes from
// the text area, by way of scroll. The column is measured here, with the
// theme's measure, over the text before the cursor as it is drawn: the text
// area counts a cluster at a time, and not every terminal does.
func (m *Model) cursor() (x, y int) {
	info := m.ta.LineInfo()
	line := m.line()
	from := min(max(info.StartColumn, 0), len(line))
	to := min(max(from+info.ColumnOffset, from), len(line))
	prompt := m.blank
	if m.row == 0 {
		prompt = m.prompt
	}
	// The prompt and the text are measured apart, as they are drawn apart:
	// the prompt has a style of its own, so nothing in the text joins it.
	x = m.left + m.th.Width(prompt) + m.th.Width(visible(string(line[from:to]), m.wc()))
	// The text area keeps the cursor inside its width. Where the terminal
	// counts a cluster wider than the text area does, the cursor stays on
	// the last cell of the text and off the border.
	x = max(min(x, m.left+m.inner-1), 0)

	y = min(max(m.row-m.top, 0), m.shownRows()-1)
	if m.boxed {
		y++
	}
	return x, y
}

// newlines makes every line ending LF. A terminal pastes a line ending as
// CR, and a Windows clipboard holds CRLF; the text area would make two lines
// of the second.
func newlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// clipboard reads the system clipboard, as the text area's paste key does,
// and returns its text as a tea.PasteMsg.
func clipboard() tea.Msg {
	return pasted(textarea.Paste())
}

// pasted turns the text area's answer from the clipboard into a
// tea.PasteMsg. The answer is a string of a type the text area keeps to
// itself, or an error, which is nothing to paste.
func pasted(msg tea.Msg) tea.Msg {
	v := reflect.ValueOf(msg)
	if !v.IsValid() || v.Kind() != reflect.String {
		return nil
	}
	return tea.PasteMsg{Content: v.String()}
}
