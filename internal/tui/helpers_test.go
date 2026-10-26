package tui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

func TestHelperStripAndOverlays(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.size(100, 30)
	h.from(sess, client.Update{Kind: client.UpdateHelpers, Helpers: []core.HelperJSON{{Handle: 1, Contact: 2, Title: "Status page", State: "TASK_STATE_WORKING", Checks: 3, Reports: 1}}})
	h.wantOnScreen("helper 1", "Status page", "WORKING", "1 helpers")
	tuitest.Golden(t, "helpers.golden", []byte(h.screen()))
	h.from(sess, client.Update{Kind: client.UpdateDelta, Delta: core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{Handle: 1, Event: "push", Method: "artifactUpdate", Dir: "in", Wire: `{"artifactUpdate":{"taskId":"example"}}`, Text: "Page changed"}}})
	h.typeText("/a2a")
	h.key(enter())
	h.wantOnScreen("A2A", "artifactUpdate", "Page changed")
	h.key(enter())
	h.wantOnScreen("taskId")
	h.key(esc())
	h.typeText("/helper 1")
	h.key(enter())
	h.wantOnScreen("read-only: helper 1 works for ada", "esc returns")
	p := h.m.chat.peek
	if p == nil {
		t.Fatal("peek missing")
	}
	raw, _ := json.Marshal(core.NoticeBody{Source: "helper-check", Text: "Check 4 of at most 61"})
	h.send(updatesMsg{sess: p.chat.sess, batch: []client.Update{{Kind: client.UpdateEvents, Start: true, Events: []core.Event{{Seq: 1, Kind: core.KindNotice, Body: raw}}}}})
	h.wantOnScreen("Check 4 of at most 61")
	h.typeText("should not send")
	h.key(enter())
	if h.m.chat.input.Value() != "" || p.chat.input.Value() != "" {
		t.Fatal("peek input is editable")
	}
	h.key(esc())
	if h.m.chat.peek != nil {
		t.Fatal("peek did not close")
	}
}

// Helpers that had finished before the chat opened are not shown, and one
// seen finishing leaves the strip, and gives its row back, once it has
// lingered, with nothing else happening.
func TestHelperStripForgetsFinishedHelpers(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.size(100, 30)
	h.from(sess, client.Update{Kind: client.UpdateHelpers, Helpers: []core.HelperJSON{
		{Handle: 1, Title: "old watch", State: "TASK_STATE_COMPLETED"},
		{Handle: 2, Title: "new watch", State: "TASK_STATE_WORKING"},
	}})
	h.wantOnScreen("new watch")
	h.wantNotOnScreen("old watch")
	if h.m.chat.rows.Helpers != 1 {
		t.Fatalf("strip rows %d, want 1", h.m.chat.rows.Helpers)
	}
	h.from(sess, delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{Handle: 2, Event: "push", State: "TASK_STATE_COMPLETED"}}))
	h.wantOnScreen("new watch", "COMPLETED")
	h.now = h.now.Add(11 * time.Minute)
	h.wantNotOnScreen("new watch")
	if h.m.chat.rows.Helpers != 0 {
		t.Fatalf("the strip kept %d rows after its last helper faded", h.m.chat.rows.Helpers)
	}
}

// The strip ticks every second only while it shows a countdown.
func TestHelperStripTicksOnlyForACountdown(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.size(100, 30)
	h.from(sess, client.Update{Kind: client.UpdateHelpers, Helpers: []core.HelperJSON{{Handle: 1, State: "TASK_STATE_INPUT_REQUIRED"}}})
	if h.m.chat.helperTick() || h.m.chat.ticking {
		t.Fatal("a strip without a countdown ticks")
	}
	next := h.now.Add(90 * time.Second).UnixMilli()
	h.from(sess, delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{Handle: 1, Event: "push", State: "TASK_STATE_WORKING", NextCheck: next}}))
	if !h.m.chat.helperTick() || !h.m.chat.ticking {
		t.Fatal("a countdown does not tick")
	}
	h.wantOnScreen("next 1:30")
	// A notice names the helper and nothing else: the countdown stays.
	h.from(sess, delta(core.Delta{Type: core.DeltaNotice, Text: "helper 1 reports:", Helper: &core.HelperDelta{Handle: 1, Event: "notice"}}))
	h.now = h.now.Add(time.Second)
	h.send(toolTickMsg{sess: sess})
	h.wantOnScreen("next 1:29")
	h.from(sess, delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{Handle: 1, Event: "push", State: "TASK_STATE_CANCELED"}}))
	if h.m.chat.helperTick() {
		t.Fatal("a finished helper keeps the strip ticking")
	}
	h.send(toolTickMsg{sess: sess})
	if h.m.chat.ticking {
		t.Fatal("the tick did not stop")
	}
}

// On a terminal too short for the whole strip, the helpers listed give way
// and the row that counts the rest stays.
func TestHelperStripOverflowSurvivesAShortTerminal(t *testing.T) {
	t.Parallel()
	h := newHarness(t, noneASCII())
	sess := h.openChat()
	h.size(100, 16)
	hs := make([]core.HelperJSON, 0, 5)
	for i := range 5 {
		hs = append(hs, core.HelperJSON{Handle: uint64(i + 1), State: "TASK_STATE_WORKING"})
	}
	h.from(sess, client.Update{Kind: client.UpdateHelpers, Helpers: hs})
	if h.m.chat.rows.Helpers != 3 {
		t.Fatalf("strip rows %d, want 3", h.m.chat.rows.Helpers)
	}
	h.wantOnScreen("helper 1", "helper 2", "+3 more")
	h.wantNotOnScreen("helper 3")
}

// Closing a peek lets go of the pictures it was given.
func TestHelperPeekReleasesItsImages(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.size(100, 30)
	h.from(sess, client.Update{Kind: client.UpdateHelpers, Helpers: []core.HelperJSON{{Handle: 1, Contact: 2, State: "TASK_STATE_WORKING"}}})
	h.typeText("/helper 1")
	h.key(enter())
	p := h.m.chat.peek
	if p == nil {
		t.Fatal("peek missing")
	}
	h.key(esc())
	if p.chat.imgs.ctx.Err() == nil {
		t.Fatal("the closed peek's pictures were not released")
	}
	if h.m.chat.raw != "" {
		t.Fatalf("bytes left unwritten: %q", h.m.chat.raw)
	}
}
