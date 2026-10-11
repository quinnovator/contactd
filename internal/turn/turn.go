// Package turn runs one turn: embed the query, recall episodes, build the
// cache-aware plan, stream the model, lease an environment only when a tool
// call needs one, and stop or re-plan if interrupted (Piece 8).
//
// A turn runs on its own goroutine and never touches actor state. It reads
// immutable snapshots handed to it in Input and reports everything back
// through Sink.
package turn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/recall"
)

// Sink is how a turn talks back to its actor. Every method blocks only until
// the actor has taken the item or ctx is done.
type Sink interface {
	// Delta forwards a fragment for live subscribers.
	Delta(ctx context.Context, d core.Delta) error
	// ToolCall and ToolResult hand an event to the actor, which appends it.
	// Only the actor writes to the log.
	//
	// ToolCall also starts the tool phase. The implementation must call enter
	// on the goroutine that serializes the contact's messages, and record the
	// call only if enter returns true; otherwise the turn was cancelled first
	// and ToolCall returns an error. Doing the transition there, rather than
	// on the turn's goroutine, is what makes "arrived before the call" and
	// "arrived during the tool" mean the same thing to both sides.
	ToolCall(ctx context.Context, call core.ToolCall, enter func() bool) error
	ToolResult(ctx context.Context, name string, res core.ToolResult) error
}

// Deps are the services a turn calls. They are shared by every contact.
type Deps struct {
	Embed  recall.Embedder
	Rerank recall.Reranker
	Log    recall.EventReader
	LLM    LLM
	Logger *slog.Logger

	Recall recall.Params
	Plan   plan.Params
	// MaxToolSteps bounds model calls per turn.
	MaxToolSteps int
	// MaxToolOutput bounds, in bytes, what one tool result adds to the prompt.
	MaxToolOutput int
	// QueryTokens bounds the recall query.
	QueryTokens int
}

// Input is one turn's view of its contact. Everything in it is immutable or
// independently synchronized.
type Input struct {
	Contact  core.ContactID
	TurnID   uint64
	Agent    *core.AgentConfig
	Profile  string
	Tail     []core.Event     // snapshot of the hot window, ending with the new message(s)
	Episodes *recall.Snapshot // immutable snapshot of the episode index
	LastPlan *plan.PromptPlan // plan of the last committed turn, or nil
	Env      *Env
	Ctl      *Control
	Sink     Sink
	Started  time.Time // when the debounce fired
}

// Result is what a finished turn hands to commitTurn.
type Result struct {
	Final  string // the reply, when the model finished one
	Plan   *plan.PromptPlan
	Replan bool // a message arrived mid-tool; start over with it
	// StepLimit means MaxToolSteps model calls were used without a final reply.
	StepLimit bool
	Usage     Usage
}

// Run executes one turn.
func Run(ctx context.Context, in Input, d *Deps) (Result, error) {
	log := d.Logger.With("contact_id", uint64(in.Contact), "turn_id", in.TurnID)
	now := time.Now().UnixMilli()

	recalled, err := d.recall(ctx, log, in, now)
	if err != nil {
		return Result{}, err
	}
	p, err := plan.Build(in.LastPlan, plan.Input{
		Contact: in.Contact,
		Agent:   in.Agent,
		Profile: in.Profile,
		Tail:    in.Tail,
	}, recalled, now, d.Plan)
	if err != nil {
		return Result{}, fmt.Errorf("build plan: %w", err)
	}
	obs.CacheBreakTier.Observe(float64(p.FirstChanged(in.LastPlan)))
	if total := p.InputTokens(); total > 0 {
		obs.PredictedCachedTokenRatio.Observe(float64(p.PredictCached(in.LastPlan, d.Plan)) / float64(total))
	}

	res := Result{Plan: p}
	for step := range d.MaxToolSteps {
		if step == 0 {
			obs.TurnOverheadMS.Observe(obs.SinceMS(in.Started))
		}
		stop, err := d.call(ctx, in, p)
		if err != nil {
			return res, err
		}
		res.Usage.InputTokens += stop.Usage.InputTokens
		res.Usage.CachedTokens += stop.Usage.CachedTokens
		res.Usage.OutputTokens += stop.Usage.OutputTokens
		log.DebugContext(ctx, "model call finished",
			"step", step,
			"input_tokens", stop.Usage.InputTokens,
			"cached_tokens", stop.Usage.CachedTokens,
			"output_tokens", stop.Usage.OutputTokens,
			"tool_call", stop.ToolCall != nil)

		if stop.ToolCall == nil {
			res.Final = stop.Text
			return res, nil
		}

		call := *stop.ToolCall
		call.Text = stop.Text
		spec, known := toolEnv(in.Agent, call.Name)
		call.Env = spec
		// The call is logged before it runs, so it is durable mid-turn and a
		// message arriving during the run sorts after it.
		if err := in.Sink.ToolCall(ctx, call, in.Ctl.beginTool); err != nil {
			return res, fmt.Errorf("turn cancelled before tool run: %w", err)
		}
		result := d.runTool(ctx, log, in, call, known)
		if err := ctx.Err(); err != nil {
			return res, fmt.Errorf("tool run: %w", err)
		}
		if err := in.Sink.ToolResult(ctx, call.Name, result); err != nil {
			return res, fmt.Errorf("record tool result: %w", err)
		}
		p.AppendTail(call, result) // append-only, cache-safe
		if in.Ctl.endTool() {
			res.Replan = true
			return res, nil
		}
	}
	res.StepLimit = true
	return res, nil
}

// call streams one model call and returns how it ended.
func (d *Deps) call(ctx context.Context, in Input, p *plan.PromptPlan) (Stop, error) {
	stream, err := d.LLM.Stream(ctx, p)
	if err != nil {
		return Stop{}, fmt.Errorf("start model call: %w", err)
	}
	var sinkErr error
	for ev := range stream.Events() {
		switch ev.Type {
		case TextDelta:
			in.Ctl.streaming()
			sinkErr = in.Sink.Delta(ctx, core.Delta{Type: core.DeltaText, Text: ev.Text})
		case ToolStart:
			sinkErr = in.Sink.Delta(ctx, core.Delta{Type: core.DeltaToolStart, Tool: ev.Name})
			// The name arrives before the arguments: start a slow restore now.
			if spec, ok := toolEnv(in.Agent, ev.Name); ok && spec.Tier == 1 {
				in.Env.Prewarm(ctx, spec)
			}
		}
		if sinkErr != nil {
			break // the actor is gone or the turn was cancelled
		}
	}
	stop, err := stream.Stop()
	if sinkErr != nil {
		return Stop{}, fmt.Errorf("forward delta: %w", sinkErr)
	}
	if err != nil {
		return Stop{}, fmt.Errorf("model call: %w", err)
	}
	obs.ProviderInputTokens.Add(float64(stop.Usage.InputTokens))
	obs.ProviderCachedTokens.Add(float64(stop.Usage.CachedTokens))
	obs.ProviderOutputTokens.Add(float64(stop.Usage.OutputTokens))
	if stop.Usage.InputTokens > 0 {
		obs.ProviderCachedTokenRatio.Observe(float64(stop.Usage.CachedTokens) / float64(stop.Usage.InputTokens))
	}
	return stop, nil
}

// recall picks the episodes for tier 2. A failure here degrades the turn to
// "no new recall" rather than failing it: the contact still gets a reply.
func (d *Deps) recall(ctx context.Context, log *slog.Logger, in Input, now int64) ([]plan.Recalled, error) {
	if in.Episodes.Len() == 0 {
		return nil, nil
	}
	lay, err := plan.Layout(in.LastPlan, in.Tail, d.Plan)
	if err != nil {
		return nil, fmt.Errorf("lay out tail: %w", err)
	}
	// The new message alone is often too vague ("ok do that again"), so the
	// reply just before it joins the query, but only when it belongs to the
	// same conversation: yesterday's reply says nothing about today's message.
	lead := ""
	if lay.TS-lay.LastReplyTS <= d.Recall.GapMS {
		lead = lay.LastReply
	}
	qText := recall.QueryText(lay.Texts, lead, d.QueryTokens)
	tailStart := lay.TailStart
	keepPrev := func() []plan.Recalled {
		// Hold a warm recall set steady through a transient failure; the
		// planner reuses its bytes when the IDs are unchanged.
		if in.LastPlan == nil || now-in.LastPlan.BuiltAt >= d.Plan.CacheTTL {
			return nil
		}
		out := make([]plan.Recalled, len(in.LastPlan.Recall))
		for i, id := range in.LastPlan.Recall {
			out[i] = plan.Recalled{ID: id}
		}
		return out
	}

	if qText == "" {
		return keepPrev(), nil // nothing new to look anything up for
	}
	vecs, err := d.Embed.Embed(ctx, []string{qText}, recall.Critical)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("embed query: %w", ctx.Err())
		}
		log.WarnContext(ctx, "query embedding failed; skipping recall", "error", err)
		return keepPrev(), nil
	}
	scored, degraded, err := recall.Recall(ctx, in.Episodes, recall.Query{
		Contact:   in.Contact,
		Text:      qText,
		Vec:       vecs[0],
		TailStart: tailStart,
		Now:       now,
	}, d.Log, d.Rerank, d.Recall)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("recall: %w", ctx.Err())
		}
		log.WarnContext(ctx, "recall failed; skipping", "error", err)
		return keepPrev(), nil
	}
	if degraded {
		log.WarnContext(ctx, "rerank failed; using stage-1 recall")
		if prev := keepPrev(); prev != nil {
			return prev, nil
		}
	}

	fresh := make([]plan.Scored, 0, len(scored))
	for _, s := range scored {
		if s.Score >= d.Recall.Theta {
			fresh = append(fresh, plan.Scored{ID: s.Ep.ID, Score: s.Score})
		}
	}
	ids := plan.ChooseRecall(in.LastPlan, fresh, now, d.Plan)
	out := make([]plan.Recalled, len(ids))
	for i, id := range ids {
		out[i] = plan.Recalled{ID: id}
		for _, s := range scored {
			if s.Ep.ID == id {
				out[i].Text = s.Passage
				break
			}
		}
	}
	return out, nil
}

// runTool executes one tool call. Failures of the tool or its environment
// become an error result the model can read and explain; only cancellation
// is left to the caller to notice.
func (d *Deps) runTool(ctx context.Context, log *slog.Logger, in Input, call core.ToolCall, known bool) core.ToolResult {
	if !known {
		return core.ToolResult{ID: call.ID, Output: "unknown tool: " + call.Name, IsError: true}
	}
	start := time.Now()
	r, err := in.Env.Exec(ctx, call.Env, call.Code)
	if err != nil {
		log.WarnContext(ctx, "tool environment failed", "tool", call.Name, "error", err)
		msg := "the environment for this tool is unavailable"
		if errors.Is(err, core.ErrNoEnvProvider) {
			msg = "this tool is not available on this deployment"
		}
		return core.ToolResult{ID: call.ID, Output: msg, IsError: true}
	}
	log.DebugContext(ctx, "tool finished",
		"tool", call.Name, "exit_code", r.ExitCode, "ms", obs.SinceMS(start))
	return core.ToolResult{ID: call.ID, Output: formatOutput(r, d.MaxToolOutput), IsError: r.ExitCode != 0}
}

func formatOutput(r ExecResult, limit int) string {
	var b strings.Builder
	b.WriteString(clip(r.Stdout, limit/2))
	if r.Stderr != "" {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("[stderr]\n")
		b.WriteString(clip(r.Stderr, limit/2))
	}
	if r.ExitCode != 0 {
		b.WriteString("\n[exit code ")
		b.WriteString(strconv.Itoa(r.ExitCode))
		b.WriteByte(']')
	}
	if r.Note != "" {
		// Tell the agent what persisted, so it doesn't assume variables
		// survived a long gap.
		b.WriteString("\n[note: ")
		b.WriteString(r.Note)
		b.WriteByte(']')
	}
	if b.Len() == 0 {
		return "[no output]"
	}
	return b.String()
}

// clip keeps the head and tail of s when it exceeds limit bytes; the two ends
// are where the useful parts of long output usually are.
func clip(s string, limit int) string {
	if len(s) <= limit || limit <= 0 {
		return s
	}
	head, tail := limit*2/3, limit/3
	for head > 0 && !isRuneStart(s[head]) {
		head--
	}
	cut := len(s) - tail
	for cut < len(s) && !isRuneStart(s[cut]) {
		cut++
	}
	return s[:head] + "\n[... " + strconv.Itoa(cut-head) + " bytes omitted ...]\n" + s[cut:]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// toolEnv is the tool registry: the environment a named tool runs in.
func toolEnv(agent *core.AgentConfig, name string) (core.EnvSpec, bool) {
	if agent == nil {
		return core.EnvSpec{}, false
	}
	for i := range agent.Tools {
		if agent.Tools[i].Name == name {
			return agent.Tools[i].Env, true
		}
	}
	return core.EnvSpec{}, false
}
