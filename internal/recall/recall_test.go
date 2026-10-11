package recall_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	fakeinfer "github.com/quinnovator/contactd/internal/infer/fake"
	"github.com/quinnovator/contactd/internal/recall"
)

const (
	minute = int64(60_000)
	day    = 24 * 60 * minute
)

// memLog is an in-memory EventReader.
type memLog struct {
	events []core.Event
}

func (l *memLog) add(kind core.EventKind, ts int64, body any) core.Event {
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	ev := core.Event{Contact: 1, Seq: uint64(len(l.events) + 1), TS: ts, Kind: kind, Body: raw}
	l.events = append(l.events, ev)
	return ev
}

func (l *memLog) Event(_ context.Context, _ core.ContactID, seq uint64) (core.Event, error) {
	if seq == 0 || seq > uint64(len(l.events)) {
		return core.Event{}, fmt.Errorf("no event %d", seq)
	}
	return l.events[seq-1], nil
}

func (l *memLog) EventsRange(_ context.Context, _ core.ContactID, from, to uint64, limit int) ([]core.Event, error) {
	var out []core.Event
	for _, ev := range l.events {
		if ev.Seq >= from && ev.Seq <= to && len(out) < limit {
			out = append(out, ev)
		}
	}
	return out, nil
}

// rerank adapts the fake cross-encoder to recall.Reranker.
type rerank struct{ err error }

func (r rerank) Score(_ context.Context, query string, passages []string) ([]float32, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := make([]float32, len(passages))
	for i, p := range passages {
		out[i] = fakeinfer.Score(query, p)
	}
	return out, nil
}

// say appends a user message, embeds it and folds it into the index.
func say(l *memLog, x *recall.Index, ts int64, text string) *recall.Episode {
	ev := l.add(core.KindUserMsg, ts, core.MsgBody{Text: text})
	return x.OnEmbedded(ev, fakeinfer.Embed(text))
}

func TestSegmentation(t *testing.T) {
	t.Parallel()
	p := recall.DefaultParams()
	p.Tau = 0.2

	t.Run("a long silence opens a new episode", func(t *testing.T) {
		t.Parallel()
		l, x := new(memLog), mustIndex(t, p, nil)
		say(l, x, 0, "the postgres migration is failing on the orders table")
		say(l, x, 5*minute, "postgres migration lock timeout on orders")
		if x.Len() != 1 {
			t.Fatalf("same topic within the gap: %d episodes, want 1", x.Len())
		}
		say(l, x, 5*minute+p.GapMS+1, "postgres migration finally passed on orders")
		if x.Len() != 2 {
			t.Fatalf("after a %d ms gap: %d episodes, want 2", p.GapMS+1, x.Len())
		}
	})

	t.Run("one off-topic aside does not split; two in a row do", func(t *testing.T) {
		t.Parallel()
		l, x := new(memLog), mustIndex(t, p, nil)
		say(l, x, 0, "the postgres migration is failing on the orders table")
		say(l, x, minute, "postgres migration lock timeout orders table")
		say(l, x, 2*minute, "lunch sandwich avocado")
		if x.Len() != 1 {
			t.Fatalf("after a single aside: %d episodes, want 1", x.Len())
		}
		say(l, x, 3*minute, "postgres migration orders table retry")
		if x.Len() != 1 {
			t.Fatalf("back on topic: %d episodes, want 1", x.Len())
		}
		say(l, x, 4*minute, "marathon training plan intervals")
		say(l, x, 5*minute, "marathon long run pacing sunday")
		if x.Len() != 2 {
			t.Fatalf("after two drifted messages in a row: %d episodes, want 2", x.Len())
		}
	})
}

func TestRowRoundTrip(t *testing.T) {
	t.Parallel()
	l, x := new(memLog), mustIndex(t, recall.DefaultParams(), nil)
	say(l, x, 0, "sourdough starter feeding schedule")
	say(l, x, minute, "sourdough hydration ratio flour")
	say(l, x, 2*day, "guitar chord progression practice")
	x.SetSummary(0, 99)

	rows := x.Rows()
	y := mustIndex(t, recall.DefaultParams(), rows)
	if y.Len() != x.Len() || y.LastIndexed() != x.LastIndexed() {
		t.Fatalf("reloaded index has %d episodes ending at %d; want %d ending at %d",
			y.Len(), y.LastIndexed(), x.Len(), x.LastIndexed())
	}
	got, want := y.Rows(), rows
	for i := range want {
		if string(got[i].Centroid) != string(want[i].Centroid) || got[i].SummarySeq != want[i].SummarySeq ||
			got[i].Start != want[i].Start || got[i].End != want[i].End {
			t.Errorf("episode %d did not round-trip", want[i].ID)
		}
	}
	// The reloaded index keeps extending the open episode.
	if ep := say(l, y, 2*day+minute, "guitar chord fingering practice"); ep.ID != 1 || ep.N != 2 {
		t.Errorf("after reload the open episode became %+v, want episode 1 with 2 members", ep)
	}

	if _, err := recall.NewIndex(recall.DefaultParams(), []core.EpisodeRow{{ID: 0, Centroid: []byte("short")}}); err == nil {
		t.Error("a truncated centroid blob should be rejected")
	}
}

func TestTopK(t *testing.T) {
	t.Parallel()
	l, x := new(memLog), mustIndex(t, recall.DefaultParams(), nil)
	topics := []string{
		"postgres migration orders table lock",
		"lisbon trip flights hotel alfama",
		"sourdough starter flour hydration",
		"marathon training long run pacing",
	}
	for i, topic := range topics {
		say(l, x, int64(i)*day, topic)
		say(l, x, int64(i)*day+minute, topic+" again")
	}
	snap := x.Snapshot()

	got := snap.TopK(fakeinfer.Embed("sourdough flour"), 2, math.MaxUint64)
	if len(got) != 2 || got[0].Ep.ID != 2 {
		t.Fatalf("top hit = %+v, want episode 2 (sourdough)", got)
	}
	if got[0].Stage1 < got[1].Stage1 {
		t.Error("candidates are not sorted by descending score")
	}

	// Episodes that reach into the tail are already in context.
	tailStart := snap.Get(2).Start
	for _, c := range snap.TopK(fakeinfer.Embed("sourdough flour"), 10, tailStart) {
		if c.Ep.End >= tailStart {
			t.Errorf("episode %d overlaps the tail (ends at %d, tail starts at %d)", c.Ep.ID, c.Ep.End, tailStart)
		}
	}
	if got := snap.TopK(fakeinfer.Embed("sourdough"), 10, 0); len(got) != 0 {
		t.Errorf("with everything in the tail TopK returned %d candidates", len(got))
	}
}

// TestInt8MatchesFloat checks the memory-budget claim: ranking by quantized
// centroids agrees with full precision closely enough for a shortlist.
func TestInt8MatchesFloat(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2))
	const episodes = 500
	l, x := new(memLog), mustIndex(t, recall.DefaultParams(), nil)
	texts := make([]string, episodes)
	for i := range texts {
		words := make([]string, 6)
		for j := range words {
			words[j] = fmt.Sprintf("w%d", rng.IntN(400))
		}
		texts[i] = strings.Join(words, " ")
		say(l, x, int64(i)*day, texts[i])
	}
	snap := x.Snapshot()

	agree := 0
	const queries = 50
	for q := range queries {
		target := rng.IntN(episodes - 1)
		qv := fakeinfer.Embed(texts[target])
		best, bestCos := -1, float32(-2)
		for i, text := range texts[:episodes-1] {
			if c := dot(qv, fakeinfer.Embed(text)); c > bestCos {
				best, bestCos = i, c
			}
		}
		for _, c := range snap.TopK(qv, 5, math.MaxUint64) {
			if int(c.Ep.ID) == best {
				agree++
				break
			}
		}
		_ = q
	}
	if agree < queries*95/100 {
		t.Errorf("exact nearest episode was in the int8 top 5 for %d of %d queries", agree, queries)
	}
}

func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

func TestRecall(t *testing.T) {
	t.Parallel()
	p := recall.DefaultParams()
	l, x := new(memLog), mustIndex(t, p, nil)
	say(l, x, 0, "the postgres migration is failing on the orders table")
	say(l, x, minute, "postgres migration lock timeout orders")
	say(l, x, 3*day, "booking flights to lisbon for the conference")
	say(l, x, 3*day+minute, "lisbon hotel in alfama near the venue")
	say(l, x, 9*day, "sourdough starter is not rising")
	say(l, x, 9*day+minute, "sourdough flour hydration ratio")
	// The current conversation, which is in the tail.
	cur := say(l, x, 20*day, "what should we have for dinner")
	snap := x.Snapshot()

	query := func(text string) recall.Query {
		return recall.Query{Contact: 1, Text: text, Vec: fakeinfer.Embed(text), TailStart: cur.Start, Now: 20 * day}
	}

	t.Run("a callback recalls its episode first", func(t *testing.T) {
		t.Parallel()
		got, degraded, err := recall.Recall(t.Context(), snap, query("back to the postgres migration thing"), l, rerank{}, p)
		if err != nil || degraded {
			t.Fatalf("Recall: err=%v degraded=%v", err, degraded)
		}
		if len(got) != 3 || got[0].Ep.ID != 0 {
			t.Fatalf("ranked %d episodes with %d first; want 3 with the postgres episode (0) first", len(got), got[0].Ep.ID)
		}
		if got[0].Score < p.Theta || got[1].Score >= p.Theta {
			t.Errorf("scores %v, %v: only the postgres episode should clear theta=%v", got[0].Score, got[1].Score, p.Theta)
		}
		if !strings.Contains(got[0].Passage, "postgres") || !strings.HasPrefix(got[0].Passage, "[1970-01-01] ") {
			t.Errorf("passage = %q", got[0].Passage)
		}
	})

	t.Run("an unrelated message recalls nothing above theta", func(t *testing.T) {
		t.Parallel()
		got, _, err := recall.Recall(t.Context(), snap, query("maybe pizza tonight"), l, rerank{}, p)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range got {
			if s.Score >= p.Theta {
				t.Errorf("episode %d scored %v, above theta, for an unrelated query", s.Ep.ID, s.Score)
			}
		}
	})

	t.Run("a reranker failure degrades to stage 1", func(t *testing.T) {
		t.Parallel()
		got, degraded, err := recall.Recall(t.Context(), snap, query("postgres migration"), l, rerank{err: errors.New("model crashed")}, p)
		if err != nil {
			t.Fatal(err)
		}
		if !degraded || len(got) != 2 || got[0].Ep.ID != 0 {
			t.Fatalf("degraded=%v, got %d candidates led by %d; want the top 2 by stage 1 led by episode 0", degraded, len(got), got[0].Ep.ID)
		}
	})

	t.Run("cancellation is an error, not a degradation", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, _, err := recall.Recall(ctx, snap, query("postgres"), l, rerank{err: context.Canceled}, p)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("recency breaks ties", func(t *testing.T) {
		t.Parallel()
		l2, x2 := new(memLog), mustIndex(t, p, nil)
		say(l2, x2, 0, "sourdough starter flour")
		say(l2, x2, 30*day, "sourdough starter flour")
		tail := say(l2, x2, 60*day, "dinner plans")
		q := recall.Query{Contact: 1, Text: "sourdough starter", Vec: fakeinfer.Embed("sourdough starter"), TailStart: tail.Start, Now: 31 * day}
		got, _, err := recall.Recall(t.Context(), x2.Snapshot(), q, l2, rerank{}, p)
		if err != nil {
			t.Fatal(err)
		}
		if got[0].Ep.ID != 1 {
			t.Errorf("of two identical episodes the older one ranked first")
		}
	})
}

func TestPassageUsesSummaryOnceItExists(t *testing.T) {
	t.Parallel()
	l, x := new(memLog), mustIndex(t, recall.DefaultParams(), nil)
	say(l, x, 0, "first message about kubernetes")
	say(l, x, minute, "last message about kubernetes")
	say(l, x, 3*day, "unrelated")
	sum := l.add(core.KindSummary, 4*day, core.SummaryBody{Episode: 0, Start: 1, End: 2, Text: "Debugged a kubernetes outage."})
	before, err := recall.Passage(t.Context(), l, 1, x.Snapshot().Get(0), 256)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, "first message") || !strings.Contains(before, "last message") {
		t.Errorf("excerpt = %q, want the first and last messages", before)
	}
	x.SetSummary(0, sum.Seq)
	after, err := recall.Passage(t.Context(), l, 1, x.Snapshot().Get(0), 256)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(after, "Debugged a kubernetes outage.") {
		t.Errorf("passage = %q, want the summary", after)
	}
}

func TestRebuildMatchesLiveIndex(t *testing.T) {
	t.Parallel()
	p := recall.DefaultParams()
	l, x := new(memLog), mustIndex(t, p, nil)
	vecs := make([]core.Vector, 0, 6)
	for i, text := range []string{
		"postgres migration orders", "postgres lock timeout", "lisbon flights", "lisbon hotel alfama",
		"sourdough starter", "sourdough flour ratio",
	} {
		ts := int64(i/2)*day + int64(i%2)*minute
		ev := l.add(core.KindUserMsg, ts, core.MsgBody{Text: text})
		v := fakeinfer.Embed(text)
		x.OnEmbedded(ev, v)
		vecs = append(vecs, core.Vector{Seq: ev.Seq, Vec: v})
	}
	rebuilt := recall.Rebuild(p, l.events, vecs, map[[2]uint64]uint64{{1, 2}: 77})
	got, want := rebuilt.Rows(), x.Rows()
	if len(got) != len(want) {
		t.Fatalf("rebuilt %d episodes, live index has %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Start != want[i].Start || got[i].End != want[i].End || string(got[i].Centroid) != string(want[i].Centroid) {
			t.Errorf("episode %d differs after rebuild", i)
		}
	}
	if got[0].SummarySeq != 77 {
		t.Errorf("summary for span [1,2] was not re-linked: %d", got[0].SummarySeq)
	}
	// A message with no vector yet is skipped, not mis-assigned.
	partial := recall.Rebuild(p, l.events, vecs[:4], nil)
	if partial.LastIndexed() != 4 {
		t.Errorf("rebuild with 4 vectors indexed up to %d, want 4", partial.LastIndexed())
	}
}

// TestSnapshotsAreImmutable hammers the single-writer / many-readers contract
// under the race detector: the actor keeps indexing while turn goroutines
// read snapshots taken earlier.
func TestSnapshotsAreImmutable(t *testing.T) {
	t.Parallel()
	l, x := new(memLog), mustIndex(t, recall.DefaultParams(), nil)
	q := fakeinfer.Embed("topic 3 words")
	var wg sync.WaitGroup
	for i := range 400 {
		say(l, x, int64(i)*day, fmt.Sprintf("topic %d words", i))
		if i%7 == 0 {
			x.SetSummary(uint32(i/2), uint64(i))
		}
		snap := x.Snapshot()
		wantLen := snap.Len()
		wg.Go(func() {
			for range 20 {
				snap.TopK(q, 5, math.MaxUint64)
			}
			if snap.Len() != wantLen {
				t.Errorf("snapshot length changed from %d to %d", wantLen, snap.Len())
			}
		})
	}
	wg.Wait()
}

func TestQueryText(t *testing.T) {
	t.Parallel()
	got := recall.QueryText([]string{"ok do that again"}, "I reran the postgres migration.", 128)
	if !strings.Contains(got, "postgres") || !strings.HasSuffix(got, "ok do that again") {
		t.Errorf("query = %q, want the previous reply followed by the new message", got)
	}
	long := strings.Repeat("word ", 400)
	if got := recall.QueryText([]string{long}, "previous", 128); len(got) > 128*4+4 {
		t.Errorf("query is %d bytes, over the ~%d budget", len(got), 128*4)
	}
}

func mustIndex(t *testing.T, p recall.Params, rows []core.EpisodeRow) *recall.Index {
	t.Helper()
	x, err := recall.NewIndex(p, rows)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

// BenchmarkStage1 measures the doc's claim that a brute-force scan of 5,000
// centroids stays under a millisecond.
func BenchmarkStage1(b *testing.B) {
	l, x := new(memLog), mustIndexB(b)
	for i := range 5000 {
		say(l, x, int64(i)*day, fmt.Sprintf("topic%d alpha%d beta%d", i, i%97, i%31))
	}
	snap := x.Snapshot()
	q := fakeinfer.Embed("topic42 alpha42 beta11")
	b.ReportAllocs()
	for b.Loop() {
		snap.TopK(q, 20, math.MaxUint64)
	}
}

func mustIndexB(b *testing.B) *recall.Index {
	b.Helper()
	x, err := recall.NewIndex(recall.DefaultParams(), nil)
	if err != nil {
		b.Fatal(err)
	}
	return x
}
