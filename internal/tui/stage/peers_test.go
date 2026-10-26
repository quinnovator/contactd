package stage

import (
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/tui/speech"
)

func TestPeersShowRequestsThenRepliesAndRetire(t *testing.T) {
	t.Parallel()
	s := newStage(t, 160, 48, Options{})
	s.Perform(Turn{ID: 1, Text: "Checking the status page.", Over: true}, true)
	beforeW, beforeH := s.Face()
	now := time.Unix(100, 0)
	s.Observe(Exchange{Handle: 1, Title: "Status watcher", Direction: "out", Method: "SendMessage"}, now)
	s.Observe(Exchange{Handle: 1, Direction: "in", Method: "artifactUpdate", State: "TASK_STATE_WORKING"}, now)
	view := strings.Join(plain(s), "\n")
	if !strings.Contains(view, "message sent") || !strings.Contains(view, "→") || len(s.plan.peers) != 1 {
		t.Fatalf("request did not bring in a communicating avatar:\n%s", view)
	}
	s.Tick(now)
	s.Tick(now.Add(time.Second))
	view = strings.Join(plain(s), "\n")
	if !strings.Contains(view, "report received") || !strings.Contains(view, "←") {
		t.Fatalf("fast reply replaced the request or lost its direction:\n%s", view)
	}
	for range 30 {
		s.step()
	}
	if s.peers[0].avatar.Mouth() == speech.Rest {
		t.Fatal("the reporting helper did not animate")
	}
	s.AdvancePeers(now.Add(3 * time.Second))
	view = strings.Join(plain(s), "\n")
	if strings.ContainsAny(view, "←→") || !strings.Contains(view, "working") {
		t.Fatalf("idle helper still implies traffic:\n%s", view)
	}
	s.AdvancePeers(now.Add(9 * time.Second))
	view = strings.Join(plain(s), "\n")
	w, h := s.Face()
	if len(s.peers) != 0 || strings.Contains(view, "A2A") || w != beforeW || h != beforeH {
		t.Fatal("expired communication did not restore the original stage")
	}
}

func TestPeerLayoutsLeaveArtifactsAndSpeechReadable(t *testing.T) {
	t.Parallel()
	s := newStage(t, 160, 48, Options{})
	s.Perform(Turn{ID: 1, Text: "The latest results are shown here.", Over: true, Shows: []Exhibit{
		box("table", 0, Auto, "#", 48, 8),
	}}, true)
	for i := range 5 {
		s.Observe(Exchange{Handle: uint64(i + 1), Title: "Status watcher", Direction: "in", Method: "artifactUpdate"}, time.Unix(100, 0))
	}
	for _, size := range [][2]int{{160, 48}, {100, 30}, {60, 24}, {32, 12}, {1, 1}, {160, 48}} {
		s.Resize(size[0], size[1])
		rows := plain(s)
		assertSeparated(t, s)
		for _, row := range rows {
			if s.th.Width(row) > size[0] {
				t.Fatalf("%v: peer exceeded the available width: %q", size, row)
			}
		}
		for i, p := range s.plan.peers {
			if !contains(s.plan.block, p.face) || !contains(s.plan.block, p.label) || p.face.meets(s.plan.face) || p.label.meets(s.plan.text) {
				t.Fatalf("%v: overlapping or out-of-bounds peer: %+v", size, p)
			}
			for _, q := range s.plan.peers[i+1:] {
				if p.face.meets(q.face) || p.label.meets(q.face) || p.label.meets(q.label) {
					t.Fatal("helper avatars overlap")
				}
			}
		}
		if size[0] == 160 && (len(s.plan.peers) == 0 || !s.cards[0].on || !strings.Contains(strings.Join(rows, "\n"), "more")) {
			t.Fatal("wide layout lost helpers, the artifact, or its overflow hint")
		}
		if size[0] == 32 && !strings.Contains(strings.Join(rows, "\n"), "A2A") {
			t.Fatal("compact stage lost the exchange indicator")
		}
	}
}

func TestPeerBurstsAndPausedWindowsDoNotReplayOldTraffic(t *testing.T) {
	t.Parallel()
	s := newStage(t, 160, 48, Options{})
	now := time.Unix(100, 0)
	for i := range 100 {
		for _, dir := range []string{"out", "in"} {
			s.Observe(Exchange{Handle: uint64(i%20 + 1), Direction: dir, Method: "GetTask"}, now)
		}
	}
	if len(s.peers) > maxPeers {
		t.Fatal("peer history is unbounded")
	}
	for _, p := range s.peers {
		if len(p.queue) > 4 {
			t.Fatal("animation backlog is unbounded")
		}
	}
	s.AdvancePeers(now.Add(time.Hour))
	if len(s.peers) != 0 || strings.Contains(strings.Join(plain(s), "\n"), "A2A") {
		t.Fatal("old traffic replayed after a pause")
	}
}

func TestOnlyValidExchangesBringPeersOnStage(t *testing.T) {
	t.Parallel()
	s := newStage(t, 100, 30, Options{})
	for _, ex := range []Exchange{
		{Handle: 1, State: "TASK_STATE_WORKING"},
		{Handle: 1, Method: "SendMessage"},
		{Direction: "out", Method: "SendMessage"},
	} {
		s.Observe(ex, time.Unix(100, 0))
	}
	if len(s.peers) > 0 {
		t.Fatal("a snapshot or invalid exchange caused an animation")
	}
}

func TestFailedExchangeRemainsDistinctFromWorkingState(t *testing.T) {
	t.Parallel()
	s := newStage(t, 100, 40, Options{})
	now := time.Unix(100, 0)
	s.Observe(Exchange{Handle: 1, Direction: "out", Method: "SendMessage", State: "TASK_STATE_WORKING"}, now)
	s.Observe(Exchange{Handle: 1, Direction: "in", Method: "SendMessage", Event: "error", State: "TASK_STATE_WORKING"}, now)
	s.AdvancePeers(now.Add(3 * time.Second))
	if !strings.Contains(strings.Join(plain(s), "\n"), "request failed") {
		t.Fatal("stale working state hid the failed request")
	}
	s.ClearPeers()
	if strings.Contains(strings.Join(plain(s), "\n"), "A2A") {
		t.Fatal("disconnect left communication on stage")
	}
}
