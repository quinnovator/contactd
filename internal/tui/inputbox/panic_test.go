package inputbox_test

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"

	"github.com/quinnovator/contactd/internal/tui/inputbox"
)

// fatal are drafts the text area panics over when it draws them with the
// cursor in the wrong place: a word that fills the row, ends in an emoji
// with a selector, and has a space after it. The cursor
// between the word and the space is the wrong place. The number is the
// width of the word, which is the width of text area it takes.
func fatal() []struct {
	width int
	draft string
} {
	return []struct {
		width int
		draft string
	}{
		{4, "ab" + heart + " a"},
		{5, "abc" + heart + " a"},
		{3, "a" + heart + " " + heart},
		{6, "abcd" + heart + " ab"},
		{4, "这" + heart + " 好"},
		{4, "first\nab" + heart + " a\nlast"},
		{4, "xxx ab" + heart + " a"},
	}
}

// TestTheTextAreaPanics shows that fatal is what it says, so that the tests
// below test something: the text area's own drawing, which the box never
// calls, panics over each draft.
func TestTheTextAreaPanics(t *testing.T) {
	t.Parallel()
	for _, f := range fatal() {
		th := unicodeTheme()
		m := inputbox.New(th, placehold)
		// Unboxed, the text area wraps to the width less the prompt.
		m.SetSize(f.width+2, 8, false)
		if got := m.WrapWidth(); got != f.width {
			t.Fatalf("the draft is wrapped to %d cells, want %d", got, f.width)
		}
		m.SetValue(f.draft)
		panics := false
		for range len([]rune(f.draft)) + 1 {
			panics = panics || m.DraftRows() == nil
			press(m, tea.KeyLeft, 0)
		}
		if !panics {
			t.Errorf("the text area draws %q with the cursor anywhere; this draft shows nothing any more", f.draft)
		}
	}
}

// TestSurvivesTheTextArea puts the cursor in every place of drafts the text
// area panics over, at every width near the fatal one, and edits there.
func TestSurvivesTheTextArea(t *testing.T) {
	t.Parallel()
	for _, nt := range widthThemes() {
		for _, boxed := range []bool{false, true} {
			for fi, f := range fatal() {
				t.Run(nt.name+"/"+boxName(boxed)+"/"+strconv.Itoa(fi), func(t *testing.T) {
					t.Parallel()
					th := nt.th
					m := inputbox.New(th, placehold)
					runes := len([]rune(f.draft))
					fatalWidths := 0
					defer func() {
						if fatalWidths == 0 {
							t.Error("the draft was never wrapped to the width the text area panics at")
						}
					}()
					for width := f.width; width <= f.width+12; width++ {
						ok := func() { t.Helper(); check(t, m, th, width, 8, boxed, "") }
						m.SetSize(width, 8, boxed)
						// The fatal width, and one to either side of it.
						if got := m.WrapWidth(); got < f.width-1 || got > f.width+1 {
							continue
						}
						if m.WrapWidth() == f.width {
							fatalWidths++
							// Messages that write nothing and move nothing,
							// with the cursor in every place.
							m.SetValue(f.draft)
							for range runes + 1 {
								for _, msg := range []tea.Msg{
									keyOf(tea.KeyEscape, 0), keyOf(tea.KeyTab, 0), keyOf(tea.KeyPgUp, 0), keyOf(tea.KeyPgDown, 0),
									tea.WindowSizeMsg{Width: width, Height: 8},
									nil,
								} {
									m.Update(msg)
									m.Lines()
									m.Cursor()
								}
								ok()
								press(m, tea.KeyLeft, 0)
							}
							// Every key that moves the cursor, from every place.
							// A border changes nothing about where they go.
							moves := []tea.KeyPressMsg{
								keyOf(tea.KeyUp, 0), keyOf(tea.KeyDown, 0), keyOf(tea.KeyLeft, 0), keyOf(tea.KeyRight, 0),
								keyOf(tea.KeyUp, tea.ModShift), keyOf(tea.KeyDown, tea.ModShift),
								keyOf(tea.KeyLeft, tea.ModShift), keyOf(tea.KeyRight, tea.ModShift),
							}
							if boxed {
								moves = moves[:2]
							}
							for _, k := range moves {
								for at := range runes + 1 {
									press(m, tea.KeyHome, tea.ModCtrl)
									pressN(m, at, tea.KeyRight)
									m.Update(k)
									m.Lines()
									m.Cursor()
								}
							}
							if got := m.Value(); got != f.draft {
								t.Fatalf("keys that write nothing left the draft %q, want %q", got, f.draft)
							}
							press(m, tea.KeyHome, tea.ModCtrl)
							ok()
						}
						// Typing and deleting in every place, from the end.
						m.SetValue(f.draft)
						for at := runes; at >= 0; at-- {
							typed(m, "x")
							pasteOf(m, heart)
							ok()
							pressN(m, 3, tea.KeyBackspace)
							ok()
							if got := m.Value(); got != f.draft {
								t.Fatalf("typing and deleting at %d left the draft %q, want %q", at, got, f.draft)
							}
							press(m, tea.KeyLeft, 0)
						}
					}
				})
			}
		}
	}
}

// TestFatalCursor pins the fatal place itself: the cursor after the heart
// in "ab", a heart with its selector, a space and "a", in a text area four
// cells wide.
func TestFatalCursor(t *testing.T) {
	t.Parallel()
	th := graphemeTheme()
	m := inputbox.New(th, placehold)
	m.SetSize(6, 8, false)
	m.SetValue("ab" + heart + " a")
	pressN(m, 2, tea.KeyLeft)
	if m.DraftRows() != nil {
		t.Log("the text area draws this; this test shows nothing new")
	}
	// The text area gives the line an empty first row.
	want := []string{"❯", "  ab" + heart, "  a"}
	got := plainLines(m)
	for i := range got {
		got[i] = strings.TrimRight(got[i], " ")
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("the box shows %q, want %q", got, want)
	}
	// The row is full, and the space after it is on the same row, off the
	// edge: the cursor is held on the row's last cell.
	if x, y := m.Cursor(); x != 5 || y != 1 {
		t.Errorf("the cursor is at %d,%d, want 5,1", x, y)
	}
	typed(m, "!")
	if got, want := m.Value(), "ab"+heart+"! a"; got != want {
		t.Errorf("typing there gave %q, want %q", got, want)
	}
	check(t, m, th, 6, 8, false, "")
}

// TestGuarded hands the text area a message where it panics, and holds the
// box to being as it was.
func TestGuarded(t *testing.T) {
	t.Parallel()
	th := unicodeTheme()
	m := inputbox.New(th, placehold)
	m.SetSize(6, 8, false)
	draft := "first\nab" + heart + " a\nlast"
	m.SetValue(draft)
	// From the end of the draft: over "last", the line break and " a".
	pressN(m, 7, tea.KeyLeft)
	typed(m, "?")
	if got, want := m.Value(), "first\nab"+heart+"? a\nlast"; got != want {
		t.Fatalf("the cursor is not after the heart: typing gave %q, want %q", got, want)
	}
	press(m, tea.KeyBackspace, 0)
	if m.DraftRows() != nil {
		t.Fatal("the text area does not panic here, so this test shows nothing")
	}
	lines := m.Lines()
	x, y := m.Cursor()

	if cmd := m.Guarded(keyOf(tea.KeyEscape, 0)); cmd != nil {
		t.Error("a message the text area panicked over returned a command")
	}
	if got := m.Value(); got != draft {
		t.Errorf("the draft is %q, want %q", got, draft)
	}
	if nx, ny := m.Cursor(); nx != x || ny != y {
		t.Errorf("the cursor went from %d,%d to %d,%d", x, y, nx, ny)
	}
	if got := m.Lines(); strings.Join(got, "\n") != strings.Join(lines, "\n") {
		t.Errorf("the box is drawn%s%q%sand before%s%q", "\n", got, "\n", "\n", lines)
	}
	typed(m, "!")
	if got, want := m.Value(), "first\nab"+heart+"! a\nlast"; got != want {
		t.Errorf("typing afterwards gave %q, want %q", got, want)
	}
	check(t, m, th, 6, 8, false, "")
}

// TestFlat holds the width the text area is given while it takes a message
// to its purpose: no line is wrapped there, before the message or after it.
// A line takes its cells and one more, for the cursor after it.
func TestFlat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		draft string
		do    func(m *inputbox.Model)
		msg   tea.Msg
		want  string
	}{
		{"typing", "hello", nil, tea.KeyPressMsg{Code: 'x', Text: "x"}, "hellox"},
		{"typing a wide character", "这这这这", nil, tea.KeyPressMsg{Code: '好', Text: "好"}, "这这这这好"},
		{"pasting wide text", "这这这这", nil, tea.PasteMsg{Content: "好好好好好好"}, "这这这这好好好好好好"},
		{"pasting into an empty box", "", nil, tea.PasteMsg{Content: cjkText}, cjkText},
		{"pasting tabs", "", nil, tea.PasteMsg{Content: "\t\t\t"}, strings.Repeat(" ", 12)},
		{"pasting lines", "ab", nil, tea.PasteMsg{Content: "c\n" + cjkText + "\nd"}, "abc\n" + cjkText + "\nd"},
		{"joining two lines", "xxxxxxxxxx\nyyyyyyyyyy", func(m *inputbox.Model) { press(m, tea.KeyHome, 0) }, keyOf(tea.KeyBackspace, 0), "xxxxxxxxxxyyyyyyyyyy"},
		{"joining two wide lines", "这这这这这\n好好好好好", func(m *inputbox.Model) {
			press(m, tea.KeyUp, 0)
			press(m, tea.KeyEnd, 0)
		}, keyOf(tea.KeyDelete, 0), "这这这这这好好好好好"},
		{"a key that writes nothing", cjkText, nil, keyOf(tea.KeyLeft, 0), cjkText},
	}
	th := unicodeTheme()
	for _, tc := range cases {
		m := inputbox.New(th, placehold)
		m.SetSize(20, 8, false)
		m.SetValue(tc.draft)
		if tc.do != nil {
			tc.do(m)
		}
		flat := m.Flat(tc.msg)
		before := m.Value()
		m.Update(tc.msg)
		if got := m.Value(); got != tc.want {
			t.Errorf("%s: the draft is %q, want %q", tc.name, got, tc.want)
		}
		for line := range strings.SplitSeq(before+"\n"+m.Value(), "\n") {
			if need := uniseg.StringWidth(line) + 1; flat < need {
				t.Errorf("%s: the text area was %d cells wide, and the line %q needs %d", tc.name, flat, line, need)
			}
		}
		// It has its own width back: the draft is wrapped again.
		if got := m.WrapWidth(); got != 18 {
			t.Errorf("%s: afterwards the draft is wrapped to %d cells, want 18", tc.name, got)
		}
	}
}
