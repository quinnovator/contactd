package ollamacloud

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/conform"
	"github.com/quinnovator/contactd/internal/plan"
)

const flash = "deepseek-v4.1-flash"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

// table is what the live service reported for these models, injected so
// that most tests need no stand-in for /api/tags and /api/show.
func table() []Model {
	return []Model{
		{
			Name: flash, ContextWindow: 1_048_576,
			Efforts: []llm.Effort{llm.EffortLow, llm.EffortHigh, llm.EffortMax}, DefaultEffort: llm.EffortHigh, ReasoningOff: true,
		},
		{
			Name: "gpt-oss:120b", ContextWindow: 131_072,
			Efforts: []llm.Effort{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}, DefaultEffort: llm.EffortMedium,
		},
		{Name: "gemma4:31b", ContextWindow: 262_144, DefaultEffort: llm.EffortNone, ReasoningOff: true},
		{Name: "mistral-large-4", ContextWindow: 1_048_576},
	}
}

func provider(t *testing.T, cfg Config) *Provider {
	t.Helper()
	if cfg.Models == nil {
		cfg.Models = table()
	}
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
	// Ollama reports no cached tokens, so none are expected. The tool reply
	// carries reasoning; the route replays none, so the suite expects no
	// payload to be kept.
	fx := conform.Fixture{
		Reply: conform.Response{
			Body: fixture(t, "reply.sse"),
			Text: "Six times seven is 42.", Input: 2312, Cached: 0, Output: 9,
		},
		Tool: conform.Response{
			Body: fixture(t, "tool.sse"),
			Text: "Let me check.", Code: "print(6 * 7)", Input: 1520, Cached: 0, Output: 41,
			Reasoning: true,
		},
	}
	// One model per kind of thinking control: levels with an off switch,
	// levels only, a switch only, and none.
	for _, model := range []string{flash, "gpt-oss:120b", "gemma4:31b", "mistral-large-4", ""} {
		t.Run("model "+model, func(t *testing.T) {
			t.Parallel()
			conform.Run(t, func(t *testing.T, baseURL string) llm.Provider {
				t.Helper()
				return provider(t, Config{BaseURL: baseURL, DefaultModel: flash})
			}, model, fx)
		})
	}
}

func TestRoute(t *testing.T) {
	t.Parallel()
	p := provider(t, Config{DefaultModel: "gpt-oss:120b"})
	if p.ID() != "ollama" {
		t.Errorf("ID = %q", p.ID())
	}
	r, err := p.Route(t.Context(), flash)
	if err != nil {
		t.Fatal(err)
	}
	want := llm.Capabilities{
		ContextWindow: 1_048_576,
		Cache:         llm.CacheNone, Affinity: llm.AffinityNone,
		Efforts: []llm.Effort{llm.EffortLow, llm.EffortHigh, llm.EffortMax}, DefaultEffort: llm.EffortHigh, ReasoningOff: true,
		Replay: llm.ReplayNone,
	}
	if r.ID() != "ollama/"+flash || r.Wire != llm.WireChat || !reflect.DeepEqual(r.Caps, want) {
		t.Errorf("route = %+v\nwant caps %+v", r, want)
	}
	if def, err := p.Route(t.Context(), ""); err != nil || def.Model != "gpt-oss:120b" || def.Caps.ReasoningOff {
		t.Errorf("default route = %+v, %v", def, err)
	}
	// Names resolve exactly: the local app's ":cloud" names are not these.
	for _, name := range []string{"deepseek-v4.1-flash:cloud", "gpt-oss", "deepseek"} {
		if _, err := p.Route(t.Context(), name); !errors.Is(err, llm.ErrUnknownModel) {
			t.Errorf("Route(%q): err = %v, want ErrUnknownModel", name, err)
		}
	}
	if _, err := provider(t, Config{}).Route(t.Context(), ""); !errors.Is(err, llm.ErrUnknownModel) {
		t.Errorf("no default model: err = %v, want ErrUnknownModel", err)
	}
	// A model whose window is not known gets the conservative one.
	bare := provider(t, Config{Models: []Model{{Name: "new-model"}}})
	if r, err := bare.Route(t.Context(), "new-model"); err != nil || r.Caps.ContextWindow != DefaultContextWindow {
		t.Errorf("route = %+v, %v", r, err)
	}
}

func TestNewValidates(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]Config{
		"unnamed model":       {Models: []Model{{ContextWindow: 8192}}},
		"model listed twice":  {Models: []Model{{Name: "m"}, {Name: "m"}}},
		"negative window":     {Models: []Model{{Name: "m", ContextWindow: -1}}},
		"none as an effort":   {Models: []Model{{Name: "m", Efforts: []llm.Effort{llm.EffortNone, llm.EffortLow}}}},
		"default not served":  {Models: table(), DefaultModel: "gpt-oss"},
		"default is an alias": {Models: table(), DefaultModel: flash + ":cloud"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(t.Context(), cfg); err == nil {
				t.Error("New accepted an invalid config")
			}
		})
	}
}

// service stands in for /api/tags and /api/show with saved live responses.
type service struct {
	url string

	mu    sync.Mutex
	auth  []string
	shown []string
}

func newService(t *testing.T, shows map[string]string) *service {
	t.Helper()
	s := &service{}
	tags := fixture(t, "tags.json")
	bodies := map[string][]byte{}
	for model, file := range shows {
		bodies[model] = fixture(t, file)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tags":
			_, _ = w.Write(tags)
		case r.Method == http.MethodPost && r.URL.Path == "/api/show":
			var req struct {
				Model string `json:"model"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &req)
			s.mu.Lock()
			s.shown = append(s.shown, req.Model)
			s.mu.Unlock()
			body, ok := bodies[req.Model]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error": "model '` + req.Model + `' not found"}`))
				return
			}
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func TestNewLoadsTheModelTable(t *testing.T) {
	t.Parallel()
	svc := newService(t, map[string]string{
		flash:             "show_deepseek-v4.1-flash.json",
		"gpt-oss:120b":    "show_gpt-oss_120b.json",
		"gemma4:31b":      "show_gemma4_31b.json",
		"mistral-large-4": "show_mistral-large-4.json",
	})
	p, err := New(t.Context(), Config{APIKey: "test-key", BaseURL: svc.url + "/", DefaultModel: flash})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// What the service says is exactly the table the other tests inject.
	want := provider(t, Config{})
	if !reflect.DeepEqual(p.models, want.models) {
		t.Errorf("loaded table:\n got %+v\nwant %+v", p.models, want.models)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if want := []string{flash, "gpt-oss:120b", "gemma4:31b", "mistral-large-4"}; !reflect.DeepEqual(svc.shown, want) {
		t.Errorf("shown = %v, want each listed model once, in order: %v", svc.shown, want)
	}
	for _, a := range svc.auth {
		if a != "Bearer test-key" {
			t.Errorf("start-up request Authorization = %q", a)
		}
	}

	r, err := p.Route(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	f, err := p.Prepare(t.Context(), r, turn(t), llm.Options{})
	if err != nil || f.URL != svc.url+"/v1/chat/completions" {
		t.Errorf("URL = %q, %v", f.URL, err)
	}
}

func TestNewFailsWhenAModelCannotBeShown(t *testing.T) {
	t.Parallel()
	// Three of the four listed models answer; start-up must not guess the
	// fourth's thinking levels.
	svc := newService(t, map[string]string{
		flash:          "show_deepseek-v4.1-flash.json",
		"gpt-oss:120b": "show_gpt-oss_120b.json",
		"gemma4:31b":   "show_gemma4_31b.json",
	})
	_, err := New(t.Context(), Config{BaseURL: svc.url})
	if err == nil || !strings.Contains(err.Error(), "mistral-large-4") || !strings.Contains(err.Error(), "404") {
		t.Errorf("New: err = %v, want it to name the model and the status", err)
	}

	if _, err := New(t.Context(), Config{BaseURL: "http://127.0.0.1:1"}); err == nil {
		t.Error("New succeeded with the service unreachable")
	}
}

func TestThinkingValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		show string
		want Model
	}{
		{
			"levels with an off switch",
			`{"model_info":{"a.context_length":1048576},"thinking":{"values":[false,"low","high","max"],"default":"high"}}`,
			Model{ContextWindow: 1_048_576, Efforts: []llm.Effort{llm.EffortLow, llm.EffortHigh, llm.EffortMax}, DefaultEffort: llm.EffortHigh, ReasoningOff: true},
		},
		{
			"levels in any order, and a name the scale lacks",
			`{"thinking":{"values":["ultra","max","low","xhigh"],"default":"ultra"}}`,
			Model{Efforts: []llm.Effort{llm.EffortLow, llm.EffortXHigh, llm.EffortMax}},
		},
		{
			"a switch only, on by default",
			`{"thinking":{"values":[false,true],"default":true}}`,
			Model{ReasoningOff: true},
		},
		{
			"a switch only, off by default",
			`{"thinking":{"values":[false,true],"default":false}}`,
			Model{DefaultEffort: llm.EffortNone, ReasoningOff: true},
		},
		{
			"false alone means the model does not think",
			`{"thinking":{"values":[false]}}`,
			Model{},
		},
		{"no thinking metadata", `{"model_info":{"general.architecture":"m"}}`, Model{}},
		{"null thinking", `{"thinking":null}`, Model{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s show
			if err := json.Unmarshal([]byte(tc.show), &s); err != nil {
				t.Fatal(err)
			}
			tc.want.Name = "m"
			if got := s.model("m"); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("model = %+v\nwant    %+v", got, tc.want)
			}
		})
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

func TestRequestShape(t *testing.T) {
	t.Parallel()
	p := provider(t, Config{})
	freeze := func(t *testing.T, model string, o llm.Options) *llm.Frozen {
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

	f := freeze(t, flash, llm.Options{Effort: llm.EffortMedium, MaxOutput: 2048, Affinity: "abc123"})
	if f.URL != "https://ollama.com/v1/chat/completions" || f.Header.Get("Authorization") != "Bearer test-key" {
		t.Errorf("URL %q, headers %v", f.URL, f.Header)
	}
	// The model takes low, high and max: medium is sent as high.
	const tail = `"max_tokens":2048,"stream":true,"stream_options":{"include_usage":true},"reasoning_effort":"high"}`
	if f.Effort != llm.EffortHigh || !bytes.HasSuffix(f.Body, []byte(tail)) {
		t.Errorf("effort %q, body = %s\nwant it to end %s", f.Effort, f.Body, tail)
	}
	// Nothing the endpoint does not support, and no pinning key anywhere.
	for _, absent := range []string{"tool_choice", "parallel_tool_calls", "cache_control", "abc123"} {
		if bytes.Contains(f.Body, []byte(absent)) {
			t.Errorf("body mentions %s: %s", absent, f.Body)
		}
	}
	for _, vs := range f.Header {
		if strings.Contains(strings.Join(vs, " "), "abc123") {
			t.Errorf("the pinning key was sent in a header: %v", f.Header)
		}
	}

	// Off is spelled "none", and only where the model lists false.
	if f := freeze(t, flash, llm.Options{Effort: llm.EffortNone}); !bytes.HasSuffix(f.Body, []byte(`,"reasoning_effort":"none"}`)) {
		t.Errorf("thinking off: body = %s", f.Body)
	}
	if f := freeze(t, "gpt-oss:120b", llm.Options{Effort: llm.EffortNone}); f.Effort != llm.EffortLow ||
		!bytes.HasSuffix(f.Body, []byte(`,"reasoning_effort":"low"}`)) {
		t.Errorf("a model with no off switch: effort %q, body = %s", f.Effort, f.Body)
	}
	// A switch-only model can be turned off and nothing else.
	if f := freeze(t, "gemma4:31b", llm.Options{Effort: llm.EffortNone}); !bytes.HasSuffix(f.Body, []byte(`,"reasoning_effort":"none"}`)) {
		t.Errorf("switch-only model, off: body = %s", f.Body)
	}
	if f := freeze(t, "gemma4:31b", llm.Options{Effort: llm.EffortHigh}); f.Effort != llm.EffortDefault || bytes.Contains(f.Body, []byte("reasoning")) {
		t.Errorf("switch-only model, high: effort %q, body = %s", f.Effort, f.Body)
	}
	// No thinking metadata, no effort sent.
	if f := freeze(t, "mistral-large-4", llm.Options{Effort: llm.EffortHigh}); bytes.Contains(f.Body, []byte("reasoning")) {
		t.Errorf("model with no thinking metadata: body = %s", f.Body)
	}
}
