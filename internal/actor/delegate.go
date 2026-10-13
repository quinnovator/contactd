package actor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/turn"
)

const (
	// noticeSettled is the source of the notice a parent gets when a
	// sub-agent finishes.
	noticeSettled = "subagent-settled"
	// maxDiagnostic bounds, in bytes, what a failed sub-agent reports.
	maxDiagnostic = 4096
	// maxResult bounds, in bytes, how much of a sub-agent's last message
	// its parent is shown. A sub-agent is meant to report, not to transfer.
	maxResult = 32 << 10
)

// taskText wraps a parent's task for its sub-agent. The wrapper tells the
// sub-agent what it is and who reads its answer, which the shared system
// prompt cannot.
func taskText(task string) string {
	return "<task>\n" + task + "\n</task>\n" +
		"You are running as a sub-agent. This task was written by the agent that started you, not by the contact. " +
		"Work on it until it is done. Your final message is the only thing the agent that started you will see, " +
		"so put the whole result in it."
}

// Delegate implements turn.Delegator. It is called from the parent's turn
// goroutine: it reserves one of the parent's sub-agent slots, creates the
// child contact, and hands it the task. It returns once the task is durable
// in the child's log, without waiting for the child to do anything.
func (r *Registry) Delegate(ctx context.Context, parent core.ContactID, d turn.Delegation) (core.ContactID, error) {
	if r.deps.Create == nil {
		return 0, errors.New("delegation is not configured")
	}
	sh := r.shard(parent)
	sh.mu.RLock()
	a := sh.actors[parent]
	sh.mu.RUnlock()
	if a == nil {
		return 0, fmt.Errorf("contact %d: %w", parent, core.ErrClosed)
	}
	limit := int32(min(r.cfg.MaxChildren, 1<<20)) //nolint:gosec // bounded just here
	for {
		n := a.children.Load()
		if n >= limit {
			return 0, core.ErrTooManyChildren
		}
		if a.children.CompareAndSwap(n, n+1) {
			break
		}
	}
	release := func() { a.children.Add(-1) }

	// The child is its parent's agent over again: same instructions, tools
	// and route, so the static tier of its prompt is one the parent has
	// already cached. Parent marks it a sub-agent.
	agent := *d.Agent
	agent.Parent = parent
	id, err := r.deps.Create.CreateContact(ctx, agent, "", time.Now().UnixMilli())
	if err != nil {
		release()
		return 0, fmt.Errorf("create sub-agent: %w", err)
	}
	ref := core.TaskRef{Parent: parent, Turn: d.Turn}
	if d.Base != nil {
		ref.Fork, ref.TailFrom, ref.Checkpoint, ref.Recall = true, d.Base.TailFrom, d.Base.Checkpoint, d.Base.Recall
	}
	ack := make(chan Ack, 1)
	if err := r.Send(ctx, id, task{text: d.Task, base: d.Base, ref: ref, ack: ack}); err != nil {
		release()
		return 0, fmt.Errorf("start sub-agent %d: %w", id, err)
	}
	select {
	case got := <-ack:
		if got.Err != nil {
			release()
			return 0, fmt.Errorf("start sub-agent %d: %w", id, got.Err)
		}
	case <-ctx.Done():
		// The task was delivered and may yet run. The slot stays taken:
		// the child will settle and give it back.
		return 0, fmt.Errorf("start sub-agent %d: %w", id, ctx.Err())
	}
	obs.Delegations.WithLabelValues(mode(d.Base)).Inc()
	return id, nil
}

func mode(base *plan.PromptPlan) string {
	if base != nil {
		return "fork"
	}
	return "spawn"
}

// settle reports a sub-agent's finished task to its parent. A sub-agent that
// failed settles too, as a notice: it never fails the parent's turn. res may
// be nil when turnErr is set.
func (a *actor) settle(ctx context.Context, res *turn.Result, turnErr error) {
	parent := a.contact.Agent.Parent
	if parent == 0 || !a.taskOpen {
		return
	}
	a.taskOpen = false
	n := core.NoticeBody{Source: noticeSettled, Child: a.id}
	switch {
	case turnErr != nil:
		n.Text = failedText(a.id, turnErr.Error())
	case res.StepLimit:
		n.Text = stoppedText(a.id, "step_limit", "It used every model call it is allowed without writing a final message.")
	case res.Final == "":
		n.Text = stoppedText(a.id, res.Stop.String(), "It ended without writing a final message.")
	default:
		n.Text = finishedText(a.id, res.Stop.String(), res.Final)
	}
	a.r.deliverSettlement(ctx, parent, a.id, n)
}

func idText(id core.ContactID) string { return strconv.FormatUint(uint64(id), 10) }

func finishedText(child core.ContactID, stop, result string) string {
	return "Sub-agent " + idText(child) + " finished (stop: " + stop + ").\n<result>\n" + clipBytes(result, maxResult) + "\n</result>"
}

func stoppedText(child core.ContactID, stop, why string) string {
	return "Sub-agent " + idText(child) + " stopped (stop: " + stop + ").\n<diagnostic>\n" + why + "\n</diagnostic>"
}

func failedText(child core.ContactID, why string) string {
	return "Sub-agent " + idText(child) + " failed (stop: error).\n<diagnostic>\n" + clipBytes(why, maxDiagnostic) + "\n</diagnostic>"
}

// deliverSettlement sends a settlement to a parent, off the caller's
// goroutine: delivery may have to wake the parent, and it deliberately
// outlives the turn that produced it.
//
// Delivery is best effort and that is enough, because the parent's log is
// the record: a sub-agent stays open there until its notice is appended, and
// a parent that hydrates with one still open asks it to report again. So a
// settlement lost here (the server is shutting down, the parent retired with
// it in its mailbox) is redelivered the next time the parent wakes.
func (r *Registry) deliverSettlement(ctx context.Context, parent, child core.ContactID, n core.NoticeBody) {
	go func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := r.Send(sctx, parent, settled{child: child, notice: n}); err != nil && !errors.Is(err, core.ErrClosed) {
			r.log.ErrorContext(sctx, "sub-agent settlement not delivered; it will be asked for again when the parent next wakes",
				"contact_id", uint64(parent), "child_id", uint64(child), "error", err)
		}
	}()
}

// reconcileChildren runs at hydration. It reads from the log which
// sub-agents this contact started and has not heard from, and asks each to
// report again.
func (a *actor) reconcileChildren(ctx context.Context) {
	a.kids = make(map[core.ContactID]struct{})
	for i := range a.tail {
		switch ev := &a.tail[i]; ev.Kind { //nolint:exhaustive // only the two kinds that open and close a sub-agent
		case core.KindToolResult:
			var res core.ToolResult
			if json.Unmarshal(ev.Body, &res) == nil && res.Child != 0 {
				a.kids[res.Child] = struct{}{}
			}
		case core.KindNotice:
			var n core.NoticeBody
			if json.Unmarshal(ev.Body, &n) == nil && n.Child != 0 {
				delete(a.kids, n.Child)
			}
		default:
		}
	}
	a.children.Store(int32(len(a.kids))) //nolint:gosec // bounded by the hot window
	for child := range a.kids {
		r, parent := a.r, a.id
		go func() {
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			if err := r.Send(sctx, child, resettle{parent: parent}); err != nil && !errors.Is(err, core.ErrClosed) {
				r.log.ErrorContext(sctx, "could not ask a sub-agent to report again",
					"contact_id", uint64(parent), "child_id", uint64(child), "error", err)
			}
		}()
	}
}

// onResettle answers a parent that is asking again. A sub-agent that is
// still working says nothing: it will settle when it is done. One that is
// not reports what its log shows.
func (a *actor) onResettle(ctx context.Context, c resettle) {
	if a.contact.Agent.Parent != c.parent || a.taskOpen || a.turn != nil {
		return
	}
	n := core.NoticeBody{Source: noticeSettled, Child: a.id}
	n.Text = failedText(a.id, "It stopped before finishing, most likely because the server restarted while it was working. Its log has whatever it had done by then.")
scan:
	for i := len(a.tail) - 1; i >= 0; i-- {
		switch ev := &a.tail[i]; ev.Kind { //nolint:exhaustive // only an answer, or the task itself, ends the search
		case core.KindAgentMsg:
			var m core.MsgBody
			if json.Unmarshal(ev.Body, &m) == nil && !m.Interrupted && m.Text != "" {
				n.Text = finishedText(a.id, "end", m.Text)
				break scan
			}
		case core.KindUserMsg:
			var m core.MsgBody
			if json.Unmarshal(ev.Body, &m) == nil && m.Task != nil {
				break scan // back at the task with no answer after it
			}
		default:
		}
	}
	a.r.deliverSettlement(ctx, c.parent, a.id, n)
}

// onSettled takes a sub-agent's settlement. It never interrupts: if a turn
// is in flight the notice waits for it to end, and otherwise it starts one.
func (a *actor) onSettled(ctx context.Context, c settled) {
	// A settlement can arrive twice (the sub-agent was asked again while its
	// first report was on the way). It counts if the sub-agent is on record
	// as open, or if a slot is reserved that no record accounts for yet.
	if _, open := a.kids[c.child]; !open && int(a.children.Load()) <= len(a.kids) {
		return
	}
	delete(a.kids, c.child)
	for {
		n := a.children.Load()
		if n <= 0 || a.children.CompareAndSwap(n, n-1) {
			break
		}
	}
	a.touch()
	a.notices = append(a.notices, c.notice)
	if a.turn != nil {
		return
	}
	a.flushNotices(ctx)
	if !a.debounceArmed {
		a.startTurn(ctx)
	}
}

// flushNotices logs the settlements that were held back and marks them as
// something to answer.
func (a *actor) flushNotices(ctx context.Context) {
	if a.turn != nil || len(a.notices) == 0 {
		return
	}
	for _, n := range a.notices {
		ev := a.append(ctx, core.KindNotice, n, nil)
		a.broadcast(core.Delta{Type: core.DeltaNotice, Text: n.Text, Seq: ev.Seq})
	}
	a.notices = nil
	a.wantTurn = true
}

// maybeFold starts a checkpoint when the committed turn's tail has outgrown
// its budget. The summary is written off the turn's path; the turn after it
// lands pays one cache miss for the rewritten tail.
func (a *actor) maybeFold(ctx context.Context, turnID uint64, res *turn.Result) {
	if !a.r.cfg.Checkpoint || a.folding || res.Plan == nil {
		return
	}
	// A checkpoint this plan has not applied yet is still on its way in;
	// folding again now would summarize the same span twice.
	if a.ckptSeq != 0 && res.Plan.Checkpoint != a.ckptSeq && a.ckptTo >= res.Plan.TailFrom {
		return
	}
	span, ok := res.Plan.FoldSpan(res.Params)
	if !ok {
		return
	}
	effort, err := a.r.deps.Turn.Effort(&a.contact.Agent)
	if err != nil {
		return // the turn would have failed on it first
	}
	a.folding = true
	f := turn.Fold{Route: res.Route, Plan: res.Plan, Span: span, Affinity: a.affinity, Effort: effort, Turn: turnID}
	f.Scope = diagnostic.Scope{Contact: a.id, Name: a.contact.Name, Turn: turnID}
	f.Diagnostics, f.Params = a.r.deps.Turn.Diagnostics, res.Params
	models, absorbs := a.r.deps.Turn.Models, res.Plan.Checkpoint != 0
	go func() {
		fctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		text, att, err := turn.Checkpoint(fctx, models, f)
		a.post(folded{turn: turnID, absorbs: absorbs, span: span, text: text, attempt: att, err: err})
	}()
}

// restoreCheckpoint makes sure a hydrated contact still has its newest
// checkpoint. The hot window is the last N events, and a checkpoint is
// written once and then stands for a long time: without this, a contact
// that hibernated a hundred turns after its last fold would come back with
// the summary of everything before that fold simply gone.
func (a *actor) restoreCheckpoint(ctx context.Context) error {
	log := a.r.deps.Snapshots
	if log == nil {
		return nil
	}
	found, err := log.LatestOfKind(ctx, a.id, core.KindCheckpoint, 1)
	if err != nil {
		return err //nolint:wrapcheck // the store's error already names the contact
	}
	if len(found) == 0 {
		return nil
	}
	ck := found[0]
	var body core.CheckpointBody
	if err := json.Unmarshal(ck.Body, &body); err != nil {
		// A summary that cannot be read is skipped, not fatal: the contact
		// still has its recent history.
		a.log.WarnContext(ctx, "newest checkpoint is unreadable; continuing without it", "seq", ck.Seq, "error", err)
		return nil
	}
	a.ckptSeq, a.ckptTo = ck.Seq, body.To
	if len(a.tail) == 0 || ck.Seq < a.tail[0].Seq {
		a.tail = append([]core.Event{ck}, a.tail...)
	}
	return nil
}

// onFolded appends the checkpoint. The events it replaces stay in the log,
// shadowed, so every request ever sent can still be reconstructed.
func (a *actor) onFolded(ctx context.Context, c folded) {
	a.folding = false
	if c.attempt != nil {
		a.append(ctx, core.KindAttempt, *c.attempt, nil)
	}
	if c.err != nil {
		// Nothing is lost: the tail keeps growing and is cut at the hard
		// budget, as it would be without checkpoints.
		a.log.WarnContext(ctx, "checkpoint failed", "from", c.span.From, "to", c.span.To, "error", c.err)
		obs.Checkpoints.WithLabelValues("error").Inc()
		return
	}
	ev := a.append(ctx, core.KindCheckpoint, core.CheckpointBody{From: c.span.From, To: c.span.To, Text: c.text}, nil)
	a.ckptSeq, a.ckptTo = ev.Seq, c.span.To
	obs.Checkpoints.WithLabelValues("ok").Inc()

	// Where the span was exactly one finished conversation, its summary is
	// that episode's summary too, and the lazy summarizer need never be
	// called for it. "Exactly" is strict, because an episode keeps the first
	// summary it is given: the checkpoint must cover nothing but that
	// episode. So not when it absorbed an earlier checkpoint, not when any
	// other episode reaches into the span, and not while a message in the
	// span is still waiting to be assigned to an episode.
	if c.absorbs || (len(a.embedQ) > 0 && a.embedQ[0].ev.Seq <= c.span.To) {
		return
	}
	snap := a.eps.Snapshot()
	eps := snap.Overlapping(c.span.From, c.span.To)
	if len(eps) != 1 {
		return
	}
	ep := eps[0]
	if snap.IsOpen(ep.ID) || ep.SummarySeq != 0 || ep.Start < c.span.From || ep.End > c.span.To {
		return
	}
	if _, busy := a.summarizing[ep.ID]; busy {
		return
	}
	a.onSummarized(ctx, summarized{ep: ep, text: c.text})
}

// failureReason is the short reason a subscriber is given for a failed turn.
func failureReason(err error) string {
	switch {
	case errors.Is(err, llm.ErrUnknownModel), errors.Is(err, llm.ErrUnknownProvider):
		return "unknown_model"
	case llm.ClassOf(err) != llm.ErrUnknown:
		return "provider_" + llm.ClassOf(err).String()
	default:
		return "turn_failed"
	}
}

// clipBytes cuts s to at most limit bytes without splitting a character.
func clipBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n[... cut ...]"
}
