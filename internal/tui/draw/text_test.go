package draw_test

import (
	"testing"

	"github.com/quinnovator/contactd/internal/tui/draw"
)

func TestTrimRight(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in, want string }{
		{"nothing to drop", "words", "words"},
		{"empty", "", ""},
		{"spaces", "words   ", "words"},
		{"only spaces", "      ", ""},
		{"spaces at the start stay", "  words  ", "  words"},
		{"a style that is closed", "\x1b[1mbold\x1b[m   ", "\x1b[1mbold\x1b[m"},
		{"spaces inside a style", "\x1b[1mbold   \x1b[m", "\x1b[1mbold\x1b[m"},
		{"styled spaces, one at a time", "│ \x1b[38;2;1;2;3mquote\x1b[m\x1b[38;2;1;2;3m \x1b[m\x1b[38;2;1;2;3m \x1b[m\x1b[38;2;1;2;3m\x1b[m", "│ \x1b[38;2;1;2;3mquote\x1b[m"},
		{"a row that is only a style", "\x1b[38;2;47;55;62m\x1b[m      ", ""},
		{"a style that opens at the end", "words \x1b[1m  ", "words"},
		{"a link's end is not a style", "\x1b]8;;https://example.com\x07link\x1b]8;;\x07\x1b[m  ", "\x1b]8;;https://example.com\x07link\x1b]8;;\x07"},
		{"a style before a link's end", "\x1b[4m\x1b]8;;https://example.com\x07link\x1b]8;;\x07\x1b[m  ", "\x1b[4m\x1b]8;;https://example.com\x07link\x1b]8;;\x07\x1b[m"},
		{"an m that ends a word", "\x1b[1mhmm", "\x1b[1mhmm"},
		{"a sequence that is not a style", "words\x1b[2K  ", "words\x1b[2K"},
	}
	for _, c := range cases {
		if got := draw.TrimRight(c.in); got != c.want {
			t.Errorf("%s: TrimRight(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestLinks(t *testing.T) {
	t.Parallel()
	const (
		on    = "\x1b]8;id=7;https://example.com/a\x07"
		other = "\x1b]8;;https://example.com/b\x1b\\"
		off   = "\x1b]8;;\x07"
	)
	cases := []struct {
		name     string
		in, want []string
	}{
		{"no links", []string{"one", "two"}, []string{"one", "two"}},
		{"a link on one line", []string{"see " + on + "this" + off + " now"}, []string{"see " + on + "this" + off + " now"}},
		{
			"a link over two lines",
			[]string{"see " + on + "this", "and that" + off + " now"},
			[]string{"see " + on + "this" + off, on + "and that" + off + " now"},
		},
		{
			"a link over three lines, with a blank one",
			[]string{on + "one", "two", "", "three" + off, "four"},
			[]string{on + "one" + off, on + "two" + off, "", on + "three" + off, "four"},
		},
		{
			"two links, the second left open",
			[]string{on + "one" + off + " " + other + "two", "three"},
			[]string{on + "one" + off + " " + other + "two" + off, other + "three" + off},
		},
		{
			"a link that ends where the line does",
			[]string{on + "one" + off, "two"},
			[]string{on + "one" + off, "two"},
		},
		{
			"half a sequence changes nothing",
			[]string{"one \x1b]8;;https://example.com", "two"},
			[]string{"one \x1b]8;;https://example.com", "two"},
		},
	}
	for _, c := range cases {
		equal(t, c.name, draw.Links(c.in), c.want)
	}
}
