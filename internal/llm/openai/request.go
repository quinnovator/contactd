package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/plan"
)

// The wire structs below are marshalled in field order, never from a map, so
// a plan always produces the same request bytes. Prefix caches match on
// those bytes (or on the tokens they render to); the golden test pins them.

type request struct {
	Model             string    `json:"model"`
	Messages          []message `json:"messages"`
	Tools             []tool    `json:"tools,omitempty"`
	ParallelToolCalls *bool     `json:"parallel_tool_calls,omitempty"`
	// max_tokens is deprecated and rejected by OpenAI's newer models; vLLM
	// and SGLang accept this name too.
	MaxCompletionTokens int            `json:"max_completion_tokens,omitempty"`
	Stream              bool           `json:"stream,omitempty"`
	StreamOptions       *streamOptions `json:"stream_options,omitempty"`
	PromptCacheKey      string         `json:"prompt_cache_key,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// message is a system, user, assistant or tool message. Content is a pointer
// because a tool message must carry it even when empty, while an assistant
// message that only calls a tool leaves it out.
type message struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // a JSON document, as a string
}

type toolArgs struct {
	Code string `json:"code"`
}

type tool struct {
	Type     string   `json:"type"`
	Function function `json:"function"`
}

type function struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  parameters `json:"parameters"`
}

type parameters struct {
	Type                 string     `json:"type"`
	Properties           properties `json:"properties"`
	Required             []string   `json:"required"`
	AdditionalProperties bool       `json:"additionalProperties"`
}

type properties struct {
	Code property `json:"code"`
}

type property struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

const (
	roleSystem    = "system"
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"

	typeFunction = "function"

	// The API has no error flag on a tool message, so a failed run is marked
	// in its text.
	toolErrorPrefix = "[tool error]\n"
	// mergeSep joins the texts of neighbouring messages with the same role.
	mergeSep = "\n\n"
)

// buildRequest lays a plan out as a Chat Completions request: tiers 0-2 as
// the system message, tiers 3-4 as the conversation. The order is the
// plan's, least volatile first, so the longest possible prefix repeats from
// one request to the next.
func (c *Client) buildRequest(p *plan.PromptPlan) (*request, error) {
	req := &request{
		Model:               c.model,
		MaxCompletionTokens: c.maxTokens,
		Stream:              true,
		StreamOptions:       &streamOptions{IncludeUsage: true},
	}
	if p.Model != "" {
		req.Model = p.Model
	}
	if req.Model == "" {
		return nil, ErrNoModel
	}
	if p.MaxTokens > 0 {
		req.MaxCompletionTokens = p.MaxTokens
	}
	if c.cacheKey {
		req.PromptCacheKey = "contact-" + p.Contact.String()
	}

	if len(p.Tools) > 0 {
		req.Tools = make([]tool, len(p.Tools))
		for i := range p.Tools {
			req.Tools[i] = toolFrom(&p.Tools[i])
		}
		// The turn loop runs one tool per step.
		parallel := false
		req.ParallelToolCalls = &parallel
	}

	var b builder
	for t := plan.TierStatic; t <= plan.TierRecall; t++ {
		for i := range p.Tiers[t].Msgs {
			b.text(roleSystem, p.Tiers[t].Msgs[i].Text)
		}
	}
	for t := plan.TierTail; t <= plan.TierLive; t++ {
		for i := range p.Tiers[t].Msgs {
			if err := b.add(&p.Tiers[t].Msgs[i]); err != nil {
				return nil, err
			}
		}
	}
	req.Messages = b.msgs
	if req.Messages == nil {
		req.Messages = []message{}
	}
	return req, nil
}

func toolFrom(d *core.ToolDef) tool {
	return tool{
		Type: typeFunction,
		Function: function{
			Name:        d.Name,
			Description: d.Description,
			Parameters: parameters{
				Type: "object",
				Properties: properties{Code: property{
					Type:        "string",
					Description: "The code to run.",
				}},
				Required: []string{"code"},
			},
		},
	}
}

// builder turns provider-neutral messages into wire messages. Neighbours
// with the same role are merged into one: many self-hosted chat templates
// accept a single leading system message and strictly alternating turns.
type builder struct {
	msgs []message
}

func (b *builder) add(m *core.Message) error {
	switch m.Role {
	case core.RoleSystem:
		b.text(roleSystem, m.Text)
	case core.RoleUser:
		b.text(roleUser, m.Text)
	case core.RoleAssistant:
		b.text(roleAssistant, m.Text)
		if tc := m.ToolCall; tc != nil {
			args, err := encode(toolArgs{Code: tc.Code})
			if err != nil {
				return err
			}
			last := b.open(roleAssistant)
			last.ToolCalls = append(last.ToolCalls, toolCall{
				ID:       tc.ID,
				Type:     typeFunction,
				Function: functionCall{Name: tc.Name, Arguments: string(args)},
			})
		}
	case core.RoleTool:
		if r := m.ToolResult; r != nil {
			content := r.Output
			if r.IsError {
				content = toolErrorPrefix + content
			}
			// Each result answers one call by ID, so results never merge.
			b.msgs = append(b.msgs, message{Role: roleTool, Content: &content, ToolCallID: r.ID})
		}
		b.text(roleUser, m.Text)
	}
	return nil
}

// text appends text to the last message if it has the same role, and starts
// a new message otherwise. Blank text is dropped.
func (b *builder) text(role, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	m := b.open(role)
	if m.Content == nil {
		m.Content = &text
		return
	}
	merged := *m.Content + mergeSep + text
	m.Content = &merged
}

// open returns the last message if it has the given role, or a new one.
func (b *builder) open(role string) *message {
	if n := len(b.msgs); n == 0 || b.msgs[n-1].Role != role {
		b.msgs = append(b.msgs, message{Role: role})
	}
	return &b.msgs[len(b.msgs)-1]
}

// marshal encodes req without HTML escaping, so "<now>" is sent as written
// rather than as <now>.
func marshal(req *request) ([]byte, error) {
	return encode(req)
}

func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func unmarshal(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("openai: decode: %w", err)
	}
	return nil
}
