package canvas

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// cell builds the pixels of one cell from a picture of it, four rows of two,
// where '.' is unpainted and letters are opaque colours.
func cell(picture string) (px [across * down]Sample) {
	ink := map[rune]Sample{
		'.': {},
		'R': Sample{}.Over(Hex(0xff0000), 1),
		'B': Sample{}.Over(Hex(0x0000ff), 1),
	}
	for i, r := range strings.ReplaceAll(picture, " ", "") {
		px[i] = ink[r]
	}
	return px
}

func TestEncode(t *testing.T) {
	t.Parallel()
	const red, blue = 0xff0000, 0x0000ff
	for _, tc := range []struct {
		name    string
		picture string
		glyph   rune
		fg, bg  int32
	}{
		{"nothing painted", ".. .. .. ..", ' ', unset, unset},
		{"one flat colour", "RR RR RR RR", ' ', unset, red},
		{"left and right", "RB RB RB RB", '▐', blue, red},
		{"top and bottom", "BB BB RR RR", '▄', red, blue},
		{"one corner on bare terminal", ".. .. .R .R", '▗', red, unset},
		{"three corners on bare terminal", "RR RR .R .R", '▜', red, unset},
		{"a single octant", ".. R. .. ..", '\U0001CD00', red, unset},
		{"a diagonal only octants can draw", "R. .R R. .R", '\U0001CD89', red, unset},
		{"all but one octant", "BR RR RR RR", '\U0001CDE5', red, blue},
	} {
		glyph, fg, bg := encode(cell(tc.picture), Hex(0x101010))
		if glyph != tc.glyph || fg != tc.fg || bg != tc.bg {
			t.Errorf("%s: got %q (%U) fg=%06x bg=%06x, want %q fg=%06x bg=%06x",
				tc.name, glyph, glyph, fg, bg, tc.glyph, tc.fg, tc.bg)
		}
	}
}

func TestEveryPatternHasItsOwnGlyph(t *testing.T) {
	t.Parallel()
	seen := map[rune]int{}
	for mask, r := range glyphs {
		if other, dup := seen[r]; dup {
			t.Errorf("patterns %08b and %08b share the glyph %U", other, mask, r)
		}
		seen[r] = mask
	}
}

// disc paints an opaque white disc that leaves the canvas corners bare.
func disc(c *Canvas) {
	c.Shade(func(x, y float64) Sample {
		return Sample{}.Over(White, 0.5-(x*x+y*y-0.5)/c.Pixel())
	})
}

func TestRender(t *testing.T) {
	t.Parallel()
	for _, grid := range []Grid{Quadrants, Octants} {
		c := New(8, 3, grid)
		if out := c.Render(Color{}); out != "        \n        \n        " {
			t.Errorf("an unpainted canvas should render as bare spaces, got %q", out)
		}

		disc(c)
		lines := strings.Split(c.Render(Color{}), "\n")
		if len(lines) != 3 {
			t.Fatalf("rendered %d lines, want 3", len(lines))
		}
		if !strings.HasPrefix(lines[0], " ") || !strings.Contains(lines[1], "\x1b[48;2;255;255;255m") {
			t.Errorf("expected a bare corner and a white centre, got %q", lines)
		}
		for _, ln := range lines {
			if cells := utf8.RuneCountInString(sgr.ReplaceAllString(ln, "")); cells != 8 {
				t.Errorf("line %q is %d cells wide, want 8", ln, cells)
			}
		}
	}
}

func TestQuadrantsUseOnlyQuadrantGlyphs(t *testing.T) {
	t.Parallel()
	c := New(40, 20, Quadrants)
	disc(c)
	for _, r := range sgr.ReplaceAllString(c.Render(Color{}), "") {
		if !strings.ContainsRune(" ▘▝▀▖▌▞▛▗▚▐▜▄▙▟█\n", r) {
			t.Fatalf("quadrant canvas drew %q (%U)", r, r)
		}
	}
}

func TestOverComposites(t *testing.T) {
	t.Parallel()
	s := Sample{}.Over(White, 0.5)
	if got := s.on(Color{}); got != (Color{0.5, 0.5, 0.5}) {
		t.Errorf("half-covered white on black = %v", got)
	}
	if got := s.Over(Hex(0xff0000), 1).on(Color{}); got != (Color{1, 0, 0}) {
		t.Errorf("an opaque layer should hide what is beneath it, got %v", got)
	}
}
