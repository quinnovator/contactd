package openrouter

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/conform"
	"github.com/quinnovator/contactd/internal/plan"
)

const (
	flash = "deepseek/deepseek-v4.1-flash"
	pro   = "deepseek/deepseek-v4-pro"
	haiku = "anthropic/claude-haiku-5.5"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

// provider builds a Provider from the saved catalog, a trimmed copy of the
// live one.
func provider(t *testing.T, cfg Config) *Provider {
	t.Helper()
	cfg.Catalog = fixture(t, "models.json")
	if cfg.APIKey == "" {
		cfg.APIKey = "test-key"
	}
	p, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestConformance(t *testing.T) {
	t.Parallel()
	fx := conform.Fixture{
		Reply: conform.Response{
			Body: fixture(t, "reply.sse"),
			Text: "Six times seven is 42.", Input: 2312, Cached: 2048, Output: 9,
		},
		Tool: conform.Response{
			Body: fixture(t, "tool.sse"),
			Text: "Let me check.", Code: "print(6 * 7)", Input: 1520, Cached: 1500, Output: 41,
			Reasoning: true,
		},
	}
	// One model per cache mode: the DeepSeek upstream caches by prefix, the
	// Anthropic one takes breakpoints. The third is pinned.
	for name, tc := range map[string]struct {
		model string
		pin   map[string][]string
	}{
		"automatic prefix cache": {model: flash},
		"cache breakpoints":      {model: haiku},
		"pinned upstream":        {model: pro, pin: map[string][]string{pro: {"deepseek"}}},
		"default model":          {model: ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			conform.Run(t, func(t *testing.T, baseURL string) llm.Provider {
				t.Helper()
				return provider(t, Config{BaseURL: baseURL, DefaultModel: flash, Pin: tc.pin})
			}, tc.model, fx)
		})
	}
}

func TestCapabilitiesFromCatalog(t *testing.T) {
	t.Parallel()
	p := provider(t, Config{})
	if p.ID() != "openrouter" {
		t.Errorf("ID = %q", p.ID())
	}
	base := llm.Capabilities{CacheTTL: 10 * time.Minute, Affinity: llm.AffinityBody, CacheUsage: true}
	with := func(edit func(*llm.Capabilities)) llm.Capabilities {
		c := base
		edit(&c)
		return c
	}
	low, medium, high, xhigh, maxEffort := llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax
	cases := map[string]llm.Capabilities{
		// A read price and no write price: the upstream caches on its own.
		flash: with(func(c *llm.Capabilities) {
			c.ContextWindow, c.MaxOutput, c.Cache = 1_048_576, 943_718, llm.CacheAutoPrefix
			c.Efforts, c.DefaultEffort, c.ReasoningOff = []llm.Effort{low, high, maxEffort}, high, true
			c.Replay, c.ToolChoice, c.StrictTools = llm.ReplayAllTurns, true, true
		}),
		pro: with(func(c *llm.Capabilities) {
			c.ContextWindow, c.MaxOutput, c.Cache = 1_048_576, 384_000, llm.CacheAutoPrefix
			c.Efforts, c.DefaultEffort, c.ReasoningOff = []llm.Effort{high, xhigh}, high, true
			c.Replay, c.ToolChoice, c.StrictTools = llm.ReplayAllTurns, true, true
		}),
		// A write price: the request marks what is cached.
		haiku: with(func(c *llm.Capabilities) {
			c.ContextWindow, c.MaxOutput, c.Cache = 1_000_000, 128_000, llm.CacheBreakpoints
			c.Efforts, c.DefaultEffort, c.ReasoningOff = []llm.Effort{low, medium, high, xhigh, maxEffort}, medium, true
			c.Replay, c.ToolChoice, c.StrictTools = llm.ReplayAllTurns, true, true
		}),
		// parallel_tool_calls is listed, and reasoning is mandatory.
		"z-ai/glm-5.3-flash": with(func(c *llm.Capabilities) {
			c.ContextWindow, c.MaxOutput, c.Cache = 1_048_576, 943_717, llm.CacheAutoPrefix
			c.Efforts, c.DefaultEffort, c.ReasoningOff = []llm.Effort{low, high, maxEffort}, maxEffort, false
			c.Replay, c.ToolChoice, c.StrictTools, c.SingleToolCall = llm.ReplayAllTurns, true, true, true
		}),
		// "none" in the list is ReasoningOff, never an effort.
		"openai/gpt-6-luna": with(func(c *llm.Capabilities) {
			c.ContextWindow, c.MaxOutput, c.Cache = 1_050_000, 128_000, llm.CacheBreakpoints
			c.Efforts, c.DefaultEffort, c.ReasoningOff = []llm.Effort{low, medium, high, xhigh, maxEffort}, medium, true
			c.Replay, c.ToolChoice, c.StrictTools = llm.ReplayAllTurns, true, true
		}),
		// "minimal" has no place on the neutral scale.
		"qwen/qwen3.8-max-0902": with(func(c *llm.Capabilities) {
			c.ContextWindow, c.MaxOutput, c.Cache = 1_000_000, 131_072, llm.CacheAutoPrefix
			c.Efforts, c.DefaultEffort, c.ReasoningOff = []llm.Effort{low, medium, high, xhigh}, xhigh, false
			c.Replay, c.ToolChoice, c.StrictTools = llm.ReplayAllTurns, true, true
		}),
		// Reasoning with no effort list: it can be switched off, not tuned.
		"xiaomi/mimo-v2.6-flash": with(func(c *llm.Capabilities) {
			c.ContextWindow, c.MaxOutput, c.Cache = 1_050_000, 131_072, llm.CacheAutoPrefix
			c.ReasoningOff = true
			c.Replay, c.ToolChoice, c.StrictTools = llm.ReplayAllTurns, true, true
		}),
		// No reasoning at all.
		"unbiased/pareto": with(func(c *llm.Capabilities) {
			c.ContextWindow, c.MaxOutput, c.Cache = 262_144, 131_072, llm.CacheAutoPrefix
			c.ToolChoice = true
		}),
		// A router: reasoning is listed with no details, and nothing is
		// priced or capped.
		"openrouter/auto": with(func(c *llm.Capabilities) {
			c.ContextWindow = 2_000_000
			c.Replay, c.ToolChoice, c.StrictTools = llm.ReplayAllTurns, true, true
		}),
	}
	for name, want := range cases {
		r, err := p.Route(t.Context(), name)
		if err != nil {
			t.Errorf("Route(%q): %v", name, err)
			continue
		}
		if r.ID() != "openrouter/"+name || r.Wire != llm.WireChat {
			t.Errorf("route = %+v", r)
		}
		if !reflect.DeepEqual(r.Caps, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", name, r.Caps, want)
		}
	}
	// A write price with no read price is still a breakpoint cache.
	if r, err := p.Route(t.Context(), "qwen/qwen3.6-flash"); err != nil || r.Caps.Cache != llm.CacheBreakpoints {
		t.Errorf("qwen/qwen3.6-flash: cache mode %d, %v", r.Caps.Cache, err)
	}
}

func TestRoute(t *testing.T) {
	t.Parallel()
	p := provider(t, Config{DefaultModel: pro})
	if r, err := p.Route(t.Context(), ""); err != nil || r.Model != pro {
		t.Errorf("default route = %+v, %v", r, err)
	}
	// A model that cannot call tools is left out, and says why.
	_, err := p.Route(t.Context(), "inference-net/schematron-v2-small")
	if !errors.Is(err, llm.ErrUnknownModel) || !strings.Contains(err.Error(), "does not support tools") {
		t.Errorf("toolless model: err = %v", err)
	}
	// Names resolve exactly: no family guess, no prefix match.
	for _, name := range []string{"deepseek-v4.1-flash", "deepseek/deepseek-v4.1", "~deepseek/deepseek-flash-latest"} {
		if _, err := p.Route(t.Context(), name); !errors.Is(err, llm.ErrUnknownModel) {
			t.Errorf("Route(%q): err = %v, want ErrUnknownModel", name, err)
		}
	}
	if _, err := provider(t, Config{}).Route(t.Context(), ""); !errors.Is(err, llm.ErrUnknownModel) {
		t.Errorf("no default model: err = %v, want ErrUnknownModel", err)
	}
	// A route this provider did not issue is not frozen.
	stray := llm.Route{Provider: ID, Model: "made/up", Wire: llm.WireChat}
	if _, err := p.Prepare(t.Context(), stray, turn(t), llm.Options{}); !errors.Is(err, llm.ErrUnknownModel) {
		t.Errorf("Prepare for an unknown model: err = %v", err)
	}
}

func TestNewValidates(t *testing.T) {
	t.Parallel()
	saved := fixture(t, "models.json")
	for name, cfg := range map[string]Config{
		"default not in catalog":     {Catalog: saved, DefaultModel: "made/up"},
		"default cannot call tools":  {Catalog: saved, DefaultModel: "inference-net/schematron-v2-small"},
		"pin for an unknown model":   {Catalog: saved, Pin: map[string][]string{"made/up": {"x"}}},
		"catalog is not JSON":        {Catalog: []byte("<html>")},
		"catalog has no tool models": {Catalog: []byte(`{"data":[{"id":"a/b","supported_parameters":["max_tokens"]}]}`)},
		"catalog is one page of many": {
			Catalog: []byte(`{"data":[{"id":"a/b","context_length":8192,"supported_parameters":["tools"]}],"total_count":2}`),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(t.Context(), cfg); err == nil {
				t.Error("New accepted an invalid config")
			}
		})
	}
}

func TestNewFetchesTheCatalog(t *testing.T) {
	t.Parallel()
	saved := fixture(t, "models.json")
	var auth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/models" {
			http.NotFound(w, r)
			return
		}
		auth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(saved)
	}))
	t.Cleanup(srv.Close)

	p, err := New(t.Context(), Config{APIKey: "test-key", BaseURL: srv.URL + "/api/v1/", DefaultModel: flash})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, _ := auth.Load().(string); got != "Bearer test-key" {
		t.Errorf("catalog request Authorization = %q", got)
	}
	r, err := p.Route(t.Context(), "")
	if err != nil || r.Caps.ContextWindow != 1_048_576 {
		t.Fatalf("route = %+v, %v", r, err)
	}
	f, err := p.Prepare(t.Context(), r, turn(t), llm.Options{})
	if err != nil || f.URL != srv.URL+"/api/v1/chat/completions" {
		t.Errorf("URL = %q, %v", f.URL, err)
	}

	// A catalog that cannot be read stops start-up; it is never guessed at.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"down for maintenance"}}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)
	if _, err := New(t.Context(), Config{BaseURL: down.URL}); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("New with the catalog down: err = %v", err)
	}
}

// turn is a small plan with one tool.
func turn(t *testing.T) *plan.PromptPlan {
	t.Helper()
	body, err := json.Marshal(core.MsgBody{Text: "What is 6 times 7?"})
	if err != nil {
		t.Fatal(err)
	}
	in := plan.Input{
		Agent: &core.AgentConfig{
			System: "You are Ada.",
			Tools:  []core.ToolDef{{Name: "python", Description: "Run Python.", Env: core.EnvSpec{Base: "python-3.12"}}},
		},
		Tail: []core.Event{{Contact: 7, Seq: 1, TS: 1_760_000_000_000, Kind: core.KindUserMsg, Body: body}},
	}
	p, err := plan.Build(nil, in, nil, 1_760_000_100_000, plan.DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func freeze(t *testing.T, p *Provider, model string, o llm.Options) *llm.Frozen {
	t.Helper()
	r, err := p.Route(t.Context(), model)
	if err != nil {
		t.Fatalf("Route(%q): %v", model, err)
	}
	f, err := p.Prepare(t.Context(), r, turn(t), o)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return f
}

func TestRequestShape(t *testing.T) {
	t.Parallel()
	p := provider(t, Config{
		Referer: "https://contactd.example",
		Title:   "contactd",
		Pin:     map[string][]string{flash: {"deepseek", "novita"}},
	})

	t.Run("pinned, with effort and a session", func(t *testing.T) {
		t.Parallel()
		f := freeze(t, p, flash, llm.Options{Affinity: "abc123", Effort: llm.EffortMedium, MaxOutput: 4096})
		if f.URL != "https://openrouter.ai/api/v1/chat/completions" || f.Effort != llm.EffortHigh {
			t.Errorf("URL %q, effort %q", f.URL, f.Effort)
		}
		for k, want := range map[string]string{
			"Authorization":      "Bearer test-key",
			"HTTP-Referer":       "https://contactd.example",
			"X-OpenRouter-Title": "contactd",
		} {
			if got := f.Header.Get(k); got != want {
				t.Errorf("header %s = %q, want %q", k, got, want)
			}
		}
		// The model accepts low, high and max: medium is sent as high. It
		// lists neither parallel_tool_calls nor a cache-write price.
		const tail = `"max_tokens":4096,"stream":true,"stream_options":{"include_usage":true},"reasoning":{"effort":"high"},` +
			`"session_id":"abc123","provider":{"only":["deepseek","novita"],"allow_fallbacks":false,"require_parameters":true}}`
		if !bytes.HasSuffix(f.Body, []byte(tail)) {
			t.Errorf("body = %s\nwant it to end %s", f.Body, tail)
		}
		for _, absent := range []string{"parallel_tool_calls", "cache_control", "tool_choice"} {
			if bytes.Contains(f.Body, []byte(absent)) {
				t.Errorf("body mentions %s: %s", absent, f.Body)
			}
		}
	})

	t.Run("unpinned, reasoning off, the default cap", func(t *testing.T) {
		t.Parallel()
		// The catalog says this model can write 384,000 tokens. That is its
		// ceiling; a call that names no cap asks for the profile's default.
		f := freeze(t, p, pro, llm.Options{Effort: llm.EffortNone})
		const tail = `"max_tokens":8192,"stream":true,"stream_options":{"include_usage":true},"reasoning":{"effort":"none"},` +
			`"provider":{"require_parameters":true}}`
		if f.Effort != llm.EffortNone || !bytes.HasSuffix(f.Body, []byte(tail)) {
			t.Errorf("effort %q, body = %s\nwant it to end %s", f.Effort, f.Body, tail)
		}
	})

	t.Run("mandatory reasoning is never switched off", func(t *testing.T) {
		t.Parallel()
		// Asked for none, the weakest accepted effort is sent instead. The
		// model lists parallel_tool_calls, so it is held to one call.
		f := freeze(t, p, "z-ai/glm-5.3-flash", llm.Options{Effort: llm.EffortNone})
		if f.Effort != llm.EffortLow || !bytes.Contains(f.Body, []byte(`"reasoning":{"effort":"low"}`)) ||
			!bytes.Contains(f.Body, []byte(`"parallel_tool_calls":false`)) {
			t.Errorf("effort %q, body = %s", f.Effort, f.Body)
		}
	})

	t.Run("a cache-write price puts breakpoints on content parts", func(t *testing.T) {
		t.Parallel()
		f := freeze(t, p, haiku, llm.Options{})
		const system = `{"role":"system","content":[{"type":"text","text":"You are Ada.","cache_control":{"type":"ephemeral"}}]}`
		if !bytes.Contains(f.Body, []byte(system)) {
			t.Errorf("body = %s\nwant the system message as %s", f.Body, system)
		}
	})

	t.Run("the output cap takes the name the model lists", func(t *testing.T) {
		t.Parallel()
		if f := freeze(t, p, "openai/gpt-5.1-codex-mini", llm.Options{}); !bytes.Contains(f.Body, []byte(`"max_completion_tokens":8192`)) {
			t.Errorf("body = %s", f.Body)
		}
		// A cap above the model's ceiling is held to the ceiling.
		if f := freeze(t, p, "openai/gpt-5.1-codex-mini", llm.Options{MaxOutput: 500_000}); !bytes.Contains(f.Body, []byte(`"max_completion_tokens":128000`)) {
			t.Errorf("body = %s", f.Body)
		}
		// A router has no ceiling of its own, and takes the default.
		if f := freeze(t, p, "openrouter/auto", llm.Options{}); !bytes.Contains(f.Body, []byte(`"max_tokens":8192`)) {
			t.Errorf("body = %s", f.Body)
		}
	})
}
