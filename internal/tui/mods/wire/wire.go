// Package wire holds the JSON types of mod ABI 1: what the terminal client
// sends a mod and what a mod sends back. The host and every guest import it,
// so it holds types and nothing else.
//
// A call is one Request in and one Reply out. Unknown fields are ignored both
// ways, so either side can be the newer one. A mod sends text and tone names,
// never escape sequences; the host makes every string safe before drawing it.
//
// Tone names, wherever a tone is asked for: "" (plain), "dim", "accent",
// "tool", "ok", "warn", "bad" and "mod".
package wire

// ABI is the version of this contract. Every request and every reply carries
// it, and a reply with another number is a failure.
const ABI = 1

// Request is one call to a mod.
type Request struct {
	ABI int `json:"abi"`
	// Op is what is asked: "hello" when the mod is loaded, "segment" for a
	// status-line value, "command" to run a command, "render" to draw an
	// item.
	Op string `json:"op"`
	// Name is the segment or command name, or the renderer kind.
	Name string `json:"name,omitempty"`
	// Args are a command's arguments.
	Args []string `json:"args,omitempty"`
	// Width is how many columns there are to draw in.
	Width int `json:"width,omitempty"`
	// Term is in every request, so a mod can leave out a glyph or a tone the
	// terminal cannot show.
	Term Term `json:"term"`
	// Data holds what the mod's grants let it read, and nothing else.
	Data Data `json:"data"`
	// Item is the item to draw. It is set for "render" only.
	Item *Item `json:"item,omitempty"`
}

// Term is what the terminal can show.
type Term struct {
	// Color is "none", "16", "256" or "truecolor".
	Color string `json:"color"`
	// Glyphs is "unicode" or "ascii".
	Glyphs string `json:"glyphs"`
	// Background is "dark", "light" or "unknown".
	Background string `json:"background"`
}

// Data is what a mod may read. Each field is there only when the mod's
// manifest holds the grant named beside it.
type Data struct {
	// Contact needs the grant "contact".
	Contact *Contact `json:"contact,omitempty"`
	// Route needs the grant "route".
	Route *Route `json:"route,omitempty"`
	// State needs the grant "state".
	State *State `json:"state,omitempty"`
	// ModelCalls needs the grant "model_calls": the newest 50, oldest first.
	ModelCalls []ModelCall `json:"model_calls,omitempty"`
	// Messages needs the grant "messages" and is sent for "command" only:
	// the newest 20, 8 KiB in all.
	Messages []Message `json:"messages,omitempty"`
	// Workspace needs the grant "workspace".
	Workspace *Workspace `json:"workspace,omitempty"`
	// Now needs the grant "clock": the time in unix ms.
	Now int64 `json:"now,omitempty"`
}

// Contact is the open contact.
type Contact struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

// Route is the provider and model the contact talks to.
type Route struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// State is what the conversation is doing.
type State struct {
	// Conn is the connection state as a word, such as "live".
	Conn string `json:"conn"`
	// Replying says a reply is being written.
	Replying bool `json:"replying"`
	// Running names the tool that is executing, or is empty.
	Running string `json:"running"`
}

// ModelCall is the record of one call to a model.
type ModelCall struct {
	Seq uint64 `json:"seq"`
	// TS is when the call was recorded, in unix ms.
	TS int64 `json:"ts"`
	// Step is the call's place among the calls of its turn.
	Step int `json:"step"`
	// Route names the provider and model that were called.
	Route string `json:"route"`
	// Input, Cached, Output and Reasoning are what the provider reported, in
	// its own tokens. Cached is the part of Input read from its cache.
	Input     int `json:"input"`
	Cached    int `json:"cached"`
	Output    int `json:"output"`
	Reasoning int `json:"reasoning"`
	// MS is how long the call took, in milliseconds.
	MS int64 `json:"ms"`
	// Error is the class of error when the call failed, or empty.
	Error string `json:"error"`
}

// Message is one message of the conversation.
type Message struct {
	Seq uint64 `json:"seq"`
	// TS is when it was said, in unix ms.
	TS int64 `json:"ts"`
	// Role is who said it.
	Role string `json:"role"`
	Text string `json:"text"`
}

// Workspace is where the client was started.
type Workspace struct {
	// Dir is the directory's base name only, never its path.
	Dir string `json:"dir"`
	// Branch is the git branch checked out there, or empty.
	Branch string `json:"branch"`
}

// Item is one item of the conversation, handed to a renderer.
type Item struct {
	// Kind is the renderer kind, such as "code:mermaid".
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Reply is a mod's answer. Which fields count depends on the request's Op.
type Reply struct {
	// ABI is in every reply.
	ABI int `json:"abi"`
	// Text is a segment's value. An empty one hides the segment.
	Text string `json:"text,omitempty"`
	// Short is a segment's compact form, for a narrow status line.
	Short string `json:"short,omitempty"`
	// Tone names the segment's tone.
	Tone string `json:"tone,omitempty"`
	// Lines are a command's output, or a rendered item.
	Lines []Line `json:"lines,omitempty"`
	// Insert is text a command puts in the input box. It is never sent.
	Insert string `json:"insert,omitempty"`
	// Note is one line a command shows on the hint row.
	Note string `json:"note,omitempty"`
	// Pass, from a renderer, lets the client draw the item itself.
	Pass bool `json:"pass,omitempty"`
	// Error says the mod could not do what was asked.
	Error string `json:"error,omitempty"`
}

// Line is one row of output: spans drawn one after another.
type Line []Span

// Span is a run of text in one tone.
type Span struct {
	T    string `json:"t"`
	Tone string `json:"tone,omitempty"`
	Bold bool   `json:"bold,omitempty"`
}
