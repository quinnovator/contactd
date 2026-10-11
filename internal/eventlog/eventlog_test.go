package eventlog_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/eventlog"
	"github.com/quinnovator/contactd/internal/obs"
)

// open starts a store with its writer running and stops both at cleanup.
func open(tb testing.TB, opt eventlog.Options) *eventlog.Store {
	tb.Helper()
	return openAt(tb, filepath.Join(tb.TempDir(), "contacts.db"), opt)
}

func openAt(tb testing.TB, path string, opt eventlog.Options) *eventlog.Store {
	tb.Helper()
	s, err := eventlog.Open(tb.Context(), path, opt)
	if err != nil {
		tb.Fatalf("Open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	tb.Cleanup(func() {
		cancel()
		<-done
		if err := s.Close(); err != nil {
			tb.Errorf("Close: %v", err)
		}
	})
	return s
}

func contact(tb testing.TB, s *eventlog.Store) core.ContactID {
	tb.Helper()
	id, err := s.CreateContact(tb.Context(), core.AgentConfig{System: "be kind"}, "likes tea", 1000)
	if err != nil {
		tb.Fatalf("CreateContact: %v", err)
	}
	return id
}

func event(id core.ContactID, seq uint64, kind core.EventKind, body string) *core.Event {
	return &core.Event{Contact: id, Seq: seq, TS: int64(1000 + seq), Kind: kind, Body: []byte(body)}
}

func TestContactRoundTrip(t *testing.T) {
	t.Parallel()
	s := open(t, eventlog.Options{})
	agent := core.AgentConfig{
		System: "You are Ada.",
		Model:  "m",
		Tools:  []core.ToolDef{{Name: "python", Description: "run", Env: core.EnvSpec{Base: "python-3.12"}}},
	}
	id, err := s.CreateContact(t.Context(), agent, "profile text", 42)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Contact(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent.System != agent.System || len(got.Agent.Tools) != 1 || got.Profile != "profile text" || got.LastActive != 42 {
		t.Errorf("contact did not round-trip: %+v", got)
	}
	if _, err := s.Contact(t.Context(), id+1000); !errors.Is(err, core.ErrContactNotFound) {
		t.Errorf("missing contact: err = %v, want ErrContactNotFound", err)
	}
	if _, err := s.Hydrate(t.Context(), id+1000, 10); !errors.Is(err, core.ErrContactNotFound) {
		t.Errorf("hydrating a missing contact: err = %v, want ErrContactNotFound", err)
	}
}

func TestAppendAndRead(t *testing.T) {
	t.Parallel()
	s := open(t, eventlog.Options{})
	id := contact(t, s)
	other := contact(t, s)
	ctx := t.Context()

	kinds := []core.EventKind{core.KindUserMsg, core.KindAgentMsg, core.KindToolCall, core.KindToolResult, core.KindUserMsg, core.KindEnvSnapshot}
	for i, k := range kinds {
		seq := uint64(i + 1)
		if err := s.Append(ctx, core.Write{Contact: id, Event: event(id, seq, k, fmt.Sprintf(`{"n":%d}`, seq)), Touch: int64(5000 + i)}); err != nil {
			t.Fatalf("append %d: %v", seq, err)
		}
	}
	// Another contact's events must never leak into this one's scans.
	if err := s.Append(ctx, core.Write{Contact: other, Event: event(other, 1, core.KindUserMsg, `{}`)}); err != nil {
		t.Fatal(err)
	}

	h, err := s.Hydrate(ctx, id, 4)
	if err != nil {
		t.Fatal(err)
	}
	if h.NextSeq != 7 || len(h.Tail) != 4 || h.Tail[0].Seq != 3 || h.Tail[3].Seq != 6 {
		t.Errorf("Hydrate: next=%d tail=%v; want next 7 and seqs 3..6 ascending", h.NextSeq, seqs(h.Tail))
	}
	if h.Contact.LastActive != 5005 {
		t.Errorf("last_active = %d, want 5005", h.Contact.LastActive)
	}

	if got, _ := s.EventsAfter(ctx, id, 2, 3); seqsString(got) != "3 4 5" {
		t.Errorf("EventsAfter(2, limit 3) = %s", seqsString(got))
	}
	if got, _ := s.EventsRange(ctx, id, 2, 4, 10); seqsString(got) != "2 3 4" {
		t.Errorf("EventsRange(2,4) = %s", seqsString(got))
	}
	if got, _ := s.MessagesAfter(ctx, id, 0, 10); seqsString(got) != "1 2 5" {
		t.Errorf("MessagesAfter = %s, want only user and agent messages", seqsString(got))
	}
	if got, _ := s.LatestOfKind(ctx, id, core.KindUserMsg, 5); seqsString(got) != "5 1" {
		t.Errorf("LatestOfKind = %s, want newest first", seqsString(got))
	}
	ev, err := s.Event(ctx, id, 3)
	if err != nil || ev.Kind != core.KindToolCall || string(ev.Body) != `{"n":3}` {
		t.Errorf("Event(3) = %+v, %v", ev, err)
	}

	fresh, err := s.Hydrate(ctx, contact(t, s), 4)
	if err != nil || fresh.NextSeq != 1 || len(fresh.Tail) != 0 {
		t.Errorf("empty contact: next=%d tail=%d err=%v", fresh.NextSeq, len(fresh.Tail), err)
	}
}

func TestVectorsAndEpisodes(t *testing.T) {
	t.Parallel()
	s := open(t, eventlog.Options{})
	id := contact(t, s)
	ctx := t.Context()

	vec := make([]float32, core.VectorDim)
	for i := range vec {
		vec[i] = float32(i) / 1000
	}
	row := core.EpisodeRow{ID: 0, Start: 1, End: 1, Centroid: []byte{1, 2, 3}}
	// The event, its vector and the episode row commit together.
	err := s.Append(ctx, core.Write{
		Contact: id,
		Event:   event(id, 1, core.KindUserMsg, `{}`),
		Vector:  &core.Vector{Seq: 1, Vec: vec},
		Episode: &row,
	})
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := s.Vectors(ctx, id, 0, 10)
	if err != nil || len(vecs) != 1 || vecs[0].Seq != 1 || vecs[0].Vec[383] != vec[383] {
		t.Fatalf("Vectors = %d rows, err %v", len(vecs), err)
	}

	// Upserts replace: extend the episode, then link a summary.
	row.End, row.SummarySeq = 5, 9
	if err := s.Append(ctx, core.Write{Contact: id, Episode: &row}); err != nil {
		t.Fatal(err)
	}
	eps, err := s.Episodes(ctx, id)
	if err != nil || len(eps) != 1 || eps[0].End != 5 || eps[0].SummarySeq != 9 || string(eps[0].Centroid) != "\x01\x02\x03" {
		t.Fatalf("Episodes = %+v, err %v", eps, err)
	}

	rebuilt := []core.EpisodeRow{{ID: 0, Start: 1, End: 2, Centroid: []byte{9}}, {ID: 1, Start: 3, End: 4, Centroid: []byte{8}}}
	if err := s.ReplaceEpisodes(ctx, id, rebuilt); err != nil {
		t.Fatal(err)
	}
	eps, _ = s.Episodes(ctx, id)
	if len(eps) != 2 || eps[0].SummarySeq != 0 || eps[1].Start != 3 {
		t.Errorf("after ReplaceEpisodes: %+v", eps)
	}
}

// TestGroupCommit checks the adaptive behaviour: an idle writer commits one
// event per transaction, and under concurrent load batches grow by
// themselves.
func TestGroupCommit(t *testing.T) {
	t.Parallel()
	s := open(t, eventlog.Options{})
	const writers, perWriter = 32, 200

	ids := make([]core.ContactID, writers)
	for i := range ids {
		ids[i] = contact(t, s)
	}
	var committed atomic.Int64
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			for seq := uint64(1); seq <= perWriter; seq++ {
				if err := s.Append(t.Context(), core.Write{Contact: id, Event: event(id, seq, core.KindUserMsg, `{}`)}); err != nil {
					t.Errorf("append: %v", err)
					return
				}
				committed.Add(1)
			}
		})
	}
	wg.Wait()
	if committed.Load() != writers*perWriter {
		t.Fatalf("committed %d of %d", committed.Load(), writers*perWriter)
	}
	for _, id := range ids {
		h, err := s.Hydrate(t.Context(), id, 1)
		if err != nil || h.NextSeq != perWriter+1 {
			t.Fatalf("contact %d: next seq %d, err %v", id, h.NextSeq, err)
		}
	}
}

// TestFailedWriteIsContainedToItsContact submits a burst in which one
// contact's write fails (a duplicate sequence number). Its neighbours in the
// same batch must commit. That contact's own later writes must not: letting
// them through would leave a hole in its sequence numbers. They are refused
// until its writer resets.
func TestFailedWriteIsContainedToItsContact(t *testing.T) {
	t.Parallel()
	s := open(t, eventlog.Options{})
	bad, good := contact(t, s), contact(t, s)
	ctx := t.Context()
	if err := s.Append(ctx, core.Write{Contact: bad, Event: event(bad, 1, core.KindUserMsg, `{}`)}); err != nil {
		t.Fatal(err)
	}

	const n = 40
	errs := make([]error, 2*n)
	var wg sync.WaitGroup
	wg.Add(2 * n)
	for i := range n {
		badSeq := uint64(i + 2)
		if i == n/2 {
			badSeq = 1 // collides with the event above
		}
		s.Submit(core.Write{Contact: bad, Event: event(bad, badSeq, core.KindUserMsg, `{}`), Done: func(err error) {
			errs[2*i] = err
			wg.Done()
		}})
		s.Submit(core.Write{Contact: good, Event: event(good, uint64(i+1), core.KindUserMsg, `{}`), Done: func(err error) {
			errs[2*i+1] = err
			wg.Done()
		}})
	}
	wg.Wait()
	for i := range n {
		if err := errs[2*i+1]; err != nil {
			t.Errorf("the healthy contact's write %d failed: %v", i, err)
		}
		switch err := errs[2*i]; {
		case i < n/2 && err != nil:
			t.Errorf("write %d, before the bad one, failed: %v", i, err)
		case i == n/2 && (err == nil || errors.Is(err, core.ErrWriteRefused)):
			t.Errorf("the duplicate write: err = %v, want the constraint failure", err)
		case i > n/2 && !errors.Is(err, core.ErrWriteRefused):
			t.Errorf("write %d, after the bad one: err = %v, want ErrWriteRefused", i, err)
		}
	}

	// The log holds exactly the prefix before the failure: no hole.
	h, err := s.Hydrate(ctx, bad, 100)
	if err != nil || h.NextSeq != uint64(n/2+2) {
		t.Fatalf("after the failure: next seq %d (err %v), want %d", h.NextSeq, err, n/2+2)
	}
	// Still refused later, in another batch, with the disk healthy.
	if err := s.Append(ctx, core.Write{Contact: bad, Event: event(bad, h.NextSeq, core.KindUserMsg, `{}`)}); !errors.Is(err, core.ErrWriteRefused) {
		t.Fatalf("write before the reset: err = %v, want ErrWriteRefused", err)
	}
	// The retiring actor's reset re-admits the contact's next writer.
	if err := s.Append(ctx, core.Write{Contact: bad, Reset: true}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := s.Append(ctx, core.Write{Contact: bad, Event: event(bad, h.NextSeq, core.KindUserMsg, `{}`)}); err != nil {
		t.Fatalf("write after the reset: %v", err)
	}
}

func TestDrainsOnShutdownAndReopens(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "contacts.db")
	s, err := eventlog.Open(t.Context(), path, eventlog.Options{SyncFull: true})
	if err != nil {
		t.Fatal(err)
	}
	id := contact(t, s)

	// Queue work before the writer even starts, then stop it immediately:
	// everything already queued must still be committed.
	const n = 300
	var done atomic.Int64
	for seq := uint64(1); seq <= n; seq++ {
		s.Submit(core.Write{Contact: id, Event: event(id, seq, core.KindUserMsg, `{}`), Done: func(err error) {
			if err == nil {
				done.Add(1)
			}
		}})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Run(ctx)
	if done.Load() != n {
		t.Fatalf("%d of %d queued writes committed before shutdown", done.Load(), n)
	}

	// After the writer has stopped, a submit fails fast instead of hanging.
	late := make(chan error, 1)
	s.Submit(core.Write{Contact: id, Event: event(id, n+1, core.KindUserMsg, `{}`), Done: func(err error) { late <- err }})
	select {
	case err := <-late:
		if !errors.Is(err, core.ErrClosed) {
			t.Errorf("late submit: err = %v, want ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late submit never completed")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening runs no migration twice and finds the data.
	s2 := openAt(t, path, eventlog.Options{})
	h, err := s2.Hydrate(t.Context(), id, 5)
	if err != nil || h.NextSeq != n+1 {
		t.Fatalf("after reopen: next seq %d, err %v", h.NextSeq, err)
	}
	if count, err := s2.CountContacts(t.Context()); err != nil || count != 1 {
		t.Errorf("CountContacts = %d, %v", count, err)
	}
}

func seqs(events []core.Event) []uint64 {
	out := make([]uint64, len(events))
	for i, ev := range events {
		out[i] = ev.Seq
	}
	return out
}

func seqsString(events []core.Event) string {
	parts := make([]string, len(events))
	for i, ev := range events {
		parts[i] = strconv.FormatUint(ev.Seq, 10)
	}
	return strings.Join(parts, " ")
}

// BenchmarkWriter measures the group-commit writer across concurrency
// levels. Each writer appends to its own contact and waits for every commit,
// as an actor acknowledging messages would. Events per commit should grow
// with load while a lone writer still pays exactly one commit per event.
func BenchmarkWriter(b *testing.B) {
	for _, writers := range []int{1, 8, 64, 512} {
		b.Run(fmt.Sprintf("writers=%d", writers), func(b *testing.B) {
			s := open(b, eventlog.Options{})
			ids := make([]core.ContactID, writers)
			for i := range ids {
				ids[i] = contact(b, s)
			}
			commitsBefore := commits(b)
			per := b.N/writers + 1
			var wg sync.WaitGroup
			b.ResetTimer()
			for _, id := range ids {
				wg.Go(func() {
					for seq := uint64(1); seq <= uint64(per); seq++ {
						if err := s.Append(b.Context(), core.Write{Contact: id, Event: event(id, seq, core.KindUserMsg, `{"text":"hello"}`)}); err != nil {
							b.Error(err)
							return
						}
					}
				})
			}
			wg.Wait()
			b.StopTimer()
			events := float64(per * writers)
			b.ReportMetric(events/b.Elapsed().Seconds(), "events/s")
			b.ReportMetric(events/float64(commits(b)-commitsBefore), "events/commit")
			b.ReportMetric(float64(b.Elapsed().Microseconds())/float64(per), "µs/append")
		})
	}
}

// commits reads how many transactions the writer has committed so far.
func commits(tb testing.TB) uint64 {
	tb.Helper()
	var m dto.Metric
	if err := obs.WriterBatchSize.Write(&m); err != nil {
		tb.Fatal(err)
	}
	return m.GetHistogram().GetSampleCount()
}
