package turn

import (
	"context"
	"slices"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
)

// withProviderTools adds upstream tools without changing the stored agent
// config. An explicitly configured tool of the same name takes precedence.
func withProviderTools(a *core.AgentConfig, defs []core.ToolDef) (*core.AgentConfig, []core.ToolDef) {
	if len(defs) == 0 {
		return a, nil
	}
	b := *a
	b.Tools = slices.Clone(a.Tools)
	var added []core.ToolDef
	for _, def := range defs {
		if findTool(&b, def.Name) == nil {
			b.Tools = append(b.Tools, def)
			added = append(added, def)
		}
	}
	return &b, added
}

func (d *Deps) providerTool(ctx context.Context, in Input, call core.ToolCall, def *core.ToolDef) (core.ToolResult, bool) {
	p, ok := d.Models.(llm.ToolProvider)
	if !ok || def == nil || len(def.Input) == 0 {
		return core.ToolResult{}, false
	}
	for _, native := range in.nativeTools {
		if native.Name != call.Name {
			continue
		}
		out, err := p.RunTool(ctx, in.route, call)
		if err != nil {
			out = err.Error()
		}
		return core.ToolResult{ID: call.ID, Output: clip(out, d.MaxToolOutput), IsError: err != nil}, true
	}
	return core.ToolResult{}, false
}
