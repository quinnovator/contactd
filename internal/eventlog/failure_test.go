package eventlog_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/eventlog"
	fakeinfer "github.com/quinnovator/contactd/internal/infer/fake"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/recall"
	"github.com/quinnovator/contactd/internal/turn"
)

type embed struct{}

func (embed) Embed(_ context.Context, texts []string, _ recall.Priority) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = fakeinfer.Embed(t)
	}
	return out, nil
}

type rerank struct{}

func (rerank) Score(_ context.Context, q string, ps []string) ([]float32, error) {
	out := make([]float32, len(ps))
	for i, p := range ps {
		out[i] = fakeinfer.Score(q, p)
	}
	return out, nil
}

// TestTransientWriteFailureKeepsOneWriterAndNoGaps runs real actors over a
// real SQLite file while another connection holds the write lock for longer
// than the busy timeout, as a backup or a shell would. Writes fail for a
// while and then the disk is healthy again. Whatever was acknowledged must
// be in the log, in order, with gapless sequence numbers, and the contact
// must keep working: the invariant that each contact has exactly one writer
// has to survive the failure.
func TestTransientWriteFailureKeepsOneWriterAndNoGaps(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "contacts.db")
	s := openAt(t, path, eventlog.Options{BusyTimeout: 150 * time.Millisecond})
	id := contact(t, s)

	models := fakellm.NewClient(fakellm.New(fakellm.Options{}))
	cfg := actor.DefaultConfig()
	cfg.Debounce, cfg.DebounceCap = 10*time.Millisecond, 50*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	reg := actor.New(ctx, actor.Deps{
		Log: s, Hydrate: s, Repair: s, Embed: embed{}, Snapshots: s,
		Turn: &turn.Deps{
			Embed: embed{}, Rerank: rerank{}, Log: s, Models: models, Logger: obs.Discard(),
			Recall: cfg.Recall, Plan: plan.DefaultParams(), MaxToolSteps: 4, MaxToolOutput: 4096, QueryTokens: 128,
		},
	}, cfg, obs.Discard())
	t.Cleanup(func() {
		reg.Shutdown(ctx)
		cancel()
	})

	type result struct {
		text string
		seq  uint64
		err  error
	}
	results := make(chan result, 64)
	send := func(text string) {
		go func() {
			seq, err := reg.Message(t.Context(), id, text)
			results <- result{text, seq, err}
		}()
	}

	// A healthy exchange first.
	send("hello")
	if r := <-results; r.err != nil || r.seq != 2 {
		t.Fatalf("first message: %+v", r)
	}
	waitEvents(t, s, id, 4) // the message, the attempt record and the reply

	// Another process takes the write lock and keeps it.
	other, err := sql.Open("sqlite3", "file:"+path+"?_txlock=immediate&_busy_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	tx, err := other.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Messages keep arriving throughout the outage and after it. The lock
	// is held until a write has failed, not for a fixed time: the store
	// waits out the busy timeout once for a batch and again for each write
	// in it, so how long an outage has to last depends on what was queued.
	acked := map[uint64]string{}
	failed, sent, seen := 0, 0, 0
	collect := func(r result) {
		seen++
		if r.err != nil {
			failed++
			return
		}
		if prev, dup := acked[r.seq]; dup {
			t.Errorf("seq %d was acknowledged for both %q and %q", r.seq, prev, r.text)
		}
		acked[r.seq] = r.text
	}
	arrive := func() {
		send("during " + strconv.Itoa(sent))
		sent++
		select {
		case r := <-results:
			collect(r)
		case <-time.After(40 * time.Millisecond):
		}
	}
	for deadline := time.Now().Add(10 * time.Second); failed == 0; {
		if time.Now().After(deadline) {
			t.Fatal("no write failed while another connection held the write lock")
		}
		arrive()
	}
	if err := tx.Rollback(); err != nil { // the lock is released mid-stream
		t.Fatal(err)
	}
	for range 6 {
		arrive()
	}
	for seen < sent {
		collect(<-results)
	}
	if len(acked) == 0 {
		t.Fatalf("%d failed, none acknowledged; the test needs both to mean anything", failed)
	}

	// With the disk healthy, the contact must simply work.
	send("after")
	if r := <-results; r.err != nil {
		t.Fatalf("a message on a healthy disk failed after the outage: %v", r.err)
	}
	time.Sleep(300 * time.Millisecond) // let the last reply land

	events, err := s.EventsAfter(t.Context(), id, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event %d in the log has seq %d: the sequence has a gap or is misordered", i+1, ev.Seq)
		}
	}
	for seq, text := range acked {
		if seq > uint64(len(events)) {
			t.Errorf("%q was acknowledged as seq %d, which is not in the log", text, seq)
			continue
		}
		ev := events[seq-1]
		if ev.Kind != core.KindUserMsg || string(ev.Body) != `{"text":"`+text+`"}` {
			t.Errorf("seq %d was acknowledged for %q but the log holds %s", seq, text, ev.Body)
		}
	}
	// Derived data never points past the log.
	vecs, err := s.Vectors(t.Context(), id, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vecs {
		if v.Seq > uint64(len(events)) {
			t.Errorf("a vector is stored for seq %d, which is not in the log", v.Seq)
		}
	}
	t.Logf("%d acknowledged, %d failed during the outage; %d events in a gapless log", len(acked), failed, len(events))
}

func waitEvents(t *testing.T, s *eventlog.Store, id core.ContactID, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		events, err := s.EventsAfter(t.Context(), id, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d events arrived", len(events), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
