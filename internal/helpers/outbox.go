package helpers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
)

type delivery struct {
	retry     time.Time
	first     time.Time
	backoff   time.Duration
	started   bool
	seq       uint64 // stream cursor: the last report published
	statusTS  int64  // stream cursor: the last status published
	heartbeat int64
	// The log is scanned once and then incrementally: after is the last seq
	// read, trigger and check describe the position there, and pending holds
	// the reports read but not yet both published and pushed.
	scanned bool
	after   uint64
	trigger string
	check   int
	pending []pending
}

func (s *Service) outbox(ctx context.Context) {
	deliveries := make(map[string]*delivery)
	for {
		rows, err := s.deps.Store.PendingHelperTasks(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.log.ErrorContext(ctx, "load pending helper pushes", "error", err)
			}
		} else {
			live := make(map[string]bool, len(rows))
			for _, t := range rows {
				live[t.ID] = true
			}
			for id := range deliveries {
				if !live[id] {
					delete(deliveries, id) // delivered in full, or gone
				}
			}
			for _, t := range rows {
				if ctx.Err() != nil {
					return
				}
				d := deliveries[t.ID]
				if d == nil {
					d = &delivery{}
					deliveries[t.ID] = d
				}
				if !d.first.IsZero() && time.Since(d.first) >= 24*time.Hour {
					if err := s.abandon(ctx, t.ID); err != nil {
						s.log.ErrorContext(ctx, "expire helper push retries", "task_id", t.ID, "error", err)
					} else {
						s.log.WarnContext(ctx, "helper push delivery expired", "task_id", t.ID)
						delete(deliveries, t.ID)
					}
					continue
				}
				if time.Now().Before(d.retry) {
					if !s.broker.watched(a2a.TaskID(t.ID)) {
						continue
					}
					if err := s.deliver(ctx, t.ID, d, false); err != nil && ctx.Err() == nil {
						s.log.DebugContext(ctx, "helper stream update failed", "task_id", t.ID, "error", err)
					}
					continue
				}
				if err := s.deliver(ctx, t.ID, d, true); err != nil {
					if ctx.Err() != nil {
						return
					}
					if d.first.IsZero() {
						d.first = time.Now()
					}
					d.backoff = min(5*time.Minute, max(time.Second, 2*d.backoff))
					d.retry = time.Now().Add(d.backoff)
					s.log.WarnContext(ctx, "helper push will retry", "task_id", t.ID, "error", err)
				} else {
					d.backoff = 0
					d.retry = time.Time{}
					d.first = time.Time{}
				}
			}
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.kick:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// deliver feeds one task's live streams and then its push configs. Streams
// are fed on every pass, even while pushes are backing off: a subscriber
// never waits on a webhook it has nothing to do with. Pushes advance the
// durable cursors; the stream cursor lives in d and starts at the push one.
func (s *Service) deliver(ctx context.Context, id string, d *delivery, push bool) error {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	// The row is read before the events, so every report up to a terminal
	// state it shows is already readable and goes out ahead of that state.
	t, err := s.deps.Store.HelperTask(ctx, id)
	if err != nil {
		unlock()
		return fmt.Errorf("load push task: %w", err)
	}
	configs, err := s.deps.Store.PushConfigs(ctx, id)
	if err != nil {
		unlock()
		return fmt.Errorf("load push configs: %w", err)
	}
	reports, err := s.reports(ctx, t, d)
	if err != nil {
		unlock()
		return err
	}
	st, err := status(t)
	if err != nil {
		unlock()
		return err
	}
	if !d.started {
		d.seq, d.started = t.PushedSeq, true
	}
	for _, r := range reports {
		if r.seq > d.seq {
			s.broker.publish(r.event)
			d.seq = r.seq
		}
	}
	event := &a2a.TaskStatusUpdateEvent{TaskID: a2a.TaskID(id), ContextID: t.ContextID, Status: st, Metadata: taskMetadata(t)}
	// Status changes are told apart by their timestamp, not their state, so a
	// second question after an answer is a change even if the answer's
	// WORKING was never pushed on its own.
	changed := t.State != t.PushedState || t.StatusTS != t.PushedStatusTS
	heartbeat := t.StatusTS == d.statusTS && !st.State.Terminal() && t.LastCheckAt > d.heartbeat
	if t.StatusTS != d.statusTS {
		s.broker.publish(event)
		d.statusTS = t.StatusTS
	} else if heartbeat {
		s.broker.publish(event)
	}
	if heartbeat {
		d.heartbeat = t.LastCheckAt
	}
	unlock()
	if !push {
		return nil
	}
	for _, r := range reports {
		if r.seq <= t.PushedSeq {
			continue
		}
		if err := s.sendAll(ctx, configs, r.event, "artifact"); err != nil {
			return err
		}
		if err := s.ackPush(ctx, id, r.seq, "", 0); err != nil {
			return err
		}
		d.trim(min(d.seq, r.seq))
	}
	if changed {
		if err := s.sendAll(ctx, configs, event, "status"); err != nil {
			return err
		}
		return s.ackPush(ctx, id, 0, t.State, t.StatusTS)
	}
	if heartbeat {
		if err := s.sendAll(ctx, configs, event, "heartbeat"); err != nil {
			s.log.DebugContext(ctx, "helper heartbeat not delivered", "task_id", id, "error", err)
		}
	}
	return nil
}

type pending struct {
	seq   uint64
	ts    int64
	event *a2a.TaskArtifactUpdateEvent
}

// reports returns the task's deliverable reports not yet both published
// and pushed, in log order: whole, not interrupted, and none written after
// a terminal state. It reads only the events after the last pass.
func (s *Service) reports(ctx context.Context, t core.HelperTask, d *delivery) ([]pending, error) {
	if t.Contact == 0 {
		return nil, nil
	}
	if !d.scanned {
		d.scanned, d.after, d.trigger, d.check, d.pending = true, 0, "brief", 1, nil
	}
	kinds := core.KindSet(0).With(core.KindUserMsg).With(core.KindAgentMsg).With(core.KindNotice)
	for {
		rows, err := s.deps.Events.EventsAfterOfKinds(ctx, t.Contact, d.after, 200, kinds)
		if err != nil {
			return nil, fmt.Errorf("read helper events: %w", err)
		}
		for _, ev := range rows {
			if err := d.read(t, ev); err != nil {
				return nil, err
			}
		}
		if len(rows) < 200 {
			break
		}
	}
	d.trim(min(d.seq, t.PushedSeq))
	var out []pending
	for _, r := range d.pending {
		if !beforeEnd(t, r.seq, r.ts) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (d *delivery) read(t core.HelperTask, ev core.Event) error {
	d.after = ev.Seq
	switch ev.Kind {
	case core.KindUserMsg:
		d.trigger = "message"
		if ev.Seq == t.FirstSeq {
			d.trigger = "brief"
		}
	case core.KindNotice:
		var n core.NoticeBody
		if err := json.Unmarshal(ev.Body, &n); err != nil {
			return fmt.Errorf("decode push notice: %w", err)
		}
		if n.Source == "helper-check" {
			d.trigger = "check"
			d.check++
		}
	case core.KindAgentMsg:
		var body core.MsgBody
		if err := json.Unmarshal(ev.Body, &body); err != nil {
			return fmt.Errorf("decode push report: %w", err)
		}
		if body.Interrupted {
			return nil
		}
		event := &a2a.TaskArtifactUpdateEvent{TaskID: a2a.TaskID(t.ID), ContextID: t.ContextID, Artifact: report(ev, body.Text, d.trigger, d.check), LastChunk: true}
		d.pending = append(d.pending, pending{seq: ev.Seq, ts: ev.TS, event: event})
	case core.KindToolCall, core.KindToolResult, core.KindSummary, core.KindEnvSnapshot, core.KindAttempt, core.KindCheckpoint:
	}
	return nil
}

// beforeEnd reports whether a reply belongs to the task: any reply while it
// runs, and after a terminal state only one written before it. The log head
// read at the transition is the boundary; a reply that was numbered but not
// yet committed then is told by its timestamp, strictly before the
// transition's millisecond.
func beforeEnd(t core.HelperTask, seq uint64, ts int64) bool {
	return !a2a.TaskState(t.State).Terminal() || seq <= t.TerminalSeq || ts < t.StatusTS
}

// trim forgets the reports at or before seq, which are published and pushed.
func (d *delivery) trim(seq uint64) {
	i := 0
	for i < len(d.pending) && d.pending[i].seq <= seq {
		i++
	}
	d.pending = d.pending[i:]
}

func (s *Service) sendAll(ctx context.Context, configs []core.PushConfig, event a2a.Event, kind string) error {
	var first error
	for _, c := range configs {
		err := s.push.send(ctx, c, event)
		result := "ok"
		if err != nil {
			result = "error"
			if first == nil {
				first = err
			}
		}
		obs.A2APush.WithLabelValues(kind, result).Inc()
	}
	return first
}

func (s *Service) ackPush(ctx context.Context, id string, seq uint64, state string, statusTS int64) error {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	t, err := s.deps.Store.HelperTask(ctx, id)
	if err != nil {
		return fmt.Errorf("load push acknowledgement: %w", err)
	}
	if seq > t.PushedSeq {
		t.PushedSeq = seq
	}
	if state != "" {
		t.PushedState, t.PushedStatusTS = state, statusTS
	}
	if err := s.deps.Store.UpdateHelperTask(ctx, t); err != nil {
		return fmt.Errorf("save push acknowledgement: %w", err)
	}
	return nil
}

// abandon advances past this failed delivery batch after its 24 hour retry budget.
// Later reports remain eligible for their own delivery attempts.
func (s *Service) abandon(ctx context.Context, id string) error {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	t, err := s.deps.Store.HelperTask(ctx, id)
	if err != nil {
		return fmt.Errorf("load abandoned push: %w", err)
	}
	events, err := s.events(ctx, t)
	if err != nil {
		return err
	}
	for _, ev := range events {
		if ev.Seq > t.PushedSeq {
			t.PushedSeq = ev.Seq
		}
	}
	t.PushedState, t.PushedStatusTS = t.State, t.StatusTS
	if err := s.deps.Store.UpdateHelperTask(ctx, t); err != nil {
		return fmt.Errorf("save abandoned push: %w", err)
	}
	return nil
}
