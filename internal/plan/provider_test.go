package plan

import (
	"bytes"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
)

func (b *logBuilder) notice(text string) *logBuilder {
	return b.add(core.KindNotice, core.NoticeBody{Source: "subagent-settled", Text: text})
}

func (b *logBuilder) checkpoint(from, to uint64, text string) *logBuilder {
	return b.add(core.KindCheckpoint, core.CheckpointBody{From: from, To: to, Text: text})
}

func (b *logBuilder) attempt() *logBuilder {
	return b.add(core.KindAttempt, core.AttemptBody{Route: "p/m"})
}

func texts(msgs []core.Message) []string {
	out := make([]string, len(msgs))
	for i := range msgs {
		out[i] = msgs[i].Text
	}
	return out
}

func TestReasoningIsRenderedOnlyForItsRoute(t *testing.T) {
	t.Parallel()
	payload := &core.Reasoning{Route: "deepseek/deepseek-flash", Data: []byte("thought")}
	log := new(logBuilder).user("run it").
		add(core.KindToolCall, core.ToolCall{ID: "c1", Name: "python", Code: "1", Reasoning: payload}).
		result("c1", "1").
		add(core.KindAgentMsg, core.MsgBody{Text: "It is 1.", Reasoning: payload}).
		user("thanks")

	build := func(route string) *PromptPlan {
		return mustBuild(t, nil, Input{Agent: testAgent(), Route: route, Tail: log.events}, nil, t0, DefaultParams())
	}
	own, other, none := build("deepseek/deepseek-flash"), build("openrouter/x"), build("")
	count := func(p *PromptPlan) int {
		n := 0
		for _, m := range p.Tiers[TierTail].Msgs {
			if m.Reasoning != nil {
				n++
			}
		}
		return n
	}
	if got := count(own); got != 2 {
		t.Errorf("own route: %d messages carry reasoning, want 2 (the call and the reply)", got)
	}
	if count(other) != 0 || count(none) != 0 {
		t.Errorf("reasoning rendered for another route (%d) or for no route (%d)", count(other), count(none))
	}
	// The payload is part of what is sent, so it must move the hash chain:
	// otherwise the cache prediction would call two different prompts equal.
	if own.Chain[TierTail] == other.Chain[TierTail] {
		t.Error("a plan with reasoning hashes the same as one without")
	}
	if other.Chain[TierTail] != none.Chain[TierTail] {
		t.Error("dropping a foreign payload should leave the same bytes as having none")
	}
	if own.Tiers[TierTail].Tokens <= other.Tiers[TierTail].Tokens {
		t.Error("stored reasoning should count toward the tail's size")
	}
}

func TestSiblingCallsAndRawArgumentsAreCanonical(t *testing.T) {
	t.Parallel()
	base := core.Message{Role: core.RoleAssistant, ToolCall: &core.ToolCall{ID: "c", Name: "python", Code: "1"}}
	sibling := core.Message{Role: core.RoleAssistant, ToolCall: &core.ToolCall{ID: "c", Name: "python", Code: "1", Sibling: 1}}
	args := core.Message{Role: core.RoleAssistant, ToolCall: &core.ToolCall{ID: "c", Name: "delegate", Args: []byte(`{"task":"x"}`)}}
	if bytes.Equal(Canonical(&base), Canonical(&sibling)) {
		t.Error("a sibling call is rendered differently on the wire and must hash differently")
	}
	if want := `{"role":"assistant","tool_call":{"args":{"task":"x"},"code":"","id":"c","name":"delegate"}}`; string(Canonical(&args)) != want {
		t.Errorf("canonical = %s\nwant        %s", Canonical(&args), want)
	}
	plain := []core.ToolDef{{Name: "t", Description: "d"}}
	schema := []core.ToolDef{{Name: "t", Description: "d", Input: []byte(`{"type":"object"}`)}}
	if bytes.Equal(CanonicalTools(plain), CanonicalTools(schema)) {
		t.Error("a tool's input schema is part of the static tier and must be hashed")
	}
}

func TestNoticesAreSomethingToAnswer(t *testing.T) {
	t.Parallel()
	log := new(logBuilder).user("start a job").agent("Started.").notice("Sub-agent 9 finished.")
	p := mustBuild(t, nil, Input{Agent: testAgent(), Tail: log.events}, nil, t0, DefaultParams())
	live := p.Tiers[TierLive].Msgs
	if len(live) != 2 || live[0].Role != core.RoleUser ||
		live[0].Text != "<notice source=\"subagent-settled\">\nSub-agent 9 finished.\n</notice>" {
		t.Fatalf("live tier = %q, want the notice then the time note", texts(live))
	}
	lay, err := Layout(nil, log.events, "", DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	if len(lay.Texts) != 1 || !strings.Contains(lay.Texts[0], "Sub-agent 9") {
		t.Errorf("pending texts = %q", lay.Texts)
	}
}

// checkpointLog is a five-turn conversation with attempt records between
// the messages, as a real log has.
func checkpointLog() *logBuilder {
	b := new(logBuilder)
	for _, turn := range []string{"one", "two", "three", "four", "five"} {
		b.user("question " + turn).attempt().agent("answer " + turn)
	}
	return b // seqs: user 1,4,7,10,13; agent 3,6,9,12,15
}

func TestCheckpointReplacesItsSpan(t *testing.T) {
	t.Parallel()
	pr := DefaultParams()
	log := checkpointLog().user("question six")
	before := mustBuild(t, nil, Input{Agent: testAgent(), Tail: log.events}, nil, t0, pr)

	// Fold the first two turns (seqs 1..6), then carry on.
	log.checkpoint(1, 6, "They discussed one and two.")
	ckSeq := log.events[len(log.events)-1].Seq
	log.agent("answer six").user("question seven")
	after := mustBuild(t, before, Input{Agent: testAgent(), Tail: log.events}, nil, t0+1000, pr)

	tail := texts(after.Tiers[TierTail].Msgs)
	want := []string{
		checkpointLead + "They discussed one and two.",
		"question three", "answer three", "question four", "answer four",
		"question five", "answer five", "question six", "answer six",
	}
	if strings.Join(tail, "|") != strings.Join(want, "|") {
		t.Fatalf("tail = %q\nwant   %q", tail, want)
	}
	if after.Tiers[TierTail].Msgs[0].Role != core.RoleUser {
		t.Error("the summary must sit in a position every provider accepts mid-history")
	}
	if after.Checkpoint != ckSeq || after.TailFrom != 7 {
		t.Errorf("Checkpoint = %d, TailFrom = %d; want %d and 7", after.Checkpoint, after.TailFrom, ckSeq)
	}
	if live := texts(after.Tiers[TierLive].Msgs); len(live) != 2 || live[0] != "question seven" {
		t.Errorf("live = %q", live)
	}
	// The rewrite is a cache miss from the tail on, and the plan says so.
	if got := after.FirstChanged(before); got != int(TierTail) {
		t.Errorf("FirstChanged = %d, want the tail tier", got)
	}

	// The turn after that extends the checkpointed tail without moving it.
	log.agent("answer seven").user("question eight")
	next := mustBuild(t, after, Input{Agent: testAgent(), Tail: log.events}, nil, t0+2000, pr)
	if next.Checkpoint != ckSeq || next.Tiers[TierTail].Msgs[0].Text != want[0] {
		t.Error("the checkpoint did not persist into the next plan")
	}
	if got := next.FirstChanged(after); got != int(TierLive) {
		t.Errorf("FirstChanged = %d after an append-only turn, want the live tier", got)
	}

	// A fresh process sees the same thing from the log alone.
	hydrated := mustBuild(t, nil, Input{Agent: testAgent(), Tail: log.events}, nil, t0+2000, pr)
	if got := texts(hydrated.Tiers[TierTail].Msgs); strings.Join(got, "|") != strings.Join(texts(next.Tiers[TierTail].Msgs), "|") {
		t.Errorf("hydrated tail = %q\nlive tail     = %q", got, texts(next.Tiers[TierTail].Msgs))
	}
}

func TestANewerCheckpointAbsorbsTheOlderOne(t *testing.T) {
	t.Parallel()
	pr := DefaultParams()
	log := checkpointLog().checkpoint(1, 6, "first summary").user("question six")
	p1 := mustBuild(t, nil, Input{Agent: testAgent(), Tail: log.events}, nil, t0, pr)
	log.checkpoint(1, 12, "second summary").agent("answer six").user("question seven")
	p2 := mustBuild(t, p1, Input{Agent: testAgent(), Tail: log.events}, nil, t0, pr)
	got := texts(p2.Tiers[TierTail].Msgs)
	if len(got) != 5 || got[0] != checkpointLead+"second summary" || got[1] != "question five" {
		t.Errorf("tail = %q, want the second summary then turn five onward", got)
	}
}

func TestACheckpointThatWasCutIsNotBroughtBack(t *testing.T) {
	t.Parallel()
	pr := DefaultParams()
	log := checkpointLog().checkpoint(1, 6, "old summary").user("question six")
	p1 := mustBuild(t, nil, Input{Agent: testAgent(), Tail: log.events}, nil, t0, pr)
	if p1.Checkpoint == 0 {
		t.Fatal("setup: the checkpoint should apply")
	}
	// Squeeze the budget so the hard cut removes the summary and more.
	pr.TailBudget, pr.CompactFraction = 30, 0.6
	log.agent("answer six").user("question seven")
	p2 := mustBuild(t, p1, Input{Agent: testAgent(), Tail: log.events}, nil, t0, pr)
	if p2.Checkpoint != 0 || strings.HasPrefix(p2.Tiers[TierTail].Msgs[0].Text, checkpointLead) {
		t.Fatalf("setup: the cut should have removed the summary; tail = %q", texts(p2.Tiers[TierTail].Msgs))
	}
	pr = DefaultParams()
	log.agent("answer seven").user("question eight")
	p3 := mustBuild(t, p2, Input{Agent: testAgent(), Tail: log.events}, nil, t0, pr)
	if p3.Checkpoint != 0 || strings.HasPrefix(p3.Tiers[TierTail].Msgs[0].Text, checkpointLead) {
		t.Errorf("a summary that was cut out came back: %q", texts(p3.Tiers[TierTail].Msgs))
	}
}

func TestFoldSpanAndRequest(t *testing.T) {
	t.Parallel()
	pr := DefaultParams()
	log := checkpointLog().user("question six")
	p := mustBuild(t, nil, Input{Agent: testAgent(), Tail: log.events}, nil, t0, pr)
	if _, ok := p.FoldSpan(pr); ok {
		t.Fatal("a tail within its budget must not be folded")
	}

	pr.TailBudget = 30 // five short turns are about 60 tokens
	pr.HardBudget = 1000
	p = mustBuild(t, nil, Input{Agent: testAgent(), Tail: log.events}, nil, t0, pr)
	span, ok := p.FoldSpan(pr)
	if !ok {
		t.Fatal("an over-budget tail should yield a span")
	}
	// Half of ten messages, ending at a turn boundary: turns one to three.
	if span.Msgs != 6 || span.From != 1 || span.To != 9 {
		t.Fatalf("span = %+v, want 6 messages covering seqs 1..9", span)
	}

	req := p.FoldRequest(span, "<instruction>summarize</instruction>")
	full, part := p.PrefixMarks(), req.PrefixMarks()
	// Everything before the instruction is a prefix of the live request.
	for i := range len(part) - 1 {
		if part[i].Hash != full[i].Hash {
			t.Fatalf("mark %d of the fold request differs from the live request; the cache would miss", i)
		}
	}
	if last := req.Tiers[TierLive].Msgs; len(last) != 1 || last[0].Text != "<instruction>summarize</instruction>" {
		t.Errorf("live tier of the fold request = %q", texts(last))
	}
	if !bytes.Equal(CanonicalTools(req.Tools), CanonicalTools(p.Tools)) {
		t.Error("the fold request must offer the same tools, or the prefix before the messages changes")
	}

	// Applying that span yields a tail that starts where the span ended.
	log.checkpoint(span.From, span.To, "summary of one to three").agent("answer six").user("question seven")
	after := mustBuild(t, p, Input{Agent: testAgent(), Tail: log.events}, nil, t0, pr)
	if got := texts(after.Tiers[TierTail].Msgs); got[1] != "question four" {
		t.Errorf("tail after the fold = %q", got)
	}
	// A second fold absorbs the first: it starts where the first started.
	pr.TailBudget = 10
	if span2, ok := after.FoldSpan(pr); !ok || span2.From != 1 {
		t.Errorf("second span = %+v (ok=%t), want it to start at seq 1", span2, ok)
	}
}

func TestFoldSpanEndsBeforeAMessageLoggedMidTool(t *testing.T) {
	t.Parallel()
	// The second user message arrived while the tool ran: it is logged at
	// seq 3, between the call (2) and its result (4), and rendered after.
	log := new(logBuilder).user("run the job").call("c1", "job()").user("and a second thing").result("c1", "done").
		agent("Both noted.").user("next").agent("ok").user("last")
	pr := DefaultParams()
	pr.TailBudget, pr.FoldFraction, pr.HardBudget = 5, 0.3, 1000
	p := mustBuild(t, nil, Input{Agent: testAgent(), Tail: log.events}, nil, t0, pr)
	span, ok := p.FoldSpan(pr)
	if !ok {
		t.Fatal("expected a span")
	}
	// The cut falls before "and a second thing" (seq 3). The span must not
	// reach seq 3 or beyond, or the checkpoint would swallow a message that
	// is still shown.
	if span.Msgs != 3 || span.To != 2 {
		t.Errorf("span = %+v, want 3 messages ending at seq 2", span)
	}
}

func TestForkBeginsWithTheParentsPrompt(t *testing.T) {
	t.Parallel()
	pr := DefaultParams()
	parentLog := checkpointLog().user("question six")
	parent := mustBuild(t, nil, Input{Agent: testAgent(), Profile: "Likes tea.", Tail: parentLog.events},
		[]Recalled{{ID: 3, Text: "an old episode"}}, t0, pr)
	parent.AppendTail(core.ToolCall{ID: "d1", Name: "delegate", Args: []byte(`{"task":"t"}`)}, core.ToolResult{ID: "d1", Output: "started"})
	base := parent.Head()

	childLog := new(logBuilder).user("<task>\ndo the thing\n</task>")
	child := mustBuild(t, nil, Input{Agent: testAgent(), Tail: childLog.events, Base: base}, nil, t0+5000, pr)

	pm, cm := parent.PrefixMarks(), child.PrefixMarks()
	shared := 0
	for shared < len(pm) && shared < len(cm) && pm[shared].Hash == cm[shared].Hash {
		shared++
	}
	// Everything up to the end of the parent's tail tier is shared.
	wantShared := 0
	for t := TierStatic; t <= TierTail; t++ {
		wantShared += len(parent.Tiers[t].Msgs)
	}
	if shared != wantShared {
		t.Fatalf("the fork shares %d leading messages with its parent, want %d (tiers 0-3)", shared, wantShared)
	}
	live := texts(child.Tiers[TierLive].Msgs)
	if len(live) != 2 || !strings.HasPrefix(live[0], "<task>") {
		t.Errorf("the fork's live tier = %q, want the task and the time note", live)
	}
	for _, m := range child.Tiers[TierLive].Msgs {
		if strings.Contains(m.Text, "question six") {
			t.Error("the fork must not see the parent's unfinished turn")
		}
	}
	// The parent keeps working; the fork's base must not move with it.
	parent.AppendTail(core.ToolCall{ID: "x", Name: "python", Code: "1"}, core.ToolResult{ID: "x", Output: "1"})
	again := mustBuild(t, nil, Input{Agent: testAgent(), Tail: childLog.events, Base: base}, nil, t0+5000, pr)
	if again.Chain != child.Chain {
		t.Error("the fork's prompt changed when its parent's plan was extended")
	}
}

func TestPromptIsAOneOffRequest(t *testing.T) {
	t.Parallel()
	p := Prompt("be brief", "hello")
	marks := p.PrefixMarks()
	if len(marks) != 2 || p.Tiers[TierStatic].Msgs[0].Text != "be brief" || p.Tiers[TierLive].Msgs[0].Text != "hello" {
		t.Errorf("Prompt laid out %d messages: %+v", len(marks), p.Tiers)
	}
}
