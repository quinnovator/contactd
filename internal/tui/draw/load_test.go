package draw_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/draw"
)

// tooSlow is the line under a reply whose text was not rendered.
const tooSlow = "shown as written: this reply would take too long to lay out"

// wasRendered reports whether a reply's text went through the Markdown renderer,
// and fails the test when the line under the reply says otherwise.
func wasRendered(t *testing.T, what string, r *draw.Renderer, lines []string) bool {
	t.Helper()
	_, markdowns, _ := r.Counts()
	// The line is wrapped where the row is narrow, in the middle of a word too.
	said := strings.Contains(strings.ReplaceAll(strings.Join(stripped(lines), ""), " ", ""), strings.ReplaceAll(tooSlow, " ", ""))
	if said == (markdowns > 0) {
		t.Errorf("%s: the Markdown renderer was called %d times, and the line under the reply is there: %v", what, markdowns, said)
	}
	return markdowns > 0
}

// nestedByIndent is a list nested depth levels deep, a level to a line.
func nestedByIndent(depth int) string {
	var b strings.Builder
	for i := range depth {
		b.WriteString(strings.Repeat("  ", i) + "- level " + strconv.Itoa(i+1) + "\n")
	}
	return b.String()
}

func TestDeepNestingIsShownAsWritten(t *testing.T) {
	t.Parallel()
	// The Markdown renderer lays a quote out again for every level it is
	// nested in, and with no width left it puts every character on a row of
	// its own. Nothing here measures the time: with the renderer in the way
	// the first three of these would take minutes.
	cases := []struct {
		name  string
		width int
		src   string
		want  bool // rendered as Markdown
	}{
		{"250 quotes in 250 bytes", 100, strings.Repeat(">", 250) + "a", false},
		{"a thousand quotes", 100, strings.Repeat("> ", 1000) + "a", false},
		{"a thousand lists", 100, strings.Repeat("- ", 1000) + "a", false},
		{"a thousand numbered lists", 100, strings.Repeat("1. ", 1000) + "a", false},
		{"quotes and lists", 100, strings.Repeat("> - ", 200) + "a", false},
		{"a list nested by its indentation", 100, nestedByIndent(40), false},
		{"definitions in quotes", 100, strings.Repeat("> ", 30) + "term\n" + strings.Repeat("> ", 30) + ": what it means", false},
		{"one level too many", 100, strings.Repeat(">", draw.MaxNest+1) + " a", false},
		{"as deep as is rendered", 100, strings.Repeat(">", draw.MaxNest) + " a", true},
		{"five quotes", 100, "> > > > > a", true},
		{"five lists", 100, "- - - - - a", true},
		{"a list four deep", 100, nestedByIndent(4), true},
		// Every level takes two cells: what fits a wide row does not fit a
		// narrow one.
		{"six quotes in 30 columns", 30, ">>>>>> a", true},
		{"six quotes in 14 columns", 14, ">>>>>> a", false},
		{"two quotes in 8 columns", 8, ">> a", true},
		{"three quotes in 8 columns", 8, ">>> a", false},
		{"a list in 5 columns", 5, "- a", true},
		{"a list in 4 columns", 4, "- a", false},
		{"words in 4 columns", 4, "a b", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for _, l := range []look{trueDark(), light16()} {
				r := l.renderer(c.width)
				lines := r.Item(agent(1, c.src))
				fits(t, c.name, lines, c.width)
				if got := wasRendered(t, c.name, r, lines); got != c.want {
					t.Errorf("rendered as Markdown: %v, want %v: %q", got, c.want, stripped(lines))
				}
				// A reply laid out for the width is not one row for each
				// character of it.
				if len(lines) > 80 {
					t.Errorf("%d rows for %d bytes", len(lines), len(c.src))
				}
			}
		})
	}

	// What is shown is the reply as it was written, and the line that says
	// why is dim.
	lines := trueDark().renderer(80).Item(agent(1, strings.Repeat(">", 40)+" **deep**"))
	equal(t, "a reply nested too deep", stripped(lines), []string{"  " + strings.Repeat(">", 40) + " **deep**", "  " + tooSlow})
	for _, c := range find(t, lines[1], tooSlow) {
		if c.fg != darkDim {
			t.Fatalf("the line under it is drawn %+v, want dim", c)
		}
	}
	// The same holds for the words before a tool call.
	r := trueDark().renderer(60)
	call := r.Item(&transcript.Item{Kind: transcript.ToolCall, Seq: 2, Tool: "python", Lead: strings.Repeat(">", 40) + " a", Text: "x"})
	if wasRendered(t, "a lead", r, call) {
		t.Errorf("a tool call's deep lead was rendered: %q", stripped(call))
	}
	// With ASCII glyphs every reply is shown as written already, and nothing
	// is said under it.
	equal(t, "ASCII", stripped(noneASCII().renderer(60).Item(agent(1, strings.Repeat(">", 40)+" a"))), []string{"  " + strings.Repeat(">", 40) + " a"})
}

func TestHeavyRepliesAreShownAsWritten(t *testing.T) {
	t.Parallel()
	// Each of these is under the size limit and costs the Markdown renderer
	// seconds: a heading, a quote and a list item are each many times the
	// work of a line of text, and a code block with no language is the work
	// of a thousand.
	highlit := []look{trueDark()}
	everywhere := []look{trueDark(), light16(), unknown256()}
	cases := []struct {
		name  string
		src   string
		looks []look
	}{
		{"headings", strings.Repeat("# a\n", 2000), everywhere},
		{"headings by underline", strings.Repeat("a\n===\n\n", 2000), everywhere},
		{"quotes", strings.Repeat("> a\n\n", 2000), everywhere},
		{"paragraphs in a quote", strings.Repeat("> a\n>\n", 2000), everywhere},
		{"quotes in quotes", strings.Repeat(">>>> a\n\n", 200), everywhere},
		{"list items", strings.Repeat("- a\n", 6000), everywhere},
		{"numbered items", strings.Repeat("1. a\n", 6000), everywhere},
		{"definitions", strings.Repeat("a\n: b\n\n", 4500), everywhere},
		{"indented code", strings.Repeat("a\n\n    code\n\n", 150), highlit},
		{"code in a list with no language", strings.Repeat("- a\n\n  ```\n  x\n  ```\n\n", 60), highlit},
		{"code in a list in languages nobody has", strings.Repeat("- a\n\n  ```no-such-language\n  x\n  ```\n\n", 60), highlit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if len(c.src) > draw.MaxMarkdown {
				t.Fatalf("the reply is %d bytes, over the size limit", len(c.src))
			}
			// A code block and an image of the reply's own are still drawn
			// as what they are.
			src := c.src + "\n```go\nx := 1\n```\n\n![chart](latency.png)\n"
			for _, l := range c.looks {
				pic := &images{shown: draw.Shown{Line: "[image: latency.png]"}}
				r := l.with(draw.Options{Width: 80, Images: pic})
				lines := r.Item(agent(1, src))
				fits(t, c.name, lines, 80)
				plain := stripped(lines)
				for i, row := range plain {
					plain[i] = strings.TrimRight(row, " ")
				}
				if got := plain[len(plain)-6:]; !slices.Equal(got, []string{"", "   go", "   x := 1", "", "  [image: latency.png]", "  " + tooSlow}) {
					t.Errorf("%s: the reply ends %q", l.name, got)
				}
				first, _, _ := strings.Cut(c.src, "\n")
				if plain[0] != "  "+first {
					t.Errorf("%s: the reply begins %q, want it as written", l.name, plain[0])
				}
				if len(pic.refs) != 1 {
					t.Errorf("%s: the image source was asked %d times", l.name, len(pic.refs))
				}
				// Only the code block went to the renderer, where code is
				// highlighted at all.
				if _, markdowns, _ := r.Counts(); markdowns > 1 {
					t.Errorf("%s: the Markdown renderer was called %d times", l.name, markdowns)
				}
			}
		})
	}

	// Where code is not highlighted, no language is guessed and a code block
	// costs what its lines do.
	for _, c := range cases[len(cases)-3:] {
		for _, l := range []look{light16(), unknown256()} {
			r := l.renderer(80)
			if lines := r.Item(agent(1, c.src)); !wasRendered(t, c.name, r, lines) {
				t.Errorf("%s on %s was not rendered", c.name, l.name)
			}
		}
	}
}

func TestOrdinaryRepliesAreRendered(t *testing.T) {
	t.Parallel()
	// The limits are far from what a reply written to be read holds.
	steps := func(n int, fence string) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			b.WriteString(strconv.Itoa(i) + ". Step " + strconv.Itoa(i) + ":\n\n    " + fence + "\n    make step" + strconv.Itoa(i) + " && ./run\n\n    echo done\n    ```\n\n")
		}
		return b.String()
	}
	cases := map[string]string{
		"the reply of the tests":          rich,
		"three hundred list items":        strings.Repeat("- an item of a long list\n", 300),
		"a hundred headings":              strings.Repeat("## A heading\n\nAnd a paragraph under it.\n\n", 100),
		"a long passage, quoted":          strings.Repeat("> a line of a long passage that was quoted line by line\n", 400),
		"a quote of forty paragraphs":     strings.Repeat("> a paragraph\n>\n", 40),
		"twenty steps, each with code":    steps(20, "```sh"),
		"a list four deep, many times":    strings.Repeat(nestedByIndent(4)+"\n", 40),
		"a table of two hundred rows":     "| a | b |\n| --- | --- |\n" + strings.Repeat("| 1 | 2 |\n", 200),
		"rules":                           strings.Repeat("words\n\n---\n\n", 100) + "* * *\n\n- - -\n",
		"code in a list, with blank rows": "1. Write it:\n\n    ```go\n" + strings.Repeat("    x := 1\n\n", 30) + "    ```\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if len(src) > draw.MaxMarkdown {
				t.Fatalf("the reply is %d bytes, over the size limit", len(src))
			}
			for _, l := range []look{trueDark(), light16()} {
				for _, w := range []int{100, 40} {
					r := l.renderer(w)
					lines := r.Item(agent(1, src))
					fits(t, name, lines, w)
					if !wasRendered(t, name, r, lines) {
						t.Errorf("%s at %d columns was shown as written", l.name, w)
					}
				}
			}
		})
	}
}

func TestWeigh(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		src         string
		depth, work int
		plain       int
		fences      []string
	}{
		{"nothing", "", 0, 1, 0, nil},
		{"a line", "words", 0, 1, 0, nil},
		{"two paragraphs", "one\n\ntwo", 0, 3, 0, nil},
		{"a heading", "# one", 0, 61, 0, nil},
		{"a heading by underline", "one\n===\ntwo\n---", 0, 124, 0, nil},
		{"a rule", "---", 0, 61, 0, nil},
		{"a list item", "- one", 1, 11, 0, nil},
		{"every marker", "- a\n+ b\n* c\n1. d\n2) e\n: f", 1, 66, 0, nil},
		{"not a marker", "-a\n*b*\n1.5\n2024\n:x\n-", 1, 16, 0, nil},
		{"a nested item", "- a\n  - b\n    - c", 3, 33, 0, nil},
		{"items on one line", "- - - a", 4, 31, 0, nil},
		{"a quote", "> a", 1, 61, 0, nil},
		{"quotes in quotes", ">>> a", 3, 361, 0, nil},
		{"spaced quotes", "> > > a", 4, 361, 0, nil},
		{"a list in a quote", "> - a\n>   - b", 3, 142, 0, nil},
		{"spaces after the last marker do not nest", ">                    a\n-                    b", 1, 72, 1, nil},
		{"a number that is not an item", "> 12 apples", 1, 61, 0, nil},
		{"indented code", "a\n\n    code\n    more\n\n    still the same block\n\nb\n\n    another", 0, 10, 2, nil},
		{"indented under an item counts too", "- a\n\n      code", 1, 13, 1, nil},
		{"a fence", "```go\nx\n```", 0, 43, 0, []string{"go", ""}},
		{"a fence in a list", "- a\n\n    ~~~ sh -x\n    x\n    ~~~", 1, 55, 0, []string{"sh", ""}},
		{"a fence in a quote", "> ```py\n> x\n> ```", 1, 223, 0, []string{"py", ""}},
		{"a table", "| a | b |\n| --- | :-: |\n| 1 | 2 |", 0, 23, 0, nil},
	}
	for _, c := range cases {
		depth, work, plain, fences := draw.Weigh(c.src)
		if depth != c.depth || work != c.work || plain != c.plain || !slices.Equal(fences, c.fences) {
			t.Errorf("%s: Weigh(%q) = depth %d, work %d, plain %d, fences %q; want %d, %d, %d, %q",
				c.name, c.src, depth, work, plain, fences, c.depth, c.work, c.plain, c.fences)
		}
	}

	// Past a number of fences the rest count as naming no language.
	_, _, plain, fences := draw.Weigh(strings.Repeat("```go\n", 300))
	if len(fences) != 256 || plain != 44 {
		t.Errorf("300 fences: %d kept, %d counted as plain", len(fences), plain)
	}
}

func TestLanguagesAreLookedUpAFewAtATime(t *testing.T) {
	t.Parallel()
	// A name the highlighter does not have costs it a millisecond to say so,
	// and a reply can hold thousands of names. Only so many new ones are
	// looked up for one reply: here the highlighter knows every name, by the
	// file it ends in, and the first drawing still colours only some.
	const blocks = 24
	var src strings.Builder
	for i := range blocks {
		src.WriteString("```file" + strconv.Itoa(i) + ".go\nx := \"text\"\n```\n\n")
	}
	coloured := func(lines []string) int {
		n := 0
		for _, line := range lines {
			if strings.Contains(stripped([]string{line})[0], `"text"`) && find(t, line, `"text"`)[0].fg != "" {
				n++
			}
		}
		return n
	}
	r := trueDark().renderer(80)
	it := agent(1, src.String())
	first := coloured(r.Item(it))
	if first == 0 || first >= blocks {
		t.Errorf("the first drawing coloured %d of %d blocks, want some and not all", first, blocks)
	}
	// Each drawing looks up a few more, and what was looked up is kept, by a
	// Renderer made from this one too.
	if second := coloured(r.With(draw.Options{Theme: trueDark().theme(), Width: 100}).Item(it)); second != blocks {
		t.Errorf("the second drawing coloured %d of %d blocks", second, blocks)
	}

	// A reply of names nobody has is drawn plain, whole.
	var junk strings.Builder
	for i := range 200 {
		junk.WriteString("```zz" + strconv.Itoa(i) + "\nx\n```\n")
	}
	lines := trueDark().renderer(80).Item(agent(2, junk.String()))
	if len(lines) != 3*200-1 {
		t.Errorf("200 code blocks are drawn as %d rows", len(lines))
	}
	for _, line := range lines {
		if text := strings.TrimSpace(stripped([]string{line})[0]); text == "x" && find(t, line, "x")[0].fg != "" {
			t.Fatalf("code in a language nobody has is coloured: %q", line)
		}
	}
}
