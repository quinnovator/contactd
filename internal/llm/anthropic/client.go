// Package anthropic adapts the Anthropic Messages API to turn.LLM and
// turn.Summarizer.
//
// It speaks raw HTTP instead of using the SDK because the request body is
// the cache key: a plan must serialize to the same bytes every time, with a
// cache breakpoint at the end of each of tiers 0-3 (Piece 5).
package anthropic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/turn"
)

const (
	defaultBaseURL      = "https://api.anthropic.com"
	defaultModel        = "claude-opus-5-5"
	defaultSummaryModel = "claude-haiku-5-5"
	// Thinking counts toward max_tokens on current models even though its
	// text is not returned, so the default leaves room for it.
	defaultMaxTokens = 64000

	apiVersion = "2023-06-01"
	// fallbackBeta gates the "default" form of the fallbacks parameter.
	fallbackBeta = "server-side-fallback-2026-07-01"

	errorExcerpt = 512
)

// Sentinel errors, checked with errors.Is.
var (
	// ErrIncomplete means the stream ended, or the caller stopped reading,
	// before the model finished.
	ErrIncomplete = errors.New("anthropic: stream ended before the model stopped")
	// ErrRefused means the call ended with stop_reason "refusal". Any text
	// already streamed is partial and must not be treated as a reply.
	ErrRefused = errors.New("anthropic: the model declined the request")
	// ErrBadToolInput means the model's tool arguments were not a JSON object
	// with a string "code".
	ErrBadToolInput = errors.New("anthropic: tool input is not an object with a string code")
	// ErrNotDrained means Stop was called before Events had returned.
	ErrNotDrained = errors.New("anthropic: stop called before events were read")
)

// APIError is a failure reported by the API: a non-2xx response, or an error
// event in the middle of a stream (Status is 0 then).
type APIError struct {
	Status    int
	Type      string // "invalid_request_error", "overloaded_error", ...
	Message   string
	Body      string // start of the response body, for non-2xx responses
	RequestID string
}

func (e *APIError) Error() string {
	if e.Status == 0 {
		return "anthropic: stream error " + e.Type + ": " + e.Message
	}
	return fmt.Sprintf("anthropic: status %d: %s", e.Status, e.Body)
}

func (e *APIError) retryable() bool {
	if e.Status != 0 {
		return e.Status == http.StatusTooManyRequests || e.Status >= 500
	}
	return e.Type == "overloaded_error" || e.Type == "api_error" || e.Type == "rate_limit_error"
}

// transientError marks a failure with no HTTP status that is still worth
// retrying: a refused connection, or a stream cut off before its first event.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// Config configures a Client.
type Config struct {
	APIKey       string
	BaseURL      string // default https://api.anthropic.com
	Model        string // used when plan.Model is empty
	SummaryModel string // small, cheap model for Summarize
	MaxTokens    int    // used when plan.MaxTokens is 0
	// Effort is sent as output_config.effort ("low", "medium", "high", ...)
	// when set. Left empty, the model's own default applies, which differs
	// by model. It is part of the cache key: changing it cools every cache.
	Effort string
	// NoFallback stops the adapter from asking the API to re-run a request
	// that a safety classifier declined on another model. Set it when BaseURL
	// is a gateway that rejects the beta header.
	NoFallback bool
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// Client calls the Messages API. It is safe for concurrent use.
type Client struct {
	apiKey       string
	endpoint     string
	model        string
	summaryModel string
	maxTokens    int
	effort       string
	fallback     bool
	http         *http.Client
	log          *slog.Logger
	// baseDelay is the first retry delay. Tests shorten it.
	baseDelay time.Duration
}

// New validates cfg and fills in defaults.
func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("anthropic: api key is required")
	}
	if cfg.MaxTokens < 0 {
		return nil, errors.New("anthropic: max tokens must not be negative")
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
	c := &Client{
		apiKey:       cfg.APIKey,
		endpoint:     strings.TrimRight(base, "/") + "/v1/messages",
		model:        cfg.Model,
		summaryModel: cfg.SummaryModel,
		maxTokens:    cfg.MaxTokens,
		effort:       cfg.Effort,
		fallback:     !cfg.NoFallback,
		http:         cfg.HTTPClient,
		log:          cfg.Logger,
		baseDelay:    defaultBaseDelay,
	}
	if c.model == "" {
		c.model = defaultModel
	}
	if c.summaryModel == "" {
		c.summaryModel = defaultSummaryModel
	}
	if c.maxTokens == 0 {
		c.maxTokens = defaultMaxTokens
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

// Stream starts one model call. It returns once the API has accepted the
// request; a non-2xx answer is returned here, later failures from Stop.
func (c *Client) Stream(ctx context.Context, p *plan.PromptPlan) (turn.Stream, error) {
	if p == nil {
		return nil, errors.New("anthropic: nil plan")
	}
	req := c.buildRequest(p)
	body, err := marshal(req)
	if err != nil {
		return nil, err
	}
	cl := call{body: body, stream: true, contact: p.Contact}
	if req.Fallbacks != "" {
		cl.beta = fallbackBeta
	}
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
	body   []byte
	beta   string
	stream bool
	// contact is only for log lines; 0 when the call belongs to no contact.
	contact core.ContactID
}

// send posts cl and returns the response once the API answers 2xx. Rate
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(cl.body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Anthropic-Version", apiVersion)
	if cl.beta != "" {
		req.Header.Set("Anthropic-Beta", cl.beta)
	}
	if cl.stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &transientError{fmt.Errorf("anthropic: send request: %w", err)}
	}
	return resp, nil
}

// pause decides whether cause is worth another try. It returns nil after
// waiting out the backoff, or the error the caller should give up with.
func (c *Client) pause(ctx context.Context, cl call, attempt *int, hdr http.Header, cause error) error {
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("anthropic: %w", cerr)
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
		"provider", "anthropic",
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

// readAPIError turns a non-2xx response into an APIError and closes it.
func readAPIError(resp *http.Response) error {
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	e := &APIError{
		Status:    resp.StatusCode,
		Body:      excerpt(raw),
		RequestID: resp.Header.Get("Request-Id"),
	}
	var env errorEnvelope
	if err := unmarshal(raw, &env); err == nil && env.Error != nil {
		e.Type, e.Message = env.Error.Type, env.Error.Message
		if e.RequestID == "" {
			e.RequestID = env.RequestID
		}
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
