// Package chat is the codec for the OpenAI-format Chat Completions wire:
// it freezes a prompt plan into a streaming request and folds the response
// stream back into a result.
//
// Many upstreams speak this format, each with its own spelling of the parts
// the format never standardized: reasoning effort, reasoning replay, the
// pinning key, routing extras. A profile describes its upstream with a
// Dialect, and declares what a route can do in its Capabilities; the codec
// acts on those two and never on a provider's name.
//
// The codec does not retry, summarize or pick a replica. Retries and
// admission belong to llm.Client above it, and replica choice to the profile.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/wire"
	"github.com/quinnovator/contactd/internal/plan"
)

// Dialect is what a profile tells the codec about one upstream. It is plain
// data: a profile builds one per upstream (or per model, where a field such
// as Extra differs by model) and passes it to Prepare. Everything about a
// route that the core also acts on is not here but in llm.Capabilities.
type Dialect struct {
	// URL is the full endpoint URL, for example
	// https://openrouter.ai/api/v1/chat/completions.
	URL string
	// Header holds the static request headers: authorization, user agent,
	// app identification. Prepare adds Content-Type and Accept unless they
	// are set here.
	Header http.Header
	// AffinityHeader names the header that carries Options.Affinity on a
	// route whose Capabilities.Affinity is llm.AffinityHeader. Empty where
	// the key does not travel in a header.
	AffinityHeader string
	// AffinityField names the top-level body field that carries
	// Options.Affinity on a route whose Capabilities.Affinity is
	// llm.AffinityBody ("session_id" on OpenRouter). Empty where the key
	// does not travel in the body.
	AffinityField string
	// Effort says how a reasoning effort is spelled. Which efforts a route
	// accepts is Capabilities.Efforts and ReasoningOff; the mapped value is
	// sent under its llm.Effort name. llm.EffortDefault sends nothing in
	// every style.
	Effort EffortStyle
	// Reasoning says in which field of an assistant message a stored
	// reasoning payload is sent back. It matters only on a route whose
	// Capabilities.Replay is not llm.ReplayNone. Reading reasoning from a
	// stream needs no dialect: every known spelling is read.
	Reasoning ReasoningStyle
	// Extra lists fixed top-level body fields, appended to every request
	// after the codec's own fields and in this order (OpenRouter's
	// "provider" routing object). A name the codec already wrote is an
	// error.
	Extra []Field
	// DefaultMaxOutput caps output tokens when Options.MaxOutput is 0. Zero
	// sends no cap and leaves it to the upstream. Whatever is chosen is held
	// to Capabilities.MaxOutput, which is a route's ceiling and never its
	// default: a model that can write 384,000 tokens should not be asked
	// for that many on every call, and an upstream that adds the cap to the
	// prompt would refuse a long prompt outright.
	DefaultMaxOutput int
	// MaxTokensField is the body field that carries the output cap:
	// "max_tokens" (the default, when empty) or "max_completion_tokens".
	MaxTokensField string
}

// Field is one top-level body field with its value already encoded as JSON.
type Field struct {
	Name  string
	Value json.RawMessage
}

// EffortStyle is how an upstream spells a reasoning effort.
type EffortStyle uint8

const (
	// EffortUnsent means the upstream has no effort parameter. It is only
	// valid on routes that declare no effort control.
	EffortUnsent EffortStyle = iota
	// EffortTopLevel sends reasoning_effort: "<name>", with "none" to switch
	// reasoning off (Ollama's /v1, plain OpenAI-compatible servers).
	EffortTopLevel
	// EffortReasoningObject sends reasoning: {"effort": "<name>"}, with
	// "none" to switch reasoning off (OpenRouter).
	EffortReasoningObject
	// EffortThinkingToggle sends thinking: {"type": "enabled"} together with
	// reasoning_effort: "<name>", and thinking: {"type": "disabled"} alone
	// to switch reasoning off (DeepSeek's Chat Completions endpoint).
	EffortThinkingToggle
)

// ReasoningStyle is how an upstream takes a model's reasoning back.
type ReasoningStyle uint8

const (
	// ReasoningUnsent never sends reasoning back.
	ReasoningUnsent ReasoningStyle = iota
	// ReasoningContent sends the reasoning text back as the assistant
	// message's reasoning_content string (DeepSeek).
	ReasoningContent
	// ReasoningDetails sends the reasoning_details array back, every item
	// whole and in the order received (OpenRouter). A reply that carried
	// reasoning text and no details is sent back as the message's reasoning
	// string, which OpenRouter documents as the form for such models.
	ReasoningDetails
)

// Sentinel errors, checked with errors.Is.
var (
	// ErrIncomplete means the stream ended, or the caller stopped reading,
	// before the model finished.
	ErrIncomplete = errors.New("chat: stream ended before the model stopped")
	// ErrBadToolArgs means the model finished a tool call whose arguments
	// are not what the tool takes: not JSON, or not an object with a string
	// "code" for a tool with the default schema.
	ErrBadToolArgs = errors.New("chat: malformed tool call arguments")
	// ErrNotDrained means Result was called before Events had returned.
	ErrNotDrained = errors.New("chat: result read before events were drained")
)

const defaultMaxTokensField = "max_tokens"

// Prepare freezes one model call for the upstream d describes. It performs
// no I/O and is deterministic: the same dialect, route, plan and options
// always freeze to the same bytes.
func Prepare(d *Dialect, r llm.Route, p *plan.PromptPlan, o llm.Options) (*llm.Frozen, error) {
	switch {
	case d == nil || d.URL == "":
		return nil, errors.New("chat: the dialect names no endpoint")
	case p == nil:
		return nil, errors.New("chat: nil plan")
	case r.Model == "":
		return nil, errors.New("chat: the route names no model")
	}

	msgs, err := renderMessages(d, r, p)
	if err != nil {
		return nil, err
	}
	var body object
	body.add("model", r.Model)
	body.add("messages", msgs)
	if len(p.Tools) > 0 {
		body.add("tools", renderTools(p.Tools))
		if r.Caps.SingleToolCall {
			body.add("parallel_tool_calls", false)
		}
	}
	n := firstPositive(o.MaxOutput, d.DefaultMaxOutput)
	if ceiling := r.Caps.MaxOutput; ceiling > 0 {
		n = min(n, ceiling)
	}
	if n > 0 {
		field := d.MaxTokensField
		if field == "" {
			field = defaultMaxTokensField
		}
		body.add(field, n)
	}
	body.add("stream", true)
	// Without this a stream carries no usage at all.
	body.add("stream_options", streamOptions{IncludeUsage: true})

	effort := r.Caps.Nearest(o.Effort)
	if err := addEffort(&body, d.Effort, effort); err != nil {
		return nil, err
	}

	header := d.Header.Clone()
	if header == nil {
		header = http.Header{}
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json")
	}
	if header.Get("Accept") == "" {
		header.Set("Accept", "text/event-stream")
	}
	if o.Affinity != "" {
		switch r.Caps.Affinity {
		case llm.AffinityNone:
			// The route takes no key: it is sent nowhere.
		case llm.AffinityHeader:
			if d.AffinityHeader == "" {
				return nil, errors.New("chat: the route takes its affinity key in a header, but the dialect names none")
			}
			header.Set(d.AffinityHeader, o.Affinity)
		case llm.AffinityBody:
			if d.AffinityField == "" {
				return nil, errors.New("chat: the route takes its affinity key in the body, but the dialect names no field")
			}
			body.add(d.AffinityField, o.Affinity)
		}
	}
	for _, f := range d.Extra {
		if !json.Valid(f.Value) {
			return nil, fmt.Errorf("chat: extra field %q is not valid JSON", f.Name)
		}
		body.raw(f.Name, f.Value)
	}
	raw, err := body.bytes()
	if err != nil {
		return nil, err
	}

	marks := p.PrefixMarks()
	return &llm.Frozen{
		Route:   r,
		URL:     d.URL,
		Header:  header,
		Body:    raw,
		Effort:  effort,
		Marks:   marks,
		Predict: llm.PredictCached(r.Caps.Cache, o.Prev, marks),
	}, nil
}

// Send streams a frozen request. A response other than 2xx is returned here
// as an *llm.Error; anything that goes wrong once the stream has started is
// reported by the stream's Result.
//
// It needs nothing but the frozen request: reasoning and usage are read in
// every spelling the format's dialects use, so a retry or a replay from the
// log can send the same value again without its dialect.
//
// hc should have no overall timeout, and no first-byte timeout shorter than
// the upstream's queue limit: a queued request legitimately waits minutes
// before its first byte. A nil hc is a client with no timeout.
func Send(ctx context.Context, hc *http.Client, f *llm.Frozen) (llm.Stream, error) {
	if f == nil {
		return nil, errors.New("chat: nil request")
	}
	if hc == nil {
		hc = &http.Client{}
	}
	//nolint:bodyclose // closed by consume, or by Result when the stream is never read
	resp, err := wire.Post(ctx, hc, f, refine)
	if err != nil {
		return nil, err //nolint:wrapcheck // already an *llm.Error, or the context's error with the provider named
	}
	return &stream{
		// The closure carries ctx so the stream does not have to hold it.
		run: func(yield func(llm.Event) bool) (llm.Result, error) {
			return consume(ctx, f, resp.Body, yield)
		},
		abandon: func() { _ = resp.Body.Close() },
	}, nil
}

func firstPositive(ns ...int) int {
	for _, n := range ns {
		if n > 0 {
			return n
		}
	}
	return 0
}

// addEffort writes the mapped effort in the dialect's spelling. EffortDefault
// writes nothing, so a request that asks for no effort is byte-identical to
// one on a route with no effort control.
func addEffort(body *object, style EffortStyle, e llm.Effort) error {
	if e == llm.EffortDefault {
		return nil
	}
	switch style {
	case EffortTopLevel:
		body.add("reasoning_effort", e.String())
	case EffortReasoningObject:
		body.add("reasoning", effortObject{Effort: e.String()})
	case EffortThinkingToggle:
		if e == llm.EffortNone {
			body.add("thinking", thinkingToggle{Type: "disabled"})
			return nil
		}
		body.add("thinking", thinkingToggle{Type: "enabled"})
		body.add("reasoning_effort", e.String())
	case EffortUnsent:
		return fmt.Errorf("chat: the route accepts effort %q, but the dialect has no way to spell one", e)
	default:
		return fmt.Errorf("chat: unknown effort style %d", style)
	}
	return nil
}
