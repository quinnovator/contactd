package requester

import (
	"context"
	"time"

	"github.com/quinnovator/contactd/internal/core"
)

// Run resolves interrupted starts, then delivers accepted helper notices to
// their contacts until ctx ends.
// Each contact drains on its own, in arrival order, waiting as long as its
// reply in flight takes; a notice leaves the inbox only once it is in the
// contact's log, so one accepted before a restart is delivered after it.
func (r *Requester) Run(ctx context.Context) {
	r.recoverStarts(ctx)
	for {
		parents, err := r.store.HelperInboxParents(ctx)
		if err != nil && ctx.Err() == nil {
			r.log.ErrorContext(ctx, "load helper inbox", "error", err)
		}
		for _, p := range parents {
			r.inboxMu.Lock()
			busy := r.draining[p]
			r.draining[p] = true
			r.inboxMu.Unlock()
			if !busy {
				go r.drain(ctx, p)
			}
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-r.kick:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (r *Requester) kickInbox() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

func (r *Requester) drain(ctx context.Context, parent core.ContactID) {
	defer func() {
		r.inboxMu.Lock()
		delete(r.draining, parent)
		r.inboxMu.Unlock()
		// A notice accepted after the last read is picked up by the next scan.
		if ctx.Err() == nil {
			r.kickInbox()
		}
	}()
	for {
		items, err := r.store.HelperInbox(ctx, parent, 16)
		if err != nil {
			if ctx.Err() == nil {
				r.log.ErrorContext(ctx, "read helper inbox", "contact_id", uint64(parent), "error", err)
			}
			return
		}
		if len(items) == 0 {
			return
		}
		for _, it := range items {
			body := core.NoticeBody{Source: "helper-update", Helper: it.Handle, Text: it.Text}
			if _, err := r.registry.HelperNotice(ctx, parent, body); err != nil {
				if ctx.Err() == nil {
					r.log.WarnContext(ctx, "helper notice will retry", "contact_id", uint64(parent), "handle", it.Handle, "error", err)
				}
				return
			}
			// A crash before this delete delivers the notice again: at least once.
			if err := r.store.DeleteHelperInbox(context.WithoutCancel(ctx), it.ID); err != nil {
				r.log.ErrorContext(ctx, "clear helper inbox", "contact_id", uint64(parent), "error", err)
				return
			}
		}
	}
}
