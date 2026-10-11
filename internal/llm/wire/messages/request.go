package messages

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/plan"
)

// The wire structs below are marshalled in field order, never from a map, so
// a plan always produces the same request bytes. Reordering or renaming a
// field changes what every warm cache was keyed on; the golden test pins it.

// requestHead and requestRest are the two halves of a request body. A
// dialect's extra fields are written between them.
type requestHead struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Stream    bool   `json:"stream"`
}

type requestRest struct {
	Thinking     json.RawMessage `json:"thinking,omitempty"`
	OutputConfig *outputConfig   `json:"output_config,omitempty"`
	Tools        []tool          `json:"tools,omitempty"`
	ToolChoice   *toolChoice     `json:"tool_choice,omitempty"`
	System       []block         `json:"system,omitempty"`
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
	EagerInputStreaming bool            `json:"eager_input_streaming,omitempty"`
	InputSchema         json.RawMessage `json:"input_schema"`
	CacheControl        *cacheControl   `json:"cache_control,omitempty"`
}

// codeSchema is the schema of a tool that declares none: one required
// string, "code". The reply decoder recognises such a tool by these bytes,
// so they are also what tells a code call from a call with raw arguments.
const codeSchema = `{"type":"object","properties":{"code":{"type":"string","description":"The code to run."}},` +
	`"required":["code"],"additionalProperties":false}`

type toolChoice struct {
	Type                   string `json:"type"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
}

type message struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

// block is a text, tool_use or tool_result content block; Type says which
// fields are meaningful. With raw set it is any block at all, sent back as
// the upstream produced it.
type block struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      string          `json:"content,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`

	// raw, when set, is the block exactly as the upstream produced it. It is
	// sent as is and every other field but Type is ignored: reasoning blocks
	// are signed, and a changed block is rejected.
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

type codeInput struct {
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

	// toolErrorMarker opens the text of a failed tool result on a route that
	// has no error flag, so the failure is still said somewhere.
	toolErrorMarker = "[tool error]\n"
)

func ephemeral() *cacheControl { return &cacheControl{Type: "ephemeral"} }

// render lays a plan out as a Messages request: tools, then tiers 0-2 as
// system blocks, then tiers 3-4 as messages, which is also the order the
// API renders them in. On a route that caches at breakpoints the last block
// of each non-empty tier 0-3 carries one; that is at most four, the API's
// limit. Tier 4 changes every turn and never gets one.
func render(d *Dialect, r llm.Route, p *plan.PromptPlan, maxTokens int, effort EffortFields) ([]byte, error) {
	marked := r.Caps.Cache == llm.CacheBreakpoints
	rest := requestRest{Thinking: effort.Thinking}
	if effort.OutputEffort != "" {
		rest.OutputConfig = &outputConfig{Effort: effort.OutputEffort}
	}

	b := builder{route: r.ID(), errorFlag: r.Caps.ToolErrorFlag, ownSchema: map[string]bool{}}
	if len(p.Tools) > 0 {
		rest.Tools = make([]tool, len(p.Tools))
		for i := range p.Tools {
			def := &p.Tools[i]
			schema := json.RawMessage(codeSchema)
			if len(def.Input) > 0 {
				if !json.Valid(def.Input) {
					return nil, fmt.Errorf("messages: tool %q: input schema is not valid JSON", def.Name)
				}
				schema, b.ownSchema[def.Name] = def.Input, true
			}
			rest.Tools[i] = tool{
				Name:                def.Name,
				Description:         def.Description,
				EagerInputStreaming: d.EagerToolInput,
				InputSchema:         schema,
			}
		}
		if r.Caps.SingleToolCall {
			// The default choice, auto, with the one thing that differs.
			rest.ToolChoice = &toolChoice{Type: "auto", DisableParallelToolUse: true}
		}
	}

	for t := plan.TierStatic; t <= plan.TierRecall; t++ {
		before := len(rest.System)
		for i := range p.Tiers[t].Msgs {
			if text := p.Tiers[t].Msgs[i].Text; hasText(text) {
				rest.System = append(rest.System, block{Type: blockText, Text: text})
			}
		}
		switch {
		case !marked:
			// The upstream caches by prefix on its own, or not at all.
		case len(rest.System) > before:
			rest.System[len(rest.System)-1].CacheControl = ephemeral()
		case t == plan.TierStatic && len(rest.Tools) > 0:
			// Tools render ahead of the system prompt. With no system text
			// the static tier ends at the last tool.
			rest.Tools[len(rest.Tools)-1].CacheControl = ephemeral()
		}
	}

	for i := range p.Tiers[plan.TierTail].Msgs {
		if err := b.add(&p.Tiers[plan.TierTail].Msgs[i]); err != nil {
			return nil, err
		}
	}
	// A reasoning block is sent as it was received and cannot carry a
	// breakpoint; a tail that ends on one goes without.
	if last := b.last(); marked && last != nil && last.raw == nil {
		last.CacheControl = ephemeral()
	}
	b.endTier()
	for i := range p.Tiers[plan.TierLive].Msgs {
		if err := b.add(&p.Tiers[plan.TierLive].Msgs[i]); err != nil {
			return nil, err
		}
	}
	rest.Messages = b.msgs
	if rest.Messages == nil {
		rest.Messages = []message{}
	}

	out, err := encode(requestHead{Model: r.Model, MaxTokens: maxTokens, Stream: true})
	if err != nil {
		return nil, err
	}
	out = out[:len(out)-1] // reopen the object
	for _, f := range d.Extra {
		name, err := encode(f.Name)
		if err != nil {
			return nil, err
		}
		val, err := compact(f.Value)
		if err != nil {
			return nil, fmt.Errorf("extra field %q: %w", f.Name, err)
		}
		out = append(out, ',')
		out = append(out, name...)
		out = append(out, ':')
		out = append(out, val...)
	}
	tail, err := encode(rest)
	if err != nil {
		return nil, err
	}
	out = append(out, ',')
	return append(out, tail[1:]...), nil
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

	// route is the route the request is for. Reasoning tagged with any other
	// is left out.
	route string
	// errorFlag says a failed tool result may carry is_error.
	errorFlag bool
	// ownSchema names the tools that declare an input schema.
	ownSchema map[string]bool
	// open says where the sibling calls of the step rendered last belong.
	open openSlots
}

func (b *builder) add(m *core.Message) error {
	switch m.Role {
	case core.RoleUser, core.RoleSystem:
		if hasText(m.Text) {
			b.push(roleUser, block{Type: blockText, Text: m.Text})
		}
	case core.RoleAssistant:
		return b.addAssistant(m)
	case core.RoleTool:
		if r := m.ToolResult; r != nil {
			bl := block{Type: blockToolResult, ToolUseID: r.ID, Content: r.Output, IsError: r.IsError}
			if r.IsError && !b.errorFlag {
				// The upstream drops the flag, so the text has to say it.
				bl.Content, bl.IsError = toolErrorMarker+r.Output, false
			}
			b.push(roleUser, bl)
		}
		if hasText(m.Text) {
			b.push(roleUser, block{Type: blockText, Text: m.Text})
		}
	}
	return nil
}

// addAssistant renders one assistant message. With reasoning recorded for
// this route the turn goes back in the order the upstream sent it;
// otherwise it is its text, then its tool call. A call that is a later
// sibling of the one before it joins that call's message instead.
func (b *builder) addAssistant(m *core.Message) error {
	var use *block
	if c := m.ToolCall; c != nil {
		input, err := b.input(c)
		if err != nil {
			return err
		}
		use = &block{Type: blockToolUse, ID: c.ID, Name: c.Name, Input: input}
		if c.Sibling > 0 && b.joinStep(m.Text, *use) {
			return nil
		}
	}
	b.open = openSlots{}
	if turn := recordedTurn(m.Reasoning, b.route); turn != nil {
		b.replay(turn, m.Text, use)
		return nil
	}
	b.pushText(roleAssistant, m.Text)
	if use != nil {
		b.push(roleAssistant, *use)
	}
	return nil
}

// replay renders an assistant turn in the order its reasoning payload
// recorded: every reasoning block unchanged and where it was, the text cut
// back into the blocks it was streamed as, and the tool call in the first
// tool slot. The slots after it are left open for the step's sibling calls,
// which arrive as later messages; one that no call ever fills (the step was
// cut short) simply stays out.
//
// What was said and run comes from the message, never from the payload. If
// the text is not the length that was recorded, it is not what was streamed
// and cannot be cut the same way: it then goes out as one block where the
// first text block stood, which keeps it behind the reasoning that led to
// it. A turn that recorded no text or no call, yet has one now, gets it at
// the nearest such place: text ahead of the calls, a call at the end.
func (b *builder) replay(turn []slot, text string, use *block) {
	pieces, exact := splitText(turn, text)
	whole := -1 // where an unsplit text goes: the index of a slot, or len(turn)
	if !exact {
		whole = slices.IndexFunc(turn, func(s slot) bool { return s.Text > 0 })
		if whole < 0 {
			whole = slices.IndexFunc(turn, func(s slot) bool { return s.Tool })
		}
		if whole < 0 {
			whole = len(turn)
		}
	}

	called := false
	var open []int
	for i := range turn {
		if i == whole {
			b.pushText(roleAssistant, text)
		}
		switch s := &turn[i]; {
		case len(s.Block) > 0:
			b.push(roleAssistant, block{Type: blockType(s.Block), raw: s.Block})
		case s.Text > 0:
			if exact {
				b.pushText(roleAssistant, pieces[0])
				pieces = pieces[1:]
			}
		case s.Tool && use != nil && !called:
			b.push(roleAssistant, *use)
			called = true
		case s.Tool && called:
			// The message exists by now: its first call has been pushed.
			open = append(open, len(b.msgs[len(b.msgs)-1].Content))
		}
	}
	if whole == len(turn) {
		b.pushText(roleAssistant, text)
	}
	if use != nil && !called {
		b.push(roleAssistant, *use)
	}
	b.open = openSlots{msg: len(b.msgs) - 1, at: open}
}

// openSlots are the places in one assistant message where a recorded turn
// had further tool calls: at holds, in order, the index in the message's
// content at which the next sibling call belongs.
type openSlots struct {
	msg int
	at  []int
}

// pushText adds a text block unless the text is empty or blank.
func (b *builder) pushText(role, text string) {
	if hasText(text) {
		b.push(role, block{Type: blockText, Text: text})
	}
}

// input is the arguments of a tool call as they go back on the wire: the
// raw arguments of a tool with its own schema, or {"code": ...}.
func (b *builder) input(c *core.ToolCall) (json.RawMessage, error) {
	switch {
	case len(c.Args) > 0 && json.Valid(c.Args):
		return c.Args, nil
	case b.ownSchema[c.Name]:
		// Its arguments are missing or unreadable. An empty object keeps the
		// request valid, which a turn in the log must never stop being.
		return json.RawMessage(`{}`), nil
	default:
		return encode(codeInput{Code: c.Code})
	}
}

// joinStep adds a sibling call to the assistant message that already holds
// the first call of its step. The model made the calls in one message; they
// were run and logged one at a time, so the plan holds
//
//	assistant(call A), tool(result A), assistant(call B), tool(result B)
//
// and the wire wants one assistant message with both calls, then one user
// message with both results. The results need no help: B's is pushed after
// this and merges into the user message that holds A's.
//
// The call goes where the step's recorded turn had its next tool call, or
// at the end of the message when nothing was recorded or every slot is
// taken.
//
// It reports false, and adds nothing, when the messages so far do not end
// in such a step; the call is then rendered as a message of its own. A
// sibling's reasoning is not looked at: a step's reasoning belongs to its
// first call.
func (b *builder) joinStep(text string, use block) bool {
	n := len(b.msgs)
	if n < 2 || b.msgs[n-1].Role != roleUser || b.msgs[n-2].Role != roleAssistant {
		return false
	}
	step, results := &b.msgs[n-2], &b.msgs[n-1]
	isCall := func(bl block) bool { return bl.Type == blockToolUse }
	if !slices.ContainsFunc(step.Content, isCall) || results.Content[0].Type != blockToolResult {
		return false
	}
	blocks := make([]block, 0, 2)
	if hasText(text) {
		blocks = append(blocks, block{Type: blockText, Text: text})
	}
	blocks = append(blocks, use)

	at := len(step.Content)
	if b.open.msg == n-2 && len(b.open.at) > 0 {
		at, b.open.at = b.open.at[0], b.open.at[1:]
		for i := range b.open.at {
			b.open.at[i] += len(blocks)
		}
	}
	step.Content = slices.Insert(step.Content, at, blocks...)
	return true
}

// blockType reads the type of a content block.
func blockType(raw json.RawMessage) string {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return ""
	}
	return head.Type
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
