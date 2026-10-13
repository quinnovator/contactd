package tuitest

import (
	"sync"
	"testing"
	"time"
)

const (
	// quietWindow is how long a program must write nothing to count as idle.
	// Bubble Tea draws at most 60 frames a second, so this is several frames.
	quietWindow = 60 * time.Millisecond
	// quietLimit is how long Quiet waits for a program to fall idle.
	quietLimit = 5 * time.Second
	// quietStep is how often Quiet looks.
	quietStep = 5 * time.Millisecond
)

// Recorder is an io.Writer that keeps what a tea.Program wrote. Safe for
// concurrent use. The zero value is ready.
type Recorder struct {
	mu  sync.Mutex
	buf []byte
}

// Write keeps p. It never fails.
func (r *Recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	return len(p), nil
}

// Mark returns a position to read from later: the number of bytes written
// so far.
func (r *Recorder) Mark() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buf)
}

// Since returns everything written after mark. The slice is a copy. A mark
// of zero gives everything; a mark past the end gives nothing.
func (r *Recorder) Since(mark int) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	mark = min(max(mark, 0), len(r.buf))
	out := make([]byte, len(r.buf)-mark)
	copy(out, r.buf[mark:])
	return out
}

// Quiet returns once nothing was written for 60 ms; it fails t after 5 s.
// The 60 ms are counted from the call, so a message sent just before it has
// time to be drawn. Call it from the test's own goroutine: it stops the test
// when the program never falls idle.
func (r *Recorder) Quiet(t *testing.T) {
	t.Helper()
	r.settle(t, time.Now, time.Sleep)
}

// stopper is the part of a *testing.T that Quiet uses.
type stopper interface {
	Helper()
	Fatalf(format string, args ...any)
}

// settle is Quiet on a clock of the caller's choice.
func (r *Recorder) settle(t stopper, now func() time.Time, sleep func(time.Duration)) {
	t.Helper()
	if !r.quiet(quietWindow, quietLimit, now, sleep) {
		t.Fatalf("the program was still writing after %v", quietLimit)
	}
}

// quiet waits until nothing was written for window, and reports whether
// that happened within limit. The clock is passed in so that a test can
// drive it.
func (r *Recorder) quiet(window, limit time.Duration, now func() time.Time, sleep func(time.Duration)) bool {
	start := now()
	last, size := start, r.Mark()
	for {
		sleep(quietStep)
		t := now()
		if n := r.Mark(); n != size {
			last, size = t, n
		}
		if t.Sub(last) >= window {
			return true
		}
		if t.Sub(start) >= limit {
			return false
		}
	}
}
