// Package wasm is the tier-0 REPL environment: a WASI build of a language
// runtime running in-process on wazero.
//
// Each base module is compiled once at startup. A lease is a fresh instance
// of it whose only view of the host is the contact's working directory,
// mounted as "/". There is no host environment, no network, and no way to
// reach another contact's files.
//
// The guest's entry point is a small driver that loops forever over a framed
// protocol, so interpreter state survives from one Exec to the next:
//
//	host  -> stdin    <token> SP <n> LF, then n bytes of code
//	guest -> stdout   whatever the code prints, then NUL <token> ':' <ddd> LF
//
// ddd is the exit status, zero-padded to three digits. Stdout and stderr are
// otherwise the code's own. The token is random per request, which is what
// keeps printed output from being read as the end of the request; see console
// and mailbox for why a guest that forges it anyway gains nothing.
//
// Files in the working directory persist for as long as the volume does.
// Interpreter variables die with the instance, and ExecResult.Note says so
// whenever a new instance has taken over. Linear memory is never snapshotted.
package wasm

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/turn"
)

// Sentinel errors, checked with errors.Is.
var (
	// ErrUnknownBase means EnvSpec.Base names no configured runtime.
	ErrUnknownBase = errors.New("unknown base")
	// ErrUnsupportedSpec means the spec asks for something tier 0 cannot
	// give: packages, network access, or another tier.
	ErrUnsupportedSpec = errors.New("not supported by tier 0")
	// ErrSnapshotMismatch means the snapshot to resume is not this contact's.
	ErrSnapshotMismatch = errors.New("snapshot does not belong to contact")
	// ErrGuestExited means a runtime returned instead of serving requests.
	ErrGuestExited = errors.New("guest exited")
)

//go:embed drivers/python.py
var pythonDriver string

//go:embed drivers/quickjs.js
var quickjsDriver string

const (
	defaultMemoryLimitPages = 4096 // 256 MiB of 64 KiB pages
	defaultExecTimeout      = 30 * time.Second
	defaultMaxOutputBytes   = 64 << 10
	// maxMemoryPages is the most a 32-bit wasm memory can hold.
	maxMemoryPages = 65536
	// snapshotPrefix marks a tier-0 snapshot ID: "fs:<contact id>". The
	// directory is the snapshot, so the ID only has to name the contact.
	snapshotPrefix = "fs:"
)

// Base is one language runtime: a WASI module whose entry point speaks the
// driver protocol described in the package comment.
type Base struct {
	Path string            // the .wasm file
	Args []string          // argv for the guest, argv[0] included
	Env  map[string]string // guest environment
	// Mounts are extra read-only host directories, for a runtime that needs
	// its standard library on disk.
	Mounts []Mount
}

// Mount exposes a host directory to the guest, read-only.
type Mount struct {
	HostDir   string
	GuestPath string // absolute, and not "/": that is the working directory
}

// Config configures a Provider.
type Config struct {
	FSRoot           string          // per-contact working dirs live at FSRoot/<contact id>/
	CacheDir         string          // wazero compilation cache directory; "" = in-memory cache
	MemoryLimitPages uint32          // per-instance cap; default 4096 (256 MiB)
	ExecTimeout      time.Duration   // default 30s, applied on top of the caller's ctx; also bounds starting an instance
	MaxOutputBytes   int             // per stream; default 64 KiB; the middle of longer output is cut, with a marker
	Bases            map[string]Base // EnvSpec.Base -> runtime
	Logger           *slog.Logger
}

// image is a Base compiled and ready to instantiate.
type image struct {
	base   Base
	module wazero.CompiledModule
}

// Provider implements turn.EnvLeaser for tier 0.
type Provider struct {
	cfg     Config
	log     *slog.Logger
	runtime wazero.Runtime
	cache   wazero.CompilationCache
	images  map[string]*image

	mu     sync.Mutex
	leases map[*lease]struct{}
	closed bool
}

// New compiles every base module once. With a CacheDir the native code is
// reused across restarts, which turns seconds of compilation into a file
// read.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.MemoryLimitPages == 0 {
		cfg.MemoryLimitPages = defaultMemoryLimitPages
	}
	if cfg.ExecTimeout <= 0 {
		cfg.ExecTimeout = defaultExecTimeout
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = defaultMaxOutputBytes
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	switch {
	case cfg.FSRoot == "":
		return nil, errors.New("wasm: FSRoot is required")
	case cfg.MemoryLimitPages > maxMemoryPages:
		return nil, fmt.Errorf("wasm: MemoryLimitPages %d exceeds %d", cfg.MemoryLimitPages, maxMemoryPages)
	case len(cfg.Bases) == 0:
		return nil, errors.New("wasm: no bases configured")
	}
	root, err := filepath.Abs(cfg.FSRoot)
	if err != nil {
		return nil, fmt.Errorf("wasm: resolve FSRoot: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("wasm: create FSRoot: %w", err)
	}
	cfg.FSRoot = root
	for name, base := range cfg.Bases {
		if err := base.validate(); err != nil {
			return nil, fmt.Errorf("wasm: base %q: %w", name, err)
		}
	}

	cache := wazero.NewCompilationCache()
	if cfg.CacheDir != "" {
		if cache, err = wazero.NewCompilationCacheWithDir(cfg.CacheDir); err != nil {
			return nil, fmt.Errorf("wasm: open compilation cache: %w", err)
		}
	}
	p := &Provider{
		cfg:   cfg,
		log:   cfg.Logger,
		cache: cache,
		// One runtime, so one cache and one memory cap, for every base.
		runtime: wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
			WithCompilationCache(cache).
			WithMemoryLimitPages(cfg.MemoryLimitPages).
			// With DWARF on, wazero resolves a source line for every wasm
			// frame whenever a guest stops abnormally, being killed included.
			// On CPython's debug info that turns a kill into seconds.
			WithDebugInfoEnabled(false).
			// Without this a guest stuck in a loop would hold its goroutine
			// and OS thread forever.
			WithCloseOnContextDone(true)),
		images: make(map[string]*image, len(cfg.Bases)),
		leases: map[*lease]struct{}{},
	}
	if err := p.compile(ctx); err != nil {
		_ = p.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	return p, nil
}

func (p *Provider) compile(ctx context.Context) error {
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, p.runtime); err != nil {
		return fmt.Errorf("wasm: instantiate WASI: %w", err)
	}
	for _, name := range slices.Sorted(maps.Keys(p.cfg.Bases)) {
		base := p.cfg.Bases[name]
		start := time.Now()
		bin, err := os.ReadFile(base.Path)
		if err != nil {
			return fmt.Errorf("wasm: read base %q: %w", name, err)
		}
		module, err := p.runtime.CompileModule(ctx, bin)
		if err != nil {
			return fmt.Errorf("wasm: compile base %q: %w", name, err)
		}
		p.images[name] = &image{base: base, module: module}
		p.log.InfoContext(ctx, "wasm base compiled",
			slog.String("base", name),
			slog.Int("wasm_bytes", len(bin)),
			slog.Float64("compile_ms", obs.SinceMS(start)))
	}
	return nil
}

func (b Base) validate() error {
	if b.Path == "" {
		return errors.New("empty Path")
	}
	if len(b.Args) == 0 {
		return errors.New("empty Args: argv[0] is required")
	}
	for _, m := range b.Mounts {
		if clean := path.Clean(m.GuestPath); !path.IsAbs(m.GuestPath) || clean == "/" {
			return fmt.Errorf("mount %q: guest path must be absolute and below the root", m.GuestPath)
		}
		if info, err := os.Stat(m.HostDir); err != nil {
			return fmt.Errorf("mount %q: %w", m.GuestPath, err)
		} else if !info.IsDir() {
			return fmt.Errorf("mount %q: %s is not a directory", m.GuestPath, m.HostDir)
		}
	}
	return nil
}

// DefaultBases returns the runtimes found under dir, laid out the way
// scripts/fetch-wasm-runtimes.sh leaves them. A runtime that has not been
// fetched is simply absent from the result.
func DefaultBases(dir string) map[string]Base {
	bases := map[string]Base{}
	if py := filepath.Join(dir, "python-3.12"); isFile(filepath.Join(py, "python.wasm")) {
		bases["python-3.12"] = Base{
			Path: filepath.Join(py, "python.wasm"),
			// -B: no __pycache__ litter in the working directory.
			// -u: output reaches the host as it is printed, so a run that is
			// killed still shows what it printed first.
			Args: []string{"python", "-B", "-u", "-c", pythonDriver},
			// This build looks for its standard library at /lib/python3.12.
			Mounts: []Mount{{HostDir: filepath.Join(py, "lib"), GuestPath: "/lib"}},
		}
	}
	if js := filepath.Join(dir, "quickjs", "qjs.wasm"); isFile(js) {
		bases["quickjs"] = Base{
			Path: js,
			Args: []string{"qjs", "--std", "-e", quickjsDriver},
		}
	}
	return bases
}

func isFile(name string) bool {
	info, err := os.Stat(name) //nolint:gosec // G703: name comes from operator configuration, never from a guest
	return err == nil && info.Mode().IsRegular()
}

// Lease starts a fresh instance of spec.Base over the contact's working
// directory. It returns once the interpreter is up and waiting for code.
//
// resume is "" or the ID an earlier Snapshot returned for the same contact.
// Either way the lease sees whatever files the directory holds.
func (p *Provider) Lease(ctx context.Context, contact core.ContactID, spec core.EnvSpec, resume core.SnapshotID) (turn.Lease, error) {
	start := time.Now()
	l, err := p.lease(ctx, contact, spec, resume)
	obs.EnvLeaseMS.WithLabelValues("0").Observe(obs.SinceMS(start))
	if err != nil {
		return nil, fmt.Errorf("wasm lease for contact %d: %w", contact, err)
	}
	return l, nil
}

func (p *Provider) lease(ctx context.Context, contact core.ContactID, spec core.EnvSpec, resume core.SnapshotID) (*lease, error) {
	switch {
	case spec.Tier != 0:
		return nil, fmt.Errorf("tier %d: %w", spec.Tier, ErrUnsupportedSpec)
	case spec.Network:
		return nil, fmt.Errorf("network access: %w", ErrUnsupportedSpec)
	case len(spec.Packages) > 0:
		// Saying yes and then failing every import would be worse.
		return nil, fmt.Errorf("packages %s: %w", strings.Join(spec.Packages, ", "), ErrUnsupportedSpec)
	case resume != "" && resume != snapshotID(contact):
		return nil, fmt.Errorf("resume %q: %w", resume, ErrSnapshotMismatch)
	}
	img, ok := p.images[spec.Base]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownBase, spec.Base)
	}
	if p.isClosed() {
		return nil, core.ErrClosed
	}
	dir, existed, err := p.workdir(contact)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, p.cfg.ExecTimeout)
	defer cancel()
	inst, err := p.start(ctx, img, dir)
	if err != nil {
		return nil, err
	}
	l := &lease{p: p, img: img, contact: contact, dir: dir, inst: inst}
	switch {
	case existed:
		l.note = noteResumed
	case resume != "":
		l.note = noteLost
	}
	if !p.track(l) {
		l.Release()
		return nil, core.ErrClosed
	}
	p.log.DebugContext(ctx, "wasm lease opened",
		slog.Uint64("contact_id", uint64(contact)),
		slog.String("base", spec.Base),
		slog.Bool("resumed", existed))
	return l, nil
}

// workdir returns the contact's directory, creating it if needed, and whether
// it was already there.
func (p *Provider) workdir(contact core.ContactID) (dir string, existed bool, err error) {
	// A ContactID renders as decimal digits, so the name cannot traverse.
	dir = filepath.Join(p.cfg.FSRoot, contact.String())
	err = os.Mkdir(dir, 0o700)
	existed = errors.Is(err, os.ErrExist)
	if err != nil && !existed {
		return "", false, fmt.Errorf("create working directory: %w", err)
	}
	// Lstat: a link here would point the whole mount somewhere else.
	if info, err := os.Lstat(dir); err != nil {
		return "", false, fmt.Errorf("stat working directory: %w", err)
	} else if !info.IsDir() {
		return "", false, fmt.Errorf("working directory %s is not a directory", dir)
	}
	return dir, existed, nil
}

func snapshotID(contact core.ContactID) core.SnapshotID {
	return core.SnapshotID(snapshotPrefix + contact.String())
}

func (p *Provider) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// track registers a live lease so Close can stop it. It reports false if the
// provider has already closed.
func (p *Provider) track(l *lease) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.leases[l] = struct{}{}
	return true
}

func (p *Provider) untrack(l *lease) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.leases, l)
}

// Close releases every open lease and frees the compiled modules. Working
// directories are left alone.
func (p *Provider) Close(ctx context.Context) error {
	p.mu.Lock()
	p.closed = true
	open := slices.Collect(maps.Keys(p.leases))
	p.mu.Unlock()
	for _, l := range open {
		l.Release()
	}
	if err := errors.Join(p.runtime.Close(ctx), p.cache.Close(ctx)); err != nil {
		return fmt.Errorf("wasm: close: %w", err)
	}
	return nil
}
