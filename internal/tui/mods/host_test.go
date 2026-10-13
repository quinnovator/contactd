package mods

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
)

// short sends a tiny mod a request it answers, and long one that triggers
// what the module was built to do.
func short(t *testing.T, h *Host) (wire.Reply, error) {
	t.Helper()
	return h.Call(t.Context(), "m", OpSegment, "seg", Input{})
}

func long(t *testing.T, h *Host) (wire.Reply, error) {
	t.Helper()
	return h.Call(t.Context(), "m", OpCommand, "go", Input{Args: longArgs()})
}

// mustShort is a short call that has to succeed.
func mustShort(t *testing.T, h *Host) wire.Reply {
	t.Helper()
	r, err := short(t, h)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	return r
}

func TestCallReturnsTheReply(t *testing.T) {
	t.Parallel()
	_, h := one(t, tiny{
		short: `{"abi":1,"text":"cache 61%","short":"61%","tone":"warn","later":true}`,
		long:  replyLong, longReply: `{"abi":1,"note":"long","lines":[[{"t":"a","tone":"dim","bold":true}]]}`,
	}, nil)

	got := mustShort(t, h)
	want := wire.Reply{ABI: 1, Text: "cache 61%", Short: "61%", Tone: "warn"}
	if !equalReply(got, want) {
		t.Errorf("short reply = %+v, want %+v", got, want)
	}
	got, err := long(t, h)
	if err != nil {
		t.Fatal(err)
	}
	want = wire.Reply{ABI: 1, Note: "long", Lines: []wire.Line{{{T: "a", Tone: "dim", Bold: true}}}}
	if !equalReply(got, want) {
		t.Errorf("long reply = %+v, want %+v", got, want)
	}
	if n := starts(h, "m"); n != 1 {
		t.Errorf("the mod had %d instances, want the one kept between calls", n)
	}
}

func equalReply(a, b wire.Reply) bool {
	if len(a.Lines) != len(b.Lines) {
		return false
	}
	for i := range a.Lines {
		if !slices.Equal(a.Lines[i], b.Lines[i]) {
			return false
		}
	}
	a.Lines, b.Lines = nil, nil
	return a.ABI == b.ABI && a.Text == b.Text && a.Short == b.Short && a.Tone == b.Tone &&
		a.Insert == b.Insert && a.Note == b.Note && a.Pass == b.Pass && a.Error == b.Error
}

func TestCallRefusesWhatIsNotDeclared(t *testing.T) {
	t.Parallel()
	_, h := one(t, reply(okReply), nil)
	cases := []struct {
		name, mod, op, call, want string
	}{
		{"a mod that is not installed", "nope", OpSegment, "seg", `no mod named "nope"`},
		{"a segment the manifest lacks", "m", OpSegment, "other", `declares no segment "other"`},
		{"a command the manifest lacks", "m", OpCommand, "seg", `declares no command "seg"`},
		{"a renderer the manifest lacks", "m", OpRender, "code:x", `declares no render "code:x"`},
		{"hello is the host's own", "m", OpHello, "", `unknown op "hello"`},
		{"an op that does not exist", "m", "panel", "seg", `unknown op "panel"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := h.Call(t.Context(), tc.mod, tc.op, tc.call, Input{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Call error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadRefusals(t *testing.T) {
	t.Parallel()
	bin := func(m tiny) []byte { return m.binary() }
	cases := []struct {
		name     string
		manifest string // extra root keys
		module   []byte
		tune     func(*Options)
		want     string
	}{
		{
			name: "an import outside WASI", module: bin(extraImport()),
			want: "the module imports env.x: only wasi_snapshot_preview1 is allowed",
		},
		{
			name: "a socket import",
			module: bin(tiny{
				short: okReply, long: same,
				imports: [][2]string{{"wasi_snapshot_preview1", "sock_accept"}},
			}),
			want: "the module imports wasi_snapshot_preview1.sock_accept: sockets are not allowed",
		},
		{
			// Were any of the module run, its start function would spin
			// until the deadline and that would be the reason.
			name: "an import outside WASI, in a module that spins as it starts",
			module: bin(tiny{
				short: okReply, long: same, start: true, init: spin,
				imports: [][2]string{{"env", "x"}},
			}),
			tune: boundedStart,
			want: "the module imports env.x: only wasi_snapshot_preview1 is allowed",
		},
		{
			name: "a socket import, in a module that spins as it starts",
			module: bin(tiny{
				short: okReply, long: same, start: true, init: spin,
				imports: [][2]string{{"wasi_snapshot_preview1", "sock_shutdown"}},
			}),
			tune: boundedStart,
			want: "the module imports wasi_snapshot_preview1.sock_shutdown: sockets are not allowed",
		},
		{
			name: "another module's name with an escape sequence in it",
			module: bin(tiny{
				short: okReply, long: same,
				imports: [][2]string{{"\x1b]0;owned\x07", "\x1b[2J"}},
			}),
			want: "the module imports ?]0;owned?.?[2J: only wasi_snapshot_preview1 is allowed",
		},
		{
			name: "an imported memory", module: bin(tiny{short: okReply, long: same, importMemory: true}),
			want: "the module imports a memory",
		},
		{
			name: "an imported global",
			module: bin(tiny{
				short: okReply, long: same, start: true, init: spin,
				otherImports: [][]byte{importedGlobal("env", "g")},
			}),
			tune: boundedStart,
			want: "start: module[env] not instantiated",
		},
		{
			name: "an imported table",
			module: bin(tiny{
				short: okReply, long: same, start: true, init: spin,
				otherImports: [][]byte{importedTable("env", "t")},
			}),
			tune: boundedStart,
			want: "start: module[env] not instantiated",
		},
		{
			name: "a global imported from WASI, which has none",
			module: bin(tiny{
				short: okReply, long: same, start: true, init: spin,
				otherImports: [][]byte{importedGlobal("wasi_snapshot_preview1", "g")},
			}),
			tune: boundedStart,
			want: `start: "g" is not exported in module "wasi_snapshot_preview1"`,
		},
		{
			name: "a start function that never returns", module: bin(tiny{short: okReply, long: same, start: true, init: spin}),
			tune: shortKill,
			want: "start: stopped at the deadline",
		},
		{
			name: "an _initialize that never returns", module: bin(tiny{short: okReply, long: same, init: spin}),
			tune: shortKill,
			want: "start: stopped at the deadline",
		},
		{
			name: "an _initialize that traps", module: bin(tiny{short: okReply, long: same, init: trap}),
			want: "start: ",
		},
		{
			name: "a mod_alloc that answers outside the memory", module: bin(tiny{short: okReply, long: same, pages: 1}),
			want: "hello: mod_alloc returned a pointer outside the module's memory",
		},
		{
			name: "a mod_alloc that never returns", module: bin(tiny{short: okReply, long: same, alloc: spin}),
			tune: shortKill,
			want: "hello: mod_alloc: stopped at the deadline",
		},
		{
			name: "a mod_alloc that traps", module: bin(tiny{short: okReply, long: same, alloc: trap}),
			want: "hello: mod_alloc: ",
		},
		{
			name: "no memory export", module: bin(tiny{short: okReply, long: same, without: exportMemory}),
			want: `the module does not export "memory"`,
		},
		{
			name: "no _initialize", module: bin(tiny{short: okReply, long: same, without: exportInit}),
			want: `the module does not export "_initialize"`,
		},
		{
			name: "no mod_alloc", module: bin(tiny{short: okReply, long: same, without: exportAlloc}),
			want: `the module does not export "mod_alloc"`,
		},
		{
			name: "no mod_call", module: bin(tiny{short: okReply, long: same, without: exportCall}),
			want: `the module does not export "mod_call"`,
		},
		{
			name: "mod_call with the wrong signature", module: bin(tiny{short: okReply, callLikeAlloc: true}),
			want: `the module's "mod_call" has the wrong signature`,
		},
		{
			name: "a file that is not a module", module: []byte("#!/bin/sh\nrm -rf /\n"),
			want: "not a module the host can run",
		},
		{
			name: "more memory than a mod may have", module: bin(tiny{short: okReply, long: same, pages: 300}),
			want: "not a module the host can run",
		},
		{
			name: "a module over 16 MiB", module: make([]byte, MaxModule+1),
			want: "the limit is 16777216",
		},
		{
			name: "a hello answered in another abi", module: bin(reply(`{"abi":2}`)),
			want: "hello: the reply's abi is 2, not 1",
		},
		{
			name: "a hello answered with no abi", module: bin(reply(`{"text":"hi"}`)),
			want: "hello: the reply's abi is 0, not 1",
		},
		{
			name: "a hello answered with something else", module: bin(reply(`cache 61%`)),
			want: "hello: the reply is not the JSON of a reply",
		},
		{
			name: "a trap in hello", module: bin(tiny{short: okReply, long: trap, trigger: 1}),
			want: "hello: mod_call:",
		},
		{
			name: "a hello that never returns", module: bin(tiny{short: okReply, long: spin, trigger: 1}),
			tune: shortKill,
			want: "hello: mod_call: stopped at the deadline",
		},
		{
			name:     "a module that is not the one pinned",
			manifest: `sha256 = "` + strings.Repeat("0", 64) + `"`, module: bin(reply(okReply)),
			want: "the module does not match the sha256 in mod.toml",
		},
		{
			name: "a budget above the host's", manifest: `budget = "6ms"`, module: bin(reply(okReply)),
			want: "budget 6ms is above the host's 5ms",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBed(t)
			put(t, filepath.Join(b.dir, "bad"), manifestFor("bad", tc.manifest), tc.module)
			b.add(t, "good", reply(okReply))
			h := b.open(t, tc.tune)

			info, err := h.SetEnabled(t.Context(), "bad", true)
			if err != nil {
				t.Fatalf("SetEnabled: %v", err)
			}
			if info.Status != StatusFailed || !strings.Contains(info.Reason, tc.want) {
				t.Errorf("the mod is %s (%q), want failed with a reason containing %q", info.Status, info.Reason, tc.want)
			}
			if _, err := h.Call(t.Context(), "bad", OpSegment, "seg", Input{}); err == nil {
				t.Error("a mod that failed to load answered a call")
			}

			// A failure affects no other mod.
			switchOn(t, h, "good")
			if _, err := h.Call(t.Context(), "good", OpSegment, "seg", Input{}); err != nil {
				t.Errorf("the good mod beside it: %v", err)
			}
			if got := h.Needs(); len(got) != 0 {
				t.Errorf("Needs() = %v, want none", got)
			}
		})
	}
}

func TestPinnedModuleLoads(t *testing.T) {
	t.Parallel()
	b := newBed(t)
	bin := reply(okReply).binary()
	put(t, filepath.Join(b.dir, "m"), manifestFor("m", `sha256 = "`+strings.ToUpper(hashOf(bin))+`"`), bin)
	h := b.open(t, nil)
	switchOn(t, h, "m")
}

func TestThreeStrikesSwitchTheModOff(t *testing.T) {
	t.Parallel()
	b := newBed(t)
	put(t, filepath.Join(b.dir, "m"), manifestFor("m", `read = ["route", "clock"]`), reply(okReply).binary())
	h := b.open(t, nil)
	switchOn(t, h, "m")
	if got := h.Needs(); !slices.Equal(got, []Grant{GrantRoute, GrantClock}) {
		t.Fatalf("Needs() = %v, want route and clock", got)
	}
	in := Input{Data: wire.Data{Now: 1}} // so the host does not read its clock for the mod's

	b.clock.set(over)
	for i := range 2 {
		// A call that only ran over is a strike, and its reply still counts.
		r, err := h.Call(t.Context(), "m", OpSegment, "seg", in)
		if err != nil || r.Text != "ok" {
			t.Fatalf("call %d: reply %+v, error %v", i+1, r, err)
		}
	}
	_, err := h.Call(t.Context(), "m", OpSegment, "seg", in)
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("the third call over budget: error %v, want one wrapping ErrDisabled", err)
	}
	const why = "over budget 3 times in a row"
	if want := "mod m: the mod was switched off: " + why; err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}

	const stamped = why + ", 2026-10-12 12:04"
	if info := infoOf(t, h, "m"); info.Status != StatusDisabled || info.Reason != stamped {
		t.Errorf("the mod is %s (%q), want disabled (%q)", info.Status, info.Reason, stamped)
	}
	if e := b.stateOf(t)["m"]; e.on || e.disabled != stamped {
		t.Errorf("mods.toml has on=%v disabled=%q, want off and %q", e.on, e.disabled, stamped)
	}
	if got := h.Needs(); len(got) != 0 {
		t.Errorf("Needs() = %v after the mod was switched off, want none", got)
	}

	// The notice is given once. After it the mod is simply not on.
	_, err = h.Call(t.Context(), "m", OpSegment, "seg", in)
	if err == nil || errors.Is(err, ErrDisabled) {
		t.Errorf("a call after the switch-off: error %v, want a plain refusal", err)
	}

	// The next start remembers, and does not load it.
	again := b.open(t, nil)
	if infos := again.Load(t.Context()); len(infos) != 1 || infos[0].Status != StatusDisabled || infos[0].Reason != stamped {
		t.Errorf("after a restart Load() = %+v, want the mod disabled with its reason", infos)
	}

	// Switching it on lifts the switch-off.
	b.clock.set(0)
	switchOn(t, again, "m")
	if e := b.stateOf(t)["m"]; !e.on || e.disabled != "" {
		t.Errorf("mods.toml has on=%v disabled=%q after switching on", e.on, e.disabled)
	}
	if _, err := again.Call(t.Context(), "m", OpSegment, "seg", in); err != nil {
		t.Errorf("a call after switching on again: %v", err)
	}
}

func TestACallWithinBudgetClearsTheStrikes(t *testing.T) {
	t.Parallel()
	b, h := one(t, reply(okReply), nil)
	for round := range 3 {
		b.clock.set(over)
		mustShort(t, h)
		mustShort(t, h)
		b.clock.set(time.Millisecond)
		mustShort(t, h)
		if info := infoOf(t, h, "m"); info.Status != StatusOn {
			t.Fatalf("round %d: the mod is %s: %s", round, info.Status, info.Reason)
		}
	}
	// Exactly at the budget is within it.
	b.clock.set(DefaultBudget)
	for range 5 {
		mustShort(t, h)
	}
	b.clock.set(over)
	mustShort(t, h)
	mustShort(t, h)
	if _, err := short(t, h); !errors.Is(err, ErrDisabled) {
		t.Fatalf("three in a row: error %v, want ErrDisabled", err)
	}
}

func TestAManifestMayLowerTheBudget(t *testing.T) {
	t.Parallel()
	b := newBed(t)
	put(t, filepath.Join(b.dir, "m"), manifestFor("m", `budget = "2ms"`), reply(okReply).binary())
	h := b.open(t, nil)
	switchOn(t, h, "m")
	b.clock.set(3 * time.Millisecond) // within the host's 5 ms, over the mod's 2 ms
	mustShort(t, h)
	mustShort(t, h)
	if _, err := short(t, h); !errors.Is(err, ErrDisabled) {
		t.Fatalf("error %v, want ErrDisabled", err)
	}
}

func TestTheFirstCallAfterAnInstantiateDoesNotStrike(t *testing.T) {
	t.Parallel()
	b, h := one(t, misbehaving(trap), func(o *Options) { o.Strikes = 2 })
	b.clock.set(over) // every call from here on takes 10 ms by the host's clock

	// Strike one: a trap. The instance is closed.
	if _, err := long(t, h); err == nil || errors.Is(err, ErrDisabled) {
		t.Fatalf("a trap: error %v, want the failure and no switch-off yet", err)
	}
	// A new instance answers. It took 10 ms, and that is forgiven: were it a
	// strike, this would be the second and the mod would be off.
	if r, err := short(t, h); err != nil || r.Text != "ok" {
		t.Fatalf("the first call of a new instance: reply %+v, error %v", r, err)
	}
	if n := starts(h, "m"); n != 2 {
		t.Fatalf("the mod had %d instances, want 2", n)
	}
	if info := infoOf(t, h, "m"); info.Status != StatusOn {
		t.Fatalf("the mod is %s: %s", info.Status, info.Reason)
	}
	// It is forgiven, not a call within budget: the first strike stands,
	// so the next slow call is the second.
	_, err := short(t, h)
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("the second call of the instance: error %v, want ErrDisabled", err)
	}
	if want := "failed 2 times in a row: mod_call: "; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err, want)
	}
}

func TestASpinningModIsKilledAndStartedAgain(t *testing.T) {
	t.Parallel()
	_, h := one(t, misbehaving(spin), shortKill)

	_, err := long(t, h)
	if err == nil || !strings.Contains(err.Error(), "stopped at the deadline") {
		t.Fatalf("a call that never returns: error %v, want it stopped at the deadline", err)
	}
	if errors.Is(err, ErrDisabled) {
		t.Fatalf("one kill switched the mod off: %v", err)
	}
	if r := mustShort(t, h); r.Text != "ok" {
		t.Errorf("after the kill the mod answered %+v", r)
	}
	if n := starts(h, "m"); n != 2 {
		t.Errorf("the mod had %d instances, want a second after the kill", n)
	}

	// The call that answered cleared the count. A kill is a strike like
	// running over, so three more in a row switch the mod off.
	for i := range 2 {
		if _, err := long(t, h); err == nil || errors.Is(err, ErrDisabled) {
			t.Fatalf("kill %d in a row: error %v", i+1, err)
		}
	}
	if n := starts(h, "m"); n != 3 {
		t.Errorf("the mod had %d instances, want a new one after each kill that was followed by a call", n)
	}
	_, err = long(t, h)
	if !errors.Is(err, ErrDisabled) || !strings.Contains(err.Error(), "over budget 3 times in a row") {
		t.Fatalf("the third kill: error %v, want ErrDisabled for running over budget", err)
	}
	if info := infoOf(t, h, "m"); info.Status != StatusDisabled {
		t.Errorf("the mod is %s, want disabled", info.Status)
	}
}

func TestAFailedCallClosesTheInstance(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mod  tiny
		want string
	}{
		{"a trap", misbehaving(trap), "mod m: mod_call: "},
		{"a reply outside the memory", misbehaving(badptr), "mod m: the reply is outside the module's memory"},
		{"a reply over 64 KiB", misbehaving(oversize), "mod m: the reply is 65537 bytes: the limit is 65536"},
		{"a reply that runs off the end of the memory", misbehaving(edge), "mod m: the reply is outside the module's memory"},
		{"a reply of no bytes", misbehaving(empty), "mod m: the reply is not the JSON of a reply"},
		{"a call that runs out of stack", misbehaving(recurse), "mod m: mod_call: "},
		{
			"a reply in another abi",
			tiny{short: okReply, long: replyLong, longReply: `{"abi":7,"text":"x"}`},
			"mod m: the reply's abi is 7, not 1",
		},
		{
			"a reply that is not JSON",
			tiny{short: okReply, long: replyLong, longReply: "\x1b]0;owned\x07"},
			"mod m: the reply is not the JSON of a reply",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, h := one(t, tc.mod, nil)
			_, err := long(t, h)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
			if strings.ContainsAny(err.Error(), "\x1b\x07\n") {
				t.Errorf("the error carries the mod's bytes: %q", err)
			}
			m := h.lookup("m")
			m.call.Lock()
			closed, strikes := m.inst == nil, m.strikes
			m.call.Unlock()
			if !closed || strikes != 1 {
				t.Errorf("after the failure: instance closed %v, strikes %d; want closed and 1", closed, strikes)
			}
			if r := mustShort(t, h); r.Text != "ok" {
				t.Errorf("the next call answered %+v", r)
			}
			if n := starts(h, "m"); n != 2 {
				t.Errorf("the mod had %d instances, want 2", n)
			}
			// The call that answered cleared the count. Three failures in
			// a row switch the mod off, and the reason says what failed.
			for i := range 2 {
				if _, err := long(t, h); err == nil || errors.Is(err, ErrDisabled) {
					t.Fatalf("failure %d in a row: error %v", i+1, err)
				}
			}
			_, err = long(t, h)
			if !errors.Is(err, ErrDisabled) || !strings.Contains(err.Error(), "failed 3 times in a row: ") {
				t.Errorf("the third failure: error %v, want ErrDisabled naming the failure", err)
			}
		})
	}
}

func TestAnExpiredCallerContextLeavesTheModAlone(t *testing.T) {
	t.Parallel()
	_, h := one(t, misbehaving(spin), nil)

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	late, stop := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer stop()

	for i := range 5 {
		// Even the request that would spin is not run.
		_, err := h.Call(cancelled, "m", OpCommand, "go", Input{Args: longArgs()})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("call %d with a cancelled context: error %v", i, err)
		}
		_, err = h.Call(late, "m", OpSegment, "seg", Input{})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call %d with an expired context: error %v", i, err)
		}
	}
	info := infoOf(t, h, "m")
	if info.Status != StatusOn || info.Calls != 0 {
		t.Errorf("the mod is %s with %d calls, want on and never called", info.Status, info.Calls)
	}
	if r := mustShort(t, h); r.Text != "ok" {
		t.Errorf("the mod answered %+v", r)
	}
	if n := starts(h, "m"); n != 1 {
		t.Errorf("the mod had %d instances: an expired context closed a healthy one", n)
	}
}

// flipContext is a context that has not ended the first time it is asked and
// has every time after: one that ends while a call waits for its turn.
type flipContext struct{ asked atomic.Bool }

func (*flipContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*flipContext) Done() <-chan struct{}       { return nil }
func (*flipContext) Value(any) any               { return nil }

func (c *flipContext) Err() error {
	if c.asked.Swap(true) {
		return context.Canceled
	}
	return nil
}

func TestAContextThatEndsWhileACallWaitsLeavesTheModAlone(t *testing.T) {
	t.Parallel()
	_, h := one(t, reply(okReply), nil)
	_, err := h.Call(&flipContext{}, "m", OpSegment, "seg", Input{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v, want the context's", err)
	}
	if info := infoOf(t, h, "m"); info.Status != StatusOn || info.Calls != 0 {
		t.Errorf("the mod is %s with %d calls, want on and never called", info.Status, info.Calls)
	}
}

func TestAContextThatEndsDuringACallDoesNotStopIt(t *testing.T) {
	t.Parallel()
	b := newBed(t)
	b.add(t, "m", reply(okReply))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The host reads its clock as the module is about to run, which is
	// when this caller gives up.
	var armed atomic.Bool
	h := b.open(t, func(o *Options) {
		o.Now = func() time.Time {
			if armed.Load() {
				cancel()
			}
			return b.clock.Now()
		}
	})
	switchOn(t, h, "m")

	armed.Store(true)
	r, err := h.Call(ctx, "m", OpSegment, "seg", Input{})
	if err != nil || r.Text != "ok" {
		t.Fatalf("reply %+v, error %v: a call that had begun was not finished", r, err)
	}
	if ctx.Err() == nil {
		t.Fatal("the test's context never ended")
	}
	armed.Store(false)
	if r := mustShort(t, h); r.Text != "ok" {
		t.Errorf("the next call answered %+v", r)
	}
	if n := starts(h, "m"); n != 1 {
		t.Errorf("the mod had %d instances: a caller that gave up closed a healthy one", n)
	}
	if info := infoOf(t, h, "m"); info.Status != StatusOn || info.Calls != 2 {
		t.Errorf("the mod is %s with %d calls", info.Status, info.Calls)
	}
}

func TestRenderPassesWhatAModShouldNotDraw(t *testing.T) {
	t.Parallel()
	b := newBed(t)
	manifest := manifestFor("m", `read = ["messages"]`) + "\n[[renderer]]\nkind = \"code:big\"\n"
	put(t, filepath.Join(b.dir, "m"), manifest, reply(`{"abi":1,"lines":[[{"t":"drawn"}]]}`).binary())
	h := b.open(t, nil)
	switchOn(t, h, "m")

	render := func(item *wire.Item) wire.Reply {
		r, err := h.Call(t.Context(), "m", OpRender, "code:big", Input{Item: item})
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		return r
	}
	fits := &wire.Item{Kind: "code:big", Text: strings.Repeat("x", MaxItem)}
	if r := render(fits); r.Pass || len(r.Lines) != 1 {
		t.Errorf("an item of exactly 8 KiB: reply %+v, want the mod's line", r)
	}
	if n := infoOf(t, h, "m").Calls; n != 1 {
		t.Fatalf("Calls = %d, want 1", n)
	}
	over := &wire.Item{Kind: "code:big", Text: strings.Repeat("x", MaxItem+1)}
	if r := render(over); !r.Pass || r.ABI != wire.ABI || len(r.Lines) != 0 {
		t.Errorf("an item over 8 KiB: reply %+v, want Pass", r)
	}
	if r := render(nil); !r.Pass {
		t.Errorf("no item: reply %+v, want Pass", r)
	}
	if n := infoOf(t, h, "m").Calls; n != 1 {
		t.Errorf("Calls = %d: the mod was run for an item it should not see", n)
	}
}

func TestARequestOverTheLimitIsNotTheModsFault(t *testing.T) {
	t.Parallel()
	_, h := one(t, reply(okReply), nil)
	for range 5 {
		_, err := h.Call(t.Context(), "m", OpCommand, "go", Input{Args: []string{strings.Repeat("x", MaxMessage)}})
		if err == nil || !strings.Contains(err.Error(), "the request is") || errors.Is(err, ErrDisabled) {
			t.Fatalf("error = %v, want the request refused for its size", err)
		}
	}
	if info := infoOf(t, h, "m"); info.Status != StatusOn || info.Calls != 0 {
		t.Errorf("the mod is %s with %d calls", info.Status, info.Calls)
	}
}

func TestFiftyFailedCallsStillFitInARequest(t *testing.T) {
	t.Parallel()
	b := newBed(t)
	put(t, filepath.Join(b.dir, "m"), manifestFor("m", `read = ["model_calls"]`), reply(okReply).binary())
	h := b.open(t, nil)
	switchOn(t, h, "m")
	// Every one of the newest calls failed with a provider's whole error
	// page. Sent as they are they would be 400 KiB.
	var in Input
	for i := range 60 {
		in.Data.ModelCalls = append(in.Data.ModelCalls, wire.ModelCall{
			Seq: uint64(i), Route: "deepseek/deepseek-flash", Error: strings.Repeat("<h1>502</h1>", 700),
		})
	}
	if r, err := h.Call(t.Context(), "m", OpSegment, "seg", in); err != nil || r.Text != "ok" {
		t.Fatalf("reply %+v, error %v", r, err)
	}
	var req wire.Request
	raw := written(t, h, "m")
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	if len(raw) > MaxMessage || len(req.Data.ModelCalls) != MaxModelCalls || req.Data.ModelCalls[0].Seq != 10 {
		t.Errorf("the request is %d bytes with %d records from %d; want the newest %d, within %d bytes",
			len(raw), len(req.Data.ModelCalls), req.Data.ModelCalls[0].Seq, MaxModelCalls, MaxMessage)
	}
	if e := req.Data.ModelCalls[49].Error; !strings.HasPrefix(e, "<h1>502</h1>") || len(e) > MaxCallText {
		t.Errorf("the newest record's error is %d bytes: %q", len(e), e)
	}
}

// written returns the request the host last wrote into a tiny mod's memory,
// byte for byte.
func written(t *testing.T, h *Host, name string) string {
	t.Helper()
	m := h.lookup(name)
	m.call.Lock()
	defer m.call.Unlock()
	if m.inst == nil {
		t.Fatalf("mod %s has no instance", name)
	}
	mem, ok := m.inst.mod.Memory().Read(tinyInput, MaxMessage)
	if !ok {
		t.Fatal("the tiny module's memory is smaller than a request")
	}
	// What follows the request is zero, or the tail of a longer one.
	var raw json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(mem)).Decode(&raw); err != nil {
		t.Fatalf("what the host wrote is not JSON: %v", err)
	}
	return string(raw)
}

// marks are the parts of a request that are not always sent: the JSON key of
// each, and a word that marked() puts in that part and nowhere else.
func marks() map[string][2]string {
	return map[string][2]string{
		"contact":     {`"contact"`, "CONTACT-MARK"},
		"route":       {`"route":{`, "ROUTE-MARK"}, // a model-call record has a "route" too
		"state":       {`"state"`, "STATE-MARK"},
		"model_calls": {`"model_calls"`, "CALLS-MARK"},
		"messages":    {`"messages"`, "MESSAGES-MARK"},
		"workspace":   {`"workspace"`, "WORKSPACE-MARK"},
		"clock":       {`"now"`, "1760270640123"},
		"args":        {`"args"`, "ARGS-MARK"},
		"item":        {`"item"`, "ITEM-MARK"},
	}
}

// marked is an input with every part set, each to its mark.
func marked() Input {
	return Input{
		Term:  wire.Term{Color: "256", Glyphs: "ascii", Background: "light"},
		Width: 80,
		Args:  []string{"ARGS-MARK"},
		Item:  &wire.Item{Kind: "code:x", Text: "ITEM-MARK"},
		Data: wire.Data{
			Contact:    &wire.Contact{ID: 3, Name: "CONTACT-MARK"},
			Route:      &wire.Route{Provider: "ROUTE-MARK", Model: "m"},
			State:      &wire.State{Conn: "STATE-MARK"},
			ModelCalls: []wire.ModelCall{{Seq: 1, Error: "CALLS-MARK"}},
			Messages:   []wire.Message{{Seq: 4, Role: "user", Text: "MESSAGES-MARK"}},
			Workspace:  &wire.Workspace{Dir: "WORKSPACE-MARK", Branch: "main"},
			Now:        1760270640123,
		},
	}
}

// TestTheBytesWrittenToAMod reads each request out of the module's own
// memory: what a mod was not granted is not there to be found.
func TestTheBytesWrittenToAMod(t *testing.T) {
	t.Parallel()
	const (
		everyButMessages = `read = ["contact", "route", "state", "model_calls", "workspace", "clock"]`
		term             = `"term":{"color":"256","glyphs":"ascii","background":"light"}`
	)
	cases := []struct {
		name, read, op, call string
		sent                 []string // the parts that must be there; every other must not
		exact                string   // the whole request, when it is short enough to write out
	}{
		{
			name: "no grant, a command", op: OpCommand, call: "go", sent: []string{"args"},
			exact: `{"abi":1,"op":"command","name":"go","args":["ARGS-MARK"],"width":80,` + term + `,"data":{}}`,
		},
		{
			name: "no grant, a segment", op: OpSegment, call: "seg",
			exact: `{"abi":1,"op":"segment","name":"seg","width":80,` + term + `,"data":{}}`,
		},
		{
			name: "the clock alone", read: `read = ["clock"]`, op: OpSegment, call: "seg", sent: []string{"clock"},
			exact: `{"abi":1,"op":"segment","name":"seg","width":80,` + term + `,"data":{"now":1760270640123}}`,
		},
		{
			name: "messages, a command", read: `read = ["messages"]`, op: OpCommand, call: "go",
			sent: []string{"messages", "args"},
		},
		{name: "messages, a segment", read: `read = ["messages"]`, op: OpSegment, call: "seg"},
		{
			name: "messages, a renderer", read: `read = ["messages"]`, op: OpRender, call: "code:x", sent: []string{"item"},
			exact: `{"abi":1,"op":"render","name":"code:x","width":80,` + term +
				`,"data":{},"item":{"kind":"code:x","text":"ITEM-MARK"}}`,
		},
		{
			name: "every grant but messages, a command", read: everyButMessages, op: OpCommand, call: "go",
			sent: []string{"contact", "route", "state", "model_calls", "workspace", "clock", "args"},
		},
		{
			name: "every grant, a command", read: everyGrant, op: OpCommand, call: "go",
			sent: []string{"contact", "route", "state", "model_calls", "messages", "workspace", "clock", "args"},
		},
		{
			name: "every grant, a segment", read: everyGrant, op: OpSegment, call: "seg",
			sent: []string{"contact", "route", "state", "model_calls", "workspace", "clock"},
		},
		{
			name: "every grant, a renderer", read: everyGrant, op: OpRender, call: "code:x",
			sent: []string{"contact", "route", "state", "model_calls", "workspace", "clock", "item"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBed(t)
			manifest := manifestFor("m", tc.read)
			if strings.Contains(tc.read, `"messages"`) {
				manifest += "\n[[renderer]]\nkind = \"code:x\"\n"
			}
			put(t, filepath.Join(b.dir, "m"), manifest, reply(okReply).binary())
			h := b.open(t, nil)
			switchOn(t, h, "m")
			if hello := written(t, h, "m"); hello != `{"abi":1,"op":"hello","term":{"color":"","glyphs":"","background":""},"data":{}}` {
				t.Errorf("hello was %s", hello)
			}

			if _, err := h.Call(t.Context(), "m", tc.op, tc.call, marked()); err != nil {
				t.Fatalf("Call: %v", err)
			}
			got := written(t, h, "m")
			if tc.exact != "" && got != tc.exact {
				t.Errorf("the request was\n%s\nwant\n%s", got, tc.exact)
			}
			for part, mark := range marks() {
				want := slices.Contains(tc.sent, part)
				for _, word := range mark {
					if strings.Contains(got, word) != want {
						t.Errorf("%s in the request: %v, want %v\n%s", word, !want, want, got)
					}
				}
			}
		})
	}
}

func TestTimesOfTheLastHundredCalls(t *testing.T) {
	t.Parallel()
	b, h := one(t, reply(okReply), nil)
	if info := infoOf(t, h, "m"); info.Calls != 0 || info.Median != 0 || info.Slowest != 0 {
		t.Fatalf("a mod never called: %+v (hello is not an update)", info)
	}
	for _, ms := range []time.Duration{1, 4, 2, 3, 2} {
		b.clock.set(ms * time.Millisecond)
		mustShort(t, h)
	}
	info := infoOf(t, h, "m")
	if info.Calls != 5 || info.Median != 2*time.Millisecond || info.Slowest != 4*time.Millisecond {
		t.Errorf("calls %d, median %s, slowest %s; want 5, 2ms, 4ms", info.Calls, info.Median, info.Slowest)
	}
	b.clock.set(time.Millisecond)
	for range keptTimes {
		mustShort(t, h)
	}
	info = infoOf(t, h, "m")
	if info.Calls != 5+keptTimes || info.Median != time.Millisecond || info.Slowest != time.Millisecond {
		t.Errorf("calls %d, median %s, slowest %s; want %d and 1ms for both: only the last %d count",
			info.Calls, info.Median, info.Slowest, 5+keptTimes, keptTimes)
	}
}

func TestCallsFromManyGoroutines(t *testing.T) {
	t.Parallel()
	b := newBed(t)
	b.add(t, "a", reply(`{"abi":1,"text":"a"}`))
	b.add(t, "b", reply(`{"abi":1,"text":"b"}`))
	h := b.open(t, nil)
	switchOn(t, h, "a")
	switchOn(t, h, "b")

	var wg sync.WaitGroup
	for i := range 8 {
		name := []string{"a", "b"}[i%2]
		wg.Go(func() {
			for range 100 {
				r, err := h.Call(t.Context(), name, OpSegment, "seg", Input{})
				if err != nil || r.Text != name {
					t.Errorf("mod %s: reply %+v, error %v", name, r, err)
					return
				}
				h.Mods()
				h.Needs()
			}
		})
	}
	wg.Wait()
	for _, info := range h.Mods() {
		if info.Calls != 400 || info.Status != StatusOn {
			t.Errorf("mod %s: %d calls, %s; want 400 and on", info.Manifest.Name, info.Calls, info.Status)
		}
	}
}

func TestOpenListsWhatIsInstalled(t *testing.T) {
	t.Parallel()
	b := newBed(t)
	b.add(t, "zeta", reply(okReply))
	b.add(t, "alpha", reply(okReply))
	b.add(t, "beta", reply(okReply))
	put(t, filepath.Join(b.dir, "broken"), "abi = 1\nname = \"other\"\nmodule = \"mod.wasm\"\n", nil)
	put(t, filepath.Join(b.dir, "greedy"), manifestFor("greedy", `budget = "1s"`), nil)
	put(t, filepath.Join(b.dir, "Shouting"), manifestFor("Shouting", ""), nil) // not a name a mod can have
	if err := os.MkdirAll(filepath.Join(b.dir, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := state{
		"alpha": {on: true, sha256: hashOf(reply(okReply).binary())},
		"beta":  {disabled: "over budget 3 times in a row, 2026-10-11 09:00"},
		"gone":  {on: true, source: "/somewhere"},
	}
	if err := saveState(b.state, st); err != nil {
		t.Fatal(err)
	}

	h := b.open(t, nil)
	type row struct {
		name   string
		status Status
		reason string
	}
	want := []row{
		{"alpha", StatusLoading, ""},
		{"beta", StatusDisabled, "over budget 3 times in a row, 2026-10-11 09:00"},
		{"broken", StatusFailed, `name "other" must equal the directory name "broken"`},
		{"greedy", StatusFailed, "mod.toml: budget 1s is above the host's 5ms: a mod may only lower it"},
		{"zeta", StatusOff, ""},
	}
	check := func(when string, infos []Info, want []row) {
		t.Helper()
		got := make([]row, 0, len(infos))
		for _, info := range infos {
			got = append(got, row{info.Manifest.Name, info.Status, info.Reason})
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", when, got, want)
		}
	}
	check("after Open", h.Mods(), want)
	if n := starts(h, "alpha"); n != 0 {
		t.Errorf("Open started %d instances, want none before Load", n)
	}
	if got := h.Needs(); len(got) != 0 {
		t.Errorf("Needs() = %v before Load", got)
	}

	want[0].status = StatusOn
	check("after Load", h.Load(t.Context()), want)
	check("after a second Load", h.Load(t.Context()), want)
	if n := starts(h, "alpha"); n != 1 {
		t.Errorf("two Loads started %d instances of the mod, want 1", n)
	}
	// What Load did not touch is still remembered.
	if e := b.stateOf(t)["gone"]; !e.on || e.source != "/somewhere" {
		t.Errorf("the entry of a mod that is not installed was lost: %+v", e)
	}
}

func TestOpenWithNothingThere(t *testing.T) {
	t.Parallel()
	b := newBed(t) // neither the directory nor the state file exists
	h := b.open(t, nil)
	if got := h.Mods(); len(got) != 0 {
		t.Errorf("Mods() = %+v, want none", got)
	}
	if got := h.Load(t.Context()); len(got) != 0 {
		t.Errorf("Load() = %+v, want none", got)
	}
	if _, err := os.Stat(b.state); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Open wrote a state file (stat error %v)", err)
	}

	none, err := Open(Options{})
	if err != nil {
		t.Fatalf("Open with no directories: %v", err)
	}
	if _, err := none.Install(t.Context(), t.TempDir()); err == nil {
		t.Error("Install with no mods directory succeeded")
	}
	if err := none.Close(t.Context()); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestOpenRefusesAStateFileItCannotRead(t *testing.T) {
	t.Parallel()
	b := newBed(t)
	b.add(t, "m", reply(okReply))
	before := []byte("[mod.m]\non = maybe\n")
	if err := os.WriteFile(b.state, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Dir: b.dir, StatePath: b.state}); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("Open error = %v, want one naming line 2", err)
	}
	after, err := os.ReadFile(b.state)
	if err != nil || !bytes.Equal(after, before) {
		t.Errorf("the state file was changed: %q (%v)", after, err)
	}
}

func TestSwitchingOffAndOn(t *testing.T) {
	t.Parallel()
	b, h := one(t, reply(okReply), nil)
	sum := hashOf(reply(okReply).binary())
	if e := b.stateOf(t)["m"]; !e.on || e.sha256 != sum {
		t.Fatalf("after switching on: %+v, want on with the module's hash", e)
	}
	mustShort(t, h)

	info, err := h.SetEnabled(t.Context(), "m", false)
	if err != nil || info.Status != StatusOff || info.Calls != 0 {
		t.Fatalf("SetEnabled(false) = %+v, %v", info, err)
	}
	if e := b.stateOf(t)["m"]; e.on || e.sha256 != sum {
		t.Errorf("after switching off: %+v", e)
	}
	if _, err := short(t, h); err == nil || !strings.Contains(err.Error(), "mod m is off") {
		t.Errorf("a call to a mod that is off: error %v", err)
	}

	switchOn(t, h, "m")
	mustShort(t, h)
	if n := starts(h, "m"); n != 2 {
		t.Errorf("the mod had %d instances, want one for each time it was on", n)
	}
	if _, err := h.SetEnabled(t.Context(), "nope", true); err == nil {
		t.Error("switching on a mod that is not installed succeeded")
	}
}

func TestASwappedModuleIsOff(t *testing.T) {
	t.Parallel()
	b, first := one(t, reply(okReply), nil)
	if err := first.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Someone replaces the module the user switched on.
	swapped := reply(`{"abi":1,"text":"not what you switched on"}`).binary()
	if err := os.WriteFile(filepath.Join(b.dir, "m", "mod.wasm"), swapped, 0o600); err != nil {
		t.Fatal(err)
	}

	h := b.open(t, nil)
	infos := h.Load(t.Context())
	if len(infos) != 1 || infos[0].Status != StatusOff || infos[0].Reason != "the module changed since it was switched on" {
		t.Fatalf("Load() = %+v, want the mod off because its module changed", infos)
	}
	if n := starts(h, "m"); n != 0 {
		t.Errorf("the swapped module was started %d times", n)
	}
	if _, err := short(t, h); err == nil {
		t.Error("the swapped module answered a call")
	}
	if e := b.stateOf(t)["m"]; e.on {
		t.Errorf("mods.toml still has the mod on: %+v", e)
	}
	// It stays off across starts, until the user switches it on again.
	if infos := b.open(t, nil).Load(t.Context()); infos[0].Status != StatusOff {
		t.Errorf("after another start the mod is %s", infos[0].Status)
	}
	switchOn(t, h, "m")
	if r := mustShort(t, h); r.Text != "not what you switched on" {
		t.Errorf("reply %+v", r)
	}
	if e := b.stateOf(t)["m"]; !e.on || e.sha256 != hashOf(swapped) {
		t.Errorf("after switching on: %+v, want on with the new hash", e)
	}
}

func TestInstallStartsOffAndRecordsTheHash(t *testing.T) {
	t.Parallel()
	b := newBed(t)
	h := b.open(t, nil)
	src := filepath.Join(t.TempDir(), "any-name-at-all")
	bin := reply(okReply).binary()
	manifest := manifestFor("m", `read = ["clock"]`)
	put(t, src, manifest, bin)
	if err := os.WriteFile(filepath.Join(src, "secrets.env"), []byte("TOKEN=x"), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := h.Install(t.Context(), src)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if info.Status != StatusOff || info.Manifest.Name != "m" || !info.Manifest.Has(GrantClock) {
		t.Errorf("Install() = %+v, want mod m, off", info)
	}
	if e, want := b.stateOf(t)["m"], (entry{source: src, sha256: hashOf(bin)}); e != want {
		t.Errorf("mods.toml has %+v, want %+v", e, want)
	}
	if got := h.Needs(); len(got) != 0 {
		t.Errorf("Needs() = %v for a mod that is off", got)
	}
	if _, err := short(t, h); err == nil {
		t.Error("a mod just installed answered a call")
	}

	// Its two files were copied, for the user only, and nothing else.
	dest := filepath.Join(b.dir, "m")
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
		if fi, err := e.Info(); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v (%v), want 0600", e.Name(), fi.Mode(), err)
		}
	}
	if !slices.Equal(names, []string{"mod.toml", "mod.wasm"}) {
		t.Errorf("installed files = %v", names)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "mod.wasm")); err != nil || !bytes.Equal(got, bin) {
		t.Errorf("the installed module differs (%v)", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, ManifestFile)); err != nil || string(got) != manifest {
		t.Errorf("the installed manifest differs (%v)", err)
	}

	switchOn(t, h, "m")
	mustShort(t, h)

	// Installing over a mod that is on replaces it and switches it off.
	put(t, src, manifest, reply(`{"abi":1,"text":"v2"}`).binary())
	info, err = h.Install(t.Context(), src)
	if err != nil || info.Status != StatusOff {
		t.Fatalf("Install over a running mod = %+v, %v", info, err)
	}
	if e := b.stateOf(t)["m"]; e.on {
		t.Errorf("mods.toml still has it on: %+v", e)
	}
	switchOn(t, h, "m")
	if r := mustShort(t, h); r.Text != "v2" {
		t.Errorf("after installing again the mod answered %+v", r)
	}
	// A later start finds it as it was left.
	if infos := b.open(t, nil).Load(t.Context()); len(infos) != 1 || infos[0].Status != StatusOn {
		t.Errorf("after a restart: %+v", infos)
	}
}

func TestInstallRefusals(t *testing.T) {
	t.Parallel()
	bin := reply(okReply).binary()
	cases := []struct {
		name  string
		setup func(t *testing.T, b bed, src string)
		want  string
	}{
		{"an empty directory", func(*testing.T, bed, string) {}, "install: mod.toml: "},
		{
			"a manifest that does not parse",
			func(t *testing.T, _ bed, src string) {
				t.Helper()
				put(t, src, "abi = \n", bin)
			},
			"install: mod.toml: line 1",
		},
		{
			"an unknown grant",
			func(t *testing.T, _ bed, src string) {
				t.Helper()
				put(t, src, manifestFor("m", `read = ["files"]`), bin)
			},
			`unknown grant "files"`,
		},
		{
			"no module",
			func(t *testing.T, _ bed, src string) {
				t.Helper()
				put(t, src, manifestFor("m", ""), bin)
				if err := os.Remove(filepath.Join(src, "mod.wasm")); err != nil {
					t.Fatal(err)
				}
			},
			"install: module: ",
		},
		{
			"a module that is a directory",
			func(t *testing.T, _ bed, src string) {
				t.Helper()
				put(t, src, manifestFor("m", ""), bin)
				if err := os.Remove(filepath.Join(src, "mod.wasm")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(src, "mod.wasm"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			"is not a regular file",
		},
		{
			"a module that is not the one pinned",
			func(t *testing.T, _ bed, src string) {
				t.Helper()
				put(t, src, manifestFor("m", `sha256 = "`+strings.Repeat("a", 64)+`"`), bin)
			},
			"the module does not match the sha256 in mod.toml",
		},
		{
			"a budget above the host's",
			func(t *testing.T, _ bed, src string) {
				t.Helper()
				put(t, src, manifestFor("m", `budget = "50ms"`), bin)
			},
			"budget 50ms is above the host's 5ms",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBed(t)
			h := b.open(t, nil)
			src := t.TempDir()
			tc.setup(t, b, src)
			_, err := h.Install(t.Context(), src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Install error = %v, want one containing %q", err, tc.want)
			}
			if got := h.Mods(); len(got) != 0 {
				t.Errorf("Mods() = %+v after a refused install", got)
			}
			if _, err := os.Stat(filepath.Join(b.dir, "m")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a refused install left files behind (stat error %v)", err)
			}
		})
	}
}

func TestInstallFromWhereTheModAlreadyIs(t *testing.T) {
	t.Parallel()
	b, h := one(t, reply(okReply), nil)
	_, err := h.Install(t.Context(), filepath.Join(b.dir, "m"))
	if err == nil || !strings.Contains(err.Error(), "is where the mod is installed already") {
		t.Errorf("Install error = %v", err)
	}
	if info := infoOf(t, h, "m"); info.Status != StatusOn {
		t.Errorf("the refused install left the mod %s", info.Status)
	}
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	b := newBed(t)
	h := b.open(t, nil)
	src := t.TempDir()
	manifest := manifestFor("m", "")
	v1 := reply(`{"abi":1,"text":"v1"}`).binary()
	put(t, src, manifest, v1)
	if _, err := h.Install(t.Context(), src); err != nil {
		t.Fatal(err)
	}
	switchOn(t, h, "m")

	// Nothing changed: the mod carries on, with the instance it had.
	info, err := h.Update(t.Context(), "m")
	if err != nil || info.Status != StatusOn {
		t.Fatalf("Update with nothing new = %+v, %v", info, err)
	}
	if r := mustShort(t, h); r.Text != "v1" || starts(h, "m") != 1 {
		t.Errorf("reply %+v, %d instances", r, starts(h, "m"))
	}

	// A changed module is off until switched on again.
	v2 := reply(`{"abi":1,"text":"v2"}`).binary()
	put(t, src, manifest, v2)
	info, err = h.Update(t.Context(), "m")
	if err != nil || info.Status != StatusOff || info.Reason == "" {
		t.Fatalf("Update with a new module = %+v, %v; want off, with a reason", info, err)
	}
	if e, want := b.stateOf(t)["m"], (entry{source: src, sha256: hashOf(v2)}); e != want {
		t.Errorf("mods.toml has %+v, want %+v", e, want)
	}
	if _, err := short(t, h); err == nil {
		t.Error("the updated mod answered before it was switched on")
	}
	switchOn(t, h, "m")
	if r := mustShort(t, h); r.Text != "v2" {
		t.Errorf("after the update the mod answered %+v", r)
	}

	// So is a changed manifest: it may ask to read more.
	put(t, src, manifestFor("m", `read = ["messages"]`), v2)
	info, err = h.Update(t.Context(), "m")
	if err != nil || info.Status != StatusOff || !info.Manifest.Has(GrantMessages) {
		t.Fatalf("Update with a new manifest = %+v, %v; want off, with the new grant shown", info, err)
	}

	// A source that now holds another mod, or nothing, changes nothing.
	put(t, src, manifestFor("other", ""), v2)
	if _, err := h.Update(t.Context(), "m"); err == nil || !strings.Contains(err.Error(), `now holds a mod named "other"`) {
		t.Errorf("Update from a source that changed its name: error %v", err)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Update(t.Context(), "m"); err == nil {
		t.Error("Update from a source that is gone succeeded")
	}
	if got, err := os.ReadFile(filepath.Join(b.dir, "m", "mod.wasm")); err != nil || !bytes.Equal(got, v2) {
		t.Errorf("a failed update changed the installed module (%v)", err)
	}
}

func TestUpdateNeedsASource(t *testing.T) {
	t.Parallel()
	_, h := one(t, reply(okReply), nil) // put in place by hand, not installed
	info, err := h.Update(t.Context(), "m")
	if err == nil || !strings.Contains(err.Error(), "was not installed from a directory") {
		t.Errorf("Update error = %v", err)
	}
	if info.Status != StatusOn {
		t.Errorf("the mod is %s after a refused update", info.Status)
	}
	if _, err := h.Update(t.Context(), "nope"); err == nil {
		t.Error("updating a mod that is not installed succeeded")
	}
}

func TestClose(t *testing.T) {
	t.Parallel()
	b := newBed(t)
	b.add(t, "m", reply(okReply))
	h, err := Open(Options{
		Dir: b.dir, StatePath: b.state, Kill: time.Minute,
		CacheDir: filepath.Join(t.TempDir(), "cache", "wazero"),
	})
	if err != nil {
		t.Fatal(err)
	}
	switchOn(t, h, "m")
	mustShort(t, h)
	for range 2 {
		if err := h.Close(t.Context()); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if _, err := short(t, h); err == nil {
		t.Error("a closed host answered a call")
	}
	if _, err := h.SetEnabled(t.Context(), "m", true); !errors.Is(err, errClosed) {
		t.Errorf("SetEnabled on a closed host: error %v", err)
	}
	if _, err := h.Install(t.Context(), t.TempDir()); !errors.Is(err, errClosed) {
		t.Errorf("Install on a closed host: error %v", err)
	}
	if _, err := h.Update(t.Context(), "m"); !errors.Is(err, errClosed) {
		t.Errorf("Update on a closed host: error %v", err)
	}
	if got := h.Needs(); len(got) != 0 {
		t.Errorf("Needs() = %v on a closed host", got)
	}
	// The state is as the user left it: on, for the next start.
	if e := b.stateOf(t)["m"]; !e.on {
		t.Errorf("closing switched the mod off: %+v", e)
	}
}

func TestSlowdownWithoutACompiler(t *testing.T) {
	t.Parallel()
	for arch, want := range map[string]time.Duration{"amd64": 1, "arm64": 1, "riscv64": 8, "386": 8, "wasm": 8} {
		if got := slowdown(arch); got != want {
			t.Errorf("slowdown(%q) = %d, want %d", arch, got, want)
		}
	}
	// The host's times and a mod's own budget are both stretched.
	h := &Host{scale: 8, budget: 8 * DefaultBudget}
	if got := h.budgetOf(Manifest{}); got != 40*time.Millisecond {
		t.Errorf("the host's budget when interpreting = %s, want 40ms", got)
	}
	if got := h.budgetOf(Manifest{Budget: 2 * time.Millisecond}); got != 16*time.Millisecond {
		t.Errorf("a mod's 2ms budget when interpreting = %s, want 16ms", got)
	}
}

func TestStatusString(t *testing.T) {
	t.Parallel()
	want := map[Status]string{
		StatusOff: "off", StatusLoading: "loading", StatusOn: "on",
		StatusFailed: "failed", StatusDisabled: "disabled", 0: "unknown",
	}
	for s, w := range want {
		if got := s.String(); got != w {
			t.Errorf("Status(%d).String() = %q, want %q", s, got, w)
		}
	}
}

func TestOptionsDefaults(t *testing.T) {
	t.Parallel()
	o := Options{}.withDefaults()
	if o.Budget != 5*time.Millisecond || o.Kill != 50*time.Millisecond || o.Strikes != 3 || o.MemoryPages != 256 {
		t.Errorf("defaults: budget %s, kill %s, %d strikes, %d pages; want 5ms, 50ms, 3, 256",
			o.Budget, o.Kill, o.Strikes, o.MemoryPages)
	}
	if o.Now == nil || o.Logger == nil {
		t.Error("no clock or no logger by default")
	}
	set := Options{Budget: time.Millisecond, Kill: time.Second, Strikes: 5, MemoryPages: 32}.withDefaults()
	if set.Budget != time.Millisecond || set.Kill != time.Second || set.Strikes != 5 || set.MemoryPages != 32 {
		t.Errorf("options that were set were replaced: %+v", set)
	}
}
