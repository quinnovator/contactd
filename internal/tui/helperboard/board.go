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
	ended                            int64
}

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
func (b *Board) Reset(hs []core.HelperJSON) {
	old := b.helpers
	b.helpers = make(map[uint64]Helper, len(hs))
	for _, h := range hs {
		v := Helper{Handle: h.Handle, Contact: h.Contact, Title: h.Title, TaskID: h.TaskID, State: h.State, NextCheck: h.NextCheck, LastCheck: h.LastCheckAt, LastReport: h.LastReportAt, Checks: h.Checks, Reports: h.Reports, LastOutcome: h.LastOutcome}
		if terminal(h.State) {
			v.ended = old[h.Handle].ended
			if v.ended == 0 {
				v.ended = b.now
			}
		}
		b.helpers[h.Handle] = v
	}
}

// Tick advances the display clock, including terminal-row expiry.
func (b *Board) Tick(now int64) { b.now = now }

// Delta folds helper state and records protocol exchanges; notices never count twice.
func (b *Board) Delta(d core.Delta, now int64) {
	b.now = now
	if d.Helper == nil || (d.Type != core.DeltaHelper && d.Type != core.DeltaNotice) {
		return
	}
	h := d.Helper
	if b.helpers == nil {
		b.helpers = make(map[uint64]Helper)
	}
	v := b.helpers[h.Handle]
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
	v.NextCheck = h.NextCheck
	v.Checks = max(v.Checks, h.Checks)
	v.Reports = max(v.Reports, h.Reports)
	if h.Outcome != "" {
		if h.Outcome != v.LastOutcome || h.Checks > b.helpers[h.Handle].Checks {
			v.LastCheck = now
		}
		v.LastOutcome = h.Outcome
	}
	if h.Method == "artifactUpdate" {
		v.LastReport = now
	}
	if terminal(v.State) && v.ended == 0 {
		v.ended = now
	}
	b.helpers[h.Handle] = v
	if d.Type == core.DeltaHelper {
		b.wire = append(b.wire, Wire{At: now, Handle: h.Handle, Event: h.Event, Method: h.Method, Dir: h.Dir, State: h.State, Text: h.Text, Payload: h.Wire})
		if len(b.wire) > 200 {
			b.wire = slices.Clone(b.wire[len(b.wire)-200:])
		}
	}
}

// Active returns live rows first, then recently ended rows, in handle order.
func (b *Board) Active() []Helper {
	var out []Helper
	for _, h := range b.helpers {
		if !terminal(h.State) || b.now-h.ended < 600000 {
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
