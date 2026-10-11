// Package openrouter is the profile for OpenRouter, a gateway that serves
// many vendors' models over Chat Completions.
//
// What a model can do there is not fixed by the format but listed, per
// model, in OpenRouter's catalog. The profile reads the catalog once at
// start-up and declares each model's capabilities from it; nothing about a
// model is assumed from its name.
//
// A gateway may serve the same model name from several upstreams, and a
// different upstream means a cold cache and possibly different behaviour.
// The profile therefore narrows every request to upstreams that honour all
// of its parameters, pins a model to chosen upstreams where the operator
// lists them, and always sends a session key so that a conversation stays on
// the upstream that holds its cache.
package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/wire"
	"github.com/quinnovator/contactd/internal/llm/wire/chat"
	"github.com/quinnovator/contactd/internal/plan"
)

const (
	// ID is what agent configs call this provider.
	ID = "openrouter"

	defaultBaseURL = "https://openrouter.ai/api/v1"

	// cacheTTL is OpenRouter's sticky-routing window: after ten idle minutes
	// a conversation may be routed to another upstream, whose cache is cold
	// whatever the first upstream still holds.
	cacheTTL = 10 * time.Minute

	// catalogLimit bounds the catalog read at start-up. The live catalog is
	// under 1 MB.
	catalogLimit = 32 << 20
)

// Config configures a Provider.
type Config struct {
	// APIKey is sent as a bearer token.
	APIKey string
	// BaseURL is the API root, up to and including /api/v1; default
	// https://openrouter.ai/api/v1. Tests point it at a stand-in.
	BaseURL string
	// DefaultModel is the model Route("") resolves to. It must be in the
	// catalog and support tools.
	DefaultModel string
	// Referer and Title identify the app on openrouter.ai, in the
	// HTTP-Referer and X-OpenRouter-Title headers. Both are optional.
	Referer string
	Title   string
	// Pin maps a model to the upstream provider slugs allowed to serve it.
	// A pinned model is sent with fallbacks off, so a request never lands on
	// an upstream the operator did not choose. A model with no entry may be
	// served by any upstream that supports every parameter of the request.
	Pin map[string][]string
	// Catalog is a saved response of GET /models. When set, New does no
	// I/O: this is for tests and for starting up offline.
	Catalog []byte
	// HTTPClient sends the requests. It must have no overall timeout: a
	// stream lives as long as its context. Nil is such a client.
	HTTPClient *http.Client
}

// model is one catalog entry the profile serves.
type model struct {
	caps    llm.Capabilities
	dialect chat.Dialect
}

// Provider serves OpenRouter's tool-calling models. It is safe for
// concurrent use; its table is fixed when New returns.
type Provider struct {
	models map[string]*model
	// toolless holds catalog models left out because they cannot call
	// tools, so that asking for one gets a reason.
	toolless map[string]bool
	def      string
	http     *http.Client
}

var _ llm.Provider = (*Provider)(nil)

// New builds the provider's model table from the catalog: cfg.Catalog when
// set, otherwise one GET of the catalog endpoint, which is the only I/O the
// provider does outside Send.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	p := &Provider{
		models:   map[string]*model{},
		toolless: map[string]bool{},
		def:      cfg.DefaultModel,
		http:     cfg.HTTPClient,
	}
	if p.http == nil {
		// No client timeout: a stream lives as long as its context, and a
		// queued request may wait minutes for its first byte.
		p.http = &http.Client{}
	}

	raw := cfg.Catalog
	if raw == nil {
		var err error
		if raw, err = fetchCatalog(ctx, p.http, base, cfg.APIKey); err != nil {
			return nil, err
		}
	}
	var cat catalog
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, fmt.Errorf("openrouter: decode catalog: %w", err)
	}
	if cat.TotalCount > len(cat.Data) {
		// A partial table would turn models the account can use into
		// unknown ones; better not to start.
		return nil, fmt.Errorf("openrouter: catalog lists %d of %d models; paging is not supported", len(cat.Data), cat.TotalCount)
	}

	header := http.Header{}
	if cfg.APIKey != "" {
		header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	if cfg.Referer != "" {
		header.Set("HTTP-Referer", cfg.Referer)
	}
	if cfg.Title != "" {
		header.Set("X-OpenRouter-Title", cfg.Title)
	}
	for i := range cat.Data {
		e := &cat.Data[i]
		if e.ID == "" {
			continue
		}
		if !e.supports("tools") {
			// contactd agents are tool-calling; a model that cannot call
			// tools is not offered at all.
			p.toolless[e.ID] = true
			continue
		}
		routing, err := providerField(cfg.Pin[e.ID])
		if err != nil {
			return nil, err
		}
		p.models[e.ID] = &model{
			caps: e.capabilities(),
			dialect: chat.Dialect{
				URL:            base + "/chat/completions",
				Header:         header,
				AffinityField:  "session_id",
				Effort:         chat.EffortReasoningObject,
				Reasoning:      chat.ReasoningDetails,
				Extra:          []chat.Field{routing},
				MaxTokensField: e.maxTokensField(),
				// Room for a model that reasons before it answers. The
				// catalog's figure is each model's ceiling, which the
				// codec holds this to.
				DefaultMaxOutput: 8192,
			},
		}
	}
	if len(p.models) == 0 {
		return nil, errors.New("openrouter: the catalog lists no model that supports tools")
	}
	for name := range cfg.Pin {
		if _, ok := p.models[name]; !ok {
			return nil, fmt.Errorf("openrouter: pin for %s: %w", p.unknown(name), llm.ErrUnknownModel)
		}
	}
	if _, ok := p.models[p.def]; p.def != "" && !ok {
		return nil, fmt.Errorf("openrouter: default %s: %w", p.unknown(p.def), llm.ErrUnknownModel)
	}
	return p, nil
}

// ID implements llm.Provider.
func (*Provider) ID() string { return ID }

// Route implements llm.Provider. Only a model in the catalog resolves, by
// its exact ID.
func (p *Provider) Route(_ context.Context, name string) (llm.Route, error) {
	if name == "" {
		name = p.def
	}
	if name == "" {
		return llm.Route{}, fmt.Errorf("no default model is configured: %w", llm.ErrUnknownModel)
	}
	m, ok := p.models[name]
	if !ok {
		return llm.Route{}, fmt.Errorf("%s: %w", p.unknown(name), llm.ErrUnknownModel)
	}
	return llm.Route{Provider: ID, Model: name, Wire: llm.WireChat, Caps: m.caps}, nil
}

// unknown says why a model does not resolve.
func (p *Provider) unknown(name string) string {
	if p.toolless[name] {
		return fmt.Sprintf("model %q does not support tools", name)
	}
	return fmt.Sprintf("model %q is not in the catalog", name)
}

// Prepare implements llm.Provider.
func (p *Provider) Prepare(_ context.Context, r llm.Route, pl *plan.PromptPlan, o llm.Options) (*llm.Frozen, error) {
	m, ok := p.models[r.Model]
	if !ok {
		return nil, fmt.Errorf("openrouter: %s: %w", p.unknown(r.Model), llm.ErrUnknownModel)
	}
	f, err := chat.Prepare(&m.dialect, r, pl, o)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	return f, nil
}

// Send implements llm.Provider.
func (p *Provider) Send(ctx context.Context, f *llm.Frozen) (llm.Stream, error) {
	return chat.Send(ctx, p.http, f) //nolint:wrapcheck // already an *llm.Error naming the provider
}

func fetchCatalog(ctx context.Context, hc *http.Client, base, key string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("openrouter: catalog request: %w", err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter: fetch catalog: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, catalogLimit))
	if err != nil {
		return nil, fmt.Errorf("openrouter: read catalog: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter: fetch catalog: HTTP %d: %s", resp.StatusCode, wire.Excerpt(raw))
	}
	return raw, nil
}

// routing is the request's "provider" object.
type routing struct {
	Only           []string `json:"only,omitempty"`
	AllowFallbacks *bool    `json:"allow_fallbacks,omitempty"`
	// RequireParameters keeps a request off any upstream that would ignore
	// one of its parameters: a silently dropped session key or effort is
	// worse than a refusal.
	RequireParameters bool `json:"require_parameters"`
}

// providerField builds the routing object for a model: its pin, when the
// operator set one, with fallbacks off.
func providerField(only []string) (chat.Field, error) {
	r := routing{RequireParameters: true}
	if len(only) > 0 {
		off := false
		r.Only, r.AllowFallbacks = only, &off
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return chat.Field{}, fmt.Errorf("openrouter: encode provider routing: %w", err)
	}
	return chat.Field{Name: "provider", Value: raw}, nil
}

// The catalog, as GET /models returns it. Only the fields the profile reads
// are declared.

type catalog struct {
	Data       []entry `json:"data"`
	TotalCount int     `json:"total_count"`
}

type entry struct {
	ID            string `json:"id"`
	ContextLength int    `json:"context_length"`
	TopProvider   struct {
		MaxCompletionTokens int `json:"max_completion_tokens"` // null for routers
	} `json:"top_provider"`
	SupportedParameters []string `json:"supported_parameters"`
	// Prices are decimal strings, in dollars per token.
	Pricing struct {
		InputCacheRead  string `json:"input_cache_read"`
		InputCacheWrite string `json:"input_cache_write"`
	} `json:"pricing"`
	Reasoning *reasoningInfo `json:"reasoning"`
}

// reasoningInfo is a model's "reasoning" object. Non-reasoning models and
// router models omit it.
type reasoningInfo struct {
	// SupportedEfforts is three-valued: omitted, the model exposes no effort
	// selection; null, it accepts every gateway effort; otherwise the list,
	// strongest first.
	SupportedEfforts json.RawMessage `json:"supported_efforts"`
	DefaultEffort    string          `json:"default_effort"`
	// Mandatory means reasoning cannot be switched off: the model rejects
	// effort "none".
	Mandatory bool `json:"mandatory"`
}

func (e *entry) supports(param string) bool { return slices.Contains(e.SupportedParameters, param) }

// capabilities declares what the catalog says about a model.
func (e *entry) capabilities() llm.Capabilities {
	caps := llm.Capabilities{
		ContextWindow: e.ContextLength,
		MaxOutput:     e.TopProvider.MaxCompletionTokens,
		Cache:         e.cacheMode(),
		CacheTTL:      cacheTTL,
		Affinity:      llm.AffinityBody,
		CacheUsage:    true,
		Replay:        llm.ReplayNone,
		ToolChoice:    e.supports("tool_choice"),
		// The model is held to one call only where the request can say so.
		SingleToolCall: e.supports("parallel_tool_calls"),
		StrictTools:    e.supports("structured_outputs"),
	}
	if !e.supports("reasoning") {
		return caps
	}
	// Reasoning is stored and returned on every later request: several
	// upstreams reject a tool-using conversation that omits it.
	caps.Replay = llm.ReplayAllTurns
	if info := e.Reasoning; info != nil {
		caps.Efforts = info.efforts()
		caps.DefaultEffort = effort(info.DefaultEffort)
		caps.ReasoningOff = !info.Mandatory
	}
	return caps
}

// cacheMode reads how the model's upstream caches from what it charges for:
// a price for writing means the request has to mark what is written; a price
// for reading alone means the upstream caches by prefix on its own.
func (e *entry) cacheMode() llm.CacheMode {
	switch {
	case priced(e.Pricing.InputCacheWrite):
		return llm.CacheBreakpoints
	case priced(e.Pricing.InputCacheRead):
		return llm.CacheAutoPrefix
	default:
		return llm.CacheNone
	}
}

// maxTokensField picks the output-cap name the model lists. Nearly every
// model takes max_tokens; a few OpenAI ones take only the newer name.
func (e *entry) maxTokensField() string {
	if !e.supports("max_tokens") && e.supports("max_completion_tokens") {
		return "max_completion_tokens"
	}
	return "max_tokens"
}

// efforts maps the catalog's effort names onto the neutral scale, weakest
// first. "none" is not an effort (switching reasoning off is ReasoningOff)
// and "minimal" has no neutral equivalent; both are left out.
func (r *reasoningInfo) efforts() []llm.Effort {
	var names []string
	switch raw := strings.TrimSpace(string(r.SupportedEfforts)); raw {
	case "":
		return nil
	case "null":
		names = []string{"low", "medium", "high", "xhigh", "max"}
	default:
		if err := json.Unmarshal(r.SupportedEfforts, &names); err != nil {
			return nil // an unreadable list declares no effort control
		}
	}
	var out []llm.Effort
	for _, name := range names {
		if e := effort(name); e > llm.EffortNone && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	slices.Sort(out)
	return out
}

// effort reads one of the catalog's effort names, or returns EffortDefault
// for a name the neutral scale does not have.
func effort(name string) llm.Effort {
	if name == "" {
		return llm.EffortDefault
	}
	e, err := llm.ParseEffort(name)
	if err != nil {
		return llm.EffortDefault
	}
	return e
}

func priced(s string) bool {
	v, err := strconv.ParseFloat(s, 64)
	return err == nil && v > 0
}
