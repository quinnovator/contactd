// Package infer runs the two local models behind micro-batchers (Piece 3).
//
// A Batcher uses the same drain pattern as the event-log writer: block for
// the first request, take whatever else is already queued up to a cap, and
// run it all as one model call. Critical-path requests (the query embedding
// and the rerank) are always drained before background indexing.
package infer

import (
	"context"
	"fmt"
	"sync"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/recall"
)

// MaxBatch is the most inputs sent to a model in one call, Hugot's suggested
// starting point.
const MaxBatch = 32

// Pipeline runs one batch through a model. Run must be safe for concurrent
// use and must return exactly one output per input, in order.
type Pipeline[In, Out any] interface {
	Run(ctx context.Context, in []In) ([]Out, error)
}

// Pair is one cross-encoder input.
type Pair struct {
	Query   string
	Passage string
}

type result[Out any] struct {
	off int
	out []Out
	err error
}

type job[In, Out any] struct {
	in        []In
	off       int                // offset of in within the caller's inputs
	res       chan<- result[Out] // buffered by the caller; never blocks
	cancelled <-chan struct{}    // the caller's ctx.Done()
	// solo jobs are run as their own model call, never merged with others.
	// A caller that split its inputs to spread them across workers sets it;
	// merging the pieces back into one call would undo that.
	solo bool
}

// Batcher coalesces concurrent requests for one model.
type Batcher[In, Out any] struct {
	name       string
	pipeline   Pipeline[In, Out]
	workers    int
	critical   chan job[In, Out]
	background chan job[In, Out]
}

// NewBatcher builds a batcher with the given number of worker loops sharing
// one pipeline. name labels its metrics.
func NewBatcher[In, Out any](name string, p Pipeline[In, Out], workers int) *Batcher[In, Out] {
	return &Batcher[In, Out]{
		name:       name,
		pipeline:   p,
		workers:    max(workers, 1),
		critical:   make(chan job[In, Out], 256),
		background: make(chan job[In, Out], 1024),
	}
}

// Run starts the worker loops and blocks until ctx is cancelled and they have
// all returned.
func (b *Batcher[In, Out]) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range b.workers {
		wg.Go(func() { b.loop(ctx) })
	}
	wg.Wait()
}

// Do runs in through the model and returns one output per input.
func (b *Batcher[In, Out]) Do(ctx context.Context, in []In, p recall.Priority) ([]Out, error) {
	return b.do(ctx, in, p, MaxBatch, false)
}

// DoSpread is Do for one latency-critical request with many inputs. It
// splits them across the worker loops, each piece its own model call, so the
// request finishes in about a worker's share of the time instead of queueing
// behind itself on one thread.
func (b *Batcher[In, Out]) DoSpread(ctx context.Context, in []In, p recall.Priority) ([]Out, error) {
	// Pieces smaller than a handful of inputs cost more in call overhead
	// than they save.
	const minPiece = 4
	piece := max((len(in)+b.workers-1)/b.workers, minPiece)
	return b.do(ctx, in, p, min(piece, MaxBatch), true)
}

func (b *Batcher[In, Out]) do(ctx context.Context, in []In, p recall.Priority, size int, solo bool) ([]Out, error) {
	if len(in) == 0 {
		return nil, nil
	}
	queue := b.critical
	if p == recall.Background {
		queue = b.background
	}
	chunks := (len(in) + size - 1) / size
	res := make(chan result[Out], chunks)
	for off := 0; off < len(in); off += size {
		j := job[In, Out]{in: in[off:min(off+size, len(in))], off: off, res: res, cancelled: ctx.Done(), solo: solo}
		select {
		case queue <- j:
			b.observeDepth()
		case <-ctx.Done():
			return nil, fmt.Errorf("%s: enqueue: %w", b.name, ctx.Err())
		}
	}
	out := make([]Out, len(in))
	for range chunks {
		select {
		case r := <-res:
			if r.err != nil {
				return nil, fmt.Errorf("%s: %w", b.name, r.err)
			}
			copy(out[r.off:], r.out)
		case <-ctx.Done():
			return nil, fmt.Errorf("%s: %w", b.name, ctx.Err())
		}
	}
	return out, nil
}

func (b *Batcher[In, Out]) loop(ctx context.Context) {
	var (
		batch []job[In, Out]
		carry *job[In, Out] // a job that did not fit in the last batch
	)
	for {
		batch = batch[:0]
		n := 0
		if carry != nil {
			batch, n, carry = append(batch, *carry), len(carry.in), nil
		} else {
			var j job[In, Out]
			select { // critical first, then background
			case j = <-b.critical:
			default:
				select {
				case j = <-b.critical:
				case j = <-b.background:
				case <-ctx.Done():
					return
				}
			}
			batch, n = append(batch, j), len(j.in)
		}
	drain:
		for n < MaxBatch && !batch[0].solo {
			var j job[In, Out]
			select {
			case j = <-b.critical:
			default:
				select {
				case j = <-b.critical:
				case j = <-b.background:
				default:
					break drain
				}
			}
			if j.solo || n+len(j.in) > MaxBatch {
				carry = &j
				break
			}
			batch, n = append(batch, j), n+len(j.in)
		}
		b.observeDepth()
		b.run(ctx, batch)
	}
}

func (b *Batcher[In, Out]) run(ctx context.Context, batch []job[In, Out]) {
	flat := make([]In, 0, MaxBatch)
	live := batch[:0]
	for _, j := range batch {
		select {
		case <-j.cancelled: // the caller is gone; do not spend a model pass on it
			continue
		default:
		}
		live = append(live, j)
		flat = append(flat, j.in...)
	}
	if len(flat) == 0 {
		return
	}
	obs.InferBatchSize.WithLabelValues(b.name).Observe(float64(len(flat)))
	out, err := b.pipeline.Run(ctx, flat)
	if err == nil && len(out) != len(flat) {
		err = fmt.Errorf("pipeline returned %d outputs for %d inputs", len(out), len(flat))
	}
	pos := 0
	for _, j := range live {
		r := result[Out]{off: j.off, err: err}
		if err == nil {
			r.out = out[pos : pos+len(j.in)]
		}
		pos += len(j.in)
		j.res <- r
	}
}

// Queued returns the number of requests waiting in both queues.
func (b *Batcher[In, Out]) Queued() int { return len(b.critical) + len(b.background) }

func (b *Batcher[In, Out]) observeDepth() {
	obs.EmbedQueueDepth.WithLabelValues(b.name, "critical").Set(float64(len(b.critical)))
	obs.EmbedQueueDepth.WithLabelValues(b.name, "background").Set(float64(len(b.background)))
}

// Embedder adapts a batched embedding pipeline to recall.Embedder.
type Embedder struct {
	b *Batcher[string, []float32]
}

// NewEmbedder wraps b.
func NewEmbedder(b *Batcher[string, []float32]) *Embedder { return &Embedder{b: b} }

// Embed implements recall.Embedder.
func (e *Embedder) Embed(ctx context.Context, texts []string, p recall.Priority) ([][]float32, error) {
	vecs, err := e.b.Do(ctx, texts, p)
	if err != nil {
		return nil, err
	}
	for i, v := range vecs {
		if len(v) != core.VectorDim {
			return nil, fmt.Errorf("embedder returned %d dims for input %d, want %d", len(v), i, core.VectorDim)
		}
	}
	return vecs, nil
}

// Reranker adapts a batched cross-encoder pipeline to recall.Reranker.
type Reranker struct {
	b *Batcher[Pair, float32]
}

// NewReranker wraps b.
func NewReranker(b *Batcher[Pair, float32]) *Reranker { return &Reranker{b: b} }

// Score implements recall.Reranker. Reranking is always on the critical path,
// and it is the most expensive thing on it, so the shortlist is spread
// across the worker loops rather than scored as one call.
func (r *Reranker) Score(ctx context.Context, query string, passages []string) ([]float32, error) {
	pairs := make([]Pair, len(passages))
	for i, p := range passages {
		pairs[i] = Pair{Query: query, Passage: p}
	}
	return r.b.DoSpread(ctx, pairs, recall.Critical)
}
