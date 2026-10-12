// Package probe checks a provider profile's declared capabilities against
// the live endpoint.
//
// A capability is a claim. The conformance suite tests each claim against
// recorded responses; the probe tests it against the real thing, with a few
// small requests, and reports where the two disagree. It goes through the
// same llm.Provider interface the core uses, so what it measures is what a
// turn would see.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/plan"
)

// Verdict is how one observation compares with the declaration.
type Verdict uint8

const (
	// OK: the endpoint behaved as declared.
	OK Verdict = iota
	// Differs: the endpoint contradicted the declaration. The profile is
	// wrong and should be corrected.
	Differs
	// Inconclusive: the request went through but could not settle the
	// claim, for example because the model chose not to do what was asked.
	Inconclusive
	// Failed: the request itself failed, so nothing was learned.
	Failed
)

func (v Verdict) String() string {
	switch v {
	case OK:
		return "ok"
	case Differs:
		return "DIFFERS"
	case Inconclusive:
		return "inconclusive"
	case Failed:
		return "failed"
	default:
		return "unknown"
	}
}

// Finding is one row of a probe report.
type Finding struct {
	Check    string
	Declared string
	Observed string
	Verdict  Verdict
}

// Options tunes a probe run.
type Options struct {
	// Pause is how long to wait between two requests that should share a
	// cache entry: providers take a moment to make a new prefix readable.
	Pause time.Duration
	// MaxOutput caps each request's output. The default leaves room for a
	// model that reasons before it answers.
	MaxOutput int
	// Now is the time written into the probe's prompts, unix ms.
	Now int64
}

// Report is the outcome of one probe run.
type Report struct {
	Route    llm.Route
	Findings []Finding
	// Requests is how many requests were sent, and Usage what the provider
	// reported for them in total: what the probe cost.
	Requests int
	Usage    llm.Usage
}

// Run probes one route. It returns one finding per check; an error means the
// probe could not even start.
func Run(ctx context.Context, p llm.Provider, model string, o Options) (*Report, error) {
	if o.MaxOutput <= 0 {
		o.MaxOutput = 4096
	}
	route, err := p.Route(ctx, model)
	if err != nil {
		return nil, fmt.Errorf("resolve route: %w", err)
	}
	r := &run{ctx: ctx, p: p, route: route, o: o}
	r.strictNames()
	r.plainReply()
	r.cache()
	r.tools()
	r.efforts()
	return &Report{Route: route, Findings: r.out, Requests: r.requests, Usage: r.spent}, nil
}

// AnyDiffers reports whether any finding contradicts the declaration.
func AnyDiffers(fs []Finding) bool {
	for _, f := range fs {
		if f.Verdict == Differs {
			return true
		}
	}
	return false
}

// Write prints a report as an aligned table.
func Write(w io.Writer, rep *Report) error {
	if _, err := fmt.Fprintf(w, "route %s (%s wire): %d requests, %d input tokens (%d from cache), %d output tokens\n\n",
		rep.Route.ID(), rep.Route.Wire, rep.Requests, rep.Usage.Input, rep.Usage.Cached, rep.Usage.Output); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprint(tw, "CHECK\tDECLARED\tOBSERVED\tVERDICT\n"); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	for _, f := range rep.Findings {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", f.Check, f.Declared, f.Observed, f.Verdict); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

type run struct {
	ctx   context.Context //nolint:containedctx // one probe run is one call; the context does not outlive it
	p     llm.Provider
	route llm.Route
	o     Options
	out   []Finding

	requests int
	spent    llm.Usage
}

func (r *run) add(check, declared, observed string, v Verdict) {
	r.out = append(r.out, Finding{Check: check, Declared: declared, Observed: observed, Verdict: v})
}

func (r *run) failed(check, declared string, err error) {
	r.add(check, declared, fmt.Sprintf("%s: %v", llm.ClassOf(err), clip(err.Error(), 120)), Failed)
}

// send freezes and sends one request and returns how it ended.
func (r *run) send(p *plan.PromptPlan, o llm.Options) (llm.Result, error) {
	if o.MaxOutput == 0 {
		o.MaxOutput = r.o.MaxOutput
	}
	// Every request a turn sends carries its contact's key, and a gateway
	// may refuse one without it. The probe is one conversation throughout.
	if r.route.Caps.Affinity != llm.AffinityNone {
		o.Affinity = "contactd-probe"
	}
	f, err := r.p.Prepare(r.ctx, r.route, p, o)
	if err != nil {
		return llm.Result{}, fmt.Errorf("prepare: %w", err)
	}
	r.requests++
	stream, err := r.p.Send(r.ctx, f)
	if err != nil {
		return llm.Result{}, err //nolint:wrapcheck // the provider's classified error is the finding
	}
	for range stream.Events() { //nolint:revive // only the result matters
	}
	res, err := stream.Result()
	r.spent.Input += res.Usage.Input
	r.spent.Cached += res.Usage.Cached
	r.spent.Output += res.Usage.Output
	return res, err //nolint:wrapcheck // as above
}

// strictNames checks, without a request, that the profile refuses a model
// it does not know. Some upstreams serve an unknown name with a default
// model; the profile is what must stop that.
func (r *run) strictNames() {
	_, err := r.p.Route(r.ctx, "contactd-probe-no-such-model")
	if errors.Is(err, llm.ErrUnknownModel) {
		r.add("unknown model name", "refused by the profile", "refused", OK)
		return
	}
	r.add("unknown model name", "refused by the profile", fmt.Sprintf("accepted (err: %v)", err), Differs)
}

func (r *run) plainReply() {
	res, err := r.send(r.prompt(shortSystem, nil, "Reply with the single word: ready"), llm.Options{})
	if err != nil {
		r.failed("plain reply", "a reply and its usage", err)
		return
	}
	observed := fmt.Sprintf("stop %s, %d input and %d output tokens", res.Stop, res.Usage.Input, res.Usage.Output)
	switch {
	case res.Text == "" || res.Stop != llm.StopEnd:
		r.add("plain reply", "a reply and its usage", observed+", text "+strconv.Quote(clip(res.Text, 40)), Inconclusive)
	case res.Usage.Input == 0 || len(res.Usage.Raw) == 0:
		r.add("plain reply", "a reply and its usage", observed+"; no usage reported", Differs)
	default:
		r.add("plain reply", "a reply and its usage", observed, OK)
	}
}

// cache sends the same long prompt twice and looks at what the second one
// reports as read from cache.
func (r *run) cache() {
	caps := &r.route.Caps
	declared := "cache " + cacheName(caps.Cache) + ", usage " + reported(caps.CacheUsage)
	p := r.prompt(longSystem(), nil, "Reply with the single word: ready")
	first, err := r.send(p, llm.Options{})
	if err != nil {
		r.failed("repeated prefix", declared, err)
		return
	}
	select {
	case <-time.After(r.o.Pause):
	case <-r.ctx.Done():
		r.failed("repeated prefix", declared, r.ctx.Err())
		return
	}
	second, err := r.send(p, llm.Options{Prev: p.PrefixMarks()})
	if err != nil {
		r.failed("repeated prefix", declared, err)
		return
	}
	observed := fmt.Sprintf("second request: %d of %d input tokens cached (first: %d)", second.Usage.Cached, second.Usage.Input, first.Usage.Cached)
	hit := second.Usage.Cached > 0
	switch {
	case hit && caps.Cache != llm.CacheNone && caps.CacheUsage:
		r.add("repeated prefix", declared, observed, OK)
	case hit:
		// Better than declared: the profile is leaving a check switched off.
		r.add("repeated prefix", declared, observed, Differs)
	case caps.Cache == llm.CacheNone || !caps.CacheUsage:
		r.add("repeated prefix", declared, observed, OK)
	default:
		// Declared cached and reported, but nothing came back. One miss can
		// be bad luck with routing, so say what was seen and let it stand
		// as a contradiction to look into.
		r.add("repeated prefix", declared, observed, Differs)
	}
}

// tools asks for tool calls and then exercises the reasoning replay rules
// with the response it gets.
func (r *run) tools() {
	caps := &r.route.Caps
	tools := []core.ToolDef{{Name: "python", Description: "Run Python 3 and return what it prints.", Env: core.EnvSpec{Base: "python-3.12"}}}
	ask := "Use the python tool twice, in one step, as two separate tool calls made together: " +
		"one call with the code print(6*7) and another with the code print(7*8). Do not answer in words first."
	p := r.prompt(shortSystem, tools, ask)
	res, err := r.send(p, llm.Options{})
	if err != nil {
		r.failed("tool call", "tool calls are returned", err)
		return
	}
	if len(res.ToolCalls) == 0 {
		r.add("tool call", "tool calls are returned", "no tool call; stop "+res.Stop.String(), Inconclusive)
		return
	}
	r.add("tool call", "tool calls are returned", fmt.Sprintf("%d call(s), stop %s", len(res.ToolCalls), res.Stop), OK)

	single := "may return several calls per step"
	if caps.SingleToolCall {
		single = "held to one call per step"
	}
	switch n := len(res.ToolCalls); {
	case caps.SingleToolCall && n > 1:
		r.add("calls per step", single, strconv.Itoa(n)+" calls in one step", Differs)
	case caps.SingleToolCall:
		r.add("calls per step", single, "1 call when asked for two", OK)
	case n > 1:
		r.add("calls per step", single, strconv.Itoa(n)+" calls in one step", OK)
	default:
		r.add("calls per step", single, "1 call when asked for two; the model may simply have chosen to", Inconclusive)
	}

	declaredReplay := "replay " + replayName(caps.Replay)
	got := res.Reasoning != nil
	switch {
	case got && caps.Replay == llm.ReplayNone:
		// Cannot happen through a correct codec, which drops the payload.
		r.add("reasoning payload", declaredReplay, "a payload was returned", Differs)
	case got:
		r.add("reasoning payload", declaredReplay, fmt.Sprintf("returned, %d bytes", len(res.Reasoning.Data)), OK)
	case caps.Replay == llm.ReplayNone:
		r.add("reasoning payload", declaredReplay, "none returned", OK)
	default:
		r.add("reasoning payload", declaredReplay, "none returned; the model may not have reasoned", Inconclusive)
	}

	// Continue the tool loop twice: once returning the reasoning as a turn
	// would, once leaving it out. Whether the second is rejected is what
	// decides how strict the replay declaration has to be.
	follow := func(withReasoning bool) (llm.Result, error) {
		q := r.prompt(shortSystem, tools, ask)
		for i := range res.ToolCalls {
			call := res.ToolCalls[i]
			call.Sibling, call.Text, call.Reasoning = i, "", nil
			if i == 0 {
				call.Text = res.Text
				if withReasoning {
					call.Reasoning = res.Reasoning
				}
			}
			q.AppendTail(call, core.ToolResult{ID: call.ID, Output: strconv.Itoa(42 + 14*i)})
		}
		return r.send(q, llm.Options{Prev: p.PrefixMarks()})
	}
	if _, err := follow(true); err != nil {
		r.failed("tool loop continues", "accepted with reasoning returned", err)
		return
	}
	r.add("tool loop continues", "accepted with reasoning returned", "accepted", OK)
	if !got {
		return
	}
	_, err = follow(false)
	switch {
	case err == nil && caps.Replay == llm.ReplayNone:
		r.add("reasoning left out", "not needed", "accepted", OK)
	case err == nil:
		r.add("reasoning left out", "must be returned ("+replayName(caps.Replay)+")",
			"accepted: leaving it out costs quality at most, not the request", Inconclusive)
	case llm.ClassOf(err) == llm.ErrBadRequest && caps.Replay == llm.ReplayNone:
		r.add("reasoning left out", "not needed", "rejected: "+clip(err.Error(), 100), Differs)
	case llm.ClassOf(err) == llm.ErrBadRequest:
		r.add("reasoning left out", "must be returned ("+replayName(caps.Replay)+")", "rejected, as declared", OK)
	default:
		r.failed("reasoning left out", "must be returned", err)
	}
}

// efforts sends one small request per effort the route says it accepts.
func (r *run) efforts() {
	caps := &r.route.Caps
	list := caps.Efforts
	if caps.ReasoningOff {
		list = append([]llm.Effort{llm.EffortNone}, list...)
	}
	if len(list) == 0 {
		r.add("effort", "no effort control", "nothing to send", OK)
		return
	}
	for _, e := range list {
		res, err := r.send(r.prompt(shortSystem, nil, "Reply with the single word: ready"), llm.Options{Effort: e})
		check := "effort " + e.String()
		switch {
		case err == nil:
			r.add(check, "accepted", fmt.Sprintf("accepted, %d reasoning tokens", res.Usage.Reasoning), OK)
		case llm.ClassOf(err) == llm.ErrBadRequest:
			r.add(check, "accepted", "rejected: "+clip(err.Error(), 100), Differs)
		default:
			r.failed(check, "accepted", err)
		}
	}
}

const shortSystem = "You are a test fixture. Follow the instruction in the user message exactly and say nothing else."

// longSystem is a system prompt long enough to clear every provider's
// minimum cacheable prefix (the largest documented is 4,096 tokens), and
// identical on every run so that it can be found in a cache.
func longSystem() string {
	var b strings.Builder
	b.WriteString(shortSystem)
	b.WriteString("\n\nReference table (ignore it; it is here only to make this prompt long):\n")
	for i := range 900 {
		fmt.Fprintf(&b, "row %04d: alpha-%04d bravo-%04d charlie-%04d delta-%04d\n", i, i*7%10000, i*13%10000, i*29%10000, i*31%10000)
	}
	return b.String()
}

// prompt lays out a one-turn conversation the way a contact's turn would be.
func (r *run) prompt(system string, tools []core.ToolDef, user string) *plan.PromptPlan {
	body, err := json.Marshal(core.MsgBody{Text: user})
	if err != nil {
		panic(err) // a struct of one string always encodes
	}
	p, err := plan.Build(nil, plan.Input{
		Agent:  &core.AgentConfig{System: system, Tools: tools},
		Reader: plan.Reader{Route: r.route.ID()},
		Tail:   []core.Event{{Seq: 1, TS: r.o.Now, Kind: core.KindUserMsg, Body: body}},
	}, nil, r.o.Now, plan.DefaultParams())
	if err != nil {
		panic(err) // the event above was built here and decodes
	}
	return p
}

func cacheName(m llm.CacheMode) string {
	switch m {
	case llm.CacheNone:
		return "none"
	case llm.CacheAutoPrefix:
		return "automatic"
	case llm.CacheBreakpoints:
		return "breakpoints"
	default:
		return "unknown"
	}
}

func replayName(m llm.Replay) string {
	switch m {
	case llm.ReplayNone:
		return "none"
	case llm.ReplaySameTurn:
		return "same turn"
	case llm.ReplayAllTurns:
		return "all turns"
	default:
		return "unknown"
	}
}

func reported(b bool) string {
	if b {
		return "reported"
	}
	return "not reported"
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
