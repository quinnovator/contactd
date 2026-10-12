package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// shortDir is a temporary directory with a path short enough to hold a
// socket: an address has room for about a hundred bytes.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ctd") //nolint:usetesting // t.TempDir is too long a path for a socket address
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestListenSocket(t *testing.T) {
	t.Parallel()
	path := filepath.Join(shortDir(t), "contactd.sock")

	first, err := listenSocket(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket permissions = %o, want 600: the file is the access control", perm)
	}

	// A second server on the same data directory is turned away while the
	// first is serving.
	if second, err := listenSocket(t.Context(), path); err == nil {
		_ = second.Close()
		t.Fatal("a second listener took a socket that was in use")
	}
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("the first listener stopped answering: %v", err)
	}
	_ = conn.Close()

	// A file left behind by a server that died is not in the way.
	first.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the stale socket file should still be there: %v", err)
	}
	again, err := listenSocket(t.Context(), path)
	if err != nil {
		t.Fatalf("a stale socket file blocked a new server: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Error(err)
	}
	// A clean stop takes the file with it.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the socket file outlived its listener: %v", err)
	}
}

func TestToolPresets(t *testing.T) {
	t.Parallel()
	tools, err := toolPresets("python, js")
	if err != nil || len(tools) != 2 || tools[0].Env.Base != "python-3.12" || tools[1].Env.Base != "quickjs" {
		t.Errorf("toolPresets = %+v, %v", tools, err)
	}
	if tools, err := toolPresets(""); err != nil || len(tools) != 0 {
		t.Errorf("no tools = %+v, %v", tools, err)
	}
	if _, err := toolPresets("python,ruby"); err == nil {
		t.Error("an unknown tool was accepted")
	}
}
