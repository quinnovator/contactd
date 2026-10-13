package draw_test

import (
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/draw"
)

func TestLive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		look    look
		width   int
		live    transcript.Live
		running time.Duration
		want    []string
	}{
		{
			"text is shown as written", trueDark(), 30,
			transcript.Live{Typing: true, Text: "Two **things** overlapped, and both matter:\n\n- one\n"},
			0,
			[]string{"  Two **things** overlapped,", "  and both matter:", "", "  - one"},
		},
		{
			"a tool call being written", trueDark(), 60,
			transcript.Live{Typing: true, Starting: "python"},
			0,
			[]string{"⏺ python …"},
		},
		{
			"text, then a tool call", trueDark(), 60,
			transcript.Live{Typing: true, Text: "Let me check.", Starting: "python"},
			0,
			[]string{"  Let me check.", "", "⏺ python …"},
		},
		{
			"a running tool is one row", trueDark(), 60,
			transcript.Live{Typing: true, Running: "python"},
			2100 * time.Millisecond,
			[]string{"  ⎿ running… 2.1s"},
		},
		{
			"it stands under the call being written", trueDark(), 60,
			transcript.Live{Starting: "python", Running: "python"},
			0,
			[]string{"⏺ python …", "  ⎿ running… 0.0s"},
		},
		{
			"and a blank row under text", trueDark(), 60,
			transcript.Live{Text: "So far", Running: "python"},
			61 * time.Second,
			[]string{"  So far", "", "  ⎿ running… 1m01s"},
		},
		{
			"ASCII", noneASCII(), 60,
			transcript.Live{Text: "So far", Starting: "python", Running: "python"},
			12340 * time.Millisecond,
			[]string{"  So far", "", "* python ...", "  |_ running... 12.3s"},
		},
		{
			"a tool with no name", trueDark(), 60,
			transcript.Live{Starting: " "},
			0,
			[]string{"⏺ tool …"},
		},
		{"nothing yet", trueDark(), 60, transcript.Live{Typing: true, Text: " \n"}, 0, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := c.look.renderer(c.width).Live(c.live, c.running)
			fits(t, c.name, got, c.width)
			equal(t, c.name, stripped(got), c.want)
		})
	}

	r := trueDark().renderer(60)
	lines := r.Live(transcript.Live{Text: "So **far**", Starting: "python", Running: "python"}, time.Second)
	if lines[0] != "  So **far**" {
		t.Errorf("the text of the reply in flight is styled: %q", lines[0])
	}
	for _, c := range find(t, lines[2], "⏺ python") {
		if c.fg != darkTool {
			t.Fatalf("the tool is drawn %+v, want the tool tone", c)
		}
	}
	if c := find(t, lines[2], "…")[0]; c.fg != darkDim {
		t.Errorf("the ellipsis is drawn %+v, want dim", c)
	}
	for _, c := range find(t, lines[3], "⎿ running… 1.0s") {
		if c.fg != darkTool {
			t.Fatalf("the running row is drawn %+v, want the tool tone", c)
		}
	}
	if _, markdowns, _ := r.Counts(); markdowns != 0 {
		t.Errorf("the Markdown renderer was called %d times for a reply in flight", markdowns)
	}
}

func TestSeconds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0.0s"},
		{-time.Second, "0.0s"},
		{99 * time.Millisecond, "0.0s"},
		{100 * time.Millisecond, "0.1s"},
		{2100 * time.Millisecond, "2.1s"},
		{2199 * time.Millisecond, "2.1s"},
		{59999 * time.Millisecond, "59.9s"},
		{time.Minute, "1m00s"},
		{83 * time.Second, "1m23s"},
		{61 * time.Minute, "61m00s"},
	}
	for _, c := range cases {
		if got := draw.Seconds(c.d); got != c.want {
			t.Errorf("Seconds(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// streamed is a reply as it arrives: paragraphs, a list, a long line with no
// break, a blank line and a line the terminal must not act on.
const streamed = "Two things overlapped, and both of them matter for what comes next:\r\n\r\n" +
	"- Turn 6 ran three model calls with tool runs in between.\n" +
	"- The turn after it re-read the tool results uncached.\n\n\n" +
	"    indented, as code would be\n" +
	"这是一个很长的中文句子用来测试换行是否正确处理宽字符 \x1b[31mred\x1b[0m\n" +
	"The fix is small. Put the time note after the tool steps, and nothing else has to change at all."

func TestLiveTextMatchesAFreshDrawing(t *testing.T) {
	t.Parallel()
	// However the reply is cut into fragments, what is drawn after each is
	// what a Renderer that never saw the fragments before it draws.
	for _, step := range []int{1, 3, 7, 40, len(streamed)} {
		r := trueDark().renderer(36)
		var kept [][]string
		for end := step; ; end += step {
			end = min(end, len(streamed))
			got := r.Live(transcript.Live{Text: streamed[:end]}, 0)
			want := trueDark().renderer(36).Live(transcript.Live{Text: streamed[:end]}, 0)
			equal(t, "after "+streamed[:end], got, want)
			fits(t, "live", got, 36)
			kept = append(kept, got)
			if end == len(streamed) {
				break
			}
		}
		// What was handed out earlier is not changed by what came after: the
		// window keeps the slice.
		at := 0
		for end := step; at < len(kept); end += step {
			end = min(end, len(streamed))
			equal(t, "kept", kept[at], trueDark().renderer(36).Live(transcript.Live{Text: streamed[:end]}, 0))
			at++
		}
	}
}

func TestLiveWrapsOnlyTheLastLine(t *testing.T) {
	t.Parallel()
	wraps := func(r *draw.Renderer) int {
		_, _, n := r.Counts()
		return n
	}
	r := trueDark().renderer(40)
	paragraph := strings.Repeat("words that fill a line and more ", 6)

	// One line that grows is wrapped once for each fragment.
	text := ""
	for i := range 5 {
		text += "word "
		r.Live(transcript.Live{Text: text}, 0)
		if got := wraps(r); got != i+1 {
			t.Fatalf("after %d fragments of one line, %d lines were wrapped", i+1, got)
		}
	}

	// A hundred finished paragraphs later, a fragment still costs one line:
	// the finished ones keep the rows they were given.
	text += strings.Repeat(paragraph+"\n\n", 100)
	r.Live(transcript.Live{Text: text}, 0)
	before := wraps(r)
	for i := range 20 {
		text += "more "
		lines := r.Live(transcript.Live{Text: text}, 0)
		if got := wraps(r) - before; got != i+1 {
			t.Fatalf("fragment %d of the last line wrapped %d lines in all", i+1, got)
		}
		if last := lines[len(lines)-1]; !strings.HasSuffix(last, "more ") {
			t.Fatalf("the last row is %q", last)
		}
	}

	// A fragment that ends lines wraps those lines, and no finished one.
	before = wraps(r)
	text += "and the end.\nA new line.\nAnother"
	r.Live(transcript.Live{Text: text}, 0)
	if got := wraps(r) - before; got != 3 {
		t.Errorf("a fragment with two line breaks wrapped %d lines, want 3", got)
	}

	// A new reply starts from nothing, and is drawn as itself.
	got := r.Live(transcript.Live{Text: "Something else."}, 0)
	equal(t, "the next reply", got, []string{"  Something else."})
	// So does the same reply cut back, as after a reconnect.
	got = r.Live(transcript.Live{Text: "Something"}, 0)
	equal(t, "a reply that got shorter", got, []string{"  Something"})
	got = r.Live(transcript.Live{Text: "one\ntwo\n"}, 0)
	equal(t, "two lines", got, []string{"  one", "  two"})
	got = r.Live(transcript.Live{Text: "one\nTWO\nthree"}, 0)
	equal(t, "a longer reply that differs in what was finished", got, []string{"  one", "  TWO", "  three"})
	got = r.Live(transcript.Live{Text: "one\nTWO"}, 0)
	equal(t, "the same, cut back inside what was finished", got, []string{"  one", "  TWO"})
}
