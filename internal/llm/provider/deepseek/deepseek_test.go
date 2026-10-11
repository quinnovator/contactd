package deepseek

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/conform"
	"github.com/quinnovator/contactd/internal/plan"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func newProvider(t *testing.T, cfg Config) *Provider {
	t.Helper()
	if cfg.APIKey == "" {
		cfg.APIKey = "test-key"
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func route(t *testing.T, p *Provider, model string) llm.Route {
	t.Helper()
	r, err := p.Route(t.Context(), model)
	if err != nil {
		t.Fatalf("Route(%q): %v", model, err)
	}
	return r
}

func freeze(t *testing.T, p *Provider, r llm.Route, pl *plan.PromptPlan, o llm.Options) *llm.Frozen {
	t.Helper()
	f, err := p.Prepare(t.Context(), r, pl, o)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return f
}

// The fixtures are written by hand in the documented Messages stream
// format; they are not recordings. Two things in them are assumptions,
// because DeepSeek's guide for this endpoint does not describe its
// responses: that usage carries the Anthropic field names, with tokens read
// from the cache in cache_read_input_tokens and counted apart from
// input_tokens, and that a thinking block carries a signature. The probe
// has to confirm both against the live endpoint.
func TestConformance(t *testing.T) {
	t.Parallel()
	fx := conform.Fixture{
		// Keep-alive comments, visible thinking, then the reply. 36 uncached
		// tokens and 1024 read from the cache.
		Reply: conform.Response{
			Body:   fixture(t, "reply.sse"),
			Text:   "Six times seven is 42.",
			Input:  1060,
			Cached: 1024,
			Output: 27,
		},
		// Thinking, a line of text, then the call. 52 uncached tokens and
		// 2048 read from the cache.
		Tool: conform.Response{
			Body:      fixture(t, "tool.sse"),
			Text:      "Let me check.",
			Code:      "print(6 * 7)",
			Input:     2100,
			Cached:    2048,
			Output:    64,
			Reasoning: true,
		},
	}
	for _, model := range []string{"", "deepseek-v4-pro"} {
		t.Run("model "+model, func(t *testing.T) {
			t.Parallel()
			conform.Run(t, func(t *testing.T, baseURL string) llm.Provider {
				t.Helper()
				return newProvider(t, Config{BaseURL: baseURL})
			}, model, fx)
		})
	}
}

// testPlan is a turn with a system prompt, one tool, a finished exchange
// and a new question.
func testPlan(t *testing.T) *plan.PromptPlan {
	t.Helper()
	event := func(seq uint64, kind core.EventKind, body any) core.Event {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode event body: %v", err)
		}
		return core.Event{Contact: 7, Seq: seq, TS: int64(1_760_000_000_000 + seq), Kind: kind, Body: raw}
	}
	in := plan.Input{
		Agent: &core.AgentConfig{
			System: "You are Ada.",
			Tools:  []core.ToolDef{{Name: "python", Description: "Run Python.", Env: core.EnvSpec{Base: "python-3.12"}}},
		},
		Profile: "Prefers short answers.",
		Tail: []core.Event{
			event(1, core.KindUserMsg, core.MsgBody{Text: "hello"}),
			event(2, core.KindAgentMsg, core.MsgBody{Text: "Hi."}),
			event(3, core.KindUserMsg, core.MsgBody{Text: "What is 6 times 7?"}),
		},
	}
	p, err := plan.Build(nil, in, nil, 1_760_000_100_000, plan.DefaultParams())
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	return p
}

func TestRequestOnTheWire(t *testing.T) {
	t.Parallel()
	p := newProvider(t, Config{})
	pl := testPlan(t)
	pl.AppendTail(
		core.ToolCall{ID: "call_1", Name: "python", Code: "1/0"},
		core.ToolResult{ID: "call_1", Output: "ZeroDivisionError", IsError: true},
	)
	f := freeze(t, p, route(t, p, ""), pl, llm.Options{Affinity: "aff1n1ty"})

	if f.URL != "https://api.deepseek.com/anthropic/v1/messages" {
		t.Errorf("URL = %q", f.URL)
	}
	wantHeader := map[string]string{
		"X-Api-Key":         "test-key",
		"Anthropic-Version": "2023-06-01",
		"Content-Type":      "application/json",
		"Accept":            "text/event-stream",
	}
	// Exactly these: no beta header, and nowhere for an affinity key to go.
	if len(f.Header) != len(wantHeader) {
		t.Errorf("headers = %v, want exactly %v", f.Header, wantHeader)
	}
	for k, v := range wantHeader {
		if got := f.Header.Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}

	const want = `{"model":"deepseek-flash","max_tokens":8192,"stream":true,` +
		`"tools":[{"name":"python","description":"Run Python.","input_schema":{"type":"object","properties":{"code":{"type":"string","description":"The code to run."}},"required":["code"],"additionalProperties":false}}],` +
		`"system":[{"type":"text","text":"You are Ada."},{"type":"text","text":"What you know about this contact:\nPrefers short answers."}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"Hi."}]},` +
		`{"role":"user","content":[{"type":"text","text":"What is 6 times 7?"},{"type":"text","text":"<now>2025-10-09T08:55Z (Thursday)</now>"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"python","input":{"code":"1/0"}}]},` +
		// The endpoint drops is_error, so the failure is in the text.
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"[tool error]\nZeroDivisionError"}]}]}`
	if string(f.Body) != want {
		t.Errorf("request body\n got: %s\nwant: %s", f.Body, want)
	}
	// What the endpoint ignores or does not document is not sent at all.
	for _, field := range []string{"cache_control", "fallbacks", "tool_choice", "disable_parallel_tool_use", "is_error", "eager_input_streaming", "aff1n1ty"} {
		if bytes.Contains(f.Body, []byte(field)) {
			t.Errorf("request carries %s: %s", field, f.Body)
		}
	}

	if f := freeze(t, p, route(t, p, "deepseek-v4-pro"), pl, llm.Options{MaxOutput: 500_000}); !bytes.HasPrefix(f.Body, []byte(`{"model":"deepseek-v4-pro","max_tokens":384000,`)) {
		t.Errorf("request does not name the route's model with the output cap held to its limit: %.80s", f.Body)
	}
}

func TestEffortOnTheWire(t *testing.T) {
	t.Parallel()
	p := newProvider(t, Config{})
	r := route(t, p, "")
	pl := testPlan(t)
	base := freeze(t, p, r, pl, llm.Options{}).Body
	for _, tc := range []struct {
		ask, want llm.Effort
		field     string
	}{
		{llm.EffortNone, llm.EffortNone, `"thinking":{"type":"disabled"},`},
		{llm.EffortLow, llm.EffortLow, `"output_config":{"effort":"low"},`},
		// The API has no medium and no xhigh. The nearest of its own three
		// is sent, so that the record says what ran. Nearest breaks the
		// xhigh tie upward; the API's own remap would have made it high.
		{llm.EffortMedium, llm.EffortHigh, `"output_config":{"effort":"high"},`},
		{llm.EffortHigh, llm.EffortHigh, `"output_config":{"effort":"high"},`},
		{llm.EffortXHigh, llm.EffortMax, `"output_config":{"effort":"max"},`},
		{llm.EffortMax, llm.EffortMax, `"output_config":{"effort":"max"},`},
	} {
		f := freeze(t, p, r, pl, llm.Options{Effort: tc.ask})
		if f.Effort != tc.want || !bytes.Contains(f.Body, []byte(`"stream":true,`+tc.field+`"tools":[`)) {
			t.Errorf("effort %q froze as %q, want %q with %s: %s", tc.ask, f.Effort, tc.want, tc.field, f.Body)
		}
		if without := bytes.Replace(f.Body, []byte(tc.field), nil, 1); !bytes.Equal(without, base) {
			t.Errorf("effort %q changed more than its own field: %s", tc.ask, f.Body)
		}
	}
}

func TestReasoningIsReplayedOnLaterTurns(t *testing.T) {
	t.Parallel()
	// With tools set the API answers 400 unless every earlier assistant
	// turn comes back with its reasoning. The log keeps the payload, and
	// the planner renders it for the route that wrote it.
	p := newProvider(t, Config{})
	r := route(t, p, "")
	event := func(seq uint64, kind core.EventKind, body any) core.Event {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode event body: %v", err)
		}
		return core.Event{Contact: 7, Seq: seq, TS: int64(1_760_000_000_000 + seq), Kind: kind, Body: raw}
	}
	payload := func(id, text, after string) *core.Reasoning {
		// A thinking block, then where the turn's text or call sat.
		return &core.Reasoning{Route: id, Data: []byte(`{"v":1,"turn":[{"block":{"type":"thinking","thinking":"` + text + `","signature":"sig"}},` + after + `]}`)}
	}
	in := plan.Input{
		Agent: &core.AgentConfig{System: "You are Ada.", Tools: []core.ToolDef{{Name: "python", Description: "Run Python."}}},
		Route: r.ID(),
		Tail: []core.Event{
			event(1, core.KindUserMsg, core.MsgBody{Text: "What is 6 times 7?"}),
			event(2, core.KindToolCall, core.ToolCall{ID: "call_1", Name: "python", Code: "print(6 * 7)", Reasoning: payload(r.ID(), "use the tool", `{"tool":true}`)}),
			event(3, core.KindToolResult, core.ToolResult{ID: "call_1", Output: "42\n"}),
			event(4, core.KindAgentMsg, core.MsgBody{Text: "It is 42.", Reasoning: payload(r.ID(), "report it", `{"text":9}`)}),
			event(5, core.KindUserMsg, core.MsgBody{Text: "And 7 times 8?"}),
			// An older turn served by the other model: not this route's to read.
			event(6, core.KindAgentMsg, core.MsgBody{Text: "56.", Reasoning: payload("deepseek/deepseek-v4-pro", "from another model", `{"text":3}`)}),
			event(7, core.KindUserMsg, core.MsgBody{Text: "Thanks"}),
		},
	}
	pl, err := plan.Build(nil, in, nil, 1_760_000_100_000, plan.DefaultParams())
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	body := string(freeze(t, p, r, pl, llm.Options{}).Body)
	for _, want := range []string{
		`{"role":"assistant","content":[{"type":"thinking","thinking":"use the tool","signature":"sig"},{"type":"tool_use","id":"call_1","name":"python","input":{"code":"print(6 * 7)"}}]}`,
		`{"role":"assistant","content":[{"type":"thinking","thinking":"report it","signature":"sig"},{"type":"text","text":"It is 42."}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"56."}]}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("request lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "from another model") {
		t.Errorf("another route's reasoning was sent: %s", body)
	}
}

func TestRoutes(t *testing.T) {
	t.Parallel()
	p := newProvider(t, Config{})
	if p.ID() != "deepseek" {
		t.Errorf("ID = %q", p.ID())
	}
	if r := route(t, p, ""); r.Model != "deepseek-flash" || r.ID() != "deepseek/deepseek-flash" {
		t.Errorf("default route = %+v", r)
	}
	for _, model := range []string{"deepseek-flash", "deepseek-v4-pro"} {
		r := route(t, p, model)
		c := r.Caps
		if r.Provider != "deepseek" || r.Model != model || r.Wire != llm.WireMessages {
			t.Errorf("%s: route = %+v", model, r)
		}
		if c.ContextWindow != 1_000_000 || c.MaxOutput != 384_000 {
			t.Errorf("%s: context %d, max output %d", model, c.ContextWindow, c.MaxOutput)
		}
		if c.Cache != llm.CacheAutoPrefix || c.CacheTTL != time.Hour || c.Affinity != llm.AffinityNone || !c.CacheUsage {
			t.Errorf("%s: cache capabilities = %+v", model, c)
		}
		if !slices.Equal(c.Efforts, []llm.Effort{llm.EffortLow, llm.EffortHigh, llm.EffortMax}) ||
			c.DefaultEffort != llm.EffortHigh || !c.ReasoningOff || c.Replay != llm.ReplayAllTurns {
			t.Errorf("%s: reasoning capabilities = %+v", model, c)
		}
		if !c.ToolChoice || c.SingleToolCall || c.StrictTools || c.ToolErrorFlag || c.SystemMidHistory {
			t.Errorf("%s: tool capabilities = %+v", model, c)
		}
		c.Efforts[0] = llm.EffortMax
		if again := route(t, p, model); again.Caps.Efforts[0] != llm.EffortLow {
			t.Errorf("%s: routes share their efforts slice", model)
		}
	}

	// The endpoint would serve each of these with a model of its own
	// choosing. The profile refuses them instead.
	for _, model := range []string{"deepseek-chat", "deepseek-v4.1-flash", "claude-opus-5-5", "DeepSeek-Flash"} {
		if _, err := p.Route(t.Context(), model); !errors.Is(err, llm.ErrUnknownModel) {
			t.Errorf("Route(%q) error = %v, want ErrUnknownModel", model, err)
		}
	}
	for _, r := range []llm.Route{
		{Provider: "deepseek", Model: "claude-opus-5-5", Wire: llm.WireMessages},
		{Provider: "anthropic", Model: "deepseek-flash", Wire: llm.WireMessages},
	} {
		if _, err := p.Prepare(t.Context(), r, testPlan(t), llm.Options{}); !errors.Is(err, llm.ErrUnknownModel) {
			t.Errorf("Prepare(%s) error = %v, want ErrUnknownModel", r.ID(), err)
		}
	}
	other := route(t, p, "")
	other.Wire = llm.WireChat
	if _, err := p.Prepare(t.Context(), other, testPlan(t), llm.Options{}); !errors.Is(err, llm.ErrUnsupportedWire) {
		t.Errorf("Prepare on another wire: error = %v, want ErrUnsupportedWire", err)
	}
}

func TestNewValidates(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]Config{
		"no key":       {},
		"bad base url": {APIKey: "k", BaseURL: "api.deepseek.com"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New accepted an invalid config", name)
		}
	}
	for _, wire := range []string{"responses", "Messages", "ollama"} {
		if _, err := New(Config{APIKey: "k", Wire: wire}); err == nil || !strings.Contains(err.Error(), `want "messages" or "chat"`) {
			t.Errorf("wire %q: error = %v, want one that names the two wires", wire, err)
		}
	}
	for _, wire := range []string{"", "messages"} {
		p := newProvider(t, Config{Wire: wire, BaseURL: "https://gateway.example.test/anthropic/"})
		r := route(t, p, "")
		if f := freeze(t, p, r, testPlan(t), llm.Options{}); r.Wire != llm.WireMessages || f.URL != "https://gateway.example.test/anthropic/v1/messages" {
			t.Errorf("wire %q: route wire %s, URL %q", wire, r.Wire, f.URL)
		}
	}
}
