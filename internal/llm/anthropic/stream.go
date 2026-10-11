package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/turn"
)

// stream is one in-flight call. It starts no goroutine: Events reads the
// response body on the caller's goroutine, and cancelling the request context
// is what unblocks a read.
type stream struct {
	run     func(yield func(turn.StreamEvent) bool) (turn.Stop, error)
	abandon func()
	started bool
	done    bool
	stop    turn.Stop
	err     error
}

func (s *stream) Events() iter.Seq[turn.StreamEvent] {
	return func(yield func(turn.StreamEvent) bool) {
		if s.started {
			return
		}
		s.started = true
		s.stop, s.err = s.run(yield)
		s.done = true
	}
}

func (s *stream) Stop() (turn.Stop, error) {
	if !s.started {
		// Never read: give the connection back.
		s.started, s.done, s.err = true, true, ErrNotDrained
		s.abandon()
	}
	if !s.done {
		return turn.Stop{}, ErrNotDrained
	}
	return s.stop, s.err
}

// breakError means the consumer left the range loop.
type breakError struct{}

func (breakError) Error() string { return "consumer stopped reading" }

// consume reads resp to the end, yielding deltas. The body is closed on
// every path out. An overload reported before anything was yielded is
// retried on a fresh connection; after that the caller has seen output and a
// retry would repeat it.
func (c *Client) consume(
	ctx context.Context, cl call, resp *http.Response, attempt int, yield func(turn.StreamEvent) bool,
) (turn.Stop, error) {
	// Covers a panic in the consumer's loop body; closing twice is harmless.
	defer func() { _ = resp.Body.Close() }()
	for {
		d := decoder{log: c.log}
		err := d.drain(ctx, resp.Body, yield)
		_ = resp.Body.Close()
		if err == nil {
			return d.finish()
		}
		if errors.As(err, new(breakError)) {
			return d.partial(), ErrIncomplete
		}
		if cerr := ctx.Err(); cerr != nil {
			return d.partial(), fmt.Errorf("anthropic: stream: %w", cerr)
		}
		if d.yielded {
			return d.partial(), err
		}
		if err := c.pause(ctx, cl, &attempt, nil, err); err != nil {
			return d.partial(), err
		}
		if resp, err = c.send(ctx, cl, &attempt); err != nil {
			return turn.Stop{}, err
		}
	}
}

// Stream events. Fields this adapter does not use (citations, stop details)
// are left out and so are ignored.

type streamEvent struct {
	Type    string        `json:"type"`
	Index   int           `json:"index"`
	Message *startMessage `json:"message"`
	// ContentBlock stays raw: a block this adapter does not understand is
	// replayed exactly as it arrived.
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        *delta          `json:"delta"`
	Usage        *usage          `json:"usage"`
	Error        *errorBody      `json:"error"`
}

type startMessage struct {
	Model string `json:"model"`
	Usage *usage `json:"usage"`
}

type startBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	Thinking  string         `json:"thinking"`
	Signature string         `json:"signature"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	From      *fallbackModel `json:"from"`
	To        *fallbackModel `json:"to"`
}

type fallbackModel struct {
	Model string `json:"model"`
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
	Error     *errorBody `json:"error"`
	RequestID string     `json:"request_id"`
}

type errorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type toolArgs struct {
	Code *string `json:"code"`
}

const (
	blockThinking = "thinking"
	blockFallback = "fallback"
)

// captured is one content block of the reply, kept so the assistant turn can
// be sent back as the API produced it (core.ToolCall.Replay).
type captured struct {
	index int
	kind  string
	// raw is the block from its start event. Blocks that are complete on
	// arrival (redacted_thinking, fallback, kinds added later) are replayed
	// from it untouched.
	raw       json.RawMessage
	text      strings.Builder // text, or a thinking block's text
	signature strings.Builder
	id, name  string // tool_use
	input     []byte // tool_use arguments, as streamed
}

// The replayed forms of the blocks this adapter assembles from deltas. A
// thinking block keeps its text field even when empty: under the default
// display the text is always empty and the signature carries the reasoning,
// so a serializer that dropped the empty field would send back a block the
// API no longer recognizes.

type replayThinking struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type replayText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type replayToolUse struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// decoder folds stream events into the final Stop.
type decoder struct {
	log        *slog.Logger
	text       strings.Builder
	stopReason string
	yielded    bool

	// blocks is the reply so far, in order.
	blocks []*captured
	// tool is the first tool_use block of the message. Later ones are
	// ignored: the turn loop runs one tool per step.
	tool *captured

	input, cacheCreation, cacheRead, output int
}

func (d *decoder) drain(ctx context.Context, body io.Reader, yield func(turn.StreamEvent) bool) error {
	sse := newSSEReader(body)
	for {
		raw, err := sse.next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return &transientError{err}
			}
			if d.stopReason != "" {
				return nil // the server skipped message_stop; the call still finished
			}
			return &transientError{ErrIncomplete}
		}
		if len(raw.data) == 0 {
			continue
		}
		var ev streamEvent
		if err := unmarshal(raw.data, &ev); err != nil {
			return err
		}
		done, err := d.handle(ctx, &ev, yield)
		if err != nil || done {
			return err
		}
	}
}

func (d *decoder) handle(ctx context.Context, ev *streamEvent, yield func(turn.StreamEvent) bool) (bool, error) {
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			d.count(ev.Message.Usage)
		}
	case "content_block_start":
		return false, d.startBlock(ctx, ev, yield)
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
		e := &APIError{Type: "unknown"}
		if ev.Error != nil {
			e.Type, e.Message = ev.Error.Type, ev.Error.Message
		}
		return false, e
	}
	// ping, content_block_stop and event types added later need no action.
	return false, nil
}

func (d *decoder) startBlock(ctx context.Context, ev *streamEvent, yield func(turn.StreamEvent) bool) error {
	if len(ev.ContentBlock) == 0 {
		return nil
	}
	var cb startBlock
	if err := unmarshal(ev.ContentBlock, &cb); err != nil {
		return err
	}
	c := &captured{index: ev.Index, kind: cb.Type, raw: ev.ContentBlock, id: cb.ID, name: cb.Name}
	c.signature.WriteString(cb.Signature)
	d.blocks = append(d.blocks, c)

	switch cb.Type {
	case blockText:
		return d.emitText(c, cb.Text, yield)
	case blockThinking:
		c.text.WriteString(cb.Thinking)
	case blockToolUse:
		if d.tool != nil {
			return nil
		}
		// The start event's input is always {}; the arguments follow as
		// input_json_delta fragments.
		d.tool = c
		// Announce the tool now: its arguments are still streaming, and the
		// turn loop uses the head start to warm a sandbox.
		d.yielded = true
		if !yield(turn.StreamEvent{Type: turn.ToolStart, Name: cb.Name}) {
			return breakError{}
		}
	case blockFallback:
		// A classifier declined mid-message and another model took over.
		// Text before the boundary stands. Reasoning and tool calls before
		// it belong to the model that was replaced and must not go back.
		d.tool = nil
		d.blocks = slices.DeleteFunc(d.blocks, func(b *captured) bool {
			return b != c && b.kind != blockText && b.kind != blockFallback
		})
		from, to := "", ""
		if cb.From != nil {
			from = cb.From.Model
		}
		if cb.To != nil {
			to = cb.To.Model
		}
		d.log.InfoContext(ctx, "llm_refusal_fallback", "provider", "anthropic", "from_model", from, "to_model", to)
	}
	return nil
}

func (d *decoder) blockDelta(ev *streamEvent, yield func(turn.StreamEvent) bool) error {
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
		return d.emitText(c, ev.Delta.Text, yield)
	case "thinking_delta":
		c.text.WriteString(ev.Delta.Thinking)
	case "signature_delta":
		// Sent once, just before the block closes.
		c.signature.WriteString(ev.Delta.Signature)
	case "input_json_delta":
		c.input = append(c.input, ev.Delta.PartialJSON...)
	}
	return nil
}

func (d *decoder) emitText(c *captured, text string, yield func(turn.StreamEvent) bool) error {
	if text == "" {
		return nil
	}
	c.text.WriteString(text)
	d.text.WriteString(text)
	d.yielded = true
	if !yield(turn.StreamEvent{Type: turn.TextDelta, Text: text}) {
		return breakError{}
	}
	return nil
}

func (d *decoder) count(u *usage) {
	if u == nil {
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
}

// partial is what is known when a call ends early.
func (d *decoder) partial() turn.Stop {
	return turn.Stop{
		Text: d.text.String(),
		Usage: turn.Usage{
			// input_tokens is only the uncached remainder.
			InputTokens:  d.input + d.cacheRead + d.cacheCreation,
			CachedTokens: d.cacheRead,
			OutputTokens: d.output,
		},
	}
}

func (d *decoder) finish() (turn.Stop, error) {
	stop := d.partial()
	switch d.stopReason {
	case "refusal":
		return stop, ErrRefused
	case "tool_use":
		if d.tool == nil {
			return stop, nil
		}
		var args toolArgs
		if err := json.Unmarshal(d.tool.input, &args); err != nil {
			return stop, fmt.Errorf("%w: %w", ErrBadToolInput, err)
		}
		if args.Code == nil {
			return stop, ErrBadToolInput
		}
		replay, err := d.replay()
		if err != nil {
			return stop, err
		}
		stop.ToolCall = &core.ToolCall{ID: d.tool.id, Name: d.tool.name, Code: *args.Code, Replay: replay}
	}
	// Any other reason (end_turn, max_tokens, ...) is a plain reply. A tool
	// call cut off by max_tokens has truncated code and must not run.
	return stop, nil
}

// replay serializes the assistant turn as a content array the API accepts
// back: every block in order up to and including the tool call. With
// thinking always on, the turn that asked for a tool has to return with its
// reasoning blocks unchanged while the tool result is pending.
//
// Blocks after the tool call are left out. They can only be further tool
// calls, which the turn loop will not run and so could not answer.
func (d *decoder) replay() ([]byte, error) {
	out := []byte{'['}
	for _, c := range d.blocks {
		var (
			elem []byte
			err  error
		)
		switch c.kind {
		case blockThinking:
			elem, err = encode(replayThinking{Type: blockThinking, Thinking: c.text.String(), Signature: c.signature.String()})
		case blockText:
			if !hasText(c.text.String()) {
				continue // the API rejects empty text blocks
			}
			elem, err = encode(replayText{Type: blockText, Text: c.text.String()})
		case blockToolUse:
			elem, err = encode(replayToolUse{Type: blockToolUse, ID: c.id, Name: c.name, Input: c.input})
		default:
			var buf bytes.Buffer
			if cerr := json.Compact(&buf, c.raw); cerr != nil {
				err = fmt.Errorf("anthropic: replay block: %w", cerr)
			}
			elem = buf.Bytes()
		}
		if err != nil {
			return nil, err
		}
		if len(out) > 1 {
			out = append(out, ',')
		}
		out = append(out, elem...)
		if c == d.tool {
			break
		}
	}
	return append(out, ']'), nil
}
