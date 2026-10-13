package inputbox_test

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"

	"github.com/quinnovator/contactd/internal/tui/inputbox"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// TestSpacesAreDrawnPlain pins what a space that is not U+0020 does. The
// text area draws each as one plain space, whatever its own width, so the
// cursor is counted after plain spaces too. An ideographic space, which an
// input method types, is two cells wide where it is drawn as itself.
func TestSpacesAreDrawnPlain(t *testing.T) {
	t.Parallel()
	for _, nt := range themes() {
		for _, sp := range wideSpaces() {
			t.Run(nt.name+"/"+strconv.QuoteToASCII(sp), func(t *testing.T) {
				t.Parallel()
				th := nt.th
				draft := "这" + sp + "这" + sp + "ab"
				m := inputbox.New(th, placehold)
				m.SetSize(40, 8, true)
				pasteOf(m, draft)
				if got := m.Value(); got != draft {
					t.Errorf("the draft is %q, want %q", got, draft)
				}
				if got, want := inside(th, m.Lines()[1], 40, true), th.G.Prompt+" 这 这 ab"; got != want {
					t.Errorf("the row is %q, want %q", got, want)
				}
				before, _, _ := frame(th, 40, true)
				left := th.Width(before + th.G.Prompt + " ")
				if x, y := m.Cursor(); x != left+8 || y != 1 {
					t.Errorf("at the end the cursor is at %d,%d, want %d,1", x, y, left+8)
				}
				if !check(t, m, th, 40, 8, true, "") {
					t.Error("the text area could not be asked where it would draw its cursor")
				}

				// After the first space: one wide character and one cell.
				press(m, tea.KeyHome, 0)
				pressN(m, 2, tea.KeyRight)
				if x, y := m.Cursor(); x != left+3 || y != 1 {
					t.Errorf("after the first space the cursor is at %d,%d, want %d,1", x, y, left+3)
				}
				check(t, m, th, 40, 8, true, "")

				// The ghost is drawn after the same plain spaces.
				m.SetValue("/a" + sp)
				m.SetGhost("usage")
				if got, want := inside(th, m.Lines()[1], 40, true), th.G.Prompt+" /a usage"; got != want {
					t.Errorf("with a ghost the row is %q, want %q", got, want)
				}
				if x, _ := m.Cursor(); x != left+3 {
					t.Errorf("with a ghost the cursor is in column %d, want %d", x, left+3)
				}
				check(t, m, th, 40, 8, true, "usage")

				// Whether a ghost fits is counted over the plain space too:
				// one that fills the row is drawn, one a cell longer is not.
				fits := strings.Repeat("g", roomAfterPrompt(th, size{40, 8, true})-3)
				m.SetGhost(fits)
				check(t, m, th, 40, 8, true, fits)
				m.SetGhost(fits + "g")
				check(t, m, th, 40, 8, true, "")
				if got, want := inside(th, m.Lines()[1], 40, true), th.G.Prompt+" /a"; got != want {
					t.Errorf("with a ghost a cell too long the row is %q, want %q", got, want)
				}
			})
		}
	}
}

// TestNotDrawn holds the box to never drawing a character that turns the
// direction of text around, nor the one kitty draws an image under. The
// draft keeps them, and the cursor is counted as if they were not there.
func TestNotDrawn(t *testing.T) {
	t.Parallel()
	for _, nt := range themes() {
		for _, c := range notDrawn() {
			t.Run(nt.name+"/"+strconv.QuoteToASCII(c), func(t *testing.T) {
				t.Parallel()
				th := nt.th
				draft := "ab" + c + "cd" + c
				m := inputbox.New(th, "Message "+c+"ada")
				m.SetSize(46, 8, false)
				if got, want := inside(th, m.Lines()[0], 46, false), th.G.Prompt+" Message ada"; got != want {
					t.Errorf("the placeholder row is %q, want %q", got, want)
				}
				check(t, m, th, 46, 8, false, "")

				pasteOf(m, draft)
				if got := m.Value(); got != draft {
					t.Errorf("the draft is %q, want %q", got, draft)
				}
				if text, send := m.Enter(); text != draft || !send {
					t.Errorf("Enter returned %q, %v, want the draft as written", text, send)
				}
				if got, want := inside(th, m.Lines()[0], 46, false), th.G.Prompt+" abcd"; got != want {
					t.Errorf("the row is %q, want %q", got, want)
				}
				if x, y := m.Cursor(); x != th.Width(th.G.Prompt+" ")+4 || y != 0 {
					t.Errorf("the cursor is at %d,%d, want %d,0", x, y, th.Width(th.G.Prompt+" ")+4)
				}
				check(t, m, th, 46, 8, false, "")

				m.SetGhost("us" + c + "age")
				if got, want := inside(th, m.Lines()[0], 46, false), th.G.Prompt+" abcdusage"; got != want {
					t.Errorf("with a ghost the row is %q, want %q", got, want)
				}
				check(t, m, th, 46, 8, false, "usage")
			})
		}
	}
}

// TestVisibleIsWhatSafeKeeps holds what the box leaves out of a row to what
// safe.Clean leaves out of a message, for text with no control character in
// it: a draft never holds one.
func TestVisibleIsWhatSafeKeeps(t *testing.T) {
	t.Parallel()
	texts := []string{
		"", "plain", cjkText, zwjFamily, skinTone, heart, "e\u0301", "a\u200bb\u200cc\u200ed\u200f", "\ufeffx\u2060y\u00ad",
		strings.Join(notDrawn(), "x"), "a\u202eb\u2066c\U0010eeeed\u200de\ufe0f",
	}
	for _, text := range texts {
		for _, wc := range []bool{true, false} {
			if got, want := inputbox.Visible(text, wc), safe.Clean(text, wc); got != want {
				t.Errorf("visible(%+q, %v) is %+q, and safe.Clean gives %+q", text, wc, got, want)
			}
		}
	}
	// Spaces are the one thing the box changes and safe.Clean does not.
	for _, sp := range wideSpaces() {
		if got := inputbox.Visible("a"+sp+"b", true); got != "a b" {
			t.Errorf("visible(%+q) is %+q, want %q", "a"+sp+"b", got, "a b")
		}
	}
}

// drawnText returns the draft as the box shows it, read off its lines: the
// text of every row, in order, without the prompt, the border and any space.
func drawnText(m *inputbox.Model, th *theme.Theme, s size) string {
	lines := m.Lines()
	if s.boxed {
		lines = lines[1 : len(lines)-1]
	}
	var b strings.Builder
	for i, l := range lines {
		text := strings.ReplaceAll(inside(th, l, s.width, s.boxed), " ", "")
		if i == 0 {
			text = strings.TrimPrefix(text, th.G.Prompt)
		}
		b.WriteString(text)
	}
	return b.String()
}

// TestEveryCharacterIsDrawn holds the box to showing all of a draft. The
// text area alone does not: it wraps a line a cell too late when a wide
// character follows a narrow one in the last cell of a row, and draws the
// row without that character.
func TestEveryCharacterIsDrawn(t *testing.T) {
	t.Parallel()
	whole := func(t *testing.T, m *inputbox.Model, th *theme.Theme, s size) {
		t.Helper()
		want := strings.NewReplacer(" ", "", "\n", "").Replace(drawnAs(th, m.Value()))
		if got := drawnText(m, th, s); got != want {
			t.Errorf("the box shows %q\nof the draft %q\n%q", got, want, plainLines(m))
		}
		check(t, m, th, s.width, s.maxRows, s.boxed, "")
	}

	// A run of narrow characters of every length, so that one of them ends
	// in each cell of a row, and wide characters after it.
	tails := []string{
		strings.Repeat("这", 40), strings.Repeat("这a", 30), strings.Repeat("😀", 20) + "b", "这",
		strings.Repeat(zwjFamily, 8), strings.Repeat(skinTone+"a", 12), " " + skinTone + " " + zwjFamily + heart, brokenRow,
	}
	for _, nt := range widthThemes() {
		for _, s := range []size{{40, 99, true}, {30, 99, false}, {13, 99, false}} {
			t.Run(nt.name+"/"+s.String(), func(t *testing.T) {
				t.Parallel()
				m := inputbox.New(nt.th, placehold)
				m.SetSize(s.width, s.maxRows, s.boxed)
				for n := range roomAfterPrompt(nt.th, s) + 3 {
					for _, tail := range tails {
						m.SetValue(strings.Repeat("x", n) + tail)
						whole(t, m, nt.th, s)
					}
				}
			})
		}
	}

	// The case itself, as numbers: 34 cells for text in a box 40 wide, 33
	// narrow characters and a wide one. The text area puts all of it on the
	// first row, which is 35 cells, and would draw the row without the wide
	// character. Wrapped to 33 cells, the wide character starts a row.
	t.Run("a wide character after a narrow one in the last cell", func(t *testing.T) {
		t.Parallel()
		th := unicodeTheme()
		m := inputbox.New(th, placehold)
		m.SetSize(40, 8, true)
		m.SetValue(strings.Repeat("x", 33))
		if got := m.WrapWidth(); got != 34 {
			t.Fatalf("the draft is wrapped to %d cells, want 34", got)
		}
		typed(m, "这好")
		if got := m.WrapWidth(); got != 33 {
			t.Errorf("with the wide character the draft is wrapped to %d cells, want 33", got)
		}
		want := []string{
			"╭" + strings.Repeat("─", 38) + "╮",
			"│ ❯ " + strings.Repeat("x", 33) + "  │",
			"│   这好" + strings.Repeat(" ", 31) + "│",
			"╰" + strings.Repeat("─", 38) + "╯",
		}
		if got := plainLines(m); !slices.Equal(got, want) {
			t.Errorf("the box is\n%q\nwant\n%q", got, want)
		}
		if x, y := m.Cursor(); x != 8 || y != 2 {
			t.Errorf("the cursor is at %d,%d, want 8,2", x, y)
		}
		// Before the wide characters the cursor is where they start.
		pressN(m, 2, tea.KeyLeft)
		if x, y := m.Cursor(); x != 4 || y != 2 {
			t.Errorf("before the wide characters the cursor is at %d,%d, want 4,2", x, y)
		}
		check(t, m, th, 40, 8, true, "")
	})

	t.Run("random", func(t *testing.T) {
		t.Parallel()
		all := themes()
		pieces := []string{"a", "b", "3", " ", " ", "这", "好", "，", "😀", "/", "\n", zwjFamily, skinTone, heart}
		rng := rand.New(rand.NewPCG(11, 5))
		for range 600 {
			var b strings.Builder
			for range 1 + rng.IntN(120) {
				b.WriteString(pieces[rng.IntN(len(pieces))])
			}
			nt := all[rng.IntN(len(all))]
			// The narrowest box here has room for a joined emoji of six
			// cells after the widest prompt.
			s := size{16 + rng.IntN(50), 999, rng.IntN(2) == 0}
			m := inputbox.New(nt.th, placehold)
			m.SetSize(s.width, s.maxRows, s.boxed)
			m.SetValue(b.String())
			whole(t, m, nt.th, s)
			if t.Failed() {
				t.Fatalf("theme %s, %s", nt.name, s)
			}
		}
	})
}

// TestWrapsNarrowerToFit pins what happens to a row that the text area
// finds to fit and the terminal does not: the draft is wrapped narrower,
// until every row fits. A thumb with a skin tone is two cells where a
// cluster is counted and four where a character is.
func TestWrapsNarrowerToFit(t *testing.T) {
	t.Parallel()
	// 16 letters, two thumbs and a word of two letters, with spaces: 25
	// cells by clusters, which is less than the 28 a box 30 wide has after
	// the prompt, and 29 by characters, which is one more.
	head := strings.Repeat("a", 16) + " " + skinTone + " " + skinTone
	draft := head + " bb"
	s := size{30, 8, false}

	t.Run("characters", func(t *testing.T) {
		t.Parallel()
		th := unicodeTheme()
		m := inputbox.New(th, placehold)
		m.SetSize(s.width, s.maxRows, s.boxed)
		if got := m.WrapWidth(); got != 28 {
			t.Fatalf("an empty box wraps to %d cells, want 28", got)
		}
		pasteOf(m, draft)
		// At 25 cells the last word no longer fits the first row.
		if got := m.WrapWidth(); got != 25 {
			t.Errorf("the draft is wrapped to %d cells, want 25", got)
		}
		want := []string{"❯ " + head + "  ", "  bb" + strings.Repeat(" ", 26)}
		if got := plainLines(m); !slices.Equal(got, want) {
			t.Errorf("the box is%s%q%swant%s%q", "\n", got, "\n", "\n", want)
		}
		if x, y := m.Cursor(); x != 4 || y != 1 {
			t.Errorf("the cursor is at %d,%d, want 4,1", x, y)
		}
		if got := m.TextRows(); got != 2 {
			t.Errorf("TextRows is %d, want 2", got)
		}
		check(t, m, th, s.width, s.maxRows, s.boxed, "")

		// Without the last word the draft fits, and has its width back.
		pressN(m, 3, tea.KeyBackspace)
		if got := m.WrapWidth(); got != 28 {
			t.Errorf("without the last word the draft is wrapped to %d cells, want 28", got)
		}
		if got := m.TextRows(); got != 1 {
			t.Errorf("TextRows is %d, want 1", got)
		}
		check(t, m, th, s.width, s.maxRows, s.boxed, "")
	})

	t.Run("clusters", func(t *testing.T) {
		t.Parallel()
		th := graphemeTheme()
		m := inputbox.New(th, placehold)
		m.SetSize(s.width, s.maxRows, s.boxed)
		pasteOf(m, draft)
		if got := m.WrapWidth(); got != 28 {
			t.Errorf("the draft is wrapped to %d cells, want 28", got)
		}
		want := []string{"❯ " + draft + "   "}
		if got := plainLines(m); !slices.Equal(got, want) {
			t.Errorf("the box is%s%q%swant%s%q", "\n", got, "\n", "\n", want)
		}
		check(t, m, th, s.width, s.maxRows, s.boxed, "")
	})

	// A space of any kind is drawn as one cell and counted as one: nine
	// wide characters with an ideographic space after each are 27 cells,
	// which fit the 28 of a box 30 wide, and the draft is left as it is.
	t.Run("wide spaces", func(t *testing.T) {
		t.Parallel()
		for _, nt := range themes()[:2] {
			m := inputbox.New(nt.th, placehold)
			m.SetSize(s.width, s.maxRows, s.boxed)
			pasteOf(m, strings.Repeat("这\u3000", 9))
			if got := m.WrapWidth(); got != 28 {
				t.Errorf("%s: the draft is wrapped to %d cells, want 28", nt.name, got)
			}
			if got := m.TextRows(); got != 1 {
				t.Errorf("%s: TextRows is %d, want 1", nt.name, got)
			}
			check(t, m, nt.th, s.width, s.maxRows, s.boxed, "")
		}
	})

	// A joined emoji of six cells in a box with room for four cannot be
	// shown. It is cut, the box keeps its width, and nothing hangs.
	t.Run("a cluster wider than the box", func(t *testing.T) {
		t.Parallel()
		for _, nt := range themes() {
			for width := range 9 {
				m := inputbox.New(nt.th, placehold)
				m.SetSize(width, 8, false)
				pasteOf(m, "a"+zwjFamily+zwjFamily+"b "+skinTone)
				check(t, m, nt.th, width, 8, false, "")
			}
		}
	})
}

// TestPlaceholderIsWhole draws placeholders the two ways of counting cells
// disagree about. The text area would wrap such a placeholder by its own
// count and show the first row of what it got.
func TestPlaceholderIsWhole(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, placeholder string }{
		{"emoji selectors", "Message " + strings.Repeat(heart, 14)},
		{"a joined emoji", "Message " + zwjFamily + " family"},
		{"a skin tone", "Message " + skinTone},
		{"cjk", "Message " + cjkText[:18]},
		{"spaces inside", "Message   two  spaces"},
		{"an ideographic space", "Message\u3000ada"},
	}
	for _, nt := range themes() {
		for _, boxed := range []bool{true, false} {
			for _, tc := range cases {
				t.Run(nt.name+"/"+boxName(boxed)+"/"+tc.name, func(t *testing.T) {
					t.Parallel()
					th := nt.th
					m := inputbox.New(th, tc.placeholder)
					m.SetSize(60, 8, boxed)
					border := 0
					if boxed {
						border = 1
					}
					if got, want := inside(th, m.Lines()[border], 60, boxed), th.G.Prompt+" "+drawnAs(th, tc.placeholder); got != want {
						t.Errorf("the row is %q, want %q", got, want)
					}
					check(t, m, th, 60, 8, boxed, "")
					// The placeholder is dim, and the prompt is the draft's.
					dim := th.Textarea().Focused.Placeholder.Inline(true).Render(drawnAs(th, tc.placeholder))
					if row := m.Lines()[border]; !strings.Contains(row, dim) {
						t.Errorf("the row %q does not hold the placeholder drawn dim, %q", row, dim)
					}
				})
			}
		}
	}
}

// TestNoPlaceholder draws an empty box that was given no placeholder.
func TestNoPlaceholder(t *testing.T) {
	t.Parallel()
	for _, nt := range themes() {
		m := inputbox.New(nt.th, "")
		m.SetSize(40, 8, true)
		if got, want := inside(nt.th, m.Lines()[1], 40, true), nt.th.G.Prompt; got != want {
			t.Errorf("%s: the row is %q, want %q", nt.name, got, want)
		}
		check(t, m, nt.th, 40, 8, true, "")
	}
}

// TestOutsideTextIsMadeSafe gives the box a placeholder and a ghost with
// escape sequences in them. A contact's label and a mod's usage line come
// from outside, and neither may reach the terminal as written.
func TestOutsideTextIsMadeSafe(t *testing.T) {
	t.Parallel()
	const hostile = "a\x1b]0;title\x07b\x1b[31mc\r\nd\te\u009b2Jf\x7f"
	for _, nt := range themes() {
		t.Run(nt.name, func(t *testing.T) {
			t.Parallel()
			th := nt.th
			clean := func(when string) {
				t.Helper()
				for i, l := range m0(t, th, hostile, when) {
					for _, bad := range []string{"\x1b]", "\x07", "\x1b[31m", "\u009b", "\x7f", "\t", "\r", "\n"} {
						if strings.Contains(l, bad) {
							t.Errorf("%s: line %d holds %q: %q", when, i, bad, l)
						}
					}
				}
			}
			clean("placeholder")
			clean("ghost")
		})
	}

	th := unicodeTheme()
	m := inputbox.New(th, hostile)
	m.SetSize(60, 8, false)
	if got, want := inside(th, m.Lines()[0], 60, false), "❯ a]0;titleb[31mc d    e2Jf"; got != want {
		t.Errorf("the placeholder row is %q, want %q", got, want)
	}
	typed(m, "/st")
	m.SetGhost(hostile)
	if got, want := inside(th, m.Lines()[0], 60, false), "❯ /sta]0;titleb[31mc d    e2Jf"; got != want {
		t.Errorf("the ghost row is %q, want %q", got, want)
	}
	if got := m.Value(); got != "/st" {
		t.Errorf("the draft is %q after SetGhost, want %q", got, "/st")
	}
	if x, y := m.Cursor(); x != 5 || y != 0 {
		t.Errorf("the cursor is at %d,%d, want 5,0", x, y)
	}
}

// m0 draws a box 60 cells wide with hostile text as its placeholder or as
// its ghost, and returns the lines.
func m0(t *testing.T, th *theme.Theme, hostile, where string) []string {
	t.Helper()
	if where == "placeholder" {
		m := inputbox.New(th, hostile)
		m.SetSize(60, 8, true)
		check(t, m, th, 60, 8, true, "")
		return m.Lines()
	}
	m := inputbox.New(th, placehold)
	m.SetSize(60, 8, true)
	typed(m, "/st")
	m.SetGhost(hostile)
	lines := m.Lines()
	if !strings.Contains(plain(lines[1]), "2Jf") {
		t.Errorf("the ghost is not drawn: %q", plain(lines[1]))
	}
	return lines
}

// TestTextRows pins the number of rows a draft asks for, which is not held
// to the rows the box may show, and what a change of that number alone does.
func TestTextRows(t *testing.T) {
	t.Parallel()
	th := unicodeTheme()
	m := inputbox.New(th, placehold)
	m.SetSize(40, 3, true)
	rows := func(when string, text, shown int) {
		t.Helper()
		if got := m.TextRows(); got != text {
			t.Errorf("%s: TextRows is %d, want %d", when, got, text)
		}
		if got := m.Rows(); got != shown {
			t.Errorf("%s: Rows is %d, want %d", when, got, shown)
		}
		if got := len(m.Lines()); got != shown {
			t.Errorf("%s: %d lines, want %d", when, got, shown)
		}
	}
	rows("empty", 1, 3)
	typed(m, "hello")
	rows("one line", 1, 3)
	m.SetValue(strings.Repeat("l\n", 9) + "l")
	rows("ten lines", 10, 5)
	// 34 cells of text to a row at 40 columns with a border: 100 cells of
	// words take four rows.
	m.SetValue(words(100))
	rows("a wrapped line", 4, 5)
	if got := m.LineCount(); got != 1 {
		t.Errorf("LineCount is %d for one wrapped line, want 1", got)
	}

	// More room shows more rows, and the draft is as it was. The box
	// showed the last three of four rows, so the cursor is a row lower.
	press(m, tea.KeyUp, 0)
	pressN(m, 3, tea.KeyLeft)
	check(t, m, th, 40, 3, true, "")
	x, y := m.Cursor()
	m.SetSize(40, 8, true)
	rows("with room for eight", 4, 6)
	check(t, m, th, 40, 8, true, "")
	if nx, ny := m.Cursor(); nx != x || ny != y+1 {
		t.Errorf("the cursor went from %d,%d to %d,%d, want %d,%d", x, y, nx, ny, x, y+1)
	}
	typed(m, "X")
	if got := strings.Count(m.Value(), "X"); got != 1 || len(m.Value()) != 101 {
		t.Errorf("after a change of rows typing gave %q", m.Value())
	}
	m.SetValue(words(100))

	// A terminal can be wider than the 500 cells the text area stops at
	// when it is left to itself.
	m.SetSize(700, 8, false)
	m.SetValue(strings.Repeat("x", 600))
	rows("600 cells in 700", 1, 1)
	typed(m, "y")
	rows("601 cells in 700", 1, 1)
	check(t, m, th, 700, 8, false, "")
	m.SetValue(words(100))
	press(m, tea.KeyUp, 0)

	// A narrower box wraps the same draft into more rows.
	m.SetSize(20, 8, false)
	if got := m.TextRows(); got <= 4 {
		t.Errorf("TextRows is %d at 20 columns, want more than 4", got)
	}
	check(t, m, th, 20, 8, false, "")
	m.Reset()
	rows("after Reset", 1, 1)
}

// asTheTextArea holds every row the box draws to the row the text area
// draws, selection and all, wherever the text area draws the row whole.
func asTheTextArea(t *testing.T, m *inputbox.Model, when string) {
	t.Helper()
	theirs, ours := m.DraftRows(), m.PlainRows()
	if len(theirs) != len(ours) {
		return
	}
	for i := range ours {
		text := strings.TrimRight(plain(ours[i]), " ")
		if strings.TrimRight(plain(theirs[i]), " ") != text {
			continue
		}
		// The text area selects half a cluster where the box selects all
		// of it.
		if uniseg.GraphemeClusterCount(text) != len([]rune(text)) {
			continue
		}
		if got, want := strings.TrimRight(ours[i], " "), strings.TrimRight(theirs[i], " "); got != want {
			t.Errorf("%s: row %d is drawn %q, and the text area draws %q", when, i, got, want)
		}
	}
}

// TestSelectByRow pins what shift with up and down select in a line that is
// wrapped: a row, not the line.
func TestSelectByRow(t *testing.T) {
	t.Parallel()
	th := unicodeTheme()
	m := inputbox.New(th, placehold)
	// 21 cells of text to a row hold four words: three rows.
	m.SetSize(23, 8, false)
	m.SetValue(words(50))
	if got := m.TextRows(); got != 3 {
		t.Fatalf("the draft takes %d rows, want 3", got)
	}
	m.Update(keyOf(tea.KeyUp, tea.ModShift))
	typed(m, "Z")
	if got, want := m.Value(), words(50)[:30]+"Z"; got != want {
		t.Errorf("typing over a row selected upwards gave %q, want %q", got, want)
	}

	m.SetValue(words(50))
	press(m, tea.KeyHome, tea.ModCtrl)
	m.Update(keyOf(tea.KeyDown, tea.ModShift))
	typed(m, "Z")
	if got, want := m.Value(), "Z"+words(50)[20:]; got != want {
		t.Errorf("typing over a row selected downwards gave %q, want %q", got, want)
	}
	check(t, m, th, 23, 8, false, "")
}

// TestUpAndDownMoveByRow pins what up and down do in a line that is
// wrapped: they move a row, and keep the column.
func TestUpAndDownMoveByRow(t *testing.T) {
	t.Parallel()
	th := unicodeTheme()
	m := inputbox.New(th, placehold)
	// 21 cells of text to a row hold four words: three rows.
	m.SetSize(23, 8, false)
	m.SetValue(words(50))
	press(m, tea.KeyHome, tea.ModCtrl)
	pressN(m, 3, tea.KeyRight)
	at := func(when string, x, y int) {
		t.Helper()
		if gx, gy := m.Cursor(); gx != x || gy != y {
			t.Errorf("%s the cursor is at %d,%d, want %d,%d", when, gx, gy, x, y)
		}
		check(t, m, th, 23, 8, false, "")
	}
	at("at the start", 5, 0)
	press(m, tea.KeyDown, 0)
	at("a row down", 5, 1)
	press(m, tea.KeyDown, 0)
	at("two rows down", 5, 2)
	press(m, tea.KeyUp, 0)
	at("a row up again", 5, 1)
	typed(m, "X")
	if got := strings.Index(m.Value(), "X"); got != 23 {
		t.Errorf("typing there put the character at %d, want 23: %q", got, m.Value())
	}
}

// TestSelection holds a box with selected text to its width, and to showing
// what is selected: the next key typed replaces it.
func TestSelection(t *testing.T) {
	t.Parallel()
	exact := func(t *testing.T, m *inputbox.Model, th *theme.Theme, s size, when string) {
		t.Helper()
		lines := m.Lines()
		if len(lines) != m.Rows() {
			t.Fatalf("%s: %d lines and Rows is %d", when, len(lines), m.Rows())
		}
		for i, l := range lines {
			if got := th.Width(l); got != s.width {
				t.Errorf("%s: line %d is %d cells wide, want %d: %q", when, i, got, s.width, l)
			}
		}
		if x, y := m.Cursor(); x < 0 || x >= max(s.width, 1) || y < 0 || y >= len(lines) {
			t.Errorf("%s: the cursor is at %d,%d, outside the box", when, x, y)
		}
	}
	for _, nt := range themes() {
		for _, s := range sizes() {
			t.Run(nt.name+"/"+s.String(), func(t *testing.T) {
				t.Parallel()
				th := nt.th
				w := textWidth(th, s)
				texts := []string{
					"hello", strings.Repeat("x", w), strings.Repeat("x", w-1) + " yz", words(3*w + 2),
					strings.Repeat(cjkText, 3), "one\ntwo\n\nthree", strings.Repeat("x", w-1) + "这这",
					"a" + zwjFamily + " " + skinTone + heart, brokenRow,
				}
				for _, text := range texts {
					m := inputbox.New(th, placehold)
					m.SetSize(s.width, 99, s.boxed)
					m.SetValue(text)
					plainBefore, before := plainLines(m), m.Lines()
					if !press(m, 'g', tea.ModCtrl) {
						t.Errorf("%q: Update reported no change for select all", text)
					}
					exact(t, m, th, s, "all selected")
					// The text is the same, and it is drawn differently.
					if got := plainLines(m); !slices.Equal(got, plainBefore) {
						t.Errorf("%q: selected, the box shows\n%q\nand before\n%q", text, got, plainBefore)
					}
					if slices.Equal(m.Lines(), before) {
						t.Errorf("%q: the selection is not shown", text)
					}
					for _, k := range []tea.KeyPressMsg{
						keyOf(tea.KeyLeft, tea.ModShift), keyOf(tea.KeyUp, tea.ModShift),
						keyOf(tea.KeyRight, tea.ModShift), keyOf(tea.KeyDown, tea.ModShift),
						keyOf(tea.KeyLeft, tea.ModShift|tea.ModAlt), keyOf(tea.KeyLeft, 0),
						keyOf(tea.KeyLeft, tea.ModShift), keyOf(tea.KeyLeft, tea.ModShift), keyOf(tea.KeyUp, tea.ModShift),
					} {
						m.Update(k)
						exact(t, m, th, s, k.String())
						asTheTextArea(t, m, k.String())
					}
					press(m, 'g', tea.ModCtrl)
					typed(m, "Z")
					if got := m.Value(); got != "Z" {
						t.Errorf("%q: typing over the selection gave %q, want %q", text, got, "Z")
					}
					check(t, m, th, s.width, 99, s.boxed, "")
				}
			})
		}
	}

	// A row ends in the space it was wrapped at. Where the row is full,
	// that space has no cell, selected or not. Here the first row is 28
	// cells by characters, all a box 30 wide has after the prompt.
	t.Run("a space at the end of a full row", func(t *testing.T) {
		t.Parallel()
		th := unicodeTheme()
		head := strings.Repeat("a", 16) + " " + skinTone + " " + skinTone + " b"
		m := inputbox.New(th, placehold)
		m.SetSize(30, 8, false)
		pasteOf(m, head+" cc")
		want := []string{"❯ " + head, "  cc" + strings.Repeat(" ", 26)}
		if got := plainLines(m); !slices.Equal(got, want) {
			t.Fatalf("the box is\n%q\nwant\n%q", got, want)
		}
		press(m, 'g', tea.ModCtrl)
		if got := plainLines(m); !slices.Equal(got, want) {
			t.Errorf("selected, the box is\n%q\nwant\n%q", got, want)
		}
		exact(t, m, th, size{30, 8, false}, "all selected")
	})

	// A selection that starts or ends inside a cluster is drawn over all of
	// the cluster. Drawn over half of it, the row would be two cells wider
	// where clusters are counted, and here it is full as it is.
	t.Run("half a cluster", func(t *testing.T) {
		t.Parallel()
		for _, nt := range themes()[:2] {
			th := nt.th
			// The thumb is four cells where characters are counted.
			width := 2 + th.Width("ab"+skinTone+"cd") + 1
			m := inputbox.New(th, placehold)
			m.SetSize(width, 8, false)
			pasteOf(m, "ab"+skinTone+"cd")
			press(m, tea.KeyHome, 0)
			pressN(m, 3, tea.KeyRight)
			m.Update(keyOf(tea.KeyRight, tea.ModShift))
			m.Update(keyOf(tea.KeyRight, tea.ModShift))
			exact(t, m, th, size{width, 8, false}, nt.name)
			selected := th.Textarea().Focused.Selection.Inline(true).Render(skinTone + "c")
			if got, want := strings.TrimRight(m.Lines()[0], " "), th.Style("accent").Render("❯ ")+"ab"+selected+"d"; got != want {
				t.Errorf("%s: the row is %q, want %q", nt.name, got, want)
			}
			// What is selected is still what the text area says: the
			// second half of the thumb and the letter after it.
			typed(m, "Z")
			if got, want := m.Value(), "ab👍Zd"; got != want {
				t.Errorf("%s: typing over the selection gave %q, want %q", nt.name, got, want)
			}

			// And a selection that ends inside the thumb.
			m.SetValue("ab" + skinTone + "cd")
			press(m, tea.KeyHome, 0)
			pressN(m, 1, tea.KeyRight)
			m.Update(keyOf(tea.KeyRight, tea.ModShift))
			m.Update(keyOf(tea.KeyRight, tea.ModShift))
			exact(t, m, th, size{width, 8, false}, nt.name)
			selected = th.Textarea().Focused.Selection.Inline(true).Render("b" + skinTone)
			if got, want := strings.TrimRight(m.Lines()[0], " "), th.Style("accent").Render("❯ ")+"a"+selected+"cd"; got != want {
				t.Errorf("%s: the row is %q, want %q", nt.name, got, want)
			}
		}
	})

	// The text area can wrap a line inside a cluster, in a box too narrow
	// for it. A selection that ends there is drawn as far as the row goes.
	t.Run("a cluster on two rows", func(t *testing.T) {
		t.Parallel()
		for _, nt := range themes() {
			for width := 4; width < 16; width++ {
				for _, boxed := range []bool{true, false} {
					m := inputbox.New(nt.th, placehold)
					m.SetSize(width, 3, boxed)
					m.SetValue(",.😀 " + zwjFamily + " b" + skinTone)
					for range 12 {
						m.Update(keyOf(tea.KeyLeft, tea.ModShift))
						exact(t, m, nt.th, size{width, 3, boxed}, nt.name)
					}
					press(m, tea.KeyUp, 0)
					for range 12 {
						m.Update(keyOf(tea.KeyRight, tea.ModShift))
						exact(t, m, nt.th, size{width, 3, boxed}, nt.name)
					}
				}
			}
		}
	})

	// A row the box draws itself shows its selection too: the first row
	// here is one the text area cuts.
	t.Run("on a row the text area cuts", func(t *testing.T) {
		t.Parallel()
		for _, th := range []*theme.Theme{unicodeTheme(), lightTheme()} {
			m := inputbox.New(th, placehold)
			m.SetSize(40, 8, true)
			m.SetValue(strings.Repeat("x", 32) + "这好")
			press(m, tea.KeyHome, 0)
			m.Update(keyOf(tea.KeyRight, tea.ModShift))
			m.Update(keyOf(tea.KeyRight, tea.ModShift))
			selected := th.Textarea().Focused.Selection.Inline(true).Render("xx")
			if row := m.Lines()[1]; !strings.Contains(row, selected+strings.Repeat("x", 30)+"这") {
				t.Errorf("the row is %q, want %q, the rest of the row and the wide character", row, selected)
			}
		}
	})
}

// TestKeyText types a key whose text holds line endings, as a terminal that
// reports the text of a key may send it.
func TestKeyText(t *testing.T) {
	t.Parallel()
	cases := []struct{ text, want string }{
		{"a", "a"},
		{"你好", "你好"},
		{"a\r\nb", "a\nb"},
		{"a\rb", "a\nb"},
		{"a\tb", "a    b"},
		{"a\x1bb", "ab"},
	}
	th := unicodeTheme()
	for _, tc := range cases {
		m := inputbox.New(th, placehold)
		m.SetSize(40, 8, true)
		m.Update(tea.KeyPressMsg{Code: 'x', Text: tc.text})
		if got := m.Value(); got != tc.want {
			t.Errorf("a key with the text %q gave %q, want %q", tc.text, got, tc.want)
		}
		check(t, m, th, 40, 8, true, "")
	}
}

// TestWidthNeedsNoSettledCaps draws with a theme made from caps that were
// never filled in, as the screen has before the terminal has answered.
func TestWidthNeedsNoSettledCaps(t *testing.T) {
	t.Parallel()
	th := theme.New(termcaps.Caps{}, theme.Options{})
	m := inputbox.New(th, placehold)
	m.SetSize(40, 8, true)
	check(t, m, th, 40, 8, true, "")
	pasteOf(m, "a"+zwjFamily+heart+"b\r\n"+cjkText)
	check(t, m, th, 40, 8, true, "")
}
