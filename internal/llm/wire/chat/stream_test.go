package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
)

// replayRoute is plainRoute on a route that takes its reasoning back.
func replayRoute() llm.Route {
	r := plainRoute()
	r.Caps.Replay = llm.ReplayAllTurns
	return r
}

func TestStreamText(t *testing.T) {
	t.Parallel()
	evs, res, err := run(t, plainRoute(), string(fixture(t, "text.sse")))
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	want := []llm.Event{
		{Kind: llm.TextDelta, Text: "Hello"},
		{Kind: llm.TextDelta, Text: ", world."},
	}
	if !slices.Equal(evs, want) {
		t.Errorf("events = %+v, want %+v", evs, want)
	}
	if res.Text != "Hello, world." || len(res.ToolCalls) != 0 || res.Stop != llm.StopEnd || res.Reasoning != nil {
		t.Errorf("result = %+v", res)
	}
	u := res.Usage
	if u.Input != 2312 || u.Cached != 2048 || u.Output != 9 || u.Reasoning != 0 {
		t.Errorf("usage = %+v", u)
	}
	// The provider's own object is kept whole, for audit.
	if !strings.HasPrefix(string(u.Raw), `{"prompt_tokens":2312,`) || !strings.Contains(string(u.Raw), `"audio_tokens":0`) {
		t.Errorf("raw usage = %s", u.Raw)
	}
}

func TestSendPostsTheFrozenRequest(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, reply{body: fixture(t, "text.sse")})
	d := plainDialect()
	d.URL = u.url + "/v1/chat/completions"
	d.AffinityHeader = "X-Session"
	r := plainRoute()
	r.Caps.Affinity = llm.AffinityHeader
	f := freeze(t, d, r, fullPlan(t), llm.Options{Affinity: "k3y"})
	s, err := Send(t.Context(), &http.Client{}, f)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	collect(s)
	if _, err := s.Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}
	req := <-u.seen
	if req.path != "/v1/chat/completions" || string(req.body) != string(f.Body) {
		t.Errorf("path %q; body on the wire differs from the frozen one: %t", req.path, string(req.body) != string(f.Body))
	}
	for k, want := range map[string]string{
		"Authorization": "Bearer test-key",
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"X-Session":     "k3y",
	} {
		if got := req.header.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}

	// A second range is a no-op and Result keeps its answer.
	if again := collect(s); len(again) != 0 {
		t.Errorf("second range yielded %d events", len(again))
	}
	if res, err := s.Result(); err != nil || res.Text != "Hello, world." {
		t.Errorf("second Result = %+v, %v", res, err)
	}
}

func TestStreamToolCall(t *testing.T) {
	t.Parallel()
	// The server holds back most of the arguments until the test has seen
	// ToolStart, so the announcement cannot be waiting on them.
	u := newUpstream(t, reply{body: fixture(t, "tool.sse")})
	u.gate = make(chan struct{})
	s := open(t, u, plainRoute())

	var evs []llm.Event
	announced := make(chan struct{})
	go func() {
		select {
		case <-announced:
		case <-time.After(5 * time.Second):
		}
		close(u.gate)
	}()
	early := false
	for ev := range s.Events() {
		evs = append(evs, ev)
		if ev.Kind == llm.ToolStart {
			early = true
			close(announced)
		}
	}
	if !early {
		t.Fatal("no ToolStart event")
	}
	want := []llm.Event{
		{Kind: llm.TextDelta, Text: "Let me "},
		{Kind: llm.TextDelta, Text: "check."},
		{Kind: llm.ToolStart, Tool: "python"},
	}
	if !slices.Equal(evs, want) {
		t.Errorf("events = %+v, want %+v", evs, want)
	}
	res, err := s.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if res.Text != "Let me check." || res.Stop != llm.StopTool || len(res.ToolCalls) != 1 {
		t.Fatalf("result = %+v", res)
	}
	tc := res.ToolCalls[0]
	if tc.ID != "call_abc123" || tc.Name != "python" || tc.Code != "print(6 * 7)\nprint(\"<done>\")\n" ||
		tc.Args != nil || tc.Sibling != 0 || tc.Text != "" || tc.Reasoning != nil {
		t.Errorf("tool call = %+v", tc)
	}
	if u := res.Usage; u.Input != 1520 || u.Cached != 1500 || u.Output != 41 {
		t.Errorf("usage = %+v", u)
	}
}

func TestStreamSeveralToolCalls(t *testing.T) {
	t.Parallel()

	t.Run("interleaved fragments", func(t *testing.T) {
		t.Parallel()
		// two_tools.sse opens call 0, sends call 1 whole, then finishes
		// call 0's arguments.
		evs, res, err := run(t, plainRoute(), string(fixture(t, "two_tools.sse")))
		if err != nil {
			t.Fatalf("Result: %v", err)
		}
		starts := []string{}
		for _, ev := range evs {
			if ev.Kind == llm.ToolStart {
				starts = append(starts, ev.Tool)
			}
		}
		if !slices.Equal(starts, []string{"python", "shell"}) {
			t.Errorf("ToolStart events = %v, want one per call as its name arrives", starts)
		}
		if res.Stop != llm.StopTool || len(res.ToolCalls) != 2 {
			t.Fatalf("result = %+v", res)
		}
		first, second := res.ToolCalls[0], res.ToolCalls[1]
		if first.ID != "call_first" || first.Name != "python" || first.Code != "a = 1" || first.Sibling != 0 {
			t.Errorf("first call = %+v", first)
		}
		// "shell" is not declared in the request; a code-shaped call is
		// still decoded, and the turn loop rejects it by name.
		if second.ID != "call_second" || second.Name != "shell" || second.Code != "ls" || second.Sibling != 1 {
			t.Errorf("second call = %+v", second)
		}
	})

	t.Run("a tool with its own schema keeps its arguments raw", func(t *testing.T) {
		t.Parallel()
		delegate := core.ToolDef{Name: "delegate", Input: json.RawMessage(`{"type":"object","properties":{"task":{"type":"string"}}}`)}
		noargs := core.ToolDef{Name: "status", Input: json.RawMessage(`{"type":"object","properties":{}}`)}
		o := fullOpts(t)
		o.tools = append(o.tools, delegate, noargs)
		body := sse(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c0","type":"function","function":{"name":"delegate","arguments":"{\"task\": "}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"look <up>\", \"code\": \"x\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"c1","type":"function","function":{"name":"python","arguments":"{\"code\":\"1\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"id":"c2","type":"function","function":{"name":"status","arguments":""}}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		)
		u := newUpstream(t, reply{body: []byte(body)})
		d := plainDialect()
		d.URL = u.url
		s, err := Send(t.Context(), nil, freeze(t, d, plainRoute(), buildPlan(t, o), llm.Options{}))
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		collect(s)
		res, err := s.Result()
		if err != nil || len(res.ToolCalls) != 3 {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if c := res.ToolCalls[0]; c.Code != "" || string(c.Args) != `{"task": "look <up>", "code": "x"}` || c.Sibling != 0 {
			t.Errorf("own-schema call = %+v, want the arguments as written and no code", c)
		}
		if c := res.ToolCalls[1]; c.Code != "1" || c.Args != nil || c.Sibling != 1 {
			t.Errorf("default-schema call = %+v", c)
		}
		if c := res.ToolCalls[2]; string(c.Args) != "{}" || c.Sibling != 2 {
			t.Errorf("call with no arguments = %+v, want {}", c)
		}
	})

	t.Run("calls that share an index and come whole", func(t *testing.T) {
		t.Parallel()
		// Some servers number every call 0, send no IDs worth the name, and
		// pass the arguments as an object.
		_, res, err := run(t, plainRoute(), sse(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"python","arguments":{"code":"1"}}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"b","function":{"name":"python","arguments":"{\"code\":\"2\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		))
		if err != nil || len(res.ToolCalls) != 2 {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if a, b := res.ToolCalls[0], res.ToolCalls[1]; a.ID != "a" || a.Code != "1" || b.ID != "b" || b.Code != "2" || b.Sibling != 1 {
			t.Errorf("calls = %+v", res.ToolCalls)
		}
	})

	t.Run("a call with no ID is given one", func(t *testing.T) {
		t.Parallel()
		_, res, err := run(t, plainRoute(), sse(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"python","arguments":"{\"code\":\"1\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		))
		if err != nil || len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "call_0" {
			t.Errorf("result = %+v, %v", res, err)
		}
	})
}

func TestStreamReasoning(t *testing.T) {
	t.Parallel()
	const stop = `{"choices":[{"index":0,"delta":{"content":"42"},"finish_reason":"stop"}]}`
	decodePL := func(t *testing.T, r *core.Reasoning) payload {
		t.Helper()
		if r == nil {
			t.Fatal("no reasoning payload captured")
		}
		if r.Route != "openai/gpt-test" {
			t.Errorf("payload tagged %q", r.Route)
		}
		pl, err := decodePayload(r.Data)
		if err != nil {
			t.Fatal(err)
		}
		return *pl
	}

	for _, field := range []string{"reasoning_content", "reasoning"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			evs, res, err := run(t, replayRoute(), sse(
				`{"choices":[{"index":0,"delta":{"role":"assistant","content":null,"`+field+`":"Six sevens "}}]}`,
				`{"choices":[{"index":0,"delta":{"`+field+`":"are <42>."}}]}`,
				stop,
			))
			if err != nil {
				t.Fatalf("Result: %v", err)
			}
			want := []llm.Event{
				{Kind: llm.ReasoningDelta, Text: "Six sevens "},
				{Kind: llm.ReasoningDelta, Text: "are <42>."},
				{Kind: llm.TextDelta, Text: "42"},
			}
			if !slices.Equal(evs, want) {
				t.Errorf("events = %+v", evs)
			}
			if pl := decodePL(t, res.Reasoning); pl.Text != "Six sevens are <42>." || len(pl.Details) != 0 {
				t.Errorf("payload = %+v", pl)
			}
			if res.Text != "42" {
				t.Errorf("reasoning leaked into the reply: %q", res.Text)
			}
		})
	}

	t.Run("reasoning_details", func(t *testing.T) {
		t.Parallel()
		// OpenRouter streams the text of one item in fragments, repeats it in
		// reasoning, and sends the signature in a fragment of its own. An
		// encrypted item follows whole.
		const encrypted = `{"type":"reasoning.encrypted","data":"b64==","id":"r-2","format":"anthropic-claude-v1","index":1}`
		evs, res, err := run(t, replayRoute(), sse(
			`{"choices":[{"index":0,"delta":{"reasoning":"Six ","reasoning_details":[{"type":"reasoning.text","text":"Six ","signature":null,"format":"anthropic-claude-v1","index":0}]}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning":"sevens.","reasoning_details":[{"type":"reasoning.text","text":"sevens.","format":"anthropic-claude-v1","index":0}]}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","text":"","signature":"sig==","index":0},`+encrypted+`]}}]}`,
			stop,
		))
		if err != nil {
			t.Fatalf("Result: %v", err)
		}
		if got := said(evs, llm.ReasoningDelta); got != "Six sevens." {
			t.Errorf("reasoning deltas = %q, want the text once", got)
		}
		pl := decodePL(t, res.Reasoning)
		if pl.Text != "Six sevens." || len(pl.Details) != 2 {
			t.Fatalf("payload = %+v", pl)
		}
		const whole = `{"type":"reasoning.text","text":"Six sevens.","signature":"sig==","format":"anthropic-claude-v1","index":0}`
		if string(pl.Details[0]) != whole {
			t.Errorf("text item = %s\nwant      %s", pl.Details[0], whole)
		}
		if string(pl.Details[1]) != encrypted {
			t.Errorf("an item that arrived whole was changed: %s", pl.Details[1])
		}
	})

	t.Run("details alone still show their text", func(t *testing.T) {
		t.Parallel()
		evs, res, err := run(t, replayRoute(), sse(
			`{"choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.summary","summary":"Multiplied."}]}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","text":"a"}]}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","text":"b"}]}}]}`,
			stop,
		))
		if err != nil {
			t.Fatalf("Result: %v", err)
		}
		if got := said(evs, llm.ReasoningDelta); got != "Multiplied.ab" {
			t.Errorf("reasoning deltas = %q", got)
		}
		pl := decodePL(t, res.Reasoning)
		if len(pl.Details) != 2 || string(pl.Details[1]) != `{"type":"reasoning.text","text":"ab"}` {
			t.Errorf("details = %s", pl.Details)
		}
	})

	t.Run("a route that replays nothing keeps no payload", func(t *testing.T) {
		t.Parallel()
		evs, res, err := run(t, plainRoute(), sse(
			`{"choices":[{"index":0,"delta":{"reasoning":"Thinking."}}]}`,
			stop,
		))
		if err != nil {
			t.Fatalf("Result: %v", err)
		}
		if said(evs, llm.ReasoningDelta) != "Thinking." || res.Reasoning != nil {
			t.Errorf("events %+v, payload %+v; want the delta shown and nothing kept", evs, res.Reasoning)
		}
	})

	t.Run("no reasoning, no payload", func(t *testing.T) {
		t.Parallel()
		_, res, err := run(t, replayRoute(), sse(stop))
		if err != nil || res.Reasoning != nil {
			t.Errorf("payload = %+v, %v", res.Reasoning, err)
		}
	})

	t.Run("a captured payload goes back as it came", func(t *testing.T) {
		t.Parallel()
		const item = `{"type":"reasoning.text","text":"Run it.","signature":"s","index":0}`
		_, res, err := run(t, replayRoute(), sse(
			`{"choices":[{"index":0,"delta":{"reasoning":"Run it.","reasoning_details":[`+item+`]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"python","arguments":"{\"code\":\"1\"}"}}]},"finish_reason":"tool_calls"}]}`,
		))
		if err != nil || len(res.ToolCalls) != 1 {
			t.Fatalf("result = %+v, %v", res, err)
		}
		call := res.ToolCalls[0]
		call.Reasoning = res.Reasoning
		for style, want := range map[ReasoningStyle]string{
			ReasoningDetails: `"reasoning_details":[` + item + `],"tool_calls"`,
			ReasoningContent: `"reasoning_content":"Run it.","tool_calls"`,
		} {
			d := plainDialect()
			d.Reasoning = style
			p := buildPlan(t, fullOpts(t))
			p.AppendTail(call, core.ToolResult{ID: call.ID, Output: "1"})
			if raw := freeze(t, d, replayRoute(), p, llm.Options{}).Body; !strings.Contains(string(raw), want) {
				t.Errorf("style %d: request lacks %s:\n%s", style, want, raw)
			}
		}
	})
}

func TestStreamUsage(t *testing.T) {
	t.Parallel()
	const stop = `{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`
	cases := []struct {
		name  string
		usage string
		want  llm.Usage
	}{
		{
			"nested cached tokens and reasoning tokens",
			`{"prompt_tokens":900,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":800,"cache_write_tokens":0},"completion_tokens_details":{"reasoning_tokens":30},"cost":0.0001}`,
			llm.Usage{Input: 900, Cached: 800, CacheReported: true, Output: 50, Reasoning: 30},
		},
		{
			"flat cache hit tokens",
			`{"prompt_tokens":900,"completion_tokens":50,"prompt_cache_hit_tokens":640,"prompt_cache_miss_tokens":260}`,
			llm.Usage{Input: 900, Cached: 640, CacheReported: true, Output: 50},
		},
		{
			"both spellings agree",
			`{"prompt_tokens":900,"completion_tokens":50,"prompt_cache_hit_tokens":640,"prompt_tokens_details":{"cached_tokens":640}}`,
			llm.Usage{Input: 900, Cached: 640, CacheReported: true, Output: 50},
		},
		{
			"no cache report",
			`{"prompt_tokens":812,"total_tokens":830,"completion_tokens":18,"prompt_tokens_details":null}`,
			llm.Usage{Input: 812, Output: 18},
		},
		{
			"explicit cold cache",
			`{"prompt_tokens":900,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":0}}`,
			llm.Usage{Input: 900, Output: 50, CacheReported: true},
		},
		{
			"flat explicit cold cache",
			`{"prompt_tokens":900,"completion_tokens":50,"prompt_cache_hit_tokens":0}`,
			llm.Usage{Input: 900, Output: 50, CacheReported: true},
		},
		{
			"nested zero takes precedence",
			`{"prompt_tokens":900,"completion_tokens":50,"prompt_cache_hit_tokens":640,"prompt_tokens_details":{"cached_tokens":0}}`,
			llm.Usage{Input: 900, Output: 50, CacheReported: true},
		},
		{
			"empty details are not a report",
			`{"prompt_tokens":900,"completion_tokens":50,"prompt_tokens_details":{}}`,
			llm.Usage{Input: 900, Output: 50},
		},
		{
			"null count is not a report",
			`{"prompt_tokens":900,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":null},"prompt_cache_hit_tokens":null}`,
			llm.Usage{Input: 900, Output: 50},
		},
		{
			"negative count is not a report",
			`{"prompt_tokens":900,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":-1}}`,
			llm.Usage{Input: 900, Output: 50},
		},
		{
			"count exceeds input",
			`{"prompt_tokens":900,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":901}}`,
			llm.Usage{Input: 900, Output: 50},
		},
		{
			"malformed count does not fail the reply",
			`{"prompt_tokens":900,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":"bad"}}`,
			llm.Usage{Input: 900, Output: 50},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// With include_usage the totals come last, in a chunk of their own.
			_, res, err := run(t, plainRoute(), sse(stop, `{"choices":[],"usage":`+tc.usage+`}`))
			if err != nil {
				t.Fatalf("Result: %v", err)
			}
			got := res.Usage
			if string(got.Raw) != tc.usage {
				t.Errorf("raw usage = %s, want the provider's object", got.Raw)
			}
			got.Raw = nil
			if got.Input != tc.want.Input || got.Cached != tc.want.Cached || got.Output != tc.want.Output || got.Reasoning != tc.want.Reasoning || got.CacheReported != tc.want.CacheReported {
				t.Errorf("usage = %+v, want %+v", got, tc.want)
			}
		})
	}

	t.Run("none reported", func(t *testing.T) {
		t.Parallel()
		_, res, err := run(t, plainRoute(), sse(`{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":null}`))
		if err != nil || res.Usage.Raw != nil || res.Usage.Input != 0 {
			t.Errorf("usage = %+v, %v", res.Usage, err)
		}
	})
}

func TestStreamEndings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		body      string
		wantText  string
		wantStop  llm.StopReason
		wantCalls []string // tool call IDs
		wantCode  string   // code of the first call
	}{
		{name: "stop", body: string(fixture(t, "text.sse")), wantText: "Hello, world.", wantStop: llm.StopEnd},
		{name: "tool_calls", body: string(fixture(t, "two_tools.sse")), wantStop: llm.StopTool, wantCalls: []string{"call_first", "call_second"}, wantCode: "a = 1"},
		{name: "length drops the cut-off call", body: string(fixture(t, "length.sse")), wantText: "Running it now.", wantStop: llm.StopLength},
		{name: "content filter", body: string(fixture(t, "content_filter.sse")), wantText: "Sure, the first", wantStop: llm.StopFiltered},
		{name: "stop with half a call is a plain reply", body: string(fixture(t, "stop_mid_call.sse")), wantText: "On it.", wantStop: llm.StopEnd},
		{
			// vLLM-style: finish_reason "stop" on a tool call, reasoning in
			// reasoning_content, a null prompt_tokens_details, no blank line
			// after [DONE].
			name: "self-hosted tool call", body: string(fixture(t, "self_hosted_tool.sse")),
			wantStop: llm.StopTool, wantCalls: []string{"chatcmpl-tool-9f"}, wantCode: "print(1)",
		},
		{
			name: "refusal",
			body: sse(
				`{"choices":[{"index":0,"delta":{"role":"assistant","content":null,"refusal":"I can't "}}]}`,
				`{"choices":[{"index":0,"delta":{"refusal":"help with that."},"finish_reason":"stop"}]}`,
			),
			wantText: "I can't help with that.", wantStop: llm.StopRefused,
		},
		{
			name:     "a finish reason and no [DONE]",
			body:     "data: " + `{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n",
			wantText: "ok", wantStop: llm.StopEnd,
		},
		{
			name: "comment lines and other choices are skipped",
			body: ": OPENROUTER PROCESSING\n\n" + sse(
				`{"choices":[{"index":1,"delta":{"content":"other"}},{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
			),
			wantText: "ok", wantStop: llm.StopEnd,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			evs, res, err := run(t, plainRoute(), tc.body)
			if err != nil {
				t.Fatalf("Result: %v", err)
			}
			if res.Text != tc.wantText || said(evs, llm.TextDelta) != tc.wantText {
				t.Errorf("text = %q (stream %q), want %q", res.Text, said(evs, llm.TextDelta), tc.wantText)
			}
			if res.Stop != tc.wantStop {
				t.Errorf("stop = %s, want %s", res.Stop, tc.wantStop)
			}
			ids := []string{}
			for _, c := range res.ToolCalls {
				ids = append(ids, c.ID)
			}
			if !slices.Equal(ids, append([]string{}, tc.wantCalls...)) {
				t.Errorf("tool calls = %v, want %v", ids, tc.wantCalls)
			}
			if len(res.ToolCalls) > 0 && res.ToolCalls[0].Code != tc.wantCode {
				t.Errorf("code = %q, want %q", res.ToolCalls[0].Code, tc.wantCode)
			}
		})
	}
}

func TestStreamFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		body      string
		wantText  string
		wantClass llm.ErrorClass
		wantIs    error
		wantMsg   string
	}{
		{
			name: "error object after output", body: string(fixture(t, "error_mid.sse")),
			wantText: "Half a rep", wantClass: llm.ErrOverloaded, wantMsg: "had an error",
		},
		{
			name: "error object first, with a status for a code", body: string(fixture(t, "error_first.sse")),
			wantClass: llm.ErrOverloaded, wantMsg: "overloaded",
		},
		{
			name:      "top-level error shape",
			body:      sse(`{"object":"error","message":"Rate limit reached","type":"rate_limit_error","code":null}`),
			wantClass: llm.ErrRateLimited, wantMsg: "Rate limit reached",
		},
		{
			name:      "context overflow reported in the stream",
			body:      sse(`{"error":{"message":"This model's maximum context length is 131072 tokens.","code":400}}`),
			wantClass: llm.ErrContextOverflow,
		},
		{
			// OpenRouter: an error object, and a closing chunk whose
			// normalized finish reason is "error".
			name: "finish reason error with an error object",
			body: sse(
				`{"choices":[{"index":0,"delta":{"content":"Half"}}]}`,
				`{"error":{"code":502,"message":"Upstream provider disconnected"},"choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}]}`,
			),
			wantText: "Half", wantClass: llm.ErrOverloaded, wantMsg: "Upstream provider disconnected",
		},
		{
			name:     "finish reason error alone",
			body:     sse(`{"choices":[{"index":0,"delta":{"content":"Half"},"finish_reason":"error"}]}`),
			wantText: "Half", wantClass: llm.ErrUnknown, wantMsg: "finish_reason error",
		},
		{
			name:     "the body ends with no finish reason",
			body:     "data: " + `{"choices":[{"index":0,"delta":{"content":"Half a"}}]}` + "\n\n",
			wantText: "Half a", wantClass: llm.ErrTransport, wantIs: ErrIncomplete,
		},
		{
			name:     "[DONE] with no finish reason",
			body:     sse(`{"choices":[{"index":0,"delta":{"content":"Half a"}}]}`),
			wantText: "Half a", wantClass: llm.ErrTransport, wantIs: ErrIncomplete,
		},
		{name: "an empty body", body: "", wantClass: llm.ErrTransport, wantIs: ErrIncomplete},
		{
			name: "a chunk that is not JSON", body: sse(`{"choices":[{"index":0,"delta":{"content":"Hal`),
			wantClass: llm.ErrTransport,
		},
		{
			name: "tool arguments that are not JSON", body: string(fixture(t, "bad_tool_input.sse")),
			wantClass: llm.ErrUnknown, wantIs: ErrBadToolArgs,
		},
		{
			name: "a code tool called without code",
			body: sse(
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"python","arguments":"{\"script\":\"1\"}"}}]},"finish_reason":"tool_calls"}]}`,
			),
			wantClass: llm.ErrUnknown, wantIs: ErrBadToolArgs,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			evs, res, err := run(t, plainRoute(), tc.body)
			e, ok := errors.AsType[*llm.Error](err)
			if !ok {
				t.Fatalf("Result error = %v, want an *llm.Error", err)
			}
			if e.Class != tc.wantClass || e.Provider != "openai" || e.Status != 0 {
				t.Errorf("error = class %s, provider %q, status %d; want class %s", e.Class, e.Provider, e.Status, tc.wantClass)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("error = %v, want it to wrap %v", err, tc.wantIs)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not carry %q", err, tc.wantMsg)
			}
			// What arrived before the failure is still reported.
			if res.Text != tc.wantText || said(evs, llm.TextDelta) != tc.wantText || len(res.ToolCalls) != 0 {
				t.Errorf("partial result = %+v, want text %q and no calls", res, tc.wantText)
			}
		})
	}
}

func TestSendClassifiesFailedResponses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		header map[string]string
		body   string
		want   llm.ErrorClass
	}{
		{"rate limited", http.StatusTooManyRequests, map[string]string{"Retry-After": "3"}, `{"error":{"message":"Rate limit reached for requests","type":"requests","code":"rate_limit_exceeded"}}`, llm.ErrRateLimited},
		{"bad request", http.StatusBadRequest, nil, `{"error":{"message":"messages with role 'tool' must be a response to a preceeding message","type":"invalid_request_error"}}`, llm.ErrBadRequest},
		{"context overflow by code", http.StatusBadRequest, nil, `{"error":{"message":"Too long.","type":"invalid_request_error","code":"context_length_exceeded"}}`, llm.ErrContextOverflow},
		{"context overflow by message", http.StatusBadRequest, nil, `{"object":"error","message":"This model's maximum context length is 32768 tokens. However, you requested 40000 tokens","code":400}`, llm.ErrContextOverflow},
		{"out of quota on a 429", http.StatusTooManyRequests, nil, `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`, llm.ErrQuota},
		{"out of credit", http.StatusPaymentRequired, nil, `{"error":{"message":"Insufficient credits","code":402}}`, llm.ErrQuota},
		{"unauthorized", http.StatusUnauthorized, nil, `{"error":{"message":"No auth credentials found","code":401}}`, llm.ErrAuth},
		{"still loading", http.StatusServiceUnavailable, nil, `{"object":"error","message":"The model is still loading.","type":"ServiceUnavailableError","code":503}`, llm.ErrOverloaded},
		// A 5xx is the server's trouble whatever its body quotes.
		{"overloaded body that mentions context", http.StatusBadGateway, nil, `maximum context length`, llm.ErrOverloaded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u := newUpstream(t, reply{status: tc.status, header: tc.header, body: []byte(tc.body)})
			d := plainDialect()
			d.URL = u.url
			_, err := Send(t.Context(), nil, freeze(t, d, plainRoute(), fullPlan(t), llm.Options{}))
			e, ok := errors.AsType[*llm.Error](err)
			if !ok {
				t.Fatalf("Send error = %v, want an *llm.Error", err)
			}
			if e.Class != tc.want || e.Status != tc.status || e.Provider != "openai" {
				t.Errorf("error = class %s, status %d, provider %q; want %s", e.Class, e.Status, e.Provider, tc.want)
			}
			if tc.header != nil && e.RetryAfter != 3*time.Second {
				t.Errorf("RetryAfter = %v, want the provider's 3s hint", e.RetryAfter)
			}
		})
	}

	t.Run("an unreachable server is a transport error", func(t *testing.T) {
		t.Parallel()
		d := plainDialect()
		d.URL = "http://127.0.0.1:1/chat/completions"
		_, err := Send(t.Context(), nil, freeze(t, d, plainRoute(), fullPlan(t), llm.Options{}))
		if llm.ClassOf(err) != llm.ErrTransport {
			t.Errorf("Send error = %v, want a transport error", err)
		}
	})
}

func TestStreamCancel(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, reply{body: fixture(t, "text.sse"), hold: true})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d := plainDialect()
	d.URL = u.url
	s, err := Send(ctx, nil, freeze(t, d, plainRoute(), fullPlan(t), llm.Options{}))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	returned := make(chan struct{})
	var evs []llm.Event
	go func() {
		defer close(returned)
		for ev := range s.Events() {
			evs = append(evs, ev)
			cancel() // the server now sends nothing more
		}
	}()
	within(t, returned, "Events to return after cancellation")
	within(t, u.gone, "the server to see the connection close")

	res, err := s.Result()
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Result error = %v, want context.Canceled", err)
	}
	if len(evs) != 1 || res.Text != "Hello" {
		t.Errorf("events = %+v, result text = %q; want the one delta sent before the cancel", evs, res.Text)
	}
}

func TestStreamBreak(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, reply{body: fixture(t, "text.sse"), hold: true})
	s := open(t, u, plainRoute())
	n := 0
	for range s.Events() {
		n++
		break
	}
	// Nothing cancelled the context: leaving the loop alone must hang up.
	within(t, u.gone, "the server to see the connection close")

	res, err := s.Result()
	if !errors.Is(err, ErrIncomplete) {
		t.Errorf("Result error = %v, want ErrIncomplete", err)
	}
	if n != 1 || res.Text != "Hello" {
		t.Errorf("read %d events, result text = %q", n, res.Text)
	}
}

func TestResultWithoutEvents(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, reply{body: fixture(t, "text.sse"), hold: true})
	s := open(t, u, plainRoute())
	if _, err := s.Result(); !errors.Is(err, ErrNotDrained) {
		t.Errorf("Result error = %v, want ErrNotDrained", err)
	}
	within(t, u.gone, "the server to see the connection close")
	if evs := collect(s); len(evs) != 0 {
		t.Errorf("Events after Result yielded %d events", len(evs))
	}
}
