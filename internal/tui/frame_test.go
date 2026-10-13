package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// frameReply is the reply of the Frame mock.
const frameReply = "Two things overlapped:\n\n" +
	"- Turn 6 ran three model calls with tool runs in between.\n" +
	"- The turn after it re-read the tool results uncached.\n\n" +
	"The fix is small. Put the time note after the tool steps:\n\n" +
	"```go\nliveMsgs = append(liveMsgs, toolSteps...)\nliveMsgs = append(liveMsgs, timeNote(now))\n```\n"

// frameDraft is the question being written in the Frame mock.
const frameDraft = "and what would the share have been with the fix?"

// frameMock brings the interface to the moment the Frame mock draws: six
// minutes past noon on Monday 12 October, a conversation with ada that began
// the Wednesday before, a tool that has run for 2.1 seconds, and the next
// question half written.
func frameMock(t *testing.T, caps termcaps.Caps) *harness {
	t.Helper()
	h := newHarness(t, caps)
	h.now = time.Date(2026, 10, 12, 12, 6, 0, 0, time.UTC)
	h.size(100, 30)
	h.m.open(client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"})
	sess := h.b.sessions[0]
	wednesday, monday := h.now.Add(-5*24*time.Hour), h.now.Add(-time.Minute)
	h.from(sess, live(), client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 1, wednesday, core.KindUserMsg, core.MsgBody{Text: "why did Thursday spike?"}),
		logged(t, 2, wednesday, core.KindAgentMsg, core.MsgBody{Text: frameReply}),
		logged(t, 3, monday, core.KindUserMsg, core.MsgBody{Text: "show me the numbers for turn 7"}),
		logged(t, 4, monday, core.KindAttempt, core.AttemptBody{Route: "deepseek/deepseek-flash", Input: 3200, Cached: 1536, Output: 90, MS: 5400}),
	}},
		delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
		delta(core.Delta{Type: core.DeltaToolCall, Tool: "python", Text: `print(df[df.turn == 7][["input", "cached", "ms"]])`, Seq: 5}),
	)
	h.now = h.now.Add(2100 * time.Millisecond)
	h.send(toolTickMsg{sess: sess})
	h.typeText(frameDraft)
	return h
}

// rows is the screen without colours, a row at a time.
func (h *harness) rows() []string { return strings.Split(h.screen(), "\n") }

// The approved screen, at the size it was drawn for. The build differs from
// the drawing where the spec says it does: no turn number in the header,
// ctrl+j as the new-line key, and no half rows. It also draws the rule of
// each day, which is what makes the header's "7 Oct – 12 Oct" true.
func TestFrameMock(t *testing.T) {
	t.Parallel()
	h := frameMock(t, trueDark())
	rows := h.rows()
	if len(rows) != 30 {
		t.Fatalf("the screen has %d rows, want 30:\n%s", len(rows), h.screen())
	}
	for i, row := range rows {
		if w := ansi.StringWidth(row); w > 100 {
			t.Errorf("row %d is %d cells wide: %q", i, w, row)
		}
	}
	tuitest.Golden(t, "frame-100x30.golden", []byte(h.screen()+"\n"))

	spread := func(left, right string) string {
		return left + strings.Repeat(" ", 100-ansi.StringWidth(left)-ansi.StringWidth(right)) + right
	}
	for _, want := range []struct {
		row  int
		text string
	}{
		{0, spread("ada  deepseek/deepseek-flash", "7 Oct – 12 Oct")},
		{1, strings.Repeat("─", 100)},
		{25, spread(" ● live │ deepseek-flash │ cache 48% │ 3.2k tok │ 5.4s", "running python │ esc stop │ 12:06 ")},
		{26, "╭" + strings.Repeat("─", 98) + "╮"},
		{27, spread("│ ❯ "+frameDraft, "│")},
		{28, "╰" + strings.Repeat("─", 98) + "╯"},
		{29, spread(" enter send · ctrl+j new line · / commands · ctrl+o details · ctrl+p contacts", "")},
	} {
		if rows[want.row] != want.text {
			t.Errorf("row %d is\n%q, want\n%q", want.row, rows[want.row], want.text)
		}
	}

	// The 23 rows of the transcript, between the header's rule and the bar.
	fill := func(s string) string { return "  " + s + strings.Repeat(" ", 60-len(s)) }
	rule := func(day string) string {
		side := 100 - len(day) - 2
		return strings.Repeat("─", side/2) + " " + day + " " + strings.Repeat("─", side-side/2)
	}
	transcript := []string{
		"",
		rule("Wed 7 Oct 2026"),
		"",
		"❯ why did Thursday spike?",
		"",
		"  Two things overlapped:",
		"",
		"  • Turn 6 ran three model calls with tool runs in between.",
		"  • The turn after it re-read the tool results uncached.",
		"",
		"  The fix is small. Put the time note after the tool steps:",
		"",
		fill(" go"),
		fill(" liveMsgs = append(liveMsgs, toolSteps...)"),
		fill(" liveMsgs = append(liveMsgs, timeNote(now))"),
		"",
		rule("Mon 12 Oct 2026"),
		"",
		"❯ show me the numbers for turn 7",
		"",
		"⏺ python",
		`    print(df[df.turn == 7][["input", "cached", "ms"]])`,
		"  ⎿ running… 2.1s",
	}
	if got := rows[2:25]; strings.Join(got, "\n") != strings.Join(transcript, "\n") {
		t.Errorf("the transcript's rows are\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(transcript, "\n"))
	}

	// The cursor is the terminal's own, after the draft, inside the box.
	v := h.m.View()
	if v.Cursor == nil || v.Cursor.Y != 27 || v.Cursor.X != len("| > "+frameDraft) {
		t.Errorf("the cursor is at %+v, want the cell after the draft on row 27", v.Cursor)
	}
	if v.MouseMode != tea.MouseModeNone {
		t.Errorf("mouse mode %v is on: text selection would stop being the terminal's", v.MouseMode)
	}
	if v.WindowTitle != "contactd: ada" || !v.AltScreen {
		t.Errorf("window title %q, alternate screen %v", v.WindowTitle, v.AltScreen)
	}
}
