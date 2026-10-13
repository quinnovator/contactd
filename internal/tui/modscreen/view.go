package modscreen

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/tui/mods"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

const (
	// wideFrom is the width from which the list and the details stand side
	// by side. Under it the details take the list's place while they have
	// the focus.
	wideFrom = 80
	// listWidth is the width of the list on a wide screen.
	listWidth = 50
	// versionFrom is the width from which the list has a version column.
	versionFrom = 40
	// sideGap is the least space between the two ends of the header and of
	// the key row.
	sideGap = 2
	// maxNoteRows is the most rows the tmux line takes on a narrow screen,
	// and the most a note from the root takes.
	maxNoteRows = 3
)

// The tones the screen draws in, by the names theme.Theme.Style takes.
const (
	tonePlain  = ""
	toneDim    = "dim"
	toneAccent = "accent"
	toneOK     = "ok"
	toneWarn   = "warn"
	toneBad    = "bad"
	toneMod    = "mod"
	toneRule   = "rule"
)

// The fixed text of the screen.
const (
	title       = "Mods"
	tmuxLine    = "images are half blocks here: tmux allow-passthrough is off"
	noMods      = "no mods installed"
	inputLabel  = "install from "
	placeholder = "a directory that holds a mod.toml"
	howInstall  = "press i and give the path of a directory that holds a mod.toml and its module"
)

// The key hints.
const (
	hintToggle  = "space on or off"
	hintDetails = "enter details"
	hintInstall = "i install from a path"
	hintUpdate  = "u update"
	hintBack    = "esc back"
	hintList    = "esc list"
	// hintInstallShort stands for hintInstall where that does not fit and
	// there is no mod to do anything else with.
	hintInstallShort = "i install"
)

// pen draws text for one theme. Text that came from outside the client (a
// manifest, a failure, a path) passes clean before it is measured or drawn.
type pen struct {
	th    *theme.Theme
	ascii bool
}

// newPen makes the pen of a theme.
func newPen(th *theme.Theme) pen {
	return pen{th: th, ascii: th.Caps.Glyphs != termcaps.GlyphsUnicode}
}

// clean makes outside text one line that is safe to draw.
func (p pen) clean(s string) string {
	return safe.Line(s, mods.MaxSpanRunes, p.ascii)
}

// tone draws text in a tone. Empty text stays empty, so no style is opened
// around nothing.
func (p pen) tone(tone, s string) string {
	if s == "" {
		return ""
	}
	return p.th.Style(tone).Render(s)
}

// cut draws text in a tone, cut to at most w cells. The text is cut before it
// is styled, so the style ends with it.
func (p pen) cut(tone, s string, w int) string {
	return p.tone(tone, p.th.Truncate(s, w))
}

// cell draws one table cell of exactly w cells: the text in a tone, cut so
// that at least one space follows it.
func (p pen) cell(tone, s string, w int) string {
	if w <= 0 {
		return ""
	}
	return p.th.Pad(p.cut(tone, s, w-1), w)
}

// join joins key hints, or the parts of one line, with the theme's
// separator.
func (p pen) join(parts ...string) string {
	return strings.Join(parts, " "+p.th.G.Mid+" ")
}

// plan says which rows the screen has at one height.
type plan struct {
	header, rule, gap bool
	// keys is the number of rows the key hints get, or what stands in
	// their place. tmux is the number the tmux line gets: all it needs, or
	// none.
	keys, tmux int
	// body is the number of rows the list and the details get.
	body int
}

// planFor shares out the rows. keys is the number of rows the key hints, the
// input or a note needs, and tmux the number the tmux line needs, 0 when it
// is not shown. The list is the last thing to go: the blank row, the tmux
// line, the rule, the header and the key hints give way to it in that order
// as the screen gets shorter. With must set the key rows stay at any height:
// an open input and a note are never the part that is dropped.
func planFor(height, keys, tmux int, must bool) plan {
	p := plan{
		header: height >= 5,
		rule:   height >= 7,
		gap:    height >= 12,
	}
	if must || height >= 3 {
		p.keys = min(keys, height)
	}
	if height >= 8+tmux {
		p.tmux = tmux
	}
	used := p.keys + p.tmux
	for _, on := range []bool{p.header, p.rule, p.gap} {
		if on {
			used++
		}
	}
	p.body = max(0, height-used)
	return p
}

// rowsOf draws one sentence in a tone, in as many rows as it takes at w
// cells and at most maxNoteRows. It is cut only where that many rows do not
// hold it.
func rowsOf(p pen, tone, s string, w int) []string {
	lines := p.th.Wrap(s, w)
	if len(lines) > maxNoteRows {
		lines[maxNoteRows-1] = p.th.Truncate(strings.Join(lines[maxNoteRows-1:], " "), w)
		lines = lines[:maxNoteRows]
	}
	for i, line := range lines {
		lines[i] = p.tone(tone, line)
	}
	return lines
}

// View draws the screen in exactly height rows of exactly width cells. The
// cursor is nil unless the install input is open, and then it is where the
// next character goes.
//
// From 80 columns the list is on the left and the selected mod's details on
// the right. Under 80 the list has the whole width until enter gives the
// details the focus. View remembers the width, which is how Update knows
// what enter and esc mean.
//
// A short screen loses, in this order, the blank row, the tmux line, the
// rule, the header and the key row; the list, an open input and a note stay.
// The tmux line is the last thing on the screen, in up to three rows where
// one does not hold it.
func (m *Model) View(th *theme.Theme, width, height int) (content string, cursor *tea.Cursor) {
	m.setWidth(width)
	if width <= 0 || height <= 0 {
		return "", nil
	}
	p := newPen(th)
	var tmux []string
	if m.tmuxNote {
		tmux = rowsOf(p, toneDim, tmuxLine, width)
	}
	keys, x, must := m.bottom(p, width)
	pl := planFor(height, len(keys), len(tmux), must)
	rows := make([]string, 0, height)
	if pl.header {
		rows = append(rows, m.header(p, width))
	}
	if pl.rule {
		rows = append(rows, th.Rule(width, ""))
	}
	if pl.gap {
		rows = append(rows, "")
	}
	rows = append(rows, m.body(p, width, pl.body)...)
	if m.typing {
		cursor = tea.NewCursor(x, len(rows))
		cursor.Blink = false
	}
	rows = append(rows, keys[:pl.keys]...)
	rows = append(rows, tmux[:pl.tmux]...)
	for i := range rows {
		rows[i] = th.Pad(rows[i], width)
	}
	return strings.Join(rows, "\n"), cursor
}

// header draws the first row: the title, and at the right the mods directory
// and how many mods are on and off. The directory goes first when the row is
// too narrow for both, then the count.
func (m *Model) header(p pen, w int) string {
	th := p.th
	on := 0
	for _, in := range m.infos {
		if isOn(in.Status) {
			on++
		}
	}
	count := strconv.Itoa(on) + " on, " + strconv.Itoa(len(m.infos)-on) + " off"
	rights := []string{count}
	if dir := p.clean(m.dirLabel); dir != "" {
		rights = []string{p.join(dir, count), count}
	}
	left := th.Title().Render(title)
	for _, right := range rights {
		if th.Width(title)+sideGap+th.Width(right) <= w {
			return th.Spread(left, p.tone(toneDim, right), w)
		}
	}
	return left
}

// isOn reports whether a mod is switched on: running, or about to be loaded.
// A mod that failed or was switched off by the host is not.
func isOn(s mods.Status) bool {
	return s == mods.StatusOn || s == mods.StatusLoading
}

// body draws the rows between the header and the key row: exactly h of them.
func (m *Model) body(p pen, w, h int) []string {
	if h <= 0 {
		return nil
	}
	th := p.th
	if w < wideFrom {
		if !m.detail {
			return m.listPane(p, w, h, true)
		}
		// One cell of margin, as the list has.
		inner, margin := w-2, " "
		if inner < 1 {
			inner, margin = w, ""
		}
		rows := make([]string, h)
		for i, line := range m.detailPane(p, inner, h) {
			rows[i] = margin + line
		}
		return rows
	}
	left := m.listPane(p, listWidth, h, false)
	rule := p.tone(toneRule, th.G.Sep)
	right := m.detailPane(p, w-listWidth-th.Width(th.G.Sep)-2, h)
	rows := make([]string, h)
	for i := range rows {
		rows[i] = left[i] + rule
		if i < len(right) && right[i] != "" {
			rows[i] += "  " + right[i]
		}
	}
	return rows
}

// columns are the widths of the list's columns. Each but the last ends in at
// least one space.
type columns struct{ toggle, name, version, adds int }

// columnsFor fits the list's columns into w cells, a cell of margin on both
// sides. The name column grows for a long name; under 40 columns there is no
// version column; what is left is the last column's. With names of up to
// seven letters that is 25 cells of the 50 a wide screen gives the list, which
// is what "renderer for code:mermaid" takes.
func (m *Model) columnsFor(p pen, w int) columns {
	th := p.th
	c := columns{
		toggle: th.Width(th.G.Pointer) + 1 + max(th.Width(th.G.CheckOn), th.Width(th.G.CheckOff)) + 1,
		name:   9,
	}
	for _, in := range m.infos {
		c.name = max(c.name, th.Width(p.clean(in.Manifest.Name))+2)
	}
	rest := w - 2 - c.toggle
	c.name = max(0, min(c.name, 18, rest))
	rest -= c.name
	if w >= versionFrom && rest >= 8 {
		c.version = 8
		rest -= c.version
	}
	if rest >= 4 {
		c.adds = rest
	}
	return c
}

// listPane draws the list in exactly h rows of exactly w cells: the column
// names, the mods, and under them what a mod can add, when there is room for
// all of it. With one row there is only the selected mod.
//
// alone says the details are not beside the list. An empty list then says
// itself how a mod is installed, which the details say otherwise.
func (m *Model) listPane(p pen, w, h int, alone bool) []string {
	th := p.th
	c := m.columnsFor(p, w)
	rows := make([]string, 0, h)
	add := func(row string) { rows = append(rows, th.Pad(row, w)) }
	if h >= 2 {
		add(" " + strings.Repeat(" ", c.toggle) +
			p.cell(toneDim, "MOD", c.name) + p.cell(toneDim, "VERSION", c.version) + p.cut(toneDim, "ADDS", c.adds))
	}
	room := h - len(rows)
	shown := min(len(m.infos), room)
	// Too many mods for the rows: the window follows the selected one.
	top := max(0, min(m.sel-shown/2, len(m.infos)-shown))
	for i := top; i < top+shown; i++ {
		add(m.modRow(p, c, i, w))
	}
	if len(m.infos) == 0 && room > 0 {
		add(" " + p.cut(toneDim, noMods, w-2))
		// Under 12 cells the sentence would be a column of syllables.
		if text := min(w-2, measure); alone && text >= 12 {
			for _, line := range p.wrap(toneDim, howInstall, text) {
				if len(rows) == h {
					break
				}
				add(" " + line)
			}
		}
	}
	if key := legend(p); h-len(rows) > len(key) {
		add("")
		for _, row := range key {
			add(row)
		}
	}
	for len(rows) < h {
		add("")
	}
	return rows
}

// modRow draws one mod's row of the list. The selected row has the pointer
// and is drawn with theme.Selected, which fills it to w cells.
func (m *Model) modRow(p pen, c columns, i, w int) string {
	th := p.th
	in := m.infos[i]
	selected := i == m.sel
	on := isOn(in.Status)

	pointer := strings.Repeat(" ", th.Width(th.G.Pointer))
	if selected {
		pointer = p.tone(toneAccent, th.G.Pointer)
	}
	check := p.tone(toneDim, th.G.CheckOff)
	if on {
		check = p.tone(toneOK, th.G.CheckOn)
	}
	nameTone, restTone := tonePlain, toneDim
	switch {
	case selected:
		nameTone, restTone = toneAccent, tonePlain
	case !on:
		nameTone = toneDim
	}
	adds, addsTone := addsCell(p, in)
	if addsTone == "" {
		addsTone = restTone
	}
	row := " " + th.Pad(pointer+" "+check, c.toggle) +
		p.cell(nameTone, p.clean(in.Manifest.Name), c.name) +
		p.cell(restTone, p.clean(in.Manifest.Version), c.version) +
		p.cut(addsTone, adds, c.adds)
	if selected {
		return th.Selected(row, w)
	}
	return row
}

// addsCell is the last cell of a mod's row: what the mod adds, or what
// became of it when it is not simply on or off. tone is "" for the tone of
// the rest of the row.
func addsCell(p pen, in mods.Info) (text, tone string) {
	switch in.Status {
	case mods.StatusLoading:
		return "loading" + p.th.G.Ellipsis, toneDim
	case mods.StatusFailed:
		return "failed", toneBad
	case mods.StatusDisabled:
		return "switched off", toneBad
	case mods.StatusOff, mods.StatusOn:
	}
	man := in.Manifest
	parts := make([]string, 0, 1+len(man.Commands)+len(man.Renderers))
	switch n := len(man.Segments); {
	case n == 1:
		parts = append(parts, "segment")
	case n > 1:
		parts = append(parts, strconv.Itoa(n)+" segments")
	}
	for _, c := range man.Commands {
		parts = append(parts, "/"+p.clean(c.Name))
	}
	for _, r := range man.Renderers {
		parts = append(parts, "renderer for "+p.clean(r.Kind))
	}
	if len(parts) == 0 {
		return "nothing", ""
	}
	return p.join(parts...), ""
}

// legend is the block under the list that says what a mod can add.
func legend(p pen) []string {
	kinds := [][2]string{
		{"segment", "one cell group in the status line"},
		{"command", "a /name run from the input box"},
		{"renderer", "how one kind of item is drawn"},
	}
	rows := make([]string, 0, 1+len(kinds))
	rows = append(rows, " "+p.tone(toneDim, "A MOD CAN ADD"))
	for _, k := range kinds {
		rows = append(rows, " "+p.cell(toneMod, k[0], kindWidth+1)+p.tone(toneDim, k[1]))
	}
	return rows
}

// bottom draws what stands under the list: the install input while it is
// open, else a note from the root, else the key hints. x is where the cursor
// goes in an open input. must says the rows are not hints, and so are kept
// on a screen too short for hints.
func (m *Model) bottom(p pen, w int) (rows []string, x int, must bool) {
	if m.typing {
		row, x := m.inputRow(p, w)
		return []string{row}, x, true
	}
	if note := p.clean(m.note); note != "" {
		return rowsOf(p, m.noteTone, note, w), 0, true
	}
	return []string{m.keyRow(p, w)}, 0, false
}

// keyRow draws the row of key hints.
func (m *Model) keyRow(p pen, w int) string {
	// What stands at the left, widest first. Updating goes first, then
	// installing. Of the last two the way to the details stays: on a screen
	// this narrow it is the only way to see what a mod may read.
	lefts := [][]string{
		{hintToggle, hintDetails, hintInstall, hintUpdate},
		{hintToggle, hintDetails, hintInstall},
		{hintToggle, hintDetails},
		{hintDetails},
	}
	right := hintBack
	switch {
	case len(m.infos) == 0:
		lefts = [][]string{{hintInstall}, {hintInstallShort}}
	case m.narrow() && m.detail:
		lefts, right = [][]string{{hintToggle, hintUpdate}, {hintToggle}}, hintList
	}
	return hintRow(p, lefts, right, w)
}

// hintRow draws key hints at the left of a row and one at its right. lefts
// are the choices for the left, and the first that fits is drawn. Where none
// does, the hint at the right stands alone.
func hintRow(p pen, lefts [][]string, right string, w int) string {
	th := p.th
	rw := th.Width(right)
	for _, left := range lefts {
		l := p.join(left...)
		if th.Width(l)+sideGap+rw <= w {
			return th.Spread(p.tone(toneDim, l), p.tone(toneDim, right), w)
		}
	}
	if rw > w {
		return p.cut(toneDim, right, w)
	}
	return strings.Repeat(" ", w-rw) + p.tone(toneDim, right)
}

// inputRow draws the open install input: a label, the field, and its two
// keys at the right. The keys go first when the row is narrow, then the
// label, so the field keeps room to type in.
func (m *Model) inputRow(p pen, w int) (row string, x int) {
	th := p.th
	label, keys := inputLabel, p.join("enter install", "esc cancel")
	field := w
	if lw := th.Width(label); field-lw >= 12 {
		field -= lw
	} else {
		label = ""
	}
	if kw := th.Width(keys) + sideGap; field-kw >= 24 {
		field -= kw
	} else {
		keys = ""
	}
	text, x := m.field(p, field)
	row = p.tone(toneDim, label) + text
	if keys != "" {
		row += strings.Repeat(" ", sideGap) + p.tone(toneDim, keys)
	}
	return row, th.Width(label) + x
}

// field draws what was typed in exactly w cells, and returns the cell the
// cursor is on. A path longer than the field is shown from the left while
// the cursor is in its first cells, and otherwise so that the cursor stands
// at the right edge.
func (m *Model) field(p pen, w int) (text string, x int) {
	th := p.th
	runes := []rune(m.input.Value())
	if len(runes) == 0 {
		return th.Pad(p.cut(toneDim, placeholder, w), w), 0
	}
	pos := max(0, min(m.input.Position(), len(runes)))
	// A cleaned character is still one character, or gone: the cursor is
	// counted over what is drawn.
	before := []rune(safe.Line(string(runes[:pos]), 4*maxPath, p.ascii))
	after := safe.Line(string(runes[pos:]), 4*maxPath, p.ascii)
	start := len(before)
	for start > 0 && th.Width(string(before[start-1:])) < w {
		start--
	}
	head := string(before[start:])
	return th.Pad(th.Truncate(head+after, w), w), min(th.Width(head), max(0, w-1))
}
