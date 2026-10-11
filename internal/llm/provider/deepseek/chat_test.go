package deepseek

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/conform"
)

// The chat fixtures are written by hand from DeepSeek's documented Chat
// Completions stream: reasoning arrives as delta.reasoning_content, and
// usage reports cache hits as prompt_cache_hit_tokens, counted inside
// prompt_tokens.
func TestChatConformance(t *testing.T) {
	t.Parallel()
	fx := conform.Fixture{
		Reply: conform.Response{
			Body: fixture(t, "chat_reply.sse"), Text: "Six times seven is 42.",
			Input: 1060, Cached: 1024, Output: 27,
		},
		Tool: conform.Response{
			Body: fixture(t, "chat_tool.sse"), Text: "Let me check.", Code: "print(6 * 7)",
			Input: 2100, Cached: 2048, Output: 64, Reasoning: true,
		},
	}
	for _, model := range []string{"", "deepseek-v4-pro"} {
		t.Run("model="+model, func(t *testing.T) {
			t.Parallel()
			conform.Run(t, func(t *testing.T, base string) llm.Provider {
				t.Helper()
				return newProvider(t, Config{Wire: WireChat, BaseURL: base})
			}, model, fx)
		})
	}
}

func TestChatRequestShape(t *testing.T) {
	t.Parallel()
	p := newProvider(t, Config{Wire: WireChat})
	r := route(t, p, "deepseek-v4-pro")
	if r.Wire != llm.WireChat || r.Caps.Replay != llm.ReplayAllTurns || r.Caps.Cache != llm.CacheAutoPrefix || r.Caps.SingleToolCall {
		t.Fatalf("route = %+v", r)
	}

	f := freeze(t, p, r, testPlan(t), llm.Options{Effort: llm.EffortMax})
	if f.URL != "https://api.deepseek.com/chat/completions" {
		t.Errorf("URL = %q", f.URL)
	}
	if f.Header.Get("Authorization") != "Bearer test-key" || f.Header.Get("X-Api-Key") != "" {
		t.Errorf("auth headers = %v", f.Header)
	}
	body := string(f.Body)
	for _, want := range []string{
		`"model":"deepseek-v4-pro"`,
		`"max_tokens":8192`, // a modest default under the model's 384K ceiling
		`"thinking":{"type":"enabled"}`,
		`"reasoning_effort":"max"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %s:\n%s", want, body)
		}
	}
	// Nothing this endpoint does not document: no breakpoints, no pinning
	// key, no parallel-call switch.
	for _, absent := range []string{"cache_control", "session", "parallel_tool_calls", "tool_choice"} {
		if strings.Contains(body, absent) {
			t.Errorf("body mentions %s:\n%s", absent, body)
		}
	}

	off := freeze(t, p, r, testPlan(t), llm.Options{Effort: llm.EffortNone})
	if !bytes.Contains(off.Body, []byte(`"thinking":{"type":"disabled"}`)) || bytes.Contains(off.Body, []byte("reasoning_effort")) {
		t.Errorf("reasoning off: %s", off.Body)
	}
	plain := freeze(t, p, r, testPlan(t), llm.Options{})
	if bytes.Contains(plain.Body, []byte("thinking")) || bytes.Contains(plain.Body, []byte("reasoning_effort")) {
		t.Errorf("no effort asked for, yet the request sets one: %s", plain.Body)
	}
}

// TestChatReplaysReasoningContent is the rule this endpoint documents: with
// tools set, reasoning_content goes back on every later request.
func TestChatReplaysReasoningContent(t *testing.T) {
	t.Parallel()
	p := newProvider(t, Config{Wire: WireChat})
	r := route(t, p, "")
	// The payload as the chat codec stores it.
	data, err := json.Marshal(map[string]any{"text": "The user wants a product. I should run it."})
	if err != nil {
		t.Fatal(err)
	}
	pl := testPlan(t)
	pl.AppendTail(
		core.ToolCall{
			ID: "call_0_a1b2", Name: "python", Code: "print(6 * 7)", Text: "Let me check.",
			Reasoning: &core.Reasoning{Route: r.ID(), Data: data},
		},
		core.ToolResult{ID: "call_0_a1b2", Output: "42"},
	)
	f := freeze(t, p, r, pl, llm.Options{})
	if !bytes.Contains(f.Body, []byte(`"reasoning_content":"The user wants a product. I should run it."`)) {
		t.Errorf("reasoning_content was not sent back:\n%s", f.Body)
	}
}
