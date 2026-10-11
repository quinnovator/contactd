// Package opencodego is the provider profile for OpenCode Go, the
// subscription gateway at opencode.ai/zen/go.
//
// The gateway serves each model over one fixed wire format: Chat Completions
// for some families, Anthropic-format Messages for others, and Responses for
// the rest. The profile keeps that table, and checks it against the
// gateway's own catalog when it starts, so that a model the gateway has
// retired fails at start-up instead of on a contact's turn.
//
// OpenCode asks two things of clients: a User-Agent of their own, and a
// stable x-opencode-session per conversation, which it uses for routing and
// prompt caching. Both are sent on every request. The session is not
// optional: a request without it is refused with HTTP 400 MissingSessionID
// (seen live on Oct 11, 2026), so a call that belongs to no conversation,
// such as an episode summary, is sent under a session of the process's own.
//
// Much about this gateway is not documented: how caching is triggered and
// reported, how reasoning effort is set, whether reasoning must be replayed,
// and which header authenticates the Messages routes. Each of those is
// declared conservatively here and marked for the probe.
package opencodego

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/wire"
	"github.com/quinnovator/contactd/internal/llm/wire/chat"
	"github.com/quinnovator/contactd/internal/llm/wire/messages"
	"github.com/quinnovator/contactd/internal/plan"
)

const (
	// ID is the provider's name in agent configs.
	ID = "opencode"
	// DefaultBaseURL is the gateway's API root.
	DefaultBaseURL = "https://opencode.ai/zen/go/v1"
	// SessionHeader carries the per-conversation key.
	SessionHeader = "X-Opencode-Session"
	// DefaultModel is the route used when an agent names no model.
	DefaultModel = "deepseek-v4.1-flash"

	userAgent        = "contactd/1.0"
	defaultMaxOutput = 8192
	// cacheTTL is a cautious guess: the gateway documents no lifetime, and
	// five minutes is the shortest any upstream behind it is known to keep.
	cacheTTL = 5 * time.Minute
)

// Config configures the profile.
type Config struct {
	APIKey string
	// BaseURL overrides the API root; default DefaultBaseURL.
	BaseURL string
	// Catalog is a saved copy of GET /models. When nil, New fetches it; that
	// is the profile's only I/O outside Send.
	Catalog []byte
	// DefaultModel overrides DefaultModel.
	DefaultModel string
	HTTPClient   *http.Client // default: no timeout
}

// family is what the profile knows about a group of models that share a
// name prefix: the wire the gateway serves them over, and how they behave.
type family struct {
	prefix string
	wire   llm.Wire
	// replay is the family's reasoning rule, where it is known.
	replay llm.Replay
	// breakpoints marks models whose price list has a cached-write price,
	// which is how explicit cache breakpoints show up in a price list.
	breakpoints func(model string) bool
	window, max int
}

func never(string) bool  { return false }
func always(string) bool { return true }

// families is the wire table from OpenCode Go's documentation, with what is
// known of each family's behaviour. A function, so the table cannot be
// changed at run time.
func families() []family {
	const window, maxOut = 128_000, 8192 // placeholders where the gateway states nothing
	return []family{
		// Chat Completions.
		{prefix: "deepseek", wire: llm.WireChat, replay: llm.ReplayAllTurns, breakpoints: never, window: 1_000_000, max: 384_000},
		{prefix: "glm", wire: llm.WireChat, breakpoints: never, window: window, max: maxOut},
		{prefix: "kimi", wire: llm.WireChat, breakpoints: never, window: window, max: maxOut},
		{prefix: "mimo", wire: llm.WireChat, breakpoints: never, window: window, max: maxOut},
		{prefix: "hy", wire: llm.WireChat, breakpoints: never, window: window, max: maxOut},
		{prefix: "longcat", wire: llm.WireChat, breakpoints: never, window: window, max: maxOut},
		// Messages.
		{prefix: "minimax", wire: llm.WireMessages, breakpoints: func(m string) bool { return m == "minimax-m2.7" }, window: window, max: maxOut},
		{prefix: "qwen", wire: llm.WireMessages, breakpoints: always, window: window, max: maxOut},
		{prefix: "claude", wire: llm.WireMessages, replay: llm.ReplaySameTurn, breakpoints: always, window: 200_000, max: 64_000},
		// Responses: no codec yet. Listed so the error can say why.
		{prefix: "gpt", wire: llm.WireResponses},
		{prefix: "grok", wire: llm.WireResponses},
		{prefix: "muse", wire: llm.WireResponses},
	}
}

func familyOf(model string) (family, bool) {
	for _, f := range families() {
		if strings.HasPrefix(model, f.prefix) {
			return f, true
		}
	}
	return family{}, false
}

// Provider implements llm.Provider.
type Provider struct {
	hc       *http.Client
	def      string
	chatD    chat.Dialect
	msgD     messages.Dialect
	routes   map[string]llm.Route
	skipped  []string // catalog models the profile has no wire for
	unserved []string // catalog models that need the Responses wire
	// session stands in for a conversation key on calls that have none.
	session string
}

// New builds the profile. It reads the gateway's catalog, from cfg.Catalog
// or the network, and serves the models that are both in the catalog and in
// the profile's wire table.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("opencode: no API key")
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		// No overall timeout: a stream lasts as long as the model writes.
		hc = &http.Client{}
	}
	header := func() http.Header {
		h := http.Header{}
		h.Set("Authorization", "Bearer "+cfg.APIKey)
		h.Set("User-Agent", userAgent)
		return h
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("opencode: make a session id: %w", err)
	}
	p := &Provider{
		hc:      hc,
		def:     cfg.DefaultModel,
		routes:  make(map[string]llm.Route),
		session: "contactd-" + hex.EncodeToString(nonce[:]),
		chatD: chat.Dialect{
			URL:              base + "/chat/completions",
			Header:           header(),
			AffinityHeader:   SessionHeader,
			Effort:           chat.EffortUnsent,
			Reasoning:        chat.ReasoningContent,
			DefaultMaxOutput: defaultMaxOutput,
		},
		msgD: messages.Dialect{
			URL:              base + "/messages",
			Header:           header(),
			AffinityHeader:   SessionHeader,
			DefaultMaxOutput: defaultMaxOutput,
		},
	}
	// The docs show Bearer auth for the gateway and do not say which header
	// its Messages routes read. The key goes in both that format's own
	// header and the gateway's, to the same host. With both, a Messages
	// route answered (probed Oct 11, 2026); which of the two it read was
	// not isolated.
	p.msgD.Header.Set("X-Api-Key", cfg.APIKey)
	p.msgD.Header.Set("Anthropic-Version", "2023-06-01")
	if p.def == "" {
		p.def = DefaultModel
	}

	catalog := cfg.Catalog
	if catalog == nil {
		var err error
		if catalog, err = fetchCatalog(ctx, hc, base+"/models", header()); err != nil {
			return nil, err
		}
	}
	ids, err := parseCatalog(catalog)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		f, ok := familyOf(id)
		switch {
		case !ok:
			p.skipped = append(p.skipped, id)
		case f.wire == llm.WireResponses:
			p.unserved = append(p.unserved, id)
		default:
			p.routes[id] = llm.Route{Provider: ID, Model: id, Wire: f.wire, Caps: capsOf(f, id)}
		}
	}
	if _, ok := p.routes[p.def]; !ok {
		return nil, fmt.Errorf("opencode: default model %q is not in the gateway's catalog: %w", p.def, llm.ErrUnknownModel)
	}
	return p, nil
}

func capsOf(f family, model string) llm.Capabilities {
	c := llm.Capabilities{
		ContextWindow: f.window,
		MaxOutput:     f.max,
		// Cached-read prices are listed for every model, so every route is
		// treated as cached; what triggers it is the family's convention.
		Cache:    llm.CacheAutoPrefix,
		CacheTTL: cacheTTL,
		Affinity: llm.AffinityHeader,
		// Where cached tokens are reported is not documented. The probe
		// found them reported on a DeepSeek route (Chat Completions) and a
		// Qwen route (Messages, with breakpoints) on Oct 11, 2026; for every
		// other family a miss is logged and never alarms until that family
		// has been probed too.
		CacheUsage: f.prefix == "deepseek" || f.prefix == "qwen",
		// No effort control is documented. Each wire's own convention may
		// well work; the probe decides.
		Replay: f.replay,
	}
	if f.breakpoints(model) {
		c.Cache = llm.CacheBreakpoints
	}
	return c
}

// ID implements llm.Provider.
func (p *Provider) ID() string { return ID }

// Models lists the models the profile serves, sorted.
func (p *Provider) Models() []string {
	out := make([]string, 0, len(p.routes))
	for id := range p.routes {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// Unrouted lists catalog models the profile cannot serve: those that need
// the Responses wire, and those whose family it does not know. The caller
// logs them at start-up, so that a new family is noticed.
func (p *Provider) Unrouted() (responses, unknown []string) {
	return slices.Clone(p.unserved), slices.Clone(p.skipped)
}

// Route implements llm.Provider.
func (p *Provider) Route(_ context.Context, model string) (llm.Route, error) {
	if model == "" {
		model = p.def
	}
	if r, ok := p.routes[model]; ok {
		return r, nil
	}
	if slices.Contains(p.unserved, model) {
		return llm.Route{}, fmt.Errorf("model %q is served over the Responses wire: %w", model, llm.ErrUnsupportedWire)
	}
	return llm.Route{}, fmt.Errorf("model %q: %w", model, llm.ErrUnknownModel)
}

// Prepare implements llm.Provider.
func (p *Provider) Prepare(_ context.Context, r llm.Route, pl *plan.PromptPlan, o llm.Options) (*llm.Frozen, error) {
	known, ok := p.routes[r.Model]
	if !ok || r.Provider != ID {
		return nil, fmt.Errorf("model %q: %w", r.Model, llm.ErrUnknownModel)
	}
	if o.Affinity == "" {
		o.Affinity = p.session // the gateway refuses a request with no session
	}
	var (
		f   *llm.Frozen
		err error
	)
	switch known.Wire {
	case llm.WireChat:
		f, err = chat.Prepare(&p.chatD, r, pl, o)
	case llm.WireMessages:
		f, err = messages.Prepare(&p.msgD, r, pl, o)
	case llm.WireResponses:
		return nil, fmt.Errorf("model %q: %w", r.Model, llm.ErrUnsupportedWire)
	default:
		return nil, fmt.Errorf("model %q: %w", r.Model, llm.ErrUnsupportedWire)
	}
	if err != nil {
		return nil, fmt.Errorf("opencode: %w", err)
	}
	return f, nil
}

// Send implements llm.Provider.
func (p *Provider) Send(ctx context.Context, f *llm.Frozen) (llm.Stream, error) {
	var (
		s   llm.Stream
		err error
	)
	switch f.Route.Wire {
	case llm.WireChat:
		s, err = chat.Send(ctx, p.hc, f)
	case llm.WireMessages:
		s, err = messages.Send(ctx, p.hc, f)
	case llm.WireResponses:
		return nil, fmt.Errorf("model %q: %w", f.Route.Model, llm.ErrUnsupportedWire)
	default:
		return nil, fmt.Errorf("model %q: %w", f.Route.Model, llm.ErrUnsupportedWire)
	}
	if err != nil {
		return nil, outOfBudget(err)
	}
	return s, nil
}

// outOfBudget reclassifies a spent subscription. The plan's limits are
// dollar budgets per five hours, week and month; once one is used up the
// gateway refuses requests until the window rolls over, which no backoff
// will outlast. Retrying it as a rate limit would only hold a contact's
// turn open for nothing.
func outOfBudget(err error) error {
	e, ok := errors.AsType[*llm.Error](err)
	if !ok || e.Class != llm.ErrRateLimited {
		return err
	}
	msg := strings.ToLower(e.Err.Error())
	for _, word := range []string{"usage limit", "quota", "budget", "monthly", "weekly", "5-hour", "5 hour", "five hour", "subscription"} {
		if strings.Contains(msg, word) {
			e.Class = llm.ErrQuota
			return e
		}
	}
	return err
}

func fetchCatalog(ctx context.Context, hc *http.Client, url string, header http.Header) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("opencode: build catalog request: %w", err)
	}
	req.Header = header
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("opencode: fetch model catalog: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // read to the end or abandoned; nothing to act on
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("opencode: read model catalog: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &llm.Error{
			Class: wire.Classify(resp.StatusCode), Status: resp.StatusCode, Provider: ID,
			Err: fmt.Errorf("fetch model catalog: %s", wire.Excerpt(body)),
		}
	}
	return body, nil
}

// parseCatalog reads the OpenAI-style model list the gateway returns. It
// carries ids and nothing about what a model can do.
func parseCatalog(raw []byte) ([]string, error) {
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("opencode: decode model catalog: %w", err)
	}
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("opencode: the model catalog is empty")
	}
	return ids, nil
}
