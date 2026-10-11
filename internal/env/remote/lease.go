package remote

import (
	"context"
	"fmt"
	"sync"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/turn"
)

// noteRestored is what the agent is told on the first Exec of a resumed
// lease.
const noteRestored = "This environment was restored from a snapshot: files and installed packages are intact. Each run is a new process, so interpreter variables from earlier runs are not available."

// lease is one snapshot chain.
type lease struct {
	p       *Provider
	contact core.ContactID
	base    string
	cmd     []string

	// mu guards the fields below. Exec calls are serial by contract, but
	// Release may come from another goroutine.
	mu sync.Mutex
	// head is the snapshot the next Exec starts from. Empty means no exec
	// has been kept yet, so the next one starts from the environment's
	// template.
	head   string
	note   string
	closed bool
}

// Exec runs code from the head of the chain and advances the head to the
// snapshot the service kept. When it returns an error the head has not
// moved: whatever the failed run did is not part of the lease.
//
// Cancelling ctx drops the request. The API has no cancel call, so stopping
// the code is then up to the service, which sees the connection close and in
// any case enforces timeout_ms.
func (l *lease) Exec(ctx context.Context, code string) (turn.ExecResult, error) {
	l.mu.Lock()
	head, closed := l.head, l.closed
	l.mu.Unlock()
	if closed {
		return turn.ExecResult{}, fmt.Errorf("remote exec: lease released: %w", core.ErrClosed)
	}

	req := execRequest{Snapshot: head, Cmd: l.cmd, Stdin: code, Keep: true}
	if head == "" {
		req.Env = l.base
	}
	out, err := l.p.exec(ctx, req)
	if err != nil {
		return turn.ExecResult{}, fmt.Errorf("remote exec for contact %d: %w", l.contact, err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return turn.ExecResult{}, fmt.Errorf("remote exec: lease released: %w", core.ErrClosed)
	}
	l.head = out.SnapshotID
	res := turn.ExecResult{Stdout: out.Stdout, Stderr: out.Stderr, ExitCode: out.Exit, Note: l.note}
	l.note = ""
	return res, nil
}

// Snapshot pins the head of the chain, so the service's garbage collector
// leaves it alone, and returns it.
//
// A lease that has not kept anything yet has no snapshot. It returns "",
// which is what Lease takes to mean a fresh environment, and that is exactly
// the state such a lease is in.
func (l *lease) Snapshot(ctx context.Context) (core.SnapshotID, error) {
	l.mu.Lock()
	head, closed := l.head, l.closed
	l.mu.Unlock()
	if closed {
		return "", fmt.Errorf("remote snapshot: lease released: %w", core.ErrClosed)
	}
	if head == "" {
		return "", nil
	}
	if err := l.p.pin(ctx, head); err != nil {
		return "", fmt.Errorf("remote snapshot for contact %d: %w", l.contact, err)
	}
	return core.SnapshotID(head), nil
}

// Release closes the lease. The service holds nothing on its behalf except
// snapshots, and those expire on their own unless Snapshot pinned them.
func (l *lease) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
}
