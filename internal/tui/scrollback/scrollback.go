// Package scrollback shows one screenful of a long list: a conversation as
// blocks of lines, of which only the blocks in view are ever drawn.
//
// A Source owns the blocks and draws one when asked. A Window remembers where
// the view is and keeps the blocks it drew lately. The view either follows
// the end of the list or is anchored: its top row is a row of a block named
// by key. An anchored view holds still while blocks are added above it, grow
// above it, or are drawn again at another width. The row is counted from the
// block's first line, so the lines in view also hold still when the gap
// above that block comes or goes.
//
// No call costs more than the blocks in view and the blocks a scroll passes
// over, however long the list is. A Window is not safe for concurrent use.
package scrollback

import "math"

// Key identifies a block for anchoring and caching. The window never orders
// keys; the Source finds them.
type Key struct {
	Kind uint8
	ID   uint64
	Sub  uint32
}

// Source is the list a Window shows. The window calls it during Sync,
// Scroll, Top and View, and never keeps it.
type Source interface {
	// Len is the number of blocks.
	Len() int
	// Key returns the key of block i. No two blocks share a key.
	Key(i int) Key
	// Rev returns a number that changes when block i's content does. A block
	// is drawn again only when its Rev differs from the one it was drawn at.
	Rev(i int) uint64
	// Gap is the number of blank lines above block i: 0 or 1. The window
	// draws them as told, above the first block too. It may change without
	// the block's Rev changing, as when older blocks arrive above a block
	// that was the first.
	Gap(i int) int
	// Index finds a block by its key, in constant time.
	Index(k Key) (int, bool)
	// Lines draws block i at a width, one string for each screen row. It is
	// called only for blocks in or entering the window. The window does not
	// wrap or measure the lines, and it keeps the slice: do not change it
	// afterwards. A block should have at least one line; one without takes
	// only its gap, and still counts as a block when the window decides
	// whether it has reached either end.
	Lines(i, width int) []string
}

// Stats counts what a Window has asked of its sources since it was made.
type Stats struct {
	// Rendered counts calls to Source.Lines.
	Rendered int
	// Hits counts the times a block was taken from the cache instead. One
	// View may take the same block more than once.
	Hits int
}

// Window is a view of a Source: so many rows, at some width, either
// following the end or anchored on a block. Make one with New.
type Window struct {
	width, height int
	following     bool
	anchor        anchor
	// tail is the turn in flight. It is drawn below the last block, as given.
	tail  []string
	cache cache

	// first and last are the blocks the last View showed a line of.
	first, last int
	shown       bool
}

// pos is a row of the content: row off of block idx, the block's gap rows
// counted first. An idx equal to the number of blocks means the tail.
type pos struct{ idx, off int }

// span is a run of blocks, when ok.
type span struct {
	first, last int
	ok          bool
}

// New returns an empty window that follows the end. It keeps the blocks in
// view, and beyond them at most maxCachedLines lines of blocks drawn before.
// 4000 is the client's value.
func New(maxCachedLines int) *Window {
	return &Window{
		following: true,
		cache:     cache{entries: make(map[Key]*entry), limit: max(maxCachedLines, 0), gen: 1},
	}
}

// Resize sets the size of the view. A new width forgets every drawn block; a
// new height forgets nothing. An anchored view keeps its top row and a
// following one its bottom row.
func (w *Window) Resize(width, height int) {
	width, height = max(width, 0), max(height, 0)
	if width != w.width {
		w.cache.clear()
	}
	w.width, w.height = width, height
}

// Sync re-finds the anchor after the source's list changed. It renders
// nothing by itself. Scroll and View do the same before they start, so
// calling Sync matters only when the list changes more than once between
// them.
func (w *Window) Sync(src Source) { w.resolve(src) }

// SetTail sets the lines of the turn in flight, which are drawn below the
// last block exactly as given. The window keeps the slice.
func (w *Window) SetTail(lines []string) { w.tail = lines }

// Scroll moves the view by delta rows; negative is up. It stops at either
// end. A view that shows the last row follows from then on. hitTop reports
// that the scroll was upward and the view now starts at the first row there
// is, whether or not it moved: the moment to ask for older history. A window
// with no rows does not scroll.
//
// It draws the blocks that enter the view, and every block a longer scroll
// passes over. Use Top and Follow to jump to an end.
func (w *Window) Scroll(src Source, delta int) (hitTop bool) {
	if w.height == 0 {
		return false // nothing is in view, so nothing can enter it
	}
	p, _, _ := w.settle(src)
	if delta == 0 {
		return false
	}
	w.cache.loose = true
	if delta < 0 {
		rows := -delta
		if rows < 0 {
			rows = math.MaxInt // -delta overflowed
		}
		p, hitTop = w.up(src, p, rows)
	} else {
		p = w.down(src, p, delta)
	}
	w.cache.loose = false

	// Drawing the new view tells whether it shows the end, and marks the
	// blocks now in view as the ones to keep.
	w.cache.begin()
	out, _, end := w.draw(src, p)
	w.following = len(out) < w.height || end
	if !w.following {
		w.record(src, p)
	}
	w.cache.trim(nil)
	return hitTop
}

// Top moves the view to the first row there is. It renders nothing. A list
// short enough to show whole is still followed.
func (w *Window) Top(src Source) {
	w.following = false
	w.record(src, pos{})
}

// Follow makes the view stick to the bottom.
func (w *Window) Follow() { w.following = true }

// Following reports whether the view sticks to the bottom. It becomes true
// by itself when a scroll, a resize or a change to the list leaves the last
// row in view.
func (w *Window) Following() bool { return w.following }

// View returns the rows in view: exactly height lines, content at the top
// and empty lines under it when there is less than a screenful. The slice is
// the caller's.
func (w *Window) View(src Source) []string {
	w.cache.begin()
	if w.height == 0 {
		w.first, w.last, w.shown = 0, 0, false
		w.cache.trim(nil)
		return []string{}
	}
	p, out, vis := w.settle(src)
	if !w.following {
		w.record(src, p)
	}
	w.first, w.last, w.shown = vis.first, vis.last, vis.ok
	w.cache.trim(nil)
	for len(out) < w.height {
		out = append(out, "")
	}
	return out
}

// Visible returns the first and last block the last View showed a line of.
// A block of which only the gap is in view does not count. ok is false when
// no block was in view.
func (w *Window) Visible() (first, last int, ok bool) { return w.first, w.last, w.shown }

// Invalidate forgets what was drawn for one block, so that it is drawn again
// when next in view. A block whose Rev changed needs no call.
func (w *Window) Invalidate(k Key) {
	if e := w.cache.entries[k]; e != nil {
		w.cache.drop(e)
	}
}

// InvalidateAll forgets every drawn block. Call it when something every
// block depends on changed: the theme, or how much of a block is shown.
// Resize does it for a new width.
func (w *Window) InvalidateAll() { w.cache.clear() }

// Stats returns the counts so far.
func (w *Window) Stats() Stats { return w.cache.stats }

// settle works out the view as it is now: the row it starts at, its rows,
// and the blocks in it. An anchored view that turns out to show the last row
// becomes a following one here.
func (w *Window) settle(src Source) (pos, []string, span) {
	if !w.following {
		p := w.locate(src)
		if !w.following {
			out, vis, end := w.draw(src, p)
			if len(out) == w.height && !end {
				return p, out, vis
			}
			w.following = true
			if len(out) == w.height {
				return p, out, vis // the anchored view ends exactly at the end
			}
		}
	}
	p, _ := w.up(src, pos{idx: src.Len(), off: len(w.tail)}, w.height)
	out, vis, _ := w.draw(src, p)
	return p, out, vis
}

// draw collects the rows from p down, at most a screenful. It reports the
// blocks it took a line from, and whether the rows reach the end of the
// content. It asks for a block's lines only when one of them is in view.
func (w *Window) draw(src Source, p pos) (out []string, vis span, end bool) {
	n := src.Len()
	out = make([]string, 0, w.height)
	i, off := min(p.idx, n), p.off
	for i < n && len(out) < w.height {
		gap := gapOf(src, i)
		for ; off < gap && len(out) < w.height; off++ {
			out = append(out, "")
		}
		if len(out) == w.height {
			return out, vis, false // block i's lines start below the view
		}
		lines := w.cache.lines(src, i, w.width)
		if at := off - gap; at < len(lines) {
			take := min(len(lines)-at, w.height-len(out))
			out = append(out, lines[at:at+take]...)
			if !vis.ok {
				vis.first, vis.ok = i, true
			}
			vis.last = i
			if at+take < len(lines) {
				return out, vis, false
			}
		}
		i, off = i+1, 0
	}
	if i < n {
		return out, vis, false
	}
	at := min(off, len(w.tail))
	take := min(len(w.tail)-at, w.height-len(out))
	out = append(out, w.tail[at:at+take]...)
	return out, vis, at+take == len(w.tail)
}

// up moves p up by k rows and stops at the first row there is. atStart
// reports that it ended there.
func (w *Window) up(src Source, p pos, k int) (_ pos, atStart bool) {
	for k > p.off {
		k -= p.off
		// The block above, passing over any that take no rows at all.
		j, rows := p.idx-1, 0
		for ; j >= 0; j-- {
			if rows = w.rows(src, j); rows > 0 {
				break
			}
		}
		if j < 0 {
			return pos{idx: p.idx}, true
		}
		p = pos{idx: j, off: rows}
	}
	p.off -= k
	return p, p.idx == 0 && p.off == 0
}

// down moves p down by k rows and stops just past the last row there is.
func (w *Window) down(src Source, p pos, k int) pos {
	n := src.Len()
	if p.idx > n {
		p = pos{idx: n}
	}
	for p.idx < n {
		if k == 0 {
			return p // without drawing a block the view may not reach
		}
		left := max(w.rows(src, p.idx)-p.off, 0)
		if k < left {
			p.off += k
			return p
		}
		k -= left
		p = pos{idx: p.idx + 1}
	}
	if left := max(len(w.tail)-p.off, 0); k < left {
		p.off += k
	} else {
		p.off = len(w.tail)
	}
	return p
}

// rows is how many rows block i takes: its gap and its lines.
func (w *Window) rows(src Source, i int) int {
	return gapOf(src, i) + len(w.cache.lines(src, i, w.width))
}

func gapOf(src Source, i int) int { return max(src.Gap(i), 0) }
