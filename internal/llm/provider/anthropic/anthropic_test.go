package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

func TestConformance(t *testing.T) {
	t.Parallel()
	fx := conform.Fixture{
		// A reply that opens with a signed thinking block whose text is
		// hidden, as these models send by default. 14 uncached tokens, 210
		// written to the cache and 1800 read from it.
		Reply: conform.Response{
			Body:   fixture(t, "reply.sse"),
			Text:   "Six times seven is 42.",
			Input:  2024,
			Cached: 1800,
			Output: 11,
		},
		// Thinking, a line of text, then the call. 20 uncached tokens and
		// 1500 read from the cache.
		Tool: conform.Response{
			Body:      fixture(t, "tool.sse"),
			Text:      "Let me check.",
			Code:      "print(6 * 7)",
			Input:     1520,
			Cached:    1500,
			Output:    41,
			Reasoning: true,
		},
	}
	for _, model := range []string{"", "claude-haiku-5-5"} {
		t.Run("model "+model, func(t *testing.T) {
			t.Parallel()
			conform.Run(t, func(t *testing.T, baseURL string) llm.Provider {
				t.Helper()
				return newProvider(t, Config{BaseURL: baseURL})
			}, model, fx)
		})
	}
}

// fixedNow is the clock the golden plan is built with.
const fixedNow = int64(1_760_000_000_000)

// goldenPlan is the plan the golden request was recorded from: all five
// tiers populated, a tool exchange in the tail and a second one appended
// to the live tier mid-turn.
func goldenPlan(t *testing.T) *plan.PromptPlan {
	t.Helper()
	event := func(seq uint64, kind core.EventKind, body any) core.Event {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode event body: %v", err)
		}
		return core.Event{Contact: 42, Seq: seq, TS: fixedNow - 60_000 + int64(seq), Kind: kind, Body: raw}
	}
	in := plan.Input{
		Agent: &core.AgentConfig{
			System: "You are Ada, a careful assistant.",
			Tools: []core.ToolDef{{
				Name:        "python",
				Description: "Run Python 3.12 and return what it prints.",
				Env:         core.EnvSpec{Tier: 0, Base: "python-3.12"},
			}},
		},
		Profile: "Name: Sam. Prefers short answers.",
		Tail: []core.Event{
			event(1, core.KindUserMsg, core.MsgBody{Text: "What is 6 times 7?"}),
			event(2, core.KindToolCall, core.ToolCall{
				ID: "toolu_01", Name: "python", Code: "print(6 * 7)", Text: "Let me compute that.",
			}),
			event(3, core.KindToolResult, core.ToolResult{ID: "toolu_01", Output: "42\n"}),
			event(4, core.KindAgentMsg, core.MsgBody{Text: "It is 42."}),
			event(5, core.KindUserMsg, core.MsgBody{Text: "And <b>7 times 8</b>? Say \"café\" & show the code."}),
		},
	}
	recall := []plan.Recalled{{ID: 3, Text: "Sam asked about Postgres vacuum settings."}}
	p, err := plan.Build(nil, in, recall, fixedNow, plan.DefaultParams())
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	p.AppendTail(
		core.ToolCall{ID: "toolu_02", Name: "python", Code: "print(7 * 8)"},
		core.ToolResult{ID: "toolu_02", Output: "56\n"},
	)
	return p
}

// TestRequestGolden pins the bytes this profile puts on the wire. They are
// the cache key: a change here cools every warm cache, so the golden file
// changes only on purpose.
func TestRequestGolden(t *testing.T) {
	t.Parallel()
	p := newProvider(t, Config{})
	f := freeze(t, p, route(t, p, ""), goldenPlan(t), llm.Options{})
	want := fixture(t, "request_full.golden.json")
	if !bytes.Equal(f.Body, want) {
		t.Errorf("request body differs from the golden file\n got: %s\nwant: %s", f.Body, want)
	}
	if f.URL != "https://api.anthropic.com/v1/messages" {
		t.Errorf("URL = %q", f.URL)
	}
	wantHeader := map[string]string{
		"X-Api-Key":         "test-key",
		"Anthropic-Version": "2023-06-01",
		"Anthropic-Beta":    "server-side-fallback-2026-07-01",
		"Content-Type":      "application/json",
		"Accept":            "text/event-stream",
	}
	if len(f.Header) != len(wantHeader) {
		t.Errorf("headers = %v, want exactly %v", f.Header, wantHeader)
	}
	for k, v := range wantHeader {
		if got := f.Header.Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
}

func TestFallback(t *testing.T) {
	t.Parallel()
	const field = `"fallbacks"`
	pl := goldenPlan(t)

	t.Run("asked for on the models that take it", func(t *testing.T) {
		t.Parallel()
		p := newProvider(t, Config{})
		for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5-1"} {
			f := freeze(t, p, route(t, p, model), pl, llm.Options{})
			if !bytes.Contains(f.Body, []byte(`"stream":true,"fallbacks":"default",`)) || f.Header.Get("Anthropic-Beta") != fallbackBeta {
				t.Errorf("%s: no fallback asked for: beta %q, body %s", model, f.Header.Get("Anthropic-Beta"), f.Body)
			}
		}
	})

	t.Run("not sent to a model without one", func(t *testing.T) {
		t.Parallel()
		p := newProvider(t, Config{})
		f := freeze(t, p, route(t, p, "claude-haiku-5-5"), pl, llm.Options{MaxOutput: 1234})
		if bytes.Contains(f.Body, []byte(field)) || f.Header.Get("Anthropic-Beta") != "" {
			t.Errorf("fallback asked of a model with none: beta %q, body %s", f.Header.Get("Anthropic-Beta"), f.Body)
		}
		if !bytes.HasPrefix(f.Body, []byte(`{"model":"claude-haiku-5-5","max_tokens":1234,"stream":true,"tools":[`)) {
			t.Errorf("request does not name the route's model and the asked-for output cap: %s", f.Body)
		}
	})

	t.Run("switched off by NoFallback", func(t *testing.T) {
		t.Parallel()
		p := newProvider(t, Config{NoFallback: true})
		f := freeze(t, p, route(t, p, ""), pl, llm.Options{})
		if bytes.Contains(f.Body, []byte(field)) || f.Header.Get("Anthropic-Beta") != "" {
			t.Errorf("fallback asked for with NoFallback set: beta %q, body %s", f.Header.Get("Anthropic-Beta"), f.Body)
		}
		// Nothing else moves: the request is the golden one without the field.
		want := bytes.Replace(fixture(t, "request_full.golden.json"), []byte(`"fallbacks":"default",`), nil, 1)
		if !bytes.Equal(f.Body, want) {
			t.Errorf("NoFallback changed more than its own field:\n%s\n%s", f.Body, want)
		}
	})
}

func TestEffortOnTheWire(t *testing.T) {
	t.Parallel()
	p := newProvider(t, Config{})
	r := route(t, p, "")
	golden := fixture(t, "request_full.golden.json")
	for e, name := range map[llm.Effort]string{
		llm.EffortLow: "low", llm.EffortMedium: "medium", llm.EffortHigh: "high", llm.EffortXHigh: "xhigh", llm.EffortMax: "max",
	} {
		f := freeze(t, p, r, goldenPlan(t), llm.Options{Effort: e})
		field := `"output_config":{"effort":"` + name + `"},`
		// One field, after fallbacks and before the tools, and no thinking
		// field: thinking is adaptive when the request says nothing.
		if f.Effort != e || !bytes.Contains(f.Body, []byte(`"fallbacks":"default",`+field+`"tools":[`)) {
			t.Errorf("effort %q froze as %q: %s", e, f.Effort, f.Body)
		}
		if without := bytes.Replace(f.Body, []byte(field), nil, 1); !bytes.Equal(without, golden) {
			t.Errorf("effort %q changed more than its own field: %s", e, f.Body)
		}
	}
	// Reasoning cannot be switched off here; the weakest effort stands in.
	if f := freeze(t, p, r, goldenPlan(t), llm.Options{Effort: llm.EffortNone}); f.Effort != llm.EffortLow || bytes.Contains(f.Body, []byte(`"thinking"`)) {
		t.Errorf("effort none froze as %q: %s", f.Effort, f.Body)
	}
}

func TestRoutes(t *testing.T) {
	t.Parallel()
	p := newProvider(t, Config{})
	if p.ID() != "anthropic" {
		t.Errorf("ID = %q", p.ID())
	}
	if r := route(t, p, ""); r.Model != "claude-opus-5-5" || r.ID() != "anthropic/claude-opus-5-5" {
		t.Errorf("default route = %+v", r)
	}
	allEfforts := []llm.Effort{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax}
	for model, want := range map[string]struct {
		defaultEffort llm.Effort
		toolChoice    bool
	}{
		// A forced tool_choice is rejected by all but the smallest model.
		"claude-opus-5-5":   {llm.EffortMedium, false},
		"claude-sonnet-5-5": {llm.EffortHigh, false},
		"claude-fable-5-1":  {llm.EffortHigh, false},
		"claude-haiku-5-5":  {llm.EffortMedium, true},
	} {
		r := route(t, p, model)
		c := r.Caps
		if r.Provider != "anthropic" || r.Model != model || r.Wire != llm.WireMessages {
			t.Errorf("%s: route = %+v", model, r)
		}
		if c.ContextWindow != 1_000_000 || c.MaxOutput != 128_000 {
			t.Errorf("%s: context %d, max output %d", model, c.ContextWindow, c.MaxOutput)
		}
		if c.Cache != llm.CacheBreakpoints || c.CacheTTL != 5*time.Minute || c.Affinity != llm.AffinityNone || !c.CacheUsage {
			t.Errorf("%s: cache capabilities = %+v", model, c)
		}
		if !slices.Equal(c.Efforts, allEfforts) || c.DefaultEffort != want.defaultEffort || c.ReasoningOff || c.Replay != llm.ReplaySameTurn {
			t.Errorf("%s: reasoning capabilities = %+v", model, c)
		}
		// No strict tools: the codec sends no strict schemas to declare.
		if c.ToolChoice != want.toolChoice || !c.SingleToolCall || c.StrictTools || !c.ToolErrorFlag || c.SystemMidHistory {
			t.Errorf("%s: tool capabilities = %+v", model, c)
		}
		// A caller changing its copy must not reach the next route.
		c.Efforts[0] = llm.EffortMax
		if again := route(t, p, model); again.Caps.Efforts[0] != llm.EffortLow {
			t.Errorf("%s: routes share their efforts slice", model)
		}
	}

	for _, model := range []string{"claude-opus-5", "claude-opus-5-5-20260401", "deepseek-flash", "Claude-Opus-5-5"} {
		if _, err := p.Route(t.Context(), model); !errors.Is(err, llm.ErrUnknownModel) {
			t.Errorf("Route(%q) error = %v, want ErrUnknownModel", model, err)
		}
	}

	// A route this profile did not make is refused before anything is frozen.
	for _, r := range []llm.Route{
		{Provider: "anthropic", Model: "claude-opus-4-8", Wire: llm.WireMessages},
		{Provider: "deepseek", Model: "claude-opus-5-5", Wire: llm.WireMessages},
	} {
		if _, err := p.Prepare(t.Context(), r, goldenPlan(t), llm.Options{}); !errors.Is(err, llm.ErrUnknownModel) {
			t.Errorf("Prepare(%s) error = %v, want ErrUnknownModel", r.ID(), err)
		}
	}
}

func TestNewValidates(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]Config{
		"no key":       {},
		"bad base url": {APIKey: "k", BaseURL: "api.anthropic.com"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New accepted an invalid config", name)
		}
	}
	p := newProvider(t, Config{BaseURL: "https://gateway.example.test/"})
	if f := freeze(t, p, route(t, p, ""), goldenPlan(t), llm.Options{}); f.URL != "https://gateway.example.test/v1/messages" {
		t.Errorf("URL = %q", f.URL)
	}
}
