//go:build wasip1

// Command git is an example mod. It adds a status-line segment with the git
// branch checked out where the client started.
//
// A mod has no files. The client reads .git/HEAD itself and hands the branch
// to mods that hold the workspace grant, which is the one grant this mod
// holds.
package main

import (
	"github.com/quinnovator/contactd/internal/tui/mods/guest"
	"github.com/quinnovator/contactd/internal/tui/mods/wire"
)

//go:wasmexport mod_alloc
func modAlloc(n uint32) uint32 { return guest.Alloc(n) }

//go:wasmexport mod_call
func modCall(ptr, n uint32) uint64 { return guest.Call(ptr, n, handle) }

func main() {}

func handle(req wire.Request) wire.Reply {
	if req.Op != "segment" || req.Name != "branch" {
		return wire.Reply{}
	}
	ws := req.Data.Workspace
	if ws == nil || ws.Branch == "" {
		// Not a repository, or a detached HEAD: an empty text hides the segment.
		return wire.Reply{}
	}
	text := "⎇ " + ws.Branch
	if req.Term.Glyphs == "ascii" {
		text = ws.Branch
	}
	return wire.Reply{Text: text, Short: ws.Branch}
}
