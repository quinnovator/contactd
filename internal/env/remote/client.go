package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// maxResponseBytes caps how much of a response is read. Output is whatever
// the sandboxed code printed, so the service's word on its size is not
// enough.
const maxResponseBytes = 16 << 20

// execRequest is the body of POST /v1/exec. Exactly one of Env and Snapshot
// is set: where to run.
type execRequest struct {
	Env       string   `json:"env,omitempty"`
	Snapshot  string   `json:"snapshot,omitempty"`
	Cmd       []string `json:"cmd"`
	Stdin     string   `json:"stdin,omitempty"`
	TimeoutMS int64    `json:"timeout_ms"`
	Keep      bool     `json:"keep"`
}

// execResponse is the reply to POST /v1/exec.
type execResponse struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Exit       int    `json:"exit"`
	SnapshotID string `json:"snapshot_id"`
}

// exec runs one command. It fills in timeout_ms from the provider's
// ExecTimeout, shortened to fit ctx's deadline, and insists on the snapshot
// ID when the request asked to keep one: without it the chain cannot go on.
func (p *Provider) exec(ctx context.Context, req execRequest) (execResponse, error) {
	budget := p.timeout
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(deadline))
	}
	if budget < time.Millisecond {
		if err := ctx.Err(); err != nil {
			return execResponse{}, fmt.Errorf("exec: %w", context.Cause(ctx))
		}
		return execResponse{}, fmt.Errorf("exec: %w", context.DeadlineExceeded)
	}
	req.TimeoutMS = budget.Milliseconds()

	// The service enforces timeout_ms. This deadline is only for a service
	// that has stopped answering.
	ctx, cancel := context.WithTimeout(ctx, budget+transportGrace)
	defer cancel()
	var out execResponse
	if err := p.call(ctx, http.MethodPost, []string{"v1", "exec"}, req, &out); err != nil {
		return execResponse{}, err
	}
	if req.Keep && out.SnapshotID == "" {
		return execResponse{}, fmt.Errorf("POST /v1/exec: no snapshot_id for a kept exec: %w", ErrBadResponse)
	}
	return out, nil
}

// statusError is a reply whose HTTP status is not success. It matches
// ErrStatus.
type statusError struct {
	code   int
	status string
	detail string // the start of the body, usually the service's reason
}

func (e *statusError) Error() string {
	if e.detail == "" {
		return ErrStatus.Error() + ": " + e.status
	}
	return ErrStatus.Error() + ": " + e.status + ": " + e.detail
}

func (*statusError) Is(target error) bool { return target == ErrStatus }

// checkEnv fails unless the service has an environment called name.
func (p *Provider) checkEnv(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	err := p.call(ctx, http.MethodGet, []string{"v1", "envs", name}, nil, nil)
	var se *statusError
	if errors.As(err, &se) && se.code == http.StatusNotFound {
		return fmt.Errorf("%w %q", ErrUnknownEnv, name)
	}
	return err
}

// pin exempts a snapshot from the service's garbage collection.
func (p *Provider) pin(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	return p.call(ctx, http.MethodPost, []string{"v1", "snapshots", id, "pin"}, nil, nil)
}

// call makes one request. Each segment becomes exactly one element of the
// URL path, whatever it contains, so an ID cannot steer the request to
// another endpoint. in and out are the JSON bodies; either may be nil.
func (p *Provider) call(ctx context.Context, method string, segments []string, in, out any) error {
	escaped := make([]string, len(segments))
	for i, seg := range segments {
		// PathEscape leaves dots alone, and JoinPath would resolve them.
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("%s: %q cannot be a path element", method, seg)
		}
		escaped[i] = url.PathEscape(seg)
	}
	target := p.base.JoinPath(escaped...)
	route := method + " " + target.Path

	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("%s: encode request: %w", route, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return fmt.Errorf("%s: %w", route, err)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", route, err)
	}
	defer func() {
		// Drain what is left so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %w", route, &statusError{
			code:   resp.StatusCode,
			status: resp.Status,
			detail: string(bytes.TrimSpace(detail)),
		})
	}
	if out == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%s: read response: %w", route, err)
	}
	if len(raw) > maxResponseBytes {
		return fmt.Errorf("%s: response over %d bytes: %w", route, maxResponseBytes, ErrBadResponse)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: %w: %w", route, ErrBadResponse, err)
	}
	return nil
}
