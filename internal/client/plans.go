package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/diagnostic"
)

// Plans reads retained model-call metadata followed by live updates until
// cancelled or disconnected. Reconnect by calling it again; snapshots carry
// stable IDs so retained calls can be merged without duplication.
func (c *Client) Plans(ctx context.Context, contact string, receive func(diagnostic.Snapshot)) error {
	return c.WatchPlans(ctx, contact, PlanObserver{Snapshot: receive})
}

// PlanObserver receives connection state separately from model-call snapshots.
// Connected runs after successful response headers, even when history is empty.
type PlanObserver struct {
	Connected func()
	Snapshot  func(diagnostic.Snapshot)
}

// WatchPlans opens a diagnostic stream and observes its connection and calls.
// Callbacks run synchronously on the caller's goroutine.
func (c *Client) WatchPlans(ctx context.Context, contact string, observer PlanObserver) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/debug/plans?contact="+url.QueryEscape(contact), nil)
	if err != nil {
		return fmt.Errorf("build diagnostic request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return c.unreachable(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return &Error{Status: resp.StatusCode, Message: "diagnostic stream: " + http.StatusText(resp.StatusCode) + "; start contactd with -diagnostics"}
	}
	if observer.Connected != nil {
		observer.Connected()
	}
	scan := bufio.NewScanner(resp.Body)
	scan.Buffer(make([]byte, 4096), 4<<20)
	for scan.Scan() {
		if len(scan.Bytes()) == 0 {
			continue // heartbeat
		}
		var s diagnostic.Snapshot
		if err := json.Unmarshal(scan.Bytes(), &s); err != nil {
			return fmt.Errorf("decode diagnostic snapshot: %w", err)
		}
		if observer.Snapshot != nil {
			observer.Snapshot(s)
		}
	}
	if err := scan.Err(); err != nil {
		return fmt.Errorf("read diagnostic stream: %w", err)
	}
	return nil
}

func (c *Client) PlanDetails(ctx context.Context, run string, call uint64) (diagnostic.Details, error) {
	var d diagnostic.Details
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/debug/plans/%s/%d", url.PathEscape(run), call), nil, &d)
	return d, err
}

func (c *Client) PlanEvents(ctx context.Context, run string, call, after uint64) ([]core.Event, uint64, error) {
	return c.planEvents(ctx, run, call, after, "")
}

func (c *Client) PlanCheckpoints(ctx context.Context, run string, call, after uint64) ([]core.Event, uint64, error) {
	return c.planEvents(ctx, run, call, after, "checkpoint")
}

func (c *Client) planEvents(ctx context.Context, run string, call, after uint64, kind string) ([]core.Event, uint64, error) {
	var d struct {
		Events []core.Event `json:"events"`
		End    uint64       `json:"end"`
	}
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/debug/plans/%s/%d/events?after=%d&kind=%s", url.PathEscape(run), call, after, url.QueryEscape(kind)), nil, &d)
	return d.Events, d.End, err
}
