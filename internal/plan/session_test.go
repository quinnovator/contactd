package plan

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/core"
)

func TestSessionNoticeIsMemoryNotRecallQuery(t *testing.T) {
	t.Parallel()
	log := new(logBuilder)
	raw, _ := json.Marshal(core.NoticeBody{Source: "session-start", Text: "The person returned on Tuesday."})
	log.events = append(log.events, core.Event{Seq: 1, TS: 123, Kind: core.KindNotice, Body: raw})
	log.user("hello")
	log.events[1].TS = 456
	pending, err := Layout(nil, log.events, Reader{}, DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.Texts) != 1 || pending.Texts[0] != "hello" || pending.TS != 456 {
		t.Fatalf("pending = %+v", pending)
	}
	first := mustBuild(t, nil, Input{Agent: testAgent(), Tail: log.events}, nil, t0, DefaultParams())
	if !strings.Contains(first.Tiers[TierLive].Msgs[0].Text, "session-start") {
		t.Fatal("notice missing from prompt")
	}
	log.agent("welcome")
	log.user("thanks")
	next := mustBuild(t, first, Input{Agent: testAgent(), Tail: log.events}, nil, t0+1000, DefaultParams())
	if next.FirstChanged(first) != int(TierLive) {
		t.Fatalf("cache broke at %d", next.FirstChanged(first))
	}
	if next.Tiers[TierTail].Msgs[0].Text != first.Tiers[TierLive].Msgs[0].Text {
		t.Fatal("notice was rewritten")
	}
}

func TestTimeNoteUsesZoneAndDSTWithoutChangingStaticTier(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ utc, want string }{
		{"2026-01-13T14:05:00Z", "<now>2026-01-13T08:05-06:00 (Tuesday, America/Chicago)</now>"},
		{"2026-10-13T14:05:00Z", "<now>2026-10-13T09:05-05:00 (Tuesday, America/Chicago)</now>"},
	} {
		instant, _ := time.Parse(time.RFC3339, tc.utc)
		if got := timeNote(instant.UnixMilli(), loc).Text; got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
	log := new(logBuilder).user("hello")
	a := mustBuild(t, nil, Input{Agent: testAgent(), Tail: log.events}, nil, t0, DefaultParams())
	b := mustBuild(t, nil, Input{Agent: testAgent(), Tail: log.events, Location: loc}, nil, t0, DefaultParams())
	if a.Tiers[TierStatic].Hash != b.Tiers[TierStatic].Hash || !strings.HasPrefix(a.Tiers[TierStatic].Msgs[0].Text, Preamble) {
		t.Fatal("zone changed static tier or preamble missing")
	}
}
