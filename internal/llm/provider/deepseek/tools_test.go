package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/obs"
)

func TestWebAPIs(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{WireMessages, WireChat} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("request method or headers: %s %v", r.Method, r.Header)
				}
				var args map[string]any
				if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
					t.Error(err)
				}
				switch r.URL.Path {
				case "/gateway/v1/tools/search":
					if args["query"] != "新闻 site:example.com" || args["count"] != float64(3) || args["ori_question"] != "What happened?" || args["localization"] != "8.8.8.8" || args["need_content"] != true {
						t.Errorf("search arguments = %v", args)
					}
					_, _ = io.WriteString(w, `{"results":[{"title":"News","url":"https://example.com","summary":"Source summary","content":"Page content","date":null,"authority":3}]}`)
				case "/gateway/v1/tools/fetch":
					if args["url"] != "https://example.com" {
						t.Errorf("fetch arguments = %v", args)
					}
					_, _ = io.WriteString(w, `{"title":"News","content":"# Page\nSource content"}`)
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			base := srv.URL + "/gateway"
			if mode == WireMessages {
				base += "/anthropic"
			} else {
				base += "/v1"
			}
			p := newProvider(t, Config{Wire: mode, BaseURL: base})
			r := route(t, p, "")
			defs := p.Tools(r)
			if len(defs) != 2 || defs[0].Name != searchTool || defs[1].Name != fetchTool {
				t.Fatalf("tools = %+v", defs)
			}
			for _, tc := range []struct{ name, args, want string }{
				{searchTool, `{"query":"新闻 site:example.com","count":3,"ori_question":"What happened?","localization":"8.8.8.8","need_content":true}`, "Source summary"},
				{fetchTool, `{"url":"https://example.com"}`, `# Page\nSource content`},
			} {
				out, err := p.RunTool(t.Context(), r, core.ToolCall{Name: tc.name, Args: json.RawMessage(tc.args)})
				if err != nil || !strings.Contains(out, tc.want) {
					t.Errorf("%s = %q, %v", tc.name, out, err)
				}
			}
			defs[0].Input[0] = '!'
			if !json.Valid(p.Tools(r)[0].Input) {
				t.Error("tool schema mutated between calls")
			}
			if got := p.Tools(llm.Route{Provider: "elsewhere", Model: r.Model}); len(got) != 0 {
				t.Error("tools offered on another provider")
			}
		})
	}
}

func TestWebToolArguments(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, args string }{
		{searchTool, `{"query":"` + strings.Repeat("界", 128) + `"}`},
		{searchTool, `{"query":"hello","ori_question":null}`},
		{searchTool, `{"query":"hello","count":1}`},
		{searchTool, `{"query":"hello","count":50}`},
		{fetchTool, `{"url":"http://example.com/path?q=x"}`},
	} {
		if _, _, err := toolArgs(core.ToolCall{Name: tc.name, Args: json.RawMessage(tc.args)}); err != nil {
			t.Errorf("valid arguments %s: %v", tc.args, err)
		}
	}
	for _, tc := range []struct{ name, args string }{
		{searchTool, `{}`},
		{searchTool, `null`},
		{searchTool, `[]`},
		{searchTool, `{`},
		{searchTool, `{"query":"hello"} {}`},
		{searchTool, `{"query":"  \n"}`},
		{searchTool, `{"query":"` + strings.Repeat("界", 129) + `"}`},
		{searchTool, `{"query":"hello","count":0}`},
		{searchTool, `{"query":"hello","count":51}`},
		{searchTool, `{"query":"hello","count":1.5}`},
		{searchTool, `{"query":"hello","need_content":"yes"}`},
		{searchTool, `{"query":"hello","ori_question":""}`},
		{searchTool, `{"query":"hello","other":true}`},
		{fetchTool, `{}`},
		{fetchTool, `{"url":"file:///etc/passwd"}`},
		{fetchTool, `{"url":"https:///no-host"}`},
		{fetchTool, `{"url":"https://user:password@example.com"}`},
		{fetchTool, `{"url":"https://example.com/` + strings.Repeat("a", 8192) + `"}`},
		{"unknown", `{}`},
	} {
		if _, _, err := toolArgs(core.ToolCall{Name: tc.name, Args: json.RawMessage(tc.args)}); err == nil {
			t.Errorf("accepted invalid %s arguments %.160s", tc.name, tc.args)
		}
	}
}

func TestWebToolErrorsAndRetry(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 401, 402, 403, 404, 408, 413, 429, 500, 504} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := calls.Add(1)
				if n > 1 {
					_, _ = io.WriteString(w, `{"results":[]}`)
					return
				}
				w.Header().Set("Retry-After", "0.001")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"message":"upstream failure"}}`)
			}))
			defer srv.Close()
			p := newProvider(t, Config{BaseURL: srv.URL})
			c, err := llm.NewClient(llm.ClientConfig{Providers: []llm.Provider{p}, Logger: obs.Discard(), Retry: llm.RetryPolicy{Attempts: 3, Base: time.Nanosecond, Max: time.Nanosecond, MaxRetryAfter: time.Millisecond}})
			if err != nil {
				t.Fatal(err)
			}
			out, err := c.RunTool(t.Context(), route(t, p, ""), core.ToolCall{Name: searchTool, Args: json.RawMessage(`{"query":"test"}`)})
			if status == 408 || status == 429 || status >= 500 {
				if err != nil || out != `{"results":[]}` || calls.Load() != 2 {
					t.Errorf("retry = %q, %v after %d calls", out, err, calls.Load())
				}
			} else if err == nil || calls.Load() != 1 {
				t.Errorf("non-retryable response: %v after %d calls", err, calls.Load())
			}
		})
	}
}

func TestWebToolMalformedResponses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ tool, body string }{
		{searchTool, "not json"},
		{searchTool, `{}`},
		{searchTool, `{"results":null}`},
		{searchTool, `{"results":{}}`},
		{fetchTool, `{"title":"missing content"}`},
		{fetchTool, `{"title":"x","content":null}`},
		{fetchTool, strings.Repeat("a", maxToolResponse+1)},
	} {
		t.Run(fmt.Sprintf("%s_%d", tc.tool, len(tc.body)), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.body) }))
			defer srv.Close()
			p := newProvider(t, Config{BaseURL: srv.URL})
			args := `{"query":"test"}`
			if tc.tool == fetchTool {
				args = `{"url":"https://example.com"}`
			}
			_, err := p.RunTool(t.Context(), route(t, p, ""), core.ToolCall{Name: tc.tool, Args: json.RawMessage(args)})
			if err == nil {
				t.Error("accepted malformed response")
			}
		})
	}
}

func TestWebToolCancellation(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	p := newProvider(t, Config{BaseURL: srv.URL})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := p.RunTool(ctx, llm.Route{Provider: ID, Model: models()[0]}, core.ToolCall{Name: searchTool, Args: json.RawMessage(`{"query":"test"}`)})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation = %v", err)
	}
}
