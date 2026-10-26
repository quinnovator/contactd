package helpers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/core"
)

type delivery struct {
	retry     time.Time
	first     time.Time
	backoff   time.Duration
	seq       uint64
	state     string
	heartbeat int64
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
					continue
				}
				if err := s.deliver(ctx, t.ID, d); err != nil {
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

func (s *Service) deliver(ctx context.Context, id string, d *delivery) error {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
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
	events, err := s.events(ctx, t)
	unlock()
	if err != nil {
		return err
	}
	trigger := "brief"
	check := 1
	for _, ev := range events {
		switch ev.Kind {
		case core.KindUserMsg:
			trigger = "message"
			if ev.Seq == t.FirstSeq {
				trigger = "brief"
			}
		case core.KindNotice:
			var n core.NoticeBody
			if err := json.Unmarshal(ev.Body, &n); err != nil {
				return fmt.Errorf("decode push notice: %w", err)
			}
			if n.Source == "helper-check" {
				trigger = "check"
				check++
			}
		case core.KindAgentMsg:
			if ev.Seq <= t.PushedSeq {
				continue
			}
			var body core.MsgBody
			if err := json.Unmarshal(ev.Body, &body); err != nil {
				return fmt.Errorf("decode push report: %w", err)
			}
			if body.Interrupted {
				continue
			}
			unlock, err := s.lock(ctx, id)
			if err != nil {
				return err
			}
			latest, err := s.deps.Store.HelperTask(ctx, id)
			if err != nil {
				unlock()
				return fmt.Errorf("load push state: %w", err)
			}
			if a2a.TaskState(latest.State).Terminal() && ev.Seq > latest.TerminalSeq {
				unlock()
				continue
			}
			event := &a2a.TaskArtifactUpdateEvent{TaskID: a2a.TaskID(id), ContextID: t.ContextID, Artifact: report(ev, body.Text, trigger, check), LastChunk: true}
			if ev.Seq > d.seq {
				s.broker.publish(event)
				d.seq = ev.Seq
			}
			unlock()
			if err := s.sendAll(ctx, configs, event); err != nil {
				return err
			}
			if err := s.ackPush(ctx, id, ev.Seq, ""); err != nil {
				return err
			}
		case core.KindToolCall, core.KindToolResult, core.KindSummary, core.KindEnvSnapshot, core.KindAttempt, core.KindCheckpoint:
		}
	}
	unlock, err = s.lock(ctx, id)
	if err != nil {
		return err
	}
	t, err = s.deps.Store.HelperTask(ctx, id)
	if err != nil {
		unlock()
		return fmt.Errorf("load push status: %w", err)
	}
	st, err := status(t)
	if err != nil {
		unlock()
		return err
	}
	event := &a2a.TaskStatusUpdateEvent{TaskID: a2a.TaskID(id), ContextID: t.ContextID, Status: st, Metadata: taskMetadata(t)}
	changed := t.State != t.PushedState
	heartbeat := !changed && !st.State.Terminal() && t.LastCheckAt > d.heartbeat
	if changed && d.state != t.State {
		s.broker.publish(event)
		d.state = t.State
	} else if heartbeat {
		s.broker.publish(event)
		d.heartbeat = t.LastCheckAt
	}
	unlock()
	if changed {
		if err := s.sendAll(ctx, configs, event); err != nil {
			return err
		}
		return s.ackPush(ctx, id, 0, t.State)
	}
	if heartbeat {
		if err := s.sendAll(ctx, configs, event); err != nil {
			s.log.DebugContext(ctx, "helper heartbeat not delivered", "task_id", id, "error", err)
		}
	}
	return nil
}

func (s *Service) sendAll(ctx context.Context, configs []core.PushConfig, event a2a.Event) error {
	var first error
	for _, c := range configs {
		if err := s.push.send(ctx, c, event); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *Service) ackPush(ctx context.Context, id string, seq uint64, state string) error {
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
		t.PushedState = state
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
	t.PushedState = t.State
	if err := s.deps.Store.UpdateHelperTask(ctx, t); err != nil {
		return fmt.Errorf("save abandoned push: %w", err)
	}
	return nil
}
