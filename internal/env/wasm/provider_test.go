package wasm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/turn"
)

// buildGuest compiles testdata/guest for WASI and returns the .wasm path.
func buildGuest(tb testing.TB) string {
	tb.Helper()
	out := filepath.Join(tb.TempDir(), "guest.wasm")
	cmd := exec.CommandContext(tb.Context(), "go", "build", "-o", out, "./testdata/guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOFLAGS=")
	if msg, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("build guest: %v\n%s", err, msg)
	}
	return out
}

// harness builds providers that share one guest binary and one compilation
// cache, so only the first of them pays for compiling it.
type harness struct {
	guest string
	cache string
}

func newHarness(tb testing.TB) *harness {
	tb.Helper()
	return &harness{guest: buildGuest(tb), cache: tb.TempDir()}
}

func (h *harness) provider(tb testing.TB, tune func(*Config)) *Provider {
	tb.Helper()
	cfg := Config{
		FSRoot:   tb.TempDir(),
		CacheDir: h.cache,
		Bases:    map[string]Base{"guest": {Path: h.guest, Args: []string{"guest"}}},
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

func guestSpec() core.EnvSpec { return core.EnvSpec{Tier: 0, Base: "guest"} }

func open(tb testing.TB, p *Provider, contact core.ContactID, resume core.SnapshotID) turn.Lease {
	tb.Helper()
	return openSpec(tb, p, contact, guestSpec(), resume)
}

// openSpec leases an environment and releases it when the test ends.
func openSpec(tb testing.TB, p *Provider, contact core.ContactID, spec core.EnvSpec, resume core.SnapshotID) turn.Lease {
	tb.Helper()
	l, err := p.Lease(tb.Context(), contact, spec, resume)
	if err != nil {
		tb.Fatalf("Lease(%d, %s): %v", contact, spec.Base, err)
	}
	tb.Cleanup(l.Release)
	return l
}

// run executes code and fails the test on an environment error.
func run(tb testing.TB, l turn.Lease, code string) turn.ExecResult {
	tb.Helper()
	res, err := l.Exec(tb.Context(), code)
	if err != nil {
		tb.Fatalf("Exec(%q): %v", code, err)
	}
	return res
}

// want checks stdout and a zero exit.
func want(tb testing.TB, res turn.ExecResult, stdout string) {
	tb.Helper()
	if res.ExitCode != 0 || res.Stdout != stdout {
		tb.Fatalf("got exit %d stdout %q stderr %q; want exit 0 stdout %q", res.ExitCode, res.Stdout, res.Stderr, stdout)
	}
}

func leaseCount(tb testing.TB) uint64 {
	tb.Helper()
	h, ok := obs.EnvLeaseMS.WithLabelValues("0").(prometheus.Histogram)
	if !ok {
		tb.Fatal("env_lease_ms is not a histogram")
	}
	var m dto.Metric
	if err := h.Write(&m); err != nil {
		tb.Fatalf("read env_lease_ms: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}

func TestProvider(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// Built before the subtests start so that it fills the cache for them.
	shared := h.provider(t, nil)

	t.Run("state persists within a lease", func(t *testing.T) {
		t.Parallel()
		l := open(t, shared, 101, "")
		first := run(t, l, "set x 41")
		want(t, first, "")
		if first.Note != "" {
			t.Errorf("first Exec on a new directory: Note = %q, want empty", first.Note)
		}
		second := run(t, l, "get x")
		want(t, second, "41\n")
		if second.Note != "" {
			t.Errorf("second Exec: Note = %q, want empty", second.Note)
		}
	})

	t.Run("a new lease keeps files and loses variables", func(t *testing.T) {
		t.Parallel()
		before := leaseCount(t)
		l := open(t, shared, 102, "")
		want(t, run(t, l, "set x 41"), "")
		want(t, run(t, l, "write notes.txt remember me"), "")
		l.Release()

		l = open(t, shared, 102, "")
		if got := leaseCount(t) - before; got < 2 {
			t.Errorf("env_lease_ms{tier=0} grew by %d over two leases", got)
		}
		res := run(t, l, "read notes.txt")
		want(t, res, "remember me")
		if !strings.Contains(res.Note, "Files in the working directory persisted") ||
			!strings.Contains(res.Note, "variables") {
			t.Errorf("first Exec over an existing directory: Note = %q", res.Note)
		}
		res = run(t, l, "get x")
		if res.ExitCode != 1 || !strings.Contains(res.Stderr, "x is not defined") {
			t.Errorf("variable survived a new lease: %+v", res)
		}
		if res.Note != "" {
			t.Errorf("second Exec: Note = %q, want empty", res.Note)
		}
	})

	t.Run("snapshot round trip", func(t *testing.T) {
		t.Parallel()
		l := open(t, shared, 103, "")
		want(t, run(t, l, "write data.txt kept"), "")
		id, err := l.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if id != "fs:103" {
			t.Errorf("Snapshot = %q, want a stable ID naming the contact", id)
		}
		if again, _ := l.Snapshot(t.Context()); again != id {
			t.Errorf("second Snapshot = %q, want %q", again, id)
		}
		l.Release()

		l = open(t, shared, 103, id)
		res := run(t, l, "read data.txt")
		want(t, res, "kept")
		if res.Note == "" {
			t.Error("first Exec of a resumed lease: Note is empty")
		}

		for _, foreign := range []core.SnapshotID{"fs:104", "fs:", "103", "snap-abc"} {
			if _, err := shared.Lease(t.Context(), 103, guestSpec(), foreign); !errors.Is(err, ErrSnapshotMismatch) {
				t.Errorf("Lease(resume=%q) = %v, want ErrSnapshotMismatch", foreign, err)
			}
		}
	})

	t.Run("resume without a directory says the files are gone", func(t *testing.T) {
		t.Parallel()
		l := open(t, shared, 105, "fs:105")
		res := run(t, l, "ls /")
		want(t, res, "")
		if !strings.Contains(res.Note, "empty working directory") {
			t.Errorf("Note = %q", res.Note)
		}
	})

	t.Run("a non-zero exit is a result", func(t *testing.T) {
		t.Parallel()
		l := open(t, shared, 106, "")
		want(t, run(t, l, "set x 1"), "")
		res := run(t, l, "fail 3 boom")
		if res.ExitCode != 3 || res.Stderr != "boom\n" || res.Stdout != "" {
			t.Errorf("got %+v, want exit 3 with boom on stderr", res)
		}
		// The interpreter is still the same one.
		res = run(t, l, "get x")
		want(t, res, "1\n")
		if res.Note != "" {
			t.Errorf("Note after a plain failure = %q, want empty", res.Note)
		}
	})

	t.Run("cancelling ctx kills a spinning guest and the lease recovers", func(t *testing.T) {
		t.Parallel()
		l := open(t, shared, 107, "")
		want(t, run(t, l, "set x 1"), "")
		want(t, run(t, l, "write keep.txt still here"), "")

		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(100*time.Millisecond, cancel)
		start := time.Now()
		_, err := l.Exec(ctx, "spin")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Exec(spin) = %v, want context.Canceled", err)
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("a cancelled guest took %s to stop", took)
		}

		res := run(t, l, "read keep.txt")
		want(t, res, "still here")
		if !strings.Contains(res.Note, "restarted") || !strings.Contains(res.Note, "Files in the working directory persisted") {
			t.Errorf("first Exec after a restart: Note = %q", res.Note)
		}
		res = run(t, l, "get x")
		if res.ExitCode != 1 || res.Note != "" {
			t.Errorf("after restart: %+v, want x undefined and no second Note", res)
		}
	})

	t.Run("cancelling ctx wakes a sleeping guest", func(t *testing.T) {
		t.Parallel()
		l := open(t, shared, 108, "")
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := l.Exec(ctx, "sleep 600000")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Exec(sleep) = %v, want context.DeadlineExceeded", err)
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("a cancelled sleep took %s to stop", took)
		}
		want(t, run(t, l, "echo back"), "back\n")
	})

	t.Run("the guest exiting is a result and the lease recovers", func(t *testing.T) {
		t.Parallel()
		l := open(t, shared, 109, "")
		res := run(t, l, "die 7")
		if res.ExitCode != 7 || !strings.Contains(res.Stderr, "exited with status 7") {
			t.Errorf("got %+v, want exit 7 explained on stderr", res)
		}
		res = run(t, l, "echo alive")
		want(t, res, "alive\n")
		if !strings.Contains(res.Note, "restarted") {
			t.Errorf("Note = %q", res.Note)
		}
	})

	t.Run("output that looks like a marker is output", func(t *testing.T) {
		t.Parallel()
		l := open(t, shared, 110, "")
		res := run(t, l, "fake")
		want(t, res, "\x00"+strings.Repeat("A", 26)+":000\nafter\n")
		res = run(t, l, "tease")
		if res.ExitCode != 0 || !strings.HasPrefix(res.Stdout, "\x00") || !strings.HasSuffix(res.Stdout, ":0x\n") {
			t.Errorf("got %+v, want the half-marker passed through", res)
		}
		want(t, run(t, l, "echo ok"), "ok\n")
	})

	t.Run("large code and output round trip", func(t *testing.T) {
		t.Parallel()
		l := open(t, shared, 111, "")
		big := strings.Repeat("0123456789abcdef", 3000) // 48 KB: several stdin reads
		want(t, run(t, l, "echo "+big), big+"\n")
		want(t, run(t, l, "echo "), "\n")
	})

	t.Run("the guest sees only its own directory", func(t *testing.T) {
		t.Parallel()
		owner := open(t, shared, 201, "")
		want(t, run(t, owner, "write secret.txt hunter2"), "")
		want(t, run(t, owner, "mkdir sub/dir"), "")

		secret := filepath.Join(shared.cfg.FSRoot, "201", "secret.txt")
		if b, err := os.ReadFile(secret); err != nil || string(b) != "hunter2" {
			t.Fatalf("the working directory is not at FSRoot/<contact>: %q, %v", b, err)
		}
		// A link planted from outside the sandbox must not be followed either.
		if err := os.Mkdir(filepath.Join(shared.cfg.FSRoot, "202"), 0o700); err != nil {
			t.Fatal(err)
		}
		for name, target := range map[string]string{"planted": secret, "planted-dir": filepath.Dir(secret)} {
			if err := os.Symlink(target, filepath.Join(shared.cfg.FSRoot, "202", name)); err != nil {
				t.Fatal(err)
			}
		}

		other := open(t, shared, 202, "")
		want(t, run(t, other, "cwd"), "/\n")
		want(t, run(t, other, "ls /"), "planted\nplanted-dir\n")
		want(t, run(t, other, "env"), "\n")
		want(t, run(t, other, "args"), "guest\n")
		for _, code := range []string{
			"read secret.txt",
			"read ../201/secret.txt",
			"read /../201/secret.txt",
			"read sub/../../201/secret.txt",
			"read " + secret,
			"read /etc/passwd",
			"read /etc/hosts",
			"ls ../201",
			"ls " + shared.cfg.FSRoot,
			"read planted",
			"read planted-dir/secret.txt",
			"ls planted-dir",
			"write planted-dir/new.txt x",
			"symlink " + secret + " link",
			"symlink ../201/secret.txt link",
		} {
			res := run(t, other, code)
			if res.ExitCode == 0 || strings.Contains(res.Stdout, "hunter2") || strings.Contains(res.Stdout, "secret") {
				t.Errorf("%q escaped the sandbox: %+v", code, res)
			}
		}
		// ".." at the root stays at the root, as it does in a chroot.
		want(t, run(t, other, "ls .."), "planted\nplanted-dir\n")
		want(t, run(t, other, "write ../../up.txt x"), "")
		if _, err := os.Stat(filepath.Join(shared.cfg.FSRoot, "202", "up.txt")); err != nil {
			t.Errorf("a path climbing past the root was not pinned to it: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(shared.cfg.FSRoot, "202", "link")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the guest created a symlink: %v", err)
		}
	})

	t.Run("release", func(t *testing.T) {
		t.Parallel()
		l := open(t, shared, 301, "")
		want(t, run(t, l, "echo hi"), "hi\n")
		l.Release()
		l.Release()
		if _, err := l.Exec(t.Context(), "echo hi"); !errors.Is(err, core.ErrClosed) {
			t.Errorf("Exec after Release = %v, want core.ErrClosed", err)
		}
		if _, err := l.Snapshot(t.Context()); !errors.Is(err, core.ErrClosed) {
			t.Errorf("Snapshot after Release = %v, want core.ErrClosed", err)
		}
	})

	t.Run("release during exec", func(t *testing.T) {
		t.Parallel()
		l := open(t, shared, 302, "")
		time.AfterFunc(100*time.Millisecond, l.Release)
		start := time.Now()
		if _, err := l.Exec(t.Context(), "spin"); !errors.Is(err, core.ErrClosed) {
			t.Errorf("Exec interrupted by Release = %v, want core.ErrClosed", err)
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("Release took %s to stop a running guest", took)
		}
	})

	t.Run("specs tier 0 cannot serve are refused", func(t *testing.T) {
		t.Parallel()
		for name, tc := range map[string]struct {
			spec core.EnvSpec
			want error
		}{
			"unknown base": {core.EnvSpec{Base: "cobol"}, ErrUnknownBase},
			"other tier":   {core.EnvSpec{Tier: 1, Base: "guest"}, ErrUnsupportedSpec},
			"network":      {core.EnvSpec{Base: "guest", Network: true}, ErrUnsupportedSpec},
			"packages":     {core.EnvSpec{Base: "guest", Packages: []string{"numpy"}}, ErrUnsupportedSpec},
		} {
			if l, err := shared.Lease(t.Context(), 303, tc.spec, ""); !errors.Is(err, tc.want) || l != nil {
				t.Errorf("%s: Lease = %v, %v; want nil, %v", name, l, err, tc.want)
			}
		}
	})

	t.Run("exec timeout", func(t *testing.T) {
		t.Parallel()
		p := h.provider(t, func(c *Config) { c.ExecTimeout = 300 * time.Millisecond })
		l := open(t, p, 1, "")
		want(t, run(t, l, "set x 1"), "")

		for _, code := range []string{
			"spin",
			"sleep 600000",
			// Printing the marker yourself does not end the request.
			"forge",
		} {
			start := time.Now()
			res := run(t, l, code)
			if res.ExitCode != exitTimeout || !strings.Contains(res.Stderr, "ran longer than 300ms") {
				t.Errorf("%s: got %+v, want exit %d and a timeout remark", code, res, exitTimeout)
			}
			if took := time.Since(start); took > 3*time.Second {
				t.Errorf("%s: stopping took %s", code, took)
			}
			res = run(t, l, "get x")
			if res.ExitCode != 1 || !strings.Contains(res.Note, "restarted") {
				t.Errorf("after %s: got %+v, want a fresh interpreter and a Note", code, res)
			}
		}
	})

	t.Run("memory cap", func(t *testing.T) {
		t.Parallel()
		p := h.provider(t, func(c *Config) { c.MemoryLimitPages = 512 }) // 32 MiB
		l := open(t, p, 1, "")
		want(t, run(t, l, "alloc 4"), "ok\n")
		res := run(t, l, "alloc 64")
		if res.ExitCode == 0 || res.Stdout != "" {
			t.Fatalf("allocating past the cap succeeded: %+v", res)
		}
		res = run(t, l, "alloc 4")
		want(t, res, "ok\n")
		if !strings.Contains(res.Note, "restarted") {
			t.Errorf("Note = %q", res.Note)
		}
	})

	t.Run("output truncation", func(t *testing.T) {
		t.Parallel()
		const limit = 2000
		p := h.provider(t, func(c *Config) { c.MaxOutputBytes = limit })
		l := open(t, p, 1, "")

		res := run(t, l, "flood stdout 2000")
		if len(res.Stdout) != limit || strings.Contains(res.Stdout, "truncated") {
			t.Errorf("output at the limit was altered: %d bytes", len(res.Stdout))
		}
		for _, stream := range []string{"stdout", "stderr"} {
			res := run(t, l, "flood "+stream+" 5000000")
			got := res.Stdout
			if stream == "stderr" {
				got = res.Stderr
			}
			if res.ExitCode != 0 {
				t.Errorf("%s: exit %d", stream, res.ExitCode)
			}
			if !strings.HasPrefix(got, "BEGIN") || !strings.HasSuffix(got, "END!!") ||
				!strings.Contains(got, "[... 4998000 bytes of output truncated ...]") {
				t.Errorf("%s: want head, a visible marker and tail; got %d bytes: %.40q ... %q",
					stream, len(got), got, got[max(0, len(got)-40):])
			}
			if len(got) > limit+64 {
				t.Errorf("%s: kept %d bytes with a %d byte limit", stream, len(got), limit)
			}
		}
	})

	t.Run("base env and args reach the guest", func(t *testing.T) {
		t.Parallel()
		p := h.provider(t, func(c *Config) {
			c.Bases = map[string]Base{"guest": {
				Path: h.guest,
				Args: []string{"guest", "-flag"},
				Env:  map[string]string{"ONLY": "this"},
			}}
		})
		l := open(t, p, 1, "")
		want(t, run(t, l, "env"), "ONLY=this\n")
		want(t, run(t, l, "args"), "guest -flag\n")
	})

	t.Run("base mounts are visible and read-only", func(t *testing.T) {
		t.Parallel()
		lib := t.TempDir()
		if err := os.WriteFile(filepath.Join(lib, "stdlib.txt"), []byte("batteries"), 0o600); err != nil {
			t.Fatal(err)
		}
		p := h.provider(t, func(c *Config) {
			c.Bases = map[string]Base{"guest": {
				Path:   h.guest,
				Args:   []string{"guest"},
				Mounts: []Mount{{HostDir: lib, GuestPath: "/lib"}},
			}}
		})
		l := open(t, p, 1, "")
		want(t, run(t, l, "read /lib/stdlib.txt"), "batteries")
		if res := run(t, l, "write /lib/new.txt x"); res.ExitCode == 0 {
			t.Errorf("wrote into a read-only mount: %+v", res)
		}
		if res := run(t, l, "write /lib/stdlib.txt x"); res.ExitCode == 0 {
			t.Errorf("overwrote a file in a read-only mount: %+v", res)
		}
		// The working directory is still the root, and still writable.
		want(t, run(t, l, "write mine.txt x"), "")
		want(t, run(t, l, "ls /"), "mine.txt\n")
		if b, err := os.ReadFile(filepath.Join(lib, "stdlib.txt")); err != nil || string(b) != "batteries" {
			t.Errorf("the mounted file changed: %q, %v", b, err)
		}
	})

	t.Run("close releases leases", func(t *testing.T) {
		t.Parallel()
		p := h.provider(t, nil)
		l := open(t, p, 1, "")
		want(t, run(t, l, "echo hi"), "hi\n")
		if err := p.Close(t.Context()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if _, err := l.Exec(t.Context(), "echo hi"); !errors.Is(err, core.ErrClosed) {
			t.Errorf("Exec after Close = %v, want core.ErrClosed", err)
		}
		if _, err := p.Lease(t.Context(), 2, guestSpec(), ""); !errors.Is(err, core.ErrClosed) {
			t.Errorf("Lease after Close = %v, want core.ErrClosed", err)
		}
	})

	t.Run("a guest that exits at startup fails the lease", func(t *testing.T) {
		t.Parallel()
		// /bin/true for wasm: the smallest valid module has no _start, so it
		// returns at once without ever reading stdin.
		empty := filepath.Join(t.TempDir(), "empty.wasm")
		if err := os.WriteFile(empty, []byte("\x00asm\x01\x00\x00\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
		p := h.provider(t, func(c *Config) {
			c.Bases = map[string]Base{"empty": {Path: empty, Args: []string{"empty"}}}
		})
		_, err := p.Lease(t.Context(), 1, core.EnvSpec{Base: "empty"}, "")
		if !errors.Is(err, ErrGuestExited) {
			t.Errorf("Lease = %v, want ErrGuestExited", err)
		}
	})
}

func TestNewRejectsBadConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	notWasm := filepath.Join(dir, "not.wasm")
	if err := os.WriteFile(notWasm, []byte("not wasm"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := Base{Path: notWasm, Args: []string{"x"}}
	for name, cfg := range map[string]Config{
		"no FSRoot":       {Bases: map[string]Base{"x": base}},
		"no bases":        {FSRoot: dir},
		"missing file":    {FSRoot: dir, Bases: map[string]Base{"x": {Path: filepath.Join(dir, "absent.wasm"), Args: []string{"x"}}}},
		"not a module":    {FSRoot: dir, Bases: map[string]Base{"x": base}},
		"no argv":         {FSRoot: dir, Bases: map[string]Base{"x": {Path: notWasm}}},
		"too much memory": {FSRoot: dir, MemoryLimitPages: 1 << 20, Bases: map[string]Base{"x": base}},
		"mount at root":   {FSRoot: dir, Bases: map[string]Base{"x": {Path: notWasm, Args: []string{"x"}, Mounts: []Mount{{HostDir: dir, GuestPath: "/"}}}}},
		"mount missing":   {FSRoot: dir, Bases: map[string]Base{"x": {Path: notWasm, Args: []string{"x"}, Mounts: []Mount{{HostDir: filepath.Join(dir, "absent"), GuestPath: "/lib"}}}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if p, err := New(t.Context(), cfg); err == nil {
				_ = p.Close(t.Context())
				t.Error("New succeeded")
			}
		})
	}
}

func TestDefaultBases(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got := DefaultBases(dir); len(got) != 0 {
		t.Errorf("an empty directory yielded bases: %v", got)
	}
	for _, name := range []string{"python-3.12/python.wasm", "quickjs/qjs.wasm"} {
		file := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := DefaultBases(dir)
	py, js := got["python-3.12"], got["quickjs"]
	if len(got) != 2 || py.Path == "" || js.Path == "" {
		t.Fatalf("DefaultBases = %v, want python-3.12 and quickjs", got)
	}
	// The drivers are embedded and handed to the runtime as its program.
	if last := py.Args[len(py.Args)-1]; !strings.Contains(last, "_contactd_serve") {
		t.Errorf("python argv does not end with the driver: %.40q", last)
	}
	if last := js.Args[len(js.Args)-1]; !strings.Contains(last, "evalScript") {
		t.Errorf("quickjs argv does not end with the driver: %.40q", last)
	}
}

// BenchmarkGuestStart measures the provider's own cost of a lease: instance
// creation, mounts, boot handshake, one request, teardown. The guest is the
// Go test runtime, whose start-up is small next to an interpreter's.
func BenchmarkGuestStart(b *testing.B) {
	p := newHarness(b).provider(b, nil)
	for b.Loop() {
		l, err := p.Lease(b.Context(), 1, guestSpec(), "")
		if err != nil {
			b.Fatal(err)
		}
		if res, err := l.Exec(b.Context(), "echo hi"); err != nil || res.Stdout != "hi\n" {
			b.Fatalf("Exec = %+v, %v", res, err)
		}
		l.Release()
	}
}
