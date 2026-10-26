package helpers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
)

// Run resumes pending deliveries and overdue checks, then runs until cancellation.
func (s *Service) Run(ctx context.Context) {
	s.countActive(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { s.schedule(ctx) })
	wg.Go(func() { s.outbox(ctx) })
	wg.Wait()
}

func nextDue(due, every, now int64) int64 {
	next := due + every
	if next <= now {
		return now + every
	}
	return next
}

func (s *Service) schedule(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	slots := make(chan struct{}, s.cfg.Concurrency)
	for {
		if ctx.Err() != nil {
			return
		}
		now := time.Now().UnixMilli()
		rows, err := s.deps.Store.DueHelperTasks(ctx, now, 32)
		if err != nil {
			if ctx.Err() == nil {
				s.log.ErrorContext(ctx, "load due helper tasks", "error", err)
			}
		} else {
			for _, row := range rows {
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					return
				}
				wg.Go(func() {
					defer func() { <-slots }()
					if err := s.check(ctx, row.ID); err != nil && ctx.Err() == nil {
						s.log.WarnContext(ctx, "schedule helper check", "task_id", row.ID, "error", err)
					}
				})
			}
		}
		due, err := s.deps.Store.NextHelperDue(ctx)
		wait := time.Minute
		if err != nil {
			if ctx.Err() == nil {
				s.log.ErrorContext(ctx, "read helper deadline", "error", err)
			}
		} else if due > 0 {
			wait = min(wait, max(time.Millisecond, time.Until(time.UnixMilli(due))))
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (s *Service) check(ctx context.Context, id string) error {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	t, err := s.deps.Store.HelperTask(ctx, id)
	if err != nil {
		return fmt.Errorf("load due task: %w", err)
	}
	state := a2a.TaskState(t.State)
	now := time.Now().UnixMilli()
	if state.Terminal() {
		return nil
	}
	if now >= t.UntilTS || t.Checks >= t.MaxChecks {
		text := fmt.Sprintf("Finished: ran %d checks between %s and %s.", t.Checks, time.UnixMilli(t.CreatedAt).UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
		if state == a2a.TaskStateInputRequired {
			text = "Ended while waiting for an answer."
		}
		return s.transition(ctx, &t, a2a.TaskStateCompleted, text)
	}
	if state != a2a.TaskStateWorking || t.NextDue == 0 || t.NextDue > now {
		return nil
	}
	_, checkErr := s.deps.Contacts.Check(ctx, t.Contact, checkText(t.Checks+1, t.MaxChecks, time.Now()))
	switch {
	case checkErr == nil:
		obs.HelperChecks.WithLabelValues("started").Inc()
		t.Checks++
	case errors.Is(checkErr, core.ErrBusy):
		obs.HelperChecks.WithLabelValues("busy").Inc()
		s.log.DebugContext(ctx, "helper check skipped while busy", "task_id", id)
	default:
		obs.HelperChecks.WithLabelValues("error").Inc()
		t.Failures++
		if t.Failures >= 3 {
			return s.transition(ctx, &t, a2a.TaskStateFailed, "Stopped after 3 failed checks: could not wake helper")
		}
	}
	t.NextDue = nextDue(t.NextDue, t.EveryMS, now)
	if err := s.deps.Store.UpdateHelperTask(ctx, t); err != nil {
		return fmt.Errorf("advance helper schedule: %w", err)
	}
	return nil
}
