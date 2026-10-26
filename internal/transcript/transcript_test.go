package transcript_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/transcript"
)

func event(t *testing.T, seq uint64, kind core.EventKind, body any) core.Event {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return core.Event{Contact: 1, Seq: seq, TS: int64(1000 + seq), Kind: kind, Body: raw}
}

// show renders a transcript one item per line, for comparison.
func show(tr *transcript.Transcript) string {
	var b strings.Builder
	for _, it := range tr.Items() {
		kind := map[transcript.Kind]string{
			transcript.User: "user", transcript.Agent: "agent", transcript.ToolCall: "call",
			transcript.ToolResult: "result", transcript.Notice: "notice",
			transcript.Checkpoint: "checkpoint", transcript.Failure: "failure",
		}[it.Kind]
		fmt.Fprintf(&b, "%d %s", it.Seq, kind)
		if it.Tool != "" {
			fmt.Fprintf(&b, "[%s]", it.Tool)
		}
		fmt.Fprintf(&b, " %q", it.Text)
		if it.Lead != "" {
			fmt.Fprintf(&b, " lead=%q", it.Lead)
		}
		if it.Interrupted {
			b.WriteString(" interrupted")
		}
		if it.IsError {
			b.WriteString(" error")
		}
		if it.Child != 0 {
			fmt.Fprintf(&b, " child=%d", it.Child)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func want(t *testing.T, tr *transcript.Transcript, lines ...string) {
	t.Helper()
	if got, want := show(tr), strings.Join(lines, "\n")+"\n"; got != want {
		t.Errorf("transcript:\n%s\nwant:\n%s", got, want)
	}
}

// A whole turn as the stream tells it: the message, a tool call with what
// the agent said first, its result, and the reply.
func TestATurnFromTheStream(t *testing.T) {
	t.Parallel()
	var tr transcript.Transcript
	tr.Sent("what is 6*7?")
	if got := tr.Pending(); len(got) != 1 {
		t.Fatalf("pending = %q", got)
	}
	for _, d := range []core.Delta{
		{Type: core.DeltaReady},
		{Type: core.DeltaUser, Text: "what is 6*7?", Seq: 1},
		{Type: core.DeltaTyping, Typing: true, Turn: 1},
		{Type: core.DeltaText, Text: "Let me "},
		{Type: core.DeltaText, Text: "check."},
		{Type: core.DeltaToolStart, Tool: "python"},
	} {
		tr.Delta(d, 5000)
	}
	// Seen on the stream, so no longer shown as pending, though the
	// acknowledgement has not come yet.
	if got := tr.Pending(); len(got) != 0 {
		t.Errorf("pending after the echo = %q", got)
	}
	if live := tr.Live(); !live.Typing || live.Text != "Let me check." || live.Starting != "python" {
		t.Errorf("live = %+v", live)
	}

	tr.Acked("what is 6*7?", 1, 5000)
	tr.Delta(core.Delta{Type: core.DeltaToolCall, Tool: "python", Text: "print(6*7)", Seq: 2}, 5000)
	if live := tr.Live(); live.Text != "" || live.Running != "python" || live.Starting != "" {
		t.Errorf("live during the tool run = %+v", live)
	}
	tr.Delta(core.Delta{Type: core.DeltaToolResult, Tool: "python", Text: "42\n", Seq: 3}, 5000)
	tr.Delta(core.Delta{Type: core.DeltaText, Text: "It is 42."}, 5000)
	tr.Delta(core.Delta{Type: core.DeltaMessage, Text: "It is 42.", Seq: 5}, 5000)
	tr.Delta(core.Delta{Type: core.DeltaTyping}, 5000)

	want(t, &tr,
		`1 user "what is 6*7?"`,
		`2 call[python] "print(6*7)" lead="Let me check."`,
		`3 result[python] "42\n"`,
		`5 agent "It is 42."`,
	)
	if live := tr.Live(); live != (transcript.Live{}) {
		t.Errorf("live after the turn = %+v", live)
	}
}

// The log and the stream describe the same events. Whichever is applied
// first, and however often, the transcript comes out the same, and the
// log's fuller account of an item replaces the stream's.
func TestLogAndStreamAgree(t *testing.T) {
	t.Parallel()
	log := []core.Event{
		event(t, 1, core.KindUserMsg, core.MsgBody{Text: "run it"}),
		event(t, 2, core.KindToolCall, core.ToolCall{ID: "c1", Name: "python", Code: "1/0", Text: "Running."}),
		event(t, 3, core.KindToolResult, core.ToolResult{ID: "c1", Output: "ZeroDivisionError", IsError: true}),
		event(t, 4, core.KindAttempt, core.AttemptBody{Route: "fake/fake-1", Input: 100, Cached: 80}),
		event(t, 5, core.KindAgentMsg, core.MsgBody{Text: "It failed."}),
		event(t, 6, core.KindSummary, core.SummaryBody{Text: "bookkeeping"}),
	}
	stream := []core.Delta{
		{Type: core.DeltaUser, Text: "run it", Seq: 1},
		{Type: core.DeltaTyping, Typing: true},
		{Type: core.DeltaText, Text: "Running."},
		{Type: core.DeltaToolCall, Tool: "python", Text: "1/0", Seq: 2},
		{Type: core.DeltaToolResult, Tool: "python", Text: "ZeroDivisionError", Seq: 3},
		{Type: core.DeltaMessage, Text: "It failed.", Seq: 5},
		{Type: core.DeltaTyping},
	}
	lines := []string{
		`1 user "run it"`,
		`2 call[python] "1/0" lead="Running."`,
		`3 result[python] "ZeroDivisionError" error`,
		`5 agent "It failed."`,
	}

	t.Run("stream then log", func(t *testing.T) {
		t.Parallel()
		var tr transcript.Transcript
		for _, d := range stream {
			tr.Delta(d, 1)
		}
		tr.Events(log)
		tr.Events(log)
		want(t, &tr, lines...)
		if a, ok := tr.Attempt(); !ok || a.Cached != 80 {
			t.Errorf("attempt = %+v, %t", a, ok)
		}
	})
	t.Run("log then stream", func(t *testing.T) {
		t.Parallel()
		var tr transcript.Transcript
		tr.Events(log)
		for _, d := range stream {
			tr.Delta(d, 1)
		}
		want(t, &tr, lines...)
	})
	t.Run("an older page arrives after a newer one", func(t *testing.T) {
		t.Parallel()
		var tr transcript.Transcript
		tr.Events(log[4:])
		tr.Events(log[:4])
		want(t, &tr, lines...)
		if tr.Oldest() != 1 {
			t.Errorf("oldest = %d", tr.Oldest())
		}
	})
}

func TestInterruptedReplyKeepsWhatWasSaid(t *testing.T) {
	t.Parallel()
	var tr transcript.Transcript
	for _, d := range []core.Delta{
		{Type: core.DeltaUser, Text: "tell me everything", Seq: 1},
		{Type: core.DeltaTyping, Typing: true},
		{Type: core.DeltaText, Text: "It began on a Tuesday, when"},
		{Type: core.DeltaInterrupted, Seq: 2},
		{Type: core.DeltaTyping},
	} {
		tr.Delta(d, 1)
	}
	want(t, &tr,
		`1 user "tell me everything"`,
		`2 agent "It began on a Tuesday, when" interrupted`,
	)
}

// A turn that fails leaves no event behind, so the transcript keeps a line
// of its own for it, directly after what it followed.
func TestFailedTurnIsShownWhereItHappened(t *testing.T) {
	t.Parallel()
	var tr transcript.Transcript
	tr.Delta(core.Delta{Type: core.DeltaUser, Text: "hello", Seq: 1}, 1)
	tr.Delta(core.Delta{Type: core.DeltaTyping, Typing: true}, 1)
	tr.Delta(core.Delta{Type: core.DeltaError, Text: "provider_unavailable"}, 1)
	tr.Delta(core.Delta{Type: core.DeltaTyping}, 1)
	tr.Delta(core.Delta{Type: core.DeltaUser, Text: "hello again", Seq: 3}, 1)
	tr.Delta(core.Delta{Type: core.DeltaMessage, Text: "Hi.", Seq: 5}, 1)
	// History read later lands before the failure, not after it.
	tr.Events([]core.Event{event(t, 1, core.KindUserMsg, core.MsgBody{Text: "hello"})})
	want(t, &tr,
		`1 user "hello"`,
		`0 failure "provider_unavailable"`,
		`3 user "hello again"`,
		`5 agent "Hi."`,
	)
}

func TestDelegationAndSettlement(t *testing.T) {
	t.Parallel()
	var tr transcript.Transcript
	tr.Events([]core.Event{
		event(t, 1, core.KindToolCall, core.ToolCall{ID: "d1", Name: "delegate", Args: json.RawMessage(`{ "task": "count the files",  "mode": "spawn" }`)}),
		event(t, 2, core.KindToolResult, core.ToolResult{ID: "d1", Output: "started sub-agent 7", Child: 7}),
		event(t, 3, core.KindNotice, core.NoticeBody{Source: "subagent-settled", Text: "sub-agent 7 finished: 12 files", Child: 7}),
		event(t, 4, core.KindCheckpoint, core.CheckpointBody{From: 1, To: 2, Text: "They counted files."}),
	})
	want(t, &tr,
		`1 call[delegate] "{\"task\":\"count the files\",\"mode\":\"spawn\"}"`,
		`2 result[delegate] "started sub-agent 7" child=7`,
		`3 notice "sub-agent 7 finished: 12 files" child=7`,
		`4 checkpoint "They counted files."`,
	)
}

// What becomes of a message sent from here: acknowledged, refused, seen in
// the log after a reconnect, or lost.
func TestPendingMessages(t *testing.T) {
	t.Parallel()

	t.Run("acknowledged before any echo", func(t *testing.T) {
		t.Parallel()
		var tr transcript.Transcript
		tr.Sent("one")
		tr.Acked("one", 1, 9)
		tr.Delta(core.Delta{Type: core.DeltaUser, Text: "one", Seq: 1}, 9)
		want(t, &tr, `1 user "one"`)
		if len(tr.Pending()) != 0 {
			t.Errorf("pending = %q", tr.Pending())
		}
	})

	t.Run("the same text twice is two messages", func(t *testing.T) {
		t.Parallel()
		var tr transcript.Transcript
		tr.Sent("ok")
		tr.Sent("ok")
		tr.Delta(core.Delta{Type: core.DeltaUser, Text: "ok", Seq: 1}, 9)
		if got := tr.Pending(); len(got) != 1 {
			t.Fatalf("pending after one echo = %q, want one left", got)
		}
		tr.Acked("ok", 1, 9)
		tr.Acked("ok", 2, 9)
		want(t, &tr, `1 user "ok"`, `2 user "ok"`)
		if len(tr.Pending()) != 0 {
			t.Errorf("pending = %q", tr.Pending())
		}
	})

	t.Run("refused", func(t *testing.T) {
		t.Parallel()
		var tr transcript.Transcript
		tr.Sent("too much")
		tr.Refused("too much")
		if len(tr.Pending()) != 0 || len(tr.Items()) != 0 {
			t.Errorf("pending = %q, items = %d", tr.Pending(), len(tr.Items()))
		}
	})

	t.Run("after a reconnect one arrived and one was lost", func(t *testing.T) {
		t.Parallel()
		var tr transcript.Transcript
		tr.Sent("made it")
		tr.Sent("did not")
		// The connection dropped before either acknowledgement. The log,
		// read on reconnect, has the first.
		tr.Events([]core.Event{event(t, 1, core.KindUserMsg, core.MsgBody{Text: "made it"})})
		if lost := tr.DropPending(); len(lost) != 1 || lost[0] != "did not" {
			t.Errorf("lost = %q, want the second message only", lost)
		}
		want(t, &tr, `1 user "made it"`)
		if len(tr.Pending()) != 0 {
			t.Errorf("pending = %q", tr.Pending())
		}
	})

	t.Run("a message someone else sent resolves nothing", func(t *testing.T) {
		t.Parallel()
		var tr transcript.Transcript
		tr.Sent("mine")
		tr.Delta(core.Delta{Type: core.DeltaUser, Text: "theirs", Seq: 1}, 9)
		if got := tr.Pending(); len(got) != 1 || got[0] != "mine" {
			t.Errorf("pending = %q", got)
		}
	})
}

// A client that opens a contact mid-reply is sent what has been said so far
// as one text delta, and carries on from there.
func TestJoiningMidReply(t *testing.T) {
	t.Parallel()
	var tr transcript.Transcript
	tr.Events([]core.Event{event(t, 1, core.KindUserMsg, core.MsgBody{Text: "go on"})})
	for _, d := range []core.Delta{
		{Type: core.DeltaReady, Seq: 1},
		{Type: core.DeltaTyping, Typing: true},
		{Type: core.DeltaText, Text: "So far, "},
		{Type: core.DeltaText, Text: "so good."},
	} {
		tr.Delta(d, 1)
	}
	if got := tr.Live().Text; got != "So far, so good." {
		t.Errorf("live text = %q", got)
	}
	rev := tr.Rev()
	tr.Delta(core.Delta{Type: core.DeltaMessage, Text: "So far, so good.", Seq: 3}, 1)
	if tr.Rev() == rev {
		t.Error("Rev did not change with the transcript")
	}
	want(t, &tr, `1 user "go on"`, `3 agent "So far, so good."`)
}

// A live check notice is hidden or shown by the rule the log uses.
func TestLiveCheckNoticesFollowShowChecks(t *testing.T) {
	t.Parallel()
	check := core.Delta{Type: core.DeltaNotice, Text: "Scheduled check 2 of 61.", Seq: 4, Source: "helper-check"}
	report := core.Delta{Type: core.DeltaNotice, Text: "Helper 1 reports: API degraded", Seq: 5, Source: "helper-update", Helper: &core.HelperDelta{Handle: 1, Event: "notice"}}
	for _, show := range []bool{false, true} {
		var tr transcript.Transcript
		tr.ShowChecks = show
		tr.Delta(check, 1)
		tr.Delta(report, 1)
		got := make([]string, 0, len(tr.Items()))
		for _, it := range tr.Items() {
			got = append(got, it.Source)
		}
		want := "helper-update"
		if show {
			want = "helper-check,helper-update"
		}
		if strings.Join(got, ",") != want {
			t.Fatalf("show checks %v: %v", show, got)
		}
	}
}
