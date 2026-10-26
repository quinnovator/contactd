package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
)

func TestAvatarCommunicationFollowsLiveA2ATraffic(t *testing.T) {
	t.Parallel()
	h := newStaged(t, trueDark(), avatarOn)
	h.size(160, 48)
	sess := h.openChat()
	h.from(sess, live(), client.Update{Kind: client.UpdateHelpers, Helpers: []core.HelperJSON{
		{Handle: 1, Title: "Status watcher", State: "TASK_STATE_WORKING"},
	}})
	body := strings.Join(h.body(), "\n")
	if !strings.Contains(body, "Status watcher") || strings.ContainsAny(body, "←→") {
		t.Fatal("opening a helper snapshot should show quiet background work")
	}
	h.from(sess, delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{
		Handle: 1, Dir: "out", Method: "SendMessage", Event: "call",
	}}))
	h.wantOnScreen("A2A", "Status watcher", "message sent", "→")
	h.from(sess, delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{
		Handle: 1, Dir: "in", Method: "artifactUpdate", Event: "push",
	}}))
	h.pass(time.Second)
	h.wantOnScreen("report received", "←")
	h.command("/a2a")
	h.wantOnScreen("artifactUpdate", "SendMessage")
	h.key(esc())
	h.from(sess, client.Update{Kind: client.UpdateState, State: client.StateDown})
	if strings.Contains(strings.Join(h.body(), "\n"), "A2A") {
		t.Fatal("disconnected stream left an animated conversation behind")
	}
}

func TestPeerNamesAreSafeAndWaitingHelpersPersist(t *testing.T) {
	t.Parallel()
	h := newStaged(t, trueDark(), avatarOn)
	h.size(160, 48)
	sess := h.openChat()
	h.from(sess, live(), delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{
		Handle: 1, Title: "Watcher\x1b]52;c;bad\a\nsecond line", Dir: "in", Method: "statusUpdate", State: "TASK_STATE_INPUT_REQUIRED",
	}}))
	h.pass(2 * time.Second)
	h.wantOnScreen("needs an answer")
	if strings.Contains(strings.Join(h.m.chat.stage.View(), "\n"), "\x1b]52") {
		t.Fatal("helper name leaked a terminal command")
	}
	h.now = h.now.Add(10 * time.Second)
	h.send(frameMsg{sess: sess})
	body := strings.Join(h.body(), "\n")
	if !strings.Contains(body, "needs an answer") || strings.ContainsAny(body, "←→") {
		t.Fatal("a helper waiting for an answer should remain without traffic animation")
	}
}

func TestBackgroundHelpersSurviveTurnsAndAvatarToggle(t *testing.T) {
	t.Parallel()
	h := newStaged(t, trueDark(), avatarOn)
	h.size(160, 48)
	sess := h.openChat()
	h.from(sess, live(), client.Update{Kind: client.UpdateHelpers, Helpers: []core.HelperJSON{
		{Handle: 1, Title: "Status watcher", State: "TASK_STATE_WORKING", NextCheck: h.now.Add(time.Minute).UnixMilli(), Checks: 4},
	}})
	h.wantOnScreen("Status watcher", "scheduled", "next check 1:00")
	h.from(sess, replying("another topic", "We can talk while the watcher runs.")...)
	h.key(esc())
	h.now = h.now.Add(30 * time.Second)
	h.send(frameMsg{sess: sess})
	h.wantOnScreen("Status watcher", "next check 0:30", "We can talk")
	h.command("/avatar off")
	h.command("/avatar on")
	h.wantOnScreen("Status watcher", "scheduled")
	h.from(sess, delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{
		Handle: 1, State: "TASK_STATE_COMPLETED", Event: "push",
	}}))
	h.wantOnScreen("completed")
	h.now = h.now.Add(9 * time.Second)
	h.send(frameMsg{sess: sess})
	if strings.Contains(strings.Join(h.body(), "\n"), "Status watcher") {
		t.Fatal("completed helper remained on stage")
	}
	// Refreshing terminal history must not bring the completed card back.
	h.from(sess, client.Update{Kind: client.UpdateHelpers, Helpers: []core.HelperJSON{
		{Handle: 1, Title: "Status watcher", State: "TASK_STATE_COMPLETED"},
	}})
	if strings.Contains(strings.Join(h.body(), "\n"), "Status watcher") {
		t.Fatal("completed history reappeared on refresh")
	}
}
