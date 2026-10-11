//go:build cgo && ORT && darwin

package hugot

import (
	"os"
	"strings"
	"syscall"
	"testing"
)

// TestMain re-executes the test binary with Go's signal-based goroutine
// preemption turned off.
//
// On Apple Silicon with SME (M4 and later) ONNX Runtime 1.24 runs its matrix
// kernels in SME streaming mode, and a signal delivered to a thread in the
// middle of one corrupts the result: slightly for fp32, by several logits for
// int8. It reproduces from Python with no Go involved, and not on Linux. The
// Go runtime preempts with SIGURG, which is enough for about one run in fifty
// of these tests to see a wrong score. The setting is read once at startup
// and is not a //go:debug key, hence the exec. A contactd run natively on
// such a Mac needs GODEBUG=asyncpreemptoff=1 too.
func TestMain(m *testing.M) {
	const off = "asyncpreemptoff=1"
	if dbg := os.Getenv("GODEBUG"); !strings.Contains(dbg, off) {
		exe, err := os.Executable()
		if err == nil && os.Setenv("GODEBUG", strings.TrimPrefix(dbg+","+off, ",")) == nil {
			// Only returns on failure; then run as we are.
			_ = syscall.Exec(exe, os.Args, os.Environ())
		}
	}
	os.Exit(m.Run())
}
