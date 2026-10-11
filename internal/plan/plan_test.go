package plan

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
)

//nolint:gochecknoglobals // the conventional -update flag for golden files
var update = flag.Bool("update", false, "rewrite golden files")

const t0 = int64(1_760_000_000_000) // a fixed clock: 2025-10-09T08:53:20Z

// logBuilder assembles an event log the way an actor would.
type logBuilder struct {
	events []core.Event
}

func (b *logBuilder) add(kind core.EventKind, body any) *logBuilder {
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	seq := uint64(len(b.events) + 1)
	b.events = append(b.events, core.Event{Contact: 7, Seq: seq, TS: t0 + int64(seq)*1000, Kind: kind, Body: raw})
	return b
}

func (b *logBuilder) user(text string) *logBuilder {
	return b.add(core.KindUserMsg, core.MsgBody{Text: text})
}

func (b *logBuilder) agent(text string) *logBuilder {
	return b.add(core.KindAgentMsg, core.MsgBody{Text: text})
}

func (b *logBuilder) cut(text string) *logBuilder {
	return b.add(core.KindAgentMsg, core.MsgBody{Text: text, Interrupted: true})
}

func (b *logBuilder) call(id, code string) *logBuilder {
	return b.add(core.KindToolCall, core.ToolCall{ID: id, Name: "python", Code: code})
}

func (b *logBuilder) result(id, out string) *logBuilder {
	return b.add(core.KindToolResult, core.ToolResult{ID: id, Output: out})
}

func testAgent() *core.AgentConfig {
	return &core.AgentConfig{
		System: "You are Ada, a thoughtful colleague.",
		Model:  "test-model",
		Tools: []core.ToolDef{
			{Name: "python", Description: "Run Python.", Env: core.EnvSpec{Tier: 0, Base: "python-3.12"}},
			{Name: "shell", Description: "Run a shell command.", Env: core.EnvSpec{Tier: 1, Base: "debian", Packages: []string{"jq", "curl"}, Network: true}},
		},
	}
}

func mustBuild(t *testing.T, prev *PromptPlan, in Input, recalled []Recalled, now int64, pr Params) *PromptPlan {
	t.Helper()
	p, err := Build(prev, in, recalled, now, pr)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return p
}

// dump renders a plan as the golden files store it: every tier's hash and
// canonical bytes, then the chain.
func dump(p *PromptPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tools %s\n", CanonicalTools(p.Tools))
	for i := range p.Tiers {
		fmt.Fprintf(&b, "tier %d %s hash=%016x tokens=%d\n", i, TierKind(i), p.Tiers[i].Hash, p.Tiers[i].Tokens)
		for j := range p.Tiers[i].Msgs {
			fmt.Fprintf(&b, "  %s\n", Canonical(&p.Tiers[i].Msgs[j]))
		}
	}
	for i, c := range p.Chain {
		fmt.Fprintf(&b, "chain %d %016x\n", i, c)
	}
	fmt.Fprintf(&b, "recall %v tail_from %d\n", p.Recall, p.TailFrom)
	return b.String()
}

// TestGolden pins the rendered bytes and the hash chain. A change that makes
// this fail would silently break every contact's prompt cache on deploy:
// update the golden files only when that is intended.
func TestGolden(t *testing.T) {
	t.Parallel()
	cases := map[string]func(t *testing.T) *PromptPlan{
		"first_message": func(t *testing.T) *PromptPlan {
			t.Helper()
			log := new(logBuilder).user("hey, are you around?")
			return mustBuild(t, nil, Input{Contact: 7, Agent: testAgent(), Tail: log.events}, nil, t0, DefaultParams())
		},
		"all_tiers": func(t *testing.T) *PromptPlan {
			t.Helper()
			log := new(logBuilder).
				user("can you check the row counts?").
				call("c1", "print(41 + 1)").
				result("c1", "42").
				agent("There are 42 rows.").
				user("great — and \"quoted\"\ttabs?\nplus unicode: naïve café ☕").
				user("one more thing")
			in := Input{Contact: 7, Agent: testAgent(), Profile: "Prefers short answers. Based in Lisbon.", Tail: log.events}
			recalled := []Recalled{
				{ID: 3, Text: "[2025-09-30] contact: the migration is stuck\n...\nyou: rerun it with --lock-timeout"},
				{ID: 9, Text: "[2025-10-02] Summary: planned the Lisbon trip."},
			}
			return mustBuild(t, nil, in, recalled, t0, DefaultParams())
		},
		"tool_step_appended": func(t *testing.T) *PromptPlan {
			t.Helper()
			log := new(logBuilder).user("what is 6 * 7?")
			p := mustBuild(t, nil, Input{Contact: 7, Agent: testAgent(), Tail: log.events}, nil, t0, DefaultParams())
			p.AppendTail(
				core.ToolCall{ID: "c9", Name: "python", Code: "print(6*7)", Text: "Let me compute that."},
				core.ToolResult{ID: "c9", Output: "42"},
			)
			return p
		},
		"interrupted_and_orphan": func(t *testing.T) *PromptPlan {
			t.Helper()
			log := new(logBuilder).
				user("tell me a long story").
				cut("Once upon a").
				user("actually, run this").
				call("c2", "import time; time.sleep(999)").
				user("never mind")
			return mustBuild(t, nil, Input{Contact: 7, Agent: testAgent(), Tail: log.events}, nil, t0, DefaultParams())
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := dump(build(t))
			path := filepath.Join("testdata", name+".golden")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if got != string(want) {
				t.Errorf("rendered plan changed; this breaks the prompt cache.\n--- got\n%s\n--- want\n%s", got, want)
			}
		})
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	t.Parallel()
	log := new(logBuilder).user("a").agent("b").user("c")
	in := Input{Agent: testAgent(), Profile: "p", Tail: log.events}
	rec := []Recalled{{ID: 1, Text: "x"}}
	a := mustBuild(t, nil, in, rec, t0, DefaultParams())
	b := mustBuild(t, nil, in, rec, t0, DefaultParams())
	if a.Chain != b.Chain {
		t.Fatalf("same inputs gave different chains: %x vs %x", a.Chain, b.Chain)
	}
}

func TestFirstChanged(t *testing.T) {
	t.Parallel()
	pr := DefaultParams()
	log := new(logBuilder).user("hello")
	base := Input{Agent: testAgent(), Profile: "likes tea", Tail: log.events}
	rec := []Recalled{{ID: 2, Text: "old chat"}}
	prev := mustBuild(t, nil, base, rec, t0, pr)

	if got := prev.FirstChanged(nil); got != 0 {
		t.Errorf("no previous plan: FirstChanged = %d, want 0", got)
	}

	// The normal next turn: the reply and a new message were appended.
	log.agent("hi!").user("how are you?")
	next := base
	next.Tail = log.events

	t.Run("append only", func(t *testing.T) {
		t.Parallel()
		p := mustBuild(t, prev, next, rec, t0+60_000, pr)
		if got := p.FirstChanged(prev); got != int(TierLive) {
			t.Errorf("FirstChanged = %d, want %d (tail only grew)", got, TierLive)
		}
		want := p.Tiers[TierStatic].Tokens + p.Tiers[TierProfile].Tokens + p.Tiers[TierRecall].Tokens + prev.Tiers[TierTail].Tokens
		if got := p.SharedPrefixTokens(prev); got != want {
			t.Errorf("SharedPrefixTokens = %d, want %d", got, want)
		}
	})
	t.Run("recall changed", func(t *testing.T) {
		t.Parallel()
		p := mustBuild(t, prev, next, []Recalled{{ID: 5, Text: "other chat"}}, t0+60_000, pr)
		if got := p.FirstChanged(prev); got != int(TierRecall) {
			t.Errorf("FirstChanged = %d, want %d", got, TierRecall)
		}
	})
	t.Run("profile changed", func(t *testing.T) {
		t.Parallel()
		in := next
		in.Profile = "likes coffee"
		p := mustBuild(t, prev, in, rec, t0+60_000, pr)
		if got := p.FirstChanged(prev); got != int(TierProfile) {
			t.Errorf("FirstChanged = %d, want %d", got, TierProfile)
		}
	})
	t.Run("tools changed", func(t *testing.T) {
		t.Parallel()
		in := next
		agent := *testAgent()
		agent.Tools = agent.Tools[:1]
		in.Agent = &agent
		p := mustBuild(t, prev, in, rec, t0+60_000, pr)
		if got := p.FirstChanged(prev); got != int(TierStatic) {
			t.Errorf("FirstChanged = %d, want %d", got, TierStatic)
		}
	})
	t.Run("nothing but the clock", func(t *testing.T) {
		t.Parallel()
		p := mustBuild(t, prev, base, rec, t0+60_000, pr)
		if got := p.FirstChanged(prev); got != int(TierLive) {
			t.Errorf("FirstChanged = %d, want %d (only the time note moved)", got, TierLive)
		}
	})
}

// TestToolStepsStayCacheSafe checks the in-turn invariant: appending a tool
// exchange changes nothing before the live tier, and the next turn's tail is
// a continuation of this one's.
func TestToolStepsStayCacheSafe(t *testing.T) {
	t.Parallel()
	pr := DefaultParams()
	log := new(logBuilder).user("earlier").agent("noted").user("compute 6*7")
	in := Input{Agent: testAgent(), Tail: log.events}
	p := mustBuild(t, nil, in, nil, t0, pr)
	before, marksBefore := p.Chain, p.PrefixMarks()

	call := core.ToolCall{ID: "c1", Name: "python", Code: "print(6*7)"}
	res := core.ToolResult{ID: "c1", Output: "42"}
	p.AppendTail(call, res)

	for i := range int(TierLive) {
		if p.Chain[i] != before[i] {
			t.Errorf("tier %d chain changed after AppendTail", i)
		}
	}
	// Strictly append-only: what was sent before the tool ran is a prefix of
	// what is sent after, mark for mark.
	live := p.Tiers[TierLive].Msgs
	if n := len(live); live[n-2].ToolCall == nil || live[n-1].ToolResult == nil || !strings.HasPrefix(live[n-3].Text, "<now>") {
		t.Errorf("tool exchange was not appended after everything already sent: %+v", live)
	}
	after := p.PrefixMarks()
	for i, m := range marksBefore {
		if after[i].Hash != m.Hash {
			t.Fatalf("message %d changed after AppendTail; the prompt is no longer append-only", i)
		}
	}

	// The actor logs the same exchange and the reply; the next turn extends.
	log.call("c1", "print(6*7)").result("c1", "42").agent("It is 42.").user("thanks")
	in.Tail = log.events
	next := mustBuild(t, p, in, nil, t0+30_000, pr)
	if got := next.FirstChanged(p); got != int(TierLive) {
		t.Errorf("next turn FirstChanged = %d, want %d", got, TierLive)
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	roles := func(es []entry) string {
		var b strings.Builder
		for _, e := range es {
			switch {
			case e.msg.ToolCall != nil:
				b.WriteString("C")
			case e.msg.ToolResult != nil:
				b.WriteString("R")
			case e.msg.Role == core.RoleUser:
				b.WriteString("U")
			case e.msg.Role == core.RoleAssistant:
				b.WriteString("A")
			default:
				b.WriteString("?")
			}
		}
		return b.String()
	}

	t.Run("user message during a tool run moves after the result", func(t *testing.T) {
		t.Parallel()
		log := new(logBuilder).user("go").call("c1", "x").user("wait").result("c1", "ok").agent("done")
		es, err := normalize(log.events, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := roles(es); got != "UCRUA" {
			t.Errorf("order = %s, want UCRUA", got)
		}
	})
	t.Run("a call with no result gets a placeholder", func(t *testing.T) {
		t.Parallel()
		log := new(logBuilder).user("go").call("c1", "x").user("hello?")
		es, err := normalize(log.events, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := roles(es); got != "UCRU" {
			t.Fatalf("order = %s, want UCRU", got)
		}
		if r := es[2].msg.ToolResult; r.ID != "c1" || !r.IsError {
			t.Errorf("placeholder result = %+v", r)
		}
	})
	t.Run("a result whose call was compacted away is dropped", func(t *testing.T) {
		t.Parallel()
		log := new(logBuilder).user("go").call("c1", "x").user("wait").result("c1", "ok").agent("done")
		es, err := normalize(log.events, 3) // start at "wait"
		if err != nil {
			t.Fatal(err)
		}
		if got := roles(es); got != "UA" {
			t.Errorf("order = %s, want UA", got)
		}
	})
	t.Run("interrupted replies say so", func(t *testing.T) {
		t.Parallel()
		log := new(logBuilder).user("story").cut("Once upon").user("stop")
		es, err := normalize(log.events, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(es[1].msg.Text, "cut off") {
			t.Errorf("interrupted reply not marked: %q", es[1].msg.Text)
		}
	})
	t.Run("bad body is an error", func(t *testing.T) {
		t.Parallel()
		_, err := normalize([]core.Event{{Seq: 1, Kind: core.KindUserMsg, Body: []byte("{")}}, 0)
		if err == nil {
			t.Error("want an error for an undecodable body")
		}
	})
}

// TestCompaction replays a long conversation and counts how often the start
// of the tail moves. Stepping moves it once per many turns; sliding moves it
// nearly every turn once the budget is reached, which is the difference
// Rule 2 exists for.
func TestCompaction(t *testing.T) {
	t.Parallel()
	const turns = 60
	run := func(slide bool) (moves int) {
		pr := DefaultParams()
		pr.TailBudget, pr.Slide = 400, slide
		log := new(logBuilder)
		var prev *PromptPlan
		for i := range turns {
			log.user(fmt.Sprintf("message number %d with some padding to give it weight", i))
			p := mustBuild(t, prev, Input{Agent: testAgent(), Tail: log.events}, nil, t0+int64(i)*1000, pr)
			if prev != nil && p.TailFrom != prev.TailFrom {
				moves++
				if got := p.FirstChanged(prev); got != int(TierTail) {
					t.Errorf("turn %d: compaction should break at the tail, got tier %d", i, got)
				}
			}
			if tokens := p.Tiers[TierTail].Tokens; tokens > pr.TailBudget {
				t.Errorf("turn %d: tail has %d tokens, over the budget of %d", i, tokens, pr.TailBudget)
			}
			if msgs := p.Tiers[TierTail].Msgs; len(msgs) > 0 && msgs[0].Role != core.RoleUser {
				t.Errorf("turn %d: tail starts with %s, not at a turn boundary", i, msgs[0].Role)
			}
			log.agent(fmt.Sprintf("reply number %d, also padded out a little", i))
			prev = p
		}
		return moves
	}
	step, slide := run(false), run(true)
	t.Logf("tail start moved %d times stepping, %d times sliding, over %d turns", step, slide, turns)
	if step == 0 {
		t.Fatal("the tail never compacted; the test budget is too generous")
	}
	if slide < 3*step {
		t.Errorf("sliding moved the tail %d times vs %d for stepping; stepping should be far rarer", slide, step)
	}
}

func TestAlignsHydratedWindowToBoundary(t *testing.T) {
	t.Parallel()
	// A freshly hydrated window can begin mid-turn: here with a tool result
	// and an agent reply whose user message fell outside the window.
	log := new(logBuilder).result("c0", "stale").agent("old reply").user("new question").agent("answer").user("follow-up")
	p := mustBuild(t, nil, Input{Agent: testAgent(), Tail: log.events}, nil, t0, DefaultParams())
	tail := p.Tiers[TierTail].Msgs
	if len(tail) != 2 || tail[0].Text != "new question" {
		t.Fatalf("tail = %+v, want it to start at the first user message", tail)
	}
	if p.TailFrom != 3 {
		t.Errorf("TailFrom = %d, want 3", p.TailFrom)
	}
}

func TestRecallTierReusedVerbatimWhenSetUnchanged(t *testing.T) {
	t.Parallel()
	pr := DefaultParams()
	log := new(logBuilder).user("hello")
	in := Input{Agent: testAgent(), Tail: log.events}
	prev := mustBuild(t, nil, in, []Recalled{{ID: 4, Text: "excerpt of episode 4"}}, t0, pr)
	// A summary landed since: the rendering would differ, but the set did not
	// change, so the cached bytes are kept.
	next := mustBuild(t, prev, in, []Recalled{{ID: 4, Text: "Summary: something new"}}, t0+1000, pr)
	if next.Chain[TierRecall] != prev.Chain[TierRecall] {
		t.Error("recall tier bytes changed although the recall set did not")
	}
}

func TestChooseRecall(t *testing.T) {
	t.Parallel()
	pr := DefaultParams() // K=4, Delta=1, CacheTTL=5m
	warm := &PromptPlan{Recall: []uint32{1, 2}, BuiltAt: t0}
	now := t0 + 60_000
	sc := func(pairs ...float32) []Scored {
		out := make([]Scored, 0, len(pairs)/2)
		for i := 0; i < len(pairs); i += 2 {
			out = append(out, Scored{ID: uint32(pairs[i]), Score: pairs[i+1]})
		}
		return out
	}
	cases := []struct {
		name  string
		prev  *PromptPlan
		fresh []Scored
		now   int64
		pr    Params
		want  []uint32
	}{
		{"no previous plan takes the top K in episode order", nil, sc(9, 5, 3, 4, 7, 3, 1, 2, 5, 1), now, pr, []uint32{1, 3, 7, 9}},
		{"warm set survives a slightly better newcomer", warm, sc(8, 3.5, 1, 3, 2, 2.9), now, pr, []uint32{1, 2}},
		{"newcomer that beats all by delta displaces", warm, sc(8, 5, 1, 3, 2, 2.9), now, pr, []uint32{1, 2, 8}},
		{"member that fell out of the shortlist forces a switch", warm, sc(1, 3, 8, 2), now, pr, []uint32{1, 8}},
		{"cold cache re-picks freely", warm, sc(8, 3.5, 1, 3, 2, 2.9), t0 + pr.CacheTTL + 1, pr, []uint32{1, 2, 8}},
		{"nothing relevant recalls nothing", warm, nil, now, pr, []uint32{}},
		{"empty warm set admits the first relevant episode", &PromptPlan{BuiltAt: t0}, sc(6, 0.5), now, pr, []uint32{6}},
		{"hysteresis off re-picks every turn", warm, sc(8, 3.5, 1, 3, 2, 2.9), now, func() Params { p := pr; p.NoHysteresis = true; return p }(), []uint32{1, 2, 8}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ChooseRecall(tc.prev, tc.fresh, tc.now, tc.pr)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestCanonicalIsValidStableJSON(t *testing.T) {
	t.Parallel()
	m := core.Message{
		Role: core.RoleAssistant,
		Text: "line1\nline2\t\"q\" \\ \x01 \xff café",
		ToolCall: &core.ToolCall{
			ID: "c1", Name: "python", Code: "print('hi')",
		},
	}
	b := Canonical(&m)
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("canonical bytes are not valid JSON: %v\n%s", err, b)
	}
	if string(b) != string(Canonical(&m)) {
		t.Error("canonical bytes are not stable")
	}
	// The environment is the deployment's business, not the model's: it must
	// not leak into the hashed bytes.
	m2 := m
	call := *m.ToolCall
	call.Env = core.EnvSpec{Tier: 1, Base: "x"}
	m2.ToolCall = &call
	if string(Canonical(&m2)) != string(b) {
		t.Error("ToolCall.Env changed the canonical bytes")
	}
}

func TestPrefixMarks(t *testing.T) {
	t.Parallel()
	log := new(logBuilder).user("a").agent("b").user("c")
	p := mustBuild(t, nil, Input{Agent: testAgent(), Profile: "p", Tail: log.events}, []Recalled{{ID: 1, Text: "r"}}, t0, DefaultParams())
	marks := p.PrefixMarks()
	if got, want := marks[len(marks)-1].Tokens, p.InputTokens(); got != want {
		t.Errorf("last mark has %d tokens, plan has %d", got, want)
	}
	breakpoints := 0
	for _, m := range marks {
		if m.Breakpoint {
			breakpoints++
			if m.Tier == TierLive {
				t.Error("breakpoint in the live tier")
			}
		}
	}
	if breakpoints != 4 {
		t.Errorf("%d breakpoints, want one per cacheable tier (4)", breakpoints)
	}
}

func BenchmarkBuild(b *testing.B) {
	log := new(logBuilder)
	for i := range 200 {
		log.user(fmt.Sprintf("message %d with a realistic amount of text in it, give or take", i))
		log.agent(fmt.Sprintf("reply %d, which is usually a little longer than the question was", i))
	}
	log.user("and the newest message")
	in := Input{Agent: testAgent(), Profile: "Prefers short answers.", Tail: log.events}
	rec := []Recalled{{ID: 1, Text: strings.Repeat("recalled text ", 60)}}
	pr := DefaultParams()
	prev, err := Build(nil, in, rec, t0, pr)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Build(prev, in, rec, t0, pr); err != nil {
			b.Fatal(err)
		}
	}
}
