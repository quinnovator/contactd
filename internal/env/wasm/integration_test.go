//go:build integration

package wasm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
)

// realBases returns the runtimes scripts/fetch-wasm-runtimes.sh installed,
// from $CONTACTD_WASM_DIR or third_party/wasm at the repo root.
func realBases(tb testing.TB) map[string]Base {
	tb.Helper()
	dir := os.Getenv("CONTACTD_WASM_DIR")
	if dir == "" {
		dir = filepath.Join("..", "..", "..", "third_party", "wasm")
	}
	return DefaultBases(dir)
}

// realProvider compiles one real runtime, or skips the test if it has not
// been fetched. Providers given the same cache directory compile it once
// between them.
func realProvider(tb testing.TB, base, cache string, tune func(*Config)) *Provider {
	tb.Helper()
	b, ok := realBases(tb)[base]
	if !ok {
		tb.Skipf("%s runtime not found; run scripts/fetch-wasm-runtimes.sh (or set CONTACTD_WASM_DIR)", base)
	}
	cfg := Config{
		FSRoot:   tb.TempDir(),
		CacheDir: cache,
		Bases:    map[string]Base{base: b},
		Logger:   obs.Discard(),
	}
	if tune != nil {
		tune(&cfg)
	}
	p, err := New(tb.Context(), cfg)
	if err != nil {
		tb.Fatalf("New: %v", err)
	}
	tb.Cleanup(func() {
		if err := p.Close(context.WithoutCancel(tb.Context())); err != nil {
			tb.Errorf("Close: %v", err)
		}
	})
	return p
}

func TestPython(t *testing.T) {
	t.Parallel()
	const base = "python-3.12"
	spec := core.EnvSpec{Base: base}
	cache := t.TempDir()
	p := realProvider(t, base, cache, nil)

	t.Run("arithmetic", func(t *testing.T) {
		t.Parallel()
		l := openSpec(t, p, 1, spec, "")
		want(t, run(t, l, "print(2 + 2)"), "4\n")
		// The last expression is echoed, as at the interactive prompt.
		want(t, run(t, l, "import math\nmath.factorial(20)"), "2432902008176640000\n")
		want(t, run(t, l, "x = 5"), "")
		want(t, run(t, l, "None"), "")
		want(t, run(t, l, "print('h\u00e9llo \u2713')"), "h\u00e9llo \u2713\n")
		want(t, run(t, l, "import sys; sys.version_info[:2] >= (3, 12)"), "True\n")
	})

	t.Run("state persists within a lease", func(t *testing.T) {
		t.Parallel()
		l := openSpec(t, p, 2, spec, "")
		res := run(t, l, "x = 41\ndef inc(n):\n    return n + 1\nimport json")
		want(t, res, "")
		if res.Note != "" {
			t.Errorf("Note on a brand new environment = %q", res.Note)
		}
		want(t, run(t, l, "print(json.dumps({'answer': inc(x)}))"), "{\"answer\": 42}\n")
	})

	t.Run("files persist across leases and variables do not", func(t *testing.T) {
		t.Parallel()
		l := openSpec(t, p, 3, spec, "")
		want(t, run(t, l, "x = 41\nwith open('notes.txt', 'w') as f:\n    f.write('remember me')\nimport os; os.getcwd()"), "'/'\n")
		id, err := l.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		l.Release()

		l = openSpec(t, p, 3, spec, id)
		res := run(t, l, "print(open('notes.txt').read())")
		want(t, res, "remember me\n")
		if !strings.Contains(res.Note, "Files in the working directory persisted") {
			t.Errorf("Note = %q", res.Note)
		}
		res = run(t, l, "x")
		if res.ExitCode != 1 || !strings.Contains(res.Stderr, "NameError: name 'x' is not defined") {
			t.Errorf("variable survived a new lease: %+v", res)
		}
	})

	t.Run("an exception is stderr and a non-zero exit", func(t *testing.T) {
		t.Parallel()
		l := openSpec(t, p, 4, spec, "")
		want(t, run(t, l, "def half(n):\n    return n / 0"), "")
		res := run(t, l, "print('before')\nhalf(1)")
		if res.ExitCode != 1 || res.Stdout != "before\n" {
			t.Errorf("got %+v, want exit 1 after printing", res)
		}
		for _, part := range []string{
			"Traceback (most recent call last):",
			"half(1)",      // source of this request
			"return n / 0", // source of an earlier one
			"ZeroDivisionError: division by zero",
		} {
			if !strings.Contains(res.Stderr, part) {
				t.Errorf("stderr lacks %q:\n%s", part, res.Stderr)
			}
		}
		if strings.Contains(res.Stderr, "_contactd") {
			t.Errorf("the driver's own frame leaked into the traceback:\n%s", res.Stderr)
		}

		res = run(t, l, "def broken(:")
		if res.ExitCode != 1 || !strings.Contains(res.Stderr, "SyntaxError") {
			t.Errorf("syntax error: %+v", res)
		}
		res = run(t, l, "import sys\nprint('bye')\nsys.exit(3)")
		if res.ExitCode != 3 || res.Stdout != "bye\n" || res.Stderr != "" {
			t.Errorf("sys.exit(3): %+v", res)
		}
		res = run(t, l, "input()")
		if res.ExitCode != 1 || !strings.Contains(res.Stderr, "EOFError") {
			t.Errorf("input(): %+v, want EOFError rather than a hang", res)
		}
		// None of that cost the interpreter its state.
		res = run(t, l, "half.__name__")
		want(t, res, "'half'\n")
		if res.Note != "" {
			t.Errorf("Note after ordinary failures = %q", res.Note)
		}
	})

	t.Run("while True is killed by ctx", func(t *testing.T) {
		t.Parallel()
		l := openSpec(t, p, 5, spec, "")
		want(t, run(t, l, "x = 1\nopen('keep.txt', 'w').write('kept')\nNone"), "")

		for _, code := range []string{"while True: pass", "import time\ntime.sleep(3600)"} {
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			start := time.Now()
			_, err := l.Exec(ctx, code)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Exec(%q) = %v, want context.DeadlineExceeded", code, err)
			}
			if took := time.Since(start); took > 3*time.Second {
				t.Errorf("%q took %s to stop", code, took)
			}
			res := run(t, l, "open('keep.txt').read()")
			want(t, res, "'kept'\n")
			if !strings.Contains(res.Note, "restarted") {
				t.Errorf("Note after a kill = %q", res.Note)
			}
		}
		res := run(t, l, "x")
		if res.ExitCode != 1 || !strings.Contains(res.Stderr, "NameError") {
			t.Errorf("variable survived a restart: %+v", res)
		}
	})

	t.Run("the sandbox holds", func(t *testing.T) {
		t.Parallel()
		l := openSpec(t, p, 6, spec, "")
		want(t, run(t, l, "import os\nsorted(os.environ)"), "[]\n")
		want(t, run(t, l, "open('mine.txt', 'w').write('x')\nos.listdir('/')"), "['mine.txt']\n")
		for _, code := range []string{
			"open('/etc/passwd').read()",
			"open('../../../../../../etc/passwd').read()",
			"os.listdir('/Users')",
			"os.listdir('/home')",
			"os.symlink('/etc', 'etc')",
			"os.symlink('../5', 'neighbour')",
			"import socket\nsocket.create_connection(('example.com', 80), timeout=2)",
			"import urllib.request\nurllib.request.urlopen('http://example.com', timeout=2)",
			"import subprocess\nsubprocess.run(['ls'])",
			"open('/lib/python3.12/os.py', 'w')", // the standard library is read-only
		} {
			if res := run(t, l, code); res.ExitCode == 0 {
				t.Errorf("%q succeeded: %+v", code, res)
			}
		}
		// A request for more memory than the cap is an exception, not a crash.
		res := run(t, l, "big = bytearray(1 << 30)")
		if res.ExitCode != 1 || !strings.Contains(res.Stderr, "MemoryError") {
			t.Errorf("1 GiB allocation: %+v, want MemoryError", res)
		}
		res = run(t, l, "os.listdir('.')")
		want(t, res, "['mine.txt']\n")
		if res.Note != "" {
			t.Errorf("the interpreter did not survive MemoryError: Note = %q", res.Note)
		}
	})

	t.Run("exec timeout keeps what was printed", func(t *testing.T) {
		t.Parallel()
		p := realProvider(t, base, cache, func(c *Config) { c.ExecTimeout = 2 * time.Second })
		l := openSpec(t, p, 1, spec, "")
		res := run(t, l, "print('started')\nwhile True: pass")
		if res.ExitCode != exitTimeout || res.Stdout != "started\n" || !strings.Contains(res.Stderr, "ran longer than 2s") {
			t.Errorf("got %+v, want exit %d with the output so far", res, exitTimeout)
		}
		// Reaching into the driver for the token and printing the marker by
		// hand does not release the request early.
		forge := "import sys, os\ntoken = sys._getframe(1).f_back.f_locals['token']\n" +
			"os.write(1, b'\\x00' + token + b':000\\n')\nwhile True: pass"
		start := time.Now()
		res = run(t, l, forge)
		if res.ExitCode != exitTimeout {
			t.Errorf("forged marker: got %+v, want a timeout", res)
		}
		if took := time.Since(start); took < time.Second {
			t.Errorf("forged marker ended the request after %s", took)
		}
	})

	t.Run("output is capped", func(t *testing.T) {
		t.Parallel()
		l := openSpec(t, p, 7, spec, "")
		res := run(t, l, "for i in range(100000): print('line', i)")
		if res.ExitCode != 0 || len(res.Stdout) > defaultMaxOutputBytes+64 ||
			!strings.HasPrefix(res.Stdout, "line 0\n") || !strings.HasSuffix(res.Stdout, "line 99999\n") ||
			!strings.Contains(res.Stdout, "bytes of output truncated") {
			t.Errorf("exit %d, %d bytes of stdout", res.ExitCode, len(res.Stdout))
		}
	})
}

func TestQuickJS(t *testing.T) {
	t.Parallel()
	const base = "quickjs"
	spec := core.EnvSpec{Base: base}
	p := realProvider(t, base, t.TempDir(), nil)

	t.Run("arithmetic", func(t *testing.T) {
		t.Parallel()
		l := openSpec(t, p, 1, spec, "")
		want(t, run(t, l, "console.log(2 + 2)"), "4\n")
		want(t, run(t, l, "[1, 2, 3].map((n) => n * 2)"), "[2,4,6]\n")
		want(t, run(t, l, "let quiet = 1"), "")
		want(t, run(t, l, "print('h\u00e9llo \u2713')"), "h\u00e9llo \u2713\n")
	})

	t.Run("state persists within a lease", func(t *testing.T) {
		t.Parallel()
		l := openSpec(t, p, 2, spec, "")
		want(t, run(t, l, "var x = 41; let y = 1; const inc = (n) => n + y; function twice(n) { return 2 * n }"), "")
		want(t, run(t, l, "console.log(twice(inc(x)))"), "84\n")
	})

	t.Run("files persist across leases and variables do not", func(t *testing.T) {
		t.Parallel()
		l := openSpec(t, p, 3, spec, "")
		want(t, run(t, l, "var x = 41; const f = std.open('notes.txt', 'w'); f.puts('remember me'); f.close(); os.getcwd()[0]"), "\"/\"\n")
		l.Release()

		l = openSpec(t, p, 3, spec, "")
		res := run(t, l, "std.loadFile('notes.txt')")
		want(t, res, "\"remember me\"\n")
		if !strings.Contains(res.Note, "Files in the working directory persisted") {
			t.Errorf("Note = %q", res.Note)
		}
		res = run(t, l, "x")
		if res.ExitCode != 1 || !strings.Contains(res.Stderr, "ReferenceError") {
			t.Errorf("variable survived a new lease: %+v", res)
		}
	})

	t.Run("an exception is stderr and a non-zero exit", func(t *testing.T) {
		t.Parallel()
		l := openSpec(t, p, 4, spec, "")
		res := run(t, l, "console.log('before'); throw new Error('boom')")
		if res.ExitCode != 1 || res.Stdout != "before\n" || !strings.Contains(res.Stderr, "Error: boom") {
			t.Errorf("got %+v, want exit 1 with the error on stderr", res)
		}
		res = run(t, l, "function (")
		if res.ExitCode != 1 || !strings.Contains(res.Stderr, "SyntaxError") {
			t.Errorf("syntax error: %+v", res)
		}
		if strings.Contains(res.Stderr, "serve") {
			t.Errorf("the driver's own frames leaked into the stack trace:\n%s", res.Stderr)
		}
		want(t, run(t, l, "1 + 1"), "2\n")
	})

	t.Run("promises and timers run", func(t *testing.T) {
		t.Parallel()
		l := openSpec(t, p, 7, spec, "")
		want(t, run(t, l, "async function f() { await null; console.log('in f'); return 7 }\nawait f()"), "in f\n7\n")
		want(t, run(t, l, "Promise.resolve(3).then((v) => console.log('then', v)); 5"), "then 3\n5\n")
		want(t, run(t, l, "await os.sleepAsync(20); 'slept'"), "\"slept\"\n")
		res := run(t, l, "await Promise.reject(new Error('rejected'))")
		if res.ExitCode != 1 || !strings.Contains(res.Stderr, "Error: rejected") {
			t.Errorf("rejected await: %+v", res)
		}
		want(t, run(t, l, "typeof f"), "\"function\"\n")
	})

	t.Run("while true is killed by ctx", func(t *testing.T) {
		t.Parallel()
		l := openSpec(t, p, 5, spec, "")
		want(t, run(t, l, "var x = 1"), "")
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		if _, err := l.Exec(ctx, "while (true) {}"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Exec = %v, want context.DeadlineExceeded", err)
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("took %s to stop", took)
		}
		res := run(t, l, "typeof x")
		want(t, res, "\"undefined\"\n")
		if !strings.Contains(res.Note, "restarted") {
			t.Errorf("Note after a kill = %q", res.Note)
		}
	})

	t.Run("the sandbox holds", func(t *testing.T) {
		t.Parallel()
		l := openSpec(t, p, 6, spec, "")
		want(t, run(t, l, "Object.keys(std.getenviron())"), "[]\n")
		for _, code := range []string{
			"std.loadFile('/etc/passwd').length",
			"std.loadFile('../../../../etc/passwd').length",
			"const [names, err] = os.readdir('/Users'); if (err) throw new Error('errno ' + err)",
		} {
			if res := run(t, l, code); res.ExitCode == 0 {
				t.Errorf("%q succeeded: %+v", code, res)
			}
		}
	})
}

// TestCompileTime reports what New costs with a cold and a warm compilation
// cache. It asserts nothing about speed; the numbers go in the log.
func TestCompileTime(t *testing.T) { //nolint:paralleltest // timing: would be skewed by the other tests' compilations
	for name, base := range realBases(t) {
		cfg := Config{
			FSRoot:   t.TempDir(),
			CacheDir: t.TempDir(),
			Bases:    map[string]Base{name: base},
			Logger:   obs.Discard(),
		}
		for _, state := range []string{"cold", "warm"} {
			start := time.Now()
			p, err := New(t.Context(), cfg)
			if err != nil {
				t.Fatalf("New(%s): %v", name, err)
			}
			t.Logf("%s: New with a %s cache took %s", name, state, time.Since(start).Round(time.Millisecond))
			if err := p.Close(t.Context()); err != nil {
				t.Errorf("Close: %v", err)
			}
		}
	}
}

// BenchmarkPythonStart is the "tier-0 start time" in docs/ARCHITECTURE.md:
// a lease on an already compiled module, up to and including the first
// trivial Exec.
func BenchmarkPythonStart(b *testing.B) { benchmarkStart(b, "python-3.12", "1 + 1", "2\n") }

func BenchmarkQuickJSStart(b *testing.B) { benchmarkStart(b, "quickjs", "1 + 1", "2\n") }

func benchmarkStart(b *testing.B, base, code, stdout string) {
	b.Helper()
	p := realProvider(b, base, b.TempDir(), nil)
	spec := core.EnvSpec{Base: base}
	for b.Loop() {
		l, err := p.Lease(b.Context(), 1, spec, "")
		if err != nil {
			b.Fatal(err)
		}
		if res, err := l.Exec(b.Context(), code); err != nil || res.Stdout != stdout {
			b.Fatalf("Exec = %+v, %v", res, err)
		}
		l.Release()
	}
}

// BenchmarkPythonExec is one request on a warm interpreter: the round trip
// through the driver protocol.
func BenchmarkPythonExec(b *testing.B) {
	const base = "python-3.12"
	p := realProvider(b, base, b.TempDir(), nil)
	l := openSpec(b, p, 1, core.EnvSpec{Base: base}, "")
	for b.Loop() {
		if res, err := l.Exec(b.Context(), "1 + 1"); err != nil || res.Stdout != "2\n" {
			b.Fatalf("Exec = %+v, %v", res, err)
		}
	}
}
