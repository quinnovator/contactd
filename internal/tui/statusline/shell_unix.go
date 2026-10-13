//go:build unix

package statusline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

// NewShell returns the Runner that runs a command with /bin/sh -c.
//
// The command runs in a session of its own, and so in a process group of
// its own and with no terminal: it cannot write to the screen or ask the
// person a question behind the client's back. Its input is empty, its error
// output is dropped, and the first 4 KiB of its output are kept.
//
// A run lasts until the command has ended and everything it started has let
// go of its output. When that takes longer than the timeout, or the context
// ends first, the whole group is killed, so that what the command started
// goes with it, and Run returns an error and no output. A process the
// command left behind with its output sent elsewhere is not waited for and
// not killed. A timeout of zero or less is one second.
//
// Off Unix the Runner runs nothing: every Run is an error that says shell
// segments are not supported.
func NewShell() Runner { return shell{} }

type shell struct{}

func (shell) Run(ctx context.Context, command string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	// Told apart from a deadline of the caller's, which is not this
	// command's timeout.
	tooLong := errors.New("the command's own timeout")
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, tooLong)
	defer cancel()

	out, err := run(ctx, command)
	switch {
	case err == nil:
		return string(out), nil
	case errors.Is(context.Cause(ctx), tooLong):
		return "", fmt.Errorf("the command took longer than %s: %w", timeout, context.DeadlineExceeded)
	case ctx.Err() != nil:
		return "", fmt.Errorf("the command was stopped: %w", ctx.Err())
	}
	return "", fmt.Errorf("the command failed: %w", err)
}

// run starts the command and waits for it and for its output to end. When
// ctx ends first it kills the command's group and returns an error.
func run(ctx context.Context, command string) ([]byte, error) {
	// The pipe is this function's own, not one os/exec makes: os/exec stops
	// looking at the context once the shell has ended, and would then wait
	// on a process the shell left holding the pipe.
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err //nolint:wrapcheck // Run says what failed
	}
	var killed atomic.Bool
	// Stdin and Stderr are left nil, which is the null device.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command) //nolint:gosec // running the user's own command is what a shell segment is for
	// A new session is a new process group too, led by the shell, and has
	// no controlling terminal: /dev/tty cannot be opened from it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		killed.Store(true)
		return killGroup(cmd.Process.Pid)
	}
	cmd.Stdout = w
	err = cmd.Start()
	w.Close() //nolint:errcheck,gosec // the command has its own copy; this one was never written to
	if err != nil {
		r.Close()       //nolint:errcheck,gosec // nothing was written
		return nil, err //nolint:wrapcheck // Run says what failed
	}
	read := make(chan []byte, 1)
	go func() {
		var out capped
		io.Copy(&out, r) //nolint:errcheck,gosec // a read that fails ends the output; what came before it counts
		read <- out
	}()

	// Wait returns when the shell has ended: by itself, or killed with its
	// group because ctx ended.
	err = cmd.Wait()
	select {
	case out := <-read:
		r.Close()       //nolint:errcheck,gosec // read to its end
		return out, err //nolint:wrapcheck // Run says what failed
	case <-ctx.Done():
	}
	// The shell has ended and something it started still holds the output.
	// Unless the group was killed a moment ago, that process is alive: it
	// goes now. Closing the pipe ends the read whatever holds the other end.
	if !killed.Load() {
		killGroup(cmd.Process.Pid) //nolint:errcheck,gosec // nothing left to kill is fine
	}
	r.Close() //nolint:errcheck,gosec // what was read is not returned
	<-read
	if err == nil {
		err = ctx.Err()
	}
	return nil, err //nolint:wrapcheck // Run says what failed
}

// killGroup kills every process of the group a command leads. The group's
// id is the pid of the shell that leads it. It returns os.ErrProcessDone
// when the group has no process left.
func killGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.ESRCH):
		return os.ErrProcessDone
	}
	return fmt.Errorf("kill the command's process group: %w", err)
}

// capped keeps the first maxOutput bytes written to it and takes the rest
// without keeping them, so that a command that prints a lot is not held up.
type capped []byte

func (c *capped) Write(p []byte) (int, error) {
	if room := maxOutput - len(*c); room > 0 {
		*c = append(*c, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
