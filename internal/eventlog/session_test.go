package eventlog_test

import (
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/eventlog"
)

func TestVisitStateOutlivesTail(t *testing.T) {
	t.Parallel()
	s := open(t, eventlog.Options{})
	id, err := s.CreateContact(t.Context(), core.AgentConfig{}, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	write := func(seq uint64, ts int64, kind core.EventKind, session, zone string) {
		t.Helper()
		err := s.Append(t.Context(), core.Write{Contact: id, Event: &core.Event{Contact: id, Seq: seq, TS: ts, Kind: kind, Body: []byte(`{}`)}, SessionID: session, TimeZone: zone})
		if err != nil {
			t.Fatal(err)
		}
	}
	write(1, 10, core.KindNotice, "visit-a", "")
	write(2, 11, core.KindUserMsg, "", "America/Chicago")
	write(3, 12, core.KindAgentMsg, "", "")
	write(4, 13, core.KindAttempt, "", "")
	h, err := s.Hydrate(t.Context(), id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if h.TimeZone != "America/Chicago" || h.LastSpoke != 12 || len(h.Sessions) != 1 || h.Sessions[0] != "visit-a" || len(h.Tail) != 1 || h.Tail[0].Seq != 4 {
		t.Fatalf("hydrate = %+v", h)
	}
}
