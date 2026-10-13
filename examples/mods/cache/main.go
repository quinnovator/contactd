//go:build wasip1

// Command cache is an example mod. It adds a status-line segment with the
// share of prompt tokens the provider served from its cache, averaged over
// the model calls it is given, and a /stats command that breaks the same
// records down by day.
//
// It holds one grant, model_calls, so the records are all it ever sees.
package main

import (
	"strconv"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/tui/mods/guest"
	"github.com/quinnovator/contactd/internal/tui/mods/wire"
)

//go:wasmexport mod_alloc
func modAlloc(n uint32) uint32 { return guest.Alloc(n) }

//go:wasmexport mod_call
func modCall(ptr, n uint32) uint64 { return guest.Call(ptr, n, handle) }

func main() {}

// defaultDays is how many days /stats lists when it is given no number.
const defaultDays = 7

// warnBelow is the cache share, in percent, under which the tone warns.
const warnBelow = 50

func handle(req wire.Request) wire.Reply {
	switch {
	case req.Op == "segment" && req.Name == "cache":
		return segment(req.Data.ModelCalls)
	case req.Op == "command" && req.Name == "stats":
		return stats(req.Args, req.Data.ModelCalls)
	}
	return wire.Reply{}
}

// tally is the model calls of one period added up.
type tally struct {
	calls, input, cached int
}

// add counts one call. A call that failed, or reported no input, says
// nothing about the cache and is left out.
func (t *tally) add(c wire.ModelCall) {
	if c.Error != "" || c.Input <= 0 {
		return
	}
	t.calls++
	t.input += c.Input
	t.cached += min(max(c.Cached, 0), c.Input)
}

// share is the cached part of the input, in percent, rounded to nearest.
func (t tally) share() int {
	if t.input == 0 {
		return 0
	}
	return (t.cached*100 + t.input/2) / t.input
}

// segment is the status-line value. With no call to go on it is empty, which
// hides the segment.
func segment(calls []wire.ModelCall) wire.Reply {
	var all tally
	for _, c := range calls {
		all.add(c)
	}
	if all.calls == 0 {
		return wire.Reply{}
	}
	pct := strconv.Itoa(all.share()) + "%"
	return wire.Reply{Text: "cache avg " + pct, Short: pct, Tone: tone(all.share())}
}

func tone(share int) string {
	if share < warnBelow {
		return "warn"
	}
	return ""
}

// stats is the /stats command: one line for each of the newest days that
// have calls, oldest first. A mod has no time zone, so a day is a UTC day.
func stats(args []string, calls []wire.ModelCall) wire.Reply {
	days := defaultDays
	if len(args) > 1 {
		return wire.Reply{Error: "usage: /stats [days]"}
	}
	if len(args) == 1 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 || n > 366 {
			return wire.Reply{Error: "usage: /stats [days], with days from 1 to 366"}
		}
		days = n
	}

	const dayMS = 24 * 60 * 60 * 1000
	byDay := map[int64]*tally{}
	var order []int64 // the records are oldest first, and so are the days
	for _, c := range calls {
		day := c.TS / dayMS
		if byDay[day] == nil {
			byDay[day] = &tally{}
			order = append(order, day)
		}
		byDay[day].add(c)
	}
	if len(order) > days {
		order = order[len(order)-days:]
	}

	type row struct {
		date, calls, input string
		share              int
	}
	rows := make([]row, 0, len(order))
	var wideCalls, wideInput int
	for _, day := range order {
		t := byDay[day]
		if t.calls == 0 {
			continue
		}
		r := row{
			date:  time.UnixMilli(day * dayMS).UTC().Format("Mon _2 Jan"),
			calls: plural(t.calls, "call"),
			input: tokens(t.input) + " in",
			share: t.share(),
		}
		wideCalls, wideInput = max(wideCalls, len(r.calls)), max(wideInput, len(r.input))
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		return wire.Reply{Note: "no model calls to count yet"}
	}
	lines := make([]wire.Line, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, wire.Line{
			{T: r.date, Tone: "dim"},
			{T: "  " + left(r.calls, wideCalls) + "  " + left(r.input, wideInput) + "  "},
			{T: "cache " + strconv.Itoa(r.share) + "%", Tone: tone(r.share)},
		})
	}
	return wire.Reply{Lines: lines}
}

// left pads s with spaces on the left to n columns.
func left(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return strings.Repeat(" ", n-len(s)) + s
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// tokens writes a count the way the status line does: 999, 3.2k, 41.2k, 1.5M.
func tokens(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 1_000_000:
		return tenths(n, 1000) + "k"
	}
	return tenths(n, 1_000_000) + "M"
}

// tenths writes n/unit with one decimal, rounded to nearest.
func tenths(n, unit int) string {
	t := (n*10 + unit/2) / unit
	return strconv.Itoa(t/10) + "." + strconv.Itoa(t%10)
}
