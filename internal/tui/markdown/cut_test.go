package markdown

import (
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// seeds are replies that exercise every kind of cut.
func seeds() []string {
	return []string{
		"",
		"hello",
		"\n\n  \n",
		"Here is the chart:\n\n![latency by day](latency.png)\n\nand inline ![a](out/charts/a.png \"t\") and abs ![b](/abs.png) and ![sp](<my chart.png>)\n",
		"```md\n![in a fence](fenced.png)\n```\n\n`![in code](code.png)` and escaped \\![no](escaped.png)\n",
		"![remote](https://evil.example/p.png?d=secret) ![proto](//evil.example/p.png)\n![up](../2/secret.png) ![enc](..%2f2%2fsecret.png)\n[![linked](thumb.png)](https://evil.example/)\n![ref][r]\n\n[r]: ref.png\n<img src=\"html.png\">",
		"before\n\n```go\nx := 1\n\ny := 2\n```\n\nafter\n",
		"before\n```\n```\nafter",
		"```\n```\n```\n```\n",
		"words\n\n```go\nx := 1\n\nmore",
		"~~~python title=\"x\"\nprint(1)\n~~~~\n",
		"````md\n```go\nx\n```\n````\nafter",
		"  ```\n  a\n    b\n\tc\nd\n  ```",
		" ```\n\tx\n ```",
		"a\r\n\r\n```go\r\nx\r\ny\r\n```\r\n\r\nb\r\n",
		"- item\n\n  ```go\n  x\n  ```\n\nafter",
		"- ```\n  ```\n```\n```\n",
		"> ```go\n> x\n> ```\n```\ny\n```",
		"```\na\n```\n\n    indented\n    more\n",
		"```\nx\n```\n[d]: https://example.com\n\nSee [docs][d].\n",
		"- one\n- ![chart](chart.png)\n- three\n\nafter",
		"![a](a.png) ![b](b.png)\n\nafter",
		"![a](a.png)\n```\nx\n```",
		"words\n\n  ![a](a.png)  \n\nafter",
		"> see ![i](i.png)\nlazy\n\nafter",
		"![i](i.png)\n===\n\nbody",
		"see ![i](i.png)\n\nTitle\n=====\n\nbody",
		"[r]: x\n===\nfoo ![i](i.png)\n\n![j](j.png)",
		"see ![i](i.png)\n***\nbody",
		"a ![i](i.png)\n\n[d]: https://example.com\n\nmore [x][d]",
		"[r]: ref.png\n![ref][r]\n",
		"<div>\n![a](a.png)\n</div>\n\n<!--\n\n![b](b.png)\n",
		"![a *b* `c` &amp; d\\* [e](https://example.com)](x.png)",
		"![outer ![inner](in.png)](out.png)",
		"![1](1.png)\n\n![2](2.png)\n\n![3](3.png)\n\n![4](4.png)\n\n![5](5.png)\n\n![6](6.png)\n",
		"-\n  ![a](a.png)\n\n#\n\n![b](b.png)\n***\n![c](c.png)",
		"| a | b |\n|---|---|\n| ![t](t.png) | x |\n",
		"![a](a.png)\\\n![b](b.png)  \n![c](c.png)",
		"\xff\x00![a](a\x00.png)\n```\x00\n\xff\n```",
		"- a\n  - b ![i](i.png)\n    - c\n\n      deep\n- d\n\nafter",
		"> q1\n> > q2 ![i](i.png)\n> back\n\nafter\n",
		"<div>\n\n![a](a.png)\n\n</div>\n",
		"<pre>\n```\nx\n```\n</pre>\n\n```\ny\n```\n",
		"Title\n-----\n![a](a.png)\n-----\ntext",
		"a\r\n===\r\n\r\n![a](a.png)\r\n\r\n- x\r\n  - y\r\n\r\n> q\r\n",
		"![a](my\\_chart.png) ![b](a&amp;b.png) ![c](https&colon;//evil.example/c.png) ![d](&period;&period;/d.png)",
		"![a][r]\n\n```\nx\n```\n\n[r]: my\\_chart.png",
		"```0",
		"```\v",
		"\u00a0",
		"\f\n\n\v",
	}
}

// FuzzSplit checks that Split loses nothing, whatever it is given: every
// byte of the reply belongs to one piece, a Text part is its bytes as
// written, a Code part is the lines between its fences, and what is dropped
// is blank lines, fence lines and the Markdown of an image that became a
// part.
func FuzzSplit(f *testing.F) {
	for _, s := range seeds() {
		f.Add(s, 4)
		f.Add(s, 1)
		f.Add(s, 0)
	}
	f.Fuzz(func(t *testing.T, src string, maxImages int) {
		pieces := cut(src, maxImages)
		parts := Split(src, maxImages)
		var kept []Part
		at, images := 0, 0
		for i, p := range pieces {
			if p.start != at || p.end < p.start || p.end > len(src) {
				t.Fatalf("piece %d is src[%d:%d], want it to start at %d and end by %d", i, p.start, p.end, at, len(src))
			}
			at = p.end
			run := src[p.start:p.end]
			var why string
			switch p.Kind {
			case 0:
				why = checkDropped(p, run)
			case Text:
				why = checkText(p, run)
			case Code:
				why = checkCode(p, run)
			case Image:
				images++
				why = checkImage(p, run)
			default:
				why = "its kind is not one of the three"
			}
			if why != "" {
				t.Errorf("piece %d, %+v for %q: %s", i, p.Part, run, why)
			}
			if p.Kind != 0 {
				kept = append(kept, p.Part)
			}
		}
		if at != len(src) {
			t.Errorf("the pieces end at %d, the reply at %d", at, len(src))
		}
		if images > max(maxImages, 0) {
			t.Errorf("%d image parts, the limit is %d", images, maxImages)
		}
		if len(kept) != len(parts) || (len(kept) > 0 && !reflect.DeepEqual(kept, parts)) {
			t.Errorf("Split returned %+v, the pieces hold %+v", parts, kept)
		}
		if why := checkParse(src); why != "" {
			t.Error(why)
		}
		if why := checkBetweenBlocks(src, pieces); why != "" {
			t.Error(why)
		}
		if t.Failed() {
			t.Logf("Split(%q, %d)", src, maxImages)
		}
	})
}

// checkDropped checks a run that no part keeps.
func checkDropped(p piece, run string) string {
	switch {
	case p.Part != Part{}:
		return "a piece with no kind holds something"
	case run == "":
		return "an empty run with no part"
	case strings.Trim(run, " \t\r\n") != "":
		return "text was dropped"
	}
	return ""
}

// checkText checks a Text part against its run.
func checkText(p piece, run string) string {
	switch {
	case p.Text != run:
		return "the text is not its run of the reply"
	case strings.Trim(run, " \t\r\n") == "":
		return "a text part with nothing to see"
	case strings.TrimRight(run, " \t\r\n") != run:
		return "the text ends in blank"
	case strings.Trim(strings.SplitN(run, "\n", 2)[0], " \t\r") == "":
		return "the text starts with a blank line"
	case p.Lang != "" || p.Alt != "" || p.Path != "":
		return "a text part with another kind's fields"
	}
	// Whole top-level blocks read the same alone as in the reply, so a
	// fence left in the text is one that Split did not find.
	for _, n := range alone(run) {
		if n.Kind() == ast.KindFencedCodeBlock {
			return "a top-level fence was left in the text"
		}
	}
	return ""
}

// plain reads Markdown with goldmark as it comes.
func plain(src string) ast.Node {
	p := parser.NewParser(
		parser.WithBlockParsers(parser.DefaultBlockParsers()...),
		parser.WithInlineParsers(parser.DefaultInlineParsers()...),
		parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
	)
	return p.Parse(text.NewReader([]byte(src)))
}

// alone reads a run of the reply by itself, with goldmark as it comes, and
// returns its top-level blocks.
func alone(run string) []ast.Node {
	var blocks []ast.Node
	for n := plain(run).FirstChild(); n != nil; n = n.NextSibling() {
		blocks = append(blocks, n)
	}
	return blocks
}

// outline writes a tree as it was read: every node's kind, a block's lines,
// and what a text, a link, an image or a fence holds.
func outline(sb *strings.Builder, n ast.Node, src []byte, depth int) {
	fmt.Fprintf(sb, "%*s%s", depth, "", n.Kind())
	if n.Type() == ast.TypeBlock {
		lines := n.Lines()
		for i := range lines.Len() {
			seg := lines.At(i)
			fmt.Fprintf(sb, " %d:%d+%d", seg.Start, seg.Stop, seg.Padding)
		}
	}
	switch v := n.(type) {
	case *ast.Text:
		fmt.Fprintf(sb, " %q", v.Segment.Value(src))
	case *ast.Link:
		fmt.Fprintf(sb, " to %q", v.Destination)
	case *ast.Image:
		fmt.Fprintf(sb, " of %q", v.Destination)
	case *ast.FencedCodeBlock:
		if v.Info != nil {
			fmt.Fprintf(sb, " info %q", v.Info.Segment.Value(src))
		}
	}
	sb.WriteByte('\n')
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		outline(sb, c, src, depth+1)
	}
}

// checkParse checks that noting where blocks open changes nothing about how
// a reply is read: the tree is the one goldmark gives without the notes.
func checkParse(src string) string {
	raw := []byte(src)
	doc, _ := parse(raw)
	var got, want strings.Builder
	outline(&got, doc, raw, 0)
	outline(&want, plain(src), raw, 0)
	if got.String() != want.String() {
		return fmt.Sprintf("parse read the reply as\n%sand goldmark alone reads it as\n%s", got.String(), want.String())
	}
	return ""
}

// checkBetweenBlocks checks that every cut falls between two top-level
// blocks: the runs, read one at a time, hold the blocks the whole reply
// holds. A cut inside a block would make two blocks of it.
//
// It says nothing of a reply with a carriage return or with blanks at the
// end of a line. Split drops the blanks that end a part, and goldmark reads
// a few lines another way without them ("<a>" with a tab after it is a
// paragraph, and with none an HTML block).
func checkBetweenBlocks(src string, pieces []piece) string {
	if strings.Contains(src, "\r") {
		return ""
	}
	for row := range strings.SplitSeq(src, "\n") {
		if strings.TrimRight(row, spaces) != row {
			return ""
		}
	}
	// goldmark drops a one-letter word after a fence on a last line with no
	// line break, so every run is read with one.
	whole := func(run string) string {
		if run == "" || strings.HasSuffix(run, "\n") {
			return run
		}
		return run + "\n"
	}
	// A paragraph of nothing but link definitions leaves an empty text
	// block behind. It is not counted: definitions above a lone image are
	// in its paragraph in the reply, and in a run of their own once cut.
	kinds := func(to []string, run string) []string {
		for _, n := range alone(whole(run)) {
			if n.Kind() != ast.KindTextBlock {
				to = append(to, n.Kind().String())
			}
		}
		return to
	}
	var got []string
	for _, p := range pieces {
		got = kinds(got, src[p.start:p.end])
	}
	want := kinds(nil, src)
	if !slices.Equal(got, want) {
		return fmt.Sprintf("the runs hold the blocks %v, the reply holds %v", got, want)
	}
	return ""
}

// checkCode checks a Code part against its run: an opening fence, the part's
// lines as written but for their indentation, and at most a closing fence.
func checkCode(p piece, run string) string {
	if p.Alt != "" || p.Path != "" {
		return "a code part with an image's fields"
	}
	if blocks := alone(run); len(blocks) != 1 || blocks[0].Kind() != ast.KindFencedCodeBlock {
		return "the run is not one fenced block"
	}
	rows := strings.SplitAfter(run, "\n")
	if rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	if len(rows) == 0 {
		return "no fence"
	}
	opening := strings.TrimLeft(rows[0], " ")
	char := opening[:min(1, len(opening))]
	if char != "`" && char != "~" {
		return "the run does not start with a fence"
	}
	info := strings.TrimLeft(opening, char)
	length := len(opening) - len(info)
	if length < 3 {
		return "the opening fence is shorter than three"
	}
	if words := strings.FieldsFunc(info, func(r rune) bool { return strings.ContainsRune(" \t\r\n\v\f", r) }); len(words) > 0 {
		if words[0] != p.Lang {
			return "the language is not the first word after the fence"
		}
	} else if p.Lang != "" {
		return "a language from a bare fence"
	}
	body := rows[1:]
	if n := len(body); n > 0 && closes(body[n-1], char, length) {
		body = body[:n-1]
	}
	var got []string
	if len(body) > 0 {
		got = strings.Split(p.Text, "\n")
	} else if p.Text != "" {
		return "code from an empty fence"
	}
	if len(got) != len(body) {
		return "the code has a different number of lines from the fence"
	}
	for i, row := range body {
		if bare(row) != bare(got[i]) {
			return "a line of code is not the line between the fences"
		}
	}
	return ""
}

// closes reports whether row closes a fence of length char bytes.
func closes(row, char string, length int) bool {
	fence := strings.TrimLeft(row, " ")
	rest := strings.TrimLeft(fence, char)
	return len(row)-len(fence) < 4 && len(fence)-len(rest) >= length && strings.Trim(rest, " \t\r\n") == ""
}

// bare is a line without its indentation and its line break.
func bare(row string) string {
	return strings.TrimLeft(strings.TrimRight(row, "\r\n"), " \t")
}

// checkImage checks an Image part. One that follows its text stands for nothing;
// one that replaced a paragraph stands for an image and nothing else.
func checkImage(p piece, run string) string {
	switch {
	case p.Text != "" || p.Lang != "":
		return "an image part with another kind's fields"
	case strings.ContainsAny(p.Path, ":?#\\") || !fs.ValidPath(p.Path) || p.Path == "." || len(p.Path) > 1024:
		return "the path is not a plain relative path"
	case strings.ContainsAny(p.Alt, "\r\n"):
		return "the alternative text has a line break"
	case run == "":
		return ""
	case !strings.HasPrefix(strings.TrimSpace(run), "!["):
		return "the run it replaced does not start with an image"
	case !strings.HasSuffix(strings.TrimSpace(run), ")") && !strings.HasSuffix(strings.TrimSpace(run), "]"):
		return "the run it replaced does not end with an image"
	}
	blocks := alone(run)
	if len(blocks) != 1 || blocks[0].Kind() != ast.KindParagraph {
		return "the run it replaced is not one paragraph"
	}
	// A reference-style image is words when read without its definition.
	if img, ok := blocks[0].FirstChild().(*ast.Image); ok {
		if path, _ := Fetchable(destination(img)); blocks[0].ChildCount() != 1 || path != p.Path {
			return "the run it replaced is not that image alone"
		}
	}
	return ""
}

// The replies the fuzz starts from are read as goldmark reads them, and are
// cut between blocks. The fuzz checks the same of whatever it makes up.
func TestSeedsAreReadAsGoldmarkReadsThemAndCutBetweenBlocks(t *testing.T) {
	t.Parallel()
	for _, src := range seeds() {
		if why := checkParse(src); why != "" {
			t.Errorf("%q: %s", src, why)
		}
		if why := checkBetweenBlocks(src, cut(src, 4)); why != "" {
			t.Errorf("%q: %s", src, why)
		}
	}
}

// A cut inside a block is told from one between blocks.
func TestCheckBetweenBlocksSeesACutInsideABlock(t *testing.T) {
	t.Parallel()
	src := "- one\n- two\n\nafter\n"
	between := []piece{{start: 0, end: 13}, {start: 13, end: len(src)}}
	inside := []piece{{start: 0, end: 6}, {start: 6, end: len(src)}}
	if why := checkBetweenBlocks(src, between); why != "" {
		t.Errorf("a cut between the list and the paragraph: %s", why)
	}
	if why := checkBetweenBlocks(src, inside); why == "" {
		t.Error("a cut between two items of one list passed for a cut between blocks")
	}
}

// A block the parser gave no place for, or a place that cannot be right,
// stays in the text, and its image still follows.
func TestBlocksThatCannotBeFoundStayText(t *testing.T) {
	t.Parallel()
	src := "see ![a](a.png) here\n\n```go\nx := 1\n```\n\nafter\n"
	whole := []piece{
		{Part: Part{Kind: Text, Text: strings.TrimRight(src, "\n")}, start: 0, end: len(src) - 1},
		{start: len(src) - 1, end: len(src)},
		{Part: Part{Kind: Image, Alt: "a", Path: "a.png"}, start: len(src), end: len(src)},
	}
	for _, tc := range []struct {
		name string
		// place returns where the parser is said to have met a block.
		place func(found int) (int, bool)
	}{
		{name: "no block has a place", place: func(int) (int, bool) { return 0, false }},
		{name: "every block is placed on the first line", place: func(int) (int, bool) { return 0, true }},
		{name: "every block is placed at the end", place: func(int) (int, bool) { return len(src), true }},
		{name: "every block is placed a line late", place: func(found int) (int, bool) { return lineEnd(src, found+1), true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc, found := parse([]byte(src))
			wrong := &marks{start: map[ast.Node]int{}, end: found.end}
			for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
				if at, ok := tc.place(found.start[n]); ok {
					wrong.start[n] = at
				}
			}
			if got := assemble(src, doc, wrong, 4); !reflect.DeepEqual(got, whole) {
				t.Errorf("assemble:\n%+v\nwant:\n%+v", got, whole)
			}
		})
	}
}

// A fence with a block after it and no closing line on record was not read
// the way Split assumes, so it is left as text.
func TestAFenceWithNoKnownEndStaysText(t *testing.T) {
	t.Parallel()
	src := "before\n\n```go\nx := 1\n```\n\nafter"
	doc, found := parse([]byte(src))
	got := assemble(src, doc, &marks{start: found.start}, 4)
	want := []piece{{Part: Part{Kind: Text, Text: src}, start: 0, end: len(src)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("assemble:\n%+v\nwant:\n%+v", got, want)
	}
}

// A paragraph whose lines are not whole lines of the reply cannot be
// replaced by its image: the words beside it would be lost.
func TestAnImageParagraphThatIsNotWholeLinesStaysText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		src        string
		start, end int // the lines the paragraph is said to hold
	}{
		{name: "words before", src: "word ![a](a.png)", start: 5, end: 16},
		{name: "words after", src: "![a](a.png) word", start: 0, end: 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			img := ast.NewImage(ast.NewLink())
			img.Destination = []byte("a.png")
			para := ast.NewParagraph()
			para.Lines().Append(text.NewSegment(tc.start, tc.end))
			para.AppendChild(para, img)
			doc := ast.NewDocument()
			doc.AppendChild(doc, para)

			got := assemble(tc.src, doc, &marks{}, 4)
			want := []piece{
				{Part: Part{Kind: Text, Text: tc.src}, start: 0, end: len(tc.src)},
				{Part: Part{Kind: Image, Path: "a.png"}, start: len(tc.src), end: len(tc.src)},
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("assemble:\n%+v\nwant:\n%+v", got, want)
			}
		})
	}
}

// The parser's notes are what tell an empty fence from the next one, and a
// fence in a list from one at the top level.
func TestWhereBlocksStart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		src  string
		want []string // the first line of each top-level block that has a place
	}{
		{name: "a paragraph", src: "one\ntwo\n\nthree", want: []string{"one", "three"}},
		{name: "empty fences", src: "```\n```\n~~~\n~~~", want: []string{"```", "~~~"}},
		{name: "a fence in a list", src: "- ```\n  ```\n```\n```", want: []string{"- ```", "```"}},
		{name: "an underlined heading", src: "a\n\nTitle\n===\nb", want: []string{"a", "Title", "b"}},
		{name: "a rule and a heading", src: "***\n# h\n> q", want: []string{"***", "# h", "> q"}},
		{name: "definitions have no place", src: "[r]: x\n\nfoo", want: []string{"foo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc, at := parse([]byte(tc.src))
			var got []string
			for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
				if s, ok := at.start[n]; ok {
					s = lineStart(tc.src, s)
					got = append(got, strings.TrimRight(tc.src[s:lineEnd(tc.src, s+1)], "\n"))
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("blocks start at %q, want %q", got, tc.want)
			}
		})
	}
}
