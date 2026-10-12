package messages

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/plan"
)

// fixedNow is the clock every test plan is built with.
const fixedNow = int64(1_760_000_000_000)

// TestMain registers -update here rather than in a package variable, which
// the linter forbids. Run `go test ./internal/llm/wire/messages -update` to
// rewrite the golden files.
func TestMain(m *testing.M) {
	flag.Bool("update", false, "rewrite golden files")
	m.Run()
}

func updating() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if updating() {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("request body differs from %s\n got: %s\nwant: %s", path, got, want)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

// testDialect is an upstream with every dialect knob in use: an extra body
// field, static headers, eager tool input and a default output cap. With
// testRoute it freezes what the Anthropic profile freezes, which is what
// the golden file holds.
func testDialect(baseURL string) *Dialect {
	header := http.Header{}
	header.Set("X-Api-Key", "test-key")
	header.Set("Anthropic-Version", "2023-06-01")
	header.Set("Anthropic-Beta", "server-side-fallback-2026-07-01")
	return &Dialect{
		URL:              baseURL + "/v1/messages",
		Header:           header,
		Extra:            []Field{{Name: "fallbacks", Value: json.RawMessage(`"default"`)}},
		EagerToolInput:   true,
		DefaultMaxOutput: 64000,
	}
}

// testRoute is a route that caches at breakpoints, flags tool errors, holds
// the model to one call and replays reasoning within a turn.
func testRoute() llm.Route {
	return llm.Route{
		Provider: "testprov",
		Model:    "claude-opus-5-5",
		Wire:     llm.WireMessages,
		Caps: llm.Capabilities{
			ContextWindow:  1_000_000,
			MaxOutput:      128_000,
			Cache:          llm.CacheBreakpoints,
			CacheTTL:       5 * time.Minute,
			CacheUsage:     true,
			Efforts:        []llm.Effort{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax},
			DefaultEffort:  llm.EffortHigh,
			Replay:         llm.ReplaySameTurn,
			ToolChoice:     true,
			SingleToolCall: true,
			StrictTools:    true,
			ToolErrorFlag:  true,
		},
	}
}

// autoRoute is a route of the other kind: automatic prefix caching, no
// error flag, several tool calls a step, reasoning that can be switched off
// and is replayed on every turn.
func autoRoute() llm.Route {
	return llm.Route{
		Provider: "autoprov",
		Model:    "auto-model",
		Wire:     llm.WireMessages,
		Caps: llm.Capabilities{
			ContextWindow: 1_000_000,
			MaxOutput:     384_000,
			Cache:         llm.CacheAutoPrefix,
			CacheTTL:      time.Hour,
			CacheUsage:    true,
			Efforts:       []llm.Effort{llm.EffortLow, llm.EffortHigh, llm.EffortMax},
			DefaultEffort: llm.EffortHigh,
			ReasoningOff:  true,
			Replay:        llm.ReplayAllTurns,
			ToolChoice:    true,
		},
	}
}

func event(t *testing.T, seq uint64, kind core.EventKind, body any) core.Event {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode event body: %v", err)
	}
	return core.Event{Contact: 42, Seq: seq, TS: fixedNow - 60_000 + int64(seq), Kind: kind, Body: raw}
}

func pythonTool() core.ToolDef {
	return core.ToolDef{
		Name:        "python",
		Description: "Run Python 3.12 and return what it prints.",
		Env:         core.EnvSpec{Tier: 0, Base: "python-3.12"},
	}
}

// lookupTool declares its own input schema, so its calls carry raw
// arguments instead of code.
func lookupTool() core.ToolDef {
	return core.ToolDef{
		Name:        "lookup",
		Description: "Look a word up.",
		Input:       json.RawMessage(`{"type": "object", "properties": {"word": {"type": "string"}, "limit": {"type": "integer"}}, "required": ["word"]}`),
	}
}

// planOpts picks which tiers a test plan has.
type planOpts struct {
	system  string
	tools   []core.ToolDef
	profile string
	recall  []plan.Recalled
	events  []core.Event
	route   string
}

func buildPlan(t *testing.T, o planOpts) *plan.PromptPlan {
	t.Helper()
	agent := &core.AgentConfig{System: o.system, Tools: o.tools}
	in := plan.Input{Agent: agent, Profile: o.profile, Tail: o.events, Reader: plan.Reader{Route: o.route}}
	p, err := plan.Build(nil, in, o.recall, fixedNow, plan.DefaultParams())
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	return p
}

// history is one finished turn with a tool exchange, then a new question.
func history(t *testing.T) []core.Event {
	t.Helper()
	return []core.Event{
		event(t, 1, core.KindUserMsg, core.MsgBody{Text: "What is 6 times 7?"}),
		event(t, 2, core.KindToolCall, core.ToolCall{
			ID: "toolu_01", Name: "python", Code: "print(6 * 7)", Text: "Let me compute that.",
		}),
		event(t, 3, core.KindToolResult, core.ToolResult{ID: "toolu_01", Output: "42\n"}),
		event(t, 4, core.KindAgentMsg, core.MsgBody{Text: "It is 42."}),
		event(t, 5, core.KindUserMsg, core.MsgBody{Text: "And <b>7 times 8</b>? Say \"café\" & show the code."}),
	}
}

func fullOpts(t *testing.T) planOpts {
	t.Helper()
	return planOpts{
		system:  "You are Ada, a careful assistant.",
		tools:   []core.ToolDef{pythonTool()},
		profile: "Name: Sam. Prefers short answers.",
		recall:  []plan.Recalled{{ID: 3, Text: "Sam asked about Postgres vacuum settings."}},
		events:  history(t),
	}
}

// fullPlan has all five tiers populated, a tool exchange in the tail and a
// second one appended to the live tier mid-turn.
func fullPlan(t *testing.T) *plan.PromptPlan {
	t.Helper()
	p := buildPlan(t, fullOpts(t))
	p.AppendTail(
		core.ToolCall{ID: "toolu_02", Name: "python", Code: "print(7 * 8)"},
		core.ToolResult{ID: "toolu_02", Output: "56\n"},
	)
	return p
}

func freeze(t *testing.T, d *Dialect, r llm.Route, p *plan.PromptPlan, o llm.Options) *llm.Frozen {
	t.Helper()
	f, err := Prepare(d, r, p, o)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return f
}

// body freezes p for the test dialect and route and returns the bytes.
func body(t *testing.T, p *plan.PromptPlan) []byte {
	t.Helper()
	return freeze(t, testDialect(""), testRoute(), p, llm.Options{}).Body
}

// bodyFor freezes p for a route with no extra dialect settings.
func bodyFor(t *testing.T, r llm.Route, p *plan.PromptPlan) []byte {
	t.Helper()
	return freeze(t, &Dialect{}, r, p, llm.Options{}).Body
}

// wireRequest is a request body as tests read it back.
type wireRequest struct {
	Model        string          `json:"model"`
	MaxTokens    int             `json:"max_tokens"`
	Stream       bool            `json:"stream"`
	Fallbacks    string          `json:"fallbacks"`
	Thinking     json.RawMessage `json:"thinking"`
	OutputConfig *outputConfig   `json:"output_config"`
	Tools        []tool          `json:"tools"`
	ToolChoice   *toolChoice     `json:"tool_choice"`
	System       []plainBlock    `json:"system"`
	Messages     []struct {
		Role    string       `json:"role"`
		Content []plainBlock `json:"content"`
	} `json:"messages"`
}

func decode(t *testing.T, raw []byte) wireRequest {
	t.Helper()
	var req wireRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("request is not valid JSON: %v", err)
	}
	return req
}

// rawMessages returns the elements of the request's messages array as they
// were written.
func rawMessages(t *testing.T, raw []byte) []string {
	t.Helper()
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("request is not valid JSON: %v", err)
	}
	out := make([]string, len(req.Messages))
	for i, m := range req.Messages {
		out[i] = string(m)
	}
	return out
}

// step is what the fake API answers one request with.
type step struct {
	status int // 0 means 200
	header map[string]string
	file   string // fixture under testdata; .sse files are streamed
	data   []byte // used instead of file when set
	// hold makes the server stop at the fixture's ": pause" line and wait for
	// the client to go away.
	hold bool
}

type recorded struct {
	header http.Header
	body   []byte
}

// fakeAPI replays fixtures, one step per request; the last step repeats.
type fakeAPI struct {
	url   string
	steps []step
	data  [][]byte
	// gate, when set, is what a ": pause" line waits on.
	gate chan struct{}
	// gone is closed when a held request sees its client disconnect.
	gone     chan struct{}
	goneOnce sync.Once
	stalled  atomic.Bool

	mu       sync.Mutex
	requests []recorded
}

func newFakeAPI(t *testing.T, steps ...step) *fakeAPI {
	t.Helper()
	f := &fakeAPI{steps: steps, gone: make(chan struct{})}
	for _, s := range steps {
		data := s.data
		if data == nil {
			data = fixture(t, s.file)
		}
		f.data = append(f.data, data)
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reqBody, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	n := len(f.requests)
	f.requests = append(f.requests, recorded{header: r.Header.Clone(), body: reqBody})
	f.mu.Unlock()

	i := min(n, len(f.steps)-1)
	st, data := f.steps[i], f.data[i]
	for k, v := range st.header {
		w.Header().Set(k, v)
	}
	status := st.status
	if status == 0 {
		status = http.StatusOK
	}
	if strings.HasSuffix(st.file, ".json") || status >= 300 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(data)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(status)
	before, after, paused := bytes.Cut(data, []byte(": pause\n"))
	_, _ = w.Write(before)
	_ = http.NewResponseController(w).Flush()
	if paused && st.hold {
		<-r.Context().Done()
		f.goneOnce.Do(func() { close(f.gone) })
		return
	}
	if paused && f.gate != nil {
		select {
		case <-f.gate:
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			f.stalled.Store(true)
		}
	}
	_, _ = w.Write(after)
}

func (f *fakeAPI) seen() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.requests...)
}

// open sends the full plan to a fake API and returns the stream.
func open(t *testing.T, api *fakeAPI, r llm.Route) llm.Stream {
	t.Helper()
	s, err := Send(t.Context(), http.DefaultClient, freeze(t, testDialect(api.url), r, fullPlan(t), llm.Options{}))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return s
}

func collect(t *testing.T, s llm.Stream) []llm.Event {
	t.Helper()
	var evs []llm.Event
	for ev := range s.Events() {
		evs = append(evs, ev)
	}
	return evs
}

// run streams one fixture to its end and returns what came of it.
func run(t *testing.T, r llm.Route, file string) ([]llm.Event, llm.Result, error) {
	t.Helper()
	s := open(t, newFakeAPI(t, step{file: file}), r)
	evs := collect(t, s)
	res, err := s.Result()
	return evs, res, err
}

func text(evs []llm.Event, kind llm.EventKind) string {
	var sb strings.Builder
	for _, ev := range evs {
		if ev.Kind == kind {
			sb.WriteString(ev.Text)
		}
	}
	return sb.String()
}

// within fails the test if ch is not closed soon.
func within(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
