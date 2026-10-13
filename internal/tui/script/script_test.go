package script

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadSaysProseWithoutItsMarks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, src, want string }{
		{"plain", "Hello there.", "Hello there."},
		{"soft break", "one\ntwo", "one two"},
		{"paragraphs", "one\n\n\n\ntwo", "one\n\ntwo"},
		{"space at the ends", "  \n\n  one  \n\n", "one"},
		{"emphasis", "a **bold** and *slanted* and __both__ word", "a bold and slanted and both word"},
		{"snake case", "call read_all_lines now", "call read_all_lines now"},
		{"underscore emphasis", "this is _fine_ too", "this is fine too"},
		{"a star that is a star", "3 * 4 and 5*3", "3 * 4 and 5*3"},
		{"strike", "~~gone~~ and ~5", "gone and ~5"},
		{"code span", "run `go test ./...` twice", "run go test ./... twice"},
		{"marks in a code span", "the glob `**/*.go` matches", "the glob **/*.go matches"},
		{"two backticks", "a ``b`c`` d", "a b`c d"},
		{"an unclosed backtick", "a ` b", "a b"},
		{"link", "see [the docs](https://example.com/a_b) now", "see the docs now"},
		{"link with emphasis", "[**bold** text](x)", "bold text"},
		{"reference link", "see [the docs][1] now", "see the docs now"},
		{"brackets that are no link", "item [1] and [x", "item [1] and [x"},
		{"address", "at <https://example.com> or a < b", "at https://example.com or a < b"},
		{"escape", `\*not\* emphasis`, "*not* emphasis"},
		{"heading", "# Title\ntext", "Title\n\ntext"},
		{"closed heading", "## Title ##\n\ntext", "Title\n\ntext"},
		{"a heading that ends in a mark", "# C#", "C#"},
		{"no heading", "#hashtag", "#hashtag"},
		{"bullets", "list:\n- one\n* two\n+ three\nafter", "list:\n• one\n• two\n• three\nafter"},
		{"numbers", "1. one\n2) two", "1. one\n2) two"},
		{"a number that is no item", "3.14 is pi\n2024 was long", "3.14 is pi 2024 was long"},
		{"quote", "> quoted\n> - item", "quoted\n• item"},
		{"rule", "one\n\n---\n\ntwo\n***\nthree", "one\n\ntwo\n\nthree"},
		{"definition", "[a]: https://example.com \"A\"\n[b]: /b\ntext", "text"},
		{"a bracket and a colon that define nothing", "[note]: this one is said", "[note]: this one is said"},
		{"rows that are no table", "| a | b |\nafter", "| a | b |\nafter"},
		{"an image that is no file", "look ![a chart](https://example.com/x.png) here", "look a chart here"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Read(tc.src, true, 4)
			if got.Text != tc.want {
				t.Errorf("Read(%q) says\n%q, want\n%q", tc.src, got.Text, tc.want)
			}
			if len(got.Shows) != 0 {
				t.Errorf("Read(%q) shows %+v, want nothing", tc.src, got.Shows)
			}
		})
	}
}

func TestReadShowsWhatIsNotProse(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"Here is the fix:",
		"",
		"```go",
		"x := 1",
		"  y := 2",
		"```",
		"",
		"And the numbers ![latency by day](plots/latency.png \"Top  Right\") speak:",
		"",
		"| day | ms |",
		"|-----|----|",
		"| Thu | 91 |",
		"Done.",
	}, "\n")
	got := Read(src, true, 4)
	if want := "Here is the fix:\n\nAnd the numbers speak:\n\nDone."; got.Text != want {
		t.Errorf("said %q, want %q", got.Text, want)
	}
	want := []Show{
		{Kind: Code, At: 16, N: 1, Text: "x := 1\n  y := 2", Lang: "go"},
		{Kind: Image, At: 33, N: 1, Path: "plots/latency.png", Alt: "latency by day", Place: "top right"},
		{Kind: Table, At: 40, N: 1, Text: "| day | ms |\n|-----|----|\n| Thu | 91 |"},
	}
	if len(got.Shows) != len(want) {
		t.Fatalf("shows %+v, want %+v", got.Shows, want)
	}
	for i := range want {
		if got.Shows[i] != want[i] {
			t.Errorf("show %d is %+v, want %+v", i, got.Shows[i], want[i])
		}
	}
}

func TestReadCountsTheImagesItMayShow(t *testing.T) {
	t.Parallel()
	got := Read("![one](a.png) ![two](b.png) ![three](c.png)", true, 2)
	if len(got.Shows) != 2 || got.Shows[1].N != 2 || got.Shows[1].Path != "b.png" {
		t.Errorf("shows %+v, want the first two images", got.Shows)
	}
	if got.Text != "three" {
		t.Errorf("said %q, want the description of the third", got.Text)
	}
}

func TestReadFencesKeepTheirShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, src, code, lang string }{
		{"tildes", "~~~ python extra\na\n~~~", "a", "python"},
		{"a longer fence", "````\n```\ninner\n```\n````", "```\ninner\n```", ""},
		{"indented", "- item\n\n   ```sh\n   ls\n     -l\n   ```", "ls\n  -l", "sh"},
		{"never closed", "```\nto the end\n", "to the end", ""},
		{"empty", "```\n```", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Read(tc.src, true, 4)
			if len(got.Shows) != 1 || got.Shows[0].Kind != Code {
				t.Fatalf("shows %+v, want one code block", got.Shows)
			}
			if s := got.Shows[0]; s.Text != tc.code || s.Lang != tc.lang {
				t.Errorf("the block is %q in %q, want %q in %q", s.Text, s.Lang, tc.code, tc.lang)
			}
		})
	}
	// Three backticks with a backtick after them are a code span.
	if got := Read("```js` is how a fence opens", true, 4); len(got.Shows) != 0 {
		t.Errorf("shows %+v, want none", got.Shows)
	}
}

// What is held back while a reply is being written, because its end could
// still change how it reads.
func TestReadHoldsBackWhatMayChange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, src, want string }{
		{"a word", "Hel", "Hel"},
		{"a space", "Hello ", "Hello"},
		{"a mark that may open a list", "one\n-", "one"},
		{"a list item", "one\n- t", "one\n• t"},
		{"a rule being written", "one\n\n--", "one"},
		{"stars that may be a rule", "one\n* *", "one"},
		{"a number", "one\n12", "one"},
		{"a number and its stop", "one\n12.", "one"},
		{"a numbered item", "one\n12. t", "one\n12. t"},
		{"a heading mark", "##", ""},
		{"a heading", "## T", "T"},
		{"emphasis at the end", "a **", "a"},
		{"an open link", "see [the docs](https://exa", "see"},
		{"a bracket", "see [the", "see"},
		{"a closed bracket", "see [1]", "see"},
		{"an open code span", "run `go te", "run"},
		{"an open fence", "code:\n```go\nx := 1\n", "code:"},
		{"a fence being opened", "code:\n``", "code:"},
		{"a table", "t:\n| a |\n|---|\n| 1 |\n", "t:"},
		{"a definition being written", "[a]: htt", ""},
		{"an image mark", "look !", "look"},
		{"a backslash", `a \`, "a"},
		{"an angle bracket", "a <htt", "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Read(tc.src, false, 4)
			if got.Text != tc.want {
				t.Errorf("Read(%q) says %q while it is written, want %q", tc.src, got.Text, tc.want)
			}
			if len(got.Shows) != 0 {
				t.Errorf("Read(%q) shows %+v while it is written, want nothing yet", tc.src, got.Shows)
			}
		})
	}
}

// replies are replies as models write them, for the tests that read each one
// at every length.
func replies() []string {
	return []string{
		"Hello! I'm **Ada** — and *this* is `me`, thinking out loud.\n\nWhy springs, and not keyframes? Because a spring never snaps.",
		"Two things overlapped:\n\n- Turn 6 ran three model calls with tool runs in between.\n- The turn after it re-read the tool results uncached.\n\nThe fix is small. Put the time note after the tool steps:\n\n```go\nliveMsgs = append(liveMsgs, toolSteps...)\nliveMsgs = append(liveMsgs, timeNote(now))\n```\n\nThat is all.",
		"# Results\n\n1. First, see [the plot](plots/a.png).\n2. Then ![latency](latency.png \"right\") and ![cost](cost.png 'left').\n\n| day | ms |\n|-----|----|\n| Thu | 91 |\n\n> A quote with _emphasis_ and a\n> second line.\n\n---\n\n[1]: https://example.com \"Example\"\n[note]: not a definition at all\nDone ~~for now~~ \\*really\\*.",
		"* * *\n** bold start** and __init__ and snake_case_name and 5*3=15 and a * b\n+ plus item\n- - -\n-5 degrees, 3.14, 2024) no. 12. yes\n``` not a fence ` here\n~~~\ntilde fence\n~~~\ntrailing ``code`` and `unclosed",
		"<https://example.com> and <br> and a < b > c\n![](empty.png)![alt only][ref] [text][ref] [open\n| not | a table |\nline\n|a|\n|-|\n",
		"```\nunclosed fence\nwith [links](x) and **marks**",
		"line one  \nline two\\\n\n\n  indented text\n\t- tabbed item\n>> nested quote\n>\n> # quoted heading #\n",
	}
}

// What Read says of a reply that only grew begins with what it said before,
// and what it showed is still shown, wherever the reply is cut and whether
// or not the longer one is finished.
func TestReadNeverTakesBackWhatItSaid(t *testing.T) {
	t.Parallel()
	for _, src := range replies() {
		whole := Read(src, true, 2)
		last := Script{}
		for end := range len(src) + 1 {
			if !utf8.ValidString(src[:end]) {
				continue
			}
			got := Read(src[:end], false, 2)
			if !grows(last, got) {
				t.Fatalf("after %q the reply reads\n%q %+v, which takes back\n%q %+v", src[:end], got.Text, got.Shows, last.Text, last.Shows)
			}
			if !grows(got, whole) {
				t.Fatalf("the whole of %q reads\n%q %+v, which takes back what %q read as\n%q %+v", src, whole.Text, whole.Shows, src[:end], got.Text, got.Shows)
			}
			last = got
		}
	}
}

// grows reports whether b says and shows everything a does, in a's place.
func grows(a, b Script) bool {
	if !strings.HasPrefix(b.Text, a.Text) || len(b.Shows) < len(a.Shows) {
		return false
	}
	for i, s := range a.Shows {
		if b.Shows[i] != s {
			return false
		}
	}
	return true
}

func TestReadSaysNoSpaceItWasNotAskedFor(t *testing.T) {
	t.Parallel()
	for _, src := range replies() {
		for _, whole := range []bool{false, true} {
			text := Read(src, whole, 2).Text
			if text != strings.TrimSpace(text) || strings.Contains(text, "\n\n\n") || strings.Contains(text, "  ") {
				t.Errorf("Read(%q, %v) says %q, which has space to spare", src, whole, text)
			}
		}
	}
}

func FuzzReadNeverTakesBackWhatItSaid(f *testing.F) {
	for _, src := range replies() {
		f.Add(src, len(src)/2)
	}
	f.Fuzz(func(t *testing.T, src string, cut int) {
		if cut < 0 || cut > len(src) || !utf8.ValidString(src) || !utf8.ValidString(src[:cut]) {
			return
		}
		short := Read(src[:cut], false, 2)
		for _, whole := range []bool{false, true} {
			if long := Read(src, whole, 2); !grows(short, long) {
				t.Fatalf("%q reads\n%q %+v, which takes back what %q read as\n%q %+v", src, long.Text, long.Shows, src[:cut], short.Text, short.Shows)
			}
		}
	})
}
