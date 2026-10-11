// Package openaicompat is the profile for a plain OpenAI-compatible server:
// vLLM, SGLang, or anything else that serves Chat Completions and is run by
// the operator. It knows nothing the operator does not tell it: the models
// are the ones the config lists.
//
// These servers cache by prefix, per replica, on their own. The profile's
// one job for the cache is to keep a contact on the replica that already
// holds its prefix, which it does by rendezvous hashing over the base URLs.
package openaicompat

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/wire/chat"
	"github.com/quinnovator/contactd/internal/plan"
)

const (
	// ID is what agent configs call this provider. It is "openai" because
	// that is what the adapter this profile replaces was called, and stored
	// configs name it.
	ID = "openai"

	defaultBaseURL = "https://api.openai.com/v1"

	// DefaultContextWindow is declared for a model whose config states no
	// window. Nothing about a self-hosted model can be looked up, so this is
	// a floor that current open models clear, not a measurement.
	DefaultContextWindow = 32_768

	// cacheTTL is how long a replica is assumed to keep a contact's prefix.
	// vLLM and SGLang evict under memory pressure, not on a clock, so this
	// keeps the figure the adapter this replaces assumed.
	cacheTTL = 5 * time.Minute

	// affinityHeader carries the pinning key. No upstream reads it: replica
	// choice happens here. It is declared and sent because the contract
	// hands a provider the key only on a route that declares where it goes.
	affinityHeader = "X-Contactd-Affinity"
)

// Model is one model the server offers.
type Model struct {
	// Name is the model name the server knows, sent as written.
	Name string
	// ContextWindow is the model's context size in tokens; 0 declares
	// DefaultContextWindow.
	ContextWindow int
	// MaxOutput caps output tokens per call; 0 leaves the server's default.
	MaxOutput int
}

// Config configures a Provider.
type Config struct {
	// APIKey is sent as a bearer token. It may be empty for a server that
	// does not check one.
	APIKey string
	// BaseURLs lists interchangeable replicas, each up to and including its
	// /v1; default ["https://api.openai.com/v1"]. With more than one, each
	// contact is pinned to a replica.
	BaseURLs []string
	// Models lists what the server offers. Empty with a DefaultModel means
	// exactly that one model.
	Models []Model
	// DefaultModel is the model Route("") resolves to. It must be listed in
	// Models when Models is not empty.
	DefaultModel string
	// HTTPClient sends the requests. It must have no overall timeout: a
	// stream lives as long as its context. Nil is such a client.
	HTTPClient *http.Client
}

// replica is one inference server.
type replica struct {
	dialect chat.Dialect
	// seed is a hash of the base URL: the replica's identity for rendezvous
	// hashing, independent of its position in the config.
	seed uint64
}

// Provider serves the configured models over Chat Completions. It is safe
// for concurrent use.
type Provider struct {
	replicas []replica
	models   map[string]llm.Capabilities
	def      string
	http     *http.Client
}

var _ llm.Provider = (*Provider)(nil)

// New validates cfg and fills in defaults.
func New(cfg Config) (*Provider, error) {
	p := &Provider{
		models: make(map[string]llm.Capabilities, len(cfg.Models)+1),
		def:    cfg.DefaultModel,
		http:   cfg.HTTPClient,
	}
	if p.http == nil {
		// No client timeout: a stream lives as long as its context, and a
		// queued request may wait minutes for its first byte.
		p.http = &http.Client{}
	}

	models := cfg.Models
	if len(models) == 0 && cfg.DefaultModel != "" {
		models = []Model{{Name: cfg.DefaultModel}}
	}
	if len(models) == 0 {
		return nil, errors.New("openaicompat: no models configured")
	}
	for _, m := range models {
		switch _, dup := p.models[m.Name]; {
		case m.Name == "":
			return nil, errors.New("openaicompat: a model has no name")
		case dup:
			return nil, fmt.Errorf("openaicompat: model %q is listed twice", m.Name)
		case m.ContextWindow < 0 || m.MaxOutput < 0:
			return nil, fmt.Errorf("openaicompat: model %q has a negative context window or output cap", m.Name)
		}
		p.models[m.Name] = capabilities(m)
	}
	if _, ok := p.models[p.def]; p.def != "" && !ok {
		return nil, fmt.Errorf("openaicompat: default model %q is not among the configured models", p.def)
	}

	header := http.Header{}
	if cfg.APIKey != "" {
		header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	bases := cfg.BaseURLs
	if len(bases) == 0 {
		bases = []string{defaultBaseURL}
	}
	seen := make(map[string]bool, len(bases))
	for _, base := range bases {
		base = strings.TrimRight(base, "/")
		u, err := url.Parse(base)
		if err != nil {
			return nil, fmt.Errorf("openaicompat: base url: %w", err)
		}
		if u.Scheme == "" || u.Host == "" {
			return nil, errors.New("openaicompat: base url needs a scheme and a host")
		}
		if seen[base] {
			return nil, errors.New("openaicompat: duplicate base url")
		}
		seen[base] = true
		p.replicas = append(p.replicas, replica{
			dialect: chat.Dialect{
				URL:            base + "/chat/completions",
				Header:         header,
				AffinityHeader: affinityHeader,
				// max_tokens is deprecated and rejected by OpenAI's newer
				// models; vLLM and SGLang accept this name too.
				MaxTokensField: "max_completion_tokens",
			},
			seed: hash(base),
		})
	}
	return p, nil
}

// capabilities is what any server of this kind is assumed to do. Nothing is
// probed: the conservative value is declared wherever servers differ.
func capabilities(m Model) llm.Capabilities {
	window := m.ContextWindow
	if window == 0 {
		window = DefaultContextWindow
	}
	return llm.Capabilities{
		ContextWindow: window,
		MaxOutput:     m.MaxOutput,
		Cache:         llm.CacheAutoPrefix,
		CacheTTL:      cacheTTL,
		// The key goes no further than the replica it selects; see
		// affinityHeader.
		Affinity:   llm.AffinityHeader,
		CacheUsage: true,
		// No effort control and no replay: reasoning parameters and fields
		// differ by server and by chat template.
		Replay:         llm.ReplayNone,
		ToolChoice:     true,
		SingleToolCall: true,
	}
}

// ID implements llm.Provider.
func (*Provider) ID() string { return ID }

// Route implements llm.Provider. Only a configured model resolves: a name
// the operator did not list is an error, never passed through for the server
// to interpret.
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

// Prepare implements llm.Provider. The replica is part of what is frozen, so
// a retry goes back to the server that holds the contact's prefix.
func (p *Provider) Prepare(_ context.Context, r llm.Route, pl *plan.PromptPlan, o llm.Options) (*llm.Frozen, error) {
	f, err := chat.Prepare(&p.pick(o.Affinity).dialect, r, pl, o)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: %w", err)
	}
	return f, nil
}

// Send implements llm.Provider.
func (p *Provider) Send(ctx context.Context, f *llm.Frozen) (llm.Stream, error) {
	return chat.Send(ctx, p.http, f) //nolint:wrapcheck // already an *llm.Error naming the provider
}

// pick chooses the replica for an affinity key by rendezvous
// (highest-random-weight) hashing: every replica scores the key and the
// highest score wins. A contact therefore keeps hitting the GPU that already
// holds its KV prefix, and adding a replica moves only the contacts the
// newcomer outscores, about 1/N of them, instead of reshuffling everyone.
// A call with no key belongs to no contact and takes the first replica.
func (p *Provider) pick(key string) *replica {
	best := &p.replicas[0]
	if len(p.replicas) == 1 || key == "" {
		return best
	}
	k := mix64(hash(key))
	bestScore := mix64(best.seed ^ k)
	for i := 1; i < len(p.replicas); i++ {
		if s := mix64(p.replicas[i].seed ^ k); s > bestScore {
			best, bestScore = &p.replicas[i], s
		}
	}
	return best
}

func hash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// mix64 is the splitmix64 finalizer. FNV leaves similar strings with similar
// high bits; this spreads them over the whole 64-bit range.
func mix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}
