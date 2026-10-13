package actor

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/recall"
)

// enqueueEmbed queues a message for background embedding. Order matters:
// the episode index must see messages in seq order.
func (a *actor) enqueueEmbed(ev core.Event) {
	var body core.MsgBody
	if json.Unmarshal(ev.Body, &body) != nil {
		return
	}
	a.embedQ = append(a.embedQ, embedItem{ev: ev, text: body.Text})
}

// pumpEmbed sends the head of the queue to the embedder, one batch at a time
// per contact. Batching across contacts happens in the micro-batcher.
func (a *actor) pumpEmbed(ctx context.Context) {
	if a.embedBusy || len(a.embedQ) == 0 {
		return
	}
	n := min(len(a.embedQ), embedBatch)
	texts := make([]string, n)
	for i := range n {
		texts[i] = a.embedQ[i].text
	}
	a.embedBusy = true
	embed := a.r.deps.Embed
	go func() {
		vecs, err := embed.Embed(ctx, texts, recall.Background)
		a.post(embedded{n: n, vecs: vecs, err: err})
	}()
}

// onEmbedded folds fresh vectors into the episode index. Each embedded
// message either extends the current episode or opens a new one; the vector
// and the changed episode row go through the group-commit writer together.
func (a *actor) onEmbedded(ctx context.Context, c embedded) {
	a.embedBusy = false
	if c.err != nil || len(c.vecs) != c.n {
		// Leave the batch queued. The next message or commit retries it,
		// which is all the backoff this needs.
		a.log.WarnContext(ctx, "background embedding failed", "messages", c.n, "error", c.err)
		return
	}
	for i := range c.n {
		ev := a.embedQ[i].ev
		if ev.Seq <= a.eps.LastIndexed() {
			continue
		}
		row := a.eps.OnEmbedded(ev, c.vecs[i]).Row()
		a.r.deps.Log.Submit(core.Write{
			Contact: a.id,
			Vector:  &core.Vector{Seq: ev.Seq, Vec: c.vecs[i]},
			Episode: &row,
			Done:    a.onDerivedWrite,
		})
	}
	a.embedQ = slices.Delete(a.embedQ, 0, c.n)
	a.pumpEmbed(ctx)
}

// onDerivedWrite handles the commit of derived data (vectors, episodes). A
// failure is treated like any other: memory is ahead of disk, so the actor
// drops its state and the next hydration re-embeds what is missing.
func (a *actor) onDerivedWrite(err error) {
	if err != nil {
		a.postAsync(writeFailed{err: err})
	}
}

// summarizeRecalled starts a lazy summary for each recalled episode that is
// closed and has none. The excerpt was used this turn; the summary is for
// next time. Episodes that are never revisited never cost a call.
func (a *actor) summarizeRecalled(ctx context.Context, ids []uint32) {
	sum := a.r.deps.Summarize
	if sum == nil {
		return
	}
	snap := a.eps.Snapshot()
	for _, id := range ids {
		ep := snap.Get(id)
		if ep == nil || ep.SummarySeq != 0 || snap.IsOpen(id) {
			continue
		}
		if _, busy := a.summarizing[id]; busy {
			continue
		}
		a.summarizing[id] = struct{}{}
		log, tokens := a.r.deps.Turn.Log, a.r.cfg.SummaryTokens
		scope := diagnostic.Scope{Contact: a.id, Name: a.contact.Name, Turn: a.turnSeq}
		go func() {
			sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			sctx = diagnostic.WithScope(sctx, scope)
			text, err := recall.Transcript(sctx, log, a.id, ep, tokens)
			if err == nil {
				text, err = sum.Summarize(sctx, text)
			}
			a.post(summarized{ep: ep, text: text, err: err})
		}()
	}
}

// onSummarized appends the summary as an event and links it from the episode
// row.
func (a *actor) onSummarized(ctx context.Context, c summarized) {
	delete(a.summarizing, c.ep.ID)
	if c.err != nil || c.text == "" {
		a.log.WarnContext(ctx, "episode summary failed", "episode", c.ep.ID, "error", c.err)
		return
	}
	ev := a.append(ctx, core.KindSummary, core.SummaryBody{
		Episode: c.ep.ID, Start: c.ep.Start, End: c.ep.End, Text: c.text,
	}, nil)
	changed := a.eps.SetSummary(c.ep.ID, ev.Seq)
	if changed == nil {
		return
	}
	row := changed.Row()
	a.r.deps.Log.Submit(core.Write{Contact: a.id, Episode: &row, Done: a.onDerivedWrite})
}
