package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/api"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/eventlog"
	fakeinfer "github.com/quinnovator/contactd/internal/infer/fake"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/recall"
	"github.com/quinnovator/contactd/internal/turn"
)

type embed struct{}

func (embed) Embed(_ context.Context, texts []string, _ recall.Priority) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = fakeinfer.Embed(t)
	}
	return out, nil
}

type rerank struct{}

func (rerank) Score(_ context.Context, q string, ps []string) ([]float32, error) {
	out := make([]float32, len(ps))
	for i, p := range ps {
		out[i] = fakeinfer.Score(q, p)
	}
	return out, nil
}

// server runs the real API over a real SQLite file with a fake model.
type server struct {
	t   *testing.T
	url string
	api *api.Server
	reg *actor.Registry
}

func start(t *testing.T) *server {
	t.Helper()
	store, err := eventlog.Open(t.Context(), filepath.Join(t.TempDir(), "contacts.db"), eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	wctx, stopWriter := context.WithCancel(context.Background())
	writerDone := make(chan struct{})
	go func() { store.Run(wctx); close(writerDone) }()

	llm := fakellm.New(fakellm.Options{PerChunk: time.Millisecond})
	cfg := actor.DefaultConfig()
	cfg.Debounce, cfg.DebounceCap = 20*time.Millisecond, 100*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	reg := actor.New(ctx, actor.Deps{
		Log: store, Hydrate: store, Repair: store, Embed: embed{}, Summarize: llm, Snapshots: store,
		Turn: &turn.Deps{
			Embed: embed{}, Rerank: rerank{}, Log: store, LLM: llm, Logger: obs.Discard(),
			Recall: cfg.Recall, Plan: plan.DefaultParams(), MaxToolSteps: 4, MaxToolOutput: 4096, QueryTokens: 128,
		},
	}, cfg, obs.Discard())

	a := api.New(store, reg, obs.Discard())
	ts := httptest.NewServer(a.Handler())
	t.Cleanup(func() {
		reg.Shutdown(ctx)
		ts.Close()
		cancel()
		stopWriter()
		<-writerDone
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return &server{t: t, url: ts.URL, api: a, reg: reg}
}

func (s *server) do(method, path, body string) (int, map[string]any) {
	s.t.Helper()
	req, err := http.NewRequestWithContext(s.t.Context(), method, s.url+path, strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	var out map[string]any
	if len(raw) > 0 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(raw, &out); err != nil {
			s.t.Fatalf("%s %s: bad JSON %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode, out
}

func (s *server) contact() string {
	s.t.Helper()
	status, body := s.do(http.MethodPost, "/v1/contacts", `{"agent":{"system":"You are Ada."},"profile":"likes tea"}`)
	if status != http.StatusCreated {
		s.t.Fatalf("create contact: %d %v", status, body)
	}
	return fmt.Sprint(body["id"])
}

// sse opens the stream and returns a channel of decoded deltas.
func (s *server) sse(id string) <-chan core.Delta {
	s.t.Helper()
	req, err := http.NewRequestWithContext(s.t.Context(), http.MethodGet, s.url+"/v1/contacts/"+id+"/stream", nil)
	if err != nil {
		s.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the reader goroutine below
	if err != nil {
		s.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		_ = resp.Body.Close()
		s.t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	out := make(chan core.Delta, 256)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		defer close(out)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var d core.Delta
			if json.Unmarshal([]byte(data), &d) == nil {
				out <- d
			}
		}
	}()
	// The first bytes confirm the subscription is in place.
	return out
}

func waitMessage(t *testing.T, deltas <-chan core.Delta) core.Delta {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case d, ok := <-deltas:
			if !ok {
				t.Fatal("stream ended before the reply")
			}
			if d.Type == core.DeltaMessage {
				return d
			}
		case <-timeout:
			t.Fatal("no reply within 10s")
		}
	}
}

func TestMessageRoundTripOverSSE(t *testing.T) {
	t.Parallel()
	s := start(t)
	id := s.contact()
	deltas := s.sse(id)
	time.Sleep(50 * time.Millisecond) // let the subscription register

	status, body := s.do(http.MethodPost, "/v1/contacts/"+id+"/messages", `{"text":"hello there"}`)
	if status != http.StatusAccepted || body["seq"] != float64(1) {
		t.Fatalf("post message: %d %v, want 202 with seq 1", status, body)
	}
	reply := waitMessage(t, deltas)
	if !strings.Contains(reply.Text, "hello there") || reply.Seq != 2 {
		t.Errorf("reply = %+v", reply)
	}

	status, body = s.do(http.MethodGet, "/v1/contacts/"+id+"/events", "")
	events, _ := body["events"].([]any)
	if status != http.StatusOK || len(events) != 2 {
		t.Fatalf("events: %d %v", status, body)
	}
	first, _ := events[0].(map[string]any)
	if first["kind"] != "user_msg" || first["seq"] != float64(1) {
		t.Errorf("first event = %v", first)
	}
	if b, _ := first["body"].(map[string]any); b["text"] != "hello there" {
		t.Errorf("first event body = %v", first["body"])
	}

	// Paging: one event per page, with a cursor until the last page.
	_, page := s.do(http.MethodGet, "/v1/contacts/"+id+"/events?limit=1", "")
	if got, _ := page["events"].([]any); len(got) != 1 || page["next_after"] != float64(1) {
		t.Errorf("page 1 = %v", page)
	}
	_, page = s.do(http.MethodGet, "/v1/contacts/"+id+"/events?after=2", "")
	if got, _ := page["events"].([]any); len(got) != 0 || page["next_after"] != nil {
		t.Errorf("page past the end = %v", page)
	}
}

func TestLiveWebSocket(t *testing.T) {
	t.Parallel()
	s := start(t)
	id := s.contact()
	conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(s.url, "http")+"/v1/contacts/"+id+"/live", nil) //nolint:bodyclose // the library owns the handshake response
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow() //nolint:errcheck // test teardown

	send := func(v string) {
		if err := conn.Write(t.Context(), websocket.MessageText, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	send(`{"type":"typing"}`)
	send(`{"type":"nonsense"}`)
	send(`{"type":"message","text":"ping over websocket"}`)

	var sawError, sawAck, sawReply bool
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for !sawReply {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (error=%t ack=%t)", err, sawError, sawAck)
		}
		var frame map[string]any
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatal(err)
		}
		switch frame["type"] {
		case "error":
			sawError = true
		case "ack":
			sawAck = frame["seq"] == float64(1)
		case "message":
			sawReply = strings.Contains(fmt.Sprint(frame["text"]), "ping over websocket")
		}
	}
	if !sawError || !sawAck {
		t.Errorf("error frame for the bad type: %t; ack with seq 1: %t", sawError, sawAck)
	}
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Errorf("close: %v", err)
	}
}

func TestValidationAndErrors(t *testing.T) {
	t.Parallel()
	s := start(t)
	id := s.contact()
	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"unknown contact", http.MethodPost, "/v1/contacts/999/messages", `{"text":"hi"}`, http.StatusNotFound},
		{"non-numeric contact", http.MethodPost, "/v1/contacts/abc/messages", `{"text":"hi"}`, http.StatusNotFound},
		{"stream of unknown contact", http.MethodGet, "/v1/contacts/999/stream", "", http.StatusNotFound},
		{"events of unknown contact", http.MethodGet, "/v1/contacts/999/events", "", http.StatusNotFound},
		{"empty text", http.MethodPost, "/v1/contacts/" + id + "/messages", `{"text":""}`, http.StatusBadRequest},
		{"malformed JSON", http.MethodPost, "/v1/contacts/" + id + "/messages", `{"text":`, http.StatusBadRequest},
		{"unknown field", http.MethodPost, "/v1/contacts/" + id + "/messages", `{"text":"hi","thread":"t1"}`, http.StatusBadRequest},
		{"oversized text", http.MethodPost, "/v1/contacts/" + id + "/messages", `{"text":"` + strings.Repeat("x", 70<<10) + `"}`, http.StatusBadRequest},
		{"bad cursor", http.MethodGet, "/v1/contacts/" + id + "/events?after=-1", "", http.StatusBadRequest},
		{"duplicate tool", http.MethodPost, "/v1/contacts", `{"agent":{"tools":[{"name":"a","env":{"base":"x"}},{"name":"a","env":{"base":"x"}}]}}`, http.StatusBadRequest},
		{"tool without a base", http.MethodPost, "/v1/contacts", `{"agent":{"tools":[{"name":"a","env":{}}]}}`, http.StatusBadRequest},
		{"network on tier 0", http.MethodPost, "/v1/contacts", `{"agent":{"tools":[{"name":"a","env":{"base":"x","network":true}}]}}`, http.StatusBadRequest},
		{"tier out of range", http.MethodPost, "/v1/contacts", `{"agent":{"tools":[{"name":"a","env":{"tier":2,"base":"x"}}]}}`, http.StatusBadRequest},
		{"no thread endpoints", http.MethodPost, "/v1/contacts/" + id + "/threads", `{}`, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status, body := s.do(tc.method, tc.path, tc.body)
			if status != tc.want {
				t.Errorf("%s %s = %d %v, want %d", tc.method, tc.path, status, body, tc.want)
			}
		})
	}
}

func TestHealthAndMetrics(t *testing.T) {
	t.Parallel()
	s := start(t)
	if status, body := s.do(http.MethodGet, "/healthz", ""); status != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthz = %d %v", status, body)
	}
	id := s.contact()
	deltas := s.sse(id)
	time.Sleep(50 * time.Millisecond)
	s.do(http.MethodPost, "/v1/contacts/"+id+"/messages", `{"text":"count me"}`)
	waitMessage(t, deltas)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.url+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); err != nil || cerr != nil {
		t.Fatalf("read metrics: %v, %v", err, cerr)
	}
	for _, name := range []string{
		"cache_break_tier", "provider_cached_token_ratio", "turn_overhead_ms", "recall_stage1_ms", "rerank_ms",
		"embed_queue_depth", "hydrated_actors", "hydrate_ms", "hibernations_total", "writer_batch_size", "commit_ms",
		"env_lease_ms", "speculative_restore_hits",
	} {
		if !strings.Contains(string(raw), "# HELP "+name+" ") {
			t.Errorf("metric %s is not exported", name)
		}
	}

	// Draining is the first step of shutdown: health goes red, and once the
	// registry is closed new messages are refused with 503.
	s.api.Drain()
	if status, _ := s.do(http.MethodGet, "/healthz", ""); status != http.StatusServiceUnavailable {
		t.Errorf("healthz while draining = %d, want 503", status)
	}
	s.reg.Shutdown(t.Context())
	if status, _ := s.do(http.MethodPost, "/v1/contacts/"+id+"/messages", `{"text":"late"}`); status != http.StatusServiceUnavailable {
		t.Errorf("message after shutdown = %d, want 503", status)
	}
	select {
	case _, open := <-deltas:
		for open {
			_, open = <-deltas
		}
	case <-time.After(5 * time.Second):
		t.Error("the SSE stream was not closed at shutdown")
	}
}
