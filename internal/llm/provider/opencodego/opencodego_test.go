package opencodego

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// testdata/models.json is the gateway's real catalog as fetched on
// Oct 11, 2026. The stream fixtures are written by hand in each wire's
// documented format: nothing about this gateway's responses is documented,
// so they stand for "what that wire normally returns".
func newProvider(t *testing.T, base string) *Provider {
	t.Helper()
	p, err := New(t.Context(), Config{APIKey: "test-key", BaseURL: base, Catalog: fixture(t, "models.json")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestConformance(t *testing.T) {
	t.Parallel()
	chatFx := conform.Fixture{
		Reply: conform.Response{Body: fixture(t, "chat_reply.sse"), Text: "Six times seven is 42.", Input: 2312, Cached: 2048, Output: 9},
		Tool: conform.Response{
			Body: fixture(t, "chat_tool.sse"), Text: "Let me check.", Code: "print(6 * 7)",
			Input: 1520, Cached: 1500, Output: 41, Reasoning: true,
		},
	}
	msgFx := conform.Fixture{
		Reply: conform.Response{Body: fixture(t, "messages_reply.sse"), Text: "Six times seven is 42.", Input: 1060, Cached: 1024, Output: 27},
		Tool: conform.Response{
			Body: fixture(t, "messages_tool.sse"), Text: "Let me check.", Code: "print(6 * 7)",
			Input: 2100, Cached: 2048, Output: 64, Reasoning: true,
		},
	}
	for model, fx := range map[string]conform.Fixture{
		"":                 chatFx, // the default: DeepSeek, which replays reasoning on every turn
		"deepseek-v4-pro":  chatFx,
		"kimi-k3":          chatFx, // Chat Completions, no replay
		"claude-haiku-5-5": msgFx,  // Messages, breakpoints, same-turn replay
		"qwen3.8-max":      msgFx,  // Messages, breakpoints, no replay
		"minimax-m3":       msgFx,  // Messages, no breakpoints
	} {
		t.Run("model="+model, func(t *testing.T) {
			t.Parallel()
			conform.Run(t, func(t *testing.T, base string) llm.Provider {
				t.Helper()
				return newProvider(t, base)
			}, model, fx)
		})
	}
}

func TestWirePerModel(t *testing.T) {
	t.Parallel()
	p := newProvider(t, "https://gateway.test/zen/go/v1")
	pl := plan.Prompt("be brief", "hello")
	for model, want := range map[string]struct {
		wire llm.Wire
		path string
	}{
		"deepseek-v4.1-flash": {llm.WireChat, "/zen/go/v1/chat/completions"},
		"glm-5.3":             {llm.WireChat, "/zen/go/v1/chat/completions"},
		"hy3":                 {llm.WireChat, "/zen/go/v1/chat/completions"},
		"longcat-2.0":         {llm.WireChat, "/zen/go/v1/chat/completions"},
		"mimo-v2.6-pro":       {llm.WireChat, "/zen/go/v1/chat/completions"},
		"minimax-m2.7":        {llm.WireMessages, "/zen/go/v1/messages"},
		"qwen3.7-plus":        {llm.WireMessages, "/zen/go/v1/messages"},
		"claude-haiku-5-5":    {llm.WireMessages, "/zen/go/v1/messages"},
	} {
		r, err := p.Route(t.Context(), model)
		if err != nil {
			t.Errorf("%s: %v", model, err)
			continue
		}
		f, err := p.Prepare(t.Context(), r, pl, llm.Options{Affinity: "abc123"})
		if err != nil {
			t.Errorf("%s: %v", model, err)
			continue
		}
		if r.Wire != want.wire || !strings.HasSuffix(f.URL, want.path) {
			t.Errorf("%s: wire %s at %s, want %s at %s", model, r.Wire, f.URL, want.wire, want.path)
		}
		// What OpenCode asks of every client.
		if f.Header.Get("User-Agent") != "contactd/1.0" || f.Header.Get(SessionHeader) != "abc123" {
			t.Errorf("%s: User-Agent %q, session %q", model, f.Header.Get("User-Agent"), f.Header.Get(SessionHeader))
		}
		if f.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("%s: Authorization = %q", model, f.Header.Get("Authorization"))
		}
		// A call that belongs to no conversation still carries a session:
		// the gateway refuses a request without one. It is the same for
		// every such call from this process.
		bare, err := p.Prepare(t.Context(), r, pl, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		again, err := p.Prepare(t.Context(), r, pl, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if s := bare.Header.Get(SessionHeader); !strings.HasPrefix(s, "contactd-") || s != again.Header.Get(SessionHeader) {
			t.Errorf("%s: session without an affinity key = %q then %q", model, s, again.Header.Get(SessionHeader))
		}
	}
}

func TestDeclaredCapabilities(t *testing.T) {
	t.Parallel()
	p := newProvider(t, "")
	caps := func(model string) llm.Capabilities {
		r, err := p.Route(t.Context(), model)
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		return r.Caps
	}
	for model, want := range map[string]struct {
		cache  llm.CacheMode
		replay llm.Replay
	}{
		"deepseek-v4-pro":  {llm.CacheAutoPrefix, llm.ReplayAllTurns},
		"kimi-k2.6":        {llm.CacheAutoPrefix, llm.ReplayNone},
		"minimax-m2.5":     {llm.CacheAutoPrefix, llm.ReplayNone},
		"minimax-m2.7":     {llm.CacheBreakpoints, llm.ReplayNone}, // the one MiniMax model with a cached-write price
		"qwen3.6-plus":     {llm.CacheBreakpoints, llm.ReplayNone},
		"claude-haiku-5-5": {llm.CacheBreakpoints, llm.ReplaySameTurn},
	} {
		c := caps(model)
		if c.Cache != want.cache || c.Replay != want.replay {
			t.Errorf("%s: cache %d, replay %d; want %d, %d", model, c.Cache, c.Replay, want.cache, want.replay)
		}
		// Cached tokens were seen reported on the two families probed.
		if c.CacheUsage != (strings.HasPrefix(model, "deepseek") || strings.HasPrefix(model, "qwen")) {
			t.Errorf("%s: CacheUsage = %t", model, c.CacheUsage)
		}
		// Undocumented, so declared conservatively until probed.
		if len(c.Efforts) != 0 || c.ReasoningOff || c.ToolChoice || c.SingleToolCall || c.ToolErrorFlag {
			t.Errorf("%s declares something the gateway does not document: %+v", model, c)
		}
		if c.Affinity != llm.AffinityHeader || c.CacheTTL <= 0 || c.ContextWindow <= 0 {
			t.Errorf("%s: %+v", model, c)
		}
	}
	if c := caps("deepseek-v4.1-flash"); c.ContextWindow != 1_000_000 || c.MaxOutput != 384_000 {
		t.Errorf("deepseek window/output = %d/%d", c.ContextWindow, c.MaxOutput)
	}
}

func TestModelsOutsideTheWireTable(t *testing.T) {
	t.Parallel()
	p := newProvider(t, "")
	// Responses-only models are known, and say why they cannot be used.
	for _, model := range []string{"gpt-6-luna", "grok-4.7", "muse-spark-1.3-contributor"} {
		if _, err := p.Route(t.Context(), model); !errors.Is(err, llm.ErrUnsupportedWire) {
			t.Errorf("%s: %v, want ErrUnsupportedWire", model, err)
		}
	}
	// A family the profile has no wire for is not guessed at.
	for _, model := range []string{"omen-alpha", "space-bunny", "deepseek-v9-not-in-catalog", "claude-opus-5-5"} {
		if _, err := p.Route(t.Context(), model); !errors.Is(err, llm.ErrUnknownModel) {
			t.Errorf("%s: %v, want ErrUnknownModel", model, err)
		}
	}
	responses, unknown := p.Unrouted()
	if len(responses) != 6 || len(unknown) != 3 {
		t.Errorf("unrouted: %d Responses-only %v, %d unknown %v", len(responses), responses, len(unknown), unknown)
	}
	if got := len(p.Models()); got != 38-6-3 {
		t.Errorf("%d models served, want the catalog's 38 less the 9 unrouted", got)
	}
}

func TestCatalogIsCheckedAtStartUp(t *testing.T) {
	t.Parallel()

	t.Run("a retired default model fails loudly", func(t *testing.T) {
		t.Parallel()
		_, err := New(t.Context(), Config{APIKey: "k", Catalog: []byte(`{"data":[{"id":"glm-5.3"}]}`)})
		if !errors.Is(err, llm.ErrUnknownModel) || !strings.Contains(err.Error(), DefaultModel) {
			t.Errorf("err = %v", err)
		}
		if _, err := New(t.Context(), Config{APIKey: "k", DefaultModel: "glm-5.3", Catalog: []byte(`{"data":[{"id":"glm-5.3"}]}`)}); err != nil {
			t.Errorf("a default that is in the catalog: %v", err)
		}
	})

	t.Run("fetched with the key when none is supplied", func(t *testing.T) {
		t.Parallel()
		var gotAuth, gotUA string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/models" {
				http.NotFound(w, r)
				return
			}
			gotAuth, gotUA = r.Header.Get("Authorization"), r.Header.Get("User-Agent")
			_, _ = w.Write(fixture(t, "models.json"))
		}))
		t.Cleanup(srv.Close)
		p, err := New(t.Context(), Config{APIKey: "live-key", BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		if gotAuth != "Bearer live-key" || gotUA != "contactd/1.0" || len(p.Models()) != 29 {
			t.Errorf("auth %q, user agent %q, %d models", gotAuth, gotUA, len(p.Models()))
		}
	})

	t.Run("a catalog that cannot be read is an error, not an empty profile", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
		}))
		t.Cleanup(srv.Close)
		if _, err := New(t.Context(), Config{APIKey: "k", BaseURL: srv.URL}); llm.ClassOf(err) != llm.ErrAuth {
			t.Errorf("err = %v, want an auth error", err)
		}
		for _, bad := range []string{`{"data":[]}`, `not json`} {
			if _, err := New(t.Context(), Config{APIKey: "k", Catalog: []byte(bad)}); err == nil {
				t.Errorf("catalog %q was accepted", bad)
			}
		}
		if _, err := New(t.Context(), Config{}); err == nil {
			t.Error("a profile with no key was built")
		}
	})
}

func TestASpentBudgetIsNotRetried(t *testing.T) {
	t.Parallel()
	for body, want := range map[string]llm.ErrorClass{
		`{"error":{"message":"You have reached your weekly usage limit. It resets on Monday."}}`: llm.ErrQuota,
		`{"error":{"message":"Monthly budget exhausted for this model."}}`:                       llm.ErrQuota,
		`{"error":{"message":"Too many requests, slow down."}}`:                                  llm.ErrRateLimited,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(body))
		}))
		p := newProvider(t, srv.URL)
		r, err := p.Route(t.Context(), "")
		if err != nil {
			t.Fatal(err)
		}
		f, err := p.Prepare(t.Context(), r, plan.Prompt("s", "hi"), llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Send(t.Context(), f)
		srv.Close()
		if got := llm.ClassOf(err); got != want || got.Retryable() != (want == llm.ErrRateLimited) {
			t.Errorf("%s\n  classified %s, want %s", body, got, want)
		}
	}
}
