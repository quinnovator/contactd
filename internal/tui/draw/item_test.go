package draw_test

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/draw"
	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// wide is text that is hard to keep inside a row: Chinese, emoji the two ways
// of counting cells disagree about, and a word longer than any row.
const wide = "这是一个很长的中文句子用来测试换行是否正确处理宽字符 and 👍🏽 and 👨\u200d👩\u200d👧 and 🇯🇵 " +
	"then Averylongwordwithoutanyspacesthatmustbebrokensomewherebecauseitcannotfitonanylineofthescreenatall."

// rich is a reply with every kind of Markdown the client is likely to meet.
const rich = "# Latency report\n\n" +
	"Two things overlapped, and both are *worth* a **closer look that goes on for long enough to wrap** with `inline code`:\n\n" +
	"- Turn 6 ran three model calls with tool runs in between, which is more than any other turn this week.\n" +
	"- The turn after it re-read the tool results uncached.\n" +
	"  - A nested point with a [link](https://example.com/a/rather/long/path/that/will/not/fit/on/one/line/at/thirty/columns).\n\n" +
	"> A quoted remark that is long enough to wrap at every width in the matrix, even the widest of them all.\n\n" +
	"| day | p50 | p99 |\n| --- | --- | --- |\n| Thu | 3.1 | 7.2 |\n\n" +
	"1. " + wide + "\n2. ~~struck~~ and https://example.com/auto\n\n---\n\n" +
	"```go\nliveMsgs = append(liveMsgs, toolSteps...) // then the time note, after every tool step, so that the prefix stays the same\n" +
	"\tif x {\n\t\treturn \"这是中文\"\n\t}\n```\n\n" +
	"![chart](latency.png)\n\n" +
	"See also ![inline](other.png) beside words, and [a reference][r].\n\n" +
	"```\nno language\n```\n\n[r]: https://example.com/ref\n"

// block is one thing drawn in the matrix.
type block struct {
	name  string
	lines []string
}

// everything draws every kind of block there is, with text that is hard to
// fit, folded and expanded, with images and mods and without. long adds a
// reply too long to read as Markdown, which is slow to wrap into narrow rows.
func everything(l look, width int, long bool) []block {
	longCode := strings.Repeat("for i in range(10): print('a long line of code that goes on', i, i * i, i * i * i)\n", 9) + "print('这是中文', '👍🏽')"
	output := strings.Repeat(wide+"\n", 3)

	pic := &images{shown: draw.Shown{
		Rows:    []string{"\x1b[38;2;1;2;3;48;2;4;5;6m" + strings.Repeat("▀", min(44, max(width-2, 1))) + "\x1b[m"},
		Caption: "latency.png · 640×360 · 692 kB · /image save 31 · changed since",
	}}
	text := &images{shown: draw.Shown{Line: "[image: latency.png, 640×360, 692 kB]", Hint: `/image save 31 "./p99 by day.png"`}}
	mods := &rendered{byKind: map[string][]wire.Line{
		"code:go":     {{{T: wide, Tone: "mod", Bold: true}, {T: " tail"}}, {}, {{T: "second"}}},
		"tool:python": {{{T: wide, Tone: "ok"}}, {{T: "second"}}},
	}}

	var out []block
	for _, o := range []struct {
		name string
		opts draw.Options
	}{
		{"folded", draw.Options{Width: width}},
		{"expanded", draw.Options{Width: width, Expanded: true}},
		{"pictures", draw.Options{Width: width, Images: pic, MaxCols: min(44, width-4), MaxRows: 10}},
		{"image lines", draw.Options{Width: width, Images: text}},
		{"mods", draw.Options{Width: width, Mods: mods}},
	} {
		r := l.with(o.opts)
		add := func(name string, lines []string) {
			out = append(out, block{o.name + "/" + name, lines})
		}
		kinds := map[string]*transcript.Item{
			"user":            item(transcript.User, 1, wide+"\n\n  second paragraph, indented"),
			"task":            {Kind: transcript.User, Seq: 2, Task: true, Text: wide},
			"agent":           agent(3, rich),
			"interrupted":     {Kind: transcript.Agent, Seq: 4, Text: wide, Interrupted: true},
			"tool call":       {Kind: transcript.ToolCall, Seq: 6, Tool: "python", Lead: rich, Text: longCode},
			"tool result":     {Kind: transcript.ToolResult, Seq: 7, Tool: "python", Text: output},
			"tool error":      {Kind: transcript.ToolResult, Seq: 8, Tool: "shell", Text: output, IsError: true},
			"no output":       {Kind: transcript.ToolResult, Seq: 9, Tool: "shell"},
			"notice":          item(transcript.Notice, 10, output),
			"checkpoint":      item(transcript.Checkpoint, 11, output),
			"failure":         {Kind: transcript.Failure, After: 11, Text: wide},
			"step limit":      {Kind: transcript.Failure, After: 12, Text: "step_limit"},
			"long tool name":  {Kind: transcript.ToolCall, Seq: 13, Tool: wide, Text: "x"},
			"unnamed result":  {Kind: transcript.ToolResult, Seq: 14, Text: "x"},
			"delegated task":  {Kind: transcript.ToolCall, Seq: 15, Tool: "delegate", Text: wide},
			"reply with code": agent(16, "```python\n"+longCode+"\n```"),
		}
		if long && o.name == "folded" {
			// A reply too long to read as Markdown is slow to wrap, and is
			// drawn the same way whatever the options are.
			kinds["long reply"] = agent(5, strings.Repeat(rich, 1+draw.MaxMarkdown/len(rich)))
		}
		for name, it := range kinds {
			add(name, r.Item(it))
		}
		add("pending", r.Pending(wide))
		add("short pending", r.Pending("hello"))
		add("live", r.Live(transcript.Live{Typing: true, Text: rich, Starting: wide}, 0))
		add("running", r.Live(transcript.Live{Running: wide}, 83*time.Second))
		add("day", r.Day(1760270400000))
		add("beginning", r.Beginning())
		add("local", r.Local(wide, []wire.Line{
			{{T: "connection ", Tone: "dim"}, {T: "live", Tone: "ok", Bold: true}},
			{},
			{{T: wide, Tone: "warn"}, {T: wide}},
		}))
	}
	return out
}

func TestNoLineIsWiderThanTheRow(t *testing.T) {
	t.Parallel()
	for _, l := range looks() {
		for _, w := range widths() {
			t.Run(l.name+"/"+strconv.Itoa(w), func(t *testing.T) {
				t.Parallel()
				for _, b := range everything(l, w, true) {
					fits(t, b.name, b.lines, w)
				}
			})
		}
	}
}

func TestNarrowRows(t *testing.T) {
	t.Parallel()
	// Nothing is too narrow to draw in: down to one cell, a line is cut
	// before it is wider than the row.
	for _, l := range looks() {
		for _, w := range []int{1, 2, 3, 4, 5, 6, 8, 10, 11, 16, 20, 24} {
			t.Run(l.name+"/"+strconv.Itoa(w), func(t *testing.T) {
				t.Parallel()
				for _, b := range everything(l, w, false) {
					fits(t, b.name, b.lines, w)
				}
			})
		}
	}
	// A width that makes no sense is one cell.
	for _, w := range []int{0, -5} {
		fits(t, "width "+strconv.Itoa(w), trueDark().renderer(w).Item(agent(1, rich)), 1)
	}
}

func TestGraphemeTerminal(t *testing.T) {
	t.Parallel()
	// A terminal that counts a cluster at a time keeps its joiners, and its
	// rows are measured its way.
	caps := trueDark().caps
	caps.Width = termcaps.WidthGrapheme
	r := draw.New(draw.Options{Theme: theme.New(caps, theme.Options{}), Width: 30, Location: time.UTC})
	const family = "👨\u200d👩\u200d👧"
	for _, it := range []*transcript.Item{item(transcript.User, 1, wide), agent(2, rich), item(transcript.Notice, 3, wide)} {
		lines := r.Item(it)
		for i, line := range lines {
			if got := ansi.StringWidth(line); got > 30 {
				t.Errorf("line %d is %d cells: %q", i, got, line)
			}
		}
		if !strings.Contains(strings.Join(lines, "\n"), family) {
			t.Errorf("the joined emoji was taken apart: %q", stripped(lines))
		}
	}
	if got := strings.Join(trueDark().renderer(30).Item(item(transcript.User, 1, wide)), "\n"); strings.Contains(got, "\u200d") {
		t.Errorf("a joiner is kept where cells are counted a character at a time: %q", got)
	}
}

func TestBothWaysOfCounting(t *testing.T) {
	t.Parallel()
	// Half a flag is one cell counted a character at a time and two counted a
	// cluster at a time, and no joiner is in it to drop. Nothing says which a
	// terminal draws, so a row has to fit by both.
	for name, text := range map[string]string{
		"half flags":            strings.Repeat("🇯 ", 90),
		"half flags, one word":  strings.Repeat("🇯a", 90),
		"three halves":          strings.Repeat("🇯🇵🇯 ", 60),
		"half flags among wide": strings.Repeat("中🇯", 60),
	} {
		for _, w := range []int{100, 40, 30, 20, 10} {
			mods := &rendered{byKind: map[string][]wire.Line{"tool:python": {{{T: text}}}}}
			pic := &images{shown: draw.Shown{Rows: []string{"▀"}, Caption: text}}
			r := trueDark().with(draw.Options{Width: w, Expanded: true, Mods: mods, Images: pic})
			blocks := map[string][]string{
				"user":        r.Item(item(transcript.User, 1, text)),
				"agent":       r.Item(agent(2, text+"\n\n> "+text+"\n\n- "+text+"\n\n![a](a.png)")),
				"code":        r.Item(agent(3, "```go\n"+text+"\n```\n\n```\n"+text+"\n```")),
				"call":        r.Item(&transcript.Item{Kind: transcript.ToolCall, Seq: 4, Tool: text, Lead: text, Text: text}),
				"result":      r.Item(&transcript.Item{Kind: transcript.ToolResult, Seq: 5, Tool: "shell", Text: text}),
				"mod result":  r.Item(&transcript.Item{Kind: transcript.ToolResult, Seq: 6, Tool: "python", Text: text}),
				"notice":      r.Item(item(transcript.Notice, 7, text)),
				"checkpoint":  r.Item(item(transcript.Checkpoint, 8, text)),
				"failure":     r.Item(&transcript.Item{Kind: transcript.Failure, Text: text}),
				"interrupted": r.Item(&transcript.Item{Kind: transcript.Agent, Seq: 9, Text: text, Interrupted: true}),
				"pending":     r.Pending(text),
				"live":        r.Live(transcript.Live{Text: text, Starting: text, Running: text}, time.Second),
				"local":       r.Local(text, []wire.Line{{{T: text, Tone: "warn"}}}),
			}
			for kind, lines := range blocks {
				fits(t, name+", "+kind+" at "+strconv.Itoa(w), lines, w)
			}
		}
	}
}

func TestUser(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		look  look
		width int
		item  *transcript.Item
		want  []string
	}{
		{
			"one line", trueDark(), 40, item(transcript.User, 1, "why did Thursday spike?"),
			[]string{"❯ why did Thursday spike?"},
		},
		{
			"a wrapped line stands under the text", trueDark(), 20, item(transcript.User, 1, "plot last week's latency by day"),
			[]string{"❯ plot last week's", "  latency by day"},
		},
		{
			"paragraphs keep their blank row", trueDark(), 40, item(transcript.User, 1, "\n\nfirst\n\n  second\n\n"),
			[]string{"❯ first", "", "    second"},
		},
		{
			"a sub-agent's task", trueDark(), 40, &transcript.Item{Kind: transcript.User, Task: true, Text: "count the rows"},
			[]string{"⇢ count the rows"},
		},
		{
			"a wider mark moves the text", noneASCII(), 12, &transcript.Item{Kind: transcript.User, Task: true, Text: "count the rows"},
			[]string{"-> count the", "   rows"},
		},
		{
			"nothing said", trueDark(), 40, item(transcript.User, 1, ""),
			[]string{"❯ "},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := c.look.renderer(c.width).Item(c.item)
			fits(t, c.name, got, c.width)
			equal(t, c.name, stripped(got), c.want)
		})
	}

	lines := trueDark().renderer(20).Item(item(transcript.User, 1, "plot last week's latency by day"))
	for _, c := range find(t, lines[1], "latency by day") {
		if c.fg != darkAccent || !c.bold {
			t.Fatalf("the second row of a message is drawn %+v, want the accent, bold", c)
		}
	}
}

func TestToolCall(t *testing.T) {
	t.Parallel()
	numbered := func(n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			b.WriteString("line " + strconv.Itoa(i) + "\n")
		}
		return b.String()
	}
	cases := []struct {
		name     string
		look     look
		width    int
		expanded bool
		item     *transcript.Item
		want     []string
	}{
		{
			"six rows are all shown", trueDark(), 60, false,
			&transcript.Item{Kind: transcript.ToolCall, Tool: "python", Text: numbered(6)},
			[]string{"⏺ python", "    line 1", "    line 2", "    line 3", "    line 4", "    line 5", "    line 6"},
		},
		{
			"the seventh is folded away", trueDark(), 60, false,
			&transcript.Item{Kind: transcript.ToolCall, Tool: "python", Text: numbered(7)},
			[]string{"⏺ python", "    line 1", "    line 2", "    line 3", "    line 4", "    line 5", "    line 6", "    … 1 more row (ctrl+o)"},
		},
		{
			"twelve more", trueDark(), 60, false,
			&transcript.Item{Kind: transcript.ToolCall, Tool: "python", Text: numbered(18)},
			[]string{"⏺ python", "    line 1", "    line 2", "    line 3", "    line 4", "    line 5", "    line 6", "    … 12 more rows (ctrl+o)"},
		},
		{
			// One line of 100 characters is three rows of 40 at 44 columns:
			// the fold counts rows of the screen, not lines of the code.
			"the fold counts rows of the screen", trueDark(), 44, false,
			&transcript.Item{Kind: transcript.ToolCall, Tool: "python", Text: strings.Repeat(strings.Repeat("x", 100)+"\n", 3)},
			[]string{
				"⏺ python",
				"    " + strings.Repeat("x", 40), "    " + strings.Repeat("x", 40), "    " + strings.Repeat("x", 20),
				"    " + strings.Repeat("x", 40), "    " + strings.Repeat("x", 40), "    " + strings.Repeat("x", 20),
				"    … 3 more rows (ctrl+o)",
			},
		},
		{
			"code is broken where the row is full, not at a space", trueDark(), 24, true,
			&transcript.Item{Kind: transcript.ToolCall, Tool: "python", Text: "print('aaaa bbbb cccc dddd eeee')"},
			[]string{"⏺ python", "    print('aaaa bbbb ccc", "    c dddd eeee')"},
		},
		{
			"expanded shows every row", trueDark(), 60, true,
			&transcript.Item{Kind: transcript.ToolCall, Tool: "python", Text: numbered(8)},
			[]string{"⏺ python", "    line 1", "    line 2", "    line 3", "    line 4", "    line 5", "    line 6", "    line 7", "    line 8"},
		},
		{
			"at 40 columns the code is still there", trueDark(), 40, false,
			&transcript.Item{Kind: transcript.ToolCall, Tool: "python", Text: "df.plot.bar()"},
			[]string{"⏺ python", "    df.plot.bar()"},
		},
		{
			"under 40 columns there is no code row", trueDark(), 39, false,
			&transcript.Item{Kind: transcript.ToolCall, Tool: "python", Text: numbered(18)},
			[]string{"⏺ python"},
		},
		{
			"under 40 columns ctrl+o still shows it", trueDark(), 39, true,
			&transcript.Item{Kind: transcript.ToolCall, Tool: "python", Text: numbered(2)},
			[]string{"⏺ python", "    line 1", "    line 2"},
		},
		{
			"what the agent said first", trueDark(), 60, false,
			&transcript.Item{Kind: transcript.ToolCall, Seq: 9, Tool: "python", Lead: "Let me **check**.", Text: "x = 1"},
			[]string{"  Let me check.", "", "⏺ python", "    x = 1"},
		},
		{
			"a tool with no name and no code", trueDark(), 60, false,
			&transcript.Item{Kind: transcript.ToolCall},
			[]string{"⏺ tool"},
		},
		{
			"ASCII", noneASCII(), 60, false,
			&transcript.Item{Kind: transcript.ToolCall, Tool: "python", Text: numbered(7)},
			[]string{"* python", "    line 1", "    line 2", "    line 3", "    line 4", "    line 5", "    line 6", "    ... 1 more row (ctrl+o)"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := c.look.with(draw.Options{Width: c.width, Expanded: c.expanded}).Item(c.item)
			fits(t, c.name, got, c.width)
			equal(t, c.name, stripped(got), c.want)
		})
	}
}

func TestToolResult(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		look     look
		width    int
		expanded bool
		item     *transcript.Item
		want     []string
	}{
		{
			"output hangs from the elbow", trueDark(), 60, false,
			&transcript.Item{Kind: transcript.ToolResult, Text: "   input  cached    ms\n7   3154    1536  5408\n"},
			[]string{"  ⎿    input  cached    ms", "    7   3154    1536  5408"},
		},
		{
			"four rows are all shown", trueDark(), 60, false,
			&transcript.Item{Kind: transcript.ToolResult, Text: "1\n2\n3\n4"},
			[]string{"  ⎿ 1", "    2", "    3", "    4"},
		},
		{
			"the fifth is folded away", trueDark(), 60, false,
			&transcript.Item{Kind: transcript.ToolResult, Text: "1\n2\n\n4\n5\n6\n7\n"},
			[]string{"  ⎿ 1", "    2", "", "    4", "    … 3 more rows (ctrl+o)"},
		},
		{
			// 90 characters at 44 columns are three rows of 40, 40 and 10.
			"the fold counts rows of the screen", trueDark(), 44, false,
			&transcript.Item{Kind: transcript.ToolResult, Text: strings.Repeat("a b ", 22) + "ab\nlast"},
			[]string{
				"  ⎿ " + strings.Repeat("a b ", 10), "    " + strings.Repeat("a b ", 10), "    a b a b ab", "    last",
			},
		},
		{
			"the fold counts wide characters by their cells", trueDark(), 26, false,
			&transcript.Item{Kind: transcript.ToolResult, Text: strings.Repeat("中", 45) + "\nlast"},
			[]string{
				"  ⎿ " + strings.Repeat("中", 11), "    " + strings.Repeat("中", 11), "    " + strings.Repeat("中", 11), "    " + strings.Repeat("中", 11),
				"    … 2 more rows (ctrl+o)",
			},
		},
		{
			"a row too narrow to name the key", trueDark(), 24, false,
			&transcript.Item{Kind: transcript.ToolResult, Text: "1\n2\n3\n4\n5\n6"},
			[]string{"  ⎿ 1", "    2", "    3", "    4", "    … 2 more rows"},
		},
		{
			"expanded shows every row", trueDark(), 60, true,
			&transcript.Item{Kind: transcript.ToolResult, Text: "1\n2\n3\n4\n5\n6"},
			[]string{"  ⎿ 1", "    2", "    3", "    4", "    5", "    6"},
		},
		{
			"under 40 columns the output is still shown", trueDark(), 30, false,
			&transcript.Item{Kind: transcript.ToolResult, Text: "saved latency.png"},
			[]string{"  ⎿ saved latency.png"},
		},
		{
			"no output", trueDark(), 60, false,
			&transcript.Item{Kind: transcript.ToolResult, Text: " \n\n"},
			[]string{"  ⎿ (no output)"},
		},
		{
			"a wider elbow moves the text", noneASCII(), 60, false,
			&transcript.Item{Kind: transcript.ToolResult, Text: "1\n2\n3\n4\n5"},
			[]string{"  |_ 1", "     2", "     3", "     4", "     ... 1 more row (ctrl+o)"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := c.look.with(draw.Options{Width: c.width, Expanded: c.expanded}).Item(c.item)
			fits(t, c.name, got, c.width)
			equal(t, c.name, stripped(got), c.want)
		})
	}

	r := trueDark().renderer(60)
	fine := r.Item(&transcript.Item{Kind: transcript.ToolResult, Text: "ok\nmore"})
	failed := r.Item(&transcript.Item{Kind: transcript.ToolResult, Text: "boom\nmore", IsError: true})
	for i, want := range []string{"⎿ ok", "more"} {
		for _, c := range find(t, fine[i], want) {
			if c.fg != darkDim {
				t.Fatalf("output is drawn %+v, want dim", c)
			}
		}
	}
	for i, want := range []string{"⎿ boom", "more"} {
		for _, c := range find(t, failed[i], want) {
			if c.fg != darkBad {
				t.Fatalf("a failed tool's output is drawn %+v, want the bad tone", c)
			}
		}
	}
}

func TestFoldedCountsWhatHardwrapMakes(t *testing.T) {
	t.Parallel()
	// The rows left out are counted without being made. The count has to be
	// the number the theme would make of them.
	th := trueDark().theme()
	texts := []string{
		"", "a", strings.Repeat("ab ", 40), "one\n\n\nfour", strings.Repeat("x", 40), strings.Repeat("x", 41),
		"  indented\n    more", strings.Repeat("中文 mixed ", 12), "tail space   \n   ", wide + "\n" + wide,
	}
	for _, text := range texts {
		for _, w := range []int{1, 2, 7, 40} {
			all := th.Hardwrap(text, w)
			for _, limit := range []int{0, 1, 3, 1000} {
				rows, hidden := trueDark().renderer(80).Folded(text, w, limit)
				want := all[:min(limit, len(all))]
				equal(t, "rows of "+strconv.Quote(text)+" at "+strconv.Itoa(w), rows, want)
				if hidden != len(all)-len(want) {
					t.Errorf("%q at %d, limit %d: %d rows left out, want %d", text, w, limit, hidden, len(all)-len(want))
				}
			}
			rows, hidden := trueDark().with(draw.Options{Width: 80, Expanded: true}).Folded(text, w, 2)
			equal(t, "expanded rows of "+strconv.Quote(text), rows, all)
			if hidden != 0 {
				t.Errorf("%q expanded: %d rows left out", text, hidden)
			}
		}
	}
}

func TestMarkedItems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		look     look
		width    int
		expanded bool
		item     *transcript.Item
		want     []string
		tone     string
	}{
		{
			"a notice", trueDark(), 60, false, item(transcript.Notice, 1, "sub-agent 7 settled\n"),
			[]string{"◆ sub-agent 7 settled"},
			darkWarn,
		},
		{
			"a notice that wraps", trueDark(), 20, false, item(transcript.Notice, 1, "sub-agent 7 settled with a result"),
			[]string{"◆ sub-agent 7", "  settled with a", "  result"},
			darkWarn,
		},
		{
			"a failure", trueDark(), 60, false, &transcript.Item{Kind: transcript.Failure, Text: "provider said no"},
			[]string{"✗ no reply: provider said no"},
			darkBad,
		},
		{
			"a failure the runtime has a code for", trueDark(), 40, false, &transcript.Item{Kind: transcript.Failure, Text: "step_limit"},
			[]string{"✗ no reply: the agent used every step it", "  is allowed in one turn"},
			darkBad,
		},
		{
			"an interrupted reply", trueDark(), 60, false, &transcript.Item{Kind: transcript.Agent, Seq: 1, Text: "So far", Interrupted: true},
			[]string{"  So far", "  ⏹ interrupted"},
			"",
		},
		{
			"a reply cut off before a word", trueDark(), 60, false, &transcript.Item{Kind: transcript.Agent, Seq: 1, Interrupted: true},
			[]string{"  ⏹ interrupted"},
			darkDim,
		},
		{
			"ASCII", noneASCII(), 60, false, &transcript.Item{Kind: transcript.Agent, Seq: 1, Text: "So far", Interrupted: true},
			[]string{"  So far", "  -- interrupted"},
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := c.look.with(draw.Options{Width: c.width, Expanded: c.expanded}).Item(c.item)
			fits(t, c.name, got, c.width)
			equal(t, c.name, stripped(got), c.want)
			if c.tone == "" {
				return
			}
			for _, line := range got {
				for _, cl := range find(t, line, strings.TrimSpace(ansi.Strip(line))) {
					if cl.fg != c.tone {
						t.Fatalf("drawn %+v, want %s", cl, c.tone)
					}
				}
			}
		})
	}
}

func TestCheckpoint(t *testing.T) {
	t.Parallel()
	it := item(transcript.Checkpoint, 5, "They spoke about latency.\n\nThe fix was to move the time note.")
	folded := trueDark().renderer(60).Item(it)
	fits(t, "folded", folded, 60)
	if len(folded) != 1 || !strings.Contains(ansi.Strip(folded[0]), "─ earlier conversation folded into a summary ─") || ansi.StringWidth(folded[0]) != 60 {
		t.Errorf("a folded checkpoint is %q, want one rule across the row", folded)
	}
	open := trueDark().with(draw.Options{Width: 60, Expanded: true}).Item(it)
	fits(t, "expanded", open, 60)
	equal(t, "an expanded checkpoint", stripped(open[1:]), []string{
		"  They spoke about latency.", "", "  The fix was to move the time note.",
	})
	if open[0] != folded[0] {
		t.Errorf("the rule changes when the checkpoint is expanded: %q", open[0])
	}
	for _, c := range find(t, open[1], "They spoke about latency.") {
		if c.fg != darkDim {
			t.Fatalf("the summary is drawn %+v, want dim", c)
		}
	}
}

func TestPending(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		look  look
		width int
		text  string
		want  []string
	}{
		{"the note follows the message", trueDark(), 40, "hello", []string{"❯ hello  sending…"}},
		{"it goes under a message that fills the row", trueDark(), 16, "hello to you", []string{"❯ hello to you", "  sending…"}},
		{"a wrapped message", trueDark(), 16, "hello to you all", []string{"❯ hello to you", "  all  sending…"}},
		{"ASCII", noneASCII(), 40, "hello", []string{"> hello  sending..."}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := c.look.renderer(c.width).Pending(c.text)
			fits(t, c.name, got, c.width)
			equal(t, c.name, stripped(got), c.want)
		})
	}
	got := trueDark().renderer(40).Pending("hello")
	for _, c := range find(t, got[0], "❯ hello") {
		if !c.faint || !c.italic {
			t.Fatalf("a pending message is drawn %+v, want faint italic", c)
		}
	}
	for _, c := range find(t, got[0], "sending…") {
		if c.fg != darkDim || c.italic {
			t.Fatalf("the note is drawn %+v, want dim", c)
		}
	}
}

func TestRules(t *testing.T) {
	t.Parallel()
	// 12 October 2025, 12:00 UTC is already the 13th in Auckland.
	const noon = 1760270400000
	auckland := time.FixedZone("NZDT", 13*3600)
	cases := []struct {
		name  string
		lines []string
		width int
		label string
	}{
		{"a day", trueDark().renderer(60).Day(noon), 60, " Sun 12 Oct 2025 "},
		{"a day somewhere else", draw.New(draw.Options{Theme: trueDark().theme(), Width: 60, Location: auckland}).Day(noon), 60, " Mon 13 Oct 2025 "},
		{"the beginning", trueDark().renderer(60).Beginning(), 60, " the beginning "},
		{"ASCII", noneASCII().renderer(46).Beginning(), 46, "- the beginning -"},
		{"a narrow day", trueDark().renderer(12).Day(noon), 12, " Sun 1… "},
	}
	for _, c := range cases {
		fits(t, c.name, c.lines, c.width)
		if len(c.lines) != 1 || ansi.StringWidth(c.lines[0]) != c.width || !strings.Contains(ansi.Strip(c.lines[0]), c.label) {
			t.Errorf("%s: %q, want one row of %d cells with %q", c.name, stripped(c.lines), c.width, c.label)
		}
	}
}

func TestLocal(t *testing.T) {
	t.Parallel()
	r := trueDark().renderer(30)
	got := r.Local("status", []wire.Line{
		{{T: "connection ", Tone: "dim"}, {T: "live", Tone: "ok", Bold: true}},
		{},
		{{T: "a line that is long enough to be wrapped", Tone: "warn"}},
		{{T: "two\nlines"}, {T: ""}, {T: "!", Tone: "no such tone"}},
	})
	fits(t, "local", got, 30)
	equal(t, "a command's output", stripped(got), []string{
		"status",
		"  connection live",
		"",
		"  a line that is long enough",
		"  to be wrapped",
		"  two lines!",
	})
	for _, c := range find(t, got[0], "status") {
		if c.fg != darkDim {
			t.Fatalf("the title is drawn %+v, want dim", c)
		}
	}
	for _, c := range find(t, got[1], "live") {
		if c.fg != "#7dc38f" || !c.bold {
			t.Fatalf("a span is drawn %+v, want its tone, bold", c)
		}
	}
	for _, c := range find(t, got[4], "to be wrapped") {
		if c.fg != darkWarn {
			t.Fatalf("a wrapped span is drawn %+v on its second row, want its tone", c)
		}
	}
	equal(t, "no title", stripped(r.Local("", []wire.Line{{{T: "one"}}})), []string{"  one"})
	if got := r.Local("", nil); len(got) != 0 {
		t.Errorf("nothing printed draws %q", got)
	}
	// In the ASCII glyph set a mod's text is ASCII too, as it is on the way
	// from a mod.
	equal(t, "ASCII", stripped(noneASCII().renderer(30).Local("café", []wire.Line{{{T: "naïve → done"}}})), []string{"caf?", "  na?ve ? done"})
}

func TestNothingToDraw(t *testing.T) {
	t.Parallel()
	r := trueDark().renderer(60)
	for name, it := range map[string]*transcript.Item{
		"no item":              nil,
		"a blank reply":        agent(1, " \n\n "),
		"an unknown kind":      {Kind: 99, Text: "x"},
		"a kind that is unset": {Text: "x"},
		"only a definition":    agent(2, "[r]: https://example.com\n"),
	} {
		if got := r.Item(it); len(got) != 0 {
			t.Errorf("%s draws %q", name, got)
		}
	}
	if got := r.Live(transcript.Live{Typing: true}, time.Second); len(got) != 0 {
		t.Errorf("a turn with nothing to show yet draws %q", got)
	}
}

// hostile is text that would do harm if a terminal were handed it: a colour
// change, a window title, a hyperlink, a C1 control sequence, a sixel image,
// a kitty image, a bidi override, a kitty image placeholder, a bell, a
// backspace and a carriage return.
const hostile = "a\x1b[31mred\x1b[0m b\x1b]0;title\x07 c\x1b]8;;x-evil:1\x07link\x1b]8;;\x07 d\u009b31m e\x1bPq#0~~\x1b\\ " +
	"f\x1b_Gf=100;AAAA\x1b\\ g\u202eh\U0010eeee\u0305 i\x07\x08\x7f j\rk\r\nl\tm\x9b\x90"

// ours matches the escape sequences the client writes itself: a style, and a
// hyperlink ended by BEL.
func ours() *regexp.Regexp {
	return regexp.MustCompile("\x1b\\[[0-9;]*m|\x1b\\]8;[^\x07\x1b]*\x07")
}

// inert fails the test when a line holds anything a terminal would act on
// besides the sequences the client writes.
func inert(t *testing.T, what string, lines []string) {
	t.Helper()
	for i, line := range lines {
		rest := ours().ReplaceAllString(line, "")
		for _, r := range rest {
			switch {
			case r < 0x20, r >= 0x7f && r <= 0x9f, r == 0x10eeee, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
				t.Errorf("%s: line %d holds %U: %q", what, i, r, line)
				return
			}
		}
	}
}

// harmless is inert for text that asks for no hyperlink in Markdown: a
// hyperlink in a line is then one the text smuggled in.
func harmless(t *testing.T, what string, lines []string) {
	t.Helper()
	inert(t, what, lines)
	for i, line := range lines {
		if strings.Contains(line, "\x1b]8;") {
			t.Errorf("%s: line %d holds a hyperlink the text asked for: %q", what, i, line)
		}
	}
}

func TestEscapeSequencesNeverReachALine(t *testing.T) {
	t.Parallel()
	for _, l := range looks() {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			pic := &images{shown: draw.Shown{Line: hostile, Hint: hostile}}
			mods := &rendered{byKind: map[string][]wire.Line{
				"code:go":         {{{T: hostile, Tone: hostile}}},
				"tool:" + hostile: {{{T: hostile}}},
			}}
			r := l.with(draw.Options{Width: 60, Expanded: true, Images: pic, Mods: mods})
			blocks := map[string][]string{
				"user":        r.Item(item(transcript.User, 1, hostile)),
				"agent":       r.Item(agent(2, hostile+"\n\n# "+hostile+"\n\n- "+hostile+"\n\n> "+hostile+"\n\n| "+hostile+" |\n| - |\n| "+hostile+" |\n")),
				"code":        r.Item(agent(3, "```"+hostile+"\n"+hostile+"\n```\n\n```go\n"+hostile+"\n```\n\n```python\n"+hostile+"\n```")),
				"image":       r.Item(agent(4, "![a"+hostile+"](pic"+"\U0010eeee\u202e"+".png)")),
				"long":        r.Item(agent(5, strings.Repeat(hostile+"\n", 1+draw.MaxMarkdown/len(hostile)))),
				"interrupted": r.Item(&transcript.Item{Kind: transcript.Agent, Seq: 6, Text: hostile, Interrupted: true}),
				"call":        r.Item(&transcript.Item{Kind: transcript.ToolCall, Seq: 7, Tool: hostile, Lead: hostile, Text: hostile}),
				"result":      r.Item(&transcript.Item{Kind: transcript.ToolResult, Seq: 8, Tool: "shell", Text: hostile}),
				"mod result":  r.Item(&transcript.Item{Kind: transcript.ToolResult, Seq: 9, Tool: hostile, Text: hostile}),
				"error":       r.Item(&transcript.Item{Kind: transcript.ToolResult, Seq: 10, Text: hostile, IsError: true}),
				"notice":      r.Item(item(transcript.Notice, 11, hostile)),
				"checkpoint":  r.Item(item(transcript.Checkpoint, 12, hostile)),
				"failure":     r.Item(&transcript.Item{Kind: transcript.Failure, Text: hostile}),
				"pending":     r.Pending(hostile),
				"live":        r.Live(transcript.Live{Text: hostile, Starting: hostile, Running: hostile}, time.Second),
				"local":       r.Local(hostile, []wire.Line{{{T: hostile, Tone: hostile}}}),
			}
			for name, lines := range blocks {
				if len(lines) == 0 {
					t.Errorf("%s draws nothing", name)
				}
				fits(t, name, lines, 60)
				harmless(t, name, lines)
				// The text is still there to read: only what a terminal would
				// act on is gone.
				if all := strings.Join(stripped(lines), ""); name != "image" && !strings.Contains(strings.ReplaceAll(all, " ", ""), "[31mred") {
					t.Errorf("%s lost the text around the escape sequences: %q", name, all)
				}
			}
			if l.caps.Color == termcaps.ColorNone {
				// Where the client writes no colour at all, any colour would
				// be the text's own.
				for name, lines := range blocks {
					for _, line := range lines {
						for _, c := range mustPaint(t, line) {
							if c.fg != "" || c.bg != "" {
								t.Fatalf("%s is drawn in a colour: %+v", name, c)
							}
						}
					}
				}
			}
		})
	}
}

// mustPaint is paint without the state the line ends in.
func mustPaint(t *testing.T, line string) []cell {
	t.Helper()
	cells, _ := paint(t, line)
	return cells
}

func TestDateRange(t *testing.T) {
	t.Parallel()
	at := func(s string) int64 {
		when, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return when.UnixMilli()
	}
	tokyo := time.FixedZone("JST", 9*3600)
	cases := []struct {
		name        string
		first, last int64
		loc         *time.Location
		dash, want  string
	}{
		{"several days", at("2026-10-07T09:00:00Z"), at("2026-10-12T12:06:00Z"), time.UTC, "–", "7 Oct – 12 Oct"},
		{"one day", at("2026-10-12T00:00:00Z"), at("2026-10-12T23:59:59Z"), time.UTC, "–", "12 Oct"},
		{"one moment", at("2026-10-12T12:00:00Z"), at("2026-10-12T12:00:00Z"), time.UTC, "–", "12 Oct"},
		{"the ASCII dash", at("2026-10-07T09:00:00Z"), at("2026-10-12T12:06:00Z"), time.UTC, "-", "7 Oct - 12 Oct"},
		{"given backwards", at("2026-10-12T12:06:00Z"), at("2026-10-07T09:00:00Z"), time.UTC, "–", "7 Oct – 12 Oct"},
		{"the day is the reader's", at("2026-10-11T16:00:00Z"), at("2026-10-12T14:00:00Z"), tokyo, "–", "12 Oct"},
		{"and so are two days", at("2026-10-12T14:00:00Z"), at("2026-10-12T16:00:00Z"), tokyo, "–", "12 Oct – 13 Oct"},
		{"across a month", at("2026-09-30T09:00:00Z"), at("2026-10-01T09:00:00Z"), time.UTC, "–", "30 Sep – 1 Oct"},
		{"across a year", at("2025-12-31T09:00:00Z"), at("2026-01-02T09:00:00Z"), time.UTC, "–", "31 Dec 2025 – 2 Jan 2026"},
		{"the same day of another year", at("2025-10-12T09:00:00Z"), at("2026-10-12T09:00:00Z"), time.UTC, "–", "12 Oct 2025 – 12 Oct 2026"},
		{"no first", 0, at("2026-10-12T12:06:00Z"), time.UTC, "–", "12 Oct"},
		{"no last", at("2026-10-07T09:00:00Z"), 0, time.UTC, "–", "7 Oct"},
		{"nothing in view", 0, 0, time.UTC, "–", ""},
	}
	for _, c := range cases {
		if got := draw.DateRange(c.first, c.last, c.loc, c.dash); got != c.want {
			t.Errorf("%s: DateRange = %q, want %q", c.name, got, c.want)
		}
	}
	// A nil location is the local one, whatever that is here.
	first, last := at("2026-10-07T09:00:00Z"), at("2026-10-12T12:06:00Z")
	if got, want := draw.DateRange(first, last, nil, "–"), draw.DateRange(first, last, time.Local, "–"); got != want {
		t.Errorf("DateRange with no location = %q, want %q", got, want)
	}
}
