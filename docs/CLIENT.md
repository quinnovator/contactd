# The terminal client

`contactd chat` is a terminal client for a server running on the same machine. It finds the server through a Unix socket in the data directory, so it needs the same `-data-dir` (or `CONTACTD_DATA_DIR`) and nothing else.

```sh
contactd chat -new ada -system "You are Ada, a thoughtful colleague." -tools python
contactd chat                  # pick a contact from the list
contactd chat ada              # open the conversation with ada
contactd chat ada "question"   # send one message, print the reply, exit
git diff | contactd chat ada - # the same, with the message read from stdin
contactd chat -list            # id, name, route and last activity, tab-separated
```

Where the terminal can draw one, an avatar says the contact's replies aloud and there is no thread to scroll: see [The avatar](#the-avatar). `-avatar=off` gives the thread, and `-mute` an avatar that makes no sound.

In a conversation: `enter` sends and `ctrl+j` starts a new line. `esc` stops the reply in flight, and the avatar's voice with it. In a thread, `pgup` and `pgdn` scroll, and scrolling past the top loads earlier history. `ctrl+o` unfolds tool calls and their output, or on a stage shows the turn as a thread would; `ctrl+p` goes back to the contacts, and `ctrl+c` clears the box, then quits. A line that starts with `/` is a command for the client, and `/help` lists them. There is nothing to do before typing while the agent is replying: a message sent then interrupts it, as it does a person.

The client draws for the terminal it finds: its colours, its background, its font. `-color`, `-glyphs` (or `-ascii`), `-theme` and `-images` say otherwise for one run, and `chat.toml` for every run.

## How it fits together

`contactd chat` is a subcommand of the same binary, so the client and the server it talks to cannot disagree about the protocol. It is built in three layers, and `depguard` keeps each in its place.

- **`internal/client`** knows the API and nothing behind it. It never opens the data directory: a contact's sequence numbers are assigned in memory by the one actor that owns it, so a second process writing the log would break it. Its `Session` keeps a WebSocket open, reconnects with a growing delay, and reads the log around the stream using the `ready` protocol (see [Reading a contact without missing anything](API.md#reading-a-contact-without-missing-anything)), so its caller sees one ordered sequence of updates. After each turn it reads the log once more, which is where the record of the model call comes from.
- **`internal/transcript`** is a pure reducer: events and deltas in, a conversation out. The same event may arrive from the log and from the stream, in either order; the sequence number makes that harmless. It imports no clock and does no I/O, so what a view shows in any situation is a unit test.
- **`internal/tui`** draws the reducer's state and reads keys. It keeps nothing about a contact between runs: contacts, names and messages live on the server. What it keeps is its own: `chat.toml`, `statusline.toml` and the installed mods, in one config directory.

What follows from the platform's shape:

- **There is nothing to pick but a contact.** Each contact keeps one log; visits do not split it into separate conversations. The avatar opens idle, with no previous question or reply on the stage, ready for whatever you want to talk about. The conversation remains in the assistant's context; `ctrl+o` shows the latest turn when you want to read it. Once you start talking, the stage holds the turn in flight, or the last one from this visit. In a thread, earlier history loads a page at a time on scrolling up, and a rule marks each new day.
- **The input never locks.** A message sent mid-reply interrupts it. `esc` interrupts without saying anything: the reply so far is kept and flagged, a running tool finishes first, and the message that was being answered stays unanswered until the next one.
- **A message is shown as sending until the server says it is durable.** One the server refuses, or one that was in flight when the connection dropped and is not in the log afterwards, is put back in the input box.
- **The status bar shows the cache working**: the share of the last prompt that the provider reported as cached, the tokens, and how long the call took.
- **Text from outside is never trusted with the terminal.** The escape character, the other control characters and the bidirectional overrides are taken out of everything a model, a tool, a contact's name, a mod, a status-line command or a pasted draft can put on screen. A test hands one of every kind of terminal sequence to every way in and holds every screen to its own colours and links.

## The screen

One column, full screen. This is the test's own picture of it at 100 columns by 30 rows (`internal/tui/testdata/frame-100x30.golden`):

```text
ada  deepseek/deepseek-flash                                                          7 Oct – 12 Oct
────────────────────────────────────────────────────────────────────────────────────────────────────

────────────────────────────────────────── Wed 7 Oct 2026 ──────────────────────────────────────────

❯ why did Thursday spike?

  Two things overlapped:

  • Turn 6 ran three model calls with tool runs in between.
  • The turn after it re-read the tool results uncached.

  The fix is small. Put the time note after the tool steps:

   go
   liveMsgs = append(liveMsgs, toolSteps...)
   liveMsgs = append(liveMsgs, timeNote(now))

───────────────────────────────────────── Mon 12 Oct 2026 ──────────────────────────────────────────

❯ show me the numbers for turn 7

⏺ python
    print(df[df.turn == 7][["input", "cached", "ms"]])
  ⎿ running… 2.1s
 ● live │ deepseek-flash │ cache 48% │ 3.2k tok │ 5.4s            running python │ esc stop │ 12:06
╭──────────────────────────────────────────────────────────────────────────────────────────────────╮
│ ❯ and what would the share have been with the fix?                                               │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
 enter send · ctrl+j new line · / commands · ctrl+o details · ctrl+p contacts
```

- **Header.** The contact's name, then its route: provider and model at 80 columns or more, the model alone under that, neither under 40. On the right, the days of the lines in view at 80 columns or more, `cache 82%` from 40 to 79, `82%` under 40.
- **Transcript.** Your messages after `❯`; replies as Markdown, indented; a tool call as `⏺ name` with its code, folded to six rows (to none under 40 columns), and its output after `⎿`, folded to four (`ctrl+o` unfolds both); a running tool's time, drawn again five times a second; a sub-agent's task after `⇢`; notices after `◆`; a failed turn after `✗`. Only the blocks in view are ever drawn, so nothing a key, a piece of a reply or a resize costs grows with the conversation.
- **Status bar.** Segments, left and right; see [The status line](#the-status-line). A note for you (`not sent: not connected`) takes the hint line's place until the next key, or the bar's on a terminal too short for a hint line.
- **Input.** A box that grows with the draft up to eight rows, then scrolls. The cursor is the terminal's own, the mouse is never asked for, so selecting text stays the terminal's, and a client with nothing going on writes nothing.

The rows there are decide what is shown. The input is never the part that goes.

| Shown | When |
| --- | --- |
| Header | 6 rows or more |
| Rule under the header | 8 rows or more |
| Rule above the bar | 8 rows or more, where the bar has no background of its own (under 256 colours, or a background the terminal did not report) |
| Box round the input, and the hint line | 16 rows or more; the box also needs 40 columns |
| Input text | as many rows as the draft, at most 8 and at most a third of the rows above 6 |
| List of commands | what it wants, less whatever keeps the transcript at 3 rows |
| Transcript | the rest, never under 1 row |

At 46 by 12 that is the header, a rule, eight rows of transcript, the bar and one input row with no box; the same at 30 columns, with the bar and the header in their short forms.

## The avatar

Where the terminal can draw one, the transcript's rows are a stage, and a conversation is not a thread. Everything around those rows is as above: the header, the status bar, the input, the list of commands and the hint line, and every command.

```text
ada  fake/fake-1                                                                              13 Oct
────────────────────────────────────────────────────────────────────────────────────────────────────
❯ plot last week's latency
                        ▄▄▄▄
                   ▄▄▄▄▄▄▄▄▄▄▄▄▄▄
                 ▗▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▖
               ▗▗▄▄▗▄▄▄▄▄▄▄▄▄▄▙▄▖▄▄▖▖
              ▗▗▗▗▗▟▟▗▄▖▌▐▐▐▐▐▌▌▙▖▖▖▖▖
              ▐▐▐▗▐▌▙▄▗▄▖▄▄▗▄▄▄▐▐▐▖▌▌▌                 ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
             ▐▐▐▐▐▌▄▄▄▟▄▄▄▄▝▄▙▟▐▐▐▐▌▌▌                 ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
             ▐▌▌▌▌▄▄▄▄▄▗▄▄▄▄▄▟▐▐▟▄▐▐▐▐                 ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
              ▌▌▌▌▄▄▄▄▄▝▄▄▄▚▟▄▄▄▄▄▐▐▐▐                 ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
              ▙▙▙▙▄▄▄▄▄▄▄▄▄▄▄▄▄▄▞▗▟▟▟▟                 ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
               ▙▙▄▄▙▄▖▄▄▄▄▄▄▄▄▗▄▟▄▄▟▟                  ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
                ▝▙▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▘                   ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
                   ▀▄▄▄▄▄▄▄▄▄▄▄▄▀                      ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
                       ▀▀▀▀▀▀                          ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
                                                       ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
  Here it is, and that is the whole picture,           chart.png · 160×90 · 43 kB · /image save 24
  drawn by hand.▍
```

The layout is a real client's, in tmux against the scripted model, read back as text; the words are put in. In a terminal the avatar is shaded in colour, and the picture is the picture.

- **It says the reply.** The words appear under it as they are said, at 38 characters a second, with a pause at a comma and a longer one at a full stop; the lips follow the letters, and a small synthesised voice babbles along. What the reply said before a tool call and after it is one speech. On opening, the avatar is idle: previous questions, replies and exhibits stay off the stage, while `ctrl+o` still lets you read the latest turn. New replies appear normally, including a reply already in progress when you connect.
- **It says prose, and shows the rest.** Markdown's marks are not said: emphasis, headings, links and code spans are read as their text. A fenced code block, a table and a picture are shown on the stage at the moment the words reach them, drawn as a thread draws them (highlighting, a mod's renderer, the picture's caption and `/image save` number), and the avatar moves over for them and glances at what it put up. What a turn showed stays up through the next one, until that one shows something of its own.
- **A picture stands where the reply puts it.** The place is the image's title, the quoted word after the path:

  ```markdown
  ![latency by day](latency.png "right")
  ```

  `top`, `bottom`, `left`, `right` or `center`, or one of the first two with one of the second (`top-left`, `bottom right`). `upper`, `lower`, `middle` and `centre` are read too. With no title, or one that names no place, the picture stands beside the avatar on a stage of 72 columns or more and over it on a narrower one, which is also where a code block and a table go. An exhibit never covers the frame around the stage, and is cut before it leaves the avatar no room. Two that would overlap give way to the newer.
- **The contact has to be told.** Nothing in the server mentions pictures or places; that a reply can show `![alt](file "place")` is something a contact's system prompt says, for instance: `Show a file you wrote with ![description](path "left"); the place can be top, bottom, left, right, center or a corner.`
- **Above it, the message it is answering**, on one line, with `sending…` until the server has it. Between it and its words, what the turn is busy with (`⏺ python  running… 2.1s`), or how it ended when it did not end with a reply (`✗ no reply: …`, `⏹ interrupted`).
- **`esc` and a new message stop it talking.** The words are then all there to read. The avatar looks down at a draft while it is typed.
- **What has no place on a stage is read on a sheet** that lies over it: `ctrl+o` (or `/details`) shows the turn as a thread would, tool calls and their output unfolded and the reply as Markdown, and what `/help`, `/status` or a mod's command prints opens the same way. `up`, `down`, `pgup`, `pgdn`, `/top` and `/bottom` scroll the sheet, and `esc` or a new message closes it.
- **`/avatar off` is the thread**, with every message of the conversation, for the session; `/avatar on` is the stage again, with nothing said twice.

It is shown when all of this holds:

| Needs | Why | Otherwise |
| --- | --- | --- |
| Unicode glyphs | It is drawn with block characters, four pixels a cell | a thread |
| 256 colours or more | Its shading; the renderer brings the 24-bit colours down to 256 | a thread |
| A background the terminal named, or `-theme` | Its glow is blended against that colour; `-avatar=on` takes a dark one on trust | a thread, in tmux before 3.4, mosh and screen |

`/avatar` says which of these keeps it away. The rest is settings, in `chat.toml` and for a session by command:

```toml
[avatar]
show = "auto"     # auto | on | off        -avatar, CONTACTD_AVATAR, /avatar on|off|auto
look = "ember"    # ember | tide | orchid | moss                     /avatar look <name>
voice = "chirp"   # chirp | mellow | retro | deep                    /avatar voice <name>
rate = 38         # characters a second, 8 to 240                    /avatar rate <n>
cells = "auto"    # auto | quadrant | octant                         /avatar cells <kind>
mute = false      #                          -mute, CONTACTD_MUTE,   /avatar mute|unmute
```

- **Cells.** A cell is 2 by 2 pixels (quadrants), which every terminal draws. Octants are 2 by 4, square pixels and twice the detail, but only a terminal that draws block characters itself has them; `auto` uses them in Ghostty.
- **Sound.** The voice is opened with the client when the avatar may be shown, also when it starts muted, and never over SSH, where it would sound at the far end. With no audio device the avatar is silent and `/avatar` says so.
- **An idle avatar is not an idle client.** It breathes and blinks, at 30 frames a second, 60 while it speaks, and each frame rewrites the cells that changed: see [Known limits](#known-limits). A thread still writes nothing when nothing happens.

## Keys

| Key | Does |
| --- | --- |
| `enter` | Send the message, or run the command |
| `ctrl+j` | A new line. So are `alt+enter`, `shift+enter` and a backslash before `enter`. The hint line names `shift+enter` once the terminal reports that it can tell it from `enter` |
| `/` | Start a command; `//` starts a message with a slash |
| `tab` | Complete the command |
| `up`, `down` | Choose a command; else scroll three rows, or move in a draft of several rows. On a stage they scroll the sheet, when one is open |
| `pgup`, `pgdn` | Scroll a page less one row. Reaching the top loads earlier history. On a stage, a page of the sheet |
| `esc` | Close the list of commands, or the sheet; else stop the reply in flight and the avatar's voice |
| `ctrl+o` | Fold or unfold tool calls and their output. On a stage, show the turn on a sheet, or close it |
| `ctrl+p` | Back to the contacts |
| `ctrl+l` | Clear the screen and draw it again; pictures are sent again |
| `ctrl+c` | Empty the box; leave when it is empty |
| `ctrl+d` | Leave, when the box is empty |

On the list of contacts: `enter` opens, `n` makes a new contact, `r` renames, `g` reloads, `q` leaves. For a terminal that keeps a `ctrl` chord for itself, each has a command that does the same, and a backslash before `enter` stands in for `ctrl+j`.

## Commands

A one-line draft that starts with `/` is a command. It is run in the client and never sent to the contact, and neither is the sign that someone is writing. While one is typed, the commands it could mean are listed between the transcript and the status bar, the rest of the chosen name and what it takes are shown dim after the cursor, and the hint line says `a line that starts with / is a command and is never sent to the contact`.

| Command | Takes | Does | Key |
| --- | --- | --- | --- |
| `/statusline` | `preset <name>` or `edit` | Apply a preset for this session, or open the editor. No argument opens the editor | |
| `/status` | | Print the connection, the server, the contact and its route, the last model call, what the terminal can do and why the client thinks so, and a line for each mod | |
| `/stop` | | Stop the reply in flight | `esc` |
| `/help` | `[command]` | Print the commands and the keys, or one command | |
| `/contacts` | | Back to the list | `ctrl+p` |
| `/open` | `<contact>` | Open another contact by its name, the start of its name when only one starts so, or its id (`3` or `#3`) | |
| `/new` | | The new-contact form | |
| `/rename` | `[name]` | Rename this contact; no argument opens the prompt | |
| `/details` | | Fold or unfold tool calls; on a stage, show the turn on a sheet | `ctrl+o` |
| `/mods` | `[on\|off\|update <name>]`, `[install <path>]` | The mods screen, or one action | |
| `/image` | `pixels`, `blocks`, `text`, `auto`, or `save <n> [path]` | How pictures are shown this session, or save one | |
| `/avatar` | `on`, `off`, `auto`, `look <name>`, `voice <name>`, `rate <n>`, `cells <kind>`, `mute` or `unmute` | Whether the avatar is shown, and how it looks and sounds, this session. No argument says how things are | |
| `/theme` | `auto`, `dark` or `light` | The background, this session | |
| `/glyphs` | `auto`, `unicode` or `ascii` | The symbols, this session | |
| `/color` | `auto`, `truecolor`, `256`, `16` or `none` | The number of colours, this session | |
| `/top` | | The oldest loaded message; asks for earlier ones. On a stage, the top of the sheet | |
| `/bottom` | | The newest; follow again. On a stage, the end of the sheet | |
| `/redraw` | | Clear the screen and draw it again | `ctrl+l` |
| `/reload` | | Read the `[glyph]` table of `chat.toml` and `statusline.toml` again, start mods that are on and not running, ask every mod again, and fetch again the pictures that could not be shown | |
| `/quit` | | Leave | `ctrl+c` |

- **Enter.** A name typed in full is run. Anything else is completed to the chosen command, which is then run unless it needs an argument (`/open`). A bare `/` is only completed.
- **A draft is kept when its command did nothing.** `no command /foo; /help lists them; start with // to send it`, a wrong argument (the note is the usage) and a refusal all leave the line in the box. A command that takes no argument refuses one: `/top of the morning` is not run as `/top`.
- **A draft of several lines that starts with one `/` is not sent**: `not sent: a draft that starts with / is a command; start with // to send it`.
- **What a command prints** (`/help`, `/status`, a mod's command) is a block in the transcript, after the message that was the newest when it was made, or on a stage a sheet over it. It is the client's own and never in the log.
- **The four setting commands change the running session only.** Their note names the line of `chat.toml` that makes the choice last: `theme: light for this session; theme = "light" in chat.toml keeps it`.

## The status line

The bar is a row of segments read from `statusline.toml` in the config directory. With no file it is the `default` preset.

```toml
[[segment]]
name = "cache"
from = "core"          # core | mod:<mod> | shell
on = true
side = "left"          # left | right
format = "cache $pct%"
short = "$pct%"
warn_below = 50
drop = 4               # 1 is dropped first when narrow; 0 is never dropped

[[segment]]
name = "branch"
from = "mod:git"
side = "right"

[[segment]]
name = "load"
from = "shell"
on = false
side = "right"
command = "uptime"
match = 'load averages?: ([0-9.]+)'   # optional RE2; $1..$9 in format
format = "load $1"                     # $out is the first line of output that is not blank
interval = "10s"                       # at least 1s
timeout = "1s"                         # at most the interval
```

File order is screen order on each side. A file that does not parse leaves the `default` preset at the start, and the bar as it was on `/reload`, with a note naming the line; an unknown key is a warning and the rest still counts.

| Core segment | Shows | Short form | Variables | Side | Drop |
| --- | --- | --- | --- | --- | --- |
| `state` | `● live`, `● connecting`, `● offline, retrying`, `gone` | the same | `$dot $state $err` | left | never |
| `contact` | `ada` | | `$name` | left | 5 |
| `route` | `deepseek-flash` | | `$model $provider` | left | 2 |
| `cache` | `cache 82%`; `call failed` after a failed call | `82%` | `$pct $cached $input` | left | 4 |
| `tokens` | `3.2k tok` | | `$input $output` | left | 1 |
| `latency` | `5.4s` | | `$secs $ms` | left | 3 |
| `activity` | `running python`, `replying`, `loading earlier` | `python` | `$what $tool` | right | 6 |
| `stop` | `esc stop`, while a reply is in flight | `esc` | | right | never |
| `scroll` | `↓ newer`, when not at the newest line | `↓` | `$newer` | right | never |
| `agents` | `2 sub-agents`; hidden at none | `2` | `$n $s` | right | 1 |
| `clock` | `12:06` | the same | `$time` | right | 7 |

`cache`, `tokens` and `latency` are hidden until there is a model call. The state is always a word as well as a colour.

**Presets.** `minimal` is state and cache, with stop, scroll and the clock on the right. `default` adds the route, tokens, latency and the activity. `ops` adds the contact's name and the sub-agents. `/statusline preset <name>` applies one for the session and keeps the mod and shell segments you added.

**When the row is too wide.** Under 40 columns only segments with a short form are drawn, in that form. Otherwise segments go by rising `drop`, the rightmost first among equals: one with a short form switches to it, one without is removed, and every segment is shortened before any that has a short form is removed. If the row still does not fit, the right side goes, then the left is cut. With `● live`, `deepseek-flash`, 82%, `3.7k tok` and `7.2s`, the default bar reads `● live │ cache 82% │ 7.2s` at 46 columns and `● live │ 82%` at 30.

**The editor.** `/statusline` opens it over the conversation, which goes on underneath. `up` and `down` select, `space` switches a segment on or off, `left` and `right` set its side, `J` and `K` move it, `e` edits its fields one at a time, `a` adds a shell segment, `d` deletes one, `p` goes through the presets, `s` saves, `esc` goes back and asks once about unsaved changes. On a terminal of 80 columns or more it previews the row at 100 and 60 columns and with no colour. It is the only thing that writes the file, whole, with a one-line header; comments written by hand are not kept.

**Shell segments.** The command runs as `/bin/sh -c` in a session of its own, with no terminal, its input and its errors at the null device, at most once at a time, the next run an interval after the last one ended. It is stopped at its timeout, and with everything it started when the client is left. The first 4 KiB of what it prints are looked at; each value taken from them is one line of at most 64 characters with no control character in it. A run that fails shows the segment's name and `?`. Commands run only while a conversation is open, and no preset switches one on. Unix only.

**The safety rule.** `statusline.toml` holds commands, so it is run from only when it is a regular file that you own and that neither its group nor everyone can write. Otherwise every shell segment in it is switched off, and a note says why: `shell segments are off: statusline.toml can be written by its group (mode 0660); chmod go-w fixes that`. The check is made before the one read of the file, at the start, on `/reload` and after a save. The directory above the file and access control lists are not looked at.

## Mods

A mod is a small WebAssembly module that adds something to the client. [MODS.md](MODS.md) is the reference for its manifest and for the contract between the client and a mod.

| A mod can add | Which is |
| --- | --- |
| A segment | One cell of the status line: text, a short form and a tone. Asked for again after a model call, when a turn ends, when the connection or the live state changes, or on an interval |
| A command | A `/name` run from the input box. It can print lines under the mod's name, put text in the box, or show a note. It can never send a message |
| A renderer | The lines that draw one kind of item: a fenced code block in one language, or one tool's results. Until the mod answers, and whenever it passes, the client draws the item itself |

- **What a mod is told is what its manifest asks for**, and `/mods` shows that list before you switch it on: `contact` (the open contact's name and id), `route`, `state`, `model_calls` (the newest 50 records), `messages` (the last 20 messages, when you run its command, and the items it draws), `workspace` (the name of the directory the client started in and its git branch, which the client reads itself) and `clock`. There is no grant for the network or for files, and a mod has neither: no host function, no filesystem, no environment, no socket.
- **The budget is 5 ms a call.** A slower call still counts, and is a strike. A call still running at 50 ms is stopped, and so is one that fails. Three strikes in a row switch the mod off, with a notice in the transcript and the reason in `mods.toml`. The shipped `cache` segment takes 0.12 ms for 50 model-call records on the development machine.
- **Mods run off the update loop.** They are loaded after the first frame and called in the background, one call at a time for each segment; a segment shows `name ?` after a call that failed. Nothing a mod returns reaches the terminal as more than text.
- **`/mods`** lists them with what each adds and may read. `space` switches one on or off, `enter` shows its details on a narrow terminal, `i` installs from a directory, `u` updates from where it was installed. `/mods on|off|update <name>` and `/mods install <path>` do the same without the screen. A mod that was just installed, or whose module or manifest changed in an update, is off until you switch it on.
- **`-mods=false`** (or `CONTACTD_MODS=0`, or `mods = false` in `chat.toml`) starts the client with none. `/mods` and `/status` then say which setting did it.

Three examples ship as source in `examples/mods`: `cache` (a segment, `cache avg 61%`, and `/stats [days]`), `git` (a segment with the branch, `⎇ main`) and `standup` (`/standup` puts a question about yesterday's work in the box). To build and install them:

```sh
mise run mods:build            # dist/mods/<name>/<name>.wasm and its mod.toml, for cache, git and standup
contactd chat ada
# then, in the client, started from the repository:
#   /mods install dist/mods/git
#   /mods on git
```

Installed mods are copied to `mods/<name>/` in the config directory, and `mods.toml` beside it records which are on.

## Images

A reply that names a picture among its contact's files, in Markdown image syntax (`![latency by day](latency.png)`), is shown with the picture, as far as the terminal can show one. The files are the ones a tier-0 tool wrote in the contact's working directory, fetched through `GET /v1/contacts/{id}/files/{path...}`.

| Level | Drawn | Needs |
| --- | --- | --- |
| `pixels` | The picture itself: sent once with the kitty graphics protocol and placed with Unicode placeholder cells, so it scrolls and is redrawn as text is | kitty or Ghostty: the terminal must answer the question about kitty graphics at the start and give one of those names; 256 colours or more; Unicode glyphs. Inside tmux, `allow-passthrough on` |
| `blocks` | Half-block characters, two pixels a cell, in 24-bit colour that the renderer brings down to what the terminal has | 16 colours or more; Unicode glyphs |
| `text` | `[image: latency.png, 640×360, 692 kB]` and, dim, the command that saves it | nothing |

- `auto` picks the best level the terminal has. `-images`, `CONTACTD_IMAGES`, `images` in `chat.toml` and `/image pixels | blocks | text | auto` say otherwise; a level the terminal cannot show gives the best one it can, and the note says which.
- **The terminal never sees the file's own bytes.** The client fetches the file (8 MiB at most), reads its header and refuses it by size before decoding anything (8192 pixels a side, 12 million in all), decodes it, scales it down and encodes a PNG of its own. PNG, JPEG and the first frame of a GIF are read. One file is fetched and decoded at a time, away from the update loop.
- **A picture takes at most 44 columns by 12 rows**, less on a small terminal (the width less four, a third of the height), fitted to its shape. The box depends on the terminal's size alone, so a growing draft or the list of commands never makes a picture again. Under it, dim: `latency.png · 640×360 · 692 kB · /image save 31`, with `changed since` when the file is newer than the message.
- **At most four pictures a reply**; the rest stay text. A reply cannot make the client call out: only a relative path with no `:`, `?`, `#`, backslash or `..` is fetched, from the client's own server. `/etc/passwd` is the contact's `etc/passwd`.
- **`/image save <n> [path]`** fetches the file again and writes its own bytes, mode 0600, to the path, or under its own name where the client was started. `<n>` is the reply's number as the caption gives it, `31.2` for its second picture. It never writes over a file, and refuses a name that starts with a dot unless you name the file yourself.
- **Failures are one line**: `[image: latency.png, not found]`, `too large`, `not an image`, or `could not be read; /reload to retry` (a tool may have been in the middle of writing it).
- **The picture is forgotten** by the terminal when the conversation is left and when the client ends. `ctrl+l` sends the pictures in view again, which is the way back when a terminal dropped one without saying so.

## What the terminal can do

Everything drawn follows from one value, worked out when the client starts from the environment, the settings, and what the terminal answers when asked. `/status` prints each part of it with the reason.

| What | How it is settled, first match first |
| --- | --- |
| Colours | The setting; none when `NO_COLOR` is set to anything; else what the terminal's environment says it shows: all of them, 256, 16 or none |
| Glyphs | The setting; ASCII when `TERM` is empty, `dumb`, `linux`, `ansi` or a `vt` number, when the locale (`LC_ALL`, `LC_CTYPE`, `LANG`) names a charset that is not UTF-8, or when `RUNEWIDTH_EASTASIAN=1`; else Unicode |
| Background | The setting; what the terminal answers; else unknown |
| Pictures | See [Images](#images). The terminal must answer the kitty graphics question and name itself kitty or Ghostty: `COLORTERM` and the device attributes are never taken as evidence |
| Cell counting | By grapheme cluster where the terminal reports mode 2027; else by character |
| Cell size | What the terminal answers, at most 16 by 32 pixels, and at most 8 by 16 over SSH; 8 by 16 without an answer |
| `shift+enter` | Told from `enter` where the terminal reports key enhancements |

**Settings.** For each, the first that speaks wins: the flag, then the environment variable, then `chat.toml`.

| Flag | Variable | `chat.toml` | Values |
| --- | --- | --- | --- |
| `-color` | `CONTACTD_COLOR` | `color` | `auto`, `truecolor`, `256`, `16`, `none` |
| `-glyphs`, `-ascii` | `CONTACTD_GLYPHS` | `glyphs` | `auto`, `unicode`, `ascii` |
| `-theme` | `CONTACTD_THEME` | `theme` | `auto`, `dark`, `light` |
| `-images` | `CONTACTD_IMAGES` | `images` | `auto`, `pixels`, `blocks`, `text` |
| `-avatar` | `CONTACTD_AVATAR` | `show` under `[avatar]` | `auto`, `on`, `off` |
| `-mute` | `CONTACTD_MUTE` | `mute` under `[avatar]` | `true`, `false` |
| `-probe` | `CONTACTD_PROBE` | `probe` | `true`, `false` |
| `-mods` | `CONTACTD_MODS` | `mods` | `true`, `false` |
| `-config-dir` | `CONTACTD_CONFIG_DIR` | | a directory |

The config directory is `$CONTACTD_CONFIG_DIR`, else `$XDG_CONFIG_HOME/contactd`, else `~/.config/contactd`, on every system. It holds `chat.toml`, `statusline.toml`, `mods/` and `mods.toml`. Compiled mods are cached under `$XDG_CACHE_HOME/contactd`, or the system's cache directory, and can be deleted.

```toml
# chat.toml
color = "256"
theme = "dark"
images = "blocks"

[glyph]            # one symbol at a time, for a font that lacks it
tool = "●"
elbow = "└"
```

The default symbols `⏺ ⎿ ⏹` come from fallback fonts on most systems; `[glyph]` swaps any symbol but the half block for one of one to three cells. Overrides apply to the Unicode set only. A mistake in `chat.toml`, a bad value in a flag or a variable, or `-ascii` beside another `-glyphs` stops the client before the screen starts, naming the file and the line; an unknown key is only a warning. A glyph that cannot be drawn is refused with a note, once, at the start.

**The questions.** At the start the client asks the terminal for its background colour, and in one more write whether it has kitty graphics, its name and version, its cell size, and its device attributes, which every terminal answers and so mark the end. Nothing waits for the answers: keys are read and the screen is drawn at once, for the terminal the environment describes, and drawn again when an answer changes the look. An answer that comes more than two seconds after the start is not believed, except the background, the key enhancements and the cell counting, which are always taken. No question is asked later. Apple's Terminal is asked for its background only, and a terminal that calls itself `dumb`, `linux`, `ansi`, a `vt` number or `screen` is asked nothing. `-probe=false` sends none of the client's questions; the two mode queries and the keyboard query Bubble Tea writes at its own start still go out.

**A terminal that cannot place the cursor** (`TERM` empty or `dumb`) is refused, with a pointer at one-shot mode, unless `-color` is given.

**tmux.** Colours are what tmux says: with `terminal-features RGB` set, tmux is still seen as 256 colours, and `-color=truecolor` says otherwise. The background is unknown in tmux before 3.4, in mosh and in screen: the client then draws with a set of colours picked to be readable on both, and puts a rule above the status bar where it would have given it a background; `-theme` settles it. Pictures need `set -g allow-passthrough on`, since the question about them has to reach the terminal behind tmux and come back; without it they are half blocks, and the `/mods` screen and `/status` say why.

**SSH.** Everything works over SSH, since the client only reads and writes the terminal. Pictures are made for cells of at most 8 by 16 pixels there, to keep them small on the wire. On a slow link a terminal's answer can arrive in two pieces more than a few hundredths of a second apart, and the second piece is then read as keys and typed into the box; `-probe=false` avoids it. In the same way an arrow key split in two can be read as `esc` and stop a reply.

**A font or terminal that draws some symbols two cells wide** (East Asian ambiguous width) cannot be detected. `-glyphs=ascii` is the switch, and `RUNEWIDTH_EASTASIAN=1` selects it.

**Colours.** Each role's colour is picked by hand for each number of colours and each background, and the renderer is told that number, so nothing is brought down by rule on the way to the terminal. Body text is never coloured. `-` is the terminal's own foreground.

| Role | All colours, dark | All colours, light | 256: dark / light / unknown | 16: dark / light / unknown | None |
| --- | --- | --- | --- | --- | --- |
| accent (always bold) | `#5ec4c8` | `#0f6f77` | 80 / 30 / 30 | 6 / 4 / - | bold |
| tool | `#6ea8e6` | `#2a5cab` | 75 / 25 / 67 | 4 / 5 / - | - |
| dim | `#7f8a91` | `#636c74` | 244 / 242 / 244 | faint | faint |
| ok | `#7dc38f` | `#2c7341` | 114 / 28 / 65 | 2 / - / - | - |
| warn | `#d9b25c` | `#8a6100` | 179 / 94 / 100 | 3 / 5 bold / bold | bold |
| bad | `#e06c75` | `#b3261e` | 203 / 124 / 196 | 9 / 1 / 9 | bold |
| mod | `#c49ae0` | `#7b3fa6` | 176 / 91 / 134 | 5 / 5 / - | - |
| rule | `#2f373e` | `#cfcabb` | 238 / 250 / 244 | faint | - |
| box border | `#3a444c` | `#b9b4a4` | 240 / 248 / 244 | faint | - |
| bar background | `#1b2126` | `#ece9df` | 235 / 254 / none | none | none |
| code background | `#181d22` | `#eeebe2` | 235 / 254 / none | none | none |

A terminal with all colours and an unknown background gets the 256 unknown column. A test holds every text colour to a contrast of 3:1 against seven reference backgrounds, four dark and three light, and the two line colours to 1.2:1. At 16 colours the client never uses black, white, or their bright forms, nor yellow, cyan or green on a light background. Code is highlighted at 256 colours or more with a known background (chroma's `github-dark` or `github`), and drawn plain otherwise.

**With ASCII glyphs** everything the client draws of its own is ASCII, so a conversation in ASCII is a screen in ASCII: `>` for the prompt, `*` for a tool, `|_` for its output, `+ - |` for the box, `...` for the ellipsis. Replies are shown as written, since rendered Markdown would bring bullets, arrows and link sequences with it, and pictures are text.

## Where the client's code is

```text
internal/tui             the model, the screens, the probe's answers, and the wiring of everything below
internal/tui/termcaps    what a terminal can do, worked out from environment, settings and answers (pure)
internal/tui/theme       glyphs, colour roles, width, the box, the bar, the Markdown style
internal/tui/safe        outside text made safe for the screen
internal/tui/toml        the subset of TOML the client's files use, parsed and written
internal/tui/conf        the config and cache directories, chat.toml, atomic writes, the safety rule
internal/tui/scrollback  the transcript as a windowed list: only blocks in view are drawn
internal/tui/markdown    a reply cut into text, code and image parts
internal/tui/draw        transcript items to lines
internal/tui/frame       the row plan, the header, the hint line, the composition
internal/tui/inputbox    the boxed input: new-line keys, the dim text after a command, the cursor
internal/tui/commands    the command registry: parse, match, complete
internal/tui/palette     the list of commands
internal/tui/statusline  the status line: file, presets, layout, core values, the shell runner
internal/tui/sledit      the status line's editor
internal/tui/mods        the mod host: manifest, sandbox (wazero), budget, state, install
internal/tui/mods/wire   the JSON a mod is sent and answers with
internal/tui/mods/guest  the glue for a mod written in Go
internal/tui/modscreen   the mods screen
internal/tui/termimg     pictures: limits, scaling, half blocks, kitty bytes, the store
internal/tui/script      a reply read as a performance: what is said, what is shown, and never a word taken back
internal/tui/stage       the avatar's stage: the voice's pace, where an exhibit stands, the rows it is drawn in
internal/tui/avatar      the puppet: springs for every moving part, and a signed-distance shader that paints a pose
internal/tui/speech      text paced into speech: mouth shapes, beats, syllables and their melody
internal/tui/sound       the voice: a formant synth driven by the mouth, and the audio device
internal/tui/canvas      a framebuffer of block characters, two colours a cell
internal/tui/motion      the fixed-step clock and the springs
internal/tui/tuitest     golden files and an output recorder, for tests
internal/env/workdir     the server's read of a contact's working directory, for the files endpoint
examples/mods            cache, git, standup
docs/MODS.md             the mod manifest and ABI
```

The packages that hold rules and no screen (`safe`, `termcaps`, `toml`, `conf`, `scrollback`, `markdown`, `commands`, `statusline`, `mods`, `termimg`, `script`, `speech`, `motion`, `sound`, `canvas`, `avatar`) may not import Bubble Tea, Lip Gloss or anything else under `charm.land`. Only `mods` may import wazero, and only `sound` the audio library.

Only `probe.go` may import the terminal library under Bubble Tea, which is an unreleased version. `internal/api` reaches a contact's files through a port and never imports `internal/env`. Each is a `depguard` rule.

## Not supported

| What | Why |
| --- | --- |
| A turn number, in the header, a segment or a mod's data | The server's counter is in memory and restarts when a contact hibernates |
| iTerm2 inline images and sixel | The renderer drops the first, both need the screen forced on every scroll, and neither was seen on a terminal |
| Pictures from sub-agents and from tier-1 tools; a list of files on a tool result | A sub-agent's file is under the sub-agent's id and tier 1 has no file path. They show as `not found` |
| SVG, WebP, animated GIF | Two dependencies, and a decode that can take 768 MiB |
| Mods from a URL, a registry, signing, host functions, a mod SDK outside this repository | Each widens what a mod can reach |
| Holding `esc` for a moment to tell it from a split arrow key; holding keys while the terminal answers | It delays every stop, and the stray bytes are typed all the same |
| `/copy`, the mouse, search, a scrollbar | The client never asks for the mouse, so selecting text stays the terminal's |
| The avatar's eyes following the pointer | The mouse is never asked for. It looks at what it shows and at the draft |
| Pausing, replaying or rewinding the avatar's speech; older turns on the stage | `ctrl+o` shows the turn and `/avatar off` the thread |
| A size for a picture, or a place by row and column | A picture is as large as a thread shows it, 44 by 12 cells at most; nine places and "where there is room" |
| Showing a sub-agent's own log beside its parent's; editing a contact's agent config or profile | The settlement appears as a notice, and `contactd chat <id>` opens a sub-agent like any contact |
| Windows | Not examined. Shell segments report that they are not supported, and `statusline.toml` is never trusted with commands there |

## Known limits

- **The avatar reads Markdown its own way**, a line at a time, so that it can speak while a reply is still arriving and never take a word back. It is not the parser the thread uses. Where the two disagree a mark is said aloud that should not be, or a picture written as a reference (`![alt][ref]`) is said by its description and not shown. A table without a leading `|` is read out. `ctrl+o` shows what the thread makes of the turn.
- **A picture's number can differ between the stage and the thread** in a reply that mixes pictures the two read differently; the picture is then fetched once for each.
- **Text arrives faster than it is said**, so the avatar can be a good way behind a long reply; `esc` gives the rest at once. More than 32 KiB of one reply is not said.
- **The avatar animates while it is on screen.** At rest in a window of 100 by 30 it writes about 250 kB a second, measured through a real program on a pipe: its breathing changes the shade of most of its 640 cells in every frame. Over a slow link `-avatar=off` is the switch.
- **On Linux the audio library looks for PulseAudio and then ALSA when the client starts.** Where there is neither, ALSA may write a line to the terminal before the screen is up.
- **Picture ids are 8 bits and shared with every program in the same terminal.** The first is taken from the clock and each is given back on leaving, but two programs that place pictures this way can show each other's. Past 255 pictures in one conversation the one shown longest ago is dropped and sent again when next in view.
- **A resize that changes a picture's box sends the picture again**, from what the client kept, with no fetch. A terminal that reports larger cells than 8 by 16 after pictures were drawn costs one more fetch for each.
- **One very long word in the draft makes every key slow.** A word of 1 KiB with no space costs 12 ms a key, 4 KiB 150 ms, 8 KiB over half a second; pasting 64 KiB of it stops the client for about 20 seconds. Text with spaces is not affected. The cause is in the text area's wrapping.
- **A reply of tens of kilobytes of unclosed emphasis or link syntax takes up to a second to draw**, each time it is drawn. That is the Markdown parser's own cost.
- **Rows with emoji or East Asian text are written whole on every change**, by the renderer; a typed character otherwise costs the terminal at most 40 bytes and a tick of a running tool's time at most 120.
- **A status-line file that is empty is an empty bar**, with no note. Delete the file for the default.
- **`esc` followed at once by `ctrl+c` is read as one key** (`alt+ctrl+c`) and does nothing. A person does not type that fast; a script can.
- **Pictures left in the terminal.** The word to forget a conversation's pictures is lost when the client is left within about a sixtieth of a second of leaving that conversation, because the renderer does not flush what it was handed when it stops.
- **Two clients at once** each write the whole `mods.toml`; the last one wins. A module that changed is never run on a stale record: its hash is checked.
- **A mod's grants can be widened by editing its installed `mod.toml` by hand**; only an update treats a changed manifest as a reason to switch the mod off.
- **The mods screen says `5 ms` on every machine**, also where modules are interpreted (anything but amd64 and arm64) and the budget is 40 ms.
- **`/reload` does not see a mod copied into the config directory by hand**; the next start does, and `/mods install` at once. A mod that failed to load is tried again by `/mods on <name>`, not by `/reload`.
- **A shell segment's command that leaves a process behind with its output sent elsewhere** (`cmd >/dev/null &`) is not waited for and not stopped.
- **On a terminal of 5 rows the list of contacts shows one contact at a time**, and one long name among the contacts takes the route and the time away from every row under about 46 columns.
- **`contactd chat -new NAME -system ...` in a terminal that is then refused** has created the contact before it refuses.
- **A note can carry a whole path** when the error under it names one.

## How it is tested

- **The update loop**, with fakes for the server, the mod host and the shell: every command, the palette, notes, mods, pictures, the status line and its editor. Timers are written down, never waited for.
- **Golden screens**, as files of the stripped screen: a conversation, the list of commands and the mods screen at 100 by 30, and four kinds of terminal at 46 and 30 columns by 12 rows.
- **Every screen at every size**: the list, both forms, a conversation, a conversation with the list of commands, the editor and the mods screen, at 100, 60, 40, 30, 20 and 10 columns by 30, 16, 12, 8 and 5 rows, for a terminal with all colours on a dark background, one with 16 on a light one, one with 256 and an unknown one, and one with no colour and ASCII. No row is wider than the terminal by either way of counting cells, and with ASCII glyphs every byte is ASCII.
- **Counts**, with 10,000 messages loaded: a key draws no block and does not rebuild the list, a batch of a streaming reply draws no settled block, a resize draws only the blocks in view, and the list of commands or a second draft line encodes no picture and writes nothing beside the screen.
- **The wire**, with a real program on a pipe: no mouse mode is ever switched on, a message that changes nothing writes nothing, a typed character writes at most 40 bytes (13 measured, 30 for the first of a draft), a tick of a running tool at most 120 (82 measured), a picture is sent once and before the cells that show it, and the deadline for the terminal's answers writes nothing.
- **The renderer**, pinned so that an update of it fails a test: a picture's cell with its two marks is one cell of width 1, a colour given as one of the 256 is written as that number at 256 colours and at all of them, what is written beside the screen comes before the frame of the same turn, and a string sequence that never ends is dropped.
- **The avatar**, with time handed in: a reply is said at a speaker's pace and never unsaid, a turn from the log is not said, `esc` and a message stop it, an exhibit is shown when the words reach it and stands at each of the nine places with the avatar whole beside it, the stage has the terminal's rows and width at every size from 140 by 40 down to 1 by 1, nothing a reply holds reaches the terminal as more than text, and under a real program a frame in which nothing moved writes nothing. The reader of replies is fuzzed for the one thing the voice depends on: what it says of a text that only grew begins with what it said before. The avatar's own picture has no golden file, because its shading need not round alike on every machine.
