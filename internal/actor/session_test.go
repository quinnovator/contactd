package actor_test

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quinnovator/contactd/internal/core"
)

func sessionNotices(t *testing.T, h *harness) []core.NoticeBody {
	t.Helper()
	h.log.mu.Lock()
	defer h.log.mu.Unlock()
	var out []core.NoticeBody
	for _, ev := range h.log.events[h.id] {
		if ev.Kind != core.KindNotice {
			continue
		}
		var n core.NoticeBody
		if err := json.Unmarshal(ev.Body, &n); err != nil {
			t.Fatal(err)
		}
		if n.Source == "session-start" {
			out = append(out, n)
		}
	}
	return out
}

func TestVisitSurvivesHibernationAndInterleavedClients(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.log.mu.Lock()
		h.log.visits[h.id] = core.Hydrated{}
		h.log.mu.Unlock()
		send := func(v core.Visit) {
			t.Helper()
			if _, err := h.reg.Message(t.Context(), h.id, "hello", v); err != nil {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Second)
		}
		a := core.Visit{SessionID: "a", TimeZone: "America/Chicago", Presentation: "avatar"}
		b := core.Visit{SessionID: "b", TimeZone: "Asia/Tokyo", Presentation: "text"}
		send(a)
		send(b)
		time.Sleep(40 * time.Minute) // hibernate, then reconnect the first client
		if h.reg.Hydrated() != 0 {
			t.Fatal("actor did not hibernate")
		}
		send(a)
		notices := sessionNotices(t, h)
		if len(notices) != 2 {
			t.Fatalf("notices = %+v", notices)
		}
		if !strings.Contains(notices[0].Text, "first meeting") || !strings.Contains(notices[0].Text, "America/Chicago") || !strings.Contains(notices[0].Text, "avatar") {
			t.Fatalf("first notice = %+v", notices[0])
		}
		if !strings.Contains(notices[1].Text, "You last spoke") || !strings.Contains(notices[1].Text, "as text") {
			t.Fatalf("return notice = %+v", notices[1])
		}
		h.log.mu.Lock()
		state := h.log.visits[h.id]
		events := h.log.events[h.id]
		h.log.mu.Unlock()
		if state.TimeZone != "America/Chicago" {
			t.Errorf("zone = %s", state.TimeZone)
		}
		if events[0].Kind != core.KindNotice || events[1].Kind != core.KindUserMsg {
			t.Fatal("notice must immediately precede the first message")
		}
	})
}

func TestImplicitVisitAfterSilenceUsesLastReply(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.say("hello")
		time.Sleep(5 * time.Second)
		h.say("still here")
		time.Sleep(31 * time.Minute)
		h.say("back again")
		time.Sleep(5 * time.Second)
		ns := sessionNotices(t, h)
		if len(ns) != 1 || !strings.Contains(ns[0].Text, "30 minutes") {
			t.Fatalf("notices = %+v", ns)
		}
	})
}
