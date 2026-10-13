package turn_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/llm"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/turn"
)

// caps returns capabilities for a fake route, starting from a plain
// prefix-caching one.
func caps(mod func(*llm.Capabilities)) *llm.Capabilities {
	c := llm.Capabilities{
		ContextWindow: 200_000, MaxOutput: 8192,
		Cache: llm.CacheAutoPrefix, CacheTTL: time.Hour, CacheUsage: true,
	}
	mod(&c)
	return &c
}

func TestSeveralToolCallsInOneStepRunInOrder(t *testing.T) {
	t.Parallel()
	s, l := &sink{}, &leaser{}
	p := fakellm.New(fakellm.Options{Caps: caps(func(c *llm.Capabilities) { c.SingleToolCall = false })})
	res, err := turn.Run(t.Context(), input(s, l, "!python first() ;; !python second() ;; !shell third"), deps(p))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 3 || len(s.results) != 3 {
		t.Fatalf("%d calls and %d results logged, want 3 of each", len(s.calls), len(s.results))
	}
	for i, c := range s.calls {
		if c.Sibling != i || s.results[i].ID != c.ID {
			t.Errorf("call %d: sibling %d, result for %q; each call must be logged with its position and answered in order", i, c.Sibling, s.results[i].ID)
		}
	}
	if s.calls[0].Code != "first()" || s.calls[1].Code != "second()" || s.calls[2].Name != "shell" {
		t.Errorf("calls ran out of order: %+v", s.calls)
	}
	// One model call produced all three; the second answered.
	if p.Sends() != 2 || len(s.attempts) != 2 {
		t.Errorf("%d model calls and %d attempt records, want 2 and 2", p.Sends(), len(s.attempts))
	}
	if res.Final == "" || res.Replan || res.StepLimit {
		t.Errorf("result = %+v", res)
	}
	// The plan holds call, result, call, result, call, result after the note.
	live := res.Plan.Tiers[plan.TierLive].Msgs
	if len(live) != 8 || live[2].ToolCall == nil || live[4].ToolCall.Sibling != 1 || live[6].ToolCall.Sibling != 2 {
		t.Errorf("live tier has %d messages; want the message, the note and three call/result pairs", len(live))
	}
}

func TestARouteHeldToOneCallMakesOne(t *testing.T) {
	t.Parallel()
	s := &sink{}
	p := fakellm.New(fakellm.Options{Caps: caps(func(c *llm.Capabilities) { c.SingleToolCall = true })})
	if _, err := turn.Run(t.Context(), input(s, &leaser{}, "!python a() ;; !python b()"), deps(p)); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 1 || s.calls[0].Sibling != 0 {
		t.Errorf("calls = %+v, want only the first", s.calls)
	}
}

func TestAnInterruptionTakesEffectBetweenSiblingCalls(t *testing.T) {
	t.Parallel()
	l := &leaser{}
	s := &interruptingSink{after: 1}
	p := fakellm.New(fakellm.Options{Caps: caps(func(c *llm.Capabilities) { c.SingleToolCall = false })})
	in := input(&s.sink, l, "!python a() ;; !python b() ;; !python c()")
	in.Sink, s.ctl = s, in.Ctl
	res, err := turn.Run(t.Context(), in, deps(p))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Replan {
		t.Fatal("a message during the first call should make the turn re-plan")
	}
	if len(s.calls) != 1 || len(s.results) != 1 {
		t.Errorf("%d calls ran after the interruption; the rest of the step must not start", len(s.calls))
	}
}

// interruptingSink interrupts the turn while its nth tool call runs.
type interruptingSink struct {
	sink
	ctl   *turn.Control
	after int
}

func (s *interruptingSink) ToolCall(ctx context.Context, c core.ToolCall, enter func() bool) error {
	if err := s.sink.ToolCall(ctx, c, enter); err != nil {
		return err
	}
	if len(s.calls) == s.after {
		s.ctl.Interrupt() // a message arrived while the tool runs
	}
	return nil
}

func TestReasoningIsLoggedOnlyWhereTheRouteNeedsItBack(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		replay     llm.Replay
		logged     bool // on the tool-call event
		withFinal  bool // on the result's final reply
		carriedNow bool // in this turn's plan
	}{
		{llm.ReplayAllTurns, true, true, true},
		{llm.ReplaySameTurn, false, false, true},
		{llm.ReplayNone, false, false, false},
	} {
		s := &sink{}
		p := fakellm.New(fakellm.Options{Caps: caps(func(c *llm.Capabilities) { c.Replay = tc.replay })})
		res, err := turn.Run(t.Context(), input(s, &leaser{}, "!python print(1)"), deps(p))
		if err != nil {
			t.Fatalf("replay %d: %v", tc.replay, err)
		}
		if got := s.calls[0].Reasoning != nil; got != tc.logged {
			t.Errorf("replay %d: reasoning on the logged call = %t, want %t", tc.replay, got, tc.logged)
		}
		if got := res.Reasoning != nil; got != tc.withFinal {
			t.Errorf("replay %d: reasoning with the final reply = %t, want %t", tc.replay, got, tc.withFinal)
		}
		carried := false
		for _, m := range res.Plan.Tiers[plan.TierLive].Msgs {
			carried = carried || m.Reasoning != nil
		}
		if carried != tc.carriedNow {
			t.Errorf("replay %d: reasoning carried within the turn = %t, want %t", tc.replay, carried, tc.carriedNow)
		}
		if tc.logged && s.calls[0].Reasoning.Route != "fake/fake-1" {
			t.Errorf("payload tagged %q, want the route that produced it", s.calls[0].Reasoning.Route)
		}
	}
}

func TestNamesResolveStrictly(t *testing.T) {
	t.Parallel()
	p := fakellm.New(fakellm.Options{Models: []string{"good"}})
	for name, mod := range map[string]func(*core.AgentConfig){
		"unknown model":    func(a *core.AgentConfig) { a.Model = "retired-model" },
		"unknown provider": func(a *core.AgentConfig) { a.Provider = "elsewhere" },
	} {
		in := input(&sink{}, &leaser{}, "hello")
		mod(in.Agent)
		_, err := turn.Run(t.Context(), in, deps(p))
		if !errors.Is(err, llm.ErrUnknownModel) && !errors.Is(err, llm.ErrUnknownProvider) {
			t.Errorf("%s: err = %v, want a strict resolution error", name, err)
		}
		if p.Sends() != 0 {
			t.Errorf("%s: a request was sent anyway", name)
		}
	}
	in := input(&sink{}, &leaser{}, "hello")
	in.Agent.Effort = "extreme"
	if _, err := turn.Run(t.Context(), in, deps(p)); err == nil || !strings.Contains(err.Error(), "effort") {
		t.Errorf("bad effort: err = %v", err)
	}
}

func TestPlannerSettingsComeFromTheRoute(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		caps       *llm.Capabilities
		checkpoint bool
		wantTTL    int64
		wantBudget int
		wantHard   int
	}{
		"hour-long automatic cache": {
			caps:    caps(func(c *llm.Capabilities) { c.CacheTTL = time.Hour }),
			wantTTL: time.Hour.Milliseconds(), wantBudget: 24_000,
		},
		"no cache means nothing to keep warm": {
			caps:    caps(func(c *llm.Capabilities) { c.Cache, c.CacheTTL = llm.CacheNone, time.Hour }),
			wantTTL: 0, wantBudget: 24_000,
		},
		"a small context window caps the tail": {
			caps:    caps(func(c *llm.Capabilities) { c.ContextWindow = 16_000 }),
			wantTTL: time.Hour.Milliseconds(), wantBudget: 8_000,
		},
		"with checkpoints the cut moves out to one and a half budgets": {
			caps: caps(func(*llm.Capabilities) {}), checkpoint: true,
			wantTTL: time.Hour.Milliseconds(), wantBudget: 24_000, wantHard: 36_000,
		},
	} {
		d := deps(fakellm.New(fakellm.Options{Caps: tc.caps}))
		d.Checkpoint = tc.checkpoint
		res, err := turn.Run(t.Context(), input(&sink{}, &leaser{}, "hello"), d)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Params.CacheTTL != tc.wantTTL || res.Params.TailBudget != tc.wantBudget || res.Params.HardBudget != tc.wantHard {
			t.Errorf("%s: params = TTL %d, budget %d, hard %d; want %d, %d, %d", name,
				res.Params.CacheTTL, res.Params.TailBudget, res.Params.HardBudget, tc.wantTTL, tc.wantBudget, tc.wantHard)
		}
	}
}

func TestAttemptsRecordWhatWasSentAndPredicted(t *testing.T) {
	t.Parallel()
	s := &sink{}
	p := fakellm.New(fakellm.Options{Caps: caps(func(c *llm.Capabilities) {
		c.Efforts, c.ReasoningOff = []llm.Effort{llm.EffortLow, llm.EffortHigh, llm.EffortMax}, true
	})})
	d := deps(p)
	in := input(s, &leaser{}, "!python print(6*7)")
	in.Agent.Effort = "medium"
	res, err := turn.Run(t.Context(), in, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.attempts) != 2 {
		t.Fatalf("%d attempt records for 2 model calls", len(s.attempts))
	}
	first, second := s.attempts[0], s.attempts[1]
	if first.Turn != 1 || first.Step != 0 || second.Step != 1 || first.Route != "fake/fake-1" {
		t.Errorf("attempts misnumbered: %+v, %+v", first, second)
	}
	// "medium" is not accepted by this route; the nearest value is sent, and
	// the record says which.
	if first.Effort != "high" {
		t.Errorf("effort recorded as %q, want the mapped value \"high\"", first.Effort)
	}
	if first.Stop != "tool" || second.Stop != "end" || first.Error != "" || first.Tries != 1 {
		t.Errorf("outcomes: %+v, %+v", first, second)
	}
	if first.BodyHash == "" || first.BodyHash == second.BodyHash || second.Bytes <= first.Bytes {
		t.Errorf("request fingerprints: %q (%d bytes), %q (%d bytes)", first.BodyHash, first.Bytes, second.BodyHash, second.Bytes)
	}
	// Step 0 is cold. Step 1 extends step 0's request, so its whole prompt
	// is predicted cached, and this provider agrees.
	if first.Predicted != 0 || first.Cached != 0 {
		t.Errorf("first call: predicted %d, cached %d; want a cold start", first.Predicted, first.Cached)
	}
	if second.Predicted == 0 || second.Predicted != second.Cached {
		t.Errorf("second call: predicted %d, provider reported %d", second.Predicted, second.Cached)
	}
	if res.Sent == nil || res.Sent.Route != "fake/fake-1" || len(res.Sent.Marks) == 0 {
		t.Fatalf("the turn did not report what it last sent: %+v", res.Sent)
	}

	// The next turn predicts against that, as long as the cache can be warm.
	next := func(sent *turn.Sent) core.AttemptBody {
		s2 := &sink{}
		in2 := input(s2, &leaser{}, "!python print(6*7)")
		in2.Agent.Effort = "medium"
		in2.Sent = sent
		if _, err := turn.Run(t.Context(), in2, d); err != nil {
			t.Fatal(err)
		}
		return s2.attempts[0]
	}
	if a := next(res.Sent); a.Predicted == 0 {
		t.Error("a warm previous request should predict a cache hit on the shared prefix")
	}
	stale := *res.Sent
	stale.At -= 2 * time.Hour.Milliseconds()
	if a := next(&stale); a.Predicted != 0 {
		t.Errorf("a previous request older than the route's cache lifetime predicted %d", a.Predicted)
	}
	elsewhere := *res.Sent
	elsewhere.Route = "other/route"
	if a := next(&elsewhere); a.Predicted != 0 {
		t.Errorf("a previous request to another route predicted %d", a.Predicted)
	}
}

func TestAFailedCallIsStillRecorded(t *testing.T) {
	t.Parallel()
	s := &sink{}
	p := fakellm.New(fakellm.Options{Fail: func(int, []byte) error {
		return &llm.Error{Class: llm.ErrAuth, Status: 401, Provider: "fake", Err: errors.New("bad key")}
	}})
	_, err := turn.Run(t.Context(), input(s, &leaser{}, "hello"), deps(p))
	if llm.ClassOf(err) != llm.ErrAuth {
		t.Fatalf("err = %v", err)
	}
	if len(s.attempts) != 1 || s.attempts[0].Error != "auth" || s.attempts[0].Stop != "" || s.attempts[0].BodyHash == "" {
		t.Errorf("attempts = %+v, want one failed attempt with its request fingerprint", s.attempts)
	}
}

// delegator records delegations and answers with scripted results.
type delegator struct {
	mu   sync.Mutex
	got  []turn.Delegation
	from []core.ContactID
	err  error
}

func (d *delegator) Delegate(_ context.Context, parent core.ContactID, req turn.Delegation) (core.ContactID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return 0, d.err
	}
	d.got, d.from = append(d.got, req), append(d.from, parent)
	return core.ContactID(100 + len(d.got)), nil
}

func delegating(t *testing.T, text string, mod func(*turn.Input, *turn.Deps)) (*sink, *delegator, turn.Result) {
	t.Helper()
	s, dg := &sink{}, &delegator{}
	d := deps(fakellm.New(fakellm.Options{Mode: fakellm.CachePrefix}))
	d.Delegate = dg
	in := input(s, &leaser{}, text)
	in.Agent.Delegate = true
	if mod != nil {
		mod(&in, d)
	}
	res, err := turn.Run(t.Context(), in, d)
	if err != nil {
		t.Fatal(err)
	}
	return s, dg, res
}

func TestDelegateTool(t *testing.T) {
	t.Parallel()

	t.Run("spawn returns at once with the child's ID", func(t *testing.T) {
		t.Parallel()
		s, dg, res := delegating(t, "!delegate count the files", nil)
		if len(dg.got) != 1 || dg.got[0].Task != "count the files" || dg.got[0].Base != nil || dg.from[0] != 1 {
			t.Fatalf("delegations = %+v from %v", dg.got, dg.from)
		}
		if len(s.calls) != 1 || s.calls[0].Name != turn.DelegateTool || string(s.calls[0].Args) != `{"task":"count the files"}` {
			t.Errorf("logged call = %+v", s.calls)
		}
		out := s.results[0]
		if out.IsError || !strings.Contains(out.Output, "Sub-agent 101 started") || !strings.Contains(out.Output, "cannot wait") {
			t.Errorf("tool result = %+v", out)
		}
		// The model is offered the tool as part of the static tier.
		names := make([]string, len(res.Plan.Tools))
		for i, td := range res.Plan.Tools {
			names[i] = td.Name
		}
		if strings.Join(names, ",") != "python,shell,delegate" {
			t.Errorf("tools offered = %v", names)
		}
	})

	t.Run("fork hands over the prompt up to the last finished turn", func(t *testing.T) {
		t.Parallel()
		_, dg, res := delegating(t, `!delegate {"task":"check it","mode":"fork"}`, nil)
		if len(dg.got) != 1 || dg.got[0].Base == nil {
			t.Fatalf("delegations = %+v", dg.got)
		}
		base := dg.got[0].Base
		for tier := plan.TierStatic; tier <= plan.TierTail; tier++ {
			if base.Chain[tier] != res.Plan.Chain[tier] {
				t.Errorf("tier %s of the fork base differs from the parent's prompt", tier)
			}
		}
		if n := len(base.Tiers[plan.TierLive].Msgs); n != 0 {
			t.Errorf("the fork base carries %d live messages; the parent's unfinished turn is not the child's business", n)
		}
	})

	t.Run("the tool is offered only where the agent has it", func(t *testing.T) {
		t.Parallel()
		_, dg, res := delegating(t, "!delegate anything", func(in *turn.Input, _ *turn.Deps) { in.Agent.Delegate = false })
		if len(dg.got) != 0 || len(res.Plan.Tools) != 2 {
			t.Errorf("delegations = %d, tools = %d; an agent without the tool must not see or use it", len(dg.got), len(res.Plan.Tools))
		}
		_, _, res = delegating(t, "hello", func(_ *turn.Input, d *turn.Deps) { d.Delegate = nil })
		if len(res.Plan.Tools) != 2 {
			t.Error("with delegation not configured the tool must not be offered")
		}
	})

	t.Run("a sub-agent sees the tool but cannot use it", func(t *testing.T) {
		t.Parallel()
		s, dg, res := delegating(t, "!delegate go deeper", func(in *turn.Input, _ *turn.Deps) { in.Agent.Parent = 7 })
		if len(res.Plan.Tools) != 3 {
			t.Error("a sub-agent's static tier must match its parent's, tool list included, to share its cache")
		}
		if len(dg.got) != 0 || !s.results[0].IsError || !strings.Contains(s.results[0].Output, "cannot start sub-agents") {
			t.Errorf("delegations = %d, result = %+v", len(dg.got), s.results)
		}
	})

	t.Run("failures are results the model can read", func(t *testing.T) {
		t.Parallel()
		for text, want := range map[string]string{
			`!delegate {"mode":"fork"}`:           `needs {"task"`,
			`!delegate {"task":"x","mode":"zap"}`: `mode must be`,
		} {
			s, dg, res := delegating(t, text, nil)
			if len(dg.got) != 0 || !s.results[0].IsError || !strings.Contains(s.results[0].Output, want) || res.Final == "" {
				t.Errorf("%s: result = %+v, final = %q", text, s.results, res.Final)
			}
		}
		s, _, res := delegating(t, "!delegate one more", func(_ *turn.Input, d *turn.Deps) {
			d.Delegate = &delegator{err: core.ErrTooManyChildren}
		})
		if !s.results[0].IsError || !strings.Contains(s.results[0].Output, "too many sub-agents") || res.Final == "" {
			t.Errorf("at the limit: result = %+v", s.results)
		}
	})
}

func TestCheckpointRequestExtendsTheLiveRequest(t *testing.T) {
	t.Parallel()
	p := fakellm.New(fakellm.Options{Mode: fakellm.CachePrefix})
	d := deps(p)
	d.Diagnostics = diagnostic.New(5)
	// Eight messages of about 17 tokens: over this budget, and under the
	// hard budget of one and a half times it, where the tail would be cut.
	d.Plan.TailBudget = 100
	d.Checkpoint = true

	// A contact with some history, then one more turn.
	var tail []core.Event
	seq := uint64(0)
	add := func(kind core.EventKind, text string) {
		seq++
		ev := userEvent(seq, text)
		ev.Kind = kind
		tail = append(tail, ev)
	}
	for _, topic := range []string{"alpha", "beta", "gamma", "delta"} {
		add(core.KindUserMsg, "tell me about "+topic+" in some detail please")
		add(core.KindAgentMsg, "Here is what I know about "+topic+", at some length.")
	}
	add(core.KindUserMsg, "and now epsilon")
	s := &sink{}
	in := input(s, &leaser{}, "unused")
	in.Tail = tail
	res, err := turn.Run(t.Context(), in, d)
	if err != nil {
		t.Fatal(err)
	}
	span, ok := res.Plan.FoldSpan(res.Params)
	if !ok {
		t.Fatal("the tail is over its budget and should yield a span")
	}

	text, att, err := turn.Checkpoint(t.Context(), d.Models, turn.Fold{
		Route: res.Route, Plan: res.Plan, Span: span,
		Scope: diagnostic.Scope{Contact: in.Contact, Turn: in.TurnID}, Diagnostics: d.Diagnostics, Params: res.Params,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "I remember") || !strings.Contains(text, "alpha") {
		t.Errorf("summary = %q", text)
	}
	if att == nil || att.Purpose != "checkpoint" || att.Route != "fake/fake-1" {
		t.Fatalf("attempt = %+v", att)
	}
	// On the same route only the instruction is new: everything up to the
	// end of the span was cached by the turn that just ran.
	if att.Cached == 0 || att.Input-att.Cached > 200 {
		t.Errorf("checkpoint request: %d of %d input tokens cached; the folded span should be read from cache", att.Cached, att.Input)
	}
	h, _, stop := d.Diagnostics.Subscribe()
	defer stop()
	if len(h) != 2 || !h[1].Done || h[1].Purpose != "checkpoint" || h[1].ReportedCached != att.Cached {
		t.Fatalf("checkpoint diagnostics: %+v", h)
	}
}

func TestModelSummarizer(t *testing.T) {
	t.Parallel()
	p := fakellm.New(fakellm.Options{Models: []string{"big", "small"}})
	sum := &turn.ModelSummarizer{Models: fakellm.NewClient(p), Model: "small"}
	got, err := sum.Summarize(t.Context(), "contact: where is my order\nagent: it shipped on Tuesday")
	if err != nil || !strings.HasPrefix(got, "Summary:") || !strings.Contains(got, "Tuesday") {
		t.Errorf("Summarize = %q, %v", got, err)
	}
	if _, err := sum.Summarize(t.Context(), "  "); err == nil {
		t.Error("an empty transcript must not cost a model call")
	}
	sum.Model = "gone"
	if _, err := sum.Summarize(t.Context(), "x"); !errors.Is(err, llm.ErrUnknownModel) {
		t.Errorf("unknown summary model: %v", err)
	}
}

func TestOnlyAFinishedReplyCountsAsACheckpoint(t *testing.T) {
	t.Parallel()
	// loopLLM answers everything with a tool call. Text that came with a
	// tool call is not a summary, and must not stand in for a conversation.
	d := deps(loopLLM{fakellm.New(fakellm.Options{})})
	p := plan.Prompt("s", "hello")
	route, err := d.Models.Route(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	// A span over a plan with no tail: FoldRequest only needs the counts.
	_, att, err := turn.Checkpoint(t.Context(), d.Models, turn.Fold{Route: route, Plan: p, Turn: 7})
	if err == nil || !strings.Contains(err.Error(), "called a tool") {
		t.Errorf("err = %v, want the tool call refused as a summary", err)
	}
	if att == nil || att.Turn != 7 || att.Purpose != "checkpoint" {
		t.Errorf("attempt = %+v, want it tied to the turn its prompt came from", att)
	}
}

func TestThePlanAndThePredictionShareOneClock(t *testing.T) {
	t.Parallel()
	res, err := turn.Run(t.Context(), input(&sink{}, &leaser{}, "!python print(1)"), deps(fakellm.New(fakellm.Options{})))
	if err != nil {
		t.Fatal(err)
	}
	// The next turn's planner (recall hysteresis) and its cache prediction
	// both ask "is the cache still warm?". They must measure from the same
	// moment: when this prompt was last sent.
	if res.Sent == nil || res.Plan.BuiltAt != res.Sent.At {
		t.Errorf("plan dated %d, last request sent at %+v", res.Plan.BuiltAt, res.Sent)
	}
}
