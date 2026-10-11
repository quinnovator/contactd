package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
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
// every path out. A server error reported before anything was yielded is
// retried on a fresh connection; after that the caller has seen output and a
// retry would repeat it.
func (c *Client) consume(
	ctx context.Context, cl call, resp *http.Response, attempt int, yield func(turn.StreamEvent) bool,
) (turn.Stop, error) {
	// Covers a panic in the consumer's loop body; closing twice is harmless.
	defer func() { _ = resp.Body.Close() }()
	for {
		var d decoder
		err := d.drain(resp.Body, yield)
		_ = resp.Body.Close()
		if err == nil {
			return d.finish()
		}
		if errors.As(err, new(breakError)) {
			return d.partial(), ErrIncomplete
		}
		if cerr := ctx.Err(); cerr != nil {
			return d.partial(), fmt.Errorf("openai: stream: %w", cerr)
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

// Stream chunks. Fields this adapter does not use (role, logprobs, reasoning
// text from self-hosted reasoning models) are left out and so are ignored.

type chunk struct {
	errorEnvelope

	Choices []chunkChoice `json:"choices"`
	Usage   *usage        `json:"usage"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason string     `json:"finish_reason"`
}

type chunkDelta struct {
	Content   string          `json:"content"`
	ToolCalls []toolCallDelta `json:"tool_calls"`
}

// toolCallDelta is one piece of a tool call. Index says which call it
// belongs to; the ID and name come once, the arguments in fragments.
type toolCallDelta struct {
	Index    int          `json:"index"`
	ID       string       `json:"id"`
	Function functionCall `json:"function"`
}

type usage struct {
	PromptTokens        int           `json:"prompt_tokens"`
	CompletionTokens    int           `json:"completion_tokens"`
	PromptTokensDetails *tokenDetails `json:"prompt_tokens_details"`
}

type tokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type streamedArgs struct {
	Code *string `json:"code"`
}

// decoder folds stream chunks into the final Stop.
type decoder struct {
	text         strings.Builder
	finishReason string
	yielded      bool

	// The first tool call of the reply. Later ones are ignored: the turn
	// loop runs one tool per step.
	tool      *core.ToolCall
	toolIndex int
	toolArgs  []byte
	announced bool

	usage usage
}

func (d *decoder) drain(body io.Reader, yield func(turn.StreamEvent) bool) error {
	sse := newSSEReader(body)
	for {
		raw, err := sse.next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return &transientError{err}
			}
			if d.finishReason != "" {
				return nil // the server skipped [DONE]; the call still finished
			}
			return &transientError{ErrIncomplete}
		}
		data := strings.TrimSpace(string(raw.data))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if d.finishReason == "" {
				return &transientError{ErrIncomplete}
			}
			return nil
		}
		var ck chunk
		if err := unmarshal(raw.data, &ck); err != nil {
			return err
		}
		if err := d.handle(&ck, yield); err != nil {
			return err
		}
	}
}

func (d *decoder) handle(ck *chunk, yield func(turn.StreamEvent) bool) error {
	if e := (&APIError{}); ck.fill(e) {
		return e
	}
	// With include_usage the totals come in a last chunk that has no choices.
	if ck.Usage != nil {
		d.usage = *ck.Usage
	}
	for i := range ck.Choices {
		ch := &ck.Choices[i]
		if ch.Index != 0 {
			continue
		}
		if text := ch.Delta.Content; text != "" {
			d.text.WriteString(text)
			d.yielded = true
			if !yield(turn.StreamEvent{Type: turn.TextDelta, Text: text}) {
				return breakError{}
			}
		}
		for j := range ch.Delta.ToolCalls {
			if err := d.toolDelta(&ch.Delta.ToolCalls[j], yield); err != nil {
				return err
			}
		}
		if ch.FinishReason != "" {
			d.finishReason = ch.FinishReason
		}
	}
	return nil
}

func (d *decoder) toolDelta(td *toolCallDelta, yield func(turn.StreamEvent) bool) error {
	if d.tool == nil {
		d.tool = &core.ToolCall{}
		d.toolIndex = td.Index
	}
	if td.Index != d.toolIndex {
		return nil
	}
	if d.tool.ID == "" {
		d.tool.ID = td.ID
	}
	d.toolArgs = append(d.toolArgs, td.Function.Arguments...)
	if td.Function.Name == "" {
		return nil
	}
	d.tool.Name += td.Function.Name
	if d.announced {
		return nil
	}
	// Announce the tool now: its arguments are still streaming, and the turn
	// loop uses the head start to warm a sandbox.
	d.announced, d.yielded = true, true
	if !yield(turn.StreamEvent{Type: turn.ToolStart, Name: d.tool.Name}) {
		return breakError{}
	}
	return nil
}

// partial is what is known when a call ends early.
func (d *decoder) partial() turn.Stop {
	stop := turn.Stop{
		Text: d.text.String(),
		Usage: turn.Usage{
			InputTokens:  d.usage.PromptTokens, // already includes cached tokens
			OutputTokens: d.usage.CompletionTokens,
		},
	}
	if det := d.usage.PromptTokensDetails; det != nil {
		stop.Usage.CachedTokens = det.CachedTokens
	}
	return stop
}

func (d *decoder) finish() (turn.Stop, error) {
	stop := d.partial()
	switch d.finishReason {
	case "content_filter":
		return stop, ErrFiltered
	case "tool_calls":
		if d.tool == nil {
			return stop, nil
		}
		code, err := parseCode(d.toolArgs)
		if err != nil {
			return stop, err
		}
		d.tool.Code = code
		stop.ToolCall = d.tool
	case "stop":
		// Some self-hosted servers report "stop" for a streamed tool call.
		// Honour the call only if its arguments arrived whole; otherwise
		// this is an ordinary reply that happened to end mid-call.
		if d.tool == nil {
			return stop, nil
		}
		if code, err := parseCode(d.toolArgs); err == nil {
			d.tool.Code = code
			stop.ToolCall = d.tool
		}
	}
	// "length" and anything else is a plain reply. A tool call cut off by
	// the token limit has truncated code and must not run.
	return stop, nil
}

func parseCode(raw []byte) (string, error) {
	var args streamedArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("%w: %w", ErrBadToolInput, err)
	}
	if args.Code == nil {
		return "", ErrBadToolInput
	}
	return *args.Code, nil
}
