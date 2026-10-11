// Package messages is the codec for the Anthropic-format Messages wire: it
// renders a prompt plan as a /v1/messages request and reads the streamed
// reply back.
//
// Several upstreams speak this format and each bends it a little. Whatever a
// provider profile may vary is either a field of Dialect or a declared
// llm.Capabilities value of the route; the codec never asks who it is
// talking to. It does no retrying and no admission: llm.Client does both.
//
// The request body is the cache key, so Prepare is pure and renders a plan
// to the same bytes every time.
package messages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/wire"
	"github.com/quinnovator/contactd/internal/plan"
)

// Dialect is what a profile tells the codec about one upstream. It is plain
// data, read and never changed by the codec, so one value can be shared by
// every call. Behaviour that depends on the model rather than the upstream
// (cache breakpoints, the tool-error flag, one tool call per step, which
// efforts exist, reasoning replay) comes from the route's Capabilities.
type Dialect struct {
	// URL is the full endpoint, such as https://api.anthropic.com/v1/messages.
	// It becomes Frozen.URL unchanged.
	URL string

	// Header holds the static request headers: authentication, the API
	// version, a user agent, beta flags. It is cloned into every Frozen. The
	// codec adds Content-Type and Accept itself.
	Header http.Header

	// AffinityHeader names the header that carries Options.Affinity, or is
	// "" when the upstream has none. The key is sent only when the route
	// declares llm.AffinityHeader and the key is not empty. This wire has no
	// body field for it, so a route declaring llm.AffinityBody sends nothing.
	AffinityHeader string

	// Effort spells a reasoning effort on the wire. It is called with the
	// effort already mapped by Capabilities.Nearest, and never with
	// llm.EffortDefault, for which nothing is sent. Returning the zero
	// EffortFields means this upstream has no spelling for that effort: the
	// request then carries nothing and Frozen.Effort is llm.EffortDefault.
	// nil means OutputConfigEffort.
	Effort func(e llm.Effort) EffortFields

	// Extra lists fixed top-level body fields the codec does not know, such
	// as Anthropic's "fallbacks". They are written in this order straight
	// after "stream" and before every optional field, so adding or removing
	// one never moves the bytes of the tools, the system prompt or the
	// messages relative to each other. A name must not repeat one the codec
	// writes (model, max_tokens, stream, thinking, output_config, tools,
	// tool_choice, system, messages).
	Extra []Field

	// EagerToolInput sets eager_input_streaming on every tool, which makes
	// the upstream stream a call's arguments as they are generated instead
	// of buffering them whole. Leave it off where the field is not accepted.
	EagerToolInput bool

	// DefaultMaxOutput is max_tokens when Options.MaxOutput is 0. When it is
	// 0 too, the route's Capabilities.MaxOutput is used, and failing that a
	// small built-in value. Whatever is chosen is capped at the route's
	// Capabilities.MaxOutput when that is set.
	DefaultMaxOutput int
}

// Field is one extra top-level field of the request body.
type Field struct {
	Name string
	// Value is the field's JSON value, sent as written.
	Value json.RawMessage
}

// EffortFields is how one effort is written into a request. A zero member
// is left out of the body.
type EffortFields struct {
	// Thinking is the JSON value of the top-level "thinking" field, such as
	// {"type":"disabled"}.
	Thinking json.RawMessage
	// OutputEffort is the value of output_config.effort, such as "high".
	OutputEffort string
}

// OutputConfigEffort is the spelling most upstreams of this wire share: an
// effort goes out under its neutral name as output_config.effort, and
// llm.EffortNone switches reasoning off with thinking: {"type": "disabled"}.
// Which names a route accepts is for its Capabilities to say; this function
// only spells what it is given.
func OutputConfigEffort(e llm.Effort) EffortFields {
	switch e {
	case llm.EffortNone:
		return EffortFields{Thinking: json.RawMessage(`{"type":"disabled"}`)}
	case llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax:
		return EffortFields{OutputEffort: e.String()}
	case llm.EffortDefault:
		return EffortFields{}
	default:
		return EffortFields{}
	}
}

// Sentinel errors, checked with errors.Is. From a stream they arrive wrapped
// in an *llm.Error.
var (
	// ErrIncomplete means the stream ended before the model stopped.
	ErrIncomplete = errors.New("messages: stream ended before the model stopped")
	// ErrAbandoned means the caller stopped reading the stream, or asked for
	// its result without reading it.
	ErrAbandoned = errors.New("messages: stream was not read to the end")
	// ErrBadToolInput means a tool call's arguments were not usable: not
	// JSON, or, for a tool with the default schema, not an object with a
	// string "code". The arguments may have been cut off, so nothing of the
	// call is returned.
	ErrBadToolInput = errors.New("messages: tool input is not usable")
)

// fallbackMaxOutput is max_tokens when neither the options, the dialect nor
// the route names one. The field is required on this wire.
const fallbackMaxOutput = 4096

// Prepare freezes one model call for the upstream d describes. It performs
// no I/O and is deterministic: the same dialect, route, plan and options
// always give the same bytes.
func Prepare(d *Dialect, r llm.Route, p *plan.PromptPlan, o llm.Options) (*llm.Frozen, error) {
	if d == nil {
		return nil, errors.New("messages: nil dialect")
	}
	if p == nil {
		return nil, errors.New("messages: nil plan")
	}

	effort := r.Caps.Nearest(o.Effort)
	var spelled EffortFields
	if effort != llm.EffortDefault {
		spell := d.Effort
		if spell == nil {
			spell = OutputConfigEffort
		}
		spelled = spell(effort)
		if len(spelled.Thinking) == 0 && spelled.OutputEffort == "" {
			effort = llm.EffortDefault // nothing went on the wire
		}
	}

	body, err := render(d, r, p, maxOutput(d, r, o), spelled)
	if err != nil {
		return nil, err
	}

	header := d.Header.Clone()
	if header == nil {
		header = http.Header{}
	}
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "text/event-stream")
	if d.AffinityHeader != "" && o.Affinity != "" && r.Caps.Affinity == llm.AffinityHeader {
		header.Set(d.AffinityHeader, o.Affinity)
	}

	marks := p.PrefixMarks()
	return &llm.Frozen{
		Route:   r,
		URL:     d.URL,
		Header:  header,
		Body:    body,
		Effort:  effort,
		Marks:   marks,
		Predict: llm.PredictCached(r.Caps.Cache, o.Prev, marks),
	}, nil
}

func maxOutput(d *Dialect, r llm.Route, o llm.Options) int {
	n := o.MaxOutput
	if n <= 0 {
		n = d.DefaultMaxOutput
	}
	if n <= 0 {
		n = r.Caps.MaxOutput
	}
	if n <= 0 {
		n = fallbackMaxOutput
	}
	if limit := r.Caps.MaxOutput; limit > 0 && n > limit {
		n = limit
	}
	return n
}

// Send streams a request frozen by Prepare. It returns once the upstream has
// accepted the request; a response outside 2xx is returned here as an
// *llm.Error, and later failures come from the stream's Result.
//
// It sets no deadline of its own. A request can sit in the upstream's queue
// for minutes before its first byte, kept alive by comment lines; the
// context is the only thing that ends a wait.
func Send(ctx context.Context, hc *http.Client, f *llm.Frozen) (llm.Stream, error) {
	if f == nil {
		return nil, errors.New("messages: nil request")
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	//nolint:bodyclose // closed by consume, or by Result when the stream is never read
	resp, err := wire.Post(ctx, hc, f, refine)
	if err != nil {
		return nil, err //nolint:wrapcheck // already an *llm.Error, or the context's error, naming the provider
	}
	d := &decoder{route: f.Route, tools: declaredTools(f.Body)}
	return &stream{
		// The closure carries ctx so the stream does not have to hold it.
		run: func(yield func(llm.Event) bool) (llm.Result, error) {
			return consume(ctx, resp.Body, d, yield)
		},
		abandon: func() { _ = resp.Body.Close() },
		unread:  &llm.Error{Class: llm.ErrUnknown, Provider: f.Route.Provider, Err: ErrAbandoned},
	}, nil
}
