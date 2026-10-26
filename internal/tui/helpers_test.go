package tui

import (
	"encoding/json"
	"testing"

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
