// Package diagnostic carries bounded, in-memory observations of model calls.
// Its stream holds metadata; opt-in detail captures also hold prompt contents.
package diagnostic

import (
	"context"
	"slices"

	"github.com/quinnovator/contactd/internal/core"
)

// Tier describes one contiguous part of a prompt, in estimated tokens.
type Tier struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	Tokens   int    `json:"tokens"`
	Messages int    `json:"messages"`
	Hash     string `json:"hash"`
	Chain    string `json:"chain"`
}

// Snapshot is published before a model call and replaced when it ends.
// Run + Call identifies a call across concurrent contacts and server restarts.
// PredictedCached and Tiers use estimates; ReportedInput and ReportedCached
// use the provider's tokenizer. Compare their ratios, not their raw counts.
type Snapshot struct {
	Parent          core.ContactID `json:"parent,omitempty"`
	InheritedTokens int            `json:"inherited_tokens,omitempty"`
	Blocks          []Block        `json:"blocks"`
	LogEnd          uint64         `json:"log_end"`
	TTFT            int64          `json:"ttft_ms"`
	OutputBytes     int            `json:"output_bytes"`
	OutputTokens    int            `json:"output_tokens"`
	Run             string         `json:"run"`
	Call            uint64         `json:"call"`
	Contact         core.ContactID `json:"contact"`
	Name            string         `json:"name"`
	Route           string         `json:"route"`
	Turn            uint64         `json:"turn"`
	Step            int            `json:"step"`
	Purpose         string         `json:"purpose"`
	Started         int64          `json:"started"`
	Window          int            `json:"window"`
	Tiers           []Tier         `json:"tiers"`
	PredictedCached int            `json:"predicted_cached"`
	ReportedInput   int            `json:"reported_input"`
	ReportedCached  int            `json:"reported_cached"`
	Reported        bool           `json:"reported"`
	Done            bool           `json:"done"`
	Error           string         `json:"error,omitempty"`
	MS              int64          `json:"ms"`
	TailBudget      int            `json:"tail_budget"`
	TailGuard       int            `json:"tail_guard"`
	Recall          []uint32       `json:"recall"`
	Events          []string       `json:"events"`
}

// Total returns the prompt size in estimated tokens.
func (s Snapshot) Total() (n int) {
	for _, t := range s.Tiers {
		n += t.Tokens
	}
	return n
}

func (s Snapshot) clone() Snapshot {
	s.Blocks = slices.Clone(s.Blocks)
	s.Tiers = slices.Clone(s.Tiers)
	s.Recall = slices.Clone(s.Recall)
	s.Events = slices.Clone(s.Events)
	return s
}

// Scope identifies background calls (such as episode summaries) without
// making the summarizer's port depend on diagnostic metadata.
type Scope struct {
	Contact core.ContactID
	Name    string
	Turn    uint64
}

type scopeKey struct{}

// WithScope attaches an immutable logging scope to a background operation.
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// FromContext reads a background operation's logging scope.
func FromContext(ctx context.Context) Scope {
	s, _ := ctx.Value(scopeKey{}).(Scope)
	return s
}
