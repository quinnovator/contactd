package chat

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
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/plan"
)

// fixedNow is the clock every test plan is built with.
const fixedNow = int64(1_760_000_000_000)

// TestMain registers -update here rather than in a package variable, which
// the linter forbids. Run `go test ./internal/llm/wire/chat -update` to
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
			ID: "call_01", Name: "python", Code: "print(6 * 7)", Text: "Let me compute that.",
		}),
		event(t, 3, core.KindToolResult, core.ToolResult{ID: "call_01", Output: "42\n"}),
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
		core.ToolCall{ID: "call_02", Name: "python", Code: "print(7 * 8)"},
		core.ToolResult{ID: "call_02", Output: "56\n"},
	)
	return p
}

// plainDialect is a self-hosted OpenAI-compatible server: the upstream the
// golden bytes were recorded for.
func plainDialect() *Dialect {
	return &Dialect{
		URL:            "http://inference.internal:8000/v1/chat/completions",
		Header:         http.Header{"Authorization": {"Bearer test-key"}},
		MaxTokensField: "max_completion_tokens",
	}
}

// plainRoute is what such a server's profile declares.
func plainRoute() llm.Route {
	return llm.Route{
		Provider: "openai",
		Model:    "gpt-test",
		Wire:     llm.WireChat,
		Caps: llm.Capabilities{
			ContextWindow:  32_768,
			Cache:          llm.CacheAutoPrefix,
			CacheTTL:       5 * time.Minute,
			CacheUsage:     true,
			ToolChoice:     true,
			SingleToolCall: true,
		},
	}
}

func freeze(t *testing.T, d *Dialect, r llm.Route, p *plan.PromptPlan, o llm.Options) *llm.Frozen {
	t.Helper()
	f, err := Prepare(d, r, p, o)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return f
}

func body(t *testing.T, d *Dialect, r llm.Route, p *plan.PromptPlan) []byte {
	t.Helper()
	return freeze(t, d, r, p, llm.Options{}).Body
}

// wireRequest decodes a frozen body for assertions. It is separate from the
// codec's own structs so a test reads the bytes, not the encoder's view.
type wireRequest struct {
	Model               string          `json:"model"`
	Messages            []wireMessage   `json:"messages"`
	Tools               []tool          `json:"tools"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls"`
	MaxTokens           int             `json:"max_tokens"`
	MaxCompletionTokens int             `json:"max_completion_tokens"`
	Stream              bool            `json:"stream"`
	StreamOptions       *streamOptions  `json:"stream_options"`
	ToolChoice          json.RawMessage `json:"tool_choice"`
}

type wireMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	ReasoningContent *string         `json:"reasoning_content"`
	Reasoning        *string         `json:"reasoning"`
	ReasoningDetails json.RawMessage `json:"reasoning_details"`
	ToolCalls        []toolCall      `json:"tool_calls"`
	ToolCallID       string          `json:"tool_call_id"`
}

// text returns string content, and "" when the content is missing or parts.
func (m wireMessage) text() string {
	s, _ := jsonString(m.Content)
	return s
}

func decode(t *testing.T, raw []byte) wireRequest {
	t.Helper()
	var req wireRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("request is not valid JSON: %v\n%s", err, raw)
	}
	return req
}

func roles(req *wireRequest) []string {
	out := make([]string, len(req.Messages))
	for i, m := range req.Messages {
		out[i] = m.Role
	}
	return out
}

// fieldOrder lists a body's top-level field names in the order written.
func fieldOrder(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	var names []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("decode body: %v", err)
		}
		names = append(names, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("decode body: %v", err)
		}
	}
	return names
}

// upstream is a stand-in endpoint that answers every request the same way.
type upstream struct {
	url string
	// gone is closed when a held request sees its client disconnect.
	gone chan struct{}
	// gate, when set, is what a ": pause" line waits on.
	gate chan struct{}
	// seen gets every request received, while there is room.
	seen chan recorded
}

type recorded struct {
	path   string
	header http.Header
	body   []byte
}

type reply struct {
	status int // 0 means 200
	header map[string]string
	body   []byte
	// hold stops at the body's ": pause" line and waits for the client to go
	// away.
	hold bool
}

func newUpstream(t *testing.T, rp reply) *upstream {
	t.Helper()
	u := &upstream{gone: make(chan struct{}), seen: make(chan recorded, 8)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ := io.ReadAll(r.Body)
		select {
		case u.seen <- recorded{path: r.URL.Path, header: r.Header.Clone(), body: sent}:
		default:
		}
		for k, v := range rp.header {
			w.Header().Set(k, v)
		}
		if rp.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(rp.status)
			_, _ = w.Write(rp.body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		before, after, paused := bytes.Cut(rp.body, []byte(": pause\n"))
		_, _ = w.Write(before)
		_ = http.NewResponseController(w).Flush()
		if paused && rp.hold {
			<-r.Context().Done()
			close(u.gone)
			return
		}
		if paused && u.gate != nil {
			select {
			case <-u.gate:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write(after)
	}))
	t.Cleanup(srv.Close)
	u.url = srv.URL
	return u
}

// open sends the full plan to u on route r and returns the stream.
func open(t *testing.T, u *upstream, r llm.Route) llm.Stream {
	t.Helper()
	d := plainDialect()
	d.URL = u.url + "/chat/completions"
	s, err := Send(t.Context(), nil, freeze(t, d, r, fullPlan(t), llm.Options{}))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return s
}

// run streams body on route r and returns the events and how it ended.
func run(t *testing.T, r llm.Route, sse string) ([]llm.Event, llm.Result, error) {
	t.Helper()
	s := open(t, newUpstream(t, reply{body: []byte(sse)}), r)
	evs := collect(s)
	res, err := s.Result()
	return evs, res, err
}

func collect(s llm.Stream) []llm.Event {
	var evs []llm.Event
	for ev := range s.Events() {
		evs = append(evs, ev)
	}
	return evs
}

// said joins the deltas of one kind.
func said(evs []llm.Event, kind llm.EventKind) string {
	var sb strings.Builder
	for _, ev := range evs {
		if ev.Kind == kind {
			sb.WriteString(ev.Text)
		}
	}
	return sb.String()
}

// sse builds a stream body from chunk JSON, ending it with [DONE].
func sse(chunks ...string) string {
	var sb strings.Builder
	for _, c := range chunks {
		sb.WriteString("data: " + c + "\n\n")
	}
	sb.WriteString("data: [DONE]\n\n")
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
