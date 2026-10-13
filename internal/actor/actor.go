package actor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/recall"
	"github.com/quinnovator/contactd/internal/turn"
)

// embedBatch is how many queued messages go to the embedder at once.
const embedBatch = 32

type actor struct {
	id  core.ContactID
	r   *Registry
	log *slog.Logger

	inbox chan Cmd
	dead  chan struct{} // closed when the actor starts retiring
	gone  chan struct{} // closed when it has fully left the registry
	// retiring is guarded by the registry's shard lock; see Registry.deliver.
	retiring bool
	// lastActive and busy are the only fields other goroutines read: the
	// registry samples them to pick hibernation victims.
	lastActive atomic.Int64
	busy       atomic.Bool
	// children counts sub-agents that have not settled. The registry
	// reserves a slot from a turn's goroutine; the actor gives it back.
	children atomic.Int32

	location  *time.Location
	lastSpoke int64
	sessions  map[string]struct{}
	contact   core.Contact
	nextSeq   uint64
	tail      []core.Event     // hot recent window; append-only, shared with turn snapshots
	eps       *recall.Index    // Piece 4
	lastPlan  *plan.PromptPlan // Piece 5
	lastSent  *turn.Sent       // the last request sent, for cache prediction
	affinity  string           // pins this contact, or its parent's family, to one backend
	env       *turn.Env        // Piece 6; holds nothing until a tool needs it
	subs      []*Subscription  // streaming subscribers

	turn    *inflight
	turnSeq uint64
	touched time.Time // last contact activity or turn end
	// pendingBytes is the size of the user messages no turn has answered.
	pendingBytes int

	debounce      *time.Timer // Piece 7
	debounceArmed bool
	firstPending  time.Time // when the oldest unanswered message arrived
	wantTurn      bool      // there are user messages no turn has answered

	embedQ      []embedItem // messages not yet embedded, in seq order
	embedBusy   bool
	summarizing map[uint32]struct{}

	// Checkpoints: at most one fold runs at a time, and ckptSeq/ckptTo
	// describe the newest checkpoint this actor appended.
	folding         bool
	ckptSeq, ckptTo uint64

	// Delegation. notices are settlements waiting for the turn in flight to
	// end, so that each lands in the log after the reply it did not
	// interrupt. forkBase and taskOpen are set on a sub-agent.
	notices  []core.NoticeBody
	forkBase *plan.PromptPlan
	taskOpen bool
	// kids are the sub-agents this contact's log shows as started and not
	// yet settled. children counts the same thing plus any slot reserved
	// for a delegation that is still being set up.
	kids map[core.ContactID]struct{}
}

type inflight struct {
	id      uint64
	cancel  context.CancelFunc
	ctl     *turn.Control
	partial strings.Builder // reply text streamed so far in the current step
}

type embedItem struct {
	ev   core.Event
	text string
}

func (a *actor) run(ctx context.Context) {
	defer a.r.finish(a)
	if err := a.hydrate(ctx); err != nil {
		a.failHydration(ctx, err)
		return
	}
	a.pumpEmbed(ctx)

	cfg := &a.r.cfg
	idle := time.NewTimer(cfg.IdleTTL)
	defer idle.Stop()
	defer a.debounce.Stop()
	for {
		select {
		case c := <-a.inbox:
			if a.handle(ctx, c) {
				return
			}
		case <-a.debounce.C:
			a.debounceArmed = false
			a.startTurn(ctx)
			a.syncBusy()
		case <-idle.C:
			// Idle means no contact activity and no turn. Bookkeeping
			// commands (embeddings, snapshots) do not keep an actor alive.
			if a.turn != nil || a.debounceArmed || a.children.Load() > 0 {
				idle.Reset(cfg.IdleTTL)
				continue
			}
			if rest := cfg.IdleTTL - time.Since(a.touched); rest > 0 {
				idle.Reset(rest)
				continue
			}
			if !a.r.retire(a) {
				idle.Reset(cfg.IdleTTL)
				continue
			}
			a.hibernate(ctx, "idle")
			return
		case <-ctx.Done():
			// The process is going down without a graceful Shutdown.
			a.r.retireNow(a)
			a.abortTurn(ctx, true)
			a.drainInbox(ctx, core.ErrClosed, false)
			return
		}
	}
}

// handle processes one command. It reports whether the actor has retired.
func (a *actor) handle(ctx context.Context, c Cmd) (retired bool) {
	defer a.syncBusy()
	switch c := c.(type) {
	case Inbound:
		a.touch()
		if a.pendingBytes > 0 && a.pendingBytes+len(c.Text) > a.r.cfg.MaxPendingBytes {
			c.Ack <- Ack{Err: core.ErrBacklog}
			// Make sure a turn is on its way for what is already queued, so
			// the backlog clears and the contact is not wedged.
			if a.turn == nil && !a.debounceArmed {
				a.wantTurn = true
				a.armDebounce()
			}
			break
		}
		// Interrupt first: a reply cut off by this message was said before
		// it, and must be logged before it.
		a.interrupt(ctx)
		a.flushNotices(ctx)
		a.beginVisit(ctx, c.Visit, time.Now())
		ev := a.append(ctx, core.KindUserMsg, core.MsgBody{Text: c.Text}, c.Ack)
		// Every stream sees the message, not only the connection that sent
		// it: a contact may be open in more than one place.
		a.broadcast(core.Delta{Type: core.DeltaUser, Text: c.Text, Seq: ev.Seq})
		a.pendingBytes += len(c.Text)
		a.wantTurn = true
		a.armDebounce()
	case Typing:
		a.touch()
		if a.wantTurn {
			a.armDebounce() // respects the hard cap
		}
	case Interrupt:
		// Stopping a reply is something a contact's human does. A sub-agent
		// has a task to settle, and is left to finish it.
		if a.contact.Agent.Parent == 0 {
			a.touch()
			a.interrupt(ctx)
		}
	case subscribe:
		a.attach(c.sub)
	case unsubscribe:
		a.dropSub(c.sub)
	case Hibernate:
		if a.turn == nil && !a.debounceArmed && a.children.Load() == 0 && a.r.retire(a) {
			a.hibernate(ctx, "pressure")
			return true
		}
	case stop:
		a.r.retireNow(a)
		a.abortTurn(ctx, true)
		a.drainInbox(ctx, core.ErrClosed, false)
		a.hibernate(ctx, "shutdown")
		return true
	case flushNow:
		if a.wantTurn && a.turn == nil {
			a.debounce.Stop()
			a.debounceArmed = false
			a.startTurn(ctx)
		}
		c.done <- struct{}{}
	case turnDelta:
		a.onTurnDelta(c)
	case turnToolCall:
		t := a.turn
		if t == nil || t.id != c.id || !c.enter() {
			c.ack <- context.Canceled // the turn was cancelled first
			break
		}
		// From here the turn is in its tool phase: a message handled after
		// this is logged after the call and waits for the tool to finish.
		t.partial.Reset() // the text before the call is part of the call event
		ev := a.append(ctx, core.KindToolCall, c.call, nil)
		a.broadcast(core.Delta{Type: core.DeltaToolCall, Turn: c.id, Tool: c.call.Name, Text: c.call.Code, Seq: ev.Seq})
		c.ack <- nil
	case turnToolResult:
		t := a.turn
		if t == nil || t.id != c.id {
			// Only a forced stop gets here: a turn in its tool phase is
			// never cancelled by a message.
			c.ack <- toolAck{err: context.Canceled}
			break
		}
		ev := a.append(ctx, core.KindToolResult, c.res, nil)
		if c.res.Child != 0 {
			a.kids[c.res.Child] = struct{}{} // now on record as waited for
		}
		a.broadcast(core.Delta{Type: core.DeltaToolResult, Turn: c.id, Tool: c.name, Text: c.res.Output, Seq: ev.Seq})
		// The result is logged; only now does the tool phase end.
		c.ack <- toolAck{replan: c.exit()}
	case turnDone:
		a.onTurnDone(ctx, c)
	case turnAttempt:
		a.append(ctx, core.KindAttempt, c.body, nil)
	case task:
		// A sub-agent's job. Nobody is typing, so there is no burst to wait
		// out: the turn starts now.
		a.touch()
		a.forkBase, a.taskOpen = c.base, true
		ref := c.ref
		ev := a.append(ctx, core.KindUserMsg, core.MsgBody{Text: taskText(c.text), Task: &ref}, c.ack)
		a.broadcast(core.Delta{Type: core.DeltaUser, Text: taskText(c.text), Seq: ev.Seq})
		a.wantTurn = true
		a.startTurn(ctx)
	case settled:
		a.onSettled(ctx, c)
	case resettle:
		a.onResettle(ctx, c)
	case folded:
		a.onFolded(ctx, c)
	case embedded:
		a.onEmbedded(ctx, c)
	case summarized:
		a.onSummarized(ctx, c)
	case envIdle:
		a.recordSnapshots(ctx, a.env.TakeSnapshots())
	case writeFailed:
		// Memory is ahead of disk. Drop it; the next message rehydrates
		// from what actually committed.
		a.log.ErrorContext(ctx, "write failed; dropping actor state", "error", c.err)
		// A sub-agent that dies here would otherwise never be heard from,
		// and its parent would wait for it for good.
		a.settle(ctx, nil, fmt.Errorf("the sub-agent's log could not be written: %w", c.err))
		a.r.retireNow(a)
		a.abortTurn(ctx, true)
		a.drainInbox(ctx, c.err, false)
		// Release the environment; its snapshot cannot be recorded, because
		// this actor's sequence numbers can no longer be trusted.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		a.env.Hibernate(sctx)
		cancel()
		// Wait until everything this actor queued has been resolved before
		// the contact can be hydrated again.
		a.flush(ctx)
		obs.Hibernations.WithLabelValues("write_failed").Inc()
		return true
	}
	return false
}

// touch records contact activity, for the idle timer and for the registry's
// choice of hibernation victims.
func (a *actor) touch() {
	a.touched = time.Now()
	a.lastActive.Store(a.touched.UnixMilli())
}

// syncBusy publishes whether the actor has work pending, for the registry's
// choice of hibernation victims.
func (a *actor) syncBusy() {
	a.busy.Store(a.turn != nil || a.debounceArmed || a.children.Load() > 0)
}

// hydrate loads the tail (the last N events, a single range scan) and the
// episode rows. Message vectors stay on disk.
func (a *actor) hydrate(ctx context.Context) error {
	start := time.Now()
	cfg, deps := &a.r.cfg, &a.r.deps
	// Attempt records sit between the events of a conversation, one per
	// model call, and the planner never reads them. Half as many rows again
	// are loaded so that, with them dropped, the hot window still holds
	// about TailEvents events of the conversation itself.
	h, err := deps.Hydrate.Hydrate(ctx, a.id, cfg.TailEvents+cfg.TailEvents/2)
	if err != nil {
		return err //nolint:wrapcheck // the store's error already names the contact
	}
	a.contact, a.nextSeq = h.Contact, h.NextSeq
	a.location = time.UTC
	if h.TimeZone != "" {
		if loc, err := time.LoadLocation(h.TimeZone); err == nil {
			a.location = loc
		}
	}
	a.lastSpoke = h.LastSpoke
	a.sessions = make(map[string]struct{}, len(h.Sessions))
	for _, id := range h.Sessions {
		a.sessions[id] = struct{}{}
	}
	a.tail = slices.DeleteFunc(h.Tail, func(ev core.Event) bool { return ev.Kind == core.KindAttempt })
	if over := len(a.tail) - cfg.TailEvents; over > 0 {
		a.tail = slices.Clone(a.tail[over:])
	}
	if err := a.restoreCheckpoint(ctx); err != nil {
		return err
	}
	// A sub-agent shares its parent's key, so that the family lands on one
	// backend and the child can read what the parent cached.
	family := a.id
	if p := a.contact.Agent.Parent; p != 0 {
		family = p
	}
	a.affinity = deps.Turn.Models.Affinity(family)

	if a.eps, err = recall.NewIndex(cfg.Recall, h.Episodes); err != nil {
		// The episodes table is derived data: replay events and vectors.
		a.log.WarnContext(ctx, "episode index unreadable; rebuilding", "error", err)
		if a.eps, err = a.rebuildIndex(ctx); err != nil {
			return err
		}
	}

	// Messages appended but never embedded (a crash, or a retirement that
	// outran the embedder) are picked up again here, in order.
	// All of them are queued, however many: stopping early would let a newer
	// message be indexed first and leave the rest behind for good.
	after := a.eps.LastIndexed()
	for {
		msgs, err := deps.Hydrate.MessagesAfter(ctx, a.id, after, 512)
		if err != nil {
			return err //nolint:wrapcheck // the store's error already names the contact
		}
		for _, ev := range msgs {
			a.enqueueEmbed(ev)
		}
		if len(msgs) < 512 {
			break
		}
		after = msgs[len(msgs)-1].Seq
	}

	a.env = turn.NewEnv(turn.EnvConfig{
		Contact: a.id,
		Leasers: deps.Envs,
		Log:     deps.Snapshots,
		IdleTTL: cfg.EnvIdleTTL,
		OnIdle: func() {
			select {
			case a.inbox <- envIdle{}:
			default: // the snapshot is still collected at the next commit or hibernation
			}
		},
	})
	a.debounce = time.NewTimer(time.Hour)
	a.debounce.Stop()
	a.summarizing = make(map[uint32]struct{})
	a.reconcileChildren(ctx)
	for i := len(a.tail) - 1; i >= 0 && a.tail[i].Kind == core.KindUserMsg; i-- {
		a.pendingBytes += len(a.tail[i].Body)
	}
	a.touch()
	obs.HydrateMS.Observe(obs.SinceMS(start))
	return nil
}

func (a *actor) rebuildIndex(ctx context.Context) (*recall.Index, error) {
	deps := &a.r.deps
	var msgs []core.Event
	for after := uint64(0); ; {
		page, err := deps.Hydrate.MessagesAfter(ctx, a.id, after, 1024)
		if err != nil {
			return nil, err //nolint:wrapcheck // the store's error already names the contact
		}
		msgs = append(msgs, page...)
		if len(page) < 1024 {
			break
		}
		after = page[len(page)-1].Seq
	}
	vecs, err := deps.Repair.Vectors(ctx, a.id, 0, a.nextSeq)
	if err != nil {
		return nil, err //nolint:wrapcheck // the store's error already names the contact
	}
	// Summary links are dropped: episode boundaries may have moved. They are
	// regenerated lazily the next time an episode is recalled.
	x := recall.Rebuild(a.r.cfg.Recall, msgs, vecs, nil)
	if err := deps.Repair.ReplaceEpisodes(ctx, a.id, x.Rows()); err != nil {
		return nil, err //nolint:wrapcheck // the store's error already names the contact
	}
	return x, nil
}

// failHydration answers everything queued for a contact that could not be
// loaded, most often because it does not exist.
func (a *actor) failHydration(ctx context.Context, err error) {
	if !errors.Is(err, core.ErrContactNotFound) {
		a.log.ErrorContext(ctx, "hydration failed", "error", err)
	}
	a.r.retireNow(a)
	for _, s := range a.subs {
		close(s.ch)
	}
	a.subs = nil
	a.drainInbox(ctx, err, true)
}

// drainInbox answers what is still in the mailbox of an actor that has been
// taken out of service, so that no caller is left waiting. retireNow has
// already stopped new deliveries. Messages are refused with err; streams are
// closed when closeSubs is set and otherwise kept, to be parked for the
// contact's next actor.
func (a *actor) drainInbox(ctx context.Context, err error, closeSubs bool) {
	for {
		select {
		case c := <-a.inbox:
			switch c := c.(type) {
			case Inbound:
				c.Ack <- Ack{Err: err}
			case subscribe:
				if closeSubs {
					close(c.sub.ch)
				} else {
					a.attach(c.sub)
				}
			case unsubscribe:
				a.dropSub(c.sub)
			case flushNow:
				c.done <- struct{}{}
			case turnToolCall:
				c.ack <- context.Canceled
			case turnToolResult:
				c.ack <- toolAck{err: context.Canceled}
			case task:
				c.ack <- Ack{Err: err}
			case resettle:
				// This contact cannot be loaded, so it cannot report. Its
				// parent is told that much, instead of waiting for good.
				a.r.deliverSettlement(ctx, c.parent, a.id, core.NoticeBody{
					Source: noticeSettled, Child: a.id,
					Text: failedText(a.id, "the sub-agent could not be loaded: "+err.Error()),
				})
			default:
				// Results from goroutines this actor started are safe to
				// lose. So is a sub-agent's settlement, for a different
				// reason: until its notice is in the log the sub-agent is
				// still on record as open, and this contact's next actor
				// asks it to report again.
			}
		default:
			return
		}
	}
}

// append assigns the next sequence number and hands the event to the
// group-commit writer. Because the actor is the only writer for its contact,
// no lock is needed and two turns can never race. It does not wait for the
// commit; ack, when set, is answered once the event is durable.
func (a *actor) append(ctx context.Context, kind core.EventKind, body any, ack chan<- Ack) core.Event {
	raw, err := json.Marshal(body)
	if err != nil {
		// Bodies are our own tagged structs; this cannot happen at run time.
		a.log.ErrorContext(ctx, "encode event body", "kind", kind.String(), "error", err)
		if ack != nil {
			ack <- Ack{Err: err}
		}
		return core.Event{}
	}
	now := time.Now().UnixMilli()
	ev := core.Event{Contact: a.id, Seq: a.nextSeq, TS: now, Kind: kind, Body: raw}
	a.nextSeq++
	// An attempt record is for the log only: the planner never reads it, so
	// it does not take up room in the hot window.
	if kind != core.KindAttempt {
		a.tail = append(a.tail, ev)
	}

	w := core.Write{Contact: a.id, Event: &ev}
	if kind.IsMessage() {
		a.lastSpoke = now
	}
	if kind == core.KindUserMsg {
		w.TimeZone = a.location.String()
	}
	if n, ok := body.(core.NoticeBody); ok && n.Source == "session-start" {
		w.SessionID = n.SessionID
	}
	if kind == core.KindUserMsg {
		w.Touch = now
	}
	seq := ev.Seq
	w.Done = func(err error) {
		if ack != nil {
			ack <- Ack{Seq: seq, Err: err}
		}
		if err != nil {
			a.postAsync(writeFailed{err: err})
		}
	}
	a.r.deps.Log.Submit(w)

	if kind.IsMessage() {
		a.enqueueEmbed(ev)
		a.pumpEmbed(ctx)
	}
	return ev
}

// armDebounce (re)starts the burst timer: 700 ms of quiet, but never more
// than the hard cap after the first pending message, so someone who types
// continuously cannot stall the reply forever.
func (a *actor) armDebounce() {
	cfg := &a.r.cfg
	now := time.Now()
	if a.firstPending.IsZero() {
		a.firstPending = now
	}
	wait := min(cfg.Debounce, a.firstPending.Add(cfg.DebounceCap).Sub(now))
	a.debounce.Reset(max(wait, 0))
	a.debounceArmed = true
}

// startTurn launches a turn for the pending messages. If a turn is still
// running (it is finishing a tool), onTurnDone starts the next one.
func (a *actor) startTurn(ctx context.Context) {
	if a.turn != nil || !a.wantTurn {
		return
	}
	a.wantTurn, a.firstPending = false, time.Time{}
	a.turnSeq++
	tctx, cancel := context.WithCancel(ctx)
	t := &inflight{id: a.turnSeq, cancel: cancel, ctl: new(turn.Control)}
	a.turn = t

	in := turn.Input{
		Contact:  a.id,
		Name:     a.contact.Name,
		TurnID:   t.id,
		Agent:    &a.contact.Agent,
		Profile:  a.contact.Profile,
		Location: a.location,
		Tail:     slices.Clip(a.tail), // the turn must not append into our backing array
		Episodes: a.eps.Snapshot(),
		LastPlan: a.lastPlan,
		Sent:     a.lastSent,
		Affinity: a.affinity,
		Base:     a.forkBase,
		Env:      a.env,
		Ctl:      t.ctl,
		Sink:     sink{a: a, id: t.id},
		Started:  time.Now(),
	}
	a.r.turns.Add(1)
	go func() {
		defer a.r.turns.Add(-1)
		defer cancel()
		res, err := turn.Run(tctx, in, a.r.deps.Turn)
		a.post(turnDone{id: in.TurnID, res: res, err: err})
	}()
	a.broadcast(core.Delta{Type: core.DeltaTyping, Turn: t.id, Typing: true})
}

// interrupt applies the interruption policy when a new message arrives
// mid-turn. A running tool is never killed; anything else is cancelled.
func (a *actor) interrupt(ctx context.Context) {
	t := a.turn
	if t == nil {
		return
	}
	if !t.ctl.Interrupt() {
		return // the turn checks for this after the tool returns and re-plans
	}
	a.abortTurn(ctx, false)
	obs.Turns.WithLabelValues("interrupted").Inc()
}

// abortTurn cancels the in-flight turn. If reply text was already streaming
// it is persisted as an agent message flagged interrupted, so the model knows
// it was cut off; a turn that had not said anything yet is cancelled
// silently. force also stops a running tool, which only shutdown does.
func (a *actor) abortTurn(ctx context.Context, force bool) {
	t := a.turn
	if t == nil {
		return
	}
	if force {
		t.ctl.Cancel()
	}
	t.cancel() // the stream and recall stop at their next select
	a.turn = nil
	if partial := t.partial.String(); partial != "" {
		ev := a.append(ctx, core.KindAgentMsg, core.MsgBody{Text: partial, Interrupted: true}, nil)
		a.broadcast(core.Delta{Type: core.DeltaInterrupted, Turn: t.id, Seq: ev.Seq})
	}
	a.broadcast(core.Delta{Type: core.DeltaTyping, Turn: t.id})
}

func (a *actor) onTurnDelta(c turnDelta) {
	t := a.turn
	if t == nil || t.id != c.id {
		return // stale fragment from a cancelled turn
	}
	if c.d.Type == core.DeltaText {
		t.partial.WriteString(c.d.Text)
	}
	c.d.Turn = c.id
	a.broadcast(c.d)
}

// onTurnDone is commitTurn. Each turn carries a monotonically increasing ID,
// so a result that arrives after its turn was cancelled is ignored. Actor
// state is written here, on the actor goroutine, and nowhere else.
func (a *actor) onTurnDone(ctx context.Context, c turnDone) {
	t := a.turn
	if t == nil || t.id != c.id {
		return
	}
	a.turn = nil
	t.cancel()
	a.touch()
	if c.res.Sent != nil {
		// Whatever became of the turn, this is what the provider last saw.
		a.lastSent = c.res.Sent
	}

	switch {
	case c.err != nil:
		a.log.ErrorContext(ctx, "turn failed", "turn_id", c.id, "error", c.err)
		obs.Turns.WithLabelValues("error").Inc()
		if partial := t.partial.String(); partial != "" {
			ev := a.append(ctx, core.KindAgentMsg, core.MsgBody{Text: partial, Interrupted: true}, nil)
			a.broadcast(core.Delta{Type: core.DeltaInterrupted, Turn: c.id, Seq: ev.Seq})
		}
		a.broadcast(core.Delta{Type: core.DeltaError, Turn: c.id, Text: failureReason(c.err)})
		a.settle(ctx, &c.res, c.err)
	default:
		res := c.res
		switch {
		case res.Replan:
			obs.Turns.WithLabelValues("replanned").Inc()
		case res.StepLimit:
			obs.Turns.WithLabelValues("step_limit").Inc()
			a.broadcast(core.Delta{Type: core.DeltaError, Turn: c.id, Text: "step_limit"})
		default:
			obs.Turns.WithLabelValues("ok").Inc()
		}
		if !res.Replan {
			a.pendingBytes = 0
		}
		if res.Final != "" {
			// The reasoning behind the reply is stored only where the route
			// must be given it back; the turn decided that.
			ev := a.append(ctx, core.KindAgentMsg, core.MsgBody{Text: res.Final, Reasoning: res.Reasoning}, nil)
			a.broadcast(core.Delta{Type: core.DeltaMessage, Turn: c.id, Text: res.Final, Seq: ev.Seq})
		}
		a.lastPlan = res.Plan
		a.trimTail(res.Plan)
		a.summarizeRecalled(ctx, res.Plan.Recall)
		a.recordSnapshots(ctx, a.env.TakeSnapshots())
		a.maybeFold(ctx, c.id, &res)
		if !res.Replan {
			a.settle(ctx, &res, nil)
		}
	}
	a.broadcast(core.Delta{Type: core.DeltaTyping, Turn: c.id})
	// Settlements that arrived mid-turn go into the log now, after the reply
	// they waited for, and are answered by the next turn.
	a.flushNotices(ctx)

	// A message that arrived while a tool was running, or while this turn
	// was failing, still needs its answer.
	if a.wantTurn && !a.debounceArmed {
		a.startTurn(ctx)
	}
}

// trimTail forgets events the planner has compacted or folded out of the
// prompt. They stay recallable through their episodes. The checkpoint event
// the plan opens with is kept wherever it sits: the next plan reads its
// summary from it.
func (a *actor) trimTail(p *plan.PromptPlan) {
	if len(a.tail) == 0 || a.tail[0].Seq >= p.TailFrom {
		return
	}
	// Copy rather than reslice so the old backing array can be collected.
	kept := make([]core.Event, 0, len(a.tail))
	for i := range a.tail {
		if ev := &a.tail[i]; ev.Seq >= p.TailFrom || (ev.Seq == p.Checkpoint && p.Checkpoint != 0) {
			kept = append(kept, *ev)
		}
	}
	a.tail = kept
}

// hibernate snapshots the environment, flushes, and lets go of memory.
func (a *actor) hibernate(ctx context.Context, reason string) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	a.recordSnapshots(ctx, a.env.Hibernate(sctx))
	a.flush(ctx)
	obs.Hibernations.WithLabelValues(reason).Inc()
}

// flush waits until everything this actor submitted has been committed or
// refused, and resets the contact in the writer. The writer is FIFO, so once
// this marker comes back nothing of this actor's is still queued and the
// contact's next actor can hydrate as its only writer.
func (a *actor) flush(ctx context.Context) {
	done := make(chan error, 1)
	a.r.deps.Log.Submit(core.Write{Contact: a.id, Reset: true, Done: func(err error) { done <- err }})
	if err := <-done; err != nil && !errors.Is(err, core.ErrClosed) {
		a.log.ErrorContext(ctx, "flush on retirement failed", "error", err)
	}
}

func (a *actor) recordSnapshots(ctx context.Context, snaps []core.EnvSnapshotBody) {
	for _, s := range snaps {
		a.append(ctx, core.KindEnvSnapshot, s, nil)
	}
}

// post delivers a result from a goroutine the actor started. It gives up if
// the actor has retired; such results are safe to lose.
func (a *actor) post(c Cmd) {
	select {
	case a.inbox <- c:
	case <-a.dead:
	}
}

// tryPost is post that reports whether the command was delivered.
func (a *actor) tryPost(c Cmd) bool {
	select {
	case a.inbox <- c:
		return true
	case <-a.dead:
		return false
	}
}

// postAsync is post for callers that must not block, such as the writer.
func (a *actor) postAsync(c Cmd) {
	select {
	case a.inbox <- c:
	case <-a.dead:
	default:
		go a.post(c)
	}
}

// attach starts a subscriber's stream. The first delta says where the stream
// begins, so that a client can read everything up to there from the log and
// miss nothing in between. A turn in flight is then described as far as it
// has got: that the agent is composing, and what it has said so far.
func (a *actor) attach(s *Subscription) {
	a.subs = append(a.subs, s)
	a.sendTo(s, core.Delta{Type: core.DeltaReady, Seq: a.nextSeq - 1})
	t := a.turn
	if t == nil {
		return
	}
	a.sendTo(s, core.Delta{Type: core.DeltaTyping, Turn: t.id, Typing: true})
	if partial := t.partial.String(); partial != "" {
		a.sendTo(s, core.Delta{Type: core.DeltaText, Turn: t.id, Text: partial})
	}
}

func (a *actor) broadcast(d core.Delta) {
	for i := 0; i < len(a.subs); {
		if a.sendTo(a.subs[i], d) {
			i++
			continue
		}
		// The subscriber fell too far behind. Close its stream rather than
		// stall the actor or silently skip deltas; the client reconnects and
		// catches up from the event log.
		close(a.subs[i].ch)
		a.subs = slices.Delete(a.subs, i, i+1)
	}
}

func (a *actor) sendTo(s *Subscription, d core.Delta) bool {
	select {
	case s.ch <- d:
		return true
	default:
		return false
	}
}

func (a *actor) dropSub(s *Subscription) {
	if i := slices.Index(a.subs, s); i >= 0 {
		close(s.ch)
		a.subs = slices.Delete(a.subs, i, i+1)
	}
}

// sink is how a turn goroutine reaches its actor.
type sink struct {
	a  *actor
	id uint64
}

func (s sink) send(ctx context.Context, c Cmd) error {
	select {
	case s.a.inbox <- c:
		return nil
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // callers wrap with what they were sending
	case <-s.a.dead:
		return core.ErrClosed
	}
}

func (s sink) Delta(ctx context.Context, d core.Delta) error {
	return s.send(ctx, turnDelta{id: s.id, d: d})
}

func (s sink) ToolCall(ctx context.Context, call core.ToolCall, enter func() bool) error {
	ack := make(chan error, 1)
	if err := s.send(ctx, turnToolCall{id: s.id, call: call, enter: enter, ack: ack}); err != nil {
		return err
	}
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // callers wrap with what they were sending
	case <-s.a.dead:
		return core.ErrClosed
	}
}

func (s sink) ToolResult(ctx context.Context, name string, res core.ToolResult, exit func() bool) (bool, error) {
	ack := make(chan toolAck, 1)
	if err := s.send(ctx, turnToolResult{id: s.id, name: name, res: res, exit: exit, ack: ack}); err != nil {
		return false, err
	}
	select {
	case got := <-ack:
		return got.replan, got.err
	case <-ctx.Done():
		return false, ctx.Err() //nolint:wrapcheck // callers wrap with what they were sending
	case <-s.a.dead:
		return false, core.ErrClosed
	}
}

// Attempt is delivered even when the turn's own context is already done: a
// cancelled or failed call is exactly the kind worth having a record of.
func (s sink) Attempt(ctx context.Context, body core.AttemptBody) {
	_ = s.send(context.WithoutCancel(ctx), turnAttempt{body: body}) // the actor is gone; nothing to record into
}
