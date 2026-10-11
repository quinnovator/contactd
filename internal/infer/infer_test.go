package infer_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/infer"
	"github.com/quinnovator/contactd/internal/infer/fake"
	"github.com/quinnovator/contactd/internal/recall"
)

// recorder is a pipeline that records the size and order of every batch.
type recorder struct {
	mu      sync.Mutex
	batches [][]string
	gate    chan struct{} // when set, each Run waits for a token
	entered chan struct{} // when set, signalled as each Run begins
	err     error
}

func (r *recorder) Run(ctx context.Context, in []string) ([]string, error) {
	if r.entered != nil {
		r.entered <- struct{}{}
	}
	if r.gate != nil {
		select {
		case <-r.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	r.mu.Lock()
	r.batches = append(r.batches, append([]string(nil), in...))
	r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = "out:" + s
	}
	return out, nil
}

func (r *recorder) snapshot() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.batches...)
}

func start[In, Out any](t *testing.T, name string, p infer.Pipeline[In, Out], workers int) *infer.Batcher[In, Out] {
	t.Helper()
	b := infer.NewBatcher(name, p, workers)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return b
}

func TestBatcherReturnsOutputsInOrder(t *testing.T) {
	t.Parallel()
	b := start(t, "order", &recorder{}, 3)
	// 75 inputs span three model calls; the caller still gets them in order.
	in := make([]string, 75)
	for i := range in {
		in[i] = strconv.Itoa(i)
	}
	out, err := b.Do(t.Context(), in, recall.Critical)
	if err != nil {
		t.Fatal(err)
	}
	for i := range in {
		if out[i] != "out:"+in[i] {
			t.Fatalf("out[%d] = %q", i, out[i])
		}
	}
	if out, err := b.Do(t.Context(), nil, recall.Critical); err != nil || out != nil {
		t.Errorf("empty input: %v, %v", out, err)
	}
}

// TestBatcherCoalescesAndPrioritizes queues work behind a blocked worker and
// checks the two properties of the drain: queued requests share one model
// call, capped at MaxBatch, and critical work runs before background work.
func TestBatcherCoalescesAndPrioritizes(t *testing.T) {
	t.Parallel()
	rec := &recorder{gate: make(chan struct{}), entered: make(chan struct{}, 8)}
	b := start(t, "drain", rec, 1)

	var wg sync.WaitGroup
	submit := func(text string, p recall.Priority) {
		wg.Go(func() {
			if _, err := b.Do(t.Context(), []string{text}, p); err != nil {
				t.Errorf("Do(%s): %v", text, err)
			}
		})
	}
	// The first request occupies the only worker.
	submit("first", recall.Critical)
	<-rec.entered

	for i := range 40 {
		submit(fmt.Sprintf("bg%d", i), recall.Background)
	}
	waitFor(t, func() bool { return queued(b) == 40 })
	for i := range 5 {
		submit(fmt.Sprintf("crit%d", i), recall.Critical)
	}
	waitFor(t, func() bool { return queued(b) == 45 })

	close(rec.gate)
	wg.Wait()

	batches := rec.snapshot()
	if len(batches) != 3 {
		t.Fatalf("%d model calls for 46 inputs, want 3 (1 + 32 + 13)", len(batches))
	}
	if len(batches[1]) != infer.MaxBatch {
		t.Errorf("second batch has %d inputs, want the cap of %d", len(batches[1]), infer.MaxBatch)
	}
	for i := range 5 {
		if got := batches[1][i]; len(got) < 4 || got[:4] != "crit" {
			t.Errorf("batch position %d is %q; critical requests must be drained first", i, got)
		}
	}
}

// queued reports how many requests are waiting, through the same gauge an
// operator would watch.
func queued[In, Out any](b *infer.Batcher[In, Out]) int { return b.Queued() }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in time")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBatcherSkipsCancelledCallers(t *testing.T) {
	t.Parallel()
	rec := &recorder{gate: make(chan struct{}), entered: make(chan struct{}, 8)}
	b := start(t, "cancel", rec, 1)

	first := make(chan struct{})
	go func() {
		defer close(first)
		_, _ = b.Do(context.Background(), []string{"first"}, recall.Critical)
	}()
	<-rec.entered

	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() {
		_, err := b.Do(ctx, []string{"abandoned"}, recall.Critical)
		errc <- err
	}()
	waitFor(t, func() bool { return queued(b) == 1 })
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller got %v", err)
	}

	close(rec.gate)
	<-first
	if _, err := b.Do(t.Context(), []string{"after"}, recall.Critical); err != nil {
		t.Fatal(err)
	}
	for _, batch := range rec.snapshot() {
		for _, s := range batch {
			if s == "abandoned" {
				t.Error("a model pass was spent on a caller that had already left")
			}
		}
	}
}

func TestBatcherPropagatesPipelineErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("model crashed")
	b := start(t, "err", &recorder{err: boom}, 2)
	if _, err := b.Do(t.Context(), []string{"a", "b"}, recall.Background); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the pipeline's error", err)
	}
}

func TestEmbedderAndRerankerAdapters(t *testing.T) {
	t.Parallel()
	emb := infer.NewEmbedder(start(t, "embed", fake.Embedder{}, 2))
	rr := infer.NewReranker(start(t, "rerank", fake.Reranker{}, 2))

	var calls atomic.Int64
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			vecs, err := emb.Embed(t.Context(), []string{"postgres migration", "postgres migration stuck", "sourdough starter"}, recall.Critical)
			if err != nil {
				t.Errorf("Embed: %v", err)
				return
			}
			calls.Add(1)
			for _, v := range vecs {
				if len(v) != core.VectorDim || math.Abs(norm(v)-1) > 1e-4 {
					t.Errorf("vector has %d dims and norm %v", len(v), norm(v))
				}
			}
			if near, far := dot(vecs[0], vecs[1]), dot(vecs[0], vecs[2]); near <= far {
				t.Errorf("related texts (%.3f) are not closer than unrelated ones (%.3f)", near, far)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 16 {
		t.Fatalf("%d of 16 concurrent embeds succeeded", calls.Load())
	}

	scores, err := rr.Score(t.Context(), "postgres migration", []string{"lisbon flights", "the postgres migration failed", "sourdough"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 3 || scores[1] <= scores[0] || scores[1] <= scores[2] {
		t.Errorf("scores = %v, want the relevant passage on top", scores)
	}
}

func norm(v []float32) float64 { return math.Sqrt(float64(dot(v, v))) }

func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// TestRerankIsSpreadAcrossWorkers checks that one shortlist becomes several
// concurrent model calls, one per worker, and still comes back in order.
func TestRerankIsSpreadAcrossWorkers(t *testing.T) {
	t.Parallel()
	const workers = 4
	p := &concurrency{release: make(chan struct{})}
	rr := infer.NewReranker(start(t, "spread", p, workers))

	passages := make([]string, 20)
	for i := range passages {
		passages[i] = strconv.Itoa(i)
	}
	done := make(chan []float32, 1)
	go func() {
		scores, err := rr.Score(t.Context(), "q", passages)
		if err != nil {
			t.Errorf("Score: %v", err)
		}
		done <- scores
	}()
	// All four workers must be inside the model at once before any returns.
	waitFor(t, func() bool { return p.inFlight.Load() == workers })
	close(p.release)
	scores := <-done
	if len(scores) != len(passages) {
		t.Fatalf("%d scores for %d passages", len(scores), len(passages))
	}
	for i, sc := range scores {
		if sc != float32(i) {
			t.Fatalf("score %d = %v; outputs are out of order", i, sc)
		}
	}
	if calls := p.calls.Load(); calls != workers {
		t.Errorf("%d model calls for 20 pairs on %d workers, want %d of 5 pairs each", calls, workers, workers)
	}
}

// concurrency is a cross-encoder stand-in that scores each pair with the
// number in its passage and reports how many calls are running at once.
type concurrency struct {
	inFlight atomic.Int64
	calls    atomic.Int64
	release  chan struct{}
}

func (c *concurrency) Run(ctx context.Context, in []infer.Pair) ([]float32, error) {
	c.inFlight.Add(1)
	c.calls.Add(1)
	defer c.inFlight.Add(-1)
	select {
	case <-c.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out := make([]float32, len(in))
	for i, p := range in {
		n, err := strconv.Atoi(p.Passage)
		if err != nil {
			return nil, err
		}
		out[i] = float32(n)
	}
	return out, nil
}
