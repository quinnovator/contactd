// Package anthropic is the provider profile for the Anthropic API: which
// models it serves, what each can do, and how the Messages codec is set up
// to reach them.
//
// The profile holds no wire code. Requests are rendered and replies read by
// internal/llm/wire/messages; what is specific to this upstream is the
// model table and the dialect below.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/wire/messages"
	"github.com/quinnovator/contactd/internal/plan"
)

const (
	// ID is the provider's name in agent configs.
	ID = "anthropic"

	defaultBaseURL = "https://api.anthropic.com"
	endpointPath   = "/v1/messages"

	apiVersion = "2023-06-01"
	// fallbackBeta gates the "default" form of the fallbacks parameter.
	fallbackBeta = "server-side-fallback-2026-07-01"

	// Thinking counts toward max_tokens on current models even though its
	// text is not returned, so the default leaves room for it.
	defaultMaxOutput = 64_000

	// The figures every model in the table shares.
	contextWindow = 1_000_000
	maxOutput     = 128_000
)

// model is one row of the model table.
type model struct {
	name string
	// defaultEffort is the effort the model runs at when a request names
	// none.
	defaultEffort llm.Effort
	// fallback reports whether the model accepts fallbacks: "default", which
	// has the API re-run a request a safety classifier declined on another
	// model. A model without it either has no fallback or is not documented
	// to take the field.
	fallback bool
	// toolChoice reports whether the model can be made to call a tool. The
	// newer models answer a forced tool_choice ("any", or a named tool) with
	// a 400 and take only "auto" and "none".
	toolChoice bool
}

// models is the model table. The first row is the default model.
func models() []model {
	return []model{
		{name: "claude-opus-5-5", defaultEffort: llm.EffortMedium, fallback: true},
		{name: "claude-sonnet-5-5", defaultEffort: llm.EffortHigh, fallback: true},
		{name: "claude-fable-5-1", defaultEffort: llm.EffortHigh, fallback: true},
		{name: "claude-haiku-5-5", defaultEffort: llm.EffortMedium, toolChoice: true},
	}
}

func lookup(name string) (model, bool) {
	for _, m := range models() {
		if m.name == name {
			return m, true
		}
	}
	return model{}, false
}

func (m model) caps() llm.Capabilities {
	return llm.Capabilities{
		ContextWindow: contextWindow,
		MaxOutput:     maxOutput,

		Cache: llm.CacheBreakpoints,
		// The default lifetime of a cache write; the codec asks for no other.
		CacheTTL:   5 * time.Minute,
		Affinity:   llm.AffinityNone,
		CacheUsage: true,

		Efforts:       []llm.Effort{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax},
		DefaultEffort: m.defaultEffort,
		// Thinking is always on, or can be switched off only at some
		// efforts; neither is a switch the core can rely on.
		ReasoningOff: false,
		// Thinking blocks go back while a tool loop continues. They are
		// bound to the conversation as it stood, so nothing is kept longer.
		Replay: llm.ReplaySameTurn,

		ToolChoice: m.toolChoice,
		// Held to one call a step by disable_parallel_tool_use, which is
		// sent with the "auto" choice every model here accepts.
		SingleToolCall: true,
		// The API has strict schemas, but the codec sends no "strict" field,
		// so no request can ask for one. A capability nothing can exercise
		// is not declared.
		StrictTools:      false,
		ToolErrorFlag:    true,
		SystemMidHistory: false,
	}
}

// Config configures a Provider.
type Config struct {
	APIKey string
	// BaseURL is the API's origin, without the /v1/messages path. It
	// defaults to https://api.anthropic.com; tests and gateways override it.
	BaseURL string
	// NoFallback stops the profile from asking the API to re-run a request
	// that a safety classifier declined on another model. Set it when
	// BaseURL is a gateway that rejects the beta header.
	NoFallback bool
	// HTTPClient defaults to a client with no timeout: a stream lives as
	// long as its context.
	HTTPClient *http.Client
}

// Provider reaches the Anthropic API. It is safe for concurrent use.
type Provider struct {
	hc *http.Client
	// plain is the dialect for models that take no fallback, and fallback
	// the one for models that do. With Config.NoFallback they are the same.
	plain, fallback messages.Dialect
}

var _ llm.Provider = (*Provider)(nil)

// New validates cfg and fills in defaults.
func New(cfg Config) (*Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("anthropic: api key is required")
	}
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("anthropic: base url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, errors.New("anthropic: base url needs a scheme and a host")
	}

	header := http.Header{}
	header.Set("X-Api-Key", cfg.APIKey)
	header.Set("Anthropic-Version", apiVersion)
	plain := messages.Dialect{
		URL:    strings.TrimRight(base, "/") + endpointPath,
		Header: header,
		// output_config.effort alone: thinking is adaptive when the request
		// says nothing about it, which is the only mode these models have.
		Effort:           messages.OutputConfigEffort,
		EagerToolInput:   true,
		DefaultMaxOutput: defaultMaxOutput,
	}
	p := &Provider{hc: cfg.HTTPClient, plain: plain, fallback: plain}
	if p.hc == nil {
		p.hc = &http.Client{}
	}
	if !cfg.NoFallback {
		p.fallback.Header = header.Clone()
		p.fallback.Header.Set("Anthropic-Beta", fallbackBeta)
		p.fallback.Extra = []messages.Field{{Name: "fallbacks", Value: json.RawMessage(`"default"`)}}
	}
	return p, nil
}

// ID returns "anthropic".
func (*Provider) ID() string { return ID }

// Route resolves a model name against the profile's table. "" is the
// default model.
func (*Provider) Route(_ context.Context, name string) (llm.Route, error) {
	if name == "" {
		name = models()[0].name
	}
	m, ok := lookup(name)
	if !ok {
		return llm.Route{}, fmt.Errorf("anthropic: model %q: %w", name, llm.ErrUnknownModel)
	}
	return llm.Route{Provider: ID, Model: m.name, Wire: llm.WireMessages, Caps: m.caps()}, nil
}

// Prepare freezes one call. It refuses a route this profile did not make.
func (p *Provider) Prepare(_ context.Context, r llm.Route, pl *plan.PromptPlan, o llm.Options) (*llm.Frozen, error) {
	m, ok := lookup(r.Model)
	if !ok || r.Provider != ID {
		return nil, fmt.Errorf("anthropic: route %s: %w", r.ID(), llm.ErrUnknownModel)
	}
	d := &p.plain
	if m.fallback {
		d = &p.fallback
	}
	f, err := messages.Prepare(d, r, pl, o)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	return f, nil
}

// Send streams a frozen call.
func (p *Provider) Send(ctx context.Context, f *llm.Frozen) (llm.Stream, error) {
	return messages.Send(ctx, p.hc, f) //nolint:wrapcheck // already an *llm.Error naming the provider
}
