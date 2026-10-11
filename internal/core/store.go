package core

import (
	"encoding/binary"
	"fmt"
	"math"
)

// The types in this file cross the storage port: domain packages build them
// and the event log persists them.

// EpisodeRow is a stored episode. Centroid is an opaque blob owned by the
// recall package.
type EpisodeRow struct {
	ID         uint32
	Start, End uint64 // seq span
	Centroid   []byte
	SummarySeq uint64 // 0 until summarized
}

// Vector is one message embedding.
type Vector struct {
	Seq uint64
	Vec []float32
}

// Write is one request to the group-commit writer. Any combination of the
// payload fields may be set; they are applied together in one transaction.
type Write struct {
	Contact ContactID
	Event   *Event      // appended to the log
	Vector  *Vector     // upserted for (Contact, Vector.Seq)
	Episode *EpisodeRow // upserted for (Contact, Episode.ID)
	Touch   int64       // when non-zero, the contact's last_active (unix ms)
	// Reset marks the point where a contact's writer starts over. After any
	// write for a contact fails, the log refuses that contact's later writes
	// (ErrWriteRefused) until a Reset arrives: a retiring actor submits one
	// as its last act, so nothing it queued can land behind its successor.
	Reset bool

	// Done, when set, is called exactly once with the commit result. It runs
	// on the writer goroutine and must not block.
	Done func(error)
}

// Hydrated is what an actor loads when a contact wakes up.
type Hydrated struct {
	Contact  Contact
	Tail     []Event      // the last events, ascending
	Episodes []EpisodeRow // ascending by ID
	NextSeq  uint64       // one past the highest seq in the log
}

// EncodeVector packs v as little-endian float32s.
func EncodeVector(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

// DecodeVector is the inverse of EncodeVector.
func DecodeVector(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("vector blob has %d bytes, not a multiple of 4", len(b))
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}
