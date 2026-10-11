// Package llm is the boundary between the core and every model provider.
//
// Both sides import it and it imports neither: the turn loop, the planner
// and the actors see only the types here, and a provider sees only a prompt
// plan and call options. Everything provider-specific that the core must act
// on is a declared Capability; the core never branches on a provider's name.
//
// The package holds the contract and the provider-neutral machinery around
// it (routing, admission, retries, cache prediction). It contains no wire
// code: request and stream formats live in internal/llm/wire, and each
// provider's profile in internal/llm/provider.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"slices"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/plan"
)

// Provider is one upstream: an account at one vendor or gateway.
type Provider interface {
	// ID is the provider's stable name, as agent configs spell it.
	ID() string
	// Route resolves a model name to a route and its declared capabilities.
	// An unknown name is an error here, never a silent default chosen by
	// the upstream. model may be "" to ask for the provider's default.
	Route(ctx context.Context, model string) (Route, error)
	// Prepare freezes one model call: the exact request bytes and headers,
	// and what the call is expected to read from the provider's cache. It
	// performs no I/O and is deterministic: the same plan and options always
	// freeze to the same bytes.
	Prepare(ctx context.Context, r Route, p *plan.PromptPlan, o Options) (*Frozen, error)
	// Send streams a frozen request. A retry calls it again with the same
	// value. A failure before the stream starts is returned as an *Error.
	Send(ctx context.Context, f *Frozen) (Stream, error)
}

// Route is one model on one provider, with what that model can do there.
type Route struct {
	Provider string
	Model    string
	Wire     Wire
	Caps     Capabilities
}

// ID names the route. Reasoning payloads are tagged with it, and only a
// route with the same ID is ever handed a payload back.
func (r Route) ID() string { return r.Provider + "/" + r.Model }

// Wire is a request and stream format shared by several providers.
type Wire uint8

const (
	WireChat      Wire = iota + 1 // OpenAI-format Chat Completions
	WireMessages                  // Anthropic-format Messages
	WireResponses                 // OpenAI-format Responses
)

func (w Wire) String() string {
	switch w {
	case WireChat:
		return "chat"
	case WireMessages:
		return "messages"
	case WireResponses:
		return "responses"
	default:
		return "unknown"
	}
}

// Capabilities is everything about a route that the core acts on. Each field
// is a claim the conformance suite tests against fixtures and the probe
// checks against the live endpoint.
type Capabilities struct {
	ContextWindow int
	MaxOutput     int

	Cache CacheMode
	// CacheTTL is how long a written prefix is assumed to stay warm. It
	// drives recall hysteresis and the cache prediction.
	CacheTTL time.Duration
	// Affinity says whether the provider takes a per-contact key that pins a
	// conversation to one backend.
	Affinity Affinity
	// CacheUsage reports whether the provider returns cached-token counts,
	// so that predictions can be checked against them.
	CacheUsage bool

	// Efforts lists the reasoning efforts the route accepts, weakest first.
	// Empty means the route has no effort control.
	Efforts       []Effort
	DefaultEffort Effort
	// ReasoningOff reports whether reasoning can be switched off.
	ReasoningOff bool
	Replay       Replay

	// ToolChoice reports whether tool_choice is honoured.
	ToolChoice bool
	// SingleToolCall reports whether the model can be held to one tool call
	// per step. Where it cannot, the turn loop runs the calls it is given
	// one at a time.
	SingleToolCall bool
	StrictTools    bool
	// ToolErrorFlag reports whether a tool result can be marked as an error.
	ToolErrorFlag bool
	// SystemMidHistory reports whether a system message may follow the
	// first turn.
	SystemMidHistory bool
}

// CacheMode is how a route's prompt cache is driven.
type CacheMode uint8

const (
	// CacheNone: nothing is cached, or nothing is known to be.
	CacheNone CacheMode = iota
	// CacheAutoPrefix: the provider caches by prefix on its own. Any shared
	// prefix can hit.
	CacheAutoPrefix
	// CacheBreakpoints: the request marks where a prefix is written. Only a
	// prefix ending at a breakpoint can hit.
	CacheBreakpoints
)

// Affinity is where a per-contact pinning key goes.
type Affinity uint8

const (
	AffinityNone Affinity = iota
	AffinityHeader
	AffinityBody
)

// Replay is when a route's reasoning payload must be sent back to it.
type Replay uint8

const (
	// ReplayNone: reasoning is never sent back.
	ReplayNone Replay = iota
	// ReplaySameTurn: reasoning is sent back while a tool loop continues,
	// and dropped when the turn ends.
	ReplaySameTurn
	// ReplayAllTurns: reasoning is stored in the log and sent back on every
	// later request.
	ReplayAllTurns
)

// Effort is the provider-neutral reasoning effort scale.
type Effort uint8

const (
	EffortDefault Effort = iota // let the route decide
	EffortNone
	EffortLow
	EffortMedium
	EffortHigh
	EffortXHigh
	EffortMax
)

func (e Effort) String() string {
	switch e {
	case EffortDefault:
		return ""
	case EffortNone:
		return "none"
	case EffortLow:
		return "low"
	case EffortMedium:
		return "medium"
	case EffortHigh:
		return "high"
	case EffortXHigh:
		return "xhigh"
	case EffortMax:
		return "max"
	default:
		return "unknown"
	}
}

// ParseEffort reads an effort name as agent configs spell it.
func ParseEffort(s string) (Effort, error) {
	for e := EffortDefault; e <= EffortMax; e++ {
		if e.String() == s {
			return e, nil
		}
	}
	return EffortDefault, fmt.Errorf("unknown effort %q (want none, low, medium, high, xhigh or max)", s)
}

// Nearest maps a requested effort onto what a route accepts: the requested
// value if accepted, otherwise the closest accepted one, preferring the
// stronger of two equally close. EffortNone is honoured only where reasoning
// can be switched off; elsewhere it becomes the weakest accepted effort.
// EffortDefault, or a route with no effort control, yields EffortDefault,
// which tells a codec to send nothing.
func (c *Capabilities) Nearest(want Effort) Effort {
	if want == EffortDefault || (len(c.Efforts) == 0 && !c.ReasoningOff) {
		return EffortDefault
	}
	if want == EffortNone {
		if c.ReasoningOff {
			return EffortNone
		}
		if len(c.Efforts) == 0 {
			return EffortDefault
		}
		return c.Efforts[0]
	}
	if len(c.Efforts) == 0 {
		return EffortDefault
	}
	best := c.Efforts[0]
	for _, e := range c.Efforts[1:] {
		if abs(int(e)-int(want)) <= abs(int(best)-int(want)) {
			best = e
		}
	}
	return best
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Options are the per-call choices the core makes.
type Options struct {
	// Affinity is a keyed hash of the contact, or "". It is the only thing
	// about a contact a provider ever sees.
	Affinity string
	// Effort is the reasoning effort the contact asks for, on the neutral
	// scale. The provider sends the nearest value its route accepts.
	Effort Effort
	// MaxOutput caps output tokens; 0 uses the route's default.
	MaxOutput int
	// Prev holds the prefix marks of the last request sent for this contact
	// on this route while its cache is presumed warm, or nil. Prepare uses
	// it to predict what this request will read from cache.
	Prev []plan.Mark
}

// Frozen is one model call, fixed: nothing about it is decided after this
// point. The same value can be sent again, and its bytes can be reproduced
// from the log.
type Frozen struct {
	Route  Route
	URL    string
	Header http.Header
	Body   []byte
	// Effort is what was actually put on the wire after mapping.
	Effort Effort
	// Marks are the plan's prefix marks, and Predict the input tokens this
	// request is expected to read from the provider's cache.
	Marks   []plan.Mark
	Predict int
}

// Stream is one in-flight model call.
//
// Events yields deltas until the model stops, the context is cancelled, or
// the call fails. It may be ranged over once; breaking out early releases
// the connection. Result is valid only after Events has returned.
type Stream interface {
	Events() iter.Seq[Event]
	Result() (Result, error)
}

// EventKind tags an Event.
type EventKind uint8

const (
	// TextDelta carries a fragment of reply text.
	TextDelta EventKind = iota + 1
	// ReasoningDelta carries a fragment of visible reasoning, when the
	// route exposes any.
	ReasoningDelta
	// ToolStart announces a tool call as soon as its name is known, before
	// its arguments have finished streaming. Tool is set.
	ToolStart
)

// Event is one item from a model stream.
type Event struct {
	Kind EventKind
	Text string
	Tool string
}

// Result is how a model call ended.
type Result struct {
	// Text is the reply text of this call.
	Text string
	// ToolCalls are the calls the model made, in order. The codec sets ID,
	// Name, Sibling, and Code or Args.
	ToolCalls []core.ToolCall
	// Reasoning is the provider's payload for this assistant turn, to be
	// returned to the same route untouched, or nil.
	Reasoning *core.Reasoning
	Stop      StopReason
	Usage     Usage
}

// StopReason is why a model call ended, normalized across providers.
type StopReason uint8

const (
	StopEnd      StopReason = iota + 1 // the model finished its reply
	StopTool                           // the model stopped to run tools
	StopLength                         // the output cap was reached
	StopRefused                        // the model or a safety layer declined
	StopFiltered                       // a content filter cut the reply
)

func (s StopReason) String() string {
	switch s {
	case StopEnd:
		return "end"
	case StopTool:
		return "tool"
	case StopLength:
		return "length"
	case StopRefused:
		return "refused"
	case StopFiltered:
		return "filtered"
	default:
		return "unknown"
	}
}

// Usage is a provider's token accounting, normalized. Raw is the provider's
// own usage object, kept for audit and never branched on.
type Usage struct {
	Input     int // every input token, cached or not
	Cached    int // input tokens read from the provider's cache
	Output    int // every output token, reasoning included
	Reasoning int // output tokens spent on reasoning, when reported
	Raw       json.RawMessage
}

// ErrorClass is the provider-neutral kind of a failed call.
type ErrorClass uint8

const (
	ErrUnknown ErrorClass = iota
	// Retryable before the first event:
	ErrRateLimited
	ErrOverloaded
	ErrTransport
	// Not retryable:
	ErrContextOverflow
	ErrQuota
	ErrAuth
	ErrBadRequest
)

func (c ErrorClass) String() string {
	switch c {
	case ErrRateLimited:
		return "rate_limited"
	case ErrOverloaded:
		return "overloaded"
	case ErrTransport:
		return "transport"
	case ErrContextOverflow:
		return "context_overflow"
	case ErrQuota:
		return "quota"
	case ErrAuth:
		return "auth"
	case ErrBadRequest:
		return "bad_request"
	case ErrUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// Retryable reports whether a call that failed this way may be sent again.
func (c ErrorClass) Retryable() bool {
	return c == ErrRateLimited || c == ErrOverloaded || c == ErrTransport
}

// Error is a failed call, classified. Providers return it from Send and from
// Stream.Result.
type Error struct {
	Class      ErrorClass
	RetryAfter time.Duration // the provider's own hint, or 0
	Status     int           // HTTP status, or 0
	Provider   string
	Err        error
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s: %s (HTTP %d): %v", e.Provider, e.Class, e.Status, e.Err)
	}
	return fmt.Sprintf("%s: %s: %v", e.Provider, e.Class, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// ClassOf returns the class of err, or ErrUnknown when it is not an *Error.
func ClassOf(err error) ErrorClass {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Class
	}
	return ErrUnknown
}

// Sentinel errors.
var (
	// ErrUnknownProvider means an agent config names a provider that is not
	// configured on this deployment.
	ErrUnknownProvider = errors.New("unknown provider")
	// ErrUnknownModel means a provider does not serve the named model.
	ErrUnknownModel = errors.New("unknown model")
	// ErrUnsupportedWire means a model is only reachable over a wire format
	// that has no codec yet.
	ErrUnsupportedWire = errors.New("wire format not supported")
)

// PredictCached estimates how many input tokens of a request a provider's
// cache will serve, given the marks of the previous request and of this one.
//
// With automatic prefix caching any shared prefix can hit, so the answer is
// the longest run of marks the two requests have in common. With breakpoints
// only a prefix that the previous request wrote can hit, so the run must end
// on one of its breakpoints.
func PredictCached(mode CacheMode, prev, cur []plan.Mark) int {
	if mode == CacheNone || len(prev) == 0 {
		return 0
	}
	n := 0
	for n < len(prev) && n < len(cur) && prev[n].Hash == cur[n].Hash {
		n++
	}
	switch mode {
	case CacheAutoPrefix:
		if n == 0 {
			return 0
		}
		return cur[n-1].Tokens
	case CacheBreakpoints:
		for i := n - 1; i >= 0; i-- {
			if prev[i].Breakpoint {
				return cur[i].Tokens
			}
		}
		return 0
	case CacheNone:
		return 0
	default:
		return 0
	}
}

// Accepts reports whether e is among the efforts a route accepts.
func (c *Capabilities) Accepts(e Effort) bool { return slices.Contains(c.Efforts, e) }
