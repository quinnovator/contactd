package infer

import (
	"runtime"
	"strings"
)

// SignalHazard returns a warning when local inference is about to run on a
// platform where it is known to be unreliable, and "" otherwise. godebug is
// the process's GODEBUG setting.
//
// ONNX Runtime 1.24 on Apple Silicon returns perturbed scores when a signal
// is delivered mid-inference (observed with int8 models off by a whole logit;
// the same happens in pure Python, so it is not this code). Go delivers
// SIGURG constantly to preempt goroutines. Linux is not affected. Until the
// runtime is fixed, native macOS runs need async preemption turned off, and
// CPU profiling avoided.
func SignalHazard(godebug string) string {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" || strings.Contains(godebug, "asyncpreemptoff=1") {
		return ""
	}
	return "ONNX Runtime on Apple Silicon miscomputes when interrupted by a signal; " +
		"set GODEBUG=asyncpreemptoff=1 or embedding and rerank scores will be noisy"
}
