//go:build cgo && ORT

package hugot

import (
	"context"
	"fmt"
	"math"

	"github.com/knights-analytics/hugot/backends"

	"github.com/quinnovator/contactd/internal/core"
)

// pooling is how token embeddings become one sentence vector.
type pooling uint8

const (
	poolCLS  pooling = iota // the [CLS] token's vector: bge
	poolMean                // attention-masked mean over tokens: MiniLM
)

func parsePooling(s string) (pooling, error) {
	switch s {
	case "", "cls":
		return poolCLS, nil
	case "mean":
		return poolMean, nil
	default:
		return 0, fmt.Errorf("hugot: unknown pooling %q (want \"cls\" or \"mean\")", s)
	}
}

type embedder struct{ m *Models }

// Run implements infer.Pipeline. Texts longer than the model's window are
// truncated.
func (e embedder) Run(ctx context.Context, in []string) ([][]float32, error) {
	out := make([][]float32, 0, len(in))
	model := &e.m.embedder
	err := e.m.chunks(ctx, len(in), func(lo, hi int) error {
		batch := backends.NewBatch(hi - lo)
		backends.TokenizeInputs(batch, model.hf.Tokenizer, in[lo:hi])
		return model.forward(batch, func(data []float32, shape []int64) error {
			if len(shape) != 3 || int(shape[0]) != hi-lo || shape[2] != core.VectorDim {
				return fmt.Errorf("output shape %v, want [%d seq %d]", shape, hi-lo, core.VectorDim)
			}
			seq, dim := int(shape[1]), int(shape[2])
			for i := range hi - lo {
				tokens := data[i*seq*dim : (i+1)*seq*dim]
				v := make([]float32, dim)
				switch e.m.pooling {
				case poolCLS:
					copy(v, tokens[:dim])
				case poolMean:
					meanPool(v, tokens, batch.Input[i].AttentionMask)
				}
				normalize(v)
				out = append(out, v)
			}
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("hugot embed: %w", err)
	}
	return out, nil
}

// meanPool averages into v the token vectors whose mask entry is set. tokens
// is one input's [seq, dim] block; positions past the mask are batch padding.
func meanPool(v, tokens []float32, mask []uint32) {
	dim := len(v)
	n := 0
	for j, attended := range mask {
		if attended == 0 || (j+1)*dim > len(tokens) {
			continue
		}
		for k, x := range tokens[j*dim : (j+1)*dim] {
			v[k] += x
		}
		n++
	}
	if n == 0 {
		return
	}
	for k := range v {
		v[k] /= float32(n)
	}
}

// normalize scales v to unit L2 length, so a dot product is a cosine.
func normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(sum))
	for k := range v {
		v[k] *= inv
	}
}
