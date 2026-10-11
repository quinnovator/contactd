package llm

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/plan"
)

// RetryPolicy bounds how a call that fails before its first event is retried.
type RetryPolicy struct {
	Attempts      int           // total tries, the first included
	Base          time.Duration // first backoff; doubles each try
	Max           time.Duration // backoff ceiling
	MaxRetryAfter time.Duration // longest provider hint that is honoured
}

// DefaultRetry is four tries, backing off from 500 ms to 8 s.
func DefaultRetry() RetryPolicy {
	return RetryPolicy{Attempts: 4, Base: 500 * time.Millisecond, Max: 8 * time.Second, MaxRetryAfter: 30 * time.Second}
}

// ClientConfig wires a Client.
type ClientConfig struct {
	Providers []Provider
	// Default is the provider used when an agent config names none, and
	// DefaultModel the model used on it when the config names no model
	// either. An empty DefaultModel takes the provider's own default.
	Default      string
	DefaultModel string
	// Limits caps concurrent calls, keyed by provider ID ("deepseek") or by
	// route ID ("deepseek/deepseek-v4-pro"); a route's own entry wins. A
	// missing or zero entry means no cap. Providers count concurrency per account, so this is the
	// place to stay under their limit instead of discovering it by 429.
	Limits map[string]int
	Retry  RetryPolicy
	// AffinitySecret keys the hash that stands in for a contact ID. It must
	// stay the same across restarts, or every contact's pinning key changes.
	AffinitySecret []byte
	Logger         *slog.Logger
}

// Client is the core's one way to reach a model. It resolves routes, admits
// calls under each provider's concurrency limit, retries a call that fails
// before it starts streaming, and records what was sent.
type Client struct {
	providers map[string]Provider
	def       string
	defModel  string
	limits    map[string]chan struct{}
	retry     RetryPolicy
	secret    []byte
	log       *slog.Logger
}

// NewClient builds a Client.
func NewClient(cfg ClientConfig) (*Client, error) {
	if len(cfg.Providers) == 0 {
		return nil, errors.New("llm: no providers configured")
	}
	if cfg.Retry.Attempts <= 0 {
		cfg.Retry = DefaultRetry()
	}
	c := &Client{
		providers: make(map[string]Provider, len(cfg.Providers)),
		def:       cfg.Default,
		defModel:  cfg.DefaultModel,
		limits:    make(map[string]chan struct{}),
		retry:     cfg.Retry,
		secret:    cfg.AffinitySecret,
		log:       cfg.Logger,
	}
	for _, p := range cfg.Providers {
		if _, dup := c.providers[p.ID()]; dup {
			return nil, fmt.Errorf("llm: provider %q configured twice", p.ID())
		}
		c.providers[p.ID()] = p
	}
	for key, n := range cfg.Limits {
		if n > 0 {
			c.limits[key] = make(chan struct{}, n)
		}
	}
	if c.def == "" {
		c.def = cfg.Providers[0].ID()
	}
	if _, ok := c.providers[c.def]; !ok {
		return nil, fmt.Errorf("llm: default provider %q: %w", c.def, ErrUnknownProvider)
	}
	return c, nil
}

// Route resolves a provider and model name strictly. Empty names take the
// deployment default and the provider's default model.
func (c *Client) Route(ctx context.Context, provider, model string) (Route, error) {
	if provider == "" {
		provider = c.def
		if model == "" {
			model = c.defModel
		}
	}
	p, ok := c.providers[provider]
	if !ok {
		return Route{}, fmt.Errorf("provider %q: %w", provider, ErrUnknownProvider)
	}
	r, err := p.Route(ctx, model)
	if err != nil {
		return Route{}, fmt.Errorf("provider %q: %w", provider, err)
	}
	return r, nil
}

// Provider returns a configured provider by name, for tools such as the
// probe that drive one directly.
func (c *Client) Provider(name string) (Provider, error) {
	if name == "" {
		name = c.def
	}
	p, ok := c.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q: %w", name, ErrUnknownProvider)
	}
	return p, nil
}

// Affinity returns the pinning key for a contact: a keyed hash, so the raw
// contact ID never leaves the process.
func (c *Client) Affinity(contact core.ContactID) string {
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte(strconv.FormatUint(uint64(contact), 10)))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// Attempt records one model call: what was sent and how it went.
type Attempt struct {
	Route    string
	BodyHash string // hex xxhash64 of the frozen request body
	Bytes    int
	Effort   Effort // as sent, after mapping
	Tries    int
	// Prompt and Predict are in the planner's estimated tokens: the size of
	// the whole prompt, and how much of it was expected to be read from
	// cache. Usage is in the provider's own tokens, so the two pairs are
	// compared as ratios, never directly.
	Prompt  int
	Predict int
	Usage   Usage
	Stop    StopReason
	Class   ErrorClass // set when the call failed
	Elapsed time.Duration
}

// Call is one admitted model call.
type Call struct {
	stream  Stream
	marks   []plan.Mark
	attempt Attempt
	start   time.Time
	release func()
	once    sync.Once
	res     Result
	err     error
	done    bool
}

// Call freezes and sends one model call. An error means the call never
// started streaming; the returned Call is still non-nil when a request was
// frozen, so that its Attempt can be logged.
func (c *Client) Call(ctx context.Context, r Route, p *plan.PromptPlan, o Options) (*Call, error) {
	prov, ok := c.providers[r.Provider]
	if !ok {
		return nil, fmt.Errorf("provider %q: %w", r.Provider, ErrUnknownProvider)
	}
	if r.Caps.Affinity == AffinityNone {
		o.Affinity = ""
	}
	f, err := prov.Prepare(ctx, r, p, o)
	if err != nil {
		return nil, fmt.Errorf("prepare request for %s: %w", r.ID(), err)
	}
	call := &Call{
		start: time.Now(),
		attempt: Attempt{
			Route:    r.ID(),
			BodyHash: strconv.FormatUint(xxhash.Sum64(f.Body), 16),
			Bytes:    len(f.Body),
			Effort:   f.Effort,
			Predict:  f.Predict,
		},
		marks:   f.Marks,
		release: func() {},
	}
	if n := len(f.Marks); n > 0 {
		call.attempt.Prompt = f.Marks[n-1].Tokens
	}
	fail := func(err error) (*Call, error) {
		call.attempt.Class, call.attempt.Elapsed = ClassOf(err), time.Since(call.start)
		call.done, call.err = true, err
		return call, err
	}

	// Admission: wait for a slot under the provider's concurrency limit.
	slots := c.limits[r.ID()]
	if slots == nil {
		slots = c.limits[r.Provider]
	}
	if slots != nil {
		select {
		case slots <- struct{}{}:
			call.release = func() { <-slots }
		case <-ctx.Done():
			return fail(fmt.Errorf("wait for a %s slot: %w", r.Provider, ctx.Err()))
		}
	}

	for try := 1; ; try++ {
		call.attempt.Tries = try
		stream, err := prov.Send(ctx, f)
		if err == nil {
			call.stream = stream
			return call, nil
		}
		// Only a call that never started streaming is retried: nothing has
		// reached a subscriber, and the frozen request goes out unchanged.
		if !ClassOf(err).Retryable() || try >= c.retry.Attempts || ctx.Err() != nil {
			call.release()
			return fail(err)
		}
		wait := c.backoff(try, err)
		c.log.WarnContext(ctx, "model call failed before streaming; retrying",
			"route", r.ID(), "try", try, "error_class", ClassOf(err).String(), "wait_ms", wait.Milliseconds())
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			call.release()
			return fail(fmt.Errorf("retry %s: %w", r.ID(), ctx.Err()))
		}
	}
}

func (c *Client) backoff(try int, err error) time.Duration {
	if e, ok := errors.AsType[*Error](err); ok && e.RetryAfter > 0 {
		return min(e.RetryAfter, c.retry.MaxRetryAfter)
	}
	d := min(c.retry.Base<<(try-1), c.retry.Max)
	// Jitter spreads retries from contacts that failed together.
	return d/2 + rand.N(d/2+1) //nolint:gosec // jitter, not a secret
}

// Events yields the stream's deltas.
func (c *Call) Events() iter.Seq[Event] {
	return func(yield func(Event) bool) {
		if c.stream == nil {
			return
		}
		for ev := range c.stream.Events() {
			if !yield(ev) {
				return
			}
		}
	}
}

// Result reports how the call ended. It is valid only after Events has
// returned, and it frees the call's admission slot.
func (c *Call) Result() (Result, error) {
	c.once.Do(func() {
		defer c.release()
		if c.done {
			return
		}
		c.res, c.err = c.stream.Result()
		c.attempt.Elapsed = time.Since(c.start)
		c.attempt.Usage, c.attempt.Stop = c.res.Usage, c.res.Stop
		if c.err != nil {
			c.attempt.Class = ClassOf(c.err)
		}
	})
	return c.res, c.err
}

// Marks returns the prefix marks of the prompt that was sent. The next
// request's cache prediction is made against them.
func (c *Call) Marks() []plan.Mark { return c.marks }

// Attempt returns the record of this call. It is complete after Result.
func (c *Call) Attempt() Attempt { return c.attempt }
