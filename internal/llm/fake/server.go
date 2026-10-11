package fake

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/plan"
)

// Handler serves the fake model over the OpenAI Chat Completions wire
// format, streaming or not. It is what cmd/fakellm runs and what integration
// tests point the OpenAI adapter at, so load tests exercise the real HTTP
// path without a provider.
//
// The wire format has no breakpoints, so the server always caches by prefix.
func Handler(opt Options) http.Handler {
	opt.Mode = CachePrefix
	m := NewModel(opt)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", m.serveChat)
	mux.HandleFunc("POST /chat/completions", m.serveChat)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

type chatRequest struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Messages []chatMessage `json:"messages"`
	Tools    []chatTool    `json:"tools"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    chatContent    `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// chatContent accepts both forms the API allows: a string, or a list of
// typed parts of which only the text parts matter here.
type chatContent string

func (c *chatContent) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*c = chatContent(s)
		return nil
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(b, &parts); err != nil {
		return fmt.Errorf("content is neither a string nor a list of parts: %w", err)
	}
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(p.Text)
	}
	*c = chatContent(sb.String())
	return nil
}

type chatTool struct {
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type chatToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatChunk struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitempty"`
}

type chatChoice struct {
	Index        int        `json:"index"`
	Delta        *chatDelta `json:"delta,omitempty"`
	Message      *chatDelta `json:"message,omitempty"`
	FinishReason *string    `json:"finish_reason"`
}

type chatDelta struct {
	Role      string         `json:"role,omitempty"`
	Content   string         `json:"content,omitempty"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	Details          struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (m *Model) serveChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":{"message":"bad request body"}}`, http.StatusBadRequest)
		return
	}
	msgs, tools := fromWire(&req)

	resp := m.Respond(msgs, tools, wireMarks(msgs), time.Now())
	if !req.Stream {
		stop := "stop"
		writeJSON(w, chatChunk{
			ID: "chatcmpl-fake", Object: "chat.completion", Model: req.Model,
			Choices: []chatChoice{{Message: &chatDelta{Role: "assistant", Content: resp.Text}, FinishReason: &stop}},
			Usage:   &chatUsage{PromptTokens: resp.Usage.Input, CompletionTokens: resp.Usage.Output},
		})
		return
	}
	stream := NewStream(r.Context(), resp, m.opt.PerChunk)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	send := func(c chatChunk) {
		c.ID, c.Object, c.Model = "chatcmpl-fake", "chat.completion.chunk", req.Model
		b, err := json.Marshal(c)
		if err != nil {
			return
		}
		// A write error means the client left; the stream notices through
		// the request context.
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	send(chatChunk{Choices: []chatChoice{{Delta: &chatDelta{Role: "assistant"}}}})
	started := 0
	for ev := range stream.Events() {
		switch ev.Kind {
		case llm.TextDelta:
			send(chatChunk{Choices: []chatChoice{{Delta: &chatDelta{Content: ev.Text}}}})
		case llm.ToolStart:
			call := chatToolCall{Index: started, ID: resp.Tools[started].ID, Type: "function"}
			call.Function.Name = ev.Tool
			send(chatChunk{Choices: []chatChoice{{Delta: &chatDelta{ToolCalls: []chatToolCall{call}}}}})
			started++
		case llm.ReasoningDelta:
		}
	}
	if _, err := stream.Result(); err != nil {
		return // the client went away mid-stream
	}
	reason := "stop"
	for i := range resp.Tools {
		args := []byte(resp.Tools[i].Args)
		if len(args) == 0 {
			var err error
			args, err = json.Marshal(struct {
				Code string `json:"code"`
			}{resp.Tools[i].Code})
			if err != nil {
				return
			}
		}
		// Arguments arrive in pieces, as they do from a real provider.
		half := len(args) / 2
		for _, piece := range []string{string(args[:half]), string(args[half:])} {
			call := chatToolCall{Index: i}
			call.Function.Arguments = piece
			send(chatChunk{Choices: []chatChoice{{Delta: &chatDelta{ToolCalls: []chatToolCall{call}}}}})
		}
		reason = "tool_calls"
	}
	send(chatChunk{Choices: []chatChoice{{Delta: &chatDelta{}, FinishReason: &reason}}})

	usage := &chatUsage{
		PromptTokens:     resp.Usage.Input,
		CompletionTokens: resp.Usage.Output,
		TotalTokens:      resp.Usage.Input + resp.Usage.Output,
	}
	usage.Details.CachedTokens = resp.Usage.Cached
	send(chatChunk{Choices: []chatChoice{}, Usage: usage})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func fromWire(req *chatRequest) ([]core.Message, []core.ToolDef) {
	msgs := make([]core.Message, 0, len(req.Messages))
	for _, wm := range req.Messages {
		m := core.Message{Text: string(wm.Content)}
		switch wm.Role {
		case "system", "developer":
			m.Role = core.RoleSystem
		case "assistant":
			m.Role = core.RoleAssistant
			if len(wm.ToolCalls) > 0 {
				// Only the last thing in a prompt decides the reply, so the
				// first call is enough to stand for the step.
				tc := wm.ToolCalls[0]
				var args struct {
					Code string `json:"code"`
				}
				// Unparseable arguments just leave the code empty.
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
				m.ToolCall = &core.ToolCall{ID: tc.ID, Name: tc.Function.Name, Code: args.Code}
			}
		case "tool":
			m.Role = core.RoleTool
			m.ToolResult = &core.ToolResult{ID: wm.ToolCallID, Output: string(wm.Content)}
			m.Text = ""
		default:
			m.Role = core.RoleUser
		}
		msgs = append(msgs, m)
	}
	tools := make([]core.ToolDef, len(req.Tools))
	for i, t := range req.Tools {
		tools[i] = core.ToolDef{Name: t.Function.Name, Description: t.Function.Description}
		// A tool whose parameters are not the default single "code" string
		// has its own schema, and takes its arguments raw.
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(t.Function.Parameters, &schema) == nil {
			if _, isCode := schema.Properties["code"]; !isCode && len(schema.Properties) > 0 {
				tools[i].Input = t.Function.Parameters
			}
		}
	}
	return msgs, tools
}

// wireMarks describes a wire prompt prefix by prefix, the way
// plan.PrefixMarks does for a plan.
func wireMarks(msgs []core.Message) []plan.Mark {
	marks := make([]plan.Mark, len(msgs))
	var buf [16]byte
	h, tokens := uint64(0), 0
	for i := range msgs {
		mh := xxhash.Sum64(plan.Canonical(&msgs[i]))
		binary.LittleEndian.PutUint64(buf[:8], h)
		binary.LittleEndian.PutUint64(buf[8:], mh)
		h = xxhash.Sum64(buf[:])
		tokens += plan.EstimateTokens(&msgs[i])
		marks[i] = plan.Mark{Hash: h, Tokens: tokens}
	}
	return marks
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	// A write error means the client left; there is no one to tell.
	_ = json.NewEncoder(w).Encode(v)
}
