// Package ollamacloud is the profile for Ollama Cloud, reached through its
// OpenAI-compatible /v1 endpoint.
//
// Ollama reports automatic prefix-cache reads through the standard nested
// Chat Completions usage field. Cache retention and reasoning replay remain
// unspecified. The model table comes from the service itself at start-up: the
// model list from /api/tags, and each model's context size and accepted
// thinking levels from /api/show.
package ollamacloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/wire"
	"github.com/quinnovator/contactd/internal/llm/wire/chat"
	"github.com/quinnovator/contactd/internal/plan"
)

const (
	// ID is what agent configs call this provider.
	ID = "ollama"

	defaultBaseURL = "https://ollama.com"

	// DefaultContextWindow is declared for a model whose /api/show reports
	// no context length, or whose config states none. Every model the cloud
	// listed when this was written reports at least this much.
	DefaultContextWindow = 128_000

	// responseLimit bounds each start-up response read.
	responseLimit = 4 << 20
)

// Model is one model the provider serves, with what /api/show says about it.
type Model struct {
	// Name is the model name as /api/tags lists it, tag included
	// ("deepseek-v4-pro:0813").
	Name string
	// ContextWindow is the model's context size in tokens; 0 declares
	// DefaultContextWindow.
	ContextWindow int
	// MaxOutput caps output tokens per call; 0 leaves the service's default.
	// Ollama reports no per-model limit.
	MaxOutput int
	// Efforts are the thinking levels the model accepts, on the neutral
	// scale. Empty means the model's thinking has no levels.
	Efforts []llm.Effort
	// DefaultEffort is the level the model uses when none is sent.
	DefaultEffort llm.Effort
	// ReasoningOff reports whether thinking can be switched off: the model
	// lists false among its thinking values.
	ReasoningOff bool
}

// Config configures a Provider.
type Config struct {
	// APIKey is sent as a bearer token.
	APIKey string
	// BaseURL is the service root; default https://ollama.com. The endpoints
	// are /v1/chat/completions, /api/tags and /api/show under it.
	BaseURL string
	// DefaultModel is the model Route("") resolves to. It must be served.
	DefaultModel string
	// Models is the model table. When set, New does no I/O: this is for
	// tests and for starting up offline. When empty, New loads the table
	// from the service.
	Models []Model
	// HTTPClient sends the requests. It must have no overall timeout: a
	// stream lives as long as its context. Nil is such a client.
	HTTPClient *http.Client
}

// Provider serves Ollama Cloud's models over Chat Completions. It is safe
// for concurrent use; its table is fixed when New returns.
type Provider struct {
	models  map[string]llm.Capabilities
	def     string
	dialect chat.Dialect
	http    *http.Client
}

var _ llm.Provider = (*Provider)(nil)

// New builds the provider's model table: from cfg.Models when set, otherwise
// from the service, with one GET of /api/tags and one POST of /api/show per
// model. That is the only I/O the provider does outside Send.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	header := http.Header{}
	if cfg.APIKey != "" {
		header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	p := &Provider{
		models: map[string]llm.Capabilities{},
		def:    cfg.DefaultModel,
		dialect: chat.Dialect{
			URL:    base + "/v1/chat/completions",
			Header: header,
			// /v1 takes reasoning_effort with each model's own level names,
			// and "none" to request no thinking.
			Effort:         chat.EffortTopLevel,
			Reasoning:      chat.ReasoningUnsent,
			MaxTokensField: "max_tokens",
			// Room for a model that reasons before it answers, without
			// asking for a whole context window of output.
			DefaultMaxOutput: 8192,
		},
		http: cfg.HTTPClient,
	}
	if p.http == nil {
		// No client timeout: a stream lives as long as its context, and a
		// queued request may wait minutes for its first byte.
		p.http = &http.Client{}
	}

	models := cfg.Models
	if len(models) == 0 {
		var err error
		if models, err = load(ctx, p.http, base, header); err != nil {
			return nil, err
		}
	}
	for _, m := range models {
		switch _, dup := p.models[m.Name]; {
		case m.Name == "":
			return nil, errors.New("ollamacloud: a model has no name")
		case dup:
			return nil, fmt.Errorf("ollamacloud: model %q is listed twice", m.Name)
		case m.ContextWindow < 0 || m.MaxOutput < 0:
			return nil, fmt.Errorf("ollamacloud: model %q has a negative context window or output cap", m.Name)
		case slices.Contains(m.Efforts, llm.EffortDefault) || slices.Contains(m.Efforts, llm.EffortNone):
			return nil, fmt.Errorf("ollamacloud: model %q lists an effort that is not a level; switching thinking off is ReasoningOff", m.Name)
		}
		p.models[m.Name] = capabilities(m)
	}
	if len(p.models) == 0 {
		return nil, errors.New("ollamacloud: the service lists no models")
	}
	if _, ok := p.models[p.def]; p.def != "" && !ok {
		return nil, fmt.Errorf("ollamacloud: default model %q: %w", p.def, llm.ErrUnknownModel)
	}
	return p, nil
}

// capabilities declares a model's route. Everything Ollama does not
// document for /v1 is declared absent.
func capabilities(m Model) llm.Capabilities {
	window := m.ContextWindow
	if window == 0 {
		window = DefaultContextWindow
	}
	efforts := slices.Clone(m.Efforts)
	slices.Sort(efforts)
	return llm.Capabilities{
		ContextWindow: window,
		MaxOutput:     m.MaxOutput,
		// /api/chat documents prompt_eval_cached_count; /v1 maps it to
		// usage.prompt_tokens_details.cached_tokens (ollama/ollama#17943).
		// Repeated cloud requests confirmed prefix reuse on 2026-10-13.
		// Retention is undocumented: leave CacheTTL zero rather than
		// assuming a prefix will still be warm on the next turn.
		Cache:         llm.CacheAutoPrefix,
		Affinity:      llm.AffinityNone,
		CacheUsage:    true,
		Efforts:       slices.Compact(efforts),
		DefaultEffort: m.DefaultEffort,
		ReasoningOff:  m.ReasoningOff,
		// The field /v1 returns reasoning in is not documented, and neither
		// is sending it back: nothing is replayed until a probe says how.
		Replay: llm.ReplayNone,
		// /v1/chat/completions does not support tool_choice, and documents
		// no way to hold a model to one call.
		ToolChoice:     false,
		SingleToolCall: false,
	}
}

// ID implements llm.Provider.
func (*Provider) ID() string { return ID }

// Route implements llm.Provider. Only a listed model resolves, by its exact
// name.
func (p *Provider) Route(_ context.Context, model string) (llm.Route, error) {
	if model == "" {
		model = p.def
	}
	if model == "" {
		return llm.Route{}, fmt.Errorf("no default model is configured: %w", llm.ErrUnknownModel)
	}
	caps, ok := p.models[model]
	if !ok {
		return llm.Route{}, fmt.Errorf("model %q: %w", model, llm.ErrUnknownModel)
	}
	return llm.Route{Provider: ID, Model: model, Wire: llm.WireChat, Caps: caps}, nil
}

// Prepare implements llm.Provider.
func (p *Provider) Prepare(_ context.Context, r llm.Route, pl *plan.PromptPlan, o llm.Options) (*llm.Frozen, error) {
	f, err := chat.Prepare(&p.dialect, r, pl, o)
	if err != nil {
		return nil, fmt.Errorf("ollamacloud: %w", err)
	}
	return f, nil
}

// Send implements llm.Provider.
func (p *Provider) Send(ctx context.Context, f *llm.Frozen) (llm.Stream, error) {
	return chat.Send(ctx, p.http, f) //nolint:wrapcheck // already an *llm.Error naming the provider
}

// tags is the response of GET /api/tags.
type tags struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// show is the response of POST /api/show, as far as the profile reads it.
type show struct {
	// ModelInfo is keyed by architecture: "<arch>.context_length" holds the
	// context size.
	ModelInfo map[string]json.RawMessage `json:"model_info"`
	// Thinking is omitted for a model with no thinking metadata.
	Thinking *struct {
		// Values mixes booleans, for on and off, with level names.
		Values  []json.RawMessage `json:"values"`
		Default json.RawMessage   `json:"default"`
	} `json:"thinking"`
}

// load reads the model table from the service.
func load(ctx context.Context, hc *http.Client, base string, header http.Header) ([]Model, error) {
	var list tags
	if err := call(ctx, hc, http.MethodGet, base+"/api/tags", header, nil, &list); err != nil {
		return nil, fmt.Errorf("ollamacloud: list models: %w", err)
	}
	models := make([]Model, 0, len(list.Models))
	for _, entry := range list.Models {
		if entry.Name == "" {
			continue
		}
		body, err := json.Marshal(map[string]string{"model": entry.Name})
		if err != nil {
			return nil, fmt.Errorf("ollamacloud: encode show request: %w", err)
		}
		var info show
		if err := call(ctx, hc, http.MethodPost, base+"/api/show", header, body, &info); err != nil {
			// A model with unknown thinking levels would be served with the
			// wrong ones; better not to start.
			return nil, fmt.Errorf("ollamacloud: show model %q: %w", entry.Name, err)
		}
		models = append(models, info.model(entry.Name))
	}
	return models, nil
}

// model reads what /api/show says about one model.
func (s *show) model(name string) Model {
	m := Model{Name: name}
	// Keys are visited in sorted order so that a response with two
	// context_length keys always reads the same one.
	keys := make([]string, 0, len(s.ModelInfo))
	for key := range s.ModelInfo {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		var n int
		if strings.HasSuffix(key, ".context_length") && json.Unmarshal(s.ModelInfo[key], &n) == nil && n > 0 {
			m.ContextWindow = n
			break
		}
	}
	if s.Thinking == nil {
		return m
	}
	on := false
	for _, raw := range s.Thinking.Values {
		switch value := strings.TrimSpace(string(raw)); value {
		case "false":
			m.ReasoningOff = true
		case "true":
			on = true
		default:
			if e := level(raw); e != llm.EffortDefault && !slices.Contains(m.Efforts, e) {
				m.Efforts = append(m.Efforts, e)
			}
		}
	}
	slices.Sort(m.Efforts)
	if len(m.Efforts) == 0 && !on {
		// The only value is false: the model does not think, so there is
		// nothing to switch off.
		m.ReasoningOff = false
		return m
	}
	switch value := strings.TrimSpace(string(s.Thinking.Default)); value {
	case "false":
		m.DefaultEffort = llm.EffortNone
	default:
		m.DefaultEffort = level(s.Thinking.Default)
	}
	return m
}

// level reads a thinking level name. A name the neutral scale does not have
// yields EffortDefault and is left out: /v1 resolves an unknown name to the
// model's default, which would send one effort and log another.
func level(raw json.RawMessage) llm.Effort {
	var name string
	if err := json.Unmarshal(raw, &name); err != nil || name == "" {
		return llm.EffortDefault
	}
	e, err := llm.ParseEffort(name)
	if err != nil || e == llm.EffortNone {
		return llm.EffortDefault
	}
	return e
}

// call makes one start-up request and decodes its JSON response.
func call(ctx context.Context, hc *http.Client, method, url string, header http.Header, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header = header.Clone()
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, wire.Excerpt(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
