package helperboard

import (
	"testing"

	"github.com/quinnovator/contactd/internal/core"
)

func TestBoard(t *testing.T) {
	t.Parallel()
	var b Board
	b.Tick(1000)
	b.Reset([]core.HelperJSON{{Handle: 2, State: "TASK_STATE_WORKING", Reports: 1}, {Handle: 1, State: "TASK_STATE_COMPLETED"}})
	if hs := b.Active(); len(hs) != 2 || hs[0].Handle != 2 {
		t.Fatalf("order: %+v", hs)
	}
	for i := range 205 {
		b.Delta(core.Delta{Type: core.DeltaHelper, Helper: &core.HelperDelta{Handle: 2, Event: "push", Method: "statusUpdate", State: "TASK_STATE_WORKING", Checks: i, Outcome: "quiet", Reports: 1}}, int64(2000+i))
	}
	if len(b.Wire()) != 200 || b.Wire()[0].At != 2005 {
		t.Fatal("wire ring")
	}
	b.Tick(601001)
	if len(b.Active()) != 1 || b.Count() != 1 {
		t.Fatal("terminal fade")
	}
	b.Delta(core.Delta{Type: core.DeltaNotice, Helper: &core.HelperDelta{Handle: 2}}, 602000)
	if h, _ := b.Get(2); h.Reports != 1 || h.Checks != 204 {
		t.Fatalf("notice damaged counters: %+v", h)
	}
}
