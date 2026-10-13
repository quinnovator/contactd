package mods

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

func TestMain(m *testing.M) { tuitest.Main(m) }

// ---- a hand-assembled module ----------------------------------------------

// The memory of a tiny module. A request the host writes goes to tinyInput;
// the two replies are data placed below it.
const (
	tinyShortAt = 1024
	tinyLongAt  = 8192
	tinyInput   = 65536
	tinyPages   = 3
	// tinyTrigger is the request length from which a tiny module does what
	// its long field says. The hello request is far shorter, so a module
	// that misbehaves still loads.
	tinyTrigger = 512
)

// behaviour is what a tiny module does with a long request.
type behaviour uint8

const (
	// same answers a long request as a short one.
	same behaviour = iota + 1
	// replyLong answers with the module's second reply.
	replyLong
	// spin loops forever.
	spin
	// trap executes unreachable.
	trap
	// badptr returns a reply address outside the module's memory.
	badptr
	// oversize returns a reply one byte over the limit.
	oversize
	// edge returns a reply that starts in the memory and ends outside it.
	edge
	// empty returns a reply of no bytes.
	empty
	// recurse calls itself until the stack runs out.
	recurse
)

// tiny describes a module small enough to assemble by hand, so the host's
// tests need no compiler and decide exactly what the guest does.
type tiny struct {
	// short is the reply to a request under tinyTrigger bytes, hello
	// included. long is what a longer request gets, and longReply the reply
	// when that is replyLong.
	short     string
	long      behaviour
	longReply string
	// trigger replaces tinyTrigger when it is not zero. One makes every
	// request long.
	trigger int64
	// imports are extra function imports, as module and name pairs.
	imports [][2]string
	// otherImports are whole import entries that are not functions.
	otherImports [][]byte
	// importMemory makes the module import its memory and define none.
	importMemory bool
	// init is what _initialize does: nothing, spin or trap. start names
	// _initialize in the start section too, so that it runs as soon as the
	// module is instantiated, whether or not the host asks for it.
	init  behaviour
	start bool
	// alloc is what mod_alloc does instead of answering: spin or trap.
	alloc behaviour
	// pages replaces tinyPages when it is not zero.
	pages int64
	// without leaves out the export of this name. callLikeAlloc exports
	// mod_call with mod_alloc's signature.
	without       string
	callLikeAlloc bool
}

// okReply is the reply of a module that has nothing to say.
const okReply = `{"abi":1,"text":"ok"}`

// reply is a tiny module that answers every request with one reply.
func reply(json string) tiny { return tiny{short: json, long: same} }

// misbehaving is a tiny module that loads, answers short requests, and does
// b with a long one.
func misbehaving(b behaviour) tiny { return tiny{short: okReply, long: b} }

// extraImport is a tiny module that imports env.x.
func extraImport() tiny {
	return tiny{short: okReply, long: same, imports: [][2]string{{"env", "x"}}}
}

// importedGlobal is the import entry of an immutable i32 global, and
// importedTable that of a table of function references.
func importedGlobal(module, name string) []byte {
	return append(append(wasmName(module), wasmName(name)...), 3, 0x7f, 0)
}

func importedTable(module, name string) []byte {
	return append(append(wasmName(module), wasmName(name)...), 1, 0x70, 0, 0)
}

// longArgs makes a request long enough to trigger a tiny module.
func longArgs() []string { return []string{strings.Repeat("x", 2*tinyTrigger)} }

func uleb(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

func sleb(v int64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

func wasmName(s string) []byte { return append(uleb(uint64(len(s))), s...) }

func wasmSection(id byte, body []byte) []byte {
	return append(append([]byte{id}, uleb(uint64(len(body)))...), body...)
}

// wasmVec is a count followed by the items.
func wasmVec(items ...[]byte) []byte {
	out := uleb(uint64(len(items)))
	for _, it := range items {
		out = append(out, it...)
	}
	return out
}

// packed is a reply's place as mod_call returns it.
func packed(at, n uint64) int64 { return int64(at<<32 | n) }

// binary assembles the module.
func (t tiny) binary() []byte {
	const (
		i32, i64    = 0x7f, 0x7e
		typeAlloc   = 0 // (i32) -> i32
		typeCall    = 1 // (i32, i32) -> i64
		typeNothing = 2 // () -> ()
	)
	trigger, pages := int64(tinyTrigger), int64(tinyPages)
	if t.trigger != 0 {
		trigger = t.trigger
	}
	if t.pages != 0 {
		pages = t.pages
	}

	m := []byte{0, 'a', 's', 'm', 1, 0, 0, 0}
	m = append(m, wasmSection(1, wasmVec(
		[]byte{0x60, 1, i32, 1, i32},
		[]byte{0x60, 2, i32, i32, 1, i64},
		[]byte{0x60, 0, 0},
	))...)

	imports := make([][]byte, 0, len(t.imports)+len(t.otherImports)+1)
	for _, imp := range t.imports {
		imports = append(imports, append(append(wasmName(imp[0]), wasmName(imp[1])...), 0, typeNothing))
	}
	imports = append(imports, t.otherImports...)
	if t.importMemory {
		imports = append(imports, append(append(wasmName("env"), wasmName("mem")...), 2, 0, tinyPages))
	}
	if len(imports) > 0 {
		m = append(m, wasmSection(2, wasmVec(imports...))...)
	}

	callType := byte(typeCall)
	if t.callLikeAlloc {
		callType = typeAlloc
	}
	m = append(m, wasmSection(3, wasmVec([]byte{typeNothing}, []byte{typeAlloc}, []byte{callType}))...)
	if !t.importMemory {
		m = append(m, wasmSection(5, wasmVec(append([]byte{0}, uleb(uint64(pages))...)))...)
	}
	m = append(m, wasmSection(7, t.exports())...)
	if t.start {
		m = append(m, wasmSection(8, []byte{byte(len(t.imports))})...)
	}
	m = append(m, wasmSection(10, wasmVec(
		wasmBody(misbehave(t.init)),
		wasmBody(append(misbehave(t.alloc), append([]byte{0x41}, sleb(tinyInput)...)...)),
		wasmBody(t.callBody(trigger, pages)),
	))...)

	data := [][]byte{wasmData(tinyShortAt, t.short)}
	if t.longReply != "" {
		data = append(data, wasmData(tinyLongAt, t.longReply))
	}
	return append(m, wasmSection(11, wasmVec(data...))...)
}

// exports lists the module's exports. An imported function takes an index
// before the module's own.
func (t tiny) exports() []byte {
	first := byte(len(t.imports))
	all := []struct {
		name        string
		kind, index byte
	}{
		{exportMemory, 2, 0},
		{exportInit, 0, first},
		{exportAlloc, 0, first + 1},
		{exportCall, 0, first + 2},
	}
	var items [][]byte
	for _, e := range all {
		if e.name != t.without {
			items = append(items, append(wasmName(e.name), e.kind, e.index))
		}
	}
	return wasmVec(items...)
}

// misbehave is the code of a function that spins or traps, and no code for
// any other behaviour.
func misbehave(b behaviour) []byte {
	switch b {
	case spin:
		return []byte{0x03, 0x40, 0x0c, 0x00, 0x0b} // loop; br 0; end
	case trap:
		return []byte{0x00} // unreachable
	case same, replyLong, badptr, oversize, edge, empty, recurse:
	}
	return nil
}

// callBody is mod_call: do the long thing when the request is long, and
// otherwise answer with the short reply.
func (t tiny) callBody(trigger, pages int64) []byte {
	if t.callLikeAlloc {
		return append([]byte{0x41}, sleb(0)...)
	}
	ret := func(v int64) []byte { return append(append([]byte{0x42}, sleb(v)...), 0x0f) }
	var long []byte
	switch t.long {
	case same:
	case replyLong:
		long = ret(packed(tinyLongAt, uint64(len(t.longReply))))
	case spin, trap:
		long = misbehave(t.long)
	case badptr:
		long = ret(packed(0xfffffff0, 16))
	case oversize:
		long = ret(packed(0, MaxMessage+1))
	case edge:
		long = ret(packed(uint64(pages)<<16-1, 2))
	case empty:
		long = ret(packed(tinyShortAt, 0))
	case recurse:
		// local.get 0; local.get 1; call mod_call; return
		long = []byte{0x20, 0x00, 0x20, 0x01, 0x10, byte(len(t.imports)) + 2, 0x0f}
	}
	body := []byte{0x20, 0x01, 0x41} // local.get 1; i32.const trigger
	body = append(body, sleb(trigger)...)
	body = append(body, 0x4f, 0x04, 0x40) // i32.ge_u; if
	body = append(body, long...)
	body = append(body, 0x0b, 0x42) // end; i64.const
	return append(body, sleb(packed(tinyShortAt, uint64(len(t.short))))...)
}

// wasmBody is a function with no locals.
func wasmBody(code []byte) []byte {
	body := append(append([]byte{0}, code...), 0x0b)
	return append(uleb(uint64(len(body))), body...)
}

func wasmData(at int64, s string) []byte {
	seg := append(append([]byte{0, 0x41}, sleb(at)...), 0x0b)
	return append(append(seg, uleb(uint64(len(s)))...), s...)
}

// ---- hosts and mods on disk -----------------------------------------------

// stepClock is a clock that moves only when it is read: each reading is step
// later than the one before. A call reads it twice, so with a step of 10 ms
// every call takes 10 ms, however fast the machine.
type stepClock struct {
	mu   sync.Mutex
	now  time.Time
	step time.Duration
}

func newClock() *stepClock {
	return &stepClock{now: time.Date(2026, 10, 12, 12, 4, 0, 0, time.UTC)}
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.now
	c.now = c.now.Add(c.step)
	return t
}

func (c *stepClock) set(step time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.step = step
}

// over is a step that puts every call over the default budget.
const over = 10 * time.Millisecond

// shortKill gives a host the 20 ms deadline of the tests about a module that
// never returns. Starting an instance then has 400 ms.
func shortKill(o *Options) { o.Kill = 20 * time.Millisecond }

// boundedStart is for a module that must be refused before it runs, and that
// would spin as it starts if it were run: a host that got that wrong fails
// its test after 20 s, where the usual minute's deadline would have it wait
// 20 minutes.
func boundedStart(o *Options) { o.Kill = time.Second }

// bed is the directories a host works in, and its clock.
type bed struct {
	dir, state string
	clock      *stepClock
}

func newBed(tb testing.TB) bed {
	tb.Helper()
	root := tb.TempDir()
	return bed{
		dir:   filepath.Join(root, "mods"),
		state: filepath.Join(root, "mods.toml"),
		clock: newClock(),
	}
}

// open opens a host on the bed and closes it when the test ends. tune may
// change the options.
//
// The kill deadline is a minute unless tune sets it. These tests run beside
// many others under the race detector, where a call that takes microseconds
// can be kept waiting past 50 ms; only the tests about the deadline want a
// short one.
func (b bed) open(tb testing.TB, tune func(*Options)) *Host {
	tb.Helper()
	o := Options{Dir: b.dir, StatePath: b.state, Now: b.clock.Now, Kill: time.Minute}
	if tune != nil {
		tune(&o)
	}
	h, err := Open(o)
	if err != nil {
		tb.Fatalf("Open: %v", err)
	}
	tb.Cleanup(func() {
		if err := h.Close(context.WithoutCancel(tb.Context())); err != nil {
			tb.Errorf("Close: %v", err)
		}
	})
	return h
}

// manifestFor is the smallest manifest for a mod with one command, "go",
// and one segment, "seg". extra is appended.
func manifestFor(name, extra string) string {
	return `abi = 1
name = "` + name + `"
version = "1.0.0"
description = "a test mod"
module = "mod.wasm"
` + extra + `
[[segment]]
name = "seg"
refresh = "turn"

[[command]]
name = "go"
`
}

// put writes a mod's two files into a directory.
func put(tb testing.TB, dir, manifest string, module []byte) {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(manifest), 0o600); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mod.wasm"), module, 0o600); err != nil {
		tb.Fatal(err)
	}
}

// add puts a tiny mod into the bed's mods directory.
func (b bed) add(tb testing.TB, name string, mod tiny) {
	tb.Helper()
	put(tb, filepath.Join(b.dir, name), manifestFor(name, ""), mod.binary())
}

// one is the usual start of a test: a bed holding one tiny mod, "m", and a
// host that has switched it on.
func one(tb testing.TB, mod tiny, tune func(*Options)) (bed, *Host) {
	tb.Helper()
	b := newBed(tb)
	b.add(tb, "m", mod)
	h := b.open(tb, tune)
	switchOn(tb, h, "m")
	return b, h
}

// switchOn switches a mod on and fails the test unless it loads.
func switchOn(tb testing.TB, h *Host, name string) {
	tb.Helper()
	info, err := h.SetEnabled(tb.Context(), name, true)
	if err != nil {
		tb.Fatalf("SetEnabled(%s): %v", name, err)
	}
	if info.Status != StatusOn {
		tb.Fatalf("mod %s is %s: %s", name, info.Status, info.Reason)
	}
}

// infoOf returns one mod's info from Mods.
func infoOf(tb testing.TB, h *Host, name string) Info {
	tb.Helper()
	for _, info := range h.Mods() {
		if info.Manifest.Name == name {
			return info
		}
	}
	tb.Fatalf("no mod %s in Mods()", name)
	return Info{}
}

// starts is how many instances a mod has had.
func starts(h *Host, name string) int {
	m := h.lookup(name)
	m.call.Lock()
	defer m.call.Unlock()
	return m.starts
}

// stateOf reads the bed's state file.
func (b bed) stateOf(t *testing.T) state {
	t.Helper()
	st, err := loadState(b.state)
	if err != nil {
		t.Fatalf("read the state file: %v", err)
	}
	return st
}

// ---- real guests -----------------------------------------------------------

// buildGuest compiles a Go mod the way `mise run mods:build` does and
// returns the module.
func buildGuest(tb testing.TB, pkg string) []byte {
	tb.Helper()
	out := filepath.Join(tb.TempDir(), "mod.wasm")
	cmd := exec.CommandContext(tb.Context(), "go", "build",
		"-buildmode=c-shared", "-trimpath", "-ldflags=-s -w", "-o", out, pkg)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOFLAGS=")
	if msg, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("build %s: %v\n%s", pkg, err, msg)
	}
	bin, err := os.ReadFile(out)
	if err != nil {
		tb.Fatal(err)
	}
	return bin
}
