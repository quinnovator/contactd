package scrollback

// minHalved is the least height the half-view rule of resolve counts a view
// as having.
const minHalved = 4

// anchor is where an anchored view starts, in terms that survive the list
// changing: a block's key and a row within it.
type anchor struct {
	// key is the block holding the top row. For a tail anchor it is the last
	// block instead.
	key Key
	// idx is where key was last found.
	idx int
	// off is the top row counted from the block's first line: below zero it
	// is that many rows above the line, in the block's gap or in the blocks
	// before it. Counting from the line and not from the gap keeps the lines
	// where they were when the block's gap comes or goes, as it does for a
	// block that stops being the first. For a tail anchor off is the top row
	// within the tail.
	off int
	// tail says the top row is in the turn in flight, off rows below the
	// last block. Blocks that settle under that block later are counted the
	// same way, so the view keeps its place when a reply becomes a block.
	tail bool
	// bare marks a tail anchor taken when there were no blocks at all.
	bare bool

	// under is the block below key's, rem the rows of key's block from the
	// top row down, and underGap the gap under's block had. They are set only
	// when paired is. A rem of zero means the view was blank rows of the gap
	// above a block not yet drawn, whose height is therefore not known.
	under    Key
	rem      int
	underGap int
	paired   bool
}

// record anchors the view at p. When the block there has a block under it,
// the pair is remembered, so that resolve can tell when something comes
// between them.
func (w *Window) record(src Source, p pos) {
	n := src.Len()
	a := anchor{off: p.off}
	switch {
	case p.idx < n:
		gap := gapOf(src, p.idx)
		a.key, a.idx, a.off = src.Key(p.idx), p.idx, p.off-gap
		if p.idx+1 < n {
			lines, drawn := w.cache.peek(src, p.idx)
			switch rows := gap + lines; {
			case drawn && rows > p.off:
				a.rem, a.paired = rows-p.off, true
			case !drawn && w.height <= gap-p.off:
				// Every row of the view is a blank row above the block, whose
				// height is not known. All there is to keep is the place in
				// the list.
				a.paired = true
			}
			if a.paired {
				a.under, a.underGap = src.Key(p.idx+1), gapOf(src, p.idx+1)
			}
		}
	case n > 0:
		a.tail, a.key, a.idx = true, src.Key(n-1), n-1
	default:
		a.tail, a.bare = true, true
	}
	w.anchor = a
}

// resolve finds the anchor in the list as it is now. It draws nothing.
//
// Three cases need more than a lookup. When blocks have come between the
// anchor's block and the one that was under it, and the anchor's block takes
// no more than half the view, the view is anchored on the block under it
// instead: that keeps more rows where they were. It is what happens when a
// day's rule stays above older messages of the same day. A block that shows
// two rows or fewer always gives way, so that a rule does in a view of a row
// or two as well: staying with it would lose the place by a page. When the
// anchor's block is gone and the one under it is not, the view is anchored
// there the same way. When both are gone, the view stays at the same place
// in the list.
//
// A view anchored on the block under keeps that block's first line on the
// screen row it had, whatever became of its gap.
func (w *Window) resolve(src Source) {
	if w.following {
		return
	}
	a := &w.anchor
	n := src.Len()
	if a.bare {
		return
	}
	i, found := find(src, a.key, n)
	if a.paired {
		j, foundUnder := find(src, a.under, n)
		between := found && foundUnder && j > i+1 && 2*a.rem <= max(w.height, minHalved)
		if foundUnder && (!found || between) {
			*a = anchor{key: a.under, idx: j, off: -(a.rem + a.underGap)}
			return
		}
		a.paired = found && foundUnder && j == i+1
	}
	switch {
	case found:
		a.idx = i
	case a.tail && n == 0:
		a.bare = true
	case n == 0:
		w.following = true
	default:
		a.idx = min(a.idx, n-1)
		a.key = src.Key(a.idx)
		if !a.tail {
			a.off = -gapOf(src, a.idx) // the block's first row
		}
	}
}

// locate turns the anchor into a row of the content as it is now. It may
// find that there is nothing to anchor on, and set the view following.
func (w *Window) locate(src Source) pos {
	w.resolve(src)
	a := w.anchor
	switch {
	case w.following:
		return pos{}
	case a.tail:
		// The blocks after the anchor's are what the tail has turned into.
		// Passing over them is not seeing them.
		start := 0
		if !a.bare {
			start = a.idx + 1
		}
		w.cache.loose = true
		p := w.down(src, pos{idx: start}, a.off)
		w.cache.loose = false
		return p
	}
	gap := gapOf(src, a.idx)
	switch off := a.off + gap; {
	case off < 0:
		// The top row is above the block: the block under the anchor's took
		// over, or the block's gap went. The rows above were counted before
		// the list changed, and perhaps at another size: not every block
		// passed need be in view.
		w.cache.loose = true
		p, _ := w.up(src, pos{idx: a.idx}, -off)
		w.cache.loose = false
		return p
	case off < gap:
		return pos{idx: a.idx, off: off}
	default:
		// A block that is shorter than it was keeps its last row at the top.
		rows := w.rows(src, a.idx)
		return pos{idx: a.idx, off: max(min(off, rows-1), 0)}
	}
}

// find looks a key up and checks the answer is a block.
func find(src Source, k Key, n int) (int, bool) {
	i, ok := src.Index(k)
	return i, ok && i >= 0 && i < n
}
