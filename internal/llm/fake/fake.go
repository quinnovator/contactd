// Package fake is a model provider that costs nothing and behaves plausibly:
// it streams scripted replies and tool calls with realistic pacing, and it
// keeps a real prefix cache so the usage it reports (input, cached, output
// tokens) responds to prompt layout the way a provider's would. That makes
// Pieces 4 and 5 tunable without spending provider tokens.
package fake

import (
	"context"
	"fmt"
	"iter"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/turn"
)

// CacheMode says how the simulated provider cache decides what to keep.
type CacheMode uint8

const (
	// CacheBreakpoints models explicit breakpoints (Anthropic): only
	// prefixes ending at a breakpoint are written, and a lookup walks back a
	// bounded number of blocks from each of the request's breakpoints.
	CacheBreakpoints CacheMode = iota
	// CachePrefix models automatic prefix caching (OpenAI, vLLM, SGLang):
	// every message boundary is a reusable prefix.
	CachePrefix
)

// lookback is how many blocks before a breakpoint a breakpoint-mode lookup
// checks for an earlier cache entry.
const lookback = 20

// Rule scripts a response. The first rule whose Match accepts the latest user
// message wins. A rule with Tool set makes the model call that tool with
// Code; both may reference capture groups as $1, $2.
type Rule struct {
	Match *regexp.Regexp
	Reply string
	Tool  string
	Code  string
}

// Options tunes the fake model. The zero value is a fast, quiet model.
type Options struct {
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
	Text  string
	Tool  *core.ToolCall
	Usage turn.Usage
	// TTFT is how long to wait before the first token.
	TTFT time.Duration
}

// Respond decides the reply for a prompt and charges it against the cache.
// marks describe the prompt prefix by prefix, as plan.PrefixMarks does.
func (m *Model) Respond(msgs []core.Message, tools []core.ToolDef, marks []plan.Mark, now time.Time) Response {
	r := m.decide(msgs, tools)
	total, cached := m.charge(marks, now)
	r.Usage = turn.Usage{
		InputTokens:  total,
		CachedTokens: cached,
		OutputTokens: len(strings.Fields(r.Text)),
	}
	if r.Tool != nil {
		r.Usage.OutputTokens += len(strings.Fields(r.Tool.Code)) + 4
	}
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
	for _, rule := range m.opt.Rules {
		groups := rule.Match.FindStringSubmatchIndex(last)
		if groups == nil {
			continue
		}
		expand := func(tmpl string) string {
			return string(rule.Match.ExpandString(nil, tmpl, last, groups))
		}
		if rule.Tool != "" {
			return Response{Text: expand(rule.Reply), Tool: m.call(expand(rule.Tool), expand(rule.Code))}
		}
		return Response{Text: expand(rule.Reply)}
	}
	// Built-in convention: "!name code" calls the tool with that name.
	if rest, ok := strings.CutPrefix(last, "!"); ok {
		name, code, _ := strings.Cut(rest, " ")
		for i := range tools {
			if tools[i].Name == name {
				return Response{Tool: m.call(name, code)}
			}
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

func (m *Model) call(name, code string) *core.ToolCall {
	return &core.ToolCall{ID: fmt.Sprintf("call_%d", m.calls.Add(1)), Name: name, Code: code}
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

// LLM is the in-process fake. It implements turn.LLM and turn.Summarizer.
type LLM struct {
	m *Model
}

// New returns an in-process fake LLM.
func New(opt Options) *LLM { return &LLM{m: NewModel(opt)} }

// Stream implements turn.LLM.
func (l *LLM) Stream(ctx context.Context, p *plan.PromptPlan) (turn.Stream, error) {
	var msgs []core.Message
	for i := range p.Tiers {
		msgs = append(msgs, p.Tiers[i].Msgs...)
	}
	r := l.m.Respond(msgs, p.Tools, p.PrefixMarks(), time.Now())
	return NewStream(ctx, r, l.m.opt.PerChunk), nil
}

// Summarize implements turn.Summarizer with a deterministic digest.
func (l *LLM) Summarize(_ context.Context, transcript string) (string, error) {
	return Summary(transcript), nil
}

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
	perChunk time.Duration
	// wait sleeps for d and returns the context's error if it was cancelled
	// first. Capturing the context here keeps it out of the struct.
	wait func(d time.Duration) error
	err  error
}

// NewStream returns a stream that plays r, pacing itself until ctx is done.
func NewStream(ctx context.Context, r Response, perChunk time.Duration) *Stream {
	return &Stream{r: r, perChunk: perChunk, wait: func(d time.Duration) error {
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

// Events implements turn.Stream.
func (s *Stream) Events() iter.Seq[turn.StreamEvent] {
	return func(yield func(turn.StreamEvent) bool) {
		if s.err = s.wait(s.r.TTFT); s.err != nil {
			return
		}
		for _, chunk := range chunks(s.r.Text) {
			if !yield(turn.StreamEvent{Type: turn.TextDelta, Text: chunk}) {
				s.err = context.Canceled
				return
			}
			if s.err = s.wait(s.perChunk); s.err != nil {
				return
			}
		}
		if s.r.Tool != nil {
			if !yield(turn.StreamEvent{Type: turn.ToolStart, Name: s.r.Tool.Name}) {
				s.err = context.Canceled
				return
			}
			// The arguments take a moment to stream after the name.
			s.err = s.wait(s.perChunk * time.Duration(1+len(strings.Fields(s.r.Tool.Code))))
		}
	}
}

// Stop implements turn.Stream.
func (s *Stream) Stop() (turn.Stop, error) {
	if s.err != nil {
		return turn.Stop{}, fmt.Errorf("fake stream: %w", s.err)
	}
	return turn.Stop{Text: s.r.Text, ToolCall: s.r.Tool, Usage: s.r.Usage}, nil
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
