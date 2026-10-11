package messages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
)

func TestStreamText(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t, step{file: "text.sse"})
	f := freeze(t, testDialect(api.url), testRoute(), fullPlan(t), llm.Options{})
	s, err := Send(t.Context(), http.DefaultClient, f)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	evs := collect(t, s)
	want := []llm.Event{
		{Kind: llm.TextDelta, Text: "Hello"},
		{Kind: llm.TextDelta, Text: ", world."},
	}
	if !slices.Equal(evs, want) {
		t.Errorf("events = %+v, want %+v", evs, want)
	}
	res, err := s.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if res.Text != "Hello, world." || len(res.ToolCalls) != 0 || res.Stop != llm.StopEnd {
		t.Errorf("result = %+v", res)
	}

	// 12 uncached + 300 written + 2000 read. The raw record is what
	// message_start reported with message_delta's final count written in.
	u := res.Usage
	if u.Input != 2312 || u.Cached != 2000 || u.Output != 9 || u.Reasoning != 0 {
		t.Errorf("usage = %+v, want input 2312, cached 2000, output 9", u)
	}
	const raw = `{"input_tokens":12,"cache_creation_input_tokens":300,"cache_read_input_tokens":2000,"output_tokens":9}`
	if string(u.Raw) != raw {
		t.Errorf("raw usage = %s, want %s", u.Raw, raw)
	}

	// The reply opened with a thinking block. The payload is that block as
	// it arrived, and where the 13 bytes of text sat after it.
	payload := string(payloadOf(blockSlot(`{"type":"thinking","thinking":"","signature":"EqQBCgIYAhIM"}`), textSlot(13)))
	if res.Reasoning == nil || res.Reasoning.Route != "testprov/claude-opus-5-5" || string(res.Reasoning.Data) != payload {
		t.Errorf("reasoning = %+v, want %s for this route", res.Reasoning, payload)
	}

	reqs := api.seen()
	if len(reqs) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(reqs))
	}
	if !bytes.Equal(reqs[0].body, f.Body) {
		t.Errorf("body on the wire differs from the frozen request")
	}
	for k, want := range map[string]string{
		"X-Api-Key":         "test-key",
		"Anthropic-Version": "2023-06-01",
		"Anthropic-Beta":    "server-side-fallback-2026-07-01",
		"Content-Type":      "application/json",
		"Accept":            "text/event-stream",
	} {
		if got := reqs[0].header.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}

	// A second range is a no-op and Result keeps its answer.
	if again := collect(t, s); len(again) != 0 {
		t.Errorf("second range yielded %d events", len(again))
	}
	if res2, err := s.Result(); err != nil || res2.Text != res.Text {
		t.Errorf("second Result = %+v, %v", res2, err)
	}

	// The same frozen request can be sent again, unchanged.
	s, err = Send(t.Context(), nil, f)
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	collect(t, s)
	if res, err := s.Result(); err != nil || res.Text != "Hello, world." {
		t.Errorf("second send: %+v, %v", res, err)
	}
	if reqs := api.seen(); len(reqs) != 2 || !bytes.Equal(reqs[1].body, f.Body) {
		t.Errorf("the second send did not repeat the first")
	}
}

func TestStreamToolUse(t *testing.T) {
	t.Parallel()
	// The server holds back most of the arguments until the test has seen
	// ToolStart, so the announcement cannot be waiting on them.
	api := newFakeAPI(t, step{file: "tool.sse"})
	api.gate = make(chan struct{})
	s := open(t, api, testRoute())

	var evs []llm.Event
	for ev := range s.Events() {
		evs = append(evs, ev)
		if ev.Kind == llm.ToolStart {
			close(api.gate)
		}
	}
	if api.stalled.Load() {
		t.Fatal("ToolStart did not arrive until the arguments had finished")
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
	if res.Text != "Let me check." || res.Stop != llm.StopTool || res.Reasoning != nil {
		t.Errorf("result = %+v", res)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("%d tool calls, want 1", len(res.ToolCalls))
	}
	tc := res.ToolCalls[0]
	if tc.ID != "toolu_01A" || tc.Name != "python" || tc.Code != "print(6 * 7)\nprint(\"<done>\")\n" || tc.Sibling != 0 || tc.Args != nil {
		t.Errorf("tool call = %+v", tc)
	}
	if u := res.Usage; u.Input != 1520 || u.Cached != 1500 || u.Output != 41 {
		t.Errorf("usage = %+v, want input 1520, cached 1500, output 41", u)
	}
}

func TestStreamThinking(t *testing.T) {
	t.Parallel()
	evs, res, err := run(t, testRoute(), "thinking_tool.sse")
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if got, want := text(evs, llm.ReasoningDelta), `Multiplying 7 by 8 with a <script> & "quotes".`; got != want {
		t.Errorf("visible reasoning = %q, want %q", got, want)
	}
	if got := text(evs, llm.TextDelta); got != "Let me check <that>." || res.Text != got {
		t.Errorf("text = %q (result %q)", got, res.Text)
	}
	if last := evs[len(evs)-1]; last.Kind != llm.ToolStart || last.Tool != "python" {
		t.Errorf("last event = %+v, want the tool start", last)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "toolu_01B" || res.ToolCalls[0].Code != "print(7 * 8)  # <b>" {
		t.Errorf("tool calls = %+v", res.ToolCalls)
	}
	// The turn as it arrived: every reasoning block whole, the first with
	// the empty text an upstream that hides reasoning sends, and between
	// them only the places of the text and the call.
	want := string(payloadOf(blockSlot(thinking1), textSlot(20), blockSlot(thinking2), blockSlot(redacted), toolSlot))
	if res.Reasoning == nil || res.Reasoning.Route != testRoute().ID() || string(res.Reasoning.Data) != want {
		t.Errorf("reasoning = %+v\nwant: %s", res.Reasoning, want)
	}
	// What was said and run is not in it; that is the log's to hold.
	if bytes.Contains(res.Reasoning.Data, []byte("check")) || bytes.Contains(res.Reasoning.Data, []byte("print")) {
		t.Errorf("the payload carries the turn's text or code: %s", res.Reasoning.Data)
	}

	t.Run("not captured on a route that never takes it back", func(t *testing.T) {
		t.Parallel()
		r := testRoute()
		r.Caps.Replay = llm.ReplayNone
		evs, res, err := run(t, r, "thinking_tool.sse")
		if err != nil || res.Reasoning != nil {
			t.Errorf("reasoning = %+v, err %v; want none", res.Reasoning, err)
		}
		// It is still shown while it streams.
		if text(evs, llm.ReasoningDelta) == "" {
			t.Error("visible reasoning was not streamed")
		}
	})

	t.Run("a block keeps the members it arrived with", func(t *testing.T) {
		t.Parallel()
		// An upstream may send a block with no signature at all, or with a
		// member this codec has never heard of. Neither is tidied away.
		sse := strings.Join([]string{
			`: keep-alive`,
			``,
			`data: {"type":"message_start","message":{"usage":{"input_tokens":3,"output_tokens":1}}}`,
			``,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"A"}}`,
			``,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"B"}}`,
			``,
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"thinking","origin":{"n": 1},"thinking":"","signature":""}}`,
			``,
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"sig"}}`,
			``,
			`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":"Hi"}}`,
			``,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5,"server_tool_use":{"n":0}}}`,
			``,
			`data: {"type":"message_stop"}`,
			``,
			``,
		}, "\n")
		s := open(t, newFakeAPI(t, step{data: []byte(sse)}), autoRoute())
		evs := collect(t, s)
		res, err := s.Result()
		if err != nil {
			t.Fatalf("Result: %v", err)
		}
		want := string(payloadOf(
			blockSlot(`{"type":"thinking","thinking":"AB"}`),
			blockSlot(`{"type":"thinking","origin":{"n":1},"thinking":"","signature":"sig"}`),
			textSlot(2),
		))
		if res.Reasoning == nil || string(res.Reasoning.Data) != want {
			t.Errorf("reasoning = %+v\nwant: %s", res.Reasoning, want)
		}
		if text(evs, llm.ReasoningDelta) != "AB" || res.Text != "Hi" || res.Stop != llm.StopEnd {
			t.Errorf("events = %+v, result = %+v", evs, res)
		}
		const raw = `{"input_tokens":3,"output_tokens":5,"server_tool_use":{"n":0}}`
		if string(res.Usage.Raw) != raw || res.Usage.Input != 3 || res.Usage.Output != 5 {
			t.Errorf("usage = %+v, raw %s", res.Usage, res.Usage.Raw)
		}
	})
}

func TestReasoningRoundTrip(t *testing.T) {
	t.Parallel()
	// What the decoder captures from one step is what the next request
	// returns: the turn in the order it arrived, its reasoning blocks byte
	// for byte and its text and call rebuilt from what the log holds.
	p := buildPlan(t, fullOpts(t))
	step1 := body(t, p)
	open1, ok := bytes.CutSuffix(step1, []byte(`]}`))
	if !ok {
		t.Fatalf("request does not end with its messages: %s", step1)
	}

	_, res, err := run(t, testRoute(), "thinking_tool.sse")
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	// As the turn loop does: the step's text and reasoning ride on its call.
	call := res.ToolCalls[0]
	call.Text, call.Reasoning = res.Text, res.Reasoning
	p.AppendTail(call, core.ToolResult{ID: call.ID, Output: "56\n"})
	step2 := body(t, p)

	turn := `{"role":"assistant","content":[` + thinking1 +
		`,{"type":"text","text":"Let me check <that>."},` + thinking2 + `,` + redacted +
		`,{"type":"tool_use","id":"toolu_01B","name":"python","input":{"code":"print(7 * 8)  # <b>"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01B","content":"56\n"}]}]}`
	if want := append(open1, ","+turn...); !bytes.Equal(step2, want) {
		t.Errorf("step 2 request\n got: %s\nwant: %s", step2, want)
	}

	// A second tool step: everything sent so far is again an exact prefix.
	_, res, err = run(t, testRoute(), "tool.sse")
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	next := res.ToolCalls[0]
	next.Text, next.Reasoning = res.Text, res.Reasoning
	p.AppendTail(next, core.ToolResult{ID: next.ID, Output: "42\n<done>\n", IsError: true})
	step3 := body(t, p)
	if !bytes.HasPrefix(step3, bytes.TrimSuffix(step2, []byte(`]}`))) {
		t.Errorf("step 3 rewrote what step 2 sent\nstep 2: %s\nstep 3: %s", step2, step3)
	}
	if n := bytes.Count(step3, []byte(thinking2)); n != 1 {
		t.Errorf("step 3 carries the first turn's blocks %d times, want once", n)
	}

	// The breakpoints are where they were before the tool steps.
	before, after := decode(t, step1), decode(t, step3)
	if got, want := breakpoints(&after), breakpoints(&before); !slices.Equal(got, want) || len(got) != 4 {
		t.Errorf("breakpoints = %v, want the four of step 1 %v", got, want)
	}
	if bytes.Contains(call.Reasoning.Data, []byte("cache_control")) {
		t.Errorf("the payload carries cache_control: %s", call.Reasoning.Data)
	}
}

func TestReasoningRoundTripAfterFallback(t *testing.T) {
	t.Parallel()
	// A classifier declined mid-message and another model finished it. What
	// goes back is the text from both sides of the boundary, where it was,
	// and the reasoning and call of the model that took over. The declined
	// model's reasoning and call do not, and neither does the marker.
	_, res, err := run(t, testRoute(), "fallback_tool.sse")
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	p := buildPlan(t, fullOpts(t))
	call := res.ToolCalls[0]
	call.Text, call.Reasoning = res.Text, res.Reasoning
	p.AppendTail(call, core.ToolResult{ID: call.ID, Output: "ok"})
	msgs := rawMessages(t, body(t, p))
	const want = `{"role":"assistant","content":[{"type":"text","text":"Before. "},` +
		`{"type":"thinking","thinking":"","signature":"fallbackModelSig"},` +
		`{"type":"text","text":"After."},` +
		`{"type":"tool_use","id":"toolu_kept","name":"python","input":{"code":"y"}}]}`
	if got := msgs[len(msgs)-2]; got != want {
		t.Errorf("replayed turn\n got: %s\nwant: %s", got, want)
	}
}

func TestReplyWithInterleavedReasoningRoundTrip(t *testing.T) {
	t.Parallel()
	// A final reply, no tool call, on a route that wants reasoning back on
	// every later turn: the model thought, wrote, thought again and wrote
	// again. The next turn's request has to carry the reply in that order.
	r := autoRoute()
	sse := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":30,"cache_read_input_tokens":900,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Two parts."}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-a"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"First, "}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"café. "}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"thinking_delta","thinking":"Now the rest."}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"signature_delta","signature":"sig-b"}}`,
		`data: {"type":"content_block_stop","index":2}`,
		`data: {"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"Second."}}`,
		`data: {"type":"content_block_stop","index":3}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":40}}`,
		`data: {"type":"message_stop"}`,
	}, "\n\n") + "\n\n"
	s := open(t, newFakeAPI(t, step{data: []byte(sse)}), r)
	collect(t, s)
	res, err := s.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	const (
		thinkA = `{"type":"thinking","thinking":"Two parts.","signature":"sig-a"}`
		thinkB = `{"type":"thinking","thinking":"Now the rest.","signature":"sig-b"}`
	)
	if res.Text != "First, café. Second." || res.Stop != llm.StopEnd || len(res.ToolCalls) != 0 {
		t.Fatalf("result = %+v", res)
	}
	// "First, café. " is 14 bytes: lengths are bytes, not characters.
	want := string(payloadOf(blockSlot(thinkA), textSlot(14), blockSlot(thinkB), textSlot(7)))
	if res.Reasoning == nil || res.Reasoning.Route != r.ID() || string(res.Reasoning.Data) != want {
		t.Fatalf("reasoning = %+v\nwant: %s", res.Reasoning, want)
	}

	// The reply is logged with its payload and rendered on the next turn.
	p := buildPlan(t, planOpts{
		system: "sys",
		tools:  []core.ToolDef{pythonTool()},
		route:  r.ID(),
		events: []core.Event{
			event(t, 1, core.KindUserMsg, core.MsgBody{Text: "Two things, please."}),
			event(t, 2, core.KindAgentMsg, core.MsgBody{Text: res.Text, Reasoning: res.Reasoning}),
			event(t, 3, core.KindUserMsg, core.MsgBody{Text: "Thanks"}),
		},
	})
	msgs := rawMessages(t, bodyFor(t, r, p))
	reply := `{"role":"assistant","content":[` + thinkA + `,{"type":"text","text":"First, café. "},` + thinkB + `,{"type":"text","text":"Second."}]}`
	if len(msgs) != 3 || msgs[1] != reply {
		t.Errorf("messages:\n%s\nwant the reply as: %s", strings.Join(msgs, "\n"), reply)
	}
}

func TestStreamSeveralToolCalls(t *testing.T) {
	t.Parallel()
	evs, res, err := run(t, testRoute(), "two_tools.sse")
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	wantEvs := []llm.Event{{Kind: llm.ToolStart, Tool: "python"}, {Kind: llm.ToolStart, Tool: "shell"}}
	if !slices.Equal(evs, wantEvs) {
		t.Errorf("events = %+v, want %+v", evs, wantEvs)
	}
	if res.Stop != llm.StopTool || len(res.ToolCalls) != 2 {
		t.Fatalf("result = %+v, want two tool calls", res)
	}
	first, second := res.ToolCalls[0], res.ToolCalls[1]
	if first.ID != "toolu_first" || first.Name != "python" || first.Code != "a = 1" || first.Sibling != 0 {
		t.Errorf("first call = %+v", first)
	}
	// The second names a tool the request did not declare; it is passed on
	// for the turn loop to refuse.
	if second.ID != "toolu_second" || second.Name != "shell" || second.Code != "ls" || second.Sibling != 1 {
		t.Errorf("second call = %+v", second)
	}
}

func TestStreamToolArguments(t *testing.T) {
	t.Parallel()
	sse := func(name, start string, fragments ...string) []byte {
		lines := make([]string, 0, len(fragments)+5)
		lines = append(lines,
			`data: {"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_x","name":"`+name+`","input":`+start+`}}`,
		)
		for _, frag := range fragments {
			quoted, err := json.Marshal(frag)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+string(quoted)+`}}`)
		}
		lines = append(lines,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
			`data: {"type":"message_stop"}`,
		)
		return []byte(strings.Join(lines, "\n\n") + "\n\n")
	}
	stream := func(t *testing.T, data []byte) (llm.Result, error) {
		t.Helper()
		o := fullOpts(t)
		o.tools = []core.ToolDef{pythonTool(), lookupTool()}
		api := newFakeAPI(t, step{data: data})
		s, err := Send(t.Context(), http.DefaultClient, freeze(t, testDialect(api.url), testRoute(), buildPlan(t, o), llm.Options{}))
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		collect(t, s)
		return s.Result()
	}

	for _, tc := range []struct {
		name     string
		data     []byte
		wantCode string
		wantArgs string
		wantErr  bool
	}{
		{name: "a code tool fills Code", data: sse("python", `{}`, `{"code": "pri`, `nt(1)"}`), wantCode: "print(1)"},
		{name: "a tool with its own schema fills Args, as written", data: sse("lookup", `{}`, `{"word": "café",`, ` "limit": 3}`), wantArgs: `{"word": "café", "limit": 3}`},
		{name: "its own schema even when the arguments look like code", data: sse("lookup", `{}`, `{"code":"x"}`), wantArgs: `{"code":"x"}`},
		{name: "no fragments means no arguments", data: sse("lookup", `{}`), wantArgs: `{}`},
		{name: "arguments that came whole in the start event", data: sse("lookup", `{"word":"w"}`), wantArgs: `{"word":"w"}`},
		{name: "an undeclared tool with code", data: sse("shell", `{}`, `{"code":"ls"}`), wantCode: "ls"},
		{name: "an undeclared tool with anything else", data: sse("search", `{}`, `{"q":"x"}`), wantArgs: `{"q":"x"}`},
		{name: "a code tool without code", data: sse("python", `{}`, `{"script":"x"}`), wantErr: true},
		{name: "a code tool with no arguments", data: sse("python", `{}`), wantErr: true},
		{name: "arguments cut off", data: sse("lookup", `{}`, `{"word": "ca`), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := stream(t, tc.data)
			if tc.wantErr {
				e, ok := errors.AsType[*llm.Error](err)
				if !ok || !errors.Is(err, ErrBadToolInput) || e.Provider != "testprov" || len(res.ToolCalls) != 0 {
					t.Errorf("err = %v with %d calls, want ErrBadToolInput and none", err, len(res.ToolCalls))
				}
				return
			}
			if err != nil || len(res.ToolCalls) != 1 {
				t.Fatalf("result = %+v, %v", res, err)
			}
			if c := res.ToolCalls[0]; c.Code != tc.wantCode || string(c.Args) != tc.wantArgs || c.ID != "toolu_x" {
				t.Errorf("call = code %q, args %s; want %q, %s", c.Code, c.Args, tc.wantCode, tc.wantArgs)
			}
		})
	}
}

func TestStreamEndings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		file      string
		wantText  string
		wantTool  string // tool call ID, "" for none
		wantStop  llm.StopReason
		reasoning string // payload, "" for none
	}{
		{name: "max_tokens drops the cut-off call", file: "max_tokens.sse", wantText: "Running it now.", wantStop: llm.StopLength},
		{name: "refusal", file: "refusal.sse", wantText: "Sure, the first", wantStop: llm.StopRefused},
		{name: "fallback discards the declined call", file: "fallback.sse", wantText: "Before. After.", wantStop: llm.StopEnd},
		{
			// The declined model's reasoning and tool call must not go back;
			// its text and everything after the boundary do.
			name: "fallback keeps what follows the boundary", file: "fallback_tool.sse",
			wantText: "Before. After.", wantTool: "toolu_kept", wantStop: llm.StopTool,
			reasoning: `{"v":1,"turn":[{"text":8},{"block":{"type":"thinking","thinking":"","signature":"fallbackModelSig"}},{"text":6},{"tool":true}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			evs, res, err := run(t, testRoute(), tc.file)
			if err != nil {
				t.Fatalf("Result: %v", err)
			}
			if res.Text != tc.wantText || text(evs, llm.TextDelta) != tc.wantText {
				t.Errorf("text = %q (stream %q), want %q", res.Text, text(evs, llm.TextDelta), tc.wantText)
			}
			if res.Stop != tc.wantStop {
				t.Errorf("stop = %s, want %s", res.Stop, tc.wantStop)
			}
			got := ""
			if len(res.ToolCalls) == 1 {
				got = res.ToolCalls[0].ID
			}
			if got != tc.wantTool || len(res.ToolCalls) > 1 {
				t.Errorf("tool calls = %+v, want %q", res.ToolCalls, tc.wantTool)
			}
			payload := ""
			if res.Reasoning != nil {
				payload = string(res.Reasoning.Data)
			}
			if payload != tc.reasoning {
				t.Errorf("reasoning = %s, want %s", payload, tc.reasoning)
			}
		})
	}

	t.Run("invalid tool input", func(t *testing.T) {
		t.Parallel()
		_, res, err := run(t, testRoute(), "bad_tool_input.sse")
		if !errors.Is(err, ErrBadToolInput) || llm.ClassOf(err).Retryable() || len(res.ToolCalls) != 0 {
			t.Errorf("err = %v with %d calls, want ErrBadToolInput, not retryable, no calls", err, len(res.ToolCalls))
		}
	})
}

func TestStopReasons(t *testing.T) {
	t.Parallel()
	for reason, want := range map[string]llm.StopReason{
		"end_turn":                      llm.StopEnd,
		"stop_sequence":                 llm.StopEnd,
		"pause_turn":                    llm.StopEnd,
		"":                              llm.StopEnd,
		"something_new":                 llm.StopEnd,
		"tool_use":                      llm.StopTool,
		"max_tokens":                    llm.StopLength,
		"model_context_window_exceeded": llm.StopLength,
		"refusal":                       llm.StopRefused,
		"content_filter":                llm.StopFiltered,
	} {
		if got := stopOf(reason); got != want {
			t.Errorf("stop_reason %q = %s, want %s", reason, got, want)
		}
	}
}

func TestStreamErrorEvent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, file, wantText string
	}{
		{"after output", "error_mid.sse", "Half a rep"},
		{"before output", "error_first.sse", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			api := newFakeAPI(t, step{file: tc.file}, step{file: "text.sse"})
			s := open(t, api, testRoute())
			evs := collect(t, s)
			res, err := s.Result()
			e, ok := errors.AsType[*llm.Error](err)
			if !ok || e.Class != llm.ErrOverloaded || e.Provider != "testprov" || e.Status != 0 {
				t.Fatalf("Result error = %v, want an overloaded *llm.Error from this provider", err)
			}
			if !strings.Contains(err.Error(), "overloaded_error") || !strings.Contains(err.Error(), "Overloaded") {
				t.Errorf("error %q does not say what the upstream said", err)
			}
			if text(evs, llm.TextDelta) != tc.wantText || res.Text != tc.wantText {
				t.Errorf("text = %q / %q, want what arrived before the error", text(evs, llm.TextDelta), res.Text)
			}
			// The codec never sends a request again; that is the client's call.
			if n := len(api.seen()); n != 1 {
				t.Errorf("server saw %d requests, want 1", n)
			}
		})
	}
}

func TestStreamBroken(t *testing.T) {
	t.Parallel()
	whole := fixture(t, "text.sse")
	cut, _, ok := bytes.Cut(whole, []byte("event: message_delta"))
	if !ok {
		t.Fatal("fixture has no message_delta")
	}

	t.Run("cut before the model stopped", func(t *testing.T) {
		t.Parallel()
		s := open(t, newFakeAPI(t, step{data: cut}), testRoute())
		evs := collect(t, s)
		res, err := s.Result()
		e, ok := errors.AsType[*llm.Error](err)
		if !ok || e.Class != llm.ErrTransport || !errors.Is(err, ErrIncomplete) {
			t.Fatalf("Result error = %v, want a transport error wrapping ErrIncomplete", err)
		}
		if res.Text != "Hello, world." || text(evs, llm.TextDelta) != res.Text || res.Usage.Input != 2312 {
			t.Errorf("partial result = %+v", res)
		}
	})

	t.Run("message_stop missing after the stop reason", func(t *testing.T) {
		t.Parallel()
		noStop, _, ok := bytes.Cut(whole, []byte("event: message_stop"))
		if !ok {
			t.Fatal("fixture has no message_stop")
		}
		s := open(t, newFakeAPI(t, step{data: noStop}), testRoute())
		collect(t, s)
		if res, err := s.Result(); err != nil || res.Text != "Hello, world." || res.Stop != llm.StopEnd {
			t.Errorf("result = %+v, %v; the call had finished", res, err)
		}
	})

	t.Run("an event that is not JSON", func(t *testing.T) {
		t.Parallel()
		s := open(t, newFakeAPI(t, step{data: append(slices.Clone(cut), "data: {\"type\":\n\n"...)}), testRoute())
		collect(t, s)
		if _, err := s.Result(); llm.ClassOf(err) != llm.ErrTransport {
			t.Errorf("Result error = %v, want a transport error", err)
		}
	})
}

func TestStreamCancel(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t, step{file: "text.sse", hold: true})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	s, err := Send(ctx, http.DefaultClient, freeze(t, testDialect(api.url), testRoute(), fullPlan(t), llm.Options{}))
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
	within(t, api.gone, "the server to see the connection close")

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
	api := newFakeAPI(t, step{file: "text.sse", hold: true})
	s := open(t, api, testRoute())
	n := 0
	for range s.Events() {
		n++
		break
	}
	// Nothing cancelled the context: leaving the loop alone must hang up.
	within(t, api.gone, "the server to see the connection close")

	res, err := s.Result()
	if !errors.Is(err, ErrAbandoned) || llm.ClassOf(err).Retryable() {
		t.Errorf("Result error = %v, want ErrAbandoned, not retryable", err)
	}
	if n != 1 || res.Text != "Hello" {
		t.Errorf("read %d events, result text = %q", n, res.Text)
	}
}

func TestResultWithoutEvents(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t, step{file: "text.sse", hold: true})
	s := open(t, api, testRoute())
	if _, err := s.Result(); !errors.Is(err, ErrAbandoned) {
		t.Errorf("Result error = %v, want ErrAbandoned", err)
	}
	within(t, api.gone, "the server to see the connection close")
	if evs := collect(t, s); len(evs) != 0 {
		t.Errorf("Events after Result yielded %d events", len(evs))
	}
}

func TestSendClassifiesFailures(t *testing.T) {
	t.Parallel()
	body := func(typ, msg string) []byte {
		return []byte(`{"type":"error","error":{"type":"` + typ + `","message":"` + msg + `"},"request_id":"req_1"}`)
	}
	for _, tc := range []struct {
		name   string
		status int
		header map[string]string
		data   []byte
		file   string
		want   llm.ErrorClass
		retry  time.Duration
	}{
		{name: "rate limit", status: 429, header: map[string]string{"Retry-After": "3"}, file: "error_429.json", want: llm.ErrRateLimited, retry: 3 * time.Second},
		{name: "overloaded", status: 529, file: "error_529.json", want: llm.ErrOverloaded},
		{name: "bad request", status: 400, file: "error_400.json", want: llm.ErrBadRequest},
		{name: "prompt too long", status: 400, data: body("invalid_request_error", "prompt is too long: 1200000 tokens > 1000000 maximum"), want: llm.ErrContextOverflow},
		{name: "context length", status: 400, data: body("invalid_request_error", "This model's maximum context length is 1000000 tokens."), want: llm.ErrContextOverflow},
		{name: "request too large", status: 413, data: body("request_too_large", "Request exceeds the maximum allowed number of bytes."), want: llm.ErrContextOverflow},
		{name: "authentication", status: 401, data: body("authentication_error", "invalid x-api-key"), want: llm.ErrAuth},
		{name: "permission", status: 403, data: body("permission_error", "not allowed"), want: llm.ErrAuth},
		{name: "billing", status: 400, data: body("billing_error", "Your credit balance is too low."), want: llm.ErrQuota},
		{name: "payment required", status: 402, data: body("billing_error", "out of credit"), want: llm.ErrQuota},
		{name: "server error", status: 500, data: body("api_error", "internal"), want: llm.ErrOverloaded},
		{name: "overloaded behind a gateway's status", status: 400, data: body("overloaded_error", "Overloaded"), want: llm.ErrOverloaded},
		{name: "a body with no error type keeps the status", status: 404, data: []byte(`not found`), want: llm.ErrBadRequest},
		{name: "an unknown type keeps the status", status: 429, data: body("slow_down", "later"), want: llm.ErrRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			api := newFakeAPI(t, step{status: tc.status, header: tc.header, file: tc.file, data: tc.data}, step{file: "text.sse"})
			_, err := Send(t.Context(), http.DefaultClient, freeze(t, testDialect(api.url), testRoute(), fullPlan(t), llm.Options{}))
			e, ok := errors.AsType[*llm.Error](err)
			if !ok {
				t.Fatalf("err = %v, want an *llm.Error", err)
			}
			if e.Class != tc.want || e.Status != tc.status || e.Provider != "testprov" || e.RetryAfter != tc.retry {
				t.Errorf("classified %s (status %d, provider %q, retry after %v), want %s after %v", e.Class, e.Status, e.Provider, e.RetryAfter, tc.want, tc.retry)
			}
			if n := len(api.seen()); n != 1 {
				t.Errorf("server saw %d requests, want 1: the codec does not retry", n)
			}
		})
	}

	t.Run("the error says what the upstream said", func(t *testing.T) {
		t.Parallel()
		api := newFakeAPI(t, step{status: 400, file: "error_400.json"})
		_, err := Send(t.Context(), http.DefaultClient, freeze(t, testDialect(api.url), testRoute(), fullPlan(t), llm.Options{}))
		if err == nil || !strings.Contains(err.Error(), "text content blocks must be non-empty") || !strings.Contains(err.Error(), "400") {
			t.Errorf("error %q does not carry the status and body excerpt", err)
		}
	})

	t.Run("no connection", func(t *testing.T) {
		t.Parallel()
		gone := httptest.NewServer(http.NotFoundHandler())
		gone.Close()
		f := freeze(t, testDialect(gone.URL), testRoute(), fullPlan(t), llm.Options{})
		if _, err := Send(t.Context(), http.DefaultClient, f); llm.ClassOf(err) != llm.ErrTransport {
			t.Errorf("err = %v, want a transport error", err)
		}
		if _, err := Send(t.Context(), http.DefaultClient, nil); err == nil {
			t.Error("Send accepted a nil request")
		}
	})
}

func TestDeclaredTools(t *testing.T) {
	t.Parallel()
	o := fullOpts(t)
	o.tools = []core.ToolDef{pythonTool(), lookupTool()}
	got := declaredTools(body(t, buildPlan(t, o)))
	if len(got) != 2 || got["python"] != toolCode || got["lookup"] != toolArgs || got["shell"] != toolUndeclared {
		t.Errorf("declared tools = %v", got)
	}
	o.tools = nil
	if got := declaredTools(body(t, buildPlan(t, o))); len(got) != 0 {
		t.Errorf("declared tools = %v with none in the request", got)
	}
	if got := declaredTools([]byte(`not json`)); got != nil {
		t.Errorf("declared tools = %v from a body that is not JSON", got)
	}
}
