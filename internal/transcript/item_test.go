package transcript_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/transcript"
)

// find returns the item with a sequence number, or the failure that follows
// the event with that number.
func find(tr *transcript.Transcript, seq uint64, failure bool) (transcript.Item, bool) {
	for _, it := range tr.Items() {
		if failure && it.Kind == transcript.Failure && it.After == seq {
			return it, true
		}
		if !failure && it.Kind != transcript.Failure && it.Seq == seq {
			return it, true
		}
	}
	return transcript.Item{}, false
}

type revCase struct {
	name string
	// before is what the transcript holds, and then what happens next.
	before, then []step
	// seq and failure name the item to watch.
	seq     uint64
	failure bool
	// changes says whether then puts or replaces the item.
	changes bool
	// wantTS is the item's time afterwards, which tells the log's account
	// from the stream's.
	wantTS int64
	// wantText is the item's text afterwards.
	wantText string
}

func revCases(t *testing.T) []revCase {
	t.Helper()
	logResult := event(t, 3, core.KindToolResult, core.ToolResult{ID: "c1", Output: "boom", IsError: true})
	streamResult := core.Delta{Type: core.DeltaToolResult, Tool: "python", Text: "boom", Seq: 3}
	hello := core.Delta{Type: core.DeltaUser, Text: "hello", Seq: 1}
	return []revCase{
		{
			name:    "a new item from the stream",
			then:    []step{delta(streamResult)},
			seq:     3,
			changes: true, wantTS: streamNow, wantText: "boom",
		},
		{
			name:    "a new item from the log",
			then:    []step{page(logResult)},
			seq:     3,
			changes: true, wantTS: 1003, wantText: "boom",
		},
		{
			name:    "the log's account replaces the stream's",
			before:  []step{delta(streamResult)},
			then:    []step{page(logResult)},
			seq:     3,
			changes: true, wantTS: 1003, wantText: "boom",
		},
		{
			name:   "the log's account a second time",
			before: []step{page(logResult)},
			then:   []step{page(logResult)},
			seq:    3,
			wantTS: 1003, wantText: "boom",
		},
		{
			name:   "the stream's account after the log's",
			before: []step{page(logResult)},
			then:   []step{delta(streamResult)},
			seq:    3,
			wantTS: 1003, wantText: "boom",
		},
		{
			name:   "the stream's account a second time",
			before: []step{delta(streamResult)},
			then:   []step{delta(streamResult)},
			seq:    3,
			wantTS: streamNow, wantText: "boom",
		},
		{
			name:   "an acknowledgement after the echo",
			before: []step{sent("hello"), delta(hello)},
			then:   []step{acked("hello", 1)},
			seq:    1,
			wantTS: streamNow, wantText: "hello",
		},
		{
			name:    "an acknowledgement before the echo",
			before:  []step{sent("hello")},
			then:    []step{acked("hello", 1)},
			seq:     1,
			changes: true, wantTS: streamNow, wantText: "hello",
		},
		{
			name:   "the echo after an acknowledgement",
			before: []step{sent("hello"), acked("hello", 1)},
			then:   []step{delta(hello)},
			seq:    1,
			wantTS: streamNow, wantText: "hello",
		},
		{
			name:    "the log's account of an acknowledged message",
			before:  []step{sent("hello"), acked("hello", 1)},
			then:    []step{page(event(t, 1, core.KindUserMsg, core.MsgBody{Text: "hello"}))},
			seq:     1,
			changes: true, wantTS: 1001, wantText: "hello",
		},
		{
			name:   "messages given up as lost",
			before: []step{delta(hello), sent("more")},
			then:   []step{func(tr *transcript.Transcript) { tr.DropPending() }},
			seq:    1,
			wantTS: streamNow, wantText: "hello",
		},
		{
			name:   "the turn ending",
			before: []step{delta(hello), delta(core.Delta{Type: core.DeltaTyping, Typing: true})},
			then:   []step{delta(core.Delta{Type: core.DeltaReady, Seq: 1}), delta(core.Delta{Type: core.DeltaTyping})},
			seq:    1,
			wantTS: streamNow, wantText: "hello",
		},
		{
			name:    "an echo of a message sent from here",
			before:  []step{sent("hello")},
			then:    []step{delta(hello)},
			seq:     1,
			changes: true, wantTS: streamNow, wantText: "hello",
		},
		{
			name:   "another item arriving after it",
			before: []step{delta(hello)},
			then:   []step{delta(core.Delta{Type: core.DeltaMessage, Text: "Hi.", Seq: 2})},
			seq:    1,
			wantTS: streamNow, wantText: "hello",
		},
		{
			name:   "an older page arriving before it",
			before: []step{delta(streamResult)},
			then:   []step{page(event(t, 1, core.KindUserMsg, core.MsgBody{Text: "run it"}))},
			seq:    3,
			wantTS: streamNow, wantText: "boom",
		},
		{
			name:   "a message sent, refused and a reply streaming",
			before: []step{delta(hello)},
			then: []step{
				sent("more"),
				func(tr *transcript.Transcript) { tr.Refused("more") },
				delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
				delta(core.Delta{Type: core.DeltaText, Text: "Well"}),
			},
			seq:    1,
			wantTS: streamNow, wantText: "hello",
		},
		{
			name:   "the record of a model call",
			before: []step{delta(hello)},
			then:   []step{page(event(t, 2, core.KindAttempt, core.AttemptBody{Route: "fake/fake-1"}))},
			seq:    1,
			wantTS: streamNow, wantText: "hello",
		},
		{
			name:    "a failure",
			before:  []step{delta(hello)},
			then:    []step{delta(core.Delta{Type: core.DeltaError, Text: "provider_unavailable"})},
			seq:     1,
			failure: true,
			changes: true, wantTS: streamNow, wantText: "provider_unavailable",
		},
		{
			name:    "a newer failure after the same event",
			before:  []step{delta(hello), delta(core.Delta{Type: core.DeltaError, Text: "provider_unavailable"})},
			then:    []step{delta(core.Delta{Type: core.DeltaError, Text: "rate_limited"})},
			seq:     1,
			failure: true,
			changes: true, wantTS: streamNow, wantText: "rate_limited",
		},
		{
			name:    "the event a failure follows read from the log",
			before:  []step{delta(hello), delta(core.Delta{Type: core.DeltaError, Text: "provider_unavailable"})},
			then:    []step{page(event(t, 1, core.KindUserMsg, core.MsgBody{Text: "hello"}))},
			seq:     1,
			failure: true,
			wantTS:  streamNow, wantText: "provider_unavailable",
		},
	}
}

// An item's Rev is the settled revision that put it or last replaced it. It
// moves when the item is put and when the log's account takes the place of
// the stream's, and at no other time.
func TestItemRev(t *testing.T) {
	t.Parallel()
	for _, tc := range revCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var tr transcript.Transcript
			feed(&tr, tc.before)
			old, had := find(&tr, tc.seq, tc.failure)
			settled := tr.SettledRev()
			feed(&tr, tc.then)

			got, ok := find(&tr, tc.seq, tc.failure)
			if !ok {
				t.Fatalf("the item is not in the transcript:\n%s", show(&tr))
			}
			if got.TS != tc.wantTS || got.Text != tc.wantText {
				t.Errorf("item = %q at %d, want %q at %d", got.Text, got.TS, tc.wantText, tc.wantTS)
			}
			if !tc.changes {
				if !had {
					t.Fatal("the item was not there to begin with")
				}
				if got.Rev != old.Rev {
					t.Errorf("Rev went from %d to %d, want it left alone", old.Rev, got.Rev)
				}
				return
			}
			// The revision is one this step brought about: newer than
			// anything before it, and no newer than the transcript's.
			if got.Rev <= settled || got.Rev > tr.SettledRev() {
				t.Errorf("Rev = %d, want one after %d and up to %d", got.Rev, settled, tr.SettledRev())
			}
			if had && got.Rev <= old.Rev {
				t.Errorf("Rev went from %d to %d, want it to grow", old.Rev, got.Rev)
			}
		})
	}
}

// With nothing else going on, the item just put carries exactly the
// transcript's settled revision, and every item carries its own.
func TestItemRevIsTheSettledRevision(t *testing.T) {
	t.Parallel()
	var tr transcript.Transcript
	seen := make(map[uint64]uint64)
	for _, s := range []struct {
		seq  uint64
		step step
	}{
		{2, delta(core.Delta{Type: core.DeltaToolCall, Tool: "python", Text: "1/0", Seq: 2})},
		{3, delta(core.Delta{Type: core.DeltaToolResult, Tool: "python", Text: "boom", Seq: 3})},
		{1, page(event(t, 1, core.KindUserMsg, core.MsgBody{Text: "run it"}))},
		{3, page(event(t, 3, core.KindToolResult, core.ToolResult{ID: "c1", Output: "boom", IsError: true}))},
		{5, delta(core.Delta{Type: core.DeltaMessage, Text: "It failed.", Seq: 5})},
	} {
		s.step(&tr)
		it, ok := find(&tr, s.seq, false)
		if !ok {
			t.Fatalf("item %d is missing", s.seq)
		}
		if it.Rev != tr.SettledRev() {
			t.Errorf("item %d: Rev = %d, want the settled revision %d", s.seq, it.Rev, tr.SettledRev())
		}
	}
	for _, it := range tr.Items() {
		if it.Rev == 0 {
			t.Errorf("item %d has no Rev", it.Seq)
		}
		if other, dup := seen[it.Rev]; dup {
			t.Errorf("items %d and %d share Rev %d", other, it.Seq, it.Rev)
		}
		seen[it.Rev] = it.Seq
	}
}

// orders calls visit with every order of the numbers below n. The slice is
// reused: visit must not keep it.
func orders(n int, visit func(order []int)) {
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	var from func(k int)
	from = func(k int) {
		if k == n {
			visit(order)
			return
		}
		for i := k; i < n; i++ {
			order[k], order[i] = order[i], order[k]
			from(k + 1)
			order[k], order[i] = order[i], order[k]
		}
	}
	from(0)
}

// blockKey names an item the way a view does: by sequence number, or for a
// failure by the event it follows.
func blockKey(it transcript.Item) string {
	if it.Kind == transcript.Failure {
		return fmt.Sprintf("failure after %d", it.After)
	}
	return fmt.Sprintf("item %d", it.Seq)
}

// oneTurn is a turn as the stream tells it and as the log does, with a
// failure and an acknowledgement, cut into steps that may come in any order.
// The log is in two pages that part a tool call from its result.
func oneTurn(t *testing.T) []step {
	t.Helper()
	return []step{
		delta(core.Delta{Type: core.DeltaUser, Text: "run it", Seq: 1}),
		delta(core.Delta{Type: core.DeltaToolCall, Tool: "python", Text: "1/0", Seq: 2}),
		delta(core.Delta{Type: core.DeltaToolResult, Tool: "python", Text: "boom", Seq: 3}),
		delta(core.Delta{Type: core.DeltaError, Text: "provider_unavailable"}),
		page(
			event(t, 1, core.KindUserMsg, core.MsgBody{Text: "run it"}),
			event(t, 2, core.KindToolCall, core.ToolCall{ID: "c1", Name: "python", Code: "1/0"}),
		),
		page(
			event(t, 3, core.KindToolResult, core.ToolResult{ID: "c1", Output: "boom", IsError: true}),
			event(t, 4, core.KindAttempt, core.AttemptBody{Route: "fake/fake-1"}),
		),
		acked("run it", 1),
	}
}

// checkRevs compares the items with what was last seen of them, and records
// what it sees. before is the settled revision before the step just taken.
// It returns the first thing wrong, or "".
func checkRevs(tr *transcript.Transcript, seen map[string]transcript.Item, before uint64) string {
	revs := make(map[uint64]string, len(tr.Items()))
	for _, it := range tr.Items() {
		key := blockKey(it)
		if it.Rev == 0 || it.Rev > tr.SettledRev() {
			return fmt.Sprintf("%s has Rev %d with the settled revision at %d", key, it.Rev, tr.SettledRev())
		}
		if other, dup := revs[it.Rev]; dup {
			return fmt.Sprintf("%s and %s share Rev %d", other, key, it.Rev)
		}
		revs[it.Rev] = key

		was, had := seen[key]
		seen[key] = it
		switch {
		case had && was == it:
			// Left alone.
		case had && was.Rev == it.Rev:
			return fmt.Sprintf("%s went from %+v to %+v and kept its Rev", key, was, it)
		case it.Rev <= before:
			return fmt.Sprintf("%s was put or replaced with Rev %d, which is not after %d", key, it.Rev, before)
		case had && it.Kind != transcript.Failure:
			// Only a different account of the item may take its place.
			was.Rev = it.Rev
			if was == it {
				return key + " got a new Rev and nothing else about it changed"
			}
		}
	}
	return ""
}

// The same turn told in every order, each part twice. Whatever the order, an
// item's Rev moves when the item is put or changes, to a revision of its
// own, and at no other time; and the log's account is what is left.
func TestItemRevInEveryOrder(t *testing.T) {
	t.Parallel()
	steps := oneTurn(t)
	tried := 0
	orders(len(steps), func(order []int) {
		if t.Failed() {
			return
		}
		tried++
		var tr transcript.Transcript
		seen := make(map[string]transcript.Item)
		for range 2 {
			for _, i := range order {
				before := tr.SettledRev()
				steps[i](&tr)
				if wrong := checkRevs(&tr, seen, before); wrong != "" {
					t.Errorf("order %v, after step %d: %s", order, i, wrong)
					return
				}
			}
		}
		var told []string
		for _, it := range tr.Items() {
			if it.Kind != transcript.Failure {
				told = append(told, fmt.Sprintf("%d %s[%s] %q error=%t at %d", it.Seq, kindName(it.Kind), it.Tool, it.Text, it.IsError, it.TS))
			}
		}
		want := []string{
			`1 user[] "run it" error=false at 1001`,
			`2 call[python] "1/0" error=false at 1002`,
			`3 result[python] "boom" error=true at 1003`,
		}
		if !slices.Equal(told, want) {
			t.Errorf("order %v: items = %q\nwant %q", order, told, want)
		}
	})
	if tried != 5040 {
		t.Errorf("tried %d orders, want every one of 5040", tried)
	}
}

// anchors lists the items in order, each failure with the event it follows.
func anchors(tr *transcript.Transcript) []string {
	out := make([]string, 0, len(tr.Items()))
	for _, it := range tr.Items() {
		if it.Kind == transcript.Failure {
			out = append(out, fmt.Sprintf("failure after %d: %s", it.After, it.Text))
			continue
		}
		line := fmt.Sprintf("%d %s", it.Seq, kindName(it.Kind))
		if it.After != 0 {
			line += fmt.Sprintf(" after %d", it.After)
		}
		out = append(out, line)
	}
	return out
}

type afterCase struct {
	name  string
	steps []step
	want  []string
}

func afterCases(t *testing.T) []afterCase {
	t.Helper()
	fail := func(reason string) step {
		return delta(core.Delta{Type: core.DeltaError, Text: reason})
	}
	user := func(seq uint64) step {
		return delta(core.Delta{Type: core.DeltaUser, Text: "hello", Seq: seq})
	}
	return []afterCase{
		{
			name:  "before any event",
			steps: []step{fail("no_route")},
			want:  []string{"failure after 0: no_route"},
		},
		{
			name:  "after the message it could not answer",
			steps: []step{user(1), fail("provider_unavailable")},
			want:  []string{"1 user", "failure after 1: provider_unavailable"},
		},
		{
			name: "after the newest of several items",
			steps: []step{
				user(1),
				delta(core.Delta{Type: core.DeltaToolCall, Tool: "python", Text: "1/0", Seq: 2}),
				delta(core.Delta{Type: core.DeltaToolResult, Tool: "python", Text: "boom", Seq: 3}),
				fail("context_too_long"),
			},
			want: []string{"1 user", "2 call", "3 result", "failure after 3: context_too_long"},
		},
		{
			name:  "one after each of two events",
			steps: []step{user(1), fail("first"), user(3), fail("second")},
			want:  []string{"1 user", "failure after 1: first", "3 user", "failure after 3: second"},
		},
		{
			name:  "a second after the same event takes the place of the first",
			steps: []step{user(1), fail("first"), fail("second")},
			want:  []string{"1 user", "failure after 1: second"},
		},
		{
			name: "history read later does not move it",
			steps: []step{
				user(3),
				fail("provider_unavailable"),
				page(
					event(t, 1, core.KindUserMsg, core.MsgBody{Text: "earlier"}),
					event(t, 2, core.KindAgentMsg, core.MsgBody{Text: "Yes?"}),
					event(t, 3, core.KindUserMsg, core.MsgBody{Text: "hello"}),
				),
			},
			want: []string{"1 user", "2 agent", "3 user", "failure after 3: provider_unavailable"},
		},
		{
			name: "what comes next lands after it",
			steps: []step{
				user(1),
				fail("provider_unavailable"),
				user(3),
				delta(core.Delta{Type: core.DeltaMessage, Text: "Hi.", Seq: 5}),
			},
			want: []string{"1 user", "failure after 1: provider_unavailable", "3 user", "5 agent"},
		},
		{
			name: "one before any event stays first",
			steps: []step{
				fail("no_route"),
				user(1),
				delta(core.Delta{Type: core.DeltaMessage, Text: "Hi.", Seq: 2}),
			},
			want: []string{"failure after 0: no_route", "1 user", "2 agent"},
		},
	}
}

// A failure has no sequence number of its own. After says which event it
// follows, there is one failure per event, and no other item sets After.
func TestFailureAfter(t *testing.T) {
	t.Parallel()
	for _, tc := range afterCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var tr transcript.Transcript
			feed(&tr, tc.steps)
			if got := anchors(&tr); !slices.Equal(got, tc.want) {
				t.Errorf("items = %q\nwant    %q", got, tc.want)
			}
			for _, it := range tr.Items() {
				if it.Kind == transcript.Failure && it.Seq != 0 {
					t.Errorf("a failure has sequence number %d", it.Seq)
				}
			}
		})
	}
}

type newestCase struct {
	name  string
	steps []step
	want  uint64
}

func newestCases(t *testing.T) []newestCase {
	t.Helper()
	user := func(seq uint64) step {
		return delta(core.Delta{Type: core.DeltaUser, Text: "hello", Seq: seq})
	}
	fail := delta(core.Delta{Type: core.DeltaError, Text: "provider_unavailable"})
	return []newestCase{
		{name: "an empty transcript"},
		{name: "one item", steps: []step{user(4)}, want: 4},
		{
			name:  "the stream running ahead",
			steps: []step{user(1), delta(core.Delta{Type: core.DeltaMessage, Text: "Hi.", Seq: 2}), user(9)},
			want:  9,
		},
		{
			name: "an older page read after a newer one",
			steps: []step{
				page(event(t, 8, core.KindAgentMsg, core.MsgBody{Text: "Hi."})),
				page(event(t, 2, core.KindUserMsg, core.MsgBody{Text: "hello"})),
			},
			want: 8,
		},
		{name: "a failure last", steps: []step{user(4), fail}, want: 4},
		{name: "a failure and nothing else", steps: []step{fail}},
		{
			name:  "a failure between two items",
			steps: []step{user(4), fail, user(6)},
			want:  6,
		},
		{
			name: "events that are not items",
			steps: []step{
				user(4),
				page(
					event(t, 5, core.KindAttempt, core.AttemptBody{Route: "fake/fake-1"}),
					event(t, 6, core.KindSummary, core.SummaryBody{Text: "bookkeeping"}),
				),
			},
			want: 4,
		},
		{
			name:  "an announcement with no sequence number",
			steps: []step{user(4), delta(core.Delta{Type: core.DeltaMessage, Text: "lost"})},
			want:  4,
		},
		{
			name:  "a message sent and not yet seen",
			steps: []step{user(4), sent("more")},
			want:  4,
		},
	}
}

func TestNewest(t *testing.T) {
	t.Parallel()
	for _, tc := range newestCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var tr transcript.Transcript
			feed(&tr, tc.steps)
			if got := tr.Newest(); got != tc.want {
				t.Errorf("Newest = %d, want %d\n%s", got, tc.want, show(&tr))
			}
		})
	}
}
