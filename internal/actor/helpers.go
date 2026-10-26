package actor

import (
	"context"
	"fmt"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/turn"
)

// SetHelperHook closes the registry/service wiring cycle. Call before the server takes requests.
func (r *Registry) SetHelperHook(h HelperHook) { r.deps.Helpers = h }

// MessageFrom sends another agent's message and waits for its durable acknowledgement.
func (r *Registry) MessageFrom(ctx context.Context, id core.ContactID, text, raw, messageID string) (uint64, error) {
	return r.message(ctx, id, Inbound{Text: text, Raw: raw, MessageID: messageID})
}

func (r *Registry) message(ctx context.Context, id core.ContactID, in Inbound) (uint64, error) {
	ack := make(chan Ack, 1)
	in.Ack = ack
	if err := r.Send(ctx, id, in); err != nil {
		return 0, err
	}
	return waitAck(ctx, ack)
}

func waitAck(ctx context.Context, ack <-chan Ack) (uint64, error) {
	select {
	case a := <-ack:
		if a.Err != nil {
			return 0, fmt.Errorf("append message: %w", a.Err)
		}
		return a.Seq, nil
	case <-ctx.Done():
		return 0, fmt.Errorf("wait for durable ack: %w", ctx.Err())
	}
}

// Check wakes a helper for one scheduled check, refusing a busy contact.
func (r *Registry) Check(ctx context.Context, id core.ContactID, text string) (uint64, error) {
	ack := make(chan Ack, 1)
	if err := r.Send(ctx, id, Check{Text: text, Ack: ack}); err != nil {
		return 0, err
	}
	return waitAck(ctx, ack)
}

// HelperNotice queues news without interrupting the reply and waits for durability.
func (r *Registry) HelperNotice(ctx context.Context, id core.ContactID, n core.NoticeBody) (uint64, error) {
	ack := make(chan Ack, 1)
	if err := r.Send(ctx, id, HelperNotice{Notice: n, Ack: ack}); err != nil {
		return 0, err
	}
	return waitAck(ctx, ack)
}

// Observe publishes an ephemeral update without hydrating a contact.
func (r *Registry) Observe(ctx context.Context, id core.ContactID, d core.HelperDelta) error {
	if r.closing.Load() {
		return core.ErrClosed
	}
	delta := core.Delta{Type: core.DeltaHelper, Helper: &d}
	return r.deliver(ctx, id, observe{d: delta}, func(sh *shard) {
		for _, sub := range sh.parked[id] {
			select {
			case sub.ch <- delta:
			default:
			}
		}
	})
}

func (a *actor) helperOutcome(ctx context.Context, o HelperOutcome) {
	if a.contact.Agent.Helper && a.r.deps.Helpers != nil {
		a.r.deps.Helpers.HelperTurnEnded(ctx, a.id, o)
	}
}

func (a *actor) appendHelperReply(ctx context.Context, res turn.Result) core.Event {
	body := core.MsgBody{Text: res.Final, Reasoning: res.Reasoning}
	if !a.contact.Agent.Helper || a.r.deps.Helpers == nil {
		return a.append(ctx, core.KindAgentMsg, body, nil)
	}
	ack := make(chan Ack, 1)
	ev := a.append(ctx, core.KindAgentMsg, body, ack)
	hook, id := a.r.deps.Helpers, a.id
	// The hook sees only captured values, and only after the writer commits.
	go func() {
		wait, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		select {
		case got := <-ack:
			if got.Err == nil {
				hook.HelperTurnEnded(wait, id, HelperOutcome{Kind: OutcomeReplied, Seq: ev.Seq})
			}
		case <-wait.Done():
		}
	}()
	return ev
}
