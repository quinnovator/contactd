package scrollback_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/scrollback"
)

// wide is the width at and above which a block has its own number of lines.
// Under it every block has twice as many, as if each line had wrapped.
const wide = 40

// block is one block of a list.
type block struct {
	key scrollback.Key
	rev uint64
	gap int
	// n is the number of lines at a width of wide or more.
	n int
}

// list is a Source for tests. Each line says which block, revision, width
// and row it is, so a stale or misplaced line shows. It records every call
// to Lines.
type list struct {
	blocks []block
	index  map[scrollback.Key]int
	// drawn holds the block of each Lines call, in order.
	drawn []scrollback.Key
	// hook, when set, is called by every Lines call.
	hook func()
}

// item returns the key the tests give an ordinary block.
func item(id uint64) scrollback.Key { return scrollback.Key{Kind: 3, ID: id} }

// items returns a list of n blocks with ids from first, each with the same
// number of lines and the same gap.
func items(first uint64, n, lines, gap int) *list {
	l := &list{index: make(map[scrollback.Key]int)}
	for id := first; len(l.blocks) < n; id++ {
		l.blocks = append(l.blocks, block{key: item(id), gap: gap, n: lines})
	}
	l.reindex()
	return l
}

func (l *list) reindex() {
	clear(l.index)
	for i, b := range l.blocks {
		l.index[b.key] = i
	}
}

// insert puts blocks in at index at.
func (l *list) insert(at int, blocks ...block) {
	l.blocks = slices.Insert(l.blocks, at, blocks...)
	l.reindex()
}

// remove takes out the block at index at.
func (l *list) remove(at int) {
	l.blocks = slices.Delete(l.blocks, at, at+1)
	l.reindex()
}

// set gives the block at index at a new number of lines, which is a change
// of content.
func (l *list) set(at, lines int) {
	l.blocks[at].n = lines
	l.blocks[at].rev++
}

func (l *list) Len() int                 { return len(l.blocks) }
func (l *list) Key(i int) scrollback.Key { return l.blocks[i].key }
func (l *list) Rev(i int) uint64         { return l.blocks[i].rev }
func (l *list) Gap(i int) int            { return l.blocks[i].gap }

func (l *list) Index(k scrollback.Key) (int, bool) {
	i, ok := l.index[k]
	return i, ok
}

func (l *list) Lines(i, width int) []string {
	b := l.blocks[i]
	l.drawn = append(l.drawn, b.key)
	if l.hook != nil {
		l.hook()
	}
	return b.lines(width)
}

func (b block) lines(width int) []string {
	n := b.n
	if width < wide {
		n *= 2
	}
	out := make([]string, n)
	for r := range out {
		out[r] = fmt.Sprintf("%d/%d r%d w%d #%d", b.key.Kind, b.key.ID, b.rev, width, r)
	}
	return out
}

// row is one row of a list laid out in full.
type row struct {
	text string
	// idx is the row's block, or the number of blocks for a row of the tail.
	idx int
	// off is the row within its block, gap rows first, or within the tail.
	off int
	// line says the row is a line of a block, not a gap or a tail row.
	line bool
}

// flat lays the whole list out, the naive way: every block drawn and joined,
// then the tail.
func (l *list) flat(tail []string, width int) []row {
	var rows []row
	for i, b := range l.blocks {
		for g := range b.gap {
			rows = append(rows, row{idx: i, off: g})
		}
		for r, text := range b.lines(width) {
			rows = append(rows, row{text: text, idx: i, off: b.gap + r, line: true})
		}
	}
	for r, text := range tail {
		rows = append(rows, row{text: text, idx: len(l.blocks), off: r})
	}
	return rows
}

// texts returns the height rows from at, with empty lines after the last.
func texts(rows []row, at, height int) []string {
	out := make([]string, height)
	for i := range out {
		if at+i < len(rows) {
			out[i] = rows[at+i].text
		}
	}
	return out
}

// tailLines returns n lines of a turn in flight.
func tailLines(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("tail #%d", i)
	}
	return out
}

// same fails the test when the view is not the rows expected.
func same(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("view differs\n got: %q\nwant: %q", got, want)
	}
}
