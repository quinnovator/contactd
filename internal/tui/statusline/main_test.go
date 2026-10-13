package statusline_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

func TestMain(m *testing.M) { tuitest.Main(m) }

// unicodeMarks and asciiMarks are the glyphs of the two glyph sets.
func unicodeMarks() statusline.Marks { return statusline.Marks{Dot: "●", Newer: "↓"} }

func asciiMarks() statusline.Marks { return statusline.Marks{Dot: "*", Newer: "v"} }

// idle is the state the spec's worked rows are computed from: connected,
// one model call behind it, nothing happening.
func idle() statusline.Snapshot {
	return statusline.Snapshot{
		Conn: statusline.ConnLive, Contact: "ada", Provider: "deepseek", Model: "deepseek-flash",
		HasCall: true, CacheReported: true, Input: 3700, Cached: 3034, Output: 210, MS: 7200,
		Clock: "12:04",
	}
}

// toolRunning is the state of the Frame mock: a reply in flight, python
// running, and a call that was mostly uncached.
func toolRunning() statusline.Snapshot {
	s := idle()
	s.Input, s.Cached, s.MS = 3200, 1536, 5400
	s.Replying, s.Running, s.Clock = true, "python", "12:06"
	return s
}

// client is how the client calls Layout.
func client() statusline.LayoutOptions {
	return statusline.LayoutOptions{SepWidth: 3, MinGap: 2}
}

// preset returns a preset that must exist.
func preset(t *testing.T, name string) statusline.Config {
	t.Helper()
	c, ok := statusline.Preset(name)
	if !ok {
		t.Fatalf("no preset %q", name)
	}
	return c
}

// drawing returns the segments of the status-line editor mock: the ops
// preset with tokens and agents off, and the git mod's branch.
func drawing(t *testing.T) statusline.Config {
	t.Helper()
	c := preset(t, "ops")
	for i, s := range c.Segments {
		if s.Name == "tokens" || s.Name == "agents" {
			c.Segments[i].On = false
		}
	}
	return c.WithMods([]statusline.ModSegment{{Mod: "git", Name: "branch"}})
}

// index returns the place of a segment in a config.
func index(t *testing.T, c statusline.Config, id string) int {
	t.Helper()
	for i, s := range c.Segments {
		if s.ID() == id {
			return i
		}
	}
	t.Fatalf("no segment %q", id)
	return -1
}

// texts returns the text of each cell.
func texts(cells []statusline.Cell) []string {
	out := make([]string, 0, len(cells))
	for _, c := range cells {
		out = append(out, c.Text)
	}
	return out
}

// sides draws each side of the bar as the theme would, with its padding:
// the cells joined by the separator.
func sides(left, right []statusline.Cell, width int, sep string) (l, r string) {
	pad := strings.Repeat(" ", statusline.Padding(width))
	if len(left) > 0 {
		l = pad + strings.Join(texts(left), sep)
	}
	if len(right) > 0 {
		r = strings.Join(texts(right), sep) + pad
	}
	return l, r
}

// bar draws the whole row: the left side at the left edge and the right
// side at the right. A row that does not fit comes out wider than width.
func bar(left, right []statusline.Cell, width int, sep string) string {
	l, r := sides(left, right, width, sep)
	fill := width - utf8.RuneCountInString(l) - utf8.RuneCountInString(r)
	return l + strings.Repeat(" ", max(fill, 0)) + r
}

// need is the width the cells take as the client draws them: padding,
// separators and the gap between the sides included.
func need(left, right []statusline.Cell, width int, o statusline.LayoutOptions) int {
	measure := o.Measure
	if measure == nil {
		measure = utf8.RuneCountInString
	}
	total := 2 * statusline.Padding(width)
	for _, side := range [][]statusline.Cell{left, right} {
		for i, c := range side {
			if i > 0 {
				total += o.SepWidth
			}
			total += measure(c.Text)
		}
	}
	if len(left) > 0 && len(right) > 0 {
		total += o.MinGap
	}
	return total
}
