package anthropic

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/turn"
)

func TestStreamText(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t, step{file: "text.sse"})
	c := newClient(t, api.url)
	p := fullPlan(t)

	s, err := c.Stream(t.Context(), p)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	evs := collect(t, s)
	want := []turn.StreamEvent{
		{Type: turn.TextDelta, Text: "Hello"},
		{Type: turn.TextDelta, Text: ", world."},
	}
	if !slices.Equal(evs, want) {
		t.Errorf("events = %+v, want %+v", evs, want)
	}
	stop, err := s.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stop.Text != "Hello, world." || stop.ToolCall != nil {
		t.Errorf("stop = %+v", stop)
	}
	// 12 uncached + 300 written + 2000 read.
	if got, want := stop.Usage, (turn.Usage{InputTokens: 2312, CachedTokens: 2000, OutputTokens: 9}); got != want {
		t.Errorf("usage = %+v, want %+v", got, want)
	}

	reqs := api.seen()
	if len(reqs) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(reqs))
	}
	if !bytes.Equal(reqs[0].body, body(t, c, p)) {
		t.Errorf("body on the wire differs from the marshalled request")
	}
	for k, want := range map[string]string{
		"X-Api-Key":         "test-key",
		"Anthropic-Version": "2023-06-01",
		"Anthropic-Beta":    "server-side-fallback-2026-07-01",
		"Content-Type":      "application/json",
	} {
		if got := reqs[0].header.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}

	// A second range is a no-op and Stop keeps its answer.
	if again := collect(t, s); len(again) != 0 {
		t.Errorf("second range yielded %d events", len(again))
	}
	if stop2, err := s.Stop(); err != nil || stop2.Text != stop.Text {
		t.Errorf("second Stop = %+v, %v", stop2, err)
	}
}

func TestStreamToolUse(t *testing.T) {
	t.Parallel()
	// The server holds back most of the arguments until the test has seen
	// ToolStart, so the announcement cannot be waiting on them.
	api := newFakeAPI(t, step{file: "tool.sse"})
	api.gate = make(chan struct{})
	c := newClient(t, api.url)

	s, err := c.Stream(t.Context(), fullPlan(t))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var evs []turn.StreamEvent
	for ev := range s.Events() {
		evs = append(evs, ev)
		if ev.Type == turn.ToolStart {
			close(api.gate)
		}
	}
	if api.stalled.Load() {
		t.Fatal("ToolStart did not arrive until the arguments had finished")
	}
	want := []turn.StreamEvent{
		{Type: turn.TextDelta, Text: "Let me "},
		{Type: turn.TextDelta, Text: "check."},
		{Type: turn.ToolStart, Name: "python"},
	}
	if !slices.Equal(evs, want) {
		t.Errorf("events = %+v, want %+v", evs, want)
	}
	stop, err := s.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stop.Text != "Let me check." {
		t.Errorf("text = %q", stop.Text)
	}
	tc := stop.ToolCall
	if tc == nil {
		t.Fatal("no tool call")
	}
	if tc.ID != "toolu_01A" || tc.Name != "python" || tc.Code != "print(6 * 7)\nprint(\"<done>\")\n" {
		t.Errorf("tool call = %+v", tc)
	}
	if got, want := stop.Usage, (turn.Usage{InputTokens: 1520, CachedTokens: 1500, OutputTokens: 41}); got != want {
		t.Errorf("usage = %+v, want %+v", got, want)
	}
}

func TestStreamEndings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		file     string
		wantText string
		wantTool string // tool call ID, "" for none
		wantErr  error
	}{
		{name: "several tool calls keep the first", file: "two_tools.sse", wantTool: "toolu_first"},
		{name: "max_tokens drops the cut-off call", file: "max_tokens.sse", wantText: "Running it now."},
		{name: "invalid tool input", file: "bad_tool_input.sse", wantErr: ErrBadToolInput},
		{name: "refusal", file: "refusal.sse", wantText: "Sure, the first", wantErr: ErrRefused},
		{name: "fallback discards the declined call", file: "fallback.sse", wantText: "Before. After."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			api := newFakeAPI(t, step{file: tc.file})
			s, err := newClient(t, api.url).Stream(t.Context(), fullPlan(t))
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			evs := collect(t, s)
			stop, err := s.Stop()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Stop error = %v, want %v", err, tc.wantErr)
			}
			if stop.Text != tc.wantText || text(evs) != tc.wantText {
				t.Errorf("text = %q (stream %q), want %q", stop.Text, text(evs), tc.wantText)
			}
			got := ""
			if stop.ToolCall != nil {
				got = stop.ToolCall.ID
			}
			if got != tc.wantTool {
				t.Errorf("tool call = %q, want %q", got, tc.wantTool)
			}
		})
	}
}

func TestStreamToolStartOncePerStep(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t, step{file: "two_tools.sse"})
	s, err := newClient(t, api.url).Stream(t.Context(), fullPlan(t))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	want := []turn.StreamEvent{{Type: turn.ToolStart, Name: "python"}}
	if evs := collect(t, s); !slices.Equal(evs, want) {
		t.Errorf("events = %+v, want %+v", evs, want)
	}
	stop, err := s.Stop()
	if err != nil || stop.ToolCall == nil || stop.ToolCall.Code != "a = 1" {
		t.Errorf("stop = %+v, %v", stop, err)
	}
}

func TestStreamCancel(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t, step{file: "text.sse", hold: true})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	s, err := newClient(t, api.url).Stream(ctx, fullPlan(t))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	returned := make(chan struct{})
	var evs []turn.StreamEvent
	go func() {
		defer close(returned)
		for ev := range s.Events() {
			evs = append(evs, ev)
			cancel() // the server now sends nothing more
		}
	}()
	within(t, returned, "Events to return after cancellation")
	within(t, api.gone, "the server to see the connection close")

	stop, err := s.Stop()
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Stop error = %v, want context.Canceled", err)
	}
	if len(evs) != 1 || stop.Text != "Hello" {
		t.Errorf("events = %+v, stop text = %q; want the one delta sent before the cancel", evs, stop.Text)
	}
}

func TestStreamBreak(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t, step{file: "text.sse", hold: true})
	s, err := newClient(t, api.url).Stream(t.Context(), fullPlan(t))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	n := 0
	for range s.Events() {
		n++
		break
	}
	// Nothing cancelled the context: leaving the loop alone must hang up.
	within(t, api.gone, "the server to see the connection close")

	stop, err := s.Stop()
	if !errors.Is(err, ErrIncomplete) {
		t.Errorf("Stop error = %v, want ErrIncomplete", err)
	}
	if n != 1 || stop.Text != "Hello" {
		t.Errorf("read %d events, stop text = %q", n, stop.Text)
	}
}

func TestStopWithoutEvents(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t, step{file: "text.sse", hold: true})
	s, err := newClient(t, api.url).Stream(t.Context(), fullPlan(t))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, err := s.Stop(); !errors.Is(err, ErrNotDrained) {
		t.Errorf("Stop error = %v, want ErrNotDrained", err)
	}
	within(t, api.gone, "the server to see the connection close")
	if evs := collect(t, s); len(evs) != 0 {
		t.Errorf("Events after Stop yielded %d events", len(evs))
	}
}

func TestRetryOn429(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t,
		step{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "0"}, file: "error_429.json"},
		step{file: "text.sse"},
	)
	s, err := newClient(t, api.url).Stream(t.Context(), fullPlan(t))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collect(t, s)
	stop, err := s.Stop()
	if err != nil || stop.Text != "Hello, world." {
		t.Fatalf("stop = %+v, %v", stop, err)
	}
	reqs := api.seen()
	if len(reqs) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(reqs))
	}
	if !bytes.Equal(reqs[0].body, reqs[1].body) {
		t.Error("the retry sent a different body")
	}
}

func TestRetryGivesUp(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t, step{status: 529, file: "error_529.json"})
	_, err := newClient(t, api.url).Stream(t.Context(), fullPlan(t))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 529 || apiErr.Type != "overloaded_error" {
		t.Fatalf("error = %v, want a 529 APIError", err)
	}
	if n := len(api.seen()); n != maxAttempts {
		t.Errorf("server saw %d requests, want %d", n, maxAttempts)
	}
}

func TestRetryHonoursContext(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t,
		step{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "20"}, file: "error_429.json"},
	)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		_, err = newClient(t, api.url).Stream(ctx, fullPlan(t))
	}()
	// Once the 429 is out the client is waiting on its 20-second retry-after.
	within(t, api.arrived, "the first request")
	cancel()
	within(t, done, "Stream to return after cancellation")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if n := len(api.seen()); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
}

func TestNoRetryOn400(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t, step{status: http.StatusBadRequest, file: "error_400.json"})
	_, err := newClient(t, api.url).Stream(t.Context(), fullPlan(t))
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want an APIError", err)
	}
	if apiErr.Status != http.StatusBadRequest || apiErr.Type != "invalid_request_error" ||
		apiErr.RequestID != "req_011CSHoEeqs5C35K2UUqR7Fy" {
		t.Errorf("api error = %+v", apiErr)
	}
	const excerpt = "text content blocks must be non-empty"
	if !strings.Contains(apiErr.Body, excerpt) || !strings.Contains(err.Error(), excerpt) ||
		!strings.Contains(err.Error(), "400") {
		t.Errorf("error %q does not carry the status and body excerpt", err)
	}
	if n := len(api.seen()); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
}

func TestStreamErrorEvent(t *testing.T) {
	t.Parallel()

	t.Run("after output it ends the call", func(t *testing.T) {
		t.Parallel()
		api := newFakeAPI(t, step{file: "error_mid.sse"}, step{file: "text.sse"})
		s, err := newClient(t, api.url).Stream(t.Context(), fullPlan(t))
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		evs := collect(t, s)
		stop, err := s.Stop()
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Type != "overloaded_error" || apiErr.Message != "Overloaded" {
			t.Fatalf("Stop error = %v, want the overloaded_error event", err)
		}
		if text(evs) != "Half a rep" || stop.Text != "Half a rep" {
			t.Errorf("text = %q / %q", text(evs), stop.Text)
		}
		// The caller already saw text, so a retry would repeat it.
		if n := len(api.seen()); n != 1 {
			t.Errorf("server saw %d requests, want 1", n)
		}
	})

	t.Run("before output it is retried", func(t *testing.T) {
		t.Parallel()
		api := newFakeAPI(t, step{file: "error_first.sse"}, step{file: "text.sse"})
		s, err := newClient(t, api.url).Stream(t.Context(), fullPlan(t))
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		evs := collect(t, s)
		stop, err := s.Stop()
		if err != nil || stop.Text != "Hello, world." || text(evs) != "Hello, world." {
			t.Fatalf("stop = %+v, %v", stop, err)
		}
		if n := len(api.seen()); n != 2 {
			t.Errorf("server saw %d requests, want 2", n)
		}
	})
}

func TestNewValidates(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]Config{
		"no key":       {},
		"bad base url": {APIKey: "k", BaseURL: "api.anthropic.com"},
		"negative max": {APIKey: "k", MaxTokens: -1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(cfg); err == nil {
				t.Error("New accepted an invalid config")
			}
		})
	}
	c, err := New(Config{APIKey: "k", BaseURL: "https://example.test/"})
	if err != nil {
		t.Fatal(err)
	}
	if c.endpoint != "https://example.test/v1/messages" || c.model != "claude-opus-5-5" ||
		c.summaryModel != "claude-haiku-5-5" {
		t.Errorf("client = %+v", c)
	}
}

// TestAbandonedStreamsLeakNoGoroutines is not parallel: it counts goroutines
// in the whole process, and tests running beside it would move the count.
//
//nolint:paralleltest // see above
func TestAbandonedStreamsLeakNoGoroutines(t *testing.T) {
	api := newFakeAPI(t, step{file: "text.sse", hold: true})
	transport := &http.Transport{}
	c, err := New(Config{APIKey: "test-key", BaseURL: api.url, HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	p := fullPlan(t)
	abandon := func(viaCancel bool) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s, err := c.Stream(ctx, p)
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		for range s.Events() {
			if !viaCancel {
				break
			}
			cancel()
		}
	}
	settle := func() int {
		transport.CloseIdleConnections()
		n := runtime.NumGoroutine()
		for range 200 {
			time.Sleep(5 * time.Millisecond)
			if m := runtime.NumGoroutine(); m >= n {
				return m
			} else {
				n = m
			}
		}
		return n
	}

	abandon(true) // warm up whatever the first request starts for good
	base := settle()
	for i := range 40 {
		abandon(i%2 == 0)
	}
	deadline := time.Now().Add(5 * time.Second)
	after := settle()
	for after > base && time.Now().Before(deadline) {
		after = settle()
	}
	if after > base {
		t.Errorf("%d goroutines before, %d after 40 abandoned streams", base, after)
	}
}

func TestRetryLogCarriesNoUserData(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t,
		step{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "0"}, file: "error_429.json"},
		step{file: "text.sse"},
	)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, err := New(Config{APIKey: "test-key", BaseURL: api.url, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	c.baseDelay = time.Millisecond
	s, err := c.Stream(t.Context(), fullPlan(t))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collect(t, s)
	if _, err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	out := logs.String()
	for _, want := range []string{`"msg":"llm_request_retry"`, `"contact_id":42`, `"status":429`, `"attempt":1`} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %s: %s", want, out)
		}
	}
	// Not the key, not the prompt, not the reply, not the error body.
	for _, secret := range []string{"test-key", "Ada", "Sam", "Hello", "ate limit"} {
		if strings.Contains(out, secret) {
			t.Errorf("log contains %q: %s", secret, out)
		}
	}
}

func TestImplementsPorts(t *testing.T) {
	t.Parallel()
	var (
		_ turn.LLM        = (*Client)(nil)
		_ turn.Summarizer = (*Client)(nil)
	)
}
