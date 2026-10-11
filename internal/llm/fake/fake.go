// Package fake is a model provider that costs nothing and behaves plausibly:
// it streams scripted replies and tool calls with realistic pacing, and it
// keeps a real prefix cache so the usage it reports (input, cached, output
// tokens) responds to prompt layout the way a provider's would. That makes
// Pieces 4 and 5 tunable without spending provider tokens.
//
// It is also the profile the core's capability-driven behaviour is tested
// against: its declared capabilities are whatever a test sets, and it
// enforces them the way a real upstream would.
package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/plan"
)

// CacheMode says how the simulated provider cache decides what to keep.
type CacheMode uint8

const (
	// CacheBreakpoints models explicit breakpoints (Anthropic): only
	// prefixes ending at a breakpoint are written, and a lookup walks back a
	// bounded number of blocks from each of the request's breakpoints.
	CacheBreakpoints CacheMode = iota
	// CachePrefix models automatic prefix caching (DeepSeek, OpenAI, vLLM,
	// SGLang): every message boundary is a reusable prefix.
	CachePrefix
	// CacheOff models an endpoint that caches nothing.
	CacheOff
)

// lookback is how many blocks before a breakpoint a breakpoint-mode lookup
// checks for an earlier cache entry.
const lookback = 20

// Conventions the scripted model recognises in the latest user message.
const (
	// TranscriptTag opens a transcript to be summarized.
	TranscriptTag = "<excerpt>"
	// InstructionTag opens a request to summarize the conversation so far.
	InstructionTag = "<instruction>"
)

// Rule scripts a response. The first rule whose Match accepts the latest user
// message wins. A rule with Tool set makes the model call that tool with
// Code; both may reference capture groups as $1, $2.
type Rule struct {
	Match *regexp.Regexp
	Reply string
	Tool  string
	Code  string
	// Calls is how many times the tool is called in the one step; 0 means
	// once.
	Calls int
}

// Options tunes the fake model. The zero value is a fast, quiet model.
type Options struct {
	// ID is the provider's name; default "fake".
	ID string
	// Models lists the model names served; default "fake-1". The first is
	// the default model.
	Models []string
	// Caps, when set, replaces the declared capabilities. Left nil they
	// follow Mode: breakpoints or automatic prefix caching, with usage
	// reported, same-turn replay and one tool call per step.
	Caps *llm.Capabilities
	// Fail, when set, is asked before each send, with the number of sends
	// so far and the request body. A non-nil error fails that send before
	// it streams.
	Fail func(n int, body []byte) error

	Mode     CacheMode
	CacheTTL time.Duration // how long a cached prefix lives; default 5 minutes
	// FirstToken is the delay before the first token of a fully cached
	// prompt. PerUncachedToken is added for every input token that missed
	// the cache, which is what makes a cold prefill slow.
	FirstToken       time.Duration
	PerUncachedToken time.Duration
	PerChunk         time.Duration // delay between streamed chunks
	ReplyWords       int           // length of unscripted replies; default 12
	Rules            []Rule
}

// Model is the scripted brain shared by the in-process LLM and the HTTP
// server.
type Model struct {
	opt   Options
	calls atomic.Uint64

	mu    sync.Mutex
	cache map[uint64]time.Time // prefix hash -> expiry
}

// NewModel builds a model.
func NewModel(opt Options) *Model {
	if opt.CacheTTL <= 0 {
		opt.CacheTTL = 5 * time.Minute
	}
	if opt.ReplyWords <= 0 {
		opt.ReplyWords = 12
	}
	return &Model{opt: opt, cache: make(map[uint64]time.Time)}
}

// Response is what the model decided to do for one request.
type Response struct {
	Text string
	// Tools are the calls the model makes, in order.
	Tools []core.ToolCall
	// Thought is the model's reasoning, when it called a tool.
	Thought string
	Usage   llm.Usage
	// TTFT is how long to wait before the first token.
	TTFT time.Duration
}

// Respond decides the reply for a prompt and charges it against the cache.
// marks describe the prompt prefix by prefix, as plan.PrefixMarks does.
func (m *Model) Respond(msgs []core.Message, tools []core.ToolDef, marks []plan.Mark, now time.Time) Response {
	r := m.decide(msgs, tools)
	total, cached := m.charge(marks, now)
	r.Usage = llm.Usage{
		Input:     total,
		Cached:    cached,
		Output:    len(strings.Fields(r.Text)),
		Reasoning: len(strings.Fields(r.Thought)),
	}
	for i := range r.Tools {
		r.Usage.Output += len(strings.Fields(r.Tools[i].Code)) + len(r.Tools[i].Args)/4 + 4
	}
	r.Usage.Output += r.Usage.Reasoning
	r.Usage.Raw = fmt.Appendf(nil, `{"input":%d,"cached":%d,"output":%d}`, r.Usage.Input, r.Usage.Cached, r.Usage.Output)
	r.TTFT = m.opt.FirstToken + time.Duration(total-cached)*m.opt.PerUncachedToken
	return r
}

// charge looks the prompt up in the cache, then writes it, and returns the
// total and cached token counts.
func (m *Model) charge(marks []plan.Mark, now time.Time) (total, cached int) {
	if len(marks) == 0 {
		return 0, 0
	}
	total = marks[len(marks)-1].Tokens
	if m.opt.Mode == CacheOff {
		return total, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	hit := func(i int) bool { return m.cache[marks[i].Hash].After(now) }
	switch m.opt.Mode {
	case CachePrefix:
		for i := range slices.Backward(marks) {
			if hit(i) {
				cached = marks[i].Tokens
				break
			}
		}
	case CacheOff:
	case CacheBreakpoints:
	scan:
		for bp, mark := range slices.Backward(marks) {
			if !mark.Breakpoint {
				continue
			}
			for i := bp; i >= max(bp-lookback, 0); i-- {
				if hit(i) {
					cached = marks[i].Tokens
					break scan
				}
			}
		}
	}

	expiry := now.Add(m.opt.CacheTTL)
	for i := range marks {
		if m.opt.Mode == CachePrefix || marks[i].Breakpoint {
			m.cache[marks[i].Hash] = expiry
		}
	}
	// Reading a prefix also refreshes it, as providers do.
	for i := range marks {
		if marks[i].Tokens > cached {
			break
		}
		if _, ok := m.cache[marks[i].Hash]; ok {
			m.cache[marks[i].Hash] = expiry
		}
	}
	if len(m.cache) > 1<<16 {
		for h, exp := range m.cache {
			if !exp.After(now) {
				delete(m.cache, h)
			}
		}
	}
	return total, cached
}

// decide picks the scripted reaction to the latest thing in the prompt.
func (m *Model) decide(msgs []core.Message, tools []core.ToolDef) Response {
	last, isResult := latest(msgs)
	if isResult {
		line, _, _ := strings.Cut(strings.TrimSpace(last), "\n")
		return Response{Text: "The result is: " + line}
	}
	switch {
	case strings.HasPrefix(last, TranscriptTag):
		return Response{Text: Summary(strings.TrimPrefix(last, TranscriptTag))}
	case strings.HasPrefix(last, InstructionTag):
		return Response{Text: digest(msgs)}
	}
	for _, rule := range m.opt.Rules {
		groups := rule.Match.FindStringSubmatchIndex(last)
		if groups == nil {
			continue
		}
		expand := func(tmpl string) string {
			return string(rule.Match.ExpandString(nil, tmpl, last, groups))
		}
		if rule.Tool != "" {
			calls := make([]core.ToolCall, max(rule.Calls, 1))
			for i := range calls {
				calls[i] = m.call(tools, expand(rule.Tool), expand(rule.Code))
			}
			return m.calling(expand(rule.Reply), last, calls...)
		}
		return Response{Text: expand(rule.Reply)}
	}
	// Built-in convention: "!name code" calls the tool with that name, and
	// " ;; " separates several calls made in one step.
	if strings.HasPrefix(last, "!") {
		var calls []core.ToolCall
		for part := range strings.SplitSeq(last, " ;; ") {
			rest, ok := strings.CutPrefix(part, "!")
			if !ok {
				continue
			}
			name, code, _ := strings.Cut(rest, " ")
			if slices.ContainsFunc(tools, func(t core.ToolDef) bool { return t.Name == name }) {
				calls = append(calls, m.call(tools, name, code))
			}
		}
		if len(calls) > 0 {
			return m.calling("", last, calls...)
		}
	}
	words := strings.Fields(last)
	if len(words) > 6 {
		words = words[:6]
	}
	reply := "Got it: " + strings.Join(words, " ") + "."
	for n := len(strings.Fields(reply)); n < m.opt.ReplyWords; n++ {
		reply += " ok"
	}
	return Response{Text: reply}
}

func (m *Model) calling(text, about string, calls ...core.ToolCall) Response {
	for i := range calls {
		calls[i].Sibling = i
	}
	return Response{Text: text, Tools: calls, Thought: "The contact wants a tool run for: " + about}
}

// call builds a tool call. A tool with its own schema gets its arguments
// raw: the text as given if it is JSON, otherwise wrapped as {"task": ...}.
func (m *Model) call(tools []core.ToolDef, name, code string) core.ToolCall {
	c := core.ToolCall{ID: fmt.Sprintf("call_%d", m.calls.Add(1)), Name: name}
	i := slices.IndexFunc(tools, func(t core.ToolDef) bool { return t.Name == name })
	if i < 0 || len(tools[i].Input) == 0 {
		c.Code = code
		return c
	}
	if json.Valid([]byte(code)) && strings.HasPrefix(strings.TrimSpace(code), "{") {
		c.Args = json.RawMessage(code)
		return c
	}
	// Marshalling a string cannot fail.
	c.Args, _ = json.Marshal(struct {
		Task string `json:"task"`
	}{code})
	return c
}

// digest is the fake checkpoint summary: how much it covers and how the
// conversation began.
func digest(msgs []core.Message) string {
	n, first := 0, ""
	for i := range msgs {
		if msgs[i].Role == core.RoleSystem {
			continue
		}
		n++
		if first == "" && msgs[i].Role == core.RoleUser {
			first = msgs[i].Text
		}
	}
	words := strings.Fields(first)
	if len(words) > 12 {
		words = words[:12]
	}
	return fmt.Sprintf("Checkpoint of %d messages. It began: %s", n-1, strings.Join(words, " "))
}

// latest returns the newest user text or tool output in the prompt, skipping
// the trailing time note.
func latest(msgs []core.Message) (text string, isToolResult bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := &msgs[i]
		switch {
		case m.Role == core.RoleTool && m.ToolResult != nil:
			return m.ToolResult.Output, true
		case m.Role == core.RoleUser && !strings.HasPrefix(m.Text, "<now>"):
			return m.Text, false
		case m.Role == core.RoleAssistant:
			return "", false
		}
	}
	return "", false
}

// Provider is the in-process fake. It implements llm.Provider.
type Provider struct {
	m     *Model
	caps  llm.Capabilities
	sends atomic.Int64
}

// New returns an in-process fake provider.
func New(opt Options) *Provider {
	m := NewModel(opt)
	caps := llm.Capabilities{
		ContextWindow:  200_000,
		MaxOutput:      8192,
		Cache:          llm.CacheBreakpoints,
		CacheTTL:       m.opt.CacheTTL,
		CacheUsage:     true,
		Replay:         llm.ReplaySameTurn,
		ToolChoice:     true,
		SingleToolCall: true,
		ToolErrorFlag:  true,
	}
	if opt.Mode == CachePrefix {
		caps.Cache = llm.CacheAutoPrefix
	}
	if opt.Caps != nil {
		caps = *opt.Caps
		// The simulated cache follows what is declared.
		switch caps.Cache {
		case llm.CacheBreakpoints:
			m.opt.Mode = CacheBreakpoints
		case llm.CacheAutoPrefix:
			m.opt.Mode = CachePrefix
		case llm.CacheNone:
			m.opt.Mode = CacheOff
		}
		if caps.CacheTTL > 0 {
			m.opt.CacheTTL = caps.CacheTTL
		}
	}
	return &Provider{m: m, caps: caps}
}

// ID implements llm.Provider.
func (p *Provider) ID() string {
	if p.m.opt.ID != "" {
		return p.m.opt.ID
	}
	return "fake"
}

// Route implements llm.Provider.
func (p *Provider) Route(_ context.Context, model string) (llm.Route, error) {
	models := p.m.opt.Models
	if len(models) == 0 {
		models = []string{"fake-1"}
	}
	if model == "" {
		model = models[0]
	}
	if !slices.Contains(models, model) {
		return llm.Route{}, fmt.Errorf("model %q: %w", model, llm.ErrUnknownModel)
	}
	return llm.Route{Provider: p.ID(), Model: model, Wire: llm.WireChat, Caps: p.caps}, nil
}

// request is the fake's wire format: the prompt as the core's own messages.
type request struct {
	Model     string         `json:"model"`
	Effort    string         `json:"effort,omitempty"`
	MaxOutput int            `json:"max_output,omitempty"`
	Session   string         `json:"session,omitempty"`
	Tools     []core.ToolDef `json:"tools,omitempty"`
	Messages  []core.Message `json:"messages"`
}

// AffinityHeader is where the fake carries an affinity key when it declares
// llm.AffinityHeader.
const AffinityHeader = "X-Fake-Session"

// Prepare implements llm.Provider.
func (p *Provider) Prepare(_ context.Context, r llm.Route, pl *plan.PromptPlan, o llm.Options) (*llm.Frozen, error) {
	req := request{Model: r.Model, MaxOutput: o.MaxOutput, Tools: pl.Tools}
	effort := r.Caps.Nearest(o.Effort)
	req.Effort = effort.String()
	for i := range pl.Tiers {
		for _, m := range pl.Tiers[i].Msgs {
			// A payload goes back only to the route that produced it.
			if m.Reasoning != nil && m.Reasoning.Route != r.ID() {
				m.Reasoning = nil
			}
			if m.ToolCall != nil {
				call := *m.ToolCall
				call.Reasoning = nil // the message carries it
				m.ToolCall = &call
			}
			req.Messages = append(req.Messages, m)
		}
	}
	header := make(http.Header)
	switch r.Caps.Affinity {
	case llm.AffinityHeader:
		if o.Affinity != "" {
			header.Set(AffinityHeader, o.Affinity)
		}
	case llm.AffinityBody:
		req.Session = o.Affinity
	case llm.AffinityNone:
	}
	// Tags such as <task> stay readable in the body, for tests that look
	// for them.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("encode fake request: %w", err)
	}
	body := bytes.TrimRight(buf.Bytes(), "\n")
	marks := pl.PrefixMarks()
	return &llm.Frozen{
		Route:   r,
		URL:     "fake://" + r.ID(),
		Header:  header,
		Body:    body,
		Effort:  effort,
		Marks:   marks,
		Predict: llm.PredictCached(r.Caps.Cache, o.Prev, marks),
	}, nil
}

// Send implements llm.Provider.
func (p *Provider) Send(ctx context.Context, f *llm.Frozen) (llm.Stream, error) {
	n := int(p.sends.Add(1)) - 1
	if fail := p.m.opt.Fail; fail != nil {
		if err := fail(n, f.Body); err != nil {
			return nil, err
		}
	}
	var req request
	if err := json.Unmarshal(f.Body, &req); err != nil {
		return nil, &llm.Error{Class: llm.ErrBadRequest, Provider: p.ID(), Err: err}
	}
	// An upstream that requires reasoning back rejects a tool-using
	// conversation that leaves any of it out.
	if f.Route.Caps.Replay == llm.ReplayAllTurns && len(req.Tools) > 0 {
		for i := range req.Messages {
			m := &req.Messages[i]
			if m.ToolCall != nil && m.ToolCall.Sibling == 0 && m.Reasoning == nil {
				return nil, &llm.Error{
					Class: llm.ErrBadRequest, Status: http.StatusBadRequest, Provider: p.ID(),
					Err: errors.New("reasoning must be passed back for every earlier tool call"),
				}
			}
		}
	}
	r := p.m.Respond(req.Messages, req.Tools, f.Marks, time.Now())
	if f.Route.Caps.SingleToolCall && len(r.Tools) > 1 {
		// Held to one call per step, a model makes only the first.
		r.Tools = r.Tools[:1]
	}
	return newStream(ctx, r, p.m.opt.PerChunk, f.Route), nil
}

// Sends reports how many requests have been sent.
func (p *Provider) Sends() int { return int(p.sends.Load()) }

// Summary is the fake summarizer: the first words of the transcript.
func Summary(transcript string) string {
	words := strings.Fields(transcript)
	if len(words) > 40 {
		words = words[:40]
	}
	return "Summary: " + strings.Join(words, " ")
}

// Stream plays a Response back as a model stream.
type Stream struct {
	r        Response
	route    llm.Route
	perChunk time.Duration
	// wait sleeps for d and returns the context's error if it was cancelled
	// first. Capturing the context here keeps it out of the struct.
	wait func(d time.Duration) error
	err  error
}

// NewStream returns a stream that plays r, pacing itself until ctx is done.
func NewStream(ctx context.Context, r Response, perChunk time.Duration) *Stream {
	return newStream(ctx, r, perChunk, llm.Route{})
}

func newStream(ctx context.Context, r Response, perChunk time.Duration, route llm.Route) *Stream {
	return &Stream{r: r, route: route, perChunk: perChunk, wait: func(d time.Duration) error {
		if d <= 0 {
			return ctx.Err()
		}
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
}

// Events implements llm.Stream.
func (s *Stream) Events() iter.Seq[llm.Event] {
	return func(yield func(llm.Event) bool) {
		if s.err = s.wait(s.r.TTFT); s.err != nil {
			return
		}
		for _, chunk := range chunks(s.r.Text) {
			if !yield(llm.Event{Kind: llm.TextDelta, Text: chunk}) {
				s.err = context.Canceled
				return
			}
			if s.err = s.wait(s.perChunk); s.err != nil {
				return
			}
		}
		for i := range s.r.Tools {
			if !yield(llm.Event{Kind: llm.ToolStart, Tool: s.r.Tools[i].Name}) {
				s.err = context.Canceled
				return
			}
			// The arguments take a moment to stream after the name.
			if s.err = s.wait(s.perChunk * time.Duration(1+len(strings.Fields(s.r.Tools[i].Code)))); s.err != nil {
				return
			}
		}
	}
}

// Result implements llm.Stream.
func (s *Stream) Result() (llm.Result, error) {
	if s.err != nil {
		return llm.Result{}, fmt.Errorf("fake stream: %w", s.err)
	}
	res := llm.Result{Text: s.r.Text, ToolCalls: s.r.Tools, Stop: llm.StopEnd, Usage: s.r.Usage}
	if len(s.r.Tools) > 0 {
		res.Stop = llm.StopTool
	}
	thought := s.r.Thought
	if thought == "" && s.route.Caps.Replay == llm.ReplayAllTurns {
		thought = "Answering directly." // such a route reasons on every turn
	}
	if thought != "" && s.route.Caps.Replay != llm.ReplayNone {
		res.Reasoning = &core.Reasoning{Route: s.route.ID(), Data: []byte(thought)}
	}
	return res, nil
}

// chunks splits text into word-sized stream fragments that concatenate back
// to the original.
func chunks(text string) []string {
	var out []string
	for text != "" {
		i := strings.IndexByte(text[1:], ' ')
		if i < 0 {
			return append(out, text)
		}
		out = append(out, text[:i+1])
		text = text[i+1:]
	}
	return out
}

// NewClient wraps providers in an llm.Client set up for tests: a fixed
// affinity secret, retries that do not wait, and no logging. The first
// provider is the default.
func NewClient(providers ...llm.Provider) *llm.Client {
	c, err := llm.NewClient(llm.ClientConfig{
		Providers:      providers,
		Retry:          llm.RetryPolicy{Attempts: 3, Base: time.Millisecond, Max: 2 * time.Millisecond, MaxRetryAfter: time.Millisecond},
		AffinitySecret: []byte("fake-affinity-secret"),
		Logger:         slog.New(slog.DiscardHandler),
	})
	if err != nil {
		panic(err) // only a programming error in a test can get here
	}
	return c
}
