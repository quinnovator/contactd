package scrollback_test

import (
	"cmp"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/scrollback"
)

// uneven returns a list of n blocks of one to five lines, some directly
// under the block above.
func uneven(n int) *list {
	l := items(1, n, 0, 0)
	for i := range l.blocks {
		l.blocks[i].n = 1 + i%5
		l.blocks[i].gap = min(i%4, 1)
	}
	return l
}

// inView returns the keys of the blocks the last View showed a line of.
func inView(l *list, w *scrollback.Window) []scrollback.Key {
	first, last, ok := w.Visible()
	if !ok {
		return nil
	}
	keys := make([]scrollback.Key, 0, last-first+1)
	for _, b := range l.blocks[first : last+1] {
		keys = append(keys, b.key)
	}
	return keys
}

// ids returns the ids of some keys, in order.
func ids(keys []scrollback.Key) []uint64 {
	out := make([]uint64, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.ID)
	}
	slices.Sort(out)
	return out
}

// without returns the keys of a that are not in b.
func without(a, b []scrollback.Key) []scrollback.Key {
	var out []scrollback.Key
	for _, k := range a {
		if !slices.Contains(b, k) {
			out = append(out, k)
		}
	}
	return out
}

// places are the positions the counting tests start from. Each leaves a
// window that has drawn the blocks in view and nothing else.
func places() []struct {
	name string
	do   func(l *list, w *scrollback.Window)
} {
	return []struct {
		name string
		do   func(l *list, w *scrollback.Window)
	}{
		{name: "following", do: func(*list, *scrollback.Window) {}},
		{name: "at the top", do: func(l *list, w *scrollback.Window) { w.Top(l) }},
		{name: "in the middle", do: func(l *list, w *scrollback.Window) {
			w.Scroll(l, -3000)
			w.InvalidateAll()
		}},
		{name: "following a turn in flight", do: func(_ *list, w *scrollback.Window) { w.SetTail(tailLines(4)) }},
		{name: "inside a long turn in flight", do: func(l *list, w *scrollback.Window) {
			w.SetTail(tailLines(50))
			w.Scroll(l, -20)
		}},
	}
}

// TestUnchangedRendersNothing is the key press: nothing about the
// conversation changed, so no block is drawn, however the view is asked for
// again.
func TestUnchangedRendersNothing(t *testing.T) {
	t.Parallel()
	again := []struct {
		name string
		do   func(l *list, w *scrollback.Window)
	}{
		{name: "View", do: func(*list, *scrollback.Window) {}},
		{name: "Sync", do: func(l *list, w *scrollback.Window) { w.Sync(l) }},
		{name: "a scroll of nothing", do: func(l *list, w *scrollback.Window) { w.Scroll(l, 0) }},
		{name: "a resize to the same size", do: func(_ *list, w *scrollback.Window) { w.Resize(width, height) }},
		{name: "a block out of view invalidated", do: func(_ *list, w *scrollback.Window) { w.Invalidate(item(4000)) }},
	}
	for _, place := range places() {
		for _, step := range again {
			t.Run(place.name+", "+step.name, func(t *testing.T) {
				t.Parallel()
				l := uneven(10000)
				w := scrollback.New(4000)
				w.Resize(width, height)
				place.do(l, w)
				before := w.View(l)
				stats := w.Stats()

				for range 3 {
					step.do(l, w)
					same(t, w.View(l), before)
				}
				got := w.Stats()
				if got.Rendered != stats.Rendered {
					t.Fatalf("an unchanged view rendered %d blocks", got.Rendered-stats.Rendered)
				}
				if len(inView(l, w)) > 0 && got.Hits <= stats.Hits {
					t.Fatal("an unchanged view did not take its blocks from the cache")
				}
			})
		}
	}
}

// TestFirstViewRendersOnlyVisible checks that showing the end of a long
// list draws the blocks in view and no others.
func TestFirstViewRendersOnlyVisible(t *testing.T) {
	t.Parallel()
	for _, place := range places() {
		t.Run(place.name, func(t *testing.T) {
			t.Parallel()
			l := uneven(10000)
			w := scrollback.New(4000)
			w.Resize(width, height)
			place.do(l, w)
			l.drawn = nil
			w.InvalidateAll()
			w.View(l)
			if got, want := ids(l.drawn), ids(inView(l, w)); !slices.Equal(got, want) {
				t.Fatalf("the view drew blocks %v and shows %v", got, want)
			}
		})
	}
}

// TestScrollRendersOnlyEntering checks that a scroll draws the blocks that
// come into view, once each, and nothing else.
func TestScrollRendersOnlyEntering(t *testing.T) {
	t.Parallel()
	for _, place := range places() {
		for _, delta := range []int{-1, -3, -(height - 1), -height, 1, 3, height - 1, height} {
			t.Run(place.name+", "+strconv.Itoa(delta), func(t *testing.T) {
				t.Parallel()
				l := uneven(10000)
				w := scrollback.New(4000)
				w.Resize(width, height)
				place.do(l, w)
				w.View(l)

				// Several scrolls in a row, so that blocks leave and enter.
				stopped := false
				for range 12 {
					before := inView(l, w)
					l.drawn = nil
					stopped = w.Scroll(l, delta) || w.Following() || stopped
					scrolled := slices.Clone(l.drawn)
					w.View(l)
					entered := without(inView(l, w), before)
					if got, want := ids(l.drawn), ids(entered); !slices.Equal(got, want) {
						t.Fatalf("a scroll of %d drew blocks %v; the ones that entered are %v", delta, got, want)
					}
					if len(scrolled) != len(l.drawn) {
						t.Fatalf("the view after a scroll drew %d blocks the scroll had not", len(l.drawn)-len(scrolled))
					}
				}

				// And back the same way, unless an end of the list cut a scroll
				// short: everything is still in the cache.
				if stopped {
					return
				}
				l.drawn = nil
				for range 12 {
					w.Scroll(l, -delta)
					w.View(l)
				}
				if len(l.drawn) != 0 {
					t.Fatalf("scrolling back drew blocks %v again", ids(l.drawn))
				}
			})
		}
	}
}

// TestOneRowRendersOnlyEntering scrolls a view of one row down a list and
// back a row at a time. A view that is a blank row shows no block, and must
// not draw the block the row belongs to. The cache has no room, so every
// block is drawn each time it comes into view, and only then.
func TestOneRowRendersOnlyEntering(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{0, 4000} {
		for _, delta := range []int{1, -1} {
			t.Run(strconv.Itoa(limit)+", "+strconv.Itoa(delta), func(t *testing.T) {
				t.Parallel()
				l := uneven(60)
				w := scrollback.New(limit)
				w.Resize(width, 1)
				if delta > 0 {
					w.Top(l)
				} else {
					w.SetTail(tailLines(1)) // or the last row would be followed
					w.Scroll(l, -1)
				}
				w.View(l)

				blank, entered := 0, 0
				for range 100 {
					before := inView(l, w)
					l.drawn = nil
					w.Scroll(l, delta)
					view := w.View(l)
					now := without(inView(l, w), before)
					if got, want := ids(l.drawn), ids(now); !slices.Equal(got, want) {
						t.Fatalf("a scroll of %d to the row %q drew blocks %v; the ones that entered are %v", delta, view[0], got, want)
					}
					entered += len(now)
					if view[0] == "" {
						blank++
					}
				}
				if blank < 10 || entered < 10 {
					t.Fatalf("the view was a blank row %d times and %d blocks came into it: too few to tell", blank, entered)
				}
			})
		}
	}
}

// TestStepsRenderOnlyVisible does random things to a long list and checks
// after each one that every block drawn for it is a block the view then
// shows: no step costs more than a screenful, wherever the view is and
// however the list changed. A scroll may also draw a block the view showed
// before it, and a view inside the turn in flight the block that turn became.
func TestStepsRenderOnlyVisible(t *testing.T) {
	t.Parallel()
	for seed := range uint64(200) {
		t.Run(strconv.FormatUint(seed, 10), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, 2))
			l := uneven(rng.IntN(300))
			limit := []int{0, 7, 60, 4000}[rng.IntN(4)]
			rows, wd := 1+rng.IntN(14), width
			w := scrollback.New(limit)
			w.Resize(wd, rows)
			w.View(l)
			next, older := uint64(5000), uint64(100000)
			for range 80 {
				// before holds the blocks a step may draw though the view does
				// not show them afterwards.
				shown := inView(l, w)
				var before []scrollback.Key
				l.drawn = nil
				var what string
				switch rng.IntN(11) {
				case 0, 1:
					delta := rng.IntN(2*rows+1) - rows
					w.Scroll(l, delta)
					before, what = shown, "a scroll of "+strconv.Itoa(delta)
				case 2:
					for range 1 + rng.IntN(30) {
						l.insert(0, block{key: item(older), gap: rng.IntN(2), n: 1 + rng.IntN(5)})
						older++
					}
					what = "older blocks arriving"
				case 3:
					if len(shown) == 0 {
						before = []scrollback.Key{item(next)} // the view may be inside the turn
					}
					l.insert(len(l.blocks), block{key: item(next), gap: rng.IntN(2), n: 1 + rng.IntN(5)})
					next++
					what = "a block added at the end"
				case 4:
					if len(l.blocks) > 0 {
						l.set(rng.IntN(len(l.blocks)), 1+rng.IntN(8))
					}
					what = "a block changing"
				case 5:
					w.SetTail(tailLines(rng.IntN(30)))
					what = "the turn in flight changing"
				case 6:
					rows = 1 + rng.IntN(14)
					w.Resize(wd, rows)
					what = "a new height"
				case 7:
					wd = []int{20, width}[rng.IntN(2)]
					w.Resize(wd, rows)
					what = "a new width"
				case 8:
					w.Top(l)
					what = "a jump to the top"
				case 9:
					w.Follow()
					what = "a jump to the end"
				case 10:
					if len(l.blocks) > 0 {
						i := rng.IntN(len(l.blocks))
						l.blocks[i].gap = 1 - l.blocks[i].gap
					}
					what = "a gap changing"
				}
				if rng.IntN(3) == 0 {
					w.Sync(l)
				}
				w.View(l)

				allowed := slices.Concat(inView(l, w), before)
				if drew := without(l.drawn, allowed); len(drew) > 0 {
					t.Fatalf("%s drew blocks %v; the view of %d rows shows %v", what, ids(drew), rows, ids(inView(l, w)))
				}
				if limit == 4000 {
					once := slices.Clone(l.drawn)
					slices.SortFunc(once, func(a, b scrollback.Key) int { return cmp.Compare(a.ID, b.ID) })
					if len(slices.Compact(once)) != len(l.drawn) {
						t.Fatalf("%s drew a block twice: %v", what, ids(l.drawn))
					}
				}
			}
		})
	}
}

// TestResizeRendersOnlyVisible checks that a new width draws the blocks in
// view and no others, and a new height only the blocks it brings into view.
func TestResizeRendersOnlyVisible(t *testing.T) {
	t.Parallel()
	sizes := []struct {
		name          string
		width, height int
	}{
		{name: "narrower", width: 20, height: height},
		{name: "wider", width: 90, height: height},
		{name: "narrower and taller", width: 20, height: 25},
		{name: "wider and shorter", width: 90, height: 3},
		{name: "taller", width: width, height: 25},
		{name: "shorter", width: width, height: 3},
	}
	for _, place := range places() {
		for _, size := range sizes {
			t.Run(place.name+", "+size.name, func(t *testing.T) {
				t.Parallel()
				l := uneven(10000)
				w := scrollback.New(4000)
				w.Resize(width, height)
				place.do(l, w)
				w.View(l)
				before := inView(l, w)

				l.drawn = nil
				w.Resize(size.width, size.height)
				w.View(l)
				want := inView(l, w)
				if size.width == width {
					want = without(want, before)
				}
				if got := ids(l.drawn); !slices.Equal(got, ids(want)) {
					t.Fatalf("the resize drew blocks %v, want %v", got, ids(want))
				}

				// A second view at the new size draws nothing.
				l.drawn = nil
				w.View(l)
				if len(l.drawn) != 0 {
					t.Fatalf("the view after the resize drew blocks %v again", ids(l.drawn))
				}
			})
		}
	}
}

// TestCacheBound walks the whole of a list with caches of several sizes and
// checks that none ever holds more than its limit and the blocks in view.
func TestCacheBound(t *testing.T) {
	t.Parallel()
	const (
		count = 300
		// most is the most lines a block of uneven has, at a narrow width.
		most = 10
	)
	cases := []struct {
		name  string
		limit int
		// redrawn says the list does not fit the cache, so blocks are drawn
		// again when the view comes back to them.
		redrawn bool
	}{
		{name: "none", limit: 0, redrawn: true},
		{name: "a negative limit is none", limit: -1, redrawn: true},
		{name: "less than a view", limit: 5, redrawn: true},
		{name: "a few views", limit: 60, redrawn: true},
		{name: "the whole list", limit: 4000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := uneven(count)
			w := scrollback.New(tc.limit)
			w.Resize(width, height)
			limit := max(tc.limit, 0)
			wd := width

			// check holds the cache to its bound: exactly after a View, and
			// by the most a view could hold after anything else.
			check := func(what string) {
				t.Helper()
				if held, bound := w.Cached(), limit+height*most; held > bound {
					t.Fatalf("after %s the cache holds %d lines, more than %d", what, held, bound)
				}
				w.View(l)
				bound, visible := limit, 0
				for _, k := range inView(l, w) {
					visible += len(l.blocks[l.index[k]].lines(wd))
				}
				bound += visible
				if held := w.Cached(); held > bound {
					t.Fatalf("after %s and a view the cache holds %d lines; %d are in view and the limit is %d", what, held, visible, limit)
				}
				if held := w.Cached(); held < visible {
					t.Fatalf("after %s and a view the cache holds %d lines and %d are in view", what, held, visible)
				}
			}

			// The list is some 1,200 rows, so 200 pages are more than enough.
			check("the first view")
			for page := 0; !w.Scroll(l, -(height - 1)); page++ {
				if page > 200 {
					t.Fatal("paging up never reached the top")
				}
				check("a page up")
			}
			once := w.Stats().Rendered
			for page := 0; !w.Following(); page++ {
				if page > 200 {
					t.Fatal("paging down never reached the end")
				}
				w.Scroll(l, height-1)
				check("a page down")
			}
			if got := w.Stats().Rendered; tc.redrawn && got <= once {
				t.Fatal("a list larger than the cache was walked twice and nothing was drawn twice")
			} else if !tc.redrawn && (once != count || got != count) {
				t.Fatalf("a list that fits the cache was drawn %d times going up and %d in all, want %d", once, got, count)
			}

			w.Scroll(l, -100000)
			check("a scroll past every block")
			w.Scroll(l, 100000)
			check("a scroll back past every block")
			w.Top(l)
			check("a jump to the top")
			wd = 20
			w.Resize(wd, height)
			check("a narrower view")
			w.Scroll(l, 37)
			check("a long scroll at the new width")
			w.Follow()
			check("a jump to the end")
			l.set(count-1, 5)
			check("a block changing")
			w.InvalidateAll()
			if held := w.Cached(); held != 0 {
				t.Fatalf("after InvalidateAll the cache holds %d lines", held)
			}
			check("forgetting everything")
			w.Invalidate(l.blocks[count-1].key)
			check("forgetting a block in view")
			w.Resize(wd, 0)
			if w.View(l); w.Cached() > limit {
				t.Fatalf("a view with no rows holds %d lines, more than the limit of %d", w.Cached(), limit)
			}
		})
	}
}

// TestCacheBoundWhileScrolling checks the cache in the middle of a scroll
// that passes over the whole list: the blocks it passes are not in view, so
// they must not pile up.
func TestCacheBoundWhileScrolling(t *testing.T) {
	t.Parallel()
	const (
		count = 2000
		limit = 40
		// most is the most lines a block of uneven has.
		most = 5
	)
	for _, delta := range []int{-100000, 100000} {
		t.Run(strconv.Itoa(delta), func(t *testing.T) {
			t.Parallel()
			l := uneven(count)
			w := scrollback.New(limit)
			w.Resize(width, height)
			if delta > 0 {
				w.Top(l)
			}
			w.View(l)

			// The view before the scroll, the block being passed, and the
			// limit.
			bound := limit + height*most + most
			peak := 0
			l.hook = func() { peak = max(peak, w.Cached()) }
			l.drawn = nil
			w.Scroll(l, delta)
			if len(l.drawn) < count-2*height {
				t.Fatalf("a scroll past %d blocks drew only %d", count, len(l.drawn))
			}
			if peak > bound {
				t.Fatalf("in the middle of a long scroll the cache held %d lines, more than %d", peak, bound)
			}
		})
	}
}

// TestCacheBoundAfterRegroup is a case the fuzzer found. Blocks go under the
// block at the top of the view, so the view is anchored some rows above the
// block that was under it. Before it is drawn the view gets shorter, and the
// rows above no longer reach back into view: the blocks they are in must not
// be kept as if they were.
func TestCacheBoundAfterRegroup(t *testing.T) {
	t.Parallel()
	l := items(1, 8, 1, 0)
	l.insert(0, block{key: scrollback.Key{Kind: 2, ID: 20261012}, gap: 1, n: 4})
	w := scrollback.New(0)
	w.Resize(width, height)
	w.Top(l)
	w.View(l)

	l.insert(1, block{key: item(100), n: 1})
	w.Sync(l)
	w.Resize(20, 3)
	w.View(l)
	visible := 0
	for _, k := range inView(l, w) {
		visible += len(l.blocks[l.index[k]].lines(20))
	}
	if held := w.Cached(); held != visible {
		t.Fatalf("a cache with no room holds %d lines and %d are in view", held, visible)
	}
}

// TestCacheBoundsEmptyBlocks checks that blocks with no lines, which add
// nothing to the count of lines, cannot fill the cache either.
func TestCacheBoundsEmptyBlocks(t *testing.T) {
	t.Parallel()
	const limit = 5
	l := items(1, 500, 0, 1)
	w := scrollback.New(limit)
	w.Resize(width, height)
	w.View(l)
	for page := 0; !w.Scroll(l, -(height - 1)); page++ {
		if page > 200 {
			t.Fatal("paging up never reached the top")
		}
		w.View(l)
		if got := w.Entries(); got > limit+height+1 {
			t.Fatalf("the cache holds %d blocks; its limit is %d lines and the view %d rows", got, limit, height)
		}
	}
}

// TestStats checks that the two counts are what they say.
func TestStats(t *testing.T) {
	t.Parallel()
	l := items(1, blocks, 3, 1)
	w := scrollback.New(4000)
	if got := w.Stats(); got != (scrollback.Stats{}) {
		t.Fatalf("a new window has counts %+v", got)
	}
	w.Resize(width, height)
	w.View(l)
	first := w.Stats()
	if first.Rendered != 3 || first.Rendered != len(l.drawn) {
		t.Fatalf("the first view rendered %d blocks and the source drew %d, want 3", first.Rendered, len(l.drawn))
	}
	w.View(l)
	second := w.Stats()
	if second.Rendered != 3 {
		t.Fatalf("the second view rendered %d blocks", second.Rendered-3)
	}
	if second.Hits <= first.Hits {
		t.Fatalf("the second view counted no cache hits: %+v then %+v", first, second)
	}
}
