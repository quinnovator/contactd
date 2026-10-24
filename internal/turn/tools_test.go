package turn_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/llm/provider/deepseek"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/turn"
)

func TestDeepSeekWebToolLoop(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{deepseek.WireMessages, deepseek.WireChat} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			var modelCalls, webCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				switch r.URL.Path {
				case "/v1/tools/search":
					webCalls.Add(1)
					if string(body) != `{"query":"test"}` {
						t.Errorf("search body = %s", body)
					}
					_, _ = io.WriteString(w, `{"results":[{"url":"https://example.com","summary":"unique search result"}]}`)
				case "/v1/tools/fetch":
					webCalls.Add(1)
					if string(body) != `{"url":"https://example.com"}` {
						t.Errorf("fetch body = %s", body)
					}
					_, _ = io.WriteString(w, `{"title":"Page","content":"unique fetched content"}`)
				case "/anthropic/v1/messages", "/chat/completions":
					n := modelCalls.Add(1)
					for _, name := range []string{"web_search", "web_fetch"} {
						if !strings.Contains(string(body), name) {
							t.Errorf("prompt is missing tool %s", name)
						}
					}
					if n > 1 && !strings.Contains(string(body), "unique search result") {
						t.Error("search result not sent back to model")
					}
					if n > 2 && !strings.Contains(string(body), "unique fetched content") {
						t.Error("fetch result not sent back to model")
					}
					tool, args := "web_search", `{"query":"test"}`
					if n == 2 {
						tool, args = "web_fetch", `{"url":"https://example.com"}`
					} else if n >= 3 {
						tool = ""
					}
					webToolStream(w, mode, tool, args)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			base := srv.URL
			if mode == deepseek.WireMessages {
				base += "/anthropic"
			}
			p, err := deepseek.New(deepseek.Config{APIKey: "test-key", Wire: mode, BaseURL: base})
			if err != nil {
				t.Fatal(err)
			}
			s, l := &sink{}, &leaser{}
			in := input(s, l, "Find and read the source.")
			// No explicitly configured tools and no environment are needed.
			in.Agent.Tools, in.Env = nil, nil
			res, err := turn.Run(t.Context(), in, deps(p))
			if err != nil {
				t.Fatal(err)
			}
			if res.Final != "Done." || modelCalls.Load() != 3 || webCalls.Load() != 2 || len(s.calls) != 2 || len(s.results) != 2 {
				t.Fatalf("result = %+v, model calls = %d, web calls = %d, logged = %d/%d", res, modelCalls.Load(), webCalls.Load(), len(s.calls), len(s.results))
			}
			for i, call := range s.calls {
				if len(call.Args) == 0 || call.Code != "" || call.Route != res.Route.ID() || s.results[i].ID != call.ID || s.results[i].IsError {
					t.Errorf("logged exchange: %+v / %+v", call, s.results[i])
				}
			}
			if len(in.Agent.Tools) != 0 {
				t.Error("provider tools mutated the stored agent config")
			}
		})
	}
}

func webToolStream(w http.ResponseWriter, mode, tool, args string) {
	w.Header().Set("Content-Type", "text/event-stream")
	if mode == deepseek.WireChat {
		if tool == "" {
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Done.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		raw, _ := json.Marshal(args)
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"Read sources.\",\"tool_calls\":[{\"index\":0,\"id\":%q,\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%s}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", tool, tool, raw)
		return
	}
	if tool == "" {
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"Done.\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
		return
	}
	_, _ = fmt.Fprintf(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":%q,\"name\":%q,\"input\":%s}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", tool, tool, args)
}

type toolProvider struct {
	*fakellm.Provider
	out   string
	err   error
	calls int
}

func (*toolProvider) Tools(llm.Route) []core.ToolDef {
	return []core.ToolDef{{Name: "lookup", Input: json.RawMessage(`{"type":"object"}`)}}
}

func (p *toolProvider) RunTool(context.Context, llm.Route, core.ToolCall) (string, error) {
	p.calls++
	return p.out, p.err
}

func TestProviderToolRuntime(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("error=", fail), func(t *testing.T) {
			t.Parallel()
			p := &toolProvider{Provider: fakellm.New(fakellm.Options{}), out: strings.Repeat("result ", 100)}
			if fail {
				p.err = errors.New("lookup failed")
			}
			s := &sink{}
			in := input(s, &leaser{}, `!lookup {}`)
			res, err := turn.Run(t.Context(), in, deps(p))
			if err != nil || res.Final == "" || p.calls != 1 || len(s.results) != 1 || s.results[0].IsError != fail {
				t.Fatalf("result = %+v, error = %v, calls = %d, results = %+v", res, err, p.calls, s.results)
			}
			if !fail && !strings.Contains(s.results[0].Output, "bytes omitted") {
				t.Error("provider output was not bounded")
			}
			if len(in.Agent.Tools) != 2 {
				t.Error("stored tools changed")
			}
		})
	}
	t.Run("explicit tool keeps its runtime", func(t *testing.T) {
		t.Parallel()
		p := &toolProvider{Provider: fakellm.New(fakellm.Options{})}
		s := &sink{}
		in := input(s, &leaser{}, "!lookup print(1)")
		in.Agent.Tools = append(in.Agent.Tools, core.ToolDef{Name: "lookup", Env: pySpec()})
		res, err := turn.Run(t.Context(), in, deps(p))
		if err != nil || p.calls != 0 || len(res.Plan.Tools) != 3 || s.results[0].Output != "out:print(1)" {
			t.Fatalf("result = %+v, error = %v, provider calls = %d", res, err, p.calls)
		}
	})
	t.Run("delegated tools are derived again from the route", func(t *testing.T) {
		t.Parallel()
		p := &toolProvider{Provider: fakellm.New(fakellm.Options{})}
		d := deps(p)
		dg := &delegator{}
		d.Delegate = dg
		in := input(&sink{}, &leaser{}, "!delegate check sources")
		in.Agent.Delegate = true
		parent, err := turn.Run(t.Context(), in, d)
		if err != nil || len(dg.got) != 1 {
			t.Fatalf("delegation = %+v, %v", dg.got, err)
		}
		child := input(&sink{}, &leaser{}, `!lookup {}`)
		child.Agent = dg.got[0].Agent
		child.Agent.Parent = 1
		res, err := turn.Run(t.Context(), child, d)
		if err != nil || p.calls != 1 || parent.Plan.Chain[plan.TierStatic] != res.Plan.Chain[plan.TierStatic] {
			t.Fatalf("child tools or static prefix differ: %+v, %v, calls %d", res, err, p.calls)
		}
	})
}
