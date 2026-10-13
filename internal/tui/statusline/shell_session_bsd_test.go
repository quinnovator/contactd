//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package statusline_test

import (
	"syscall"
	"testing"
)

// sessionOf returns the id of the session a process is in.
func sessionOf(t *testing.T, pid int) int {
	t.Helper()
	sid, err := syscall.Getsid(pid)
	if err != nil {
		t.Fatalf("the session of %d: %v", pid, err)
	}
	return sid
}
