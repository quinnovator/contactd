//go:build wasip1

// Command standup is an example mod. It adds a /standup command that puts a
// question in the input box, with yesterday's date filled in. The question is
// only inserted: a mod can never send a message, so the user reads it, edits
// it and sends it.
//
// It holds one grant, clock. A mod has no time zone, so yesterday is the day
// before the current UTC day.
package main

import (
	"time"

	"github.com/quinnovator/contactd/internal/tui/mods/guest"
	"github.com/quinnovator/contactd/internal/tui/mods/wire"
)

//go:wasmexport mod_alloc
func modAlloc(n uint32) uint32 { return guest.Alloc(n) }

//go:wasmexport mod_call
func modCall(ptr, n uint32) uint64 { return guest.Call(ptr, n, handle) }

func main() {}

func handle(req wire.Request) wire.Reply {
	if req.Op != "command" || req.Name != "standup" {
		return wire.Reply{}
	}
	when := ""
	if req.Data.Now != 0 {
		yesterday := time.UnixMilli(req.Data.Now).UTC().AddDate(0, 0, -1)
		when = " (" + yesterday.Format("Mon 2 Jan") + ")"
	}
	return wire.Reply{
		Insert: "Summarize what we did yesterday" + when + " for the team: done, next, blocked.",
	}
}
