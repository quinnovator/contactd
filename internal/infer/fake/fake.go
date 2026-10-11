// Package fake provides deterministic stand-ins for the embedder and the
// reranker. They behave plausibly rather than randomly: texts that share
// words get nearby vectors and higher rerank scores, so recall, segmentation
// and the benchmarks produce meaningful results without ONNX Runtime.
package fake

import (
	"context"
	"math"
	"strings"
	"unicode"

	"github.com/cespare/xxhash/v2"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/infer"
)

// Embedder is a bag-of-words embedder: every word hashes to a fixed
// pseudo-random direction, and a text is the normalized sum of its words.
type Embedder struct{}

// Run implements infer.Pipeline.
func (Embedder) Run(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = Embed(t)
	}
	return out, nil
}

// Embed returns the unit vector for one text.
func Embed(text string) []float32 {
	v := make([]float32, core.VectorDim)
	for _, w := range words(text) {
		weight := float32(1)
		if isStopword(w) {
			weight = 0.05 // function words carry almost no topic
		}
		state := xxhash.Sum64String(w)
		for i := range v {
			state = splitmix(state)
			// Map the top 24 bits to [-1, 1).
			v[i] += weight * (float32(state>>40)/float32(1<<23) - 1)
		}
	}
	var norm float64
	for _, f := range v {
		norm += float64(f) * float64(f)
	}
	if norm == 0 {
		v[0] = 1 // empty text still needs a unit vector
		return v
	}
	inv := float32(1 / math.Sqrt(norm))
	for i := range v {
		v[i] *= inv
	}
	return v
}

// Reranker scores a pair by how many distinct content words of the query the
// passage contains, mapped onto a logit-like range of [-6, 6]: no overlap is
// clearly negative, two shared words are just positive.
type Reranker struct{}

// Run implements infer.Pipeline.
func (Reranker) Run(_ context.Context, pairs []infer.Pair) ([]float32, error) {
	out := make([]float32, len(pairs))
	for i, p := range pairs {
		out[i] = Score(p.Query, p.Passage)
	}
	return out, nil
}

// Score returns the fake relevance logit for one pair.
func Score(query, passage string) float32 {
	have := make(map[string]struct{})
	for _, w := range words(passage) {
		have[w] = struct{}{}
	}
	seen := make(map[string]struct{})
	hits := 0
	for _, w := range words(query) {
		// Short words are mostly glue; only content words count.
		if len(w) < 4 || isStopword(w) {
			continue
		}
		if _, dup := seen[w]; dup {
			continue
		}
		seen[w] = struct{}{}
		if _, ok := have[w]; ok {
			hits++
		}
	}
	return min(2.5*float32(hits)-4, 6)
}

func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func isStopword(w string) bool {
	switch w {
	case "a", "an", "the", "and", "or", "but", "of", "to", "in", "on", "at", "for", "with",
		"is", "are", "was", "were", "be", "been", "it", "its", "this", "that", "these", "those",
		"i", "you", "we", "they", "he", "she", "me", "my", "your", "our", "do", "does", "did",
		"can", "could", "would", "should", "will", "have", "has", "had", "so", "as", "if",
		"not", "no", "yes", "ok", "what", "how", "when", "where", "why", "about", "from",
		"back", "again", "thing", "just", "also", "then", "there", "here", "us", "by":
		return true
	default:
		return len(w) <= 1
	}
}

func splitmix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}
