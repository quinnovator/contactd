package transcript_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/transcript"
)

// delegated is the log's record of a delegation: the call at seq and, at
// seq+1, the result naming the sub-agent it started.
func delegated(t *testing.T, seq uint64, child core.ContactID) []core.Event {
	t.Helper()
	id := fmt.Sprintf("d%d", seq)
	return []core.Event{
		event(t, seq, core.KindToolCall, core.ToolCall{ID: id, Name: "delegate", Args: json.RawMessage(`{"task":"count the files"}`)}),
		event(t, seq+1, core.KindToolResult, core.ToolResult{ID: id, Output: "Sub-agent started.", Child: child}),
	}
}

// settled is the log's record of a sub-agent finishing.
func settled(t *testing.T, seq uint64, child core.ContactID) core.Event {
	t.Helper()
	return event(t, seq, core.KindNotice, core.NoticeBody{Source: "subagent-settled", Text: "sub-agent finished", Child: child})
}

type waitingCase struct {
	name  string
	steps []step
	want  int
}

func waitingCases(t *testing.T) []waitingCase {
	t.Helper()
	both := func(a, b []core.Event) step { return page(append(a, b...)...) }
	return []waitingCase{
		{name: "an empty transcript"},
		{
			name: "a conversation with no delegation",
			steps: []step{page(
				event(t, 1, core.KindUserMsg, core.MsgBody{Text: "run it"}),
				event(t, 2, core.KindToolCall, core.ToolCall{ID: "c1", Name: "python", Code: "print(1)"}),
				event(t, 3, core.KindToolResult, core.ToolResult{ID: "c1", Output: "1\n"}),
				event(t, 4, core.KindAgentMsg, core.MsgBody{Text: "Done."}),
			)},
		},
		{
			name:  "one sub-agent started",
			steps: []step{page(delegated(t, 1, 7)...)},
			want:  1,
		},
		{
			name:  "started and settled",
			steps: []step{page(delegated(t, 1, 7)...), page(settled(t, 3, 7))},
		},
		{
			name:  "two started",
			steps: []step{both(delegated(t, 1, 7), delegated(t, 3, 8))},
			want:  2,
		},
		{
			name:  "two started, the first settled",
			steps: []step{both(delegated(t, 1, 7), delegated(t, 3, 8)), page(settled(t, 5, 7))},
			want:  1,
		},
		{
			name:  "two started, the second settled",
			steps: []step{both(delegated(t, 1, 7), delegated(t, 3, 8)), page(settled(t, 5, 8))},
			want:  1,
		},
		{
			name:  "two started, both settled",
			steps: []step{both(delegated(t, 1, 7), delegated(t, 3, 8)), page(settled(t, 5, 8), settled(t, 6, 7))},
		},
		{
			name:  "one settles and another starts",
			steps: []step{page(delegated(t, 1, 7)...), page(settled(t, 3, 7)), page(delegated(t, 4, 8)...)},
			want:  1,
		},
		{
			name:  "a notice about another sub-agent settles nothing",
			steps: []step{page(delegated(t, 1, 7)...), page(settled(t, 3, 9))},
			want:  1,
		},
		{
			name: "a notice that names no sub-agent settles nothing",
			steps: []step{
				page(delegated(t, 1, 7)...),
				page(event(t, 3, core.KindNotice, core.NoticeBody{Source: "reminder", Text: "still there?"})),
			},
			want: 1,
		},
		{
			// A parent that wakes asks again, so a sub-agent may report twice.
			name:  "one settled twice and another still running",
			steps: []step{both(delegated(t, 1, 7), delegated(t, 3, 8)), page(settled(t, 5, 7), settled(t, 6, 7))},
			want:  1,
		},
		{
			name: "a settlement on the stream and then in the log",
			steps: []step{
				both(delegated(t, 1, 7), delegated(t, 3, 8)),
				delta(core.Delta{Type: core.DeltaNotice, Text: "sub-agent finished", Seq: 5}),
				page(settled(t, 5, 8)),
				page(settled(t, 5, 8)),
			},
			want: 1,
		},
		{
			name: "a delegation that started nothing",
			steps: []step{page(
				event(t, 1, core.KindToolCall, core.ToolCall{ID: "d1", Name: "delegate", Args: json.RawMessage(`{}`)}),
				event(t, 2, core.KindToolResult, core.ToolResult{ID: "d1", Output: "too many active sub-agents", IsError: true}),
			)},
		},
		{
			name:  "the log read twice counts each once",
			steps: []step{page(delegated(t, 1, 7)...), page(delegated(t, 1, 7)...)},
			want:  1,
		},
		{
			name:  "the settlement read before the page that started it",
			steps: []step{page(settled(t, 3, 7)), page(delegated(t, 1, 7)...)},
		},
		{
			// The stream does not say which sub-agent a result started.
			name: "a delegation known from the stream alone",
			steps: []step{
				delta(core.Delta{Type: core.DeltaToolCall, Tool: "delegate", Text: `{"task":"count the files"}`, Seq: 1}),
				delta(core.Delta{Type: core.DeltaToolResult, Tool: "delegate", Text: "Sub-agent started.", Seq: 2}),
			},
		},
		{
			name: "the log's account of a delegation the stream announced",
			steps: []step{
				delta(core.Delta{Type: core.DeltaToolCall, Tool: "delegate", Text: `{"task":"count the files"}`, Seq: 1}),
				delta(core.Delta{Type: core.DeltaToolResult, Tool: "delegate", Text: "Sub-agent started.", Seq: 2}),
				page(delegated(t, 1, 7)...),
			},
			want: 1,
		},
		{
			// Nor which sub-agent a notice is about.
			name: "a settlement known from the stream alone",
			steps: []step{
				page(delegated(t, 1, 7)...),
				delta(core.Delta{Type: core.DeltaNotice, Text: "sub-agent finished", Seq: 3}),
			},
			want: 1,
		},
		{
			name: "the log's account of a settlement the stream announced",
			steps: []step{
				page(delegated(t, 1, 7)...),
				delta(core.Delta{Type: core.DeltaNotice, Text: "sub-agent finished", Seq: 3}),
				page(settled(t, 3, 7)),
			},
		},
		{
			name: "a failed turn in between changes nothing",
			steps: []step{
				page(delegated(t, 1, 7)...),
				delta(core.Delta{Type: core.DeltaError, Text: "provider_unavailable"}),
				page(delegated(t, 3, 8)...),
				page(settled(t, 5, 7)),
			},
			want: 1,
		},
	}
}

// Waiting is the number of sub-agents started and not yet settled, as the
// items say: a delegation's result names the child, and so does the notice
// that settles it.
func TestWaiting(t *testing.T) {
	t.Parallel()
	for _, tc := range waitingCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var tr transcript.Transcript
			feed(&tr, tc.steps)
			rev, settledRev := tr.Rev(), tr.SettledRev()
			if got := tr.Waiting(); got != tc.want {
				t.Errorf("Waiting = %d, want %d\n%s", got, tc.want, show(&tr))
			}
			// Asking is not a change: a caller keeps the answer by revision.
			if tr.Rev() != rev || tr.SettledRev() != settledRev {
				t.Error("Waiting changed the transcript's revision")
			}
		})
	}
}

// The count moves only when the settled revision does, which is what lets a
// caller keep it between changes.
func TestWaitingFollowsTheSettledRevision(t *testing.T) {
	t.Parallel()
	var tr transcript.Transcript
	last, lastRev := tr.Waiting(), tr.SettledRev()
	seen := []int{last}
	for _, s := range []step{
		page(delegated(t, 1, 7)...),
		delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
		delta(core.Delta{Type: core.DeltaText, Text: "Working on it."}),
		page(delegated(t, 3, 8)...),
		page(delegated(t, 3, 8)...),
		delta(core.Delta{Type: core.DeltaTyping}),
		page(settled(t, 5, 7)),
		page(settled(t, 6, 8)),
	} {
		s(&tr)
		got := tr.Waiting()
		if got != last && tr.SettledRev() == lastRev {
			t.Errorf("Waiting went from %d to %d with the settled revision still %d", last, got, lastRev)
		}
		if got != last {
			seen = append(seen, got)
		}
		last, lastRev = got, tr.SettledRev()
	}
	if want := []int{0, 1, 2, 1, 0}; fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Errorf("Waiting over time = %v, want %v", seen, want)
	}
}
