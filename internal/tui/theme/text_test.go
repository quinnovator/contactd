package theme_test

import (
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// samples are lines of text that are awkward to measure, cut or wrap.
func samples() []string {
	bold := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#5ec4c8"))
	return []string{
		"",
		"a",
		"hello world",
		"and what would the share have been with the fix?",
		"https://example.com/docs/provider-cache/breakpoints?turn=6&detail=full",
		"    indented    with   runs of   spaces   ",
		zwjFamily,
		skinTone,
		cjkText,
		"mixed 中文 and 👍🏽 and e\u0301 and ⏺\ufe0f text",
		bold.Render("styled") + " then plain then " + bold.Render("styled again, at some length"),
		"\x1b[38;5;203mopened and never closed",
	}
}

func TestWidth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s            string
		wc, grapheme int
	}{
		{"", 0, 0},
		{"hello", 5, 5},
		{"\x1b[1;38;5;80mhello\x1b[m", 5, 5},
		{"中文", 4, 4},
		{"👨\u200d👩\u200d👧", 6, 2}, // three people, or one family
		{"👍🏽", 4, 2},              // a hand and a colour, or one hand
		{"e\u0301", 1, 1},
		{"⏺\ufe0f", 1, 2},
	}
	wc, grapheme := withWidth(termcaps.WidthWc), withWidth(termcaps.WidthGrapheme)
	unset := theme.New(termcaps.Caps{}, theme.Options{})
	for _, tc := range cases {
		if got := wc.Width(tc.s); got != tc.wc {
			t.Errorf("wcwidth of %q = %d, want %d", tc.s, got, tc.wc)
		}
		if got := grapheme.Width(tc.s); got != tc.grapheme {
			t.Errorf("grapheme width of %q = %d, want %d", tc.s, got, tc.grapheme)
		}
		if got := unset.Width(tc.s); got != tc.wc {
			t.Errorf("width of %q with no method set = %d, want wcwidth's %d", tc.s, got, tc.wc)
		}
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	uni, grapheme := withWidth(termcaps.WidthWc), withWidth(termcaps.WidthGrapheme)
	ascii := asciiTheme(termcaps.ColorNone, termcaps.BgUnknown)
	cases := []struct {
		name string
		th   *theme.Theme
		s    string
		w    int
		want string
	}{
		{"fits", uni, "hello", 5, "hello"},
		{"cut", uni, "hello world", 5, "hell…"},
		{"one cell", uni, "hello", 1, "…"},
		{"no cells", uni, "hello", 0, ""},
		{"negative", uni, "hello", -3, ""},
		{"ascii tail", ascii, "hello world", 5, "he..."},
		{"ascii tail alone", ascii, "hello world", 3, "..."},
		{"no room for the ascii tail", ascii, "hello world", 2, "he"},
		{"wide character does not straddle the edge", uni, "这是中文", 4, "这…"},
		{"wide characters", uni, "这是中文", 5, "这是…"},
		{"style is closed after the cut", uni, "\x1b[1mhello world\x1b[m", 5, "\x1b[1mhell…\x1b[m"},
		{"later styles are kept, their text is not", uni, "ab\x1b[1mcdef\x1b[m gh \x1b[2mij\x1b[m", 4, "ab\x1b[1mc…\x1b[m\x1b[2m\x1b[m"},
		// x/ansi's own TruncateWc lets this one through at 7 cells.
		{"skin tone counted per character", uni, "👍🏽 ok", 5, "👍🏽…"},
		{"skin tone counted per cluster", grapheme, "👍🏽 ok", 5, "👍🏽 ok"},
		{"family counted per character", uni, "👨\u200d👩\u200d👧 ok", 8, "👨\u200d👩\u200d👧 …"},
		{"family counted per cluster", grapheme, "👨\u200d👩\u200d👧 family", 4, "👨\u200d👩\u200d👧 …"},
		// A digit and a variation selector are one character of two cells.
		{"keycap counted per cluster", grapheme, "0\ufe0f00", 3, "0\ufe0f…"},
		{"keycap counted per character", uni, "0\ufe0f00", 3, "0\ufe0f00"},
		{"a byte that is not UTF-8", uni, "ab\xffcd", 10, "ab\ufffdcd"},
		{"a byte that is not UTF-8, with ASCII glyphs", ascii, "ab\xffcd", 10, "ab?cd"},
		{"a tab is four spaces", uni, "a\tb", 10, "a    b"},
		{"a tab is counted when the text is cut", uni, "a\tbcdef", 7, "a    b…"},
		{"a newline is a space", uni, "one\ntwo", 10, "one two"},
		{"other control characters are dropped", uni, "a\rb\bc\x07d\x00e\x7ff", 10, "abcdef"},
		{"a C1 control character is dropped", uni, "a\u009b31mb", 10, "a31mb"},
		{"the bell that ends a link is kept", uni, "\x1b]8;;https://example.com/\x07a\tb\x1b]8;;\x07", 10, "\x1b]8;;https://example.com/\x07a    b\x1b]8;;\x07"},
		{"a control character inside a link's address is dropped", uni, "\x1b]8;;https://exa\nmple.com/\x1b\\ab\x1b]8;;\x1b\\", 10, "\x1b]8;;https://example.com/\x1b\\ab\x1b]8;;\x1b\\"},
		{"half an escape sequence that fits is left alone", uni, "abc\x1b[31", 10, "abc\x1b[31"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.th.Truncate(tc.s, tc.w)
			if got != tc.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tc.s, tc.w, got, tc.want)
			}
			if w := tc.th.Width(got); w > max(tc.w, 0) {
				t.Errorf("Truncate(%q, %d) is %d cells wide", tc.s, tc.w, w)
			}
		})
	}
}

func TestTruncateAndPadKeepTheWidth(t *testing.T) {
	t.Parallel()
	for _, method := range widthMethods() {
		for _, th := range []*theme.Theme{
			withWidth(method),
			theme.New(termcaps.Caps{Glyphs: termcaps.GlyphsASCII, Width: method}, theme.Options{}),
		} {
			for _, s := range samples() {
				full := th.Width(s)
				for w := -1; w <= full+2; w++ {
					cut := th.Truncate(s, w)
					if got := th.Width(cut); got > max(w, 0) {
						t.Errorf("%v: Truncate(%q, %d) is %d cells wide", method, s, w, got)
					}
					if w >= full && cut != s {
						t.Errorf("%v: Truncate(%q, %d) changed text that fits: %q", method, s, w, cut)
					}
					if w < full && w >= th.Width(th.G.Ellipsis) && !strings.Contains(ansi.Strip(cut), th.G.Ellipsis) {
						t.Errorf("%v: Truncate(%q, %d) = %q has no ellipsis", method, s, w, cut)
					}
					// What is left is the start of what there was.
					kept := strings.TrimSuffix(ansi.Strip(cut), th.G.Ellipsis)
					if w < full && !strings.HasPrefix(ansi.Strip(s), kept) {
						t.Errorf("%v: Truncate(%q, %d) = %q is not a prefix", method, s, w, cut)
					}
					if got := th.Width(th.Pad(s, w)); got != max(w, 0) {
						t.Errorf("%v: Pad(%q, %d) is %d cells wide", method, s, w, got)
					}
				}
			}
		}
	}
}

func TestTextThatCannotBeMeasuredIsDrawnPlain(t *testing.T) {
	t.Parallel()
	// Half an escape sequence at the end of a row would swallow the padding
	// after it, and the border after that. Nothing of the kind should reach
	// the theme; if it does, the row keeps its width and loses its styles.
	for _, method := range widthMethods() {
		th := withWidth(method)
		for _, s := range []string{"abc\x1b[31", "abc\x1b", "\x1b[1mabc\x1b]0;title", "abc\x1bP+q", "ab\xf0", "\xe6\xe5"} {
			padded := th.Pad(s, 8)
			if th.Width(padded) != 8 || strings.Contains(padded, "\x1b") || !strings.HasSuffix(padded, "   ") {
				t.Errorf("%v: Pad(%q, 8) = %q", method, s, padded)
			}
			rows := th.Box([]string{s}, 12, "", "")
			if got := ansi.Strip(rows[1]); th.Width(rows[1]) != 12 || !strings.HasSuffix(got, " │") || !strings.HasPrefix(got, "│ ") {
				t.Errorf("%v: Box(%q) row = %q", method, s, rows[1])
			}
		}
		// Whole sequences are measured, and kept.
		const whole = "\x1b[1mabc\x1b[m\x1b]8;;https://example.com\x07link\x1b]8;;\x07"
		if got := th.Pad(whole, 9); got != whole+"  " {
			t.Errorf("%v: Pad(%q, 9) = %q", method, whole, got)
		}
		if got := th.Truncate(whole, 6); got != "\x1b[1mabc\x1b[m\x1b]8;;https://example.com\x07li…\x1b]8;;\x07" {
			t.Errorf("%v: Truncate(%q, 6) = %q", method, whole, got)
		}
	}
}

// controls reports whether s holds a control character a terminal would act
// on: anything under 0x20 but the ESC and BEL of an escape sequence, DEL, or a
// C1 control.
func controls(s string) bool {
	for i, r := range s {
		switch {
		case r == 0x1b, r == 0x07:
		case r < 0x20, r >= 0x7f && r < 0xa0:
			return true
		case r == 0xfffd && s[i] != 0xef:
			return true // a byte that is not UTF-8
		}
	}
	return false
}

func TestRowsHoldNoControlCharacters(t *testing.T) {
	t.Parallel()
	// Width counts a tab or a newline as no cells and a terminal moves its
	// cursor for it, so a row that kept one would lose its right edge on the
	// screen while every measurement said it was whole.
	texts := []string{
		"tab\there", "new\nline", "return\rhere", "back\bspace", "bell\x07here", "nul\x00here", "del\x7fhere",
		"csi\u009b31mhere", "\x1b[1mbold\ttab\x1b[m", "\t", "\n\n", "中\t文", "not\xffutf8",
	}
	for _, method := range widthMethods() {
		for _, th := range []*theme.Theme{
			withWidth(method),
			theme.New(termcaps.Caps{Color: termcaps.ColorNone, Glyphs: termcaps.GlyphsASCII, Width: method}, theme.Options{}),
		} {
			for _, s := range texts {
				for _, w := range []int{40, 12, 6, 3} {
					_, bar := th.Bar([]theme.Cell{{Text: s, Tone: "ok"}, {Text: s}}, []theme.Cell{{Text: s, Tone: "dim"}}, w)
					rows := append(th.Box([]string{s}, w, s, s),
						th.Pad(s, w), th.Rule(w, s), th.Spread(s, s, w), th.Selected(s, w), bar)
					for _, row := range rows {
						if th.Width(row) != w {
							t.Errorf("%v: %q in %d cells: row %q is %d cells wide", method, s, w, row, th.Width(row))
						}
					}
					rows = append(rows, th.Truncate(s, w), th.CodeFill(s, w))
					rows = append(rows, th.Wrap(s, w)...)
					rows = append(rows, th.Hardwrap(s, w)...)
					for _, row := range rows {
						if controls(row) {
							t.Errorf("%v: %q in %d cells: row %q holds a control character", method, s, w, row)
						}
					}
				}
			}
		}
	}
	// What a tab was is still there, as spaces, where the terminal will draw it.
	th := withWidth(termcaps.WidthWc)
	if got := ansi.Strip(th.Box([]string{"a\tb"}, 12, "", "")[1]); got != "│ a    b   │" {
		t.Errorf("a boxed tab = %q", got)
	}
	if got := th.Spread("a\tb", "c\nd", 14); got != "a    b     c d" {
		t.Errorf("Spread = %q", got)
	}
}

func TestASCIIGlyphsAddNothingForBrokenText(t *testing.T) {
	t.Parallel()
	// A byte that is not UTF-8 has to become something. With ASCII glyphs it
	// is not U+FFFD, which is three bytes above 0x7F.
	th := asciiTheme(termcaps.ColorNone, termcaps.BgUnknown)
	const s = "ab\xffcd \xe6\xe5 ef"
	rows := append(th.Box([]string{s}, 20, s, ""), th.Pad(s, 20), th.Truncate(s, 5), th.Rule(30, s), th.Spread(s, s, 40))
	rows = append(rows, th.Wrap(s, 6)...)
	rows = append(rows, th.Hardwrap(s, 6)...)
	for _, row := range rows {
		for i := range len(row) {
			if row[i] >= 0x80 {
				t.Fatalf("%q holds byte %#x", row, row[i])
			}
		}
	}
	if got := th.Pad(s, 14); got != "ab?cd ? ef    " {
		t.Errorf("Pad = %q", got)
	}
}

func TestPad(t *testing.T) {
	t.Parallel()
	th := withWidth(termcaps.WidthWc)
	cases := []struct {
		s    string
		w    int
		want string
	}{
		{"ab", 5, "ab   "},
		{"abcde", 5, "abcde"},
		{"abcdef", 5, "abcd…"},
		{"", 3, "   "},
		{"ab", 0, ""},
		{"这是中文", 4, "这… "}, // the cut leaves a cell over, which is padded
		{"\x1b[1mab\x1b[m", 4, "\x1b[1mab\x1b[m  "},
	}
	for _, tc := range cases {
		if got := th.Pad(tc.s, tc.w); got != tc.want {
			t.Errorf("Pad(%q, %d) = %q, want %q", tc.s, tc.w, got, tc.want)
		}
	}
}

func TestWrap(t *testing.T) {
	t.Parallel()
	th := withWidth(termcaps.WidthWc)
	cases := []struct {
		name string
		s    string
		w    int
		want []string
	}{
		{"empty", "", 10, []string{""}},
		{"fits", "hello world", 11, []string{"hello world"}},
		{"breaks at a space", "hello world", 10, []string{"hello", "world"}},
		{"a word that fills the line", "hello world", 5, []string{"hello", "world"}},
		{"several words a line", "the quick brown fox jumps", 10, []string{"the quick", "brown fox", "jumps"}},
		{"a long word is broken", "abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"a long word after a short one", "ab cdefghij", 4, []string{"ab", "cdef", "ghij"}},
		{"newlines are kept", "one\n\ntwo", 10, []string{"one", "", "two"}},
		{"runs of spaces inside a line are kept", "a  b", 10, []string{"a  b"}},
		{"spaces at a break go", "aaaa    bbbb", 4, []string{"aaaa", "bbbb"}},
		{"a trailing space adds no line", "hello ", 5, []string{"hello"}},
		{"indentation is kept", "  indented text here", 12, []string{"  indented", "text here"}},
		{"an indented long word leaves no blank line", "  abcdefghij", 6, []string{"  abcd", "efghij"}},
		{"indentation wider than the line goes", "        ab", 4, []string{"ab"}},
		{"a tab is four spaces", "\tab", 10, []string{"    ab"}},
		{"wide characters", "这是中文这是", 5, []string{"这是", "中文", "这是"}},
		{"width under one is one", "abc", 0, []string{"a", "b", "c"}},
		// A hand and a skin tone are four cells counted per character.
		{"a character wider than the line leaves no blank line", "x 👍🏽 y", 3, []string{"x", "…", "y"}},
		{"a wide character in one cell", "a 中 b", 1, []string{"a", "…", "b"}},
		{"a carriage return is dropped", "one\r\ntwo", 10, []string{"one", "two"}},
		{"control characters are dropped", "a\bb\x07c\x7fd\u009be", 10, []string{"abcde"}},
		{
			"a style is carried over a break",
			"\x1b[1mhello world\x1b[m", 5,
			[]string{"\x1b[1mhello\x1b[m", "\x1b[1mworld\x1b[m"},
		},
		{
			"a style that ended is not carried",
			"\x1b[1mhello\x1b[m world", 5,
			[]string{"\x1b[1mhello\x1b[m", "world"},
		},
		{
			"a style is carried over a newline",
			"\x1b[38;5;80mone\ntwo\x1b[m three", 20,
			[]string{"\x1b[38;5;80mone\x1b[m", "\x1b[38;5;80mtwo\x1b[m three"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := th.Wrap(tc.s, tc.w); !slices.Equal(got, tc.want) {
				t.Errorf("Wrap(%q, %d) =\n %q, want\n %q", tc.s, tc.w, got, tc.want)
			}
		})
	}
}

func TestHardwrap(t *testing.T) {
	t.Parallel()
	th := withWidth(termcaps.WidthWc)
	cases := []struct {
		name string
		s    string
		w    int
		want []string
	}{
		{"empty", "", 10, []string{""}},
		{"fits", "hello", 5, []string{"hello"}},
		{"breaks where the line is full", "hello world", 4, []string{"hell", "o wo", "rld"}},
		{"spaces are kept", "a    b", 3, []string{"a  ", "  b"}},
		{"newlines are kept", "ab\n\ncdef", 3, []string{"ab", "", "cde", "f"}},
		{"a wide character does not straddle the edge", "这是中文", 3, []string{"这", "是", "中", "文"}},
		{"a tab is four spaces", "\tx", 3, []string{"   ", " x"}},
		{"width under one is one", "abc", -2, []string{"a", "b", "c"}},
		{"a carriage return is dropped", "ab\r\ncd", 3, []string{"ab", "cd"}},
		{"control characters are dropped", "a\bb\x07c\x7fd\u009be", 3, []string{"abc", "de"}},
		{
			"the colour in force is put back on each row",
			"\x1b[38;5;80mhello world\x1b[m", 4,
			[]string{"\x1b[38;5;80mhell\x1b[m", "\x1b[38;5;80mo wo\x1b[m", "\x1b[38;5;80mrld\x1b[m"},
		},
		{
			"only the colour in force",
			"\x1b[31mab\x1b[0m\x1b[1;32mcdef\x1b[0mgh", 3,
			[]string{"\x1b[31mab\x1b[0m\x1b[1;32mc\x1b[m", "\x1b[1;32mdef\x1b[0m", "gh"},
		},
		{
			"a reset that also sets a style starts again from it",
			"\x1b[31ma\x1b[0;4mbcd", 2,
			[]string{"\x1b[31ma\x1b[0;4mb\x1b[m", "\x1b[0;4mcd\x1b[m"},
		},
		{
			"a reset written with more zeros is a reset",
			"\x1b[1mab\x1b[00mcd", 3,
			[]string{"\x1b[1mab\x1b[00mc", "d"},
		},
		{
			"a palette index of zero is not a reset",
			"\x1b[1m\x1b[38;5;0mabcd", 2,
			[]string{"\x1b[1m\x1b[38;5;0mab\x1b[m", "\x1b[1m\x1b[38;5;0mcd\x1b[m"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := th.Hardwrap(tc.s, tc.w); !slices.Equal(got, tc.want) {
				t.Errorf("Hardwrap(%q, %d) =\n %q, want\n %q", tc.s, tc.w, got, tc.want)
			}
		})
	}
}

// squeeze drops what wrapping may drop or add: spaces, and style sequences.
func squeeze(s string) string {
	s = strings.ReplaceAll(ansi.Strip(s), "\t", "")
	return strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "\n", "")
}

// checkWrapped checks what holds for any text wrapped either way: no line is
// wider than the width, and from 6 cells up, where every character of the
// samples fits on a line, no text is lost.
func checkWrapped(t *testing.T, th *theme.Theme, name, s string, w int, lines []string) {
	t.Helper()
	if len(lines) == 0 {
		t.Errorf("%s(%q, %d) returned no lines", name, s, w)
	}
	for _, line := range lines {
		if got := th.Width(line); got > max(w, 1) {
			t.Errorf("%s(%q, %d): line %q is %d cells wide", name, s, w, line, got)
		}
		if strings.ContainsAny(line, "\n\t") {
			t.Errorf("%s(%q, %d): line %q holds a newline or a tab", name, s, w, line)
		}
	}
	if w >= 6 && squeeze(strings.Join(lines, "")) != squeeze(s) {
		t.Errorf("%s(%q, %d) lost text: %q", name, s, w, lines)
	}
}

func TestWrappedLinesFitAndLoseNothing(t *testing.T) {
	t.Parallel()
	all := append(samples(), strings.Join(samples(), "\n"), "one\n\n  two\tthree\n")
	for _, method := range widthMethods() {
		th := withWidth(method)
		for _, s := range all {
			for w := -1; w <= 40; w++ {
				checkWrapped(t, th, "Wrap", s, w, th.Wrap(s, w))
				hard := th.Hardwrap(s, w)
				checkWrapped(t, th, "Hardwrap", s, w, hard)
				// A hard wrap keeps every space too.
				if w >= 6 {
					want := strings.ReplaceAll(ansi.Strip(strings.ReplaceAll(s, "\t", "    ")), "\n", "")
					if got := ansi.Strip(strings.Join(hard, "")); got != want {
						t.Errorf("%v: Hardwrap(%q, %d) changed the text: %q", method, s, w, got)
					}
				}
			}
		}
	}
}

func TestHardwrapFillsEveryRow(t *testing.T) {
	t.Parallel()
	th := withWidth(termcaps.WidthWc)
	lines := th.Hardwrap(strings.Repeat("x := compute(a, b) ", 20), 17)
	for i, line := range lines[:len(lines)-1] {
		if th.Width(line) != 17 {
			t.Errorf("row %d, %q, is %d cells wide, want 17: code is never broken early", i, line, th.Width(line))
		}
	}
}

func TestWrappedLinesStandAlone(t *testing.T) {
	t.Parallel()
	// Each line, drawn by itself, shows its text as the whole would have.
	th := withWidth(termcaps.WidthWc)
	accent := th.Style("accent").Render("this accent text runs over several lines of the screen")
	src := "plain " + accent + " plain again and on for a while"
	whole := paint(t, src)
	for _, wrap := range []func(string, int) []string{th.Wrap, th.Hardwrap} {
		got := make([]cell, 0, len(whole))
		for _, line := range wrap(src, 12) {
			got = append(got, paint(t, line)...)
		}
		i := 0
		for _, c := range got {
			for i < len(whole) && whole[i].text == " " && c.text != " " {
				i++ // a space that went at a break
			}
			if i >= len(whole) {
				t.Fatalf("more text came out than went in: %+v", c)
			}
			if c != whole[i] {
				t.Fatalf("character %d drawn %+v, want %+v", i, c, whole[i])
			}
			i++
		}
	}
}

func TestSpread(t *testing.T) {
	t.Parallel()
	th := withWidth(termcaps.WidthWc)
	bold := lipgloss.NewStyle().Bold(true).Render
	cases := []struct {
		name        string
		left, right string
		w           int
		want        string
	}{
		{"both fit", "ada", "12:04", 12, "ada    12:04"},
		{"two cells apart at least", "ada", "12:04", 10, "ada  12:04"},
		{"right gives way", "ada", "12:04", 9, "ada      "},
		{"then left is truncated", "deepseek-flash", "12:04", 8, "deepsee…"},
		{"right alone", "", "12:04", 8, "   12:04"},
		{"right alone, exactly", "", "12:04", 5, "12:04"},
		{"right alone and too wide", "", "12:04", 4, "    "},
		{"left alone", "ada", "", 6, "ada   "},
		{"nothing", "", "", 3, "   "},
		{"no width", "ada", "12:04", 0, ""},
		{"styles take no room", bold("ada"), bold("12:04"), 10, bold("ada") + "  " + bold("12:04")},
		{"wide text", "这是", "中文", 10, "这是  中文"},
		{"wide text, right gives way", "这是", "中文", 9, "这是     "},
		{"a style left on ends with its side", "\x1b[7mada", "\x1b[1m12:04", 12, "\x1b[7mada\x1b[m    \x1b[1m12:04\x1b[m"},
		{"a style left on ends with a side that is cut", "\x1b[7mdeepseek-flash", "", 8, "\x1b[7mdeepsee…\x1b[m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := th.Spread(tc.left, tc.right, tc.w)
			if got != tc.want {
				t.Errorf("Spread(%q, %q, %d) = %q, want %q", tc.left, tc.right, tc.w, got, tc.want)
			}
			if th.Width(got) != tc.w {
				t.Errorf("Spread(%q, %q, %d) is %d cells wide", tc.left, tc.right, tc.w, th.Width(got))
			}
		})
	}
}
