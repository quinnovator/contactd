package actor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/core"
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
	ack := make(chan Ack, 1)
	if err := r.Send(ctx, id, task{text: d.Task, base: d.Base, ack: ack}); err != nil {
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
// failed settles too, as a notice: it never fails the parent's turn.
func (a *actor) settle(ctx context.Context, res *turn.Result, turnErr error) {
	parent := a.contact.Agent.Parent
	if parent == 0 || !a.taskOpen {
		return
	}
	a.taskOpen = false
	id := strconv.FormatUint(uint64(a.id), 10)
	var n core.NoticeBody
	switch {
	case turnErr != nil:
		n.Text = "Sub-agent " + id + " failed (stop: error).\n<diagnostic>\n" +
			clipBytes(turnErr.Error(), maxDiagnostic) + "\n</diagnostic>"
	case res.StepLimit:
		n.Text = "Sub-agent " + id + " stopped (stop: step_limit).\n<diagnostic>\n" +
			"It used every model call it is allowed without writing a final message.\n</diagnostic>"
	case res.Final == "":
		n.Text = "Sub-agent " + id + " stopped (stop: " + res.Stop.String() + ").\n<diagnostic>\n" +
			"It ended without writing a final message.\n</diagnostic>"
	default:
		n.Text = "Sub-agent " + id + " finished (stop: " + res.Stop.String() + ").\n<result>\n" +
			clipBytes(res.Final, maxResult) + "\n</result>"
	}
	n.Source = noticeSettled
	child, r := a.id, a.r
	// Delivery may have to wake the parent, so it does not run on this
	// actor's goroutine. It outlives this turn's context on purpose.
	go func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := r.Send(sctx, parent, settled{child: child, notice: n}); err != nil && !errors.Is(err, core.ErrClosed) {
			r.log.ErrorContext(sctx, "sub-agent settlement not delivered",
				"contact_id", uint64(parent), "child_id", uint64(child), "error", err)
		}
	}()
}

// onSettled takes a sub-agent's settlement. It never interrupts: if a turn
// is in flight the notice waits for it to end, and otherwise it starts one.
func (a *actor) onSettled(ctx context.Context, c settled) {
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
func (a *actor) maybeFold(ctx context.Context, res *turn.Result) {
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
	f := turn.Fold{Route: res.Route, Plan: res.Plan, Span: span, Affinity: a.affinity, Effort: effort}
	models := a.r.deps.Turn.Models
	go func() {
		fctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		text, att, err := turn.Checkpoint(fctx, models, f)
		a.post(folded{span: span, text: text, attempt: att, err: err})
	}()
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

	// Where the span was one conversation, its summary is that episode's
	// summary too, and the lazy summarizer need never be called for it.
	eps := a.eps.Snapshot().Within(c.span.From, c.span.To)
	if len(eps) != 1 || eps[0].SummarySeq != 0 {
		return
	}
	if _, busy := a.summarizing[eps[0].ID]; busy {
		return
	}
	a.onSummarized(ctx, summarized{ep: eps[0], text: c.text})
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
