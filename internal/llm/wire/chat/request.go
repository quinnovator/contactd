package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/plan"
)

// The wire structs below are marshalled in field order, never from a map, so
// a plan always produces the same request bytes. Prefix caches match on
// those bytes (or on the tokens they render to); the golden test pins them.

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type effortObject struct {
	Effort string `json:"effort"`
}

type thinkingToggle struct {
	Type string `json:"type"`
}

// message is a system, user, assistant or tool message. Content is encoded
// ahead of time because it has two shapes: a string everywhere, and an array
// of typed parts on the messages that carry a cache breakpoint. A tool
// message carries it even when empty, while an assistant message that only
// calls a tool leaves it out.
type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content,omitempty"`
	// At most one of the three reasoning fields is set, by the dialect's
	// ReasoningStyle.
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	Reasoning        string          `json:"reasoning,omitempty"`
	ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
	ToolCalls        []toolCall      `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
}

// part is one typed content part. Only text parts are used; they exist so a
// cache breakpoint has something to sit on.
type part struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
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

type codeArgs struct {
	Code string `json:"code"`
}

type tool struct {
	Type     string   `json:"type"`
	Function function `json:"function"`
}

type function struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

const (
	roleSystem    = "system"
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"

	typeFunction = "function"
	typeText     = "text"

	// The format has no error flag on a tool message, so a failed run is
	// marked in its text.
	toolErrorPrefix = "[tool error]\n"
	// mergeSep joins the texts of neighbouring messages with the same role.
	mergeSep = "\n\n"

	// codeSchema is the parameters of a tool that declares no schema of its
	// own: one required string, "code". A call to such a tool is decoded
	// into ToolCall.Code; Send recognises the tool by these exact bytes.
	codeSchema = `{"type":"object","properties":{"code":{"type":"string","description":"The code to run."}},` +
		`"required":["code"],"additionalProperties":false}`
	// noArgs stands in for the arguments of a call that recorded none.
	noArgs = "{}"
)

func renderTools(defs []core.ToolDef) []tool {
	tools := make([]tool, len(defs))
	for i := range defs {
		params := json.RawMessage(codeSchema)
		if len(defs[i].Input) > 0 {
			params = defs[i].Input
		}
		tools[i] = tool{
			Type:     typeFunction,
			Function: function{Name: defs[i].Name, Description: defs[i].Description, Parameters: params},
		}
	}
	return tools
}

// renderMessages lays a plan out as Chat Completions messages: tiers 0-2 as
// the system message, tiers 3-4 as the conversation. The order is the
// plan's, least volatile first, so the longest possible prefix repeats from
// one request to the next.
func renderMessages(d *Dialect, r llm.Route, p *plan.PromptPlan) ([]message, error) {
	b := builder{
		route:       r.ID(),
		ownSchema:   make(map[string]bool, len(p.Tools)),
		breakpoints: r.Caps.Cache == llm.CacheBreakpoints,
	}
	if r.Caps.Replay != llm.ReplayNone {
		b.reasoning = d.Reasoning
	}
	for i := range p.Tools {
		b.ownSchema[p.Tools[i].Name] = len(p.Tools[i].Input) > 0
	}

	for t := plan.TierStatic; t <= plan.TierLive; t++ {
		b.tierText = nil
		for i := range p.Tiers[t].Msgs {
			m := &p.Tiers[t].Msgs[i]
			if t <= plan.TierRecall {
				b.text(roleSystem, m.Text)
				continue
			}
			if err := b.add(m); err != nil {
				return nil, err
			}
		}
		// A breakpoint closes each cacheable tier, which is where the plan's
		// own marks put them: at most four, one per tier below the live one.
		if b.breakpoints && t < plan.TierLive && b.tierText != nil {
			b.tierText.breakpoint = true
		}
	}

	msgs := make([]message, len(b.msgs))
	for i, dm := range b.msgs {
		m, err := dm.wire()
		if err != nil {
			return nil, err
		}
		msgs[i] = m
	}
	return msgs, nil
}

// draft is a wire message under construction.
type draft struct {
	role string
	// texts are the pieces of the message's content, one per plan message
	// merged into it.
	texts []*segment
	// reasoning is the payload this assistant message returns to its route.
	reasoning  *payload
	style      ReasoningStyle
	calls      []toolCall
	toolCallID string
}

type segment struct {
	text string
	// breakpoint marks the end of a cacheable tier.
	breakpoint bool
}

// builder turns provider-neutral messages into wire messages. Neighbours
// with the same role are merged into one: many self-hosted chat templates
// accept a single leading system message and strictly alternating turns.
type builder struct {
	msgs []*draft

	route       string
	reasoning   ReasoningStyle // ReasoningUnsent on a route that replays nothing
	breakpoints bool
	// ownSchema says, per declared tool, whether it has its own input schema.
	ownSchema map[string]bool

	// step is the assistant message that holds the calls of the model step
	// being rendered, and stepEnd the index just past its last tool result.
	// A sibling call joins step, and its result is placed at stepEnd.
	step    *draft
	stepEnd int
	// tierText is the last non-empty text the current tier produced: where
	// its cache breakpoint goes.
	tierText *segment
}

func (b *builder) add(m *core.Message) error {
	switch m.Role {
	case core.RoleSystem:
		b.text(roleSystem, m.Text)
	case core.RoleUser:
		b.text(roleUser, m.Text)
	case core.RoleAssistant:
		return b.assistant(m)
	case core.RoleTool:
		if r := m.ToolResult; r != nil {
			b.result(r)
		}
		b.text(roleUser, m.Text)
	}
	return nil
}

func (b *builder) assistant(m *core.Message) error {
	tc := m.ToolCall
	if tc == nil && strings.TrimSpace(m.Text) == "" {
		return nil // nothing was said: no message, and so nowhere for reasoning to go
	}
	pl, err := b.own(m.Reasoning)
	if err != nil {
		return err
	}
	var a *draft
	if tc != nil && tc.Sibling > 0 && b.step != nil {
		// Calls are logged one at a time, each followed by its result. A
		// sibling belongs to the assistant message of the call before it.
		a = b.step
	} else {
		a = b.open(roleAssistant)
		if pl != nil && a.reasoning != nil {
			// One message returns one payload. Rather than drop either,
			// keep the two model calls apart.
			a = b.push(roleAssistant)
		}
		b.step = nil
	}
	b.appendText(a, m.Text)
	if pl != nil && a.reasoning == nil {
		a.reasoning, a.style = pl, b.reasoning
	}
	if tc == nil {
		return nil
	}
	args, err := b.arguments(tc)
	if err != nil {
		return err
	}
	a.calls = append(a.calls, toolCall{
		ID:       tc.ID,
		Type:     typeFunction,
		Function: functionCall{Name: tc.Name, Arguments: args},
	})
	if a != b.step {
		b.step, b.stepEnd = a, len(b.msgs)
	}
	return nil
}

// arguments renders a call's arguments as the JSON string the format wants.
func (b *builder) arguments(tc *core.ToolCall) (string, error) {
	if len(tc.Args) > 0 {
		return string(tc.Args), nil
	}
	if b.ownSchema[tc.Name] {
		return noArgs, nil
	}
	args, err := encode(codeArgs{Code: tc.Code})
	if err != nil {
		return "", err
	}
	return string(args), nil
}

// result adds a tool message. Each result answers one call by ID, so results
// never merge. The results of one step directly follow its assistant
// message, in call order, even when a message from the contact was logged
// between two of them.
func (b *builder) result(r *core.ToolResult) {
	content := r.Output
	if r.IsError {
		content = toolErrorPrefix + content
	}
	seg := &segment{text: content}
	dm := &draft{role: roleTool, texts: []*segment{seg}, toolCallID: r.ID}
	if content != "" {
		b.tierText = seg
	}
	inStep := b.step != nil && slices.ContainsFunc(b.step.calls, func(c toolCall) bool { return c.ID == r.ID })
	if !inStep {
		b.msgs = append(b.msgs, dm)
		return
	}
	b.msgs = slices.Insert(b.msgs, b.stepEnd, dm)
	b.stepEnd++
}

// own decodes a reasoning payload if it is this route's to receive. A
// payload tagged for any other route yields nil: it must not reach the
// request in any form.
func (b *builder) own(r *core.Reasoning) (*payload, error) {
	if r == nil || len(r.Data) == 0 || b.reasoning == ReasoningUnsent || r.Route != b.route {
		return nil, nil
	}
	pl, err := decodePayload(r.Data)
	if err != nil {
		return nil, fmt.Errorf("chat: reasoning payload for %s: %w", b.route, err)
	}
	if pl.empty() {
		return nil, nil
	}
	return pl, nil
}

// text appends text to the last message if it has the same role, and starts
// a new message otherwise. Blank text is dropped.
func (b *builder) text(role, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	b.appendText(b.open(role), text)
}

func (b *builder) appendText(m *draft, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	seg := &segment{text: text}
	m.texts = append(m.texts, seg)
	b.tierText = seg
}

// open returns the last message if it has the given role, or a new one.
func (b *builder) open(role string) *draft {
	if n := len(b.msgs); n > 0 && b.msgs[n-1].role == role {
		return b.msgs[n-1]
	}
	return b.push(role)
}

func (b *builder) push(role string) *draft {
	m := &draft{role: role}
	b.msgs = append(b.msgs, m)
	return m
}

// wire encodes a finished draft.
func (m *draft) wire() (message, error) {
	out := message{Role: m.role, ToolCalls: m.calls, ToolCallID: m.toolCallID}
	content, err := m.content()
	if err != nil {
		return message{}, err
	}
	out.Content = content
	if m.reasoning == nil {
		return out, nil
	}
	switch m.style {
	case ReasoningContent:
		out.ReasoningContent = m.reasoning.Text
	case ReasoningDetails:
		if len(m.reasoning.Details) == 0 {
			out.Reasoning = m.reasoning.Text
			break
		}
		details, err := encode(m.reasoning.Details)
		if err != nil {
			return message{}, err
		}
		out.ReasoningDetails = details
	case ReasoningUnsent:
		// Never reached: own returns no payload in this style.
	}
	return out, nil
}

// content encodes the message's content: a plain string, or typed parts when
// a cache breakpoint falls inside it. The parts concatenate to the same text
// the string form has, so a breakpoint moves no model-visible byte.
func (m *draft) content() (json.RawMessage, error) {
	if len(m.texts) == 0 {
		return nil, nil
	}
	marked := slices.ContainsFunc(m.texts, func(s *segment) bool { return s.breakpoint })
	if !marked {
		texts := make([]string, len(m.texts))
		for i, s := range m.texts {
			texts[i] = s.text
		}
		return encode(strings.Join(texts, mergeSep))
	}
	var (
		parts []part
		cur   strings.Builder
	)
	for i, s := range m.texts {
		if i > 0 {
			cur.WriteString(mergeSep)
		}
		cur.WriteString(s.text)
		if s.breakpoint {
			parts = append(parts, part{Type: typeText, Text: cur.String(), CacheControl: &cacheControl{Type: "ephemeral"}})
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, part{Type: typeText, Text: cur.String()})
	}
	return encode(parts)
}

// object writes a JSON object field by field, in the order given. A struct
// would fix the names; the names here are partly the dialect's.
type object struct {
	buf   bytes.Buffer
	names []string
	err   error
}

// add encodes v as the value of a new field.
func (o *object) add(name string, v any) {
	if o.err != nil {
		return
	}
	raw, err := encode(v)
	if err != nil {
		o.err = err
		return
	}
	o.raw(name, raw)
}

// raw appends a field whose value is already JSON.
func (o *object) raw(name string, value []byte) {
	if o.err != nil {
		return
	}
	if name == "" || slices.Contains(o.names, name) {
		o.err = fmt.Errorf("chat: body field %q is empty or written twice", name)
		return
	}
	key, err := encode(name)
	if err != nil {
		o.err = err
		return
	}
	if len(o.names) > 0 {
		o.buf.WriteByte(',')
	}
	o.names = append(o.names, name)
	o.buf.Write(key)
	o.buf.WriteByte(':')
	o.buf.Write(value)
}

func (o *object) bytes() ([]byte, error) {
	if o.err != nil {
		return nil, o.err
	}
	out := make([]byte, 0, o.buf.Len()+2)
	out = append(out, '{')
	out = append(out, o.buf.Bytes()...)
	return append(out, '}'), nil
}

// encode marshals v without HTML escaping, so "<now>" is sent as written
// rather than as <now>.
func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("chat: encode request: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
