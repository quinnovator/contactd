package chat

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/plan"
)

func TestRequestGolden(t *testing.T) {
	t.Parallel()
	golden(t, "request_full.golden.json", body(t, plainDialect(), plainRoute(), fullPlan(t)))
}

func TestRequestIsByteStable(t *testing.T) {
	t.Parallel()
	p := fullPlan(t)
	first := body(t, plainDialect(), plainRoute(), p)
	if again := body(t, plainDialect(), plainRoute(), p); !bytes.Equal(first, again) {
		t.Errorf("same plan frozen twice differs:\n%s\n%s", first, again)
	}
	// A plan rebuilt from the same inputs must match too.
	if rebuilt := body(t, plainDialect(), plainRoute(), fullPlan(t)); !bytes.Equal(first, rebuilt) {
		t.Errorf("rebuilt plan differs:\n%s\n%s", first, rebuilt)
	}
}

func TestFrozen(t *testing.T) {
	t.Parallel()
	p := fullPlan(t)
	d := plainDialect()
	f := freeze(t, d, plainRoute(), p, llm.Options{})
	if f.URL != d.URL || f.Route.ID() != "openai/gpt-test" || f.Effort != llm.EffortDefault {
		t.Errorf("frozen = %q for %s with effort %q", f.URL, f.Route.ID(), f.Effort)
	}
	for k, want := range map[string]string{
		"Authorization": "Bearer test-key",
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
	} {
		if got := f.Header.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
	if len(d.Header) != 1 {
		t.Errorf("Prepare changed the dialect's own headers: %v", d.Header)
	}
	marks := p.PrefixMarks()
	if !slices.Equal(f.Marks, marks) || f.Predict != 0 {
		t.Errorf("marks = %d (plan has %d), predict = %d with no previous request", len(f.Marks), len(marks), f.Predict)
	}
	// A warm cache is predicted from the previous request's marks.
	prev := buildPlan(t, fullOpts(t)).PrefixMarks()
	warm := freeze(t, d, plainRoute(), p, llm.Options{Prev: prev})
	if want := llm.PredictCached(llm.CacheAutoPrefix, prev, marks); warm.Predict != want || want == 0 {
		t.Errorf("predict = %d, want %d", warm.Predict, want)
	}
}

func TestTierLayout(t *testing.T) {
	t.Parallel()
	req := decode(t, body(t, plainDialect(), plainRoute(), fullPlan(t)))

	if req.Model != "gpt-test" || !req.Stream || req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
		t.Errorf("model %q, stream %v, stream_options %+v", req.Model, req.Stream, req.StreamOptions)
	}
	if req.MaxCompletionTokens != 0 || req.MaxTokens != 0 || req.ToolChoice != nil {
		t.Errorf("max tokens %d/%d, tool_choice %s; want all left out", req.MaxCompletionTokens, req.MaxTokens, req.ToolChoice)
	}
	if req.ParallelToolCalls == nil || *req.ParallelToolCalls {
		t.Errorf("parallel_tool_calls = %v, want false", req.ParallelToolCalls)
	}
	if len(req.Tools) != 1 || req.Tools[0].Type != "function" || req.Tools[0].Function.Name != "python" {
		t.Fatalf("tools = %+v", req.Tools)
	}
	if got := string(req.Tools[0].Function.Parameters); got != codeSchema {
		t.Errorf("parameters = %s", got)
	}

	// The in-turn tool exchange comes after the time note: it is appended,
	// never inserted, so each step's messages are a prefix of the next's.
	want := []string{"system", "user", "assistant", "tool", "assistant", "user", "assistant", "tool"}
	if got := roles(&req); !slices.Equal(got, want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
	// Tiers 0-2 in order, least volatile first.
	sys := req.Messages[0].text()
	static := strings.Index(sys, "You are Ada")
	profile := strings.Index(sys, "What you know about the person you are with")
	recall := strings.Index(sys, "Things you remember")
	if !strings.HasPrefix(sys, plan.Preamble) || static < 0 || profile <= static || recall <= profile {
		t.Errorf("system tiers out of order: %q", sys)
	}

	call := req.Messages[2]
	if call.text() != "Let me compute that." || len(call.ToolCalls) != 1 {
		t.Fatalf("assistant message = %+v", call)
	}
	tc := call.ToolCalls[0]
	if tc.ID != "call_01" || tc.Type != "function" || tc.Function.Name != "python" ||
		tc.Function.Arguments != `{"code":"print(6 * 7)"}` {
		t.Errorf("tool call = %+v", tc)
	}
	if res := req.Messages[3]; res.ToolCallID != "call_01" || res.text() != "42\n" {
		t.Errorf("tool message = %+v", res)
	}
	// A call with no text has no content field at all.
	if req.Messages[6].Content != nil || len(req.Messages[6].ToolCalls) != 1 {
		t.Errorf("in-turn assistant message = %+v", req.Messages[6])
	}
	if live := req.Messages[5].text(); !strings.HasPrefix(live, "And <b>7 times 8</b>?") ||
		!strings.Contains(live, "\n\n<now>") {
		t.Errorf("live user message = %q, want the question and the time note", live)
	}
	if res := req.Messages[7]; res.ToolCallID != "call_02" || res.text() != "56\n" {
		t.Errorf("in-turn tool message = %+v", res)
	}
}

func TestRequestOptions(t *testing.T) {
	t.Parallel()

	t.Run("output cap precedence and field name", func(t *testing.T) {
		t.Parallel()
		d, r := plainDialect(), plainRoute()
		d.DefaultMaxOutput = 11
		if req := decode(t, body(t, d, r, fullPlan(t))); req.MaxCompletionTokens != 11 || req.MaxTokens != 0 {
			t.Errorf("dialect default: max_completion_tokens %d, max_tokens %d", req.MaxCompletionTokens, req.MaxTokens)
		}
		// The route's figure is a ceiling, not a default.
		r.Caps.MaxOutput = 384_000
		if req := decode(t, body(t, d, r, fullPlan(t))); req.MaxCompletionTokens != 11 {
			t.Errorf("under a high ceiling: max_completion_tokens = %d, want the dialect's 11", req.MaxCompletionTokens)
		}
		f := freeze(t, d, r, fullPlan(t), llm.Options{MaxOutput: 1234})
		if req := decode(t, f.Body); req.MaxCompletionTokens != 1234 {
			t.Errorf("option: max_completion_tokens = %d, want 1234", req.MaxCompletionTokens)
		}
		r.Caps.MaxOutput = 99
		f = freeze(t, d, r, fullPlan(t), llm.Options{MaxOutput: 1234})
		if req := decode(t, f.Body); req.MaxCompletionTokens != 99 {
			t.Errorf("over the ceiling: max_completion_tokens = %d, want it held to 99", req.MaxCompletionTokens)
		}
		d.MaxTokensField = ""
		if req := decode(t, body(t, d, r, fullPlan(t))); req.MaxTokens != 11 || req.MaxCompletionTokens != 0 {
			t.Errorf("default field: max_tokens %d, max_completion_tokens %d", req.MaxTokens, req.MaxCompletionTokens)
		}
		// With no option and no dialect default, nothing is sent.
		d.DefaultMaxOutput = 0
		if req := decode(t, body(t, d, r, fullPlan(t))); req.MaxTokens != 0 || req.MaxCompletionTokens != 0 {
			t.Errorf("no cap asked for: max_tokens %d, max_completion_tokens %d", req.MaxTokens, req.MaxCompletionTokens)
		}
	})

	t.Run("no tools", func(t *testing.T) {
		t.Parallel()
		o := fullOpts(t)
		o.tools = nil
		raw := body(t, plainDialect(), plainRoute(), buildPlan(t, o))
		if bytes.Contains(raw, []byte(`"tools"`)) || bytes.Contains(raw, []byte(`"parallel_tool_calls"`)) {
			t.Errorf("request mentions tools: %s", raw)
		}
	})

	t.Run("a route that cannot be held to one call says nothing about it", func(t *testing.T) {
		t.Parallel()
		r := plainRoute()
		r.Caps.SingleToolCall = false
		raw := body(t, plainDialect(), r, fullPlan(t))
		if bytes.Contains(raw, []byte(`"parallel_tool_calls"`)) {
			t.Errorf("request sets parallel_tool_calls: %s", raw)
		}
	})

	t.Run("tool_choice is never sent", func(t *testing.T) {
		t.Parallel()
		for _, honoured := range []bool{true, false} {
			r := plainRoute()
			r.Caps.ToolChoice = honoured
			if raw := body(t, plainDialect(), r, fullPlan(t)); bytes.Contains(raw, []byte(`"tool_choice"`)) {
				t.Errorf("ToolChoice=%t: request sets tool_choice: %s", honoured, raw)
			}
		}
	})

	t.Run("invalid input", func(t *testing.T) {
		t.Parallel()
		noModel := plainRoute()
		noModel.Model = ""
		for name, call := range map[string]func() (*llm.Frozen, error){
			"no model":    func() (*llm.Frozen, error) { return Prepare(plainDialect(), noModel, fullPlan(t), llm.Options{}) },
			"no endpoint": func() (*llm.Frozen, error) { return Prepare(&Dialect{}, plainRoute(), fullPlan(t), llm.Options{}) },
			"nil dialect": func() (*llm.Frozen, error) { return Prepare(nil, plainRoute(), fullPlan(t), llm.Options{}) },
			"nil plan":    func() (*llm.Frozen, error) { return Prepare(plainDialect(), plainRoute(), nil, llm.Options{}) },
		} {
			if _, err := call(); err == nil {
				t.Errorf("%s: Prepare accepted it", name)
			}
		}
	})
}

func TestMergeSameRole(t *testing.T) {
	t.Parallel()

	t.Run("system tiers become one leading message", func(t *testing.T) {
		t.Parallel()
		for name, edit := range map[string]func(*planOpts){
			"all":        func(*planOpts) {},
			"no profile": func(o *planOpts) { o.profile = "" },
			"no recall":  func(o *planOpts) { o.recall = nil },
		} {
			o := fullOpts(t)
			edit(&o)
			req := decode(t, body(t, plainDialect(), plainRoute(), buildPlan(t, o)))
			n := 0
			for _, m := range req.Messages {
				if m.Role == "system" {
					n++
				}
			}
			if n != 1 || req.Messages[0].Role != "system" {
				t.Errorf("%s: %d system messages, first role %q", name, n, req.Messages[0].Role)
			}
		}
	})

	t.Run("empty contact prompt still carries the server preamble", func(t *testing.T) {
		t.Parallel()
		p := buildPlan(t, planOpts{events: []core.Event{event(t, 1, core.KindUserMsg, core.MsgBody{Text: "Hi"})}})
		req := decode(t, body(t, plainDialect(), plainRoute(), p))
		if got := roles(&req); !slices.Equal(got, []string{"system", "user"}) {
			t.Errorf("roles = %v, want one user message", got)
		}
	})

	t.Run("consecutive user messages", func(t *testing.T) {
		t.Parallel()
		p := buildPlan(t, planOpts{events: []core.Event{
			event(t, 1, core.KindUserMsg, core.MsgBody{Text: "one"}),
			event(t, 2, core.KindUserMsg, core.MsgBody{Text: "two"}),
		}})
		req := decode(t, body(t, plainDialect(), plainRoute(), p))
		if len(req.Messages) != 2 {
			t.Fatalf("messages = %+v, want one user message", req.Messages)
		}
		if got := req.Messages[1].text(); !strings.HasPrefix(got, "one\n\ntwo\n\n<now>") {
			t.Errorf("merged content = %q", got)
		}
	})

	t.Run("tool results stay separate and errors are marked", func(t *testing.T) {
		t.Parallel()
		// The tool never answered and the contact wrote again: the planner
		// renders a placeholder error result, then the message.
		p := buildPlan(t, planOpts{
			tools: []core.ToolDef{pythonTool()},
			events: []core.Event{
				event(t, 1, core.KindUserMsg, core.MsgBody{Text: "Run it"}),
				event(t, 2, core.KindToolCall, core.ToolCall{ID: "call_01", Name: "python", Code: "while True: pass"}),
				event(t, 3, core.KindUserMsg, core.MsgBody{Text: "Still there?"}),
			},
		})
		req := decode(t, body(t, plainDialect(), plainRoute(), p))
		if got := roles(&req); !slices.Equal(got, []string{"system", "user", "assistant", "tool", "user"}) {
			t.Fatalf("roles = %v", got)
		}
		if got := req.Messages[3].text(); !strings.HasPrefix(got, toolErrorPrefix) {
			t.Errorf("tool content = %q, want the error marker", got)
		}
		if got := req.Messages[4].text(); !strings.HasPrefix(got, "Still there?\n\n<now>") {
			t.Errorf("user content = %q, want the message and the time note merged", got)
		}
	})

	t.Run("assistant runs and blank text", func(t *testing.T) {
		t.Parallel()
		var b builder
		for _, m := range []core.Message{
			{Role: core.RoleUser, Text: "a"},
			{Role: core.RoleAssistant, Text: " \n"},
			{Role: core.RoleUser, Text: "b"},
			{Role: core.RoleAssistant, Text: "first"},
			{Role: core.RoleAssistant, ToolCall: &core.ToolCall{ID: "call_1", Name: "python", Code: "1"}},
			{Role: core.RoleTool, ToolResult: &core.ToolResult{ID: "call_1"}},
		} {
			if err := b.add(&m); err != nil {
				t.Fatal(err)
			}
		}
		if len(b.msgs) != 3 {
			t.Fatalf("%d messages, want user, assistant, tool", len(b.msgs))
		}
		msgs := make([]message, len(b.msgs))
		for i, dm := range b.msgs {
			var err error
			if msgs[i], err = dm.wire(); err != nil {
				t.Fatal(err)
			}
		}
		if got := string(msgs[0].Content); got != `"a\n\nb"` {
			t.Errorf("user content = %s", got)
		}
		if string(msgs[1].Content) != `"first"` || len(msgs[1].ToolCalls) != 1 {
			t.Errorf("assistant message = %+v", msgs[1])
		}
		// An empty result still needs its content field.
		if string(msgs[2].Content) != `""` {
			t.Errorf("tool content = %q, want an empty string", msgs[2].Content)
		}
	})
}

func TestTailGrowthKeepsPrefix(t *testing.T) {
	t.Parallel()
	// There are no breakpoints here: the cache is whatever prefix of the body
	// repeats. The next turn must reproduce everything up to the end of the
	// previous tail byte for byte.
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

	// The tail of the earlier plan ends with the reply "It is 42.".
	const tailEnd = `"content":"It is 42."}`
	a, b := body(t, plainDialect(), plainRoute(), prev), body(t, plainDialect(), plainRoute(), next)
	cut := bytes.Index(a, []byte(tailEnd))
	if cut < 0 {
		t.Fatalf("tail end not found in %s", a)
	}
	cut += len(tailEnd)
	if len(b) < cut || !bytes.Equal(a[:cut], b[:cut]) {
		t.Errorf("shared prefix changed between turns:\n%s\n%s", a[:cut], b[:min(cut, len(b))])
	}
}

func TestToolStepsAppend(t *testing.T) {
	t.Parallel()
	// Within a turn each request's messages must be a prefix of the next
	// one's, so the server's prefix cache covers everything already sent.
	p := buildPlan(t, fullOpts(t))
	messages := func() []byte {
		t.Helper()
		raw := body(t, plainDialect(), plainRoute(), p)
		msgs, _, ok := bytes.Cut(raw, []byte(`],"tools":`))
		if !ok {
			t.Fatalf("no end of messages in %s", raw)
		}
		return msgs
	}

	prev := messages()
	for step := range 3 {
		p.AppendTail(
			core.ToolCall{
				ID: "call_9", Name: "python", Code: "1", Text: "Trying.",
				// Another provider's payload: this route must not see it.
				Reasoning: &core.Reasoning{Route: "anthropic/claude", Data: []byte(`[{"type":"thinking"}]`)},
			},
			core.ToolResult{ID: "call_9", Output: "1"},
		)
		next := messages()
		if !bytes.HasPrefix(next, prev) {
			t.Fatalf("step %d rewrote earlier messages:\n%s\n%s", step+1, prev, next)
		}
		if bytes.Contains(next, []byte("thinking")) {
			t.Fatalf("step %d sent another provider's reasoning: %s", step+1, next)
		}
		prev = next
	}
}

func TestSiblingCalls(t *testing.T) {
	t.Parallel()
	shell := core.ToolDef{Name: "shell", Description: "Run a shell command."}
	callA := core.ToolCall{ID: "call_a", Name: "python", Code: "a = 1", Text: "Two things."}
	callB := core.ToolCall{ID: "call_b", Name: "shell", Code: "ls", Sibling: 1}
	callC := core.ToolCall{ID: "call_c", Name: "python", Code: "c = 3", Sibling: 2}

	t.Run("calls of one step share an assistant message", func(t *testing.T) {
		t.Parallel()
		p := buildPlan(t, planOpts{
			tools: []core.ToolDef{pythonTool(), shell},
			events: []core.Event{
				event(t, 1, core.KindUserMsg, core.MsgBody{Text: "Run them"}),
				event(t, 2, core.KindToolCall, callA),
				event(t, 3, core.KindToolResult, core.ToolResult{ID: "call_a", Output: "ok a"}),
				event(t, 4, core.KindToolCall, callB),
				event(t, 5, core.KindToolResult, core.ToolResult{ID: "call_b", Output: "ok b"}),
				event(t, 6, core.KindToolCall, callC),
				event(t, 7, core.KindToolResult, core.ToolResult{ID: "call_c", Output: "ok c"}),
				event(t, 8, core.KindAgentMsg, core.MsgBody{Text: "Done."}),
				event(t, 9, core.KindUserMsg, core.MsgBody{Text: "Thanks"}),
			},
		})
		req := decode(t, body(t, plainDialect(), plainRoute(), p))
		want := []string{"system", "user", "assistant", "tool", "tool", "tool", "assistant", "user"}
		if got := roles(&req); !slices.Equal(got, want) {
			t.Fatalf("roles = %v, want %v", got, want)
		}
		step := req.Messages[2]
		if step.text() != "Two things." || len(step.ToolCalls) != 3 {
			t.Fatalf("step message = %+v", step)
		}
		for i, id := range []string{"call_a", "call_b", "call_c"} {
			if step.ToolCalls[i].ID != id || req.Messages[3+i].ToolCallID != id {
				t.Errorf("position %d: call %q, result for %q; want %q", i, step.ToolCalls[i].ID, req.Messages[3+i].ToolCallID, id)
			}
		}
		if got := step.ToolCalls[1].Function; got.Name != "shell" || got.Arguments != `{"code":"ls"}` {
			t.Errorf("second call = %+v", got)
		}
	})

	t.Run("a message logged mid-step follows the step's results", func(t *testing.T) {
		t.Parallel()
		// The contact wrote while call A ran. The log holds the message
		// between the two calls; the wire needs both results first.
		p := buildPlan(t, planOpts{
			tools: []core.ToolDef{pythonTool(), shell},
			events: []core.Event{
				event(t, 1, core.KindUserMsg, core.MsgBody{Text: "Run them"}),
				event(t, 2, core.KindToolCall, callA),
				event(t, 3, core.KindUserMsg, core.MsgBody{Text: "Hurry"}),
				event(t, 4, core.KindToolResult, core.ToolResult{ID: "call_a", Output: "ok a"}),
				event(t, 5, core.KindToolCall, callB),
				event(t, 6, core.KindToolResult, core.ToolResult{ID: "call_b", Output: "ok b"}),
				event(t, 7, core.KindAgentMsg, core.MsgBody{Text: "Done."}),
				event(t, 8, core.KindUserMsg, core.MsgBody{Text: "Thanks"}),
			},
		})
		req := decode(t, body(t, plainDialect(), plainRoute(), p))
		want := []string{"system", "user", "assistant", "tool", "tool", "user", "assistant", "user"}
		if got := roles(&req); !slices.Equal(got, want) {
			t.Fatalf("roles = %v, want %v", got, want)
		}
		if len(req.Messages[2].ToolCalls) != 2 || req.Messages[3].ToolCallID != "call_a" ||
			req.Messages[4].ToolCallID != "call_b" || req.Messages[5].text() != "Hurry" {
			t.Errorf("messages = %+v", req.Messages[2:5])
		}
	})

	t.Run("a sibling whose first call is gone stands alone", func(t *testing.T) {
		t.Parallel()
		var b builder
		for _, m := range []core.Message{
			{Role: core.RoleUser, Text: "go on"},
			{Role: core.RoleAssistant, ToolCall: &callB},
			{Role: core.RoleTool, ToolResult: &core.ToolResult{ID: "call_b", Output: "ok"}},
		} {
			if err := b.add(&m); err != nil {
				t.Fatal(err)
			}
		}
		if len(b.msgs) != 3 || len(b.msgs[1].calls) != 1 || b.msgs[2].toolCallID != "call_b" {
			t.Errorf("%d messages; want user, assistant with one call, its result", len(b.msgs))
		}
	})

	t.Run("in-turn siblings", func(t *testing.T) {
		t.Parallel()
		o := fullOpts(t)
		o.tools = append(o.tools, shell)
		p := buildPlan(t, o)
		p.AppendTail(callA, core.ToolResult{ID: "call_a", Output: "ok a"})
		p.AppendTail(callB, core.ToolResult{ID: "call_b", Output: "ok b", IsError: true})
		req := decode(t, body(t, plainDialect(), plainRoute(), p))
		n := len(req.Messages)
		if got := roles(&req)[n-3:]; !slices.Equal(got, []string{"assistant", "tool", "tool"}) {
			t.Fatalf("last roles = %v", got)
		}
		if len(req.Messages[n-3].ToolCalls) != 2 || req.Messages[n-1].text() != toolErrorPrefix+"ok b" {
			t.Errorf("step = %+v", req.Messages[n-3:])
		}
	})
}

func TestToolSchemas(t *testing.T) {
	t.Parallel()
	const schema = `{"type":"object","properties":{"task":{"type":"string"},"n":{"type":"integer"}},"required":["task"]}`
	delegate := core.ToolDef{Name: "delegate", Description: "Start a sub-agent.", Input: json.RawMessage(schema)}
	o := fullOpts(t)
	o.tools = append(o.tools, delegate)
	p := buildPlan(t, o)
	const args = `{"task": "find <it>", "n": 2}`
	p.AppendTail(
		core.ToolCall{ID: "call_d", Name: "delegate", Args: json.RawMessage(args)},
		core.ToolResult{ID: "call_d", Output: "started"},
	)
	// A call that recorded no arguments still sends a JSON document.
	p.AppendTail(
		core.ToolCall{ID: "call_e", Name: "delegate"},
		core.ToolResult{ID: "call_e", Output: "started"},
	)
	raw := body(t, plainDialect(), plainRoute(), p)
	req := decode(t, raw)

	if len(req.Tools) != 2 {
		t.Fatalf("tools = %+v", req.Tools)
	}
	if got := string(req.Tools[0].Function.Parameters); got != codeSchema {
		t.Errorf("default parameters = %s", got)
	}
	if got := string(req.Tools[1].Function.Parameters); got != schema {
		t.Errorf("own parameters = %s, want the schema verbatim", got)
	}
	n := len(req.Messages)
	if got := req.Messages[n-4].ToolCalls[0].Function.Arguments; got != args {
		t.Errorf("arguments = %q, want Args verbatim", got)
	}
	if got := req.Messages[n-2].ToolCalls[0].Function.Arguments; got != "{}" {
		t.Errorf("arguments of a call with none = %q, want {}", got)
	}
	// The frozen request is what Send reads the tool kinds back from.
	kinds := toolKinds(raw)
	if kinds["python"] != toolCode || kinds["delegate"] != toolArgs || kinds["nope"] != toolUndeclared {
		t.Errorf("kinds = %v", kinds)
	}

	bad := fullOpts(t)
	bad.tools = []core.ToolDef{{Name: "broken", Input: json.RawMessage(`{"type":`)}}
	if _, err := Prepare(plainDialect(), plainRoute(), buildPlan(t, bad), llm.Options{}); err == nil {
		t.Error("Prepare accepted a tool whose schema is not JSON")
	}
}

func mustPayload(t *testing.T, p payload) []byte {
	t.Helper()
	raw, err := encode(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReasoningReplay(t *testing.T) {
	t.Parallel()
	const details = `[{"type":"reasoning.text","text":"6 * 7 <is> 42","signature":"sig==","format":"anthropic-claude-v1","index":0},` +
		`{"type":"reasoning.encrypted","data":"b64","index":1}]`
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(details), &items); err != nil {
		t.Fatal(err)
	}
	replaying := func(style ReasoningStyle) (*Dialect, llm.Route) {
		d, r := plainDialect(), plainRoute()
		d.Reasoning = style
		r.Caps.Replay = llm.ReplayAllTurns
		return d, r
	}
	// The payload rides on a tool call in the tail and on the reply after it.
	planWith := func(route string, data []byte) *plan.PromptPlan {
		var reason *core.Reasoning
		if data != nil {
			reason = &core.Reasoning{Route: route, Data: data}
		}
		return buildPlan(t, planOpts{
			route: "openai/gpt-test",
			tools: []core.ToolDef{pythonTool()},
			events: []core.Event{
				event(t, 1, core.KindUserMsg, core.MsgBody{Text: "What is 6 times 7?"}),
				event(t, 2, core.KindToolCall, core.ToolCall{ID: "call_01", Name: "python", Code: "print(6 * 7)", Reasoning: reason}),
				event(t, 3, core.KindToolResult, core.ToolResult{ID: "call_01", Output: "42"}),
				event(t, 4, core.KindAgentMsg, core.MsgBody{Text: "It is 42.", Reasoning: reason}),
				event(t, 5, core.KindUserMsg, core.MsgBody{Text: "Thanks"}),
			},
		})
	}
	both := mustPayload(t, payload{Text: "6 * 7 <is> 42", Details: items})

	t.Run("reasoning_content", func(t *testing.T) {
		t.Parallel()
		d, r := replaying(ReasoningContent)
		req := decode(t, body(t, d, r, planWith(r.ID(), both)))
		for _, i := range []int{2, 4} {
			m := req.Messages[i]
			if m.ReasoningContent == nil || *m.ReasoningContent != "6 * 7 <is> 42" || m.Reasoning != nil || m.ReasoningDetails != nil {
				t.Errorf("message %d = %+v, want reasoning_content only", i, m)
			}
		}
		if req.Messages[1].ReasoningContent != nil || req.Messages[3].ReasoningContent != nil {
			t.Error("reasoning was put on a message that is not the assistant's")
		}
	})

	t.Run("reasoning_details", func(t *testing.T) {
		t.Parallel()
		d, r := replaying(ReasoningDetails)
		req := decode(t, body(t, d, r, planWith(r.ID(), both)))
		for _, i := range []int{2, 4} {
			m := req.Messages[i]
			if string(m.ReasoningDetails) != details || m.Reasoning != nil || m.ReasoningContent != nil {
				t.Errorf("message %d = %+v, want the details unmodified and nothing else", i, m)
			}
		}
	})

	t.Run("reasoning_details falls back to the text", func(t *testing.T) {
		t.Parallel()
		d, r := replaying(ReasoningDetails)
		req := decode(t, body(t, d, r, planWith(r.ID(), mustPayload(t, payload{Text: "plain thought"}))))
		m := req.Messages[2]
		if m.Reasoning == nil || *m.Reasoning != "plain thought" || m.ReasoningDetails != nil {
			t.Errorf("message = %+v, want the reasoning string", m)
		}
	})

	t.Run("never to another route, a route that replays nothing, or an unsent style", func(t *testing.T) {
		t.Parallel()
		d, r := replaying(ReasoningContent)
		bare := body(t, d, r, planWith(r.ID(), nil))
		if bytes.Contains(bare, []byte("reasoning")) {
			t.Fatalf("a plan without reasoning mentions it: %s", bare)
		}
		// AppendTail carries a payload whatever its tag; the codec is the
		// last check.
		foreign := planWith(r.ID(), nil)
		foreign.AppendTail(
			core.ToolCall{ID: "c9", Name: "python", Code: "1", Reasoning: &core.Reasoning{Route: "openai/other-model", Data: both}},
			core.ToolResult{ID: "c9", Output: "1"},
		)
		if raw := body(t, d, r, foreign); bytes.Contains(raw, []byte("reasoning")) || bytes.Contains(raw, []byte("42 <is>")) {
			t.Errorf("a payload tagged for another route was sent: %s", raw)
		}

		none := r
		none.Caps.Replay = llm.ReplayNone
		if raw := body(t, d, none, planWith(r.ID(), both)); !bytes.Equal(raw, bare) {
			t.Errorf("a ReplayNone route sent reasoning back: %s", raw)
		}
		unsent, _ := replaying(ReasoningUnsent)
		if raw := body(t, unsent, r, planWith(r.ID(), both)); !bytes.Equal(raw, bare) {
			t.Errorf("an unsent style sent reasoning back: %s", raw)
		}
	})

	t.Run("two payloads are never folded into one message", func(t *testing.T) {
		t.Parallel()
		b := builder{route: "openai/gpt-test", reasoning: ReasoningContent}
		first := &core.Reasoning{Route: "openai/gpt-test", Data: mustPayload(t, payload{Text: "one"})}
		second := &core.Reasoning{Route: "openai/gpt-test", Data: mustPayload(t, payload{Text: "two"})}
		for _, m := range []core.Message{
			{Role: core.RoleAssistant, Text: "a", Reasoning: first},
			{Role: core.RoleAssistant, Text: "b", Reasoning: second},
		} {
			if err := b.add(&m); err != nil {
				t.Fatal(err)
			}
		}
		if len(b.msgs) != 2 || b.msgs[0].reasoning.Text != "one" || b.msgs[1].reasoning.Text != "two" {
			t.Errorf("%d messages; want one per payload", len(b.msgs))
		}
	})

	t.Run("a corrupt payload for this route is an error", func(t *testing.T) {
		t.Parallel()
		d, r := replaying(ReasoningContent)
		if _, err := Prepare(d, r, planWith(r.ID(), []byte("not json")), llm.Options{}); err == nil {
			t.Error("Prepare accepted a payload it cannot decode")
		}
	})
}

func TestEffortStyles(t *testing.T) {
	t.Parallel()
	route := plainRoute()
	route.Caps.Efforts = []llm.Effort{llm.EffortLow, llm.EffortHigh, llm.EffortMax}
	route.Caps.ReasoningOff = true
	base := plainDialect()
	tail := func(t *testing.T, style EffortStyle, e llm.Effort) (string, llm.Effort) {
		t.Helper()
		d := plainDialect()
		d.Effort = style
		f := freeze(t, d, route, fullPlan(t), llm.Options{Effort: e})
		_, after, ok := bytes.Cut(f.Body, []byte(`"stream_options":{"include_usage":true}`))
		if !ok {
			t.Fatalf("no stream_options in %s", f.Body)
		}
		return string(after), f.Effort
	}
	cases := []struct {
		name  string
		style EffortStyle
		ask   llm.Effort
		sent  llm.Effort
		want  string
	}{
		{"top level", EffortTopLevel, llm.EffortHigh, llm.EffortHigh, `,"reasoning_effort":"high"}`},
		{"top level maps to the nearest", EffortTopLevel, llm.EffortMedium, llm.EffortHigh, `,"reasoning_effort":"high"}`},
		{"top level off", EffortTopLevel, llm.EffortNone, llm.EffortNone, `,"reasoning_effort":"none"}`},
		{"reasoning object", EffortReasoningObject, llm.EffortMax, llm.EffortMax, `,"reasoning":{"effort":"max"}}`},
		{"reasoning object off", EffortReasoningObject, llm.EffortNone, llm.EffortNone, `,"reasoning":{"effort":"none"}}`},
		{"thinking toggle", EffortThinkingToggle, llm.EffortLow, llm.EffortLow, `,"thinking":{"type":"enabled"},"reasoning_effort":"low"}`},
		{"thinking toggle off", EffortThinkingToggle, llm.EffortNone, llm.EffortNone, `,"thinking":{"type":"disabled"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, sent := tail(t, tc.style, tc.ask)
			if got != tc.want || sent != tc.sent {
				t.Errorf("body ends %s with Frozen.Effort %q; want %s and %q", got, sent, tc.want, tc.sent)
			}
		})
	}

	t.Run("default sends nothing in any style", func(t *testing.T) {
		t.Parallel()
		want := body(t, base, plainRoute(), fullPlan(t))
		for _, style := range []EffortStyle{EffortUnsent, EffortTopLevel, EffortReasoningObject, EffortThinkingToggle} {
			d := plainDialect()
			d.Effort = style
			f := freeze(t, d, route, fullPlan(t), llm.Options{})
			if f.Effort != llm.EffortDefault || !bytes.Equal(f.Body, want) {
				t.Errorf("style %d: no effort asked for, yet the request changed: %s", style, f.Body)
			}
		}
		// A route with no effort control ignores a request for one.
		d := plainDialect()
		d.Effort = EffortTopLevel
		f := freeze(t, d, plainRoute(), fullPlan(t), llm.Options{Effort: llm.EffortHigh})
		if f.Effort != llm.EffortDefault || !bytes.Equal(f.Body, want) {
			t.Errorf("a route with no effort control sent one: %s", f.Body)
		}
	})

	t.Run("a route with efforts needs a style", func(t *testing.T) {
		t.Parallel()
		if _, err := Prepare(base, route, fullPlan(t), llm.Options{Effort: llm.EffortHigh}); err == nil {
			t.Error("Prepare froze an effort the dialect cannot spell")
		}
	})
}

func TestAffinity(t *testing.T) {
	t.Parallel()
	const key = "aff1n1tyk3y"
	dialect := func() *Dialect {
		d := plainDialect()
		d.AffinityHeader, d.AffinityField = "X-Session", "session_id"
		return d
	}
	at := func(where llm.Affinity) *llm.Frozen {
		r := plainRoute()
		r.Caps.Affinity = where
		return freeze(t, dialect(), r, fullPlan(t), llm.Options{Affinity: key})
	}
	inHeader := func(f *llm.Frozen) bool {
		for _, vs := range f.Header {
			if slices.Contains(vs, key) {
				return true
			}
		}
		return false
	}
	bare := body(t, dialect(), plainRoute(), fullPlan(t))

	if f := at(llm.AffinityHeader); f.Header.Get("X-Session") != key || !bytes.Equal(f.Body, bare) {
		t.Errorf("header affinity: header %q, body changed %t", f.Header.Get("X-Session"), !bytes.Equal(f.Body, bare))
	}
	f := at(llm.AffinityBody)
	if inHeader(f) || !bytes.HasSuffix(f.Body, []byte(`,"session_id":"`+key+`"}`)) {
		t.Errorf("body affinity: in header %t, body %s", inHeader(f), f.Body)
	}
	if f := at(llm.AffinityNone); inHeader(f) || !bytes.Equal(f.Body, bare) {
		t.Errorf("no affinity: in header %t, body changed %t", inHeader(f), !bytes.Equal(f.Body, bare))
	}
	// No key, nothing sent, wherever it would go.
	for _, where := range []llm.Affinity{llm.AffinityHeader, llm.AffinityBody} {
		r := plainRoute()
		r.Caps.Affinity = where
		f := freeze(t, dialect(), r, fullPlan(t), llm.Options{})
		if f.Header.Get("X-Session") != "" || !bytes.Equal(f.Body, bare) {
			t.Errorf("affinity %d with no key still sent something", where)
		}
	}
	// A route that takes a key where the dialect has no name for it is a
	// profile bug, not something to drop silently.
	for _, where := range []llm.Affinity{llm.AffinityHeader, llm.AffinityBody} {
		r := plainRoute()
		r.Caps.Affinity = where
		if _, err := Prepare(plainDialect(), r, fullPlan(t), llm.Options{Affinity: key}); err == nil {
			t.Errorf("affinity %d: Prepare dropped a key it had nowhere to put", where)
		}
	}
}

func TestBreakpoints(t *testing.T) {
	t.Parallel()
	route := plainRoute()
	route.Caps.Cache = llm.CacheBreakpoints
	plain := decode(t, body(t, plainDialect(), plainRoute(), fullPlan(t)))
	raw := body(t, plainDialect(), route, fullPlan(t))
	req := decode(t, raw)

	if n := bytes.Count(raw, []byte(`"cache_control":{"type":"ephemeral"}`)); n != 4 {
		t.Fatalf("%d breakpoints, want one per cacheable tier: %s", n, raw)
	}
	parts := func(m wireMessage) []part {
		var ps []part
		if err := json.Unmarshal(m.Content, &ps); err != nil {
			return nil
		}
		return ps
	}
	joined := func(ps []part) string {
		var sb strings.Builder
		for _, p := range ps {
			sb.WriteString(p.Text)
		}
		return sb.String()
	}

	// The system message holds tiers 0-2: one part each, each closing a tier.
	sys := parts(req.Messages[0])
	if len(sys) != 3 {
		t.Fatalf("system content = %s, want three parts", req.Messages[0].Content)
	}
	for i, p := range sys {
		if p.Type != "text" || p.CacheControl == nil {
			t.Errorf("system part %d = %+v, want a text part with a breakpoint", i, p)
		}
	}
	if !strings.HasPrefix(sys[1].Text, "\n\nWhat you know about the person you are with") {
		t.Errorf("profile part = %q", sys[1].Text)
	}
	// Breakpoints move no model-visible byte.
	if joined(sys) != plain.Messages[0].text() {
		t.Errorf("system parts join to %q, the plain form is %q", joined(sys), plain.Messages[0].text())
	}

	// The tail ends with the reply "It is 42.".
	last := parts(req.Messages[4])
	if len(last) != 1 || last[0].Text != "It is 42." || last[0].CacheControl == nil {
		t.Errorf("tail end = %s, want one part with a breakpoint", req.Messages[4].Content)
	}
	// Every other message keeps the plain string form, byte for byte.
	for _, i := range []int{1, 2, 3, 5, 6, 7} {
		if !bytes.Equal(req.Messages[i].Content, plain.Messages[i].Content) {
			t.Errorf("message %d content = %s, want %s", i, req.Messages[i].Content, plain.Messages[i].Content)
		}
	}

	t.Run("an empty tier gets none, and other cache modes never do", func(t *testing.T) {
		t.Parallel()
		o := fullOpts(t)
		o.profile, o.recall = "", nil
		if n := bytes.Count(body(t, plainDialect(), route, buildPlan(t, o)), []byte(`"cache_control"`)); n != 2 {
			t.Errorf("%d breakpoints with two tiers empty, want 2", n)
		}
		for _, mode := range []llm.CacheMode{llm.CacheNone, llm.CacheAutoPrefix} {
			r := plainRoute()
			r.Caps.Cache = mode
			if raw := body(t, plainDialect(), r, fullPlan(t)); bytes.Contains(raw, []byte("cache_control")) {
				t.Errorf("cache mode %d sent a breakpoint", mode)
			}
		}
	})

	t.Run("a tier that ends in a tool result", func(t *testing.T) {
		t.Parallel()
		// The tail ends with a placeholder result; the live message follows.
		p := buildPlan(t, planOpts{
			tools: []core.ToolDef{pythonTool()},
			events: []core.Event{
				event(t, 1, core.KindUserMsg, core.MsgBody{Text: "Run it"}),
				event(t, 2, core.KindToolCall, core.ToolCall{ID: "call_01", Name: "python", Code: "1"}),
				event(t, 3, core.KindUserMsg, core.MsgBody{Text: "Still there?"}),
			},
		})
		req := decode(t, body(t, plainDialect(), route, p))
		ps := parts(req.Messages[3])
		if req.Messages[3].Role != "tool" || len(ps) != 1 || ps[0].CacheControl == nil {
			t.Errorf("tool message content = %s, want one part with a breakpoint", req.Messages[3].Content)
		}
	})
}

func TestExtraFields(t *testing.T) {
	t.Parallel()
	d := plainDialect()
	d.Effort, d.AffinityField = EffortReasoningObject, "session_id"
	d.Extra = []Field{
		{Name: "provider", Value: json.RawMessage(`{"only":["deepseek"],"allow_fallbacks":false}`)},
		{Name: "usage", Value: json.RawMessage(`{"include":true}`)},
	}
	r := plainRoute()
	r.Caps.Efforts, r.Caps.Affinity, r.Caps.MaxOutput = []llm.Effort{llm.EffortHigh}, llm.AffinityBody, 64
	f := freeze(t, d, r, fullPlan(t), llm.Options{Effort: llm.EffortHigh, Affinity: "k", MaxOutput: 32})
	want := []string{
		"model", "messages", "tools", "parallel_tool_calls", "max_completion_tokens",
		"stream", "stream_options", "reasoning", "session_id", "provider", "usage",
	}
	if got := fieldOrder(t, f.Body); !slices.Equal(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}
	if !bytes.HasSuffix(f.Body, []byte(`,"provider":{"only":["deepseek"],"allow_fallbacks":false},"usage":{"include":true}}`)) {
		t.Errorf("extras not written verbatim and last: %s", f.Body)
	}

	for name, extra := range map[string]Field{
		"a name the codec wrote": {Name: "model", Value: json.RawMessage(`"other"`)},
		"invalid JSON":           {Name: "provider", Value: json.RawMessage(`{"only":`)},
		"no name":                {Value: json.RawMessage(`1`)},
	} {
		bad := plainDialect()
		bad.Extra = []Field{extra}
		if _, err := Prepare(bad, plainRoute(), fullPlan(t), llm.Options{}); err == nil {
			t.Errorf("%s: Prepare accepted the extra field", name)
		}
	}
	// The dialect's headers are sent as given.
	hd := plainDialect()
	hd.Header = http.Header{"User-Agent": {"contactd/1.0"}, "Accept": {"application/x-ndjson"}}
	hf := freeze(t, hd, plainRoute(), fullPlan(t), llm.Options{})
	if hf.Header.Get("User-Agent") != "contactd/1.0" || hf.Header.Get("Accept") != "application/x-ndjson" {
		t.Errorf("headers = %v", hf.Header)
	}
}
