package inputbox

import (
	"reflect"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// OwnCursorRows draws every row of the draft the way the text area draws it
// when it shows a cursor of its own: the cell under the cursor is in reverse
// video. A test compares that cell with what Cursor reports. It returns nil
// when the text area panics over the draft, which it does over some.
func (m *Model) OwnCursorRows() (rows []string) {
	defer func() {
		if recover() != nil {
			rows = nil
		}
	}()
	twin := m.ta
	twin.SetVirtualCursor(true)
	return strings.Split(twin.View(), "\n")
}

// DraftRows draws every row of the draft as the text area draws it: the
// prompt and the text, with no cursor. The box never calls this drawing; a
// test compares the rows the box draws with it. It returns nil when the
// text area panics over the draft.
func (m *Model) DraftRows() (rows []string) {
	defer func() {
		if recover() != nil {
			rows = nil
		}
	}()
	return strings.Split(m.ta.View(), "\n")
}

// PlainRows draws every row of the draft as the box draws them.
func (m *Model) PlainRows() []string { return m.rows(0, m.ta.Height()) }

// WrapWidth returns the number of cells of a row the text area wraps the
// draft to, prompt not included.
func (m *Model) WrapWidth() int { return m.ta.Width() }

// Top returns the first row of the draft that is shown.
func (m *Model) Top() int { return m.top }

// Pasted is pasted, for the tests.
func Pasted(msg tea.Msg) tea.Msg { return pasted(msg) }

// ReadsClipboard reports whether cmd is the command that reads the system
// clipboard and hands its text back as a tea.PasteMsg. A test can tell it
// from the text area's own without running either.
func ReadsClipboard(cmd tea.Cmd) bool {
	return cmd != nil && reflect.ValueOf(cmd).Pointer() == reflect.ValueOf(clipboard).Pointer()
}

// Visible is visible, for the tests.
func Visible(s string, wc bool) string { return visible(s, wc) }

// Guarded is guarded, for the tests: the message goes to the text area at
// the width the draft is wrapped to, which is where it can panic.
func (m *Model) Guarded(msg tea.Msg) tea.Cmd { return m.guarded(msg) }

// Flat is flat, for the tests, without the cells the prompt takes: the
// width of text the text area is given while it takes msg.
func (m *Model) Flat(msg tea.Msg) int { return m.flat(msg) - len(m.blank) }

// RowTexts returns the text of every row of the draft, with every space a
// plain one and without the prompt: all of each row, also the spaces at its
// end that the box has no room to draw.
func (m *Model) RowTexts() []string {
	segments := m.segments(0, m.ta.Height())
	out := make([]string, len(segments))
	for i, s := range segments {
		out[i] = s.text()
	}
	return out
}
