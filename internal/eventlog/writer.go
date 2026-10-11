package eventlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/eventlog/sqlcgen"
	"github.com/quinnovator/contactd/internal/obs"
)

// maxBatch caps one group commit.
const maxBatch = 512

// Submit queues w for the next group commit. It returns as soon as the
// request is queued; w.Done reports the outcome. Actors do not wait on the
// commit before streaming, only before acknowledging a message as durable.
func (s *Store) Submit(w core.Write) {
	select {
	case <-s.stopped:
		finish(&w, core.ErrClosed)
		return
	default:
	}
	select {
	case s.in <- w:
	case <-s.stopped:
		finish(&w, core.ErrClosed)
	}
}

// Append submits w and waits for it to commit.
func (s *Store) Append(ctx context.Context, w core.Write) error {
	done := make(chan error, 1)
	w.Done = func(err error) { done <- err }
	s.Submit(w)
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("wait for commit: %w", ctx.Err())
	}
}

// Run is the single writer. It blocks for the first request, drains whatever
// else is already queued, and commits it all in one transaction: when idle,
// one event costs one commit; under load, batches grow by themselves, with no
// timer adding latency.
//
// Run returns after ctx is cancelled and everything queued has been
// committed. Cancel it only once nothing submits any more.
func (s *Store) Run(ctx context.Context) {
	defer close(s.stopped)
	batch := make([]core.Write, 0, maxBatch)
	for {
		select {
		case <-ctx.Done():
			// Drain: everything already queued is still committed.
			for {
				batch = s.drain(batch[:0])
				if len(batch) == 0 {
					return
				}
				s.commitBatch(ctx, batch)
			}
		case w := <-s.in:
			batch = s.drain(append(batch[:0], w))
			s.commitBatch(ctx, batch)
		}
	}
}

func (s *Store) drain(batch []core.Write) []core.Write {
	for len(batch) < cap(batch) {
		select {
		case w := <-s.in:
			batch = append(batch, w)
		default:
			return batch
		}
	}
	return batch
}

func (s *Store) commitBatch(ctx context.Context, batch []core.Write) {
	start := time.Now()
	// The writer must finish what it was handed even while shutting down, so
	// commits do not inherit the run context's cancellation.
	ctx = context.WithoutCancel(ctx)

	// Refuse writes for contacts whose earlier write failed. In log order,
	// so a Reset re-admits whatever follows it.
	admitted := batch[:0]
	for i := range batch {
		w := batch[i]
		if s.refused(&w) {
			finish(&w, core.ErrWriteRefused)
			continue
		}
		admitted = append(admitted, w)
	}
	if len(admitted) == 0 {
		return
	}

	err := s.commit(ctx, admitted)
	obs.WriterBatchSize.Observe(float64(len(admitted)))
	obs.CommitMS.Observe(obs.SinceMS(start))
	if err == nil {
		for i := range admitted {
			finish(&admitted[i], nil)
		}
		return
	}
	// One bad request must not fail its neighbours: retry each on its own.
	// A contact whose write fails here is refused from then on, so that a
	// later event of the same contact cannot commit past the hole.
	for i := range admitted {
		w := &admitted[i]
		if s.refused(w) {
			finish(w, core.ErrWriteRefused)
			continue
		}
		werr := err
		if len(admitted) > 1 {
			werr = s.commit(ctx, admitted[i:i+1])
		}
		if werr != nil {
			s.failed[w.Contact] = struct{}{}
		}
		finish(w, werr)
	}
}

// refused reports whether w must not be committed because an earlier write
// for its contact failed. A Reset clears that state and is itself admitted.
func (s *Store) refused(w *core.Write) bool {
	if w.Reset {
		delete(s.failed, w.Contact)
		return false
	}
	_, bad := s.failed[w.Contact]
	return bad
}

func (s *Store) commit(ctx context.Context, batch []core.Write) (err error) {
	tx, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	insert := tx.StmtContext(ctx, s.insertEvent)
	q := sqlcgen.New(tx)
	for i := range batch {
		if err = apply(ctx, insert, q, &batch[i]); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func apply(ctx context.Context, insert *sql.Stmt, q *sqlcgen.Queries, w *core.Write) error {
	contact := i64(uint64(w.Contact))
	if ev := w.Event; ev != nil {
		if _, err := insert.ExecContext(ctx, i64(uint64(ev.Contact)), i64(ev.Seq), ev.TS, int64(ev.Kind), ev.Body); err != nil {
			return fmt.Errorf("insert event %d of contact %d: %w", ev.Seq, ev.Contact, err)
		}
	}
	if v := w.Vector; v != nil {
		err := q.UpsertVector(ctx, sqlcgen.UpsertVectorParams{Contact: contact, Seq: i64(v.Seq), Vec: core.EncodeVector(v.Vec)})
		if err != nil {
			return fmt.Errorf("upsert vector %d of contact %d: %w", v.Seq, w.Contact, err)
		}
	}
	if ep := w.Episode; ep != nil {
		if err := q.UpsertEpisode(ctx, episodeParams(w.Contact, ep)); err != nil {
			return fmt.Errorf("upsert episode %d of contact %d: %w", ep.ID, w.Contact, err)
		}
	}
	if w.Touch != 0 {
		if err := q.TouchContact(ctx, sqlcgen.TouchContactParams{LastActive: w.Touch, ID: contact}); err != nil {
			return fmt.Errorf("touch contact %d: %w", w.Contact, err)
		}
	}
	return nil
}

func finish(w *core.Write, err error) {
	if w.Done != nil {
		w.Done(err)
	}
}
