package draw_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/draw"
	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// fenced writes a fenced code block.
func fenced(lang, code string) string { return "```" + lang + "\n" + code + "\n```\n" }

// codeRows returns the rows of the one code block in a reply's lines, without
// the reply's indent, the cell of space each row starts with, and whatever
// the background was padded with. The language row is left out.
func codeRows(t *testing.T, lines []string, lang string) []string {
	t.Helper()
	plain := stripped(lines)
	start := 0
	if lang != "" {
		if len(plain) == 0 || strings.TrimSpace(plain[0]) != lang {
			t.Fatalf("the block does not start with its language %q: %q", lang, plain)
		}
		start = 1
	}
	rows := make([]string, 0, len(plain))
	for _, row := range plain[start:] {
		if row != "" && !strings.HasPrefix(row, "   ") {
			t.Fatalf("a code row is not three cells in: %q", row)
		}
		rows = append(rows, strings.TrimRight(strings.TrimPrefix(row, "   "), " "))
	}
	return rows
}

func TestCodeIsNeverWordWrapped(t *testing.T) {
	t.Parallel()
	// A line of words, so that there is a space to break at before every
	// place the row is full.
	line := `message := "` + strings.Repeat("alpha beta gamma delta ", 9) + `end"`
	for _, l := range looks() {
		for _, w := range widths() {
			t.Run(l.name+"/"+strconv.Itoa(w), func(t *testing.T) {
				t.Parallel()
				lines := l.renderer(w).Item(agent(1, fenced("go", line+"\nshort := 1")))
				fits(t, "code", lines, w)
				rows := codeRows(t, lines, "go")
				// The block takes the whole width, since the line needs it:
				// the indent, and a cell of space at each side. Every row
				// but the last is full, whether or not a space was near.
				room := w - 4
				var want []string
				for rest := line; rest != ""; rest = rest[min(room, len(rest)):] {
					want = append(want, strings.TrimRight(rest[:min(room, len(rest))], " "))
				}
				equal(t, "the rows of the long line", rows[:len(rows)-1], want)
				if rows[len(rows)-1] != "short := 1" {
					t.Errorf("the next line of code is %q", rows[len(rows)-1])
				}
			})
		}
	}
}

func TestCodeFill(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 70)
	cases := []struct {
		name  string
		look  look
		width int
		code  string
		// fill is the width of the background, and colour what it is.
		fill   int
		colour string
	}{
		{"60 columns", trueDark(), 100, "x := 1\n\ny := 2", 60, darkCodeFill},
		{"60 columns exactly fit a line of 58", trueDark(), 100, strings.Repeat("x", 58), 60, darkCodeFill},
		{"a longer line takes the width", trueDark(), 100, strings.Repeat("x", 59), 98, darkCodeFill},
		{"the width, where a line needs more than it", trueDark(), 100, strings.Repeat("x", 300), 98, darkCodeFill},
		{"the width at 62 columns", trueDark(), 62, long, 60, darkCodeFill},
		{"the width at 60 columns", trueDark(), 60, "x := 1", 58, darkCodeFill},
		{"the width at 40 columns", trueDark(), 40, long, 38, darkCodeFill},
		{"the width at 30 columns", trueDark(), 30, "x := 1", 28, darkCodeFill},
		{"wide characters", trueDark(), 100, strings.Repeat("中", 40), 98, darkCodeFill},
		{"256 colours", look{"256-dark", termcaps.Caps{Color: termcaps.Color256, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc}}, 100, "x := 1", 60, "256:235"},
		{"ASCII glyphs keep the background", look{"ascii-dark", termcaps.Caps{Color: termcaps.ColorTrue, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsASCII, Width: termcaps.WidthWc}}, 100, "x := 1", 60, darkCodeFill},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			lines := c.look.renderer(c.width).Item(agent(1, fenced("go", c.code)))
			fits(t, c.name, lines, c.width)
			if len(lines) < 2 {
				t.Fatalf("drawn as %q", lines)
			}
			for i, line := range lines {
				cells := mustPaint(t, line)
				if got := ansi.StringWidth(line); got != 2+c.fill {
					t.Fatalf("row %d is %d cells, want the indent and %d: %q", i, got, c.fill, line)
				}
				for j, cl := range cells {
					want := c.colour
					if j < 2 {
						want = ""
					}
					if cl.bg != want {
						t.Fatalf("row %d, character %d has background %q, want %q: %q", i, j, cl.bg, want, line)
					}
				}
			}
		})
	}

	// Where the theme has no code background a row is only as long as its
	// code, and a blank one is empty.
	for _, l := range []look{light16(), unknown256(), noneASCII()} {
		lines := l.renderer(100).Item(agent(1, fenced("go", "x := 1\n\ny := 2")))
		fits(t, l.name, lines, 100)
		equal(t, l.name, stripped(lines), []string{"   go", "   x := 1", "", "   y := 2"})
		for i, line := range lines {
			for _, cl := range mustPaint(t, line) {
				if cl.bg != "" || (i > 0 && cl.fg != "") {
					t.Fatalf("%s: code is drawn in a colour: %+v", l.name, cl)
				}
			}
		}
	}
}

func TestCodeRows(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		look look
		src  string
		want []string
	}{
		{"a language row, then the code", trueDark(), fenced("go", "x := 1"), []string{"go", "x := 1"}},
		{"no language, no row for it", trueDark(), fenced("", "x := 1"), []string{"x := 1"}},
		{"indentation is kept", trueDark(), fenced("go", "if x {\n\treturn\n}"), []string{"go", "if x {", "    return", "}"}},
		{"a blank row inside stays", light16(), fenced("", "a\n\n\nb"), []string{"a", "", "", "b"}},
		{"blank rows at the ends go", trueDark(), fenced("go", "\n\nx := 1\n\n"), []string{"go", "x := 1"}},
		{"an empty block is its language", trueDark(), fenced("go", ""), []string{"go"}},
		{"tildes in the code do not end it", trueDark(), fenced("go", "a\n~~~\nb\n~~~~~\nc"), []string{"go", "a", "~~~", "b", "~~~~~", "c"}},
		{"backquotes in the code do not end it", trueDark(), "````md\n```go\nx\n```\n````\n", []string{"md", "```go", "x", "```"}},
		{"an unknown language is still drawn", trueDark(), fenced("no-such-language", "x := 1"), []string{"no-such-language", "x := 1"}},
		{"the rest of the fence line is not the language", trueDark(), fenced("go title=main.go", "x := 1"), []string{"go", "x := 1"}},
		{"a fence that never closes", trueDark(), "```go\nx := 1", []string{"go", "x := 1"}},
		// The highlighter knows a language by a file name too, and the block
		// is fenced with tildes to be highlighted.
		{"a language that begins like a fence", trueDark(), "```~~~~main.go\nx := 1\n```\n", []string{"~~~~main.go", "x := 1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			lines := c.look.renderer(100).Item(agent(1, c.src))
			fits(t, c.name, lines, 100)
			got := make([]string, len(lines))
			for i, row := range stripped(lines) {
				got[i] = strings.TrimSpace(strings.TrimPrefix(row, "   ")) // the block's own left edge
				if row != "" {
					got[i] = strings.TrimRight(strings.TrimPrefix(row, "   "), " ")
				}
			}
			equal(t, c.name, got, c.want)
		})
	}
}

func TestCodeIsHighlighted(t *testing.T) {
	t.Parallel()
	src := fenced("go", "// a comment\nfunc main() { x := \"text\" }")
	// With 256 colours or more on a known background the words of a
	// language have colours of their own.
	for _, c := range []termcaps.Color{termcaps.ColorTrue, termcaps.Color256} {
		for _, bg := range []termcaps.Background{termcaps.BgDark, termcaps.BgLight} {
			l := look{"", termcaps.Caps{Color: c, Background: bg, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc}}
			lines := l.renderer(100).Item(agent(1, src))
			comment, word, str := find(t, lines[1], "// a comment")[0], find(t, lines[2], "func")[0], find(t, lines[2], `"text"`)[0]
			if comment.fg == "" || word.fg == "" || str.fg == "" || comment.fg == word.fg || word.fg == str.fg {
				t.Errorf("%v on %v: a comment, a keyword and a string are drawn %q, %q and %q", c, bg, comment.fg, word.fg, str.fg)
			}
			if truecolor := strings.HasPrefix(word.fg, "#"); truecolor != (c == termcaps.ColorTrue) {
				t.Errorf("%v: a keyword is drawn %q", c, word.fg)
			}
		}
	}
	// Dark and light do not share their colours, and the second does not
	// keep the first's: each Renderer asks for its own.
	dark := trueDark().renderer(100).Item(agent(1, src))
	lightLook := look{"", termcaps.Caps{Color: termcaps.ColorTrue, Background: termcaps.BgLight, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc}}
	light := lightLook.renderer(100).Item(agent(1, src))
	again := trueDark().renderer(100).Item(agent(1, src))
	if find(t, dark[2], "func")[0].fg == find(t, light[2], "func")[0].fg {
		t.Error("a keyword has one colour on dark and on light")
	}
	equal(t, "dark, after light was drawn", again, dark)

	// A block that names no language, or one the highlighter does not have,
	// is drawn plain, on the code background.
	for _, lang := range []string{"", "no-such-language"} {
		r := trueDark().renderer(100)
		lines := r.Item(agent(1, fenced(lang, "func main() { x := \"text\" }")))
		for _, cl := range find(t, lines[len(lines)-1], "func main()") {
			if cl.fg != "" || cl.bold || cl.bg != darkCodeFill {
				t.Fatalf("language %q: code is drawn %+v, want plain on the code background", lang, cl)
			}
		}
		if _, markdowns, _ := r.Counts(); markdowns != 0 {
			t.Errorf("language %q: the Markdown renderer was called %d times", lang, markdowns)
		}
	}
	// A language is known by its other names too, in any case.
	for _, lang := range []string{"golang", "Python", "JS", "sh"} {
		lines := trueDark().renderer(100).Item(agent(1, fenced(lang, "x = \"text\"")))
		if c := find(t, lines[1], `"text"`)[0]; c.fg == "" {
			t.Errorf("language %q is not highlighted: %+v", lang, c)
		}
	}

	// Nowhere else: 16 colours come out wrong, and with an unknown background
	// no set of colours can be chosen. Nor in the ASCII glyph set.
	plain := []look{
		light16(), unknown256(), noneASCII(),
		{"", termcaps.Caps{Color: termcaps.ColorTrue, Background: termcaps.BgUnknown, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc}},
		{"", termcaps.Caps{Color: termcaps.ColorTrue, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsASCII, Width: termcaps.WidthWc}},
	}
	for _, l := range plain {
		r := l.renderer(100)
		lines := r.Item(agent(1, src))
		for _, line := range lines[1:] {
			for _, cl := range mustPaint(t, line) {
				if cl.fg != "" || cl.bold || cl.italic {
					t.Fatalf("%+v: code is highlighted: %+v", l.caps, cl)
				}
			}
		}
		if _, markdowns, _ := r.Counts(); markdowns != 0 {
			t.Errorf("%+v: the Markdown renderer was called %d times for a code block that is drawn plain", l.caps, markdowns)
		}
	}
}

func TestCodeContinuationRowsKeepTheirColour(t *testing.T) {
	t.Parallel()
	words := strings.Repeat("words of a comment ", 12)
	// chunks are the rows a line is broken into at 60 columns: the indent
	// and a cell at each side leave 56. Each is trimmed, to be looked for.
	chunks := func(line string) []string {
		var out []string
		for ; line != ""; line = line[min(56, len(line)):] {
			out = append(out, strings.TrimSpace(line[:min(56, len(line))]))
		}
		return out
	}
	cases := []struct {
		name string
		src  string
		// rows are the rows of the block after the language: of each, a
		// piece that belongs to the one coloured thing.
		rows []string
	}{
		{
			"a comment broken by the width", fenced("go", "// "+words),
			chunks("// " + words),
		},
		{
			"a comment that runs over lines of the code", fenced("go", "/* first\nsecond\nthird "+words+"*/"),
			append([]string{"/* first", "second"}, chunks("third "+words+"*/")...),
		},
		{
			"a raw string over lines", fenced("go", "x := `first\nsecond and\nthird and fourth`"),
			[]string{"`first", "second and", "third and fourth`"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			lines := trueDark().renderer(60).Item(agent(1, c.src))
			fits(t, c.name, lines, 60)
			if len(lines) != len(c.rows)+1 {
				t.Fatalf("the block is %d rows, want %d: %q", len(lines), len(c.rows)+1, stripped(lines))
			}
			first := find(t, lines[1], c.rows[0])[0]
			if first.fg == "" {
				t.Fatalf("the first row has no colour: %+v", first)
			}
			for i, part := range c.rows {
				for _, got := range find(t, lines[i+1], part) {
					if got.fg != first.fg || got.italic != first.italic || got.bg != darkCodeFill {
						t.Fatalf("row %d is drawn %+v, want what the first row has: %+v", i+1, got, first)
					}
				}
			}
		})
	}
}

func TestASCIIDrawsRepliesAsWritten(t *testing.T) {
	t.Parallel()
	const reply = "# Title\n\nSome *words* and `code` and a [link](https://example.com/x).\n\n" +
		"- one\n- two\n\n> quote\n\n| a | b |\n| - | - |\n| 1 | 2 |\n\n" +
		"```go\nx := 1 // then\n```\n\n![chart](latency.png)\n\nAn image ![beside](other.png) words, and [a reference][r].\n\n[r]: https://example.com/r\n"
	asciiColour := look{"ascii-truecolor", termcaps.Caps{
		Color: termcaps.ColorTrue, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsASCII, Width: termcaps.WidthWc,
	}}
	for _, l := range []look{noneASCII(), asciiColour} {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			pic := &images{shown: draw.Shown{Line: "[image: latency.png, 640x360, 692 kB]", Hint: "/image save 3 ./latency.png"}}
			mods := &rendered{byKind: map[string][]wire.Line{"tool:python": {{{T: "from a mod", Tone: "mod"}}}}}
			r := l.with(draw.Options{Width: 60, Expanded: true, Images: pic, Mods: mods})
			call := &transcript.Item{Kind: transcript.ToolCall, Seq: 4, Tool: "python", Lead: reply, Text: "print(1)\nprint(2)"}
			blocks := map[string][]string{
				"user":        r.Item(item(transcript.User, 1, "plot last week's latency")),
				"task":        r.Item(&transcript.Item{Kind: transcript.User, Seq: 2, Task: true, Text: "count the rows"}),
				"agent":       r.Item(agent(3, reply)),
				"call":        r.Item(call),
				"result":      r.Item(&transcript.Item{Kind: transcript.ToolResult, Seq: 5, Tool: "shell", Text: "1\n2\n3\n4\n5\n6"}),
				"mod result":  r.Item(&transcript.Item{Kind: transcript.ToolResult, Seq: 6, Tool: "python", Text: "1"}),
				"error":       r.Item(&transcript.Item{Kind: transcript.ToolResult, Seq: 7, Text: "boom", IsError: true}),
				"notice":      r.Item(item(transcript.Notice, 8, "sub-agent 7 settled")),
				"checkpoint":  r.Item(item(transcript.Checkpoint, 9, "They spoke about latency.")),
				"failure":     r.Item(&transcript.Item{Kind: transcript.Failure, Text: "step_limit"}),
				"interrupted": r.Item(&transcript.Item{Kind: transcript.Agent, Seq: 10, Text: "So far", Interrupted: true}),
				"long":        r.Item(agent(11, strings.Repeat("word ", 1+draw.MaxMarkdown/5))),
				"pending":     r.Pending("hello"),
				"live":        r.Live(transcript.Live{Text: reply, Starting: "python", Running: "python"}, 0),
				"day":         r.Day(1760270400000),
				"beginning":   r.Beginning(),
				"local":       r.Local("status", []wire.Line{{{T: "connection ", Tone: "dim"}, {T: "live", Tone: "ok"}}}),
			}
			for name, lines := range blocks {
				if len(lines) == 0 {
					t.Errorf("%s draws nothing", name)
				}
				fits(t, name, lines, 60)
				for _, line := range lines {
					for i := range len(line) {
						if line[i] >= 0x80 {
							t.Fatalf("%s holds byte %#x: %q", name, line[i], line)
						}
					}
				}
			}
			if _, markdowns, _ := r.Counts(); markdowns != 0 {
				t.Errorf("the Markdown renderer was called %d times", markdowns)
			}

			// The text is as it was written, and the parts that are not text
			// are still drawn as what they are.
			got := stripped(blocks["agent"])
			for i, row := range got {
				got[i] = strings.TrimRight(row, " ")
			}
			equal(t, "the reply", got, []string{
				"  # Title",
				"",
				"  Some *words* and `code` and a",
				"  [link](https://example.com/x).",
				"",
				"  - one",
				"  - two",
				"",
				"  > quote",
				"",
				"  | a | b |",
				"  | - | - |",
				"  | 1 | 2 |",
				"",
				"   go",
				"   x := 1 // then",
				"",
				"  [image: latency.png, 640x360, 692 kB]",
				"  /image save 3 ./latency.png",
				"",
				"  An image ![beside](other.png) words, and [a reference][r].",
				"",
				"  [r]: https://example.com/r",
				"",
				"  [image: latency.png, 640x360, 692 kB]",
				"  /image save 3 ./latency.png",
			})
		})
	}
}

func TestReplyKeepsLineBreaksWhenStreamingFinishes(t *testing.T) {
	t.Parallel()
	for _, l := range looks() {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			r := l.renderer(80)
			src := "ateb ahpla\n🚀 éfac\nりわ終"
			live := stripped(r.Live(transcript.Live{Text: src}, 0))
			finished := stripped(r.Item(agent(1, src)))
			equal(t, "finished reply keeps the streamed rows", finished, live)
			if len(finished) != 3 {
				t.Fatalf("three source lines became %d rows: %q", len(finished), finished)
			}
		})
	}
}

func TestMarkdownIsRendered(t *testing.T) {
	t.Parallel()
	r := trueDark().renderer(60)
	lines := r.Item(agent(1, "# Title\n\nSome *words* that are **bold enough to go on past the end of the row** and `code`.\n\n"+
		"- one\n- two\n\n\n\n> quote\n\nSee [notes](notes/today.md)."))
	fits(t, "markdown", lines, 60)
	equal(t, "a reply", stripped(lines), []string{
		"  # Title",
		"",
		"  Some words that are bold enough to go on past the end of",
		"  the row and code.",
		"",
		"  • one",
		"  • two",
		"",
		"  │ quote",
		"",
		// A relative address is shown as it was written, not from the root.
		"  See notes notes/today.md.",
	})
	for _, c := range find(t, lines[0], "# Title") {
		if c.fg != darkAccent || !c.bold {
			t.Fatalf("a heading is drawn %+v, want the accent, bold", c)
		}
	}
	// A style that runs over a line break is switched on again on the next
	// row, so that the row can be drawn without the one above it.
	for _, row := range []struct {
		line int
		text string
	}{{2, "bold enough to go on past the end of"}, {3, "the row"}} {
		for _, c := range find(t, lines[row.line], row.text) {
			if !c.bold {
				t.Fatalf("%q is drawn %+v, want bold", row.text, c)
			}
		}
	}
	if c := find(t, lines[3], "and")[0]; c.bold {
		t.Errorf("the bold runs on past its end: %+v", c)
	}
	if c := find(t, lines[10], "notes")[0]; c.link != "notes/today.md" {
		t.Errorf("the link is drawn %+v", c)
	}
	if _, markdowns, _ := r.Counts(); markdowns != 1 {
		t.Errorf("the Markdown renderer was called %d times for one piece of text", markdowns)
	}

	// The renderer leaves two blank rows after a nested list. One is drawn.
	nested := trueDark().renderer(60).Item(agent(2, "- one\n  - nested\n\n> quote"))
	equal(t, "a quote after a nested list", stripped(nested), []string{"  • one", "    • nested", "", "  │ quote"})
}

func TestQuotesKeepTheirBar(t *testing.T) {
	t.Parallel()
	// Every row of a quote starts with its bar, however the words fall: the
	// renderer must be told how wide the bar is, or it lays a row out a cell
	// too wide, breaks it again and leaves a word on a row with no bar.
	words := strings.TrimSpace(strings.Repeat("ab cde f ghij klm no pqrst uv w xyz ", 8))
	cases := []struct{ name, src, bar string }{
		{"a quote", "> " + words + "\n>\n> " + words, "  │"},
		{"a quote in a quote", "> > " + words, "  │ │"},
		{"a list in a quote", "> - " + words + "\n> - " + words, "  │"},
		{"styled words", "> **" + words + "** and *" + words + "*", "  │"},
	}
	// A quote in the middle of a reply, and one whose own first row is a
	// bar and nothing else, lose nothing.
	equal(t, "a quote among paragraphs", stripped(trueDark().renderer(40).Item(agent(1, "Before.\n\n> - one\n> - two\n\nBetween.\n\n> a\n>\n> > b\n\nAfter.\n\n│\n\n> │"))), []string{
		"  Before.", "", "  │ • one", "  │ • two", "", "  Between.", "", "  │ a", "  │", "  │ │ b", "", "  After.", "", "  │", "", "  │ │",
	})
	for _, c := range cases {
		for _, l := range []look{trueDark(), light16(), unknown256()} {
			// Every width near the ones tested elsewhere: a row is a cell too
			// wide only when its last word ends on the last cell.
			for w := 28; w <= 104; w++ {
				lines := l.renderer(w).Item(agent(1, c.src))
				fits(t, c.name, lines, w)
				for i, row := range stripped(lines) {
					if !strings.HasPrefix(row, c.bar) {
						t.Fatalf("%s, %s at %d columns: row %d has no bar: %q", c.name, l.name, w, i, stripped(lines))
					}
				}
				// Every word is still there, and the first row is not empty.
				said := strings.Fields(strings.NewReplacer("│", " ", "•", " ").Replace(strings.Join(stripped(lines), " ")))
				if want := strings.Fields(strings.NewReplacer(">", " ", "*", " ", "- ", " ").Replace(c.src)); strings.Join(said, " ") != strings.Join(want, " ") {
					t.Fatalf("%s, %s at %d columns: drawn as %q", c.name, l.name, w, stripped(lines))
				}
				if first := stripped(lines)[0]; strings.TrimSpace(strings.ReplaceAll(first, "│", "")) == "" {
					t.Fatalf("%s, %s at %d columns: the first row is empty: %q", c.name, l.name, w, stripped(lines))
				}
			}
		}
	}
}

func TestThemeChange(t *testing.T) {
	t.Parallel()
	// A Renderer made for another theme draws what one made from nothing
	// draws: nothing of the theme before is kept, in the Renderer or in the
	// libraries under it.
	mk := func(name string, c termcaps.Color, bg termcaps.Background) look {
		return look{name, termcaps.Caps{Color: c, Background: bg, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc}}
	}
	all := []look{
		trueDark(), mk("truecolor-light", termcaps.ColorTrue, termcaps.BgLight), mk("256-dark", termcaps.Color256, termcaps.BgDark),
		mk("256-light", termcaps.Color256, termcaps.BgLight), light16(), unknown256(), noneASCII(),
	}
	items := []*transcript.Item{
		agent(1, rich),
		{Kind: transcript.ToolCall, Seq: 2, Tool: "python", Lead: rich, Text: "x = 1"},
		agent(3, "```python\ndef f(x):\n    return \"a\"  # c\n```\n\n- a\n\n  ```go\n  var x = 1 // c\n  ```\n"),
		item(transcript.User, 4, wide),
	}
	drawAll := func(r *draw.Renderer) []string {
		out := make([]string, 0, 256)
		for _, it := range items {
			out = append(out, r.Item(it)...)
		}
		return out
	}
	fresh := make(map[string][]string)
	for _, l := range all {
		fresh[l.name] = drawAll(draw.New(draw.Options{Theme: l.theme(), Width: 80}))
	}
	if strings.Join(fresh["truecolor-dark"], "\n") == strings.Join(fresh["truecolor-light"], "\n") {
		t.Fatal("dark and light are drawn alike")
	}
	// Dark, then light, then dark again, and every other order of two.
	var r *draw.Renderer
	for _, i := range []int{0, 1, 0, 2, 3, 2, 0, 4, 5, 6, 1, 6, 0, 3, 1, 5, 2, 4, 0} {
		l := all[i]
		o := draw.Options{Theme: l.theme(), Width: 80}
		if r == nil {
			r = draw.New(o)
		} else {
			r = r.With(o)
		}
		equal(t, l.name+" after another theme", drawAll(r), fresh[l.name])
	}
}

func TestMarkdownIsWrappedByTheThemesMeasure(t *testing.T) {
	t.Parallel()
	// The Markdown renderer counts a thumbs-up with a skin tone as two
	// cells; a terminal that counts a character at a time shows four. A row
	// the renderer thinks is full is twice too long, and has to be broken
	// again, not cut.
	const thumbs = 40
	lines := trueDark().renderer(30).Item(agent(1, strings.TrimSpace(strings.Repeat("👍🏽 ", thumbs))))
	fits(t, "emoji", lines, 30)
	all := strings.Join(stripped(lines), "\n")
	if got := strings.Count(all, "👍🏽"); got != thumbs {
		t.Errorf("%d of %d are drawn: %q", got, thumbs, all)
	}
	if strings.Contains(all, "…") {
		t.Errorf("a row was cut: %q", all)
	}
}

func TestLinkOverALineBreak(t *testing.T) {
	t.Parallel()
	// A link whose address is longer than a row. Each row has to switch the
	// link off, and the next has to switch it on again.
	const address = "https://example.com/a/rather/long/path/that/does/not/fit/on/one/row/of/thirty/columns"
	lines := trueDark().renderer(30).Item(agent(1, "Read [this]("+address+") first."))
	fits(t, "link", lines, 30)
	linked := 0
	var shown strings.Builder
	for _, line := range lines {
		for _, c := range mustPaint(t, line) {
			if c.link == "" {
				continue
			}
			if c.link != address {
				t.Fatalf("a character links to %q", c.link)
			}
			shown.WriteString(c.text)
		}
		if strings.Contains(line, "\x1b]8;") {
			linked++
		}
	}
	if linked < 3 {
		t.Errorf("the link is on %d rows, want it broken over several: %q", linked, lines)
	}
	if got := shown.String(); got != "this"+address {
		t.Errorf("the linked text is %q", got)
	}
}

func TestImages(t *testing.T) {
	t.Parallel()
	picture := []string{"\x1b[38;5;7m\U0010eeee\u0305\u0305\U0010eeee\u0305\u030d\x1b[39m", "\x1b[38;5;7m\U0010eeee\u030d\u0305\U0010eeee\u030d\u030d\x1b[39m"}
	const reply = "Here it is:\n\n![a chart of SECRETALT](charts/latency.png \"SECRETTITLE\")\n\nDone."
	cases := []struct {
		name  string
		look  look
		width int
		src   draw.ImageSource
		want  []string
	}{
		{
			"rows and a caption", trueDark(), 60,
			&images{shown: draw.Shown{Rows: picture, Caption: "latency.png · 640×360 · 692 kB · /image save 31"}},
			[]string{"  \U0010eeee\u0305\u0305\U0010eeee\u0305\u030d", "  \U0010eeee\u030d\u0305\U0010eeee\u030d\u030d", "  latency.png · 640×360 · 692 kB · /image save 31"},
		},
		{
			"a line and how to save it", trueDark(), 60,
			&images{shown: draw.Shown{Line: "[image: latency.png, 640×360, 692 kB]", Hint: "/image save 31 ./latency.png"}},
			[]string{"  [image: latency.png, 640×360, 692 kB]", "  /image save 31 ./latency.png"},
		},
		{
			"a line alone", trueDark(), 60,
			&images{shown: draw.Shown{Line: "[image: latency.png, not found]"}},
			[]string{"  [image: latency.png, not found]"},
		},
		{
			"a caption too long for the row", trueDark(), 30,
			&images{shown: draw.Shown{Rows: picture, Caption: "latency.png · 640×360 · 692 kB · /image save 31"}},
			[]string{"  \U0010eeee\u0305\u0305\U0010eeee\u0305\u030d", "  \U0010eeee\u030d\u0305\U0010eeee\u030d\u030d", "  latency.png · 640×360 · 692", "  kB · /image save 31"},
		},
		{
			"a source with nothing to say", trueDark(), 60,
			&images{},
			[]string{"  [image: latency.png]"},
		},
		{
			"no source", trueDark(), 60, nil,
			[]string{"  [image: latency.png]"},
		},
		{
			"no source, ASCII", noneASCII(), 60, nil,
			[]string{"  [image: latency.png]"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			lines := c.look.with(draw.Options{Width: c.width, Images: c.src}).Item(agent(31, reply))
			fits(t, c.name, lines, c.width)
			got := stripped(lines)
			if got[0] != "  Here it is:" || got[1] != "" || got[len(got)-2] != "" || got[len(got)-1] != "  Done." {
				t.Fatalf("the image is not between the two paragraphs, a blank row away from each: %q", got)
			}
			equal(t, c.name, got[2:len(got)-2], c.want)
			// The description of an image is not drawn, and neither is its
			// title.
			for _, line := range got {
				if strings.Contains(line, "SECRET") {
					t.Errorf("the description is drawn: %q", line)
				}
			}
		})
	}

	// What an image source says is made safe too: a file name is the
	// model's.
	for name, shown := range map[string]draw.Shown{
		"a caption":       {Rows: picture, Caption: hostile},
		"a line and hint": {Line: hostile, Hint: hostile},
	} {
		lines := trueDark().with(draw.Options{Width: 60, Images: &images{shown: shown}}).Item(agent(31, "![chart](latency.png)"))
		fits(t, name, lines, 60)
		said := lines
		if len(shown.Rows) > 0 {
			said = lines[len(shown.Rows):] // the picture is its own
		}
		harmless(t, name, said)
		if all := strings.Join(stripped(said), ""); !strings.Contains(strings.ReplaceAll(all, " ", ""), "[31mred") {
			t.Errorf("%s lost the text around the escape sequences: %q", name, all)
		}
	}

	// The picture is handed over as it is; what is said under it is dim.
	src := &images{shown: draw.Shown{Rows: picture, Caption: "latency.png · /image save 31"}}
	lines := trueDark().with(draw.Options{Width: 60, Images: src}).Item(agent(31, reply))
	if lines[2] != "  "+picture[0] || lines[3] != "  "+picture[1] {
		t.Errorf("the rows of the picture were changed: %q", lines[2:4])
	}
	for _, c := range find(t, lines[4], "latency.png · /image save 31") {
		if c.fg != darkDim {
			t.Fatalf("the caption is drawn %+v, want dim", c)
		}
	}
	src = &images{shown: draw.Shown{Line: "[image: latency.png, 640×360, 692 kB]", Hint: "/image save 31 ./latency.png"}}
	lines = trueDark().with(draw.Options{Width: 60, Images: src}).Item(agent(31, reply))
	for _, c := range find(t, lines[2], "[image: latency.png, 640×360, 692 kB]") {
		if c != (cell{text: c.text}) {
			t.Fatalf("the image's line is styled: %+v", c)
		}
	}
	for _, c := range find(t, lines[3], "/image save 31 ./latency.png") {
		if c.fg != darkDim {
			t.Fatalf("the hint is drawn %+v, want dim", c)
		}
	}
}

func TestImageSourceIsAsked(t *testing.T) {
	t.Parallel()
	const reply = "![first](a.png)\n\nWords with ![second](sub/b.png) in them.\n\n![third](https://example.com/c.png)\n\n![fourth](c.png)"
	cases := []struct {
		name          string
		width         int
		cols, rows    int
		wantC, wantR  int
		item          *transcript.Item
		seq           uint64
		wantRefs      []draw.ImageRef
		wantRemaining string
	}{
		{
			name: "a reply's images are counted from 1", width: 100, cols: 44, rows: 10, wantC: 44, wantR: 10,
			item: &transcript.Item{Kind: transcript.Agent, Seq: 31, TS: 1700, Text: reply},
			wantRefs: []draw.ImageRef{
				{Seq: 31, N: 1, Path: "a.png", Alt: "first", TS: 1700},
				{Seq: 31, N: 2, Path: "sub/b.png", Alt: "second", TS: 1700},
				{Seq: 31, N: 3, Path: "c.png", Alt: "fourth", TS: 1700},
			},
		},
		{
			name: "the words before a tool call are a reply too", width: 100, cols: 30, rows: 4, wantC: 30, wantR: 4,
			item:     &transcript.Item{Kind: transcript.ToolCall, Seq: 40, TS: 1800, Tool: "python", Lead: "![first](a.png)", Text: "x"},
			wantRefs: []draw.ImageRef{{Seq: 40, N: 1, Path: "a.png", Alt: "first", TS: 1800}},
		},
		{
			name: "no box given is 44 by 12", width: 100, wantC: 44, wantR: 12,
			item:     &transcript.Item{Kind: transcript.Agent, Seq: 31, Text: "![first](a.png)"},
			wantRefs: []draw.ImageRef{{Seq: 31, N: 1, Path: "a.png", Alt: "first"}},
		},
		{
			name: "the box is never wider than the row inside the indent", width: 30, cols: 44, rows: 12, wantC: 28, wantR: 12,
			item:     &transcript.Item{Kind: transcript.Agent, Seq: 31, Text: "![first](a.png)"},
			wantRefs: []draw.ImageRef{{Seq: 31, N: 1, Path: "a.png", Alt: "first"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			src := &images{shown: draw.Shown{Line: "[image]"}}
			r := trueDark().with(draw.Options{Width: c.width, Images: src, MaxCols: c.cols, MaxRows: c.rows})
			r.Item(c.item)
			if len(src.refs) != len(c.wantRefs) {
				t.Fatalf("asked for %+v, want %+v", src.refs, c.wantRefs)
			}
			for i, want := range c.wantRefs {
				if src.refs[i] != want {
					t.Errorf("image %d: asked for %+v, want %+v", i, src.refs[i], want)
				}
				if src.boxes[i] != [2]int{c.wantC, c.wantR} {
					t.Errorf("image %d: the box is %v, want %d by %d", i, src.boxes[i], c.wantC, c.wantR)
				}
			}
			// The source is asked again every time the item is drawn: it is
			// how it learns that the image is in view.
			r.Item(c.item)
			if len(src.refs) != 2*len(c.wantRefs) {
				t.Errorf("drawn twice, the source was asked %d times", len(src.refs))
			}
		})
	}

	// After four images the rest stay text.
	src := &images{shown: draw.Shown{Line: "[image]"}}
	many := strings.Repeat("![x](a.png)\n\n", 6)
	trueDark().with(draw.Options{Width: 60, Images: src}).Item(agent(1, many))
	if len(src.refs) != 4 {
		t.Errorf("a reply with six images asked for %d", len(src.refs))
	}
	// And a streaming reply asks for none: the file may not be written yet.
	src = &images{shown: draw.Shown{Line: "[image]"}}
	trueDark().with(draw.Options{Width: 60, Images: src}).Live(transcript.Live{Text: many}, 0)
	if len(src.refs) != 0 {
		t.Errorf("the reply in flight asked for %d images", len(src.refs))
	}
}

func TestModsDrawCodeBlocks(t *testing.T) {
	t.Parallel()
	const reply = "A graph:\n\n```mermaid\ngraph TD\n  A --> B\n```\n\n```go\nx := 1\n```\n\n```mermaid\ngraph LR\n```\n\n```\nbare\n```"
	mods := &rendered{byKind: map[string][]wire.Line{
		"code:mermaid": {
			{{T: "A", Tone: "mod", Bold: true}, {T: " to "}, {T: "B", Tone: "ok"}},
			{},
			{{T: strings.Repeat("a line longer than the row ", 4)}},
		},
	}}
	r := trueDark().with(draw.Options{Width: 40, Mods: mods})
	lines := r.Item(agent(12, reply))
	fits(t, "mods", lines, 40)
	equal(t, "a reply with a mod's blocks", stripped(lines), []string{
		"  A graph:",
		"",
		"  A to B",
		"",
		"  a line longer than the row a line lon…",
		"",
		"   go" + strings.Repeat(" ", 35),
		"   x := 1" + strings.Repeat(" ", 31),
		"",
		"  A to B",
		"",
		"  a line longer than the row a line lon…",
		"",
		"   bare" + strings.Repeat(" ", 33),
	})
	if c := find(t, lines[2], "A")[0]; c.fg != "#c49ae0" || !c.bold || c.bg != "" {
		t.Errorf("a span is drawn %+v, want the mod tone, bold, and no code background", c)
	}
	if c := find(t, lines[2], "B")[0]; c.fg != "#7dc38f" || c.bold {
		t.Errorf("a span is drawn %+v, want the ok tone", c)
	}

	// What the source was asked: every block with a language, counted from
	// 1, with the code and the cells there are for a line.
	want := []draw.RenderRef{
		{Seq: 12, N: 1, Kind: "code:mermaid", Text: "graph TD\n  A --> B"},
		{Seq: 12, N: 2, Kind: "code:go", Text: "x := 1"},
		{Seq: 12, N: 3, Kind: "code:mermaid", Text: "graph LR"},
	}
	if len(mods.refs) != len(want) {
		t.Fatalf("asked for %+v, want %+v", mods.refs, want)
	}
	for i := range want {
		if mods.refs[i] != want[i] || mods.widths[i] != 38 {
			t.Errorf("asked for %+v at %d cells, want %+v at 38", mods.refs[i], mods.widths[i], want[i])
		}
	}

	// Until the mod has answered, and when it draws nothing, core draws.
	for name, src := range map[string]*rendered{
		"not answered yet": {},
		"no lines":         {byKind: map[string][]wire.Line{"code:mermaid": {}}},
	} {
		lines := trueDark().with(draw.Options{Width: 40, Mods: src}).Item(agent(12, "```mermaid\ngraph TD\n```"))
		equal(t, name, stripped(lines), []string{"   mermaid" + strings.Repeat(" ", 30), "   graph TD" + strings.Repeat(" ", 29)})
	}

	// More lines than a mod may print are cut.
	many := &rendered{byKind: map[string][]wire.Line{"code:x": make([]wire.Line, 500)}}
	for i := range many.byKind["code:x"] {
		many.byKind["code:x"][i] = wire.Line{{T: "row"}}
	}
	if got := trueDark().with(draw.Options{Width: 40, Mods: many}).Item(agent(1, "```x\ny\n```")); len(got) != 200 {
		t.Errorf("a mod's 500 lines are drawn as %d", len(got))
	}
}

func TestModsDrawToolOutput(t *testing.T) {
	t.Parallel()
	mods := &rendered{byKind: map[string][]wire.Line{
		"tool:python": {{{T: "3 rows", Tone: "mod"}}, {}, {{T: strings.Repeat("a line longer than the row ", 4)}}, {{T: "last"}}, {{T: "and a fifth"}}},
	}}
	r := trueDark().with(draw.Options{Width: 40, Mods: mods})
	result := &transcript.Item{Kind: transcript.ToolResult, Seq: 8, Tool: "python", Text: "raw\toutput\x1b[31m\n\n"}
	lines := r.Item(result)
	fits(t, "mods", lines, 40)
	// The mod's lines hang from the elbow as the output would, and are not
	// folded: the mod chose them.
	equal(t, "a mod's tool output", stripped(lines), []string{
		"  ⎿ 3 rows",
		"",
		"    a line longer than the row a line l…",
		"    last",
		"    and a fifth",
	})
	if c := find(t, lines[0], "3 rows")[0]; c.fg != "#c49ae0" {
		t.Errorf("a span is drawn %+v, want the mod tone", c)
	}
	want := draw.RenderRef{Seq: 8, N: 1, Kind: "tool:python", Text: "raw    output[31m"}
	if len(mods.refs) != 1 || mods.refs[0] != want || mods.widths[0] != 36 {
		t.Errorf("asked for %+v at %v cells, want %+v at 36", mods.refs, mods.widths, want)
	}

	// Another tool's output, and a result whose tool is not known yet, are
	// core's. The second is not even asked about: it has no kind.
	other := r.Item(&transcript.Item{Kind: transcript.ToolResult, Seq: 9, Tool: "shell", Text: "plain"})
	equal(t, "another tool", stripped(other), []string{"  ⎿ plain"})
	asked := len(mods.refs)
	unnamed := r.Item(&transcript.Item{Kind: transcript.ToolResult, Seq: 10, Text: "plain"})
	equal(t, "a result with no tool", stripped(unnamed), []string{"  ⎿ plain"})
	if len(mods.refs) != asked {
		t.Errorf("the source was asked about a result with no tool: %+v", mods.refs[asked:])
	}
	// A tool call is never a mod's.
	call := r.Item(&transcript.Item{Kind: transcript.ToolCall, Seq: 11, Tool: "python", Text: "x = 1"})
	equal(t, "a tool call", stripped(call), []string{"⏺ python", "    x = 1"})
}

func TestReferenceLinksAcrossParts(t *testing.T) {
	t.Parallel()
	// The definitions are in the last piece of text, and the links that use
	// them in the first two: a code block and an image stand between.
	const reply = "See [the docs][d] first.\n\n```sh\nmake\n```\n\nThen [again][D], and [the other one][o].\n\n" +
		"![chart](latency.png)\n\nLast, [here][d].\n\n[d]: https://example.com/docs \"SECRETTITLE\"\n[o]: <https://example.com/o>\n"
	r := trueDark().renderer(100)
	lines := r.Item(agent(1, reply))
	fits(t, "references", lines, 100)
	got := stripped(lines)
	for i, row := range got {
		got[i] = strings.TrimRight(row, " ")
	}
	equal(t, "a reply with reference links", got, []string{
		"  See the docs https://example.com/docs first.",
		"",
		"   sh",
		"   make",
		"",
		"  Then again https://example.com/docs, and the other one https://example.com/o.",
		"",
		"  [image: latency.png]",
		"",
		"  Last, here https://example.com/docs.",
	})
	if c := find(t, lines[0], "the docs")[0]; c.link != "https://example.com/docs" {
		t.Errorf("the link is drawn %+v", c)
	}

	// A reply in one piece of text finds its own definitions, and is not
	// given any.
	one := trueDark().renderer(100).Item(agent(1, "See [the docs][d].\n\n[d]: https://example.com/docs\n"))
	equal(t, "one piece of text", stripped(one), []string{"  See the docs https://example.com/docs."})

	// A label nothing defines stays as it was written.
	none := trueDark().renderer(100).Item(agent(1, "See [the docs][d].\n\n```\nx\n```\n\nAnd [more][e].\n\n[e]: https://example.com/e\n"))
	if got := stripped(none); got[0] != "  See [the docs][d]." || !strings.Contains(got[len(got)-1], "more https://example.com/e") {
		t.Errorf("drawn as %q", got)
	}
}

// refs reads Markdown the way the renderer does and returns its link
// definitions by label, and whether anything in it would be drawn.
func refs(src string) (byLabel map[string]string, visible bool) {
	pc := parser.NewContext()
	doc := goldmark.DefaultParser().Parse(text.NewReader([]byte(src)), parser.WithContext(pc))
	byLabel = make(map[string]string)
	for _, ref := range pc.References() {
		byLabel[util.ToLinkReference(ref.Label())] = string(ref.Destination())
	}
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		if n.Kind() != ast.KindTextBlock || n.Lines().Len() != 0 {
			visible = true
		}
	}
	return byLabel, visible
}

// definitionSources are replies with link definitions of every shape.
func definitionSources() map[string]string {
	return map[string]string{
		"none":                   "just words\n",
		"bare":                   "[a]: /x\n",
		"in angle brackets":      "[a]: <x y z>\n",
		"with a title":           "[a]: /x \"title\"\n[b]: /y 'it''s'\n[c]: /z (paren)\n",
		"a title on its line":    "[a]: /x\n  \"title\"\n",
		"the first wins":         "[a]: /first\n\n[A]: /second\n",
		"a bracket in the dest":  "[a]: x<y>z\n",
		"an escaped bracket":     "[a\\]b]: /x\n",
		"brackets in a label":    "[a [b\\] c]: /x\n",
		"a label over lines":     "[one\n   two]: /x\n",
		"Unicode":                "[Ünï çödé]: /ü?q=ß#é\n",
		"an empty destination":   "[a]: <>\n",
		"parentheses":            "[a]: /x(y)z\n[b]: <a(b>\n",
		"backslashes":            "[a]: /x\\<y\\>\n[b]: <a\\>b>\n",
		"in a quote and a list":  "> [a]: /x\n\n- [b]: /y\n",
		"not in code":            "    [a]: /x\n\n```\n[b]: /y\n```\n\n[c]: /z\n",
		"among words":            "words\n\n[a]: /x\nmore words [a]\n\n[b]: /y\n",
		"many":                   strings.Repeat("[r]: /x\n\n[s]: <y y>\n\n", 20) + "[t]: /z",
		"an entity":              "[a]: /x&amp;y\n",
		"a title that is not":    "[a]: /x \"title\" trailing\n",
		"a quote mark alone":     "[a]: /x\n\"\n",
		"footnote-like":          "[^1]: note text here\n",
		"control characters":     "[a\x1b]: /x\x07\n",
		"a backslash at the end": "[a]: /x\\\n[b]: <y\\\\>\n",
		"a bracket and no space": "[a]: x>y\n[b]: <\\<x\\>>\n",
	}
}

// checkDefinitions fails the test unless the lines made from a reply read as
// its link definitions and as nothing else.
func checkDefinitions(t *testing.T, src string) {
	t.Helper()
	defs := draw.Definitions(src)
	want, _ := refs(src)
	got, visible := refs(defs)
	if visible {
		t.Errorf("%q: the definitions would draw something: %q", src, defs)
	}
	for label, dest := range got {
		if w, ok := want[label]; !ok || w != dest {
			t.Errorf("%q: the definitions have %q as %q, the reply has %q (%v)", src, label, dest, w, ok)
		}
	}
	// In front of text they still draw nothing, and the text keeps its own.
	if _, visible := refs(defs + "\n[zz]: /zz\n"); visible {
		t.Errorf("%q: the definitions draw something in front of text: %q", src, defs)
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(defs, "\n"), "\n") {
		if defs != "" && !strings.HasPrefix(line, "[") && !strings.Contains(defs, "[one\n") {
			t.Errorf("%q: a line that is not a definition: %q", src, line)
		}
	}
}

func TestDefinitions(t *testing.T) {
	t.Parallel()
	for name, src := range definitionSources() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			checkDefinitions(t, src)
			// Every definition these replies have can be written out.
			want, _ := refs(src)
			got, _ := refs(draw.Definitions(src))
			if len(got) != len(want) {
				t.Errorf("%d of the reply's %d definitions were written: %q", len(got), len(want), draw.Definitions(src))
			}
		})
	}
	cases := []struct{ name, src, want string }{
		{"in label order, in angle brackets, without titles", "[b]: /y 'title'\n\n[a]: /x\n", "[a]: </x>\n[b]: </y>\n"},
		{"the first of a label", "[a]: /first\n\n[A]: /second\n", "[a]: </first>\n"},
		{"a destination that cannot stand in brackets", "[a]: x<y>z\n", "[a]: x<y>z\n"},
		// In brackets the backslash would escape the closing one, and the
		// line would be drawn as text.
		{"a destination that ends in a backslash", "[a]: /x\\\n", "[a]: /x\\\n"},
		{"none", "no definitions here: [a] and ]: too\n", ""},
	}
	for _, c := range cases {
		if got := draw.Definitions(c.src); got != c.want {
			t.Errorf("%s: Definitions(%q) = %q, want %q", c.name, c.src, got, c.want)
		}
	}
}

func FuzzDefinitions(f *testing.F) {
	for _, src := range definitionSources() {
		f.Add(src)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 2048 {
			return // the parser is quadratic on some input
		}
		checkDefinitions(t, src)
	})
}

func TestLongRepliesAreDrawnAsWritten(t *testing.T) {
	t.Parallel()
	const line = "Some **bold** words, a [link](https://example.com), and `code`.\n\n```go\nx := 1\n```\n\n"
	over := strings.Repeat(line, draw.MaxMarkdown/len(line)+1)
	r := trueDark().renderer(80)
	lines := r.Item(agent(1, over))
	fits(t, "long", lines, 80)
	got := stripped(lines)
	equal(t, "the start of a reply over the limit", got[:6], []string{
		"  Some **bold** words, a [link](https://example.com), and `code`.",
		"",
		"  ```go",
		"  x := 1",
		"  ```",
		"",
	})
	last := len(lines) - 1
	equal(t, "its end", got[last-1:], []string{"  ```", "  shown as written: this reply is longer than 32 KiB"})
	for _, c := range find(t, lines[last], "shown as written: this reply is longer than 32 KiB") {
		if c.fg != darkDim {
			t.Fatalf("the line under it is drawn %+v, want dim", c)
		}
	}
	for i, l := range lines[:last] {
		if strings.Contains(l, "\x1b") {
			t.Fatalf("line %d of a reply drawn as written is styled: %q", i, l)
		}
	}
	if splits, markdowns, _ := r.Counts(); splits != 0 || markdowns != 0 {
		t.Errorf("a reply over the limit was parsed: %d splits, %d calls to the Markdown renderer", splits, markdowns)
	}

	// A reply the parser would take seconds over is drawn at once. Nothing
	// here measures the time; with the parser in the way the test would be
	// the slowest in the package by far.
	hostile := strings.Repeat("[a](", draw.MaxMarkdown)
	r = trueDark().renderer(80)
	if got := r.Item(agent(2, hostile)); len(got) < 2 {
		t.Errorf("drawn as %d lines", len(got))
	}
	if splits, markdowns, _ := r.Counts(); splits != 0 || markdowns != 0 {
		t.Errorf("a hostile reply was parsed: %d splits, %d calls to the Markdown renderer", splits, markdowns)
	}

	// The same holds for the words before a tool call.
	r = trueDark().renderer(80)
	call := r.Item(&transcript.Item{Kind: transcript.ToolCall, Seq: 3, Tool: "python", Lead: over, Text: "x"})
	if splits, markdowns, _ := r.Counts(); splits != 0 || markdowns != 0 || !strings.Contains(strings.Join(stripped(call), "\n"), "shown as written") {
		t.Errorf("a tool call's long lead was parsed: %d splits, %d calls", splits, markdowns)
	}

	// Making a reply safe can make it longer, and it is the text the parser
	// would be handed that counts: a tab is four spaces.
	tabs := strings.Repeat("\t[a](\n", draw.MaxMarkdown/6)
	if len(tabs) > draw.MaxMarkdown {
		t.Fatalf("the reply is %d bytes, want at most %d", len(tabs), draw.MaxMarkdown)
	}
	r = trueDark().renderer(80)
	lines = r.Item(agent(5, tabs))
	if got := stripped(lines); got[0] != "      [a](" || !strings.Contains(got[len(got)-1], "shown as written") {
		t.Errorf("a reply that tabs make too long is drawn as %q ... %q", got[0], got[len(got)-1])
	}
	if splits, markdowns, _ := r.Counts(); splits != 0 || markdowns != 0 {
		t.Errorf("a reply that tabs make too long was parsed: %d splits, %d calls to the Markdown renderer", splits, markdowns)
	}

	// At the limit a reply is still Markdown. The limit counts bytes.
	at := strings.Repeat("**bold** words\n\n", draw.MaxMarkdown/16)
	if len(at) != draw.MaxMarkdown {
		t.Fatalf("the reply is %d bytes, want %d", len(at), draw.MaxMarkdown)
	}
	r = trueDark().renderer(80)
	lines = r.Item(agent(4, at))
	if got := stripped(lines); got[0] != "  bold words" || strings.Contains(got[len(got)-1], "shown as written") {
		t.Errorf("a reply at the limit is drawn as %q ... %q", got[0], got[len(got)-1])
	}
	if splits, markdowns, _ := r.Counts(); splits != 1 || markdowns != 1 {
		t.Errorf("a reply at the limit: %d splits, %d calls to the Markdown renderer", splits, markdowns)
	}
}

func TestPartsAreKept(t *testing.T) {
	t.Parallel()
	const reply = "Words.\n\n```go\nx := 1\n```\n\nMore words."
	splits := func(r *draw.Renderer) int {
		n, _, _ := r.Counts()
		return n
	}
	r := trueDark().renderer(80)
	it := agent(7, reply)
	first := r.Item(it)
	for range 3 {
		equal(t, "drawn again", r.Item(it), first)
	}
	if got := splits(r); got != 1 {
		t.Fatalf("a reply drawn four times was cut %d times", got)
	}

	// The item changes: the log's account replaces the stream's.
	changed := agent(7, reply+"\n\nAnd a last line.")
	changed.Rev = 8
	if got := stripped(r.Item(changed)); got[len(got)-1] != "  And a last line." {
		t.Errorf("a changed reply is drawn from the old parts: %q", got)
	}
	if got := splits(r); got != 2 {
		t.Errorf("after the reply changed it was cut %d times in all, want 2", got)
	}
	r.Item(changed)
	if got := splits(r); got != 2 {
		t.Errorf("the changed reply was cut again: %d", got)
	}

	// Another item has parts of its own, and does not push the first out.
	r.Item(agent(8, "Other words."))
	r.Item(changed)
	if got := splits(r); got != 3 {
		t.Errorf("two replies were cut %d times, want 3", got)
	}

	// Forget drops one item's parts and no other's.
	r.Forget(7)
	r.Item(agent(8, "Other words."))
	if got := splits(r); got != 3 {
		t.Errorf("Forget(7) dropped the parts of item 8: %d", got)
	}
	r.Item(changed)
	if got := splits(r); got != 4 {
		t.Errorf("after Forget the reply was cut %d times in all, want 4", got)
	}
	r.Forget(99) // nothing kept: nothing happens

	// The words before a tool call are kept the same way.
	call := &transcript.Item{Kind: transcript.ToolCall, Seq: 9, Tool: "python", Lead: reply, Text: "x"}
	r.Item(call)
	r.Item(call)
	if got := splits(r); got != 5 {
		t.Errorf("a tool call's lead was cut %d times, want once", got-4)
	}

	// A Renderer made with With keeps them: a resize does not read every
	// reply again. One made with New starts with nothing.
	wider := r.With(draw.Options{Theme: trueDark().theme(), Width: 120, Expanded: true})
	lines := wider.Item(changed)
	wider.Item(call)
	if got := splits(wider); got != 0 {
		t.Errorf("a Renderer made with With cut %d replies it was handed the parts of", got)
	}
	fits(t, "at the new width", lines, 120)
	if got := stripped(lines); got[0] != "  Words." {
		t.Errorf("drawn at the new width as %q", got)
	}
	wider.Forget(7)
	r.Item(changed)
	if got := splits(r); got != 6 {
		t.Errorf("Forget on the new Renderer did not reach the old one: %d", got)
	}
	fresh := trueDark().renderer(120)
	fresh.Item(changed)
	if got := splits(fresh); got != 1 {
		t.Errorf("a new Renderer cut %d replies", got)
	}

	// An item with no sequence number has nothing to be kept by.
	r = trueDark().renderer(80)
	r.Item(agent(0, reply))
	r.Item(agent(0, "Another reply."))
	if got := stripped(r.Item(agent(0, reply))); got[0] != "  Words." {
		t.Errorf("drawn as %q", got)
	}
}

func TestDefaults(t *testing.T) {
	t.Parallel()
	// No theme is the safest one, and no width is one cell.
	r := draw.New(draw.Options{Width: 40})
	lines := r.Item(agent(1, "**bold** • words"))
	equal(t, "no theme", lines, []string{"  **bold** • words"})
	equal(t, "no theme, a rule", r.Beginning(), []string{"------------ \x1b[2mthe beginning\x1b[m -------------"})
	fits(t, "no width", draw.New(draw.Options{Theme: trueDark().theme()}).Item(agent(1, "words")), 1)

	// A glyph the user chose is the one drawn, and measured.
	th := theme.New(trueDark().caps, theme.Options{Glyph: map[string]string{"prompt": ">>>", "elbow": "└─"}})
	r = draw.New(draw.Options{Theme: th, Width: 14})
	equal(t, "a wider prompt", stripped(r.Item(item(transcript.User, 1, "plot the latency"))), []string{">>> plot the", "    latency"})
	equal(t, "a wider elbow", stripped(r.Item(&transcript.Item{Kind: transcript.ToolResult, Text: "one\ntwo"})), []string{"  └─ one", "     two"})
}
