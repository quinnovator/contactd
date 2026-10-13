package scrollback_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/scrollback"
)

// perDay is how many messages each day of a chat has.
const perDay = 7

// chat returns the blocks a client shows for messages from to last of a
// conversation, keyed and spaced as the client's are. A rule opens the list
// when it starts at message 1, and a rule stands above each day's first
// message. Every fourth message is a tool call and the one after it the
// result, which has no gap when it is directly under its call. With flush
// set the first block has no gap either.
//
// Loading an older page therefore does more than add blocks at the front. A
// day's rule keeps its key and moves above the older messages of that day. A
// result whose call arrives loses its gap, and so does a first block that is
// first no longer.
func chat(from, last uint64, flush bool) *list {
	l := &list{index: make(map[scrollback.Key]int)}
	if from == 1 {
		l.blocks = append(l.blocks, block{key: scrollback.Key{Kind: 1}, gap: 1, n: 1})
	}
	day := uint64(0)
	for seq := from; seq <= last; seq++ {
		if d := 20261001 + seq/perDay; d != day {
			day = d
			l.blocks = append(l.blocks, block{key: scrollback.Key{Kind: 2, ID: d}, gap: 1, n: 1})
		}
		b := block{key: item(seq), gap: 1, n: 3}
		switch seq % 4 {
		case 2:
			b.n = 2
		case 3:
			b.n = 2
			if seq > from && seq/perDay == (seq-1)/perDay {
				b.gap = 0
			}
		}
		l.blocks = append(l.blocks, b)
	}
	if flush {
		l.blocks[0].gap = 0
	}
	l.reindex()
	return l
}

// TestOlderPages scrolls to somewhere near the top of a chat, loads an older
// page, and checks that every line of a message that was in view is still on
// the same screen row. Only a rule may give its row up, to an older message.
// A view too short to show a line of a message must keep its place all the
// same: the message under it is the one that was.
func TestOlderPages(t *testing.T) {
	t.Parallel()
	const last = 40
	for _, flush := range []bool{false, true} {
		for _, page := range []uint64{1, 2, 3, 5, 8, 13} {
			t.Run(fmt.Sprintf("flush=%v, page=%d", flush, page), func(t *testing.T) {
				t.Parallel()
				held := 0
				for from := uint64(2); from <= 24; from++ {
					for _, rows := range []int{1, 2, 3, 5, 10} {
						for top := range 12 {
							held += olderPage(t, chat(from, last, flush), chat(from-min(page, from-1), last, flush), rows, top)
						}
					}
				}
				if held == 0 {
					t.Fatal("no view had a line of a message in it")
				}
			})
		}
	}
}

// olderPage shows rows rows of a list from row top on, swaps the list for
// one with an older page loaded, and checks the view. It returns how many
// rows it held to their place.
func olderPage(t *testing.T, before, after *list, rows, top int) int {
	t.Helper()
	w := scrollback.New(4000)
	w.Resize(width, rows)
	w.Top(before)
	w.Scroll(before, top)
	was := w.View(before)
	if w.Following() {
		return 0
	}
	flat := before.flat(nil, width)
	same(t, was, texts(flat, top, rows))
	laid := after.flat(nil, width)

	w.Sync(after)
	got := w.View(after)
	held := 0
	for r := range rows {
		at := flat[top+r]
		if !at.line || before.blocks[at.idx].key.Kind != 3 {
			continue
		}
		held++
		if got[r] != was[r] {
			t.Fatalf("%d blocks became %d under a view of %d rows from row %d, and row %d moved\n got: %q\nwant: %q",
				len(before.blocks), len(after.blocks), rows, top, r, got, was)
		}
	}

	// Whatever is on the other rows, the view is one screenful of the list.
	found := false
	for at := range laid {
		found = found || slices.Equal(got, texts(laid, at, rows))
	}
	if !found {
		t.Fatalf("the view is no screenful of the list: %q", got)
	}
	// And the page was not drawn: only what the view shows was.
	if drew := without(after.drawn, inView(after, w)); len(drew) > 0 {
		t.Fatalf("an older page arriving drew blocks %v, which are not in view", ids(drew))
	}

	// The place is kept even when no message was in view: the first line of
	// a message at or under the top row is no further off than it was.
	next := slices.IndexFunc(flat[top:], func(r row) bool { return r.line && before.blocks[r.idx].key.Kind == 3 })
	want := flat[top+next].text
	met := got[0] == want
	for range next {
		w.Scroll(after, 1)
		met = met || w.View(after)[0] == want
	}
	if !met {
		t.Fatalf("%d blocks became %d under a view of %d rows from row %d: %q was %d rows under the top row and is not now",
			len(before.blocks), len(after.blocks), rows, top, want, next)
	}
	return held
}
