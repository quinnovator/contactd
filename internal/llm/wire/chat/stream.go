package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"slices"
	"strconv"
	"strings"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/wire"
)

// stream is one in-flight call. It starts no goroutine: Events reads the
// response body on the caller's goroutine, and cancelling the request context
// is what unblocks a read.
type stream struct {
	run     func(yield func(llm.Event) bool) (llm.Result, error)
	abandon func()
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
		s.started, s.done, s.err = true, true, ErrNotDrained
		s.abandon()
	}
	if !s.done {
		return llm.Result{}, ErrNotDrained
	}
	return s.res, s.err
}

// errBreak means the consumer left the range loop.
var errBreak = errors.New("consumer stopped reading")

// consume reads body to the end, yielding deltas, and closes it on every
// path out. Nothing here is retried: by the time a stream exists the caller
// may have seen output, and sending again would repeat it.
func consume(ctx context.Context, f *llm.Frozen, body io.ReadCloser, yield func(llm.Event) bool) (llm.Result, error) {
	// Also covers a panic in the consumer's loop body.
	defer func() { _ = body.Close() }()
	d := decoder{provider: f.Route.Provider}
	err := d.drain(body, yield)
	switch {
	case err == nil:
		return d.finish(f)
	case errors.Is(err, errBreak):
		return d.partial(), fmt.Errorf("%s: %w", f.Route.Provider, ErrIncomplete)
	case ctx.Err() != nil:
		// A cancelled read surfaces as a transport error; say what it was.
		return d.partial(), fmt.Errorf("%s: stream: %w", f.Route.Provider, ctx.Err())
	default:
		return d.partial(), err
	}
}

// Stream chunks. Only the fields the codec acts on are declared; the rest
// (role, logprobs, native_finish_reason) are ignored.

type chunk struct {
	errorEnvelope

	Choices []chunkChoice   `json:"choices"`
	Usage   json.RawMessage `json:"usage"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason string     `json:"finish_reason"`
}

// chunkDelta reads reasoning under every name a dialect of this format uses:
// reasoning_content (DeepSeek, vLLM), reasoning (OpenRouter, others), and
// OpenRouter's structured reasoning_details. Reading all of them is what
// lets Send work from a frozen request alone.
type chunkDelta struct {
	Content          string            `json:"content"`
	Refusal          text              `json:"refusal"`
	Reasoning        text              `json:"reasoning"`
	ReasoningContent text              `json:"reasoning_content"`
	ReasoningDetails []json.RawMessage `json:"reasoning_details"`
	ToolCalls        []toolCallDelta   `json:"tool_calls"`
}

// toolCallDelta is one piece of a tool call. Index says which call it
// belongs to; the ID and name come once, the arguments in fragments.
type toolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string    `json:"name"`
		Arguments arguments `json:"arguments"`
	} `json:"function"`
}

// text is a string field whose shape is not pinned down by any document: a
// value that is not a string is read as empty instead of failing the chunk.
type text string

func (t *text) UnmarshalJSON(raw []byte) error {
	if s, ok := jsonString(raw); ok {
		*t = text(s)
	}
	return nil
}

// arguments is a fragment of a call's arguments: a string per the format,
// though some servers send the finished object itself.
type arguments string

func (a *arguments) UnmarshalJSON(raw []byte) error {
	switch s, ok := jsonString(raw); {
	case ok:
		*a = arguments(s)
	case !isNull(raw):
		*a = arguments(raw)
	}
	return nil
}

// usage reads the token counts in both spellings in use: OpenAI's nested
// prompt_tokens_details.cached_tokens (also OpenRouter's) and DeepSeek's
// flat prompt_cache_hit_tokens.
type usage struct {
	PromptTokens        int `json:"prompt_tokens"` // already includes cached tokens
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	PromptCacheHitTokens *int `json:"prompt_cache_hit_tokens"`
}

// pendingCall is a tool call still streaming.
type pendingCall struct {
	id        string
	name      string
	args      []byte
	announced bool
}

// decoder folds stream chunks into the final Result.
type decoder struct {
	provider string

	text         strings.Builder
	reasoning    reasoning
	refused      bool
	finishReason string

	// calls holds the tool calls in the order the model opened them, and
	// byIndex finds the one a delta's wire index refers to.
	calls   []*pendingCall
	byIndex map[int]*pendingCall

	usage llm.Usage
}

func (d *decoder) drain(body io.Reader, yield func(llm.Event) bool) error {
	sse := wire.NewSSEReader(body)
	for {
		ev, err := sse.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return d.broken(err)
			}
			if d.finishReason != "" {
				return nil // the server skipped [DONE]; the call still finished
			}
			return d.broken(ErrIncomplete)
		}
		data := bytes.TrimSpace(ev.Data)
		if len(data) == 0 {
			continue
		}
		if string(data) == "[DONE]" {
			if d.finishReason == "" {
				return d.broken(ErrIncomplete)
			}
			return nil
		}
		var ck chunk
		if err := json.Unmarshal(data, &ck); err != nil {
			return d.broken(fmt.Errorf("decode chunk: %w", err))
		}
		if err := d.handle(&ck, yield); err != nil {
			return err
		}
	}
}

// broken reports a stream that cannot be read to its end.
func (d *decoder) broken(err error) error {
	return &llm.Error{Class: llm.ErrTransport, Provider: d.provider, Err: err}
}

func (d *decoder) handle(ck *chunk, yield func(llm.Event) bool) error {
	if err := ck.streamError(d.provider); err != nil {
		return err
	}
	// With include_usage the totals come in a last chunk that has no choices.
	d.readUsage(ck.Usage)
	for i := range ck.Choices {
		ch := &ck.Choices[i]
		if ch.Index != 0 {
			continue
		}
		thought, err := d.reasoning.add(&ch.Delta)
		if err != nil {
			return d.broken(err)
		}
		if thought != "" && !yield(llm.Event{Kind: llm.ReasoningDelta, Text: thought}) {
			return errBreak
		}
		said := ch.Delta.Content
		if refusal := string(ch.Delta.Refusal); refusal != "" {
			// A refusal is the reply the contact gets; it only arrives in a
			// field of its own.
			d.refused = true
			said += refusal
		}
		if said != "" {
			d.text.WriteString(said)
			if !yield(llm.Event{Kind: llm.TextDelta, Text: said}) {
				return errBreak
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

func (d *decoder) toolDelta(td *toolCallDelta, yield func(llm.Event) bool) error {
	call := d.byIndex[td.Index]
	// Some servers number every call 0 and send each one whole. A second ID
	// under the same index is then a second call, not more of the first.
	if call == nil || (td.ID != "" && call.id != "" && td.ID != call.id) {
		call = &pendingCall{}
		if d.byIndex == nil {
			d.byIndex = map[int]*pendingCall{}
		}
		d.byIndex[td.Index] = call
		d.calls = append(d.calls, call)
	}
	if call.id == "" {
		call.id = td.ID
	}
	call.args = append(call.args, td.Function.Arguments...)
	if td.Function.Name == "" {
		return nil
	}
	call.name += td.Function.Name
	if call.announced {
		return nil
	}
	// Announce the tool now: its arguments are still streaming, and the turn
	// loop uses the head start to warm a sandbox.
	call.announced = true
	if !yield(llm.Event{Kind: llm.ToolStart, Tool: call.name}) {
		return errBreak
	}
	return nil
}

func (d *decoder) readUsage(raw json.RawMessage) {
	if len(raw) == 0 || isNull(raw) {
		return
	}
	// The object is kept whatever it holds; counts that do not decode stay
	// zero rather than failing a reply over its accounting.
	var u usage
	err := json.Unmarshal(raw, &u)
	d.usage = llm.Usage{
		Input:  u.PromptTokens,
		Output: u.CompletionTokens,
		Raw:    slices.Clone(raw),
	}
	cached := u.PromptCacheHitTokens
	if det := u.PromptTokensDetails; det != nil && det.CachedTokens != nil {
		cached = det.CachedTokens
	}
	if err == nil && cached != nil && *cached >= 0 && *cached <= u.PromptTokens {
		d.usage.Cached = *cached
		d.usage.CacheReported = true
	}
	if det := u.CompletionTokensDetails; det != nil {
		d.usage.Reasoning = det.ReasoningTokens
	}
}

// partial is what is known when a call ends early.
func (d *decoder) partial() llm.Result {
	return llm.Result{Text: d.text.String(), Usage: d.usage}
}

func (d *decoder) finish(f *llm.Frozen) (llm.Result, error) {
	res := d.partial()
	switch d.finishReason {
	case "error":
		// OpenRouter's normalized reason for an upstream that failed mid-reply
		// without sending an error object.
		return res, &llm.Error{
			Class:    llm.ErrUnknown,
			Provider: d.provider,
			Err:      errors.New("the upstream ended the reply with finish_reason error"),
		}
	case "length":
		// A tool call cut off by the token limit has truncated arguments and
		// must not run.
		res.Stop = llm.StopLength
	case "content_filter":
		res.Stop = llm.StopFiltered
	default:
		calls, err := d.toolCalls(f)
		switch {
		case len(d.calls) == 0:
			res.Stop = llm.StopEnd
		case err == nil:
			res.ToolCalls, res.Stop = calls, llm.StopTool
		case d.finishReason == "tool_calls":
			return res, &llm.Error{Class: llm.ErrUnknown, Provider: d.provider, Err: err}
		default:
			// Some self-hosted servers report "stop" for a streamed tool
			// call, so a whole call is honoured above whatever the reason.
			// A call that did not arrive whole under "stop" is an ordinary
			// reply that happened to end mid-call.
			res.Stop = llm.StopEnd
		}
		if d.refused && res.Stop == llm.StopEnd {
			res.Stop = llm.StopRefused
		}
	}
	if f.Route.Caps.Replay == llm.ReplayNone {
		return res, nil
	}
	data, err := d.reasoning.payload()
	if err != nil {
		return res, &llm.Error{Class: llm.ErrUnknown, Provider: d.provider, Err: err}
	}
	if data != nil {
		res.Reasoning = &core.Reasoning{Route: f.Route.ID(), Data: data}
	}
	return res, nil
}

// toolCalls finishes the streamed calls, in the order the model made them.
func (d *decoder) toolCalls(f *llm.Frozen) ([]core.ToolCall, error) {
	if len(d.calls) == 0 {
		return nil, nil
	}
	kinds := toolKinds(f.Body)
	calls := make([]core.ToolCall, len(d.calls))
	for i, pc := range d.calls {
		if pc.name == "" {
			return nil, fmt.Errorf("%w: call %d has no tool name", ErrBadToolArgs, i)
		}
		call := core.ToolCall{ID: pc.id, Name: pc.name, Sibling: i}
		if call.ID == "" {
			// A result is matched to its call by ID, so every call needs one.
			call.ID = "call_" + strconv.Itoa(i)
		}
		var err error
		if call.Code, call.Args, err = parseArgs(kinds[pc.name], pc.args); err != nil {
			return nil, fmt.Errorf("%w: tool %q: %w", ErrBadToolArgs, pc.name, err)
		}
		calls[i] = call
	}
	return calls, nil
}

// toolKind is how a declared tool takes its arguments.
type toolKind uint8

const (
	// toolUndeclared: the model named a tool the request did not offer.
	toolUndeclared toolKind = iota
	// toolCode: the default schema; the call is decoded into ToolCall.Code.
	toolCode
	// toolArgs: the tool's own schema; the call is kept raw in ToolCall.Args.
	toolArgs
)

// toolKinds reads back, from the frozen request itself, which tools use the
// default schema. The request is the one thing Send is guaranteed to have.
func toolKinds(body []byte) map[string]toolKind {
	var req struct {
		Tools []tool `json:"tools"`
	}
	// The body was encoded by Prepare; if it does not decode, every tool is
	// undeclared and each call is judged by its own shape.
	_ = json.Unmarshal(body, &req)
	kinds := make(map[string]toolKind, len(req.Tools))
	for _, t := range req.Tools {
		kind := toolArgs
		if string(t.Function.Parameters) == codeSchema {
			kind = toolCode
		}
		kinds[t.Function.Name] = kind
	}
	return kinds
}

func parseArgs(kind toolKind, raw []byte) (code string, args json.RawMessage, err error) {
	raw = bytes.TrimSpace(raw)
	switch kind {
	case toolCode:
		code, err = parseCode(raw)
		return code, nil, err
	case toolArgs:
		return "", rawArgs(raw), validArgs(raw)
	case toolUndeclared:
		// The turn loop rejects the call by name; keep whatever shape it has.
		if code, err = parseCode(raw); err == nil {
			return code, nil, nil
		}
		return "", rawArgs(raw), validArgs(raw)
	default:
		return "", nil, fmt.Errorf("unknown tool kind %d", kind)
	}
}

func parseCode(raw []byte) (string, error) {
	var args struct {
		Code *string `json:"code"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode arguments: %w", err)
	}
	if args.Code == nil {
		return "", errors.New(`arguments have no string "code"`)
	}
	return *args.Code, nil
}

// rawArgs keeps a call's arguments as written. A tool that takes none is
// often called with an empty string, which stands for the empty object.
func rawArgs(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(noArgs)
	}
	return slices.Clone(raw)
}

func validArgs(raw []byte) error {
	if len(raw) > 0 && !json.Valid(raw) {
		return errors.New("arguments are not valid JSON")
	}
	return nil
}
