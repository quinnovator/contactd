package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/api/apitest"
	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/scrollback"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

func alt(c rune) tea.KeyPressMsg   { return tea.KeyPressMsg{Code: c, Mod: tea.ModAlt} }
func shift(c rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: c, Mod: tea.ModShift} }

// messages is a page of the log: one message from here for each sequence
// number from first to last, all at one moment.
func messages(t *testing.T, first, last uint64, at time.Time) client.Update {
	t.Helper()
	events := make([]core.Event, 0, last-first+1)
	for seq := first; seq <= last; seq++ {
		events = append(events, logged(t, seq, at, core.KindUserMsg, core.MsgBody{Text: fmt.Sprintf("message %d", seq)}))
	}
	return client.Update{Kind: client.UpdateEvents, Events: events}
}

// wantFits fails the test when the screen has more rows than the terminal,
// or a row wider than it, by either way a terminal counts cells.
func (h *harness) wantFits(what string, width, height int) {
	h.t.Helper()
	rows := h.rows()
	if len(rows) > height {
		h.t.Errorf("%s at %dx%d: %d rows", what, width, height, len(rows))
	}
	for i, row := range rows {
		if w := max(ansi.StringWidth(row), ansi.StringWidthWc(row)); w > width {
			h.t.Errorf("%s at %dx%d: row %d is %d cells wide: %q", what, width, height, i, w, row)
		}
	}
}

// A terminal that answers late, or a setting changed while a conversation is
// open, goes through setCaps: everything on screen is drawn again in the new
// look, the box being typed in included.
func TestLateCapsRestyleAnOpenChat(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.from(sess, live(), client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 1, fixedNow(), core.KindUserMsg, core.MsgBody{Text: "plot last week's latency"}),
		logged(t, 2, fixedNow(), core.KindToolCall, core.ToolCall{ID: "c", Name: "python", Code: "df.plot.bar()"}),
		logged(t, 3, fixedNow(), core.KindToolResult, core.ToolResult{ID: "c", Output: "saved latency.png\n"}),
		logged(t, 4, fixedNow(), core.KindAgentMsg, core.MsgBody{Text: "p99 peaked on Thursday at 7.2 s."}),
	}})
	h.typeText("compare")
	h.wantOnScreen("❯ plot last week's latency", "⏺ python", "⎿ saved latency.png", "● live │ fake-1", "│ ❯ compare", "╭─", "enter send · ctrl+j")
	if raw := h.m.View().Content; !strings.Contains(raw, "\x1b[1;38;2;94;196;200m") {
		t.Errorf("the truecolor accent is not on screen:\n%q", raw)
	}
	if h.m.setCaps(trueDark()) != nil {
		t.Error("the same caps again asked for the screen to be cleared")
	}

	cmd := h.m.setCaps(noneASCII())
	if cmd == nil {
		t.Fatal("a change of caps did not ask for the screen to be cleared")
	}
	if cmd() != tea.ClearScreen() {
		t.Errorf("a change of caps answered %T, want the screen cleared", cmd())
	}
	h.wantOnScreen("> plot last week's latency", "* python", "|_ saved latency.png", "* live | fake-1", "| > compare", "+-", "enter send | ctrl+j")
	raw := h.m.View().Content
	for i := range len(raw) {
		if raw[i] >= 0x80 {
			t.Fatalf("with ASCII glyphs the screen holds byte %#x at %d:\n%s", raw[i], i, h.screen())
		}
	}
	if strings.Contains(raw, "38;2;") || strings.Contains(raw, "38;5;") {
		t.Errorf("with no colour the screen still holds one:\n%q", raw)
	}
	if got := h.m.chat.input.Value(); got != "compare" {
		t.Errorf("the draft became %q", got)
	}

	// Key enhancements reported late change the key the hint line names.
	caps := trueDark()
	caps.ShiftEnter = true
	h.size(100, 24)
	h.m.setCaps(caps)
	h.wantOnScreen("❯ plot last week's latency", "shift+enter new line")
	h.wantNotOnScreen("ctrl+j")

	// The list and its forms follow too.
	h.send(h.key(ctrl('p'))())
	h.typeText("n")
	h.wantOnScreen("New contact", "shift+enter new line", "❯")
	h.m.setCaps(noneASCII())
	h.wantOnScreen("New contact", "ctrl+j new line", ">")
	h.wantNotOnScreen("❯")
}

// Nothing a key press, a fragment of a reply or a resize costs may grow with
// the length of the conversation.
func TestWorkDoesNotGrowWithHistory(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.from(sess, live(), messages(t, 1, 10000, fixedNow()))
	h.wantOnScreen("message 10000")
	c := h.m.chat
	if got := c.list.Len(); got != 10001 {
		t.Fatalf("the list has %d blocks, want 10000 messages and a day's rule", got)
	}
	drawn := func() int { return c.win.Stats().Rendered }
	shown := func() int {
		first, last, ok := c.win.Visible()
		if !ok {
			t.Fatal("no block is in view")
		}
		return last - first + 1
	}
	if drawn() != shown() || drawn() > c.rows.Body {
		t.Fatalf("opening drew %d blocks with %d in view", drawn(), shown())
	}
	was, built := drawn(), c.rebuilds

	// One key press.
	h.typeText("x")
	h.wantOnScreen("│ ❯ x", "message 10000")
	if drawn() != was || c.rebuilds != built {
		t.Errorf("a key press drew %d blocks and rebuilt the list %d times", drawn()-was, c.rebuilds-built)
	}

	// A minute passing.
	h.now = h.now.Add(time.Minute)
	h.send(clockMsg{})
	h.wantOnScreen("12:01", "message 10000")
	if drawn() != was || c.rebuilds != built {
		t.Errorf("the clock drew %d blocks and rebuilt the list %d times", drawn()-was, c.rebuilds-built)
	}

	// A batch of a reply being written draws no settled block.
	h.from(sess,
		delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
		delta(core.Delta{Type: core.DeltaText, Text: "Counting "}),
		delta(core.Delta{Type: core.DeltaText, Text: "them all."}),
	)
	h.wantOnScreen("Counting them all.", "replying │ esc stop")
	if drawn() != was || c.rebuilds != built {
		t.Errorf("a streamed batch drew %d settled blocks and rebuilt the list %d times", drawn()-was, c.rebuilds-built)
	}

	// A page of scrolling draws the blocks that come into view.
	h.key(special(tea.KeyPgUp))
	h.wantNotOnScreen("Counting them all.")
	if got := drawn() - was; got < 1 || got > c.rows.Body || c.rebuilds != built {
		t.Errorf("a page up drew %d blocks and rebuilt the list %d times", got, c.rebuilds-built)
	}
	h.key(special(tea.KeyPgDown))
	h.wantOnScreen("Counting them all.")

	// A resize draws the blocks in view again, and no others.
	was = drawn()
	h.size(64, 30)
	h.wantOnScreen("message 10000", "Counting them all.")
	if got := drawn() - was; got != shown() || got > c.rows.Body || c.rebuilds != built {
		t.Errorf("a resize drew %d blocks with %d in view, and rebuilt the list %d times", got, shown(), c.rebuilds-built)
	}

	// Unfolding draws only what is in view as well.
	was = drawn()
	h.key(ctrl('o'))
	h.screen()
	if got := drawn() - was; got != shown() || c.rebuilds != built {
		t.Errorf("ctrl+o drew %d blocks with %d in view, and rebuilt the list %d times", got, shown(), c.rebuilds-built)
	}

	// The end of the turn changes the list: it is built once, and only the
	// new block is drawn.
	was = drawn()
	h.from(sess,
		delta(core.Delta{Type: core.DeltaMessage, Text: "Counting them all.", Seq: 10001}),
		delta(core.Delta{Type: core.DeltaTyping}),
	)
	h.wantOnScreen("Counting them all.")
	if got := drawn() - was; got != 1 || c.rebuilds != built+1 {
		t.Errorf("a reply settling drew %d blocks and rebuilt the list %d times", got, c.rebuilds-built)
	}
}

// View hands back what it drew last until something changes, so a client
// with nothing going on writes nothing.
func TestTheViewIsKeptUntilSomethingChanges(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.from(sess, live(), messages(t, 1, 3, fixedNow()))
	c := h.m.chat
	first := h.m.View()
	if c.dirty != 0 {
		t.Fatalf("after a draw the regions %b are still stale", c.dirty)
	}
	drawn := c.win.Stats()

	// A session that says nothing new, and a key the chat has no use for.
	h.send(updatesMsg{sess: sess})
	h.send(tea.KeyPressMsg{Code: tea.KeyTab})
	h.send(tea.FocusMsg{})
	again := h.m.View()
	if again.Content != first.Content || *again.Cursor != *first.Cursor {
		t.Error("the screen changed with nothing to change it")
	}
	if got := c.win.Stats(); got.Rendered != drawn.Rendered {
		t.Errorf("an idle redraw drew %d blocks", got.Rendered-drawn.Rendered)
	}

	// Only the clock wakes it, once a minute.
	h.now = h.now.Add(time.Minute)
	if cmd := h.send(clockMsg{}); cmd == nil {
		t.Error("the clock did not ask for its next minute")
	}
	h.wantOnScreen("12:01")
	h.wantNotOnScreen("12:00")
	if got := c.win.Stats(); got.Rendered != drawn.Rendered {
		t.Errorf("the clock drew %d blocks", got.Rendered-drawn.Rendered)
	}

	// The list of contacts is kept the same way.
	h.send(h.key(ctrl('p'))())
	list := h.m.View()
	if h.m.stale {
		t.Fatal("after a draw the list is still stale")
	}
	if again := h.m.View(); again.Content != list.Content {
		t.Error("the list changed with nothing to change it")
	}
}

func TestChatKeys(t *testing.T) {
	t.Parallel()

	t.Run("the four ways to start a new line", func(t *testing.T) {
		t.Parallel()
		for name, newline := range map[string]func(h *harness){
			"ctrl+j":               func(h *harness) { h.key(ctrl('j')) },
			"alt+enter":            func(h *harness) { h.key(alt(tea.KeyEnter)) },
			"shift+enter":          func(h *harness) { h.key(shift(tea.KeyEnter)) },
			"backslash then enter": func(h *harness) { h.typeText(`\`); h.key(enter()) },
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				h := newHarness(t, trueDark())
				sess := h.openChat()
				h.from(sess, live())
				h.typeText("one")
				newline(h)
				h.typeText("two")
				if got := h.m.chat.input.Value(); got != "one\ntwo" {
					t.Fatalf("the box holds %q", got)
				}
				if len(sess.said) != 0 {
					t.Fatalf("a new line sent %q", sess.said)
				}
				// The box grew by a row, and the transcript gave it.
				h.wantOnScreen("│ ❯ one", "│   two")
				if c := h.m.chat; c.rows.Input != 4 || c.rows.Body != 16 || len(h.rows()) != 24 {
					t.Errorf("the rows are %+v", c.rows)
				}
				h.key(enter())
				if fmt.Sprint(sess.said) != "[one\ntwo]" {
					t.Errorf("said = %q", sess.said)
				}
				if c := h.m.chat; c.rows.Input != 3 || c.input.Value() != "" {
					t.Errorf("after sending the box holds %q in %d rows", c.input.Value(), c.rows.Input)
				}
			})
		}
	})

	t.Run("ctrl+d leaves an empty box and deletes in a full one", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, trueDark())
		h.openChat()
		h.typeText("ab")
		h.key(special(tea.KeyHome))
		if cmd := h.key(ctrl('d')); cmd != nil {
			t.Fatal("ctrl+d with something typed did more than delete")
		}
		if got := h.m.chat.input.Value(); got != "b" {
			t.Fatalf("the box holds %q", got)
		}
		h.key(ctrl('d'))
		cmd := h.key(ctrl('d'))
		if cmd == nil {
			t.Fatal("ctrl+d on an empty box did nothing")
		}
		if _, quit := cmd().(tea.QuitMsg); !quit {
			t.Error("ctrl+d on an empty box did not quit")
		}
	})

	t.Run("ctrl+l draws the whole screen again", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, trueDark())
		h.openChat()
		cmd := h.key(ctrl('l'))
		if cmd == nil {
			t.Fatal("ctrl+l did nothing")
		}
		if cmd() != tea.ClearScreen() {
			t.Errorf("ctrl+l answered %T", cmd())
		}
	})

	t.Run("the arrows scroll under a one-line draft and move in a longer one", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, trueDark())
		sess := h.openChat()
		h.from(sess, live(), messages(t, 1, 40, fixedNow()))
		h.typeText("draft")
		h.wantOnScreen("message 40")
		h.wantNotOnScreen("newer")
		h.key(special(tea.KeyUp))
		h.wantNotOnScreen("message 40")
		h.wantOnScreen("message 38", "↓ newer")
		h.key(special(tea.KeyDown))
		h.wantOnScreen("message 40")
		h.wantNotOnScreen("newer")

		h.key(ctrl('j'))
		h.typeText("more")
		for range 3 {
			h.key(special(tea.KeyUp))
		}
		h.wantOnScreen("message 40")
		h.typeText("!")
		if got := h.m.chat.input.Value(); got != "draf!t\nmore" {
			t.Errorf("up did not move the cursor into the first line: the box holds %q", got)
		}
		// Page up and page down still scroll.
		h.key(special(tea.KeyPgUp))
		h.wantNotOnScreen("message 40")
		h.key(special(tea.KeyPgDown))
		h.wantOnScreen("message 40")
	})

	t.Run("a pasted message keeps its lines", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, trueDark())
		sess := h.openChat()
		h.from(sess, live())
		h.send(tea.PasteMsg{Content: "first\r\nsecond"})
		if got := h.m.chat.input.Value(); got != "first\nsecond" {
			t.Fatalf("the box holds %q", got)
		}
		if sess.typings != 1 {
			t.Errorf("a paste reported typing %d times", sess.typings)
		}
		h.key(enter())
		if fmt.Sprint(sess.said) != "[first\nsecond]" {
			t.Errorf("said = %q", sess.said)
		}
	})

	t.Run("a message over the limit is not sent", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, trueDark())
		sess := h.openChat()
		h.from(sess, live())
		// 64 bytes a line, and one byte more than the server takes.
		long := strings.Repeat(strings.Repeat("lorem ipsum ", 5)+"abc\n", maxMessageBytes/64) + "x"
		h.send(tea.PasteMsg{Content: long})
		h.key(enter())
		if len(sess.said) != 0 {
			t.Fatal("a message over the limit was sent")
		}
		h.wantOnScreen("not sent: the message is 65537 bytes and the limit is 65536")
		if got := h.m.chat.input.Value(); got != long {
			t.Errorf("the box no longer holds the message: %d bytes", len(got))
		}
		h.wantFits("a long draft", 80, 24)
	})
}

// A note takes the place of the key hints until the next key. Where the
// terminal is too short for a hint row it takes the status bar's.
func TestNotes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		width, height int
		hintRow       bool
	}{
		{"on the hint row", 80, 24, true},
		{"on the bar's row", 46, 12, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, trueDark())
			h.size(tc.width, tc.height)
			h.openChat()
			h.typeText("hello")
			last := func() string { rows := h.rows(); return rows[len(rows)-1] }
			if hinted := strings.Contains(last(), "enter send"); hinted != tc.hintRow {
				t.Fatalf("the last row is %q", last())
			}
			h.wantOnScreen("● connecting")

			h.key(enter())
			h.wantOnScreen("not sent: not connected", "❯ hello")
			h.wantNotOnScreen("enter send")
			if tc.hintRow {
				h.wantOnScreen("● connecting")
				if got := strings.TrimSpace(last()); got != "not sent: not connected" {
					t.Errorf("the last row is %q", got)
				}
			} else {
				h.wantNotOnScreen("● connecting")
			}
			h.wantFits(tc.name, tc.width, tc.height)
			if raw := h.m.View().Content; !strings.Contains(raw, h.m.th.Style("warn").Render("not sent: not connected")) {
				t.Errorf("the note is not in the warn tone:\n%q", raw)
			}

			// The next key puts the row back.
			h.typeText("!")
			h.wantNotOnScreen("not sent")
			h.wantOnScreen("● connecting", "❯ hello!")
			if hinted := strings.Contains(last(), "enter send"); hinted != tc.hintRow {
				t.Errorf("the last row is %q", last())
			}
		})
	}
}

// A note can quote the server: why a message was refused, or why the
// connection dropped. None of it may reach the terminal as more than text.
func TestNotesFromTheServerAreMadeSafe(t *testing.T) {
	t.Parallel()
	hostile := "\x1b]0;owned\x07\x1b[2Jno\u202e room\r\nat all\x00"
	for name, update := range map[string]client.Update{
		"a refusal":              {Kind: client.UpdateRefused, Text: "hello", Reason: hostile},
		"a dropped connection":   {Kind: client.UpdateState, State: client.StateDown, Err: errors.New(hostile)},
		"a page that never came": {Kind: client.UpdateOlder, Err: errors.New(hostile)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, size := range [][2]int{{100, 30}, {46, 12}} {
				h := newHarness(t, trueDark())
				h.size(size[0], size[1])
				sess := h.openChat()
				h.from(sess, live(), update)
				// The escape character is dropped; the rest is shown as text.
				h.wantOnScreen("]0;owned[2J")
				if size[0] == 100 {
					h.wantOnScreen("]0;owned[2Jno room at all")
				}
				raw := h.m.View().Content
				for _, bad := range []string{"\x1b]", "\x07", "\x1b[2J", "\u202e", "\x00", "\r"} {
					if strings.Contains(raw, bad) {
						t.Errorf("at %dx%d the screen contains %q", size[0], size[1], bad)
					}
				}
				h.wantFits(name, size[0], size[1])
			}
		})
	}
}

// The time a tool has run is drawn from a tick that exists only while a tool
// runs.
func TestTheToolTickRunsOnlyWhileAToolRuns(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	c := h.m.chat
	if cmd := h.send(updatesMsg{sess: sess, batch: []client.Update{live()}, closed: true}); cmd != nil || c.ticking {
		t.Fatal("a tick was asked for with no tool running")
	}
	h.from(sess, delta(core.Delta{Type: core.DeltaTyping, Typing: true}), delta(core.Delta{Type: core.DeltaText, Text: "Let me look."}))
	if c.ticking {
		t.Fatal("a reply being written started the tick")
	}
	if cmd := h.send(toolTickMsg{sess: sess}); cmd != nil {
		t.Fatal("a stray tick asked for another")
	}

	start := updatesMsg{sess: sess, closed: true, batch: []client.Update{
		delta(core.Delta{Type: core.DeltaToolCall, Tool: "python", Text: "import time", Seq: 1}),
	}}
	if cmd := h.send(start); cmd == nil || !c.ticking {
		t.Fatal("a tool started and no tick was asked for")
	}
	h.wantOnScreen("⏺ python", "⎿ running… 0.0s", "running python │ esc stop")
	// A second batch while it runs does not start a second chain of ticks.
	if cmd := h.send(updatesMsg{sess: sess, closed: true, batch: []client.Update{live()}}); cmd != nil {
		t.Error("a second tick was asked for while one was on its way")
	}
	drawn := c.win.Stats().Rendered
	for _, want := range []string{"0.2s", "0.4s"} {
		h.now = h.now.Add(toolTickEvery)
		if cmd := h.send(toolTickMsg{sess: sess}); cmd == nil {
			t.Fatal("a tick while the tool runs did not ask for the next")
		}
		h.wantOnScreen("⎿ running… " + want)
	}
	if got := c.win.Stats().Rendered; got != drawn {
		t.Errorf("the ticks drew %d settled blocks", got-drawn)
	}

	h.from(sess, delta(core.Delta{Type: core.DeltaToolResult, Tool: "python", Text: "done", Seq: 2}))
	h.wantOnScreen("⎿ done")
	h.wantNotOnScreen("running")
	if cmd := h.send(toolTickMsg{sess: sess}); cmd != nil || c.ticking {
		t.Error("the tick went on after the tool ended")
	}

	// A tick for a conversation that was left is dropped.
	h.send(h.key(ctrl('p'))())
	if cmd := h.send(toolTickMsg{sess: sess}); cmd != nil {
		t.Error("a tick for a conversation that was left asked for another")
	}
}

// The header names the days of what is in view, so it follows the scroll.
func TestTheHeaderNamesTheDaysInView(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.wantNotOnScreen("Oct")
	h.from(sess, live(),
		messages(t, 1, 30, fixedNow().Add(-5*24*time.Hour)),
		messages(t, 31, 60, fixedNow()),
	)
	header := func() string { return h.rows()[0] }
	// pageUpTo scrolls up until the header ends with what is wanted.
	pageUpTo := func(want string) {
		t.Helper()
		for range 12 {
			if strings.HasSuffix(header(), want) {
				return
			}
			h.key(special(tea.KeyPgUp))
		}
		t.Errorf("the header never said %q; it is %q", want, header())
	}
	if !strings.HasSuffix(header(), "  12 Oct") || strings.Contains(header(), "7 Oct") {
		t.Errorf("at the newest message the header is %q", header())
	}
	pageUpTo("  7 Oct – 12 Oct")
	h.wantOnScreen("message 30", "Mon 12 Oct 2026", "message 31")
	pageUpTo("  7 Oct")
	h.wantNotOnScreen("Mon 12 Oct 2026")

	// Under 80 columns the header says how the cache did, once a call was made.
	h.size(60, 24)
	if got := strings.TrimSpace(header()); got != "ada  fake-1" {
		t.Errorf("before a model call the header is %q", got)
	}
	h.from(sess, client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 61, fixedNow(), core.KindAttempt, core.AttemptBody{Input: 1000, Cached: 820, MS: 7200}),
	}})
	if !strings.HasPrefix(header(), "ada  fake-1 ") || !strings.HasSuffix(header(), "  cache 82%") {
		t.Errorf("after a model call the header is %q", header())
	}
	h.from(sess, client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 62, fixedNow(), core.KindAttempt, core.AttemptBody{Error: "overloaded"}),
	}})
	if got := strings.TrimSpace(header()); got != "ada  fake-1" {
		t.Errorf("after a failed call the header is %q", got)
	}
	h.wantOnScreen("call failed")
}

// The list the window sees: which blocks there are, what each is known by,
// and where the blank rows go.
func TestBlockKeys(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	saturday, monday := fixedNow().Add(-48*time.Hour), fixedNow()
	h.from(sess, live(), client.Update{Kind: client.UpdateEvents, Start: true, Events: []core.Event{
		logged(t, 1, saturday, core.KindUserMsg, core.MsgBody{Text: "what is 6*7?"}),
		logged(t, 2, saturday, core.KindToolCall, core.ToolCall{ID: "c1", Name: "python", Code: "print(6*7)"}),
		logged(t, 3, saturday, core.KindToolResult, core.ToolResult{ID: "c1", Output: "42\n"}),
		logged(t, 4, monday, core.KindAgentMsg, core.MsgBody{Text: "It is 42."}),
		logged(t, 5, monday, core.KindToolResult, core.ToolResult{ID: "c0", Output: "a result whose call is not above it\n"}),
	}}, delta(core.Delta{Type: core.DeltaError, Text: "step_limit"}))
	h.typeText("first")
	h.key(enter())
	h.typeText("second")
	h.key(enter())
	h.screen()

	type entry struct {
		key scrollback.Key
		gap int
	}
	key := func(kind uint8, id uint64) scrollback.Key { return scrollback.Key{Kind: kind, ID: id} }
	want := []entry{
		{key(blockBeginning, 0), 1},
		{key(blockDay, 20261010), 1},
		{key(blockItem, 1), 1},
		{key(blockItem, 2), 1},
		{key(blockItem, 3), 0}, // a result directly under its call
		{key(blockDay, 20261012), 1},
		{key(blockItem, 4), 1},
		{key(blockItem, 5), 1},
		{key(blockFailure, 5), 1}, // the failure follows event 5
		{key(blockPending, 1), 1},
		{key(blockPending, 2), 1},
	}
	list := &h.m.chat.list
	got := make([]entry, list.Len())
	for i := range got {
		got[i] = entry{list.Key(i), list.Gap(i)}
		if at, ok := list.Index(list.Key(i)); !ok || at != i {
			t.Errorf("block %d is found at %d, %v", i, at, ok)
		}
		if i >= 2 && list.Key(i).Kind == blockItem && list.Rev(i) == 0 {
			t.Errorf("item block %d has no revision", i)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("the blocks are\n%v\nwant\n%v", got, want)
	}
	if _, ok := list.Index(key(blockItem, 99)); ok {
		t.Error("a block that does not exist was found")
	}

	// The first message arrives. The second keeps the number it was sent
	// with, so a view that stands on it does not move.
	h.from(sess, client.Update{Kind: client.UpdateAck, Text: "first", Seq: 6})
	h.screen()
	if last := list.Key(list.Len() - 1); last != key(blockPending, 2) {
		t.Errorf("the message still on its way is now known by %+v", last)
	}
	if at, ok := list.Index(key(blockItem, 6)); !ok || at != list.Len()-2 {
		t.Errorf("the message that arrived is at %d, %v", at, ok)
	}
	h.typeText("third")
	h.key(enter())
	h.screen()
	if last := list.Key(list.Len() - 1); last != key(blockPending, 3) {
		t.Errorf("a new message is known by %+v", last)
	}
}

func TestRenumber(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		old   []sent
		texts []string
		next  uint64
		want  []sent
	}{
		{"the first message", nil, []string{"a"}, 0, []sent{{1, "a"}}},
		{"one more", []sent{{1, "a"}}, []string{"a", "b"}, 1, []sent{{1, "a"}, {2, "b"}}},
		{"the first arrived", []sent{{1, "a"}, {2, "b"}, {3, "c"}}, []string{"b", "c"}, 3, []sent{{2, "b"}, {3, "c"}}},
		{"the middle one was refused", []sent{{1, "a"}, {2, "b"}, {3, "c"}}, []string{"a", "c"}, 3, []sent{{1, "a"}, {3, "c"}}},
		{"all were lost", []sent{{1, "a"}, {2, "b"}}, nil, 2, []sent{}},
		{"the same text twice", []sent{{1, "a"}, {2, "a"}}, []string{"a"}, 2, []sent{{1, "a"}}},
		{"one arrived and one was sent", []sent{{4, "a"}}, []string{"b"}, 4, []sent{{5, "b"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			next := tc.next
			if got := renumber(tc.old, tc.texts, &next); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// No screen is ever wider or taller than the terminal, down to a phone's 30
// columns and well below what anyone would choose.
func TestEveryScreenFitsTheTerminal(t *testing.T) {
	t.Parallel()
	looks := map[string]termcaps.Caps{
		"truecolor": trueDark(), "16 colours": light16(), "256 colours": unknown256(), "no colour, ASCII": noneASCII(),
	}
	contacts := []client.Contact{
		{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash", LastActive: fixedNow().Add(-3 * time.Minute).UnixMilli()},
		{ID: 2, Name: "a contact with a name that goes on and on", Provider: "a-provider", Model: "a-model-with-a-long-name-2026-10-12", LastActive: 1},
		{ID: 3, Name: "日本語の名前 👩‍👩‍👧", Provider: "fake", Model: "fake-1"},
	}
	for name, caps := range looks {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, width := range []int{100, 60, 40, 30, 20, 10, 1} {
				for _, height := range []int{30, 16, 12, 8, 5, 2, 1} {
					h := newHarness(t, caps, contacts...)
					h.size(width, height)
					h.send(h.m.showContacts()())
					h.wantFits("the list", width, height)
					if width >= 30 && !strings.Contains(h.screen(), "ada") {
						t.Errorf("the list at %dx%d does not name the first contact:\n%s", width, height, h.screen())
					}
					h.m.note = "another contact has that name, and the server said so at some length"
					h.m.stale = true
					h.wantFits("the list with a note", width, height)

					h.typeText("n")
					h.typeText("carol with a long name")
					h.wantFits("the new-contact form", width, height)
					h.key(enter())
					h.typeText("You are Carol.")
					h.key(ctrl('j'))
					h.typeText("You answer in one line, however long the question, and you never run out of words.")
					h.wantFits("the new-contact form with a prompt", width, height)
					if v := h.m.View(); v.Cursor == nil || v.Cursor.Y >= height || v.Cursor.X >= max(width, 1) {
						t.Errorf("the new-contact form at %dx%d has its cursor at %+v", width, height, v.Cursor)
					}
					h.key(esc())

					h.typeText("r")
					h.wantFits("the rename prompt", width, height)
					h.key(esc())

					h.key(special(tea.KeyDown))
					h.key(enter())
					sess := h.b.sessions[len(h.b.sessions)-1]
					h.from(sess, live(), client.Update{Kind: client.UpdateEvents, Start: true, Events: []core.Event{
						logged(t, 1, fixedNow(), core.KindUserMsg, core.MsgBody{Text: "plot last week's latency"}),
						logged(t, 2, fixedNow(), core.KindToolCall, core.ToolCall{ID: "c", Name: "python", Code: "df.plot.bar()"}),
						logged(t, 3, fixedNow(), core.KindToolResult, core.ToolResult{ID: "c", Output: "saved latency.png\n"}),
						logged(t, 4, fixedNow(), core.KindAttempt, core.AttemptBody{Input: 1000, Cached: 820, MS: 7200}),
						logged(t, 5, fixedNow(), core.KindAgentMsg, core.MsgBody{Text: "p99 peaked on Thursday at 7.2 s."}),
					}})
					h.typeText("compare with the week before")
					h.wantFits("the chat", width, height)
					if rows := h.rows(); len(rows) != height {
						t.Errorf("the chat at %dx%d has %d rows", width, height, len(rows))
					}
					c := h.m.chat
					if c.rows.Input < 1 || c.rows.Body < min(height-1, 1) {
						t.Errorf("the chat at %dx%d has the rows %+v", width, height, c.rows)
					}
					if v := h.m.View(); v.Cursor == nil || v.Cursor.Y != c.rows.InputTop()+c.rows.Input-1-(c.rows.Input-c.rows.InputText)/2 {
						t.Errorf("the chat at %dx%d has its cursor at %+v with the rows %+v", width, height, v.Cursor, c.rows)
					}
					h.key(ctrl('j'))
					h.typeText("and the one before that")
					h.key(enter())
					h.wantFits("the chat with a note", width, height)
				}
			}
		})
	}
}

// The list keeps the cursor's row in view and drops the columns a narrow
// terminal has no room for: first how long ago, then the route.
func TestContactListColumns(t *testing.T) {
	t.Parallel()
	contacts := make([]client.Contact, 0, 40)
	for i := 1; i <= 40; i++ {
		contacts = append(contacts, client.Contact{
			ID: core.ContactID(i), Name: fmt.Sprintf("contact-%02d", i), Provider: "deepseek", Model: "deepseek-flash",
			LastActive: fixedNow().Add(-time.Duration(i) * time.Minute).UnixMilli(),
		})
	}
	for _, tc := range []struct {
		width      int
		route, ago bool
	}{
		{100, true, true},
		{46, true, true},
		{40, true, false},
		{30, false, false},
	} {
		t.Run(fmt.Sprintf("%d columns", tc.width), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, trueDark(), contacts...)
			h.size(tc.width, 12)
			h.send(h.m.showContacts()())
			h.wantOnScreen("▸ contact-01")
			if got := strings.Contains(h.screen(), "deepseek/deepseek-flash"); got != tc.route {
				t.Errorf("the route is shown: %v\n%s", got, h.screen())
			}
			if got := strings.Contains(h.screen(), "1m ago"); got != tc.ago {
				t.Errorf("how long ago is shown: %v\n%s", got, h.screen())
			}
			for range 30 {
				h.key(special(tea.KeyDown))
			}
			h.wantOnScreen("▸ contact-31")
			h.wantNotOnScreen("contact-01")
			h.wantFits("the list", tc.width, 12)
		})
	}
}

// The forms are typed into the same box as a message, so a system prompt
// takes the same new-line keys and the terminal's own cursor.
func TestFormBoxes(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	h.send(h.m.showContacts()())
	h.typeText("n")
	if v := h.m.View(); v.Cursor == nil || v.Cursor.Y != 3 || v.Cursor.X != 2 {
		t.Fatalf("the cursor of an empty form is at %+v, want the name's box", v.Cursor)
	}

	// A name is one line of at most 64 characters.
	h.key(ctrl('j'))
	h.send(tea.PasteMsg{Content: "car\nol"})
	if got := h.m.name.Value(); got != "car ol" {
		t.Fatalf("the name is %q", got)
	}
	h.send(tea.PasteMsg{Content: strings.Repeat("x", 100)})
	if got := h.m.name.Value(); len(got) != nameLimit {
		t.Fatalf("the name has %d characters", len(got))
	}

	h.key(special(tea.KeyTab))
	h.typeText(`You are Carol.\`)
	if cmd := h.key(enter()); cmd != nil || len(h.b.created) != 0 {
		t.Fatal("a backslash and enter created the contact")
	}
	h.typeText("Be brief.")
	h.key(shift(tea.KeyEnter))
	h.typeText("Be kind.")
	h.wantOnScreen("❯ You are Carol.", "  Be brief.", "  Be kind.")
	if v := h.m.View(); v.Cursor == nil || v.Cursor.Y != 8 || v.Cursor.X != 2+len("Be kind.") {
		t.Errorf("the cursor is at %+v, want the end of the prompt's third row", v.Cursor)
	}
	h.send(h.key(enter())())
	if len(h.b.created) != 1 || h.b.created[0].Agent.System != "You are Carol.\nBe brief.\nBe kind." {
		t.Fatalf("created = %+v", h.b.created)
	}
}

// Remote is the real Backend: a file a tool wrote is fetched through the
// API, and asking again with its tag fetches nothing.
func TestRemoteFetchesAFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	srv := apitest.Start(t, apitest.Options{FSRoot: root})
	c, err := client.New(client.Options{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	var b Backend = Remote{Client: c}
	id, err := b.Create(t.Context(), client.NewContact{Agent: core.AgentConfig{System: "You are Ada."}})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, id.String())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "latency.png"), []byte("\x89PNG\r\n\x1a\nnot much of one"), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := b.File(t.Context(), id, "latency.png", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(f.Data), "\x89PNG") || f.ContentType != "image/png" || f.ETag == "" {
		t.Errorf("fetched %d bytes of %q with the tag %q", len(f.Data), f.ContentType, f.ETag)
	}
	again, err := b.File(t.Context(), id, "latency.png", f.ETag)
	if err != nil || !again.NotModified || again.Data != nil {
		t.Errorf("asked again with the tag: %+v, %v", again, err)
	}
	if _, err := b.File(t.Context(), id, "missing.png", ""); !errors.Is(err, client.ErrFileNotFound) {
		t.Errorf("a file that is not there: %v", err)
	}

	// The fake the other tests use answers the same three ways.
	fake := &fakeBackend{files: map[string]client.File{"latency.png": {Data: []byte("png"), ETag: `"1"`}}}
	if f, err := fake.File(t.Context(), id, "latency.png", ""); err != nil || string(f.Data) != "png" {
		t.Errorf("the fake fetched %+v, %v", f, err)
	}
	if f, err := fake.File(t.Context(), id, "latency.png", `"1"`); err != nil || !f.NotModified || f.Data != nil {
		t.Errorf("the fake, asked again with the tag: %+v, %v", f, err)
	}
	if _, err := fake.File(t.Context(), id, "missing.png", ""); !errors.Is(err, client.ErrFileNotFound) {
		t.Errorf("the fake, for a file that is not there: %v", err)
	}
}
