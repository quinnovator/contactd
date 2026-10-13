//go:build linux

package statusline_test

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// sessionOf returns the id of the session a process is in. Package syscall
// has no getsid on Linux; /proc has the same number.
func sessionOf(t *testing.T, pid int) int {
	t.Helper()
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatalf("the session of %d: %v", pid, err)
	}
	// The line is "pid (name) state ppid pgrp session ...", and the name may
	// hold spaces and brackets of its own.
	_, rest, _ := strings.Cut(string(stat), ")")
	for strings.Contains(rest, ")") {
		_, rest, _ = strings.Cut(rest, ")")
	}
	fields := strings.Fields(rest)
	if len(fields) < 4 {
		t.Fatalf("the session of %d: cannot read %q", pid, stat)
	}
	sid, err := strconv.Atoi(fields[3])
	if err != nil {
		t.Fatalf("the session of %d: %v", pid, err)
	}
	return sid
}
