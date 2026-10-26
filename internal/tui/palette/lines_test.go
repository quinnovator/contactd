package palette_test

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/commands"
	"github.com/quinnovator/contactd/internal/tui/palette"
	"github.com/quinnovator/contactd/internal/tui/theme"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// The widths the layout changes at, and one on each side of them.
func widths() []int { return []int{100, 60, 59, 40, 39, 30} }

// on returns the escape sequence that switches a style on.
func on(t *testing.T, st interface{ Render(...string) string }) string {
	t.Helper()
	before, _, found := strings.Cut(st.Render("x"), "x")
	if !found || before == "" {
		t.Fatalf("the style draws nothing: %q", st.Render("x"))
	}
	return before
}

// boxed is one row inside the box at a width: left at the left, right at the
// right edge, a border and a space at each side.
func boxed(th *theme.Theme, width int, left, right string) string {
	gap := width - 2*th.Width(th.G.BoxV) - 2 - th.Width(left) - th.Width(right)
	return th.G.BoxV + " " + left + strings.Repeat(" ", gap) + right + " " + th.G.BoxV
}

// pointed returns the names on the rows that have the pointer.
func pointed(th *theme.Theme, lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.Contains(ansi.Strip(l), th.G.Pointer+" /") {
			out = append(out, names([]string{l})...)
		}
	}
	return out
}

func TestCommandsMock(t *testing.T) {
	t.Parallel()
	reg := mockRegistry()
	th := mockLook().theme()
	m := opened(t, reg, "/st")
	if got := m.Want(100); got != 9 {
		t.Fatalf("Want(100) = %d, want 9: two edges, a title, five commands, the keys", got)
	}
	lines := m.Lines(th, 100, 9)
	tuitest.Golden(t, "commands-100.golden", []byte(strings.Join(plain(lines), "\n")+"\n"))
	tuitest.Golden(t, "commands-100-styled.golden", []byte(quoted(lines)))

	// The mock-up, row for row. Inside the box there are 96 cells: 20 for
	// the name, 16 for the source, and the summary between them.
	inside := func(s string) string { return "│ " + s + strings.Repeat(" ", 96-utf8.RuneCountInString(s)) + " │" }
	command := func(name, summary, source string) string {
		return inside(fmt.Sprintf("%-20s%-60s%16s", name, summary, source))
	}
	count := fmt.Sprintf("5 of %d", reg.Len())
	want := []string{
		"╭" + strings.Repeat("─", 98) + "╮",
		inside("commands matching /st" + strings.Repeat(" ", 96-21-len(count)) + count),
		command("▸ /statusline", "choose a preset or edit the status line", "core"),
		command("  /status", "connection, route and cache for this contact", "core"),
		command("  /stop", "stop the reply in flight (esc)", "core"),
		command("  /standup", "summarize yesterday for the team", "mod standup"),
		command("  /stats", "token use and cache share by day", "mod cache"),
		inside("↑↓ choose · tab complete · enter run · esc close"),
		"╰" + strings.Repeat("─", 98) + "╯",
	}
	got := plain(lines)
	if len(got) != len(want) {
		t.Fatalf("%d lines, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
	if count != "5 of 27" {
		t.Errorf("the count is %q; the mock-up's registry has 24 commands", count)
	}

	// How the rows are drawn: the selected row is filled, its name is the
	// accent in bold and its summary is body text; every other summary is
	// dim; a source is dim for core and in the mod tone for a mod.
	dim, mod, fill := on(t, th.Style("dim")), on(t, th.Style("mod")), "\x1b[48;2;27;33;38m"
	accent := on(t, th.Style("accent"))
	if !strings.HasPrefix(accent, "\x1b[1;") {
		t.Errorf("the accent is not bold, as the mock-up draws the selected name: %q", accent)
	}
	if !th.BarFilled {
		t.Fatalf("the mock-up's terminal has a filled bar")
	}
	for i, l := range lines {
		if filled := strings.Contains(l, fill); filled != (i == 2) {
			t.Errorf("line %d: filled = %v", i, filled)
		}
	}
	selected := lines[2]
	for what, part := range map[string]string{
		"the name in the accent, bold": accent + "▸ /statusline",
		"the source dim":               dim + "core",
	} {
		if !strings.Contains(selected, part) {
			t.Errorf("selected row: want %s (%q) in %q", what, part, selected)
		}
	}
	if strings.Contains(selected, dim+"choose") {
		t.Errorf("selected row: the summary is dim: %q", selected)
	}
	// The fill is the 96 cells inside the box: it starts after the border's
	// space and is switched off before the space at the other side.
	if !strings.HasPrefix(selected, on(t, th.Style("border"))+"│\x1b[m "+fill) || !strings.HasSuffix(selected, "\x1b[m "+on(t, th.Style("border"))+"│\x1b[m") {
		t.Errorf("selected row: the fill is not what is inside the box: %q", selected)
	}
	for i, part := range map[int]string{
		3: dim + "connection, route and cache for this contact",
		4: dim + "stop the reply in flight (esc)",
		5: mod + "mod standup",
		6: mod + "mod cache",
		7: dim + "↑↓ choose · tab complete · enter run · esc close",
	} {
		if !strings.Contains(lines[i], part) {
			t.Errorf("line %d: want %q in %q", i, part, lines[i])
		}
		if strings.Contains(lines[i], accent) {
			t.Errorf("line %d has the accent: %q", i, lines[i])
		}
	}
	for i := 3; i <= 6; i++ {
		if !strings.Contains(lines[i], "│\x1b[m   /st") {
			t.Errorf("line %d: the name is styled: %q", i, lines[i])
		}
	}
	// The title and the count are dim.
	if !strings.Contains(lines[1], dim+"commands matching /st") || !strings.Contains(lines[1], dim+count) {
		t.Errorf("title row: %q", lines[1])
	}
}

func TestLooksGolden(t *testing.T) {
	t.Parallel()
	// One file for a reader: the palette as each terminal shows it, at each
	// width where the layout changes.
	var b strings.Builder
	for _, l := range []look{mockLook(), asciiLook()} {
		th := l.theme()
		for _, draft := range []string{"/st", "/", "/zzz"} {
			for _, w := range widths() {
				m := opened(t, mockRegistry(), draft)
				m.Move(1)
				fmt.Fprintf(&b, "%s, draft %q, %d columns\n", l.name, draft, w)
				for _, line := range plain(m.Lines(th, w, m.Want(w))) {
					b.WriteString(line + "$\n")
				}
				b.WriteByte('\n')
			}
		}
	}
	tuitest.Golden(t, "widths.golden", []byte(b.String()))

	// And the styles of each look, at one width with a box and one without.
	b.Reset()
	for _, l := range looks() {
		th := l.theme()
		for _, w := range []int{60, 39} {
			m := opened(t, mockRegistry(), "/st")
			fmt.Fprintf(&b, "%s, %d columns\n%s\n", l.name, w, quoted(m.Lines(th, w, m.Want(w))))
		}
	}
	tuitest.Golden(t, "looks.golden", []byte(b.String()))
}

// widthCase is one palette to draw at every width.
type widthCase struct {
	name  string
	reg   func() *commands.Registry
	draft string
	move  int
	// both says the text holds nothing the two ways of counting cells
	// disagree about, so a line must be the width by both.
	both bool
}

func widthCases() []widthCase {
	long := "/" + strings.Repeat("st", 150)
	return []widthCase{
		{"the mock-up", mockRegistry, "/st", 0, true},
		{"the mock-up, the last chosen", mockRegistry, "/st", 4, true},
		{"every command", mockRegistry, "/", 0, true},
		{"every command, scrolled", mockRegistry, "/", 14, true},
		{"nothing matches", mockRegistry, "/zzz", 0, true},
		{"a word of 300 letters", mockRegistry, long, 0, true},
		{"hostile mods", hostileRegistry, "/st", 0, false},
		{"hostile mods, a wide row chosen", hostileRegistry, "/st", 5, false},
		{"hostile mods, every command", hostileRegistry, "/", 23, false},
		{"a word that is not ASCII", hostileRegistry, "/st👍🏽é\u200d日本\x1b[2J", 0, false},
	}
}

func TestLinesAreTheWidth(t *testing.T) {
	t.Parallel()
	for _, l := range append(looks(), wideLooks()...) {
		for _, tc := range widthCases() {
			t.Run(l.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				th := l.theme()
				for _, w := range append(widths(), 80, 44, 41, 20, 12, 10, 5, 4, 2, 1) {
					m := opened(t, tc.reg(), tc.draft)
					m.Move(tc.move)
					want := m.Want(w)
					for _, rows := range []int{want, want - 1, want + 3, 12, 8, 6, 5, 4, 3, 2, 1} {
						if rows < 1 {
							continue
						}
						lines := m.Lines(th, w, rows)
						if len(lines) != rows {
							t.Fatalf("%d columns: %d lines for %d rows", w, len(lines), rows)
						}
						for i, line := range lines {
							if got := th.Width(line); got != w {
								t.Fatalf("%d columns, %d rows, line %d is %d cells: %q", w, rows, i, got, line)
							}
							if strings.ContainsAny(line, "\n\r\t") {
								t.Fatalf("%d columns, line %d is not one line: %q", w, i, line)
							}
							if !tc.both {
								continue
							}
							if wc, gr := ansi.StringWidthWc(line), ansi.StringWidth(line); wc != w || gr != w {
								t.Fatalf("%d columns, line %d is %d cells by wcwidth and %d by cluster: %q", w, i, wc, gr, line)
							}
						}
					}
				}
			})
		}
	}
}

func TestWideLooksAreWide(t *testing.T) {
	t.Parallel()
	// The width matrix means nothing for these looks if the theme refused
	// their glyphs and drew the usual ones.
	for _, l := range wideLooks() {
		th := l.theme()
		if refused := theme.Check(l.opts); refused != nil {
			t.Errorf("%s: the theme refused %v", l.name, refused)
		}
		if th.Width(th.G.BoxV) < 2 || th.Width(th.G.Pointer) < 2 || th.Width(th.G.Ellipsis) < 2 {
			t.Errorf("%s: glyphs %q %q %q are not wide", l.name, th.G.BoxV, th.G.Pointer, th.G.Ellipsis)
		}
	}
}

func TestNoWidthNoRows(t *testing.T) {
	t.Parallel()
	th := mockLook().theme()
	m := opened(t, mockRegistry(), "/st")
	for _, rows := range []int{0, -1} {
		if got := m.Lines(th, 100, rows); got != nil {
			t.Errorf("Lines with %d rows = %q", rows, got)
		}
	}
	for _, w := range []int{0, -5} {
		got := m.Lines(th, w, 5)
		if len(got) != 5 || strings.Join(got, "") != "" {
			t.Errorf("Lines at %d columns = %q, want 5 empty lines", w, got)
		}
	}
}

func TestColumns(t *testing.T) {
	t.Parallel()
	th := mockLook().theme()
	draw := func(w int) []string {
		m := opened(t, mockRegistry(), "/st")
		return plain(m.Lines(th, w, m.Want(w)))
	}

	t.Run("60 columns: three columns", func(t *testing.T) {
		t.Parallel()
		got := draw(60)
		want := []string{
			"│ ▸ /statusline       choose a preset or…             core │",
			"│   /status           connection, route …             core │",
			"│   /stop             stop the reply in …             core │",
			"│   /standup          summarize yesterda…      mod standup │",
			"│   /stats            token use and cach…        mod cache │",
		}
		if !slices.Equal(got[2:7], want) {
			t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(got[2:7], "\n"), strings.Join(want, "\n"))
		}
		if want := boxed(th, 60, "commands matching /st", "5 of 27"); got[1] != want {
			t.Errorf("title row %q, want %q", got[1], want)
		}
		if want := boxed(th, 60, "↑↓ choose · tab complete · enter run · esc close", ""); got[7] != want {
			t.Errorf("key row %q, want %q", got[7], want)
		}
	})

	t.Run("59 columns: no source", func(t *testing.T) {
		t.Parallel()
		got := draw(59)
		want := []string{
			"│ ▸ /statusline       choose a preset or edit the status… │",
			"│   /status           connection, route and cache for th… │",
			"│   /stop             stop the reply in flight (esc)      │",
			"│   /standup          summarize yesterday for the team    │",
			"│   /stats            token use and cache share by day    │",
		}
		if !slices.Equal(got[2:7], want) {
			t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(got[2:7], "\n"), strings.Join(want, "\n"))
		}
		if all := strings.Join(got, "\n"); strings.Contains(all, "core") || strings.Contains(all, "mod ") {
			t.Errorf("a source is drawn under 60 columns:\n%s", all)
		}
	})

	t.Run("40 columns: still a box and a summary", func(t *testing.T) {
		t.Parallel()
		got := draw(40)
		want := []string{
			"╭──────────────────────────────────────╮",
			"│ commands matching /st        5 of 27 │",
			"│ ▸ /statusline       choose a preset… │",
			"│   /status           connection, rou… │",
			"│   /stop             stop the reply … │",
			"│   /standup          summarize yeste… │",
			"│   /stats            token use and c… │",
			"│ ↑↓ choose · tab complete · enter run │",
			"╰──────────────────────────────────────╯",
		}
		if !slices.Equal(got, want) {
			t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})

	for _, w := range []int{39, 30} {
		t.Run(strconv.Itoa(w)+" columns: names alone", func(t *testing.T) {
			t.Parallel()
			got := draw(w)
			want := []string{"▸ /statusline", "  /status", "  /stop", "  /standup", "  /stats"}
			for i := range want {
				want[i] += strings.Repeat(" ", w-utf8.RuneCountInString(want[i]))
			}
			if !slices.Equal(got, want) {
				t.Errorf("lines = %q, want %q", got, want)
			}
		})
	}
}

func TestSelectedRowOnEveryLook(t *testing.T) {
	t.Parallel()
	for _, l := range looks() {
		for _, w := range widths() {
			th := l.theme()
			m := opened(t, mockRegistry(), "/st")
			m.Move(2)
			lines := m.Lines(th, w, m.Want(w))
			if got := pointed(th, lines); !slices.Equal(got, []string{"stop"}) {
				t.Errorf("%s at %d: the pointer is on %v, want stop alone", l.name, w, got)
			}
			if got := names(lines); !slices.Equal(got, []string{"statusline", "status", "stop", "standup", "stats"}) {
				t.Errorf("%s at %d: rows %v", l.name, w, got)
			}
			// The selected row, and no other, is filled or in reverse video.
			mark := "\x1b[7m"
			if th.BarFilled {
				mark = "\x1b[48;"
			}
			for i, line := range lines {
				chosen := strings.Contains(ansi.Strip(line), th.G.Pointer+" /stop")
				if marked := strings.Contains(line, mark); marked != chosen {
					t.Errorf("%s at %d, line %d: marked = %v, chosen = %v: %q", l.name, w, i, marked, chosen, line)
				}
			}
		}
	}
}

func TestKeyRow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		look  look
		draft string
		width int
		want  string
	}{
		{mockLook(), "/st", 100, "↑↓ choose · tab complete · enter run · esc close"},
		{mockLook(), "/st", 52, "↑↓ choose · tab complete · enter run · esc close"},
		{mockLook(), "/st", 51, "↑↓ choose · tab complete · enter run"},
		{mockLook(), "/st", 40, "↑↓ choose · tab complete · enter run"},
		// Nothing matches, so there is nothing to choose, complete or run.
		{mockLook(), "/zzz", 100, "esc close"},
		{asciiLook(), "/st", 100, "up/down choose | tab complete | enter run | esc close"},
		{asciiLook(), "/st", 40, "up/down choose | tab complete"},
	}
	for _, tc := range tests {
		m := opened(t, mockRegistry(), tc.draft)
		lines := plain(m.Lines(tc.look.theme(), tc.width, m.Want(tc.width)))
		row := lines[len(lines)-2]
		v := tc.look.theme().G.BoxV
		got := strings.TrimRight(strings.TrimSuffix(strings.TrimPrefix(row, v+" "), " "+v), " ")
		if got != tc.want {
			t.Errorf("%s, %q at %d: key row %q, want %q", tc.look.name, tc.draft, tc.width, got, tc.want)
		}
	}
}

func TestTitleAndCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		look         look
		draft        string
		title, count string
	}{
		{"as typed", mockLook(), "/St", "commands matching /St", "5 of 27"},
		{"a slash alone", mockLook(), "/", "commands", "27 of 27"},
		{"nothing matches", mockLook(), "/zzz", "commands matching /zzz", "0 of 27"},
		// The box is 56 cells inside: the count, two cells, and the title in
		// what is left.
		{
			"a long word gives way to the count", mockLook(), "/" + strings.Repeat("x", 300),
			"commands matching /" + strings.Repeat("x", 27) + "…", "0 of 27",
		},
		{"not ASCII", mockLook(), "/sté日本", "commands matching /sté日本", "0 of 27"},
		{"not ASCII, on an ASCII terminal", asciiLook(), "/sté日本", "commands matching /st???", "0 of 27"},
		{"an escape sequence typed", mockLook(), "/st\x1b[2J\x1b]0;x\x07", "commands matching /st[2J]0;x", "0 of 27"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			th := tc.look.theme()
			m := opened(t, mockRegistry(), tc.draft)
			lines := m.Lines(th, 60, m.Want(60))
			if got, want := ansi.Strip(lines[1]), boxed(th, 60, tc.title, tc.count); got != want {
				t.Errorf("title row\n got %q\nwant %q", got, want)
			}
		})
	}
}

func TestCountFollowsTheRegistry(t *testing.T) {
	t.Parallel()
	th := mockLook().theme()
	reg := mockRegistry()
	m := opened(t, reg, "/st")
	title := func() string { return ansi.Strip(m.Lines(th, 60, m.Want(60))[1]) }
	if got, want := title(), boxed(th, 60, "commands matching /st", "5 of 27"); got != want {
		t.Errorf("title row %q, want %q", got, want)
	}
	reg.SetMod("clock", []commands.Spec{{Name: "start"}, {Name: "lap"}})
	m.Set(reg, "/st")
	if got, want := title(), boxed(th, 60, "commands matching /st", "6 of 29"); got != want {
		t.Errorf("after a mod was added: title row %q, want %q", got, want)
	}
	reg.RemoveMod("git")
	reg.RemoveMod("cache")
	m.Set(reg, "/st")
	if got, want := title(), boxed(th, 60, "commands matching /st", "5 of 26"); got != want {
		t.Errorf("after two mods were removed: title row %q, want %q", got, want)
	}
	// A registry of another size.
	small := commands.NewRegistry(commands.Core()[:4])
	m.Set(small, "/st")
	if got, want := title(), boxed(th, 60, "commands matching /st", "3 of 4"); got != want {
		t.Errorf("a registry of four: title row %q, want %q", got, want)
	}
}

func TestNothingMatches(t *testing.T) {
	t.Parallel()
	th := mockLook().theme()
	m := opened(t, mockRegistry(), "/zzz")
	want := []string{
		"╭──────────────────────────────────────────────────────────╮",
		boxed(th, 60, "commands matching /zzz", "0 of 27"),
		boxed(th, 60, "no command matches; start with // to send it", ""),
		boxed(th, 60, "esc close", ""),
		"╰──────────────────────────────────────────────────────────╯",
	}
	lines := m.Lines(th, 60, m.Want(60))
	if got := plain(lines); !slices.Equal(got, want) {
		t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if dim := on(t, th.Style("dim")); !strings.Contains(lines[2], dim+"no command matches") {
		t.Errorf("the row is not dim: %q", lines[2])
	}
	if strings.Contains(strings.Join(lines, ""), "\x1b[48;") {
		t.Errorf("a row is drawn as selected where nothing matches")
	}
	narrow := plain(m.Lines(th, 30, m.Want(30)))
	if !slices.Equal(narrow, []string{"no command matches            "}) {
		t.Errorf("under 40 columns: %q", narrow)
	}
}

func TestScroll(t *testing.T) {
	t.Parallel()
	reg := mockRegistry()
	all := specNames(reg.Match(""))
	if len(all) != 27 {
		t.Fatalf("the registry has %d commands", len(all))
	}
	// Each step moves the selection, then the palette is drawn in 12 rows:
	// 8 commands. first is the first command drawn.
	steps := []struct {
		move, first, chosen int
	}{
		{0, 0, 0},
		{7, 0, 7},   // the last row in view
		{1, 1, 8},   // one past it: the list moves one row
		{-1, 1, 7},  // back up: the list stays
		{-6, 1, 1},  // the first row in view
		{-1, 0, 0},  // one above it
		{11, 4, 11}, // a jump
		{100, 19, 26},
		{-3, 19, 23},
		{-100, 0, 0},
	}
	for _, l := range []look{mockLook(), {name: "narrow"}} {
		width := 100
		if l.name == "narrow" {
			l = mockLook()
			width = 30
		}
		th := l.theme()
		m := opened(t, reg, "/")
		rows := m.Want(width)
		for i, s := range steps {
			m.Move(s.move)
			lines := m.Lines(th, width, rows)
			if got, want := names(lines), all[s.first:s.first+8]; !slices.Equal(got, want) {
				t.Fatalf("width %d, step %d: rows %v, want %v", width, i, got, want)
			}
			if got := pointed(th, lines); !slices.Equal(got, []string{all[s.chosen]}) {
				t.Fatalf("width %d, step %d: the pointer is on %v, want %s", width, i, got, all[s.chosen])
			}
			// Drawing twice changes nothing.
			if again := m.Lines(th, width, rows); !slices.Equal(again, lines) {
				t.Fatalf("width %d, step %d: a second draw differs", width, i)
			}
		}
		// A new word starts at the top again.
		m.Move(20)
		m.Lines(th, width, rows)
		m.Set(reg, "/s")
		if got := names(m.Lines(th, width, rows)); len(got) == 0 || got[0] != "statusline" {
			t.Errorf("width %d: after a new word the list starts at %v", width, got)
		}
	}
}

func TestNewWordStartsAtTheTop(t *testing.T) {
	t.Parallel()
	th := mockLook().theme()
	reg := mockRegistry()
	reg.SetMod("clock", []commands.Spec{{Name: "status-of-clock"}})
	m := opened(t, reg, "/")
	m.Move(100)
	m.Lines(th, 100, 12)
	// Three commands match, the second is chosen because the word is its
	// whole name, and there are two rows: the list is not left scrolled.
	m.Set(reg, "/status")
	lines := m.Lines(th, 100, 2)
	if got := names(lines); !slices.Equal(got, []string{"statusline", "status"}) {
		t.Errorf("rows %v, want the first two matches", got)
	}
	if got := pointed(th, lines); !slices.Equal(got, []string{"status"}) {
		t.Errorf("the pointer is on %v, want status", got)
	}
}

func TestScrollFollowsARegistryChange(t *testing.T) {
	t.Parallel()
	th := mockLook().theme()
	reg := mockRegistry()
	m := opened(t, reg, "/")
	m.Move(26) // the last command, stats
	m.Lines(th, 100, 12)
	reg.RemoveMod("git")
	m.Set(reg, "/")
	lines := m.Lines(th, 100, 12)
	all := specNames(reg.Match(""))
	if got := names(lines); !slices.Equal(got, all[len(all)-8:]) {
		t.Errorf("rows %v, want the last eight of %v", got, all)
	}
	if got := pointed(th, lines); !slices.Equal(got, []string{"stats"}) {
		t.Errorf("the pointer is on %v", got)
	}

	// A change that leaves the rows in view alone leaves the list where it
	// stands: the chosen command stays on its row.
	m.Move(-3)
	before := names(m.Lines(th, 100, 12))
	if chosen, _ := m.Selected(); before[4] != chosen.Name {
		t.Fatalf("rows %v with %s chosen: want it on the fifth row", before, chosen.Name)
	}
	reg.SetMod("zeta", []commands.Spec{{Name: "zeta"}})
	m.Set(reg, "/")
	if after := names(m.Lines(th, 100, 12)); !slices.Equal(after, before) {
		t.Errorf("rows %v after a command was added below them, want %v", after, before)
	}
}

func TestFewerRowsThanWanted(t *testing.T) {
	t.Parallel()
	th := mockLook().theme()
	all := specNames(mockRegistry().Match(""))
	tests := []struct {
		name          string
		width, rows   int
		chosen        int
		boxed         bool
		first, listed int
	}{
		{"all it wants", 100, 12, 0, true, 0, 8},
		{"three rows short", 100, 9, 0, true, 0, 5},
		{"three rows short, scrolled", 100, 9, 9, true, 5, 5},
		{"room for the box and three commands", 100, 7, 9, true, 7, 3},
		// A box of six rows would list two commands of 27, and one of five
		// rows one: the rows go to commands.
		{"a row short of the box and three", 100, 6, 9, false, 4, 6},
		{"room for the box and one command only", 100, 5, 9, false, 5, 5},
		{"no room for the box", 100, 4, 9, false, 6, 4},
		{"two rows", 100, 2, 0, false, 0, 2},
		{"one row", 100, 1, 9, false, 9, 1},
		{"narrow, all it wants", 30, 8, 9, false, 2, 8},
		{"narrow, three rows", 30, 3, 9, false, 7, 3},
		{"narrow, one row", 30, 1, 23, false, 23, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := opened(t, mockRegistry(), "/")
			m.Move(tc.chosen)
			lines := m.Lines(th, tc.width, tc.rows)
			if len(lines) != tc.rows {
				t.Fatalf("%d lines for %d rows", len(lines), tc.rows)
			}
			if got, want := names(lines), all[tc.first:tc.first+tc.listed]; !slices.Equal(got, want) {
				t.Errorf("rows %v, want %v", got, want)
			}
			if got := pointed(th, lines); !slices.Equal(got, []string{all[tc.chosen]}) {
				t.Errorf("the pointer is on %v, want %s", got, all[tc.chosen])
			}
			text := strings.Join(plain(lines), "\n")
			if boxed := strings.Contains(text, th.G.BoxTL) && strings.Contains(text, "27 of 27"); boxed != tc.boxed {
				t.Errorf("boxed = %v:\n%s", boxed, text)
			}
			// Without the box at 100 columns a row is still name, summary
			// and source.
			if !tc.boxed && tc.width == 100 && !strings.HasSuffix(plain(lines)[0], "core") {
				t.Errorf("no source column: %q", plain(lines)[0])
			}
		})
	}
}

func TestBoxNeedsRoomForThreeCommands(t *testing.T) {
	t.Parallel()
	th := mockLook().theme()
	tests := []struct {
		draft       string
		width, rows int
		boxed       bool
		// listed is the commands drawn, and blank the rows left empty above.
		listed, blank int
	}{
		// Five match.
		{"/st", 100, 9, true, 5, 0},
		{"/st", 100, 8, true, 4, 0},
		{"/st", 100, 7, true, 3, 0},
		{"/st", 100, 6, false, 5, 1},
		{"/st", 100, 5, false, 5, 0},
		{"/st", 100, 4, false, 4, 0},
		{"/st", 40, 7, true, 3, 0},
		{"/st", 40, 6, false, 5, 1},
		// Two match: a box of six rows holds both.
		{"/tus", 100, 6, true, 2, 0},
		{"/tus", 100, 5, false, 2, 3},
		// One matches: a box of five rows holds it.
		{"/statusl", 100, 5, true, 1, 0},
		{"/statusl", 100, 4, false, 1, 3},
		// Nothing matches: the one row that says so counts as a command.
		{"/zzz", 100, 5, true, 0, 0},
		{"/zzz", 100, 4, false, 0, 3},
		// Under 40 columns there is never a box.
		{"/st", 39, 9, false, 5, 4},
		{"/st", 39, 3, false, 3, 0},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s at %dx%d", tc.draft, tc.width, tc.rows), func(t *testing.T) {
			t.Parallel()
			m := opened(t, mockRegistry(), tc.draft)
			m.Move(1)
			lines := plain(m.Lines(th, tc.width, tc.rows))
			if len(lines) != tc.rows {
				t.Fatalf("%d lines for %d rows", len(lines), tc.rows)
			}
			text := strings.Join(lines, "\n")
			if boxed := strings.HasPrefix(lines[tc.blank], th.G.BoxTL) && strings.HasPrefix(lines[tc.rows-1], th.G.BoxBL); boxed != tc.boxed {
				t.Errorf("boxed = %v:\n%s", boxed, text)
			}
			if title := strings.Contains(text, "commands matching") || strings.Contains(text, " of 27") || strings.Contains(text, "esc close"); title != tc.boxed {
				t.Errorf("title, count or keys drawn = %v:\n%s", title, text)
			}
			if got := len(names(lines)); got != tc.listed {
				t.Errorf("%d commands listed, want %d:\n%s", got, tc.listed, text)
			}
			for i, line := range lines {
				if blank := strings.TrimSpace(line) == ""; blank != (i < tc.blank) {
					t.Errorf("line %d: blank = %v, want %d blank rows above:\n%s", i, blank, tc.blank, text)
				}
			}
			// The chosen command is drawn whatever the rows.
			if sel, ok := m.Selected(); ok && !slices.Equal(pointed(th, m.Lines(th, tc.width, tc.rows)), []string{sel.Name}) {
				t.Errorf("the chosen command %s is not drawn:\n%s", sel.Name, text)
			}
		})
	}
}

func TestSpareRowsWithoutTheBox(t *testing.T) {
	t.Parallel()
	th := mockLook().theme()
	blank := strings.Repeat(" ", 100)
	// One command matches, and four rows do not hold the box and a command:
	// the command is drawn alone, against the status bar.
	m := opened(t, mockRegistry(), "/statusl")
	got := plain(m.Lines(th, 100, 4))
	want := []string{
		blank, blank, blank,
		fmt.Sprintf("%-20s%-64s%16s", "▸ /statusline", "choose a preset or edit the status line", "core"),
	}
	if !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
	// The same where nothing matches.
	m = opened(t, mockRegistry(), "/zzz")
	got = plain(m.Lines(th, 100, 3))
	want = []string{blank, blank, fmt.Sprintf("%-100s", "no command matches; start with // to send it")}
	if !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

func TestMoreRowsThanWanted(t *testing.T) {
	t.Parallel()
	th := mockLook().theme()
	// With every command matching, eight are listed however many rows there
	// are.
	for _, draft := range []string{"/st", "/"} {
		for _, w := range []int{100, 30} {
			m := opened(t, mockRegistry(), draft)
			want := m.Want(w)
			wanted := m.Lines(th, w, want)
			lines := m.Lines(th, w, want+3)
			if len(lines) != want+3 {
				t.Fatalf("%q at %d columns: %d lines for %d rows", draft, w, len(lines), want+3)
			}
			// The palette sits against the status bar; the spare rows are
			// above it.
			for i, line := range lines[:3] {
				if line != strings.Repeat(" ", w) {
					t.Errorf("%q at %d columns: spare row %d = %q", draft, w, i, line)
				}
			}
			if !slices.Equal(lines[3:], wanted) {
				t.Errorf("%q at %d columns: the palette changed with spare rows:\n%s", draft, w, strings.Join(plain(lines), "\n"))
			}
		}
	}
}

// styles matches the one kind of escape sequence the palette may write.
func styles() *regexp.Regexp { return regexp.MustCompile(`\x1b\[[0-9;]*m`) }

func TestHostileTextNeverReachesALine(t *testing.T) {
	t.Parallel()
	for _, l := range looks() {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			th := l.theme()
			for _, w := range widths() {
				for _, draft := range []string{"/st", "/", "/st\x1b]0;typed\x07\u009b2J\u202e"} {
					m := opened(t, hostileRegistry(), draft)
					for move := 0; move < 30; move += 3 {
						m.Move(3)
						for i, line := range m.Lines(th, w, m.Want(w)) {
							rest := styles().ReplaceAllString(line, "")
							for _, r := range rest {
								if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x200d || (r >= 0x202a && r <= 0x202e) || r == utf8.RuneError {
									t.Fatalf("%d columns, %q, line %d holds %U: %q", w, draft, i, r, line)
								}
							}
						}
					}
				}
			}
		})
	}

	// What can be read is still there, as text.
	t.Run("the words are kept", func(t *testing.T) {
		t.Parallel()
		th := mockLook().theme()
		m := opened(t, hostileRegistry(), "/st")
		// Nine commands match and eight are drawn: once from the top, and
		// once from the end.
		text := strings.Join(plain(m.Lines(th, 200, m.Want(200))), "\n")
		m.Move(8)
		text += "\n" + strings.Join(plain(m.Lines(th, 200, m.Want(200))), "\n")
		for _, want := range []string{
			"]0;ownedred [31malert[2J 5mblink    and a tab and a line",
			"an open title ]2;never closed",
			"… e\u0301 and right to left",
			"/wide.status",
			"mod evil[2J]0;o…",
			"mod a-mod-with-…",
			"👨👩👧 family at the table",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("want %q in:\n%s", want, text)
			}
		}
	})
}

func TestASCIITerminal(t *testing.T) {
	t.Parallel()
	reg := mockRegistry()
	reg.SetMod("résumé", []commands.Spec{{Name: "stamp", Summary: "résumé ✓ 日本 done"}})
	for _, l := range []look{asciiLook(), {name: "zero caps"}} {
		th := l.theme()
		for _, w := range widths() {
			for _, draft := range []string{"/st", "/", "/zzz", "/sté"} {
				m := opened(t, reg, draft)
				m.Move(5)
				for i, line := range m.Lines(th, w, m.Want(w)) {
					for j := range len(line) {
						if line[j] >= 0x80 {
							t.Fatalf("%s, %q at %d: line %d has byte %#x: %q", l.name, draft, w, i, line[j], line)
						}
					}
				}
			}
		}
		m := opened(t, reg, "/stam")
		got := ansi.Strip(m.Lines(th, 100, m.Want(100))[2])
		want := "| " + fmt.Sprintf("%-20s%-60s%16s", "> /stamp", "r?sum? ? ?? done", "mod r?sum?") + " |"
		if got != want {
			t.Errorf("%s:\n got %q\nwant %q", l.name, got, want)
		}
	}
}

func TestLongNameKeepsAGap(t *testing.T) {
	t.Parallel()
	th := mockLook().theme()
	reg := mockRegistry()
	reg.SetMod("timer", []commands.Spec{
		{Name: "exactly-16-lette", Summary: "fits"},
		{Name: "exactly-17-letter", Summary: "is cut"},
	})
	m := opened(t, reg, "/exactly")
	got := plain(m.Lines(th, 100, m.Want(100)))
	// The name column is 20 cells: the pointer, a space, the slash, 16 cells
	// of name, and a cell that is always blank.
	if !strings.HasPrefix(got[2], "│ ▸ /exactly-16-lette fits ") {
		t.Errorf("row %q", got[2])
	}
	if !strings.HasPrefix(got[3], "│   /exactly-17-lett… is cut ") {
		t.Errorf("row %q", got[3])
	}
	// Under 40 columns the name has the whole row.
	narrow := plain(m.Lines(th, 30, 2))
	if !slices.Equal(narrow, []string{"▸ /exactly-16-lette           ", "  /exactly-17-letter          "}) {
		t.Errorf("narrow rows %q", narrow)
	}
}

func TestClosedPaletteDrawsNothing(t *testing.T) {
	t.Parallel()
	th := mockLook().theme()
	reg := mockRegistry()
	m := palette.New()
	if got := m.Lines(th, 100, 9); got != nil {
		t.Errorf("a new palette draws %q", got)
	}
	m.Set(reg, "/st")
	m.Dismiss()
	if got := m.Lines(th, 100, 9); got != nil || m.Want(100) != 0 {
		t.Errorf("a dismissed palette draws %q and wants %d rows", got, m.Want(100))
	}
}
