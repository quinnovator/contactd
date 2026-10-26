package turn

import (
	"context"

	"github.com/quinnovator/contactd/internal/core"
)

// BeginTool and EndTool expose the turn side of Control to tests of its
// interleaving with the actor side.
func BeginTool(c *Control) bool { return c.beginTool() }

func EndTool(c *Control) bool { return c.endTool() }

// HelperTools exposes the static helper tool contract to its golden tests.
func HelperTools(a *core.AgentConfig, d *Deps) *core.AgentConfig { return withHelperTools(a, d) }

// HelperTool runs a built-in without a model for argument and role tests.
func HelperTool(ctx context.Context, d *Deps, in Input, c core.ToolCall) core.ToolResult {
	return d.helperTool(ctx, in, c)
}
