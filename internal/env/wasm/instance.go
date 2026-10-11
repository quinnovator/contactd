package wasm

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/sys"
)

// killGrace bounds the wait for a cancelled guest to unwind. Compiled code
// checks for cancellation at every loop header and every host call here is
// interruptible, so in practice this takes microseconds.
const killGrace = 5 * time.Second

// mailbox is the guest's stdin: a queue of at most one request.
//
// It also knows something the guest cannot fake: whether the guest is blocked
// reading with nothing left to read. A request counts as finished only when
// its marker has arrived and the guest is parked here again. So code that
// forges its own marker and carries on computing is still on the clock, and
// between requests an instance is provably not running.
type mailbox struct {
	mu     sync.Mutex
	next   []byte // posted, not yet picked up
	buf    []byte // the unread rest of the request being read
	parked bool
	wake   chan struct{} // a request was posted
	idle   chan struct{} // the guest parked
	closed <-chan struct{}
}

func newMailbox(closed <-chan struct{}) *mailbox {
	return &mailbox{
		wake:   make(chan struct{}, 1),
		idle:   make(chan struct{}, 1),
		closed: closed,
	}
}

// Read is called by the guest's fd_read on stdin.
func (m *mailbox) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		m.mu.Lock()
		if len(m.buf) == 0 && m.next != nil {
			m.buf, m.next = m.next, nil
		}
		if len(m.buf) > 0 {
			n := copy(p, m.buf)
			m.buf = m.buf[n:]
			m.mu.Unlock()
			return n, nil
		}
		m.parked = true
		m.mu.Unlock()
		notify(m.idle)

		select {
		case <-m.wake:
		case <-m.closed:
			return 0, io.EOF
		}
	}
}

// post hands the guest its next request. It reports false if the previous
// one was never picked up.
func (m *mailbox) post(frame []byte) bool {
	m.mu.Lock()
	if m.next != nil {
		m.mu.Unlock()
		return false
	}
	m.next = frame
	m.parked = false
	m.mu.Unlock()
	notify(m.wake)
	return true
}

func (m *mailbox) isParked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.parked
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// instance is one running guest: a module instance whose _start is the
// driver's read-eval loop. It lives on its own goroutine for as long as the
// loop runs.
type instance struct {
	stdin  *mailbox
	out    *console
	cancel context.CancelFunc
	exited chan struct{}
	// err is what InstantiateModule returned. Valid once exited is closed.
	err error
}

// start boots a guest and waits until its driver is reading stdin. ctx bounds
// the boot only; the instance outlives it and stops when kill is called.
func (p *Provider) start(ctx context.Context, img *image, dir string) (*instance, error) {
	fsConfig, err := img.fsConfig(dir)
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	inst := &instance{
		stdin:  newMailbox(life.Done()),
		out:    newConsole(),
		cancel: cancel,
		exited: make(chan struct{}),
	}
	stop := life.Done()
	cfg := wazero.NewModuleConfig().
		WithName(""). // anonymous, so any number of instances can coexist
		WithArgs(img.base.Args...).
		WithStdin(inst.stdin).
		WithStdout(stdoutWriter{inst.out}).
		WithStderr(stderrWriter{inst.out}).
		WithFSConfig(fsConfig).
		WithSysWalltime().
		WithSysNanotime().
		// wazero's default random source is deterministic.
		WithRandSource(rand.Reader).
		// A sleeping guest must still die with its context.
		WithNanosleep(func(ns int64) {
			t := time.NewTimer(time.Duration(ns))
			defer t.Stop()
			select {
			case <-t.C:
			case <-stop:
			}
		})
	for k, v := range img.base.Env {
		cfg = cfg.WithEnv(k, v)
	}

	go func() {
		defer close(inst.exited)
		defer cancel()
		// Blocks until the guest returns from _start, is killed through
		// life, or traps.
		mod, err := p.runtime.InstantiateModule(life, img.module, cfg)
		if mod != nil {
			// A guest that returns without calling proc_exit leaves its
			// module, and so its memory, open.
			_ = mod.Close(context.WithoutCancel(life))
		}
		inst.err = err
	}()

	if err := inst.waitParked(ctx); err != nil {
		inst.kill()
		return nil, fmt.Errorf("start guest: %w", err)
	}
	return inst, nil
}

// waitParked returns once the guest is blocked waiting for a request.
func (i *instance) waitParked(ctx context.Context) error {
	for !i.stdin.isParked() {
		select {
		case <-i.stdin.idle:
		case <-i.exited:
			return i.exitError()
		case <-ctx.Done():
			return fmt.Errorf("guest not ready: %w", context.Cause(ctx))
		}
	}
	return nil
}

// outcome is how one request ended.
type outcome uint8

const (
	finished  outcome = iota + 1 // the driver reported a status and is waiting again
	died                         // the guest exited or trapped
	abandoned                    // ctx ended first
)

// run executes one request and reports how it ended. Only finished leaves
// the instance usable. What the code printed is in i.out either way.
func (i *instance) run(ctx context.Context, code string, limit int) (status int, how outcome) {
	token := rand.Text()
	done := i.out.arm(token, limit)

	frame := make([]byte, 0, len(token)+len(code)+16)
	frame = append(frame, token...)
	frame = append(frame, ' ')
	frame = strconv.AppendInt(frame, int64(len(code)), 10)
	frame = append(frame, '\n')
	frame = append(frame, code...)
	if !i.stdin.post(frame) {
		return 0, died // the guest stopped following the protocol
	}

	select {
	case status = <-done:
	case <-i.exited:
		return 0, died
	case <-ctx.Done():
		return 0, abandoned
	}
	// The marker alone is the guest's word. Being parked is the host's.
	for !i.stdin.isParked() {
		select {
		case <-i.stdin.idle:
		case <-i.exited:
			return status, died
		case <-ctx.Done():
			return status, abandoned
		}
	}
	return status, finished
}

// kill stops the guest and reports whether it was seen to stop.
func (i *instance) kill() bool {
	i.cancel()
	t := time.NewTimer(killGrace)
	defer t.Stop()
	// No ctx here: the caller's is typically already done, which is why the
	// guest is being killed. The timer is the bound.
	select {
	case <-i.exited:
		return true
	case <-t.C:
		return false
	}
}

// exitCode is the guest's exit status, valid once exited is closed. A trap is
// reported as 1.
func (i *instance) exitCode() int {
	var exit *sys.ExitError
	switch {
	case i.err == nil:
		return 0
	case errors.As(i.err, &exit):
		return int(exit.ExitCode() & 0xff)
	default:
		return 1
	}
}

// exitError describes a guest that returned when it should have kept
// serving.
func (i *instance) exitError() error {
	detail := i.out.stderrTail(512)
	if detail != "" {
		detail = ": " + detail
	}
	var exit *sys.ExitError
	if i.err != nil && !errors.As(i.err, &exit) {
		return fmt.Errorf("guest crashed: %w%s", i.err, detail)
	}
	return fmt.Errorf("%w with status %d%s", ErrGuestExited, i.exitCode(), detail)
}
