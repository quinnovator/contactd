package actor

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
)

const numShards = 64

type shard struct {
	mu     sync.RWMutex
	actors map[core.ContactID]*actor
	// parked holds subscribers of contacts with no live actor. A stream
	// survives its contact hibernating, and subscribing never hydrates one.
	parked map[core.ContactID][]*Subscription
}

// Registry is the set of live actors: a sharded map, each shard a
// map[ContactID]*actor behind a lock.
type Registry struct {
	deps Deps
	cfg  Config
	log  *slog.Logger

	shards  [numShards]shard
	start   func(*actor) // runs the actor on the registry's lifetime context
	wg      sync.WaitGroup
	count   atomic.Int64  // live actors
	turns   atomic.Int64  // turns in flight
	cursor  atomic.Uint64 // rotates eviction sampling across shards
	closing atomic.Bool
}

// New builds a registry. Actors and their turns live until ctx is cancelled;
// call Shutdown first for a graceful stop.
func New(ctx context.Context, deps Deps, cfg Config, log *slog.Logger) *Registry {
	r := &Registry{deps: deps, cfg: cfg, log: log}
	for i := range r.shards {
		r.shards[i].actors = make(map[core.ContactID]*actor)
		r.shards[i].parked = make(map[core.ContactID][]*Subscription)
	}
	r.start = func(a *actor) { go a.run(ctx) }
	return r
}

// Message sends a user message to a contact and waits until it is durable.
// It returns the message's sequence number.
func (r *Registry) Message(ctx context.Context, id core.ContactID, text string, visits ...core.Visit) (uint64, error) {
	var visit core.Visit
	if len(visits) > 0 {
		visit = visits[0]
	}
	if err := visit.Validate(); err != nil {
		return 0, fmt.Errorf("validate visit: %w", err)
	}
	ack := make(chan Ack, 1)
	if err := r.Send(ctx, id, Inbound{Text: text, Visit: visit, Ack: ack}); err != nil {
		return 0, err
	}
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

// Typing forwards a typing indicator. It never wakes a hibernated contact:
// with no actor there is no pending reply to hold back.
func (r *Registry) Typing(ctx context.Context, id core.ContactID) error {
	if r.closing.Load() {
		return core.ErrClosed
	}
	return r.deliver(ctx, id, Typing{}, func(*shard) {})
}

// Interrupt stops the reply a contact has in flight, if it has one. Like a
// typing indicator it never wakes a hibernated contact.
func (r *Registry) Interrupt(ctx context.Context, id core.ContactID) error {
	if r.closing.Load() {
		return core.ErrClosed
	}
	return r.deliver(ctx, id, Interrupt{}, func(*shard) {})
}

// Send delivers a command to a contact's actor, hydrating the contact if it
// is not in memory.
func (r *Registry) Send(ctx context.Context, id core.ContactID, c Cmd) error {
	if r.closing.Load() {
		return core.ErrClosed
	}
	return r.deliver(ctx, id, c, nil)
}

// Subscribe opens a live stream of a contact's deltas.
func (r *Registry) Subscribe(ctx context.Context, id core.ContactID) (*Subscription, error) {
	if r.closing.Load() {
		return nil, core.ErrClosed
	}
	sub := newSubscription(r.cfg.SubBuffer)
	err := r.deliver(ctx, id, subscribe{sub: sub}, func(sh *shard) {
		// With no actor nothing is in flight: the log is the whole story.
		sub.ch <- core.Delta{Type: core.DeltaReady}
		sh.parked[id] = append(sh.parked[id], sub)
	})
	if err != nil {
		return nil, err
	}
	return sub, nil
}

// Unsubscribe ends a stream. The subscription's channel is closed.
func (r *Registry) Unsubscribe(id core.ContactID, sub *Subscription) {
	// Not cancellable: a subscriber that leaves must always be removed.
	_ = r.deliver(context.Background(), id, unsubscribe{sub: sub}, func(sh *shard) {
		if i := slices.Index(sh.parked[id], sub); i >= 0 {
			close(sub.ch)
			sh.parked[id] = slices.Delete(sh.parked[id], i, i+1)
			if len(sh.parked[id]) == 0 {
				delete(sh.parked, id)
			}
		}
	})
}

// Hydrated returns the number of contacts currently in memory.
func (r *Registry) Hydrated() int { return int(r.count.Load()) }

// Turns returns the number of turns in flight.
func (r *Registry) Turns() int { return int(r.turns.Load()) }

func (r *Registry) shard(id core.ContactID) *shard {
	// Fibonacci hashing spreads sequential IDs across shards.
	return &r.shards[(uint64(id)*0x9e3779b97f4a7c15)>>58]
}

// deliver puts c in the mailbox of id's live actor. With no live actor it
// runs orElse under the shard lock, or, when orElse is nil, spawns one.
//
// Every delivery happens under the shard's read lock with the actor not
// retiring, and retirement flips that flag under the write lock after seeing
// an empty mailbox. So a command is either handled by the actor it was sent
// to or never enters its mailbox: nothing is lost when a send races with a
// retirement.
func (r *Registry) deliver(ctx context.Context, id core.ContactID, c Cmd, orElse func(*shard)) error {
	sh := r.shard(id)
	backoff := 50 * time.Microsecond
	for {
		sh.mu.RLock()
		a := sh.actors[id]
		if a != nil && !a.retiring {
			select {
			case a.inbox <- c:
				sh.mu.RUnlock()
				return nil
			default:
			}
			sh.mu.RUnlock()
			// The mailbox is full. The actor never blocks, so this clears
			// quickly; back off briefly instead of holding the lock.
			select {
			case <-time.After(backoff):
				backoff = min(backoff*2, 5*time.Millisecond)
			case <-ctx.Done():
				return fmt.Errorf("deliver to contact %d: %w", id, ctx.Err())
			}
			continue
		}
		sh.mu.RUnlock()

		if a != nil {
			// Raced with retirement: wait for the old actor to finish
			// flushing so the contact still has exactly one writer.
			select {
			case <-a.gone:
			case <-ctx.Done():
				return fmt.Errorf("deliver to contact %d: %w", id, ctx.Err())
			}
			continue
		}

		sh.mu.Lock()
		if sh.actors[id] != nil {
			sh.mu.Unlock()
			continue
		}
		if orElse != nil {
			orElse(sh)
			sh.mu.Unlock()
			return nil
		}
		// Shutdown sets closing before it takes any shard lock, so checking
		// here means a spawn is either seen by Shutdown or refused: no actor
		// can appear after the stop commands went out.
		if r.closing.Load() {
			sh.mu.Unlock()
			return core.ErrClosed
		}
		r.spawnLocked(sh, id)
		sh.mu.Unlock()
		r.evict()
	}
}

// spawnLocked creates and starts an actor. The actor hydrates itself before
// reading its mailbox, so the shard lock is not held across disk I/O.
func (r *Registry) spawnLocked(sh *shard, id core.ContactID) {
	a := &actor{
		id:    id,
		r:     r,
		log:   r.log.With("contact_id", uint64(id)),
		inbox: make(chan Cmd, r.cfg.InboxSize),
		dead:  make(chan struct{}),
		gone:  make(chan struct{}),
		subs:  sh.parked[id],
	}
	a.lastActive.Store(time.Now().UnixMilli())
	delete(sh.parked, id)
	sh.actors[id] = a
	r.wg.Add(1)
	obs.HydratedActors.Set(float64(r.count.Add(1)))
	r.start(a)
}

// retire takes an idle actor out of service. It refuses if anything is
// waiting in the mailbox.
func (r *Registry) retire(a *actor) bool {
	sh := r.shard(a.id)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if len(a.inbox) > 0 {
		return false
	}
	a.retiring = true
	close(a.dead)
	return true
}

// retireNow takes an actor out of service unconditionally. Commands still in
// its mailbox are dropped, so it is only for shutdown and for actors whose
// state is no longer trustworthy.
func (r *Registry) retireNow(a *actor) {
	sh := r.shard(a.id)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if !a.retiring {
		a.retiring = true
		close(a.dead)
	}
}

// finish removes a retired actor from the map and parks its subscribers.
func (r *Registry) finish(a *actor) {
	sh := r.shard(a.id)
	sh.mu.Lock()
	if !a.retiring {
		a.retiring = true
		close(a.dead)
	}
	delete(sh.actors, a.id)
	switch {
	case len(a.subs) == 0:
	case r.closing.Load():
		for _, s := range a.subs {
			close(s.ch)
		}
	default:
		sh.parked[a.id] = append(sh.parked[a.id], a.subs...)
	}
	sh.mu.Unlock()
	close(a.gone)
	obs.HydratedActors.Set(float64(r.count.Add(-1)))
	r.wg.Done()
}

// evict keeps an approximate LRU. When more than MaxHydrated actors are live
// it asks the least recently active idle ones it samples to hibernate, so a
// traffic spike degrades into rehydration cost instead of an OOM kill.
func (r *Registry) evict() {
	if r.cfg.MaxHydrated <= 0 {
		return
	}
	over := r.count.Load() - int64(r.cfg.MaxHydrated)
	for range min(over, 8) { // bounded work per spawn
		sh := &r.shards[r.cursor.Add(1)%numShards]
		var victim *actor
		oldest := int64(math.MaxInt64)
		sampled := 0
		sh.mu.RLock()
		for _, a := range sh.actors { // map order is random: a fair sample
			if a.retiring || a.busy.Load() {
				continue
			}
			if at := a.lastActive.Load(); at < oldest {
				victim, oldest = a, at
			}
			if sampled++; sampled == 5 {
				break
			}
		}
		if victim != nil {
			select {
			case victim.inbox <- Hibernate{}:
			default:
			}
		}
		sh.mu.RUnlock()
	}
}

// hardStop bounds how long Shutdown waits for actors after the grace period,
// so one environment that will not die cannot hold the process hostage.
const hardStop = 45 * time.Second

// Shutdown stops the registry gracefully. It refuses new messages, starts the
// reply to anything already acknowledged, gives in-flight turns until ctx is
// done to finish, cancels the rest (persisting partial replies as
// interrupted), then hibernates every actor, which snapshots environments and
// releases leases. It returns once every actor has flushed.
func (r *Registry) Shutdown(ctx context.Context) {
	r.closing.Store(true)

	// A message acknowledged a moment ago may still be inside its debounce
	// window. Start its turn now so the grace period can cover the reply.
	// An actor that is already retiring has nothing left to flush, and may
	// never read its mailbox again, so each wait ends when the actor answers
	// or when it is dead, whichever comes first.
	type flushing struct {
		done <-chan struct{}
		dead <-chan struct{}
	}
	var waits []flushing
	for _, a := range r.live() {
		done := make(chan struct{}, 1)
		if a.tryPost(flushNow{done: done}) {
			waits = append(waits, flushing{done: done, dead: a.dead})
		}
	}
flush:
	for _, w := range waits {
		select {
		case <-w.done:
		case <-w.dead:
		case <-ctx.Done():
			break flush // the grace period is over
		}
	}

	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
grace:
	for r.turns.Load() > 0 {
		select {
		case <-tick.C:
		case <-ctx.Done():
			break grace
		}
	}

	for _, a := range r.live() {
		a.post(stop{})
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(hardStop):
		r.log.ErrorContext(ctx, "actors did not stop in time; continuing shutdown", "live", r.count.Load())
	}

	for i := range r.shards {
		sh := &r.shards[i]
		sh.mu.Lock()
		for id, subs := range sh.parked {
			for _, s := range subs {
				close(s.ch)
			}
			delete(sh.parked, id)
		}
		sh.mu.Unlock()
	}
}

// live returns every actor currently in the registry.
func (r *Registry) live() []*actor {
	var out []*actor
	for i := range r.shards {
		sh := &r.shards[i]
		sh.mu.RLock()
		for _, a := range sh.actors {
			out = append(out, a)
		}
		sh.mu.RUnlock()
	}
	return out
}
