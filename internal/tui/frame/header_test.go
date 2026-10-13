package frame_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/frame"
	"github.com/quinnovator/contactd/internal/tui/theme"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// ada is the header of the mock-ups: the Frame screen's route and date range,
// and the cache share of the Compatibility panels.
func ada(th *theme.Theme) frame.HeaderInfo {
	return frame.HeaderInfo{
		Label: "ada", Provider: "deepseek", Model: "deepseek-flash",
		Range: "7 Oct " + th.G.Dash + " 12 Oct", HasCall: true, CachePct: 82,
	}
}

// The header at the widths the mock-ups draw, and on both sides of the two
// widths where it says less.
func TestHeaderAsDrawn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		width       int
		left, right string
	}{
		{100, "ada  deepseek/deepseek-flash", "7 Oct – 12 Oct"},
		{80, "ada  deepseek/deepseek-flash", "7 Oct – 12 Oct"},
		{79, "ada  deepseek-flash", "cache 82%"},
		{46, "ada  deepseek-flash", "cache 82%"},
		{40, "ada  deepseek-flash", "cache 82%"},
		{39, "ada", "82%"},
		{30, "ada", "82%"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d columns", tt.width), func(t *testing.T) {
			t.Parallel()
			th := dark()
			got := frame.Header(th, ada(th), tt.width)
			wantRow(t, th, got, tt.width)
			if want := between(tt.left, tt.right, tt.width); ansi.Strip(got) != want {
				t.Errorf("Header at %d\n got %q\nwant %q", tt.width, ansi.Strip(got), want)
			}
		})
	}
}

// The label is bold, and the route and the right side are dim.
func TestHeaderStyles(t *testing.T) {
	t.Parallel()
	for _, l := range looks() {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			th := themeOf(l)
			dim := th.Style("dim")
			tests := []struct {
				width int
				parts []string
			}{
				{100, []string{
					th.Title().Render("ada"), dim.Render("  deepseek/deepseek-flash"), dim.Render(ada(th).Range),
				}},
				{46, []string{th.Title().Render("ada"), dim.Render("  deepseek-flash"), dim.Render("cache 82%")}},
				{30, []string{th.Title().Render("ada"), dim.Render("82%")}},
			}
			for _, tt := range tests {
				got := frame.Header(th, ada(th), tt.width)
				rest := got
				for _, part := range tt.parts {
					i := strings.Index(rest, part)
					if i < 0 {
						t.Errorf("at %d columns %q is not in the row, or not in its place: %q", tt.width, part, got)
						break
					}
					rest = rest[i+len(part):]
				}
			}
		})
	}
}

func TestHeader(t *testing.T) {
	t.Parallel()
	const long = "the contact with a very long name"
	tests := []struct {
		name  string
		info  frame.HeaderInfo
		width int
		want  string
	}{
		{
			"nothing on the right before the first model call, at 79",
			frame.HeaderInfo{Label: "ada", Provider: "deepseek", Model: "deepseek-flash", CachePct: 82},
			79, "ada  deepseek-flash",
		},
		{
			"nor at 39",
			frame.HeaderInfo{Label: "ada", Provider: "deepseek", Model: "deepseek-flash", CachePct: 82},
			39, "ada",
		},
		{
			"the date range needs no model call",
			frame.HeaderInfo{Label: "ada", Provider: "deepseek", Model: "deepseek-flash", Range: "12 Oct"},
			100, between("ada  deepseek/deepseek-flash", "12 Oct", 100),
		},
		{
			"no date range, nothing on the right at 80",
			frame.HeaderInfo{Label: "ada", Provider: "deepseek", Model: "deepseek-flash", HasCall: true, CachePct: 82},
			80, "ada  deepseek/deepseek-flash",
		},
		{
			"the date range is not shown under 80",
			frame.HeaderInfo{Label: "ada", Model: "deepseek-flash", Range: "12 Oct"},
			79, "ada  deepseek-flash",
		},
		{
			"a cache share of nothing is still a share",
			frame.HeaderInfo{Label: "ada", Model: "m", HasCall: true},
			46, between("ada  m", "cache 0%", 46),
		},
		{
			"a share over the whole is the whole",
			frame.HeaderInfo{Label: "ada", Model: "m", HasCall: true, CachePct: 250},
			30, between("ada", "100%", 30),
		},
		{
			"a share under nothing is nothing",
			frame.HeaderInfo{Label: "ada", Model: "m", HasCall: true, CachePct: -3},
			46, between("ada  m", "cache 0%", 46),
		},
		{
			"no provider",
			frame.HeaderInfo{Label: "ada", Model: "deepseek-flash"},
			100, "ada  deepseek-flash",
		},
		{
			"no model",
			frame.HeaderInfo{Label: "ada", Provider: "deepseek"},
			100, "ada  deepseek",
		},
		{
			"no model under 80 columns",
			frame.HeaderInfo{Label: "ada", Provider: "deepseek"},
			79, "ada",
		},
		{
			"no route",
			frame.HeaderInfo{Label: "ada"},
			100, "ada",
		},
		{
			"no label",
			frame.HeaderInfo{Provider: "deepseek", Model: "deepseek-flash", HasCall: true, CachePct: 5},
			60, between("deepseek-flash", "cache 5%", 60),
		},
		{"nothing at all", frame.HeaderInfo{}, 20, ""},
		{
			"both sides fit two cells apart",
			frame.HeaderInfo{Label: long, Model: "m", HasCall: true, CachePct: 82},
			47, long + "  m  cache 82%",
		},
		{
			"the right side gives way first",
			frame.HeaderInfo{Label: long, Model: "m", HasCall: true, CachePct: 82},
			46, long + "  m",
		},
		{
			"and gives way whole",
			frame.HeaderInfo{Label: long, Model: "deepseek-flash", HasCall: true, CachePct: 82},
			55, long + "  deepseek-flash",
		},
		{
			"then the left is truncated, the route before the label",
			frame.HeaderInfo{Label: long, Model: "deepseek-flash", HasCall: true, CachePct: 82},
			40, long + "  deep…",
		},
		{
			"and the label last",
			frame.HeaderInfo{Label: long, Model: "deepseek-flash", HasCall: true, CachePct: 82},
			20, "the contact with a …",
		},
		{
			"one column",
			frame.HeaderInfo{Label: "ada", Model: "deepseek-flash", HasCall: true, CachePct: 82},
			1, "…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			th := dark()
			got := frame.Header(th, tt.info, tt.width)
			wantRow(t, th, got, tt.width)
			if shown(got) != strings.TrimRight(tt.want, " ") {
				t.Errorf("Header(%+v, %d)\n got %q\nwant %q", tt.info, tt.width, shown(got), tt.want)
			}
		})
	}
}

// A terminal with no width has no header.
func TestHeaderNoWidth(t *testing.T) {
	t.Parallel()
	th := dark()
	for _, width := range []int{0, -1, -80} {
		if got := frame.Header(th, ada(th), width); got != "" {
			t.Errorf("Header at %d columns = %q, want nothing", width, got)
		}
	}
}

// Whatever the label holds and however the terminal counts cells, the header
// is one row of exactly the width.
func TestHeaderWidth(t *testing.T) {
	t.Parallel()
	infos := func(th *theme.Theme) []frame.HeaderInfo {
		return []frame.HeaderInfo{
			ada(th),
			{Label: "ada"},
			{},
			{Label: "这是一个很长的中文句子用来测试", Provider: "深度", Model: "求索", Range: "7 Oct", HasCall: true, CachePct: 100},
			{Label: "👨‍👩‍👧 family", Provider: "p", Model: "👍🏽 model", Range: "12 Oct", HasCall: true, CachePct: 9},
			{Label: "two\nlines\tand a tab", Provider: "a\rb", Model: "c\x07d", Range: "x\ny", HasCall: true},
			{Label: strings.Repeat("long ", 80), Provider: strings.Repeat("p", 90), Model: strings.Repeat("m", 90), Range: strings.Repeat("r", 90)},
		}
	}
	for _, l := range everyLook() {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			th := themeOf(l)
			for _, info := range infos(th) {
				for width := 1; width <= 200; width++ {
					got := frame.Header(th, info, width)
					if strings.ContainsAny(got, "\r\n\t\a") || th.Width(got) != width {
						t.Fatalf("Header(%+v, %d) is %d cells wide: %q", info, width, th.Width(got), got)
					}
				}
			}
		})
	}
}

// Every piece is drawn as text whatever it holds: no escape sequence in a
// label, a route or a date range reaches the row, and replaced glyphs of other
// widths leave the row exactly as wide as the terminal.
func TestHeaderIsInert(t *testing.T) {
	t.Parallel()
	for _, l := range everyLook() {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			for _, th := range []*theme.Theme{themeOf(l), wideGlyphs(t, l)} {
				for _, h := range hostile() {
					info := frame.HeaderInfo{
						Label: "ada" + h, Provider: h + "deepseek", Model: "deepseek" + h + "-flash",
						Range: h + "7 Oct " + th.G.Dash + " 12 Oct" + h, HasCall: true, CachePct: 82,
					}
					for width := 1; width <= 120; width++ {
						got := frame.Header(th, info, width)
						wantRow(t, th, got, width)
						wantInert(t, got)
					}
				}
			}
		})
	}
}

// What is left of an escape sequence is drawn as the characters it was made
// of.
func TestHeaderAsText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		info  frame.HeaderInfo
		width int
		want  string
	}{
		{
			"a label that would clear the screen",
			frame.HeaderInfo{Label: "ada\x1b[2J", Model: "m"},
			60, "ada[2J  m",
		},
		{
			"a route that would set the title",
			frame.HeaderInfo{Label: "ada", Provider: "p\x1b]0;x\a", Model: "\x1b[7mm"},
			100, "ada  p]0;x/[7mm",
		},
		{
			"a date range that would swallow the rest",
			frame.HeaderInfo{Label: "ada", Range: "12 Oct\x1bP"},
			80, between("ada", "12 OctP", 80),
		},
		{
			"a label that is nothing but escape characters",
			frame.HeaderInfo{Label: "\x1b\x1b", Model: "m"},
			60, "m",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			th := dark()
			got := frame.Header(th, tt.info, tt.width)
			wantRow(t, th, got, tt.width)
			wantInert(t, got)
			if shown(got) != tt.want {
				t.Errorf("the header reads %q, want %q", shown(got), tt.want)
			}
		})
	}
}

// With ASCII glyphs and an ASCII contact no byte of the header is above 0x7F,
// truncated or not.
func TestHeaderASCII(t *testing.T) {
	t.Parallel()
	th := plainASCII()
	for width := 1; width <= 120; width++ {
		wantASCII(t, frame.Header(th, ada(th), width))
	}
	long := frame.HeaderInfo{Label: "the contact with a very long name", Model: "m"}
	if got := shown(frame.Header(th, long, 20)); got != "the contact with ..." {
		t.Errorf("a truncated header: %q", got)
	}
}

func TestHeaderGolden(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for _, l := range looks() {
		th := themeOf(l)
		for _, width := range []int{100, 79, 46, 39, 30} {
			fmt.Fprintf(&b, "%s at %d\n%s", l.name, width, quoted(frame.Header(th, ada(th), width)))
		}
	}
	tuitest.Golden(t, "header.golden", []byte(b.String()))
}
