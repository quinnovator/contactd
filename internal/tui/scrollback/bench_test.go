package scrollback_test

import (
	"strconv"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/scrollback"
)

// bench runs one benchmark at 100 and at 10,000 blocks. The two should cost
// the same: nothing a window does depends on how long the list is.
func bench(b *testing.B, run func(b *testing.B, l *list, w *scrollback.Window)) {
	b.Helper()
	for _, n := range []int{100, 10000} {
		b.Run("blocks="+strconv.Itoa(n), func(b *testing.B) {
			l := uneven(n)
			w := scrollback.New(4000)
			w.Resize(100, 23)
			w.View(l)
			b.ReportAllocs()
			run(b, l, w)
		})
	}
}

// BenchmarkView is a key press: the view is asked for and nothing changed.
func BenchmarkView(b *testing.B) {
	bench(b, func(b *testing.B, l *list, w *scrollback.Window) {
		b.Helper()
		for b.Loop() {
			w.View(l)
		}
	})
}

// BenchmarkViewScrolledUp is the same with the view anchored on a block.
func BenchmarkViewScrolledUp(b *testing.B) {
	bench(b, func(b *testing.B, l *list, w *scrollback.Window) {
		b.Helper()
		w.Scroll(l, -40)
		for b.Loop() {
			w.Sync(l)
			w.View(l)
		}
	})
}

// BenchmarkTail is a streamed batch: the turn in flight grew by a line.
func BenchmarkTail(b *testing.B) {
	bench(b, func(b *testing.B, l *list, w *scrollback.Window) {
		b.Helper()
		tail := tailLines(64)
		n := 0
		for b.Loop() {
			n = n%len(tail) + 1
			w.SetTail(tail[:n])
			w.View(l)
		}
	})
}

// BenchmarkChange is a block in view changing, as when a message settles.
func BenchmarkChange(b *testing.B) {
	bench(b, func(b *testing.B, l *list, w *scrollback.Window) {
		b.Helper()
		last := len(l.blocks) - 1
		for b.Loop() {
			l.drawn = l.drawn[:0]
			l.set(last, 3)
			w.Sync(l)
			w.View(l)
		}
	})
}

// BenchmarkScroll is a page up and a page back down.
func BenchmarkScroll(b *testing.B) {
	bench(b, func(b *testing.B, l *list, w *scrollback.Window) {
		b.Helper()
		for b.Loop() {
			w.Scroll(l, -22)
			w.View(l)
			w.Scroll(l, 22)
			w.View(l)
		}
	})
}

// BenchmarkResize is a change of width, which draws every block in view
// again.
func BenchmarkResize(b *testing.B) {
	bench(b, func(b *testing.B, l *list, w *scrollback.Window) {
		b.Helper()
		width := 100
		for b.Loop() {
			l.drawn = l.drawn[:0]
			width = 180 - width
			w.Resize(width, 23)
			w.View(l)
		}
	})
}
