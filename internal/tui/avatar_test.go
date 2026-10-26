package tui

import (
	"fmt"
	"image/color"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/script"
	"github.com/quinnovator/contactd/internal/tui/sound"
	"github.com/quinnovator/contactd/internal/tui/stage"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// A conversation shown by the avatar is driven like any other: messages into
// Update, the screen read back from View. Time is the harness's own, so the
// avatar speaks exactly as long as a test lets it.

// newStaged makes the interface with the avatar switched as show says, for a
// terminal that can do caps.
func newStaged(t *testing.T, caps termcaps.Caps, show string) *harness {
	t.Helper()
	env, over := terminal(t, caps)
	return newHarnessWith(t, Options{Env: env, Overrides: over, Avatar: Avatar{Show: show}})
}

// pass lets time go by for the avatar, a frame at a time.
func (h *harness) pass(d time.Duration) {
	h.t.Helper()
	for range int(d / frameEvery) {
		h.now = h.now.Add(frameEvery)
		h.send(frameMsg{sess: h.m.chat.sess})
	}
}

// body is the rows where a thread would be, without colours.
func (h *harness) body() []string {
	h.t.Helper()
	rows, c := h.rows(), h.m.chat
	top := c.rows.Header + c.rows.HeaderRule
	return rows[top : top+c.rows.Body]
}

// frame is every row but the transcript's: the frame the stage stands in.
func (h *harness) frame() []string {
	h.t.Helper()
	rows, c := h.rows(), h.m.chat
	top := c.rows.Header + c.rows.HeaderRule
	return append(append([]string{}, rows[:top]...), rows[top+c.rows.Body:]...)
}

// told is a conversation the log already holds when it is opened.
func told(t *testing.T, reply string) client.Update {
	t.Helper()
	at := fixedNow().Add(-time.Hour)
	return client.Update{Kind: client.UpdateEvents, Start: true, Events: []core.Event{
		logged(t, 1, at, core.KindUserMsg, core.MsgBody{Text: "why did Thursday spike?"}),
		logged(t, 2, at, core.KindAttempt, core.AttemptBody{Route: "fake/fake-1", Input: 2000, Cached: 1500, MS: 1234}),
		logged(t, 3, at, core.KindAgentMsg, core.MsgBody{Text: reply}),
	}}
}

// replying is a new message and the reply to it, as the stream brings them.
func replying(question, reply string) []client.Update {
	return []client.Update{
		delta(core.Delta{Type: core.DeltaUser, Text: question, Seq: 10}),
		typing(true),
		delta(core.Delta{Type: core.DeltaText, Text: reply}),
	}
}

func TestTheStageStandsWhereTheThreadDid(t *testing.T) {
	t.Parallel()
	thread, staged := newStaged(t, trueDark(), avatarOff), newStaged(t, trueDark(), avatarOn)
	for _, h := range []*harness{thread, staged} {
		h.size(100, 30)
		sess := h.openChat()
		h.from(sess, told(t, "Two things overlapped."), live())
		h.typeText("and the fix?")
	}
	if thread.m.chat.stage != nil || staged.m.chat.stage == nil {
		t.Fatal("the avatar is on where it was switched off, or off where it was switched on")
	}
	// The header, the rules, the bar, the input and the hints are the same
	// rows, drawn the same. The header's day is the turn's in both.
	if got, want := staged.frame(), thread.frame(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("around the stage:\n%s\nwant what is around the thread:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if staged.m.chat.rows != thread.m.chat.rows {
		t.Errorf("the rows are %+v, want the thread's %+v", staged.m.chat.rows, thread.m.chat.rows)
	}
	// The avatar opens idle, without the thread's previous question or reply.
	body := strings.Join(staged.body(), "\n")
	for _, want := range []string{"❯ why did Thursday spike?", "Two things overlapped."} {
		if strings.Contains(body, want) {
			t.Errorf("the idle stage shows history %q:\n%s", want, body)
		}
	}
	if cols, rows := staged.m.chat.stage.Face(); cols < 20 || rows < 10 {
		t.Errorf("the avatar is %d by %d cells on a stage of 100 by %d", cols, rows, staged.m.chat.rows.Body)
	}
	if strings.Contains(body, "the beginning") || strings.Contains(body, "Mon 12 Oct") {
		t.Errorf("the stage draws the thread's rules:\n%s", body)
	}
	if v := staged.m.View(); v.MouseMode != tea.MouseModeNone || v.Cursor == nil {
		t.Errorf("the view asks for the mouse (%v) or has no cursor (%v)", v.MouseMode, v.Cursor)
	}
}

func TestTheAvatarSaysAReplyAtASpeakersPace(t *testing.T) {
	t.Parallel()
	h := newStaged(t, trueDark(), avatarOn)
	h.size(100, 30)
	sess := h.openChat()
	h.from(sess, told(t, "Two things overlapped."), live())

	// What the log held stays off the stage and is not said again.
	st := h.m.chat.stage
	if got := st.Said(); got != "" || st.Speaking() {
		t.Fatalf("on opening, %q is said and speaking is %v", got, st.Speaking())
	}

	reply := "The fix is small. Put the time note after the tool steps, and the cache holds."
	h.from(sess, replying("and the fix?", reply)...)
	h.wantOnScreen("❯ and the fix?", "replying │ esc stop")
	if got := st.Said(); got != "" {
		t.Fatalf("%q is said before any time has passed", got)
	}
	// It is said a little at a time, and never unsaid.
	last := ""
	for range 12 {
		h.pass(100 * time.Millisecond)
		said := st.Said()
		if !strings.HasPrefix(said, last) || !strings.HasPrefix(reply, said) {
			t.Fatalf("said %q after %q", said, last)
		}
		last = said
	}
	if last == "" || last == reply {
		t.Fatalf("after 1.2 seconds %q of %q is said", last, reply)
	}
	h.wantOnScreen(last)
	h.wantNotOnScreen(reply)

	// The log's account of the reply changes nothing that was said.
	h.from(sess, delta(core.Delta{Type: core.DeltaMessage, Text: reply, Seq: 11}), typing(false))
	if got := st.Said(); !strings.HasPrefix(got, last) {
		t.Fatalf("said %q once the reply was whole, after %q", got, last)
	}
	h.pass(4 * time.Second)
	if got := st.Said(); got != reply || st.Speaking() {
		t.Fatalf("said %q in the end, speaking %v", got, st.Speaking())
	}
	h.wantOnScreen("The fix is small.", "cache holds.")
	h.wantNotOnScreen("replying", "Two things overlapped.")
}

func TestOpeningHistoryStaysOffTheStageAcrossReconnects(t *testing.T) {
	t.Parallel()
	h := newStaged(t, trueDark(), avatarOn)
	h.size(100, 30)
	sess := h.openChat()
	history := told(t, "Yesterday's reply.\n\n```go\noldExhibit()\n```")
	// Loading and drawing the history before the connection is ready must
	// not flash yesterday's reply or its exhibits onto the stage.
	h.from(sess, history)
	h.wantNotOnScreen("Yesterday's reply.", "oldExhibit()", "why did Thursday spike?")
	h.from(sess, live())
	h.wantNotOnScreen("Yesterday's reply.", "oldExhibit()", "why did Thursday spike?")
	if st := h.m.chat.stage; st.Said() != "" || st.Speaking() {
		t.Fatalf("opening history was spoken: %q", st.Said())
	}
	if len(sess.said) != 0 {
		t.Fatalf("opening sent a message: %v", sess.said)
	}

	// A reconnect rereads history. It can also recover a new reply from the
	// same turn: only that new reply belongs on this visit's stage.
	h.from(sess, client.Update{Kind: client.UpdateState, State: client.StateDown})
	h.from(sess, history, client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 4, h.now, core.KindAgentMsg, core.MsgBody{Text: "A fresh update."}),
	}}, live())
	h.pass(time.Second)
	h.wantOnScreen("A fresh update.")
	h.wantNotOnScreen("Yesterday's reply.", "oldExhibit()", "why did Thursday spike?")
	if got := h.m.chat.stage.Said(); got != "A fresh update." {
		t.Fatalf("after reconnect, said %q", got)
	}

	// Opening the contact again begins another quiet visit, with the entire
	// latest turn still available when explicitly requested.
	sess = h.openChat()
	history.Events = append(history.Events, logged(t, 4, h.now, core.KindAgentMsg, core.MsgBody{Text: "A fresh update."}))
	h.from(sess, history, live())
	h.wantNotOnScreen("Yesterday's reply.", "A fresh update.", "oldExhibit()")
	h.key(ctrl('o'))
	h.wantOnScreen("Yesterday's reply.", "A fresh update.", "oldExhibit()")
	h.key(esc())
	h.wantNotOnScreen("Yesterday's reply.", "A fresh update.", "oldExhibit()")
}

func TestConnectingDuringAReplyShowsOnlyItsLiveStep(t *testing.T) {
	t.Parallel()
	h := newStaged(t, trueDark(), avatarOn)
	sess := h.openChat()
	h.from(sess, told(t, "An earlier step."), live(), typing(true),
		delta(core.Delta{Type: core.DeltaText, Text: "Still working."}))
	h.pass(time.Second)
	h.wantOnScreen("Still working.")
	h.wantNotOnScreen("An earlier step.", "why did Thursday spike?")
	h.from(sess, delta(core.Delta{Type: core.DeltaMessage, Text: "Still working.", Seq: 4}), typing(false))
	h.pass(time.Second)
	if got := h.m.chat.stage.Said(); got != "Still working." {
		t.Fatalf("after the live step was committed, said %q", got)
	}
}

func TestEscAndAMessageStopTheAvatarTalking(t *testing.T) {
	t.Parallel()
	h := newStaged(t, trueDark(), avatarOn)
	sess := h.openChat()
	reply := "A long answer that would take the avatar a good few seconds to get through."
	h.from(sess, live())
	h.from(sess, replying("go on", reply)...)
	h.pass(300 * time.Millisecond)
	st := h.m.chat.stage

	// esc stops the reply, and the voice with it: the words are all there.
	h.key(esc())
	if sess.interrupts != 1 || st.Said() != reply || st.Speaking() {
		t.Fatalf("after esc: %d interrupts, %q said, speaking %v", sess.interrupts, st.Said(), st.Speaking())
	}
	// With no reply in flight esc still stops the voice, and interrupts
	// nothing.
	h.from(sess, delta(core.Delta{Type: core.DeltaMessage, Text: reply + " And more.", Seq: 11}), typing(false))
	h.pass(100 * time.Millisecond)
	if !st.Speaking() {
		t.Fatal("more was said and the avatar is silent")
	}
	h.key(esc())
	if sess.interrupts != 1 || st.Speaking() {
		t.Fatalf("after a second esc: %d interrupts, speaking %v", sess.interrupts, st.Speaking())
	}

	// A message does the same: the avatar listens.
	h.from(sess, replying("and then?", reply)...)
	h.pass(300 * time.Millisecond)
	h.typeText("never mind")
	h.key(enter())
	if h.m.chat.stage.Speaking() {
		t.Error("the avatar talks on over a message")
	}
	h.wantOnScreen("❯ never mind", "sending")
}

func TestAReplyIsSaidWithoutItsMarksAndShowsTheRest(t *testing.T) {
	t.Parallel()
	h := newStaged(t, trueDark(), avatarOn)
	h.size(100, 30)
	sess := h.openChat()
	h.from(sess, live())
	reply := "The **fix** is `small`:\n\n```go\nliveMsgs = append(liveMsgs, timeNote(now))\n```\n\nThat is [all](https://example.com)."
	h.from(sess, replying("the fix?", reply)...)
	h.from(sess, delta(core.Delta{Type: core.DeltaMessage, Text: reply, Seq: 11}), typing(false))

	h.pass(200 * time.Millisecond)
	h.wantNotOnScreen("timeNote", "**", "`")
	h.pass(5 * time.Second)
	if got, want := h.m.chat.stage.Said(), "The fix is small:\n\nThat is all."; got != want {
		t.Fatalf("said %q, want %q", got, want)
	}
	h.wantOnScreen("The fix is small:", "That is all.", "liveMsgs = append(liveMsgs, timeNote(now))")
	h.wantNotOnScreen("```", "example.com")
	// The code stands beside the avatar, which has moved over for it.
	for _, row := range h.body() {
		if at := strings.Index(row, "liveMsgs"); at >= 0 && at < 40 {
			t.Errorf("the code stands at byte %d of its row, want it on the right:\n%s", at, strings.Join(h.body(), "\n"))
		}
	}
}

func TestAnExhibitWithTheSameSourceCanChange(t *testing.T) {
	t.Parallel()
	h := newStaged(t, trueDark(), avatarOn)
	h.size(120, 40)
	h.openChat()
	c := h.m.chat
	show := script.Show{Kind: script.Table, N: 1, Text: "| Count |\n|---|\n| 7 |"}
	c.stage.Resize(120, 36)
	before := c.exhibit(0, 0, 11, 0, show)
	c.stage.Perform(stage.Turn{ID: 1, Over: true, Shows: []stage.Exhibit{before}}, true)
	c.stage.View()
	show.Text = "| Count |\n|---|\n| 8 |"
	after := c.exhibit(0, 0, 11, 0, show)
	if before.Key != after.Key || before.Rev == after.Rev {
		t.Fatal("content changes must invalidate the same exhibit's drawing")
	}
	c.stage.Perform(stage.Turn{ID: 1, Over: true, Shows: []stage.Exhibit{after}}, true)
	view := ansi.Strip(strings.Join(c.stage.View(), "\n"))
	if !strings.Contains(view, "8") || strings.Contains(view, "7") {
		t.Fatalf("the old value remains on stage:\n%s", view)
	}
}

func TestInspectionIncludesARetainedExhibitsReply(t *testing.T) {
	t.Parallel()
	h := newStaged(t, trueDark(), avatarOn)
	h.size(100, 40)
	sess := h.openChat()
	h.from(sess, live())
	reply := "Here is the result.\n\n```text\nretained-artifact\n```"
	h.from(sess, replying("show the result", reply)...)
	h.from(sess, delta(core.Delta{Type: core.DeltaMessage, Text: reply, Seq: 11}), typing(false))
	h.key(esc())
	h.from(sess, delta(core.Delta{Type: core.DeltaUser, Text: "is that final?", Seq: 12}), typing(true))
	h.from(sess, delta(core.Delta{Type: core.DeltaMessage, Text: "Yes, it is final.", Seq: 13}), typing(false))
	h.key(esc())
	h.key(ctrl('o'))
	lines := ansi.Strip(strings.Join(h.m.chat.sheetLines(h.now), "\n"))
	if !strings.Contains(lines, "retained-artifact") || !strings.Contains(lines, "Yes, it is final.") {
		t.Fatalf("inspection lost the retained artifact or current answer:\n%s", lines)
	}
}

func TestAPictureStandsWhereTheReplyPutsIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		place     string
		left, top bool
		right     bool
		bottom    bool
	}{
		{place: "left", left: true},
		{place: "right", right: true},
		{place: "top", top: true},
		{place: "bottom", bottom: true},
		{place: "top-left", left: true, top: true},
		{place: "bottom right", right: true, bottom: true},
		{place: "", right: true},        // where there is room: beside the avatar
		{place: "nowhere", right: true}, // a word that is no place is no place
	} {
		t.Run(tc.place, func(t *testing.T) {
			t.Parallel()
			h := newStaged(t, trueDark(), avatarOn)
			h.size(100, 30)
			sess := h.openChat()
			h.from(sess, live())
			reply := fmt.Sprintf("Here it is: ![latency by day](latency.png %q) as asked.", tc.place)
			if tc.place == "" {
				reply = "Here it is: ![latency by day](latency.png) as asked."
			}
			h.from(sess, replying("plot it", reply)...)
			h.from(sess, delta(core.Delta{Type: core.DeltaMessage, Text: reply, Seq: 11}), typing(false))
			h.pass(200 * time.Millisecond)
			h.wantNotOnScreen("[image:")
			h.pass(3 * time.Second)
			if got := h.m.chat.stage.Said(); got != "Here it is: as asked." {
				t.Fatalf("said %q", got)
			}
			body := h.body()
			row, col := -1, -1
			for i, line := range body {
				if before, _, found := strings.Cut(line, "[image: latency.png"); found {
					row, col = i, ansi.StringWidth(before)
				}
			}
			if row < 0 {
				t.Fatalf("the picture is not on the stage:\n%s", strings.Join(body, "\n"))
			}
			// The first row is the message that is being answered.
			if got := col < 10; got != tc.left {
				t.Errorf("the picture is at column %d", col)
			}
			if got := col > 50; got != tc.right {
				t.Errorf("the picture is at column %d", col)
			}
			if got := row <= 2; got != tc.top {
				t.Errorf("the picture is on row %d of %d", row, len(body))
			}
			if got := row >= len(body)-4; got != tc.bottom {
				t.Errorf("the picture is on row %d of %d", row, len(body))
			}
			if cols, rows := h.m.chat.stage.Face(); cols < minFace || rows < 4 {
				t.Errorf("the avatar is %d by %d cells beside the picture", cols, rows)
			}
		})
	}
}

// minFace is the fewest columns of avatar a test takes for one.
const minFace = 10

func TestAPictureIsFetchedOnceItsReplyIsInTheLog(t *testing.T) {
	t.Parallel()
	caps := trueDark()
	caps.Images = termcaps.ImagesBlocks
	r := newImageRig(t, caps, Options{Avatar: Avatar{Show: avatarOn}})
	r.size(100, 30)
	r.put("latency.png", chartPNG(t, 640, 360), fixedNow())
	sess := r.open()
	reply := `Look ![latency by day](latency.png "left") here.`
	r.from(sess, replying("plot it", reply)...)
	r.pass(2 * time.Second)
	// While the reply is being written its picture has no name to be kept
	// under: nothing is fetched for it.
	r.settle()
	if got := r.b.fetched(); len(got) != 0 {
		t.Fatalf("fetched %v for a reply that is not in the log", got)
	}
	r.wantOnScreen("[image: latency.png]")

	r.from(sess, delta(core.Delta{Type: core.DeltaMessage, Text: reply, Seq: 11}), typing(false))
	r.settle()
	r.pass(100 * time.Millisecond)
	if got := r.b.fetched(); len(got) != 1 || got[0].path != "latency.png" {
		t.Fatalf("fetched %v, want the picture once", got)
	}
	r.wantOnScreen("latency.png · 640×360", "/image save 11")
	r.wantNotOnScreen("[image:")
	body := r.body()
	blocks := 0
	for _, row := range body {
		if before, _, found := strings.Cut(row, "▀▀▀▀"); found && ansi.StringWidth(before) < 10 {
			blocks++
		}
	}
	if blocks < 6 {
		t.Errorf("%d rows of the picture stand on the left:\n%s", blocks, strings.Join(body, "\n"))
	}
	// The thread shows the same picture, from what was fetched for the stage.
	r.command("/avatar off")
	r.settle()
	r.wantOnScreen("latency.png · 640×360")
	if got := r.b.fetched(); len(got) != 1 {
		t.Errorf("the thread fetched the picture again: %v", got)
	}
}

func TestWhatATurnIsBusyWithStandsUnderTheAvatar(t *testing.T) {
	t.Parallel()
	h := newStaged(t, trueDark(), avatarOn)
	h.size(100, 30)
	sess := h.openChat()
	h.from(sess, live())
	h.from(sess,
		delta(core.Delta{Type: core.DeltaUser, Text: "what is 6*7?", Seq: 10}),
		typing(true),
		delta(core.Delta{Type: core.DeltaText, Text: "Let me check."}),
		delta(core.Delta{Type: core.DeltaToolCall, Tool: "python", Text: "print(6*7)", Seq: 11}),
	)
	h.now = h.now.Add(2100 * time.Millisecond)
	h.send(toolTickMsg{sess: sess})
	h.wantOnScreen("⏺ python  running… 2.1s", "running python │ esc stop")
	h.wantNotOnScreen("print(6*7)")

	// What it said before the call and after it is one speech.
	h.from(sess,
		delta(core.Delta{Type: core.DeltaToolResult, Tool: "python", Text: "42\n", Seq: 12}),
		delta(core.Delta{Type: core.DeltaText, Text: "It is 42."}),
		delta(core.Delta{Type: core.DeltaMessage, Text: "It is 42.", Seq: 13}),
		typing(false),
	)
	h.pass(3 * time.Second)
	if got, want := h.m.chat.stage.Said(), "Let me check.\n\nIt is 42."; got != want {
		t.Fatalf("said %q, want %q", got, want)
	}
	h.wantNotOnScreen("running")

	// A turn that failed says so where the tool stood, and so does one that
	// was cut off.
	h.from(sess, replying("and 6*8?", "It is")...)
	h.from(sess, delta(core.Delta{Type: core.DeltaError, Text: "the provider is down"}), typing(false))
	h.wantOnScreen("✗ no reply: the provider is down")
	h.from(sess, replying("again?", "It is")...)
	h.from(sess, delta(core.Delta{Type: core.DeltaInterrupted, Seq: 21}), typing(false))
	h.wantOnScreen("⏹ interrupted")
}

func TestTheTurnAndWhatACommandPrintsAreReadOnASheet(t *testing.T) {
	t.Parallel()
	h := newStaged(t, trueDark(), avatarOn)
	h.size(100, 30)
	sess := h.openChat()
	at := fixedNow().Add(-time.Hour)
	h.from(sess, client.Update{Kind: client.UpdateEvents, Start: true, Events: []core.Event{
		logged(t, 1, at, core.KindUserMsg, core.MsgBody{Text: "an older question"}),
		logged(t, 2, at, core.KindAgentMsg, core.MsgBody{Text: "An older answer."}),
		logged(t, 3, at, core.KindUserMsg, core.MsgBody{Text: "what is 6*7?"}),
		logged(t, 4, at, core.KindToolCall, core.ToolCall{ID: "c1", Name: "python", Code: "print(6*7)", Text: "Let me check."}),
		logged(t, 5, at, core.KindToolResult, core.ToolResult{ID: "c1", Output: "42\n"}),
		logged(t, 6, at, core.KindAgentMsg, core.MsgBody{Text: "It is **42**."}),
	}}, live())
	h.wantNotOnScreen("❯ what is 6*7?", "Let me check.", "It is 42.", "print(6*7)", "An older answer.", "this turn")

	// ctrl+o: the turn as a thread shows it, and nothing older.
	h.key(ctrl('o'))
	h.wantOnScreen("this turn · esc closes", "❯ what is 6*7?", "Let me check.", "⏺ python", "print(6*7)", "⎿ 42", "It is 42.")
	h.wantNotOnScreen("An older answer.")
	h.key(ctrl('o'))
	h.wantNotOnScreen("this turn", "print(6*7)", "Let me check.", "It is 42.")
	h.command("/details")
	h.wantOnScreen("this turn · esc closes")
	h.key(esc())
	h.wantNotOnScreen("this turn")
	if sess.interrupts != 0 {
		t.Errorf("closing the sheet interrupted %d times", sess.interrupts)
	}

	// What a command prints is longer than the stage: it scrolls.
	h.command("/help")
	h.wantOnScreen("commands · esc closes · ↑↓ scroll", "/statusline preset | edit")
	h.wantNotOnScreen("/quit", "❯ what is 6*7?")
	h.key(special(tea.KeyPgDown))
	h.wantNotOnScreen("/statusline preset | edit")
	h.command("/bottom")
	h.wantOnScreen("leave, when the box is empty", "read the turn as a thread shows it")
	h.command("/top")
	h.wantOnScreen("/statusline preset | edit")
	for range 3 {
		h.key(special(tea.KeyDown))
	}
	h.wantNotOnScreen("/statusline preset | edit")
	h.wantFits("the sheet", 100, 30)

	// A message closes it: the answer is the avatar's to give.
	h.typeText("thanks")
	h.key(enter())
	h.wantNotOnScreen("esc closes")
	h.wantOnScreen("❯ thanks")

	h.command("/status")
	h.wantOnScreen("status · esc closes", "connection", "avatar       on; look ember, voice chirp, silent: no audio device, 38 characters a second")
}

func TestTheAvatarIsThereWhereTheTerminalCanDrawIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, show string
		caps       termcaps.Caps
		staged     bool
		why        string
	}{
		{"asked for nothing", "", trueDark(), false, "off; a conversation is a thread"},
		{"switched off", avatarOff, trueDark(), false, "off; a conversation is a thread"},
		{"where it can be drawn", avatarAuto, trueDark(), true, ""},
		{"256 colours on a light background", avatarAuto, func() termcaps.Caps {
			c := unknown256()
			c.Background = termcaps.BgLight
			return c
		}(), true, ""},
		{"a background nobody named", avatarAuto, unknown256(), false, "the terminal has not said what its background is"},
		{"asked for all the same", avatarOn, unknown256(), true, ""},
		{"16 colours", avatarOn, light16(), false, "it needs 256 colours or more"},
		{"ASCII", avatarOn, noneASCII(), false, "it is drawn with block characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newStaged(t, tc.caps, tc.show)
			sess := h.openChat()
			h.from(sess, told(t, "Two things overlapped."), live())
			if got := h.m.chat.stage != nil; got != tc.staged {
				t.Fatalf("staged = %v, want %v", got, tc.staged)
			}
			if tc.staged {
				h.wantNotOnScreen("Two things overlapped.")
			} else {
				h.wantOnScreen("Two things overlapped.")
			}
			h.command("/avatar")
			if !strings.Contains(h.note(), tc.why) {
				t.Errorf("/avatar says %q, want it to say %q", h.note(), tc.why)
			}
			h.wantFits(tc.name, 80, 24)
		})
	}
}

// The terminal says what its background is a moment after the start. The
// avatar comes with the answer, and the opening history stays off its stage.
func TestTheAvatarComesWithTheTerminalsAnswer(t *testing.T) {
	t.Parallel()
	env := termcaps.Env{Term: "xterm-256color", Detected: termcaps.ColorTrue, Lang: "en_US.UTF-8"}
	h := newHarnessWith(t, Options{Env: env, Avatar: Avatar{Show: avatarAuto}})
	sess := h.openChat()
	h.from(sess, told(t, "Two things overlapped."), live())
	if h.m.chat.stage != nil {
		t.Fatal("the avatar is drawn against a background nobody named")
	}
	h.wantOnScreen("the beginning")

	h.send(tea.BackgroundColorMsg{Color: color.RGBA{R: 0x1e, G: 0x1e, B: 0x2e, A: 0xff}})
	st := h.m.chat.stage
	if st == nil {
		t.Fatal("the terminal named a dark background and there is no avatar")
	}
	if st.Said() != "" || st.Speaking() {
		t.Errorf("%q is said, speaking %v", st.Said(), st.Speaking())
	}
	h.wantNotOnScreen("the beginning", "Two things overlapped.", "why did Thursday spike?")
	if paper, dark := h.m.paper(); !dark || paper.R > 0.2 || paper.B < 0.15 {
		t.Errorf("the avatar is drawn against %+v (dark %v), want the terminal's own colour", paper, dark)
	}
}

func TestAvatarCommand(t *testing.T) {
	t.Parallel()
	synth := sound.NewSynth(sound.Timbres()[0])
	env, over := terminal(t, trueDark())
	h := newHarnessWith(t, Options{Env: env, Overrides: over, Avatar: Avatar{Show: avatarOn, Synth: synth}})
	sess := h.openChat()
	h.from(sess, told(t, "Two things overlapped."), live())

	for _, tc := range []struct{ line, note string }{
		{"/avatar", "avatar: on; look ember, voice chirp, 38 characters a second, drawn in quadrants"},
		{"/avatar look tide", `avatar: look tide for this session; look = "tide" under [avatar] in chat.toml keeps it`},
		{"/avatar voice deep", `avatar: voice deep for this session; voice = "deep" under [avatar] in chat.toml keeps it`},
		{"/avatar rate 60", "avatar: rate 60 for this session; rate = 60 under [avatar] in chat.toml keeps it"},
		{"/avatar cells octant", `avatar: cells octant for this session; cells = "octant" under [avatar] in chat.toml keeps it`},
		{"/avatar mute", "avatar: muted for this session; mute = true under [avatar] in chat.toml keeps it"},
		{"/avatar", "avatar: on; look tide, voice deep, muted, 60 characters a second, drawn in octants"},
		{"/avatar unmute", "avatar: sound on for this session; mute = false under [avatar] in chat.toml keeps it"},
		{"/avatar off", `avatar: off for this session; show = "off" under [avatar] in chat.toml keeps it`},
		{"/avatar", "avatar: off; a conversation is a thread"},
	} {
		h.command(tc.line)
		if h.note() != tc.note {
			t.Errorf("%s says\n%q, want\n%q", tc.line, h.note(), tc.note)
		}
		if h.draft() != "" {
			t.Errorf("%s left %q in the box", tc.line, h.draft())
		}
	}
	if h.m.chat.stage != nil {
		t.Fatal("/avatar off left the avatar on")
	}
	h.wantOnScreen("the beginning", "❯ why did Thursday spike?", "Two things overlapped.")

	// What it cannot do it says, and keeps the line to be put right.
	for _, tc := range []struct{ line, note string }{
		{"/avatar look plaid", "a look is one of ember, tide, orchid, moss"},
		{"/avatar voice loud", "a voice is one of chirp, mellow, retro, deep"},
		{"/avatar rate 1", "a rate is from 8 to 240 characters a second"},
		{"/avatar rate fast", "a rate is from 8 to 240 characters a second"},
		{"/avatar cells hex", "usage: /avatar on | off | auto | look <name> | voice <name> | rate <n> | cells <kind> | mute | unmute"},
		{"/avatar sing", "usage: /avatar on | off | auto | look <name> | voice <name> | rate <n> | cells <kind> | mute | unmute"},
	} {
		h.command(tc.line)
		if h.note() != tc.note || h.draft() != tc.line {
			t.Errorf("%s says %q and leaves %q in the box, want %q and the line", tc.line, h.note(), h.draft(), tc.note)
		}
		h.m.chat.clearInput()
	}

	// Switched on again, nothing that was on the screen is said aloud.
	h.command("/avatar on")
	if st := h.m.chat.stage; st == nil || st.Speaking() || st.Said() != "" {
		t.Fatal("/avatar on did not bring back an idle avatar")
	}
}

func TestFramesAreAskedForWhileTheStageIsOnScreen(t *testing.T) {
	t.Parallel()
	h := newStaged(t, trueDark(), avatarOn)
	var waits []time.Duration
	h.m.after = func(d time.Duration, msg tea.Msg) tea.Cmd {
		if _, ok := msg.(frameMsg); ok {
			waits = append(waits, d)
		}
		return func() tea.Msg { return msg }
	}
	sess := h.openChat()
	h.from(sess, live())
	if len(waits) != 1 || waits[0] != idleEvery {
		t.Fatalf("frames asked for: %v, want one at the idle pace", waits)
	}
	// One is asked for at a time, however much happens before it comes.
	h.typeText("hello")
	if len(waits) != 1 {
		t.Fatalf("frames asked for: %v, want still one", waits)
	}
	h.send(frameMsg{sess: sess})
	if len(waits) != 2 {
		t.Fatalf("frames asked for: %v, want the next one", waits)
	}
	// While the avatar speaks they come faster.
	h.m.chat.clearInput()
	h.from(sess, replying("go on", "A reply that takes a while to say, word by word.")...)
	h.send(frameMsg{sess: sess})
	if last := waits[len(waits)-1]; last != frameEvery {
		t.Errorf("the frame while speaking is asked for after %v, want %v", last, frameEvery)
	}

	// Under another screen the avatar holds still, and a frame that was on
	// its way starts none.
	n := len(waits)
	h.command("/statusline")
	h.send(frameMsg{sess: sess})
	h.send(frameMsg{sess: sess})
	if len(waits) != n {
		t.Errorf("%d frames were asked for under the status line's editor", len(waits)-n)
	}
	h.key(esc())
	if len(waits) != n+1 {
		t.Errorf("back in the conversation %d frames were asked for, want 1", len(waits)-n)
	}

	// A frame for a conversation that was left is dropped; a thread asks
	// for none.
	n = len(waits)
	h.command("/avatar off")
	h.send(frameMsg{sess: sess})
	h.key(ctrl('p'))
	h.send(frameMsg{sess: sess})
	if len(waits) != n {
		t.Errorf("%d frames were asked for with no stage to draw", len(waits)-n)
	}
}

// A thread asks for no frame at all: a client with nothing going on stays
// silent, as before.
func TestAThreadAsksForNoFrames(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	h.m.after = func(time.Duration, tea.Msg) tea.Cmd {
		t.Error("a timer was set")
		return nil
	}
	sess := h.openChat()
	h.from(sess, told(t, "Two things overlapped."), live())
	h.typeText("hello")
	if h.m.frames() != nil {
		t.Error("a thread asks for a frame")
	}
}

// The stage at every size, in every state: no row is wider than the
// terminal by either way of counting cells, the screen has exactly the
// terminal's rows, and the row that is typed in is always there.
func TestTheStageFitsAtEverySize(t *testing.T) {
	t.Parallel()
	reply := "p99 **peaked** on `Thursday`, 日本 too 👍🏽:\n\n```go\nx := 1 // a line of code that goes on and on and on and on and on and on and on\n```\n\n" +
		"![chart](latency.png \"left\")\n\n| day | ms |\n|-----|----|\n| Thu | 91 |\n\nAnd a last paragraph that is long enough to wrap in any caption there is room for."
	for _, width := range []int{120, 100, 72, 60, 40, 30, 20, 10} {
		for _, height := range []int{40, 30, 16, 12, 8, 5, 3, 1} {
			h := newStaged(t, trueDark(), avatarOn)
			h.size(width, height)
			sess := h.openChat()
			what := func(state string) string { return "the stage, " + state }
			h.wantFits(what("empty"), width, height)
			h.from(sess, told(t, reply), live())
			h.wantFits(what("with a turn from the log"), width, height)
			h.from(sess, replying("plot last week's latency, and tell me what it means for the cache", reply)...)
			for range 6 {
				h.pass(500 * time.Millisecond)
				h.wantFits(what("speaking"), width, height)
			}
			h.from(sess, delta(core.Delta{Type: core.DeltaToolCall, Tool: "python", Text: "import time", Seq: 11}))
			h.typeText("compare with the week before")
			h.wantFits(what("running a tool, with a draft"), width, height)
			h.m.chat.clearInput()
			h.key(ctrl('o'))
			h.wantFits(what("the turn's sheet"), width, height)
			h.key(esc())
			h.typeText("/")
			h.wantFits(what("with the list of commands"), width, height)
			h.m.chat.clearInput()
			h.command("/help")
			h.wantFits(what("the help sheet"), width, height)
		}
	}
}

// Nothing a reply holds reaches the terminal as more than text: on a stage
// as in a thread.
func TestTheStageDrawsNoSequenceOfAReplys(t *testing.T) {
	t.Parallel()
	h := newStaged(t, trueDark(), avatarOn)
	h.size(100, 30)
	sess := h.openChat()
	hostile := "safe \x1b]0;owned\x07words \x1b[2Jand \u202eflipped\u202c text\x1b]8;;https://evil.example\x1b\\link\x1b]8;;\x1b\\ \x9b31m \x07 end."
	h.from(sess, told(t, hostile), live())
	h.from(sess, replying("again", hostile)...)
	h.pass(4 * time.Second)
	for _, v := range []string{h.m.View().Content} {
		if strings.ContainsAny(v, "\x07\u009b\u202e\u202c") || strings.Contains(v, "\x1b]") || strings.Contains(v, "\x1b[2J") {
			t.Fatalf("the screen holds a sequence of the reply's:\n%q", v)
		}
		for rest := v; ; {
			at := strings.IndexByte(rest, 0x1b)
			if at < 0 {
				break
			}
			rest = rest[at+1:]
			// Every sequence on the screen is a colour or a style.
			end := strings.IndexFunc(rest, func(r rune) bool { return r >= 0x40 && r != '[' })
			if !strings.HasPrefix(rest, "[") || end < 0 || rest[end] != 'm' {
				t.Fatalf("the screen holds the sequence %q", rest[:min(len(rest), 12)])
			}
		}
	}
	h.wantOnScreen("safe")
}

// The stage under a real program, with a recorder for a terminal: the avatar
// is written in the terminal's own colours, a frame in which nothing moved
// writes nothing, and the mouse is still never asked for. The program's
// clock stands still, so the avatar does.
func TestTheProgramDrawsTheStage(t *testing.T) {
	t.Parallel()
	env, over := terminal(t, trueDark())
	r := startProgram(t, Options{Env: env, Overrides: over, Avatar: Avatar{Show: avatarOn}})
	out := r.out

	untilShown(t, out, 0, "▸ ada  deepseek/deepseek-flash")
	mark := out.Mark()
	r.write("\r")
	untilShown(t, out, mark, "❯ Message ada")
	drawn := string(out.Since(mark))
	if !strings.Contains(drawn, "\x1b[38;2;") || !strings.ContainsAny(drawn, "▄▀▌▐") {
		t.Fatalf("the avatar was not written:\n%q", drawn)
	}
	if r.m.chat == nil || r.m.chat.stage == nil {
		t.Fatal("the conversation has no stage")
	}

	// Frames go on coming, and write nothing while nothing has moved.
	out.Quiet(t)
	mark = out.Mark()
	for range 5 {
		r.p.Send(frameMsg{sess: r.m.chat.sess})
	}
	out.Quiet(t)
	if idle := out.Since(mark); len(idle) != 0 {
		t.Errorf("frames in which nothing moved wrote %d bytes: %q", len(idle), idle)
	}

	// A typed character still costs what it costs a thread.
	mark = out.Mark()
	r.write("x")
	until(t, out, mark, "x")
	out.Quiet(t)
	if typed := out.Since(mark); len(typed) > 40 {
		t.Errorf("one typed character wrote %d bytes: %q", len(typed), typed)
	}

	r.write("\x03")
	untilShown(t, out, mark, "Message ada")
	r.quit()
	for _, mode := range []string{"?1000h", "?1002h", "?1003h", "?1006h"} {
		if strings.Contains(string(out.Since(0)), mode) {
			t.Errorf("the program switched mouse mode %s on", mode)
		}
	}
}
