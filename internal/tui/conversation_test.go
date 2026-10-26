package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// dropped is the connection going down, and back is everything a session
// says on the way back up: the log since the last read, that it has caught
// up, and that it is live.
func dropped() client.Update {
	return client.Update{Kind: client.UpdateState, State: client.StateDown, Err: errors.New("connection reset")}
}

func back(events ...core.Event) []client.Update {
	return []client.Update{
		{Kind: client.UpdateState, State: client.StateConnecting},
		{Kind: client.UpdateEvents, Events: events},
		{Kind: client.UpdateSynced},
		live(),
	}
}

// A turn in flight is known from the stream alone. When the connection goes,
// nothing says any more that a reply is being written: the log has what the
// turn committed, and a new stream describes a turn that is still going. A
// reply that ended while the connection was down must not stay half written
// under the log's account of it.
func TestALostConnectionEndsTheTurnInFlight(t *testing.T) {
	t.Parallel()

	t.Run("a reply that ended meanwhile is shown once", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, trueDark())
		sess := h.openChat()
		h.from(sess, live(), messages(t, 1, 2, fixedNow()),
			delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
			delta(core.Delta{Type: core.DeltaText, Text: "It began on a "}),
		)
		h.wantOnScreen("It began on a", "replying │ esc stop")

		h.from(sess, dropped())
		h.wantOnScreen("● offline, retrying", "disconnected: connection reset")
		h.wantNotOnScreen("It began on a", "replying", "esc stop")
		h.key(esc())
		if sess.interrupts != 0 {
			t.Errorf("esc sent %d interrupts with no reply known to be in flight", sess.interrupts)
		}

		h.from(sess, back(logged(t, 3, fixedNow(), core.KindAgentMsg, core.MsgBody{Text: "It began on a Tuesday."}))...)
		h.wantOnScreen("● live", "It began on a Tuesday.")
		h.wantNotOnScreen("replying", "esc stop", "offline")
		if n := strings.Count(h.screen(), "It began on a"); n != 1 {
			t.Errorf("the reply is on screen %d times:\n%s", n, h.screen())
		}
		if got := h.m.chat.tr.Live(); got.Typing || got.Text != "" {
			t.Errorf("the turn in flight is still %+v", got)
		}
	})

	t.Run("a reply still being written is taken up again", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, trueDark())
		sess := h.openChat()
		h.from(sess, live(), messages(t, 1, 2, fixedNow()),
			delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
			delta(core.Delta{Type: core.DeltaText, Text: "It began on a "}),
		)
		h.from(sess, dropped())
		h.from(sess, back()...)
		// The new stream says how far the turn has got.
		h.from(sess,
			delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
			delta(core.Delta{Type: core.DeltaText, Text: "It began on a Tuesday, and"}),
		)
		h.wantOnScreen("It began on a Tuesday, and", "replying │ esc stop")
		if n := strings.Count(h.screen(), "It began on a"); n != 1 {
			t.Errorf("the reply is on screen %d times:\n%s", n, h.screen())
		}
	})

	t.Run("a running tool stops being timed", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, trueDark())
		sess := h.openChat()
		c := h.m.chat
		h.from(sess, live(), messages(t, 1, 2, fixedNow()), delta(core.Delta{Type: core.DeltaTyping, Typing: true}))
		start := updatesMsg{sess: sess, closed: true, batch: []client.Update{
			delta(core.Delta{Type: core.DeltaToolCall, Tool: "python", Text: "import time", Seq: 3}),
		}}
		if cmd := h.send(start); cmd == nil || !c.ticking {
			t.Fatal("a tool started and no tick was asked for")
		}
		h.wantOnScreen("⎿ running… 0.0s", "running python │ esc stop")

		h.from(sess, dropped())
		h.wantOnScreen("⏺ python", "import time")
		h.wantNotOnScreen("running")
		// The tick that was on its way is the last: nothing wakes the client
		// while it waits for the server.
		if cmd := h.send(toolTickMsg{sess: sess}); cmd != nil || c.ticking {
			t.Error("the tick went on after the connection was lost")
		}

		h.from(sess, back(
			logged(t, 3, fixedNow(), core.KindToolCall, core.ToolCall{ID: "c", Name: "python", Code: "import time"}),
			logged(t, 4, fixedNow(), core.KindToolResult, core.ToolResult{ID: "c", Output: "done\n"}),
			logged(t, 5, fixedNow(), core.KindAgentMsg, core.MsgBody{Text: "It took a second."}),
		)...)
		h.wantOnScreen("⏺ python", "⎿ done", "It took a second.")
		h.wantNotOnScreen("running", "replying", "esc stop")
		if cmd := h.send(toolTickMsg{sess: sess}); cmd != nil || c.ticking {
			t.Error("a tick was asked for with no tool running")
		}
	})

	t.Run("a contact that is gone has no turn either", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, trueDark())
		sess := h.openChat()
		h.from(sess, live(), messages(t, 1, 2, fixedNow()),
			delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
			delta(core.Delta{Type: core.DeltaText, Text: "It began on a "}),
		)
		h.from(sess, client.Update{Kind: client.UpdateState, State: client.StateGone, Err: core.ErrContactNotFound})
		h.wantOnScreen("gone", "❯ message 2")
		h.wantNotOnScreen("It began on a", "replying", "esc stop")
	})
}

// A view that was scrolled up stays where it is while the conversation goes
// on below it. A view at the newest line follows.
func TestAScrolledViewStaysPut(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.from(sess, live(), messages(t, 1, 60, fixedNow()))
	c := h.m.chat
	transcript := func() string { return strings.Join(h.rows()[2:2+c.rows.Body], "\n") }

	// At the newest line: what arrives is shown.
	h.from(sess, delta(core.Delta{Type: core.DeltaUser, Text: "message 61", Seq: 61}))
	h.wantOnScreen("❯ message 61")
	h.wantNotOnScreen("newer")

	h.key(special(tea.KeyPgUp))
	h.key(special(tea.KeyPgUp))
	h.wantOnScreen("↓ newer")
	h.wantNotOnScreen("message 61")
	was := transcript()

	// A reply is written, a message arrives, and the reply settles.
	h.from(sess,
		delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
		delta(core.Delta{Type: core.DeltaText, Text: "Counting them "}),
	)
	h.from(sess, delta(core.Delta{Type: core.DeltaUser, Text: "message 62", Seq: 62}))
	h.from(sess,
		delta(core.Delta{Type: core.DeltaMessage, Text: "Counting them all.", Seq: 63}),
		delta(core.Delta{Type: core.DeltaTyping}),
	)
	if got := transcript(); got != was {
		t.Errorf("the view moved while the conversation went on below it:\n%s\n--- was ---\n%s", got, was)
	}
	h.wantOnScreen("↓ newer")
	h.wantNotOnScreen("Counting them", "message 62")

	// A draft that grows takes its rows from the bottom of the transcript,
	// and the top of the view stays.
	first := h.rows()[2]
	h.typeText("one")
	h.key(ctrl('j'))
	h.typeText("two")
	if got := h.rows()[2]; got != first {
		t.Errorf("the first row in view is %q, and was %q before the draft grew", got, first)
	}
	h.key(ctrl('c'))

	// A terminal that answers late has everything drawn again, in place.
	if cmd := h.m.setCaps(light16()); cmd == nil {
		t.Fatal("a change of caps did not ask for the screen to be cleared")
	}
	if got := h.rows()[2]; got != first {
		t.Errorf("the first row in view is %q, and was %q before the terminal answered", got, first)
	}
	h.wantOnScreen("↓ newer")

	// Back at the newest line, the view follows again.
	for range 6 {
		h.key(special(tea.KeyPgDown))
	}
	h.wantOnScreen("Counting them all.")
	h.wantNotOnScreen("newer")
	h.from(sess, delta(core.Delta{Type: core.DeltaUser, Text: "message 64", Seq: 64}))
	h.wantOnScreen("❯ message 64")
}

// A turn that ended without a reply says so where the reply would be, and
// the items a sub-agent brings are drawn as what they are: the task it was
// given, and the notice that it has finished.
func TestAFailedTurnAndSubAgents(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	at := fixedNow()
	h.from(sess, live(), client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 1, at, core.KindUserMsg, core.MsgBody{Text: "look both of these up", Task: &core.TaskRef{Parent: 9}}),
		logged(t, 2, at, core.KindToolCall, core.ToolCall{ID: "d1", Name: "delegate", Args: []byte(`{"task":"the first"}`)}),
		logged(t, 3, at, core.KindToolResult, core.ToolResult{ID: "d1", Output: "started #7", Child: 7}),
		logged(t, 4, at, core.KindToolCall, core.ToolCall{ID: "d2", Name: "delegate", Args: []byte(`{"task":"the second"}`)}),
		logged(t, 5, at, core.KindToolResult, core.ToolResult{ID: "d2", Output: "started #8", Child: 8}),
	}})
	h.wantOnScreen("⇢ look both of these up", "⏺ delegate", `{"task":"the first"}`, "⎿ started #7", "⎿ started #8")
	h.wantNotOnScreen("❯ look both")
	c := h.m.chat
	if c.waiting != 2 {
		t.Errorf("the contact waits for %d sub-agents, want 2", c.waiting)
	}
	// The default bar has no cell for them; the ops preset has.
	h.wantNotOnScreen("sub-agent")
	ops, ok := statusline.Preset("ops")
	if !ok {
		t.Fatal("there is no ops preset")
	}
	c.bar, c.dirty = ops, c.dirty|dirtyBar
	h.wantOnScreen("2 sub-agents │ 12:00")

	built := c.rebuilds
	h.from(sess, client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 6, at, core.KindNotice, core.NoticeBody{Text: "#7 finished: nothing found", Child: 7}),
	}})
	h.wantOnScreen("◆ #7 finished: nothing found", "1 sub-agent │ 12:00")
	h.wantNotOnScreen("sub-agents")
	// The count is taken when the list is built, and not on every key.
	h.typeText("x")
	h.screen()
	if c.rebuilds != built+1 {
		t.Errorf("the list was built %d times for one notice and one key", c.rebuilds-built)
	}

	// The turn fails: the reason stands under the last thing that happened.
	h.from(sess,
		delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
		delta(core.Delta{Type: core.DeltaText, Text: "Let me th"}),
		delta(core.Delta{Type: core.DeltaError, Text: "step_limit"}),
		delta(core.Delta{Type: core.DeltaTyping}),
	)
	h.wantOnScreen("✗ no reply: ")
	h.wantNotOnScreen("Let me th", "replying")
	rows := h.rows()
	notice, failure := -1, -1
	for i, row := range rows {
		switch {
		case strings.HasPrefix(row, "◆ #7 finished"):
			notice = i
		case strings.HasPrefix(row, "✗ no reply"):
			failure = i
		}
	}
	if notice < 0 || failure != notice+2 {
		t.Errorf("the failure is on row %d and the notice on row %d, want it a blank row below:\n%s", failure, notice, h.screen())
	}
	// It is drawn in the tone of something gone wrong.
	bad, _, _ := strings.Cut(h.m.th.Style("bad").Render("✗"), "✗")
	if raw := h.m.View().Content; bad == "" || !strings.Contains(raw, bad+"✗") {
		t.Errorf("the failure does not start with %q:\n%q", bad, raw)
	}
}

// A refusal that names no message has nothing to give back, and must leave
// the draft as it is.
func TestARefusalThatNamesNoMessage(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.from(sess, live())
	h.typeText("next thought")
	h.from(sess, client.Update{Kind: client.UpdateRefused, Reason: "too many unanswered messages"})
	if got := h.m.chat.input.Value(); got != "next thought" {
		t.Errorf("the box holds %q", got)
	}
	h.wantOnScreen("not sent: too many unanswered messages", "❯ next thought")
}

// A note is shown until the next key, whichever key that is: also one the
// conversation never sees, because the whole screen takes it.
func TestEveryKeyClearsANote(t *testing.T) {
	t.Parallel()
	for name, key := range map[string]tea.KeyPressMsg{
		"a letter":             {Code: 'x', Text: "x"},
		"ctrl+c with a draft":  ctrl('c'),
		"ctrl+l":               ctrl('l'),
		"ctrl+o":               ctrl('o'),
		"ctrl+d with a draft":  ctrl('d'),
		"tab":                  special(tea.KeyTab),
		"page up":              special(tea.KeyPgUp),
		"esc":                  esc(),
		"an arrow":             special(tea.KeyLeft),
		"a key with no effect": special(tea.KeyF5),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, trueDark())
			h.openChat()
			h.typeText("hello")
			h.key(enter())
			h.wantOnScreen("not sent: not connected")
			h.key(key)
			h.wantNotOnScreen("not sent")
			h.wantOnScreen("enter send")
		})
	}
}

// On a terminal so short that the transcript has a row or two, the keys
// still move it: a page is never less than a row, and an arrow never skips
// rows that were not shown.
func TestScrollingAShortTranscript(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		height, body int
		up, down     rune
		// want is the message on the transcript's last row after each of
		// three presses of up; "" is the blank row between two messages.
		want [3]string
	}{
		{name: "pages", height: 3, body: 1, up: tea.KeyPgUp, down: tea.KeyPgDown, want: [3]string{"", "message 39", ""}},
		{name: "arrows", height: 3, body: 1, up: tea.KeyUp, down: tea.KeyDown, want: [3]string{"", "message 39", ""}},
		{name: "pages", height: 4, body: 2, up: tea.KeyPgUp, down: tea.KeyPgDown, want: [3]string{"", "message 39", ""}},
		{name: "arrows", height: 4, body: 2, up: tea.KeyUp, down: tea.KeyDown, want: [3]string{"message 39", "message 38", "message 37"}},
		{name: "pages", height: 5, body: 3, up: tea.KeyPgUp, down: tea.KeyPgDown, want: [3]string{"message 39", "message 38", "message 37"}},
		{name: "arrows", height: 5, body: 3, up: tea.KeyUp, down: tea.KeyDown, want: [3]string{"", "message 37", ""}},
	} {
		t.Run(fmt.Sprintf("%s on %d rows", tc.name, tc.height), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, trueDark())
			h.size(40, tc.height)
			sess := h.openChat()
			h.from(sess, live(), messages(t, 1, 40, fixedNow()))
			c := h.m.chat
			if c.rows.Body != tc.body {
				t.Fatalf("the transcript has %d rows, want %d", c.rows.Body, tc.body)
			}
			last := func() string { return strings.TrimPrefix(h.rows()[tc.body-1], "❯ ") }
			if got := last(); got != "message 40" {
				t.Fatalf("the newest row is %q", got)
			}
			for i, want := range tc.want {
				h.key(special(tc.up))
				if got := last(); got != want {
					t.Errorf("after %d presses the last row is %q, want %q", i+1, got, want)
				}
			}
			h.wantOnScreen("↓")
			for range tc.want {
				h.key(special(tc.down))
			}
			if got := last(); got != "message 40" {
				t.Errorf("back down, the last row is %q", got)
			}
			h.wantNotOnScreen("↓")
		})
	}
}

// everything is a page of the log with one of every kind of item in it, and
// text that is hard to lay out: a word longer than a row, wide characters, a
// joined emoji, code wider than the screen, a table, a quote and an image.
func everything(t *testing.T) client.Update {
	t.Helper()
	sunday, monday := fixedNow().Add(-26*time.Hour), fixedNow().Add(-time.Minute)
	reply := "p99 **peaked** on `Thursday`:\n\n" +
		"```go\nx := 1 // a line of code that goes on and on and on and on and on and on and on and on and on and on\n```\n\n" +
		"> a quote\n\n| day | p99 |\n|---|---|\n| Thu | 7.2 |\n\n![chart](latency.png)\n"
	return client.Update{Kind: client.UpdateEvents, Start: true, Events: []core.Event{
		logged(t, 1, sunday, core.KindUserMsg, core.MsgBody{
			Text: "plot last week's latency 日本語のテキスト 👩‍👩‍👧 averyveryveryveryveryveryveryverylongwordwithnospacesinitanywhereatall",
		}),
		logged(t, 2, sunday, core.KindToolCall, core.ToolCall{
			ID: "c", Name: "python", Text: "Let me look.",
			Code: "df.plot.bar()\nfor i in range(100):\n    print(i)\n\n\nx=1\ny=2\nz=3\nw=4",
		}),
		logged(t, 3, sunday, core.KindToolResult, core.ToolResult{ID: "c", Output: "no such column\nrow 2\nrow 3\nrow 4\nrow 5\nrow 6\n", IsError: true}),
		logged(t, 4, sunday, core.KindAttempt, core.AttemptBody{Route: "deepseek/deepseek-flash", Input: 1000, Cached: 820, MS: 7200}),
		logged(t, 5, sunday, core.KindAgentMsg, core.MsgBody{Text: reply}),
		logged(t, 6, monday, core.KindNotice, core.NoticeBody{Text: "sub-agent #7 finished: all good", Child: 7}),
		logged(t, 7, monday, core.KindCheckpoint, core.CheckpointBody{Text: "Earlier: they talked about latency."}),
		logged(t, 8, monday, core.KindUserMsg, core.MsgBody{Text: "do the task", Task: &core.TaskRef{Parent: 9}}),
		logged(t, 9, monday, core.KindAgentMsg, core.MsgBody{Text: "cut off", Interrupted: true}),
	}}
}

// Whatever a conversation is doing, its screen is the terminal's size and no
// more: every row fits, there are exactly as many rows as the terminal has,
// the input is one of them, and the cursor is on the input and on the screen.
func TestTheChatFitsInEveryState(t *testing.T) {
	t.Parallel()
	clusters := trueDark()
	clusters.Width = termcaps.WidthGrapheme
	for name, caps := range map[string]termcaps.Caps{
		"truecolor": trueDark(), "16 colours": light16(), "256 colours": unknown256(),
		"no colour, ASCII": noneASCII(), "cells counted by cluster": clusters,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, width := range []int{100, 60, 40, 30, 20, 10} {
				for _, height := range []int{30, 16, 12, 8, 5, 3} {
					h := newHarness(t, trueDark())
					h.m.setCaps(caps)
					h.size(width, height)
					sess := h.openChat()
					c := h.m.chat
					check := func(what string) {
						t.Helper()
						v := h.m.View()
						rows := strings.Split(ansi.Strip(v.Content), "\n")
						if len(rows) != height {
							t.Errorf("%s at %dx%d: %d rows", what, width, height, len(rows))
						}
						for i, row := range rows {
							if w := max(ansi.StringWidth(row), ansi.StringWidthWc(row)); w > width {
								t.Errorf("%s at %dx%d: row %d is %d cells wide: %q", what, width, height, i, w, row)
							}
						}
						top := c.rows.InputTop()
						if c.rows.Input < 1 || c.rows.Body < 1 || top+c.rows.Input > height {
							t.Errorf("%s at %dx%d: the rows are %+v", what, width, height, c.rows)
						}
						if (c.peek != nil || c.wireOpen) && v.Cursor == nil {
							return
						}
						if v.Cursor == nil {
							t.Errorf("%s at %dx%d: there is no cursor", what, width, height)
							return
						}
						if x, y := v.Cursor.X, v.Cursor.Y; x < 0 || x >= width || y < top || y >= top+c.rows.Input {
							t.Errorf("%s at %dx%d: the cursor is at %d,%d and the input on rows %d to %d", what, width, height, x, y, top, top+c.rows.Input-1)
						}
						if v.MouseMode != tea.MouseModeNone || !v.AltScreen {
							t.Errorf("%s at %dx%d: mouse mode %v, alternate screen %v", what, width, height, v.MouseMode, v.AltScreen)
						}
					}
					check("an empty conversation")
					h.from(sess, live(), everything(t))
					check("one of everything")
					h.from(sess, client.Update{Kind: client.UpdateHelpers, Helpers: []core.HelperJSON{{Handle: 1, Contact: 2, Title: "Status page", State: "TASK_STATE_WORKING"}}})
					check("helper strip")
					c.wireOpen = true
					c.dirty = dirtyAll
					check("wire overlay")
					c.closeHelperOverlay()
					p := newChat(client.Contact{ID: 2, Name: "helper 1"}, sess, c.th, c.loc, c.bar, c.cmds, h.m.newImages(2))
					c.peek = &helperPeek{chat: p, handle: 1}
					c.dirty = dirtyAll
					check("helper overlay")
					c.peek = nil
					c.dirty = dirtyAll

					h.typeText("compare with the week before")
					check("a draft")
					h.from(sess,
						delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
						delta(core.Delta{Type: core.DeltaText, Text: "A reply that is long enough to be wrapped more than once, 日本語 too. "}),
						delta(core.Delta{Type: core.DeltaToolStart, Tool: "python"}),
					)
					check("a reply being written")
					h.from(sess, delta(core.Delta{Type: core.DeltaToolCall, Tool: "python", Text: "import time", Seq: 10}))
					h.now = h.now.Add(61 * time.Second)
					h.send(toolTickMsg{sess: sess})
					check("a tool running")
					h.from(sess, delta(core.Delta{Type: core.DeltaError, Text: "step_limit"}), delta(core.Delta{Type: core.DeltaTyping}))
					check("a turn that failed")
					h.key(enter())
					check("a message on its way")
					for range 9 {
						h.key(ctrl('j'))
						h.typeText("another line, 日本語")
					}
					check("a draft of ten lines")
					for range 3 {
						h.key(special(tea.KeyPgUp))
					}
					check("scrolled up")
					h.key(ctrl('o'))
					check("unfolded")
					h.from(sess, dropped())
					check("disconnected")
					h.from(sess, client.Update{Kind: client.UpdateRefused, Text: "compare with the week before", Reason: "not connected"})
					check("a message given back")
					h.send(tea.PasteMsg{Content: strings.Repeat("word ", 100) + strings.Repeat("x", 120)})
					check("a long paste")
					h.from(sess, client.Update{Kind: client.UpdateState, State: client.StateGone, Err: core.ErrContactNotFound})
					check("a contact that is gone")
					h.key(ctrl('c'))
					h.typeText("/")
					check("the list of commands")
					h.typeText("st")
					h.key(special(tea.KeyDown))
					check("a command chosen")
					h.key(special(tea.KeyTab))
					check("a command completed")
					h.key(enter())
					check("what a command printed")
					h.typeText("/help")
					h.key(enter())
					check("the help")
					h.typeText("/zzz")
					h.key(enter())
					check("no such command")
				}
			}
		})
	}
}

// A glyph chat.toml puts in place of another may be up to three cells wide.
// Every row is measured with the glyphs it is drawn with, so the screens
// still fit, and the cursor is still after the draft.
func TestGlyphsThreeCellsWideStillFit(t *testing.T) {
	t.Parallel()
	env, over := terminal(t, trueDark())
	chat := conf.Chat{Probe: true, Mods: true, Glyph: map[string]string{
		"prompt": "=>>", "tool": "###", "elbow": "|__", "sep": "|||", "dot": "(*)", "mid": "<->", "ellipsis": "...",
		"pointer": "-->", "dash": "---", "newer": "vvv", "stopped": "[#]", "notice": "(!)", "fail": "(x)", "task": "==>",
		"hrule": "=", "boxh": "=", "boxv": "||", "boxtl": "/", "boxtr": `\`, "boxbl": `\`, "boxbr": "/",
	}}
	ada := client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash", LastActive: fixedNow().Add(-time.Hour).UnixMilli()}
	for _, width := range []int{100, 60, 40, 30, 20, 10} {
		for _, height := range []int{30, 16, 12, 8, 5, 3} {
			h := newHarnessWith(t, Options{Env: env, Overrides: over, Chat: chat}, ada)
			if h.m.note != "" {
				t.Fatalf("the glyphs were refused: %s", h.m.note)
			}
			h.size(width, height)
			h.send(h.m.showContacts()())
			h.wantFits("the list", width, height)
			h.typeText("n")
			h.wantFits("the new-contact form", width, height)
			h.key(esc())
			h.key(enter())
			h.from(h.b.sessions[0], live(), everything(t),
				delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
				delta(core.Delta{Type: core.DeltaToolCall, Tool: "python", Text: "import time", Seq: 10}),
			)
			h.typeText("compare")
			for _, what := range []string{"the chat", "the chat, scrolled up"} {
				h.wantFits(what, width, height)
				if got := len(h.rows()); got != height {
					t.Errorf("%s at %dx%d has %d rows", what, width, height, got)
				}
				c, v := h.m.chat, h.m.View()
				if v.Cursor == nil || v.Cursor.X >= width || v.Cursor.Y < c.rows.InputTop() || v.Cursor.Y >= c.rows.InputTop()+c.rows.Input {
					t.Errorf("%s at %dx%d has its cursor at %+v with the rows %+v", what, width, height, v.Cursor, c.rows)
				}
				h.key(special(tea.KeyPgUp))
			}
			if width == 100 && height == 30 {
				h.wantOnScreen("(*) live ||| cache 82%", "||| vvv newer ||| 12:00", "|| =>> compare", "enter send <-> ctrl+j new line", "### python")
			}
		}
	}
}
