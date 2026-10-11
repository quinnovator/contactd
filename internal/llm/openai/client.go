// Package openai adapts the Chat Completions API to turn.LLM and
// turn.Summarizer.
//
// Chat Completions, not the Responses API, because vLLM and SGLang serve the
// same wire format: one adapter covers OpenAI and self-hosted models. None of
// these take explicit cache breakpoints. They reuse whatever prefix of the
// request matches an earlier one byte for byte, so the adapter's whole job
// for the cache is to render a plan the same way every time (Piece 5).
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/turn"
)

const (
	defaultBaseURL = "https://api.openai.com/v1"
	errorExcerpt   = 512
)

// Sentinel errors, checked with errors.Is.
var (
	// ErrIncomplete means the stream ended, or the caller stopped reading,
	// before the model finished.
	ErrIncomplete = errors.New("openai: stream ended before the model stopped")
	// ErrFiltered means the call ended with finish_reason "content_filter".
	// Any text already streamed is partial and must not be treated as a reply.
	ErrFiltered = errors.New("openai: the reply was cut off by a content filter")
	// ErrBadToolInput means the model's tool arguments were not a JSON object
	// with a string "code".
	ErrBadToolInput = errors.New("openai: tool arguments are not an object with a string code")
	// ErrNotDrained means Stop was called before Events had returned.
	ErrNotDrained = errors.New("openai: stop called before events were read")
	// ErrNoModel means neither the plan nor the config names a model.
	ErrNoModel = errors.New("openai: no model configured")
)

// APIError is a failure reported by the server: a non-2xx response, or an
// error object in the middle of a stream (Status is 0 then).
type APIError struct {
	Status    int
	Type      string
	Code      string
	Message   string
	Body      string // start of the response body, for non-2xx responses
	RequestID string
}

func (e *APIError) Error() string {
	if e.Status == 0 {
		return "openai: stream error " + e.Type + ": " + e.Message
	}
	return fmt.Sprintf("openai: status %d: %s", e.Status, e.Body)
}

func (e *APIError) retryable() bool {
	if e.Status != 0 {
		return e.Status == http.StatusTooManyRequests || e.Status >= 500
	}
	// Mid-stream errors carry no status; servers disagree on whether the
	// kind goes in type or code, and some put an HTTP status in code.
	if n, err := strconv.Atoi(e.Code); err == nil {
		return n == http.StatusTooManyRequests || n >= 500
	}
	for _, kind := range []string{e.Type, e.Code} {
		switch kind {
		case "server_error", "overloaded_error", "rate_limit_exceeded", "rate_limit_error", "service_unavailable":
			return true
		}
	}
	return false
}

// transientError marks a failure with no HTTP status that is still worth
// retrying: a refused connection, or a stream cut off before its first event.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// Config configures a Client.
type Config struct {
	// APIKey is sent as a bearer token. It may be empty for a self-hosted
	// server that does not check one.
	APIKey string
	// BaseURLs lists interchangeable replicas; default
	// ["https://api.openai.com/v1"]. With more than one, each contact is
	// pinned to a replica.
	BaseURLs     []string
	Model        string // used when plan.Model is empty
	SummaryModel string // for Summarize; falls back to Model
	MaxTokens    int    // used when plan.MaxTokens is 0; 0 leaves the server default
	// PromptCacheKey sends prompt_cache_key "contact-<id>", which helps
	// OpenAI route a contact's requests to the machine holding its cache. It
	// is off by default because self-hosted servers may reject the field.
	PromptCacheKey bool
	HTTPClient     *http.Client
	Logger         *slog.Logger
}

// replica is one inference server.
type replica struct {
	endpoint string
	// seed is a hash of the base URL: the replica's identity for rendezvous
	// hashing, independent of its position in the config.
	seed uint64
}

// Client calls a Chat Completions API. It is safe for concurrent use.
type Client struct {
	apiKey       string
	replicas     []replica
	model        string
	summaryModel string
	maxTokens    int
	cacheKey     bool
	http         *http.Client
	log          *slog.Logger
	// baseDelay is the first retry delay. Tests shorten it.
	baseDelay time.Duration
}

// New validates cfg and fills in defaults.
func New(cfg Config) (*Client, error) {
	if cfg.MaxTokens < 0 {
		return nil, errors.New("openai: max tokens must not be negative")
	}
	bases := cfg.BaseURLs
	if len(bases) == 0 {
		bases = []string{defaultBaseURL}
	}
	c := &Client{
		apiKey:       cfg.APIKey,
		replicas:     make([]replica, 0, len(bases)),
		model:        cfg.Model,
		summaryModel: cfg.SummaryModel,
		maxTokens:    cfg.MaxTokens,
		cacheKey:     cfg.PromptCacheKey,
		http:         cfg.HTTPClient,
		log:          cfg.Logger,
		baseDelay:    defaultBaseDelay,
	}
	seen := make(map[string]bool, len(bases))
	for _, base := range bases {
		base = strings.TrimRight(base, "/")
		u, err := url.Parse(base)
		if err != nil {
			return nil, fmt.Errorf("openai: base url: %w", err)
		}
		if u.Scheme == "" || u.Host == "" {
			return nil, errors.New("openai: base url needs a scheme and a host")
		}
		if seen[base] {
			return nil, errors.New("openai: duplicate base url")
		}
		seen[base] = true
		h := fnv.New64a()
		_, _ = h.Write([]byte(base))
		c.replicas = append(c.replicas, replica{endpoint: base + "/chat/completions", seed: h.Sum64()})
	}
	if c.summaryModel == "" {
		c.summaryModel = c.model
	}
	if c.http == nil {
		// No client timeout: a stream lives as long as its context.
		c.http = &http.Client{}
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	return c, nil
}

// pick chooses the replica for key by rendezvous (highest-random-weight)
// hashing: every replica scores the key and the highest score wins. A
// contact therefore keeps hitting the GPU that already holds its KV prefix,
// and adding a replica moves only the contacts the newcomer outscores,
// about 1/N of them, instead of reshuffling everyone.
func (c *Client) pick(key uint64) *replica {
	best := &c.replicas[0]
	if len(c.replicas) == 1 {
		return best
	}
	k := mix64(key)
	bestScore := mix64(best.seed ^ k)
	for i := 1; i < len(c.replicas); i++ {
		if s := mix64(c.replicas[i].seed ^ k); s > bestScore {
			best, bestScore = &c.replicas[i], s
		}
	}
	return best
}

// mix64 is the splitmix64 finalizer. Contact IDs are small consecutive
// integers; this spreads them over the whole 64-bit range.
func mix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func (c *Client) replicaFor(contact core.ContactID) *replica {
	return c.pick(uint64(contact))
}

// Stream starts one model call. It returns once the server has accepted the
// request; a non-2xx answer is returned here, later failures from Stop.
func (c *Client) Stream(ctx context.Context, p *plan.PromptPlan) (turn.Stream, error) {
	if p == nil {
		return nil, errors.New("openai: nil plan")
	}
	req, err := c.buildRequest(p)
	if err != nil {
		return nil, err
	}
	body, err := marshal(req)
	if err != nil {
		return nil, err
	}
	// Retries stay on the contact's replica: another one would start cold.
	cl := call{endpoint: c.replicaFor(p.Contact).endpoint, body: body, stream: true, contact: p.Contact}
	attempt := 0
	//nolint:bodyclose // closed by consume, or by Stop when the stream is never read
	resp, err := c.send(ctx, cl, &attempt)
	if err != nil {
		return nil, err
	}
	return &stream{
		// The closure carries ctx so the stream does not have to hold it.
		run: func(yield func(turn.StreamEvent) bool) (turn.Stop, error) {
			return c.consume(ctx, cl, resp, attempt, yield)
		},
		abandon: func() { _ = resp.Body.Close() },
	}, nil
}

// call is one request, kept so it can be sent again unchanged.
type call struct {
	endpoint string
	body     []byte
	stream   bool
	// contact is only for log lines; 0 when the call belongs to no contact.
	contact core.ContactID
}

// send posts cl and returns the response once the server answers 2xx. Rate
// limits, server errors and transport failures are retried; attempt counts
// tries across the caller's own retries so the total stays bounded.
func (c *Client) send(ctx context.Context, cl call, attempt *int) (*http.Response, error) {
	for {
		resp, err := c.post(ctx, cl)
		if err == nil && resp.StatusCode/100 == 2 {
			return resp, nil
		}
		var hdr http.Header
		if err == nil {
			hdr = resp.Header
			err = readAPIError(resp)
		}
		if err := c.pause(ctx, cl, attempt, hdr, err); err != nil {
			return nil, err
		}
	}
}

func (c *Client) post(ctx context.Context, cl call) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.endpoint, bytes.NewReader(cl.body))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if cl.stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &transientError{fmt.Errorf("openai: send request: %w", err)}
	}
	return resp, nil
}

// pause decides whether cause is worth another try. It returns nil after
// waiting out the backoff, or the error the caller should give up with.
func (c *Client) pause(ctx context.Context, cl call, attempt *int, hdr http.Header, cause error) error {
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("openai: %w", cerr)
	}
	if !retryable(cause) || *attempt+1 >= maxAttempts {
		return cause
	}
	wait := backoff(c.baseDelay, *attempt, hdr)
	*attempt++
	status, kind := 0, "transport"
	if apiErr, ok := errors.AsType[*APIError](cause); ok {
		status, kind = apiErr.Status, apiErr.Type
	}
	// The error body is left out on purpose: it can quote the prompt.
	c.log.WarnContext(ctx, "llm_request_retry",
		"provider", "openai",
		"contact_id", uint64(cl.contact),
		"attempt", *attempt,
		"status", status,
		"error_type", kind,
		"delay_ms", wait.Milliseconds(),
	)
	return sleep(ctx, wait)
}

func retryable(err error) bool {
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		return apiErr.retryable()
	}
	var transient *transientError
	return errors.As(err, &transient)
}

// errorEnvelope covers the two error shapes in use: OpenAI and current vLLM
// nest the details under "error"; older self-hosted servers put them at the
// top level.
type errorEnvelope struct {
	Error   *errorBody      `json:"error"`
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"`
}

type errorBody struct {
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"` // a string from OpenAI, a number from vLLM
}

// fill copies the error details into e. It reports whether env held any.
func (env *errorEnvelope) fill(e *APIError) bool {
	switch {
	case env.Error != nil:
		e.Type, e.Message, e.Code = env.Error.Type, env.Error.Message, codeString(env.Error.Code)
	case env.Message != "":
		e.Type, e.Message, e.Code = env.Type, env.Message, codeString(env.Code)
	default:
		return false
	}
	return true
}

func codeString(raw json.RawMessage) string {
	s := strings.Trim(string(raw), `"`)
	if s == "null" {
		return ""
	}
	return s
}

// readAPIError turns a non-2xx response into an APIError and closes it.
func readAPIError(resp *http.Response) error {
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	e := &APIError{
		Status:    resp.StatusCode,
		Body:      excerpt(raw),
		RequestID: resp.Header.Get("X-Request-Id"),
	}
	var env errorEnvelope
	if err := unmarshal(raw, &env); err == nil {
		env.fill(e)
	}
	return e
}

func excerpt(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > errorExcerpt {
		s = s[:errorExcerpt]
	}
	return strings.ToValidUTF8(s, "")
}
