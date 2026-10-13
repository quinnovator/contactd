package inputbox_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/tui/inputbox"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// widths are the widths section 9 names: a wide terminal, the narrowest
// with a box, the widest without, and a phone.
func widths() []int { return []int{100, 40, 39, 30} }

// lineWidths adds two widths no screen gives a box, where a row has room
// for little or nothing after the prompt.
func lineWidths() []int { return append(widths(), 12, 5) }

type draft struct{ name, text string }

func drafts() []draft {
	return []draft{
		{"empty", ""},
		{"short", "compare with the week before"},
		{"wrapped", strings.Repeat("and what would the share have been with the fix? ", 4)},
		{"several lines", "one\ntwo two\n\nfour"},
		{"more lines than rows", strings.Repeat("line\n", 11) + "end"},
		{"cjk", strings.Repeat(cjkText, 3)},
		{"emoji", "ok 😀 fine 👍 done " + zwjFamily + " " + skinTone + " " + heart},
		{"a word longer than a row", strings.Repeat("x", 500)},
		{"nine lines", strings.Repeat("line\n", 8) + "end"},
		{"eight lines", strings.Repeat("line\n", 7) + "end"},
		{"wide spaces", "这\u3000这\u3000ab\u2028cd\u00a0ef"},
		{"text that turns around", "ab\u202ecd\u2066ef\u2069"},
	}
}

func boxName(boxed bool) string {
	if boxed {
		return "boxed"
	}
	return "unboxed"
}

func TestLines(t *testing.T) {
	t.Parallel()
	for _, nt := range themes() {
		for _, boxed := range []bool{true, false} {
			for _, width := range lineWidths() {
				for _, d := range drafts() {
					name := nt.name + "/" + boxName(boxed) + "/" + strconv.Itoa(width) + "/" + d.name
					t.Run(name, func(t *testing.T) {
						t.Parallel()
						m := inputbox.New(nt.th, placehold)
						m.SetSize(width, 8, boxed)
						m.SetValue(d.text)
						check(t, m, nt.th, width, 8, boxed, "")

						lines := plainLines(m)
						if !boxed {
							return
						}
						g := nt.th.G
						if side, _, _ := frame(nt.th, width, boxed); side == "" {
							// No room for the sides of a box: the theme
							// draws a rule above and below.
							for _, i := range []int{0, len(lines) - 1} {
								if lines[i] != strings.Repeat(g.HRule, width) {
									t.Errorf("line %d is %q, want a rule", i, lines[i])
								}
							}
							return
						}
						if top := lines[0]; !strings.HasPrefix(top, g.BoxTL+g.BoxH) || !strings.HasSuffix(top, g.BoxH+g.BoxTR) {
							t.Errorf("the first line is not the top of a box: %q", top)
						}
						if bottom := lines[len(lines)-1]; !strings.HasPrefix(bottom, g.BoxBL+g.BoxH) || !strings.HasSuffix(bottom, g.BoxH+g.BoxBR) {
							t.Errorf("the last line is not the bottom of a box: %q", bottom)
						}
						for i, l := range lines[1 : len(lines)-1] {
							if !strings.HasPrefix(l, g.BoxV+" ") || !strings.HasSuffix(l, g.BoxV) {
								t.Errorf("row %d has lost a side of the box: %q", i, l)
							}
						}
					})
				}
			}
		}
	}
}

func TestDefaultSize(t *testing.T) {
	t.Parallel()
	th := unicodeTheme()
	m := inputbox.New(th, placehold)
	check(t, m, th, 80, 8, false, "")
	if got := m.Rows(); got != 1 {
		t.Errorf("Rows is %d before SetSize, want 1", got)
	}
	m.SetValue(strings.Repeat("line\n", 20))
	check(t, m, th, 80, 8, false, "")
	if got := m.Rows(); got != 8 {
		t.Errorf("Rows is %d with 21 lines, want 8", got)
	}
}

func TestPlaceholder(t *testing.T) {
	t.Parallel()
	for _, nt := range themes() {
		for _, boxed := range []bool{true, false} {
			for _, width := range widths() {
				t.Run(nt.name+"/"+boxName(boxed)+"/"+strconv.Itoa(width), func(t *testing.T) {
					t.Parallel()
					th := nt.th
					m := inputbox.New(th, placehold)
					m.SetSize(width, 8, boxed)
					border := 0
					if boxed {
						border = 1
					}
					prompt := th.G.Prompt + " "

					empty := func(when string) {
						t.Helper()
						check(t, m, th, width, 8, boxed, "")
						lines := m.Lines()
						if len(lines) != 1+2*border {
							t.Fatalf("%s: %d lines, want %d", when, len(lines), 1+2*border)
						}
						if got, want := inside(th, lines[border], width, boxed), prompt+placehold; got != want {
							t.Errorf("%s: the row is %q, want %q", when, got, want)
						}
						before, _, _ := frame(th, width, boxed)
						x, y := m.Cursor()
						if wantX := th.Width(before + prompt); x != wantX || y != border {
							t.Errorf("%s: the cursor is at %d,%d, want %d,%d", when, x, y, wantX, border)
						}
					}
					empty("new")

					typed(m, "h")
					check(t, m, th, width, 8, boxed, "")
					if got, want := inside(th, m.Lines()[border], width, boxed), prompt+"h"; got != want {
						t.Errorf("with a draft the row is %q, want %q", got, want)
					}

					m.Reset()
					empty("after Reset")
					typed(m, "h")
					press(m, tea.KeyBackspace, 0)
					empty("after deleting the draft")
				})
			}
		}
	}
}

func TestPlaceholderTooLong(t *testing.T) {
	t.Parallel()
	long := "Message " + strings.Repeat("a contact with a very long name ", 4)
	long = strings.TrimSpace(long)
	for _, nt := range themes() {
		t.Run(nt.name, func(t *testing.T) {
			t.Parallel()
			th := nt.th
			m := inputbox.New(th, long)
			m.SetSize(30, 8, false)
			check(t, m, th, 30, 8, false, "")
			if got := m.Rows(); got != 1 {
				t.Fatalf("a long placeholder takes %d rows, want 1", got)
			}
			row := inside(th, m.Lines()[0], 30, false)
			if !strings.HasPrefix(row, th.G.Prompt+" Message a contact") || !strings.HasSuffix(row, th.G.Ellipsis) {
				t.Errorf("the row is %q, want the start of the placeholder and %q", row, th.G.Ellipsis)
			}
			if got := th.Width(row); got != 30 {
				t.Errorf("the cut placeholder takes %d cells of the row, want all 30: %q", got, row)
			}

			// It is cut for the room there is, not for good.
			m.SetSize(200, 8, true)
			check(t, m, th, 200, 8, true, "")
			if got, want := inside(th, m.Lines()[1], 200, true), th.G.Prompt+" "+long; got != want {
				t.Errorf("with room the row is %q, want %q", got, want)
			}
		})
	}
}

// newline is one way of starting a new line.
type newline struct {
	name string
	do   func(t *testing.T, m *inputbox.Model)
}

// viaUpdate is a newline key that Update handles.
func viaUpdate(code rune, mod tea.KeyMod) func(*testing.T, *inputbox.Model) {
	return func(t *testing.T, m *inputbox.Model) {
		t.Helper()
		cmd, changed := m.Update(keyOf(code, mod))
		if !changed {
			t.Errorf("Update reported no change for %s", keyOf(code, mod))
		}
		if cmd != nil {
			t.Errorf("Update returned a command for %s", keyOf(code, mod))
		}
	}
}

func newlines() []newline {
	return []newline{
		{"ctrl+j", viaUpdate('j', tea.ModCtrl)},
		{"alt+enter", viaUpdate(tea.KeyEnter, tea.ModAlt)},
		{"shift+enter", viaUpdate(tea.KeyEnter, tea.ModShift)},
		{"backslash then enter", func(t *testing.T, m *inputbox.Model) {
			t.Helper()
			typed(m, `\`)
			if text, send := m.Enter(); text != "" || send {
				t.Errorf("Enter after a backslash returned %q, %v, want \"\", false", text, send)
			}
		}},
	}
}

func TestNewlineKeys(t *testing.T) {
	t.Parallel()
	th := unicodeTheme()
	for _, nl := range newlines() {
		t.Run(nl.name, func(t *testing.T) {
			t.Parallel()
			m := inputbox.New(th, placehold)
			m.SetSize(40, 3, true)

			typed(m, "one")
			nl.do(t, m)
			typed(m, "two")
			if got, want := m.Value(), "one\ntwo"; got != want {
				t.Fatalf("the draft is %q, want %q", got, want)
			}
			if got := m.LineCount(); got != 2 {
				t.Errorf("LineCount is %d, want 2", got)
			}
			if got := m.Rows(); got != 4 {
				t.Errorf("Rows is %d, want 4", got)
			}
			check(t, m, th, 40, 3, true, "")
			rows := plainLines(m)
			if got, want := inside(th, rows[1], 40, true), "❯ one"; got != want {
				t.Errorf("the first row is %q, want %q", got, want)
			}
			if got, want := inside(th, rows[2], 40, true), "  two"; got != want {
				t.Errorf("the second row is %q, want %q", got, want)
			}

			// In the middle of a line the rest of the line goes down.
			pressN(m, 2, tea.KeyLeft)
			nl.do(t, m)
			if got, want := m.Value(), "one\nt\nwo"; got != want {
				t.Errorf("the draft is %q, want %q", got, want)
			}
			check(t, m, th, 40, 3, true, "")

			// The box shows three rows, and that is no limit on the draft.
			for range 20 {
				nl.do(t, m)
			}
			if got := m.LineCount(); got != 23 {
				t.Errorf("LineCount is %d after 20 more new lines, want 23", got)
			}
			if got := m.Rows(); got != 5 {
				t.Errorf("Rows is %d, want 5", got)
			}
			check(t, m, th, 40, 3, true, "")
		})
	}
}

func TestEnter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		do   func(m *inputbox.Model)
		text string
		send bool
		// draft is the draft after Enter, and after typing "X" where a
		// newline was made.
		draft string
	}{
		{"a message", func(m *inputbox.Model) { typed(m, "hello") }, "hello", true, "hello"},
		{"an empty box", func(*inputbox.Model) {}, "", true, ""},
		{"only space", func(m *inputbox.Model) { typed(m, "   ") }, "", true, "   "},
		{"trailing space goes", func(m *inputbox.Model) { m.SetValue("hello \n\n  ") }, "hello", true, "hello \n\n  "},
		{"leading space stays", func(m *inputbox.Model) { m.SetValue("  hello\n  there") }, "  hello\n  there", true, "  hello\n  there"},
		{"a command", func(m *inputbox.Model) { typed(m, "/status ") }, "/status", true, "/status "},
		{"backslash at the end", func(m *inputbox.Model) { typed(m, `one\`) }, "", false, "one\nX"},
		{"backslash alone", func(m *inputbox.Model) { typed(m, `\`) }, "", false, "\nX"},
		{"backslash at the end, with the draft selected", func(m *inputbox.Model) {
			typed(m, `one\`)
			press(m, 'g', tea.ModCtrl)
		}, "", false, "one\nX"},
		{"only the last backslash", func(m *inputbox.Model) { typed(m, `one\\`) }, "", false, "one\\\nX"},
		{"backslash before the cursor, mid-line", func(m *inputbox.Model) {
			typed(m, `one\two`)
			pressN(m, 3, tea.KeyLeft)
		}, "", false, "one\nXtwo"},
		{"backslash before the cursor, second line", func(m *inputbox.Model) {
			m.SetValue("zero\none\\two\nthree")
			press(m, tea.KeyUp, 0)
			press(m, tea.KeyHome, 0)
			pressN(m, 4, tea.KeyRight)
		}, "", false, "zero\none\nXtwo\nthree"},
		{"a backslash that is not before the cursor", func(m *inputbox.Model) { typed(m, `one\two`) }, `one\two`, true, `one\two`},
		{"a backslash after the cursor", func(m *inputbox.Model) {
			typed(m, `one\`)
			press(m, tea.KeyLeft, 0)
		}, `one\`, true, `one\`},
	}
	th := unicodeTheme()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := inputbox.New(th, placehold)
			m.SetSize(40, 8, true)
			tc.do(m)
			text, send := m.Enter()
			if text != tc.text || send != tc.send {
				t.Errorf("Enter returned %q, %v, want %q, %v", text, send, tc.text, tc.send)
			}
			if !send {
				typed(m, "X")
			}
			if got := m.Value(); got != tc.draft {
				t.Errorf("the draft is %q, want %q", got, tc.draft)
			}
			check(t, m, th, 40, 8, true, "")
		})
	}
}

func TestPaste(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, pasted, want string
	}{
		{"plain", "hello", "hello"},
		{"LF", "a\nb", "a\nb"},
		{"CRLF", "a\r\nb", "a\nb"},
		{"CR", "a\rb", "a\nb"},
		{"CRLF twice", "a\r\n\r\nb", "a\n\nb"},
		{"CRLF at the end", "a\r\n", "a\n"},
		{"LF then CR", "a\n\rb", "a\n\nb"},
		{"CR then CRLF", "a\r\r\nb", "a\n\nb"},
		{"mixed", "one\r\ntwo\rthree\nfour", "one\ntwo\nthree\nfour"},
		{"a tab is four spaces", "a\tb", "a    b"},
		{"other control characters go", "a\x1b[31mb\x00c\x07", "a[31mbc"},
		{"wide text", cjkText + "\r\n" + zwjFamily, cjkText + "\n" + zwjFamily},
		{"more lines than the box shows", strings.Repeat("l\r\n", 30), strings.Repeat("l\n", 30)},
	}
	th := unicodeTheme()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := inputbox.New(th, placehold)
			m.SetSize(40, 8, true)
			if !pasteOf(m, tc.pasted) {
				t.Error("Update reported no change for a paste")
			}
			if got := m.Value(); got != tc.want {
				t.Errorf("pasting %q gave %q, want %q", tc.pasted, got, tc.want)
			}
			if got, want := m.LineCount(), strings.Count(tc.want, "\n")+1; got != want {
				t.Errorf("LineCount is %d, want %d", got, want)
			}
			check(t, m, th, 40, 8, true, "")

			// SetValue takes text the same way.
			m.SetValue(tc.pasted)
			if got := m.Value(); got != tc.want {
				t.Errorf("SetValue(%q) gave %q, want %q", tc.pasted, got, tc.want)
			}
			check(t, m, th, 40, 8, true, "")
		})
	}
}

func TestPasteAtTheCursor(t *testing.T) {
	t.Parallel()
	th := unicodeTheme()
	m := inputbox.New(th, placehold)
	m.SetSize(40, 8, true)
	typed(m, "ab")
	press(m, tea.KeyLeft, 0)
	pasteOf(m, "1\r\n2")
	if got, want := m.Value(), "a1\n2b"; got != want {
		t.Errorf("the draft is %q, want %q", got, want)
	}
	check(t, m, th, 40, 8, true, "")
	typed(m, "X")
	if got, want := m.Value(), "a1\n2Xb"; got != want {
		t.Errorf("the cursor was not left after the pasted text: %q, want %q", got, want)
	}
	if pasteOf(m, "") {
		t.Error("Update reported a change for an empty paste")
	}
}

func TestClipboard(t *testing.T) {
	t.Parallel()
	th := unicodeTheme()
	m := inputbox.New(th, placehold)
	typed(m, "ab")

	// The paste key asks for the clipboard and changes nothing yet. The
	// command is not run here: it would read this machine's clipboard.
	cmd, changed := m.Update(keyOf('v', tea.ModCtrl))
	if !inputbox.ReadsClipboard(cmd) {
		t.Error("ctrl+v did not return the command that hands the clipboard back as a paste")
	}
	if changed || m.Value() != "ab" {
		t.Errorf("ctrl+v changed the box: changed %v, draft %q", changed, m.Value())
	}

	type clip string
	type failed struct{ error }
	cases := []struct {
		name string
		in   tea.Msg
		want tea.Msg
	}{
		{"text", clip("a\r\nb"), tea.PasteMsg{Content: "a\r\nb"}},
		{"nothing", clip(""), tea.PasteMsg{}},
		{"an error", failed{}, nil},
		{"no answer", nil, nil},
		{"something else", 7, nil},
	}
	for _, tc := range cases {
		if got := inputbox.Pasted(tc.in); got != tc.want {
			t.Errorf("%s: got %#v, want %#v", tc.name, got, tc.want)
		}
	}

	// What comes back goes in like any paste.
	m.Update(inputbox.Pasted(clip("1\r\n2")))
	if got, want := m.Value(), "ab1\n2"; got != want {
		t.Errorf("the draft is %q, want %q", got, want)
	}
}

func TestUpdateReportsChange(t *testing.T) {
	t.Parallel()
	type other struct{}
	cases := []struct {
		name    string
		before  func(m *inputbox.Model)
		msg     tea.Msg
		changed bool
		draft   string
	}{
		{"a character", nil, tea.KeyPressMsg{Code: 'a', Text: "a"}, true, "a"},
		{"a wide character", nil, tea.KeyPressMsg{Code: '这', Text: "这"}, true, "这"},
		{"plain enter is not handled here", func(m *inputbox.Model) { typed(m, "hi") }, keyOf(tea.KeyEnter, 0), false, "hi"},
		{"tab", func(m *inputbox.Model) { typed(m, "hi") }, keyOf(tea.KeyTab, 0), false, "hi"},
		{"esc", func(m *inputbox.Model) { typed(m, "hi") }, keyOf(tea.KeyEscape, 0), false, "hi"},
		{"page up scrolls the screen, not the draft", func(m *inputbox.Model) { m.SetValue("a\nb\nc") }, keyOf(tea.KeyPgUp, 0), false, "a\nb\nc"},
		{"page down", func(m *inputbox.Model) {
			m.SetValue("a\nb\nc")
			pressN(m, 2, tea.KeyUp)
		}, keyOf(tea.KeyPgDown, 0), false, "a\nb\nc"},
		{"down moves the cursor", func(m *inputbox.Model) {
			m.SetValue("a\nb")
			press(m, tea.KeyUp, 0)
		}, keyOf(tea.KeyDown, 0), true, "a\nb"},
		{"down on the last line", func(m *inputbox.Model) { m.SetValue("a\nb") }, keyOf(tea.KeyDown, 0), false, "a\nb"},
		{"up drops a selection", func(m *inputbox.Model) {
			typed(m, "hi")
			press(m, 'g', tea.ModCtrl)
		}, keyOf(tea.KeyUp, 0), true, "hi"},
		{"backspace", func(m *inputbox.Model) { typed(m, "hi") }, keyOf(tea.KeyBackspace, 0), true, "h"},
		{"backspace in an empty box", nil, keyOf(tea.KeyBackspace, 0), false, ""},
		{"left in an empty box", nil, keyOf(tea.KeyLeft, 0), false, ""},
		{"right at the end", func(m *inputbox.Model) { typed(m, "hi") }, keyOf(tea.KeyRight, 0), false, "hi"},
		{"left moves the cursor", func(m *inputbox.Model) { typed(m, "hi") }, keyOf(tea.KeyLeft, 0), true, "hi"},
		{"up moves the cursor", func(m *inputbox.Model) { m.SetValue("a\nb") }, keyOf(tea.KeyUp, 0), true, "a\nb"},
		{"up on the first line", func(m *inputbox.Model) { typed(m, "hi") }, keyOf(tea.KeyUp, 0), false, "hi"},
		{"a selection", func(m *inputbox.Model) { typed(m, "hi") }, keyOf('g', tea.ModCtrl), true, "hi"},
		{"a key release", func(m *inputbox.Model) { typed(m, "hi") }, tea.KeyReleaseMsg{Code: 'a', Text: "a"}, false, "hi"},
		{"a window size", func(m *inputbox.Model) { typed(m, "hi") }, tea.WindowSizeMsg{Width: 10, Height: 10}, false, "hi"},
		{"a message of another kind", func(m *inputbox.Model) { typed(m, "hi") }, other{}, false, "hi"},
		{"no message", func(m *inputbox.Model) { typed(m, "hi") }, nil, false, "hi"},
	}
	th := unicodeTheme()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := inputbox.New(th, placehold)
			m.SetSize(40, 8, true)
			if tc.before != nil {
				tc.before(m)
			}
			lines := m.Lines()
			x, y := m.Cursor()

			cmd, changed := m.Update(tc.msg)
			if changed != tc.changed {
				t.Errorf("changed is %v, want %v", changed, tc.changed)
			}
			if cmd != nil {
				t.Error("Update returned a command")
			}
			if got := m.Value(); got != tc.draft {
				t.Errorf("the draft is %q, want %q", got, tc.draft)
			}
			// "No change" has to be true of what is drawn.
			nx, ny := m.Cursor()
			same := slices.Equal(lines, m.Lines()) && nx == x && ny == y
			if same == changed {
				t.Errorf("changed is %v, and the box is drawn the same: %v", changed, same)
			}
		})
	}
}

func TestFocus(t *testing.T) {
	t.Parallel()
	m := inputbox.New(unicodeTheme(), placehold)
	if cmd := m.Focus(); cmd != nil {
		t.Error("Focus returned a command; nothing blinks")
	}
	typed(m, "a")
	if got := m.Value(); got != "a" {
		t.Errorf("a new box does not take typing: %q", got)
	}
}

func TestScroll(t *testing.T) {
	t.Parallel()
	th := unicodeTheme()
	m := inputbox.New(th, placehold)
	m.SetSize(40, 3, false)
	m.SetValue("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10")

	shown := func(when string, cursorRow int, want ...string) {
		t.Helper()
		check(t, m, th, 40, len(want), false, "")
		got := plainLines(m)
		for i := range got {
			got[i] = strings.TrimRight(got[i], " ")
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: the box shows %q, want %q", when, got, want)
		}
		if _, y := m.Cursor(); y != cursorRow {
			t.Errorf("%s: the cursor is on row %d, want %d", when, y, cursorRow)
		}
	}
	shown("a long draft shows its end", 2, "  l8", "  l9", "  l10")

	pressN(m, 1, tea.KeyUp)
	shown("moving inside what is shown does not scroll", 1, "  l8", "  l9", "  l10")
	pressN(m, 2, tea.KeyUp)
	shown("moving above it scrolls one row", 0, "  l7", "  l8", "  l9")
	pressN(m, 20, tea.KeyUp)
	shown("the first row has the prompt", 0, "❯ l1", "  l2", "  l3")
	pressN(m, 3, tea.KeyDown)
	shown("moving below it scrolls one row", 2, "  l2", "  l3", "  l4")

	m.SetSize(40, 5, false)
	shown("more room shows more rows", 2, "  l2", "  l3", "  l4", "  l5", "  l6")
	m.SetSize(40, 1, false)
	shown("one row shows the cursor's", 0, "  l4")
	m.SetSize(40, 3, false)
	shown("and the rows after it when there is room again", 0, "  l4", "  l5", "  l6")

	// Deleting the end of the draft never leaves a shown row empty.
	m.SetValue("l1\nl2\nl3\nl4\nl5")
	shown("a new draft", 2, "  l3", "  l4", "  l5")
	pressN(m, 3, tea.KeyBackspace)
	shown("a line fewer", 2, "  l2", "  l3", "  l4")
	pressN(m, 6, tea.KeyBackspace)
	shown("fewer lines than rows", 1, "❯ l1", "  l2")
	m.Reset()
	shown("empty again", 0, "❯ "+placehold)
}

func TestSetTheme(t *testing.T) {
	t.Parallel()
	for _, nt := range themes()[1:] {
		for _, boxed := range []bool{true, false} {
			t.Run(nt.name+"/"+boxName(boxed), func(t *testing.T) {
				t.Parallel()
				write := func(m *inputbox.Model) {
					m.SetSize(40, 8, boxed)
					typed(m, "hello")
					press(m, 'j', tea.ModCtrl)
					typed(m, "world")
					pressN(m, 2, tea.KeyLeft)
					m.SetGhost("ghost")
				}
				first := unicodeTheme()
				m := inputbox.New(first, placehold)
				write(m)
				check(t, m, first, 40, 8, boxed, "")
				before := m.Lines()

				m.SetTheme(nt.th)
				check(t, m, nt.th, 40, 8, boxed, "")
				if got := m.Value(); got != "hello\nworld" {
					t.Errorf("the draft is %q after SetTheme", got)
				}

				// It is drawn as a box that had the theme from the start.
				fresh := inputbox.New(nt.th, placehold)
				write(fresh)
				if got, want := m.Lines(), fresh.Lines(); !slices.Equal(got, want) {
					t.Errorf("after SetTheme the box is drawn\n%q\nand a new box with that theme\n%q", got, want)
				}
				x, y := m.Cursor()
				if fx, fy := fresh.Cursor(); x != fx || y != fy {
					t.Errorf("after SetTheme the cursor is at %d,%d, and a new box's at %d,%d", x, y, fx, fy)
				}
				if nt.name != "truecolor dark grapheme" && slices.Equal(before, m.Lines()) {
					t.Error("SetTheme changed nothing that is drawn")
				}

				// The cursor is still where it was in the draft.
				typed(m, "X")
				if got, want := m.Value(), "hello\nworXld"; got != want {
					t.Errorf("the draft is %q, want %q", got, want)
				}
			})
		}
	}
}

func TestSetThemeASCII(t *testing.T) {
	t.Parallel()
	m := inputbox.New(unicodeTheme(), placehold)
	m.SetSize(40, 8, true)
	ascii := func(when string, wantASCII bool) {
		t.Helper()
		ascii := true
		for _, l := range m.Lines() {
			for i := range len(l) {
				ascii = ascii && l[i] < 0x80
			}
		}
		if ascii != wantASCII {
			t.Errorf("%s: every byte is ASCII: %v, want %v (%q)", when, ascii, wantASCII, m.Lines())
		}
	}
	ascii("unicode, empty", false)
	m.SetTheme(asciiTheme())
	ascii("ascii, empty", true)
	typed(m, "/st")
	m.SetGhost("atus")
	ascii("ascii, with a ghost", true)
	m.SetValue("one\ntwo")
	ascii("ascii, two lines", true)
	if got, want := plainLines(m)[1], "| > one"+strings.Repeat(" ", 32)+"|"; got != want {
		t.Errorf("the first row is %q, want %q", got, want)
	}
}

// TestSetThemeRestyles pins what a late answer about the background does:
// the prompt of a box that is already open changes colour.
func TestSetThemeRestyles(t *testing.T) {
	t.Parallel()
	dark := unicodeTheme()
	light := lightTheme()
	m := inputbox.New(dark, placehold)
	m.SetSize(46, 8, false)
	typed(m, "hi")

	prompt := func(th *theme.Theme) string { return th.Style("accent").Render("❯ ") }
	if got := m.Lines()[0]; !strings.HasPrefix(got, prompt(dark)) {
		t.Errorf("the row is %q, want the dark theme's prompt %q first", got, prompt(dark))
	}
	m.SetTheme(light)
	if got := m.Lines()[0]; !strings.HasPrefix(got, prompt(light)) {
		t.Errorf("after SetTheme the row is %q, want the light theme's prompt %q first", got, prompt(light))
	}
	if prompt(dark) == prompt(light) {
		t.Fatal("the two themes draw the prompt the same, so this test shows nothing")
	}
}

func TestNarrow(t *testing.T) {
	t.Parallel()
	for _, nt := range themes() {
		for _, boxed := range []bool{true, false} {
			t.Run(nt.name+"/"+boxName(boxed), func(t *testing.T) {
				t.Parallel()
				m := inputbox.New(nt.th, placehold)
				texts := []string{"", "compare with", "one\ntwo two\n\nfour", cjkText, "ok 😀 " + zwjFamily + " " + skinTone + " " + heart}
				for width := 12; width >= 0; width-- {
					m.SetSize(width, 4, boxed)
					for _, text := range texts {
						m.SetValue(text)
						check(t, m, nt.th, width, 4, boxed, "")
						pressN(m, 3, tea.KeyLeft)
						press(m, tea.KeyUp, 0)
						check(t, m, nt.th, width, 4, boxed, "")
					}
				}
				// A size below nothing is nothing.
				m.SetSize(-5, -5, boxed)
				check(t, m, nt.th, 0, 1, boxed, "")
			})
		}
	}
}

// TestMocks draws the input rows of the mock-ups.
func TestMocks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		th     *theme.Theme
		width  int
		boxed  bool
		draft  string
		ghost  string
		want   []string
		cx, cy int
	}{
		{
			name: "Frame", th: unicodeTheme(), width: 100, boxed: true,
			draft: "and what would the share have been with the fix?",
			want: []string{
				"╭" + strings.Repeat("─", 98) + "╮",
				"│ ❯ and what would the share have been with the fix?" + strings.Repeat(" ", 47) + "│",
				"╰" + strings.Repeat("─", 98) + "╯",
			},
			cx: 52, cy: 1,
		},
		{
			name: "Commands", th: unicodeTheme(), width: 100, boxed: true,
			draft: "/st", ghost: "atusline  preset | edit",
			want: []string{
				"╭" + strings.Repeat("─", 98) + "╮",
				"│ ❯ /statusline  preset | edit" + strings.Repeat(" ", 69) + "│",
				"╰" + strings.Repeat("─", 98) + "╯",
			},
			cx: 7, cy: 1,
		},
		{
			name: "Compatibility, truecolor", th: unicodeTheme(), width: 46,
			draft: "compare with the week before",
			want:  []string{"❯ compare with the week before" + strings.Repeat(" ", 16)},
			cx:    30, cy: 0,
		},
		{
			name: "Compatibility, 16 colours", th: lightTheme(), width: 46,
			draft: "compare with the week before",
			want:  []string{"❯ compare with the week before" + strings.Repeat(" ", 16)},
			cx:    30, cy: 0,
		},
		{
			name: "Compatibility, 30 columns", th: unicodeTheme(), width: 30,
			draft: "compare with the week",
			want:  []string{"❯ compare with the week" + strings.Repeat(" ", 7)},
			cx:    23, cy: 0,
		},
		{
			name: "Compatibility, no colour", th: asciiTheme(), width: 46,
			draft: "compare with the week before",
			want:  []string{"> compare with the week before" + strings.Repeat(" ", 16)},
			cx:    30, cy: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := inputbox.New(tc.th, placehold)
			m.SetSize(tc.width, 8, tc.boxed)
			typed(m, tc.draft)
			m.SetGhost(tc.ghost)
			if got := plainLines(m); !slices.Equal(got, tc.want) {
				t.Errorf("the box is\n%q\nwant\n%q", got, tc.want)
			}
			if x, y := m.Cursor(); x != tc.cx || y != tc.cy {
				t.Errorf("the cursor is at %d,%d, want %d,%d", x, y, tc.cx, tc.cy)
			}
			check(t, m, tc.th, tc.width, 8, tc.boxed, tc.ghost)
		})
	}
}

func TestGhost(t *testing.T) {
	t.Parallel()
	const usage = "atusline  preset | edit"
	cases := []struct {
		name  string
		do    func(m *inputbox.Model)
		ghost string
		shown bool
	}{
		{"at the end of a one-line draft", func(m *inputbox.Model) { typed(m, "/st") }, usage, true},
		{"the usage, after the name and a space", func(m *inputbox.Model) { typed(m, "/statusline ") }, "preset | edit", true},
		{"after a draft that is no command", func(m *inputbox.Model) { typed(m, "hello") }, " there", true},
		{"after wide text", func(m *inputbox.Model) { typed(m, "/这😀") }, usage, true},
		{"back at the end", func(m *inputbox.Model) {
			typed(m, "/st")
			press(m, tea.KeyLeft, 0)
			press(m, tea.KeyRight, 0)
		}, usage, true},
		{"after SetValue", func(m *inputbox.Model) { m.SetValue("/st") }, usage, true},
		{"none set", func(m *inputbox.Model) { typed(m, "/st") }, "", false},
		{"the cursor is not at the end", func(m *inputbox.Model) {
			typed(m, "/st")
			press(m, tea.KeyLeft, 0)
		}, usage, false},
		{"the cursor is at the start", func(m *inputbox.Model) {
			typed(m, "/st")
			press(m, tea.KeyHome, 0)
		}, usage, false},
		{"the draft has two lines", func(m *inputbox.Model) {
			typed(m, "/st")
			press(m, 'j', tea.ModCtrl)
		}, usage, false},
		{"the draft has two lines and the cursor is at the end of the first", func(m *inputbox.Model) {
			m.SetValue("/st\nmore")
			press(m, tea.KeyUp, 0)
			press(m, tea.KeyEnd, 0)
		}, usage, false},
		{"the draft is empty", func(*inputbox.Model) {}, usage, false},
		{"the draft was emptied", func(m *inputbox.Model) {
			typed(m, "/")
			press(m, tea.KeyBackspace, 0)
		}, usage, false},
		{"the draft is selected", func(m *inputbox.Model) {
			typed(m, "/st")
			press(m, 'g', tea.ModCtrl)
		}, usage, false},
		{"it does not fit", func(m *inputbox.Model) { typed(m, "/st") }, strings.Repeat("g", 200), false},
		{"the draft is wrapped", func(m *inputbox.Model) { typed(m, strings.Repeat("word ", 30)) }, usage, false},
	}
	for _, nt := range themes() {
		for _, s := range []size{{100, 8, true}, {46, 8, false}} {
			for _, tc := range cases {
				t.Run(nt.name+"/"+s.String()+"/"+tc.name, func(t *testing.T) {
					t.Parallel()
					th := nt.th
					m := inputbox.New(th, placehold)
					m.SetSize(s.width, s.maxRows, s.boxed)
					tc.do(m)
					without := m.Lines()
					wx, wy := m.Cursor()
					written := m.Value()
					m.SetGhost(tc.ghost)
					// The ghost is drawn, never written.
					defer func() {
						if got := m.Value(); got != written {
							t.Errorf("with a ghost the draft is %q, want %q", got, written)
						}
						if text, _ := m.Enter(); strings.Contains(text, "preset") {
							t.Errorf("Enter returned the ghost: %q", text)
						}
					}()

					border := 0
					if s.boxed {
						border = 1
					}
					lines := m.Lines()
					x, y := m.Cursor()
					if x != wx || y != wy {
						t.Errorf("with a ghost the cursor is at %d,%d, and without at %d,%d", x, y, wx, wy)
					}
					if !tc.shown {
						if !slices.Equal(lines, without) {
							t.Errorf("the box is drawn\n%q\nwant it as without a ghost\n%q", lines, without)
						}
						if tc.ghost != "" && strings.Contains(strings.Join(plainLines(m), "\n"), tc.ghost) {
							t.Errorf("the ghost is drawn: %q", plainLines(m))
						}
						return
					}

					check(t, m, th, s.width, s.maxRows, s.boxed, tc.ghost)
					draft := m.Value()
					if got, want := inside(th, lines[border], s.width, s.boxed), th.G.Prompt+" "+draft+tc.ghost; got != want {
						t.Errorf("the row is %q, want %q", got, want)
					}
					// The ghost is dim, and what is before it is drawn as
					// it is without a ghost.
					row := lines[border]
					dim := th.Style("dim").Render(tc.ghost)
					at := strings.Index(row, dim)
					if at < 0 {
						t.Fatalf("the row %q does not hold the ghost drawn dim, %q", row, dim)
					}
					if !strings.HasPrefix(without[border], row[:at]) {
						t.Errorf("before the ghost the row is %q, and without a ghost it is %q", row[:at], without[border])
					}
					// The cursor is on the ghost's first cell: after the draft.
					before, _, _ := frame(th, s.width, s.boxed)
					if want := th.Width(before + th.G.Prompt + " " + draft); x != want || y != border {
						t.Errorf("the cursor is at %d,%d, want %d,%d", x, y, want, border)
					}

					// Typing on moves the ghost's place; SetGhost("") removes it.
					m.SetGhost("")
					if !slices.Equal(m.Lines(), without) {
						t.Errorf("after SetGhost(\"\") the box is\n%q\nwant\n%q", m.Lines(), without)
					}
				})
			}
		}
	}
}

// TestGhostFits pins the edge: the ghost is drawn when prompt, draft and
// ghost fill the row exactly, and not when they are one cell more.
func TestGhostFits(t *testing.T) {
	t.Parallel()
	for _, nt := range themes() {
		for _, s := range []size{{40, 8, true}, {30, 8, false}} {
			t.Run(nt.name+"/"+s.String(), func(t *testing.T) {
				t.Parallel()
				th := nt.th
				room := roomAfterPrompt(th, s) - 3
				border := 0
				if s.boxed {
					border = 1
				}
				m := inputbox.New(th, placehold)
				m.SetSize(s.width, s.maxRows, s.boxed)
				typed(m, "/st")

				fits := strings.Repeat("g", room)
				m.SetGhost(fits)
				check(t, m, th, s.width, s.maxRows, s.boxed, fits)
				if got, want := inside(th, m.Lines()[border], s.width, s.boxed), th.G.Prompt+" /st"+fits; got != want {
					t.Errorf("a ghost that fills the row: the row is %q, want %q", got, want)
				}

				m.SetGhost(fits + "g")
				check(t, m, th, s.width, s.maxRows, s.boxed, "")
				if got, want := inside(th, m.Lines()[border], s.width, s.boxed), th.G.Prompt+" /st"; got != want {
					t.Errorf("a ghost one cell too long: the row is %q, want %q", got, want)
				}

				// A narrower box drops a ghost that fitted.
				m.SetGhost(fits)
				m.SetSize(s.width-1, s.maxRows, s.boxed)
				check(t, m, th, s.width-1, s.maxRows, s.boxed, "")
				m.SetSize(s.width, s.maxRows, s.boxed)
				check(t, m, th, s.width, s.maxRows, s.boxed, fits)
			})
		}
	}
}

// TestGhostNeedsOneRow covers a draft that fits its row by the theme's count
// and not by the text area's: the text area has given the cursor a second
// row, so the box has two rows and no ghost.
func TestGhostNeedsOneRow(t *testing.T) {
	t.Parallel()
	th := unicodeTheme()
	m := inputbox.New(th, placehold)
	m.SetSize(30, 8, false)
	// 14 hearts are 14 cells where characters are counted, and 28, a full
	// row, where clusters are.
	pasteOf(m, strings.Repeat(heart, 14))
	m.SetGhost("  usage")
	if got := m.Rows(); got != 2 {
		t.Fatalf("Rows is %d, want 2: this draft no longer fills the text area's row", got)
	}
	check(t, m, th, 30, 8, false, "")
	if got := strings.Join(plainLines(m), "\n"); strings.Contains(got, "usage") {
		t.Errorf("the ghost is drawn on a draft of two rows: %q", got)
	}
}

// TestLinesAreTheCallers checks that the lines are a copy: the box keeps
// what it drew until something changes, and a screen may write over what it
// was given.
func TestLinesAreTheCallers(t *testing.T) {
	t.Parallel()
	th := unicodeTheme()
	m := inputbox.New(th, placehold)
	m.SetSize(40, 8, true)
	typed(m, "hello")
	first := m.Lines()
	want := slices.Clone(first)
	for i := range first {
		first[i] = "overwritten"
	}
	if got := m.Lines(); !slices.Equal(got, want) {
		t.Errorf("after the caller wrote over its lines the box is\n%q\nwant\n%q", got, want)
	}
	check(t, m, th, 40, 8, true, "")
}
