package messages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"slices"
	"strings"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/wire"
)

// stream is one in-flight call. It starts no goroutine: Events reads the
// response body on the caller's goroutine, and cancelling the request
// context is what unblocks a read.
type stream struct {
	run     func(yield func(llm.Event) bool) (llm.Result, error)
	abandon func()
	// unread is the error Result gives when Events was never ranged over.
	unread  error
	started bool
	done    bool
	res     llm.Result
	err     error
}

func (s *stream) Events() iter.Seq[llm.Event] {
	return func(yield func(llm.Event) bool) {
		if s.started {
			return
		}
		s.started = true
		s.res, s.err = s.run(yield)
		s.done = true
	}
}

func (s *stream) Result() (llm.Result, error) {
	if !s.started {
		// Never read: give the connection back.
		s.started, s.done, s.err = true, true, s.unread
		s.abandon()
	}
	if !s.done {
		return llm.Result{}, s.unread
	}
	return s.res, s.err
}

// consume reads a response to its end, yielding deltas, and closes the body
// on every path out. Whatever was read before a failure is returned beside
// the error.
func consume(ctx context.Context, body io.ReadCloser, d *decoder, yield func(llm.Event) bool) (llm.Result, error) {
	// Covers a panic in the consumer's loop body too.
	defer func() { _ = body.Close() }()
	err := d.drain(body, yield)
	if err == nil {
		return d.finish()
	}
	if cerr := ctx.Err(); cerr != nil {
		// Not an *llm.Error: nothing went wrong upstream, the caller left.
		return d.partial(), fmt.Errorf("%s: stream: %w", d.route.Provider, cerr)
	}
	return d.partial(), err
}

// Stream events. Fields this codec does not use (citations, stop details)
// are left out and so are ignored.

type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage json.RawMessage `json:"usage"`
	} `json:"message"`
	// ContentBlock stays raw: a reasoning block goes back with every field
	// it arrived with, known to this codec or not.
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        *delta          `json:"delta"`
	Usage        json.RawMessage `json:"usage"`
	Error        *errorBody      `json:"error"`
}

type startBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type delta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	Signature   string `json:"signature"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

// usage uses pointers because message_delta repeats only some of the counts
// that message_start reported.
type usage struct {
	InputTokens              *int `json:"input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
}

type errorEnvelope struct {
	Error *errorBody `json:"error"`
}

type errorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

const (
	blockThinking = "thinking"
	blockRedacted = "redacted_thinking"
	blockFallback = "fallback"

	fieldThinking  = "thinking"
	fieldSignature = "signature"

	stopToolUse = "tool_use"
)

// captured is one content block of the reply.
type captured struct {
	index int
	kind  string
	// raw is the block from its start event. A reasoning block is rebuilt
	// on it, so that whatever it carried goes back.
	raw       json.RawMessage
	text      strings.Builder // text, or a thinking block's text
	signature strings.Builder
	id, name  string // tool_use
	// input is a tool_use block's arguments as streamed in fragments, and
	// startInput what its start event carried, which is {} wherever the
	// arguments are streamed.
	input      []byte
	startInput json.RawMessage
}

// toolKind says how a declared tool takes its arguments.
type toolKind uint8

const (
	toolUndeclared toolKind = iota
	toolCode                // the default schema: one string, "code"
	toolArgs                // its own schema: the arguments are passed on raw
)

// declaredTools reads the tools a frozen request declares, and for each
// whether it has the default schema. A frozen request is all Send is given,
// and its body says everything the decoder needs. The scan stops where the
// tools end: they are written ahead of the system prompt and the messages,
// so the long part of the body is never read.
func declaredTools(body []byte) map[string]toolKind {
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		switch tok {
		case "tools":
			var defs []struct {
				Name        string          `json:"name"`
				InputSchema json.RawMessage `json:"input_schema"`
			}
			if err := dec.Decode(&defs); err != nil {
				return nil
			}
			kinds := make(map[string]toolKind, len(defs))
			for _, def := range defs {
				kinds[def.Name] = toolArgs
				if string(def.InputSchema) == codeSchema {
					kinds[def.Name] = toolCode
				}
			}
			return kinds
		case "system", "messages":
			return nil // past where the tools would be: none are declared
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil
		}
	}
	return nil
}

// decoder folds stream events into the final Result.
type decoder struct {
	route llm.Route
	tools map[string]toolKind

	text       strings.Builder
	stopReason string

	// blocks is the reply so far, in order.
	blocks []*captured

	input, cacheCreation, cacheRead, output int
	// rawUsage is the provider's usage record: what message_start reported,
	// with the counts message_delta repeated written over it.
	rawUsage object
}

// fail classifies a failure that has no HTTP status behind it.
func (d *decoder) fail(class llm.ErrorClass, err error) error {
	return &llm.Error{Class: class, Provider: d.route.Provider, Err: err}
}

func (d *decoder) drain(body io.Reader, yield func(llm.Event) bool) error {
	sse := wire.NewSSEReader(body)
	for {
		raw, err := sse.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return d.fail(llm.ErrTransport, err)
			}
			if d.stopReason != "" {
				return nil // the server skipped message_stop; the call still finished
			}
			return d.fail(llm.ErrTransport, ErrIncomplete)
		}
		if len(raw.Data) == 0 {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal(raw.Data, &ev); err != nil {
			return d.fail(llm.ErrTransport, fmt.Errorf("decode stream event: %w", err))
		}
		done, err := d.handle(&ev, yield)
		if err != nil || done {
			return err
		}
	}
}

func (d *decoder) handle(ev *streamEvent, yield func(llm.Event) bool) (bool, error) {
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			d.count(ev.Message.Usage)
		}
	case "content_block_start":
		return false, d.startBlock(ev, yield)
	case "content_block_delta":
		return false, d.blockDelta(ev, yield)
	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			d.stopReason = ev.Delta.StopReason
		}
		d.count(ev.Usage)
	case "message_stop":
		return true, nil
	case "error":
		e := errorBody{Type: "unknown"}
		if ev.Error != nil {
			e = *ev.Error
		}
		return false, d.fail(classOf(e.Type, e.Message), errors.New("stream error "+e.Type+": "+wire.Excerpt([]byte(e.Message))))
	}
	// ping, content_block_stop and event types added later need no action.
	return false, nil
}

func (d *decoder) startBlock(ev *streamEvent, yield func(llm.Event) bool) error {
	if len(ev.ContentBlock) == 0 {
		return nil
	}
	var cb startBlock
	if err := json.Unmarshal(ev.ContentBlock, &cb); err != nil {
		return d.fail(llm.ErrTransport, fmt.Errorf("decode content block: %w", err))
	}
	c := &captured{index: ev.Index, kind: cb.Type, raw: ev.ContentBlock, id: cb.ID, name: cb.Name, startInput: cb.Input}
	c.signature.WriteString(cb.Signature)
	d.blocks = append(d.blocks, c)

	switch cb.Type {
	case blockText:
		return d.emit(c, llm.TextDelta, cb.Text, yield)
	case blockThinking:
		return d.emit(c, llm.ReasoningDelta, cb.Thinking, yield)
	case blockToolUse:
		// The start event's input is {} and the arguments follow as
		// input_json_delta fragments. Announce the tool now: the turn loop
		// uses the head start to warm a sandbox.
		if !yield(llm.Event{Kind: llm.ToolStart, Tool: cb.Name}) {
			return d.fail(llm.ErrUnknown, ErrAbandoned)
		}
	case blockFallback:
		// A classifier declined mid-message and another model took over.
		// Text before the boundary stands. Reasoning and tool calls before
		// it belong to the model that was replaced and must not go back.
		// The marker itself is for audit only and is not kept either.
		d.blocks = slices.DeleteFunc(d.blocks, func(b *captured) bool { return b.kind != blockText })
	}
	return nil
}

func (d *decoder) blockDelta(ev *streamEvent, yield func(llm.Event) bool) error {
	if ev.Delta == nil {
		return nil
	}
	// Deltas follow their block's start closely, so search from the end.
	var c *captured
	for _, b := range slices.Backward(d.blocks) {
		if b.index == ev.Index {
			c = b
			break
		}
	}
	if c == nil {
		return nil
	}
	switch ev.Delta.Type {
	case "text_delta":
		return d.emit(c, llm.TextDelta, ev.Delta.Text, yield)
	case "thinking_delta":
		return d.emit(c, llm.ReasoningDelta, ev.Delta.Thinking, yield)
	case "signature_delta":
		// Sent once, just before the block closes.
		c.signature.WriteString(ev.Delta.Signature)
	case "input_json_delta":
		c.input = append(c.input, ev.Delta.PartialJSON...)
	}
	return nil
}

// emit records a fragment of a text or thinking block and yields it.
func (d *decoder) emit(c *captured, kind llm.EventKind, text string, yield func(llm.Event) bool) error {
	if text == "" {
		return nil
	}
	c.text.WriteString(text)
	if kind == llm.TextDelta {
		d.text.WriteString(text)
	}
	if !yield(llm.Event{Kind: kind, Text: text}) {
		return d.fail(llm.ErrUnknown, ErrAbandoned)
	}
	return nil
}

func (d *decoder) count(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var u usage
	if err := json.Unmarshal(raw, &u); err != nil {
		return
	}
	set := func(dst, src *int) {
		if src != nil {
			*dst = *src
		}
	}
	set(&d.input, u.InputTokens)
	set(&d.cacheCreation, u.CacheCreationInputTokens)
	set(&d.cacheRead, u.CacheReadInputTokens)
	set(&d.output, u.OutputTokens)

	if o, err := parseObject(raw); err == nil {
		for _, m := range o {
			d.rawUsage.set(m.key, m.val)
		}
	}
}

// partial is what is known when a call ends early.
func (d *decoder) partial() llm.Result {
	res := llm.Result{
		Text: d.text.String(),
		Usage: llm.Usage{
			// input_tokens is only the uncached remainder: tokens read from
			// the cache and tokens written to it are counted beside it.
			Input:  d.input + d.cacheRead + d.cacheCreation,
			Cached: d.cacheRead,
			Output: d.output,
			// Reasoning stays 0: this wire reports no separate count, and
			// reasoning tokens are already part of output_tokens.
		},
	}
	if len(d.rawUsage) > 0 {
		if raw, err := d.rawUsage.encode(); err == nil {
			res.Usage.Raw = raw
		}
	}
	return res
}

func (d *decoder) finish() (llm.Result, error) {
	res := d.partial()
	res.Stop = stopOf(d.stopReason)
	// A tool call cut off by max_tokens has truncated arguments and must
	// not run, so calls are returned for no other stop reason.
	calls := d.stopReason == stopToolUse
	reasoning, err := d.reasoning(calls)
	if err != nil {
		return res, d.fail(llm.ErrUnknown, err)
	}
	res.Reasoning = reasoning
	if !calls {
		return res, nil
	}
	for _, c := range d.blocks {
		if c.kind != blockToolUse {
			continue
		}
		call := core.ToolCall{ID: c.id, Name: c.name, Sibling: len(res.ToolCalls)}
		if err := d.arguments(c, &call); err != nil {
			res.ToolCalls = nil
			return res, d.fail(llm.ErrUnknown, fmt.Errorf("tool %q: %w", c.name, err))
		}
		res.ToolCalls = append(res.ToolCalls, call)
	}
	if len(res.ToolCalls) == 0 {
		// Every call fell before a fallback boundary: a plain reply.
		res.Stop = llm.StopEnd
	}
	return res, nil
}

// arguments fills a call's Code or Args from what was streamed.
func (d *decoder) arguments(c *captured, call *core.ToolCall) error {
	input := c.input
	if len(bytes.TrimSpace(input)) == 0 {
		// No fragments: the call has no arguments, or they came whole in
		// the start event.
		input = c.startInput
	}
	if len(bytes.TrimSpace(input)) == 0 {
		input = []byte(`{}`)
	}
	if !json.Valid(input) {
		return ErrBadToolInput
	}
	kind := d.tools[c.name]
	if kind == toolArgs {
		call.Args = input
		return nil
	}
	var args struct {
		Code *string `json:"code"`
	}
	if err := json.Unmarshal(input, &args); err != nil || args.Code == nil {
		if kind == toolUndeclared {
			// The model made the tool up. Pass on what it wrote; the turn
			// loop is what tells it there is no such tool.
			call.Args = input
			return nil
		}
		return ErrBadToolInput
	}
	call.Code = *args.Code
	return nil
}

// reasoning records the reply as the payload that returns its reasoning to
// this route: the thinking and redacted_thinking blocks as they arrived,
// and where the text blocks and the tool calls sat among them (see
// payload). Tool calls are recorded only when the reply returns them. It is
// nil when the reply had no reasoning block, or when the route never takes
// reasoning back.
func (d *decoder) reasoning(calls bool) (*core.Reasoning, error) {
	if d.route.Caps.Replay == llm.ReplayNone {
		return nil, nil //nolint:nilnil // no payload is a normal outcome, not an error
	}
	turn := make([]slot, 0, len(d.blocks))
	reasoned := false
	for _, c := range d.blocks {
		switch c.kind {
		case blockThinking:
			raw, err := c.thinkingBlock()
			if err != nil {
				return nil, err
			}
			turn, reasoned = append(turn, slot{Block: raw}), true
		case blockRedacted:
			// Complete on arrival.
			raw, err := compact(c.raw)
			if err != nil {
				return nil, err
			}
			turn, reasoned = append(turn, slot{Block: raw}), true
		case blockText:
			// The length of everything the block streamed, blank or not:
			// the lengths have to add up to the reply's text.
			if n := c.text.Len(); n > 0 {
				turn = append(turn, slot{Text: n})
			}
		case blockToolUse:
			if calls {
				turn = append(turn, slot{Tool: true})
			}
		}
	}
	if !reasoned {
		return nil, nil //nolint:nilnil // no payload is a normal outcome, not an error
	}
	data, err := encodePayload(turn)
	if err != nil {
		return nil, err
	}
	return &core.Reasoning{Route: d.route.ID(), Data: data}, nil
}

// thinkingBlock is a streamed thinking block put back together: the block
// from its start event with the text and signature that followed in deltas
// written in. Members keep their order, and any this codec does not know
// are kept as they are.
//
// The text member stays even when empty. Where reasoning is not displayed
// the text is always empty and the signature carries everything, and a
// block sent back without the member is no longer recognised.
func (c *captured) thinkingBlock() ([]byte, error) {
	o, err := parseObject(c.raw)
	if err != nil {
		return nil, err
	}
	if text := c.text.String(); text != "" || o.has(fieldThinking) {
		if err := o.setString(fieldThinking, text); err != nil {
			return nil, err
		}
	}
	if sig := c.signature.String(); sig != "" || o.has(fieldSignature) {
		if err := o.setString(fieldSignature, sig); err != nil {
			return nil, err
		}
	}
	return o.encode()
}

// stopOf normalizes a stop_reason. An empty or unknown reason is a finished
// reply: the stream ended in good order and said nothing else.
func stopOf(reason string) llm.StopReason {
	switch reason {
	case stopToolUse:
		return llm.StopTool
	case "max_tokens", "model_context_window_exceeded":
		return llm.StopLength
	case "refusal":
		return llm.StopRefused
	case "content_filter":
		// Not a Messages stop reason. Gateways that front other formats
		// pass their own name for a filtered reply through.
		return llm.StopFiltered
	default:
		return llm.StopEnd
	}
}

// refine reads the error type out of a failed response's body. It is what
// tells an overflowing prompt from any other bad request, and it holds on
// gateways that put a different status in front of the same body.
func refine(status int, body []byte) llm.ErrorClass {
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err != nil || env.Error == nil {
		return llm.ErrUnknown
	}
	class := classOf(env.Error.Type, env.Error.Message)
	if class == llm.ErrBadRequest && status != http.StatusBadRequest {
		// invalid_request_error under another status: the status knows more.
		return llm.ErrUnknown
	}
	return class
}

// classOf maps this wire's error types onto the neutral classes. A type it
// does not know is ErrUnknown.
func classOf(typ, message string) llm.ErrorClass {
	switch typ {
	case "overloaded_error", "api_error", "timeout_error":
		return llm.ErrOverloaded
	case "rate_limit_error":
		return llm.ErrRateLimited
	case "authentication_error", "permission_error":
		return llm.ErrAuth
	case "billing_error":
		return llm.ErrQuota
	case "request_too_large":
		return llm.ErrContextOverflow
	case "invalid_request_error":
		msg := strings.ToLower(message)
		for _, sign := range []string{"prompt is too long", "context length", "context window", "context limit"} {
			if strings.Contains(msg, sign) {
				return llm.ErrContextOverflow
			}
		}
		return llm.ErrBadRequest
	default:
		return llm.ErrUnknown
	}
}
