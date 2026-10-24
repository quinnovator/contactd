package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/quinnovator/contactd/internal/core"
)

// ToolProvider is an optional provider extension for tools executed by the
// upstream API. Definitions are part of the plan before it is hashed; calls
// still pass through the turn's ordinary logging and interruption handling.
type ToolProvider interface {
	Tools(r Route) []core.ToolDef
	RunTool(ctx context.Context, r Route, call core.ToolCall) (string, error)
}

// Tools returns the tools available on this route. Most providers have none.
func (c *Client) Tools(r Route) []core.ToolDef {
	if p, ok := c.providers[r.Provider].(ToolProvider); ok {
		return p.Tools(r)
	}
	return nil
}

// RunTool executes a provider tool, retrying transient failures. These APIs
// have their own rate limits, separate from model concurrency admission.
func (c *Client) RunTool(ctx context.Context, r Route, call core.ToolCall) (string, error) {
	p, ok := c.providers[r.Provider].(ToolProvider)
	if !ok {
		return "", fmt.Errorf("provider %q tool %q: %w", r.Provider, call.Name, core.ErrUnknownTool)
	}
	known := false
	for _, def := range p.Tools(r) {
		known = known || def.Name == call.Name
	}
	if !known {
		return "", fmt.Errorf("provider %q tool %q: %w", r.Provider, call.Name, core.ErrUnknownTool)
	}
	for try := 1; ; try++ {
		out, err := p.RunTool(ctx, r, call)
		if err == nil {
			return out, nil
		}
		if !ClassOf(err).Retryable() || try >= c.retry.Attempts || ctx.Err() != nil {
			return out, fmt.Errorf("provider %q tool %q: %w", r.Provider, call.Name, err)
		}
		timer := time.NewTimer(c.backoff(try, err))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return "", fmt.Errorf("retry tool %s: %w", call.Name, ctx.Err())
		}
	}
}
