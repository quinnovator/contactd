//go:build wasip1

// Command guest is the mod the host's tests run: a real Go reactor that goes
// through the same build and the same glue as a mod someone would write. Its
// answers show the tests what the host sent it.
package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/tui/mods/guest"
	"github.com/quinnovator/contactd/internal/tui/mods/wire"
)

// calls counts the calls this instance has answered, so a test can tell a
// new instance from an old one.
//
//nolint:gochecknoglobals // the state an instance keeps between calls
var calls int

// mirror says the command "mirror" was run. From then on the instance
// answers every request, whatever it asks, with an echo of the request as it
// was decoded.
//
//nolint:gochecknoglobals // the state an instance keeps between calls
var mirror bool

//go:wasmexport mod_alloc
func modAlloc(n uint32) uint32 { return guest.Alloc(n) }

//go:wasmexport mod_call
func modCall(ptr, n uint32) uint64 { return guest.Call(ptr, n, handle) }

func main() {}

func handle(req wire.Request) wire.Reply {
	calls++
	if mirror {
		return wire.Reply{Lines: echo(req)}
	}
	switch req.Op {
	case "segment":
		return segment(req)
	case "command":
		return command(req)
	case "render":
		return render(req)
	}
	return wire.Reply{}
}

func segment(req wire.Request) wire.Reply {
	switch req.Name {
	case "count":
		n := strconv.Itoa(calls)
		return wire.Reply{Text: "calls " + n, Short: n, Tone: "mod"}
	case "seen":
		return wire.Reply{Text: seen(req)}
	}
	return wire.Reply{Error: "no segment " + req.Name}
}

// seen names the parts of a request that arrived, in a fixed order.
func seen(req wire.Request) string {
	var parts []string
	d := req.Data
	if d.Contact != nil {
		parts = append(parts, "contact")
	}
	if d.Route != nil {
		parts = append(parts, "route")
	}
	if d.State != nil {
		parts = append(parts, "state")
	}
	if len(d.ModelCalls) > 0 {
		parts = append(parts, "model_calls="+strconv.Itoa(len(d.ModelCalls)))
	}
	if len(d.Messages) > 0 {
		parts = append(parts, "messages="+strconv.Itoa(len(d.Messages)))
	}
	if d.Workspace != nil {
		parts = append(parts, "workspace")
	}
	if d.Now != 0 {
		parts = append(parts, "now")
	}
	if len(req.Args) > 0 {
		parts = append(parts, "args="+strconv.Itoa(len(req.Args)))
	}
	if req.Item != nil {
		parts = append(parts, "item")
	}
	return strings.Join(parts, " ")
}

func command(req wire.Request) wire.Reply {
	switch req.Name {
	case "echo":
		return wire.Reply{Lines: echo(req), Note: seen(req)}
	case "mirror":
		mirror = true
		return wire.Reply{Note: "every request is echoed from now on"}
	case "upper":
		return wire.Reply{
			Insert: strings.ToUpper(strings.Join(req.Args, " ")),
			Note:   strconv.Itoa(len(req.Args)) + " args",
		}
	case "clock":
		// The guest's own clock, beside the time the request carried.
		return wire.Reply{
			Text:  time.Now().UTC().Format(time.RFC3339),
			Short: strconv.FormatInt(req.Data.Now, 10),
		}
	case "mono":
		// Three readings of the guest's monotonic clock, as the two gaps
		// between them in nanoseconds.
		a := time.Now()
		b := time.Now()
		c := time.Now()
		return wire.Reply{
			Text:  strconv.FormatInt(int64(b.Sub(a)), 10),
			Short: strconv.FormatInt(int64(c.Sub(b)), 10),
		}
	case "sleep":
		// A mod cannot sleep: the host gives it no way to wait, so this
		// spins until the host stops it.
		time.Sleep(time.Hour)
		return wire.Reply{Text: "slept"}
	case "hostile":
		return hostile()
	case "churn":
		return churn()
	case "big":
		return wire.Reply{Text: strings.Repeat("x", guest.MaxMessage)}
	case "panic":
		panic("the test asked for a panic")
	}
	return wire.Reply{Error: "no command " + req.Name}
}

// churn makes garbage: 32 KiB that nothing keeps. A guest whose collector
// keeps up can do it for ever in the same memory.
func churn() wire.Reply {
	junk := make([]byte, 32<<10)
	for i := 0; i < len(junk); i += 512 {
		junk[i] = byte(calls & 0xff)
	}
	return wire.Reply{Note: strconv.Itoa(len(junk)) + " bytes, call " + strconv.Itoa(int(junk[0]))}
}

// echo returns the request as the guest decoded it, as JSON cut into spans
// short enough to pass the host's limit on a span.
func echo(req wire.Request) []wire.Line {
	raw, err := json.Marshal(req)
	if err != nil {
		return []wire.Line{{{T: "echo: " + err.Error(), Tone: "bad"}}}
	}
	const chunk = 400
	runes := []rune(string(raw))
	var lines []wire.Line
	for len(runes) > 0 {
		n := min(chunk, len(runes))
		lines = append(lines, wire.Line{{T: string(runes[:n])}})
		runes = runes[n:]
	}
	return lines
}

// hostile is what a mod must not get onto the screen: escape sequences, more
// lines than the host keeps, a span longer than it keeps, and a tone it does
// not know.
func hostile() wire.Reply {
	const esc = "\x1b"
	r := wire.Reply{
		Text:   esc + "]0;owned\x07title",
		Short:  esc + "[31mred",
		Tone:   "blink",
		Insert: "rm -rf /\nyes",
		Note:   "\u202eevil\u009b31m",
		Error:  esc + "Pq#0;2;0;0;0#0~~" + esc + "\\",
	}
	for i := range 300 {
		r.Lines = append(r.Lines, wire.Line{{T: "line " + strconv.Itoa(i) + esc + "[2J", Tone: "accent", Bold: true}})
	}
	r.Lines[0] = wire.Line{{T: strings.Repeat("é", 600), Tone: "nope"}, {T: "⎇ main"}}
	return r
}

func render(req wire.Request) wire.Reply {
	if req.Item == nil {
		return wire.Reply{Error: "no item"}
	}
	switch req.Name {
	case "code:shout":
		var lines []wire.Line
		for l := range strings.SplitSeq(req.Item.Text, "\n") {
			lines = append(lines, wire.Line{
				{T: req.Item.Kind + " ", Tone: "dim"},
				{T: strings.ToUpper(l)},
			})
		}
		return wire.Reply{Lines: lines}
	case "code:seen":
		return wire.Reply{Lines: []wire.Line{{{T: seen(req)}}}}
	case "tool:pass":
		return wire.Reply{Pass: true}
	}
	return wire.Reply{Error: "no renderer " + req.Name}
}
