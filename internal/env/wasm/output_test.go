package wasm

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClip(t *testing.T) {
	t.Parallel()
	const limit = 100
	for name, tc := range map[string]struct {
		in      string
		want    string // exact output, when nothing is dropped
		dropped string // otherwise, the count the marker must report
	}{
		"empty":       {in: "", want: ""},
		"under":       {in: "hello", want: "hello"},
		"at limit":    {in: strings.Repeat("a", limit), want: strings.Repeat("a", limit)},
		"one over":    {in: strings.Repeat("a", limit+1), dropped: "1 bytes"},
		"far over":    {in: "HEAD" + strings.Repeat("a", 100000) + "TAIL", dropped: "99908 bytes"},
		"cut in rune": {in: strings.Repeat("é", 500), dropped: "900 bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// The result must not depend on how the guest chunks its writes.
			for _, chunk := range []int{1, 7, 64, 1 << 20} {
				c := clip{limit: limit}
				for rest := []byte(tc.in); len(rest) > 0; {
					n := min(chunk, len(rest))
					c.write(rest[:n])
					rest = rest[n:]
				}
				got := c.String()
				if !utf8.ValidString(got) {
					t.Errorf("chunk %d: output is not valid UTF-8", chunk)
				}
				if tc.dropped == "" {
					if got != tc.want {
						t.Errorf("chunk %d: got %q, want %q", chunk, got, tc.want)
					}
					continue
				}
				head, tail := tc.in[:limit/2], tc.in[len(tc.in)-limit/2:]
				marker := "\n[... " + tc.dropped + " of output truncated ...]\n"
				// A rune split by the cut becomes U+FFFD; compare the intact part.
				if !strings.Contains(got, marker) ||
					!strings.HasPrefix(got, strings.ToValidUTF8(head, "")[:limit/2-2]) ||
					!strings.HasSuffix(got, strings.ToValidUTF8(tail, "")[2:]) {
					t.Errorf("chunk %d: got %q, want head + %q + tail", chunk, got, marker)
				}
				if len(c.head)+cap(c.tail) > 4*limit+chunk {
					t.Errorf("chunk %d: buffers grew to %d+%d bytes for a %d byte limit", chunk, len(c.head), cap(c.tail), limit)
				}
			}
		})
	}
}

func TestConsoleFindsTheMarkerHoweverItIsSplit(t *testing.T) {
	t.Parallel()
	const token = "TOKENTOKENTOKENTOKENTOKEN0"
	marker := "\x00" + token + ":"
	for name, tc := range map[string]struct {
		stream string
		stdout string
		status int
		done   bool
	}{
		"plain":            {"hello\n" + marker + "000\n", "hello\n", 0, true},
		"status":           {marker + "042\n", "", 42, true},
		"no newline":       {"partial" + marker + "001\n", "partial", 1, true},
		"never ends":       {"hello\n", "hello\n", 0, false},
		"marker cut short": {"a" + marker + "00", "a" + marker + "00", 0, false},
		"wrong token":      {"\x00" + strings.Repeat("X", len(token)) + ":000\n" + marker + "000\n", "\x00" + strings.Repeat("X", len(token)) + ":000\n", 0, true},
		"bad status":       {marker + "0x0\n" + marker + "007\n", marker + "0x0\n", 7, true},
		"token in output":  {token + marker[:10] + "\n" + marker + "000\n", token + marker[:10] + "\n", 0, true},
		"nul bytes":        {"\x00\x00\x00" + marker + "000\n", "\x00\x00\x00", 0, true},
		"after the marker": {"a" + marker + "000\nignored", "a", 0, true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for chunk := 1; chunk <= len(tc.stream); chunk++ {
				c := newConsole()
				done := c.arm(token, 1<<20)
				for rest := []byte(tc.stream); len(rest) > 0; {
					n := min(chunk, len(rest))
					c.writeStdout(rest[:n])
					rest = rest[n:]
				}
				select {
				case status := <-done:
					if !tc.done || status != tc.status {
						t.Errorf("chunk %d: marker reported status %d; want done=%v status %d", chunk, status, tc.done, tc.status)
					}
				default:
					if tc.done {
						t.Errorf("chunk %d: marker not seen", chunk)
					}
				}
				if stdout, _ := c.collect(); stdout != tc.stdout {
					t.Errorf("chunk %d: stdout %q, want %q", chunk, stdout, tc.stdout)
				}
			}
		})
	}
}

func TestConsoleKeepsOutputOnlyWhileSomeoneIsListening(t *testing.T) {
	t.Parallel()
	c := newConsole()
	// Booting: stderr is kept to explain a failed start, stdout has no reader.
	c.writeStdout([]byte("banner\n"))
	c.writeStderr([]byte("cannot find stdlib\n"))
	if got := c.stderrTail(512); got != "cannot find stdlib" {
		t.Errorf("boot stderr = %q", got)
	}

	done := c.arm("T", 1<<10)
	c.writeStdout([]byte("out\n"))
	c.writeStderr([]byte("err\n"))
	c.writeStdout([]byte("\x00T:000\n"))
	if status := <-done; status != 0 {
		t.Errorf("status = %d", status)
	}
	// Idle: a guest that prints between requests prints into the void.
	c.writeStdout([]byte("late\n"))
	c.writeStderr([]byte("late\n"))
	if stdout, stderr := c.collect(); stdout != "out\n" || stderr != "err\n" {
		t.Errorf("collect = %q, %q", stdout, stderr)
	}

	// A request that never finishes still yields what it printed, including
	// bytes held back as a possible marker.
	c.arm("T", 1<<10)
	c.writeStdout([]byte("partial\x00T"))
	if stdout, _ := c.collect(); stdout != "partial\x00T" {
		t.Errorf("unfinished request: stdout %q", stdout)
	}
}
