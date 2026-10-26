package theme_test

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// glyphFields returns every glyph of a set by its field name, so that a test
// over the table cannot miss a glyph that is added later.
func glyphFields(g theme.Glyphs) map[string]string {
	out := map[string]string{}
	v := reflect.ValueOf(g)
	for i := range v.NumField() {
		out[v.Type().Field(i).Name] = v.Field(i).String()
	}
	return out
}

// glyphCells is the width layout assumes for each glyph: in the Unicode set,
// then in the ASCII set.
func glyphCells() map[string][2]int {
	return map[string][2]int{
		"Wire":   {1, 2},
		"Prompt": {1, 1}, "Task": {1, 2}, "Tool": {1, 1}, "Elbow": {1, 2},
		"Stopped": {1, 2}, "Notice": {1, 1}, "Fail": {1, 1}, "Pointer": {1, 1}, "Dot": {1, 1},
		"Sep": {1, 1}, "HRule": {1, 1}, "Ellipsis": {1, 3}, "Mid": {1, 1}, "Bullet": {1, 1},
		"Newer": {1, 1}, "UpDown": {2, 7}, "LeftRight": {2, 10},
		"Dash": {1, 1}, "Times": {1, 1}, "Arrow": {1, 2},
		"CheckOn": {3, 3}, "CheckOff": {3, 3}, "HalfBlock": {1, 0},
		"BoxTL": {1, 1}, "BoxTR": {1, 1}, "BoxBL": {1, 1}, "BoxBR": {1, 1}, "BoxH": {1, 1}, "BoxV": {1, 1},
	}
}

func TestGlyphTable(t *testing.T) {
	t.Parallel()
	want := map[string][2]string{
		"Wire":   {"⇄", "<>"},
		"Prompt": {"❯", ">"}, "Task": {"⇢", "->"}, "Tool": {"⏺", "*"}, "Elbow": {"⎿", "|_"},
		"Stopped": {"⏹", "--"}, "Notice": {"◆", "+"}, "Fail": {"✗", "x"}, "Pointer": {"▸", ">"},
		"Dot": {"●", "*"}, "Sep": {"│", "|"}, "HRule": {"─", "-"}, "Ellipsis": {"…", "..."},
		"Mid": {"·", "|"}, "Bullet": {"•", "-"}, "Newer": {"↓", "v"},
		"UpDown": {"↑↓", "up/down"}, "LeftRight": {"←→", "left/right"},
		"Dash": {"–", "-"}, "Times": {"×", "x"}, "Arrow": {"→", "->"},
		"CheckOn": {"[x]", "[x]"}, "CheckOff": {"[ ]", "[ ]"}, "HalfBlock": {"▀", ""},
		"BoxTL": {"╭", "+"}, "BoxTR": {"╮", "+"}, "BoxBL": {"╰", "+"}, "BoxBR": {"╯", "+"},
		"BoxH": {"─", "-"}, "BoxV": {"│", "|"},
	}
	uni := glyphFields(theme.GlyphSet(termcaps.GlyphsUnicode))
	asc := glyphFields(theme.GlyphSet(termcaps.GlyphsASCII))
	if len(uni) != len(want) {
		t.Errorf("the Unicode set has %d glyphs and the table %d", len(uni), len(want))
	}
	for name, w := range want {
		if uni[name] != w[0] {
			t.Errorf("Unicode %s = %q, want %q", name, uni[name], w[0])
		}
		if asc[name] != w[1] {
			t.Errorf("ASCII %s = %q, want %q", name, asc[name], w[1])
		}
	}
	// A value that is not one of the two sets is drawn the safe way.
	if got := theme.GlyphSet(0); got != theme.GlyphSet(termcaps.GlyphsASCII) {
		t.Errorf("GlyphSet(0) = %+v, want the ASCII set", got)
	}
}

func TestGlyphSetASCIIIsASCII(t *testing.T) {
	t.Parallel()
	for name, g := range glyphFields(theme.GlyphSet(termcaps.GlyphsASCII)) {
		for i := range len(g) {
			if g[i] < 0x20 || g[i] > 0x7e {
				t.Errorf("ASCII %s = %q: byte %#x is not printable ASCII", name, g, g[i])
			}
		}
	}
}

func TestGlyphsHoldNothingUndrawn(t *testing.T) {
	t.Parallel()
	for _, set := range []termcaps.Glyphs{termcaps.GlyphsUnicode, termcaps.GlyphsASCII} {
		for name, g := range glyphFields(theme.GlyphSet(set)) {
			for _, r := range g {
				if r == 0xfe0f {
					t.Errorf("%v %s = %q holds the variation selector U+FE0F", set, name, g)
				}
				if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
					t.Errorf("%v %s = %q holds %U, which is not drawn", set, name, g, r)
				}
			}
		}
	}
}

func TestGlyphWidths(t *testing.T) {
	t.Parallel()
	cells := glyphCells()
	for _, method := range widthMethods() {
		for col, set := range []termcaps.Glyphs{termcaps.GlyphsUnicode, termcaps.GlyphsASCII} {
			th := theme.New(termcaps.Caps{Color: termcaps.ColorNone, Glyphs: set, Width: method}, theme.Options{})
			for name, g := range glyphFields(th.G) {
				want, ok := cells[name]
				if !ok {
					t.Errorf("no width is recorded for %s", name)
					continue
				}
				if got := th.Width(g); got != want[col] {
					t.Errorf("%v %v: %s = %q is %d cells, layout assumes %d", method, set, name, g, got, want[col])
				}
			}
		}
	}
}

func TestOverridesAccepted(t *testing.T) {
	t.Parallel()
	o := theme.Options{Glyph: map[string]string{
		"tool":     "●",
		"elbow":    "└",
		"Box_TL":   "┌",
		"check-on": "[✓]",
		"ELLIPSIS": "..",
		"notice":   "中", // two cells
		"mid":      " ",
	}}
	before := maps.Clone(o.Glyph)
	if got := theme.Check(o); got != nil {
		t.Errorf("Check = %q, want every override applied", got)
	}
	th := theme.New(termcaps.Caps{Color: termcaps.ColorNone, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc}, o)
	want := theme.GlyphSet(termcaps.GlyphsUnicode)
	want.Tool, want.Elbow, want.BoxTL, want.CheckOn, want.Ellipsis, want.Notice, want.Mid = "●", "└", "┌", "[✓]", "..", "中", " "
	if th.G != want {
		t.Errorf("glyphs after overrides:\n got %+v\nwant %+v", th.G, want)
	}
	if !maps.Equal(o.Glyph, before) {
		t.Errorf("New changed the options: %v", o.Glyph)
	}

	// The overrides are for the Unicode set. An ASCII terminal keeps ASCII.
	ascii := theme.New(termcaps.Caps{Color: termcaps.ColorNone, Glyphs: termcaps.GlyphsASCII, Width: termcaps.WidthWc}, o)
	if ascii.G != theme.GlyphSet(termcaps.GlyphsASCII) {
		t.Errorf("overrides changed the ASCII set: %+v", ascii.G)
	}
}

func TestOverridesRefused(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, key, value, why string
	}{
		{"unknown name", "spinner", "*", "no glyph of that name"},
		{"the half block is not replaceable", "halfblock", "#", "no glyph of that name"},
		{"empty", "tool", "", "is empty"},
		{"escape sequence", "tool", "\x1b[31m*", "not drawn"},
		{"newline", "tool", "a\nb", "not drawn"},
		{"tab", "tool", "a\tb", "not drawn"},
		{"carriage return", "tool", "a\r", "not drawn"},
		{"C1 control", "tool", "a\u009b", "not drawn"},
		{"bidi override", "tool", "\u202e*", "not drawn"},
		{"joiner", "tool", "a\u200db", "not drawn"},
		{"variation selector", "tool", "⏺\ufe0f", "not drawn"},
		{"zero-width space", "tool", "a\u200b", "not drawn"},
		{"graphics placeholder", "tool", "\U0010eeee", "not drawn"},
		{"not UTF-8", "tool", "\xff", "not UTF-8"},
		{"four cells", "tool", "abcd", "4 cells wide"},
		{"two wide characters", "tool", "中文", "4 cells wide"},
		{"no cells", "tool", "\u0301", "0 cells wide"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := theme.Options{Glyph: map[string]string{tc.key: tc.value}}
			th := theme.New(termcaps.Caps{Color: termcaps.ColorNone, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc}, o)
			if th.G != theme.GlyphSet(termcaps.GlyphsUnicode) {
				t.Errorf("a refused override was applied: %+v", th.G)
			}
			got := theme.Check(o)
			if len(got) != 1 || !strings.Contains(got[0], tc.why) || !strings.Contains(got[0], tc.key) {
				t.Fatalf("Check = %q, want one line naming %q and saying %q", got, tc.key, tc.why)
			}
			// The line is shown on a terminal, so it must not carry what was refused.
			for i := range len(got[0]) {
				if b := got[0][i]; b < 0x20 || b > 0x7e {
					t.Errorf("Check line %q holds byte %#x", got[0], b)
				}
			}
		})
	}
}

func TestCheckOrderAndMix(t *testing.T) {
	t.Parallel()
	o := theme.Options{Glyph: map[string]string{
		"zebra": "z", "tool": "●", "arrow": "", "dot": "abcd", "elbow": "└",
	}}
	got := theme.Check(o)
	if len(got) != 3 {
		t.Fatalf("Check = %q, want three lines", got)
	}
	for i, name := range []string{"arrow", "dot", "zebra"} {
		if !strings.Contains(got[i], `"`+name+`"`) {
			t.Errorf("line %d = %q, want the one about %q: lines are in name order", i, got[i], name)
		}
	}
	if !slices.Equal(got, theme.Check(o)) {
		t.Error("Check gave two answers for one input")
	}
	// The good ones still apply beside the bad.
	th := theme.New(termcaps.Caps{Glyphs: termcaps.GlyphsUnicode}, o)
	if th.G.Tool != "●" || th.G.Elbow != "└" || th.G.Arrow != "→" || th.G.Dot != "●" {
		t.Errorf("glyphs = %+v", th.G)
	}
	if theme.Check(theme.Options{}) != nil {
		t.Error("Check of no overrides is not nil")
	}
}
