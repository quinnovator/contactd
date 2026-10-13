package mods

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
)

// benchTerm is the terminal of the benchmarks' requests.
func benchTerm() wire.Term {
	return wire.Term{Color: "truecolor", Glyphs: "unicode", Background: "dark"}
}

// usualCalls is n model-call records as a contact has them: a short route
// and no error.
func usualCalls(n int) []wire.ModelCall {
	const hour = 60 * 60 * 1000
	at := time.Date(2026, 10, 12, 12, 4, 0, 0, time.UTC).UnixMilli()
	calls := make([]wire.ModelCall, n)
	for i := range calls {
		calls[i] = wire.ModelCall{
			Seq: uint64(100 + 3*i), TS: at + int64(i-n)*hour, Step: 1 + i%3,
			Route: "deepseek/deepseek-flash",
			Input: 3000 + 211*i, Cached: 1500 + 97*i, Output: 200 + i, MS: 5000 + int64(i),
		}
	}
	return calls
}

// largestCalls is the most model-call data the host sends when the text is
// plain: MaxModelCalls records, the route and the error of each as long as
// the host lets them be, and numbers with as many digits as a real one has.
// Text that has to be escaped makes a longer request and a slower one; see
// worstCalls.
func largestCalls() []wire.ModelCall {
	calls := usualCalls(MaxModelCalls)
	for i := range calls {
		calls[i].Seq += 1_000_000
		calls[i].Input += 1_000_000
		calls[i].Cached += 900_000
		calls[i].Output += 10_000
		calls[i].Reasoning = 10_000 + i
		calls[i].MS += 100_000
		calls[i].Route = strings.Repeat("r", MaxCallText)
		if i%2 == 1 {
			// A failed call is left out of the count, so only every
			// other record has an error.
			calls[i].Error = strings.Repeat("e", MaxCallText)
		}
	}
	return calls
}

// failedCalls is MaxModelCalls records of calls that all failed with a
// provider's error page, which is what an outage leaves behind: text full of
// the characters JSON has to escape.
func failedCalls() []wire.ModelCall {
	const page = "<html>\n<head><title>502 Bad Gateway</title></head>\n<body class=\"error\">\n" +
		"<center><h1>502 Bad Gateway</h1></center>\n<hr><center>nginx & friends</center>\n</body>\n</html>\n"
	return failedWith(strings.Repeat(page, 2))
}

// failedWith is MaxModelCalls records of calls that all failed with the same
// error text. A failed call is left out of the count, so the mod reads every
// record and answers with nothing.
func failedWith(text string) []wire.ModelCall {
	calls := usualCalls(MaxModelCalls)
	for i := range calls {
		calls[i].Input, calls[i].Cached, calls[i].Output = 0, 0, 0
		calls[i].Error = text
	}
	return calls
}

// jsonFailedCalls is an outage at a provider that answers in JSON, as most
// do: a quote every few characters, each of which is written as an escape.
func jsonFailedCalls() []wire.ModelCall {
	const body = `{"error":{"message":"Rate limit reached for model \"flash\" in organization org-1","type":"rate_limit_error",` +
		`"param":null,"code":"rate_limit_exceeded"}}`
	return failedWith(strings.Repeat(body, 2))
}

// foreignFailedCalls is an outage whose error text is not in ASCII: 77
// characters of three bytes each in every record.
func foreignFailedCalls() []wire.ModelCall {
	return failedWith(strings.Repeat("請求過於頻繁請稍後再試", 7))
}

// controlCalls is records whose errors are binary: control characters, each
// of which is written as an escape of six bytes. 180 in each record is as
// many as fit in a request.
func controlCalls() []wire.ModelCall {
	return failedWith(strings.Repeat("\x01\x1b", 90))
}

// worstCalls is the request that takes a Go guest longest to read for its
// size: MaxModelCalls records whose route and error are nothing but quotes,
// so that every second byte of 57 KiB is an escape. Nothing real looks like
// it. It is here to show where the ceiling is.
func worstCalls() []wire.ModelCall {
	calls := failedWith(strings.Repeat(`"`, MaxCallText))
	for i := range calls {
		calls[i].Route = strings.Repeat(`"`, MaxCallText)
	}
	return calls
}

// benchCall is one call a benchmark repeats.
type benchCall struct {
	title, mod, op, name string
	in                   Input
}

// benchCalls are the calls of the shipped example mods, each with the usual
// request and with the largest ones the host can send it: the most plain
// data, and the text that is slowest to read.
func benchCalls() []benchCall {
	at := time.Date(2026, 10, 12, 12, 4, 0, 0, time.UTC).UnixMilli()
	long := strings.Repeat("b", 128)
	data := func(calls []wire.ModelCall) Input {
		return Input{Term: benchTerm(), Width: 100, Data: wire.Data{ModelCalls: calls}}
	}
	return []benchCall{
		{"cache/segment/1-call", "cache", OpSegment, "cache", data(usualCalls(1))},
		{"cache/segment/10-calls", "cache", OpSegment, "cache", data(usualCalls(10))},
		{"cache/segment/50-calls", "cache", OpSegment, "cache", data(usualCalls(MaxModelCalls))},
		{"cache/segment/largest", "cache", OpSegment, "cache", data(largestCalls())},
		{"cache/segment/50-failed", "cache", OpSegment, "cache", data(failedCalls())},
		{"cache/segment/50-failed-json", "cache", OpSegment, "cache", data(jsonFailedCalls())},
		{"cache/segment/50-failed-not-ascii", "cache", OpSegment, "cache", data(foreignFailedCalls())},
		{"cache/segment/50-failed-binary", "cache", OpSegment, "cache", data(controlCalls())},
		{"cache/segment/worst", "cache", OpSegment, "cache", data(worstCalls())},
		{"cache/stats/50-calls", "cache", OpCommand, "stats", data(usualCalls(MaxModelCalls))},
		{"cache/stats/largest", "cache", OpCommand, "stats", data(largestCalls())},
		{"git/branch/usual", "git", OpSegment, "branch", Input{
			Term: benchTerm(), Width: 100,
			Data: wire.Data{Workspace: &wire.Workspace{Dir: "contactd", Branch: "main"}},
		}},
		{"git/branch/largest", "git", OpSegment, "branch", Input{
			Term: benchTerm(), Width: 100,
			Data: wire.Data{Workspace: &wire.Workspace{Dir: long, Branch: long}},
		}},
		{"standup/standup", "standup", OpCommand, "standup", Input{
			Term: benchTerm(), Width: 100, Data: wire.Data{Now: at},
		}},
	}
}

// BenchmarkExamples times one Host.Call to each shipped example mod, with
// the usual request and with the largest ones the host can send. The target
// on the development machine is 0.5 ms for a segment; it is never asserted.
//
// The host here is the one the client runs, with the real clock and the
// default budget and memory limit, except that it never stops a call and
// never switches a mod off: a benchmark has to finish to report a time.
func BenchmarkExamples(b *testing.B) {
	root := b.TempDir()
	h, err := Open(Options{
		Dir:       filepath.Join(root, "mods"),
		StatePath: filepath.Join(root, "mods.toml"),
		CacheDir:  filepath.Join(root, "wazero"),
		Kill:      time.Minute,
		Strikes:   1 << 30,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = h.Close(b.Context()) })
	for _, name := range []string{"cache", "git", "standup"} {
		installExample(b, h, name)
	}
	for _, c := range benchCalls() {
		b.Run(c.title, func(b *testing.B) {
			info := infoOf(b, h, c.mod)
			body, err := encodeRequest(buildRequest(info.Manifest, c.op, c.name, c.in))
			if err != nil {
				b.Fatal(err)
			}
			// The first call of an instance warms it up.
			if _, err := h.Call(b.Context(), c.mod, c.op, c.name, c.in); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := h.Call(b.Context(), c.mod, c.op, c.name, c.in); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(len(body)), "request-bytes")
		})
	}
}

// BenchmarkBuildRequest times the host's share of a call that is not the
// module: choosing what the grants allow and writing it as JSON.
func BenchmarkBuildRequest(b *testing.B) {
	man := Manifest{
		Name: "cache", Read: []Grant{GrantModelCalls},
		Segments: []SegmentDecl{{Name: "cache", Refresh: RefreshModelCall}},
	}
	for _, c := range benchCalls() {
		if c.mod != "cache" || c.op != OpSegment {
			continue
		}
		b.Run(c.title, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := encodeRequest(buildRequest(man, c.op, c.name, c.in)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkHostOverhead times one Host.Call to a module that does nothing:
// it answers every request with the same bytes. What is left is the host's
// own share of a call: the request written as JSON and copied in, two calls
// into the sandbox, the reply copied out, read and made safe.
func BenchmarkHostOverhead(b *testing.B) {
	bd := newBed(b)
	put(b, filepath.Join(bd.dir, "m"), manifestFor("m", `read = ["model_calls"]`), reply(okReply).binary())
	h := bd.open(b, func(o *Options) {
		o.Now = nil
		o.Strikes = 1 << 30
	})
	switchOn(b, h, "m")
	for _, c := range benchCalls() {
		if c.mod != "cache" || c.op != OpSegment {
			continue
		}
		b.Run(strings.TrimPrefix(c.title, "cache/segment/"), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := h.Call(b.Context(), "m", OpSegment, "seg", c.in); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
