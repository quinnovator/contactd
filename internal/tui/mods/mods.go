// Package mods runs the terminal client's mods: small WebAssembly modules that
// add a status-line segment, a slash command, or a way to draw one kind of
// item.
//
// A mod is a directory holding a manifest, mod.toml, and one module. The
// manifest names what the mod adds and what it may read. The host keeps one
// instance of each mod that is switched on and calls it with a JSON request;
// the mod answers with a JSON reply. That is the whole boundary: a mod has no
// host functions, no files, no network, no environment and no output of its
// own, so what it can reach is what its request holds.
//
// The host does not trust a mod. It refuses a module that imports anything
// but WASI, gives each call a deadline, counts the calls that run over, and
// switches a mod off after three in a row. Every string a mod returns is made
// safe to draw before the caller sees it.
//
// docs/MODS.md is the reference for the manifest and the ABI.
package mods

import (
	"errors"
	"log/slog"
	"time"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
)

// Grant names one thing a mod may read. A manifest lists the grants it
// wants; the list is closed.
type Grant string

const (
	// GrantContact is the open contact's name and id.
	GrantContact Grant = "contact"
	// GrantRoute is the contact's provider and model.
	GrantRoute Grant = "route"
	// GrantState is whether the client is connected and a reply is being
	// written.
	GrantState Grant = "state"
	// GrantModelCalls is the records of the open contact's model calls.
	GrantModelCalls Grant = "model_calls"
	// GrantMessages is the last messages, sent when a command of the mod is
	// run, and the items the mod draws.
	GrantMessages Grant = "messages"
	// GrantWorkspace is the directory name and git branch where the client
	// started.
	GrantWorkspace Grant = "workspace"
	// GrantClock is the time.
	GrantClock Grant = "clock"
)

// Grants returns every grant there is, in the order they are listed
// everywhere.
func Grants() []Grant {
	return []Grant{
		GrantContact, GrantRoute, GrantState, GrantModelCalls,
		GrantMessages, GrantWorkspace, GrantClock,
	}
}

// Status is what a mod is doing.
type Status uint8

const (
	// StatusOff means the mod is installed and not switched on.
	StatusOff Status = iota + 1
	// StatusLoading means the mod is switched on and Load has not reached it
	// yet.
	StatusLoading
	// StatusOn means the mod is loaded and can be called.
	StatusOn
	// StatusFailed means the mod could not be read or loaded. Reason says
	// why.
	StatusFailed
	// StatusDisabled means the host switched the mod off for running over
	// its budget or failing. Reason says why. It stays off until it is
	// switched on again.
	StatusDisabled
)

// String returns the status as one lower-case word.
func (s Status) String() string {
	switch s {
	case StatusOff:
		return "off"
	case StatusLoading:
		return "loading"
	case StatusOn:
		return "on"
	case StatusFailed:
		return "failed"
	case StatusDisabled:
		return "disabled"
	}
	return "unknown"
}

// Info is one installed mod as a screen shows it.
type Info struct {
	// Manifest is the mod's manifest. When the manifest could not be read
	// only its Name is set, to the directory's name.
	Manifest Manifest
	Status   Status
	// Reason is the load error, or why the mod was switched off. It is safe
	// to draw.
	Reason string
	// Median and Slowest are over the last 100 calls.
	Median, Slowest time.Duration
	// Calls counts the calls made since the mod was loaded.
	Calls int
}

// Options configure a Host. A zero number or a nil function takes the
// default named beside it.
type Options struct {
	// Dir holds one directory per installed mod. StatePath is the file that
	// records which are on. CacheDir holds compiled modules. With no Dir
	// there are no mods; with no StatePath nothing is remembered; with no
	// CacheDir modules are compiled again on every start.
	Dir, StatePath, CacheDir string
	// Budget is how long a call may take before it counts as a strike: 5 ms.
	// Kill is how long before the call is stopped: 50 ms.
	Budget, Kill time.Duration
	// Strikes is how many strikes in a row switch a mod off: 3.
	Strikes int
	// MemoryPages is the most memory a mod may have, in 64 KiB pages: 256.
	MemoryPages uint32
	// Now measures calls, and stamps the notice when a mod is switched off.
	// Tests inject it. The default is time.Now.
	Now func() time.Time
	// Logger hears about mods that fail to load and mods switched off. The
	// default discards.
	Logger *slog.Logger
}

// The defaults of Options.
const (
	DefaultBudget      = 5 * time.Millisecond
	DefaultKill        = 50 * time.Millisecond
	DefaultStrikes     = 3
	DefaultMemoryPages = 256
)

// Input is everything a request could carry; the host keeps what the mod's
// grants allow.
type Input struct {
	Term  wire.Term
	Data  wire.Data
	Width int
	// Args are a command's arguments. They are sent for "command" only.
	Args []string
	// Item is the item to draw. It is sent for "render" only.
	Item *wire.Item
}

// ErrDisabled is in the error Call returns when it has just switched a mod
// off. Call wraps it with the reason, once, on the third strike in a row.
var ErrDisabled = errors.New("the mod was switched off")

// The sizes the ABI fixes.
const (
	// MaxModule is the largest module the host reads, in bytes.
	MaxModule = 16 << 20
	// MaxMessage is the largest request or reply, in bytes.
	MaxMessage = 64 << 10
	// MaxItem is the largest item text handed to a renderer, in bytes. A
	// larger item is drawn by the client.
	MaxItem = 8 << 10
	// MaxMessagesBytes is how much message text one request carries, in
	// bytes, and MaxMessages how many messages.
	MaxMessagesBytes = 8 << 10
	MaxMessages      = 20
	// MaxModelCalls is how many model-call records one request carries, and
	// MaxCallText how many bytes of a record's route and of its error.
	MaxModelCalls = 50
	MaxCallText   = 256
	// MaxLines is how many lines of a reply are kept, and MaxSpanRunes how
	// many runes of each string.
	MaxLines     = 200
	MaxSpanRunes = 512
)

// withDefaults fills the options a caller left zero.
func (o Options) withDefaults() Options {
	if o.Budget <= 0 {
		o.Budget = DefaultBudget
	}
	if o.Kill <= 0 {
		o.Kill = DefaultKill
	}
	if o.Strikes <= 0 {
		o.Strikes = DefaultStrikes
	}
	if o.MemoryPages == 0 {
		o.MemoryPages = DefaultMemoryPages
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	return o
}

// slowdown is how many times longer a call may take on a machine where
// wazero has no compiler and interprets the module.
func slowdown(goarch string) time.Duration {
	if goarch == "amd64" || goarch == "arm64" {
		return 1
	}
	return 8
}
