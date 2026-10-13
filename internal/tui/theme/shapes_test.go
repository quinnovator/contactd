package theme_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// frameBar is the status bar of the Frame mock-up.
func frameBar(th *theme.Theme) (left, right []theme.Cell) {
	left = []theme.Cell{
		{Text: th.G.Dot + " live", Tone: "ok"},
		{Text: "deepseek-flash"},
		{Text: "cache 48%", Tone: "warn"},
		{Text: "3.2k tok"},
		{Text: "5.4s"},
	}
	right = []theme.Cell{{Text: "running python", Tone: "tool"}, {Text: "esc stop", Tone: "dim"}, {Text: "12:06"}}
	return left, right
}

// narrowBar is the status bar of the Compatibility mock-up's 46-column
// panels.
func narrowBar(th *theme.Theme) (left, right []theme.Cell) {
	return []theme.Cell{{Text: th.G.Dot + " live", Tone: "ok"}, {Text: "cache 82%"}, {Text: "7.2s"}},
		[]theme.Cell{{Text: "12:04"}}
}

// covered reports whether every character of a row, spaces too, is drawn
// with a background or in reverse video.
func covered(t *testing.T, row string) bool {
	t.Helper()
	cells := paint(t, row)
	for _, c := range cells {
		if c.bg == "" && !c.reverse {
			return false
		}
	}
	return len(cells) > 0
}

func TestBarAsDrawn(t *testing.T) {
	t.Parallel()
	t.Run("the Frame mock-up at 100 columns", func(t *testing.T) {
		t.Parallel()
		th := newTheme(termcaps.ColorTrue, termcaps.BgDark)
		left, right := frameBar(th)
		rule, row := th.Bar(left, right, 100)
		if rule != "" {
			t.Errorf("a filled bar has a rule: %q", rule)
		}
		plain := ansi.Strip(row)
		const l, r = " ● live │ deepseek-flash │ cache 48% │ 3.2k tok │ 5.4s", "running python │ esc stop │ 12:06 "
		if !strings.HasPrefix(plain, l+"  ") || !strings.HasSuffix(plain, "  "+r) || th.Width(row) != 100 {
			t.Errorf("row = %q (%d cells)", plain, th.Width(row))
		}
		if strings.TrimSpace(plain[len(l):len(plain)-len(r)]) != "" {
			t.Errorf("something stands between the two sides: %q", plain)
		}
		if !covered(t, row) {
			t.Errorf("the fill has a hole: %q", row)
		}
		// Each cell in its own tone, on the bar's background; the separators dim.
		want := map[string]string{
			"●": "#7dc38f", "v": "#7dc38f", "d": "", "c": "#d9b25c", "%": "#d9b25c", "3": "",
			"│": "#7f8a91", "r": "#6ea8e6", "y": "#6ea8e6", "u": "#6ea8e6", ":": "",
		}
		seen := map[string]bool{}
		for _, c := range paint(t, row) {
			if c.bg != "#1b2126" {
				t.Fatalf("%q is on %q, want the bar fill", c.text, c.bg)
			}
			if fg, ok := want[c.text]; ok && !seen[c.text] {
				seen[c.text] = true
				if c.fg != fg {
					t.Errorf("the first %q is drawn in %q, want %q", c.text, c.fg, fg)
				}
			}
		}
		if len(seen) != len(want) {
			t.Errorf("saw %v, want every one of %v", seen, want)
		}
	})
	t.Run("the Compatibility mock-up, truecolor", func(t *testing.T) {
		t.Parallel()
		th := newTheme(termcaps.ColorTrue, termcaps.BgDark)
		left, right := narrowBar(th)
		rule, row := th.Bar(left, right, 46)
		if want := "● live │ cache 82% │ 7.2s" + strings.Repeat(" ", 16) + "12:04"; rule != "" || ansi.Strip(row) != want {
			t.Errorf("rule %q, row %q, want %q", rule, ansi.Strip(row), want)
		}
		if !covered(t, row) {
			t.Errorf("the fill has a hole: %q", row)
		}
	})
	t.Run("the Compatibility mock-up, 16 colours on light", func(t *testing.T) {
		t.Parallel()
		th := newTheme(termcaps.Color16, termcaps.BgLight)
		left, right := narrowBar(th)
		rule, row := th.Bar(left, right, 46)
		if want := strings.Repeat("─", 46); ansi.Strip(rule) != want {
			t.Errorf("rule = %q, want %q", ansi.Strip(rule), want)
		}
		if want := "● live │ cache 82% │ 7.2s" + strings.Repeat(" ", 16) + "12:04"; ansi.Strip(row) != want {
			t.Errorf("row = %q, want %q", ansi.Strip(row), want)
		}
		for _, c := range paint(t, row) {
			if c.bg != "" || c.reverse {
				t.Fatalf("a bar with a rule has a fill: %q", row)
			}
		}
	})
	t.Run("the Compatibility mock-up, 30 columns", func(t *testing.T) {
		t.Parallel()
		th := newTheme(termcaps.ColorTrue, termcaps.BgDark)
		_, row := th.Bar([]theme.Cell{{Text: "● live", Tone: "ok"}, {Text: "82%"}}, []theme.Cell{{Text: "12:04"}}, 30)
		if want := "● live │ 82%" + strings.Repeat(" ", 13) + "12:04"; ansi.Strip(row) != want {
			t.Errorf("row = %q, want %q", ansi.Strip(row), want)
		}
	})
	t.Run("the Compatibility mock-up, no colour and ASCII", func(t *testing.T) {
		t.Parallel()
		th := asciiTheme(termcaps.ColorNone, termcaps.BgUnknown)
		left, right := narrowBar(th)
		rule, row := th.Bar(left, right, 46)
		if rule != strings.Repeat("-", 46) {
			t.Errorf("rule = %q, want 46 hyphens and nothing else", rule)
		}
		if want := "* live | cache 82% | 7.2s" + strings.Repeat(" ", 16) + "12:04"; ansi.Strip(row) != want {
			t.Errorf("row = %q, want %q", ansi.Strip(row), want)
		}
	})
}

func TestBarGolden(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for _, c := range colors() {
		for _, bg := range backgrounds() {
			for _, th := range []*theme.Theme{newTheme(c, bg), asciiTheme(c, bg)} {
				fmt.Fprintf(&b, "%s %s, filled %v\n", capsName(c, bg), th.Caps.Glyphs, th.BarFilled)
				for _, w := range []int{100, 46} {
					left, right := frameBar(th)
					if w == 46 {
						left, right = narrowBar(th)
					}
					rule, row := th.Bar(left, right, w)
					if rule != "" {
						b.WriteString(quoted(rule))
					}
					b.WriteString(quoted(row))
				}
				b.WriteByte('\n')
			}
		}
	}
	tuitest.Golden(t, "bar.golden", []byte(b.String()))
}

func TestBarPaddingAndGivingWay(t *testing.T) {
	t.Parallel()
	th := newTheme(termcaps.ColorNone, termcaps.BgUnknown)
	left := []theme.Cell{{Text: "● live"}, {Text: ""}, {Text: "cache 82%"}}
	right := []theme.Cell{{Text: ""}, {Text: "12:04"}}
	cases := []struct {
		w    int
		want string
	}{
		{61, " ● live │ cache 82%" + strings.Repeat(" ", 36) + "12:04 "},
		{60, " ● live │ cache 82%" + strings.Repeat(" ", 35) + "12:04 "},
		{59, "● live │ cache 82%" + strings.Repeat(" ", 36) + "12:04"},
		{25, "● live │ cache 82%  12:04"},
		{24, "● live │ cache 82%      "}, // the right side gives way whole
		{18, "● live │ cache 82%"},
		{17, "● live │ cache 8…"}, // then the left is truncated
		{1, "…"},
		{0, ""},
		{-1, ""},
	}
	for _, tc := range cases {
		if _, row := th.Bar(left, right, tc.w); ansi.Strip(row) != tc.want {
			t.Errorf("Bar at %d = %q, want %q", tc.w, ansi.Strip(row), tc.want)
		}
	}
	if _, row := th.Bar(nil, nil, 12); row != strings.Repeat(" ", 12) {
		t.Errorf("an empty bar = %q, want 12 spaces", row)
	}
}

func TestBarIsExactlyTheWidth(t *testing.T) {
	t.Parallel()
	for _, c := range colors() {
		for _, bg := range backgrounds() {
			for _, th := range []*theme.Theme{newTheme(c, bg), asciiTheme(c, bg)} {
				left, right := frameBar(th)
				left = append(left, theme.Cell{Text: cjkText, Tone: "mod"}, theme.Cell{Text: skinTone})
				for w := 1; w <= 200; w++ {
					rule, row := th.Bar(left, right, w)
					if got := th.Width(row); got != w {
						t.Fatalf("%s: the bar at %d is %d cells wide: %q", capsName(c, bg), w, got, row)
					}
					if th.BarFilled != (rule == "") {
						t.Fatalf("%s: filled %v, rule %q", capsName(c, bg), th.BarFilled, rule)
					}
					if rule != "" && th.Width(rule) != w {
						t.Fatalf("%s: the rule at %d is %d cells wide", capsName(c, bg), w, th.Width(rule))
					}
					if th.BarFilled && !covered(t, row) {
						t.Fatalf("%s: the fill at %d has a hole: %q", capsName(c, bg), w, row)
					}
				}
			}
		}
	}
}

func TestBoxKeepsItsRightBorder(t *testing.T) {
	t.Parallel()
	lines := []string{
		zwjFamily, skinTone, cjkText, "", "plain",
		// Characters that join whatever stands beside them: a row is not
		// always as wide as its parts.
		"🏽 begins with a skin tone", "ends in a number sign \u0600", "0\ufe0f00", "\u0301\u0301", "\ufe0f",
		"mixed 中文 and 👍🏽 and e\u0301 and ⏺\ufe0f text",
		"\x1b[1;38;5;80mstyled " + cjkText + "\x1b[m and on",
	}
	for _, method := range widthMethods() {
		for _, glyphs := range []termcaps.Glyphs{termcaps.GlyphsUnicode, termcaps.GlyphsASCII} {
			th := theme.New(termcaps.Caps{
				Color: termcaps.ColorTrue, Background: termcaps.BgDark, Glyphs: glyphs, Width: method,
			}, theme.Options{})
			for _, w := range []int{100, 46, 40, 30, 21, 20, 12, 7, 6, 5} {
				rows := th.Box(lines, w, "", "")
				if len(rows) != len(lines)+2 {
					t.Fatalf("%v %v at %d: %d rows for %d lines", method, glyphs, w, len(rows), len(lines))
				}
				for i, row := range rows {
					if got := th.Width(row); got != w {
						t.Errorf("%v %v at %d: row %d, %q, is %d cells wide", method, glyphs, w, i, ansi.Strip(row), got)
					}
				}
				for i, row := range rows[1 : len(rows)-1] {
					plain := ansi.Strip(row)
					if !strings.HasPrefix(plain, th.G.BoxV+" ") || !strings.HasSuffix(plain, " "+th.G.BoxV) {
						t.Errorf("%v %v at %d: line %d, %q, lost a border", method, glyphs, w, i, plain)
					}
				}
				top, bottom := ansi.Strip(rows[0]), ansi.Strip(rows[len(rows)-1])
				if want := th.G.BoxTL + strings.Repeat(th.G.BoxH, w-2) + th.G.BoxTR; top != want {
					t.Errorf("%v %v at %d: top = %q, want %q", method, glyphs, w, top, want)
				}
				if want := th.G.BoxBL + strings.Repeat(th.G.BoxH, w-2) + th.G.BoxBR; bottom != want {
					t.Errorf("%v %v at %d: bottom = %q, want %q", method, glyphs, w, bottom, want)
				}
			}
		}
	}
}

func TestBoxKeepsALinesStyleInside(t *testing.T) {
	t.Parallel()
	// A line that switches a style on and never off must not draw the right
	// border in it, nor the rows under it. Without colour the border has no
	// style of its own to end the line's with.
	lines := []string{"\x1b[7mreversed and left on", "\x1b[4;38;5;203munderlined " + cjkText, "next"}
	for _, c := range colors() {
		for _, w := range []int{40, 16, 4} {
			th := newTheme(c, termcaps.BgDark)
			rows := th.Box(lines, w, "", "")
			cells := paint(t, strings.Join(rows, ""))
			if len(cells) == 0 {
				t.Fatalf("%v at %d: nothing drawn", c, w)
			}
			for _, got := range cells {
				border := strings.ContainsAny(got.text, "│─╭╮╰╯")
				if border && (got.reverse || got.underline) {
					t.Fatalf("%v at %d: border %q drawn %+v", c, w, got.text, got)
				}
			}
			last := paint(t, strings.Join(rows[:len(rows)-1], ""))
			if got := last[len(last)-1]; got.reverse || got.underline || strings.HasPrefix(got.fg, "256:203") {
				t.Errorf("%v at %d: the row under a styled line ends %+v", c, w, got)
			}
			// The style still reaches the line's own text.
			if got := cells[len([]rune(ansi.Strip(rows[0])))+2]; w >= 5 && !got.reverse {
				t.Errorf("%v at %d: the line lost its own style: %+v", c, w, got)
			}
		}
	}
	// A bar with no fill has nothing after it to end a style either.
	th := newTheme(termcaps.ColorNone, termcaps.BgUnknown)
	_, row := th.Bar([]theme.Cell{{Text: "\x1b[7mlive"}}, []theme.Cell{{Text: "12:04"}}, 30)
	cells := paint(t, row+"x")
	if got := cells[len(cells)-1]; got.reverse {
		t.Errorf("a style left on in a cell reaches past the bar: %q", row)
	}
	if got := find(t, cells, "12:04")[0]; got.reverse {
		t.Errorf("a style left on in a cell reaches the other side: %q", row)
	}
}

func TestBox(t *testing.T) {
	t.Parallel()
	th := newTheme(termcaps.ColorNone, termcaps.BgUnknown)
	cases := []struct {
		name         string
		lines        []string
		w            int
		title, count string
		want         []string
	}{
		{"one line", []string{"hi"}, 10, "", "", []string{
			"╭────────╮",
			"│ hi     │",
			"╰────────╯",
		}},
		{"a long line is truncated", []string{"hello world"}, 10, "", "", []string{
			"╭────────╮",
			"│ hello… │",
			"╰────────╯",
		}},
		{"no lines", nil, 6, "", "", []string{"╭────╮", "╰────╯"}},
		{"title and count", []string{"/stop"}, 24, "commands", "1 of 23", []string{
			"╭──────────────────────╮",
			"│ commands     1 of 23 │",
			"│ /stop                │",
			"╰──────────────────────╯",
		}},
		{"the count gives way to the title", []string{"/stop"}, 20, "commands", "1 of 23", []string{
			"╭──────────────────╮",
			"│ commands         │",
			"│ /stop            │",
			"╰──────────────────╯",
		}},
		{"a count alone", []string{"x"}, 12, "", "1 of 2", []string{
			"╭──────────╮",
			"│   1 of 2 │",
			"│ x        │",
			"╰──────────╯",
		}},
		{"the narrowest box", []string{"abc"}, 5, "", "", []string{"╭───╮", "│ … │", "╰───╯"}},
		{"too narrow for a border", []string{"abc", "d"}, 4, "", "", []string{"────", "abc ", "d   ", "────"}},
		{"too narrow, with a title", []string{"abc"}, 3, "title", "9", []string{"───", "ti…", "abc", "───"}},
		{"no width", []string{"abc"}, 0, "t", "", []string{"", "", "", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := th.Box(tc.lines, tc.w, tc.title, tc.count)
			for i := range got {
				got[i] = ansi.Strip(got[i])
			}
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") || len(got) != len(tc.want) {
				t.Errorf("Box =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestBoxGolden(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for _, th := range []*theme.Theme{
		newTheme(termcaps.ColorTrue, termcaps.BgDark),
		newTheme(termcaps.Color16, termcaps.BgLight),
		asciiTheme(termcaps.ColorNone, termcaps.BgUnknown),
	} {
		row := th.Style("accent").Render(th.G.Pointer+" /statusline") + "  choose a preset or edit the status line"
		lines := []string{th.Selected(row, 56), "  /status      connection, route and cache for this contact"}
		fmt.Fprintf(&b, "%s %s\n", capsName(th.Caps.Color, th.Caps.Background), th.Caps.Glyphs)
		b.WriteString(quoted(th.Box(lines, 60, "commands matching /st", "2 of 23")...))
		b.WriteByte('\n')
	}
	tuitest.Golden(t, "box.golden", []byte(b.String()))
}

func TestBoxWithWideBorderGlyphs(t *testing.T) {
	t.Parallel()
	// A replaced glyph may be up to three cells wide. The box still closes.
	th := theme.New(termcaps.Caps{Color: termcaps.ColorNone, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc}, theme.Options{
		Glyph: map[string]string{"boxv": "||", "boxh": "=-", "boxtl": "/", "hrule": "中"},
	})
	widest := theme.New(termcaps.Caps{Color: termcaps.ColorNone, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc}, theme.Options{
		Glyph: map[string]string{
			"boxv": "|||", "boxh": "===", "boxtl": "/--", "boxtr": "--\\", "boxbl": "\\__", "boxbr": "__/",
			"hrule": "-=-", "sep": "<|>", "ellipsis": "...",
		},
	})
	for _, th := range []*theme.Theme{th, widest} {
		for w := 1; w <= 30; w++ {
			for i, row := range th.Box([]string{"hello", cjkText}, w, "title", "3") {
				if got := th.Width(row); got != w {
					t.Errorf("at %d, row %d, %q, is %d cells wide", w, i, row, got)
				}
			}
			for _, label := range []string{"", "Mon 12 Oct"} {
				if got := th.Width(th.Rule(w, label)); got != w {
					t.Errorf("Rule(%d, %q) is %d cells wide", w, label, got)
				}
			}
			left, right := narrowBar(th)
			if rule, row := th.Bar(left, right, w); th.Width(rule) != w || th.Width(row) != w {
				t.Errorf("Bar at %d: rule %q, row %q", w, rule, row)
			}
		}
	}
	got := th.Box([]string{"hello"}, 13, "", "")
	want := []string{"/=-=-=-=-=- ╮", "|| hello   ||", "╰=-=-=-=-=- ╯"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Box =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRule(t *testing.T) {
	t.Parallel()
	uni := newTheme(termcaps.ColorNone, termcaps.BgUnknown)
	ascii := asciiTheme(termcaps.ColorNone, termcaps.BgUnknown)
	cases := []struct {
		name  string
		th    *theme.Theme
		w     int
		label string
		want  string
	}{
		{"plain", uni, 8, "", "────────"},
		{"ascii", ascii, 8, "", "--------"},
		{"label in the middle", uni, 20, "Mon 12 Oct", "──── Mon 12 Oct ────"},
		{"an odd cell goes to the right", uni, 21, "Mon 12 Oct", "──── Mon 12 Oct ─────"},
		{"ascii label", ascii, 16, "today", "---- today -----"},
		{"a long label is truncated", uni, 12, "Mon 12 Oct 2026", "── Mon 1… ──"},
		{"a short label", uni, 9, "Mon", "── Mon ──"},
		{"the narrowest labelled rule", uni, 7, "M", "── M ──"},
		{"the narrowest labelled rule, label cut", uni, 7, "Mon", "── … ──"},
		{"too narrow for a line", uni, 6, "Monday", "Monday"},
		{"too narrow for the label", uni, 4, "Monday", "Mon…"},
		{"no width", uni, 0, "Monday", ""},
		{"negative", uni, -4, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.th.Rule(tc.w, tc.label)
			if ansi.Strip(got) != tc.want {
				t.Errorf("Rule(%d, %q) = %q, want %q", tc.w, tc.label, ansi.Strip(got), tc.want)
			}
			if tc.th.Width(got) != max(tc.w, 0) {
				t.Errorf("Rule(%d, %q) is %d cells wide", tc.w, tc.label, tc.th.Width(got))
			}
		})
	}

	// The line is in the rule tone; the label has to be read, so it is dim.
	th := newTheme(termcaps.ColorTrue, termcaps.BgDark)
	for _, c := range paint(t, th.Rule(30, "Mon 12 Oct")) {
		switch {
		case c.text == "─" && c.fg != "#2f373e":
			t.Errorf("the line is drawn in %q", c.fg)
		case c.text != "─" && c.text != " " && c.fg != "#7f8a91":
			t.Errorf("the label's %q is drawn in %q", c.text, c.fg)
		}
	}
}

func TestSelectedAndCodeFillHaveNoHoles(t *testing.T) {
	t.Parallel()
	// Rows that switch styles off in every way a library might.
	rows := []string{
		"plain",
		"",
		"\x1b[1;38;2;94;196;200m▸ /statusline\x1b[m  choose a preset  \x1b[38;5;244mcore\x1b[m",
		"\x1b[38;5;203mfunc\x1b[0m main() \x1b[38;5;0m{\x1b[0m",
		"a\x1b[39;49mb\x1b[0;1mc\x1b[22md\x1b[27me\x1b[me",
		"\x1b[48;5;52mdeleted\x1b[49m kept",
		cjkText + " " + skinTone,
	}
	for _, c := range colors() {
		for _, bg := range backgrounds() {
			th := newTheme(c, bg)
			for _, row := range rows {
				for _, w := range []int{1, 8, 40} {
					selected := th.Selected(row, w)
					if th.Width(selected) != w || !covered(t, selected) {
						t.Errorf("%s: Selected(%q, %d) = %q", capsName(c, bg), row, w, selected)
					}
					code := th.CodeFill(row, w)
					if th.BarFilled {
						if th.Width(code) != w || !covered(t, code) {
							t.Errorf("%s: CodeFill(%q, %d) = %q", capsName(c, bg), row, w, code)
						}
					} else if code != th.Truncate(row, w) {
						t.Errorf("%s: CodeFill(%q, %d) = %q, want the row cut and nothing more", capsName(c, bg), row, w, code)
					}
				}
			}
			if th.Selected("x", 0) != "" || th.CodeFill("x", 0) != "" {
				t.Errorf("%s: a row of no width is not empty", capsName(c, bg))
			}
		}
	}
}

func TestSelectedKeepsTheRowsOwnColours(t *testing.T) {
	t.Parallel()
	th := newTheme(termcaps.ColorTrue, termcaps.BgDark)
	row := th.Style("accent").Render("▸ /stop") + " stop the reply " + th.Style("mod").Render("mod x")
	cells := paint(t, th.Selected(row, 40))
	if got := text(cells); got != "▸ /stop stop the reply mod x"+strings.Repeat(" ", 12) {
		t.Fatalf("text = %q", got)
	}
	if cells[0].fg != "#5ec4c8" || !cells[0].bold || cells[8].fg != "" || cells[8].bold || cells[23].fg != "#c49ae0" {
		t.Errorf("colours changed: %+v %+v %+v", cells[0], cells[8], cells[23])
	}
	// Where there is no fill the row is reversed, colours and all.
	plain := newTheme(termcaps.Color16, termcaps.BgDark)
	for _, c := range paint(t, plain.Selected(plain.Style("accent").Render("▸ /stop")+" rest", 20)) {
		if !c.reverse || c.bg != "" {
			t.Fatalf("drawn %+v, want reverse video and no background", c)
		}
	}
}

func TestASCIIGlyphsAddOnlyASCII(t *testing.T) {
	t.Parallel()
	for _, c := range colors() {
		for _, bg := range backgrounds() {
			for _, method := range widthMethods() {
				th := theme.New(termcaps.Caps{Color: c, Background: bg, Glyphs: termcaps.GlyphsASCII, Width: method}, theme.Options{
					Glyph: map[string]string{"sep": "│", "boxv": "│", "hrule": "─", "ellipsis": "…"},
				})
				out := make([]string, 0, 256)
				for _, w := range []int{100, 60, 46, 30, 12, 6, 4, 2} {
					out = append(out, th.Box([]string{"a line of text that is long enough to be cut", ""}, w, "commands matching /st", "5 of 23")...)
					out = append(out, th.Box([]string{"short"}, w, "", "")...)
					out = append(out, th.Rule(w, ""), th.Rule(w, "Mon 12 Oct 2026"))
					left, right := frameBar(th)
					rule, row := th.Bar(left, right, w)
					out = append(out, rule, row, th.Selected("a row that is selected", w), th.CodeFill("x := 1", w),
						th.Truncate("a line of text that is long enough to be cut", w), th.Spread("left", "right", w))
					out = append(out, th.Wrap("words to wrap at a narrow width", w)...)
				}
				for _, s := range out {
					for i := range len(s) {
						if s[i] >= 0x80 {
							t.Fatalf("%s %v: %q holds byte %#x", capsName(c, bg), method, s, s[i])
						}
					}
				}
			}
		}
	}
}
