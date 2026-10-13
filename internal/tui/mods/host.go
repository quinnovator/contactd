package mods

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/toml"
)

// errClosed is what a host that was closed answers.
var errClosed = errors.New("the mod host is closed")

// keptTimes is how many call durations Median and Slowest are taken over.
const keptTimes = 100

// startFactor is how many kill deadlines an instance gets to start. A Go
// guest starts its runtime then, which takes a few milliseconds.
const startFactor = 20

// Host runs the installed mods. It is safe for concurrent use: calls to one
// mod wait for each other, and calls to different mods do not.
type Host struct {
	o Options
	// budget, kill and start are the options' times, made longer on a
	// machine that interprets modules.
	budget, kill, start time.Duration
	scale               time.Duration

	// admin lets one of Load, SetEnabled, Install, Update and Close run at
	// a time. It guards the runtime and its cache.
	admin sync.Mutex
	rt    wazero.Runtime
	cache wazero.CompilationCache

	// mu guards the list of mods, each mod's info and times, the state and
	// its file. It is never held across a call into a module.
	mu     sync.Mutex
	mods   map[string]*mod
	state  state
	closed bool
}

// mod is one installed mod.
type mod struct {
	name string

	// call lets one request into the module at a time. It guards the
	// fields down to the blank line, and is taken before Host.mu.
	call     sync.Mutex
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	inst     *instance
	// fresh says the instance has not answered a call yet. Its first call
	// pays for warming up and cannot strike for time.
	fresh bool
	// strikes counts the calls in a row that ran over or failed. failed
	// says one of them was a failure, and lastErr what the last failure
	// was.
	strikes int
	failed  bool
	lastErr string
	// wall is what the guest's wall clock reads, in unix ms.
	wall int64
	// starts counts the instances started, so tests can tell a new one.
	starts int

	// Guarded by Host.mu.
	info  Info
	times []time.Duration // the last keptTimes calls, oldest first
}

// Open reads the manifests in Options.Dir and the state file. It compiles
// nothing and runs nothing: mods that are switched on are StatusLoading
// until Load.
//
// A mod whose manifest cannot be read is listed as StatusFailed, and stops
// no other mod. A state file that cannot be read is an error, since writing
// a new one would forget which mods were on.
func Open(o Options) (*Host, error) {
	o = o.withDefaults()
	st, err := loadState(o.StatePath)
	if err != nil {
		return nil, err
	}
	scale := slowdown(runtime.GOARCH)
	h := &Host{
		o:      o,
		scale:  scale,
		budget: o.Budget * scale,
		kill:   o.Kill * scale,
		start:  o.Kill * scale * startFactor,
		mods:   map[string]*mod{},
		state:  st,
	}
	if err := h.scan(); err != nil {
		return nil, err
	}
	return h, nil
}

// scan lists the mod directories. A directory without a manifest is not a
// mod; one whose name could not be a mod's is skipped with a warning.
func (h *Host) scan() error {
	if h.o.Dir == "" {
		return nil
	}
	entries, err := os.ReadDir(h.o.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mods: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		info, err := os.Stat(filepath.Join(h.o.Dir, name))
		if err != nil || !info.IsDir() {
			continue
		}
		if !validName(name, false) {
			h.o.Logger.Warn("skipped a directory that cannot be a mod's", "dir", safe.Line(name, 80, true))
			continue
		}
		man, err := h.readManifest(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		m := &mod{name: name}
		m.info = h.rest(name, man, err)
		h.mods[name] = m
	}
	return nil
}

// readManifest reads and checks an installed mod's manifest.
func (h *Host) readManifest(name string) (Manifest, error) {
	data, err := readRegular(filepath.Join(h.o.Dir, name, ManifestFile), toml.MaxSize)
	if err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	man, err := ParseManifest(data, name)
	if err != nil {
		return Manifest{}, err
	}
	if err := man.checkBudget(h.o.Budget); err != nil {
		return Manifest{}, err
	}
	return man, nil
}

// rest is the info of a mod that is not loaded: what its manifest and the
// state say. err is what reading the manifest gave.
func (h *Host) rest(name string, man Manifest, err error) Info {
	if err != nil {
		return Info{Manifest: Manifest{Name: name}, Status: StatusFailed, Reason: reason(err)}
	}
	e := h.state[name]
	switch {
	case e.disabled != "":
		return Info{Manifest: man, Status: StatusDisabled, Reason: e.disabled}
	case e.on:
		return Info{Manifest: man, Status: StatusLoading}
	}
	return Info{Manifest: man, Status: StatusOff}
}

// reason makes an error into the one safe line an Info carries.
func reason(err error) string { return safe.Line(err.Error(), 240, false) }

// Load brings up every mod that is switched on and not loaded yet: it reads
// the module, compiles it, checks its imports and exports, starts it and
// says hello. It is slow the first time a module is seen, so call it off
// the UI loop. A mod that fails is StatusFailed with the reason and affects
// no other. It returns what Mods would.
func (h *Host) Load(ctx context.Context) []Info {
	h.admin.Lock()
	defer h.admin.Unlock()
	for _, m := range h.list() {
		if ctx.Err() != nil || h.isClosed() {
			break
		}
		if h.info(m).Status != StatusLoading {
			continue
		}
		m.call.Lock()
		err := h.load(ctx, m, false)
		m.call.Unlock()
		if err != nil {
			h.o.Logger.Error("could not record a mod's state", "mod", m.name, "err", err)
		}
	}
	return h.Mods()
}

// Mods returns every installed mod, sorted by name.
func (h *Host) Mods() []Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Info, 0, len(h.mods))
	for _, name := range slices.Sorted(maps.Keys(h.mods)) {
		out = append(out, h.mods[name].snapshot())
	}
	return out
}

// snapshot is the mod's info with its times worked out. Host.mu is held.
func (m *mod) snapshot() Info {
	info := m.info
	if len(m.times) > 0 {
		sorted := slices.Sorted(slices.Values(m.times))
		info.Median = sorted[len(sorted)/2]
		info.Slowest = sorted[len(sorted)-1]
	}
	return info
}

// Needs returns the grants held by the mods that are on, in the order of
// Grants. The client builds only what some mod will be sent.
func (h *Host) Needs() []Grant {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Grant
	for _, g := range Grants() {
		for _, m := range h.mods {
			if m.info.Status == StatusOn && m.info.Manifest.Has(g) {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

// Call sends one request to a mod and returns its reply, made safe to draw.
// op is OpSegment, OpCommand or OpRender, and name the segment, the command
// or the renderer kind, which the manifest must declare. The request holds
// what of in the mod's grants allow.
//
// Call blocks for as long as the mod takes, up to the kill deadline, so the
// UI loop must not call it directly. A caller's context that has already
// ended is an error and the mod is not run; once the mod is running the
// call is bounded by the kill deadline and not by the caller's context,
// because ending a call closes the instance.
//
// A render request with no item, or with an item over MaxItem bytes, is
// answered with Pass and the mod is not run.
//
// A call that fails, or runs over the budget, is a strike. The reply of a
// call that only ran over is still returned. On the last strike in a row
// the mod is switched off and the error wraps ErrDisabled.
func (h *Host) Call(ctx context.Context, modName, op, name string, in Input) (wire.Reply, error) {
	if err := ctx.Err(); err != nil {
		return wire.Reply{}, fmt.Errorf("mod %s: %w", clip(modName), err)
	}
	m := h.lookup(modName)
	if m == nil {
		return wire.Reply{}, fmt.Errorf("no mod named %q", clip(modName))
	}
	m.call.Lock()
	defer m.call.Unlock()
	// Another call may have held the mod for a while.
	if err := ctx.Err(); err != nil {
		return wire.Reply{}, fmt.Errorf("mod %s: %w", m.name, err)
	}
	info := h.info(m)
	if info.Status != StatusOn || m.compiled == nil {
		return wire.Reply{}, fmt.Errorf("mod %s is %s", m.name, info.Status)
	}
	man := info.Manifest
	if err := man.declares(op, name); err != nil {
		return wire.Reply{}, err
	}
	if op == OpRender && (in.Item == nil || len(in.Item.Text) > MaxItem) {
		return wire.Reply{ABI: wire.ABI, Pass: true}, nil
	}
	body, err := encodeRequest(buildRequest(man, op, name, in))
	if err != nil {
		return wire.Reply{}, fmt.Errorf("mod %s: %w", m.name, err)
	}
	if len(body) > MaxMessage {
		return wire.Reply{}, fmt.Errorf("mod %s: the request is %d bytes: the limit is %d", m.name, len(body), MaxMessage)
	}
	if man.Has(GrantClock) {
		// The guest's wall clock reads the time of the request.
		if m.wall = in.Data.Now; m.wall == 0 {
			m.wall = h.o.Now().UnixMilli()
		}
	}

	reply, took, fresh, err := h.exchange(ctx, m, man, body)
	if took >= 0 {
		h.record(m, took)
	}
	switch {
	case err != nil:
		h.stop(ctx, m)
		return wire.Reply{}, h.strike(ctx, m, err)
	case took > h.budgetOf(man):
		if fresh {
			break
		}
		if err := h.strike(ctx, m, nil); err != nil {
			return wire.Reply{}, err
		}
	default:
		m.strikes, m.failed, m.lastErr = 0, false, ""
	}
	return cleanReply(reply, in.Term.Glyphs == glyphsASCII), nil
}

// budgetOf is the budget of one mod: its own when the manifest lowers the
// host's.
func (h *Host) budgetOf(man Manifest) time.Duration {
	if man.Budget > 0 {
		return min(man.Budget*h.scale, h.budget)
	}
	return h.budget
}

// exchange runs one request through the mod's instance, starting one when
// the last was closed. took is how long the module ran by Options.Now, or
// negative when it never ran. fresh says this was the instance's first call.
// m.call is held.
func (h *Host) exchange(ctx context.Context, m *mod, man Manifest, body []byte) (reply wire.Reply, took time.Duration, fresh bool, err error) {
	if m.inst == nil {
		if err := h.startInstance(ctx, m, man); err != nil {
			return wire.Reply{}, -1, false, err
		}
	}
	fresh, m.fresh = m.fresh, false

	// The deadline is the host's own. The caller's context may end at any
	// moment, and a context that ends during a call closes the instance.
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), h.kill)
	defer cancel()
	begin := h.o.Now()
	raw, err := m.inst.roundTrip(callCtx, body)
	took = max(h.o.Now().Sub(begin), 0)
	if err != nil {
		return wire.Reply{}, took, fresh, err
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return wire.Reply{}, took, fresh, errors.New("the reply is not the JSON of a reply")
	}
	if reply.ABI != wire.ABI {
		return wire.Reply{}, took, fresh, fmt.Errorf("the reply's abi is %d, not %d", reply.ABI, wire.ABI)
	}
	return reply, took, fresh, nil
}

// startInstance makes the mod a new instance from its compiled module.
// m.call is held.
func (h *Host) startInstance(ctx context.Context, m *mod, man Manifest) error {
	var wall *int64
	if man.Has(GrantClock) {
		wall = &m.wall
	}
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), h.start)
	defer cancel()
	inst, err := instantiate(startCtx, m.rt, m.compiled, wall)
	if err != nil {
		return err
	}
	m.inst, m.fresh = inst, true
	m.starts++
	return nil
}

// stop closes the mod's instance and keeps its compiled module, so the next
// call starts a new one. m.call is held.
func (h *Host) stop(ctx context.Context, m *mod) {
	if m.inst != nil {
		m.inst.close(ctx)
		m.inst = nil
	}
}

// unload closes the instance and the compiled module. m.call is held.
func (h *Host) unload(ctx context.Context, m *mod) {
	h.stop(ctx, m)
	if m.compiled != nil {
		_ = m.compiled.Close(context.WithoutCancel(ctx))
		m.compiled, m.rt = nil, nil
	}
	m.strikes, m.failed, m.lastErr = 0, false, ""
}

// strike counts a call that failed (cause is why) or ran over the budget
// (cause is nil). It returns the error for the caller: the cause, or on the
// last strike in a row an error that wraps ErrDisabled. m.call is held.
func (h *Host) strike(ctx context.Context, m *mod, cause error) error {
	m.strikes++
	if cause != nil && !errors.Is(cause, errDeadline) {
		m.failed, m.lastErr = true, reason(cause)
	}
	if m.strikes < h.o.Strikes {
		if cause == nil {
			return nil
		}
		return fmt.Errorf("mod %s: %w", m.name, cause)
	}

	why := fmt.Sprintf("over budget %d times in a row", m.strikes)
	if m.failed {
		why = fmt.Sprintf("failed %d times in a row: %s", m.strikes, m.lastErr)
	}
	stamped := why + ", " + h.o.Now().Format("2006-01-02 15:04")
	h.unload(ctx, m)

	h.mu.Lock()
	e := h.state[m.name]
	e.on, e.disabled = false, stamped
	h.state[m.name] = e
	m.info.Status, m.info.Reason = StatusDisabled, stamped
	saveErr := saveState(h.o.StatePath, h.state)
	h.mu.Unlock()

	h.o.Logger.Warn("switched a mod off", "mod", m.name, "reason", why)
	if saveErr != nil {
		h.o.Logger.Error("could not record that a mod was switched off", "mod", m.name, "err", saveErr)
	}
	return fmt.Errorf("mod %s: %w: %s", m.name, ErrDisabled, why)
}

// record keeps how long a call took.
func (h *Host) record(m *mod, took time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m.info.Calls++
	if len(m.times) == keptTimes {
		m.times = append(m.times[:0], m.times[1:]...)
	}
	m.times = append(m.times, took)
}

// SetEnabled switches a mod on or off and records it in the state file.
//
// Switching on reads the manifest and the module again, records the
// module's hash, and loads the mod as Load does; it also lifts a switch-off
// by the host. A mod that cannot be loaded comes back as StatusFailed with
// its reason and no error. The error is for a mod that does not exist and a
// state file that cannot be written.
func (h *Host) SetEnabled(ctx context.Context, name string, on bool) (Info, error) {
	h.admin.Lock()
	defer h.admin.Unlock()
	if h.isClosed() {
		return Info{}, errClosed
	}
	m := h.lookup(name)
	if m == nil {
		return Info{}, fmt.Errorf("no mod named %q", clip(name))
	}
	m.call.Lock()
	defer m.call.Unlock()
	h.unload(ctx, m)
	if on {
		if err := h.load(ctx, m, true); err != nil {
			return h.info(m), err
		}
		return h.info(m), nil
	}

	man, manErr := h.readManifest(name)
	h.mu.Lock()
	e := h.state[name]
	e.on, e.disabled = false, ""
	h.state[name] = e
	m.info = h.rest(name, man, manErr)
	m.times = nil
	err := saveState(h.o.StatePath, h.state)
	h.mu.Unlock()
	return h.info(m), err
}

// load brings one mod up from its files. With switchOn the user has just
// asked for it, so the module's hash is recorded; without, the hash must be
// the one recorded, and a module that was swapped is switched off. Every
// failure but one ends as StatusFailed and a nil error; the error is a state
// file that could not be written. admin and m.call are held.
func (h *Host) load(ctx context.Context, m *mod, switchOn bool) error {
	man, err := h.readManifest(m.name)
	if err != nil {
		h.fail(m, Manifest{Name: m.name}, err)
		return nil
	}
	bin, err := readRegular(filepath.Join(h.o.Dir, m.name, man.Module), MaxModule)
	if err != nil {
		h.fail(m, man, fmt.Errorf("module: %w", err))
		return nil
	}
	sum := hashOf(bin)
	if man.SHA256 != "" && man.SHA256 != sum {
		h.fail(m, man, errors.New("the module does not match the sha256 in "+ManifestFile))
		return nil
	}
	if stop, err := h.admit(m, man, sum, switchOn); stop || err != nil {
		return err
	}

	rt, err := h.runtime(ctx)
	if err != nil {
		h.fail(m, man, err)
		return nil
	}
	cm, err := rt.CompileModule(ctx, bin)
	if err != nil {
		h.fail(m, man, fmt.Errorf("not a module the host can run: %w", describe(err)))
		return nil
	}
	if err := checkModule(cm); err != nil {
		_ = cm.Close(context.WithoutCancel(ctx))
		h.fail(m, man, err)
		return nil
	}
	m.rt, m.compiled = rt, cm
	if err := h.hello(ctx, m, man); err != nil {
		h.unload(ctx, m)
		h.fail(m, man, err)
		return nil
	}

	h.mu.Lock()
	m.info = Info{Manifest: man, Status: StatusOn}
	m.times = nil
	h.mu.Unlock()
	h.o.Logger.Info("loaded a mod", "mod", m.name, "adds", man.summary())
	return nil
}

// admit settles the state before a module is compiled. Switching on records
// the hash. Otherwise the hash must match the record: a module that changed
// since the user switched it on is off until they do so again, and stop is
// true.
func (h *Host) admit(m *mod, man Manifest, sum string, switchOn bool) (stop bool, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.state[m.name]
	switch {
	case switchOn:
		e.on, e.sha256, e.disabled = true, sum, ""
	case e.sha256 != sum:
		e.on = false
		m.info = Info{
			Manifest: man, Status: StatusOff,
			Reason: "the module changed since it was switched on",
		}
		stop = true
	default:
		return false, nil
	}
	h.state[m.name] = e
	return stop, saveState(h.o.StatePath, h.state)
}

// hello starts the mod's first instance and checks that it answers in this
// ABI. m.call is held.
func (h *Host) hello(ctx context.Context, m *mod, man Manifest) error {
	body, err := encodeRequest(buildRequest(man, OpHello, "", Input{}))
	if err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	if man.Has(GrantClock) {
		m.wall = h.o.Now().UnixMilli()
	}
	if _, _, _, err := h.exchange(ctx, m, man, body); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	return nil
}

// fail marks a mod that could not be loaded.
func (h *Host) fail(m *mod, man Manifest, err error) {
	why := reason(err)
	h.mu.Lock()
	m.info = Info{Manifest: man, Status: StatusFailed, Reason: why}
	m.times = nil
	h.mu.Unlock()
	h.o.Logger.Warn("could not load a mod", "mod", m.name, "reason", why)
}

// runtime returns the shared runtime, making it on first use. admin is held.
func (h *Host) runtime(ctx context.Context) (wazero.Runtime, error) {
	if h.rt != nil {
		return h.rt, nil
	}
	if h.o.CacheDir != "" {
		if err := os.MkdirAll(h.o.CacheDir, 0o700); err != nil {
			h.o.Logger.Warn("could not make the compilation cache directory", "err", err)
		}
	}
	cache, err := newCache(h.o.CacheDir)
	if err != nil {
		h.o.Logger.Warn("compiled mods will not be kept", "err", err)
	}
	rt, err := newRuntime(context.WithoutCancel(ctx), cache, h.o.MemoryPages)
	if err != nil {
		_ = cache.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	h.rt, h.cache = rt, cache
	return rt, nil
}

// Close stops every mod and frees the runtime. The host cannot be used
// afterwards; closing it twice is harmless.
func (h *Host) Close(ctx context.Context) error {
	h.admin.Lock()
	defer h.admin.Unlock()
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	for _, m := range h.list() {
		m.call.Lock()
		h.unload(ctx, m)
		m.call.Unlock()
	}
	ctx = context.WithoutCancel(ctx)
	var errs []error
	if h.rt != nil {
		errs = append(errs, h.rt.Close(ctx))
		h.rt = nil
	}
	if h.cache != nil {
		errs = append(errs, h.cache.Close(ctx))
		h.cache = nil
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("close mods: %w", err)
	}
	return nil
}

func (h *Host) lookup(name string) *mod {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mods[name]
}

func (h *Host) isClosed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

// list returns the mods in name order.
func (h *Host) list() []*mod {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*mod, 0, len(h.mods))
	for _, name := range slices.Sorted(maps.Keys(h.mods)) {
		out = append(out, h.mods[name])
	}
	return out
}

// info returns a mod's info as Mods would list it. A closed host has no mod
// that is on.
func (h *Host) info(m *mod) Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	info := m.snapshot()
	if h.closed && info.Status == StatusOn {
		info.Status = StatusOff
	}
	return info
}

// hashOf is the lower-case hex SHA-256 of a module.
func hashOf(bin []byte) string {
	sum := sha256.Sum256(bin)
	return hex.EncodeToString(sum[:])
}
