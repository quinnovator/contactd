package helpers

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

type (
	subscription struct {
		events chan a2a.Event
		slow   bool
	}
	broker struct {
		mu   sync.Mutex
		subs map[a2a.TaskID]map[*subscription]struct{}
	}
)

func (b *broker) subscribe(id a2a.TaskID) (*subscription, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs == nil {
		b.subs = make(map[a2a.TaskID]map[*subscription]struct{})
	}
	if b.subs[id] == nil {
		b.subs[id] = make(map[*subscription]struct{})
	}
	s := &subscription{events: make(chan a2a.Event, 64)}
	b.subs[id][s] = struct{}{}
	return s, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs[id], s)
		if len(b.subs[id]) == 0 {
			delete(b.subs, id)
		}
	}
}

// watched reports whether any stream is open on the task.
func (b *broker) watched(id a2a.TaskID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs[id]) > 0
}

func interrupted(state a2a.TaskState) bool {
	return state.Terminal() || state == a2a.TaskStateInputRequired || state == a2a.TaskStateAuthRequired
}

func (b *broker) publish(ev a2a.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := ev.TaskInfo().TaskID
	ended := false
	if e, ok := ev.(*a2a.TaskStatusUpdateEvent); ok {
		ended = interrupted(e.Status.State)
	}
	for s := range b.subs[id] {
		select {
		case s.events <- ev:
			if ended {
				close(s.events)
				delete(b.subs[id], s)
			}
		default:
			s.slow = true
			close(s.events)
			delete(b.subs[id], s)
		}
	}
	if len(b.subs[id]) == 0 {
		delete(b.subs, id)
	}
}

func (b *broker) stream(ctx context.Context, s *subscription, cancel func()) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		defer cancel()
		for {
			select {
			case ev, ok := <-s.events:
				if !ok {
					b.mu.Lock()
					slow := s.slow
					b.mu.Unlock()
					if slow {
						yield(nil, errors.New("task subscriber fell behind"))
					}
					return
				}
				if !yield(ev, nil) {
					return
				}
			case <-ctx.Done():
				yield(nil, fmt.Errorf("task stream: %w", ctx.Err()))
				return
			}
		}
	}
}
