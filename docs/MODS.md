# Mods

A mod is a small WebAssembly module that adds something to `contactd chat`: a status-line segment, a slash command, or a way to draw one kind of item. This file is the reference for the manifest a mod ships with and for ABI 1, the contract between the client and a mod.

The host is `internal/tui/mods`. The JSON types are in `internal/tui/mods/wire`. The glue for a mod written in Go is `internal/tui/mods/guest`. Three example mods are in `examples/mods`.

## What a mod can and cannot do

A mod is called with a JSON request and answers with a JSON reply. That is the whole boundary.

| A mod can | A mod cannot |
| --- | --- |
| Return text and tone names for a status-line segment | Read a file, an environment variable or an argument |
| Print lines under its name, put text in the input box, or show a note, when its command is run | Open a socket or call any host function |
| Return the lines that draw a code block or a tool result | Send a message, or press enter for the user |
| Read what its manifest asks for and the user can see on the `/mods` screen | Emit an escape sequence, or write to the terminal |
| Keep state in memory between calls | Sleep, or keep running after its deadline |

The module runs in the client's process, in a sandbox (wazero). It has no filesystem, no network, no environment and no standard output.

## On disk

```text
<config>/mods/<name>/mod.toml     the manifest, written by the mod's author
<config>/mods/<name>/<module>     the .wasm file
<config>/mods.toml                which mods are on; the client writes it
<cache>/wazero                    compiled modules; safe to delete
```

`<config>` is what `-config-dir` names, else `$CONTACTD_CONFIG_DIR`, else `$XDG_CONFIG_HOME/contactd`, else `~/.config/contactd`. `<cache>` is `$XDG_CACHE_HOME/contactd`, else `contactd` in the system's cache directory.

`/mods install <directory>` copies `mod.toml` and the module it names from a directory, and nothing else in it; so does `i` on the `/mods` screen. A path that does not start at the root starts where the client was started, and `~` is the home directory. A mod that was just installed is off. Switching it on is a separate step: `space` on the `/mods` screen, where what the mod may read is shown first, or `/mods on <name>`.

## The manifest: `mod.toml`

```toml
abi = 1
name = "cache"
version = "0.3.1"
description = "How much of each prompt the provider served from its cache."
module = "cache.wasm"
sha256 = ""
read = ["model_calls"]
budget = "5ms"

[[segment]]
name = "cache"
refresh = "model_call"

[[command]]
name = "stats"
usage = "[days]"
summary = "token use and cache share by day"

[[renderer]]
kind = "code:mermaid"
```

The file is TOML, in the subset the client reads: comments, strings, integers, booleans, arrays of strings, `[table]` and `[[table]]`.

| Key | Required | Meaning |
| --- | --- | --- |
| `abi` | yes | The contract the mod was written to. Only `1` loads. |
| `name` | yes | A lower-case letter, then at most 31 of `a-z`, `0-9` and `-`. It must equal the directory's name. |
| `module` | yes | The file name of the module in the same directory: letters, digits, `.`, `-` and `_`, not starting with a dot, at most 64 bytes. The file is at most 16 MiB. |
| `version` | no | Shown on the `/mods` screen. At most 32 characters. |
| `description` | no | Shown on the `/mods` screen. At most 240 characters. |
| `sha256` | no | A pin: the module's SHA-256 in hex. A module that does not match fails to load. Empty pins nothing. |
| `read` | no | The grants the mod asks for, from the list below. A name that is not on the list is an error. |
| `budget` | no | The mod's own budget for one call, as a duration. It may only lower the host's 5 ms; a higher one is an error. |

Each `[[segment]]` has a `name` and a `refresh`. Each `[[command]]` has a `name`, and may have a `usage` (at most 64 characters) and a `summary` (at most 120). Each `[[renderer]]` has a `kind`. A segment or command name is a lower-case letter, then at most 31 of `a-z`, `0-9`, `-` and `_`. A manifest holds at most 32 of each, and no name twice.

`refresh` says when a segment is asked for again:

| Value | When |
| --- | --- |
| `model_call` | after each new model-call record |
| `turn` | when a turn ends |
| `state` | when the connection or the live state changes |
| a duration, such as `30s` | on a timer; at least `1s` |

`kind` is `code:<language>` for fenced code blocks in that language, or `tool:<tool name>` for the results of that tool. The part after the colon is at most 64 of letters, digits and `_ . + # -`. A renderer is handed text from the conversation, so a manifest with a renderer must have `messages` in `read`.

A key the client does not know is skipped, so a manifest written for a newer client still loads on an older one.

A command whose name is taken by the client, or by a mod that is on and whose name sorts before this one's, is registered as `<mod>.<name>`. Which mod has the plain name depends only on which mods are on, never on the order they were switched on in.

## Grants

A request carries only what the manifest's `read` names. The list is closed.

| Grant | Field of `data` | What it is |
| --- | --- | --- |
| `contact` | `contact` | the open contact's name and id |
| `route` | `route` | its provider and model |
| `state` | `state` | whether the client is connected and replying, and the tool that is running |
| `model_calls` | `model_calls` | records of the open contact's model calls: the newest 50, oldest first. A record's `route` and `error` are each cut to 256 bytes. |
| `messages` | `messages` | the last 20 messages, at most 8 KiB of text in all, sent only when the mod's command is run. The grant also lets the mod be handed the items it draws. |
| `workspace` | `workspace` | the base name of the directory where the client started, and the git branch checked out there |
| `clock` | `now` | the time, in unix milliseconds. The module's own wall clock reads the same time. |

There is no grant for the network or for files.

Without `clock` the module's wall clock is a fake that says nothing about the real time. With it, the clock reads the time of the request and stands still during a call. A mod is told no time zone: a day is a UTC day.

The client reads `.git/HEAD` itself for the `workspace` grant, and only when a mod that is on holds it. A detached HEAD has no branch.

## ABI 1

### The module

A mod is a WASI reactor: a module that is instantiated once and then called many times.

It must export:

| Export | Signature | What it does |
| --- | --- | --- |
| `memory` | | the module's memory; at most 256 pages (16 MiB) |
| `_initialize` | `() -> ()` | run once, when the module is instantiated |
| `mod_alloc` | `(n: i32) -> i32` | returns the address of `n` bytes the host may write to |
| `mod_call` | `(ptr: i32, len: i32) -> i64` | handles the request at `ptr`; returns `ptr << 32 \| len` of the reply |

It may import functions from `wasi_snapshot_preview1` only, and none whose name starts with `sock_`. A module that imports anything else, or imports a memory, is refused when it is loaded, before any of it runs. There are no host functions.

### A call

1. The host calls `mod_alloc(len)` and writes the request, as JSON, at the address it returns.
2. The host calls `mod_call(ptr, len)`.
3. The host copies the reply out of the module's memory, from the address and length in the result.

The module keeps the request buffer and the reply alive until the next call. A request and a reply are each at most 64 KiB.

### The request

```json
{
  "abi": 1,
  "op": "segment",
  "name": "cache",
  "width": 100,
  "term": {"color": "truecolor", "glyphs": "unicode", "background": "dark"},
  "data": {
    "model_calls": [
      {"seq": 8, "ts": 1791201810000, "step": 1, "route": "deepseek/deepseek-flash",
       "input": 3154, "cached": 1536, "output": 211, "reasoning": 0, "ms": 5408, "error": ""}
    ]
  }
}
```

| Field | Meaning |
| --- | --- |
| `abi` | always `1` |
| `op` | `hello`, `segment`, `command` or `render` |
| `name` | the segment, the command, or the renderer kind |
| `args` | a command's arguments; `command` only |
| `width` | the columns there are to draw in: the terminal's for a segment, two fewer for a command's lines, and the item's own width for a renderer |
| `term` | what the terminal shows: `color` is `none`, `16`, `256` or `truecolor`; `glyphs` is `unicode` or `ascii`; `background` is `dark`, `light` or `unknown`. It is in every request, so a mod can leave out a glyph under ASCII. In `hello` its values are empty. |
| `data` | what the grants allow; see above |
| `item` | the item to draw, as `{"kind": ..., "text": ...}`; `render` only |

`hello` is sent once, when the mod is loaded. It carries no data. Its reply is checked for `"abi": 1` and otherwise ignored.

Unknown fields are to be ignored, in both directions, so either side can be the newer one. Keys are written in lower case, exactly as in the tables here, and a mod may rely on that.

The request is one line of JSON in UTF-8, with no white space between its parts. A character outside ASCII is written as it is, and so are `<`, `>` and `&`. What is written as an escape is a quote, a backslash, a control character, U+2028 and U+2029, and a byte that is not UTF-8, which becomes `\ufffd`. So most strings can be read without looking for one.

### The reply

```json
{"abi": 1, "text": "cache avg 61%", "short": "61%", "tone": "warn"}
```

| Field | Used by | Meaning |
| --- | --- | --- |
| `abi` | all | must be `1`; any other number is a failure |
| `text` | `segment` | the segment's value; empty hides the segment |
| `short` | `segment` | a compact form for a narrow status line |
| `tone` | `segment`, `command` | the tone of the segment, or of a command's `note`; a note with no tone is drawn as a warning |
| `lines` | `command`, `render` | lines of spans: `[[{"t": "11 Oct", "tone": "dim"}, {"t": " 61%", "bold": true}]]` |
| `insert` | `command` | text put in the input box. It is never sent; the user reads it and presses enter. |
| `note` | `command` | one line shown on the hint row |
| `pass` | `render` | `true` lets the client draw the item itself |
| `error` | all | the mod could not do what was asked. A command's is shown as a note, and the line that was run goes back into the input box. A segment then reads `<name> ?`. A renderer's leaves the item to the client |

A command's `lines` are printed in the transcript as a local block under the mod's name. They are not in the conversation and the contact never sees them.

Tone names: `""` (plain), `dim`, `accent`, `tool`, `ok`, `warn`, `bad`, `mod`. An unknown tone is drawn plain. The client chooses the colours.

### What the host does to a reply

- Every string becomes one line with no control character in it: an escape sequence a mod returns is drawn as its printable characters and does nothing. Newlines become spaces.
- Every string is cut to 512 characters, and a reply to 200 lines.
- When the terminal's glyph set is ASCII, any other character becomes `?`.

### What the host does not send

- A `render` request whose item is over 8 KiB is not sent. The client draws the item.
- `messages` are sent with `command` only, never with `segment` or `render`.

## When the client calls a mod

Nothing calls a mod from the loop that reads keys and draws. Mods are loaded after the first frame, and each call is made in the background; its answer is taken when it arrives.

- **A segment** is asked for when a conversation is opened, and then on its `refresh`. One call at a time is made for a segment: a trigger that comes during a call has it asked once more when the call returns. Until an answer comes the bar shows the last one. Segments are asked only while a conversation is open, and what they said is dropped when another is opened: their data is the open contact's. A change of what the terminal shows (`term`), a rename of the contact and `/reload` ask every segment again.
- **A command** is run when its line is entered. Its `lines` are printed, its `insert` goes into the input box ahead of anything typed since, and its `note` is shown. An answer that comes after the conversation was left is dropped.
- **A renderer** is asked for an item when the item first comes into view at a width. Until it answers the client draws the item itself, and it does so for good when the reply is `pass`, has an `error`, has no `lines`, or the call fails. What a mod drew is kept by item, part and width, and asked for again after `/reload` and after a change of `term`. Kinds are matched without regard to case. An item with no sequence number, such as a reply still being written, is never sent.
- **`messages`** are the newest 20 messages of the two people in the conversation, oldest first, with the roles `user` and `agent`. Tool calls and their results are not among them.
- **`workspace`** is read when the mods are loaded, after a mod is switched on, installed or updated, on `/reload`, and when a turn ends.
- **`/reload`** starts the mods that are on and not running. It does not read the mods directory again: a mod copied there by hand is seen at the next start.

## Time, strikes and being switched off

A call has two limits.

- **The budget, 5 ms.** A call that takes longer still counts, and its reply is used, but it is a strike.
- **The deadline, 50 ms.** A call that is still running is stopped and its instance thrown away. That is a strike too.

Any failure is also a strike and also costs the instance: a trap or a panic, a reply that is not JSON, a reply with another `abi`, a reply over 64 KiB or outside the module's memory.

After a failure the next call gets a new instance, so whatever the mod kept in memory is gone. The first call of a new instance cannot strike for time. A call within the budget clears the count.

Three strikes in a row switch the mod off. The client says so once, in the transcript (`mod cache was switched off: over budget 3 times in a row`), takes the mod's segments and commands away, and records the reason in `mods.toml`. The mod stays off until it is switched on again, on the `/mods` screen or with `/mods on <name>`.

On a machine where the module is interpreted (anything but amd64 and arm64) both limits are eight times longer.

A module cannot sleep: the sleep it is given returns at once.

## The state file: `mods.toml`

```toml
[mod.cache]
on = true
source = "/Users/jack/src/contactd/dist/mods/cache"
sha256 = "9f2c..."
disabled = ""
```

| Key | Meaning |
| --- | --- |
| `on` | the user switched the mod on |
| `source` | the directory the mod was installed from; `u` on the `/mods` screen and `/mods update <name>` copy from it again |
| `sha256` | the module's hash when the mod was installed or last switched on |
| `disabled` | why the host switched the mod off, such as `over budget 3 times in a row, 2026-10-12 12:04` |

A module on disk that no longer matches `sha256` is off until it is switched on again. So is a mod whose module or manifest changed in an update.

## Writing a mod in Go

A mod is a `main` package with two exported functions and an empty `main`. Every file carries `//go:build wasip1`.

```go
//go:build wasip1

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
		return wire.Reply{}
	}
	return wire.Reply{Text: ws.Branch}
}
```

`guest.Call` decodes the request, runs the handler, sets `abi` and encodes the reply. A handler answers an `op` it does not know, `hello` included, with an empty reply.

`guest` reads and writes the JSON with code of its own, not with `encoding/json`. It reads what `encoding/json` would read into a `wire.Request`, with one difference: a key counts only in lower case, as it is written above. A request that is not JSON, or has a value of the wrong kind, is answered with `"error": "the request is not JSON"` and the handler is not run.

Build it as a reactor:

```sh
GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -buildmode=c-shared \
    -trimpath -ldflags='-s -w' -o dist/mods/git/git.wasm ./examples/mods/git
cp examples/mods/git/mod.toml dist/mods/git/
```

`mise run mods:build` does this for the three examples. Then, in the client, `/mods install dist/mods/git`.

Things to know:

- A Go module of this kind is about 2 MiB and takes a few milliseconds to start. It is compiled once and the result is cached.
- There is no network to reach. Go's `net` cannot open a connection under wasip1, and a module that imports a socket function is refused.
- A panic ends the instance. It is a strike, and the next call starts a new instance.
- A jump is the slow part. The host has to be able to stop a module that never returns, so the sandbox checks for a stop at every jump a Go module takes: each time round a loop, each call of a function, each `if` that skips something. A check costs 10 to 30 ns, about what forty additions cost, and a Go guest runs four to seven times slower for it. An allocation costs about 0.4 µs. The budget of 5 ms is about 200,000 jumps.
- So keep `encoding/json`, `fmt` and anything else that works by reflection out of a mod. With `encoding/json` in the glue, a segment that was sent 50 model-call records (7.6 KiB) took 2.7 ms on an Apple M5 Max, over half the budget. The glue now reads the request by hand and the same call takes 0.12 ms; a call with no data takes 0.02 ms. `strconv`, `strings` and `time` are fine.
- What a request holds counts for more than how long it is. Plain text is read eight bytes at a time; an escape and a character outside ASCII are each read one at a time. On the same machine, 50 records with the longest plain routes and errors the host sends (26 KiB) take 0.23 ms; 50 failed calls that each hold an error page, a provider's JSON or text outside ASCII take 0.3 to 0.4 ms. The ceiling is a request that is nothing but escapes, which nothing real sends: 0.9 ms for 60 KiB of control characters and 1.2 ms for 56 KiB of quotes. `mise exec -- go test -run '^$' -bench Examples ./internal/tui/mods/` measures all of these for the three examples.
- Ask only for the grants the mod uses. What a mod is not sent it does not have to read.
- Random numbers are the same on every start: the module is given a fixed seed, not the system's.
- `GOOS=wasip1 GOARCH=wasm go vet ./examples/mods/...` checks a mod's source. Without `GOOS=wasip1` the build tag hides it from every tool.

A mod can be written in any language that produces a module with the four exports. The glue in `guest` is two buffers, a function that compares an address, and the JSON of a request and a reply.

## The examples

| Mod | Grants | Adds |
| --- | --- | --- |
| `cache` | `model_calls` | A segment, `cache avg 61%` (short `61%`), in the warn tone below 50%: the share of prompt tokens served from the provider's cache, over the calls it is given. A command, `/stats [days]`: one line a day with the calls, the input tokens and the cache share. |
| `git` | `workspace` | A segment, `⎇ main`, or `main` with ASCII glyphs. It is hidden when there is no branch. |
| `standup` | `clock` | A command, `/standup`, that puts `Summarize what we did yesterday (Sun 11 Oct) for the team: done, next, blocked.` in the input box. |

## What is refused, and when

| When | What | Result |
| --- | --- | --- |
| Reading `mod.toml` | bad TOML, another `abi`, a bad name, a name that is not the directory's, an unknown grant, a renderer without `messages`, a budget above the host's | the mod is listed as failed, with the reason |
| Loading | a module over 16 MiB, a module that does not match the pin, a file that is not a module, an import outside WASI or of a socket function, a missing export or one with the wrong signature, more than 16 MiB of memory, a `hello` that fails | the mod is failed, with the reason; no other mod is affected |
| Starting | a module that changed since it was switched on | the mod is off until it is switched on again |
| Calling | a failure or a slow call, three times in a row | the mod is switched off, with a notice |

`contactd chat -mods=false` starts the client with no mods at all. So do `CONTACTD_MODS=0` and `mods = false` in `chat.toml`; `/mods` and `/status` then say which of the three it was. A `mods.toml` that cannot be read also starts the client without mods, and says so before the screen and in both places.
