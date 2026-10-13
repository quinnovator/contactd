package draw_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/draw"
	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// The colours of the truecolor dark theme that the tests name.
const (
	darkCodeFill = "#181d22"
	darkDim      = "#7f8a91"
	darkTool     = "#6ea8e6"
	darkAccent   = "#5ec4c8"
	darkBad      = "#e06c75"
	darkWarn     = "#d9b25c"
)

// look is one terminal a block is drawn for.
type look struct {
	name string
	caps termcaps.Caps
}

// trueDark, light16, unknown256 and noneASCII are the four terminals of the
// work package: the best one, a 16-colour one on a light background, a
// 256-colour one that never said what its background is, and a serial
// console.
func trueDark() look {
	return look{"truecolor-dark", termcaps.Caps{
		Color: termcaps.ColorTrue, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
	}}
}

func light16() look {
	return look{"16-light", termcaps.Caps{
		Color: termcaps.Color16, Background: termcaps.BgLight, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
	}}
}

func unknown256() look {
	return look{"256-unknown", termcaps.Caps{
		Color: termcaps.Color256, Background: termcaps.BgUnknown, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
	}}
}

func noneASCII() look {
	return look{"none-ascii", termcaps.Caps{
		Color: termcaps.ColorNone, Background: termcaps.BgUnknown, Glyphs: termcaps.GlyphsASCII, Width: termcaps.WidthWc,
	}}
}

func looks() []look { return []look{trueDark(), light16(), unknown256(), noneASCII()} }

func widths() []int { return []int{100, 60, 40, 30} }

func (l look) theme() *theme.Theme { return theme.New(l.caps, theme.Options{}) }

// renderer makes a Renderer for a terminal and a width, in UTC.
func (l look) renderer(width int) *draw.Renderer {
	return draw.New(draw.Options{Theme: l.theme(), Width: width, Location: time.UTC})
}

// with makes a Renderer from options, filling in the theme and the time zone.
func (l look) with(o draw.Options) *draw.Renderer {
	o.Theme, o.Location = l.theme(), time.UTC
	return draw.New(o)
}

// cell is one character as a terminal would draw it.
type cell struct {
	text string
	// fg and bg are "" for the terminal's own colour, "ansi:N" for one of the
	// 16, "256:N" for a palette index, or "#rrggbb".
	fg, bg                                 string
	bold, faint, italic, underline, strike bool
	// link is the address of the hyperlink the character is part of.
	link string
}

// paint interprets a line as a terminal would and returns what each
// character is drawn with, and the state the line leaves the terminal in: the
// zero cell when it switched off everything it switched on. It knows two
// kinds of escape sequence, a style and a hyperlink, and fails the test on
// any other.
func paint(t *testing.T, s string) (cells []cell, end cell) {
	t.Helper()
	var cur cell
	for s != "" {
		switch {
		case s[0] != 0x1b:
			r, n := utf8.DecodeRuneInString(s)
			c := cur
			c.text = string(r)
			cells = append(cells, c)
			s = s[n:]
		case strings.HasPrefix(s, "\x1b]8;"):
			stop := strings.IndexByte(s, 0x07)
			if stop < 0 {
				t.Fatalf("a hyperlink sequence that does not end: %q", s)
			}
			_, cur.link, _ = strings.Cut(s[4:stop], ";")
			s = s[stop+1:]
		case strings.HasPrefix(s, "\x1b["):
			stop := strings.IndexFunc(s[2:], func(r rune) bool { return r < '0' || r > ';' })
			if stop < 0 || s[2+stop] != 'm' {
				t.Fatalf("an escape sequence that is not a style: %q", s)
			}
			cur = applySGR(t, cur, s[2:2+stop])
			s = s[3+stop:]
		default:
			t.Fatalf("an escape sequence the client does not write: %q", s)
		}
	}
	return cells, cur
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
			c = cell{link: c.link}
		case n == 1:
			c.bold = true
		case n == 2:
			c.faint = true
		case n == 3:
			c.italic = true
		case n == 4:
			c.underline = true
		case n == 9:
			c.strike = true
		case n == 22:
			c.bold, c.faint = false, false
		case n == 23:
			c.italic = false
		case n == 24:
			c.underline = false
		case n == 29:
			c.strike = false
		case n >= 30 && n <= 37:
			c.fg = "ansi:" + strconv.Itoa(n-30)
		case n >= 90 && n <= 97:
			c.fg = "ansi:" + strconv.Itoa(n-90+8)
		case n == 39:
			c.fg = ""
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

// said returns what the cells say.
func said(cells []cell) string {
	var b strings.Builder
	for _, c := range cells {
		b.WriteString(c.text)
	}
	return b.String()
}

// find returns the cells that draw the first occurrence of want in a line.
func find(t *testing.T, line, want string) []cell {
	t.Helper()
	cells, _ := paint(t, line)
	all := said(cells)
	i := strings.Index(all, want)
	if i < 0 {
		t.Fatalf("%q is not in %q", want, all)
	}
	start := utf8.RuneCountInString(all[:i])
	return cells[start : start+utf8.RuneCountInString(want)]
}

// stripped returns lines without their escape sequences.
func stripped(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

// quoted writes lines so that escape sequences and trailing spaces can be
// read in a golden file.
func quoted(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(strconv.Quote(l))
		b.WriteByte('\n')
	}
	return b.String()
}

// fits fails the test for every line that would not sit in a row of w cells
// by itself: one that is wider, by either way of counting cells, one that
// holds a line break, and one that leaves a style or a link switched on.
func fits(t *testing.T, what string, lines []string, w int) {
	t.Helper()
	for i, line := range lines {
		if strings.ContainsAny(line, "\n\r") {
			t.Errorf("%s: line %d holds a line break: %q", what, i, line)
		}
		if got := ansi.StringWidthWc(line); got > w {
			t.Errorf("%s: line %d is %d cells by wcwidth, want at most %d: %q", what, i, got, w, line)
		}
		if got := ansi.StringWidth(line); got > w {
			t.Errorf("%s: line %d is %d cells by grapheme, want at most %d: %q", what, i, got, w, line)
		}
		if _, end := paint(t, line); end != (cell{}) {
			t.Errorf("%s: line %d leaves a style or a link on (%+v): %q", what, i, end, line)
		}
	}
}

// equal fails the test when two lists of lines differ, and shows both.
func equal(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) == len(want) {
		same := true
		for i := range got {
			same = same && got[i] == want[i]
		}
		if same {
			return
		}
	}
	t.Errorf("%s:\ngot\n%swant\n%s", what, quoted(got), quoted(want))
}

// images is an ImageSource that shows the same thing for every image and
// keeps what it was asked.
type images struct {
	shown draw.Shown
	refs  []draw.ImageRef
	boxes [][2]int
}

func (f *images) Image(ref draw.ImageRef, maxCols, maxRows int) draw.Shown {
	f.refs = append(f.refs, ref)
	f.boxes = append(f.boxes, [2]int{maxCols, maxRows})
	return f.shown
}

// rendered is a RenderSource that has lines for some kinds and keeps what it
// was asked.
type rendered struct {
	byKind map[string][]wire.Line
	refs   []draw.RenderRef
	widths []int
}

func (f *rendered) Rendered(ref draw.RenderRef, width int) ([]wire.Line, bool) {
	f.refs = append(f.refs, ref)
	f.widths = append(f.widths, width)
	lines, ok := f.byKind[ref.Kind]
	return lines, ok
}

// item makes an item of a kind with a sequence number and a text.
func item(kind transcript.Kind, seq uint64, text string) *transcript.Item {
	return &transcript.Item{Kind: kind, Seq: seq, Rev: seq, TS: 1760270400000 + int64(seq), Text: text}
}

// agent makes a reply.
func agent(seq uint64, text string) *transcript.Item { return item(transcript.Agent, seq, text) }
