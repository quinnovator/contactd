package transcript_test

import (
	"cmp"
	"slices"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/transcript"
)

// call is the record of a model call a turn made. Its token count follows
// from its sequence number, so a test can tell a record filed under the
// wrong number.
func call(t *testing.T, seq uint64) core.Event {
	t.Helper()
	return event(t, seq, core.KindAttempt, core.AttemptBody{Route: "fake/fake-1", Input: int(seq) * 10})
}

// sideJob is the record of a model call made for bookkeeping.
func sideJob(t *testing.T, seq uint64, purpose string) core.Event {
	t.Helper()
	return event(t, seq, core.KindAttempt, core.AttemptBody{Route: "fake/fake-1", Purpose: purpose, Input: int(seq) * 10})
}

// calls is one page of model calls with these sequence numbers.
func calls(t *testing.T, seqs ...uint64) []core.Event {
	t.Helper()
	out := make([]core.Event, 0, len(seqs))
	for _, seq := range seqs {
		out = append(out, call(t, seq))
	}
	return out
}

// span counts from one number to another by a stride, both ends included.
func span(from, to, stride uint64) []uint64 {
	var out []uint64
	for n := from; n <= to; n += stride {
		out = append(out, n)
	}
	return out
}

// falling counts down from n to 1.
func falling(n uint64) []uint64 {
	out := span(1, n, 1)
	slices.Reverse(out)
	return out
}

// dealt is span(1, n, 1) in a fixed order that is neither rising nor
// falling. stride must share no factor with n.
func dealt(n, stride uint64) []uint64 {
	out := make([]uint64, 0, n)
	for i := range n {
		out = append(out, (i*stride)%n+1)
	}
	return out
}

type attemptsCase struct {
	name string
	// pages are read from the log one after another.
	pages [][]core.Event
	// want is the sequence numbers kept, in order.
	want []uint64
}

func attemptsCases(t *testing.T) []attemptsCase {
	t.Helper()
	evens := span(2, 100, 2) // fifty calls, with room between them
	return []attemptsCase{
		{name: "none"},
		{
			name:  "in order",
			pages: [][]core.Event{calls(t, 2, 4, 6)},
			want:  []uint64{2, 4, 6},
		},
		{
			name:  "out of order within a page",
			pages: [][]core.Event{calls(t, 6, 2, 4)},
			want:  []uint64{2, 4, 6},
		},
		{
			name:  "a newer page read before an older one",
			pages: [][]core.Event{calls(t, 6, 8), calls(t, 2, 4)},
			want:  []uint64{2, 4, 6, 8},
		},
		{
			name:  "repeated",
			pages: [][]core.Event{calls(t, 2, 4), calls(t, 2, 4), calls(t, 4, 4, 6), calls(t, 2)},
			want:  []uint64{2, 4, 6},
		},
		{
			name: "side jobs left out",
			pages: [][]core.Event{{
				call(t, 2), sideJob(t, 3, "checkpoint"), sideJob(t, 5, "summary"), call(t, 7), sideJob(t, 9, "checkpoint"),
			}},
			want: []uint64{2, 7},
		},
		{
			name:  "side jobs and nothing else",
			pages: [][]core.Event{{sideJob(t, 3, "checkpoint"), sideJob(t, 5, "summary")}},
		},
		{
			name: "a record that cannot be read is skipped",
			pages: [][]core.Event{{
				call(t, 2),
				{Contact: 1, Seq: 3, TS: 1003, Kind: core.KindAttempt, Body: []byte(`{"route":`)},
				call(t, 4),
			}},
			want: []uint64{2, 4},
		},
		{
			name: "other events are not model calls",
			pages: [][]core.Event{{
				event(t, 1, core.KindUserMsg, core.MsgBody{Text: "hello"}),
				call(t, 2),
				event(t, 3, core.KindAgentMsg, core.MsgBody{Text: "Hi."}),
			}},
			want: []uint64{2},
		},
		{
			name:  "exactly fifty are all kept",
			pages: [][]core.Event{calls(t, span(1, 50, 1)...)},
			want:  span(1, 50, 1),
		},
		{
			name:  "sixty in order keep the newest fifty",
			pages: [][]core.Event{calls(t, span(1, 60, 1)...)},
			want:  span(11, 60, 1),
		},
		{
			name:  "sixty, newest first, keep the newest fifty",
			pages: [][]core.Event{calls(t, falling(60)...)},
			want:  span(11, 60, 1),
		},
		{
			name:  "sixty in no order keep the newest fifty",
			pages: [][]core.Event{calls(t, dealt(60, 37)...)},
			want:  span(11, 60, 1),
		},
		{
			name:  "sixty in no order, each one twice",
			pages: [][]core.Event{calls(t, dealt(60, 37)...), calls(t, dealt(60, 7)...)},
			want:  span(11, 60, 1),
		},
		{
			name:  "an older call arriving once full is dropped",
			pages: [][]core.Event{calls(t, span(11, 60, 1)...), calls(t, 5, 10)},
			want:  span(11, 60, 1),
		},
		{
			name:  "a repeat once full changes nothing",
			pages: [][]core.Event{calls(t, span(11, 60, 1)...), calls(t, 11, 30, 60)},
			want:  span(11, 60, 1),
		},
		{
			name:  "a newer call once full pushes the oldest out",
			pages: [][]core.Event{calls(t, evens...), calls(t, 101)},
			want:  append(span(4, 100, 2), 101),
		},
		{
			name:  "a call between two held once full pushes the oldest out",
			pages: [][]core.Event{calls(t, evens...), calls(t, 51)},
			want:  slices.Concat(span(4, 50, 2), []uint64{51}, span(52, 100, 2)),
		},
		{
			name:  "a call just newer than the oldest once full takes its place",
			pages: [][]core.Event{calls(t, evens...), calls(t, 3)},
			want:  append([]uint64{3}, span(4, 100, 2)...),
		},
	}
}

// Attempts keeps a turn's own model calls in sequence order however the log
// was read, the newest fifty of them, and Attempt is the newest of those.
func TestAttempts(t *testing.T) {
	t.Parallel()
	for _, tc := range attemptsCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var tr transcript.Transcript
			for _, p := range tc.pages {
				tr.Events(p)
			}

			got := tr.Attempts()
			seqs := make([]uint64, 0, len(got))
			for _, a := range got {
				seqs = append(seqs, a.Seq)
				// The record filed under a number is that event's own.
				if a.Input != int(a.Seq)*10 || a.TS != int64(1000+a.Seq) || a.Route != "fake/fake-1" {
					t.Errorf("attempt %d = %+v, which is not that event's record", a.Seq, a)
				}
				if a.Purpose != "" {
					t.Errorf("attempt %d is a side job: %q", a.Seq, a.Purpose)
				}
			}
			if !slices.Equal(seqs, tc.want) {
				t.Errorf("attempts = %d\nwant       %d", seqs, tc.want)
			}

			newest, ok := tr.Attempt()
			if ok != (len(tc.want) > 0) {
				t.Fatalf("Attempt reports %t with %d calls kept", ok, len(tc.want))
			}
			if !ok {
				if newest != (core.AttemptBody{}) {
					t.Errorf("Attempt = %+v with no call made", newest)
				}
				return
			}
			if last := got[len(got)-1]; newest != last.AttemptBody || newest.Input != int(tc.want[len(tc.want)-1])*10 {
				t.Errorf("Attempt = %+v, want the newest, %+v", newest, last.AttemptBody)
			}
		})
	}
}

// Model calls are not part of the conversation: reading one adds no item and
// leaves what a view drew for the settled part good.
func TestAttemptsAreNotItems(t *testing.T) {
	t.Parallel()
	var tr transcript.Transcript
	tr.Events([]core.Event{event(t, 1, core.KindUserMsg, core.MsgBody{Text: "hello"})})
	rev, settled := tr.Rev(), tr.SettledRev()
	tr.Events(calls(t, 2, 3))
	if len(tr.Items()) != 1 || tr.SettledRev() != settled {
		t.Errorf("items = %d, settled revision %d to %d; want one item and no change", len(tr.Items()), settled, tr.SettledRev())
	}
	// The status a view draws from them is stale all the same.
	if tr.Rev() == rev {
		t.Error("Rev did not change when a model call was read")
	}
}

type heldCase struct {
	name string
	// first is read before the slice is taken, and then after it.
	first, then []uint64
}

func heldCases() []heldCase {
	evens := span(2, 100, 2)
	return []heldCase{
		{name: "a newer call", first: []uint64{2, 4, 6}, then: []uint64{8}},
		{name: "an older call", first: []uint64{4, 6, 8}, then: []uint64{2}},
		{name: "a call between two held", first: []uint64{2, 4, 8}, then: []uint64{6, 5, 3}},
		{name: "a newer call once full", first: evens, then: []uint64{101, 102}},
		{name: "a call between two held once full", first: evens, then: []uint64{51, 3}},
		{name: "repeats and one too old once full", first: evens, then: []uint64{2, 50, 100, 1}},
	}
}

// A view keeps the slice Attempts returned while it works out what to draw.
// Reading more of the log must not change that slice under it.
func TestAttemptsDoesNotChangeASliceItReturned(t *testing.T) {
	t.Parallel()
	for _, tc := range heldCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var tr transcript.Transcript
			tr.Events(calls(t, tc.first...))
			held := tr.Attempts()
			was := slices.Clone(held)
			for _, seq := range tc.then {
				tr.Events(calls(t, seq))
			}
			if !slices.Equal(held, was) {
				t.Errorf("the slice returned earlier now starts at call %d, and held %d to %d before",
					held[0].Seq, was[0].Seq, was[len(was)-1].Seq)
			}
			// The transcript itself did take the calls in.
			if got := tr.Attempts(); !slices.IsSortedFunc(got, func(a, b transcript.Attempt) int {
				return cmp.Compare(a.Seq, b.Seq)
			}) || len(got) > 50 {
				t.Errorf("attempts afterwards: %d of them, or out of order", len(got))
			}
			if newest, ok := tr.Attempt(); !ok || newest.Input != int(slices.Max(append(slices.Clone(tc.first), tc.then...)))*10 {
				t.Errorf("Attempt = %+v, %t; want the newest of all read", newest, ok)
			}
		})
	}
}
