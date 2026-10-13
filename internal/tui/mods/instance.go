package mods

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// The names ABI 1 fixes.
const (
	exportMemory = "memory"
	exportInit   = "_initialize"
	exportAlloc  = "mod_alloc"
	exportCall   = "mod_call"
	wasiModule   = wasi_snapshot_preview1.ModuleName
	socketPrefix = "sock_"
)

// newRuntime builds the one runtime every mod shares. A call whose context
// ends is stopped, and its instance closed; no module may have more memory
// than pages.
func newRuntime(ctx context.Context, cache wazero.CompilationCache, pages uint32) (wazero.Runtime, error) {
	cfg := wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithDebugInfoEnabled(false).
		WithMemoryLimitPages(pages).
		WithCompilationCache(cache)
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("wasi: %w", err)
	}
	return rt, nil
}

// newCache opens the compilation cache in dir, or one in memory when there
// is no directory or it cannot be used.
func newCache(dir string) (wazero.CompilationCache, error) {
	if dir == "" {
		return wazero.NewCompilationCache(), nil
	}
	cache, err := wazero.NewCompilationCacheWithDir(dir)
	if err != nil {
		return wazero.NewCompilationCache(), fmt.Errorf("compilation cache: %w", err)
	}
	return cache, nil
}

// checkModule refuses a module that could reach anything but its requests,
// or that does not have the four exports of the ABI. It reads the compiled
// module's own lists, so nothing of the module has run.
func checkModule(cm wazero.CompiledModule) error {
	for _, f := range cm.ImportedFunctions() {
		mod, name, _ := f.Import()
		if mod != wasiModule {
			return fmt.Errorf("the module imports %s.%s: only %s is allowed", clip(mod), clip(name), wasiModule)
		}
		if strings.HasPrefix(name, socketPrefix) {
			return fmt.Errorf("the module imports %s.%s: sockets are not allowed", wasiModule, clip(name))
		}
	}
	if len(cm.ImportedMemories()) > 0 {
		return errors.New("the module imports a memory: it must export its own")
	}
	if _, ok := cm.ExportedMemories()[exportMemory]; !ok {
		return errors.New(`the module does not export "memory"`)
	}
	i32, i64 := api.ValueTypeI32, api.ValueTypeI64
	want := []struct {
		name            string
		params, results []api.ValueType
	}{
		{exportInit, nil, nil},
		{exportAlloc, []api.ValueType{i32}, []api.ValueType{i32}},
		{exportCall, []api.ValueType{i32, i32}, []api.ValueType{i64}},
	}
	funcs := cm.ExportedFunctions()
	for _, w := range want {
		f, ok := funcs[w.name]
		if !ok {
			return fmt.Errorf("the module does not export %q", w.name)
		}
		if !sameTypes(f.ParamTypes(), w.params) || !sameTypes(f.ResultTypes(), w.results) {
			return fmt.Errorf("the module's %q has the wrong signature", w.name)
		}
	}
	return nil
}

func sameTypes(a, b []api.ValueType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// clip makes a name from a module safe and short enough for an error.
func clip(s string) string {
	const limit = 60
	clean := make([]byte, 0, limit)
	for i := 0; i < len(s) && len(clean) < limit; i++ {
		c := s[i]
		if c < 0x21 || c > 0x7E {
			c = '?'
		}
		clean = append(clean, c)
	}
	return string(clean)
}

// instance is one running copy of a mod's module.
type instance struct {
	mod   api.Module
	alloc api.Function
	call  api.Function
	stack [2]uint64
}

// instantiate starts a module: memory, then _initialize. The instance has no
// files, environment, arguments or output. Its monotonic clock is the real
// one, because wazero's fake one makes a Go guest's collector fall behind
// and its memory grow. It cannot sleep: the fake sleep returns at once.
//
// wall, when it is not nil, is where the guest's wall clock reads the time
// from, in unix ms; without it the wall clock is wazero's fake one.
func instantiate(ctx context.Context, rt wazero.Runtime, cm wazero.CompiledModule, wall *int64) (*instance, error) {
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithStartFunctions(exportInit).
		WithSysNanotime()
	if wall != nil {
		cfg = cfg.WithWalltime(func() (int64, int32) {
			ms := *wall
			return ms / 1000, int32(ms%1000) * 1_000_000
		}, sys.ClockResolution(time.Millisecond))
	}
	mod, err := rt.InstantiateModule(ctx, cm, cfg)
	if err != nil {
		if mod != nil {
			_ = mod.Close(ctx)
		}
		return nil, fmt.Errorf("start: %w", describe(err))
	}
	return &instance{
		mod:   mod,
		alloc: mod.ExportedFunction(exportAlloc),
		call:  mod.ExportedFunction(exportCall),
	}, nil
}

// roundTrip is one call: ask the guest for room, write the request there,
// run it, and copy the reply out. The copy matters: the guest keeps the
// reply only until the next call.
func (i *instance) roundTrip(ctx context.Context, req []byte) ([]byte, error) {
	i.stack[0] = uint64(len(req))
	if err := i.alloc.CallWithStack(ctx, i.stack[:1]); err != nil {
		return nil, fmt.Errorf("%s: %w", exportAlloc, describe(err))
	}
	ptr := api.DecodeU32(i.stack[0])
	if !i.mod.Memory().Write(ptr, req) {
		return nil, fmt.Errorf("%s returned a pointer outside the module's memory", exportAlloc)
	}
	i.stack[0], i.stack[1] = api.EncodeU32(ptr), uint64(len(req))
	if err := i.call.CallWithStack(ctx, i.stack[:2]); err != nil {
		return nil, fmt.Errorf("%s: %w", exportCall, describe(err))
	}
	at, n := api.DecodeU32(i.stack[0]>>32), api.DecodeU32(i.stack[0])
	if n > MaxMessage {
		return nil, fmt.Errorf("the reply is %d bytes: the limit is %d", n, MaxMessage)
	}
	view, ok := i.mod.Memory().Read(at, n)
	if !ok {
		return nil, errors.New("the reply is outside the module's memory")
	}
	return append([]byte(nil), view...), nil
}

// memoryBytes is how much memory the instance has now.
func (i *instance) memoryBytes() uint32 { return i.mod.Memory().Size() }

// close frees the instance. A context that has ended still closes it.
func (i *instance) close(ctx context.Context) {
	_ = i.mod.Close(context.WithoutCancel(ctx))
}

// errDeadline is a call that ran until the host stopped it.
var errDeadline = errors.New("stopped at the deadline")

// describe turns a wazero error into one short line. A call stopped at its
// deadline becomes errDeadline; the stack trace wazero appends is dropped.
func describe(err error) error {
	if exit, ok := errors.AsType[*sys.ExitError](err); ok {
		switch exit.ExitCode() {
		case sys.ExitCodeDeadlineExceeded, sys.ExitCodeContextCanceled:
			return errDeadline
		}
		return fmt.Errorf("the module exited with code %d", exit.ExitCode())
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return errDeadline
	}
	msg, _, _ := strings.Cut(err.Error(), "\n")
	msg = strings.TrimSuffix(msg, " (recovered by wazero)")
	return errors.New(clipText(msg))
}

// clipText keeps an error message printable and short.
func clipText(s string) string {
	const limit = 160
	out := make([]byte, 0, limit)
	for i := 0; i < len(s) && len(out) < limit; i++ {
		c := s[i]
		if c < 0x20 || c > 0x7E {
			c = '?'
		}
		out = append(out, c)
	}
	return string(out)
}
