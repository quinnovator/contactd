// Package helperboard reduces helper snapshots and live exchanges without I/O.
package helperboard

import (
	"cmp"
	"slices"

	"github.com/quinnovator/contactd/internal/core"
)

// Helper is one row of the helper strip.
type Helper struct {
	Handle                           uint64
	Contact                          core.ContactID
	Title, TaskID, State             string
	NextCheck, LastCheck, LastReport int64
	Checks, Reports                  int
	LastOutcome                      string
	// ended is when this session saw the helper finish. before marks one
	// that had already finished when the session first heard of it: it
	// never lingers in the strip.
	ended  int64
	before bool
}

// linger is how long a helper seen finishing stays in the strip, in ms.
const linger = 10 * 60 * 1000

// Wire is an ephemeral, already-redacted protocol exchange.
type (
	Wire struct {
		At                                       int64
		Handle                                   uint64
		Event, Method, Dir, State, Text, Payload string
	}
	// Board keeps current helpers and the newest 200 exchanges.
	Board struct {
		helpers map[uint64]Helper
		wire    []Wire
		now     int64
	}
)

// Reset replaces the snapshot while retaining this session's wire history.
// A helper that is finished in the snapshot lingers only when this session
// knew it unfinished: one that finished before the session heard of it is
// history, kept for Get but not shown in the strip.
func (b *Board) Reset(hs []core.HelperJSON) {
	old := b.helpers
	b.helpers = make(map[uint64]Helper, len(hs))
	for _, h := range hs {
		v := Helper{Handle: h.Handle, Contact: h.Contact, Title: h.Title, TaskID: h.TaskID, State: h.State, NextCheck: h.NextCheck, LastCheck: h.LastCheckAt, LastReport: h.LastReportAt, Checks: h.Checks, Reports: h.Reports, LastOutcome: h.LastOutcome}
		if terminal(h.State) {
			v.NextCheck = 0
			was, known := old[h.Handle]
			switch {
			case !known:
				v.before = true
			case terminal(was.State):
				v.ended, v.before = was.ended, was.before
			default:
				v.ended = b.now // it finished while the session was reconnecting
			}
		}
		b.helpers[h.Handle] = v
	}
}

// Tick advances the display clock, including terminal-row expiry.
func (b *Board) Tick(now int64) { b.now = now }

// Delta folds helper state and records protocol exchanges; notices never count twice.
// A delta changes only the fields it carries: a notice names its helper and
// nothing else. A delta without a state does not make a row for a helper the
// board does not know.
func (b *Board) Delta(d core.Delta, now int64) {
	b.now = now
	if d.Helper == nil || (d.Type != core.DeltaHelper && d.Type != core.DeltaNotice) {
		return
	}
	h := d.Helper
	if d.Type == core.DeltaHelper {
		b.wire = append(b.wire, Wire{At: now, Handle: h.Handle, Event: h.Event, Method: h.Method, Dir: h.Dir, State: h.State, Text: h.Text, Payload: h.Wire})
		if len(b.wire) > 200 {
			b.wire = slices.Clone(b.wire[len(b.wire)-200:])
		}
	}
	old, known := b.helpers[h.Handle]
	if !known && h.State == "" {
		return
	}
	if b.helpers == nil {
		b.helpers = make(map[uint64]Helper)
	}
	v := old
	v.Handle = h.Handle
	if h.Title != "" {
		v.Title = h.Title
	}
	if h.Contact != 0 {
		v.Contact = h.Contact
	}
	if h.TaskID != "" {
		v.TaskID = h.TaskID
	}
	if h.State != "" {
		v.State = h.State
	}
	// A delta with a state is a whole row, where no next check means none.
	if h.State != "" || h.NextCheck != 0 {
		v.NextCheck = h.NextCheck
	}
	v.Checks = max(v.Checks, h.Checks)
	v.Reports = max(v.Reports, h.Reports)
	if h.Outcome != "" {
		if h.Outcome != v.LastOutcome || h.Checks > old.Checks {
			v.LastCheck = now
		}
		v.LastOutcome = h.Outcome
	}
	if h.Method == "artifactUpdate" {
		v.LastReport = now
	}
	if terminal(v.State) {
		v.NextCheck = 0
		if !known || !terminal(old.State) {
			v.ended, v.before = now, false
		}
	}
	b.helpers[h.Handle] = v
}

// Active returns live rows first, then rows this session saw end in the last
// ten minutes, in handle order.
func (b *Board) Active() []Helper {
	var out []Helper
	for _, h := range b.helpers {
		if !terminal(h.State) || (!h.before && b.now-h.ended < linger) {
			out = append(out, h)
		}
	}
	slices.SortFunc(out, func(a, c Helper) int {
		if terminal(a.State) != terminal(c.State) {
			if terminal(a.State) {
				return 1
			}
			return -1
		}
		return cmp.Compare(a.Handle, c.Handle)
	})
	return out
}

// Count returns the number of unfinished helpers.
func (b *Board) Count() int {
	n := 0
	for _, h := range b.helpers {
		if !terminal(h.State) {
			n++
		}
	}
	return n
}

// Wire returns exchanges oldest first; callers must not modify them.
func (b *Board) Wire() []Wire { return b.wire }

// Get returns one helper, including a terminal helper hidden from the strip.
func (b *Board) Get(handle uint64) (Helper, bool) { h, ok := b.helpers[handle]; return h, ok }

func terminal(s string) bool {
	switch s {
	case "TASK_STATE_COMPLETED", "TASK_STATE_CANCELED", "TASK_STATE_FAILED", "TASK_STATE_REJECTED":
		return true
	}
	return false
}
