package helpers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
)

type timerStore struct {
	Store
	mu       sync.Mutex
	rows     map[string]core.HelperTask
	attempts []time.Time
	fail     bool
	dueCalls int
}

func (s *timerStore) HelperTask(_ context.Context, id string) (core.HelperTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		s.attempts = append(s.attempts, time.Now())
		return core.HelperTask{}, errors.New("temporary storage failure")
	}
	return s.rows[id], nil
}

func (s *timerStore) UpdateHelperTask(_ context.Context, row core.HelperTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[row.ID] = row
	return nil
}

func (s *timerStore) PendingHelperTasks(context.Context) ([]core.HelperTask, error) {
	return []core.HelperTask{{ID: "retry"}}, nil
}

func (s *timerStore) DueHelperTasks(_ context.Context, now int64, _ int) ([]core.HelperTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dueCalls++
	var out []core.HelperTask
	for _, r := range s.rows {
		if !a2a.TaskState(r.State).Terminal() && (r.UntilTS <= now || r.NextDue > 0 && r.NextDue <= now) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *timerStore) NextHelperDue(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var next int64
	for _, r := range s.rows {
		if a2a.TaskState(r.State).Terminal() {
			continue
		}
		due := r.UntilTS
		if r.NextDue > 0 {
			due = min(due, r.NextDue)
		}
		if next == 0 || due < next {
			next = due
		}
	}
	return next, nil
}
func (s *timerStore) CountActiveHelperTasks(context.Context) (int, error) { return 0, nil }

type timerContacts struct {
	Contacts
	mu      sync.Mutex
	calls   int
	active  int
	peak    int
	busy    bool
	release chan struct{}
}

func (c *timerContacts) Check(ctx context.Context, _ core.ContactID, _ string) (uint64, error) {
	c.mu.Lock()
	c.calls++
	c.active++
	c.peak = max(c.peak, c.active)
	busy := c.busy
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.active--; c.mu.Unlock() }()
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if busy {
		return 0, core.ErrBusy
	}
	return 1, nil
}

func TestOutboxTimerBackoff(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		store := &timerStore{fail: true}
		s := New(Deps{Store: store}, DefaultConfig(), obs.Discard())
		start := time.Now()
		go s.outbox(ctx)
		synctest.Wait()
		for _, elapsed := range []time.Duration{time.Second, 3 * time.Second, 7 * time.Second, 15 * time.Second} {
			time.Sleep(time.Until(start.Add(elapsed)))
			synctest.Wait()
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		want := []time.Duration{0, time.Second, 3 * time.Second, 7 * time.Second, 15 * time.Second}
		if len(store.attempts) != len(want) {
			t.Fatalf("attempts=%v", store.attempts)
		}
		for i, at := range store.attempts {
			if at.Sub(start) != want[i] {
				t.Errorf("attempt %d at %s, want %s", i, at.Sub(start), want[i])
			}
		}
		cancel()
	})
}

func TestSchedulerTimers(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		now := time.Now().UnixMilli()
		store := &timerStore{rows: map[string]core.HelperTask{"watch": {ID: "watch", State: string(a2a.TaskStateWorking), EveryMS: 1000, NextDue: now - 10000, UntilTS: now + 3500, MaxChecks: 20}, "waiting": {ID: "waiting", State: string(a2a.TaskStateInputRequired), UntilTS: now + 2000}}}
		contacts := &timerContacts{busy: true}
		s := New(Deps{Store: store, Contacts: contacts}, DefaultConfig(), obs.Discard())
		go s.schedule(ctx)
		synctest.Wait()
		if contacts.calls != 1 {
			t.Fatalf("missed periods dispatched %d checks", contacts.calls)
		}
		row, _ := store.HelperTask(ctx, "watch")
		if row.NextDue != now+1000 || row.Checks != 0 {
			t.Fatalf("busy schedule: %+v", row)
		}
		contacts.mu.Lock()
		contacts.busy = false
		contacts.mu.Unlock()
		time.Sleep(time.Second)
		synctest.Wait()
		row, _ = store.HelperTask(ctx, "watch")
		if row.Checks != 1 {
			t.Fatalf("due check not run: %+v", row)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		waiting, _ := store.HelperTask(ctx, "waiting")
		if waiting.State != string(a2a.TaskStateCompleted) {
			t.Fatalf("waiting task did not expire: %+v", waiting)
		}
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		row, _ = store.HelperTask(ctx, "watch")
		if row.State != string(a2a.TaskStateCompleted) {
			t.Fatalf("working task did not expire: %+v", row)
		}
		cancel()
		synctest.Wait()
	})
}

func TestSchedulerConcurrencyCap(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		now := time.Now().UnixMilli()
		store := &timerStore{rows: make(map[string]core.HelperTask)}
		for _, id := range []string{"a", "b", "c", "d"} {
			store.rows[id] = core.HelperTask{ID: id, State: string(a2a.TaskStateWorking), NextDue: now, EveryMS: 1000, UntilTS: now + 10000, MaxChecks: 20}
		}
		contacts := &timerContacts{release: make(chan struct{})}
		cfg := DefaultConfig()
		cfg.Concurrency = 2
		s := New(Deps{Store: store, Contacts: contacts}, cfg, obs.Discard())
		go s.schedule(ctx)
		synctest.Wait()
		if contacts.active != 2 || contacts.calls != 2 {
			t.Fatalf("dispatch did not respect cap: %+v", contacts)
		}
		close(contacts.release)
		synctest.Wait()
		if contacts.calls != 4 || contacts.peak != 2 {
			t.Fatalf("dispatch did not finish bounded batch: %+v", contacts)
		}
		cancel()
		synctest.Wait()
	})
}

// A check in flight keeps its row due. The scheduler must neither dispatch it
// again nor poll the store in a tight loop while it runs.
func TestSchedulerWaitsForCheckInFlight(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		now := time.Now().UnixMilli()
		store := &timerStore{rows: map[string]core.HelperTask{"slow": {ID: "slow", State: string(a2a.TaskStateWorking), NextDue: now, EveryMS: 60000, UntilTS: now + 3600000, MaxChecks: 20}}}
		contacts := &timerContacts{release: make(chan struct{})}
		s := New(Deps{Store: store, Contacts: contacts}, DefaultConfig(), obs.Discard())
		go s.schedule(ctx)
		time.Sleep(5 * time.Second)
		synctest.Wait()
		store.mu.Lock()
		polls := store.dueCalls
		store.mu.Unlock()
		contacts.mu.Lock()
		calls := contacts.calls
		contacts.mu.Unlock()
		if calls != 1 || polls > 10 {
			t.Fatalf("in-flight check: %d dispatches, %d polls in 5s", calls, polls)
		}
		close(contacts.release)
		synctest.Wait()
		row, _ := store.HelperTask(ctx, "slow")
		if row.Checks != 1 || row.NextDue <= now {
			t.Fatalf("check did not advance: %+v", row)
		}
		cancel()
		synctest.Wait()
	})
}
