package mods

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// everyGrant is the read line of a manifest that asks for everything.
const everyGrant = `read = ["contact", "route", "state", "model_calls", "messages", "workspace", "clock"]`

// guestManifest is a manifest for the test guest: every segment and command
// it answers, and its renderers when the grants allow them.
func guestManifest(name, read string) string {
	var m strings.Builder
	m.WriteString(`abi = 1
name = "` + name + `"
version = "0.0.1"
module = "mod.wasm"
` + read + `

[[segment]]
name = "count"
refresh = "turn"

[[segment]]
name = "seen"
refresh = "state"
`)
	for _, c := range []string{"echo", "mirror", "upper", "clock", "mono", "sleep", "hostile", "big", "churn", "panic"} {
		m.WriteString("\n[[command]]\nname = \"" + c + "\"\n")
	}
	if strings.Contains(read, `"messages"`) {
		for _, k := range []string{"code:shout", "code:seen", "tool:pass"} {
			m.WriteString("\n[[renderer]]\nkind = \"" + k + "\"\n")
		}
	}
	return m.String()
}

// echoed decodes the request a guest's echo command sent back.
func echoed(t *testing.T, r wire.Reply) wire.Request {
	t.Helper()
	var raw strings.Builder
	for _, line := range r.Lines {
		for _, span := range line {
			raw.WriteString(span.T)
		}
	}
	var req wire.Request
	if err := json.Unmarshal([]byte(raw.String()), &req); err != nil {
		t.Fatalf("the echo is not a request: %v\n%s", err, raw.String())
	}
	return req
}

// TestRealGuest builds the Go guest in testdata once and runs every subtest
// against it: the build, the glue in package guest and the host, together.
func TestRealGuest(t *testing.T) {
	t.Parallel()
	bin := buildGuest(t, "./testdata/guest")
	b := newBed(t)
	const noMessages = `read = ["contact", "route", "state", "model_calls", "workspace", "clock"]`
	for name, read := range map[string]string{
		"full": everyGrant, "nomsg": noMessages, "bare": "", "counter": "", "soak": everyGrant, "raw": "", "mirror": "",
	} {
		put(t, filepath.Join(b.dir, name), guestManifest(name, read), bin)
	}
	cache := filepath.Join(t.TempDir(), "wazero")
	h := b.open(t, func(o *Options) { o.CacheDir = cache })
	for _, name := range []string{"full", "nomsg", "bare", "counter", "soak", "raw", "mirror"} {
		switchOn(t, h, name)
	}
	if entries, err := os.ReadDir(cache); err != nil || len(entries) == 0 {
		t.Errorf("the compilation cache directory is empty (%v)", err)
	}

	// A second host, with a deadline short enough to wait for. A Go guest
	// takes milliseconds to start, so it is not the 20 ms of the tiny tests.
	quick := newBed(t)
	put(t, filepath.Join(quick.dir, "sleeper"), guestManifest("sleeper", ""), bin)
	impatient := quick.open(t, func(o *Options) {
		o.CacheDir = cache
		o.Kill = 250 * time.Millisecond
	})
	switchOn(t, impatient, "sleeper")

	call := func(t *testing.T, mod, op, name string, in Input) wire.Reply {
		t.Helper()
		r, err := h.Call(t.Context(), mod, op, name, in)
		if err != nil {
			t.Fatalf("Call(%s, %s, %s): %v", mod, op, name, err)
		}
		if r.ABI != wire.ABI {
			t.Fatalf("Call(%s, %s, %s): abi %d", mod, op, name, r.ABI)
		}
		return r
	}

	t.Run("an instance keeps its state until it fails", func(t *testing.T) {
		t.Parallel()
		// hello was the instance's first call.
		for _, want := range []string{"2", "3", "4"} {
			r := call(t, "counter", OpSegment, "count", Input{})
			if r.Text != "calls "+want || r.Short != want || r.Tone != "mod" {
				t.Fatalf("reply %+v, want calls %s", r, want)
			}
		}
		_, err := h.Call(t.Context(), "counter", OpCommand, "panic", Input{})
		if err == nil || !strings.Contains(err.Error(), "mod counter: mod_call: ") {
			t.Fatalf("a panic in the guest: error %v", err)
		}
		// The next call gets a new instance, which has counted nothing.
		if r := call(t, "counter", OpSegment, "count", Input{}); r.Text != "calls 1" {
			t.Errorf("after the panic: %+v, want a new instance's first call", r)
		}
		if n := starts(h, "counter"); n != 2 {
			t.Errorf("the mod had %d instances, want 2", n)
		}
	})

	t.Run("a guest that sleeps is stopped at the deadline", func(t *testing.T) {
		t.Parallel()
		// The host gives a module no way to wait, so Go's sleep spins, and
		// a call that spins is stopped. Were the module given a real sleep,
		// the host would wait the hour out with it.
		_, err := impatient.Call(t.Context(), "sleeper", OpCommand, "sleep", Input{})
		if err == nil || !strings.Contains(err.Error(), "mod sleeper: mod_call: stopped at the deadline") {
			t.Fatalf("a guest that sleeps for an hour: error %v, want it stopped at the deadline", err)
		}
		r, err := impatient.Call(t.Context(), "sleeper", OpSegment, "count", Input{})
		if err != nil || r.Text != "calls 1" {
			t.Errorf("after the kill: reply %+v, error %v; want a new instance's first call", r, err)
		}
		if n := starts(impatient, "sleeper"); n != 2 {
			t.Errorf("the mod had %d instances, want 2", n)
		}
	})

	t.Run("a command inserts text and leaves a note", func(t *testing.T) {
		t.Parallel()
		r := call(t, "full", OpCommand, "upper", Input{Args: []string{"two words", "more"}})
		if r.Insert != "TWO WORDS MORE" || r.Note != "2 args" || r.Text != "" || len(r.Lines) != 0 {
			t.Errorf("reply %+v", r)
		}
	})

	t.Run("a renderer draws an item", func(t *testing.T) {
		t.Parallel()
		item := &wire.Item{Kind: "code:shout", Text: "graph td\na --> b"}
		r := call(t, "full", OpRender, "code:shout", Input{Item: item, Width: 60})
		want := []wire.Line{
			{{T: "code:shout ", Tone: "dim"}, {T: "GRAPH TD"}},
			{{T: "code:shout ", Tone: "dim"}, {T: "A --> B"}},
		}
		if !reflect.DeepEqual(r.Lines, want) || r.Pass {
			t.Errorf("reply %+v, want lines %+v", r, want)
		}
		if r := call(t, "full", OpRender, "tool:pass", Input{Item: &wire.Item{Kind: "tool:pass", Text: "x"}}); !r.Pass {
			t.Errorf("a renderer that passes: %+v", r)
		}
		// An item over 8 KiB never reaches the guest, which would shout it.
		big := &wire.Item{Kind: "code:shout", Text: strings.Repeat("a", MaxItem+1)}
		if r := call(t, "full", OpRender, "code:shout", Input{Item: big}); !r.Pass || len(r.Lines) != 0 {
			t.Errorf("an item over 8 KiB: %+v, want Pass", r)
		}
	})

	t.Run("a mod receives what its grants allow", func(t *testing.T) {
		t.Parallel()
		in := fullInput()
		in.Item = &wire.Item{Kind: "code:seen", Text: "x"}
		base := wire.Request{ABI: 1, Op: "command", Name: "echo", Args: in.Args, Width: 100, Term: in.Term}

		full := base
		full.Data = in.Data
		if got := echoed(t, call(t, "full", OpCommand, "echo", in)); !reflect.DeepEqual(got, full) {
			t.Errorf("every grant, a command:\n got %+v\nwant %+v", got, full)
		}

		// A mod without the messages grant never receives any.
		noMsg := base
		noMsg.Data = in.Data
		noMsg.Data.Messages = nil
		r := call(t, "nomsg", OpCommand, "echo", in)
		if got := echoed(t, r); !reflect.DeepEqual(got, noMsg) {
			t.Errorf("every grant but messages, a command:\n got %+v\nwant %+v", got, noMsg)
		}
		if strings.Contains(r.Note, "messages") {
			t.Errorf("the guest saw %q", r.Note)
		}

		// A mod with no grant receives no data at all.
		if got := echoed(t, call(t, "bare", OpCommand, "echo", in)); !reflect.DeepEqual(got, base) {
			t.Errorf("no grant, a command:\n got %+v\nwant %+v", got, base)
		}

		// Messages go to a command only, arguments too, and the item to a
		// renderer only.
		seen := []struct{ mod, op, name, want string }{
			{"full", OpSegment, "seen", "contact route state model_calls=2 workspace now"},
			{"nomsg", OpSegment, "seen", "contact route state model_calls=2 workspace now"},
			{"bare", OpSegment, "seen", ""},
			{"full", OpRender, "code:seen", "contact route state model_calls=2 workspace now item"},
		}
		for _, tc := range seen {
			r := call(t, tc.mod, tc.op, tc.name, in)
			got := r.Text
			if tc.op == OpRender {
				got = r.Lines[0][0].T
			}
			if got != tc.want {
				t.Errorf("%s %s %s saw %q, want %q", tc.mod, tc.op, tc.name, got, tc.want)
			}
		}
		if r := call(t, "full", OpCommand, "echo", in); r.Note != "contact route state model_calls=2 messages=2 workspace now args=2" {
			t.Errorf("a command with every grant saw %q", r.Note)
		}
	})

	t.Run("a request arrives as the host wrote it, whatever its strings hold", func(t *testing.T) {
		t.Parallel()
		// The guest reads a request with a decoder of its own, and writes
		// its reply with an encoder of its own. This sends both the
		// strings and the numbers that are easy to get wrong, through the
		// module, and compares what the guest saw with what the host's
		// own decoder makes of the same bytes.
		in := Input{
			Term:  wire.Term{Color: "256", Glyphs: "unicode", Background: "light"},
			Width: -5,
			Args: []string{
				"", " ", `say "hi"`, `C:\temp\new`, "one\ntwo\r\nthree\tfour", "nul \x00 esc \x1b[31m bell \a",
				"é ⎇ 日本語 🙂", "<b>&amp;</b>", "not \xff UTF-8 \xc3", `\u0041 \n /`, strings.Repeat("語", 300),
				"1234567", "12345678", "123456789", `1234567"`, `12345678\`,
			},
			Data: wire.Data{
				Contact: &wire.Contact{ID: math.MaxUint64, Name: `"`},
				Route:   &wire.Route{Provider: `\`, Model: "a/b"},
				State:   &wire.State{Conn: "live\n", Running: "\t"},
				ModelCalls: []wire.ModelCall{
					{Seq: 1 << 63, TS: math.MinInt64, Step: -1, Route: "r\"r", Input: math.MaxInt64, Cached: -3, Error: "e\\e"},
					{TS: math.MaxInt64, Input: 99_999_999, Cached: 100_000_000, Output: 999_999_999_999_999, MS: 1_000_000_000_000_000},
				},
				Messages:  []wire.Message{{Seq: 9, TS: -1, Role: "user", Text: "line one\nline \"two\"\n"}, {}},
				Workspace: &wire.Workspace{Dir: "dir with spaces", Branch: "feature/ünïcode"},
				Now:       -1,
			},
		}
		body, err := encodeRequest(buildRequest(infoOf(t, h, "full").Manifest, OpCommand, "echo", in))
		if err != nil {
			t.Fatal(err)
		}
		var want wire.Request
		if err := json.Unmarshal(body, &want); err != nil {
			t.Fatal(err)
		}
		if got := echoed(t, call(t, "full", OpCommand, "echo", in)); !reflect.DeepEqual(got, want) {
			t.Errorf("the guest saw\n got %+v\nwant %+v", got, want)
		}
	})

	t.Run("the glue reads any JSON that is a request, and refuses the rest", func(t *testing.T) {
		t.Parallel()
		// The host only ever writes a request one way, so these go to the
		// module's memory directly.
		m := h.lookup("raw")
		send := func(request string) wire.Reply {
			t.Helper()
			m.call.Lock()
			defer m.call.Unlock()
			raw, err := m.inst.roundTrip(t.Context(), []byte(request))
			if err != nil {
				t.Fatalf("%s: %v", request, err)
			}
			var r wire.Reply
			if err := json.Unmarshal(raw, &r); err != nil {
				t.Fatalf("%s: the reply %s is not JSON: %v", request, raw, err)
			}
			if r.ABI != wire.ABI {
				t.Fatalf("%s: the reply's abi is %d", request, r.ABI)
			}
			return r
		}
		for _, request := range []string{
			``, `{`, `not json`, `[]`, `{"op":"segment","name":"seen"`, `{"op":"segment","name":"seen"} x`,
			`{"abi":"1"}`, `{"op":7}`, `{"width":1.5}`, `{"data":{"now":99999999999999999999}}`, "{\"op\":\"a\nb\"}",
		} {
			if r := send(request); r.Error != "the request is not JSON" || r.Text != "" {
				t.Errorf("%q was answered with %+v, want the error", request, r)
			}
		}
		// A request from a newer host, or one that writes JSON another
		// way: unknown keys, white space, another order, escapes.
		newer := "{ \"future\" : [1, {\"deep\": null}], \"data\" : { \"now\" : 12, \"later\" : 1e9 },\n" +
			"\t\"name\" : \"s\\u0065en\", \"op\" : \"segment\", \"abi\" : 1 }"
		if r := send(newer); r.Text != "now" || r.Error != "" {
			t.Errorf("a request written another way was answered with %+v, want the segment's text %q", r, "now")
		}
	})

	t.Run("the module reads a request as encoding/json does", func(t *testing.T) {
		t.Parallel()
		// The decoder in the glue looks at eight bytes at a time, and its
		// own tests run it as the host's compiler builds it. A module is
		// built by another compiler, so the same questions are asked
		// here of the module itself: numbers of every length, strings of
		// every length with something to stumble on at every place, and
		// requests written in ways the host never writes them.
		if r := call(t, "mirror", OpCommand, "mirror", Input{}); r.Note == "" {
			t.Fatalf("the guest did not switch to echoing: %+v", r)
		}
		m := h.lookup("mirror")
		for _, request := range awkwardRequests() {
			m.call.Lock()
			raw, err := m.inst.roundTrip(t.Context(), []byte(request))
			m.call.Unlock()
			if err != nil {
				t.Fatalf("%q: %v", request, err)
			}
			var r wire.Reply
			if err := json.Unmarshal(raw, &r); err != nil {
				t.Fatalf("%q: the reply is not JSON: %v\n%s", request, err, raw)
			}
			var want wire.Request
			wantErr := json.Unmarshal([]byte(request), &want)
			if refused := r.Error == "the request is not JSON"; refused != (wantErr != nil) {
				t.Fatalf("%q: the module refused it: %v; encoding/json: %v", request, refused, wantErr)
			}
			if wantErr != nil {
				continue
			}
			// The echo is the request written out again, which leaves
			// out a list with nothing in it. Do the same to the host's
			// reading before comparing.
			again, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			want = wire.Request{}
			if err := json.Unmarshal(again, &want); err != nil {
				t.Fatal(err)
			}
			if got := echoed(t, r); !reflect.DeepEqual(got, want) {
				t.Fatalf("%q: the module read\n got %+v\nwant %+v", request, got, want)
			}
		}
	})

	t.Run("the clock is the time of the request, for a mod that may read it", func(t *testing.T) {
		t.Parallel()
		at := time.Date(2031, 3, 9, 7, 30, 15, 0, time.UTC)
		in := Input{Data: wire.Data{Now: at.UnixMilli()}}
		r := call(t, "full", OpCommand, "clock", in)
		if r.Text != "2031-03-09T07:30:15Z" || r.Short != "1930807815000" {
			t.Errorf("with the grant: the guest's clock read %q and its request %q", r.Text, r.Short)
		}
		// With no time in the request the host's own clock is used.
		if r := call(t, "full", OpCommand, "clock", Input{}); r.Text != "2026-10-12T12:04:00Z" {
			t.Errorf("with the grant and no time given: the guest's clock read %q", r.Text)
		}
		// Without the grant the request has no time and the clock is fake.
		r = call(t, "bare", OpCommand, "clock", in)
		if r.Short != "0" || strings.HasPrefix(r.Text, "2031") || strings.HasPrefix(r.Text, "2026-10-12") {
			t.Errorf("without the grant: the guest's clock read %q and its request %q", r.Text, r.Short)
		}
	})

	t.Run("the monotonic clock is the real one", func(t *testing.T) {
		t.Parallel()
		// wazero's fake monotonic clock moves one millisecond each time it
		// is read, and a Go guest under it lets its memory grow without
		// end (measured: 62 MiB after 20,000 calls, 810 MiB after 60,000).
		// The 5,000 calls below are too few to show that, so the clock is
		// checked here: gaps that are whole milliseconds are the fake's.
		// This asserts nothing about how long anything took.
		r := call(t, "bare", OpCommand, "mono", Input{})
		stepped := 0
		for _, gap := range []string{r.Text, r.Short} {
			ns, err := strconv.ParseInt(gap, 10, 64)
			if err != nil {
				t.Fatalf("the guest answered %+v", r)
			}
			if ns > 0 && ns%int64(time.Millisecond) == 0 {
				stepped++
			}
		}
		if stepped == 2 {
			t.Errorf("two readings of the guest's monotonic clock were %s and %s ns apart: "+
				"that is wazero's fake clock, not WithSysNanotime", r.Text, r.Short)
		}
	})

	t.Run("nothing a mod returns can draw outside its text", func(t *testing.T) {
		t.Parallel()
		for _, glyphs := range []string{"unicode", "ascii"} {
			r := call(t, "full", OpCommand, "hostile", Input{Term: wire.Term{Glyphs: glyphs}})
			if len(r.Lines) != MaxLines {
				t.Errorf("%s: %d lines, want %d", glyphs, len(r.Lines), MaxLines)
			}
			texts := []string{r.Text, r.Short, r.Tone, r.Insert, r.Note, r.Error}
			for _, line := range r.Lines {
				for _, span := range line {
					texts = append(texts, span.T, span.Tone)
				}
			}
			for _, s := range texts {
				for _, c := range s {
					if c < 0x20 || c == 0x7F || (c >= 0x80 && c <= 0x9F) || c == '\u202e' {
						t.Fatalf("%s: %q holds %U", glyphs, s, c)
					}
					if glyphs == "ascii" && c > 0x7E {
						t.Fatalf("%s: %q holds %U", glyphs, s, c)
					}
				}
				if utf8.RuneCountInString(s) > MaxSpanRunes {
					t.Fatalf("%s: a string of %d runes", glyphs, utf8.RuneCountInString(s))
				}
			}
			if r.Tone != "" || r.Lines[0][0].Tone != "" || r.Lines[1][0].Tone != "accent" || !r.Lines[1][0].Bold {
				t.Errorf("%s: tones %q, %q, %q", glyphs, r.Tone, r.Lines[0][0].Tone, r.Lines[1][0].Tone)
			}
			if r.Insert != "rm -rf / yes" || r.Text != "]0;ownedtitle" || r.Lines[1][0].T != "line 1[2J" {
				t.Errorf("%s: insert %q, text %q, line %q", glyphs, r.Insert, r.Text, r.Lines[1][0].T)
			}
			branch := map[string]string{"unicode": "⎇ main", "ascii": "? main"}[glyphs]
			if got := r.Lines[0][1].T; got != branch {
				t.Errorf("%s: %q, want %q", glyphs, got, branch)
			}
		}
	})

	t.Run("the glue refuses to return more than 64 KiB", func(t *testing.T) {
		t.Parallel()
		r := call(t, "full", OpCommand, "big", Input{})
		if r.Error != "the reply is over 64 KiB" || r.Text != "" {
			t.Errorf("reply error %q and %d bytes of text", r.Error, len(r.Text))
		}
	})

	t.Run("5,000 calls leave the guest's memory under the limit", func(t *testing.T) {
		t.Parallel()
		// Each call decodes a request with every field set and then throws
		// away 32 KiB: 160 MiB of garbage in all, ten times the limit. The
		// guest only stays under it while its collector keeps up.
		in := fullInput()
		for i := range 5000 {
			r, err := h.Call(t.Context(), "soak", OpCommand, "churn", in)
			if err != nil {
				t.Fatalf("call %d: %v", i, err)
			}
			if !strings.HasPrefix(r.Note, "32768 bytes") {
				t.Fatalf("call %d: note %q", i, r.Note)
			}
		}
		m := h.lookup("soak")
		m.call.Lock()
		size, started := m.inst.memoryBytes(), m.starts
		m.call.Unlock()
		limit := uint32(DefaultMemoryPages) * 65536
		if size >= limit {
			t.Errorf("the guest's memory is %d bytes, at the limit of %d", size, limit)
		}
		if started != 1 {
			t.Errorf("the mod had %d instances: one ran out of memory", started)
		}
		t.Logf("guest memory after 5,000 calls: %.1f MiB of %d", float64(size)/(1<<20), limit>>20)
	})
}

// exampleCall is one call to an example mod and the golden file of its reply.
type exampleCall struct {
	golden, op, name string
	in               Input
	check            func(wire.Reply) bool
}

// example is one of the mods in examples/mods.
type example struct {
	name  string
	read  []Grant
	calls []exampleCall
}

func examples() []example {
	const day = 24 * 60 * 60 * 1000
	at := time.Date(2026, 10, 12, 12, 4, 0, 0, time.UTC).UnixMilli()
	unicode := wire.Term{Color: "truecolor", Glyphs: "unicode", Background: "dark"}
	ascii := wire.Term{Color: "none", Glyphs: "ascii", Background: "unknown"}
	calls := []wire.ModelCall{
		{Seq: 3, TS: at - 2*day, Input: 9000, Cached: 1800, Output: 400, MS: 5400},
		{Seq: 5, TS: at - day, Input: 20000, Cached: 15000, Output: 900, MS: 3100},
		{Seq: 6, TS: at - day + 60_000, Input: 21200, Cached: 14000, Output: 120, MS: 2000},
		{Seq: 8, TS: at - 30_000, Input: 3154, Cached: 1536, Output: 211, MS: 5408},
		{Seq: 9, TS: at - 20_000, Error: "rate_limit"},
		{Seq: 11, TS: at - 10_000, Input: 2000, Cached: 2000, Output: 30, MS: 900},
	}
	cold := []wire.ModelCall{{Seq: 1, TS: at, Input: 4000, Cached: 1000}, {Seq: 2, TS: at, Input: 4000, Cached: 2900}}
	return []example{
		{
			name: "cache", read: []Grant{GrantModelCalls},
			calls: []exampleCall{
				{
					golden: "cache.segment", op: OpSegment, name: "cache",
					in: Input{Term: unicode, Width: 100, Data: wire.Data{ModelCalls: calls}},
					// 34336 of 55354 tokens came from the cache.
					check: func(r wire.Reply) bool { return r.Text == "cache avg 62%" && r.Short == "62%" && r.Tone == "" },
				},
				{
					golden: "cache.segment-cold", op: OpSegment, name: "cache",
					in:    Input{Term: unicode, Data: wire.Data{ModelCalls: cold}},
					check: func(r wire.Reply) bool { return r.Text == "cache avg 49%" && r.Short == "49%" && r.Tone == "warn" },
				},
				{
					golden: "cache.segment-none", op: OpSegment, name: "cache", in: Input{Term: unicode},
					check: func(r wire.Reply) bool { return r.Text == "" && r.Short == "" },
				},
				{
					golden: "cache.stats", op: OpCommand, name: "stats",
					in: Input{Term: unicode, Width: 100, Data: wire.Data{ModelCalls: calls}},
					check: func(r wire.Reply) bool {
						return len(r.Lines) == 3 && r.Lines[0][0] == wire.Span{T: "Sat 10 Oct", Tone: "dim"} &&
							r.Lines[0][2] == wire.Span{T: "cache 20%", Tone: "warn"} &&
							r.Lines[1][1].T == "  2 calls  41.2k in  " && r.Lines[2][1].T == "  2 calls   5.2k in  "
					},
				},
				{
					golden: "cache.stats-1", op: OpCommand, name: "stats",
					in: Input{Term: unicode, Args: []string{"1"}, Data: wire.Data{ModelCalls: calls}},
					check: func(r wire.Reply) bool {
						return len(r.Lines) == 1 && r.Lines[0][0].T == "Mon 12 Oct" && r.Lines[0][2].T == "cache 69%"
					},
				},
				{
					golden: "cache.stats-bad", op: OpCommand, name: "stats",
					in: Input{Term: unicode, Args: []string{"soon"}, Data: wire.Data{ModelCalls: calls}},
					check: func(r wire.Reply) bool {
						return strings.HasPrefix(r.Error, "usage: /stats [days]") && len(r.Lines) == 0
					},
				},
				{
					golden: "cache.stats-none", op: OpCommand, name: "stats", in: Input{Term: unicode},
					check: func(r wire.Reply) bool { return r.Note == "no model calls to count yet" && len(r.Lines) == 0 },
				},
			},
		},
		{
			name: "git", read: []Grant{GrantWorkspace},
			calls: []exampleCall{
				{
					golden: "git.branch", op: OpSegment, name: "branch",
					in:    Input{Term: unicode, Data: wire.Data{Workspace: &wire.Workspace{Dir: "contactd", Branch: "main"}}},
					check: func(r wire.Reply) bool { return r.Text == "⎇ main" && r.Short == "main" },
				},
				{
					golden: "git.branch-ascii", op: OpSegment, name: "branch",
					in:    Input{Term: ascii, Data: wire.Data{Workspace: &wire.Workspace{Dir: "contactd", Branch: "main"}}},
					check: func(r wire.Reply) bool { return r.Text == "main" && r.Short == "main" },
				},
				{
					golden: "git.branch-none", op: OpSegment, name: "branch",
					in:    Input{Term: unicode, Data: wire.Data{Workspace: &wire.Workspace{Dir: "notes"}}},
					check: func(r wire.Reply) bool { return r.Text == "" && r.Short == "" },
				},
			},
		},
		{
			name: "standup", read: []Grant{GrantClock},
			calls: []exampleCall{
				{
					golden: "standup.standup", op: OpCommand, name: "standup",
					in: Input{Term: unicode, Data: wire.Data{Now: at}},
					check: func(r wire.Reply) bool {
						return r.Insert == "Summarize what we did yesterday (Sun 11 Oct) for the team: done, next, blocked." &&
							len(r.Lines) == 0 && r.Note == ""
					},
				},
				{
					// The first of a month: yesterday is in the month before.
					golden: "standup.standup-first", op: OpCommand, name: "standup",
					in: Input{Term: unicode, Data: wire.Data{Now: time.Date(2027, 1, 1, 0, 30, 0, 0, time.UTC).UnixMilli()}},
					check: func(r wire.Reply) bool {
						return r.Insert == "Summarize what we did yesterday (Thu 31 Dec) for the team: done, next, blocked."
					},
				},
			},
		},
	}
}

// TestExamples builds each mod in examples/mods as `mise run mods:build`
// does, installs it from a directory laid out like dist/mods/<name>, and
// calls it.
func TestExamples(t *testing.T) {
	t.Parallel()
	b := newBed(t)
	h := b.open(t, nil)
	for _, ex := range examples() {
		t.Run(ex.name, func(t *testing.T) {
			t.Parallel()
			info := installExample(t, h, ex.name)
			if info.Status != StatusOff || info.Manifest.Name != ex.name || !slices.Equal(info.Manifest.Read, ex.read) {
				t.Fatalf("Install() = %+v, want %s, off, reading %v", info, ex.name, ex.read)
			}
			if info.Manifest.Version == "" || info.Manifest.Description == "" {
				t.Errorf("the manifest has no version or no description: %+v", info.Manifest)
			}

			for _, c := range ex.calls {
				r, err := h.Call(t.Context(), ex.name, c.op, c.name, c.in)
				if err != nil {
					t.Fatalf("%s: %v", c.golden, err)
				}
				if !c.check(r) {
					t.Errorf("%s: reply %+v", c.golden, r)
				}
				got, err := json.MarshalIndent(r, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				tuitest.Golden(t, "examples/"+c.golden+".golden.json", append(got, '\n'))
			}
			if info := infoOf(t, h, ex.name); info.Status != StatusOn || info.Calls != len(ex.calls) {
				t.Errorf("after its calls the mod is %s with %d calls", info.Status, info.Calls)
			}
		})
	}
}

// TestALongLivedModAnswersAsANewOne puts every request of the benchmarks and
// of TestExamples to each example mod three times: in order, then backwards
// to the instance that has by then answered all of them, then to an instance
// that has answered nothing else. The glue keeps a decoder, its buffers and
// the short strings it has read from one call to the next, and none of that
// may show in an answer.
func TestALongLivedModAnswersAsANewOne(t *testing.T) {
	t.Parallel()
	type request struct {
		title, op, name string
		in              Input
	}
	requests := map[string][]request{}
	for _, c := range benchCalls() {
		requests[c.mod] = append(requests[c.mod], request{c.title, c.op, c.name, c.in})
	}
	for _, ex := range examples() {
		for _, c := range ex.calls {
			requests[ex.name] = append(requests[ex.name], request{c.golden, c.op, c.name, c.in})
		}
	}
	// Short strings that differ and still fall in the same place in the
	// glue's memory of strings: equally long, with the same ends.
	for _, days := range []string{"1", "3", "7"} {
		requests["cache"] = append(requests["cache"], request{"stats " + days, OpCommand, "stats", Input{
			Term: benchTerm(), Args: []string{days}, Data: wire.Data{ModelCalls: usualCalls(MaxModelCalls)},
		}})
	}
	for _, branch := range []string{"mean", "moan", "main"} {
		requests["git"] = append(requests["git"], request{"branch " + branch, OpSegment, "branch", Input{
			Term: benchTerm(), Data: wire.Data{Workspace: &wire.Workspace{Dir: "notes", Branch: branch}},
		}})
	}
	b := newBed(t)
	h := b.open(t, nil)
	for _, name := range []string{"cache", "git", "standup"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			installExample(t, h, name)
			ask := func(r request) wire.Reply {
				t.Helper()
				reply, err := h.Call(t.Context(), name, r.op, r.name, r.in)
				if err != nil {
					t.Fatalf("%s: %v", r.title, err)
				}
				return reply
			}
			all := requests[name]
			first := make([]wire.Reply, len(all))
			for i, r := range all {
				first[i] = ask(r)
			}
			for i, r := range slices.Backward(all) {
				if again := ask(r); !reflect.DeepEqual(again, first[i]) {
					t.Errorf("%s: an instance that had answered other requests said\n     %+v\nfirst %+v", r.title, again, first[i])
				}
			}
			if n := starts(h, name); n != 1 {
				t.Fatalf("the mod had %d instances so far, want 1", n)
			}
			m := h.lookup(name)
			for i, r := range all {
				// Closing the instance is what the host does after a
				// call that failed: the next call starts a new one.
				m.call.Lock()
				h.stop(t.Context(), m)
				m.call.Unlock()
				if fresh := ask(r); !reflect.DeepEqual(fresh, first[i]) {
					t.Errorf("%s: a new instance said\n     %+v\nfirst %+v", r.title, fresh, first[i])
				}
			}
			if n, want := starts(h, name), 1+len(all); n != want {
				t.Errorf("the mod had %d instances, want %d", n, want)
			}
		})
	}
}

// installExample builds one of the mods in examples/mods as `mise run
// mods:build` does, installs it from a directory laid out like
// dist/mods/<name>, and switches it on. It returns what Install said, which
// is from before the mod was on.
func installExample(tb testing.TB, h *Host, name string) Info {
	tb.Helper()
	bin := buildGuest(tb, "github.com/quinnovator/contactd/examples/mods/"+name)
	manifest, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "mods", name, ManifestFile))
	if err != nil {
		tb.Fatal(err)
	}
	dist := filepath.Join(tb.TempDir(), "dist", "mods", name)
	if err := os.MkdirAll(dist, 0o700); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, ManifestFile), manifest, 0o600); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, name+".wasm"), bin, 0o600); err != nil {
		tb.Fatal(err)
	}
	info, err := h.Install(tb.Context(), dist)
	if err != nil {
		tb.Fatalf("Install(%s): %v", name, err)
	}
	switchOn(tb, h, name)
	return info
}

// TestShippedModsLinkNoReflection keeps the slow code out of what ships.
//
// A module pays for every jump it takes, and encoding/json and fmt, which
// work by reflection, take them by the thousand: with encoding/json in the
// glue, a segment that was sent 50 model-call records took 2.7 ms of a 5 ms
// budget. The glue reads and writes JSON by hand instead. This fails when
// the glue or an example mod imports reflection again, by any road.
func TestShippedModsLinkNoReflection(t *testing.T) {
	t.Parallel()
	const module = "github.com/quinnovator/contactd/"
	shipped := []string{
		module + "internal/tui/mods/guest",
		module + "examples/mods/cache",
		module + "examples/mods/git",
		module + "examples/mods/standup",
	}
	args := append([]string{"list", "-deps", "-f", "{{.ImportPath}}"}, shipped...)
	cmd := exec.CommandContext(t.Context(), "go", args...)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOFLAGS=")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	linked := strings.Fields(string(out))
	for _, pkg := range shipped {
		if !slices.Contains(linked, pkg) {
			t.Fatalf("go list did not list %s: the test looks at nothing", pkg)
		}
	}
	for _, slow := range []string{"reflect", "encoding/json", "fmt"} {
		if slices.Contains(linked, slow) {
			t.Errorf("the shipped mods link %s", slow)
		}
	}
}

// awkwardRequests are requests, and things that are nearly requests, chosen
// for what a decoder that reads eight bytes at a time could get wrong.
func awkwardRequests() []string {
	var out []string
	// Whole numbers of 1 to 21 digits, in a field of every number type,
	// plain and below zero. The longest fit no field.
	for n := 1; n <= 21; n++ {
		for _, digits := range []string{"1" + strings.Repeat("0", n-1), strings.Repeat("9", n), "1234567890123456789012"[:n]} {
			for _, sign := range []string{"", "-"} {
				num := sign + digits
				out = append(out,
					`{"abi":`+num+`,"data":{"now":`+num+`}}`,
					`{"data":{"contact":{"id":`+num+`}}}`,
					`{"data":{"model_calls":[{"seq":`+digits+`,"ts":`+num+`,"step":`+num+`,"input":`+num+`,"ms":`+num+`}]}}`,
					`{"width":`+num+`}`,
				)
			}
		}
	}
	// Numbers that are not whole, or not JSON.
	for _, num := range []string{"0", "-0", "00", "01", "1.0", "1e3", "1E3", "12345678.5", "123456789e1", "0x1", "+1", "-", "1a", "12345678a", ""} {
		out = append(out, `{"abi":`+num+`}`, `{"data":{"model_calls":[{"input":`+num+`,"cached":7}]}}`)
	}
	// Strings of 0 to 40 bytes: plain, and with a quote, a backslash, a
	// newline, a control character, a two-byte and a four-byte character,
	// and a byte that is no character, at every place.
	for n := range 41 {
		plain := strings.Repeat("abcdefghij", 4)[:n]
		out = append(out, `{"op":"`+plain+`","name":"`+plain+`","args":["`+plain+`"]}`)
		for at := range n {
			for _, odd := range []string{`\"`, `\\`, `\n`, `\u00e9`, `\ud83d\ude42`, "é", "🙂", "\xff", "\x01", `\q`} {
				text := plain[:at] + odd + plain[at+1:]
				out = append(out, `{"op":"`+text+`"}`, `{"data":{"model_calls":[{"route":"`+text+`","error":"`+text+`"}]}}`)
			}
		}
	}
	// Escapes one after another, of both kinds, and text outside ASCII in
	// runs, with what is wrong at every place of a run; each at every
	// distance from the end of the input, where the quick ways stop.
	for _, text := range []string{
		`\n\n\"\"\\\\\/\b\f\r\t`, `\u0001\u001f\u007f\u0080\u07ff\u0800\uffff\u0000`, `a\u0001b\nc\u00e9\"d\u2028\\e\u001b[31m`,
		`\u00AB\uabcd\uAbCd`, `\u0041\ud83d\u0042\ud83d\ude42\u0043\ude42`, `\ug041`, `\u0g41`, `\u00g1`, `\u004g`, `\n\q`, `\u004`,
		"\\u00\xb1\xb1", "日本語のテキスト", "日本語\\n日本語\\u0001日本語\\\"日本語", "🙂🙂🙂🙂", "ж\xd0", "語\xe8\xaa", "🙂\xf0\x9f\x99",
		"\xed\xa0\x80語", "語\xf4\x90\x80\x80", "\xc0\xaf\xe0\x9f\xbf\xf0\x8f\xbf\xbf", "é\x80é\xffé", "語語語語語語語語語語語語語語語語語語語語\xff",
	} {
		for pad := range 9 {
			out = append(out,
				`{"op":"`+text+`"`+strings.Repeat(" ", pad)+`}`,
				`{"data":{"model_calls":[{"route":"`+text+`","error":"`+text+text+`"}]}}`+strings.Repeat(" ", pad),
				`{"args":["`+text+`","`+strings.Repeat("x", pad)+text+`"]}`,
			)
		}
	}
	// Every key, in the host's order and form.
	full, err := encodeRequest(buildRequest(Manifest{Read: Grants()}, OpCommand, "echo", fullInput()))
	if err != nil {
		panic(err)
	}
	out = append(out, string(full))
	// The same cut short at every byte, which is never a request.
	for cut := range full {
		out = append(out, string(full[:cut]))
	}
	// As much as a request can hold of what is hard on a decoder: nesting
	// to the limit and past it, a number of 60,000 digits, and nothing but
	// quotes, backslashes, brackets or bytes that are no character.
	nested := func(n int) string { return `{"x":` + strings.Repeat("[", n) + strings.Repeat("]", n) + `,"op":"deep"}` }
	out = append(out,
		nested(9_999), nested(10_000), `{"x":`+strings.Repeat(`{"a":`, 9_999)+"1"+strings.Repeat("}", 9_999)+`}`,
		`{"abi":`+strings.Repeat("9", 60_000)+`}`, `{"abi":-`+strings.Repeat("0", 60_000)+`}`,
		strings.Repeat(`"`, MaxMessage), strings.Repeat(`\`, MaxMessage), strings.Repeat("[", MaxMessage),
		strings.Repeat("\xff", MaxMessage), strings.Repeat(`{"a":`, MaxMessage/5), `{"op":"`+strings.Repeat(`\ud83d`, 10_000),
		strings.Repeat(" ", 60_000)+`{"op":"late"}`, `{"args":[`+strings.Repeat("null,", 5_000)+`null],"op":"nulls"}`,
	)
	// And in other forms.
	return append(out,
		``, `{`, `{}`, `null`, `[]`, ` { } `, `{"abi":1,}`, `{"abi":1}{}`, `{"abi":1} `,
		"{\n\t\"abi\" : 1 ,\n\t\"op\" : \"x\" ,\n\t\"data\" : { \"now\" : 5 , \"model_calls\" : [ { \"seq\" : 1 } , { \"seq\" : 2 } ] }\n}",
		`{"data":{"now":3,"model_calls":[{"error":"e","seq":4}]},"term":{"background":"dark"},"op":"x","abi":1}`,
		`{"abi":1,"abi":2,"args":["a","b","c"],"args":["d"],"args":[null,null,null]}`,
		`{"term":{"color":"16","glyphs":"ascii"},"term":{"color":"256"},"item":{"kind":"k"},"item":null}`,
		`{"a":1,"b":"s","c":true,"d":null,"e":[1,[2,{"g":[]}]],"f":{"i":{"j":-1.5e+3}},"abi":4}`,
		`{"\u0061bi":7}`, `{"x":[1,2,}`, `{"x":`+strings.Repeat("[", 200)+strings.Repeat("]", 200)+`}`,
		`{"abi":null,"op":null,"args":null,"term":null,"data":null,"item":null}`,
		`{"data":{"state":{"replying":true,"conn":"live"},"messages":[],"workspace":null}}`,
		`{"data":{"state":{"replying":1}}}`, `{"op":7}`, `{"args":[1]}`, `{"term":[]}`,
	)
}

// callCounter counts the functions a module calls. wazero tells it of each.
type callCounter struct{ calls atomic.Int64 }

func (c *callCounter) NewFunctionListener(api.FunctionDefinition) experimental.FunctionListener {
	return c
}

func (c *callCounter) Before(context.Context, api.Module, api.FunctionDefinition, []uint64, experimental.StackIterator) {
	c.calls.Add(1)
}

func (*callCounter) After(context.Context, api.Module, api.FunctionDefinition, []uint64) {}

func (*callCounter) Abort(context.Context, api.Module, api.FunctionDefinition, error) {}

// TestTheCacheSegmentDoesLittleWork is the gate on the speed of a call that
// no clock is needed for: it counts the functions the cache mod calls to
// answer the largest requests the host sends its segment, and the ones whose
// text is slowest to read.
//
// A module pays the sandbox for every function it enters, so the count is
// what the call costs, on any machine. With encoding/json in the glue it was
// over 100,000. It is the fewest of several calls that is held to the limit,
// since now and then the guest's collector runs during one.
func TestTheCacheSegmentDoesLittleWork(t *testing.T) {
	t.Parallel()
	bin := buildGuest(t, "github.com/quinnovator/contactd/examples/mods/cache")
	counter := &callCounter{}
	ctx := experimental.WithFunctionListenerFactory(context.WithoutCancel(t.Context()), counter)
	cache := wazero.NewCompilationCache()
	rt, err := newRuntime(ctx, cache, DefaultMemoryPages)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = rt.Close(ctx)
		_ = cache.Close(ctx)
	})
	cm, err := rt.CompileModule(ctx, bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkModule(cm); err != nil {
		t.Fatal(err)
	}
	inst, err := instantiate(ctx, rt, cm, nil)
	if err != nil {
		t.Fatal(err)
	}

	man := Manifest{Name: "cache", Read: []Grant{GrantModelCalls}}
	for _, tc := range []struct {
		name  string
		calls []wire.ModelCall
		want  string
		limit int64
	}{
		{"50 records", usualCalls(MaxModelCalls), "cache avg 47%", 2600},
		{"the largest request", largestCalls(), "cache avg 90%", 3200},
		// Every call failed with an error page: text with a newline or a
		// quote every few words, and angle brackets, which the host
		// leaves as they are.
		{"50 failed calls", failedCalls(), "", 4800},
		// The same with a provider's JSON for an error: a quote every few
		// characters, each one an escape.
		{"50 failed calls, JSON", jsonFailedCalls(), "", 6500},
		// Text outside ASCII is checked to be UTF-8 without a call for
		// each character.
		{"50 failed calls, not ASCII", foreignFailedCalls(), "", 3400},
		// Control characters are written as escapes of six bytes, and
		// nothing but quotes is an escape at every second byte. Neither is
		// read with a call for each escape: when they were, these two took
		// 29,540 and 54,224 calls.
		{"50 failed calls, binary", controlCalls(), "", 3100},
		{"nothing but escapes", worstCalls(), "", 3900},
	} {
		in := Input{Term: benchTerm(), Width: 100, Data: wire.Data{ModelCalls: tc.calls}}
		body, err := encodeRequest(buildRequest(man, OpSegment, "cache", in))
		if err != nil {
			t.Fatal(err)
		}
		fewest := int64(math.MaxInt64)
		for range 20 {
			before := counter.calls.Load()
			raw, err := inst.roundTrip(ctx, body)
			if err != nil {
				t.Fatal(err)
			}
			fewest = min(fewest, counter.calls.Load()-before)
			var r wire.Reply
			if err := json.Unmarshal(raw, &r); err != nil || r.Text != tc.want {
				t.Fatalf("%s: the reply is %s (%v), want the text %q", tc.name, raw, err, tc.want)
			}
		}
		t.Logf("%s (%d bytes): %d function calls in the module", tc.name, len(body), fewest)
		if fewest > tc.limit {
			t.Errorf("%s: the module called %d functions to answer; the limit is %d", tc.name, fewest, tc.limit)
		}
	}
}
