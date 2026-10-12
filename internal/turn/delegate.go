package turn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/plan"
)

// DelegateTool is the name of the built-in tool that starts a sub-agent.
const DelegateTool = "delegate"

// delegateSchema is written out by hand so its bytes never change: it is
// part of the static tier of every prompt that offers the tool.
const delegateSchema = `{"type":"object","properties":{` +
	`"task":{"type":"string","description":"What the sub-agent should do, with everything it needs to know. It cannot ask you questions."},` +
	`"mode":{"type":"string","enum":["spawn","fork"],"description":"spawn starts it with only your instructions and the task. fork also gives it this conversation up to your last finished reply."}` +
	`},"required":["task"],"additionalProperties":false}`

// delegateDef is a function, not a variable, so nothing can alter the
// definition every agent shares.
func delegateDef() core.ToolDef {
	return core.ToolDef{
		Name: DelegateTool,
		Description: "Start a sub-agent on a task and keep going without waiting. " +
			"The call returns at once with the sub-agent's ID. You cannot wait for it or check on it; " +
			"when it finishes you receive a notice with its result.",
		Input: json.RawMessage(delegateSchema),
	}
}

// withDelegate returns the agent the model actually sees: the configured one,
// plus the delegate tool where the agent has it. A sub-agent is shown the
// tool too, so that its static tier is byte for byte its parent's and reads
// the same cache; what keeps delegation one level deep is that its calls to
// the tool are refused.
func withDelegate(a *core.AgentConfig, on bool) *core.AgentConfig {
	if a == nil || !on || !a.Delegate {
		return a
	}
	for i := range a.Tools {
		if a.Tools[i].Name == DelegateTool {
			return a // the config names its own; leave it alone
		}
	}
	b := *a
	b.Tools = append(slices.Clip(a.Tools), delegateDef())
	return &b
}

type delegateArgs struct {
	Task string `json:"task"`
	Mode string `json:"mode"`
}

// delegate runs one call to the delegate tool. Whatever goes wrong becomes a
// result the model can read: a failed delegation never fails the turn.
func (d *Deps) delegate(ctx context.Context, in Input, p *plan.PromptPlan, call core.ToolCall) core.ToolResult {
	fail := func(msg string) core.ToolResult {
		return core.ToolResult{ID: call.ID, Output: msg, IsError: true}
	}
	if d.Delegate == nil || in.Agent == nil || !in.Agent.Delegate {
		return fail("delegation is not available to this agent")
	}
	if in.Agent.Parent != 0 {
		return fail("a sub-agent cannot start sub-agents")
	}
	var args delegateArgs
	if err := json.Unmarshal(call.Args, &args); err != nil || args.Task == "" {
		return fail(`the delegate tool needs {"task": "..."} and optionally "mode": "spawn" or "fork"`)
	}
	req := Delegation{Task: args.Task, Agent: in.Agent, Turn: in.TurnID}
	switch args.Mode {
	case "", "spawn":
	case "fork":
		req.Base = p.Head()
	default:
		return fail(`mode must be "spawn" or "fork"`)
	}
	child, err := d.Delegate.Delegate(ctx, in.Contact, req)
	switch {
	case errors.Is(err, core.ErrTooManyChildren):
		return fail("too many sub-agents are still running; wait for a notice that one has settled")
	case err != nil:
		return fail(fmt.Sprintf("the sub-agent could not be started: %v", err))
	}
	id := strconv.FormatUint(uint64(child), 10)
	return core.ToolResult{ID: call.ID, Child: child, Output: "Sub-agent " + id + " started. It runs on its own: you cannot wait for it or check on it. " +
		"A notice will tell you when it settles, with its result."}
}
