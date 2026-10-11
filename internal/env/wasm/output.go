package wasm

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
)

// statusLen is the width of the zero-padded exit status in a marker.
const statusLen = 3

// clip keeps one output stream within a byte budget. When the guest prints
// more than the budget it keeps the first half and the last half, because the
// end of a stream is where an exception's message lands, and says how much
// went missing in between.
type clip struct {
	head  []byte // the first limit-limit/2 bytes
	tail  []byte // a sliding window over the newest limit/2 bytes past head
	total int64
	limit int
}

func (c *clip) write(p []byte) {
	c.total += int64(len(p))
	if room := c.limit - c.limit/2 - len(c.head); room > 0 {
		n := min(room, len(p))
		c.head = append(c.head, p[:n]...)
		p = p[n:]
	}
	window := c.limit / 2
	if len(p) > window {
		p = p[len(p)-window:] // nothing older can survive
	}
	c.tail = append(c.tail, p...)
	// Trim lazily, so a guest printing in small pieces costs amortised O(1)
	// per byte while the buffer stays under twice the window.
	if len(c.tail) > 2*window {
		c.tail = append(c.tail[:0], c.tail[len(c.tail)-window:]...)
	}
}

func (c *clip) String() string {
	tail := c.tail
	if window := c.limit / 2; len(tail) > window {
		tail = tail[len(tail)-window:]
	}
	var b strings.Builder
	b.Write(c.head)
	if dropped := c.total - int64(len(c.head)) - int64(len(tail)); dropped > 0 {
		fmt.Fprintf(&b, "\n[... %d bytes of output truncated ...]\n", dropped)
	}
	b.Write(tail)
	// Guest bytes are arbitrary and a cut can land inside a rune; the result
	// ends up in JSON.
	return strings.ToValidUTF8(b.String(), "�")
}

// console receives everything a guest writes to stdout and stderr. Both
// streams are the guest's own: the host captures them directly, so output
// written below the interpreter (a C extension, os.write) is caught and
// capped like any other.
//
// The one thing the driver says to the host travels on stdout too: the
// end-of-request marker, "\x00<token>:<status>\n". The token is random and
// fresh per request, so nothing the code prints can be mistaken for it by
// accident. Code that digs the token out of the driver and prints the marker
// itself gains nothing: see mailbox.
type console struct {
	mu     sync.Mutex
	stdout clip
	stderr clip
	// marker is "\x00<token>:" while a request is in flight, nil otherwise.
	// Stdout is only kept while it is set.
	marker []byte
	// live says stderr is being kept: while the guest boots, so a failed
	// start can be explained, and while a request is in flight.
	live bool
	// carry holds back the end of stdout while it could still turn out to be
	// the start of the marker.
	carry []byte
	done  chan int
}

// bootStderr is how much stderr is kept while a guest starts.
const bootStderr = 4096

func newConsole() *console {
	return &console{stderr: clip{limit: bootStderr}, live: true}
}

// arm starts capturing for one request. The returned channel yields the exit
// status when the marker for token arrives.
func (c *console) arm(token string, limit int) <-chan int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stdout = clip{limit: limit}
	c.stderr = clip{limit: limit}
	c.marker = []byte("\x00" + token + ":")
	c.carry = nil
	c.live = true
	c.done = make(chan int, 1)
	return c.done
}

// collect ends the capture and returns what was printed.
func (c *console) collect() (stdout, stderr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.marker != nil { // the request ended without a marker
		c.stdout.write(c.carry)
		c.marker, c.carry = nil, nil
	}
	c.live = false
	return c.stdout.String(), c.stderr.String()
}

// stderrTail returns the end of stderr without ending the capture, for
// describing a guest that died before it was ready.
func (c *console) stderrTail(n int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stderr.String()
	if len(s) > n {
		s = strings.ToValidUTF8(s[len(s)-n:], "")
	}
	return strings.TrimSpace(s)
}

func (c *console) writeStdout(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.marker == nil {
		return // between requests there is nobody to print to
	}
	data := p
	if len(c.carry) > 0 {
		data = append(c.carry, p...)
		c.carry = nil
	}
	for {
		i := bytes.Index(data, c.marker)
		if i < 0 {
			break
		}
		rest := data[i+len(c.marker):]
		if len(rest) < statusLen+1 {
			// The marker is still arriving.
			c.stdout.write(data[:i])
			c.carry = bytes.Clone(data[i:])
			return
		}
		if status, ok := parseStatus(rest[:statusLen+1]); ok {
			c.stdout.write(data[:i])
			c.marker, c.live = nil, false
			c.done <- status // buffered, and the marker is now disarmed
			return
		}
		// The token followed by something else is just output.
		c.stdout.write(data[:i+len(c.marker)])
		data = rest
	}
	keep := overlap(data, c.marker)
	c.stdout.write(data[:len(data)-keep])
	c.carry = bytes.Clone(data[len(data)-keep:])
}

func (c *console) writeStderr(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.live {
		c.stderr.write(p)
	}
}

// parseStatus reads "ddd\n".
func parseStatus(b []byte) (int, bool) {
	if b[statusLen] != '\n' {
		return 0, false
	}
	n := 0
	for _, d := range b[:statusLen] {
		if d < '0' || d > '9' {
			return 0, false
		}
		n = n*10 + int(d-'0')
	}
	return n, true
}

// overlap returns the length of the longest suffix of data that is a proper
// prefix of marker.
func overlap(data, marker []byte) int {
	for n := min(len(data), len(marker)-1); n > 0; n-- {
		if bytes.HasSuffix(data, marker[:n]) {
			return n
		}
	}
	return 0
}

// stdoutWriter and stderrWriter adapt a console to the two io.Writers wazero
// wants.
type stdoutWriter struct{ c *console }

func (w stdoutWriter) Write(p []byte) (int, error) {
	w.c.writeStdout(p)
	return len(p), nil
}

type stderrWriter struct{ c *console }

func (w stderrWriter) Write(p []byte) (int, error) {
	w.c.writeStderr(p)
	return len(p), nil
}
