package openai

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/plan"
)

func TestRequestGolden(t *testing.T) {
	t.Parallel()
	c := newClient(t, Config{})
	golden(t, "request_full.golden.json", body(t, c, fullPlan(t)))
}

func TestRequestIsByteStable(t *testing.T) {
	t.Parallel()
	c := newClient(t, Config{})
	p := fullPlan(t)
	first := body(t, c, p)
	if again := body(t, c, p); !bytes.Equal(first, again) {
		t.Errorf("same plan marshalled twice differs:\n%s\n%s", first, again)
	}
	// A plan rebuilt from the same inputs, sent by another client, must match too.
	if rebuilt := body(t, newClient(t, Config{}), fullPlan(t)); !bytes.Equal(first, rebuilt) {
		t.Errorf("rebuilt plan differs:\n%s\n%s", first, rebuilt)
	}
}

func content(m message) string {
	if m.Content == nil {
		return ""
	}
	return *m.Content
}

func roles(req *request) []string {
	out := make([]string, len(req.Messages))
	for i, m := range req.Messages {
		out[i] = m.Role
	}
	return out
}

func TestTierLayout(t *testing.T) {
	t.Parallel()
	req := decode(t, body(t, newClient(t, Config{}), fullPlan(t)))

	if req.Model != "gpt-test" || !req.Stream || req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
		t.Errorf("model %q, stream %v, stream_options %+v", req.Model, req.Stream, req.StreamOptions)
	}
	if req.MaxCompletionTokens != 0 || req.PromptCacheKey != "" {
		t.Errorf("max_completion_tokens %d, prompt_cache_key %q; want both left out", req.MaxCompletionTokens, req.PromptCacheKey)
	}
	if req.ParallelToolCalls == nil || *req.ParallelToolCalls {
		t.Errorf("parallel_tool_calls = %v, want false", req.ParallelToolCalls)
	}
	if len(req.Tools) != 1 || req.Tools[0].Type != "function" || req.Tools[0].Function.Name != "python" {
		t.Fatalf("tools = %+v", req.Tools)
	}
	if got := req.Tools[0].Function.Parameters.Required; !slices.Equal(got, []string{"code"}) {
		t.Errorf("required = %v", got)
	}

	// The in-turn tool exchange comes after the time note: it is appended,
	// never inserted, so each step's messages are a prefix of the next's.
	want := []string{"system", "user", "assistant", "tool", "assistant", "user", "assistant", "tool"}
	if got := roles(&req); !slices.Equal(got, want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
	// Tiers 0-2 in order, least volatile first.
	sys := content(req.Messages[0])
	static := strings.Index(sys, "You are Ada")
	profile := strings.Index(sys, "What you know about this contact")
	recall := strings.Index(sys, "Earlier conversations")
	if static != 0 || profile <= static || recall <= profile {
		t.Errorf("system tiers out of order: %q", sys)
	}

	call := req.Messages[2]
	if content(call) != "Let me compute that." || len(call.ToolCalls) != 1 {
		t.Fatalf("assistant message = %+v", call)
	}
	tc := call.ToolCalls[0]
	if tc.ID != "call_01" || tc.Type != "function" || tc.Function.Name != "python" ||
		tc.Function.Arguments != `{"code":"print(6 * 7)"}` {
		t.Errorf("tool call = %+v", tc)
	}
	if res := req.Messages[3]; res.ToolCallID != "call_01" || content(res) != "42\n" {
		t.Errorf("tool message = %+v", res)
	}
	// A call with no text has no content field at all.
	if req.Messages[6].Content != nil || len(req.Messages[6].ToolCalls) != 1 {
		t.Errorf("in-turn assistant message = %+v", req.Messages[6])
	}
	if live := content(req.Messages[5]); !strings.HasPrefix(live, "And <b>7 times 8</b>?") ||
		!strings.Contains(live, "\n\n<now>") {
		t.Errorf("live user message = %q, want the question and the time note", live)
	}
	if res := req.Messages[7]; res.ToolCallID != "call_02" || content(res) != "56\n" {
		t.Errorf("in-turn tool message = %+v", res)
	}
}

func TestRequestOptions(t *testing.T) {
	t.Parallel()

	t.Run("plan overrides model and max tokens", func(t *testing.T) {
		t.Parallel()
		p := fullPlan(t)
		p.Model, p.MaxTokens = "served-model", 1234
		req := decode(t, body(t, newClient(t, Config{MaxTokens: 99}), p))
		if req.Model != "served-model" || req.MaxCompletionTokens != 1234 {
			t.Errorf("model %q, max_completion_tokens %d", req.Model, req.MaxCompletionTokens)
		}
	})

	t.Run("config max tokens", func(t *testing.T) {
		t.Parallel()
		req := decode(t, body(t, newClient(t, Config{MaxTokens: 99}), fullPlan(t)))
		if req.MaxCompletionTokens != 99 {
			t.Errorf("max_completion_tokens = %d, want 99", req.MaxCompletionTokens)
		}
	})

	t.Run("prompt cache key", func(t *testing.T) {
		t.Parallel()
		req := decode(t, body(t, newClient(t, Config{PromptCacheKey: true}), fullPlan(t)))
		if req.PromptCacheKey != "contact-42" {
			t.Errorf("prompt_cache_key = %q, want contact-42", req.PromptCacheKey)
		}
	})

	t.Run("no tools", func(t *testing.T) {
		t.Parallel()
		o := fullOpts(t)
		o.tools = nil
		raw := body(t, newClient(t, Config{}), buildPlan(t, o))
		if bytes.Contains(raw, []byte(`"tools"`)) || bytes.Contains(raw, []byte(`"parallel_tool_calls"`)) {
			t.Errorf("request mentions tools: %s", raw)
		}
	})

	t.Run("no model anywhere", func(t *testing.T) {
		t.Parallel()
		c, err := New(Config{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Stream(t.Context(), fullPlan(t)); !errors.Is(err, ErrNoModel) {
			t.Errorf("Stream error = %v, want ErrNoModel", err)
		}
	})
}

func TestMergeSameRole(t *testing.T) {
	t.Parallel()

	t.Run("system tiers become one leading message", func(t *testing.T) {
		t.Parallel()
		for name, edit := range map[string]func(*planOpts){
			"all":        func(*planOpts) {},
			"no profile": func(o *planOpts) { o.profile = "" },
			"no recall":  func(o *planOpts) { o.recall = nil },
		} {
			o := fullOpts(t)
			edit(&o)
			req := decode(t, body(t, newClient(t, Config{}), buildPlan(t, o)))
			n := 0
			for _, m := range req.Messages {
				if m.Role == "system" {
					n++
				}
			}
			if n != 1 || req.Messages[0].Role != "system" {
				t.Errorf("%s: %d system messages, first role %q", name, n, req.Messages[0].Role)
			}
		}
	})

	t.Run("no system text means no system message", func(t *testing.T) {
		t.Parallel()
		p := buildPlan(t, planOpts{events: []core.Event{event(t, 1, core.KindUserMsg, core.MsgBody{Text: "Hi"})}})
		req := decode(t, body(t, newClient(t, Config{}), p))
		if got := roles(&req); !slices.Equal(got, []string{"user"}) {
			t.Errorf("roles = %v, want one user message", got)
		}
	})

	t.Run("consecutive user messages", func(t *testing.T) {
		t.Parallel()
		p := buildPlan(t, planOpts{events: []core.Event{
			event(t, 1, core.KindUserMsg, core.MsgBody{Text: "one"}),
			event(t, 2, core.KindUserMsg, core.MsgBody{Text: "two"}),
		}})
		req := decode(t, body(t, newClient(t, Config{}), p))
		if len(req.Messages) != 1 {
			t.Fatalf("messages = %+v, want one user message", req.Messages)
		}
		if got := content(req.Messages[0]); !strings.HasPrefix(got, "one\n\ntwo\n\n<now>") {
			t.Errorf("merged content = %q", got)
		}
	})

	t.Run("tool results stay separate and errors are marked", func(t *testing.T) {
		t.Parallel()
		// The tool never answered and the contact wrote again: the planner
		// renders a placeholder error result, then the message.
		p := buildPlan(t, planOpts{
			tools: []core.ToolDef{pythonTool()},
			events: []core.Event{
				event(t, 1, core.KindUserMsg, core.MsgBody{Text: "Run it"}),
				event(t, 2, core.KindToolCall, core.ToolCall{ID: "call_01", Name: "python", Code: "while True: pass"}),
				event(t, 3, core.KindUserMsg, core.MsgBody{Text: "Still there?"}),
			},
		})
		req := decode(t, body(t, newClient(t, Config{}), p))
		if got := roles(&req); !slices.Equal(got, []string{"user", "assistant", "tool", "user"}) {
			t.Fatalf("roles = %v", got)
		}
		if got := content(req.Messages[2]); !strings.HasPrefix(got, toolErrorPrefix) {
			t.Errorf("tool content = %q, want the error marker", got)
		}
		if got := content(req.Messages[3]); !strings.HasPrefix(got, "Still there?\n\n<now>") {
			t.Errorf("user content = %q, want the message and the time note merged", got)
		}
	})

	t.Run("assistant runs and blank text", func(t *testing.T) {
		t.Parallel()
		var b builder
		for _, m := range []core.Message{
			{Role: core.RoleUser, Text: "a"},
			{Role: core.RoleAssistant, Text: " \n"},
			{Role: core.RoleUser, Text: "b"},
			{Role: core.RoleAssistant, Text: "first"},
			{Role: core.RoleAssistant, ToolCall: &core.ToolCall{ID: "call_1", Name: "python", Code: "1"}},
			{Role: core.RoleTool, ToolResult: &core.ToolResult{ID: "call_1"}},
		} {
			if err := b.add(&m); err != nil {
				t.Fatal(err)
			}
		}
		if len(b.msgs) != 3 {
			t.Fatalf("messages = %+v, want user, assistant, tool", b.msgs)
		}
		if content(b.msgs[0]) != "a\n\nb" {
			t.Errorf("user content = %q", content(b.msgs[0]))
		}
		if content(b.msgs[1]) != "first" || len(b.msgs[1].ToolCalls) != 1 {
			t.Errorf("assistant message = %+v", b.msgs[1])
		}
		// An empty result still needs its content field.
		if b.msgs[2].Content == nil || *b.msgs[2].Content != "" {
			t.Errorf("tool message = %+v", b.msgs[2])
		}
	})
}

func TestTailGrowthKeepsPrefix(t *testing.T) {
	t.Parallel()
	// There are no breakpoints here: the cache is whatever prefix of the body
	// repeats. The next turn must reproduce everything up to the end of the
	// previous tail byte for byte.
	c := newClient(t, Config{})
	o := fullOpts(t)
	prev := buildPlan(t, o)

	o.events = append(o.events,
		event(t, 6, core.KindAgentMsg, core.MsgBody{Text: "56."}),
		event(t, 7, core.KindUserMsg, core.MsgBody{Text: "Thanks"}),
	)
	in := plan.Input{Contact: 42, Agent: &core.AgentConfig{System: o.system, Tools: o.tools}, Profile: o.profile, Tail: o.events}
	next, err := plan.Build(prev, in, o.recall, fixedNow+60_000, plan.DefaultParams())
	if err != nil {
		t.Fatal(err)
	}

	// The tail of the earlier plan ends with the reply "It is 42.".
	const tailEnd = `"content":"It is 42."}`
	a, b := body(t, c, prev), body(t, c, next)
	cut := bytes.Index(a, []byte(tailEnd))
	if cut < 0 {
		t.Fatalf("tail end not found in %s", a)
	}
	cut += len(tailEnd)
	if len(b) < cut || !bytes.Equal(a[:cut], b[:cut]) {
		t.Errorf("shared prefix changed between turns:\n%s\n%s", a[:cut], b[:min(cut, len(b))])
	}
}

func TestToolStepsAppend(t *testing.T) {
	t.Parallel()
	// Within a turn each request's messages must be a prefix of the next
	// one's, so the server's prefix cache covers everything already sent.
	// Replay is another provider's wire form; this adapter ignores it.
	c := newClient(t, Config{})
	p := buildPlan(t, fullOpts(t))
	messages := func() []byte {
		t.Helper()
		raw := body(t, c, p)
		msgs, _, ok := bytes.Cut(raw, []byte(`],"tools":`))
		if !ok {
			t.Fatalf("no end of messages in %s", raw)
		}
		return msgs
	}

	prev := messages()
	for step := range 3 {
		p.AppendTail(
			core.ToolCall{ID: "call_9", Name: "python", Code: "1", Text: "Trying.", Replay: []byte(`[{"type":"thinking"}]`)},
			core.ToolResult{ID: "call_9", Output: "1"},
		)
		next := messages()
		if !bytes.HasPrefix(next, prev) {
			t.Fatalf("step %d rewrote earlier messages:\n%s\n%s", step+1, prev, next)
		}
		if bytes.Contains(next, []byte("thinking")) {
			t.Fatalf("step %d sent another provider's replay blocks: %s", step+1, next)
		}
		prev = next
	}
}
