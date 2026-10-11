//go:build cgo && ORT

package hugot

import (
	"context"
	"fmt"

	"github.com/knights-analytics/hugot/backends"

	"github.com/quinnovator/contactd/internal/infer"
)

type reranker struct{ m *Models }

// Run implements infer.Pipeline. Each pair is tokenized and scored on its
// own, so a batch may mix queries from several turns. The scores are the
// model's raw logits: the threshold and recency bonus downstream are
// calibrated on those, not on probabilities. Pairs longer than the model's
// window lose the end of the passage.
func (r reranker) Run(ctx context.Context, in []infer.Pair) ([]float32, error) {
	out := make([]float32, 0, len(in))
	model := &r.m.reranker
	err := r.m.chunks(ctx, len(in), func(lo, hi int) error {
		pairs := make([][2]string, hi-lo)
		for i, p := range in[lo:hi] {
			pairs[i] = [2]string{p.Query, p.Passage}
		}
		batch := backends.NewBatch(len(pairs))
		backends.TokenizeInputPairs(batch, model.hf.Tokenizer, pairs, model.hf.SeparatorToken)
		return model.forward(batch, func(data []float32, shape []int64) error {
			if len(shape) != 2 || int(shape[0]) != len(pairs) || shape[1] != 1 {
				return fmt.Errorf("output shape %v, want [%d 1]", shape, len(pairs))
			}
			out = append(out, data[:len(pairs)]...)
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("hugot rerank: %w", err)
	}
	return out, nil
}
