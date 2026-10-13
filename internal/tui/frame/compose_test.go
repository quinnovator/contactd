package frame_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/frame"
	"github.com/quinnovator/contactd/internal/tui/theme"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// numbered returns n lines named after a region: "body 0", "body 1", ...
func numbered(name string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s %d", name, i)
	}
	return out
}

// labelled is regions whose every row says where it belongs. Each part of
// several rows has more lines than any plan in these tests asks for.
func labelled() frame.Regions {
	return frame.Regions{
		Header: "header", HeaderRule: "header rule", BarRule: "bar rule", Bar: "bar", Hint: "hint",
		Body: numbered("body", 200), Palette: numbered("palette", 200), Input: numbered("input", 200),
	}
}

// planned is the rows Compose must give for a plan and the labelled regions.
func planned(r frame.Rows) []string {
	var out []string
	if r.Header > 0 {
		out = append(out, "header")
	}
	if r.HeaderRule > 0 {
		out = append(out, "header rule")
	}
	out = append(out, numbered("body", r.Body)...)
	out = append(out, numbered("palette", r.Palette)...)
	if r.BarRule > 0 {
		out = append(out, "bar rule")
	}
	if r.Bar > 0 {
		out = append(out, "bar")
	}
	out = append(out, numbered("input", r.Input)...)
	if r.Hint > 0 {
		out = append(out, "hint")
	}
	return out
}

// rowsOf splits a screen into its rows. A screen with no rows is "".
func rowsOf(screen string) []string {
	if screen == "" {
		return nil
	}
	return strings.Split(screen, "\n")
}

// Whatever Layout plans, Compose gives exactly those rows, in order, each
// from the region it belongs to.
func TestComposeEmitsThePlannedRows(t *testing.T) {
	t.Parallel()
	g := labelled()
	for _, filled := range []bool{true, false} {
		t.Run(fmt.Sprintf("filled=%t", filled), func(t *testing.T) {
			t.Parallel()
			for _, width := range []int{1, 30, 39, 40, 46, 100} {
				for height := 2; height <= 60; height++ {
					for _, lines := range []int{1, 3, 20} {
						for _, want := range []int{0, 5, 11} {
							r := frame.Layout(width, height, lines, want, filled)
							got := rowsOf(frame.Compose(r, g))
							if len(got) != height {
								t.Fatalf("%dx%d, plan %+v: %d rows", width, height, r, len(got))
							}
							if exp := planned(r); !slices.Equal(got, exp) {
								t.Fatalf("%dx%d, plan %+v:\n got %q\nwant %q", width, height, r, got, exp)
							}
						}
					}
				}
			}
		})
	}
}

// The screen has as many rows as the plan whatever was drawn: nothing at all,
// a transcript of one line, or one of far more lines than there are rows.
func TestComposeRowCount(t *testing.T) {
	t.Parallel()
	regions := []struct {
		name string
		g    frame.Regions
	}{
		{"nothing drawn", frame.Regions{}},
		{"one line in each part", frame.Regions{
			Header: "header", Bar: "bar",
			Body: []string{"body"}, Palette: []string{"palette"}, Input: []string{"input"},
		}},
		{"too many lines in each part", labelled()},
		{"line breaks in every row", frame.Regions{
			Header: "a\nb\nc", HeaderRule: "\n\n", BarRule: "a\n", Bar: "\na", Hint: "a\r\nb",
			Body: []string{"a\nb", "\n", "c\n\nd"}, Palette: []string{"\n\n\n"}, Input: []string{"a\nb\nc\nd"},
		}},
	}
	for _, tt := range regions {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, width := range []int{1, 39, 40, 100} {
				for height := 1; height <= 100; height++ {
					for _, lines := range []int{1, 4, 50} {
						for _, want := range []int{0, 3, 20} {
							for _, filled := range []bool{true, false} {
								r := frame.Layout(width, height, lines, want, filled)
								got := frame.Compose(r, tt.g)
								if n := strings.Count(got, "\n") + 1; n != height {
									t.Fatalf("%dx%d, plan %+v: %d rows", width, height, r, n)
								}
							}
						}
					}
				}
			}
		})
	}
}

func TestCompose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rows frame.Rows
		g    frame.Regions
		want []string
	}{
		{"no rows is no screen", frame.Rows{}, labelled(), nil},
		{"a plan with no height", frame.Layout(100, 0, 1, 0, true), labelled(), nil},
		{"one row is the input", frame.Layout(100, 1, 1, 0, true), labelled(), []string{"input 0"}},
		{
			"two rows", frame.Layout(100, 2, 1, 0, true), labelled(),
			[]string{"body 0", "input 0"},
		},
		{
			"three rows", frame.Layout(100, 3, 1, 0, false), labelled(),
			[]string{"body 0", "bar", "input 0"},
		},
		{
			"a part that is not planned is left out",
			frame.Rows{Body: 2, Input: 1},
			labelled(),
			[]string{"body 0", "body 1", "input 0"},
		},
		{
			"a short part is filled up below",
			frame.Rows{Header: 1, Body: 4, Palette: 2, Bar: 1, Input: 3, Hint: 1},
			frame.Regions{
				Header: "header", Bar: "bar", Hint: "hint",
				Body: []string{"a", "b"}, Palette: []string{"p"}, Input: []string{"i"},
			},
			[]string{"header", "a", "b", "", "", "p", "", "bar", "i", "", "", "hint"},
		},
		{
			"a long part is cut after its first rows",
			frame.Rows{Body: 2, Palette: 1, Input: 1},
			frame.Regions{
				Body: []string{"a", "b", "c", "d"}, Palette: []string{"p", "q"}, Input: []string{"i", "j"},
			},
			[]string{"a", "b", "p", "i"},
		},
		{
			"nothing drawn is empty rows, and as many",
			frame.Rows{Header: 1, HeaderRule: 1, Body: 3, Palette: 2, BarRule: 1, Bar: 1, Input: 3, Hint: 1},
			frame.Regions{},
			slices.Repeat([]string{""}, 13),
		},
		{
			"a region with a line break in it keeps to its row",
			frame.Rows{Header: 1, HeaderRule: 1, Body: 2, Palette: 1, BarRule: 1, Bar: 1, Input: 1, Hint: 1},
			frame.Regions{
				Header: "header\nspill", HeaderRule: "rule\n", BarRule: "\nbar rule", Bar: "bar\n\n", Hint: "hint\nmore",
				Body: []string{"a\nb", "c"}, Palette: []string{"p\nq"}, Input: []string{"i\nj"},
			},
			[]string{"header", "rule", "a", "c", "p", "", "bar", "i", "hint"},
		},
		{
			"a one-row part planned for three rows",
			frame.Rows{Header: 3, Body: 1},
			frame.Regions{Header: "header", Body: []string{"a"}},
			[]string{"header", "", "", "a"},
		},
		{
			"a negative count is no rows",
			frame.Rows{Header: -1, HeaderRule: -3, Body: -2, Palette: -1, BarRule: -1, Bar: -1, Input: 1, Hint: -7},
			labelled(),
			[]string{"input 0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := frame.Compose(tt.rows, tt.g)
			if rows := rowsOf(got); !slices.Equal(rows, tt.want) {
				t.Errorf("Compose(%+v)\n got %q\nwant %q", tt.rows, rows, tt.want)
			}
		})
	}
}

// Compose does not change the regions it is given.
func TestComposeLeavesRegionsAlone(t *testing.T) {
	t.Parallel()
	g := frame.Regions{Header: "h\nx", Body: []string{"a\nb", "c"}, Input: []string{"i"}}
	body := slices.Clone(g.Body)
	frame.Compose(frame.Rows{Header: 2, Body: 5, Input: 3}, g)
	if g.Header != "h\nx" || !slices.Equal(g.Body, body) || len(g.Input) != 1 {
		t.Errorf("regions changed: %+v", g)
	}
}

// mockBody is the transcript rows of the Compatibility mock-up's panels, as
// plain text.
func mockBody(th *theme.Theme) []string {
	return []string{
		th.G.Prompt + " plot last week's latency",
		"",
		th.G.Tool + " python",
		"    df.plot.bar()",
		"  " + th.G.Elbow + " saved latency.png",
		"",
		"  p99 peaked on Thursday at 7.2 s.",
	}
}

// screen draws a whole chat screen out of this package and the theme: the
// header and hint line from here, the rules, the bar and the box from the
// theme, and a transcript of plain rows.
func screen(th *theme.Theme, width, height, paletteWant int, h frame.HeaderInfo, bar []theme.Cell, clock, draft string) (frame.Rows, string) {
	r := frame.Layout(width, height, 1, paletteWant, th.BarFilled)
	body := make([]string, r.Body)
	for i, l := range mockBody(th) {
		if i < len(body) {
			body[i] = l
		}
	}
	for i := range body {
		body[i] = th.Pad(body[i], width)
	}
	input := []string{th.Pad(th.G.Prompt+" "+draft, width)}
	if r.Boxed {
		input = th.Box([]string{th.G.Prompt + " " + draft}, width, "", "")
	}
	var palette []string
	if r.Palette > 0 {
		palette = th.Box(make([]string, max(r.Palette-2, 0)), width, "", "")
	}
	barRule, barRow := th.Bar(bar, []theme.Cell{{Text: clock}}, width)
	return r, frame.Compose(r, frame.Regions{
		Header:     frame.Header(th, h, width),
		HeaderRule: th.Rule(width, ""),
		Body:       body,
		Palette:    palette,
		BarRule:    barRule,
		Bar:        barRow,
		Input:      input,
		Hint:       frame.Hints(th, frame.HintState{PaletteOpen: r.Palette > 0}, width),
	})
}

// The whole screen at the sizes the mock-ups draw, in each of the four
// terminals: as many rows as the terminal has, each as wide as the terminal.
func TestComposeScreens(t *testing.T) {
	t.Parallel()
	sizes := []struct{ width, height, palette int }{
		{100, 30, 0}, {100, 30, 11}, {46, 12, 0}, {30, 12, 0}, {40, 16, 0}, {39, 16, 0}, {20, 5, 0}, {10, 3, 0},
	}
	for _, l := range looks() {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			th := themeOf(l)
			info := frame.HeaderInfo{
				Label: "ada", Provider: "deepseek", Model: "deepseek-flash",
				Range: "7 Oct " + th.G.Dash + " 12 Oct", HasCall: true, CachePct: 82,
			}
			bar := []theme.Cell{{Text: th.G.Dot + " live", Tone: "ok"}, {Text: "cache 82%"}, {Text: "7.2s"}}
			var golden strings.Builder
			for _, s := range sizes {
				r, got := screen(th, s.width, s.height, s.palette, info, bar, "12:04", "compare with the week before")
				rows := rowsOf(got)
				if len(rows) != s.height {
					t.Errorf("%dx%d: %d rows, plan %+v", s.width, s.height, len(rows), r)
				}
				for i, row := range rows {
					if w := th.Width(row); w != s.width {
						t.Errorf("%dx%d: row %d is %d cells wide: %q", s.width, s.height, i, w, row)
					}
				}
				fmt.Fprintf(&golden, "%dx%d, palette %d\n%s", s.width, s.height, s.palette, quoted(rowsOf(ansi.Strip(got))...))
			}
			if l.name == "none-ascii" {
				wantASCII(t, golden.String())
			}
			tuitest.Golden(t, "screens-"+l.name+".golden", []byte(golden.String()))
		})
	}
}

// between puts left and right at the two ends of a row of w cells. Both are
// ASCII or single-cell characters.
func between(left, right string, w int) string {
	gap := w - len([]rune(left)) - len([]rune(right))
	return left + strings.Repeat(" ", max(gap, 0)) + right
}

// The Compatibility mock-up's panels, row by row: a header, a rule, the
// transcript, the bar (under a rule where it has no fill) and one input row,
// with no box and no hint line.
func TestComposeCompatibilityPanels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		look        look
		width       int
		headerRight string
		barRule     bool
		bar, draft  string
		body        int
	}{
		{"truecolor at 46 columns", looks()[0], 46, "cache 82%", false, "● live │ cache 82% │ 7.2s", "❯ compare", 8},
		{"16 colours at 46 columns", looks()[1], 46, "cache 82%", true, "● live │ cache 82% │ 7.2s", "❯ compare", 7},
		{"30 columns", looks()[0], 30, "82%", false, "● live │ 82%", "❯ compare", 8},
		{"no colour, ASCII", looks()[3], 46, "cache 82%", true, "* live | cache 82% | 7.2s", "> compare", 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			th := themeOf(tt.look)
			info := frame.HeaderInfo{Label: "ada", Provider: "deepseek", Model: "deepseek-flash", HasCall: true, CachePct: 82}
			bar := []theme.Cell{{Text: th.G.Dot + " live", Tone: "ok"}, {Text: "cache 82%"}, {Text: "7.2s"}}
			left := "ada  deepseek-flash"
			if tt.width < 40 {
				bar = []theme.Cell{{Text: th.G.Dot + " live", Tone: "ok"}, {Text: "82%"}}
				left = "ada"
			}
			r, got := screen(th, tt.width, 12, 0, info, bar, "12:04", "compare")
			rows := rowsOf(got)
			if len(rows) != 12 {
				t.Fatalf("%d rows, plan %+v", len(rows), r)
			}
			if r.Boxed || r.Hint != 0 || r.Body != tt.body {
				t.Errorf("plan %+v: want no box, no hint line and %d transcript rows", r, tt.body)
			}
			rule := strings.Repeat(th.G.HRule, tt.width)
			if want := between(left, tt.headerRight, tt.width); shown(rows[0]) != want {
				t.Errorf("header\n got %q\nwant %q", shown(rows[0]), want)
			}
			if shown(rows[1]) != rule {
				t.Errorf("the row under the header: %q", shown(rows[1]))
			}
			if first := shown(rows[2]); first != th.G.Prompt+" plot last week's latency" {
				t.Errorf("first transcript row: %q", first)
			}
			if above := shown(rows[9]); tt.barRule != (above == rule) {
				t.Errorf("the row above the bar: %q", above)
			}
			if want := between(tt.bar, "12:04", tt.width); shown(rows[10]) != want {
				t.Errorf("bar\n got %q\nwant %q", shown(rows[10]), want)
			}
			if shown(rows[11]) != tt.draft {
				t.Errorf("input row: %q", shown(rows[11]))
			}
		})
	}
}
