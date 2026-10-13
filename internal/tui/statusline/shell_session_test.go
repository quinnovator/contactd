//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package statusline_test

import (
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The command runs in a session of its own, which the shell leads. A session
// that was never given a terminal has none, so the command cannot open
// /dev/tty to write to the screen or to read what the person types.
//
// The shell says who it is and waits at the gate while the test asks the
// system which session it is in.
func TestShellRunsInItsOwnSession(t *testing.T) {
	t.Parallel()
	g := newGated(t)
	script := "echo $$ >" + shellQuote(g.fifo) + "; read x <" + shellQuote(g.gatePath)
	done := g.run(t.Context(), script)
	said, err := io.ReadAll(g.open(t))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(said)))
	if err != nil {
		t.Fatalf("the shell did not say who it is: read %q; the run: %v", said, <-done)
	}

	if got := sessionOf(t, pid); got != pid {
		t.Errorf("the shell %d is in session %d, want one it leads", pid, got)
	}
	if own := sessionOf(t, os.Getpid()); own == pid {
		t.Errorf("the test is in the shell's session %d", own)
	}

	if _, err := g.gate.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Errorf("Run: %v", err)
	}
}
