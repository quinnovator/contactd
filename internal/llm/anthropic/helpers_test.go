package anthropic

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
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/turn"
)

// fixedNow is the clock every test plan is built with.
const fixedNow = int64(1_760_000_000_000)

// TestMain registers -update here rather than in a package variable, which
// the linter forbids. Run `go test ./internal/llm/anthropic -update` to
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

// planOpts picks which tiers a test plan has.
type planOpts struct {
	system  string
	tools   []core.ToolDef
	profile string
	recall  []plan.Recalled
	events  []core.Event
}

func buildPlan(t *testing.T, o planOpts) *plan.PromptPlan {
	t.Helper()
	agent := &core.AgentConfig{System: o.system, Tools: o.tools}
	in := plan.Input{Contact: 42, Agent: agent, Profile: o.profile, Tail: o.events}
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

func newClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := New(Config{APIKey: "test-key", BaseURL: baseURL})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.baseDelay = time.Millisecond
	return c
}

func body(t *testing.T, c *Client, p *plan.PromptPlan) []byte {
	t.Helper()
	raw, err := marshal(c.buildRequest(p))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func decode(t *testing.T, raw []byte) request {
	t.Helper()
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("request is not valid JSON: %v", err)
	}
	return req
}

// step is what the fake API answers one request with.
type step struct {
	status int // 0 means 200
	header map[string]string
	file   string // fixture under testdata; .sse files are streamed
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
	data  map[string][]byte
	// gate, when set, is what a ": pause" line waits on.
	gate chan struct{}
	// gone is closed when a held request sees its client disconnect.
	gone     chan struct{}
	goneOnce sync.Once
	// arrived gets a value for every request received.
	arrived chan struct{}
	stalled atomic.Bool

	mu       sync.Mutex
	requests []recorded
}

func newFakeAPI(t *testing.T, steps ...step) *fakeAPI {
	t.Helper()
	f := &fakeAPI{
		steps:   steps,
		data:    map[string][]byte{},
		gone:    make(chan struct{}),
		arrived: make(chan struct{}, 64),
	}
	for _, s := range steps {
		f.data[s.file] = fixture(t, s.file)
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
	select {
	case f.arrived <- struct{}{}:
	default:
	}

	st := f.steps[min(n, len(f.steps)-1)]
	data := f.data[st.file]
	for k, v := range st.header {
		w.Header().Set(k, v)
	}
	status := st.status
	if status == 0 {
		status = http.StatusOK
	}
	if !strings.HasSuffix(st.file, ".sse") {
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

func collect(t *testing.T, s turn.Stream) []turn.StreamEvent {
	t.Helper()
	var evs []turn.StreamEvent
	for ev := range s.Events() {
		evs = append(evs, ev)
	}
	return evs
}

func text(evs []turn.StreamEvent) string {
	var sb strings.Builder
	for _, ev := range evs {
		if ev.Type == turn.TextDelta {
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
