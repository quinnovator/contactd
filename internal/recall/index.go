package recall

import (
	"fmt"
	"slices"

	"github.com/quinnovator/contactd/internal/core"
)

// Index is one contact's episode index. It is owned by that contact's actor
// and is not safe for concurrent use; turn goroutines read Snapshots.
type Index struct {
	p Params
	// closed episodes never change again, except for a summary link, which
	// copies the slice. The backing array is shared with snapshots, so the
	// index only ever appends past a snapshot's length.
	closed []*Episode
	cur    *Episode
	// mean is the open episode's running mean at full precision. Keeping it
	// here rather than requantizing an int8 centroid on every message stops
	// rounding error from accumulating.
	mean        []float32
	lastDrifted bool
}

// NewIndex rebuilds the in-memory index from stored rows, which must be in ID
// order. The last row is the open episode.
func NewIndex(p Params, rows []core.EpisodeRow) (*Index, error) {
	x := &Index{p: p}
	for i, r := range rows {
		e, err := FromRow(r)
		if err != nil {
			return nil, err
		}
		if i > 0 && e.Start <= rows[i-1].End {
			return nil, fmt.Errorf("episode %d overlaps episode %d", r.ID, rows[i-1].ID)
		}
		if i == len(rows)-1 {
			x.cur, x.mean = e, e.dequant()
		} else {
			x.closed = append(x.closed, e)
		}
	}
	return x, nil
}

// LastIndexed returns the highest seq folded into the index, or 0.
func (x *Index) LastIndexed() uint64 {
	if x.cur == nil {
		return 0
	}
	return x.cur.End
}

// Len returns the number of episodes.
func (x *Index) Len() int {
	if x.cur == nil {
		return 0
	}
	return len(x.closed) + 1
}

// OnEmbedded folds one embedded message into the index and returns the
// episode that changed, to be upserted. Messages must arrive in seq order.
//
// A message opens a new episode after a long silence, or when it and the
// message before it both drifted from the running centroid. Requiring two in
// a row stops a single off-topic aside from splitting an episode.
func (x *Index) OnEmbedded(ev core.Event, v []float32) *Episode {
	if x.cur == nil {
		return x.open(ev, v)
	}
	drift := cosine(v, x.mean) < x.p.Tau
	if ev.TS-x.cur.LastTS > x.p.GapMS || (drift && x.lastDrifted) {
		x.lastDrifted = false
		return x.open(ev, v)
	}
	x.lastDrifted = drift
	return x.extend(ev, v)
}

func (x *Index) open(ev core.Event, v []float32) *Episode {
	id := uint32(0)
	if x.cur != nil {
		id = x.cur.ID + 1
		x.closed = append(x.closed, x.cur)
	}
	x.mean = slices.Clone(v)
	e := &Episode{ID: id, Start: ev.Seq, End: ev.Seq, N: 1, LastTS: ev.TS}
	e.Centroid, e.Scale = quantize(x.mean)
	e.norm = int8Norm(&e.Centroid)
	x.cur = e
	return e
}

func (x *Index) extend(ev core.Event, v []float32) *Episode {
	n := float32(x.cur.N + 1)
	for i := range x.mean {
		x.mean[i] += (v[i] - x.mean[i]) / n
	}
	e := *x.cur // copy: published snapshots keep the old value
	e.End, e.N, e.LastTS = ev.Seq, x.cur.N+1, ev.TS
	e.Centroid, e.Scale = quantize(x.mean)
	e.norm = int8Norm(&e.Centroid)
	x.cur = &e
	return x.cur
}

// SetSummary links a summary event to an episode and returns the changed
// episode, or nil if the episode is unknown.
func (x *Index) SetSummary(id uint32, seq uint64) *Episode {
	if x.cur != nil && x.cur.ID == id {
		e := *x.cur
		e.SummarySeq = seq
		x.cur = &e
		return x.cur
	}
	i, ok := slices.BinarySearchFunc(x.closed, id, func(e *Episode, id uint32) int {
		return int(e.ID) - int(id)
	})
	if !ok {
		return nil
	}
	e := *x.closed[i]
	e.SummarySeq = seq
	// Snapshots share the backing array, so replace the slice, not the slot.
	x.closed = slices.Clone(x.closed)
	x.closed[i] = &e
	return &e
}

// Snapshot returns an immutable view of the index. It costs two words: the
// closed episodes are shared, not copied.
func (x *Index) Snapshot() *Snapshot {
	return &Snapshot{closed: slices.Clip(x.closed), cur: x.cur}
}

// Rows returns every episode in stored form, for tests and rebuilds.
func (x *Index) Rows() []core.EpisodeRow {
	rows := make([]core.EpisodeRow, 0, x.Len())
	for _, e := range x.closed {
		rows = append(rows, e.Row())
	}
	if x.cur != nil {
		rows = append(rows, x.cur.Row())
	}
	return rows
}

// Rebuild replays embedded messages into a fresh index. The episodes table is
// derived data, so a corrupt or outdated index is fixed by replaying events
// and vectors. msgs must be in seq order; messages without a vector are
// skipped (they are embedded again later). summaries re-links any summary
// whose span still matches an episode exactly.
func Rebuild(p Params, msgs []core.Event, vecs []core.Vector, summaries map[[2]uint64]uint64) *Index {
	x := &Index{p: p}
	j := 0
	for _, ev := range msgs {
		for j < len(vecs) && vecs[j].Seq < ev.Seq {
			j++
		}
		if j == len(vecs) || vecs[j].Seq != ev.Seq {
			continue
		}
		x.OnEmbedded(ev, vecs[j].Vec)
	}
	link := func(e *Episode) {
		if seq, ok := summaries[[2]uint64{e.Start, e.End}]; ok {
			e.SummarySeq = seq // nothing has been published yet; in-place is safe
		}
	}
	for _, e := range x.closed {
		link(e)
	}
	if x.cur != nil {
		link(x.cur)
	}
	return x
}

// Snapshot is an immutable view of an Index, safe to read from any goroutine.
type Snapshot struct {
	closed []*Episode
	cur    *Episode
}

// Len returns the number of episodes in the snapshot.
func (s *Snapshot) Len() int {
	if s == nil || s.cur == nil {
		return 0
	}
	return len(s.closed) + 1
}

// Get returns the episode with the given ID, or nil.
func (s *Snapshot) Get(id uint32) *Episode {
	if s == nil {
		return nil
	}
	if s.cur != nil && s.cur.ID == id {
		return s.cur
	}
	i, ok := slices.BinarySearchFunc(s.closed, id, func(e *Episode, id uint32) int {
		return int(e.ID) - int(id)
	})
	if !ok {
		return nil
	}
	return s.closed[i]
}

// IsOpen reports whether id is the episode still being extended.
func (s *Snapshot) IsOpen(id uint32) bool {
	return s != nil && s.cur != nil && s.cur.ID == id
}

// Candidate is a stage-1 hit.
type Candidate struct {
	Ep     *Episode
	Stage1 float32 // approximate cosine to the query
}

// TopK is stage 1: the int8 dot product of the query against every centroid,
// keeping the best k. Episodes that reach into the tail (End >= tailStart)
// are skipped, since they are already in context. At 5,000 episodes x 384
// dims this is about 2M multiply-adds, so no ANN index is needed.
func (s *Snapshot) TopK(q []float32, k int, tailStart uint64) []Candidate {
	if s == nil || k <= 0 {
		return nil
	}
	q8, _ := quantize(q)
	qn := int8Norm(&q8)
	if qn == 0 {
		return nil
	}
	top := make([]Candidate, 0, k+1)
	consider := func(e *Episode) {
		if e.End >= tailStart || e.norm == 0 {
			return
		}
		score := float32(dotInt8(&q8, &e.Centroid)) / (qn * e.norm)
		if len(top) == k && score <= top[k-1].Stage1 {
			return
		}
		i, _ := slices.BinarySearchFunc(top, score, func(c Candidate, s float32) int {
			if c.Stage1 > s {
				return -1
			}
			return 1
		})
		top = slices.Insert(top, i, Candidate{Ep: e, Stage1: score})
		if len(top) > k {
			top = top[:k]
		}
	}
	for _, e := range s.closed {
		consider(e)
	}
	if s.cur != nil {
		consider(s.cur)
	}
	return top
}
