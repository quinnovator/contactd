package helperboard

import (
	"testing"

	"github.com/quinnovator/contactd/internal/core"
)

func TestBoard(t *testing.T) {
	t.Parallel()
	var b Board
	b.Tick(1000)
	b.Reset([]core.HelperJSON{{Handle: 3, State: "TASK_STATE_WORKING"}, {Handle: 2, State: "TASK_STATE_WORKING", Reports: 1}})
	b.Delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{Handle: 3, Event: "push", Method: "statusUpdate", State: "TASK_STATE_COMPLETED"}}, 1500)
	if hs := b.Active(); len(hs) != 2 || hs[0].Handle != 2 || hs[1].Handle != 3 {
		t.Fatalf("order: %+v", hs)
	}
	for i := range 205 {
		b.Delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{Handle: 2, Event: "push", Method: "statusUpdate", State: "TASK_STATE_WORKING", Checks: i, Outcome: "quiet", Reports: 1}}, int64(2000+i))
	}
	if len(b.Wire()) != 200 || b.Wire()[0].At != 2005 {
		t.Fatal("wire ring")
	}
	b.Tick(601501)
	if len(b.Active()) != 1 || b.Count() != 1 {
		t.Fatal("terminal fade")
	}
	b.Delta(core.Delta{Type: core.DeltaNotice, Helper: &core.HelperDelta{Handle: 2}}, 602000)
	if h, _ := b.Get(2); h.Reports != 1 || h.Checks != 204 {
		t.Fatalf("notice damaged counters: %+v", h)
	}
}

// A helper that had finished before the session heard of it is history: it
// is kept for Get, but the strip does not show it, on the first snapshot or
// on any snapshot after a reconnect.
func TestSnapshotTerminalDoesNotLinger(t *testing.T) {
	t.Parallel()
	var b Board
	b.Tick(1000)
	snap := []core.HelperJSON{
		{Handle: 1, State: "TASK_STATE_COMPLETED"},
		{Handle: 2, State: "TASK_STATE_FAILED"},
		{Handle: 3, State: "TASK_STATE_WORKING", NextCheck: 9000},
	}
	b.Reset(snap)
	if hs := b.Active(); len(hs) != 1 || hs[0].Handle != 3 {
		t.Fatalf("active after first snapshot: %+v", hs)
	}
	if _, ok := b.Get(1); !ok {
		t.Fatal("a finished helper is gone from Get")
	}
	b.Tick(2000)
	b.Reset(snap)
	if hs := b.Active(); len(hs) != 1 || hs[0].Handle != 3 {
		t.Fatalf("active after reconnect: %+v", hs)
	}
	// A repeated terminal delta does not bring it back.
	b.Delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{Handle: 1, Event: "push", State: "TASK_STATE_COMPLETED"}}, 2500)
	if hs := b.Active(); len(hs) != 1 {
		t.Fatalf("a repeated terminal delta revived a finished helper: %+v", hs)
	}
}

// A helper seen finishing lingers ten minutes, also across a reconnect, and
// one that finished while the session was reconnecting lingers from then.
func TestLiveFinishLingers(t *testing.T) {
	t.Parallel()
	var b Board
	b.Tick(1000)
	b.Reset([]core.HelperJSON{{Handle: 1, State: "TASK_STATE_WORKING", NextCheck: 5000}, {Handle: 2, State: "TASK_STATE_WORKING"}})
	b.Delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{Handle: 1, Event: "push", State: "TASK_STATE_CANCELED"}}, 2000)
	h, _ := b.Get(1)
	if h.NextCheck != 0 {
		t.Fatalf("a finished helper keeps a countdown: %+v", h)
	}
	b.Tick(300000)
	b.Reset([]core.HelperJSON{{Handle: 1, State: "TASK_STATE_CANCELED"}, {Handle: 2, State: "TASK_STATE_COMPLETED"}})
	if hs := b.Active(); len(hs) != 2 {
		t.Fatalf("live finishes should linger: %+v", hs)
	}
	b.Tick(2000 + linger)
	if hs := b.Active(); len(hs) != 1 || hs[0].Handle != 2 {
		t.Fatalf("helper 1 should have faded, helper 2 not yet: %+v", hs)
	}
	b.Tick(300000 + linger)
	if hs := b.Active(); len(hs) != 0 {
		t.Fatalf("every finished helper should have faded: %+v", hs)
	}
}

// A notice carries only the helper's handle: it changes nothing else, and
// makes no row for a helper the board does not know.
func TestSparseDeltas(t *testing.T) {
	t.Parallel()
	var b Board
	b.Tick(1000)
	b.Reset([]core.HelperJSON{{Handle: 1, Title: "watch", State: "TASK_STATE_WORKING", NextCheck: 60000, Checks: 2, LastOutcome: "quiet"}})
	b.Delta(core.Delta{Type: core.DeltaNotice, Helper: &core.HelperDelta{Handle: 1, Event: "notice"}}, 2000)
	h, _ := b.Get(1)
	if h.NextCheck != 60000 || h.Title != "watch" || h.State != "TASK_STATE_WORKING" || h.Checks != 2 || h.LastOutcome != "quiet" {
		t.Fatalf("a notice changed the row: %+v", h)
	}
	b.Delta(core.Delta{Type: core.DeltaNotice, Helper: &core.HelperDelta{Handle: 9, Event: "notice"}}, 2000)
	b.Delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{Handle: 8, Event: "push", Method: "artifactUpdate"}}, 2000)
	if _, ok := b.Get(9); ok {
		t.Fatal("a notice made a row for an unknown helper")
	}
	if _, ok := b.Get(8); ok {
		t.Fatal("a delta without a state made a row for an unknown helper")
	}
	if len(b.Active()) != 1 || b.Count() != 1 {
		t.Fatalf("ghost rows: %+v", b.Active())
	}
	if len(b.Wire()) != 1 || b.Wire()[0].Handle != 8 {
		t.Fatalf("the exchange should still be recorded: %+v", b.Wire())
	}
	// A whole row with no next check clears the countdown.
	b.Delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{Handle: 1, Event: "push", State: "TASK_STATE_INPUT_REQUIRED"}}, 3000)
	if h, _ := b.Get(1); h.NextCheck != 0 {
		t.Fatalf("a full row kept a stale countdown: %+v", h)
	}
	// A new helper announced with its state does get a row.
	b.Delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{Handle: 4, Event: "push", State: "TASK_STATE_WORKING", NextCheck: 9000}}, 3000)
	if h, ok := b.Get(4); !ok || h.NextCheck != 9000 {
		t.Fatalf("a new helper: %+v %v", h, ok)
	}
}
