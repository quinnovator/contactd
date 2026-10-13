package diagnostic

import (
	"crypto/rand"
	"sync"
)

// Bus retains the latest state of a bounded number of calls. Publishing never
// waits for readers. A reader that falls behind is disconnected so it can
// reconnect and receive a fresh history instead of silently missing completion.
type Bus struct {
	mu          sync.Mutex
	run         string
	next        uint64
	limit       int
	history     []Snapshot
	subs        map[chan Snapshot]struct{}
	closed      bool
	details     map[uint64][]byte
	detailBytes int
}

// New makes a bus retaining at most limit calls; non-positive means 500.
func New(limit int) *Bus {
	if limit <= 0 {
		limit = 500
	}
	return &Bus{run: rand.Text(), limit: limit, subs: make(map[chan Snapshot]struct{})}
}

// Begin assigns a call ID and publishes its planned state.
func (b *Bus) Begin(s Snapshot) Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	s.Run, s.Call = b.run, b.next
	if !b.closed {
		if len(b.history) == b.limit {
			b.detailBytes -= len(b.details[b.history[0].Call])
			delete(b.details, b.history[0].Call)
			copy(b.history, b.history[1:])
			b.history = b.history[:len(b.history)-1]
		}
		b.history = append(b.history, s.clone())
		b.broadcast(s)
	}
	return s
}

// Finish replaces the planned state, even when other contacts started calls
// since it. An already evicted call is not reinserted into the history.
func (b *Bus) Finish(s Snapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || s.Run != b.run {
		return
	}
	s.Done = true
	for i := range b.history {
		if b.history[i].Call == s.Call {
			b.history[i] = s.clone()
			b.broadcast(s)
			return
		}
	}
}

func (b *Bus) broadcast(s Snapshot) {
	for ch := range b.subs {
		select {
		case ch <- s.clone():
		default:
			close(ch)
			delete(b.subs, ch)
		}
	}
}

// Subscribe atomically reads retained history and starts following updates.
// The caller owns every returned snapshot. cancel is safe to call repeatedly.
func (b *Bus) Subscribe() (history []Snapshot, updates <-chan Snapshot, cancel func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	history = make([]Snapshot, len(b.history))
	for i, s := range b.history {
		history[i] = s.clone()
	}
	ch := make(chan Snapshot, 64)
	if b.closed {
		close(ch)
	} else {
		b.subs[ch] = struct{}{}
	}
	return history, ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
	}
}

// Close ends all streams, allowing HTTP shutdown to finish promptly.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for ch := range b.subs {
		close(ch)
		delete(b.subs, ch)
	}
}
