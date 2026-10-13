package draw_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/draw"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// referenceAttacks are replies that write, as character references, what a
// reply may not hold: the escape character, a bidi override, the character a
// terminal draws another program's image with. Each is written every way the
// Markdown renderer reads one, in every place it reads them. want is text the
// reply must still show, as it was written.
func referenceAttacks() map[string]struct{ src, want string } {
	return map[string]struct{ src, want string }{
		"decimal":               {"a &#27;[31mred&#27;[0m b", "&#27;[31mred&#27;[0m"},
		"hexadecimal":           {"a &#x1b;[2J b &#X1B;[2J", "&#x1b;[2J"},
		"a window title":        {"a &#27;]0;title&#7; b", "&#27;]0;title&#7;"},
		"no semicolon":          {"a &#27[2J b &#x1b[2J", "&#27[2J"},
		"leading zeros":         {"a &#0000000000000000027;[2J b", "&#0000000000000000027;[2J"},
		"digits that overflow":  {"a &#4294967323;[2J b &#x10000001b;[2J", "&#4294967323;[2J"},
		"a C1 control":          {"a &#x9b;31m b &#x90;q &#x81; &#129;", "&#x90;q"},
		"a C0 control":          {"a &#7; b &#8; c &#13; d &#127;", "&#7;"},
		"a bidi override":       {"a &#x202e;evil&#x202c; b &#8238;", "&#x202e;evil"},
		"a bidi isolate":        {"a &#x2066;evil&#x2069; b", "&#x2066;evil"},
		"an image placeholder":  {"a &#x10EEEE;&#x305;&#x305; b", "&#x10EEEE;"},
		"a heading":             {"# h &#27;[2J", "&#27;[2J"},
		"emphasis":              {"*&#27;[2J* **&#27;[2J** ***&#27;[2J***", "&#27;[2J"},
		"struck":                {"~~&#27;[2J~~", "&#27;[2J"},
		"a code span":           {"`&#27;[2J`", "&#27;[2J"},
		"a quote":               {"> &#27;[2J", "&#27;[2J"},
		"a list":                {"- &#27;[2J\n  - &#27;[2J\n\n1. &#27;[2J", "&#27;[2J"},
		"a task":                {"- [ ] &#27;[2J", "&#27;[2J"},
		"a table":               {"| a&#27;[2J |\n| - |\n| b&#27;[2J |", "a&#27;[2J"},
		"a definition":          {"term&#27;[2J\n: &#27;[2J", "&#27;[2J"},
		"link text":             {"[a&#27;[2J](https://example.com)", "a&#27;"},
		"a link's address":      {"[a](https://example.com/&#27;[2J)", ""},
		"a link's title":        {"[a](https://example.com \"&#27;[2J\")", ""},
		"an address in angles":  {"<https://example.com/&#27;[2J>", ""},
		"a bare address":        {"see https://example.com/&#27;[2J now", ""},
		"an image's words":      {"words ![a&#27;b](https://example.com/y.png) words", ""},
		"a reference link":      {"[a][r] and\n\n```\nx\n```\n\n[b][r]\n\n[r]: https://example.com/&#27;[2J \"&#27;\"", ""},
		"an HTML block":         {"<div>&#27;[2J</div>", "&#27;[2J"},
		"HTML in a line":        {"a <b>&#27;[2J</b> c", "&#27;[2J"},
		"an HTML comment":       {"<!-- &#27;[2J -->\n\nwords &#27;[2J", "&#27;[2J"},
		"a fence in a list":     {"- item\n\n  ```&#27;[2J\n  &#27;[2J\n  ```", "&#27;[2J"},
		"indented code":         {"words\n\n    &#27;[2J", "&#27;[2J"},
		"escaped twice":         {"&amp;#27;[2J &amp;amp;#27;[2J", "#27;[2J"},
		"in two halves, struck": {"~~&\\#27;[2J~~ ~~&*#27;[2J*~~ ~~&#2\\7;[2J~~", "[2J"},
		"a harmless first half": {"~~&#0*27;[2J*~~ ~~&#00<b>27;[2J</b>~~ ~~&#x0*1b;[2J*~~ ~~&#6*5;*~~", "[2J"},
		"in two halves, code":   {"`&\n#27;[2J` and `&#2\n7;[2J`", "[2J"},
		"in two halves, HTML":   {"<div>&#2<b></b>7;[2J &<i></i>#27;[2J</div>", "[2J"},
		"in two halves, a line": {"&\\#27;[2J &#\\27;[2J &#2\\7;[2J &#2\n7;[2J &#2*7;[2J*", "[2J"},
		"a name":                {"a &zwj; b &Tab; c", "&Tab;"},
		"its own stand-in":      {"a &#xF0000; b &#27;[2J", "&#27;[2J"},
	}
}

func TestCharacterReferencesNeverReachALine(t *testing.T) {
	t.Parallel()
	// The Markdown renderer reads "&#27;" as the escape character. A reply is
	// made safe before it is rendered, so this is the one way a reply has to
	// write an escape sequence.
	for name, c := range referenceAttacks() {
		for _, l := range looks() {
			t.Run(name+"/"+l.name, func(t *testing.T) {
				t.Parallel()
				r := l.renderer(60)
				blocks := map[string][]string{
					"a reply":   r.Item(agent(1, c.src)),
					"a lead":    r.Item(&transcript.Item{Kind: transcript.ToolCall, Seq: 2, Tool: "python", Lead: c.src, Text: "x"}),
					"cut short": r.Item(&transcript.Item{Kind: transcript.Agent, Seq: 3, Text: c.src, Interrupted: true}),
				}
				for what, lines := range blocks {
					fits(t, what, lines, 60)
					inert(t, what, lines)
					all := strings.Join(stripped(lines), " ")
					if !strings.Contains(strings.ReplaceAll(all, " ", ""), strings.ReplaceAll(c.want, " ", "")) {
						t.Errorf("%s does not show %q as it was written: %q", what, c.want, all)
					}
					// Where the client writes no hyperlink, a reference must
					// not have written one either.
					if !strings.Contains(c.src, "https://") {
						harmless(t, what, lines)
					}
				}
				// The reply was rendered, not thrown away: the references
				// were disarmed before the renderer saw them.
				if got := r.Unsound(); got != 0 {
					t.Errorf("%d renderings held something a terminal would act on", got)
				}
			})
		}
	}
}

func TestJoinersFromReferences(t *testing.T) {
	t.Parallel()
	// Where cells are counted a character at a time a reply loses its
	// joiners, so that both ways of counting agree. A reference must not put
	// one back.
	const src = "a &zwj; b &#x200d; c &#xfe0f; d ☺&#xfe0f;☺&#xFE0F;☺&#65039;"
	got := strings.Join(trueDark().renderer(60).Item(agent(1, src)), "\n")
	if strings.ContainsAny(got, "\u200d\ufe0f") {
		t.Errorf("a joiner came out of a reference: %q", got)
	}
	// A terminal that counts a cluster at a time keeps them, written either
	// way.
	caps := trueDark().caps
	caps.Width = termcaps.WidthGrapheme
	r := draw.New(draw.Options{Theme: theme.New(caps, theme.Options{}), Width: 60})
	if got := strings.Join(r.Item(agent(1, "a &zwj; b")), "\n"); !strings.Contains(got, "a \u200d b") {
		t.Errorf("a joiner written as a reference is not drawn: %q", got)
	}
}

func TestAmpersandsReadAsWritten(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, src, want string }{
		{"an ampersand among words", "R&D at AT&T: a & b && c", "  R&D at AT&T: a & b && c"},
		{"references to characters that are drawn", "&amp; &lt;tag&gt; &copy; &#65;&#x42; &mdash; &hellip;", "  & <tag> © AB — …"},
		{"a reference with no semicolon is not one", "&lt and &amp and &#65 and &copy2", "  &lt and &amp and &#65 and &copy2"},
		{"not a reference", "&nosuchname; &; &# &#x &#;", "  &nosuchname; &; &# &#x &#;"},
		{"a code span", "`a && b` and `&x`", "  a && b and &x"},
		{"an address", "[q](https://example.com/?a=1&b=2)", "  q https://example.com/?a=1&b=2"},
		{"a heading", "# R&D", "  # R&D"},
		{"a table", "| a&b |\n| --- |\n| c&d |", "   a&b\n  ──────────────────────────────────────────────────────────\n   c&d"},
		{"struck", "~~R&D~~", "  R&D"},
		{"an HTML block", "<div>R&D &amp; more</div>", "  R&D & more"},
		{"a fence in a list", "- run\n\n  ```sh\n  make && ./run 2>&1\n  ```", "  • run\n  make && ./run 2>&1"},
		{"a reference that names a control stays as written", "&#27;[2J and &#x202e;", "  &#27;[2J and &#x202e;"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := trueDark().renderer(60)
			lines := r.Item(agent(1, c.src))
			fits(t, c.name, lines, 60)
			got := stripped(lines)
			for i, row := range got {
				got[i] = strings.TrimRight(row, " ")
			}
			if all := strings.Join(got, "\n"); all != c.want {
				t.Errorf("drawn as %q, want %q", all, c.want)
			}
			if r.Unsound() != 0 {
				t.Error("the rendering was thrown away")
			}
		})
	}

	// The address a link goes to is the one that was written.
	lines := trueDark().renderer(60).Item(agent(1, "[q](https://example.com/?a=1&b=2)"))
	if c := find(t, lines[0], "q")[0]; c.link != "https://example.com/?a=1&b=2" {
		t.Errorf("the link goes to %q", c.link)
	}
}

func TestShield(t *testing.T) {
	t.Parallel()
	const m = "\U000f0000"
	cases := []struct{ name, in, want string }{
		{"no ampersand", "plain words", "plain words"},
		{"alone", "a & b", "a " + m + " b"},
		{"at the end", "a &", "a " + m},
		{"two together", "a && b", "a " + m + m + " b"},
		{"a harmless reference is kept", "&amp; &lt; &#65; &#x41; &copy;", "&amp; &lt; &#65; &#x41; &copy;"},
		{"no semicolon", "&amp &#65 &lt", m + "amp " + m + "#65 " + m + "lt"},
		{"the escape character", "&#27; &#x1b; &#X1B; &#027;", m + "#27; " + m + "#x1b; " + m + "#X1B; " + m + "#027;"},
		{"digits that overflow", "&#4294967323;", m + "#4294967323;"},
		{"a control", "&#7; &#x81; &#127; &#13;", m + "#7; " + m + "#x81; " + m + "#127; " + m + "#13;"},
		// The renderer reads these as Windows-1252, where they are letters.
		{"a C1 number that names a character", "&#x9b; &#128;", "&#x9b; &#128;"},
		{"bidi", "&#x202e; &#x2066;", m + "#x202e; " + m + "#x2066;"},
		{"a placeholder", "&#x10eeee;", m + "#x10eeee;"},
		{"a joiner", "&zwj; &#x200d; &#xfe0f;", m + "zwj; " + m + "#x200d; " + m + "#xfe0f;"},
		{"a tab", "&Tab; &#9;", m + "Tab; " + m + "#9;"},
		{"not a name", "&nosuchname;", m + "nosuchname;"},
		{"an old name that is the start of the word", "&notit; &ampx;", "&notit; &ampx;"},
		{"decimal digits, then letters", "&#27abc; &#65abc;", m + "#27abc; &#65abc;"},
		{"a reference after an ampersand", "&&amp;", m + "&amp;"},
		{"the stand-in itself", "&#xf0000;", m + "#xf0000;"},
		{"a line break is kept", "&NewLine;", "&NewLine;"},
	}
	r := trueDark().renderer(60)
	for _, c := range cases {
		got, mark, ok := r.Shield(c.in)
		if !ok || got != c.want {
			t.Errorf("%s: Shield(%q) = %q, %v, want %q", c.name, c.in, got, ok, c.want)
		}
		if wantMark := strings.Contains(c.in, "&"); (mark == m) != wantMark {
			t.Errorf("%s: the stand-in is %q", c.name, mark)
		}
	}

	// Text that holds the first stand-in is given another, and one that
	// holds them all is not rendered.
	if got, mark, ok := r.Shield("a" + m + " & b"); !ok || mark != "\U000f0001" || got != "a"+m+" \U000f0001 b" {
		t.Errorf("Shield with the stand-in in the text = %q, %q, %v", got, mark, ok)
	}
	var all strings.Builder
	for c := rune(0xf0000); c <= 0xffffd; c++ {
		all.WriteRune(c)
	}
	if got := draw.StandIn(all.String()); got != "" {
		t.Errorf("a stand-in was found in text that holds them all: %q", got)
	}
	if _, _, ok := r.Shield(all.String() + "&"); ok {
		t.Error("text that holds every stand-in was shielded")
	}
	if _, _, ok := r.Shield(all.String()); !ok {
		t.Error("text with no ampersand needs no stand-in")
	}
	if got := draw.StandIn(strings.TrimSuffix(all.String(), "\U000ffffd")); got != "\U000ffffd" {
		t.Errorf("the last stand-in is %q", got)
	}
}

func TestSound(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"words", "plain words\nand more — 中文 👍", true},
		{"nothing", "", true},
		{"a style", "\x1b[1;38;2;1;2;3mbold\x1b[m \x1b[0m\x1b[38:5:7mx\x1b[m", true},
		{"a hyperlink ended by a bell", "\x1b]8;id=7;https://example.com/a?b=c&d\x07link\x1b]8;;\x07", true},
		{"a hyperlink ended by a string terminator", "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\", true},
		{"the screen cleared", "words \x1b[2J", false},
		{"the cursor moved", "\x1b[10;10Hwords", false},
		{"a window title", "\x1b]0;title\x07", false},
		{"a clipboard write", "\x1b]52;c;AAAA\x07", false},
		{"a hyperlink that does not end", "\x1b]8;;https://example.com", false},
		{"a control in a hyperlink", "\x1b]8;;https://example.com/\x9b\x07", false},
		{"a device control string", "\x1bPq#0~~\x1b\\", false},
		{"an image command", "\x1b_Gf=100;AAAA\x1b\\", false},
		{"an escape character alone", "words\x1b", false},
		{"a style cut short", "\x1b[1", false},
		{"a private mode", "\x1b[?1049h", false},
		{"a bell", "words\x07", false},
		{"a carriage return", "words\rmore", false},
		{"a C1 control", "words\u009b31m", false},
		{"a bidi override", "words\u202eevil", false},
		{"an image placeholder", "\U0010eeee\u0305", false},
		{"a joiner", "a\u200db", false},
		{"bytes that are not text", "words\xff", false},
		{"a control after a style", "\x1b[1mbold\x1b[m\x08", false},
	}
	r := trueDark().renderer(60)
	for _, c := range cases {
		if got := r.Sound(c.out); got != c.want {
			t.Errorf("%s: Sound(%q) = %v, want %v", c.name, c.out, got, c.want)
		}
	}
	if got := r.Unsound(); got == 0 {
		t.Error("what was thrown away was not counted")
	}
}

func FuzzReply(f *testing.F) {
	for _, c := range referenceAttacks() {
		f.Add(c.src)
	}
	f.Add(hostile)
	f.Add(frameReply)
	f.Add(strings.Repeat(">", 200) + "a")
	f.Add(strings.Repeat("- ", 200) + "a\n\n" + nestedByIndent(20))
	f.Add("> - a\n>\n>   ```\n>   x\n>   ```\n\n    code\n\n# h\n\n| a |\n| - |\n| b |\n")
	f.Add("&#" + strconv.Itoa(1<<32+27) + ";[2J ~~&\\#x1b;]0;t&#7;~~ `&\n#27;`")
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 1024 {
			return // the parser is quadratic on some input
		}
		for _, w := range []int{60, 9} {
			lines := trueDark().renderer(w).Item(agent(1, src))
			fits(t, "a reply", lines, w)
			inert(t, "a reply", lines)
		}
	})
}
