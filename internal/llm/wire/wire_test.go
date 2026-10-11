package wire_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/wire"
)

func TestClassify(t *testing.T) {
	t.Parallel()
	for status, want := range map[int]llm.ErrorClass{
		429: llm.ErrRateLimited,
		401: llm.ErrAuth,
		403: llm.ErrAuth,
		402: llm.ErrQuota,
		413: llm.ErrContextOverflow,
		408: llm.ErrOverloaded,
		500: llm.ErrOverloaded,
		529: llm.ErrOverloaded,
		400: llm.ErrBadRequest,
		404: llm.ErrBadRequest,
		200: llm.ErrUnknown,
	} {
		if got := wire.Classify(status); got != want {
			t.Errorf("Classify(%d) = %s, want %s", status, got, want)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	t.Parallel()
	h := func(k, v string) http.Header { return http.Header{k: {v}} }
	if got := wire.RetryAfter(h("Retry-After", "2")); got != 2*time.Second {
		t.Errorf("seconds: %v", got)
	}
	if got := wire.RetryAfter(h("Retry-After-Ms", "1500")); got != 1500*time.Millisecond {
		t.Errorf("milliseconds: %v", got)
	}
	date := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	if got := wire.RetryAfter(h("Retry-After", date)); got < 80*time.Second || got > 91*time.Second {
		t.Errorf("HTTP date: %v", got)
	}
	for _, bad := range []string{"", "soon", "-3", "0"} {
		if got := wire.RetryAfter(h("Retry-After", bad)); got != 0 {
			t.Errorf("Retry-After %q: %v, want none", bad, got)
		}
	}
}

func TestSSEReader(t *testing.T) {
	t.Parallel()
	stream := ": keep-alive\n\n" +
		"event: message_start\ndata: {\"a\":1}\n\n" +
		": another comment\n" +
		"data: first line\ndata: second line\r\n\r\n" +
		"data:" + "x" + strings.Repeat("y", 200_000) + "\n\n" + // far longer than any buffer default
		"data: [DONE]" // no trailing blank line, as some servers send
	r := wire.NewSSEReader(strings.NewReader(stream))
	want := []struct{ name, data string }{
		{"message_start", `{"a":1}`},
		{"", "first line\nsecond line"},
		{"", "x" + strings.Repeat("y", 200_000)},
		{"", "[DONE]"},
	}
	for i, w := range want {
		ev, err := r.Next()
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if ev.Name != w.name || string(ev.Data) != w.data {
			t.Errorf("event %d = %q / %.40q (%d bytes), want %q / %.40q", i, ev.Name, ev.Data, len(ev.Data), w.name, w.data)
		}
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("after the last event: %v, want EOF", err)
	}
}

func TestPost(t *testing.T) {
	t.Parallel()
	var gotHeader, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotHeader, gotBody = r.Header.Get("X-Test"), string(body)
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("data: hi\n\n"))
		case "/limited":
			w.Header().Set("Retry-After", "7")
			http.Error(w, `{"error":{"message":"slow down `+strings.Repeat("z", 2000)+`"}}`, http.StatusTooManyRequests)
		default:
			http.Error(w, `{"error":{"type":"context_length_exceeded"}}`, http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	frozen := func(path string) *llm.Frozen {
		return &llm.Frozen{
			Route: llm.Route{Provider: "p", Model: "m"}, URL: srv.URL + path,
			Header: http.Header{"X-Test": {"yes"}}, Body: []byte(`{"q":1}`),
		}
	}

	resp, err := wire.Post(t.Context(), srv.Client(), frozen("/ok"), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "data: hi\n\n" || gotHeader != "yes" || gotBody != `{"q":1}` {
		t.Errorf("round trip: body %q, header %q, sent %q", body, gotHeader, gotBody)
	}

	_, err = wire.Post(t.Context(), srv.Client(), frozen("/limited"), nil) //nolint:bodyclose // an error carries no body
	e, ok := errors.AsType[*llm.Error](err)
	if !ok || e.Class != llm.ErrRateLimited || e.Status != 429 || e.RetryAfter != 7*time.Second || e.Provider != "p" {
		t.Fatalf("429: %+v", err)
	}
	// Error bodies can echo the prompt; only a short excerpt is kept.
	if n := len(e.Err.Error()); n > 320 || !strings.Contains(e.Err.Error(), "slow down") {
		t.Errorf("excerpt is %d bytes: %.60q", n, e.Err.Error())
	}

	// A codec can refine what a status alone cannot say.
	refine := func(status int, body []byte) llm.ErrorClass {
		if status == 400 && strings.Contains(string(body), "context_length_exceeded") {
			return llm.ErrContextOverflow
		}
		return llm.ErrUnknown
	}
	if _, err = wire.Post(t.Context(), srv.Client(), frozen("/other"), refine); llm.ClassOf(err) != llm.ErrContextOverflow { //nolint:bodyclose // an error carries no body
		t.Errorf("refined 400: %v", err)
	}
	if _, err = wire.Post(t.Context(), srv.Client(), frozen("/other"), nil); llm.ClassOf(err) != llm.ErrBadRequest { //nolint:bodyclose // an error carries no body
		t.Errorf("plain 400: %v", err)
	}

	// A connection that cannot be made is a transport error, and retryable.
	dead := frozen("/ok")
	dead.URL = "http://127.0.0.1:1/nothing-listens-here"
	if _, err = wire.Post(t.Context(), srv.Client(), dead, nil); llm.ClassOf(err) != llm.ErrTransport { //nolint:bodyclose // an error carries no body
		t.Errorf("unreachable: %v", err)
	}
}
