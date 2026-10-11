package turn

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
)

// SnapshotLog finds a contact's past environment snapshots.
type SnapshotLog interface {
	// LatestOfKind returns the newest events of one kind, newest first.
	LatestOfKind(ctx context.Context, contact core.ContactID, kind core.EventKind, limit int) ([]core.Event, error)
}

// EnvConfig wires an Env.
type EnvConfig struct {
	Contact core.ContactID
	Leasers map[uint8]EnvLeaser // by tier
	Log     SnapshotLog
	// IdleTTL is how long an unused lease is kept before it is snapshotted
	// and released, even while the actor stays hydrated.
	IdleTTL time.Duration
	// OnIdle is called, without blocking, after an idle lease was released,
	// so the owner can collect the snapshot with TakeSnapshots.
	OnIdle func()
}

// Env is a contact's REPL environment handle (Piece 6). No environment exists
// until a tool call needs one; between uses the contact owns it only as a
// snapshot ID in the log.
//
// It guards itself with one mutex. That is sufficient because a contact never
// has two turns in flight; the mutex only has to arbitrate between a turn,
// the idle timer and hibernation.
type Env struct {
	cfg EnvConfig

	mu       sync.Mutex
	lease    Lease
	spec     core.EnvSpec
	warm     *warming
	idle     *time.Timer
	lastUsed time.Time
	known    map[string]core.SnapshotID // latest snapshot per tier+base

	// pending has its own lock so that collecting snapshots never waits on a
	// snapshot or an Exec in progress: the actor calls TakeSnapshots from
	// its own goroutine, which must not block on I/O.
	pmu     sync.Mutex
	pending []core.EnvSnapshotBody // snapshots not yet recorded in the log
}

// warming is a lease being acquired speculatively.
type warming struct {
	spec core.EnvSpec
	done chan warmed // buffered; the goroutine never blocks on it
}

type warmed struct {
	lease Lease
	err   error
}

// NewEnv returns an idle handle. It holds no resources until first use.
func NewEnv(cfg EnvConfig) *Env {
	return &Env{cfg: cfg, known: make(map[string]core.SnapshotID)}
}

// Exec runs code in an environment matching spec, leasing or restoring one if
// needed.
func (e *Env) Exec(ctx context.Context, spec core.EnvSpec, code string) (ExecResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	lease, err := e.ensure(ctx, spec)
	if err != nil {
		return ExecResult{}, err
	}
	res, err := lease.Exec(ctx, code)
	if err != nil {
		// The environment itself failed. Drop it so the next call starts
		// clean rather than reusing something half-broken.
		e.lease.Release()
		e.lease = nil
		return ExecResult{}, fmt.Errorf("exec in %s: %w", spec.Base, err)
	}
	e.armIdle()
	return res, nil
}

// Prewarm starts acquiring an environment for spec in the background
// (speculative restore). A streamed tool call reveals its name before its
// arguments finish, so the restore overlaps the rest of the stream.
func (e *Env) Prewarm(ctx context.Context, spec core.EnvSpec) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.warm != nil || (e.lease != nil && e.spec.Compatible(spec)) {
		return
	}
	leaser, ok := e.cfg.Leasers[spec.Tier]
	if !ok {
		return
	}
	resume, err := e.lastSnapshot(ctx, spec)
	if err != nil {
		return // Exec will hit the same error and report it
	}
	w := &warming{spec: spec, done: make(chan warmed, 1)}
	e.warm = w
	// A prewarm nobody ends up using must not hold its environment open.
	e.armIdle()
	go func() {
		l, err := leaser.Lease(ctx, e.cfg.Contact, spec, resume)
		w.done <- warmed{lease: l, err: err}
	}()
}

// ensure returns a lease compatible with spec. Called with mu held.
func (e *Env) ensure(ctx context.Context, spec core.EnvSpec) (Lease, error) {
	if w := e.warm; w != nil {
		e.warm = nil
		select {
		case r := <-w.done:
			e.adopt(ctx, w.spec, r, spec)
		case <-ctx.Done():
			go discard(w) // the lease may still arrive; do not leak it
			return nil, fmt.Errorf("wait for environment: %w", ctx.Err())
		}
	}
	if e.lease != nil && e.spec.Compatible(spec) {
		return e.lease, nil
	}
	if e.lease != nil {
		// A different environment is needed; park the current one.
		e.snapshotAndRelease(ctx)
	}
	leaser, ok := e.cfg.Leasers[spec.Tier]
	if !ok {
		return nil, fmt.Errorf("tier %d: %w", spec.Tier, core.ErrNoEnvProvider)
	}
	resume, err := e.lastSnapshot(ctx, spec)
	if err != nil {
		return nil, err
	}
	l, err := leaser.Lease(ctx, e.cfg.Contact, spec, resume)
	if err != nil {
		return nil, fmt.Errorf("lease %s: %w", spec.Base, err)
	}
	e.lease, e.spec = l, spec
	return l, nil
}

// adopt takes over a speculatively acquired lease if it fits, and releases it
// otherwise. Called with mu held.
func (e *Env) adopt(ctx context.Context, warmSpec core.EnvSpec, r warmed, want core.EnvSpec) {
	if r.err != nil {
		return
	}
	if !warmSpec.Compatible(want) || (e.lease != nil && e.spec.Compatible(want)) {
		r.lease.Release()
		return
	}
	if e.lease != nil {
		e.snapshotAndRelease(ctx)
	}
	e.lease, e.spec = r.lease, warmSpec
	obs.SpeculativeRestoreHits.Inc()
}

func discard(w *warming) {
	if r := <-w.done; r.err == nil {
		r.lease.Release()
	}
}

// lastSnapshot returns the snapshot to resume spec from, or "". Called with
// mu held.
func (e *Env) lastSnapshot(ctx context.Context, spec core.EnvSpec) (core.SnapshotID, error) {
	key := specKey(spec)
	if id, ok := e.known[key]; ok {
		return id, nil
	}
	if e.cfg.Log == nil {
		return "", nil
	}
	// Snapshot events are rare, so the newest few cover every base in use.
	events, err := e.cfg.Log.LatestOfKind(ctx, e.cfg.Contact, core.KindEnvSnapshot, 32)
	if err != nil {
		return "", fmt.Errorf("find snapshot for %s: %w", spec.Base, err)
	}
	for _, ev := range events {
		var body core.EnvSnapshotBody
		if err := json.Unmarshal(ev.Body, &body); err != nil {
			return "", fmt.Errorf("decode snapshot event %d: %w", ev.Seq, err)
		}
		if k := specKey(body.Spec); e.known[k] == "" {
			e.known[k] = body.Snapshot // newest first: keep the first seen
		}
	}
	if _, ok := e.known[key]; !ok {
		e.known[key] = "" // remember that there is none
	}
	return e.known[key], nil
}

// snapshotAndRelease parks the current lease. Called with mu held.
func (e *Env) snapshotAndRelease(ctx context.Context) {
	if e.lease == nil {
		return
	}
	// A failed snapshot loses this session's state but must not wedge the
	// handle: the lease is released either way and the previous snapshot,
	// if any, stays the resume point.
	if id, err := e.lease.Snapshot(ctx); err == nil && id != "" {
		e.known[specKey(e.spec)] = id
		e.pmu.Lock()
		e.pending = append(e.pending, core.EnvSnapshotBody{Spec: e.spec, Snapshot: id})
		e.pmu.Unlock()
	}
	e.lease.Release()
	e.lease = nil
	if e.idle != nil {
		e.idle.Stop()
	}
}

// armIdle (re)starts the idle countdown. Called with mu held.
func (e *Env) armIdle() {
	e.lastUsed = time.Now()
	if e.cfg.IdleTTL <= 0 {
		return
	}
	if e.idle == nil {
		e.idle = time.AfterFunc(e.cfg.IdleTTL, e.expire)
		return
	}
	e.idle.Reset(e.cfg.IdleTTL)
}

// expire runs on the idle timer.
func (e *Env) expire() {
	if !e.mu.TryLock() {
		// Something holds the handle: usually an Exec, which re-arms the
		// timer when it finishes, but possibly a brief bookkeeping call.
		// Look again shortly rather than leaving the lease open for good.
		time.AfterFunc(time.Second, e.expire)
		return
	}
	// A retry or an earlier timer can fire after the environment was used
	// again; only a full TTL without use counts as idle.
	if time.Since(e.lastUsed) < e.cfg.IdleTTL {
		e.mu.Unlock()
		return
	}
	if w := e.warm; w != nil {
		e.warm = nil
		go discard(w)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	e.snapshotAndRelease(ctx)
	cancel()
	e.mu.Unlock()

	e.pmu.Lock()
	notify := len(e.pending) > 0
	e.pmu.Unlock()
	if notify && e.cfg.OnIdle != nil {
		e.cfg.OnIdle()
	}
}

// Hibernate snapshots and releases whatever is held, and returns every
// snapshot not yet recorded in the log.
func (e *Env) Hibernate(ctx context.Context) []core.EnvSnapshotBody {
	e.mu.Lock()
	if w := e.warm; w != nil {
		e.warm = nil
		go discard(w)
	}
	e.snapshotAndRelease(ctx)
	e.mu.Unlock()
	return e.TakeSnapshots()
}

// TakeSnapshots returns the snapshots taken since the last call, for the
// owner to append to the log as KindEnvSnapshot events. It never waits on
// the environment itself.
func (e *Env) TakeSnapshots() []core.EnvSnapshotBody {
	e.pmu.Lock()
	defer e.pmu.Unlock()
	out := e.pending
	e.pending = nil
	return out
}

func specKey(s core.EnvSpec) string {
	return strconv.Itoa(int(s.Tier)) + "/" + s.Base
}
