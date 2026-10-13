package inputbox_test

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"

	"github.com/quinnovator/contactd/internal/tui/inputbox"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// size is the room a box is given.
type size struct {
	width, maxRows int
	boxed          bool
}

func (s size) String() string {
	return boxName(s.boxed) + " " + strconv.Itoa(s.width) + "x" + strconv.Itoa(s.maxRows)
}

func sizes() []size {
	return []size{{100, 8, true}, {40, 8, true}, {39, 8, false}, {30, 3, false}, {12, 2, false}}
}

// roomAfterPrompt is the number of cells a row has after the prompt.
func roomAfterPrompt(th *theme.Theme, s size) int {
	_, _, inner := frame(th, s.width, s.boxed)
	return inner - th.Width(th.G.Prompt+" ")
}

// textWidth is the number of cells of a row the draft is wrapped to: all
// the row has after the prompt, unless a row of the draft would not fit.
func textWidth(th *theme.Theme, s size) int {
	return roomAfterPrompt(th, s)
}

// scene puts a draft in a box and its cursor somewhere. w is the number of
// cells a row has for text, so that a scene can fill a row exactly.
type scene struct {
	name string
	do   func(m *inputbox.Model, w int)
}

// words is n cells of four-letter words.
func words(n int) string {
	return strings.Repeat("word ", n/5+1)[:n]
}

func scenes() []scene {
	return []scene{
		{"empty", func(*inputbox.Model, int) {}},
		{"end of a short draft", func(m *inputbox.Model, _ int) { typed(m, "hello") }},
		{"middle of a line", func(m *inputbox.Model, _ int) {
			typed(m, "hello you")
			pressN(m, 4, tea.KeyLeft)
		}},
		{"start of a line", func(m *inputbox.Model, _ int) {
			typed(m, "hello")
			press(m, tea.KeyHome, 0)
		}},
		{"end of a wrapped line", func(m *inputbox.Model, w int) { typed(m, words(2*w+w/2)) }},
		{"a row up in a wrapped line", func(m *inputbox.Model, w int) {
			typed(m, words(2*w+w/2))
			press(m, tea.KeyUp, 0)
		}},
		{"inside a wrapped line", func(m *inputbox.Model, w int) {
			typed(m, words(2*w+w/2))
			pressN(m, w, tea.KeyLeft)
		}},
		{"start of a wrapped line", func(m *inputbox.Model, w int) {
			typed(m, words(2*w+w/2))
			press(m, tea.KeyHome, 0)
		}},
		{"a line that fills its row", func(m *inputbox.Model, w int) { typed(m, strings.Repeat("x", w)) }},
		{"a line one cell short of its row", func(m *inputbox.Model, w int) { typed(m, strings.Repeat("x", w-1)) }},
		{"a line one cell longer than its row", func(m *inputbox.Model, w int) { typed(m, strings.Repeat("x", w+1)) }},
		{"a space at the end of a full row", func(m *inputbox.Model, w int) { typed(m, strings.Repeat("x", w-1)+" ") }},
		{"a word after a full row", func(m *inputbox.Model, w int) { typed(m, strings.Repeat("x", w-1)+" yz") }},
		{"just before the wrap", func(m *inputbox.Model, w int) {
			typed(m, strings.Repeat("x", w-1)+" yz")
			pressN(m, 3, tea.KeyLeft)
		}},
		{"cjk", func(m *inputbox.Model, _ int) { typed(m, cjkText) }},
		{"cjk, wrapped", func(m *inputbox.Model, _ int) { pasteOf(m, strings.Repeat(cjkText, 3)) }},
		{"cjk, inside", func(m *inputbox.Model, _ int) {
			pasteOf(m, strings.Repeat(cjkText, 3))
			pressN(m, 7, tea.KeyLeft)
		}},
		{"a wide character that does not fit the row's last cell", func(m *inputbox.Model, w int) {
			typed(m, strings.Repeat("x", w-1)+"这这")
		}},
		{"emoji", func(m *inputbox.Model, _ int) {
			typed(m, "ok 😀 fine 👍 done")
			pressN(m, 7, tea.KeyLeft)
		}},
		{"after a joined emoji", func(m *inputbox.Model, _ int) { pasteOf(m, "a"+zwjFamily+"b") }},
		{"before the letter after a joined emoji", func(m *inputbox.Model, _ int) {
			pasteOf(m, "a"+zwjFamily+"b")
			press(m, tea.KeyLeft, 0)
		}},
		{"inside a joined emoji", func(m *inputbox.Model, _ int) {
			pasteOf(m, "a"+zwjFamily+"b")
			pressN(m, 3, tea.KeyLeft)
		}},
		{"after a skin tone", func(m *inputbox.Model, _ int) { pasteOf(m, skinTone+" ok "+skinTone) }},
		{"after an emoji selector", func(m *inputbox.Model, _ int) { pasteOf(m, heart+" ok "+heart) }},
		{"after a combining mark", func(m *inputbox.Model, _ int) {
			pasteOf(m, "cafe\u0301 x")
			pressN(m, 2, tea.KeyLeft)
		}},
		{"after an ideographic space", func(m *inputbox.Model, _ int) { pasteOf(m, "这\u3000这\u3000a") }},
		{"between two ideographic spaces", func(m *inputbox.Model, _ int) {
			pasteOf(m, "这\u3000\u3000这")
			pressN(m, 2, tea.KeyLeft)
		}},
		{"after a line separator", func(m *inputbox.Model, _ int) { pasteOf(m, "ab\u2028cd\u2029ef") }},
		{"after text that turns around", func(m *inputbox.Model, _ int) { pasteOf(m, "ab\u202ecd\U0010eeeeef") }},
		{"a wide character after a narrow one in the last cell", func(m *inputbox.Model, w int) {
			// The text area cuts this row after its last narrow character,
			// so it can be asked about a cursor before that and no further.
			typed(m, strings.Repeat("x", max(w-1, 1))+"这好")
			pressN(m, 4, tea.KeyLeft)
		}},
		{"middle of a draft of several lines", func(m *inputbox.Model, _ int) {
			m.SetValue("first\nsecond one\nthird")
			press(m, tea.KeyUp, 0)
			pressN(m, 2, tea.KeyLeft)
		}},
		{"an empty line between two", func(m *inputbox.Model, _ int) {
			m.SetValue("first\n\nthird")
			press(m, tea.KeyUp, 0)
		}},
		{"below a wrapped line", func(m *inputbox.Model, w int) {
			m.SetValue(words(2*w+3) + "\nsecond\n" + cjkText)
			press(m, tea.KeyUp, 0)
			pressN(m, 2, tea.KeyLeft)
		}},
		{"between two wrapped lines", func(m *inputbox.Model, w int) {
			m.SetValue(words(2*w+3) + "\nab\n" + words(2*w+3))
			pressN(m, 3, tea.KeyUp)
		}},
		{"the end of more lines than fit", func(m *inputbox.Model, _ int) {
			m.SetValue(strings.Repeat("line\n", 11) + "end")
		}},
		{"scrolled back up", func(m *inputbox.Model, _ int) {
			m.SetValue(strings.Repeat("line\n", 11) + "end")
			pressN(m, 5, tea.KeyUp)
			press(m, tea.KeyLeft, 0)
		}},
		{"the top of more lines than fit", func(m *inputbox.Model, _ int) {
			m.SetValue(strings.Repeat("line\n", 11) + "end")
			pressN(m, 30, tea.KeyUp)
		}},
		{"after backslash then enter", func(m *inputbox.Model, _ int) {
			typed(m, `one\`)
			m.Enter()
			typed(m, "tw")
		}},
		{"after joining two lines", func(m *inputbox.Model, _ int) {
			m.SetValue("one\ntwo")
			press(m, tea.KeyHome, 0)
			press(m, tea.KeyBackspace, 0)
		}},
		{"after a paste of several lines", func(m *inputbox.Model, _ int) {
			typed(m, "ab")
			press(m, tea.KeyLeft, 0)
			pasteOf(m, "1\r\n2\r\n3")
		}},
	}
}

// TestCursor checks the reported cursor against the cell the text area
// would draw its own cursor in, for every scene, size and theme.
func TestCursor(t *testing.T) {
	t.Parallel()
	for _, nt := range themes() {
		for _, s := range sizes() {
			t.Run(nt.name+"/"+s.String(), func(t *testing.T) {
				t.Parallel()
				for _, sc := range scenes() {
					t.Run(sc.name, func(t *testing.T) {
						t.Parallel()
						m := inputbox.New(nt.th, placehold)
						m.SetSize(s.width, s.maxRows, s.boxed)
						sc.do(m, textWidth(nt.th, s))
						if !check(t, m, nt.th, s.width, s.maxRows, s.boxed, "") {
							t.Error("the text area could not be asked where it would draw its cursor")
						}

						// A resize keeps the cursor in the draft and still
						// reports it right.
						m.SetSize(s.width-7, s.maxRows, !s.boxed)
						check(t, m, nt.th, s.width-7, s.maxRows, !s.boxed, "")
					})
				}
			})
		}
	}
}

// TestCursorCells pins a few cursors as numbers, so that the check against
// the text area cannot be wrong in the same way as the box.
func TestCursorCells(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		th    *theme.Theme
		s     size
		do    func(m *inputbox.Model)
		x, y  int
		value string
	}{
		{"empty, boxed", unicodeTheme(), size{40, 8, true}, func(*inputbox.Model) {}, 4, 1, ""},
		{"empty, unboxed", unicodeTheme(), size{40, 8, false}, func(*inputbox.Model) {}, 2, 0, ""},
		{"ascii", asciiTheme(), size{40, 8, true}, func(m *inputbox.Model) { typed(m, "abc") }, 7, 1, "abc"},
		{"cjk counts two cells each", unicodeTheme(), size{40, 8, true}, func(m *inputbox.Model) { typed(m, "这是一") }, 10, 1, "这是一"},
		{"emoji counts two cells", unicodeTheme(), size{40, 8, false}, func(m *inputbox.Model) { typed(m, "a😀b") }, 6, 0, "a😀b"},
		{
			"34 cells fill a boxed row at 40, and the cursor goes to the next",
			unicodeTheme(),
			size{40, 8, true},
			func(m *inputbox.Model) { typed(m, strings.Repeat("x", 34)) },
			4, 2, strings.Repeat("x", 34),
		},
		{
			"33 cells leave the row's last cell for the cursor",
			unicodeTheme(),
			size{40, 8, true},
			func(m *inputbox.Model) { typed(m, strings.Repeat("x", 33)) },
			37, 1, strings.Repeat("x", 33),
		},
		{
			"17 cjk characters fill it too",
			unicodeTheme(),
			size{40, 8, true},
			func(m *inputbox.Model) { typed(m, strings.Repeat("这", 17)) },
			4, 2, strings.Repeat("这", 17),
		},
		{
			"second of three lines",
			unicodeTheme(),
			size{40, 8, true},
			func(m *inputbox.Model) {
				m.SetValue("first\nsecond\nthird")
				press(m, tea.KeyUp, 0)
				press(m, tea.KeyHome, 0)
				pressN(m, 3, tea.KeyRight)
			},
			7, 2, "first\nsecond\nthird",
		},
		{
			"a joined emoji is six cells where characters are counted",
			unicodeTheme(),
			size{40, 8, false},
			func(m *inputbox.Model) { pasteOf(m, "a"+zwjFamily) },
			9, 0, "a" + zwjFamily,
		},
		{
			"and two where clusters are",
			graphemeTheme(),
			size{40, 8, false},
			func(m *inputbox.Model) { pasteOf(m, "a"+zwjFamily) },
			5, 0, "a" + zwjFamily,
		},
		{
			"wide glyphs move the text right",
			wideGlyphTheme(),
			size{40, 8, true},
			func(m *inputbox.Model) { typed(m, "abc") },
			9, 1, "abc",
		},
		{
			"the last of 12 lines in 3 rows",
			unicodeTheme(),
			size{40, 3, true},
			func(m *inputbox.Model) { m.SetValue(strings.Repeat("line\n", 11) + "end") },
			7, 3, strings.Repeat("line\n", 11) + "end",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := inputbox.New(tc.th, placehold)
			m.SetSize(tc.s.width, tc.s.maxRows, tc.s.boxed)
			tc.do(m)
			if x, y := m.Cursor(); x != tc.x || y != tc.y {
				t.Errorf("the cursor is at %d,%d, want %d,%d\n%q", x, y, tc.x, tc.y, plainLines(m))
			}
			if got := m.Value(); got != tc.value {
				t.Errorf("the draft is %q, want %q", got, tc.value)
			}
			check(t, m, tc.th, tc.s.width, tc.s.maxRows, tc.s.boxed, "")
		})
	}
}

// TestJoinersAreDrawnApart pins what is drawn of a joined emoji where the
// terminal counts a character at a time: its parts, without the joiner, so
// that every such terminal draws the six cells the cursor is counted from.
// The draft keeps the joiner.
func TestJoinersAreDrawnApart(t *testing.T) {
	t.Parallel()
	draft := "a" + zwjFamily + heart + "b"
	cases := []struct {
		name string
		th   *theme.Theme
		row  string
	}{
		{"characters", unicodeTheme(), "❯ a👨👩👧❤b"},
		{"clusters", graphemeTheme(), "❯ " + draft},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := inputbox.New(tc.th, placehold)
			m.SetSize(40, 8, false)
			pasteOf(m, draft)
			if got := strings.TrimRight(plainLines(m)[0], " "); got != tc.row {
				t.Errorf("the row is %q, want %q", got, tc.row)
			}
			if got := m.Value(); got != draft {
				t.Errorf("the draft is %q, want %q", got, draft)
			}
			if text, _ := m.Enter(); text != draft {
				t.Errorf("Enter returned %q, want %q", text, draft)
			}
			check(t, m, tc.th, 40, 8, false, "")
		})
	}
}

// TestRandom drives a box with random typing, editing, pasting, resizing
// and restyling, and holds it to check after every step.
func TestRandom(t *testing.T) {
	t.Parallel()
	all := themes()
	letters := []rune("abc xyz .,/\\这好😀é\u3000")
	pastes := []string{
		"word word", zwjFamily, skinTone, heart, "a\r\nb", cjkText, "x\u202ey\u2028z", "3这",
		"a line long enough to be wrapped at most of the widths a box is given here",
		"e\u0301", "\tindented", "ab" + heart + " a", heart + " ",
	}
	values := []string{"", "one", "one\ntwo\nthree", strings.Repeat("line\n", 14), strings.Repeat(cjkText, 4)}
	keys := []tea.KeyPressMsg{
		keyOf(tea.KeyBackspace, 0), keyOf(tea.KeyDelete, 0),
		keyOf(tea.KeyLeft, 0), keyOf(tea.KeyRight, 0), keyOf(tea.KeyUp, 0), keyOf(tea.KeyDown, 0),
		keyOf(tea.KeyHome, 0), keyOf(tea.KeyEnd, 0),
		keyOf(tea.KeyLeft, tea.ModAlt), keyOf(tea.KeyRight, tea.ModAlt),
		keyOf('k', tea.ModCtrl), keyOf('u', tea.ModCtrl), keyOf('w', tea.ModCtrl), keyOf('t', tea.ModCtrl),
		keyOf('j', tea.ModCtrl), keyOf(tea.KeyEnter, tea.ModAlt), keyOf(tea.KeyEnter, tea.ModShift),
		keyOf('a', tea.ModCtrl), keyOf('e', tea.ModCtrl), keyOf('d', tea.ModAlt), keyOf(tea.KeyBackspace, tea.ModAlt), keyOf('d', tea.ModCtrl),
		keyOf(tea.KeyPgUp, 0), keyOf(tea.KeyPgDown, 0), keyOf(tea.KeyHome, tea.ModCtrl), keyOf(tea.KeyEnd, tea.ModCtrl),
	}

	for seed := range uint64(12) {
		t.Run("seed "+strconv.FormatUint(seed, 10), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, 7))
			nt := all[rng.IntN(len(all))]
			s := size{80, 8, false}
			m := inputbox.New(nt.th, placehold)

			var trail []string
			defer func() {
				if t.Failed() {
					t.Logf("theme %s, steps:\n%s", nt.name, strings.Join(trail, "\n"))
				}
			}()
			steps, asked := 0, 0
			step := func(what string) {
				trail = append(trail, what)
				steps++
				if check(t, m, nt.th, s.width, s.maxRows, s.boxed, "") {
					asked++
				}
			}
			// The check against the text area's own cursor must not fall
			// silent: it is left out only at the few steps where the text
			// area draws the cursor's row wrongly.
			defer func() {
				if asked*4 < steps*3 {
					t.Errorf("the text area could be asked where its cursor is at only %d of %d steps", asked, steps)
				}
			}()
			step("new")

			for range 160 {
				if t.Failed() {
					return
				}
				switch n := rng.IntN(100); {
				case n < 40:
					r := letters[rng.IntN(len(letters))]
					typed(m, string(r))
					step("type " + strconv.QuoteRune(r))
				case n < 70:
					k := keys[rng.IntN(len(keys))]
					m.Update(k)
					step("press " + k.String())
				case n < 78:
					p := pastes[rng.IntN(len(pastes))]
					pasteOf(m, p)
					step("paste " + strconv.Quote(p))
				case n < 88:
					s = size{rng.IntN(121), 1 + rng.IntN(8), rng.IntN(2) == 0}
					m.SetSize(s.width, s.maxRows, s.boxed)
					step("size " + s.String())
				case n < 92:
					nt = all[rng.IntN(len(all))]
					m.SetTheme(nt.th)
					step("theme " + nt.name)
				case n < 96:
					text, send := m.Enter()
					if send && text != strings.TrimRightFunc(m.Value(), unicode.IsSpace) {
						t.Errorf("Enter returned %q for the draft %q", text, m.Value())
					}
					step("enter")
				case n < 99:
					v := values[rng.IntN(len(values))]
					m.SetValue(v)
					step("value " + strconv.Quote(v))
				default:
					m.Reset()
					step("reset")
				}
			}
		})
	}
}

// brokenRow is a draft the text area draws one row too many for, in a box
// with 6 cells for text: it counts the skin tone after "/" as part of the "/", wraps
// the line by that count, and then breaks the row again where it draws it.
const brokenRow = "x/🏽这这这这这这这这这"

// TestBrokenRow holds the box to the rows the cursor is counted in when the
// text area draws more rows than the draft has. The terminal counts a
// cluster at a time here; where it counts a character at a time this draft
// is wrapped narrower, which TestWrapsNarrowerToFit covers.
func TestBrokenRow(t *testing.T) {
	t.Parallel()
	for _, nt := range themes()[1:2] {
		for _, boxed := range []bool{false, true} {
			t.Run(nt.name+"/"+boxName(boxed), func(t *testing.T) {
				t.Parallel()
				th := nt.th
				width, border := 8, 0
				if boxed {
					width, border = 12, 1
				}
				m := inputbox.New(th, placehold)
				m.SetSize(width, 8, boxed)
				m.SetValue(brokenRow)
				if len(m.DraftRows()) == m.TextRows() {
					t.Log("the text area draws this draft with the right number of rows, so this test shows nothing new")
				}
				if got := m.TextRows(); got != 4 {
					t.Fatalf("the draft has %d rows, want 4", got)
				}
				check(t, m, th, width, 8, boxed, "")

				rows := m.Lines()
				if len(rows) != 4+2*border {
					t.Fatalf("the box has %d lines, want %d", len(rows), 4+2*border)
				}
				want := []string{"", "  这这这", "  这这这", "  这"}
				for i, w := range want {
					if got := inside(th, rows[border+i], width, boxed); i > 0 && got != w {
						t.Errorf("row %d is %q, want %q", i, got, w)
					}
				}
				if got := inside(th, rows[border], width, boxed); !strings.HasPrefix(got, "❯ x/🏽") {
					t.Errorf("the first row is %q, want it to start %q", got, "❯ x/🏽")
				}
				before, _, _ := frame(th, width, boxed)
				if x, y := m.Cursor(); x != th.Width(before)+4 || y != border+3 {
					t.Errorf("the cursor is at %d,%d, want %d,%d: after the last character", x, y, th.Width(before)+4, border+3)
				}

				// The cursor moves through the rows that are drawn: a row
				// up it is after the first character of the third row.
				press(m, tea.KeyUp, 0)
				check(t, m, th, width, 8, boxed, "")
				if x, y := m.Cursor(); x != th.Width(before)+4 || y != border+2 {
					t.Errorf("a row up the cursor is at %d,%d, want %d,%d", x, y, th.Width(before)+4, border+2)
				}
				typed(m, "A")
				check(t, m, th, width, 8, boxed, "")
				if got, want := inside(th, m.Lines()[border+2], width, boxed), "  这A这"; got != want {
					t.Errorf("after typing in the third row it is %q, want %q", got, want)
				}
			})
		}
	}
}

// TestPlainRows holds the rows the box draws to the rows the text area
// draws, wherever the text area draws the right number of them and does not
// panic. The box never calls the text area's drawing; this test does.
func TestPlainRows(t *testing.T) {
	t.Parallel()
	// same compares the two for one box. prompt is the number of cells,
	// and of characters, a row starts with before its text.
	same := func(t *testing.T, m *inputbox.Model, prompt int) {
		t.Helper()
		draft, rows := m.DraftRows(), m.PlainRows()
		if len(rows) != m.TextRows() {
			t.Fatalf("%d rows for a draft of %d: %q", len(rows), m.TextRows(), m.Value())
		}
		if len(draft) != m.TextRows() {
			return
		}
		// Every row of the text area is as wide as the text area.
		room := ansi.StringWidth(draft[0])
		for i := range rows {
			got, want := strings.TrimRight(plain(rows[i]), " "), strings.TrimRight(plain(draft[i]), " ")
			text := string([]rune(plain(rows[i]))[prompt:])
			if w := ansi.StringWidth(text); prompt+w > room || w != uniseg.StringWidth(text) {
				// The text area does not draw every row whole. It wraps
				// by one count of cells and cuts its rows by another, so
				// a row the two disagree about can lose its end; and it
				// wraps a wide character that follows a narrow one a
				// cell too late, and then cuts that row too.
				continue
			}
			if got != want {
				t.Errorf("row %d of %q is %q, and the text area draws %q", i, m.Value(), got, want)
			}
		}
	}

	for _, nt := range widthThemes() {
		t.Run(nt.name, func(t *testing.T) {
			t.Parallel()
			prompt := nt.th.Width(nt.th.G.Prompt + " ")
			for _, s := range append(sizes(), size{6, 8, false}, size{4, 8, false}, size{3, 8, false}) {
				m := inputbox.New(nt.th, placehold)
				m.SetSize(s.width, s.maxRows, s.boxed)
				for _, d := range drafts() {
					m.SetValue(d.text)
					same(t, m, prompt)
				}
				for _, sc := range scenes() {
					m.Reset()
					sc.do(m, max(textWidth(nt.th, s), 2))
					same(t, m, prompt)
				}
				m.SetValue(brokenRow)
				same(t, m, prompt)
			}
		})
	}

	t.Run("random", func(t *testing.T) {
		t.Parallel()
		th := unicodeTheme()
		rng := rand.New(rand.NewPCG(3, 9))
		letters := []rune("ab  \n这好😀é/\\")
		for range 400 {
			var b strings.Builder
			for range rng.IntN(60) {
				b.WriteRune(letters[rng.IntN(len(letters))])
			}
			m := inputbox.New(th, placehold)
			m.SetSize(3+rng.IntN(30), 8, false)
			m.SetValue(b.String())
			same(t, m, 2)
			// The walk through the draft leaves the cursor where it was.
			typed(m, "Z")
			if got, want := m.Value(), b.String()+"Z"; got != want {
				t.Fatalf("after drawing the rows, typing gave %q, want %q", got, want)
			}
		}
	})
}
