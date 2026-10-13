//go:build unix

package statusline_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/tui/statusline"
)

// A sleeping command is killed at its timeout. Only the error is checked:
// how long it took is not.
//
// A run is not over while something the command started holds its output,
// so a command that leaves a sleeping process behind runs into its timeout
// too, whether the shell itself ended well or badly.
func TestShellKillsAtTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		command string
	}{
		{"a sleeping command", "echo before; sleep 300; echo after"},
		{"a process left holding the output", "sleep 300 & echo before"},
		{"the same, from a command that failed", "sleep 300 & exit 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, err := statusline.NewShell().Run(t.Context(), tt.command, 50*time.Millisecond)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("the error is %v, want one that wraps context.DeadlineExceeded", err)
			}
			if want := "the command took longer than 50ms"; !strings.Contains(err.Error(), want) {
				t.Errorf("the error is %q, want it to say %q", err, want)
			}
			if out != "" {
				t.Errorf("a run that timed out returned output: %q", out)
			}
			// The segment then shows its name and a question mark.
			seg := statusline.Segment{Name: "slow", From: statusline.FromShell}
			if got, _ := seg.Text(statusline.ShellValue(seg, out, err, false)); got != "slow ?" {
				t.Errorf("the segment shows %q", got)
			}
		})
	}
}

// gated is how a test talks to a command while it runs: the command writes
// to a FIFO the test reads, and waits at a second one, the gate, until the
// test writes a line to it. Opening a FIFO blocks until the other end is
// open, so the test waits on no clock.
type gated struct {
	fifo, gatePath string
	gate           *os.File
}

func newGated(t *testing.T) gated {
	t.Helper()
	dir := t.TempDir()
	g := gated{fifo: filepath.Join(dir, "fifo"), gatePath: filepath.Join(dir, "gate")}
	for _, path := range []string{g.fifo, g.gatePath} {
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Held open for reading and writing, so that the command's open of the
	// gate never blocks and what is written to it later is kept for it.
	gate, err := os.OpenFile(g.gatePath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gate.Close() }) //nolint:errcheck // a FIFO of the test's own; nothing is lost
	g.gate = gate
	return g
}

// run runs the script aside and returns where Run's error will arrive.
func (g gated) run(ctx context.Context, script string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := statusline.NewShell().Run(ctx, script, 10*time.Minute)
		// Had the command never opened the FIFO, the test would still be
		// waiting to open it. Opening the other end lets it go on and fail.
		if w, openErr := os.OpenFile(g.fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); openErr == nil {
			w.Close() //nolint:errcheck // nothing was written
		}
		done <- err
	}()
	return done
}

// open opens the FIFO for reading. It blocks until the command has opened
// its end.
func (g gated) open(t *testing.T) *os.File {
	t.Helper()
	r, err := os.Open(g.fifo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() }) //nolint:errcheck // read only
	return r
}

// started reads the word a command's child writes once it is running.
func started(t *testing.T, r io.Reader, done <-chan error) {
	t.Helper()
	word := make([]byte, len("started\n"))
	if _, err := io.ReadFull(r, word); err != nil || string(word) != "started\n" {
		t.Fatalf("the child did not start: read %q, %v; the run: %v", word, err, <-done)
	}
}

// outlived opens the gate and reads what the command's child writes after
// it. A child that outlived the kill writes "late" and ends; one that did
// not has already let go of the FIFO. Either way the read ends, so the test
// cannot pass by waiting.
func (g gated) outlived(t *testing.T, r io.Reader) {
	t.Helper()
	if _, err := g.gate.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 {
		t.Errorf("after the kill the child wrote %q: it was still running", rest)
	}
}

// The kill reaches what the command started, not only the shell: the
// command leads a process group, and the group is killed. Here it is the
// context that ends; a timeout kills the same way.
func TestShellKillsTheWholeGroup(t *testing.T) {
	t.Parallel()
	g := newGated(t)
	// The shell waits for its child. The child says "started" when it has
	// the FIFO open, and from there on starts no process of its own: read
	// is part of the shell.
	script := "exec 3>" + shellQuote(g.fifo) +
		"; (echo started >&3; read x <" + shellQuote(g.gatePath) + "; echo late >&3) & wait"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := g.run(ctx, script)
	r := g.open(t)
	started(t, r, done)

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("the error is %v, want one that wraps context.Canceled", err)
	}
	g.outlived(t, r)
}

// The kill also reaches what the shell left behind when it ended. Here the
// shell does not wait for its child, which holds the command's output. The
// child says "started" only once the shell is gone: kill -0 fails when Run
// has seen the shell end. Run is then waiting for the output alone, and
// ending the context must still kill the child.
func TestShellKillsWhatOutlivesTheShell(t *testing.T) {
	t.Parallel()
	g := newGated(t)
	// In the child $$ is still the shell that started it.
	script := "exec 3>" + shellQuote(g.fifo) +
		"; (while kill -0 $$ 2>/dev/null; do :; done; echo started >&3; read x <" +
		shellQuote(g.gatePath) + "; echo late >&3) &"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := g.run(ctx, script)
	r := g.open(t)
	started(t, r, done)

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("the error is %v, want one that wraps context.Canceled", err)
	}
	g.outlived(t, r)
}

// shellQuote writes a path as one word of a shell command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func TestShellRun(t *testing.T) {
	t.Parallel()
	sh := statusline.NewShell()
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"what the command printed", "echo main", "main\n"},
		{"it is a shell: pipes and variables work", `x=3; echo "up $x days" | tr a-z A-Z`, "UP 3 DAYS\n"},
		{"several lines", "printf 'one\\ntwo\\n'", "one\ntwo\n"},
		{"no output", "true", ""},
		{"its input is empty, so a command that reads ends", "cat; echo done", "done\n"},
		{"its error output is dropped", "echo oops >&2; echo fine", "fine\n"},
		{"a timeout of zero is one second, not none", "echo quick", "quick\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			timeout := 30 * time.Second
			if strings.HasPrefix(tt.name, "a timeout of zero") {
				timeout = 0
			}
			got, err := sh.Run(t.Context(), tt.command, timeout)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got != tt.want {
				t.Errorf("the output is %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("the first 4 KiB of the output are kept", func(t *testing.T) {
		t.Parallel()
		got, err := sh.Run(t.Context(), "head -c 100000 /dev/zero | tr '\\0' x", 30*time.Second)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got != strings.Repeat("x", 4096) {
			t.Errorf("kept %d bytes, want 4096 of x", len(got))
		}
	})

	t.Run("a command that fails is an error", func(t *testing.T) {
		t.Parallel()
		got, err := sh.Run(t.Context(), "echo partial; exit 3", 30*time.Second)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 3 {
			t.Errorf("the error is %v, want one that wraps exit status 3", err)
		}
		if got != "" {
			t.Errorf("a failed run returned output: %q", got)
		}
	})

	t.Run("a command that is not there is an error", func(t *testing.T) {
		t.Parallel()
		if _, err := sh.Run(t.Context(), "contactd-no-such-command-anywhere", 30*time.Second); err == nil {
			t.Error("no error")
		}
	})

	t.Run("a deadline of the caller's is not the command's timeout", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
		defer cancel()
		_, err := sh.Run(ctx, "echo late", 30*time.Second)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the error is %v, want one that wraps context.DeadlineExceeded", err)
		}
		if want := "the command was stopped"; !strings.Contains(err.Error(), want) {
			t.Errorf("the error is %q, want it to say %q", err, want)
		}
	})

	t.Run("a context that has ended runs nothing", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		marker := filepath.Join(t.TempDir(), "ran")
		_, err := sh.Run(ctx, "touch "+shellQuote(marker), 30*time.Second)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the error is %v, want one that wraps context.Canceled", err)
		}
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Error("the command ran")
		}
	})
}
