package messages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/plan"
)

func TestRequestGolden(t *testing.T) {
	t.Parallel()
	golden(t, "request_full.golden.json", body(t, fullPlan(t)))
}

func TestRequestIsByteStable(t *testing.T) {
	t.Parallel()
	p := fullPlan(t)
	first := body(t, p)
	if again := body(t, p); !bytes.Equal(first, again) {
		t.Errorf("same plan marshalled twice differs:\n%s\n%s", first, again)
	}
	// A plan rebuilt from the same inputs must match too.
	if rebuilt := body(t, fullPlan(t)); !bytes.Equal(first, rebuilt) {
		t.Errorf("rebuilt plan differs:\n%s\n%s", first, rebuilt)
	}
}

// breakpoints lists where a request carries cache_control, in render order.
func breakpoints(req *wireRequest) []string {
	var at []string
	for i := range req.Tools {
		if req.Tools[i].CacheControl != nil {
			at = append(at, fmt.Sprintf("tools[%d]", i))
		}
	}
	for i := range req.System {
		if req.System[i].CacheControl != nil {
			at = append(at, fmt.Sprintf("system[%d]", i))
		}
	}
	for i := range req.Messages {
		for j := range req.Messages[i].Content {
			if req.Messages[i].Content[j].CacheControl != nil {
				at = append(at, fmt.Sprintf("messages[%d].content[%d]", i, j))
			}
		}
	}
	return at
}

func TestBreakpoints(t *testing.T) {
	t.Parallel()
	firstTurn := func(t *testing.T) []core.Event {
		t.Helper()
		return []core.Event{event(t, 1, core.KindUserMsg, core.MsgBody{Text: "Hi"})}
	}
	cases := []struct {
		name string
		edit func(t *testing.T, o *planOpts)
		want []string
	}{
		{
			name: "all tiers",
			edit: func(*testing.T, *planOpts) {},
			// messages[3] is the reply that ends the tail; messages[4] opens tier 4.
			want: []string{"system[0]", "system[1]", "system[2]", "messages[3].content[0]"},
		},
		{
			name: "no profile",
			edit: func(_ *testing.T, o *planOpts) { o.profile = "" },
			want: []string{"system[0]", "system[1]", "messages[3].content[0]"},
		},
		{
			name: "no recall",
			edit: func(_ *testing.T, o *planOpts) { o.recall = nil },
			want: []string{"system[0]", "system[1]", "messages[3].content[0]"},
		},
		{
			name: "no profile or recall",
			edit: func(_ *testing.T, o *planOpts) { o.profile, o.recall = "", nil },
			want: []string{"system[0]", "messages[3].content[0]"},
		},
		{
			name: "empty tail",
			edit: func(t *testing.T, o *planOpts) { t.Helper(); o.events = firstTurn(t) },
			want: []string{"system[0]", "system[1]", "system[2]"},
		},
		{
			name: "no system prompt puts the static breakpoint on the last tool",
			edit: func(_ *testing.T, o *planOpts) {
				o.system = ""
				o.tools = []core.ToolDef{pythonTool(), {Name: "shell", Description: "Run sh."}}
			},
			want: []string{"tools[1]", "system[0]", "system[1]", "messages[3].content[0]"},
		},
		{
			name: "nothing static",
			edit: func(t *testing.T, o *planOpts) {
				t.Helper()
				*o = planOpts{events: firstTurn(t)}
			},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := fullOpts(t)
			tc.edit(t, &o)
			p := buildPlan(t, o)
			req := decode(t, body(t, p))
			if got := breakpoints(&req); !slices.Equal(got, tc.want) {
				t.Errorf("breakpoints = %v, want %v", got, tc.want)
			}

			// Tool steps inside the turn land in tier 4 and must not move or
			// add a breakpoint.
			p.AppendTail(
				core.ToolCall{ID: "toolu_09", Name: "python", Code: "1"},
				core.ToolResult{ID: "toolu_09", Output: "1"},
			)
			req = decode(t, body(t, p))
			if got := breakpoints(&req); !slices.Equal(got, tc.want) {
				t.Errorf("after a tool step: breakpoints = %v, want %v", got, tc.want)
			}
			if n := len(breakpoints(&req)); n > 4 {
				t.Errorf("%d breakpoints, the API allows 4", n)
			}

			// A route that caches by prefix on its own is sent none at all.
			if raw := bodyFor(t, autoRoute(), p); bytes.Contains(raw, []byte("cache_control")) {
				t.Errorf("a route without breakpoints was sent cache_control: %s", raw)
			}
		})
	}
}

func TestBreakpointsOffChangesNothingElse(t *testing.T) {
	t.Parallel()
	p := fullPlan(t)
	marked := testRoute()
	unmarked := testRoute()
	unmarked.Caps.Cache = llm.CacheAutoPrefix
	with, without := bodyFor(t, marked, p), bodyFor(t, unmarked, p)
	stripped := bytes.ReplaceAll(with, []byte(`,"cache_control":{"type":"ephemeral"}`), nil)
	if bytes.Equal(with, stripped) || !bytes.Equal(stripped, without) {
		t.Errorf("breakpoints changed more than their own fields:\n%s\n%s", with, without)
	}
}

func TestTierLayout(t *testing.T) {
	t.Parallel()
	req := decode(t, body(t, fullPlan(t)))

	if req.Model != "claude-opus-5-5" || req.MaxTokens != 64000 || !req.Stream {
		t.Errorf("model %q, max_tokens %d, stream %v", req.Model, req.MaxTokens, req.Stream)
	}
	if req.Fallbacks != "default" {
		t.Errorf("fallbacks = %q, want the dialect's extra field", req.Fallbacks)
	}
	if req.ToolChoice == nil || req.ToolChoice.Type != "auto" || !req.ToolChoice.DisableParallelToolUse {
		t.Errorf("tool_choice = %+v, want auto without parallel calls", req.ToolChoice)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "python" || !req.Tools[0].EagerInputStreaming {
		t.Fatalf("tools = %+v", req.Tools)
	}
	if got := string(req.Tools[0].InputSchema); got != codeSchema {
		t.Errorf("input_schema = %s, want the default code schema", got)
	}
	if len(req.System) != 3 || req.System[0].Text != "You are Ada, a careful assistant." {
		t.Fatalf("system = %+v", req.System)
	}

	type shape struct{ role, blocks string }
	got := make([]shape, 0, len(req.Messages))
	for _, m := range req.Messages {
		kinds := make([]string, len(m.Content))
		for i, b := range m.Content {
			kinds[i] = b.Type
		}
		got = append(got, shape{m.Role, strings.Join(kinds, ",")})
	}
	want := []shape{
		{"user", "text"},
		{"assistant", "text,tool_use"},
		{"user", "tool_result"},
		{"assistant", "text"},
		// The new message and the time note share one user turn. The in-turn
		// tool exchange comes after the note: it is appended, never inserted.
		{"user", "text,text"},
		{"assistant", "tool_use"},
		{"user", "tool_result"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("messages = %v, want %v", got, want)
	}
	use := req.Messages[1].Content[1]
	if use.ID != "toolu_01" || use.Name != "python" || string(use.Input) != `{"code":"print(6 * 7)"}` {
		t.Errorf("tool_use = %+v", use)
	}
	res := req.Messages[2].Content[0]
	if res.ToolUseID != "toolu_01" || res.Content != "42\n" || res.IsError {
		t.Errorf("tool_result = %+v", res)
	}
}

func TestDialectAndCapabilitiesShapeTheRequest(t *testing.T) {
	t.Parallel()
	p := fullPlan(t)

	t.Run("a bare dialect adds nothing", func(t *testing.T) {
		t.Parallel()
		raw := bodyFor(t, autoRoute(), p)
		for _, field := range []string{"fallbacks", "eager_input_streaming", "tool_choice", "thinking", "output_config", "is_error"} {
			if bytes.Contains(raw, []byte(`"`+field+`"`)) {
				t.Errorf("request carries %s: %s", field, raw)
			}
		}
		if !bytes.HasPrefix(raw, []byte(`{"model":"auto-model","max_tokens":384000,"stream":true,"tools":[`)) {
			t.Errorf("request does not open as expected: %s", raw)
		}
	})

	t.Run("extra fields follow stream in the order given", func(t *testing.T) {
		t.Parallel()
		d := &Dialect{Extra: []Field{
			{Name: "fallbacks", Value: json.RawMessage(`"default"`)},
			{Name: "metadata", Value: json.RawMessage(`{ "user_id": "<u>" }`)},
		}}
		raw := freeze(t, d, autoRoute(), p, llm.Options{}).Body
		const want = `"stream":true,"fallbacks":"default","metadata":{"user_id":"<u>"},"tools":[`
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("extra fields are not where they belong: %s", raw)
		}
		// And nothing else moved.
		if plain := bodyFor(t, autoRoute(), p); !bytes.Equal(bytes.Replace(raw, []byte(`"fallbacks":"default","metadata":{"user_id":"<u>"},`), nil, 1), plain) {
			t.Errorf("extra fields changed more than themselves:\n%s\n%s", raw, plain)
		}
		bad := &Dialect{Extra: []Field{{Name: "x", Value: json.RawMessage(`{`)}}}
		if _, err := Prepare(bad, autoRoute(), p, llm.Options{}); err == nil {
			t.Error("an extra field that is not JSON was accepted")
		}
	})

	t.Run("single tool call is asked for only where it is honoured", func(t *testing.T) {
		t.Parallel()
		r := autoRoute()
		r.Caps.SingleToolCall = true
		if raw := bodyFor(t, r, p); !bytes.Contains(raw, []byte(`"tool_choice":{"type":"auto","disable_parallel_tool_use":true}`)) {
			t.Errorf("request does not hold the model to one call: %s", raw)
		}
		noTools := fullOpts(t)
		noTools.tools = nil
		if raw := bodyFor(t, r, buildPlan(t, noTools)); bytes.Contains(raw, []byte("tool_choice")) {
			t.Errorf("tool_choice sent with no tools: %s", raw)
		}
	})

	t.Run("max_tokens", func(t *testing.T) {
		t.Parallel()
		capped, uncapped := autoRoute(), autoRoute()
		uncapped.Caps.MaxOutput = 0
		for _, tc := range []struct {
			name    string
			route   llm.Route
			dialect int
			option  int
			want    int
		}{
			{"the option wins", capped, 8192, 1234, 1234},
			{"then the dialect's default", capped, 8192, 0, 8192},
			{"then the route's limit", capped, 0, 0, 384_000},
			{"then a built-in value", uncapped, 0, 0, fallbackMaxOutput},
			{"an option above the route's limit is capped", capped, 8192, 1_000_000, 384_000},
			{"a dialect default above the route's limit is capped", capped, 500_000, 0, 384_000},
		} {
			f := freeze(t, &Dialect{DefaultMaxOutput: tc.dialect}, tc.route, p, llm.Options{MaxOutput: tc.option})
			if got := decode(t, f.Body).MaxTokens; got != tc.want {
				t.Errorf("%s: max_tokens = %d, want %d", tc.name, got, tc.want)
			}
		}
	})

	t.Run("headers", func(t *testing.T) {
		t.Parallel()
		d := testDialect("https://example.test")
		f := freeze(t, d, testRoute(), p, llm.Options{})
		if f.URL != "https://example.test/v1/messages" {
			t.Errorf("URL = %q", f.URL)
		}
		for k, want := range map[string]string{
			"X-Api-Key":         "test-key",
			"Anthropic-Version": "2023-06-01",
			"Anthropic-Beta":    "server-side-fallback-2026-07-01",
			"Content-Type":      "application/json",
			"Accept":            "text/event-stream",
		} {
			if got := f.Header.Get(k); got != want {
				t.Errorf("header %s = %q, want %q", k, got, want)
			}
		}
		// The frozen header is a copy: changing it must not reach the dialect.
		f.Header.Set("X-Api-Key", "changed")
		if d.Header.Get("X-Api-Key") != "test-key" || d.Header.Get("Content-Type") != "" {
			t.Errorf("freezing a request changed the dialect's header: %v", d.Header)
		}
	})

	t.Run("nil arguments", func(t *testing.T) {
		t.Parallel()
		if _, err := Prepare(nil, testRoute(), p, llm.Options{}); err == nil {
			t.Error("Prepare accepted a nil dialect")
		}
		if _, err := Prepare(&Dialect{}, testRoute(), nil, llm.Options{}); err == nil {
			t.Error("Prepare accepted a nil plan")
		}
	})
}

func TestAffinity(t *testing.T) {
	t.Parallel()
	const key = "k3y0fth3c0ntact"
	p := fullPlan(t)
	withHeader := &Dialect{AffinityHeader: "X-Session"}
	for _, tc := range []struct {
		name     string
		dialect  *Dialect
		affinity llm.Affinity
		key      string
		want     string
	}{
		{"sent in the dialect's header", withHeader, llm.AffinityHeader, key, key},
		{"not sent when the route declares none", withHeader, llm.AffinityNone, key, ""},
		{"not sent when the route wants it in the body", withHeader, llm.AffinityBody, key, ""},
		{"not sent without a key", withHeader, llm.AffinityHeader, "", ""},
		{"not sent when the dialect has no header for it", &Dialect{}, llm.AffinityHeader, key, ""},
	} {
		r := autoRoute()
		r.Caps.Affinity = tc.affinity
		f := freeze(t, tc.dialect, r, p, llm.Options{Affinity: tc.key})
		if got := f.Header.Get("X-Session"); got != tc.want {
			t.Errorf("%s: header = %q, want %q", tc.name, got, tc.want)
		}
		if bytes.Contains(f.Body, []byte(key)) {
			t.Errorf("%s: the key is in the body", tc.name)
		}
		if plain := bodyFor(t, r, p); !bytes.Equal(f.Body, plain) {
			t.Errorf("%s: an affinity key changed the body", tc.name)
		}
	}
}

func TestMarksAndPrediction(t *testing.T) {
	t.Parallel()
	p := buildPlan(t, fullOpts(t))
	prev := p.PrefixMarks()
	p.AppendTail(
		core.ToolCall{ID: "toolu_02", Name: "python", Code: "print(7 * 8)"},
		core.ToolResult{ID: "toolu_02", Output: "56\n"},
	)
	marks := p.PrefixMarks()
	for _, r := range []llm.Route{testRoute(), autoRoute()} {
		f := freeze(t, &Dialect{}, r, p, llm.Options{Prev: prev})
		if !slices.Equal(f.Marks, marks) {
			t.Errorf("%s: Frozen.Marks are not the plan's prefix marks", r.ID())
		}
		want := llm.PredictCached(r.Caps.Cache, prev, marks)
		if f.Predict != want || want == 0 {
			t.Errorf("%s: Predict = %d, want %d (and not 0)", r.ID(), f.Predict, want)
		}
		if cold := freeze(t, &Dialect{}, r, p, llm.Options{}); cold.Predict != 0 {
			t.Errorf("%s: Predict = %d with no previous request", r.ID(), cold.Predict)
		}
		if f.Route.ID() != r.ID() {
			t.Errorf("Frozen.Route = %s, want %s", f.Route.ID(), r.ID())
		}
	}
}

func TestMergeSameRole(t *testing.T) {
	t.Parallel()

	t.Run("orphaned tool result and the next user message", func(t *testing.T) {
		t.Parallel()
		// The tool never answered and the contact wrote again: the planner
		// renders a placeholder result, then the message. Both are user
		// turns on the wire, and the result is where the tail ends.
		p := buildPlan(t, planOpts{
			system: "sys",
			tools:  []core.ToolDef{pythonTool()},
			events: []core.Event{
				event(t, 1, core.KindUserMsg, core.MsgBody{Text: "Run it"}),
				event(t, 2, core.KindToolCall, core.ToolCall{ID: "toolu_01", Name: "python", Code: "while True: pass"}),
				event(t, 3, core.KindUserMsg, core.MsgBody{Text: "Still there?"}),
			},
		})
		req := decode(t, body(t, p))
		if len(req.Messages) != 3 {
			t.Fatalf("got %d messages, want 3: %+v", len(req.Messages), req.Messages)
		}
		last := req.Messages[2]
		if last.Role != "user" || len(last.Content) != 3 {
			t.Fatalf("last message = %+v", last)
		}
		if last.Content[0].Type != "tool_result" || !last.Content[0].IsError || last.Content[0].CacheControl == nil {
			t.Errorf("first block = %+v, want the error result carrying the tail breakpoint", last.Content[0])
		}
		if last.Content[1].Text != "Still there?" || last.Content[1].CacheControl != nil {
			t.Errorf("second block = %+v", last.Content[1])
		}
		if last.Content[2].Type != "text" || last.Content[2].CacheControl != nil {
			t.Errorf("third block = %+v, want the time note", last.Content[2])
		}
	})

	t.Run("consecutive user messages", func(t *testing.T) {
		t.Parallel()
		p := buildPlan(t, planOpts{events: []core.Event{
			event(t, 1, core.KindUserMsg, core.MsgBody{Text: "one"}),
			event(t, 2, core.KindUserMsg, core.MsgBody{Text: "two"}),
		}})
		req := decode(t, body(t, p))
		if len(req.Messages) != 1 || len(req.Messages[0].Content) != 3 {
			t.Fatalf("messages = %+v, want one user turn with three text blocks", req.Messages)
		}
	})

	t.Run("results move ahead of text only within a tier", func(t *testing.T) {
		t.Parallel()
		res := func(id string) *core.Message {
			return &core.Message{Role: core.RoleTool, ToolResult: &core.ToolResult{ID: id}}
		}
		var b builder
		add(t, &b, &core.Message{Role: core.RoleUser, Text: "tail text"})
		b.endTier()
		add(t, &b, &core.Message{Role: core.RoleUser, Text: "live text"})
		add(t, &b, res("a"))
		add(t, &b, res("b"))
		if len(b.msgs) != 1 {
			t.Fatalf("got %d messages, want 1", len(b.msgs))
		}
		got := make([]string, 0, len(b.msgs[0].Content))
		for _, bl := range b.msgs[0].Content {
			got = append(got, bl.Type+":"+bl.Text+bl.ToolUseID)
		}
		want := []string{"text:tail text", "tool_result:a", "tool_result:b", "text:live text"}
		if !slices.Equal(got, want) {
			t.Errorf("blocks = %v, want %v", got, want)
		}
	})

	t.Run("empty text is dropped", func(t *testing.T) {
		t.Parallel()
		var b builder
		add(t, &b, &core.Message{Role: core.RoleUser, Text: "a"})
		add(t, &b, &core.Message{Role: core.RoleAssistant, Text: " \n"})
		add(t, &b, &core.Message{Role: core.RoleUser, Text: "b"})
		if len(b.msgs) != 1 || len(b.msgs[0].Content) != 2 {
			t.Errorf("messages = %+v, want the two user texts merged", b.msgs)
		}
	})
}

func add(t *testing.T, b *builder, m *core.Message) {
	t.Helper()
	if err := b.add(m); err != nil {
		t.Fatalf("add: %v", err)
	}
}

func TestTailGrowthKeepsPrefix(t *testing.T) {
	t.Parallel()
	// The next turn's request must start with every byte the previous
	// breakpoints covered, apart from the tail marker moving forward.
	o := fullOpts(t)
	prev := buildPlan(t, o)

	o.events = append(o.events,
		event(t, 6, core.KindAgentMsg, core.MsgBody{Text: "56."}),
		event(t, 7, core.KindUserMsg, core.MsgBody{Text: "Thanks"}),
	)
	in := plan.Input{Agent: &core.AgentConfig{System: o.system, Tools: o.tools}, Profile: o.profile, Tail: o.events}
	next, err := plan.Build(prev, in, o.recall, fixedNow+60_000, plan.DefaultParams())
	if err != nil {
		t.Fatal(err)
	}

	// Tier 3 of the earlier plan is its first four messages.
	const marker = `,"cache_control":{"type":"ephemeral"}`
	a, b := rawMessages(t, body(t, prev))[:4], rawMessages(t, body(t, next))[:4]
	for i := range a {
		if strings.ReplaceAll(a[i], marker, "") != strings.ReplaceAll(b[i], marker, "") {
			t.Errorf("message %d of the cached prefix changed between turns:\n%s\n%s", i, a[i], b[i])
		}
	}
	// On a route with no markers the prefix is the same bytes outright.
	a, b = rawMessages(t, bodyFor(t, autoRoute(), prev))[:4], rawMessages(t, bodyFor(t, autoRoute(), next))[:4]
	if !slices.Equal(a, b) {
		t.Errorf("cached prefix changed between turns:\n%s\n%s", a, b)
	}
}

func TestEffort(t *testing.T) {
	t.Parallel()
	p := fullPlan(t)

	t.Run("the default sends nothing", func(t *testing.T) {
		t.Parallel()
		for _, r := range []llm.Route{testRoute(), autoRoute()} {
			f := freeze(t, &Dialect{}, r, p, llm.Options{})
			if f.Effort != llm.EffortDefault {
				t.Errorf("%s: Frozen.Effort = %q with none requested", r.ID(), f.Effort)
			}
			for _, field := range []string{`"output_config"`, `"thinking"`} {
				if bytes.Contains(f.Body, []byte(field)) {
					t.Errorf("%s: request sets %s with no effort requested: %s", r.ID(), field, f.Body)
				}
			}
		}
	})

	t.Run("an effort is one field and nothing else moves", func(t *testing.T) {
		t.Parallel()
		f := freeze(t, testDialect(""), testRoute(), p, llm.Options{Effort: llm.EffortLow})
		if f.Effort != llm.EffortLow {
			t.Errorf("Frozen.Effort = %q, want low", f.Effort)
		}
		const field = `"output_config":{"effort":"low"},`
		if !bytes.Contains(f.Body, []byte(`"fallbacks":"default",`+field+`"tools":[`)) {
			t.Errorf("request does not carry the effort where it belongs: %s", f.Body)
		}
		if without := bytes.Replace(f.Body, []byte(field), nil, 1); !bytes.Equal(without, body(t, p)) {
			t.Errorf("effort changed more than its own field:\n%s\n%s", without, body(t, p))
		}
	})

	t.Run("the nearest accepted effort is what is sent", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			route llm.Route
			ask   llm.Effort
			want  llm.Effort
			field string
		}{
			{testRoute(), llm.EffortXHigh, llm.EffortXHigh, `"output_config":{"effort":"xhigh"}`},
			{testRoute(), llm.EffortNone, llm.EffortLow, `"output_config":{"effort":"low"}`},
			{autoRoute(), llm.EffortMedium, llm.EffortHigh, `"output_config":{"effort":"high"}`},
			{autoRoute(), llm.EffortMax, llm.EffortMax, `"output_config":{"effort":"max"}`},
			{autoRoute(), llm.EffortNone, llm.EffortNone, `"thinking":{"type":"disabled"}`},
		} {
			f := freeze(t, &Dialect{}, tc.route, p, llm.Options{Effort: tc.ask})
			if f.Effort != tc.want || !bytes.Contains(f.Body, []byte(tc.field)) {
				t.Errorf("%s asked for %q: froze %q, body %s; want %q and %s", tc.route.ID(), tc.ask, f.Effort, f.Body, tc.want, tc.field)
			}
		}
		// Switching reasoning off says nothing about an effort.
		if f := freeze(t, &Dialect{}, autoRoute(), p, llm.Options{Effort: llm.EffortNone}); bytes.Contains(f.Body, []byte("output_config")) {
			t.Errorf("reasoning off also set an effort: %s", f.Body)
		}
	})

	t.Run("a route with no effort control ignores the request", func(t *testing.T) {
		t.Parallel()
		r := autoRoute()
		r.Caps.Efforts, r.Caps.ReasoningOff = nil, false
		base := bodyFor(t, r, p)
		for e := llm.EffortNone; e <= llm.EffortMax; e++ {
			f := freeze(t, &Dialect{}, r, p, llm.Options{Effort: e})
			if f.Effort != llm.EffortDefault || !bytes.Equal(f.Body, base) {
				t.Errorf("effort %q changed a request on a route with no control", e)
			}
		}
	})

	t.Run("a dialect spells efforts its own way", func(t *testing.T) {
		t.Parallel()
		d := &Dialect{Effort: func(e llm.Effort) EffortFields {
			if e == llm.EffortMax {
				return EffortFields{} // this upstream has no word for it
			}
			return EffortFields{Thinking: json.RawMessage(`{"type": "enabled", "level": "` + e.String() + `"}`)}
		}}
		f := freeze(t, d, autoRoute(), p, llm.Options{Effort: llm.EffortLow})
		if f.Effort != llm.EffortLow || !bytes.Contains(f.Body, []byte(`"stream":true,"thinking":{"type":"enabled","level":"low"},"tools":[`)) {
			t.Errorf("froze %q: %s", f.Effort, f.Body)
		}
		// What cannot be spelled is not sent, and the record says so.
		f = freeze(t, d, autoRoute(), p, llm.Options{Effort: llm.EffortMax})
		if f.Effort != llm.EffortDefault || !bytes.Equal(f.Body, bodyFor(t, autoRoute(), p)) {
			t.Errorf("an effort with no spelling froze as %q: %s", f.Effort, f.Body)
		}
	})
}

func TestToolErrorFlag(t *testing.T) {
	t.Parallel()
	p := buildPlan(t, fullOpts(t))
	p.AppendTail(
		core.ToolCall{ID: "toolu_02", Name: "python", Code: "1/0"},
		core.ToolResult{ID: "toolu_02", Output: "ZeroDivisionError: division by zero", IsError: true},
	)
	p.AppendTail(
		core.ToolCall{ID: "toolu_03", Name: "python", Code: "print(1)"},
		core.ToolResult{ID: "toolu_03", Output: "1\n"},
	)

	const flagged = `{"type":"tool_result","tool_use_id":"toolu_02","content":"ZeroDivisionError: division by zero","is_error":true}`
	if raw := bodyFor(t, testRoute(), p); !bytes.Contains(raw, []byte(flagged)) {
		t.Errorf("a failed result is not flagged on a route with the flag: %s", raw)
	}

	// Without the flag the failure is written into the text, and a result
	// that did not fail is left as it is.
	raw := bodyFor(t, autoRoute(), p)
	const written = `{"type":"tool_result","tool_use_id":"toolu_02","content":"[tool error]\nZeroDivisionError: division by zero"}`
	const fine = `{"type":"tool_result","tool_use_id":"toolu_03","content":"1\n"}`
	if !bytes.Contains(raw, []byte(written)) || !bytes.Contains(raw, []byte(fine)) || bytes.Contains(raw, []byte("is_error")) {
		t.Errorf("results on a route without the flag: %s", raw)
	}
}

func TestToolSchemas(t *testing.T) {
	t.Parallel()
	o := fullOpts(t)
	o.tools = []core.ToolDef{pythonTool(), lookupTool()}
	p := buildPlan(t, o)
	p.AppendTail(
		core.ToolCall{ID: "toolu_02", Name: "lookup", Args: json.RawMessage(`{"word": "caf\u00e9 <b>", "limit": 3}`)},
		core.ToolResult{ID: "toolu_02", Output: "a place"},
	)
	p.AppendTail(
		core.ToolCall{ID: "toolu_03", Name: "python", Code: "print(1)"},
		core.ToolResult{ID: "toolu_03", Output: "1\n"},
	)
	// A call whose arguments were lost still renders as a valid request.
	p.AppendTail(
		core.ToolCall{ID: "toolu_04", Name: "lookup", Args: json.RawMessage(`{"word": `)},
		core.ToolResult{ID: "toolu_04", Output: "bad arguments", IsError: true},
	)
	raw := body(t, p)
	req := decode(t, raw)

	if got := string(req.Tools[0].InputSchema); got != codeSchema {
		t.Errorf("a tool with no schema got %s, want the code schema", got)
	}
	// The declared schema goes out as written, but for insignificant space.
	const schema = `{"type":"object","properties":{"word":{"type":"string"},"limit":{"type":"integer"}},"required":["word"]}`
	if got := string(req.Tools[1].InputSchema); got != schema {
		t.Errorf("input_schema = %s, want %s", got, schema)
	}
	for _, want := range []string{
		`{"type":"tool_use","id":"toolu_02","name":"lookup","input":{"word":"caf\u00e9 <b>","limit":3}}`,
		`{"type":"tool_use","id":"toolu_03","name":"python","input":{"code":"print(1)"}}`,
		`{"type":"tool_use","id":"toolu_04","name":"lookup","input":{}}`,
	} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("request lacks %s:\n%s", want, raw)
		}
	}

	bad := fullOpts(t)
	bad.tools = []core.ToolDef{{Name: "broken", Input: json.RawMessage(`{"type": `)}}
	if _, err := Prepare(&Dialect{}, testRoute(), buildPlan(t, bad), llm.Options{}); err == nil {
		t.Error("a tool schema that is not JSON was accepted")
	}
}

func TestSiblingCalls(t *testing.T) {
	t.Parallel()
	// The model made three calls in one step. They were run and logged one
	// at a time; the wire has to put them back into one assistant message
	// and one user message.
	calls := []core.ToolCall{
		{ID: "toolu_a", Name: "python", Code: "a = 1", Text: "Three things."},
		{ID: "toolu_b", Name: "lookup", Args: json.RawMessage(`{"word":"b"}`), Sibling: 1},
		{ID: "toolu_c", Name: "python", Code: "c = 3", Sibling: 2},
	}
	results := []core.ToolResult{
		{ID: "toolu_a", Output: "ok a"},
		{ID: "toolu_b", Output: "no such word", IsError: true},
		{ID: "toolu_c", Output: "ok c"},
	}
	const assistant = `{"role":"assistant","content":[{"type":"text","text":"Three things."},` +
		`{"type":"tool_use","id":"toolu_a","name":"python","input":{"code":"a = 1"}},` +
		`{"type":"tool_use","id":"toolu_b","name":"lookup","input":{"word":"b"}},` +
		`{"type":"tool_use","id":"toolu_c","name":"python","input":{"code":"c = 3"}}]}`
	const user = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_a","content":"ok a"},` +
		`{"type":"tool_result","tool_use_id":"toolu_b","content":"no such word","is_error":true},` +
		`{"type":"tool_result","tool_use_id":"toolu_c","content":"ok c"}]}`

	t.Run("in the live tier", func(t *testing.T) {
		t.Parallel()
		o := fullOpts(t)
		o.tools = []core.ToolDef{pythonTool(), lookupTool()}
		p := buildPlan(t, o)
		before := rawMessages(t, body(t, p))
		for i := range calls {
			p.AppendTail(calls[i], results[i])
		}
		after := rawMessages(t, body(t, p))
		if len(after) != len(before)+2 || !slices.Equal(after[:len(before)], before) {
			t.Fatalf("the step did not append exactly two messages:\n%s", strings.Join(after, "\n"))
		}
		if got := after[len(before)]; got != assistant {
			t.Errorf("assistant message\n got: %s\nwant: %s", got, assistant)
		}
		if got := after[len(before)+1]; got != user {
			t.Errorf("user message\n got: %s\nwant: %s", got, user)
		}

		// A new step afterwards starts a message of its own.
		p.AppendTail(
			core.ToolCall{ID: "toolu_d", Name: "python", Code: "d = 4"},
			core.ToolResult{ID: "toolu_d", Output: "ok d"},
		)
		next := rawMessages(t, body(t, p))
		if len(next) != len(after)+2 || !slices.Equal(next[:len(after)], after) {
			t.Errorf("a later step rewrote the sibling step:\n%s", strings.Join(next, "\n"))
		}
	})

	t.Run("in the tail, read back from the log", func(t *testing.T) {
		t.Parallel()
		events := make([]core.Event, 0, 2*len(calls)+3)
		events = append(events, event(t, 1, core.KindUserMsg, core.MsgBody{Text: "Do three things."}))
		for i := range calls {
			events = append(events,
				event(t, uint64(2+2*i), core.KindToolCall, calls[i]),
				event(t, uint64(3+2*i), core.KindToolResult, results[i]),
			)
		}
		events = append(events,
			event(t, 8, core.KindAgentMsg, core.MsgBody{Text: "Done."}),
			event(t, 9, core.KindUserMsg, core.MsgBody{Text: "Thanks"}),
		)
		p := buildPlan(t, planOpts{system: "sys", tools: []core.ToolDef{pythonTool(), lookupTool()}, events: events})
		raw := body(t, p)
		msgs := rawMessages(t, raw)
		if len(msgs) != 5 || msgs[1] != assistant || msgs[2] != user {
			t.Errorf("messages:\n%s", strings.Join(msgs, "\n"))
		}
		req := decode(t, raw)
		if got, want := breakpoints(&req), []string{"system[0]", "messages[3].content[0]"}; !slices.Equal(got, want) {
			t.Errorf("breakpoints = %v, want %v", got, want)
		}
	})

	t.Run("a message written while the tools ran follows every result", func(t *testing.T) {
		t.Parallel()
		var b builder
		add(t, &b, &core.Message{Role: core.RoleUser, Text: "go"})
		add(t, &b, &core.Message{Role: core.RoleAssistant, ToolCall: &calls[0]})
		add(t, &b, &core.Message{Role: core.RoleTool, ToolResult: &results[0]})
		add(t, &b, &core.Message{Role: core.RoleUser, Text: "hurry"})
		add(t, &b, &core.Message{Role: core.RoleAssistant, ToolCall: &calls[2]})
		add(t, &b, &core.Message{Role: core.RoleTool, ToolResult: &results[2]})
		if len(b.msgs) != 3 {
			t.Fatalf("got %d messages, want 3", len(b.msgs))
		}
		got := make([]string, 0, len(b.msgs[2].Content))
		for _, bl := range b.msgs[2].Content {
			got = append(got, bl.Type+":"+bl.Text+bl.ToolUseID)
		}
		if want := []string{"tool_result:toolu_a", "tool_result:toolu_c", "text:hurry"}; !slices.Equal(got, want) {
			t.Errorf("user message = %v, want %v", got, want)
		}
	})

	t.Run("a sibling with no step to join stands alone", func(t *testing.T) {
		t.Parallel()
		var b builder
		add(t, &b, &core.Message{Role: core.RoleUser, Text: "go"})
		add(t, &b, &core.Message{Role: core.RoleAssistant, ToolCall: &calls[1]})
		add(t, &b, &core.Message{Role: core.RoleTool, ToolResult: &results[1]})
		if len(b.msgs) != 3 || b.msgs[1].Role != roleAssistant || len(b.msgs[1].Content) != 1 {
			t.Errorf("messages = %+v", b.msgs)
		}
	})
}

// The reasoning blocks of thinking_tool.sse as they arrive: one with the
// empty text an upstream that hides reasoning sends, one with visible text,
// and one that comes whole.
const (
	thinking1 = `{"type":"thinking","thinking":"","signature":"EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pkiMOYds+/=="}`
	thinking2 = `{"type":"thinking","thinking":"Multiplying 7 by 8 with a <script> & \"quotes\".","signature":"ErUBCkYIBxgCIkD9second"}`
	redacted  = `{"type":"redacted_thinking","data":"EmwKAhgBEgy3va3pzix/LafPsn4aDFIT2Xlxh0L5L8rLVyIw"}`

	toolSlot = `{"tool":true}`
)

// payloadOf writes a reasoning payload from its slots.
func payloadOf(slots ...string) []byte {
	return []byte(`{"v":1,"turn":[` + strings.Join(slots, ",") + `]}`)
}

func blockSlot(raw string) string { return `{"block":` + raw + `}` }
func textSlot(n int) string       { return fmt.Sprintf(`{"text":%d}`, n) }

func TestReasoningReplay(t *testing.T) {
	t.Parallel()
	route := testRoute()
	own := func(slots ...string) *core.Reasoning {
		return &core.Reasoning{Route: route.ID(), Data: payloadOf(slots...)}
	}
	// The assistant turn a step leaves in the request, as written.
	turn := func(t *testing.T, text string, r *core.Reasoning) string {
		t.Helper()
		p := buildPlan(t, fullOpts(t))
		p.AppendTail(
			core.ToolCall{ID: "toolu_02", Name: "python", Code: "print(7 * 8)", Text: text, Reasoning: r},
			core.ToolResult{ID: "toolu_02", Output: "56\n"},
		)
		msgs := rawMessages(t, body(t, p))
		return msgs[len(msgs)-2]
	}
	content := func(blocks ...string) string {
		return `{"role":"assistant","content":[` + strings.Join(blocks, ",") + `]}`
	}
	const (
		onIt = `{"type":"text","text":"On it."}`
		use  = `{"type":"tool_use","id":"toolu_02","name":"python","input":{"code":"print(7 * 8)"}}`
	)
	bare := content(onIt, use)
	if got := turn(t, "On it.", nil); got != bare {
		t.Fatalf("a call without reasoning:\n got: %s\nwant: %s", got, bare)
	}

	t.Run("sent back to its own route in the order it arrived", func(t *testing.T) {
		t.Parallel()
		// thinking, text, thinking, redacted thinking, call: the text stays
		// between the blocks it was written between.
		r := own(blockSlot(thinking1), textSlot(6), blockSlot(thinking2), blockSlot(redacted), toolSlot)
		want := content(thinking1, onIt, thinking2, redacted, use)
		got := turn(t, "On it.", r)
		if got != want {
			t.Errorf("replayed turn\n got: %s\nwant: %s", got, want)
		}
		if again := turn(t, "On it.", r); again != got {
			t.Error("replaying a payload is not deterministic")
		}
	})

	t.Run("never sent to another route", func(t *testing.T) {
		t.Parallel()
		r := &core.Reasoning{Route: "testprov/another-model", Data: payloadOf(blockSlot(thinking1), textSlot(6), toolSlot)}
		if got := turn(t, "On it.", r); got != bare {
			t.Errorf("a payload tagged for another route changed the request: %s", got)
		}
	})

	t.Run("read from the message, not from the call", func(t *testing.T) {
		t.Parallel()
		p := buildPlan(t, fullOpts(t))
		p.AppendTail(
			core.ToolCall{ID: "toolu_02", Name: "python", Code: "print(7 * 8)", Text: "On it.", Reasoning: own(blockSlot(thinking1), textSlot(6), toolSlot)},
			core.ToolResult{ID: "toolu_02", Output: "56\n"},
		)
		live := &p.Tiers[plan.TierLive]
		live.Msgs[len(live.Msgs)-2].Reasoning = nil
		if msgs := rawMessages(t, body(t, p)); msgs[len(msgs)-2] != bare {
			t.Errorf("reasoning left only on the tool call was sent: %s", msgs[len(msgs)-2])
		}
	})

	t.Run("a payload this codec did not write is left out", func(t *testing.T) {
		t.Parallel()
		for name, data := range map[string]string{
			"empty":               ``,
			"a bare array":        `[` + thinking1 + `]`,
			"another version":     `{"v":2,"turn":[` + blockSlot(thinking1) + `,` + toolSlot + `]}`,
			"no version":          `{"turn":[` + blockSlot(thinking1) + `,` + toolSlot + `]}`,
			"an empty turn":       `{"v":1,"turn":[]}`,
			"an untyped block":    `{"v":1,"turn":[{"block":{"thinking":"x"}}]}`,
			"a slot of two kinds": `{"v":1,"turn":[{"block":` + thinking1 + `,"tool":true}]}`,
			"a slot of no kind":   `{"v":1,"turn":[{}]}`,
			"a negative length":   `{"v":1,"turn":[` + blockSlot(thinking1) + `,{"text":-6}]}`,
			"truncated":           `{"v":1,"turn":[` + blockSlot(thinking1),
			"a string":            `"reasoning text"`,
		} {
			r := &core.Reasoning{Route: route.ID(), Data: []byte(data)}
			if got := turn(t, "On it.", r); got != bare {
				t.Errorf("%s: the request changed: %s", name, got)
			}
		}
	})

	t.Run("text streamed as several blocks is cut the same way", func(t *testing.T) {
		t.Parallel()
		r := own(blockSlot(thinking1), textSlot(3), blockSlot(thinking2), textSlot(3), toolSlot)
		want := content(thinking1, `{"type":"text","text":"On "}`, thinking2, `{"type":"text","text":"it."}`, use)
		if got := turn(t, "On it.", r); got != want {
			t.Errorf("replayed turn\n got: %s\nwant: %s", got, want)
		}
		// A blank block is counted, so the cut holds, but it is not sent.
		r = own(blockSlot(thinking1), textSlot(2), blockSlot(thinking2), textSlot(6), toolSlot)
		want = content(thinking1, thinking2, onIt, use)
		if got := turn(t, "\n\nOn it.", r); got != want {
			t.Errorf("a blank text block\n got: %s\nwant: %s", got, want)
		}
	})

	t.Run("text that is not what was streamed goes out whole where text began", func(t *testing.T) {
		t.Parallel()
		// The recorded lengths no longer add up to the text. It cannot be
		// cut, so it is one block after the reasoning that preceded text.
		r := own(blockSlot(thinking1), textSlot(20), blockSlot(thinking2), textSlot(4), toolSlot)
		want := content(thinking1, onIt, thinking2, use)
		if got := turn(t, "On it.", r); got != want {
			t.Errorf("replayed turn\n got: %s\nwant: %s", got, want)
		}
		// With no text at all, the text slots are empty.
		if got, want := turn(t, "", r), content(thinking1, thinking2, use); got != want {
			t.Errorf("a turn whose text is gone\n got: %s\nwant: %s", got, want)
		}
		// Text where none was recorded goes ahead of the call.
		r = own(blockSlot(thinking1), blockSlot(thinking2), toolSlot)
		if got, want := turn(t, "On it.", r), content(thinking1, thinking2, onIt, use); got != want {
			t.Errorf("text the turn did not record\n got: %s\nwant: %s", got, want)
		}
		// A call where none was recorded closes the turn.
		r = own(blockSlot(thinking1), textSlot(6))
		if got, want := turn(t, "On it.", r), content(thinking1, onIt, use); got != want {
			t.Errorf("a call the turn did not record\n got: %s\nwant: %s", got, want)
		}
	})

	t.Run("replayed on every turn where the log keeps it", func(t *testing.T) {
		t.Parallel()
		// On a route that wants reasoning back for good, the planner renders
		// it into the tail: for a tool call and for a plain reply alike.
		r := autoRoute()
		think := func(sig string) string { return `{"type":"thinking","thinking":"hm","signature":"` + sig + `"}` }
		events := []core.Event{
			event(t, 1, core.KindUserMsg, core.MsgBody{Text: "What is 6 times 7?"}),
			event(t, 2, core.KindToolCall, core.ToolCall{
				ID: "call_1", Name: "python", Code: "print(6 * 7)",
				Reasoning: &core.Reasoning{Route: r.ID(), Data: payloadOf(blockSlot(think("s1")), toolSlot)},
			}),
			event(t, 3, core.KindToolResult, core.ToolResult{ID: "call_1", Output: "42\n"}),
			event(t, 4, core.KindAgentMsg, core.MsgBody{
				Text:      "It is 42.",
				Reasoning: &core.Reasoning{Route: r.ID(), Data: payloadOf(blockSlot(think("s2")), textSlot(9))},
			}),
			event(t, 5, core.KindUserMsg, core.MsgBody{Text: "Thanks"}),
		}
		o := planOpts{system: "sys", tools: []core.ToolDef{pythonTool()}, events: events, route: r.ID()}
		msgs := rawMessages(t, bodyFor(t, r, buildPlan(t, o)))
		want := []string{
			content(think("s1"), `{"type":"tool_use","id":"call_1","name":"python","input":{"code":"print(6 * 7)"}}`),
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"42\n"}]}`,
			content(think("s2"), `{"type":"text","text":"It is 42."}`),
		}
		if len(msgs) != 5 || !slices.Equal(msgs[1:4], want) {
			t.Errorf("messages:\n%s", strings.Join(msgs, "\n"))
		}

		// With breakpoints, the tail's marker goes on the reply's text, and
		// the reasoning block ahead of it is untouched.
		marked := testRoute()
		marked.Provider, marked.Model = r.Provider, r.Model
		msgs = rawMessages(t, bodyFor(t, marked, buildPlan(t, o)))
		reply := content(think("s2"), `{"type":"text","text":"It is 42.","cache_control":{"type":"ephemeral"}}`)
		if len(msgs) != 5 || msgs[3] != reply {
			t.Errorf("messages:\n%s", strings.Join(msgs, "\n"))
		}
	})

	t.Run("a tail that ends on a reasoning block takes no breakpoint", func(t *testing.T) {
		t.Parallel()
		p := buildPlan(t, fullOpts(t))
		tail := &p.Tiers[plan.TierTail]
		tail.Msgs = append(slices.Clip(tail.Msgs),
			core.Message{Role: core.RoleUser, Text: "Go on."},
			core.Message{Role: core.RoleAssistant, Reasoning: own(blockSlot(thinking1), blockSlot(thinking2), blockSlot(redacted))},
		)
		raw := body(t, p)
		if !bytes.Contains(raw, []byte(content(thinking1, thinking2, redacted))) {
			t.Errorf("reasoning blocks were altered: %s", raw)
		}
		req := decode(t, raw)
		if got, want := breakpoints(&req), []string{"system[0]", "system[1]", "system[2]"}; !slices.Equal(got, want) {
			t.Errorf("breakpoints = %v, want %v", got, want)
		}
	})
}

func TestSiblingCallsWithReasoning(t *testing.T) {
	t.Parallel()
	// The model reasoned between its calls: thinking, text, call A, more
	// thinking, calls B and C. Each call arrives as a message of its own and
	// has to land in the slot the turn recorded for it.
	route := testRoute()
	r := &core.Reasoning{Route: route.ID(), Data: payloadOf(
		blockSlot(thinking1), textSlot(13), toolSlot, blockSlot(thinking2), toolSlot, toolSlot,
	)}
	calls := []core.ToolCall{
		{ID: "toolu_a", Name: "python", Code: "a = 1", Text: "Three things.", Reasoning: r},
		{ID: "toolu_b", Name: "python", Code: "b = 2", Sibling: 1},
		{ID: "toolu_c", Name: "python", Code: "c = 3", Sibling: 2},
	}
	use := func(id, code string) string {
		return `{"type":"tool_use","id":"toolu_` + id + `","name":"python","input":{"code":"` + code + `"}}`
	}
	result := func(id string) string {
		return `{"type":"tool_result","tool_use_id":"toolu_` + id + `","content":"ok ` + id + `"}`
	}
	const said = `{"type":"text","text":"Three things."}`
	a, b, c := use("a", "a = 1"), use("b", "b = 2"), use("c", "c = 3")
	message := func(role string, blocks ...string) string {
		return `{"role":"` + role + `","content":[` + strings.Join(blocks, ",") + `]}`
	}
	// step renders the turn with only its first n calls run.
	step := func(t *testing.T, calls []core.ToolCall) (assistant, user string) {
		t.Helper()
		p := buildPlan(t, fullOpts(t))
		for i := range calls {
			id := strings.TrimPrefix(calls[i].ID, "toolu_")
			p.AppendTail(calls[i], core.ToolResult{ID: calls[i].ID, Output: "ok " + id})
		}
		msgs := rawMessages(t, body(t, p))
		return msgs[len(msgs)-2], msgs[len(msgs)-1]
	}

	t.Run("every call in its recorded slot", func(t *testing.T) {
		t.Parallel()
		assistant, user := step(t, calls)
		if want := message("assistant", thinking1, said, a, thinking2, b, c); assistant != want {
			t.Errorf("assistant message\n got: %s\nwant: %s", assistant, want)
		}
		if want := message("user", result("a"), result("b"), result("c")); user != want {
			t.Errorf("user message\n got: %s\nwant: %s", user, want)
		}
	})

	t.Run("a step cut short keeps only the calls that were run", func(t *testing.T) {
		t.Parallel()
		// The turn was interrupted between sibling calls, so the log holds
		// fewer calls than the model made. The slots nothing fills are
		// dropped; a call with no result would not be a valid request.
		assistant, user := step(t, calls[:2])
		if want := message("assistant", thinking1, said, a, thinking2, b); assistant != want {
			t.Errorf("two of three calls: assistant message\n got: %s\nwant: %s", assistant, want)
		}
		if want := message("user", result("a"), result("b")); user != want {
			t.Errorf("two of three calls: user message\n got: %s\nwant: %s", user, want)
		}

		assistant, user = step(t, calls[:1])
		if want := message("assistant", thinking1, said, a, thinking2); assistant != want {
			t.Errorf("one of three calls: assistant message\n got: %s\nwant: %s", assistant, want)
		}
		if want := message("user", result("a")); user != want {
			t.Errorf("one of three calls: user message\n got: %s\nwant: %s", user, want)
		}
	})

	t.Run("a call beyond the recorded slots closes the message", func(t *testing.T) {
		t.Parallel()
		extra := append(slices.Clone(calls), core.ToolCall{ID: "toolu_d", Name: "python", Code: "d = 4", Sibling: 3})
		assistant, _ := step(t, extra)
		if want := message("assistant", thinking1, said, a, thinking2, b, c, use("d", "d = 4")); assistant != want {
			t.Errorf("assistant message\n got: %s\nwant: %s", assistant, want)
		}
	})

	t.Run("the next step does not inherit the slots", func(t *testing.T) {
		t.Parallel()
		// Two slots of the first step stay open. A later step with siblings
		// of its own, and no reasoning, must not fall into them.
		p := buildPlan(t, fullOpts(t))
		p.AppendTail(calls[0], core.ToolResult{ID: "toolu_a", Output: "ok a"})
		p.AppendTail(core.ToolCall{ID: "toolu_x", Name: "python", Code: "x"}, core.ToolResult{ID: "toolu_x", Output: "ok x"})
		p.AppendTail(core.ToolCall{ID: "toolu_y", Name: "python", Code: "y", Sibling: 1}, core.ToolResult{ID: "toolu_y", Output: "ok y"})
		msgs := rawMessages(t, body(t, p))
		if got, want := msgs[len(msgs)-4], message("assistant", thinking1, said, a, thinking2); got != want {
			t.Errorf("first step\n got: %s\nwant: %s", got, want)
		}
		if got, want := msgs[len(msgs)-2], message("assistant", use("x", "x"), use("y", "y")); got != want {
			t.Errorf("second step\n got: %s\nwant: %s", got, want)
		}
	})
}
