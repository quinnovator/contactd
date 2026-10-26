package sledit_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/tui/sledit"
	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// The mock-up: the drawing's segments at 100 by 30, cache selected.
func TestGoldenDrawing(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	m := newDrawing(t, th)
	content, cursor := m.View(th, 100, 30)
	if cursor != nil {
		t.Errorf("cursor is %+v with no field open, want nil", cursor)
	}
	tuitest.Golden(t, "drawing-100x30.golden", plain(content))
	tuitest.Golden(t, "drawing-100x30.styled.golden", quoted(content))
}

// The same segments in the other shapes the screen takes.
func TestGoldenShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		caps          termcaps.Caps
		width, height int
		keys          []tea.Msg
	}{
		{"drawing-120x30", trueDark(), 120, 30, nil},
		{"drawing-80x24", trueDark(), 80, 24, nil},
		{"drawing-79x30", trueDark(), 79, 30, nil},
		{"drawing-40x16", trueDark(), 40, 16, nil},
		{"drawing-ascii-100x30", plainASCII(), 100, 30, nil},
		{"field-100x30", trueDark(), 100, 30, []tea.Msg{char('e'), down(), down()}},
		{"shell-100x30", trueDark(), 100, 30, []tea.Msg{down(), down(), down(), down(), down(), down(), down()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			th := themeOf(tt.caps)
			m := newDrawing(t, th)
			send(t, m, tt.keys...)
			content, _ := m.View(th, tt.width, tt.height)
			tuitest.Golden(t, tt.name+".golden", plain(content))
		})
	}
}

// Forty segments, the 21st selected: the table scrolls and says where it is.
func TestGoldenForty(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	for _, size := range [][2]int{{100, 30}, {80, 24}, {40, 16}} {
		m := sledit.New(forty(t), path, live(th))
		send(t, m, slices.Repeat([]tea.Msg{down()}, 20)...)
		content, _ := m.View(th, size[0], size[1])
		tuitest.Golden(t, "forty-"+strconv.Itoa(size[0])+"x"+strconv.Itoa(size[1])+".golden", plain(content))
	}
}

// The rows of the mock-up, asserted as written so that an updated golden
// file cannot move them.
func TestDrawingRows(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	m := newDrawing(t, th)
	rows := screen(t, m, th, 100, 30)
	if len(rows) != 30 {
		t.Fatalf("%d rows, want 30", len(rows))
	}
	for i, row := range rows {
		if w := th.Width(row); w > 100 {
			t.Errorf("row %d is %d cells wide: %q", i, w, row)
		}
	}
	title := "Status line"
	if want := title + strings.Repeat(" ", 100-len(title)-len(path)) + path; rows[0] != want {
		t.Errorf("header is\n%q, want\n%q", rows[0], want)
	}
	if want := strings.Repeat("─", 100); rows[1] != want {
		t.Errorf("row 1 is %q, want a rule", rows[1])
	}
	// The table is 56 columns wide; the settings follow a column rule.
	want := []string{
		"     SEGMENT    SHOWS               SIDE   FROM         │  cache",
		" [x] state      ● live              left   core         │  format      \"cache $pct%\"",
		" [x] contact    ada                 left   core         │  short       \"$pct%\"",
		" [x] route      deepseek-flash      left   core         │  warn below  50",
		" [x] cache      cache 82%           left   core         │  drop order  3rd when narrow",
		" [ ] tokens     3.7k tok            left   core         │  refresh     each model call",
		" [x] latency    7.2s                left   core         │",
		" [x] activity   running python      right  core         │  A shell segment runs a command",
		" [x] stop       esc stop            right  core         │  on an interval, off the UI loop:",
		" [x] scroll     ↓ newer             right  core         │",
		" [ ] agents     1 sub-agent         right  core         │  command   \"uptime\"",
		" [ ] load       load 0.42           right  shell        │  interval  \"10s\"",
		" [x] branch     ⎇ main              right  mod git      │  timeout   \"1s\"",
		" [x] clock      12:04               right  core         │",
	}
	if got := rows[3 : 3+len(want)]; !slices.Equal(got, want) {
		t.Errorf("the table is\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	gap := func(left, right string, w int) string {
		return left + strings.Repeat(" ", w-utf8.RuneCountInString(left)-utf8.RuneCountInString(right)) + right
	}
	// Each bar stands after its label, as drawn. The 100-column bars hold
	// what a bar of 100 columns holds, in the 88 cells beside the label.
	previews := []string{
		"PREVIEW",
		"100 columns " + gap(" ● live │ ada │ deepseek-flash │ cache 82% │ 7.2s", "⎇ main │ 12:04 ", 88),
		// 66 cells are needed; without the branch 57, and 57 fits 60.
		"60 columns  " + gap(" ● live │ ada │ deepseek-flash │ cache 82% │ 7.2s", "12:04 ", 60),
		strings.Repeat(" ", 12) + strings.Repeat("-", 88),
		"no colour   " + gap(" * live | ada | deepseek-flash | cache 82% | 7.2s", "main | 12:04 ", 88),
	}
	at := slices.Index(rows, "PREVIEW")
	if at < 0 || !slices.Equal(rows[at:at+len(previews)], previews) {
		t.Errorf("the previews are\n%s\nwant\n%s", strings.Join(rows[max(at, 0):], "\n"), strings.Join(previews, "\n"))
	}
	// A blank row stands over the table and one under it, as drawn, and
	// the rows left over are above the presets.
	if rows[2] != "" || rows[at-1] != "" {
		t.Errorf("the rows around the table are %q and %q, want both empty", rows[2], rows[at-1])
	}
	for i := at + len(previews); i < 28; i++ {
		if rows[i] != "" {
			t.Errorf("row %d is %q, want it empty", i, rows[i])
		}
	}
	if got, want := rows[28], "presets  minimal  default  ops"; got != want {
		t.Errorf("row 28 is %q, want %q", got, want)
	}
	keys := "space toggle · ←→ side · J K reorder · e edit · a add · d delete · p preset · s save · esc back"
	if rows[29] != keys {
		t.Errorf("row 29 is %q, want %q", rows[29], keys)
	}
}

// What the styles say, on the mock-up's screen: the selected row is filled
// and its name is in the accent tone, a switch that is on is in the ok tone,
// and the sources have their tones.
func TestDrawingStyles(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	m := newDrawing(t, th)
	content, _ := m.View(th, 100, 30)
	rows := strings.Split(content, "\n")
	find := func(text string) string {
		t.Helper()
		for _, row := range rows {
			if strings.Contains(plainText(row), text) {
				return row
			}
		}
		t.Fatalf("no row holds %q", text)
		return ""
	}
	has := func(row, part, what string) {
		t.Helper()
		if !strings.Contains(row, part) {
			t.Errorf("%s: %q is not in %q", what, part, row)
		}
	}
	cache := find("[x] cache")
	selected := th.Selected(" x", 56)
	fill := selected[:strings.Index(selected, " x")]
	if fill == "" || !strings.HasPrefix(cache, fill) {
		t.Errorf("the selected row does not start with the bar's fill %q: %q", fill, cache)
	}
	has(cache, th.Style("accent").Render("cache"), "the selected name")
	has(cache, th.Style("ok").Render("[x]"), "a switch that is on")
	if state := find("[x] state"); strings.HasPrefix(state, fill) {
		t.Errorf("a row that is not selected is filled: %q", state)
	}
	has(find("[ ] tokens"), th.Style("dim").Render("[ ]"), "a switch that is off")
	has(find("[ ] tokens"), th.Style("dim").Render("tokens"), "the name of a segment that is off")
	has(find("[x] branch"), th.Style("mod").Render("mod git"), "a mod's source")
	has(find("[ ] load"), th.Style("warn").Render("shell"), "the shell's source")
	has(find("warn below"), th.Style("warn").Render("50"), "the warn threshold")
	has(rows[0], th.Title().Render("Status line"), "the title")
	has(rows[0], th.Style("dim").Render(path), "the path")
	has(rows[28], th.Style("accent").Underline(true).Render("default"), "the current preset")
	// The 100 and 60 column bars are the theme's own; the last is drawn
	// with no colour at all.
	plainBar := find(" * live | ada")
	label := th.Style("dim").Render("no colour")
	has(plainBar, label, "the label of the no-colour bar")
	_, plainBar, _ = strings.Cut(plainBar, label)
	for _, part := range strings.Split(plainBar, "\x1b[")[1:] {
		code := part[:strings.IndexByte(part, 'm')]
		if !slices.Contains([]string{"", "1", "2", "22"}, code) {
			t.Errorf("the no-colour bar has the style %q in it: %q", code, plainBar)
		}
	}
	narrow := find(" ● live │ ada │ deepseek-flash │ cache 82% │ 7.2s     12:04")
	has(narrow, th.Style("dim").Render("60 columns")+"  "+fill, "the 60-column bar's label and fill")
}

// The sizes of the screen: every row fits, and there are exactly as many as
// the terminal has.
func TestSizes(t *testing.T) {
	t.Parallel()
	widths := []int{200, 120, 112, 111, 100, 99, 80, 79, 60, 59, 56, 55, 41, 40, 39, 30, 20, 19, 10, 5, 2, 1}
	heights := []int{60, 30, 24, 16, 12, 8, 6, 5, 4, 3, 2, 1}
	states := []struct {
		name string
		keys []tea.Msg
	}{
		{"the table", nil},
		{"the last segment", slices.Repeat([]tea.Msg{down()}, 20)},
		{"a shell segment", slices.Repeat([]tea.Msg{down()}, 7)},
		{"a note", []tea.Msg{char('d')}},
		{"unsaved, asked", []tea.Msg{space(), esc()}},
		{"a field", []tea.Msg{char('e')}},
		{"a refused field", append(append([]tea.Msg{char('e'), down(), down(), down()}, typed("never")...), enter())},
		{"a new segment", []tea.Msg{char('a')}},
		{"a long value", append([]tea.Msg{char('e')}, typed(strings.Repeat("0123456789 ", 30))...)},
	}
	for _, term := range terminals() {
		for _, state := range states {
			t.Run(term.name+", "+state.name, func(t *testing.T) {
				t.Parallel()
				th := themeOf(term.caps)
				m := newDrawing(t, th)
				send(t, m, state.keys...)
				for _, width := range widths {
					for _, height := range heights {
						checkScreen(t, m, th, width, height)
					}
				}
			})
		}
	}
}

// checkScreen draws the editor and checks what holds at every size: as many
// rows as the terminal has, none too wide, nothing in them a terminal acts
// on but styles, and a cursor exactly when a field is open, inside the
// screen.
func checkScreen(t *testing.T, m *sledit.Model, th *theme.Theme, width, height int) {
	t.Helper()
	content, cursor := m.View(th, width, height)
	rows := strings.Split(content, "\n")
	if len(rows) != height {
		t.Errorf("%dx%d: %d rows", width, height, len(rows))
	}
	for i, row := range rows {
		if w := th.Width(row); w > width {
			t.Errorf("%dx%d: row %d is %d cells wide: %q", width, height, i, w, plainText(row))
		}
		if bad := unsafeIn(row); bad != "" {
			t.Errorf("%dx%d: row %d has %s: %q", width, height, i, bad, row)
		}
	}
	switch {
	case m.Editing() && cursor == nil:
		t.Errorf("%dx%d: no cursor while a field is open", width, height)
	case !m.Editing() && cursor != nil:
		t.Errorf("%dx%d: a cursor with no field open", width, height)
	case cursor != nil:
		if cursor.X < 0 || cursor.X >= width || cursor.Y < 0 || cursor.Y >= height {
			t.Errorf("%dx%d: the cursor is at %d,%d", width, height, cursor.X, cursor.Y)
		}
		if cursor.Blink {
			t.Errorf("%dx%d: the cursor blinks", width, height)
		}
	}
}

// unsafeIn names the first thing in a row that a terminal would act on and
// that is not a style: a control character, or an escape sequence other than
// "set graphic rendition".
func unsafeIn(row string) string {
	for i := 0; i < len(row); {
		r, n := utf8.DecodeRuneInString(row[i:])
		switch {
		case r == 0x1b:
			end := i + 2
			for end < len(row) && (row[end] >= '0' && row[end] <= '9' || row[end] == ';' || row[end] == ':') {
				end++
			}
			if !strings.HasPrefix(row[i:], "\x1b[") || end >= len(row) || row[end] != 'm' {
				return "an escape sequence that is not a style"
			}
			n = end + 1 - i
		case r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0:
			return "the control character " + strconv.QuoteRune(r)
		case r == utf8.RuneError && n == 1:
			return "a byte that is not UTF-8"
		case r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069:
			return "the direction control " + strconv.QuoteRune(r)
		}
		i += n
	}
	return ""
}

// The widths the work package names, at the mock-up's height, for the four
// terminals: no row wider than the screen. TestSizes covers them too; this
// states them.
func TestNoRowWiderThanTheScreen(t *testing.T) {
	t.Parallel()
	for _, term := range terminals() {
		for _, width := range []int{100, 80, 79, 60, 40, 30} {
			t.Run(term.name+", "+strconv.Itoa(width), func(t *testing.T) {
				t.Parallel()
				th := themeOf(term.caps)
				m := newDrawing(t, th)
				rows := screen(t, m, th, width, 30)
				if len(rows) != 30 {
					t.Errorf("%d rows, want 30", len(rows))
				}
				for i, row := range rows {
					if w := th.Width(row); w > width {
						t.Errorf("row %d is %d cells wide, over %d: %q", i, w, width, row)
					}
				}
				// The rows that fill the screen do fill it.
				if w := th.Width(rows[0]); w != width {
					t.Errorf("the header is %d cells wide, want %d", w, width)
				}
				if w := th.Width(rows[1]); w != width {
					t.Errorf("the rule is %d cells wide, want %d", w, width)
				}
			})
		}
	}
}

// With ASCII glyphs and ASCII text, every byte of the screen is ASCII.
func TestASCIIScreen(t *testing.T) {
	t.Parallel()
	th := themeOf(plainASCII())
	states := [][]tea.Msg{nil, {char('e')}, {char('a')}, {space(), esc()}, {char('d')}}
	for i, keys := range states {
		m := newDrawing(t, th)
		send(t, m, keys...)
		for _, width := range []int{120, 100, 80, 79, 60, 40, 30, 20, 10} {
			for _, height := range []int{30, 16, 12, 8, 5} {
				content, _ := m.View(th, width, height)
				if at := strings.IndexFunc(content, func(r rune) bool { return r >= 0x80 }); at >= 0 {
					t.Errorf("state %d at %dx%d: a byte over 0x7F at %d: %q", i, width, height, at, content[max(at-20, 0):min(at+20, len(content))])
				}
			}
		}
	}
	rows := screen(t, newDrawing(t, th), th, 100, 30)
	// The ASCII names of the arrow keys are longer, so one key gives way.
	want := "space toggle | left/right side | J K reorder | e edit | a add | p preset | s save | esc back"
	if got := rows[29]; got != want {
		t.Errorf("the key row is %q, want %q", got, want)
	}
	if got := rowWith(t, rows, "[x] cache"); !strings.HasPrefix(got, " [x] cache      cache 82%           left   core         |  ") {
		t.Errorf("the cache row is %q", got)
	}
}

// From 80 columns the settings stand beside the table and three previews are
// drawn. Under 80 the settings go below the table and one preview is drawn,
// at the screen's own width.
func TestNarrowLayout(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	m := newDrawing(t, th)

	wide := screen(t, m, th, 80, 40)
	if got := rowWith(t, wide, "SEGMENT"); !strings.HasSuffix(got, "│  cache") {
		t.Errorf("at 80 columns the heading row is %q, want the settings beside it", got)
	}
	labelled := func(rows []string, label string) bool {
		return slices.ContainsFunc(rows, func(r string) bool { return strings.HasPrefix(r, label) })
	}
	for _, label := range []string{"100 columns", "60 columns", "no colour"} {
		if !labelled(wide, label) {
			t.Errorf("at 80 columns there is no %q preview", label)
		}
	}

	narrow := screen(t, m, th, 79, 40)
	for _, row := range narrow {
		if strings.Contains(row, "│  ") && strings.Contains(row, "[") {
			t.Errorf("at 79 columns a table row has something beside it: %q", row)
		}
	}
	for _, label := range []string{"100 columns", "60 columns", "no colour"} {
		if labelled(narrow, label) {
			t.Errorf("at 79 columns there is a %q preview", label)
		}
	}
	clock := slices.IndexFunc(narrow, func(r string) bool { return strings.HasPrefix(r, " [x] clock") })
	name := slices.Index(narrow, " cache")
	format := slices.Index(narrow, ` format      "cache $pct%"`)
	heading := slices.Index(narrow, "PREVIEW")
	if clock < 0 || name < clock || format != name+1 || heading < format {
		t.Fatalf("rows are out of order: table ends at %d, settings at %d and %d, preview at %d\n%s",
			clock, name, format, heading, strings.Join(narrow, "\n"))
	}
	// The one preview is the bar at 79 columns.
	if got, want := narrow[heading+1], plainText(realBar(th, drawing(t), live(th), 79)); got != want {
		t.Errorf("the preview is\n%q, want the bar at 79 columns\n%q", got, want)
	}
}

// realBar draws the status bar the way the client does: laid out with a
// separator of three cells and two cells between the sides, and drawn by the
// theme.
func realBar(th *theme.Theme, c statusline.Config, vals map[string]statusline.Value, width int) string {
	left, right := statusline.Layout(c, vals, width, statusline.LayoutOptions{SepWidth: 3, MinGap: 2, Measure: th.Width})
	cells := func(in []statusline.Cell) []theme.Cell {
		out := make([]theme.Cell, 0, len(in))
		for _, c := range in {
			out = append(out, theme.Cell{Text: c.Text, Tone: c.Tone})
		}
		return out
	}
	_, row := th.Bar(cells(left), cells(right), width)
	return row
}

// Under 80 columns the one preview is the client's own bar at that width,
// cell for cell and style for style, at every width and whatever is going
// on. The widths one or two cells short of what a row needs are the ones
// that tell a layout with the wrong gap apart.
func TestNarrowPreviewIsTheRealBar(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	states := []struct {
		name string
		vals map[string]statusline.Value
	}{{"idle", live(th)}, {"busy", busy(th)}, {"wide characters", wideValues(th)}}
	for _, state := range states {
		t.Run(state.name, func(t *testing.T) {
			t.Parallel()
			m := sledit.New(drawing(t), path, state.vals)
			for width := 12; width < 80; width++ {
				content, _ := m.View(th, width, 40)
				rows := strings.Split(content, "\n")
				at := slices.IndexFunc(rows, func(r string) bool { return plainText(r) == "PREVIEW" })
				if at < 0 {
					t.Fatalf("at %d columns there is no preview", width)
				}
				if want := realBar(th, drawing(t), state.vals, width); rows[at+1] != want {
					t.Errorf("at %d columns the preview is\n%q, want the real bar\n%q", width, rows[at+1], want)
				}
			}
		})
	}
}

// The 60-column preview of a wide screen is the client's own bar too.
func TestWidePreviewIsTheRealBar(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	c := drawing(t)
	m := sledit.New(c, path, busy(th))
	label := th.Style("dim").Render("60 columns") + "  "
	// Every segment in turn switched the other way: rows of many lengths.
	for i := range c.Segments {
		send(t, m, space())
		content, _ := m.View(th, 100, 40)
		want := label + realBar(th, m.Config(), busy(th), 60)
		if !slices.Contains(strings.Split(content, "\n"), want) {
			t.Errorf("with %s switched, no row is the real 60-column bar after its label:\n%q\nin\n%s", c.Segments[i].Name, want, plainText(content))
		}
		send(t, m, space(), down())
	}
}

// bar returns the bar of a preview row: what follows the label and the space
// after it, twelve cells in all.
func bar(t *testing.T, row, label string) string {
	t.Helper()
	rest, ok := strings.CutPrefix(row, label+strings.Repeat(" ", 12-len(label)))
	if !ok {
		t.Fatalf("the row %q does not start with the label %q in its column", row, label)
	}
	return rest
}

// A preview is the real bar: what statusline.Layout keeps at that width,
// drawn by Theme.Bar. It follows every change at once.
func TestPreviewFollowsTheConfig(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	m := newDrawing(t, th)
	bars := func() (wide, narrow, plain string) {
		t.Helper()
		rows := screen(t, m, th, 100, 30)
		at := slices.Index(rows, "PREVIEW")
		if at < 0 {
			t.Fatalf("no previews in\n%s", strings.Join(rows, "\n"))
		}
		return bar(t, rows[at+1], "100 columns"), bar(t, rows[at+2], "60 columns"), bar(t, rows[at+4], "no colour")
	}
	send(t, m, space()) // cache off
	wide, narrow, plain := bars()
	for _, got := range []string{wide, narrow, plain} {
		if strings.Contains(got, "cache") || !strings.Contains(got, "deepseek-flash") {
			t.Errorf("with cache off the bar is %q", got)
		}
	}
	if !strings.Contains(narrow, "⎇ main │ 12:04") {
		t.Errorf("with cache off the branch fits 60 columns again, but the bar is %q", narrow)
	}
	send(t, m, space(), right()) // back on, at the right
	wide, _, plain = bars()
	if !strings.HasPrefix(wide, " ● live │ ada │ deepseek-flash │ 7.2s  ") || !strings.HasSuffix(wide, "  cache 82% │ ⎇ main │ 12:04 ") {
		t.Errorf("with cache on the right the bar is %q", wide)
	}
	if !strings.HasPrefix(plain, " * live | ada | deepseek-flash | 7.2s  ") || !strings.HasSuffix(plain, "  cache 82% | main | 12:04 ") {
		t.Errorf("with cache on the right the no-colour bar is %q", plain)
	}
	sel(t, m, "core:tokens")
	send(t, m, space())
	if wide, _, _ = bars(); !strings.Contains(wide, "│ 3.7k tok │") {
		t.Errorf("with tokens on the bar is %q", wide)
	}
	// A format is used as soon as it is accepted.
	sel(t, m, "core:latency")
	send(t, m, char('e'))
	retype(t, m, "took ${secs}s")
	send(t, m, enter(), esc())
	if wide, _, _ = bars(); !strings.Contains(wide, "│ took 7.2s ") {
		t.Errorf("with a new format the bar is %q", wide)
	}
	if got := rowWith(t, screen(t, m, th, 100, 30), "] latency"); !strings.Contains(got, "took 7.2s") {
		t.Errorf("the latency row is %q, want the new format shown", got)
	}
}

// Before the first model call there is nothing live to show for most
// segments. The table shows a sample for each; the previews fill in the
// segments that are there most of the time, and leave out those that come
// and go and a mod that has not answered.
func TestSamples(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	fresh := statusline.CoreValues(statusline.Snapshot{Conn: statusline.ConnConnecting, Clock: "09:30"},
		statusline.Marks{Dot: th.G.Dot, Newer: th.G.Newer})
	m := sledit.New(drawing(t), path, fresh)
	rows := screen(t, m, th, 100, 30)
	shows := map[string]string{
		"state":    "● connecting", // live
		"clock":    "09:30",        // live
		"contact":  "ada",
		"route":    "deepseek-flash",
		"cache":    "cache 82%",
		"tokens":   "3.7k tok",
		"latency":  "7.2s",
		"activity": "running python",
		"stop":     "esc stop",
		"scroll":   "↓ newer",
		"agents":   "1 sub-agent",
		"load":     "load 0.42",
		"branch":   "no value",
	}
	for name, want := range shows {
		row := rowWith(t, rows, "] "+name+" ")
		if got := strings.TrimSpace(row[strings.Index(row, name)+len(name) : strings.Index(row, "│")]); !strings.HasPrefix(got, want+"  ") {
			t.Errorf("%s shows %q, want %q", name, got, want)
		}
	}
	at := slices.Index(rows, "PREVIEW")
	wide := bar(t, rows[at+1], "100 columns")
	want := " ● connecting │ ada │ deepseek-flash │ cache 82% │ 7.2s"
	if !strings.HasPrefix(wide, want+"  ") || !strings.HasSuffix(wide, "  09:30 ") {
		t.Errorf("the bar is %q, want %q and the clock alone at the right", wide, want)
	}

	// Live values take the place of the samples as they arrive.
	m.SetLive(busy(th))
	rows = screen(t, m, th, 100, 30)
	at = slices.Index(rows, "PREVIEW")
	wide = bar(t, rows[at+1], "100 columns")
	if !strings.HasPrefix(wide, " ● live │ grace │ big-one │ cache 10% │ 0.9s  ") ||
		!strings.HasSuffix(wide, "  running bash │ esc stop │ ⎇ trunk │ 09:31 ") {
		t.Errorf("with live values the bar is %q", wide)
	}
	if got := rowWith(t, rows, "] branch "); !strings.Contains(got, "⎇ trunk") {
		t.Errorf("the branch row is %q, want the live value", got)
	}
	// The no-colour bar is in ASCII, whatever the values hold.
	if plain := bar(t, rows[at+4], "no colour"); !strings.HasPrefix(plain, " * live | grace | big-one | cache 10% | 0.9s  ") ||
		!strings.HasSuffix(plain, "  running bash | esc stop | trunk | 09:31 ") {
		t.Errorf("the no-colour bar is %q", plain)
	}
}

// The no-colour preview is in ASCII even when the screen is not: a glyph
// becomes its stand-in, a letter with none a question mark, and any other
// symbol is left out.
func TestNoColourPreviewIsASCII(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	c := drawing(t)
	for i, s := range c.Segments {
		switch s.Name {
		case "contact":
			c.Segments[i].Format = "→ $name ✓"
		case "latency":
			c.Segments[i].Format = "⏱"
		}
	}
	vals := live(th)
	vals["core:contact"] = statusline.Value{Vars: map[string]string{"name": "Zoë 愛"}}
	m := sledit.New(c, path, vals)
	rows := screen(t, m, th, 100, 30)
	at := slices.Index(rows, "PREVIEW")
	if got := bar(t, rows[at+1], "100 columns"); !strings.HasPrefix(got, " ● live │ → Zoë 愛 ✓ │ deepseek-flash │ cache 82% │ ⏱  ") {
		t.Errorf("the bar is %q", got)
	}
	plain := bar(t, rows[at+4], "no colour")
	if !strings.HasPrefix(plain, " * live | -> Zo? ? | deepseek-flash | cache 82% | ?  ") {
		t.Errorf("the no-colour bar is %q", plain)
	}
	if strings.IndexFunc(plain, func(r rune) bool { return r >= 0x80 }) >= 0 {
		t.Errorf("the no-colour bar is not ASCII: %q", plain)
	}
	if got, want := rows[at+3], strings.Repeat(" ", 12)+strings.Repeat("-", 88); got != want {
		t.Errorf("the row over the no-colour bar is %q, want a rule of hyphens under the bar's own cells", got)
	}
}

// busy returns the values of a moment with a tool running, where the bar
// holds 88 cells at 100 columns: 44 at the left, 42 at the right and the two
// between them.
func busy(th *theme.Theme) map[string]statusline.Value {
	vals := statusline.CoreValues(statusline.Snapshot{
		Conn: statusline.ConnLive, Contact: "grace", Provider: "x", Model: "big-one",
		HasCall: true, CacheReported: true, Input: 1000, Cached: 100, Output: 5, MS: 900,
		Replying: true, Running: "bash", Clock: "09:31",
	}, statusline.Marks{Dot: th.G.Dot, Newer: th.G.Newer})
	vals["mod:git/branch"] = statusline.ModValue("⎇ trunk", "", "")
	return vals
}

// wideValues returns the idle values with a contact and a branch whose
// names are in characters two cells wide.
func wideValues(th *theme.Theme) map[string]statusline.Value {
	vals := live(th)
	vals["core:contact"] = statusline.Value{Vars: map[string]string{"name": "愛と勇気の連絡先"}}
	vals["mod:git/branch"] = statusline.ModValue("⎇ 機能/日本語", "", "")
	return vals
}

// Each bar stands after its label. A bar wider than the room there is drawn
// in the room, with its two sides closer together: the cells are still the
// ones a bar of that width holds. Where the cells do not fit the room, the
// bar is cut at the screen's edge.
func TestPreviewPlacement(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	row := func(rows []string, label string) (string, int) {
		t.Helper()
		at := slices.IndexFunc(rows, func(r string) bool { return strings.HasPrefix(r, label) })
		if at < 0 {
			t.Fatalf("no row starts with %q\n%s", label, strings.Join(rows, "\n"))
		}
		return bar(t, rows[at], label), at
	}

	m := newDrawing(t, th)
	const left = " ● live │ ada │ deepseek-flash │ cache 82% │ 7.2s  "
	for _, tt := range []struct{ width, room int }{{200, 100}, {112, 100}, {111, 99}, {100, 88}, {80, 68}} {
		rows := screen(t, m, th, tt.width, 30)
		got, _ := row(rows, "100 columns")
		if th.Width(got) != tt.room || !strings.HasPrefix(got, left) || !strings.HasSuffix(got, "  ⎇ main │ 12:04 ") {
			t.Errorf("at %d columns the 100-column bar is %q, want both sides in %d cells", tt.width, got, tt.room)
		}
		// The 60-column bar always has its 60 cells, and so shows how much
		// room is left in a bar that narrow.
		if got, _ := row(rows, "60 columns"); th.Width(got) != 60 || !strings.HasPrefix(got, left) || !strings.HasSuffix(got, "  12:04 ") {
			t.Errorf("at %d columns the 60-column bar is %q, want 60 cells", tt.width, got)
		}
		got, at := row(rows, "no colour")
		if th.Width(got) != tt.room || !strings.HasSuffix(got, "  main | 12:04 ") {
			t.Errorf("at %d columns the no-colour bar is %q, want %d cells", tt.width, got, tt.room)
		}
		if want := strings.Repeat(" ", 12) + strings.Repeat("-", tt.room); rows[at-1] != want {
			t.Errorf("at %d columns the row over the no-colour bar is %q, want a rule of %d cells after the label's column", tt.width, rows[at-1], tt.room)
		}
	}

	// 88 cells of bar: they fill the room at 100 columns, with the two
	// cells between the sides that a bar must have.
	m.SetLive(busy(th))
	const full = " ● live │ grace │ big-one │ cache 10% │ 0.9s  running bash │ esc stop │ ⎇ trunk │ 09:31 "
	rows := screen(t, m, th, 100, 30)
	if got, _ := row(rows, "100 columns"); got != full {
		t.Errorf("at 100 columns the busy bar is\n%q, want\n%q", got, full)
	}
	// One column less and they do not fit: the bar is the 100 cells it is
	// on the real screen, cut at the edge of this one. The no-colour bar is
	// two cells shorter without the branch mark, and still fits.
	rows = screen(t, m, th, 99, 30)
	got, _ := row(rows, "100 columns")
	if th.Width(got) != 87 || !strings.HasSuffix(got, "  running bash │ esc stop │ ⎇ …") ||
		!strings.HasPrefix(got, " ● live │ grace │ big-one │ cache 10% │ 0.9s    ") {
		t.Errorf("at 99 columns the busy bar is %q, want it cut to 87 cells with an ellipsis", got)
	}
	const fullPlain = " * live | grace | big-one | cache 10% | 0.9s   running bash | esc stop | trunk | 09:31 "
	if got, _ := row(rows, "no colour"); got != fullPlain {
		t.Errorf("at 99 columns the busy no-colour bar is\n%q, want\n%q", got, fullPlain)
	}
	// Narrower still, the no-colour bar is cut too, and its rule with it.
	rows = screen(t, m, th, 90, 30)
	got, at := row(rows, "no colour")
	if th.Width(got) != 78 || !strings.HasSuffix(got, "  running bash | ...") {
		t.Errorf("at 90 columns the busy no-colour bar is %q, want it cut to 78 cells", got)
	}
	if want := strings.Repeat(" ", 12) + strings.Repeat("-", 78); rows[at-1] != want {
		t.Errorf("at 90 columns the row over the no-colour bar is %q, want a rule of 78 cells", rows[at-1])
	}
	for _, width := range []int{99, 90, 80} {
		for i, r := range screen(t, m, th, width, 30) {
			if th.Width(r) > width {
				t.Errorf("at %d columns row %d is %d cells wide: %q", width, i, th.Width(r), r)
			}
		}
	}

	// Whether the cells fit is counted in cells, not in characters: these
	// are 86 cells and 73 characters.
	m.SetLive(wideValues(th))
	const wide = " ● live │ 愛と勇気の連絡先 │ deepseek-flash │ cache 82% │ 7.2s"
	if got, _ := row(screen(t, m, th, 98, 30), "100 columns"); got != wide+"  ⎇ 機能/日本語 │ 12:04 " {
		t.Errorf("at 98 columns the bar of wide characters is %q, want all of it in 86 cells", got)
	}
	got, _ = row(screen(t, m, th, 96, 30), "100 columns")
	if w := th.Width(got); w > 84 || w < 83 || !strings.HasPrefix(got, wide+"  ") || !strings.HasSuffix(got, "  ⎇ 機…") {
		t.Errorf("at 96 columns the bar of wide characters is %q (%d cells), want it cut at the edge", got, th.Width(got))
	}
}

// A screen too short for everything loses its empty rows first. Then the
// table scrolls with the selection, down to six rows, before the previews go
// one by one, the last first. The key row is the last thing left.
func TestShortScreens(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	count := func(rows []string) (segments int) {
		for _, row := range rows {
			if strings.HasPrefix(row, " [x] ") || strings.HasPrefix(row, " [ ] ") {
				segments++
			}
		}
		return segments
	}
	tests := []struct {
		height   int
		segments int
		previews []string
		gone     []string
	}{
		{30, 14, []string{"100 columns", "60 columns", "no colour"}, nil},
		{24, 14, []string{"100 columns", "60 columns", "no colour"}, nil},
		{23, 13, []string{"100 columns", "60 columns", "no colour"}, nil},
		{22, 12, []string{"100 columns", "60 columns", "no colour"}, nil},
		{20, 10, []string{"100 columns", "60 columns", "no colour"}, nil},
		{16, 6, []string{"100 columns", "60 columns", "no colour"}, nil},
		{15, 7, []string{"100 columns", "60 columns"}, []string{"no colour"}},
		{14, 6, []string{"100 columns", "60 columns"}, []string{"no colour"}},
		{13, 6, []string{"100 columns"}, []string{"60 columns", "no colour"}},
		{12, 7, nil, []string{"PREVIEW", "100 columns", "60 columns", "no colour"}},
		{8, 3, nil, []string{"PREVIEW"}},
		{5, 1, nil, []string{"PREVIEW", "presets"}},
		{3, 1, nil, []string{"PREVIEW", "presets", "SEGMENT"}},
		{2, 1, nil, []string{"Status line"}},
		{1, 0, nil, []string{"Status line", "[x]"}},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.height), func(t *testing.T) {
			t.Parallel()
			m := newDrawing(t, th)
			rows := screen(t, m, th, 100, tt.height)
			all := strings.Join(rows, "\n")
			if got := count(rows); got != tt.segments {
				t.Errorf("%d segment rows, want %d\n%s", got, tt.segments, all)
			}
			for _, label := range tt.previews {
				if !slices.ContainsFunc(rows, func(r string) bool { return strings.HasPrefix(r, label+" ") }) {
					t.Errorf("no %q\n%s", label, all)
				}
			}
			for _, text := range tt.gone {
				if strings.Contains(all, text) {
					t.Errorf("%q is still drawn\n%s", text, all)
				}
			}
			if last := rows[len(rows)-1]; !strings.Contains(last, "esc back") {
				t.Errorf("the last row is %q, want the keys", last)
			}
			// Whatever the height, the selected segment is on the screen,
			// all the way down the list and back up.
			if tt.height < 2 {
				return
			}
			segments := m.Config().Segments
			sel(t, m, segments[0].ID())
			back := slices.Clone(segments)
			slices.Reverse(back)
			for i, seg := range slices.Concat(segments, back[1:]) {
				rows := screen(t, m, th, 100, tt.height)
				if !slices.ContainsFunc(rows, func(r string) bool { return strings.Contains(r, "] "+seg.Name+" ") }) {
					t.Fatalf("with %s selected its row is not on the screen\n%s", seg.Name, strings.Join(rows, "\n"))
				}
				if count(rows) != tt.segments {
					t.Errorf("with %s selected there are %d segment rows, want %d", seg.Name, count(rows), tt.segments)
				}
				if tt.segments < len(segments) && tt.height >= 4 {
					want := strconv.Itoa(slices.Index(ids(m.Config()), seg.ID())+1) + "/" + strconv.Itoa(len(segments))
					if got := rowWith(t, rows, "SEGMENT"); !strings.Contains(got, " "+want+" │") {
						t.Errorf("the heading row is %q, want it to say %s", got, want)
					}
				}
				if i < len(segments)-1 {
					send(t, m, down())
				} else {
					send(t, m, up())
				}
			}
		})
	}
}

// The terminal's own cursor stands in the field being edited, after the
// text before it, and stays on the screen however long the value is.
func TestCursor(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	m := newDrawing(t, th)
	send(t, m, char('e'))
	at := func(width, height int) (row string, x int) {
		t.Helper()
		content, cursor := m.View(th, width, height)
		if cursor == nil {
			t.Fatalf("no cursor at %dx%d", width, height)
		}
		return strings.Split(plainText(content), "\n")[cursor.Y], cursor.X
	}
	row, x := at(100, 30)
	if row != "format ❯ cache $pct%" || x != th.Width(row) {
		t.Errorf("the cursor is at %d in %q, want it after the text", x, row)
	}
	send(t, m, left(), left(), left())
	if row, x = at(100, 30); x != th.Width("format ❯ cache $p") {
		t.Errorf("after three lefts the cursor is at %d in %q", x, row)
	}
	send(t, m, char('X'))
	if row, x = at(100, 30); row != "format ❯ cache $pXct%" || x != th.Width("format ❯ cache $pX") {
		t.Errorf("after typing the cursor is at %d in %q", x, row)
	}
	// A wide character moves the cursor by its cells.
	wipe(t, m)
	send(t, m, typed("愛a")...)
	if row, x = at(100, 30); row != "format ❯ 愛a" || x != 12 {
		t.Errorf("after a wide character the cursor is at %d in %q, want 12", x, row)
	}

	// A value longer than its row scrolls: the end shows while typing, the
	// start after home, and the cursor never leaves the row.
	wipe(t, m)
	long := strings.Repeat("0123456789", 12)
	send(t, m, typed(long)...)
	for _, width := range []int{100, 60, 30, 12, 5, 2, 1} {
		row, x := at(width, 30)
		if th.Width(row) > width || x >= width || x < 0 {
			t.Errorf("at %d columns the row is %d cells and the cursor at %d: %q", width, th.Width(row), x, row)
		}
		if width >= 12 && (!strings.HasSuffix(row, "789") || x != th.Width(row)) {
			t.Errorf("at %d columns the row is %q with the cursor at %d, want the end of the value and the cursor after it", width, row, x)
		}
	}
	send(t, m, key(tea.KeyHome))
	row, x = at(40, 30)
	if !strings.HasPrefix(row, "format ❯ 0123456789") || x != th.Width("format ❯ ") || th.Width(row) > 40 {
		t.Errorf("after home the row is %q with the cursor at %d", row, x)
	}
	send(t, m, key(tea.KeyEnd))
	if row, x = at(40, 30); !strings.HasSuffix(row, "789") || x != th.Width(row) {
		t.Errorf("after end the row is %q with the cursor at %d", row, x)
	}
	// Text deleted at the end pulls the rest back into view.
	for range 100 {
		send(t, m, key(tea.KeyBackspace))
	}
	if row, x = at(40, 30); row != "format ❯ "+long[:20] || x != th.Width(row) {
		t.Errorf("after deleting the row is %q with the cursor at %d", row, x)
	}
}

// The row a field is edited in is the last thing a short screen gives up.
func TestFieldOnShortScreens(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	m := newDrawing(t, th)
	send(t, m, char('e'))
	for height := 1; height <= 30; height++ {
		content, cursor := m.View(th, 100, height)
		rows := strings.Split(plainText(content), "\n")
		if cursor == nil || cursor.Y >= len(rows) {
			t.Fatalf("at height %d the cursor is %+v", height, cursor)
		}
		if got := rows[cursor.Y]; got != "format ❯ cache $pct%" {
			t.Errorf("at height %d the cursor's row is %q", height, got)
		}
		if height >= 6 && !strings.Contains(rows[len(rows)-1], "enter accept · ↑↓ field · esc cancel") {
			t.Errorf("at height %d the last row is %q, want the field's keys", height, rows[len(rows)-1])
		}
	}
	// The row under the input says what the field takes.
	rows := screen(t, m, th, 100, 30)
	if got := rows[29]; !strings.HasSuffix(got, "what it shows, with $cached $input $pct; empty for the default") {
		t.Errorf("the last row is %q", got)
	}
	// The field being edited is marked among the settings.
	content, _ := m.View(th, 100, 30)
	if !strings.Contains(content, th.Style("accent").Render("format      ")) {
		t.Error("the format's label is not in the accent tone while it is edited")
	}
	if strings.Contains(content, th.Style("accent").Render("short       ")) {
		t.Error("the short form's label is in the accent tone while the format is edited")
	}
}

// Text from the file, from a mod and from the caller is drawn as text: an
// escape sequence in it never reaches the screen, and wide characters do
// not push a row past the edge.
func TestHostileText(t *testing.T) {
	t.Parallel()
	const csi, osc = "\x1b[2J\x1b[31m", "\x1b]0;title\x07"
	hostile := statusline.Config{Segments: []statusline.Segment{
		{Name: "state", From: statusline.FromCore, On: true, Side: statusline.Left, Format: csi + "$dot" + osc + " $state\x9b31m"},
		{Name: "cache", From: statusline.FromCore, On: true, Side: statusline.Left, Format: "这是一个很长的中文句子用来测试 $pct", Short: "👨\u200d👩\u200d👧"},
		{Name: csi + "evil" + osc, From: "mod:" + csi + "bad", On: true, Side: statusline.Right, Format: "\u202e$text"},
		{
			Name: "sh\x1b[0m", From: statusline.FromShell, On: true, Side: statusline.Right,
			Command: "printf '\x1b[2J'; " + strings.Repeat("echo 👍🏽; ", 40), Match: csi + "(.*)\r\n",
			Format: osc + "$1", Interval: statusline.DefaultInterval, Timeout: statusline.DefaultTimeout,
		},
		{Name: "clock", From: statusline.FromCore, On: true, Side: statusline.Right},
	}}
	vals := map[string]statusline.Value{
		"core:state": {Vars: map[string]string{"dot": "●", "state": "live" + csi}},
		"core:cache": {Vars: map[string]string{"pct": "82\x07"}},
		"core:clock": {Text: "12:04" + osc},
	}
	for _, s := range hostile.Segments {
		if _, ok := vals[s.ID()]; !ok {
			vals[s.ID()] = statusline.ModValue(csi+"值 value"+osc, "", "bad"+csi)
		}
	}
	for _, term := range terminals() {
		t.Run(term.name, func(t *testing.T) {
			t.Parallel()
			th := themeOf(term.caps)
			m := sledit.New(hostile, "~/"+csi+"statusline.toml"+osc, vals)
			m.SaveFailed("disk" + csi + osc)
			sizes := [][2]int{{120, 40}, {100, 30}, {80, 24}, {79, 30}, {60, 30}, {40, 16}, {30, 12}, {20, 8}, {10, 5}}
			for range len(hostile.Segments) {
				for _, size := range sizes {
					checkScreen(t, m, th, size[0], size[1])
				}
				send(t, m, char('e'))
				for range 9 {
					for _, size := range sizes {
						checkScreen(t, m, th, size[0], size[1])
					}
					send(t, m, down())
				}
				send(t, m, tea.PasteMsg{Content: csi + osc + "\u202e"}, esc(), down())
			}
		})
	}
}

func TestNoScreen(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	m := newDrawing(t, th)
	send(t, m, char('e'))
	for _, size := range [][2]int{{0, 0}, {0, 30}, {100, 0}, {-1, 30}, {100, -5}} {
		if content, cursor := m.View(th, size[0], size[1]); content != "" || cursor != nil {
			t.Errorf("at %dx%d the screen is %q with cursor %v, want nothing", size[0], size[1], content, cursor)
		}
	}
	if content, cursor := m.View(nil, 100, 30); content != "" || cursor != nil {
		t.Errorf("with no theme the screen is %q with cursor %v, want nothing", content, cursor)
	}
}

// The settings pane, for each kind of segment.
func TestSettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		id   string
		want []string
	}{
		{"a core segment that never gives way", "core:state", []string{
			"state", `format      "$dot $state"`, `short       "$dot $state"`, "warn below  none",
			"drop order  never", "refresh     as the connection changes",
		}},
		{"a core segment with no short form", "core:route", []string{
			"route", `format      "$model"`, "short       none", "warn below  none",
			"drop order  1st when narrow", "refresh     with the contact",
		}},
		{"a mod's segment", "mod:git/branch", []string{
			"branch", `format      "$text"`, `short       "$short"`, "warn below  none",
			"drop order  2nd when narrow", "refresh     when its mod answers",
		}},
		{"a shell segment", "shell:load", []string{
			"load", `command     "uptime"`, `match       "load averages?: ([0-9.]+)"`, `format      "load $1"`,
			"short       none", "warn below  none", "drop order  3rd when narrow", `interval    "10s"`, `timeout     "1s"`,
			"", "A shell segment runs a command", "on an interval, off the UI loop.",
		}},
		{"the clock", "core:clock", []string{
			"clock", `format      "$time"`, `short       "$time"`, "warn below  none",
			"drop order  4th when narrow", "refresh     each minute",
			"", "A shell segment runs a command", "on an interval, off the UI loop:", "",
			`command   "uptime"`, `interval  "10s"`, `timeout   "1s"`,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			th := themeOf(trueDark())
			m := sledit.New(drawing(t), path, live(th))
			sel(t, m, tt.id)
			rows := screen(t, m, th, 100, 30)
			for i, want := range tt.want {
				_, got, _ := strings.Cut(rows[3+i], "│")
				if got = strings.TrimPrefix(got, "  "); got != want {
					t.Errorf("settings row %d is %q, want %q", i, got, want)
				}
			}
		})
	}
}

// The place a segment has in the order its side gives way in is the one
// statusline.DropOrdinal gives, for every segment of the drawing.
func TestDropOrder(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	c := drawing(t)
	m := sledit.New(c, path, live(th))
	for i, s := range c.Segments {
		sel(t, m, s.ID())
		want := "never"
		if n := statusline.DropOrdinal(c, i); n > 0 {
			want = strconv.Itoa(n) + map[int]string{1: "st", 2: "nd", 3: "rd"}[n] + " when narrow"
			if n > 3 {
				want = strconv.Itoa(n) + "th when narrow"
			}
		}
		if got := rowWith(t, screen(t, m, th, 100, 30), "drop order"); !strings.HasSuffix(got, "drop order  "+want) {
			t.Errorf("%s: the row is %q, want %q", s.ID(), got, want)
		}
	}
	if got := statusline.DropOrdinal(c, slices.Index(ids(c), "core:cache")); got != 3 {
		t.Errorf("the drawing's cache is %d in its side's order, want 3", got)
	}
}
