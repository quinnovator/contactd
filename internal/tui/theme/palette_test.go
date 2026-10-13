package theme_test

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// The backgrounds a colour must read on: the mock-ups' own, pure black and
// white, and three themes people use.
func darkGrounds() []string  { return []string{"#111417", "#000000", "#1e1e2e", "#002b36"} }
func lightGrounds() []string { return []string{"#f7f5ee", "#ffffff", "#fdf6e3"} }

func grounds(bg termcaps.Background) []string {
	switch bg {
	case termcaps.BgDark:
		return darkGrounds()
	case termcaps.BgLight:
		return lightGrounds()
	case termcaps.BgUnknown:
		return append(darkGrounds(), lightGrounds()...)
	}
	return nil
}

// rgb turns a painted colour ("#rrggbb" or "256:N") into its three parts.
// The 256-colour palette above the 16 ANSI colours is the same in every
// terminal: a 6x6x6 cube, then 24 greys.
func rgb(t *testing.T, colour string) [3]float64 {
	t.Helper()
	if rest, ok := strings.CutPrefix(colour, "256:"); ok {
		n, err := strconv.Atoi(rest)
		if err != nil || n < 16 || n > 255 {
			t.Fatalf("%q is not a fixed palette colour", colour)
		}
		if n >= 232 {
			v := float64(8 + 10*(n-232))
			return [3]float64{v, v, v}
		}
		levels := []float64{0, 95, 135, 175, 215, 255}
		n -= 16
		return [3]float64{levels[n/36], levels[n/6%6], levels[n%6]}
	}
	var r, g, b int
	if _, err := fmt.Sscanf(colour, "#%02x%02x%02x", &r, &g, &b); err != nil {
		t.Fatalf("%q is not a colour with a known value", colour)
	}
	return [3]float64{float64(r), float64(g), float64(b)}
}

// contrast is the WCAG contrast ratio of two colours: 1 for the same colour,
// 21 for black on white.
func contrast(a, b [3]float64) float64 {
	lum := func(c [3]float64) float64 {
		lin := func(v float64) float64 {
			v /= 255
			if v <= 0.04045 {
				return v / 12.92
			}
			return math.Pow((v+0.055)/1.055, 2.4)
		}
		return 0.2126*lin(c[0]) + 0.7152*lin(c[1]) + 0.0722*lin(c[2])
	}
	la, lb := lum(a), lum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func TestContrastHelper(t *testing.T) {
	t.Parallel()
	if got := contrast(rgb(t, "#000000"), rgb(t, "#ffffff")); math.Abs(got-21) > 0.001 {
		t.Errorf("black on white = %.3f, want 21", got)
	}
	if got := contrast(rgb(t, "256:231"), rgb(t, "256:16")); math.Abs(got-21) > 0.001 {
		t.Errorf("palette white on palette black = %.3f, want 21", got)
	}
	if got := contrast(rgb(t, "256:244"), rgb(t, "#808080")); got != 1 {
		t.Errorf("grey 244 against #808080 = %.3f, want 1", got)
	}
}

// drawn returns the one way a tone's text is drawn.
func drawn(t *testing.T, th *theme.Theme, tone string) cell {
	t.Helper()
	cells := paint(t, th.Style(tone).Render("x"))
	if len(cells) != 1 || cells[0].text != "x" {
		t.Fatalf("tone %q drew %+v", tone, cells)
	}
	return cells[0]
}

func TestRolesGolden(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for _, c := range colors() {
		for _, bg := range backgrounds() {
			th := newTheme(c, bg)
			name := capsName(c, bg)
			for _, tone := range append(append([]string{""}, textTones()...), lineTones()...) {
				label := tone
				if label == "" {
					label = "body"
				}
				fmt.Fprintf(&b, "%-18s %-8s %q\n", name, label, th.Style(tone).Render(label))
			}
			fmt.Fprintf(&b, "%-18s %-8s %q\n", name, "title", th.Title().Render("title"))
			fmt.Fprintf(&b, "%-18s %-8s %q\n", name, "pending", th.Pending().Render("pending"))
			fmt.Fprintf(&b, "%-18s %-8s %q\n", name, "selected", th.Selected("row", 5))
			fmt.Fprintf(&b, "%-18s %-8s %q\n", name, "code", th.CodeFill("code", 6))
			fmt.Fprintf(&b, "%-18s %-8s %v\n\n", name, "filled", th.BarFilled)
		}
	}
	tuitest.Golden(t, "roles.golden", []byte(b.String()))
}

func TestContrast(t *testing.T) {
	t.Parallel()
	for _, c := range []termcaps.Color{termcaps.ColorTrue, termcaps.Color256} {
		for _, bg := range backgrounds() {
			t.Run(capsName(c, bg), func(t *testing.T) {
				t.Parallel()
				th := newTheme(c, bg)
				against := grounds(bg)
				if th.BarFilled {
					// Text in a tone is also drawn on the two fills.
					for _, row := range []string{th.Selected("x", 1), th.CodeFill("x", 1)} {
						fill := paint(t, row)[0].bg
						if fill == "" {
							t.Fatalf("no fill in %q", row)
						}
						against = append(against, fill)
					}
				}
				for _, tone := range textTones() {
					got := drawn(t, th, tone)
					if got.fg == "" {
						t.Errorf("%s has no colour", tone)
						continue
					}
					for _, ground := range against {
						if r := contrast(rgb(t, got.fg), rgb(t, hexOf(t, ground))); r < 3 {
							t.Errorf("%s %s on %s is %.2f:1, want at least 3:1", tone, got.fg, ground, r)
						}
					}
				}
				// A rule is a line, not text: it has to be seen, not read. The
				// mock-ups' own rule is 1.5:1 on their own background.
				for _, tone := range lineTones() {
					got := drawn(t, th, tone)
					if got.fg == "" {
						t.Errorf("%s has no colour", tone)
						continue
					}
					for _, ground := range grounds(bg) {
						if r := contrast(rgb(t, got.fg), rgb(t, ground)); r < 1.2 {
							t.Errorf("%s %s on %s is %.2f:1, want at least 1.2:1", tone, got.fg, ground, r)
						}
					}
				}
			})
		}
	}
}

// hexOf passes a "#rrggbb" through and turns a painted palette colour into
// the name rgb takes.
func hexOf(t *testing.T, colour string) string {
	t.Helper()
	if strings.HasPrefix(colour, "ansi:") {
		t.Fatalf("%q is one of the 16 colours, whose value depends on the terminal", colour)
	}
	return colour
}

func TestTruecolorWithUnknownBackgroundUsesThe256Set(t *testing.T) {
	t.Parallel()
	a, b := newTheme(termcaps.ColorTrue, termcaps.BgUnknown), newTheme(termcaps.Color256, termcaps.BgUnknown)
	for _, tone := range append(textTones(), lineTones()...) {
		if got, want := a.Style(tone).Render("x"), b.Style(tone).Render("x"); got != want {
			t.Errorf("%s: truecolor %q, 256 colours %q", tone, got, want)
		}
	}
	if a.BarFilled || b.BarFilled {
		t.Error("a bar is filled on an unknown background")
	}
}

// everything draws each shape once, with styled and plain text in it, so that
// a rule about colour can be checked on all the package writes.
func everything(th *theme.Theme) []string {
	out := make([]string, 0, 32)
	for _, tone := range append(append([]string{"", "no such tone"}, textTones()...), lineTones()...) {
		out = append(out, th.Style(tone).Render("text"))
	}
	styled := th.Style("accent").Render("name") + " plain " + th.Style("dim").Render("dim")
	out = append(out, th.Title().Render("title"), th.Pending().Render("pending"),
		th.Selected(styled, 30), th.CodeFill(styled, 30), th.Rule(30, ""), th.Rule(30, "Mon 12 Oct 2026"))
	out = append(out, th.Box([]string{styled, "second"}, 30, "title", "1 of 2")...)
	left := []theme.Cell{
		{Text: th.G.Dot + " live", Tone: "ok"},
		{Text: "deepseek-flash"},
		{Text: "cache 48%", Tone: "warn"},
		{Text: "call failed", Tone: "bad"},
	}
	right := []theme.Cell{
		{Text: "running python", Tone: "tool"},
		{Text: "cache avg 61%", Tone: "mod"},
		{Text: "esc stop", Tone: "dim"},
		{Text: "12:06"},
	}
	rule, row := th.Bar(left, right, 100)
	return append(out, rule, row)
}

func TestSixteenColourRules(t *testing.T) {
	t.Parallel()
	// Black, white and their bright forms vanish on one background or the
	// other. Yellow, cyan and green, bright or not, vanish on a light one.
	never := []string{"ansi:0", "ansi:7", "ansi:8", "ansi:15"}
	neverOnLight := []string{"ansi:2", "ansi:3", "ansi:6", "ansi:10", "ansi:11", "ansi:14"}
	for _, bg := range backgrounds() {
		t.Run(bg.String(), func(t *testing.T) {
			t.Parallel()
			banned := never
			if bg == termcaps.BgLight {
				banned = append(append([]string{}, never...), neverOnLight...)
			}
			coloured := 0
			for _, th := range []*theme.Theme{newTheme(termcaps.Color16, bg), asciiTheme(termcaps.Color16, bg)} {
				for _, s := range everything(th) {
					for _, c := range paint(t, s) {
						if c.bg != "" {
							t.Errorf("%q has a background, %s, with 16 colours", s, c.bg)
						}
						if c.fg == "" {
							continue
						}
						coloured++
						if !strings.HasPrefix(c.fg, "ansi:") {
							t.Errorf("%q uses %s, which is not one of the 16", s, c.fg)
						}
						for _, no := range banned {
							if c.fg == no {
								t.Errorf("%q uses %s on a %s background", s, c.fg, bg)
							}
						}
					}
				}
			}
			if coloured == 0 {
				t.Error("nothing was coloured: the rule was checked on nothing")
			}
		})
	}
}

func TestSixteenColourTable(t *testing.T) {
	t.Parallel()
	type look struct {
		fg          string
		bold, faint bool
	}
	want := map[termcaps.Background]map[string]look{
		termcaps.BgDark: {
			"accent": {"ansi:6", true, false}, "tool": {"ansi:4", false, false}, "dim": {"", false, true},
			"ok": {"ansi:2", false, false}, "warn": {"ansi:3", false, false}, "bad": {"ansi:9", false, false},
			"mod": {"ansi:5", false, false}, "rule": {"", false, true}, "border": {"", false, true},
		},
		termcaps.BgLight: {
			"accent": {"ansi:4", true, false}, "tool": {"ansi:5", false, false}, "dim": {"", false, true},
			"ok": {"", false, false}, "warn": {"ansi:5", true, false}, "bad": {"ansi:1", false, false},
			"mod": {"ansi:5", false, false}, "rule": {"", false, true}, "border": {"", false, true},
		},
		termcaps.BgUnknown: {
			"accent": {"", true, false}, "tool": {"", false, false}, "dim": {"", false, true},
			"ok": {"", false, false}, "warn": {"", true, false}, "bad": {"ansi:9", false, false},
			"mod": {"", false, false}, "rule": {"", false, true}, "border": {"", false, true},
		},
	}
	for bg, tones := range want {
		th := newTheme(termcaps.Color16, bg)
		for tone, w := range tones {
			got := drawn(t, th, tone)
			if (look{got.fg, got.bold, got.faint}) != w {
				t.Errorf("16 colours, %s, %s: drawn %+v, want %+v", bg, tone, got, w)
			}
		}
	}
}

func TestNoColourUsesNoColour(t *testing.T) {
	t.Parallel()
	for _, bg := range backgrounds() {
		for _, th := range []*theme.Theme{newTheme(termcaps.ColorNone, bg), asciiTheme(termcaps.ColorNone, bg)} {
			for _, s := range everything(th) {
				for _, c := range paint(t, s) {
					if c.fg != "" || c.bg != "" {
						t.Errorf("%q is coloured (%s on %s) with no colour", s, c.fg, c.bg)
					}
				}
			}
			// Without colour the roles that matter most are still told apart.
			for tone, want := range map[string]cell{
				"accent": {text: "x", bold: true}, "warn": {text: "x", bold: true}, "bad": {text: "x", bold: true},
				"dim": {text: "x", faint: true}, "tool": {text: "x"}, "ok": {text: "x"}, "mod": {text: "x"},
				"rule": {text: "x"}, "border": {text: "x"},
			} {
				if got := drawn(t, th, tone); got != want {
					t.Errorf("no colour, %s: drawn %+v, want %+v", tone, got, want)
				}
			}
		}
	}
}

func TestBarFilledRule(t *testing.T) {
	t.Parallel()
	for _, c := range colors() {
		for _, bg := range backgrounds() {
			want := c >= termcaps.Color256 && bg != termcaps.BgUnknown
			th := newTheme(c, bg)
			if th.BarFilled != want {
				t.Errorf("%s: BarFilled = %v, want %v", capsName(c, bg), th.BarFilled, want)
			}
			// The same condition gives a code block its background, and a
			// selected row a fill and not reverse video.
			code, selected := paint(t, th.CodeFill("x", 2)), paint(t, th.Selected("x", 2))
			if (code[0].bg != "") != want {
				t.Errorf("%s: code background %q", capsName(c, bg), code[0].bg)
			}
			if (selected[0].bg != "") != want || selected[0].reverse == want {
				t.Errorf("%s: selected row drawn %+v", capsName(c, bg), selected[0])
			}
		}
	}
}

func TestAccentIsAlwaysBold(t *testing.T) {
	t.Parallel()
	for _, c := range colors() {
		for _, bg := range backgrounds() {
			if !drawn(t, newTheme(c, bg), "accent").bold {
				t.Errorf("%s: the accent is not bold", capsName(c, bg))
			}
		}
	}
}

func TestStyleNames(t *testing.T) {
	t.Parallel()
	th := newTheme(termcaps.ColorTrue, termcaps.BgDark)
	for _, tone := range []string{"", "no such tone", "ACCENT", "accent "} {
		if got := th.Style(tone).Render("body"); got != "body" {
			t.Errorf("Style(%q) drew %q, want plain text", tone, got)
		}
	}
	if got := drawn(t, th, "accent"); got.fg != "#5ec4c8" || !got.bold {
		t.Errorf("accent drawn %+v", got)
	}
	title := paint(t, th.Title().Render("x"))[0]
	if (title != cell{text: "x", bold: true}) {
		t.Errorf("Title drew %+v, want bold and nothing else", title)
	}
	pending := paint(t, th.Pending().Render("x"))[0]
	if (pending != cell{text: "x", faint: true, italic: true}) {
		t.Errorf("Pending drew %+v, want faint italic and nothing else", pending)
	}
}
