package transcript_test

import (
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/transcript"
)

type labelCase struct {
	name string
	// before is what the transcript holds, and then what happens next.
	before, then []step
	// wantTool is the tool the result at sequence number 3 names afterwards.
	wantTool string
	// changes says whether then changes the result.
	changes bool
}

func labelCases(t *testing.T) []labelCase {
	t.Helper()
	logCall := event(t, 2, core.KindToolCall, core.ToolCall{ID: "c1", Name: "python", Code: "print(1)"})
	logResult := event(t, 3, core.KindToolResult, core.ToolResult{ID: "c1", Output: "1\n"})
	streamResult := core.Delta{Type: core.DeltaToolResult, Tool: "python", Text: "1\n", Seq: 3}
	return []labelCase{
		{
			name:     "the call and then the result",
			before:   []step{page(logCall)},
			then:     []step{page(logResult)},
			wantTool: "python", changes: true,
		},
		{
			name:    "the result with its call on a page not yet read",
			then:    []step{page(logResult)},
			changes: true,
		},
		{
			name:     "the call read from an older page",
			before:   []step{page(logResult)},
			then:     []step{page(event(t, 1, core.KindUserMsg, core.MsgBody{Text: "run it"}), logCall)},
			wantTool: "python", changes: true,
		},
		{
			name:     "the call later in the same page",
			then:     []step{page(logResult, logCall)},
			wantTool: "python", changes: true,
		},
		{
			name:     "the call read a second time",
			before:   []step{page(logResult), page(logCall)},
			then:     []step{page(logCall), page(logResult), page(logCall)},
			wantTool: "python",
		},
		{
			name:   "another call read from an older page",
			before: []step{page(logResult)},
			then:   []step{page(event(t, 1, core.KindToolCall, core.ToolCall{ID: "c0", Name: "delegate"}))},
		},
		{
			// An ID used again is another call's: it cannot have made a
			// result that came before it.
			name:   "a later call with the same ID",
			before: []step{page(logResult)},
			then:   []step{page(event(t, 5, core.KindToolCall, core.ToolCall{ID: "c1", Name: "delegate"}))},
		},
		{
			name:     "the stream's word stands until the log knows better",
			before:   []step{delta(streamResult)},
			then:     []step{page(logResult)},
			wantTool: "python", changes: true,
		},
		{
			name:     "the call read after the stream named the tool",
			before:   []step{delta(streamResult), page(logResult)},
			then:     []step{page(logCall)},
			wantTool: "python",
		},
	}
}

// The log names a result's tool through its call. When the result is read
// first, as when the two fall either side of a page of history, the result
// is labelled once the call turns up, and that is a change to the item.
func TestResultIsLabelledWhenItsCallIsRead(t *testing.T) {
	t.Parallel()
	for _, tc := range labelCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var tr transcript.Transcript
			feed(&tr, tc.before)
			old, had := find(&tr, 3, false)
			settled := tr.SettledRev()
			feed(&tr, tc.then)

			got, ok := find(&tr, 3, false)
			if !ok {
				t.Fatalf("the result is not in the transcript:\n%s", show(&tr))
			}
			if got.Tool != tc.wantTool {
				t.Errorf("the result names %q, want %q\n%s", got.Tool, tc.wantTool, show(&tr))
			}
			if got.Kind != transcript.ToolResult || got.Text != "1\n" {
				t.Errorf("the result = %+v", got)
			}
			switch {
			case !tc.changes && !had:
				t.Fatal("the result was not there to begin with")
			case !tc.changes && got != old:
				t.Errorf("the result went from %+v to %+v, want it left alone", old, got)
			case tc.changes && (got.Rev <= settled || got.Rev > tr.SettledRev()):
				t.Errorf("Rev = %d, want one after %d and up to %d", got.Rev, settled, tr.SettledRev())
			}
		})
	}
}
