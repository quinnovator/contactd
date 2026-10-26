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
	s.recoverSubmitted(ctx)
	s.countActive(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { s.schedule(ctx) })
	wg.Go(func() { s.outbox(ctx) })
	wg.Wait()
}

// A SUBMITTED task has no committed acknowledgement that its brief was handed
// to the actor. Never replay it blindly after a crash: that could run it twice.
// Resolve the interrupted creation explicitly so it cannot consume capacity forever.
func (s *Service) recoverSubmitted(ctx context.Context) {
	rows, err := s.deps.Store.PendingHelperTasks(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "load interrupted helper creations", "error", err)
		return
	}
	for _, row := range rows {
		if row.State != string(a2a.TaskStateSubmitted) {
			continue
		}
		if err := s.failSubmitted(ctx, row.ID); err != nil {
			s.log.ErrorContext(ctx, "recover interrupted helper creation", "task_id", row.ID, "error", err)
		}
	}
}

func (s *Service) failSubmitted(ctx context.Context, id string) error {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	row, err := s.deps.Store.HelperTask(ctx, id)
	if err != nil {
		return fmt.Errorf("load interrupted helper creation: %w", err)
	}
	if row.State != string(a2a.TaskStateSubmitted) {
		return nil
	}
	return s.transition(ctx, &row, a2a.TaskStateFailed, "Server restarted before helper creation completed. Start a new helper.")
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
	// A row stays due while its check runs, so it is dispatched once and
	// skipped until that check ends; the end wakes the loop.
	var mu sync.Mutex
	inflight := make(map[string]bool)
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
				mu.Lock()
				busy := inflight[row.ID]
				inflight[row.ID] = true
				mu.Unlock()
				if busy {
					continue
				}
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					return
				}
				wg.Go(func() {
					defer func() {
						<-slots
						mu.Lock()
						delete(inflight, row.ID)
						mu.Unlock()
						select {
						case s.wake <- struct{}{}:
						default:
						}
					}()
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
			// A deadline already past belongs to a check in flight, or to one
			// that failed before it could advance; wait for a wake, or retry
			// in a second, rather than spin.
			wait = min(wait, max(time.Millisecond, time.Until(time.UnixMilli(due))))
			if due <= time.Now().UnixMilli() {
				wait = time.Second
			}
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
