package turn

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/plan"
)

type observation struct {
	bus          *diagnostic.Bus
	snapshot     diagnostic.Snapshot
	cacheUsage   bool
	details      diagnostic.Details
	lastProgress time.Time
}

func observe(bus *diagnostic.Bus, scope diagnostic.Scope, p *plan.PromptPlan, route *llm.Route, opts llm.Options, pr plan.Params, step int, purpose string, events []string) *observation {
	if bus == nil {
		return nil
	}
	s := diagnostic.Snapshot{
		Contact: scope.Contact, Name: scope.Name, Turn: scope.Turn, Step: step,
		Route: route.ID(), Purpose: purpose, Started: time.Now().UnixMilli(),
		Window: route.Caps.ContextWindow, TailBudget: pr.TailBudget,
		TailGuard: max(pr.HardBudget, pr.TailBudget), Recall: slices.Clone(p.Recall), Events: events,
		PredictedCached: llm.PredictCached(route.Caps.Cache, opts.Prev, p.PrefixMarks()),
		Tiers:           make([]diagnostic.Tier, len(p.Tiers)),
	}
	for i, t := range p.Tiers {
		s.Tiers[i] = diagnostic.Tier{
			Index: i, Name: plan.TierKind(i).String(), Tokens: t.Tokens, Messages: len(t.Msgs),
			Hash: strconv.FormatUint(t.Hash, 16), Chain: strconv.FormatUint(p.Chain[i], 16),
		}
	}
	var detail diagnostic.Details
	for i, b := range p.Blocks(pr) {
		id := fmt.Sprintf("%s:%d:%d", b.Hash, b.Seq, i)
		preview := strings.Join(strings.Fields(b.Text), " ")
		if len(preview) > 120 {
			preview = preview[:120]
			for !utf8.ValidString(preview) {
				preview = preview[:len(preview)-1]
			}
		}
		s.Blocks = append(s.Blocks, diagnostic.Block{ID: id, Kind: b.Kind, Tier: b.Tier, Offset: b.Offset, Tokens: b.Tokens, Bytes: len(b.Canonical), Seq: b.Seq, From: b.From, To: b.To, Episode: b.Episode, Hash: b.Hash, Chain: b.Chain, Preview: preview, Fate: b.Fate})
		detail.Blocks = append(detail.Blocks, diagnostic.Content{ID: id, Text: b.Text, Canonical: b.Canonical})
	}
	o := &observation{bus: bus, snapshot: bus.Begin(s), cacheUsage: route.Caps.CacheUsage, details: detail}
	o.save()
	return o
}

func (o *observation) finish(call *llm.Call, err error) {
	if o == nil {
		return
	}
	s := o.snapshot
	s.MS = time.Now().UnixMilli() - s.Started
	if call != nil {
		a := call.Attempt()
		s.PredictedCached = a.Predict
		s.ReportedInput, s.ReportedCached = a.Usage.Input, a.Usage.Cached
		s.Reported = o.cacheUsage && a.Usage.Input > 0
		s.OutputTokens = a.Usage.Output
	}
	if err != nil {
		// Error classes are useful here; provider error text can contain data.
		s.Error = llm.ClassOf(err).String()
	}
	o.bus.Finish(s)
}

func planEvents(p, prev *plan.PromptPlan, step int) []string {
	if step > 0 {
		return []string{"tool"}
	}
	var events []string
	if prev != nil {
		if p.Checkpoint != prev.Checkpoint && p.Checkpoint != 0 {
			events = append(events, "fold")
		} else if p.TailFrom > prev.TailFrom && prev.TailFrom != 0 {
			events = append(events, "cut")
		}
		if !slices.Equal(p.Recall, prev.Recall) {
			events = append(events, "recall")
		}
	} else if len(p.Recall) > 0 {
		events = append(events, "recall")
	}
	return events
}

func (o *observation) save() { o.bus.StoreDetails(o.snapshot.Run, o.snapshot.Call, o.details) }
func (o *observation) prepared(body []byte) {
	o.details.WireBytes = len(body)
	if len(body) <= 2<<20 {
		o.details.Wire = string(body)
	} else {
		o.details.Truncated = true
	}
	o.save()
}

func (o *observation) progress(text string) {
	if o == nil {
		return
	}
	now := time.Now()
	if o.snapshot.TTFT == 0 {
		o.snapshot.TTFT = max(1, now.UnixMilli()-o.snapshot.Started)
	}
	o.snapshot.OutputBytes += len(text)
	if now.Sub(o.lastProgress) >= 250*time.Millisecond {
		o.bus.Update(o.snapshot)
		o.lastProgress = now
	}
}

func (o *observation) enrich(in Input) {
	o.details.Recall = in.recallTrace
	if in.Agent != nil {
		o.snapshot.Parent = in.Agent.Parent
	}
	if in.Base != nil {
		o.snapshot.InheritedTokens = in.Base.InputTokens()
	}
	for _, ep := range in.Episodes.Overlapping(0, ^uint64(0)) {
		o.details.Episodes = append(o.details.Episodes, diagnostic.Episode{ID: ep.ID, Start: ep.Start, End: ep.End, Summary: ep.SummarySeq})
		for i := range o.snapshot.Blocks {
			b := &o.snapshot.Blocks[i]
			if b.Seq >= ep.Start && b.Seq <= ep.End && b.Kind != "checkpoint" {
				b.Episode = ep.ID
			}
		}
	}
	if n := len(in.Tail); n > 0 {
		o.snapshot.LogEnd = in.Tail[n-1].Seq
	}
	o.save()
	o.bus.Update(o.snapshot)
}
