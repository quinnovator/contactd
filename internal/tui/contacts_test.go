package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/client"
)

// Under the width of the label's column a short label is drawn whole, with
// no mark of having been cut after the spaces that would have filled the
// column, and a long one is cut where the row ends.
func TestTheListOnANarrowTerminal(t *testing.T) {
	t.Parallel()
	contacts := []client.Contact{
		{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash", LastActive: fixedNow().Add(-3 * time.Minute).UnixMilli()},
		{ID: 2, Name: "a contact with a name that goes on and on", Provider: "fake", Model: "fake-1"},
	}
	for _, tc := range []struct {
		caps  string
		width int
		rows  [2]string
	}{
		{"truecolor", 20, [2]string{"▸ ada", "  a contact with a …"}},
		{"truecolor", 12, [2]string{"▸ ada", "  a contact…"}},
		{"truecolor", 26, [2]string{"▸ ada", "  a contact with a name t…"}},
		{"none-ascii", 20, [2]string{"> ada", "  a contact with ..."}},
		{"truecolor", 3, [2]string{"▸ …", "  …"}},
	} {
		t.Run(fmt.Sprintf("%s at %d columns", tc.caps, tc.width), func(t *testing.T) {
			t.Parallel()
			caps := trueDark()
			if tc.caps == "none-ascii" {
				caps = noneASCII()
			}
			h := newHarness(t, caps, contacts...)
			h.size(tc.width, 12)
			h.send(h.m.showContacts()())
			rows := h.rows()
			for i, want := range tc.rows {
				if got := strings.TrimRight(rows[2+i], " "); got != want {
					t.Errorf("row %d is %q, want %q:\n%s", 2+i, got, want, h.screen())
				}
			}
			h.wantFits("the list", tc.width, 12)
		})
	}
}

// A long system prompt has the rows the terminal can spare: the blank rows
// and the tip give way to it, and the rows that say what the form is and
// what its keys are stay.
func TestTheSystemPromptTakesTheRowsThereAre(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		width, height int
		// lines is how many lines the prompt has, and shown how many of them
		// the form shows: the last ones, where the cursor is.
		lines, shown int
		// blanks says the blank rows are still there, hints the key hints.
		blanks, hints bool
	}{
		{width: 100, height: 30, lines: 3, shown: 3, blanks: true, hints: true},
		{width: 100, height: 30, lines: 20, shown: systemRows, blanks: true, hints: true},
		{width: 100, height: 16, lines: 20, shown: 11, hints: true},
		{width: 100, height: 12, lines: 3, shown: 3, blanks: true, hints: true},
		{width: 100, height: 10, lines: 5, shown: 5, hints: true},
		{width: 100, height: 10, lines: 9, shown: 5, hints: true},
		{width: 30, height: 8, lines: 3, shown: 3, hints: true},
		{width: 30, height: 5, lines: 3, shown: 1},
		{width: 30, height: 2, lines: 3, shown: 1},
	} {
		t.Run(fmt.Sprintf("%d lines at %dx%d", tc.lines, tc.width, tc.height), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, trueDark())
			h.size(tc.width, tc.height)
			h.send(h.m.showContacts()())
			h.typeText("n")
			h.typeText("carol")
			h.key(enter())
			h.typeText("line 1")
			for i := 2; i <= tc.lines; i++ {
				h.key(ctrl('j'))
				h.typeText(fmt.Sprintf("line %d", i))
			}
			screen := h.screen()
			for i := 1; i <= tc.lines; i++ {
				want := i > tc.lines-tc.shown
				if got := strings.Contains(screen, fmt.Sprintf("line %d ", i)); got != want {
					t.Errorf("line %d is shown: %v, want %v:\n%s", i, got, want, screen)
				}
			}
			rows := h.rows()
			if got := strings.Contains(screen, "tab switch field"); got != tc.hints {
				t.Errorf("the key hints are shown: %v, want %v:\n%s", got, tc.hints, screen)
			}
			blanks := 0
			for _, row := range rows {
				if strings.TrimSpace(row) == "" {
					blanks++
				}
			}
			if (blanks > 0) != tc.blanks {
				t.Errorf("the form has %d blank rows, want some: %v:\n%s", blanks, tc.blanks, screen)
			}
			// The cursor is at the end of the last line, which is in view.
			v := h.m.View()
			last := fmt.Sprintf("line %d", tc.lines)
			if v.Cursor == nil || v.Cursor.Y >= len(rows) || !strings.Contains(rows[v.Cursor.Y], last) || v.Cursor.X != 2+len(last) {
				t.Errorf("the cursor is at %+v, want the end of %q:\n%s", v.Cursor, last, screen)
			}
			h.wantFits("the form", tc.width, tc.height)

			// The name's box is still there to go back to.
			h.key(special(tea.KeyTab))
			v = h.m.View()
			if v.Cursor == nil || !strings.Contains(h.rows()[v.Cursor.Y], "❯ carol") {
				t.Errorf("back in the name, the cursor is at %+v:\n%s", v.Cursor, h.screen())
			}
			h.wantFits("the form", tc.width, tc.height)
		})
	}
}

// While the server is asked to create or rename a contact the form takes no
// key: a second enter must not ask twice. Leaving is not held up by it.
func TestCtrlCLeavesAFormThatWaits(t *testing.T) {
	t.Parallel()
	for _, form := range []string{"new", "rename"} {
		t.Run(form, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, trueDark(), client.Contact{ID: 1, Name: "ada"})
			h.send(h.m.showContacts()())
			var asked tea.Cmd
			if form == "new" {
				h.typeText("n")
				h.typeText("carol")
				h.key(enter())
				h.typeText("You are Carol.")
				asked = h.key(enter())
				h.wantOnScreen("creating…")
			} else {
				h.typeText("r")
				h.typeText("-lovelace")
				asked = h.key(enter())
			}
			if asked == nil || !h.m.busy {
				t.Fatal("enter did not ask the server")
			}
			// The server has not answered. Typing and enter do nothing.
			h.typeText("x")
			if cmd := h.key(enter()); cmd != nil {
				t.Error("a second enter asked the server again")
			}
			if cmd := h.key(esc()); cmd != nil || h.m.screen == screenContacts {
				t.Error("esc left a form that waits for the server")
			}
			if got := h.m.name.Value() + h.m.system.Value(); strings.Contains(got, "x") {
				t.Errorf("a key was typed into a form that waits: %q", got)
			}
			cmd := h.key(ctrl('c'))
			if cmd == nil {
				t.Fatal("ctrl+c did nothing while the form waits for the server")
			}
			if _, quit := cmd().(tea.QuitMsg); !quit {
				t.Error("ctrl+c did not quit")
			}
		})
	}
}

// A name is one line in its box, also the name the server has when the box
// opens on it: a name with a line break in it must not hide its first line.
func TestRenamingANameOfTwoLines(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark(), client.Contact{ID: 1, Name: "ada\r\nlovelace"})
	h.send(h.m.showContacts()())
	h.typeText("r")
	if got := h.m.name.Value(); got != "ada lovelace" {
		t.Errorf("the box holds %q", got)
	}
	h.wantOnScreen("Rename ada lovelace", "❯ ada lovelace")
	v := h.m.View()
	if v.Cursor == nil || v.Cursor.Y != 2 || v.Cursor.X != len("> ada lovelace") {
		t.Errorf("the cursor is at %+v, want the end of the name", v.Cursor)
	}
}
