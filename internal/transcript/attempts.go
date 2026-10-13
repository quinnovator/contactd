package transcript

import (
	"cmp"
	"slices"

	"github.com/quinnovator/contactd/internal/core"
)

// maxAttempts is how many model calls a transcript remembers.
const maxAttempts = 50

// Attempt is the record of one model call a turn made, with where and when
// the log put it.
type Attempt struct {
	core.AttemptBody

	// Seq is the event's sequence number.
	Seq uint64
	TS  int64 // unix ms
}

// Attempts returns the model calls the contact's turns made, oldest first by
// sequence number. Only a turn's own calls are here: a checkpoint or a
// summary is a side job. The newest 50 are kept. The slice is the
// transcript's own; do not modify it. What it holds does not change once
// returned: ask again to see the calls filed since.
func (t *Transcript) Attempts() []Attempt { return t.attempts }

// Attempt returns the record of the newest model call a turn made, and
// whether there is one.
func (t *Transcript) Attempt() (core.AttemptBody, bool) {
	if len(t.attempts) == 0 {
		return core.AttemptBody{}, false
	}
	return t.attempts[len(t.attempts)-1].AttemptBody, true
}

// attempted files a model call in sequence order. Calls arrive in any order
// and more than once, so what is kept is always the newest of all seen: one
// already held is left alone, and one older than a full list is dropped.
func (t *Transcript) attempted(a Attempt) {
	if a.Seq == 0 {
		return // not an event of the log
	}
	i, found := slices.BinarySearchFunc(t.attempts, a.Seq, func(have Attempt, seq uint64) int {
		return cmp.Compare(have.Seq, seq)
	})
	if found {
		return
	}
	keep := t.attempts
	if len(keep) == maxAttempts {
		if i == 0 {
			return
		}
		// Full: the oldest makes room.
		keep, i = keep[1:], i-1
	} else if i == len(keep) {
		// The usual case. A slice handed out earlier is shorter than this
		// one and does not see the new call.
		t.attempts = append(keep, a)
		return
	}
	// Anything else would move calls a caller may still be reading, so the
	// list is built again.
	next := make([]Attempt, 0, maxAttempts)
	next = append(next, keep[:i]...)
	next = append(next, a)
	t.attempts = append(next, keep[i:]...)
}
