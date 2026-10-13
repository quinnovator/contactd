package scrollback_test

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/scrollback"
)

// model is the window done the naive way: it lays out every row of the list
// each time it is asked anything, and remembers only where the top row is.
type model struct {
	height    int
	following bool
	// The top row of an anchored view: off rows below the first line of the
	// block with this key, or above it when off is below zero; or, when tail
	// is set, off rows below that block. bare means there was no block to
	// name.
	key        scrollback.Key
	off        int
	tail, bare bool
}

// top returns the index of the view's top row. Like the window, it starts
// following when the last row is in view.
func (m *model) top(l *list, rows []row) int {
	if !m.following {
		at := m.find(l, rows)
		if at+m.height < len(rows) {
			return at
		}
		m.following = true
	}
	return max(len(rows)-m.height, 0)
}

// find returns the index of the anchored row.
func (m *model) find(l *list, rows []row) int {
	if m.tail && m.bare {
		return min(m.off, len(rows))
	}
	i := l.index[m.key]
	first, count := 0, 0
	for at, r := range rows {
		if r.idx == i {
			if count == 0 {
				first = at
			}
			count++
		}
	}
	if m.tail {
		return min(first+count+m.off, len(rows))
	}
	// A block that lost the row keeps its last row at the top. A row above
	// the block, when its gap has gone, is a row of the block before it.
	return max(first+min(m.off+l.blocks[i].gap, count-1), 0)
}

// anchor remembers a row as the top of the view.
func (m *model) anchor(l *list, r row) {
	n := len(l.blocks)
	switch {
	case r.idx < n:
		m.key, m.off, m.tail, m.bare = l.blocks[r.idx].key, r.off-l.blocks[r.idx].gap, false, false
	case n > 0:
		m.key, m.off, m.tail, m.bare = l.blocks[n-1].key, r.off, true, false
	default:
		m.off, m.tail, m.bare = r.off, true, true
	}
}

// view returns what the window should show, and the blocks with a line in
// it.
func (m *model) view(l *list, rows []row) (out []string, first, last int, ok bool) {
	if m.height == 0 {
		return []string{}, 0, 0, false
	}
	at := m.top(l, rows)
	if !m.following {
		m.anchor(l, rows[at])
	}
	for _, r := range rows[at:min(at+m.height, len(rows))] {
		if !r.line {
			continue
		}
		if !ok {
			first, ok = r.idx, true
		}
		last = r.idx
	}
	return texts(rows, at, m.height), first, last, ok
}

// scroll moves the view and reports whether it hit the top.
func (m *model) scroll(l *list, rows []row, delta int) bool {
	if m.height == 0 {
		return false
	}
	at := m.top(l, rows)
	if delta == 0 {
		return false
	}
	at = min(max(at+delta, 0), len(rows))
	m.following = at+m.height >= len(rows)
	if !m.following {
		m.anchor(l, rows[at])
	}
	return delta < 0 && at == 0
}

// script feeds a window and the model the same steps, chosen by the bytes
// of a script, and fails at the first difference between them.
//
// The model knows a list that grows at its ends and whose blocks change, in
// their lines or in the gap above them. A
// script with any set changes the list in every other way too: blocks go in
// and out anywhere and change places. What the view should then start at is
// a matter of judgement that TestRegroup pins down, so such a script holds
// the window only to what must be true wherever the view is.
type script struct {
	t     *testing.T
	in    []byte
	any   bool
	l     *list
	w     *scrollback.Window
	m     model
	limit int
	width int
	tail  []string
	next  uint64 // the id of the next block made
	older uint64 // the id of the next block put at the front
}

// byte returns the next byte of the script, or zero when it has run out.
func (s *script) byte() int {
	if len(s.in) == 0 {
		return 0
	}
	b := s.in[0]
	s.in = s.in[1:]
	return int(b)
}

func (s *script) block(id uint64) block {
	return block{key: item(id), gap: s.byte() % 2, n: 1 + s.byte()%4}
}

func (s *script) rows() []row { return s.l.flat(s.tail, s.width) }

// step does one thing to both.
func (s *script) step() {
	t := s.t
	t.Helper()
	ops := 16
	if s.any {
		ops = 20
	}
	switch op := s.byte() % ops; op {
	case 0, 1, 2:
		s.check()
	case 3:
		s.l.insert(len(s.l.blocks), s.block(s.next))
		s.next++
	case 4:
		// Older blocks arrive at the front, a few at a time.
		for range 1 + s.byte()%3 {
			s.l.insert(0, s.block(s.older))
			s.older--
		}
	case 5:
		if n := len(s.l.blocks); n > 0 {
			// A block changes; one time in four it is the block's gap that
			// comes or goes, as for a block that stops being the first.
			if i, b := s.byte()%n, s.byte(); b >= 192 {
				s.l.blocks[i].gap = 1 - s.l.blocks[i].gap
			} else {
				s.l.set(i, 1+b%6)
			}
		}
	case 6:
		s.tail = tailLines(s.byte() % 16)
		s.w.SetTail(s.tail)
	case 7:
		s.width = []int{20, 60}[s.byte()%2]
		s.m.height = s.byte() % 13
		s.w.Resize(s.width, s.m.height)
	case 8, 9, 10:
		delta := s.byte()%31 - 15
		if op == 10 {
			delta *= 40
		}
		got := s.w.Scroll(s.l, delta)
		if s.any {
			// Hitting the top is only ever said of a scroll up, and then the
			// view starts at the first row.
			if got && delta >= 0 {
				t.Fatalf("Scroll(%d) reported hitting the top", delta)
			}
			if got {
				same(t, s.w.View(s.l), texts(s.rows(), 0, s.m.height))
			}
		} else if want := s.m.scroll(s.l, s.rows(), delta); got != want {
			t.Fatalf("Scroll(%d) reported hitting the top: %v, want %v", delta, got, want)
		}
	case 11:
		s.w.Top(s.l)
		s.m.following = false
		if rows := s.rows(); len(rows) > 0 {
			s.m.anchor(s.l, rows[0])
		} else {
			s.m.off, s.m.tail, s.m.bare = 0, true, true
		}
	case 12:
		s.w.Follow()
		s.m.following = true
	case 13:
		if n := len(s.l.blocks); n > 0 {
			s.w.Invalidate(s.l.blocks[s.byte()%n].key)
		}
	case 14:
		s.w.InvalidateAll()
	case 15:
		s.w.Sync(s.l)
	case 16:
		s.l.insert(s.byte()%(len(s.l.blocks)+1), s.block(s.next))
		s.next++
	case 17:
		if n := len(s.l.blocks); n > 0 {
			s.l.remove(s.byte() % n)
		}
	case 18:
		if n := len(s.l.blocks); n > 0 {
			i, j := s.byte()%n, s.byte()%n
			s.l.blocks[i], s.l.blocks[j] = s.l.blocks[j], s.l.blocks[i]
			s.l.reindex()
		}
	case 19:
		// Everything from some block on goes.
		s.l.blocks = s.l.blocks[:s.byte()%(len(s.l.blocks)+1)]
		s.l.reindex()
	}
}

// check compares what the window shows with the model, and holds the window
// to its promises about the cache.
func (s *script) check() {
	t := s.t
	t.Helper()
	rows := s.rows()
	got := s.w.View(s.l)
	if len(got) != s.m.height {
		t.Fatalf("View returned %d lines at height %d", len(got), s.m.height)
	}
	var (
		first, last int
		ok          bool
	)
	if s.any {
		at, sure := s.somewhere(rows, got)
		if !sure {
			return
		}
		for _, r := range rows[min(at, len(rows)):min(at+s.m.height, len(rows))] {
			if !r.line {
				continue
			}
			if !ok {
				first, ok = r.idx, true
			}
			last = r.idx
		}
	} else {
		var want []string
		want, first, last, ok = s.m.view(s.l, rows)
		same(t, got, want)
		if s.w.Following() != s.m.following {
			t.Fatalf("Following is %v, want %v", s.w.Following(), s.m.following)
		}
	}
	if f, l, o := s.w.Visible(); f != first || l != last || o != ok {
		t.Fatalf("Visible is %d, %d, %v; want %d, %d, %v", f, l, o, first, last, ok)
	}
	if st := s.w.Stats(); st.Rendered != len(s.l.drawn) {
		t.Fatalf("Stats counts %d rendered blocks and the source drew %d", st.Rendered, len(s.l.drawn))
	}
	bound := s.limit
	if ok {
		for _, b := range s.l.blocks[first : last+1] {
			bound += len(b.lines(s.width))
		}
	}
	if held := s.w.Cached(); held > bound {
		t.Fatalf("the cache holds %d lines; its limit of %d and the blocks in view allow %d", held, s.limit, bound)
	}
}

// somewhere checks a view against what must hold wherever it is. A view
// that follows is the last screenful. One that does not is some screenful of
// the list with rows left under it. It returns the row the view starts at,
// and whether that is certain: a view of blank rows alone could be in more
// than one place.
func (s *script) somewhere(rows []row, got []string) (at int, sure bool) {
	t := s.t
	t.Helper()
	h := s.m.height
	if s.w.Following() || h == 0 {
		at = max(len(rows)-h, 0)
		same(t, got, texts(rows, at, h))
		return at, true
	}
	found := 0
	for c := 0; c+h < len(rows); c++ {
		if slices.Equal(got, texts(rows, c, h)) {
			at = c
			found++
		}
	}
	if found == 0 {
		t.Fatalf("a view that does not follow is no screenful of the list with rows under it: %q", got)
	}
	return at, found == 1
}

// run plays a whole script. Its first bytes choose the cache's limit and the
// size of the view.
func run(t *testing.T, in []byte, anyChange bool) {
	t.Helper()
	s := &script{t: t, in: in, any: anyChange, l: items(1000, 0, 0, 0), next: 1000, older: 999, width: 60}
	s.limit = []int{0, 7, 60, 4000}[s.byte()%4]
	s.w = scrollback.New(s.limit)
	s.m = model{height: 1 + s.byte()%12, following: true}
	s.w.Resize(s.width, s.m.height)
	for range s.byte() % 24 {
		s.l.insert(len(s.l.blocks), s.block(s.next))
		s.next++
	}
	for len(s.in) > 0 {
		s.step()
	}
	s.check()
}

// random returns a script of n bytes.
func random(seed uint64, n int) []byte {
	rng := rand.New(rand.NewPCG(seed, 1))
	in := make([]byte, n)
	for i := range in {
		in[i] = byte(rng.UintN(256))
	}
	return in
}

// TestAgainstNaiveModel runs random scripts of every operation against a
// model that joins all the blocks and slices the result.
func TestAgainstNaiveModel(t *testing.T) {
	t.Parallel()
	for seed := range uint64(400) {
		t.Run(strconv.FormatUint(seed, 10), func(t *testing.T) {
			t.Parallel()
			run(t, random(seed, 600), false)
		})
	}
}

// TestAnyChange runs random scripts that also put blocks in, take them out
// and move them anywhere in the list, and checks what must hold of any view.
func TestAnyChange(t *testing.T) {
	t.Parallel()
	for seed := range uint64(400) {
		t.Run(strconv.FormatUint(seed, 10), func(t *testing.T) {
			t.Parallel()
			run(t, random(seed, 600), true)
		})
	}
}

// FuzzAgainstNaiveModel is both of those with scripts the fuzzer writes.
func FuzzAgainstNaiveModel(f *testing.F) {
	f.Add([]byte{}, false)
	f.Add([]byte{3, 5, 20, 8, 0, 0, 11, 0, 9, 30, 0, 4, 2, 1, 1, 1, 1, 1, 1, 15, 0, 12, 0}, false)
	f.Add([]byte{0, 3, 0, 6, 15, 8, 2, 0, 6, 3, 3, 1, 2, 0, 7, 0, 9, 0, 6, 0, 0}, false)
	f.Add([]byte{1, 9, 12, 1, 2, 1, 2, 1, 2, 8, 20, 0, 17, 3, 0, 16, 2, 1, 1, 18, 0, 5, 19, 2, 0}, true)
	f.Fuzz(func(t *testing.T, in []byte, anyChange bool) {
		run(t, in, anyChange)
	})
}
