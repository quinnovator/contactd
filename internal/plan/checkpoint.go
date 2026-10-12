package plan

import (
	"slices"

	"github.com/quinnovator/contactd/internal/core"
)

// Span is the oldest part of a plan's tail, chosen to be folded into one
// checkpoint summary.
type Span struct {
	From, To uint64 // first and last event seq the checkpoint will replace
	Msgs     int    // how many leading tail messages it covers
	Tokens   int    // their estimated size
}

// FoldSpan picks what a checkpoint should fold: nothing while the tail is
// within its budget, otherwise the oldest FoldFraction of it, ending at a
// turn boundary. It reports false when there is nothing to fold, or no
// boundary to end at.
func (p *PromptPlan) FoldSpan(pr Params) (Span, bool) {
	src := p.tailSrc
	total := 0
	for i := range src {
		total += src[i].tokens
	}
	if total <= pr.TailBudget || len(src) != len(p.Tiers[TierTail].Msgs) {
		return Span{}, false
	}
	target := int(float64(total) * pr.FoldFraction)
	// A span has to hold at least one real turn. Where the tail opens with
	// an earlier checkpoint, that summary alone is not something to fold:
	// the result would cover nothing new and could never be applied.
	first := 1
	if src[0].ckpt != 0 {
		first = 2
	}
	cut, removed := 0, 0
	for i := 1; i < len(src); i++ {
		removed += src[i-1].tokens
		if i >= first && src[i].boundary && removed >= target {
			cut = i
			break
		}
	}
	if cut == 0 {
		return Span{}, false
	}
	s := Span{Msgs: cut, Tokens: removed}
	// The span ends just before the oldest event that stays. A message that
	// arrived mid-tool is rendered after the tool's result but logged before
	// it, so "the last seq folded" is not simply the seq of the last entry.
	for i := cut; i < len(src); i++ {
		if seq := src[i].seq; seq != 0 && (s.To == 0 || seq-1 < s.To) {
			s.To = seq - 1
		}
	}
	// It starts where the tail does, or where the checkpoint it absorbs did.
	s.From = p.TailFrom
	if src[0].ckpt != 0 {
		s.From = src[0].ckptFrom
	}
	// The span must reach into the events the tail still shows verbatim;
	// otherwise the planner would treat the checkpoint as already spent.
	if s.To == 0 || s.To < s.From || s.To < p.TailFrom {
		return Span{}, false
	}
	return s, true
}

// FoldRequest returns the prompt that asks for a checkpoint summary: the
// plan as it was last sent, cut off at the end of the span, with the
// instruction as the only new message. Everything before the instruction is
// byte for byte what the provider has already cached for this contact.
func (p *PromptPlan) FoldRequest(s Span, instruction string) *PromptPlan {
	q := &PromptPlan{
		Recall:     p.Recall,
		TailFrom:   p.TailFrom,
		Checkpoint: p.Checkpoint,
		BuiltAt:    p.BuiltAt,
		Tools:      p.Tools,
	}
	q.Tiers[TierStatic], q.Tiers[TierProfile], q.Tiers[TierRecall] = p.Tiers[TierStatic], p.Tiers[TierProfile], p.Tiers[TierRecall]
	q.Tiers[TierTail], q.tailMarks = newMarkedTier(slices.Clip(p.Tiers[TierTail].Msgs[:s.Msgs]))
	q.tailSrc = slices.Clip(p.tailSrc[:s.Msgs])
	q.Tiers[TierLive] = newTier([]core.Message{{Role: core.RoleUser, Text: instruction}})
	q.rechain(0)
	return q
}

// Head returns the part of a plan that a fork inherits: tiers 0 to 3, which
// is everything up to the owner's last completed turn. The copy shares no
// mutable state with p.
func (p *PromptPlan) Head() *PromptPlan {
	q := &PromptPlan{
		Recall:     slices.Clone(p.Recall),
		TailFrom:   p.TailFrom,
		Checkpoint: p.Checkpoint,
		BuiltAt:    p.BuiltAt,
		Tools:      p.Tools,
	}
	for t := TierStatic; t <= TierTail; t++ {
		q.Tiers[t] = p.Tiers[t]
		q.Tiers[t].Msgs = slices.Clip(p.Tiers[t].Msgs)
	}
	q.rechain(0)
	return q
}

// Prompt lays out a one-off request that is not part of any conversation: a
// system prompt and a single user message.
func Prompt(system, user string) *PromptPlan {
	p := &PromptPlan{}
	p.Tiers[TierStatic] = staticTier(system, nil)
	p.Tiers[TierLive] = newTier([]core.Message{{Role: core.RoleUser, Text: user}})
	p.rechain(0)
	return p
}
