package scrollback_test

import (
	"math"
	"slices"
	"strconv"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/scrollback"
)

// The list most tests use: 30 blocks of a blank row and 3 lines, so block b
// is rows 4b to 4b+3 of 120, and a view of 10 rows that follows starts at
// row 110.
const (
	blocks = 30
	width  = 60
	height = 10
)

// open returns the usual list and a window on it that has been viewed once.
func open(t *testing.T) (*list, *scrollback.Window) {
	t.Helper()
	l := items(1, blocks, 3, 1)
	w := scrollback.New(4000)
	w.Resize(width, height)
	same(t, w.View(l), texts(l.flat(nil, width), 110, height))
	return l, w
}

// rowAt returns the index of row off of block idx.
func rowAt(t *testing.T, rows []row, idx, off int) int {
	t.Helper()
	at := slices.IndexFunc(rows, func(r row) bool { return r.idx == idx && r.off == off })
	if at < 0 {
		t.Fatalf("block %d has no row %d", idx, off)
	}
	return at
}

func TestFollow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// do changes the list or the window and returns the tail it set.
		do func(l *list, w *scrollback.Window) []string
		// width and height are the view's size afterwards, when do changed it.
		width, height int
	}{
		{name: "nothing happens", do: func(*list, *scrollback.Window) []string { return nil }},
		{name: "a block is added", do: func(l *list, _ *scrollback.Window) []string {
			l.insert(len(l.blocks), block{key: item(100), gap: 1, n: 5})
			return nil
		}},
		{name: "many blocks are added", do: func(l *list, _ *scrollback.Window) []string {
			for id := uint64(100); id < 140; id++ {
				l.insert(len(l.blocks), block{key: item(id), gap: 0, n: 2})
			}
			return nil
		}},
		{name: "the last block grows", do: func(l *list, _ *scrollback.Window) []string {
			l.set(blocks-1, 9)
			return nil
		}},
		{name: "the last block shrinks", do: func(l *list, _ *scrollback.Window) []string {
			l.set(blocks-1, 1)
			return nil
		}},
		{name: "a turn starts", do: func(_ *list, w *scrollback.Window) []string {
			w.SetTail(tailLines(4))
			return tailLines(4)
		}},
		{name: "a turn outgrows the view", do: func(l *list, w *scrollback.Window) []string {
			w.SetTail(tailLines(4))
			w.View(l)
			w.SetTail(tailLines(25))
			return tailLines(25)
		}},
		{name: "a turn becomes a block", do: func(l *list, w *scrollback.Window) []string {
			w.SetTail(tailLines(6))
			w.View(l)
			w.SetTail(nil)
			l.insert(len(l.blocks), block{key: item(100), gap: 1, n: 4})
			return nil
		}},
		{name: "older blocks arrive", do: func(l *list, w *scrollback.Window) []string {
			l.insert(0, block{key: item(900), gap: 1, n: 7}, block{key: item(901), gap: 1, n: 7})
			w.Sync(l)
			return nil
		}},
		{name: "the view gets shorter", height: 4, do: func(_ *list, w *scrollback.Window) []string {
			w.Resize(width, 4)
			return nil
		}},
		{name: "the view gets taller", height: 17, do: func(_ *list, w *scrollback.Window) []string {
			w.Resize(width, 17)
			return nil
		}},
		{name: "the view gets narrower", width: 20, do: func(_ *list, w *scrollback.Window) []string {
			w.Resize(20, height)
			return nil
		}},
		{name: "follow is asked for again", do: func(l *list, w *scrollback.Window) []string {
			w.Scroll(l, -30)
			w.View(l)
			w.Follow()
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, w := open(t)
			if !w.Following() {
				t.Fatal("a new window does not follow")
			}
			tail := tc.do(l, w)
			wd, ht := width, height
			if tc.width != 0 {
				wd = tc.width
			}
			if tc.height != 0 {
				ht = tc.height
			}
			rows := l.flat(tail, wd)
			same(t, w.View(l), texts(rows, len(rows)-ht, ht))
			if !w.Following() {
				t.Fatal("the window stopped following")
			}
		})
	}
}

func TestScroll(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		deltas []int
		// top is the row the view should start at.
		top       int
		following bool
	}{
		{name: "up one", deltas: []int{-1}, top: 109},
		{name: "up into a blank row", deltas: []int{-2}, top: 108},
		{name: "up three and down one", deltas: []int{-3, 1}, top: 108},
		{name: "up and all the way back", deltas: []int{-5, 5}, top: 110, following: true},
		{name: "up and down past the end", deltas: []int{-5, 50}, top: 110, following: true},
		{name: "down while following", deltas: []int{3}, top: 110, following: true},
		{name: "a page up, twice", deltas: []int{-9, -9}, top: 92},
		{name: "more than a page", deltas: []int{-47}, top: 63},
		{name: "up past the top", deltas: []int{-1000}, top: 0},
		{name: "to the top and down two", deltas: []int{-1000, 2}, top: 2},
		{name: "to the top and down a long way", deltas: []int{-1000, 61}, top: 61},
		{name: "nothing", deltas: []int{-4, 0}, top: 106},
		{name: "nothing while following", deltas: []int{0}, top: 110, following: true},
		{name: "the least integer", deltas: []int{math.MinInt}, top: 0},
		{name: "the greatest integer", deltas: []int{-50, math.MaxInt}, top: 110, following: true},
	}
	for _, tc := range cases {
		// A view between the scrolls must make no difference.
		for _, viewed := range []bool{true, false} {
			name := tc.name
			if viewed {
				name += ", viewed between"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				l, w := open(t)
				for _, d := range tc.deltas {
					w.Scroll(l, d)
					if viewed {
						w.View(l)
					}
				}
				same(t, w.View(l), texts(l.flat(nil, width), tc.top, height))
				if w.Following() != tc.following {
					t.Fatalf("Following is %v, want %v", w.Following(), tc.following)
				}
			})
		}
	}
}

// TestScrollFarInsideTail checks the largest scrolls from a view whose top
// row is in the turn in flight, where there is no block to stop at.
func TestScrollFarInsideTail(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		blocks    int
		delta     int
		hit       bool
		following bool
	}{
		{name: "down with blocks", blocks: 5, delta: math.MaxInt, following: true},
		{name: "down without blocks", delta: math.MaxInt, following: true},
		{name: "up with blocks", blocks: 5, delta: math.MinInt, hit: true},
		{name: "up without blocks", delta: math.MinInt, hit: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := items(1, tc.blocks, 3, 1)
			w := scrollback.New(4000)
			w.Resize(width, height)
			w.SetTail(tailLines(40))
			w.Scroll(l, -7)
			w.View(l)
			if got := w.Scroll(l, tc.delta); got != tc.hit {
				t.Fatalf("the scroll reported hitting the top: %v, want %v", got, tc.hit)
			}
			rows := l.flat(tailLines(40), width)
			at := 0
			if tc.following {
				at = len(rows) - height
			}
			same(t, w.View(l), texts(rows, at, height))
			if w.Following() != tc.following {
				t.Fatalf("Following is %v, want %v", w.Following(), tc.following)
			}
		})
	}
}

func TestHitTop(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		blocks int
		tail   int
		top    bool // start from Top
		deltas []int
		want   []bool
	}{
		{name: "an empty list", deltas: []int{-1, 1}, want: []bool{true, false}},
		{name: "less than a screenful", blocks: 2, deltas: []int{-1, 1, -3, 0}, want: []bool{true, false, true, false}},
		{name: "only a turn in flight", tail: 3, deltas: []int{-1}, want: []bool{true}},
		{name: "a long turn in flight and no blocks", tail: 30, deltas: []int{-19, -1, -1}, want: []bool{false, true, true}},
		{name: "one row short of the top", blocks: 30, deltas: []int{-109}, want: []bool{false}},
		{name: "exactly to the top", blocks: 30, deltas: []int{-110}, want: []bool{true}},
		{name: "past the top", blocks: 30, deltas: []int{-500}, want: []bool{true}},
		{name: "again once there", blocks: 30, deltas: []int{-110, -1, -1}, want: []bool{true, true, true}},
		{name: "away and back", blocks: 30, deltas: []int{-110, 1, -1}, want: []bool{true, false, true}},
		{name: "down never hits it", blocks: 30, deltas: []int{5, -3, 3}, want: []bool{false, false, false}},
		{name: "down from the top", blocks: 30, top: true, deltas: []int{0, 4}, want: []bool{false, false}},
		{name: "up from the top", blocks: 30, top: true, deltas: []int{-1}, want: []bool{true}},
		{name: "the top of a later block is not the top", blocks: 30, deltas: []int{-106}, want: []bool{false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := items(1, tc.blocks, 3, 1)
			w := scrollback.New(4000)
			w.Resize(width, height)
			w.SetTail(tailLines(tc.tail))
			if tc.top {
				w.Top(l)
			}
			for i, d := range tc.deltas {
				if got := w.Scroll(l, d); got != tc.want[i] {
					t.Fatalf("scroll %d of %v reported %v, want %v", i, tc.deltas, got, tc.want[i])
				}
			}
		})
	}
}

func TestTop(t *testing.T) {
	t.Parallel()
	l, w := open(t)
	drawn := w.Stats().Rendered
	w.Top(l)
	if w.Following() {
		t.Fatal("the window still follows after Top")
	}
	if got := w.Stats().Rendered; got != drawn {
		t.Fatalf("Top rendered %d blocks", got-drawn)
	}
	same(t, w.View(l), texts(l.flat(nil, width), 0, height))
	if first, last, ok := w.Visible(); first != 0 || last != 2 || !ok {
		t.Fatalf("Visible is %d, %d, %v; want 0, 2, true", first, last, ok)
	}
}

// TestPrepend checks that blocks arriving above the view leave it alone, and
// are not drawn.
func TestPrepend(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// place puts the view somewhere before the blocks arrive.
		place func(l *list, w *scrollback.Window)
		add   int
		sync  bool
	}{
		{name: "in the middle of a block", add: 7, sync: true, place: func(l *list, w *scrollback.Window) { w.Scroll(l, -13) }},
		{name: "on a blank row", add: 7, sync: true, place: func(l *list, w *scrollback.Window) { w.Scroll(l, -14) }},
		{name: "without Sync", add: 7, place: func(l *list, w *scrollback.Window) { w.Scroll(l, -13) }},
		{name: "at the top", add: 3, sync: true, place: func(l *list, w *scrollback.Window) { w.Top(l) }},
		{name: "at the top after scrolling there", add: 200, sync: true, place: func(l *list, w *scrollback.Window) { w.Scroll(l, -400) }},
		{name: "one row from the top", add: 1, sync: true, place: func(l *list, w *scrollback.Window) {
			w.Top(l)
			w.Scroll(l, 1)
		}},
		{name: "while following", add: 50, sync: true, place: func(*list, *scrollback.Window) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, w := open(t)
			tc.place(l, w)
			before := w.View(l)
			first, last, _ := w.Visible()
			following := w.Following()
			drawn := w.Stats().Rendered

			for id := uint64(1000); len(l.blocks) < blocks+tc.add; id++ {
				l.insert(0, block{key: item(id), gap: 1, n: 5})
			}
			if tc.sync {
				w.Sync(l)
			}
			same(t, w.View(l), before)
			if f, la, ok := w.Visible(); f != first+tc.add || la != last+tc.add || !ok {
				t.Fatalf("Visible is %d, %d, %v; want %d, %d, true", f, la, ok, first+tc.add, last+tc.add)
			}
			if w.Following() != following {
				t.Fatalf("Following changed to %v", w.Following())
			}
			if got := w.Stats().Rendered; got != drawn {
				t.Fatalf("blocks arriving above the view caused %d to be drawn", got-drawn)
			}
		})
	}
}

// TestChangeAbove checks that the view keeps its top row when blocks above
// it change size, and what it does when the block at its top does.
func TestChangeAbove(t *testing.T) {
	t.Parallel()
	// The view starts at row 70: row 2 of block 17.
	cases := []struct {
		name string
		do   func(l *list)
		// idx and off name the row the view should start at afterwards.
		idx, off int
	}{
		{name: "a block far above grows", do: func(l *list) { l.set(3, 40) }, idx: 17, off: 2},
		{name: "a block far above shrinks", do: func(l *list) { l.set(3, 1) }, idx: 17, off: 2},
		{name: "the block just above grows", do: func(l *list) { l.set(16, 11) }, idx: 17, off: 2},
		{name: "every block above changes", idx: 17, off: 2, do: func(l *list) {
			for i := range 17 {
				l.set(i, 1+i%5)
			}
		}},
		{name: "a block above is taken out", do: func(l *list) { l.remove(5) }, idx: 16, off: 2},
		{name: "a block is put in above", do: func(l *list) { l.insert(9, block{key: item(500), n: 6}) }, idx: 18, off: 2},
		{name: "the top block grows", do: func(l *list) { l.set(17, 12) }, idx: 17, off: 2},
		{name: "the top block shrinks to above the top row", do: func(l *list) { l.set(17, 1) }, idx: 17, off: 1},
		{name: "a block below changes", do: func(l *list) { l.set(18, 9) }, idx: 17, off: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, w := open(t)
			w.Scroll(l, -40)
			same(t, w.View(l), texts(l.flat(nil, width), 70, height))

			tc.do(l)
			w.Sync(l)
			rows := l.flat(nil, width)
			same(t, w.View(l), texts(rows, rowAt(t, rows, tc.idx, tc.off), height))
			if w.Following() {
				t.Fatal("the window started following")
			}
		})
	}
}

// TestGapChange checks that the lines in view stay on their rows when the
// gap above the view's top block comes or goes: what happens to a block that
// was the first until older ones arrived, and to a tool's result whose call
// did.
func TestGapChange(t *testing.T) {
	t.Parallel()
	// Block 17 is rows 68 to 71: a blank row and three lines.
	cases := []struct {
		name string
		// gap is block 17's gap to begin with, and top the row the view
		// starts at.
		gap, top int
		// to is the block's gap afterwards.
		to int
		// idx and off name the row the view should then start at.
		idx, off int
	}{
		{name: "it goes from above a line at the top", gap: 1, top: 70, to: 0, idx: 17, off: 1},
		{name: "it goes from above the first line at the top", gap: 1, top: 69, to: 0, idx: 17, off: 0},
		{name: "it comes above a line at the top", gap: 0, top: 69, to: 1, idx: 17, off: 2},
		{name: "it comes above the first line at the top", gap: 0, top: 68, to: 1, idx: 17, off: 1},
		// The blank row was the top row. The lines under it stay where they
		// are, so the last row of the block before takes its place.
		{name: "the blank top row goes", gap: 1, top: 68, to: 0, idx: 16, off: 3},
		{name: "a second blank row comes above the top one", gap: 1, top: 68, to: 2, idx: 17, off: 1},
		{name: "it stays", gap: 1, top: 70, to: 1, idx: 17, off: 2},
	}
	for _, tc := range cases {
		for _, sync := range []bool{true, false} {
			name := tc.name
			if !sync {
				name += ", without Sync"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				l := items(1, blocks, 3, 1)
				l.blocks[17].gap = tc.gap
				w := scrollback.New(4000)
				w.Resize(width, height)
				w.Top(l)
				w.Scroll(l, tc.top)
				before := w.View(l)
				same(t, before, texts(l.flat(nil, width), tc.top, height))
				drawn := len(l.drawn)

				l.blocks[17].gap = tc.to
				if sync {
					w.Sync(l)
				}
				rows := l.flat(nil, width)
				got := w.View(l)
				same(t, got, texts(rows, rowAt(t, rows, tc.idx, tc.off), height))
				// Every line that was in view is on the row it was on.
				for r, text := range before {
					if text != "" && got[r] != text {
						t.Fatalf("row %d moved\n got: %q\nwant: %q", r, got, before)
					}
				}
				if len(l.drawn) != drawn {
					t.Fatalf("a change of gap drew %d blocks", len(l.drawn)-drawn)
				}
			})
		}
	}
}

// TestFirstGapChange is the same at the top of the list, where there is no
// block before to show a row of.
func TestFirstGapChange(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		gap, top, to int
		// want is the row the view should then start at.
		want int
	}{
		{name: "the first block gains a gap under a view of its first line", gap: 0, top: 0, to: 1, want: 1},
		{name: "the first block gains a gap under a view of a later line", gap: 0, top: 2, to: 1, want: 3},
		{name: "the first block loses its gap under a view of its first line", gap: 1, top: 1, to: 0, want: 0},
		{name: "the first block loses the blank row at the top", gap: 1, top: 0, to: 0, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := items(1, blocks, 3, 1)
			l.blocks[0].gap = tc.gap
			w := scrollback.New(4000)
			w.Resize(width, height)
			w.Top(l)
			w.Scroll(l, tc.top)
			same(t, w.View(l), texts(l.flat(nil, width), tc.top, height))

			l.blocks[0].gap = tc.to
			w.Sync(l)
			same(t, w.View(l), texts(l.flat(nil, width), tc.want, height))
			if w.Following() {
				t.Fatal("the window started following")
			}
		})
	}
}

func TestResize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// from is the width the view starts at, and scroll how far up from
		// the end it is moved before the resize.
		from, scroll int
		// to and rows are the new size.
		to, rows int
		// idx and off name the row the view should then start at, unless it
		// should follow.
		idx, off  int
		following bool
	}{
		{name: "taller while following", from: 60, to: 60, rows: 25, following: true},
		{name: "shorter while following", from: 60, to: 60, rows: 3, following: true},
		{name: "one row while following", from: 60, to: 60, rows: 1, following: true},
		{name: "narrower while following", from: 60, to: 20, rows: 10, following: true},
		{name: "taller keeps the top row", from: 60, scroll: -40, to: 60, rows: 15, idx: 17, off: 2},
		{name: "shorter keeps the top row", from: 60, scroll: -40, to: 60, rows: 2, idx: 17, off: 2},
		{name: "tall enough to show the end", from: 60, scroll: -5, to: 60, rows: 15, following: true},
		{name: "taller than everything", from: 60, scroll: -40, to: 60, rows: 300, following: true},
		{name: "narrower keeps the top row", from: 60, scroll: -40, to: 20, rows: 10, idx: 17, off: 2},
		{name: "narrower keeps a blank top row", from: 60, scroll: -42, to: 20, rows: 10, idx: 17, off: 0},
		// At 20 columns a block is a blank row and 6 lines. 68 rows up from
		// where a following view starts is the last row of block 18, and a
		// wider block has no such row, so its last row is the top one.
		{name: "wider than the top row allows", from: 20, scroll: -68, to: 60, rows: 10, idx: 18, off: 3},
		{name: "wider keeps the top row", from: 20, scroll: -72, to: 60, rows: 10, idx: 18, off: 2},
		{name: "the same size", from: 60, scroll: -40, to: 60, rows: 10, idx: 17, off: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := items(1, blocks, 3, 1)
			w := scrollback.New(4000)
			w.Resize(tc.from, height)
			w.Scroll(l, tc.scroll)
			w.View(l)

			w.Resize(tc.to, tc.rows)
			rows := l.flat(nil, tc.to)
			at := max(len(rows)-tc.rows, 0)
			if !tc.following {
				at = rowAt(t, rows, tc.idx, tc.off)
			}
			got := w.View(l)
			same(t, got, texts(rows, at, tc.rows))
			if w.Following() != tc.following {
				t.Fatalf("Following is %v, want %v", w.Following(), tc.following)
			}

			// A view that came to show the end stays with it.
			l.insert(len(l.blocks), block{key: item(700), gap: 1, n: 2})
			rows = l.flat(nil, tc.to)
			if tc.following {
				at = max(len(rows)-tc.rows, 0)
			}
			same(t, w.View(l), texts(rows, at, tc.rows))
		})
	}
}

// TestHeight checks that View returns exactly as many lines as the view has
// rows, with what there is at the top.
func TestHeight(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		blocks int
		tail   int
	}{
		{name: "nothing"},
		{name: "one block", blocks: 1},
		{name: "two blocks", blocks: 2},
		{name: "a turn in flight alone", tail: 3},
		{name: "two blocks and a turn in flight", blocks: 2, tail: 3},
		{name: "many blocks", blocks: 30},
		{name: "many blocks and a long turn in flight", blocks: 30, tail: 40},
	}
	places := []struct {
		name string
		do   func(l *list, w *scrollback.Window)
	}{
		{name: "following", do: func(*list, *scrollback.Window) {}},
		{name: "at the top", do: func(l *list, w *scrollback.Window) { w.Top(l) }},
		{name: "scrolled up", do: func(l *list, w *scrollback.Window) { w.Scroll(l, -6) }},
	}
	for _, tc := range cases {
		for _, place := range places {
			t.Run(tc.name+", "+place.name, func(t *testing.T) {
				t.Parallel()
				l := items(1, tc.blocks, 3, 1)
				rows := l.flat(tailLines(tc.tail), width)
				for h := range 20 {
					w := scrollback.New(4000)
					w.Resize(width, h)
					w.SetTail(tailLines(tc.tail))
					place.do(l, w)
					got := w.View(l)
					if len(got) != h {
						t.Fatalf("View returned %d lines at height %d", len(got), h)
					}
					if len(rows) > h {
						continue // a full view; where it starts is for other tests
					}
					same(t, got, texts(rows, 0, h))
					if h > 0 && !w.Following() {
						t.Fatalf("a view of %d rows that shows all %d does not follow", h, len(rows))
					}
				}
			})
		}
	}
}

func TestBeforeResize(t *testing.T) {
	t.Parallel()
	l := items(1, blocks, 3, 1)
	w := scrollback.New(4000)
	if got := w.View(l); len(got) != 0 {
		t.Fatalf("a window with no size showed %q", got)
	}
	if w.Scroll(l, -3) {
		t.Fatal("a window with no rows reported hitting the top")
	}
	if _, _, ok := w.Visible(); ok {
		t.Fatal("a window with no rows reported blocks in view")
	}
	if got := w.Stats(); got != (scrollback.Stats{}) {
		t.Fatalf("a window with no rows asked for blocks: %+v", got)
	}
	// A negative size is no size.
	w.Resize(-5, -5)
	if got := w.View(l); len(got) != 0 {
		t.Fatalf("a window with a negative size showed %q", got)
	}
}

// TestTail checks the turn in flight: it is drawn under the last block, and
// a view scrolled up inside it keeps its place while it grows and when it
// becomes a block.
func TestTail(t *testing.T) {
	t.Parallel()
	// Five blocks are rows 0 to 19, and a tail of 40 lines rows 20 to 59.
	cases := []struct {
		name   string
		blocks int
		scroll int
		do     func(l *list, w *scrollback.Window) []string
		// top is the row the view should start at afterwards.
		top       int
		following bool
	}{
		{name: "it grows under a view anchored on a block", blocks: 5, scroll: -45, top: 5, do: func(_ *list, w *scrollback.Window) []string {
			w.SetTail(tailLines(90))
			return tailLines(90)
		}},
		{name: "it grows under a view anchored inside it", blocks: 5, scroll: -7, top: 43, do: func(_ *list, w *scrollback.Window) []string {
			w.SetTail(tailLines(45))
			return tailLines(45)
		}},
		{name: "it becomes a block", blocks: 5, scroll: -7, top: 43, do: func(l *list, w *scrollback.Window) []string {
			w.SetTail(nil)
			l.insert(5, block{key: item(100), gap: 1, n: 45})
			return nil
		}},
		{name: "it becomes two blocks and a new tail", blocks: 5, scroll: -7, top: 43, do: func(l *list, w *scrollback.Window) []string {
			w.SetTail(tailLines(30))
			l.insert(5, block{key: item(100), gap: 1, n: 9}, block{key: item(101), gap: 1, n: 9})
			return tailLines(30)
		}},
		{name: "it becomes a block too short to scroll", blocks: 5, scroll: -7, following: true, do: func(l *list, w *scrollback.Window) []string {
			w.SetTail(nil)
			l.insert(5, block{key: item(100), gap: 1, n: 30})
			return nil
		}},
		{name: "it shrinks to above the view", blocks: 5, scroll: -7, following: true, do: func(_ *list, w *scrollback.Window) []string {
			w.SetTail(tailLines(12))
			return tailLines(12)
		}},
		{name: "it goes", blocks: 5, scroll: -7, following: true, do: func(_ *list, w *scrollback.Window) []string {
			w.SetTail(nil)
			return nil
		}},
		{name: "older blocks arrive above a view inside it", blocks: 5, scroll: -7, top: 43 + 24, do: func(l *list, w *scrollback.Window) []string {
			for id := uint64(900); id < 906; id++ {
				l.insert(0, block{key: item(id), gap: 1, n: 3})
			}
			return tailLines(40)
		}},
		{name: "the block above it goes", blocks: 5, scroll: -7, top: 43 - 4, do: func(l *list, w *scrollback.Window) []string {
			l.remove(4)
			return tailLines(40)
		}},
		{name: "every block goes", blocks: 5, scroll: -7, top: 23, do: func(l *list, w *scrollback.Window) []string {
			l.blocks = nil
			l.reindex()
			return tailLines(40)
		}},
		// With no block to count from, the view is so many rows from the
		// start of everything.
		{name: "it grows with no blocks", scroll: -7, top: 23, do: func(_ *list, w *scrollback.Window) []string {
			w.SetTail(tailLines(55))
			return tailLines(55)
		}},
		{name: "blocks appear where there were none", scroll: -7, top: 23, do: func(l *list, w *scrollback.Window) []string {
			l.insert(0, block{key: item(100), gap: 1, n: 3})
			return tailLines(40)
		}},
	}
	for _, tc := range cases {
		for _, sync := range []bool{true, false} {
			name := tc.name
			if !sync {
				name += ", without Sync"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				l := items(1, tc.blocks, 3, 1)
				w := scrollback.New(4000)
				w.Resize(width, height)
				w.SetTail(tailLines(40))
				rows := l.flat(tailLines(40), width)
				same(t, w.View(l), texts(rows, len(rows)-height, height))
				w.Scroll(l, tc.scroll)
				same(t, w.View(l), texts(rows, len(rows)-height+tc.scroll, height))

				tail := tc.do(l, w)
				if sync {
					w.Sync(l)
				}
				rows = l.flat(tail, width)
				at := tc.top
				if tc.following {
					at = len(rows) - height
				}
				same(t, w.View(l), texts(rows, at, height))
				if w.Following() != tc.following {
					t.Fatalf("Following is %v, want %v", w.Following(), tc.following)
				}
			})
		}
	}
}

// TestRegroup checks what the view does when the list changes around its
// top block, and not only above or below it.
func TestRegroup(t *testing.T) {
	t.Parallel()
	rule := scrollback.Key{Kind: 2, ID: 20261012}
	cases := []struct {
		name string
		// list builds the list and place puts the view on it.
		list  func() *list
		place func(l *list, w *scrollback.Window)
		do    func(l *list)
		// The view should afterwards start up rows above row off of the
		// block with this key.
		key     scrollback.Key
		off, up int
		// kept is how many rows at the bottom of the view must be the very
		// rows that were there before.
		kept      int
		following bool
	}{
		{
			// The chat's first block is a day's rule. Older messages of the
			// same day go under the rule, which keeps its key: the messages
			// in view must not move.
			name: "older blocks go under the rule at the top",
			list: func() *list {
				l := items(10, 30, 3, 1)
				l.insert(0, block{key: rule, gap: 1, n: 1})
				return l
			},
			place: func(l *list, w *scrollback.Window) { w.Top(l) },
			do: func(l *list) {
				for id := uint64(5); id < 10; id++ {
					l.insert(len(l.blocks)-30, block{key: item(id), gap: 1, n: 3})
				}
			},
			key: item(10), up: 2, kept: 8,
		},
		{
			name: "older blocks and an earlier rule go above the rule at the top",
			list: func() *list {
				l := items(10, 30, 3, 1)
				l.insert(0, block{key: rule, gap: 1, n: 1})
				return l
			},
			place: func(l *list, w *scrollback.Window) { w.Top(l) },
			do: func(l *list) {
				l.insert(0, block{key: scrollback.Key{Kind: 2, ID: 20261011}, gap: 1, n: 1}, block{key: item(5), gap: 1, n: 3})
			},
			key: rule, kept: 10,
		},
		{
			// A block that fills most of the view is what is being read, so
			// it stays and what is under it moves.
			name: "a block goes under a tall block at the top",
			list: func() *list {
				l := items(1, 30, 3, 1)
				l.set(17, 20)
				return l
			},
			place: func(l *list, w *scrollback.Window) { w.Scroll(l, -57) },
			do:    func(l *list) { l.insert(18, block{key: item(500), gap: 1, n: 4}) },
			key:   item(18), off: 2,
		},
		{
			// Rows 68 to 88 are block 17. From row 84 it has five rows in a
			// view of ten: no more than half, so the block under it holds.
			name: "a block goes under a block that fills half the view",
			list: func() *list {
				l := items(1, 30, 3, 1)
				l.set(17, 20)
				return l
			},
			place: func(l *list, w *scrollback.Window) { w.Scroll(l, -43) },
			do:    func(l *list) { l.insert(18, block{key: item(500), gap: 1, n: 4}) },
			key:   item(19), up: 5, kept: 5,
		},
		{
			name: "a block goes under a block that fills a row more than half",
			list: func() *list {
				l := items(1, 30, 3, 1)
				l.set(17, 20)
				return l
			},
			place: func(l *list, w *scrollback.Window) { w.Scroll(l, -44) },
			do:    func(l *list) { l.insert(18, block{key: item(500), gap: 1, n: 4}) },
			key:   item(18), off: 15,
		},
		{
			// A tool's result whose call arrives with the older page: it
			// loses its gap, and its lines must stay where they were.
			name: "the block under the rule loses its gap as older blocks arrive",
			list: func() *list {
				l := items(10, 30, 3, 1)
				l.insert(0, block{key: rule, gap: 1, n: 1})
				return l
			},
			place: func(l *list, w *scrollback.Window) { w.Top(l) },
			do: func(l *list) {
				l.insert(1, block{key: item(8), gap: 1, n: 3}, block{key: item(9), gap: 1, n: 3})
				l.blocks[3].gap = 0
			},
			key: item(10), up: 3, kept: 7,
		},
		{
			name: "the block under the rule gains a gap as older blocks arrive",
			list: func() *list {
				l := items(10, 30, 3, 1)
				l.blocks[0].gap = 0
				l.insert(0, block{key: rule, gap: 1, n: 1})
				return l
			},
			place: func(l *list, w *scrollback.Window) { w.Top(l) },
			do: func(l *list) {
				l.insert(1, block{key: item(8), gap: 1, n: 3}, block{key: item(9), gap: 1, n: 3})
				l.blocks[3].gap = 1
			},
			key: item(10), off: 1, up: 2, kept: 8,
		},
		{
			name:  "the top block is replaced by another",
			list:  func() *list { return items(1, 30, 3, 1) },
			place: func(l *list, w *scrollback.Window) { w.Scroll(l, -40) },
			do: func(l *list) {
				l.remove(17)
				l.insert(17, block{key: item(500), gap: 1, n: 6})
			},
			key: item(19), up: 2, kept: 8,
		},
		{
			name:  "the top block goes",
			list:  func() *list { return items(1, 30, 3, 1) },
			place: func(l *list, w *scrollback.Window) { w.Scroll(l, -40) },
			do:    func(l *list) { l.remove(17) },
			key:   item(19), up: 2, kept: 8,
		},
		{
			name:  "the top block and the one under it go",
			list:  func() *list { return items(1, 30, 3, 1) },
			place: func(l *list, w *scrollback.Window) { w.Scroll(l, -40) },
			do: func(l *list) {
				l.remove(17)
				l.remove(17)
			},
			key: item(20),
		},
		{
			name:  "the top block and all after it go",
			list:  func() *list { return items(1, 30, 3, 1) },
			place: func(l *list, w *scrollback.Window) { w.Scroll(l, -40) },
			do: func(l *list) {
				l.blocks = l.blocks[:17]
				l.reindex()
			},
			following: true,
		},
		{
			name:  "every block goes",
			list:  func() *list { return items(1, 30, 3, 1) },
			place: func(l *list, w *scrollback.Window) { w.Scroll(l, -40) },
			do: func(l *list) {
				l.blocks = nil
				l.reindex()
			},
			following: true,
		},
	}
	for _, tc := range cases {
		for _, sync := range []bool{true, false} {
			name := tc.name
			if !sync {
				name += ", without Sync"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				l := tc.list()
				w := scrollback.New(4000)
				w.Resize(width, height)
				w.View(l)
				tc.place(l, w)
				before := w.View(l)

				tc.do(l)
				if sync {
					w.Sync(l)
					w.Sync(l) // finding the anchor twice is finding it once
				}
				rows := l.flat(nil, width)
				got := w.View(l)
				if w.Following() != tc.following {
					t.Fatalf("Following is %v, want %v", w.Following(), tc.following)
				}
				if tc.following {
					same(t, got, texts(rows, max(len(rows)-height, 0), height))
					return
				}
				same(t, got, texts(rows, rowAt(t, rows, l.index[tc.key], tc.off)-tc.up, height))
				same(t, got[height-tc.kept:], before[height-tc.kept:])
			})
		}
	}
}

// forgetful is a list whose Index still answers for keys that have gone,
// with the place each was last at. A Source should not, and a window must
// not follow it out of the list.
type forgetful struct {
	*list
	gone map[scrollback.Key]int
}

func (f forgetful) Index(k scrollback.Key) (int, bool) {
	if i, ok := f.gone[k]; ok {
		return i, true
	}
	return f.list.Index(k)
}

// TestStaleIndex checks that an index past the end of the list is taken as
// no index at all.
func TestStaleIndex(t *testing.T) {
	t.Parallel()
	l, w := open(t)
	w.Scroll(l, -8)
	w.View(l) // the view starts in block 25 of 30, which has block 26 under it
	src := forgetful{list: l, gone: map[scrollback.Key]int{l.blocks[25].key: 25, l.blocks[26].key: 26}}
	l.blocks = l.blocks[:20]
	l.reindex()

	w.Sync(src)
	rows := l.flat(nil, width)
	same(t, w.View(src), texts(rows, len(rows)-height, height))
	if !w.Following() {
		t.Fatal("a view whose blocks have all gone does not follow")
	}
}

// TestRegroupShortView checks the same in views of a few rows, where a rule
// and its blank row are most or all of the view and must give way all the
// same, or the place in the list is lost by as much as arrived.
func TestRegroupShortView(t *testing.T) {
	t.Parallel()
	// ruled is a rule and then blocks 10 to 39; older blocks go under the
	// rule. Block 10's first line is row 3.
	ruled := func() *list {
		l := items(10, 30, 3, 1)
		l.insert(0, block{key: scrollback.Key{Kind: 2, ID: 20261012}, gap: 1, n: 1})
		return l
	}
	older := func(l *list) {
		l.insert(1, block{key: item(8), gap: 1, n: 3}, block{key: item(9), gap: 1, n: 3})
	}
	// plain is blocks 1 to 30, and a block goes under block 18, whose first
	// line is row 69.
	plain := func() *list { return items(1, 30, 3, 1) }
	under := func(l *list) { l.insert(18, block{key: item(500), gap: 1, n: 4}) }
	cases := []struct {
		name string
		list func() *list
		do   func(l *list)
		// rows is the height of the view and top the row it starts at.
		rows, top int
		// The view should afterwards start up rows above the first line of
		// the block with this key.
		key scrollback.Key
		up  int
	}{
		{name: "a blank row, the rule and a blank row", list: ruled, do: older, rows: 3, top: 0, key: item(10), up: 3},
		{name: "a blank row and the rule", list: ruled, do: older, rows: 2, top: 0, key: item(10), up: 3},
		{name: "the rule and a blank row", list: ruled, do: older, rows: 2, top: 1, key: item(10), up: 2},
		{name: "the rule alone", list: ruled, do: older, rows: 1, top: 1, key: item(10), up: 2},
		// The rule was never drawn, so how far above block 10 the view was is
		// not known: it goes to the blank row above that block.
		{name: "the blank row above the rule alone", list: ruled, do: older, rows: 1, top: 0, key: item(10), up: 1},
		{name: "the blank row under the rule alone", list: ruled, do: older, rows: 1, top: 2, key: item(10), up: 1},
		// Three rows are more than half of four or five: the block stays.
		{name: "three lines of a block in four rows", list: plain, do: under, rows: 4, top: 69, key: item(18), up: 0},
		{name: "three lines of a block in five rows", list: plain, do: under, rows: 5, top: 69, key: item(18), up: 0},
		{name: "three lines of a block in six rows", list: plain, do: under, rows: 6, top: 69, key: item(19), up: 4},
		{name: "two lines of a block in three rows", list: plain, do: under, rows: 3, top: 70, key: item(19), up: 3},
	}
	for _, tc := range cases {
		for _, sync := range []bool{true, false} {
			name := tc.name
			if !sync {
				name += ", without Sync"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				l := tc.list()
				w := scrollback.New(4000)
				w.Resize(width, tc.rows)
				w.Top(l)
				w.Scroll(l, tc.top)
				same(t, w.View(l), texts(l.flat(nil, width), tc.top, tc.rows))

				tc.do(l)
				if sync {
					w.Sync(l)
				}
				rows := l.flat(nil, width)
				i := l.index[tc.key]
				same(t, w.View(l), texts(rows, rowAt(t, rows, i, l.blocks[i].gap)-tc.up, tc.rows))
			})
		}
	}
}

// TestTopThenOlder checks that Top, which has drawn nothing, stays with the
// block it found first when older blocks arrive under it before any View.
func TestTopThenOlder(t *testing.T) {
	t.Parallel()
	for _, gap := range []int{0, 1} {
		t.Run(strconv.Itoa(gap), func(t *testing.T) {
			t.Parallel()
			l := items(10, 30, 3, 1)
			l.insert(0, block{key: scrollback.Key{Kind: 2, ID: 20261012}, gap: gap, n: 1})
			w := scrollback.New(4000)
			w.Resize(width, height)
			w.Top(l)
			l.insert(1, block{key: item(8), gap: 1, n: 3}, block{key: item(9), gap: 1, n: 3})
			w.Sync(l)
			same(t, w.View(l), texts(l.flat(nil, width), 0, height))
		})
	}
}

func TestInvalidate(t *testing.T) {
	t.Parallel()
	// The view follows, so it shows blocks 27 to 29 and two rows of 27's
	// neighbour above.
	cases := []struct {
		name string
		do   func(l *list, w *scrollback.Window)
		// want is the ids of the blocks the next View should draw.
		want []uint64
	}{
		{name: "nothing", do: func(*list, *scrollback.Window) {}},
		{name: "a block in view", do: func(_ *list, w *scrollback.Window) { w.Invalidate(item(29)) }, want: []uint64{29}},
		{name: "a block out of view", do: func(_ *list, w *scrollback.Window) { w.Invalidate(item(3)) }},
		{name: "a block that was never drawn", do: func(_ *list, w *scrollback.Window) { w.Invalidate(item(12345)) }},
		{name: "every block", do: func(_ *list, w *scrollback.Window) { w.InvalidateAll() }, want: []uint64{28, 29, 30}},
		{name: "a block in view changes", do: func(l *list, _ *scrollback.Window) { l.set(28, 3) }, want: []uint64{29}},
		{name: "a block out of view changes", do: func(l *list, _ *scrollback.Window) { l.set(2, 9) }},
		{name: "a block in view is invalidated twice", want: []uint64{30}, do: func(_ *list, w *scrollback.Window) {
			w.Invalidate(item(30))
			w.Invalidate(item(30))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, w := open(t)
			l.drawn = nil
			tc.do(l, w)
			rows := l.flat(nil, width)
			same(t, w.View(l), texts(rows, len(rows)-height, height))
			if got := ids(l.drawn); !slices.Equal(got, tc.want) {
				t.Fatalf("the view drew blocks %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVisible(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		blocks      int
		tail        int
		rows        int
		scroll      int
		first, last int
		ok          bool
	}{
		{name: "nothing", rows: 10},
		{name: "only a turn in flight", tail: 5, rows: 10},
		{name: "following", blocks: 30, rows: 10, first: 27, last: 29, ok: true},
		{name: "one whole block", blocks: 30, rows: 4, first: 29, last: 29, ok: true},
		{name: "one row", blocks: 30, rows: 1, first: 29, last: 29, ok: true},
		{name: "a short list", blocks: 2, rows: 10, first: 0, last: 1, ok: true},
		{name: "a turn in flight under the blocks", blocks: 30, tail: 4, rows: 10, first: 28, last: 29, ok: true},
		{name: "a turn in flight that fills the view", blocks: 30, tail: 10, rows: 10},
		// The view is rows 105 to 109: the lines of block 26, and the blank
		// row and first line of block 27.
		{name: "a block cut at each edge", blocks: 30, rows: 5, scroll: -10, first: 26, last: 27, ok: true},
		// Rows 104 to 108: block 27's blank row is in view and none of its
		// lines, so it does not count.
		{name: "a block of which only the blank row shows", blocks: 30, rows: 5, scroll: -11, first: 26, last: 26, ok: true},
		{name: "a view that is one blank row", blocks: 30, rows: 1, scroll: -3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := items(1, tc.blocks, 3, 1)
			w := scrollback.New(4000)
			w.Resize(width, tc.rows)
			w.SetTail(tailLines(tc.tail))
			if _, _, ok := w.Visible(); ok {
				t.Fatal("blocks were in view before any View")
			}
			w.Scroll(l, tc.scroll)
			w.View(l)
			if first, last, ok := w.Visible(); first != tc.first || last != tc.last || ok != tc.ok {
				t.Fatalf("Visible is %d, %d, %v; want %d, %d, %v", first, last, ok, tc.first, tc.last, tc.ok)
			}
		})
	}
}

// TestGaps checks that the blank rows are the source's to decide.
func TestGaps(t *testing.T) {
	t.Parallel()
	l := items(1, 4, 1, 0)
	l.blocks[0].gap = 1  // above the first block too
	l.blocks[1].gap = 0  // directly under the block above
	l.blocks[2].gap = 2  // more than one
	l.blocks[3].gap = -3 // nonsense is none
	w := scrollback.New(4000)
	w.Resize(width, 8)
	want := []string{"", "3/1 r0 w60 #0", "3/2 r0 w60 #0", "", "", "3/3 r0 w60 #0", "3/4 r0 w60 #0", ""}
	same(t, w.View(l), want)

	// A gap that changes needs no new drawing.
	l.blocks[2].gap = 0
	drawn := len(l.drawn)
	want = []string{"", "3/1 r0 w60 #0", "3/2 r0 w60 #0", "3/3 r0 w60 #0", "3/4 r0 w60 #0", "", "", ""}
	same(t, w.View(l), want)
	if len(l.drawn) != drawn {
		t.Fatalf("a change of gap drew %d blocks", len(l.drawn)-drawn)
	}
}

// TestEmptyBlocks checks that blocks without lines, which a source should
// not have, do no harm: the view is right, both ends can be reached, and no
// scroll gets stuck.
func TestEmptyBlocks(t *testing.T) {
	t.Parallel()
	shapes := []struct {
		name string
		// lines and gaps give each block's number of lines and its gap.
		lines, gaps []int
	}{
		{name: "among others", lines: []int{0, 2, 0, 0, 3, 0}, gaps: []int{0, 1, 1, 0, 0, 0}},
		{name: "at both ends", lines: []int{0, 0, 4, 4, 4, 0, 0}, gaps: []int{0, 0, 1, 1, 1, 0, 0}},
		{name: "with gaps at both ends", lines: []int{0, 4, 4, 4, 0}, gaps: []int{1, 1, 1, 1, 1}},
		{name: "nothing else", lines: []int{0, 0, 0}, gaps: []int{0, 0, 0}},
		{name: "nothing else but gaps", lines: []int{0, 0, 0, 0, 0}, gaps: []int{1, 1, 1, 1, 1}},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			t.Parallel()
			l := items(1, len(shape.lines), 0, 0)
			for i := range l.blocks {
				l.blocks[i].n, l.blocks[i].gap = shape.lines[i], shape.gaps[i]
			}
			rows := l.flat(nil, width)
			const h = 3
			w := scrollback.New(4000)
			w.Resize(width, h)
			bottom := max(len(rows)-h, 0)
			same(t, w.View(l), texts(rows, bottom, h))

			// Up a row at a time: the view moves a row at a time, and says
			// so when it is at the top.
			at := bottom
			for range len(rows) + 3 {
				hit := w.Scroll(l, -1)
				at = max(at-1, 0)
				same(t, w.View(l), texts(rows, at, h))
				if at == 0 && !hit && !w.Scroll(l, -1) {
					t.Fatal("the view is at the top and two scrolls up did not say so")
				}
			}
			// And down again, until it follows.
			for range len(rows) + 3 {
				w.Scroll(l, 1)
				at = min(at+1, bottom)
				same(t, w.View(l), texts(rows, at, h))
			}
			w.Scroll(l, 1)
			if !w.Following() {
				t.Fatal("the view is at the end and does not follow")
			}
			w.Top(l)
			same(t, w.View(l), texts(rows, 0, h))
		})
	}
}
