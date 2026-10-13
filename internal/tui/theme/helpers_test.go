package theme_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// Text that the two ways of counting cells disagree about, or that is wide.
const (
	zwjFamily = "👨\u200d👩\u200d👧 family at the table"
	skinTone  = "👍🏽 thumbs up, twice 👍🏽"
	cjkText   = "这是一个很长的中文句子用来测试"
)

func colors() []termcaps.Color {
	return []termcaps.Color{termcaps.ColorTrue, termcaps.Color256, termcaps.Color16, termcaps.ColorNone}
}

func backgrounds() []termcaps.Background {
	return []termcaps.Background{termcaps.BgDark, termcaps.BgLight, termcaps.BgUnknown}
}

func widthMethods() []termcaps.Width {
	return []termcaps.Width{termcaps.WidthWc, termcaps.WidthGrapheme}
}

// textTones are the tones that colour text; rule and border colour lines.
func textTones() []string {
	return []string{"accent", "tool", "dim", "ok", "warn", "bad", "mod"}
}

func lineTones() []string { return []string{"rule", "border"} }

// newTheme makes a theme with Unicode glyphs and wcwidth counting.
func newTheme(c termcaps.Color, bg termcaps.Background) *theme.Theme {
	return theme.New(termcaps.Caps{Color: c, Background: bg, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc}, theme.Options{})
}

// asciiTheme makes a theme with ASCII glyphs.
func asciiTheme(c termcaps.Color, bg termcaps.Background) *theme.Theme {
	return theme.New(termcaps.Caps{Color: c, Background: bg, Glyphs: termcaps.GlyphsASCII, Width: termcaps.WidthWc}, theme.Options{})
}

// withWidth makes a truecolor theme that counts cells one way or the other.
func withWidth(w termcaps.Width) *theme.Theme {
	return theme.New(termcaps.Caps{
		Color: termcaps.ColorTrue, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsUnicode, Width: w,
	}, theme.Options{})
}

// capsName names a combination for a subtest or a golden line.
func capsName(c termcaps.Color, bg termcaps.Background) string {
	return c.String() + "/" + bg.String()
}

// cell is one character as a terminal would draw it.
type cell struct {
	text string
	// fg and bg are "" for the terminal's own colour, "ansi:N" for one of the
	// 16, "256:N" for a palette index, or "#rrggbb".
	fg, bg                                  string
	bold, faint, italic, underline, reverse bool
}

// paint interprets the style sequences in s, as a terminal would, and returns
// what each character is drawn with. It knows the sequences this package and
// its libraries write, and fails the test on any other escape sequence.
func paint(t *testing.T, s string) []cell {
	t.Helper()
	var out []cell
	var cur cell
	for s != "" {
		if s[0] != 0x1b {
			r, n := utf8.DecodeRuneInString(s)
			c := cur
			c.text = string(r)
			out = append(out, c)
			s = s[n:]
			continue
		}
		end := strings.IndexByte(s, 'm')
		if !strings.HasPrefix(s, "\x1b[") || end < 0 {
			t.Fatalf("an escape sequence that is not a style: %q", s)
		}
		cur = applySGR(t, cur, s[2:end])
		s = s[end+1:]
	}
	return out
}

// applySGR applies the parameters of one style sequence.
func applySGR(t *testing.T, c cell, params string) cell {
	t.Helper()
	ps := strings.Split(params, ";")
	for i := 0; i < len(ps); i++ {
		n := 0
		if ps[i] != "" {
			var err error
			if n, err = strconv.Atoi(ps[i]); err != nil {
				t.Fatalf("style parameter %q in %q", ps[i], params)
			}
		}
		switch {
		case n == 0:
			c = cell{}
		case n == 1:
			c.bold = true
		case n == 2:
			c.faint = true
		case n == 3:
			c.italic = true
		case n == 4:
			c.underline = true
		case n == 7:
			c.reverse = true
		case n == 22:
			c.bold, c.faint = false, false
		case n == 23:
			c.italic = false
		case n == 24:
			c.underline = false
		case n == 27:
			c.reverse = false
		case n >= 30 && n <= 37:
			c.fg = "ansi:" + strconv.Itoa(n-30)
		case n >= 90 && n <= 97:
			c.fg = "ansi:" + strconv.Itoa(n-90+8)
		case n == 39:
			c.fg = ""
		case n >= 40 && n <= 47:
			c.bg = "ansi:" + strconv.Itoa(n-40)
		case n >= 100 && n <= 107:
			c.bg = "ansi:" + strconv.Itoa(n-100+8)
		case n == 49:
			c.bg = ""
		case n == 38 || n == 48:
			var colour string
			switch {
			case i+2 < len(ps) && ps[i+1] == "5":
				colour = "256:" + ps[i+2]
				i += 2
			case i+4 < len(ps) && ps[i+1] == "2":
				r, _ := strconv.Atoi(ps[i+2])
				g, _ := strconv.Atoi(ps[i+3])
				b, _ := strconv.Atoi(ps[i+4])
				colour = fmt.Sprintf("#%02x%02x%02x", r, g, b)
				i += 4
			default:
				t.Fatalf("colour parameters in %q", params)
			}
			if n == 38 {
				c.fg = colour
			} else {
				c.bg = colour
			}
		default:
			t.Fatalf("style parameter %d in %q is not one this client writes", n, params)
		}
	}
	return c
}

// text returns what the cells say.
func text(cells []cell) string {
	var b strings.Builder
	for _, c := range cells {
		b.WriteString(c.text)
	}
	return b.String()
}

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
