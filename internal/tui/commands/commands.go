// Package commands is what the chat client knows about slash commands: the
// list the client ships with, a registry that mods add to, how a draft is
// read as a command or a message, and the text that helps finish one.
//
// It holds names and descriptions only. What a command does lives with the
// client's model. Nothing here draws, does I/O or reads a clock.
package commands

// SourceCore is the Source of a command the client ships with.
const SourceCore = "core"

// Spec describes one command.
type Spec struct {
	// Name is what follows the slash, in lower case.
	Name string
	// Usage is the arguments the command takes, or empty.
	Usage string
	// Summary is one line on what the command does. A core command with a
	// Key ends it with that key in brackets.
	Summary string
	// Source is "core", or "mod <name>" for a command a mod adds.
	Source string
	// Key is the chord that does the same, for /help.
	Key string
	// NeedsArg means the command cannot run without an argument.
	NeedsArg bool
}

// Core returns the commands the client ships with, in the order they are
// listed. The slice is the caller's own.
func Core() []Spec {
	specs := []Spec{
		// The usage is the ghost text the Commands mock draws. The command
		// still takes a preset's name after the word.
		{Name: "statusline", Usage: "preset | edit", Summary: "choose a preset or edit the status line"},
		{Name: "status", Summary: "connection, route and cache for this contact"},
		{Name: "stop", Summary: "stop the reply in flight", Key: "esc"},
		{Name: "help", Usage: "[command]", Summary: "list the commands and the keys"},
		{Name: "contacts", Summary: "back to the list of contacts", Key: "ctrl+p"},
		{Name: "open", Usage: "<contact>", Summary: "open another contact by name or id", NeedsArg: true},
		{Name: "new", Summary: "start a new contact"},
		{Name: "rename", Usage: "[name]", Summary: "rename this contact"},
		{Name: "details", Summary: "fold or unfold tool calls", Key: "ctrl+o"},
		{Name: "mods", Usage: "[on|off|install|update <x>]", Summary: "list the mods, or change one"},
		{
			Name: "image", Usage: "pixels | blocks | text | auto | save <n> [path]",
			Summary: "how images are shown this session, or save one",
		},
		{Name: "theme", Usage: "auto | dark | light", Summary: "the background this session"},
		{Name: "glyphs", Usage: "auto | unicode | ascii", Summary: "the glyph set this session"},
		{Name: "color", Usage: "auto | truecolor | 256 | 16 | none", Summary: "the colour profile this session"},
		{Name: "top", Summary: "go to the oldest loaded message"},
		{Name: "bottom", Summary: "go to the newest message and follow"},
		{Name: "redraw", Summary: "clear the screen and draw it again", Key: "ctrl+l"},
		{Name: "reload", Summary: "read the config files and mods again"},
		{Name: "quit", Summary: "leave", Key: "ctrl+c"},
	}
	for i := range specs {
		specs[i].Source = SourceCore
		if specs[i].Key != "" {
			specs[i].Summary += " (" + specs[i].Key + ")"
		}
	}
	return specs
}
