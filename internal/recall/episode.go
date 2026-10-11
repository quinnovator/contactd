// Package recall segments a contact's messages into episodes as they are
// embedded, and recalls the relevant ones at the start of each turn in two
// stages: a cheap bi-encoder scan over every centroid, then a cross-encoder
// rerank of the shortlist (Piece 4). Nobody names a thread or picks one.
package recall

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/quinnovator/contactd/internal/core"
)

// Params are the recall knobs. The defaults are starting values, tuned with
// the recall benchmark (cmd/replay).
type Params struct {
	Tau       float32 // cosine to the running centroid below which a message has drifted
	GapMS     int64   // silence that opens a new episode
	Shortlist int     // stage-1 candidates handed to the reranker
	Theta     float32 // rerank score an episode must clear to be recalled
	Lambda    float32 // weight of the recency bonus
	HalfLife  int64   // ms; decay constant of the recency bonus
	// PassageTokens bounds each rerank passage, which bounds rerank cost.
	PassageTokens int
}

// DefaultParams returns the starting values from the design doc.
func DefaultParams() Params {
	return Params{
		Tau:           0.45,
		GapMS:         30 * 60 * 1000,
		Shortlist:     20,
		Theta:         0,
		Lambda:        1,
		HalfLife:      7 * 24 * 60 * 60 * 1000,
		PassageTokens: 256,
	}
}

// Episode is a run of consecutive messages about one thing. An Episode value
// is immutable once published: the index replaces it rather than editing it,
// so turn goroutines can read snapshots without locks.
type Episode struct {
	ID         uint32
	Start, End uint64               // seq span
	Centroid   [core.VectorDim]int8 // quantized mean; multiply by Scale to dequantize
	Scale      float32
	N          uint32 // members, for the running mean
	LastTS     int64
	SummarySeq uint64 // 0 until summarized lazily

	norm float32 // |Centroid| in int8 units, cached for cosine
}

// centroidBlob is the stored form of everything the episodes table has no
// column for: scale, N, lastTS, then the int8 centroid.
const centroidBlob = 4 + 4 + 8 + core.VectorDim

// Row returns the stored form of e.
func (e *Episode) Row() core.EpisodeRow {
	b := make([]byte, centroidBlob)
	binary.LittleEndian.PutUint32(b[0:], math.Float32bits(e.Scale))
	binary.LittleEndian.PutUint32(b[4:], e.N)
	binary.LittleEndian.PutUint64(b[8:], uint64(e.LastTS)) //nolint:gosec // unix ms, round-trips through the same cast
	for i, c := range e.Centroid {
		b[16+i] = byte(c) //nolint:gosec // int8 to its two's-complement byte
	}
	return core.EpisodeRow{ID: e.ID, Start: e.Start, End: e.End, Centroid: b, SummarySeq: e.SummarySeq}
}

// FromRow decodes a stored episode.
func FromRow(r core.EpisodeRow) (*Episode, error) {
	if len(r.Centroid) != centroidBlob {
		return nil, fmt.Errorf("episode %d: centroid blob has %d bytes, want %d", r.ID, len(r.Centroid), centroidBlob)
	}
	e := &Episode{
		ID: r.ID, Start: r.Start, End: r.End, SummarySeq: r.SummarySeq,
		Scale:  math.Float32frombits(binary.LittleEndian.Uint32(r.Centroid[0:])),
		N:      binary.LittleEndian.Uint32(r.Centroid[4:]),
		LastTS: int64(binary.LittleEndian.Uint64(r.Centroid[8:])), //nolint:gosec // see Row
	}
	for i := range e.Centroid {
		e.Centroid[i] = int8(r.Centroid[16+i]) //nolint:gosec // see Row
	}
	e.norm = int8Norm(&e.Centroid)
	return e, nil
}

// dequant returns the centroid at float precision.
func (e *Episode) dequant() []float32 {
	v := make([]float32, core.VectorDim)
	for i, c := range e.Centroid {
		v[i] = float32(c) * e.Scale
	}
	return v
}

// quantize packs v into int8 with a per-vector scale: 384 bytes instead of
// 1,536. That is safe because the cross-encoder re-scores the shortlist at
// full precision.
func quantize(v []float32) (q [core.VectorDim]int8, scale float32) {
	var maxAbs float32
	for _, f := range v {
		if a := float32(math.Abs(float64(f))); a > maxAbs {
			maxAbs = a
		}
	}
	if maxAbs == 0 {
		return q, 1
	}
	scale = maxAbs / 127
	for i, f := range v {
		q[i] = int8(math.RoundToEven(float64(f / scale)))
	}
	return q, scale
}

func int8Norm(q *[core.VectorDim]int8) float32 {
	var sum int32
	for _, c := range q {
		sum += int32(c) * int32(c)
	}
	return float32(math.Sqrt(float64(sum)))
}

func dotInt8(a, b *[core.VectorDim]int8) int32 {
	var sum int32
	for i := range a {
		sum += int32(a[i]) * int32(b[i])
	}
	return sum
}

func cosine(a, b []float32) float32 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / math.Sqrt(na*nb))
}
