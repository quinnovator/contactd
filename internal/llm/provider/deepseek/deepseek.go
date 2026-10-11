// Package deepseek is the provider profile for the DeepSeek API: which
// models it serves, what each can do, and how a wire codec is set up to
// reach them.
//
// DeepSeek offers its models over two formats. The default here is its
// Anthropic-format endpoint, the one DeepSeek's own harness uses. The
// facts below were read from DeepSeek's API guides on Oct 11, 2026
// (anthropic_api, thinking_mode, kv_cache).
package deepseek

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/wire/messages"
	"github.com/quinnovator/contactd/internal/plan"
)

const (
	// ID is the provider's name in agent configs.
	ID = "deepseek"

	// WireMessages is the Config.Wire value for the Anthropic-format
	// endpoint. It is the default.
	WireMessages = "messages"

	messagesBaseURL = "https://api.deepseek.com/anthropic"
	messagesPath    = "/v1/messages"
	// The endpoint ignores the version header; it is sent because every
	// client of this format sends it.
	apiVersion = "2023-06-01"

	// defaultMaxOutput is modest on purpose. The models accept far more,
	// and an agent that needs it says so in its config.
	defaultMaxOutput = 8192

	// Both models list the same sizes.
	contextWindow = 1_000_000
	maxOutput     = 384_000
)

// models is the model table. The first name is the default model.
//
// deepseek-flash (DeepSeek-V4.1-Flash) takes text and images;
// deepseek-v4-pro takes text only. Prompts are text only today, so the
// difference has nothing to act on yet.
func models() []string {
	return []string{"deepseek-flash", "deepseek-v4-pro"}
}

func known(name string) bool { return slices.Contains(models(), name) }

// codec is one wire format's way of reaching DeepSeek: the capabilities its
// endpoint gives the models, and how a call is frozen and sent on it. New
// picks one. A second format is added by writing another constructor like
// messagesCodec and a case for it in New; nothing else in the profile
// knows which wire is in use.
type codec struct {
	wire    llm.Wire
	caps    func() llm.Capabilities
	prepare func(r llm.Route, p *plan.PromptPlan, o llm.Options) (*llm.Frozen, error)
	send    func(ctx context.Context, f *llm.Frozen) (llm.Stream, error)
}

// Config configures a Provider.
type Config struct {
	APIKey string
	// BaseURL is the endpoint's base, without the /v1/messages path. It
	// defaults to https://api.deepseek.com/anthropic; tests override it.
	BaseURL string
	// Wire picks the request format: "" or "messages".
	Wire string
	// HTTPClient defaults to a client with no timeout. A request can wait
	// in DeepSeek's queue for up to ten minutes, kept alive by comment
	// lines, before the server gives up on it; nothing here may be shorter.
	HTTPClient *http.Client
}

// Provider reaches the DeepSeek API. It is safe for concurrent use.
type Provider struct {
	codec codec
}

var _ llm.Provider = (*Provider)(nil)

// New validates cfg and fills in defaults.
func New(cfg Config) (*Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("deepseek: api key is required")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	switch cfg.Wire {
	case "", WireMessages:
		c, err := messagesCodec(cfg, hc)
		if err != nil {
			return nil, err
		}
		return &Provider{codec: c}, nil
	case WireChat:
		c, err := chatCodec(cfg, hc)
		if err != nil {
			return nil, err
		}
		return &Provider{codec: c}, nil
	default:
		return nil, fmt.Errorf("deepseek: wire %q: want %q or %q", cfg.Wire, WireMessages, WireChat)
	}
}

// messagesCodec reaches DeepSeek over its Anthropic-format endpoint.
func messagesCodec(cfg Config, hc *http.Client) (codec, error) {
	base, err := baseURL(cfg.BaseURL, messagesBaseURL)
	if err != nil {
		return codec{}, err
	}
	header := http.Header{}
	header.Set("X-Api-Key", cfg.APIKey)
	header.Set("Anthropic-Version", apiVersion)
	d := &messages.Dialect{
		URL: base + messagesPath,
		// No anthropic-beta header and no extra body fields: the endpoint
		// has no use for either.
		Header: header,
		// output_config.effort takes low, high or max, and thinking:
		// {"type": "disabled"} switches reasoning off.
		Effort: messages.OutputConfigEffort,
		// Only a tool's name, description and input_schema are documented
		// as supported, so eager input streaming is not asked for.
		EagerToolInput:   false,
		DefaultMaxOutput: defaultMaxOutput,
	}
	return codec{
		wire: llm.WireMessages,
		caps: messagesCaps,
		prepare: func(r llm.Route, p *plan.PromptPlan, o llm.Options) (*llm.Frozen, error) {
			return messages.Prepare(d, r, p, o)
		},
		send: func(ctx context.Context, f *llm.Frozen) (llm.Stream, error) {
			return messages.Send(ctx, hc, f)
		},
	}, nil
}

// messagesCaps is what either model can do on the Anthropic-format
// endpoint.
func messagesCaps() llm.Capabilities {
	return llm.Capabilities{
		ContextWindow: contextWindow,
		MaxOutput:     maxOutput,

		// Caching is automatic and by prefix; cache_control is ignored.
		Cache: llm.CacheAutoPrefix,
		// Unused cache is cleared "within a few hours to a few days". An
		// hour is the cautious end of that.
		CacheTTL: time.Hour,
		Affinity: llm.AffinityNone,
		// The endpoint's usage fields are not documented. The codec reads
		// the Anthropic names (cache_read_input_tokens); the probe has to
		// confirm DeepSeek uses them.
		CacheUsage: true,

		// Other names are remapped by the API; these three are its own. Its
		// remap sends medium and xhigh to high. Capabilities.Nearest agrees
		// on medium but sends xhigh to max, and the conformance suite holds
		// a profile to Nearest, so a contact asking for xhigh runs at max.
		Efforts:       []llm.Effort{llm.EffortLow, llm.EffortHigh, llm.EffortMax},
		DefaultEffort: llm.EffortHigh,
		ReasoningOff:  true,
		// With tools set, reasoning must come back in every later request,
		// or the API answers 400.
		Replay: llm.ReplayAllTurns,

		ToolChoice: true,
		// disable_parallel_tool_use and is_error are both ignored here, and
		// strict schemas exist only on the other format's beta base.
		SingleToolCall:   false,
		StrictTools:      false,
		ToolErrorFlag:    false,
		SystemMidHistory: false,
	}
}

func baseURL(configured, fallback string) (string, error) {
	base := configured
	if base == "" {
		base = fallback
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("deepseek: base url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("deepseek: base url needs a scheme and a host")
	}
	return strings.TrimRight(base, "/"), nil
}

// ID returns "deepseek".
func (*Provider) ID() string { return ID }

// Route resolves a model name against the profile's table. "" is the
// default model.
//
// The check is the profile's to make: the endpoint answers a name it does
// not know with deepseek-flash, and says nothing.
func (p *Provider) Route(_ context.Context, name string) (llm.Route, error) {
	if name == "" {
		name = models()[0]
	}
	if !known(name) {
		return llm.Route{}, fmt.Errorf("deepseek: model %q: %w", name, llm.ErrUnknownModel)
	}
	return llm.Route{Provider: ID, Model: name, Wire: p.codec.wire, Caps: p.codec.caps()}, nil
}

// Prepare freezes one call. It refuses a route this profile did not make,
// for the reason Route checks names: a model the table does not hold would
// be served by another one.
func (p *Provider) Prepare(_ context.Context, r llm.Route, pl *plan.PromptPlan, o llm.Options) (*llm.Frozen, error) {
	if r.Provider != ID || !known(r.Model) {
		return nil, fmt.Errorf("deepseek: route %s: %w", r.ID(), llm.ErrUnknownModel)
	}
	if r.Wire != p.codec.wire {
		return nil, fmt.Errorf("deepseek: route %s is for the %s wire: %w", r.ID(), r.Wire, llm.ErrUnsupportedWire)
	}
	f, err := p.codec.prepare(r, pl, o)
	if err != nil {
		return nil, fmt.Errorf("deepseek: %w", err)
	}
	return f, nil
}

// Send streams a frozen call.
func (p *Provider) Send(ctx context.Context, f *llm.Frozen) (llm.Stream, error) {
	return p.codec.send(ctx, f)
}
