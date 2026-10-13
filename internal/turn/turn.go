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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/llm"
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
	// ToolResult ends the tool phase the same way: the implementation
	// records the result and then calls exit, both on the goroutine that
	// serializes the contact's messages, and returns what exit reported
	// (whether a message arrived while the tool ran). Until then the turn
	// is still in its tool phase, so a message that is handled before the
	// result waits for it and can never cancel the turn out from under a
	// result that is already on its way.
	ToolResult(ctx context.Context, name string, res core.ToolResult, exit func() bool) (replan bool, err error)
	// Attempt records one model call. It is best effort: a record that
	// cannot be delivered never fails the turn.
	Attempt(ctx context.Context, a core.AttemptBody)
}

// Deps are the services a turn calls. They are shared by every contact.
type Deps struct {
	Embed  recall.Embedder
	Rerank recall.Reranker
	Log    recall.EventReader
	Models Models
	// Delegate starts sub-agents; nil disables the delegate tool.
	Delegate Delegator
	Logger   *slog.Logger
	// Diagnostics enables bounded, in-memory prompt inspection. Nil disables it.
	Diagnostics *diagnostic.Bus

	Recall recall.Params
	Plan   plan.Params
	// Checkpoint folds the oldest part of an over-budget tail into a
	// summary instead of cutting it. The cut then happens only at one and a
	// half times the budget.
	Checkpoint bool
	// DefaultEffort and DefaultMaxOutput apply to agents that set none.
	DefaultEffort    string
	DefaultMaxOutput int
	// MaxToolSteps bounds model calls per turn.
	MaxToolSteps int
	// MaxToolOutput bounds, in bytes, what one tool result adds to the prompt.
	MaxToolOutput int
	// QueryTokens bounds the recall query.
	QueryTokens int
}

// Sent describes the last request sent for a contact: where it went, when,
// and the prefix marks of its prompt. The next request's cache prediction is
// made against it.
type Sent struct {
	Route string
	At    int64 // unix ms
	Marks []plan.Mark
}

// Input is one turn's view of its contact. Everything in it is immutable or
// independently synchronized.
type Input struct {
	recallTrace *diagnostic.Recall
	Contact     core.ContactID
	Name        string
	TurnID      uint64
	Agent       *core.AgentConfig
	Profile     string
	Tail        []core.Event     // snapshot of the hot window, ending with the new message(s)
	Episodes    *recall.Snapshot // immutable snapshot of the episode index
	LastPlan    *plan.PromptPlan // plan of the last committed turn, or nil
	// Sent is the contact's last request, or nil.
	Sent *Sent
	// Affinity is the key that pins this contact to one backend. A
	// sub-agent carries its parent's, so that it lands on the same cache.
	Affinity string
	// Base makes this turn a fork of another contact's prompt.
	Base    *plan.PromptPlan
	Env     *Env
	Ctl     *Control
	Sink    Sink
	Started time.Time // when the debounce fired
}

// Result is what a finished turn hands to commitTurn.
type Result struct {
	Final string // the reply, when the model finished one
	// Reasoning is the payload that belongs to Final. It is set only on a
	// route that must be given it back on later turns.
	Reasoning *core.Reasoning
	Stop      llm.StopReason
	Plan      *plan.PromptPlan
	Replan    bool // a message arrived mid-tool; start over with it
	// StepLimit means MaxToolSteps model calls were used without a final reply.
	StepLimit bool
	Usage     llm.Usage
	// Route and Params are what the turn ran with, and Sent its last
	// request. They are set as soon as they are known, including on a turn
	// that then fails.
	Route  llm.Route
	Params plan.Params
	Sent   *Sent
}

// Effort is the reasoning effort an agent's turns ask for: its own, or the
// deployment's default.
func (d *Deps) Effort(a *core.AgentConfig) (llm.Effort, error) {
	name := a.Effort
	if name == "" {
		name = d.DefaultEffort
	}
	e, err := llm.ParseEffort(name)
	if err != nil {
		return llm.EffortDefault, fmt.Errorf("effort: %w", err)
	}
	return e, nil
}

// params derives the planner settings for one route. The cache lifetime and
// the room for history are properties of the route, not of the deployment.
func (d *Deps) params(r *llm.Route) plan.Params {
	pr := d.Plan
	pr.CacheTTL = 0
	if r.Caps.Cache != llm.CacheNone {
		pr.CacheTTL = r.Caps.CacheTTL.Milliseconds()
	}
	if w := r.Caps.ContextWindow; w > 0 {
		pr.TailBudget = min(pr.TailBudget, w/2)
	}
	if d.Checkpoint && !pr.Slide {
		pr.HardBudget = pr.TailBudget + pr.TailBudget/2
	}
	return pr
}

// Run executes one turn.
func Run(ctx context.Context, in Input, d *Deps) (Result, error) {
	log := d.Logger.With("contact_id", uint64(in.Contact), "turn_id", in.TurnID)
	now := time.Now().UnixMilli()
	if in.Agent == nil {
		return Result{}, errors.New("turn has no agent config")
	}

	// Names resolve strictly, every turn: a model that has been retired is
	// an error the contact hears about, never a quiet substitute.
	route, err := d.Models.Route(ctx, in.Agent.Provider, in.Agent.Model)
	if err != nil {
		return Result{}, fmt.Errorf("resolve model route: %w", err)
	}
	effort, err := d.Effort(in.Agent)
	if err != nil {
		return Result{}, fmt.Errorf("agent config: %w", err)
	}
	pr := d.params(&route)
	res := Result{Route: route, Params: pr}
	in.Agent = withDelegate(in.Agent, d.Delegate != nil)
	log = log.With("route", route.ID())
	reader := plan.Reader{Route: route.ID(), ReplaysAllTurns: route.Caps.Replay == llm.ReplayAllTurns}

	if d.Diagnostics != nil {
		in.recallTrace = &diagnostic.Recall{Threshold: d.Recall.Theta, Margin: pr.Delta, Status: "no eligible episodes"}
	}
	var recalled []plan.Recalled
	if in.Base == nil {
		if recalled, err = d.recall(ctx, log, in, reader, pr, now); err != nil {
			return res, err
		}
	}
	p, err := plan.Build(in.LastPlan, plan.Input{
		Agent:   in.Agent,
		Profile: in.Profile,
		Reader:  reader,
		Tail:    in.Tail,
		Base:    in.Base,
	}, recalled, now, pr)
	if err != nil {
		return res, fmt.Errorf("build plan: %w", err)
	}
	res.Plan = p
	obs.CacheBreakTier.Observe(float64(p.FirstChanged(in.LastPlan)))

	opts := llm.Options{Affinity: in.Affinity, Effort: effort, MaxOutput: in.Agent.MaxTokens}
	if opts.MaxOutput == 0 {
		opts.MaxOutput = d.DefaultMaxOutput
	}
	// A previous request counts toward the prediction only if it went to
	// this route and its cache can still be warm.
	if s := in.Sent; s != nil && s.Route == route.ID() && now-s.At < pr.CacheTTL {
		opts.Prev = s.Marks
	}

	for step := range d.MaxToolSteps {
		if step == 0 {
			obs.TurnOverheadMS.Observe(obs.SinceMS(in.Started))
		}
		// A tool can run for longer than the route keeps a prefix. The
		// request before it then says nothing about what is cached now.
		if s := res.Sent; s != nil && time.Now().UnixMilli()-s.At >= pr.CacheTTL {
			opts.Prev = nil
		}
		out, sent, err := d.call(ctx, log, in, &route, p, opts, step)
		if sent != nil {
			res.Sent, opts.Prev = sent, sent.Marks
			// The planner and the prediction measure the cache's age from
			// the same moment: when this prompt was last sent.
			p.BuiltAt = sent.At
		}
		if err != nil {
			return res, err
		}
		res.Usage.Input += out.Usage.Input
		res.Usage.Cached += out.Usage.Cached
		res.Usage.Output += out.Usage.Output
		res.Usage.Reasoning += out.Usage.Reasoning
		res.Stop = out.Stop

		if len(out.ToolCalls) == 0 {
			res.Final = out.Text
			if route.Caps.Replay == llm.ReplayAllTurns {
				res.Reasoning = out.Reasoning
			}
			if out.Stop != llm.StopEnd {
				log.WarnContext(ctx, "model call ended early", "stop", out.Stop.String())
			}
			return res, nil
		}

		// A route that cannot be held to one call per step may return
		// several. They run one at a time, in order, each logged with its
		// result before the next starts, so an interruption takes effect
		// between calls and never in the middle of one.
		for i := range out.ToolCalls {
			call := out.ToolCalls[i]
			call.Route = route.ID()
			call.Sibling, call.Text, call.Reasoning = i, "", nil
			if i == 0 {
				call.Text = out.Text
				if route.Caps.Replay != llm.ReplayNone {
					call.Reasoning = out.Reasoning
				}
			}
			def := findTool(in.Agent, call.Name)
			if def != nil {
				call.Env = def.Env
			}
			// The call is logged before it runs, so it is durable mid-turn
			// and a message arriving during the run sorts after it. The
			// reasoning that came with it is logged only where the route
			// wants it back on later turns; within this turn the plan
			// carries it either way.
			logged := call
			if route.Caps.Replay != llm.ReplayAllTurns {
				logged.Reasoning = nil
			}
			if err := in.Sink.ToolCall(ctx, logged, in.Ctl.beginTool); err != nil {
				return res, fmt.Errorf("turn cancelled before tool run: %w", err)
			}
			result := d.runTool(ctx, log, in, p, call, def)
			if err := ctx.Err(); err != nil {
				return res, fmt.Errorf("tool run: %w", err)
			}
			replan, err := in.Sink.ToolResult(ctx, call.Name, result, in.Ctl.endTool)
			if err != nil {
				return res, fmt.Errorf("record tool result: %w", err)
			}
			p.AppendTail(call, result) // append-only, cache-safe
			if replan {
				res.Replan = true
				return res, nil
			}
		}
	}
	res.StepLimit = true
	return res, nil
}

// call streams one model call and returns how it ended, and what was sent.
func (d *Deps) call(ctx context.Context, log *slog.Logger, in Input, route *llm.Route, p *plan.PromptPlan, opts llm.Options, step int) (llm.Result, *Sent, error) {
	var watch *observation
	if d.Diagnostics != nil {
		watch = observe(d.Diagnostics, diagnostic.Scope{Contact: in.Contact, Name: in.Name, Turn: in.TurnID}, p, route, opts, d.params(route), step+1, "reply", planEvents(p, in.LastPlan, step))
	}
	if watch != nil {
		watch.enrich(in)
		opts.Prepared = watch.prepared
	}
	call, err := d.Models.Call(ctx, *route, p, opts)
	if err != nil {
		watch.finish(call, err)
		if call != nil {
			in.Sink.Attempt(ctx, attemptBody(in.TurnID, step, "", call.Attempt()))
		}
		return llm.Result{}, nil, fmt.Errorf("start model call: %w", err)
	}
	sent := &Sent{Route: route.ID(), At: time.Now().UnixMilli(), Marks: call.Marks()}
	var sinkErr error
	for ev := range call.Events() {
		watch.progress(ev.Text)
		switch ev.Kind {
		case llm.TextDelta:
			in.Ctl.streaming()
			sinkErr = in.Sink.Delta(ctx, core.Delta{Type: core.DeltaText, Text: ev.Text})
		case llm.ToolStart:
			sinkErr = in.Sink.Delta(ctx, core.Delta{Type: core.DeltaToolStart, Tool: ev.Tool})
			// The name arrives before the arguments: start a slow restore now.
			if def := findTool(in.Agent, ev.Tool); def != nil && def.Env.Tier == 1 {
				in.Env.Prewarm(ctx, def.Env)
			}
		case llm.ReasoningDelta:
			// Reasoning is not part of the reply and is not forwarded.
		}
		if sinkErr != nil {
			break // the actor is gone or the turn was cancelled
		}
	}
	out, err := call.Result()
	watch.finish(call, errors.Join(err, sinkErr))
	att := call.Attempt()
	in.Sink.Attempt(ctx, attemptBody(in.TurnID, step, "", att))
	if sinkErr != nil {
		return llm.Result{}, sent, fmt.Errorf("forward delta: %w", sinkErr)
	}
	if err != nil {
		return llm.Result{}, sent, fmt.Errorf("model call: %w", err)
	}
	d.account(ctx, log, route, &att, step)
	return out, sent, nil
}

// cacheGap is how far the reported cached share may fall below the predicted
// one before it counts as a miss worth looking at.
const cacheGap = 0.25

// account records a finished call's usage and holds the provider's reported
// cache hit against the prediction. A gap is how cache bugs get caught: on
// our side (a prefix that moved) or theirs (a declared capability that does
// not hold).
func (d *Deps) account(ctx context.Context, log *slog.Logger, route *llm.Route, att *llm.Attempt, step int) {
	u := att.Usage
	log.DebugContext(ctx, "model call finished",
		"step", step, "tries", att.Tries, "stop", att.Stop.String(),
		"input_tokens", u.Input, "cached_tokens", u.Cached, "output_tokens", u.Output,
		"predicted_cached_tokens", att.Predict)
	obs.ProviderInputTokens.Add(float64(u.Input))
	obs.ProviderOutputTokens.Add(float64(u.Output))

	predicted := 0.0
	if att.Prompt > 0 {
		predicted = float64(att.Predict) / float64(att.Prompt)
		obs.PredictedCachedTokenRatio.Observe(predicted)
	}
	if !route.Caps.CacheUsage || u.Input == 0 {
		// A route that reports no cached tokens cannot be checked, and is
		// left out of the cached share instead of dragging it to zero.
		return
	}
	reported := float64(u.Cached) / float64(u.Input)
	obs.ProviderCachedTokens.Add(float64(u.Cached))
	obs.ProviderCachedTokenRatio.Observe(reported)
	if predicted-reported > cacheGap {
		obs.CachePredictionMisses.Inc()
		log.WarnContext(ctx, "provider cached less than predicted",
			"step", step, "predicted_ratio", predicted, "reported_ratio", reported,
			"cache_mode", int(route.Caps.Cache))
	}
}

func attemptBody(turnID uint64, step int, purpose string, a llm.Attempt) core.AttemptBody {
	b := core.AttemptBody{
		Turn:      turnID,
		Step:      step,
		Purpose:   purpose,
		Route:     a.Route,
		BodyHash:  a.BodyHash,
		Bytes:     a.Bytes,
		Effort:    a.Effort.String(),
		Tries:     a.Tries,
		Estimated: a.Prompt,
		Predicted: a.Predict,
		Input:     a.Usage.Input,
		Cached:    a.Usage.Cached,
		Output:    a.Usage.Output,
		Reasoning: a.Usage.Reasoning,
		MS:        a.Elapsed.Milliseconds(),
	}
	if a.Stop != 0 {
		b.Stop = a.Stop.String()
	}
	if a.Class != llm.ErrUnknown || a.Stop == 0 {
		b.Error = a.Class.String()
	}
	return b
}

// recall picks the episodes for tier 2. A failure here degrades the turn to
// "no new recall" rather than failing it: the contact still gets a reply.
func (d *Deps) recall(ctx context.Context, log *slog.Logger, in Input, reader plan.Reader, pr plan.Params, now int64) ([]plan.Recalled, error) {
	if in.Episodes.Len() == 0 {
		return nil, nil
	}
	lay, err := plan.Layout(in.LastPlan, in.Tail, reader, pr)
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
	if tr := in.recallTrace; tr != nil {
		tr.Query = qText
		tr.Status = "recall unavailable"
		if in.LastPlan != nil {
			tr.Warm = slices.Clone(in.LastPlan.Recall)
		}
	}
	tailStart := lay.TailStart
	keepPrev := func() []plan.Recalled {
		// Hold a warm recall set steady through a transient failure; the
		// planner reuses its bytes when the IDs are unchanged.
		if in.LastPlan == nil || now-in.LastPlan.BuiltAt >= pr.CacheTTL {
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
	ids := plan.ChooseRecall(in.LastPlan, fresh, now, pr)
	if tr := in.recallTrace; tr != nil {
		tr.Status = "reranked; scores include recency bonus"
		if degraded {
			tr.Status = "reranker unavailable; stage 1 fallback"
		}
		tr.Chosen = slices.Clone(ids)
		for _, c := range scored {
			reason := "outside top K"
			switch {
			case slices.Contains(ids, c.Ep.ID):
				reason = "selected"
			case c.Score < d.Recall.Theta:
				reason = "below threshold"
			case slices.Equal(ids, tr.Warm) && !pr.NoHysteresis && in.LastPlan != nil && now-in.LastPlan.BuiltAt < pr.CacheTTL:
				reason = "warm set retained by hysteresis"
			}
			tr.Candidates = append(tr.Candidates, diagnostic.Candidate{Episode: c.Ep.ID, Start: c.Ep.Start, End: c.Ep.End, Cosine: c.Stage1, Score: c.Score, Rerank: c.Rerank, Reason: reason, Passage: c.Passage})
		}
	}
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
func (d *Deps) runTool(ctx context.Context, log *slog.Logger, in Input, p *plan.PromptPlan, call core.ToolCall, def *core.ToolDef) core.ToolResult {
	switch {
	case def == nil:
		return core.ToolResult{ID: call.ID, Output: "unknown tool: " + call.Name, IsError: true}
	case def.Name == DelegateTool && len(def.Input) > 0:
		return d.delegate(ctx, in, p, call)
	case len(def.Input) > 0:
		// Only REPL tools, which take code, have somewhere to run.
		return core.ToolResult{ID: call.ID, Output: "this tool has no runtime on this deployment", IsError: true}
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

// findTool is the tool registry: the definition of a named tool, or nil.
func findTool(agent *core.AgentConfig, name string) *core.ToolDef {
	if agent == nil {
		return nil
	}
	for i := range agent.Tools {
		if agent.Tools[i].Name == name {
			return &agent.Tools[i]
		}
	}
	return nil
}
