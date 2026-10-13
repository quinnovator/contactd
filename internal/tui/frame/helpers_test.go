package frame_test

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// look is one terminal a screen is drawn for.
type look struct {
	name string
	caps termcaps.Caps
}

// looks are the four terminals of the Compatibility mock-up.
func looks() []look {
	return []look{
		{"truecolor-dark", termcaps.Caps{
			Color: termcaps.ColorTrue, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
		}},
		{"16-light", termcaps.Caps{
			Color: termcaps.Color16, Background: termcaps.BgLight, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
		}},
		{"256-unknown", termcaps.Caps{
			Color: termcaps.Color256, Background: termcaps.BgUnknown, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
		}},
		{"none-ascii", termcaps.Caps{
			Color: termcaps.ColorNone, Background: termcaps.BgUnknown, Glyphs: termcaps.GlyphsASCII, Width: termcaps.WidthWc,
		}},
	}
}

// everyLook is the four terminals, each counting cells both ways.
func everyLook() []look {
	out := make([]look, 0, 2*len(looks()))
	for _, l := range looks() {
		out = append(out, l)
		l.name += "-grapheme"
		l.caps.Width = termcaps.WidthGrapheme
		out = append(out, l)
	}
	return out
}

// themeOf makes the theme of a look.
func themeOf(l look) *theme.Theme { return theme.New(l.caps, theme.Options{}) }

// dark is the terminal of the Frame mock-up: 24-bit colour on a dark
// background, Unicode glyphs.
func dark() *theme.Theme { return themeOf(looks()[0]) }

// plainASCII is the serial console of the Compatibility mock-up: no colour,
// ASCII glyphs.
func plainASCII() *theme.Theme { return themeOf(looks()[3]) }

// quoted writes lines so that escape sequences and trailing spaces can be
// read in a golden file.
func quoted(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(strconv.Quote(l))
		b.WriteByte('\n')
	}
	return b.String()
}

// shown is a row as it reads on the screen: without its styles and without
// the spaces that fill it up.
func shown(row string) string { return strings.TrimRight(ansi.Strip(row), " ") }

// wantRow fails the test unless row is one line of exactly w cells.
func wantRow(t *testing.T, th *theme.Theme, row string, w int) {
	t.Helper()
	if strings.ContainsAny(row, "\r\n") {
		t.Errorf("the row holds a line break: %q", row)
	}
	if got := th.Width(row); got != max(w, 0) {
		t.Errorf("the row is %d cells wide, want %d: %q", got, max(w, 0), row)
	}
}

// wantASCII fails the test when s holds a byte a serial console cannot show.
func wantASCII(t *testing.T, s string) {
	t.Helper()
	for i := range len(s) {
		if s[i] >= 0x80 {
			t.Errorf("byte %#x at %d of %q", s[i], i, s)
			return
		}
	}
}

// hostile is text that would make a terminal do something if its escape
// sequences were passed on: switch a style on, clear the screen, set the
// title, write the clipboard, draw a picture, or swallow what follows.
func hostile() []string {
	return []string{
		"\x1b[7m", "\x1b[2J\x1b[H", "\x1b[?1049h", "\x1b]0;title\a", "\x1b]52;c;aGk=\x1b\\",
		"\x1bPq#0\x1b\\", "\x1b_Gf=24;AAAA\x1b\\", "\x1b]0;never ended", "\x1bP", "\x1b", "\x1b[", "\x1b[38;5",
		"\u009b31m", "\u009d0;title\u009c", "\x00\a\b\v\f\x7f", "a\x1b\x1b[31mb", "\r\n\t",
	}
}

// wantInert fails the test unless the only escape sequences in a row are
// styles, the last of them switches every style off, and nothing else in the
// row is a control character. The rows tested are drawn from text that holds
// no style sequence once its escape characters are gone, so a style in the row
// is one this package put there.
func wantInert(t *testing.T, row string) {
	t.Helper()
	styles := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	if seqs := styles.FindAllString(row, -1); len(seqs) > 0 && seqs[len(seqs)-1] != "\x1b[m" {
		t.Errorf("a style is left on at the end of the row: %q", row)
	}
	for _, r := range styles.ReplaceAllString(row, "") {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			t.Errorf("control character %U in the row: %q", r, row)
			return
		}
	}
}

// wideGlyphs is a theme whose glyphs were replaced by wider ones: a separator
// and an ellipsis of three cells, and a dash of two.
func wideGlyphs(t *testing.T, l look) *theme.Theme {
	t.Helper()
	o := theme.Options{Glyph: map[string]string{"mid": "<=>", "ellipsis": "~~~", "dash": "日"}}
	if bad := theme.Check(o); bad != nil {
		t.Fatalf("the overrides are refused: %q", bad)
	}
	return theme.New(l.caps, o)
}
