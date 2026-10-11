package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/plan"
)

// The wire structs below are marshalled in field order, never from a map, so
// a plan always produces the same request bytes. Reordering or renaming a
// field changes what every warm cache was keyed on; the golden test pins it.

type request struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Stream    bool   `json:"stream,omitempty"`
	// Fallbacks is "default" or absent: the API picks the model that re-runs
	// a request a safety classifier declined.
	Fallbacks    string        `json:"fallbacks,omitempty"`
	OutputConfig *outputConfig `json:"output_config,omitempty"`
	Tools        []tool        `json:"tools,omitempty"`
	ToolChoice   *toolChoice   `json:"tool_choice,omitempty"`
	System       []block       `json:"system,omitempty"`
	// Messages stays last so that, within a turn, each request is a byte
	// prefix of the next one up to the closing brackets.
	Messages []message `json:"messages"`
}

type outputConfig struct {
	Effort string `json:"effort"`
}

type tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Code arguments are long. Without eager streaming the API buffers the
	// whole argument before sending any of it, and the stream sits silent.
	EagerInputStreaming bool          `json:"eager_input_streaming"`
	InputSchema         inputSchema   `json:"input_schema"`
	CacheControl        *cacheControl `json:"cache_control,omitempty"`
}

type inputSchema struct {
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

type toolChoice struct {
	Type                   string `json:"type"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
}

type message struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

// block is a text, tool_use or tool_result content block; Type says which
// fields are meaningful. With raw set it is any block at all, replayed as
// the API produced it.
type block struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	ID           string        `json:"id,omitempty"`
	Name         string        `json:"name,omitempty"`
	Input        *toolInput    `json:"input,omitempty"`
	ToolUseID    string        `json:"tool_use_id,omitempty"`
	Content      string        `json:"content,omitempty"`
	IsError      bool          `json:"is_error,omitempty"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`

	// raw, when set, is the block exactly as the API produced it. It is sent
	// as is and every other field but Type is ignored: reasoning blocks are
	// signed, and a changed block is rejected.
	raw json.RawMessage
}

// plainBlock is block without its MarshalJSON, so the method can fall back
// to the ordinary struct encoding.
type plainBlock block

func (b block) MarshalJSON() ([]byte, error) {
	if b.raw != nil {
		return b.raw, nil
	}
	// Not json.Marshal: it would escape "<" and ">" in message text.
	return encode(plainBlock(b))
}

type toolInput struct {
	Code string `json:"code"`
}

type cacheControl struct {
	Type string `json:"type"`
}

const (
	roleUser      = "user"
	roleAssistant = "assistant"

	blockText       = "text"
	blockToolUse    = "tool_use"
	blockToolResult = "tool_result"
)

func ephemeral() *cacheControl { return &cacheControl{Type: "ephemeral"} }

// buildRequest lays a plan out as a Messages request: tools, then tiers 0-2
// as system blocks, then tiers 3-4 as messages, which is also the order the
// API renders them in. The last block of each non-empty tier 0-3 carries a
// cache breakpoint; that is at most four, the API's limit. Tier 4 changes
// every turn and never gets one.
func (c *Client) buildRequest(p *plan.PromptPlan) *request {
	req := &request{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		Stream:    true,
	}
	if p.Model != "" {
		req.Model = p.Model
	}
	if p.MaxTokens > 0 {
		req.MaxTokens = p.MaxTokens
	}
	if c.fallback && takesFallback(req.Model) {
		req.Fallbacks = "default"
	}
	if c.effort != "" {
		req.OutputConfig = &outputConfig{Effort: c.effort}
	}

	if len(p.Tools) > 0 {
		req.Tools = make([]tool, len(p.Tools))
		for i := range p.Tools {
			req.Tools[i] = toolFrom(&p.Tools[i])
		}
		// The turn loop runs one tool per step.
		req.ToolChoice = &toolChoice{Type: "auto", DisableParallelToolUse: true}
	}

	for t := plan.TierStatic; t <= plan.TierRecall; t++ {
		before := len(req.System)
		for i := range p.Tiers[t].Msgs {
			if text := p.Tiers[t].Msgs[i].Text; hasText(text) {
				req.System = append(req.System, block{Type: blockText, Text: text})
			}
		}
		switch {
		case len(req.System) > before:
			req.System[len(req.System)-1].CacheControl = ephemeral()
		case t == plan.TierStatic && len(req.Tools) > 0:
			// Tools render ahead of the system prompt. With no system text
			// the static tier ends at the last tool.
			req.Tools[len(req.Tools)-1].CacheControl = ephemeral()
		}
	}

	var b builder
	for i := range p.Tiers[plan.TierTail].Msgs {
		b.add(&p.Tiers[plan.TierTail].Msgs[i])
	}
	if last := b.last(); last != nil {
		last.CacheControl = ephemeral()
	}
	b.endTier()
	for i := range p.Tiers[plan.TierLive].Msgs {
		b.add(&p.Tiers[plan.TierLive].Msgs[i])
	}
	req.Messages = b.msgs
	if req.Messages == nil {
		req.Messages = []message{}
	}
	return req
}

// takesFallback reports whether model accepts fallbacks: "default". Other
// models either have no fallback or are not documented to take the field.
func takesFallback(model string) bool {
	switch model {
	case "claude-opus-5-5", "claude-opus-5", "claude-fable-5-1", "claude-sonnet-5-5":
		return true
	default:
		return false
	}
}

func toolFrom(d *core.ToolDef) tool {
	return tool{
		Name:                d.Name,
		Description:         d.Description,
		EagerInputStreaming: true,
		InputSchema: inputSchema{
			Type: "object",
			Properties: properties{Code: property{
				Type:        "string",
				Description: "The code to run.",
			}},
			Required: []string{"code"},
		},
	}
}

// hasText reports whether s can be sent as a text block. The API rejects
// blocks that are empty or only whitespace.
func hasText(s string) bool { return strings.TrimSpace(s) != "" }

// builder turns provider-neutral messages into wire messages, merging
// neighbours that share a wire role: the API wants user and assistant turns
// to alternate, and a tool result is a user turn.
type builder struct {
	msgs []message
	// seg is where the current tier's blocks begin in the last message.
	seg int
}

func (b *builder) add(m *core.Message) {
	switch m.Role {
	case core.RoleUser, core.RoleSystem:
		if hasText(m.Text) {
			b.push(roleUser, block{Type: blockText, Text: m.Text})
		}
	case core.RoleAssistant:
		if c := m.ToolCall; c != nil && b.replay(c.Replay) {
			return
		}
		if hasText(m.Text) {
			b.push(roleAssistant, block{Type: blockText, Text: m.Text})
		}
		if c := m.ToolCall; c != nil {
			b.push(roleAssistant, block{
				Type:  blockToolUse,
				ID:    c.ID,
				Name:  c.Name,
				Input: &toolInput{Code: c.Code},
			})
		}
	case core.RoleTool:
		if r := m.ToolResult; r != nil {
			b.push(roleUser, block{
				Type:      blockToolResult,
				ToolUseID: r.ID,
				Content:   r.Output,
				IsError:   r.IsError,
			})
		}
		if hasText(m.Text) {
			b.push(roleUser, block{Type: blockText, Text: m.Text})
		}
	}
}

// replay sends an assistant turn back as the content array captured while it
// streamed, instead of rebuilding it from text and code. Only a turn from the
// current tool loop carries one; history rendered from the log does not, and
// neither does a call made through another provider. It reports false, and
// adds nothing, if there is no usable array.
func (b *builder) replay(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil || len(elems) == 0 {
		return false
	}
	blocks := make([]block, len(elems))
	for i, elem := range elems {
		var head blockHead
		if err := json.Unmarshal(elem, &head); err != nil || head.Type == "" {
			return false
		}
		blocks[i] = block{Type: head.Type, raw: elem}
	}
	for _, bl := range blocks {
		b.push(roleAssistant, bl)
	}
	return true
}

type blockHead struct {
	Type string `json:"type"`
}

func (b *builder) push(role string, bl block) {
	n := len(b.msgs)
	if n == 0 || b.msgs[n-1].Role != role {
		b.msgs = append(b.msgs, message{Role: role})
		b.seg = 0
		n++
	}
	m := &b.msgs[n-1]
	at := len(m.Content)
	if bl.Type == blockToolResult {
		// Results go ahead of text in a user message. Only blocks of the
		// current tier move: reordering across a tier boundary would change
		// bytes that an earlier breakpoint already covers.
		for at > b.seg && m.Content[at-1].Type == blockText {
			at--
		}
	}
	m.Content = slices.Insert(m.Content, at, bl)
}

// last is the final block written so far, or nil.
func (b *builder) last() *block {
	if len(b.msgs) == 0 {
		return nil
	}
	m := &b.msgs[len(b.msgs)-1]
	return &m.Content[len(m.Content)-1]
}

func (b *builder) endTier() {
	if n := len(b.msgs); n > 0 {
		b.seg = len(b.msgs[n-1].Content)
	}
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
		return nil, fmt.Errorf("anthropic: encode: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func unmarshal(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("anthropic: decode: %w", err)
	}
	return nil
}
