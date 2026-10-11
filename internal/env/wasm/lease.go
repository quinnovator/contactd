package wasm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero/sys"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/turn"
)

// What the agent is told about persistence. The wording is plain on purpose:
// it is read by a model deciding whether to re-run its setup.
const (
	noteResumed   = "This is a fresh interpreter. Files in the working directory persisted from earlier use; interpreter variables, imports and definitions did not."
	noteRestarted = "The interpreter was restarted after the previous run was stopped or crashed. Files in the working directory persisted; interpreter variables, imports and definitions did not."
	noteLost      = "This is a fresh interpreter with an empty working directory. Neither files nor interpreter variables from earlier use are available."
	whatSurvives  = "A fresh interpreter starts on the next run: files in the working directory are kept, variables are not."
)

// exitTimeout is the exit status reported for code stopped by ExecTimeout,
// following timeout(1).
const exitTimeout = 124

// lease is one contact's hold on a runtime. It owns at most one instance at a
// time and replaces it lazily when it dies.
type lease struct {
	p       *Provider
	img     *image
	contact core.ContactID
	dir     string

	// execMu makes Exec serial. Callers already promise that; this turns a
	// broken promise into a wait instead of interleaved requests.
	execMu sync.Mutex

	// mu guards the fields below. Release may arrive from another goroutine
	// while an Exec is in flight.
	mu       sync.Mutex
	inst     *instance
	note     string // owed to the agent on the next successful Exec
	released bool
}

// Exec runs code in the lease's interpreter.
//
// It returns an error only when the environment itself failed or the caller
// gave up. Code that raises, exits, crashes the interpreter or runs past
// ExecTimeout gets an ExecResult describing what happened.
func (l *lease) Exec(ctx context.Context, code string) (turn.ExecResult, error) {
	l.execMu.Lock()
	defer l.execMu.Unlock()

	caller := ctx
	ctx, cancel := context.WithTimeout(ctx, l.p.cfg.ExecTimeout)
	defer cancel()

	inst, err := l.instance(ctx)
	if err != nil {
		return turn.ExecResult{}, fmt.Errorf("wasm exec: %w", err)
	}
	status, how := inst.run(ctx, code, l.p.cfg.MaxOutputBytes)
	stdout, stderr := inst.out.collect()
	res := turn.ExecResult{Stdout: stdout, Stderr: stderr, ExitCode: status}
	if how == finished {
		res.Note = l.takeNote()
		return res, nil
	}

	// The instance is dead or has to be. Its successor starts on the next
	// Exec, which is also when the agent is told.
	stopped := inst.kill()
	if !stopped {
		l.p.log.WarnContext(ctx, "wasm instance did not stop", slog.Uint64("contact_id", uint64(l.contact)))
	}
	note := l.takeNote()
	switch released := l.retire(inst); {
	case released:
		return turn.ExecResult{}, fmt.Errorf("wasm exec: lease released: %w", core.ErrClosed)
	case how == abandoned && caller.Err() != nil:
		return turn.ExecResult{}, fmt.Errorf("wasm exec: %w", context.Cause(caller))
	case how == abandoned:
		res.ExitCode = exitTimeout
		res.Stderr = annotate(stderr, fmt.Sprintf("stopped: ran longer than %s", l.p.cfg.ExecTimeout))
	case !stopped:
		// Its exit status is not there to read yet, and may never be.
		res.ExitCode = 1
		res.Stderr = annotate(stderr, "the interpreter stopped responding")
	default:
		res.ExitCode = inst.exitCode()
		res.Stderr = annotate(stderr, describeExit(inst.err))
	}
	res.Note = note
	return res, nil
}

// instance returns the live instance, starting one if the last one died.
func (l *lease) instance(ctx context.Context) (*instance, error) {
	l.mu.Lock()
	inst, released := l.inst, l.released
	l.mu.Unlock()
	if released {
		return nil, fmt.Errorf("lease released: %w", core.ErrClosed)
	}
	if inst != nil {
		select {
		case <-inst.exited: // died while idle; fall through and replace it
			l.retire(inst)
		default:
			return inst, nil
		}
	}

	inst, err := l.p.start(ctx, l.img, l.dir)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		inst.kill()
		return nil, fmt.Errorf("lease released: %w", core.ErrClosed)
	}
	l.inst = inst
	l.mu.Unlock()
	return inst, nil
}

// retire forgets a dead instance and records that the agent must be told its
// variables are gone. It reports whether the lease has been released.
func (l *lease) retire(inst *instance) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inst == inst {
		l.inst = nil
	}
	l.note = noteRestarted
	return l.released
}

func (l *lease) takeNote() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	note := l.note
	l.note = ""
	return note
}

// Snapshot returns the ID to resume from. The working directory is the
// snapshot and is already on disk, so there is nothing to copy.
func (l *lease) Snapshot(context.Context) (core.SnapshotID, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return "", fmt.Errorf("wasm snapshot: lease released: %w", core.ErrClosed)
	}
	return snapshotID(l.contact), nil
}

// Release stops the instance and frees its memory. It is safe to call more
// than once and from any goroutine; an Exec in flight returns core.ErrClosed.
func (l *lease) Release() {
	l.mu.Lock()
	inst := l.inst
	l.inst, l.released = nil, true
	l.mu.Unlock()

	l.p.untrack(l)
	if inst != nil && !inst.kill() {
		l.p.log.Warn("wasm instance did not stop", slog.Uint64("contact_id", uint64(l.contact)))
	}
}

// annotate appends a host remark to what the guest wrote on stderr.
func annotate(stderr, remark string) string {
	if stderr != "" && !strings.HasSuffix(stderr, "\n") {
		stderr += "\n"
	}
	return stderr + "[" + remark + ". " + whatSurvives + "]\n"
}

// describeExit says why a guest stopped serving, in words fit for a tool
// result.
func describeExit(err error) string {
	var exit *sys.ExitError
	if err == nil || errors.As(err, &exit) {
		code := 0
		if exit != nil {
			code = int(exit.ExitCode() & 0xff)
		}
		return fmt.Sprintf("the interpreter exited with status %d", code)
	}
	// A trap. Only its first line: the rest is a wasm stack trace.
	reason, _, _ := strings.Cut(err.Error(), "\n")
	if i := strings.LastIndex(reason, "wasm error: "); i >= 0 {
		reason = reason[i+len("wasm error: "):]
	}
	return "the interpreter crashed: " + reason
}
