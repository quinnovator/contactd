package sledit

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

const (
	// wideFrom is the width from which the settings stand beside the table
	// and all three previews are drawn.
	wideFrom = 80
	// tableWidth is the width of the table, as drawn. A narrower screen
	// gets a narrower table with fewer columns.
	tableWidth = 56
	// The columns of the table, each with one cell of space at its end.
	toggleWidth = 4
	nameWidth   = 11
	showsWidth  = 20
	sideWidth   = 7
	// minShows is the narrowest the SHOWS column is drawn, midShows the
	// narrowest it gets to make room for FROM, and minFrom the narrowest
	// FROM is drawn.
	minShows = 8
	midShows = 12
	minFrom  = 8
	// labelWidth is the column a preview's label stands in.
	labelWidth = 12
	// softRows is how many rows of the table the previews and the
	// settings make room for when the screen is short. Past that the
	// table scrolls and they stay.
	softRows = 6
	// maxText is the most characters of one value the screen cleans to
	// draw; no row is that wide.
	maxText = 512
	// The widths the previews are drawn at.
	previewWide   = 100
	previewNarrow = 60
	// sideGap is the least space between the two sides of a row: of a bar,
	// as statusline.Layout and Theme.Bar count it, and of the table's
	// heading.
	sideGap = 2
)

// The order the parts of the screen give way in when it is too short: the
// lowest goes first. The three in the middle are, on a wide screen, the
// previews from the last to the first; on a narrow one they are the note on
// shell segments, the preview and the settings. The row at the very bottom
// never goes.
const (
	dropGap = iota + 1
	dropThird
	dropSecond
	dropFirst
	dropLow
	dropRule
	dropHead
	dropHeader
)

// section is some rows of the screen that stay or go together.
type section struct {
	lines []string
	// drop is the section's turn to give way; 0 never.
	drop int
	// need is how many rows the table must be left with for the section
	// to stay. The sections that go first need the most.
	need int
	// bottom says the section belongs at the bottom edge: empty rows go
	// above the first such section.
	bottom bool
	gone   bool
}

// columns are the widths of the table's columns. A column of width 0 is not
// drawn.
type columns struct{ pad, toggle, name, shows, side, from int }

// columnsFor fits the columns to a table w cells wide. As the table narrows
// it loses where a segment comes from, then its side, then what it shows.
func columnsFor(w int) columns {
	var c columns
	if w >= 20 {
		c.pad = 1
	}
	inner := max(w-2*c.pad, 0)
	c.toggle = min(toggleWidth, inner)
	inner -= c.toggle
	if inner < nameWidth+minShows {
		c.name = inner
		return c
	}
	c.name = nameWidth
	rest := inner - nameWidth
	switch {
	case rest < minShows+sideWidth:
		c.shows = rest
	case rest < sideWidth+midShows+minFrom:
		c.side, c.shows = sideWidth, rest-sideWidth
	default:
		c.side = sideWidth
		c.shows = min(showsWidth, rest-sideWidth-minFrom)
		c.from = rest - c.side - c.shows
	}
	return c
}

// screen draws one frame of the editor.
type screen struct {
	m  *Model
	th *theme.Theme
	// w is the width of the screen in cells.
	w int
	// ascii says text from the file is folded to ASCII as it is cleaned.
	ascii bool
	// core holds a sample value for every core segment.
	core map[string]statusline.Value
}

// View draws the screen in exactly height rows, none wider than width. The
// cursor is where the terminal's own cursor belongs while a field is edited,
// and nil otherwise.
//
// From 80 columns the settings of the selected segment stand beside the
// table and three previews are drawn; under that the settings go below it
// and the one preview is the bar at the screen's own width. A screen too
// short for everything loses its empty rows first. Then the table scrolls,
// down to six rows, before the previews go, the last one first.
func (m *Model) View(th *theme.Theme, width, height int) (content string, cursor *tea.Cursor) {
	if th == nil || width <= 0 || height <= 0 {
		return "", nil
	}
	m.clamp()
	s := screen{
		m: m, th: th, w: width,
		ascii: th.Caps.Glyphs != termcaps.GlyphsUnicode,
		core:  coreSamples(th),
	}
	lines, inputAt, inputX := s.build(height)
	for i, line := range lines {
		if th.Width(line) > width {
			lines[i] = th.Truncate(line, width)
		}
	}
	if m.ed != nil && inputAt >= 0 {
		cursor = tea.NewCursor(min(inputX, width-1), inputAt)
		cursor.Blink = false
	}
	return strings.Join(lines, "\n"), cursor
}

// build lays the screen out in height rows. It returns the rows, and the row
// and column of the cursor when a field is being edited.
func (s screen) build(height int) (lines []string, inputAt, inputX int) {
	m, th := s.m, s.th
	wide := s.w >= wideFrom
	tw := min(s.w, tableWidth)
	cols := columnsFor(tw)
	n := len(m.cfg.Segments)

	// A gap or a note stays only while the whole table shows, a preview or
	// the settings while a good part of it does, and the rest while one
	// row does.
	all, some := max(n, 1), max(min(n, softRows), 1)
	var secs []*section
	add := func(sec *section) *section {
		secs = append(secs, sec)
		return sec
	}
	gap := func() { add(&section{lines: []string{""}, drop: dropGap, need: all}) }

	add(&section{lines: []string{s.header()}, drop: dropHeader, need: 1})
	add(&section{lines: []string{th.Rule(s.w, "")}, drop: dropRule, need: 1})
	gap()
	head := add(&section{lines: []string{""}, drop: dropHead, need: 1})
	table := add(&section{})
	gap()
	var beside []string
	if wide {
		fields, note := s.detail(s.w - tw - th.Width(th.G.Sep) - 2)
		beside = append(fields, note...)
		for i, block := range s.previews() {
			add(&section{lines: block, drop: dropFirst - i, need: some})
		}
	} else {
		fields, note := s.detail(s.w - 2*cols.pad)
		add(&section{lines: indent(fields, cols.pad), drop: dropFirst, need: some})
		add(&section{lines: indent(note, cols.pad), drop: dropThird, need: all})
		gap()
		add(&section{lines: s.preview(), drop: dropSecond, need: some})
	}
	input := &section{bottom: true}
	if m.ed != nil {
		var row string
		row, inputX = s.inputRow()
		input.lines = []string{row}
		add(input)
		add(&section{lines: []string{s.fieldHints()}, drop: dropLow, need: 1, bottom: true})
	} else {
		add(&section{lines: []string{s.presets()}, drop: dropLow, need: 1, bottom: true})
		add(&section{lines: []string{s.keyRow()}, bottom: true})
	}

	rows, spare := fit(secs, height, n)
	m.top = max(min(m.top, m.sel, n-rows), m.sel-rows+1, 0)
	if !head.gone {
		head.lines[0] = s.tableHead(cols, tw, rows)
	}
	table.lines = make([]string, 0, rows)
	for i := m.top; i < m.top+rows; i++ {
		table.lines = append(table.lines, s.row(i, cols, tw))
	}
	if wide {
		// The settings stand beside the table and its heading. Where
		// they are longer than both, and there is room, the table's
		// part of the screen grows to hold them.
		left := table.lines
		if !head.gone {
			left = slices.Concat(head.lines, table.lines)
			head.gone = true
		}
		more := max(min(len(beside)-len(left), spare), 0)
		spare -= more
		left = append(left, make([]string, more)...)
		table.lines = s.besideRows(left, beside, tw)
	}

	lines = make([]string, 0, height)
	inputAt = -1
	filled := false
	for _, sec := range secs {
		if sec.gone {
			continue
		}
		if sec.bottom && !filled {
			lines = append(lines, make([]string, spare)...)
			filled = true
		}
		if sec == input {
			inputAt = len(lines)
		}
		lines = append(lines, sec.lines...)
	}
	return lines, inputAt, inputX
}

// fit decides which sections stay on a screen of height rows, and returns
// how many rows of an n-row table are shown and how many rows are left
// empty. Sections give way in the order of their drop, each when the table
// would be left with fewer rows than the section needs it to have. A gap
// that gave way comes back when there is a row to spare after all.
func fit(secs []*section, height, n int) (rows, spare int) {
	for {
		fixed := 0
		var next *section
		for _, sec := range secs {
			if sec.gone {
				continue
			}
			fixed += len(sec.lines)
			if sec.drop > 0 && (next == nil || sec.drop < next.drop) {
				next = sec
			}
		}
		room := height - fixed
		if next == nil || room >= next.need {
			rows = max(min(room, n), 0)
			spare = max(room-rows, 0)
			break
		}
		for _, sec := range secs {
			if sec.drop == next.drop {
				sec.gone = true
			}
		}
	}
	for _, sec := range secs {
		if sec.gone && sec.drop == dropGap && spare >= len(sec.lines) {
			sec.gone = false
			spare -= len(sec.lines)
		}
	}
	return rows, spare
}

// indent puts n spaces before every line that is not empty.
func indent(lines []string, n int) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		if line != "" {
			out[i] = strings.Repeat(" ", n) + line
		}
	}
	return out
}

// clean makes text from the file or from a message fit to draw: one line
// with nothing in it that a terminal would act on.
func (s screen) clean(text string) string {
	return safe.Line(text, maxText, s.ascii)
}

// tint draws text in a tone.
func (s screen) tint(tone, text string) string {
	if text == "" {
		return ""
	}
	return s.th.Style(tone).Render(text)
}

// cell draws one cell of the table: text in a tone, cut to leave a cell of
// space at its end, in exactly w cells.
func (s screen) cell(text, tone string, w int) string {
	if w <= 0 {
		return ""
	}
	return s.th.Pad(s.tint(tone, s.th.Truncate(text, w-1)), w)
}

// header is the first row: the screen's name, a word when there are unsaved
// changes, and the file at the right.
func (s screen) header() string {
	left := s.th.Title().Render("Status line")
	if s.m.dirty() {
		left += "  " + s.tint(toneWarn, "not saved")
	}
	return s.th.Spread(left, s.tint(toneDim, s.clean(s.m.path)), s.w)
}

// tableHead is the row of column names. When the table shows only some of
// the segments it also says which one is selected, as "5/13". Where the two
// do not fit, the place in the list stays and column names give way, the
// last first.
func (s screen) tableHead(c columns, tw, rows int) string {
	heads := []struct {
		text string
		w    int
	}{{"", c.toggle}, {"SEGMENT", c.name}, {"SHOWS", c.shows}, {"SIDE", c.side}, {"FROM", c.from}}
	names := func(keep int) string {
		var b strings.Builder
		b.WriteString(strings.Repeat(" ", c.pad))
		for _, h := range heads[:keep] {
			b.WriteString(s.cell(h.text, "", h.w))
		}
		return strings.TrimRight(b.String(), " ")
	}
	n := len(s.m.cfg.Segments)
	if rows >= n {
		return s.tint(toneDim, names(len(heads)))
	}
	at := strconv.Itoa(s.m.sel+1) + "/" + strconv.Itoa(n)
	room := tw - c.pad
	keep := len(heads)
	for keep > 0 && s.th.Width(names(keep))+sideGap+s.th.Width(at) > room {
		keep--
	}
	return s.th.Spread(s.tint(toneDim, names(keep)), s.tint(toneDim, at), room)
}

// row is one segment's row of the table.
func (s screen) row(i int, c columns, tw int) string {
	seg := s.m.cfg.Segments[i]
	selected := i == s.m.sel
	check, checkTone := s.th.G.CheckOff, toneDim
	if seg.On {
		check, checkTone = s.th.G.CheckOn, toneOK
	}
	nameTone, tone := "", toneDim
	switch {
	case selected:
		nameTone, tone = toneAccent, ""
	case !seg.On:
		nameTone = toneDim
	}
	from, fromTone := s.from(seg)
	if fromTone == "" {
		fromTone = tone
	}
	line := strings.Repeat(" ", c.pad) +
		s.cell(check, checkTone, c.toggle) +
		s.cell(s.clean(seg.Name), nameTone, c.name) +
		s.cell(s.shows(seg), tone, c.shows) +
		s.cell(seg.Side.String(), tone, c.side) +
		s.cell(from, fromTone, c.from)
	if selected {
		return s.th.Selected(line, tw)
	}
	return s.th.Pad(line, tw)
}

// from says where a segment comes from, and the tone that says it too. A
// core segment has no tone of its own.
func (s screen) from(seg statusline.Segment) (text, tone string) {
	switch kindOf(seg) {
	case kindMod:
		return "mod " + s.clean(strings.TrimPrefix(seg.From, fromMod)), toneMod
	case kindShell:
		return statusline.FromShell, toneWarn
	case kindCore:
	}
	return s.clean(seg.From), ""
}

// shows is what a segment shows: its live value, or a sample when it has
// nothing to show right now.
func (s screen) shows(seg statusline.Segment) string {
	if v, ok := current(seg, s.m.live); ok {
		full, _ := seg.Text(v)
		return full
	}
	if v, ok := sampleOf(seg, s.core); ok {
		if full, _ := seg.Text(v); full != "" {
			return full
		}
	}
	return "no value"
}

// besideRows puts the settings to the right of the table's rows, after a
// column rule.
func (s screen) besideRows(left, right []string, tw int) []string {
	rule := s.tint(toneRule, s.th.G.Sep)
	out := make([]string, len(left))
	for i, line := range left {
		out[i] = s.th.Pad(line, tw) + rule
		if i < len(right) && right[i] != "" {
			out[i] += "  " + right[i]
		}
	}
	return out
}

// defaults returns the formats a segment takes when its own are empty.
func defaults(seg statusline.Segment) (format, short string) {
	switch kindOf(seg) {
	case kindCore:
		def, _ := coreDefault(seg)
		return def.Format, def.Short
	case kindMod:
		return "$text", "$short"
	case kindShell:
		return "$out", ""
	}
	return "", ""
}

// quoted draws a setting that is text, in quotes as the file holds it. An
// empty one shows what applies in its place, dim: the default, or "none".
func (s screen) quoted(value, fallback string) string {
	switch {
	case value != "":
		return `"` + s.clean(value) + `"`
	case fallback != "":
		return s.tint(toneDim, `"`+fallback+`"`)
	}
	return s.tint(toneDim, "none")
}

// detail draws the settings of the selected segment in rows of at most w
// cells: its name and fields, and then, to be left out where rows are short,
// the note on shell segments.
func (s screen) detail(w int) (fields, note []string) {
	seg := s.m.selected()
	if seg == nil || w <= 0 {
		return nil, nil
	}
	labels := labelWidth
	if w < 30 {
		labels--
	}
	row := func(f field, label, value string) string {
		tone := toneDim
		if s.m.ed != nil && s.m.ed.field() == f {
			tone = toneAccent
		}
		return s.th.Truncate(s.tint(tone, s.th.Pad(label, labels))+value, w)
	}
	k := kindOf(*seg)
	format, short := defaults(*seg)
	warn := s.tint(toneDim, "none")
	if seg.HasWarn {
		warn = s.tint(toneWarn, strconv.Itoa(seg.WarnBelow))
	}
	order := "never"
	if nth := statusline.DropOrdinal(s.m.cfg, s.m.sel); nth > 0 {
		order = ordinal(nth) + " when narrow"
	}

	name := s.tint(toneAccent, s.clean(seg.Name))
	if seg.Name == "" {
		name = s.tint(toneDim, "new segment")
	}
	fields = append(fields, s.th.Truncate(name, w))
	if k == kindShell {
		fields = append(fields,
			row(fieldCommand, "command", s.quoted(seg.Command, "")),
			row(fieldMatch, "match", s.quoted(seg.Match, "")))
	}
	fields = append(fields,
		row(fieldFormat, "format", s.quoted(seg.Format, format)),
		row(fieldShort, "short", s.quoted(seg.Short, short)),
		row(fieldWarn, "warn below", warn),
		row(fieldDrop, "drop order", order))
	if k == kindShell {
		fields = append(fields,
			row(fieldInterval, "interval", s.quoted(seg.Interval.String(), "")),
			row(fieldTimeout, "timeout", s.quoted(seg.Timeout.String(), "")))
	} else {
		fields = append(fields, row(0, "refresh", refresh(*seg)))
	}

	note = []string{"", s.th.Truncate(s.tint(toneDim, "A shell segment runs a command"), w)}
	at := slices.IndexFunc(s.m.cfg.Segments, func(o statusline.Segment) bool { return kindOf(o) == kindShell })
	if k == kindShell || at < 0 {
		return fields, append(note, s.th.Truncate(s.tint(toneDim, "on an interval, off the UI loop."), w))
	}
	// With another kind of segment selected, the first shell segment of
	// the list is the example.
	example := s.m.cfg.Segments[at]
	short3 := func(label, value string) string {
		return s.th.Truncate(s.tint(toneDim, s.th.Pad(label, labelWidth-2))+value, w)
	}
	return fields, append(note,
		s.th.Truncate(s.tint(toneDim, "on an interval, off the UI loop:"), w),
		"",
		short3("command", s.quoted(example.Command, "")),
		short3("interval", s.quoted(example.Interval.String(), "")),
		short3("timeout", s.quoted(example.Timeout.String(), "")))
}

// refresh says when a segment's value changes.
func refresh(seg statusline.Segment) string {
	if kindOf(seg) == kindMod {
		return "when its mod answers"
	}
	switch seg.Name {
	case "state":
		return "as the connection changes"
	case "contact", "route":
		return "with the contact"
	case "cache", "tokens", "latency":
		return "each model call"
	case "activity", "stop":
		return "as a reply runs"
	case "scroll":
		return "as you scroll"
	case "agents":
		return "as sub-agents come and go"
	case "clock":
		return "each minute"
	}
	return "each model call"
}

// ordinal writes a place in a row: 1st, 2nd, 3rd, 4th, 11th, 21st.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

// preview is one bar to draw: its label, the theme and the width it is drawn
// for, and what it is drawn from.
type preview struct {
	label string
	th    *theme.Theme
	cfg   statusline.Config
	vals  map[string]statusline.Value
	width int
}

// previews draws the three previews of a wide screen, each a block of rows:
// the bar at 100 columns under the heading, the bar at 60, and the bar of a
// terminal with no colour and ASCII glyphs. Each bar stands after its label.
func (s screen) previews() [][]string {
	vals := barValues(s.m.cfg, s.m.live, s.core)
	plain := s.m.plainTheme(s.th)
	fold := newFolder(s.th.G, plain.G)
	plainVals := fold.values(barValues(s.m.cfg, s.m.live, coreSamples(plain)))
	return [][]string{
		append([]string{s.tint(toneDim, "PREVIEW")},
			s.bar(preview{"100 columns", s.th, s.m.cfg, vals, previewWide})...),
		s.bar(preview{"60 columns", s.th, s.m.cfg, vals, previewNarrow}),
		s.bar(preview{"no colour", plain, fold.config(s.m.cfg), plainVals, previewWide}),
	}
}

// preview draws the one preview of a narrow screen: the bar at the screen's
// own width, with no label.
func (s screen) preview() []string {
	vals := barValues(s.m.cfg, s.m.live, s.core)
	return append([]string{s.tint(toneDim, "PREVIEW")},
		s.bar(preview{th: s.th, cfg: s.m.cfg, vals: vals, width: s.w})...)
}

// bar draws the status bar of a config as it is at one width, the way the
// client draws the real one: laid out by statusline.Layout and drawn by the
// theme. A label stands before the bar, in a column of its own.
//
// The cells are always the ones the bar holds at the preview's width. A bar
// wider than the room beside its label is drawn in that room, with less
// space between its two sides, as the mock-up draws its 100 columns in 88.
// Where the cells do not fit the room either, the bar is drawn at its true
// width and cut at the screen's edge.
func (s screen) bar(p preview) []string {
	left, right := statusline.Layout(p.cfg, p.vals, p.width, statusline.LayoutOptions{
		SepWidth: p.th.Width(p.th.G.Sep) + 2,
		MinGap:   sideGap,
		Measure:  p.th.Width,
	})
	label, room := "", s.w
	if p.label != "" {
		label, room = s.th.Pad(s.tint(toneDim, p.label), labelWidth), s.w-labelWidth
	}
	drawn := p.width
	if drawn > room && barFits(p.th, left, right, room) {
		drawn = room
	}
	rule, row := p.th.Bar(cells(left), cells(right), drawn)
	if drawn > room {
		row = p.th.Truncate(row, room)
	}
	var out []string
	if rule != "" {
		out = append(out, strings.Repeat(" ", s.th.Width(label))+p.th.Rule(min(drawn, room), ""))
	}
	return append(out, label+row)
}

// barFits says a bar of w cells holds these cells whole: both sides, the
// gap between them and the padding at the ends, as Theme.Bar counts them.
func barFits(th *theme.Theme, left, right []statusline.Cell, w int) bool {
	side := func(cells []statusline.Cell) int {
		total, n := 0, 0
		for _, c := range cells {
			if c.Text == "" {
				continue
			}
			total += th.Width(c.Text)
			n++
		}
		return total + max(n-1, 0)*(th.Width(th.G.Sep)+2)
	}
	l, r := side(left), side(right)
	gap := 0
	if l > 0 && r > 0 {
		gap = sideGap
	}
	return l+gap+r+2*statusline.Padding(w) <= w
}

// cells hands the layout's cells to the theme.
func cells(in []statusline.Cell) []theme.Cell {
	out := make([]theme.Cell, 0, len(in))
	for _, c := range in {
		out = append(out, theme.Cell{Text: c.Text, Tone: c.Tone})
	}
	return out
}

// presets is the row that names the presets, the current one underlined.
func (s screen) presets() string {
	var b strings.Builder
	b.WriteString(s.tint(toneDim, "presets"))
	for _, name := range statusline.Presets() {
		b.WriteString("  ")
		if name == s.m.preset {
			name = s.th.Style(toneAccent).Underline(true).Render(name)
		}
		b.WriteString(name)
	}
	return b.String()
}

// keyRow is the bottom row: the keys, or the note about the last one. Where
// the keys do not all fit, the ones a person can best do without go first.
func (s screen) keyRow() string {
	if s.m.note != "" {
		return s.tint(s.m.tone, s.clean(s.m.note))
	}
	g := s.th.G
	keys := []struct {
		text string
		// keep is the key's place in the order they are kept in: 1 stays
		// longest.
		keep int
	}{
		{"space toggle", 3},
		{g.LeftRight + " side", 5},
		{"J K reorder", 7},
		{"e edit", 4},
		{"a add", 8},
		{"d delete", 9},
		{"p preset", 6},
		{"s save", 2},
		{"esc back", 1},
	}
	row := ""
	for limit := len(keys); limit >= 1; limit-- {
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			if k.keep <= limit {
				parts = append(parts, k.text)
			}
		}
		row = strings.Join(parts, " "+g.Mid+" ")
		if s.th.Width(row) <= s.w {
			break
		}
	}
	return s.tint(toneDim, row)
}

// inputRow is the row a field is edited in: the field's name, the prompt,
// and as much of the value as fits around the cursor. It returns the
// cursor's column too.
func (s screen) inputRow() (row string, cursor int) {
	e := s.m.ed
	prefix := s.tint(toneDim, e.field().label()) + " " + s.tint(toneAccent, s.th.G.Prompt) + " "
	if s.th.Width(prefix) > s.w/2 {
		prefix = s.tint(toneAccent, s.th.G.Prompt) + " "
	}
	if s.th.Width(prefix) > s.w/2 {
		prefix = ""
	}
	before := s.th.Width(prefix)
	// One cell is kept free, so that the cursor has a place after the last
	// character.
	shown, at := e.window(s.w-before-1, s.th.Width)
	return prefix + safe.Clean(shown, false), before + at
}

// fieldHints is the row under the input: the keys and what the field takes,
// or why what was typed was refused.
func (s screen) fieldHints() string {
	e := s.m.ed
	if e.problem != "" {
		return s.tint(toneBad, s.clean(e.problem))
	}
	g := s.th.G
	keys := strings.Join([]string{"enter accept", g.UpDown + " field", "esc cancel"}, " "+g.Mid+" ")
	help := ""
	if seg := s.m.selected(); seg != nil {
		help = e.field().help(*seg, s.variables(*seg))
	}
	return s.th.Spread(s.tint(toneDim, keys), s.tint(toneDim, help), s.w)
}

// variables lists what a segment's formats may name, as "$pct $cached".
func (s screen) variables(seg statusline.Segment) string {
	switch kindOf(seg) {
	case kindMod:
		return "$text $short"
	case kindShell:
		return "$out and $1 to $9"
	case kindCore:
	}
	v, _ := sampleOf(seg, s.core)
	names := make([]string, 0, len(v.Vars))
	for name := range v.Vars {
		names = append(names, "$"+name)
	}
	slices.Sort(names)
	return strings.Join(names, " ")
}
