package recall

import (
	"context"

	"github.com/quinnovator/contactd/internal/core"
)

// Priority orders work inside the inference micro-batchers.
type Priority uint8

const (
	// Critical is the turn's own work: the query embedding and the rerank.
	Critical Priority = iota
	// Background is indexing of already-appended messages.
	Background
)

// Embedder turns texts into unit-length core.VectorDim vectors.
type Embedder interface {
	Embed(ctx context.Context, texts []string, p Priority) ([][]float32, error)
}

// Reranker scores each (query, passage) pair jointly. Higher is more
// relevant; scores are logits, not probabilities.
type Reranker interface {
	Score(ctx context.Context, query string, passages []string) ([]float32, error)
}

// EventReader reads a contact's history back for passages and excerpts.
type EventReader interface {
	// EventsRange returns up to limit events with from <= seq <= to, in
	// ascending order.
	EventsRange(ctx context.Context, contact core.ContactID, from, to uint64, limit int) ([]core.Event, error)
	// Event returns the event at exactly seq.
	Event(ctx context.Context, contact core.ContactID, seq uint64) (core.Event, error)
}
