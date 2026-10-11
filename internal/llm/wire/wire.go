// Package wire holds what the wire codecs share: sending a frozen request,
// classifying a failed response, and reading a Server-Sent Events stream.
// The codecs themselves are in the subpackages, one per request format.
package wire

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/llm"
)

// Refine lets a codec improve on the status-based classification of a failed
// response, for example by recognising a context-overflow message in a 400.
// It returns ErrUnknown to keep the default.
type Refine func(status int, body []byte) llm.ErrorClass

// Post sends a frozen request. On a 2xx it returns the response with its
// body open for the caller to stream and close. Otherwise it returns an
// *llm.Error: classified from the status (and refine, when given), carrying
// the provider's retry hint and a short excerpt of the error body.
func Post(ctx context.Context, hc *http.Client, f *llm.Frozen, refine Refine) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.URL, bytes.NewReader(f.Body))
	if err != nil {
		return nil, &llm.Error{Class: llm.ErrBadRequest, Provider: f.Route.Provider, Err: fmt.Errorf("build request: %w", err)}
	}
	req.Header = f.Header.Clone()
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s: %w", f.Route.Provider, ctx.Err())
		}
		return nil, &llm.Error{Class: llm.ErrTransport, Provider: f.Route.Provider, Err: err}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close() // the error below is what the caller acts on
	class := Classify(resp.StatusCode)
	if refine != nil {
		if c := refine(resp.StatusCode, body); c != llm.ErrUnknown {
			class = c
		}
	}
	return nil, &llm.Error{
		Class:      class,
		RetryAfter: RetryAfter(resp.Header),
		Status:     resp.StatusCode,
		Provider:   f.Route.Provider,
		Err:        errors.New(Excerpt(body)),
	}
}

// Classify maps an HTTP status to an error class.
func Classify(status int) llm.ErrorClass {
	switch {
	case status == http.StatusTooManyRequests:
		return llm.ErrRateLimited
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return llm.ErrAuth
	case status == http.StatusPaymentRequired:
		return llm.ErrQuota
	case status == http.StatusRequestEntityTooLarge:
		return llm.ErrContextOverflow
	case status == http.StatusRequestTimeout || status >= 500:
		return llm.ErrOverloaded
	case status >= 400:
		return llm.ErrBadRequest
	default:
		return llm.ErrUnknown
	}
}

// RetryAfter reads a provider's retry hint: retry-after-ms, or retry-after
// as seconds or an HTTP date. It returns 0 when there is none.
func RetryAfter(h http.Header) time.Duration {
	if v := h.Get("Retry-After-Ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms > 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if at, err := http.ParseTime(v); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}

// Excerpt shortens an error body for an error message. Bodies can echo the
// prompt, so only the start is kept.
func Excerpt(body []byte) string {
	const limit = 300
	s := strings.Join(strings.Fields(string(body)), " ")
	if s == "" {
		return "empty error body"
	}
	if len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// SSEEvent is one Server-Sent Event. Only the fields model APIs use are kept.
type SSEEvent struct {
	Name string
	Data []byte
}

// SSEReader splits a response body into events. It reads lines of any
// length, since one delta can carry a long run of text, and skips comment
// lines, which providers send as keep-alives while a request waits.
type SSEReader struct {
	r *bufio.Reader
}

// NewSSEReader reads events from r.
func NewSSEReader(r io.Reader) *SSEReader { return &SSEReader{r: bufio.NewReader(r)} }

// Next returns the next event. It returns an error wrapping io.EOF when the
// body ends cleanly.
func (s *SSEReader) Next() (SSEEvent, error) {
	var ev SSEEvent
	seen, lines := false, 0
	for {
		line, err := s.r.ReadBytes('\n')
		line = bytes.TrimRight(line, "\r\n")
		switch {
		case len(line) == 0:
			if seen && err == nil {
				return ev, nil // a blank line ends the event
			}
		case line[0] == ':':
			// Comment: a keep-alive.
		default:
			field, value, _ := bytes.Cut(line, []byte(":"))
			value = bytes.TrimPrefix(value, []byte(" "))
			switch string(field) {
			case "event":
				ev.Name, seen = string(value), true
			case "data":
				if lines > 0 {
					ev.Data = append(ev.Data, '\n')
				}
				ev.Data = append(ev.Data, value...)
				seen = true
				lines++
			}
		}
		if err != nil {
			// Some servers drop the blank line after their last event.
			if seen && errors.Is(err, io.EOF) {
				return ev, nil
			}
			return SSEEvent{}, fmt.Errorf("read stream: %w", err)
		}
	}
}
