# contactd

One Go binary, backed by SQLite and two small local ONNX models, that lets a client talk to an AI contact the way it would talk to a person. There is one continuous relationship per contact. A client sends messages to a contact ID; it never creates, picks or names a thread.

Threads, prompt-cache layout and the REPL environment are not stored objects. Each turn derives them just in time from one append-only event log.

The original design is in [docs/DESIGN.md](docs/DESIGN.md). This README covers running it, the provider core and the terminal client that were added afterwards, what was measured, and where the implementation departs from the design.

## Quick start

```sh
mise install            # Go, golangci-lint, sqlc, sqlfluff, uv at pinned versions
mise run test           # unit tests; needs a C compiler for SQLite only

# Run with a scripted model and lexical recall: no API key, no ONNX Runtime.
go run ./cmd/contactd -llm=fake -infer=fake -data-dir=./data -wasm-dir=
```

Talk to it:

```sh
curl -s -XPOST localhost:8080/v1/contacts \
  -d '{"agent":{"system":"You are Ada, a thoughtful colleague."},"profile":"Prefers short answers."}'
# {"id":1}

curl -sN localhost:8080/v1/contacts/1/stream &          # SSE: typing, text deltas, tool activity, the committed reply
curl -s -XPOST localhost:8080/v1/contacts/1/messages -d '{"text":"hey, are you around?"}'
# {"seq":1}   <- 202 once the message is durable
```

### From the terminal

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

See [The terminal client](#the-terminal-client) for the screen, the keys and commands, the status line, mods, images, and what is done for which terminal.

### Local development

Run this from the repo:

```sh
# Server logs stay here; chat and diagnostics open in two new windows.
# Uses your configured model provider and lightweight lexical recall.
mise run dev
```

In the chat window, select an existing contact or press `n` to create one.
The picker waits for the server to be ready before loading. Closing either
client leaves the server running; Ctrl+C in the server terminal stops both
clients and closes their spawned Ghostty, macOS Terminal, or tmux windows.

You can also open a chat manually:

```sh
mise run chat ada       # reopen a contact
mise run chat          # open the picker
mise run chat:new ada   # create a contact and start chatting
```

The tasks use `./data` for the server and `./data/chat-config` for the client's
settings and mods, both ignored by Git. `CONTACTD_DATA_DIR` and
`CONTACTD_CONFIG_DIR` override those paths. Stop a process with `ctrl+c` and
rerun its task after changing code; each launch rebuilds it.

Extra arguments are passed through to the server or client. The dev server
honors `CONTACTD_LLM` and `CONTACTD_MODEL`, or selects the first provider with
configured credentials (for example, `DEEPSEEK_API_KEY`). You can select one
explicitly with `mise run dev -llm=deepseek`. For an offline demo with scripted
replies and no API key, use `mise run dev -llm=fake`.
Recall uses the lightweight lexical backend; the full ONNX setup is below.
To customize a new contact:

```sh
mise run chat:new grace -system="You are Grace, a concise coding colleague." -tools=python,js
```

`mise run test` runs the race-enabled unit suite, `mise run lint` checks style
and static analysis, and `mise run ci` runs every CI gate. `mise tasks` lists
all available tasks.

#### Live context diagnostics

`mise run dev` enables `/debug/plans` and opens two client windows: chat and
`ctxtop`. In Ghostty on macOS both are Ghostty windows; otherwise macOS uses
Terminal. Under tmux they are tmux windows, and on Linux the launcher tries
Ghostty or `x-terminal-emulator`. `-diagnostics=false` opens only chat.

To open or reopen the viewer yourself:

```sh
mise run ctxtop                         # every contact, over the dev socket
mise run ctxtop -contact=ada            # one contact by name or ID
mise run ctxtop -url=http://127.0.0.1:8080
CONTACTD_DEV_VIEWER=off mise run dev    # server only; diagnostics still enabled
```

The header says which call is shown, how full its context window is, and how
much of the prompt the provider reused from cache against what the planner
expected. The bar under it draws the same thing, with its legend. Each of the
six views opens with the question it answers, and the last line lists the keys
that work in it:

| Key | View |
| --- | --- |
| `1` | **Prompt**: what the call sent, tier by tier, with each part's size, share and cache reuse, a marker where the cache ends, and the selected part in full |
| `2` | **Log**: the contact's episodes, what this prompt took from each, and their messages |
| `3` | **Changes**: which tiers held since an earlier call of the same contact and purpose, where the first change is, what it did to the cache, and the bytes at that point |
| `4` | **Recall**: the search text, the outcome, and each candidate's similarity, rerank and final scores with the decision in words |
| `5` | **Checkpoints**: the tail against its budget, and each checkpoint beside the log events it replaced |
| `6` | **Timeline**: prompt size by tier, cache share and events, one column per retained call, plus inherited context for forks |

Use up/down to select, Enter to fold a tier or open the selection full width,
left/right to move between calls, and `f` to follow the latest; the header says
when the view is paused on an older call. `g` jumps from a part to its episode
in Log; `r` shows the exact provider request body. `s` sorts Prompt by size, `/`
filters the view, `z` scales the bar to the window or to the prompt, and Page
Up/Down scrolls content. In Changes, `[`/`]` picks the call to compare with. In
Checkpoints, `m` marks numbers and paths absent from the summary (a heuristic,
not a quality score). Tab/Shift+Tab cycles contacts; `?` shows the keys and a
short glossary; `q` closes the viewer.

The bar uses the planner's token estimates. Provider token counts come from a
different tokenizer, so the reported marker is projected by its cached share;
the comparison is in percentage points, not a subtraction of unlike token
counts. A gap over five percentage points is highlighted for investigation.
It is not by itself proof of a cache bug. Missing usage stays unknown. Per-block
cache hits and the break location are estimates; only aggregate usage is reported.
Canonical block bytes are the planner's hashing representation. They are distinct
from the raw provider JSON body, which is captured before sending and excludes
HTTP headers. The provider body can include replayed reasoning.

Diagnostics are off outside the dev task unless `-diagnostics` (or
`CONTACTD_DIAGNOSTICS=true`) is set. `/debug/plans` replays up to 500 calls and
streams planning, bounded progress updates and completion. `/debug/plans/{run}/{call}`
serves details on demand; its `/events` endpoint pages through source events up
to the log head captured for that call. Captures are in memory only, limited to
2 MiB per call and 32 MiB overall, with explicit truncation/eviction. Slow stream
readers are disconnected and can replay retained state.

**Diagnostic mode exposes prompt previews, full prompt/tool contents and source
log messages through the same socket and network access as the API.** Use it only
where that access is appropriate. It does not capture authentication headers.

Some precision still depends on runtime instrumentation: in-turn tool entries can
have an unknown log sequence until the next plan; recalls currently appear as one
combined system-message block. Log/fold previews load at most 2,000 events per
selected span; search covers loaded data. Output counts while streaming are byte
estimates and switch to provider usage on completion. Monetary overlays remain
unavailable until per-route prices are supplied; the panel does not invent them.

### The real thing

```sh
mise run deps:native    # libonnxruntime + libtokenizers.a into third_party/lib, checksum-verified
mise run deps:wasm      # CPython 3.12 and QuickJS WASI builds into third_party/wasm
mise run models:fetch   # export + int8-quantize bge-small-en-v1.5 and ms-marco-MiniLM-L6-v2 into models/
mise run build          # bin/contactd, built with -tags ORT

DEEPSEEK_API_KEY=... GOMEMLIMIT=3GiB bin/contactd \
  -data-dir=./data -models-dir=./models -wasm-dir=./third_party/wasm
```

Any provider whose key is in the environment is available; see [Model providers](#model-providers).

Or as a container (`mise run image`), with one volume at `/data`. `GOMEMLIMIT` is not cgroup-aware: set it to the container memory limit minus room for ONNX Runtime's native memory.

## API

There are no thread endpoints; that's the point.

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/v1/contacts` | Create a contact from an agent config (below), an optional `name` and an optional `profile`. An unknown provider, model or effort is a 400 here, and a name another contact has is a 409 |
| GET | `/v1/contacts` | The contacts a person talks to, most recently active first; sub-agents are left out. `?name=` finds one by its exact name |
| GET | `/v1/contacts/{id}` | One contact: name, agent config, profile, and the route that serves it with the deployment's defaults filled in |
| PATCH | `/v1/contacts/{id}` | `{"name": ...}` renames; an empty name removes it |
| POST | `/v1/contacts/{id}/messages` | Send `{"text": ...}` with optional `session_id`, `time_zone` and `presentation`; returns 202 with the durable `seq` |
| POST | `/v1/contacts/{id}/interrupt` | Stop the reply in flight; 202 |
| GET | `/v1/contacts/{id}/stream` | SSE: `ready`, `user`, `typing`, `text`, `tool_start`, `tool_call`, `tool_result`, `message`, `interrupted`, `notice`, `error` |
| GET | `/v1/contacts/{id}/live` | WebSocket: send an optional `hello` (see below), then `{"type":"message","text":...}`, `{"type":"typing"}` and `{"type":"interrupt"}`; receive the same deltas, and for each message one `ack` or one `error` frame, in the order sent |
| GET | `/v1/contacts/{id}/events` | Page through the log in log order: `?after={seq}` forward, `?before={seq}` backward, `?tail=1` backward from the end, each with `limit`. `?kinds=user_msg,agent_msg` keeps only those kinds; the others are `tool_call`, `tool_result`, `attempt`, `checkpoint`, `notice`, `summary` and `env_snapshot` |
| GET | `/v1/contacts/{id}/files/{path...}` | One file from the contact's tier-0 working directory, for a client that shows what a tool wrote. At most 8 MiB (413 beyond). PNG, JPEG, GIF, WebP and BMP are served as themselves, by their first bytes; anything else as `application/octet-stream` with `Content-Disposition: attachment`. A file is sent with an `ETag`, and `If-None-Match` gives 304. There is no listing, and a path that leaves the directory, by name or through a symbolic link, is a 404 like a file that is not there |
| GET | `/metrics`, `/healthz` | Prometheus metrics; liveness (503 while draining) |

A client may introduce a visit with `{"type":"hello","session_id":"a-client-generated-id","time_zone":"America/Chicago","presentation":"avatar"}`. `presentation` is `avatar` or `text`; `time_zone` is an IANA name. Invalid metadata is rejected. The TUI mints an ID for each open contact and resends it on reconnect. Opening and closing without speaking leaves no log entry and triggers no greeting.

The first accepted message of a visit logs a `session-start` notice immediately before the message. It records the person's local date and time, the gap since the last exchange, and the presentation mode. A first meeting is described as such. These notices are model memory, hidden in the TUI and excluded from recall queries. Reusing an ID does not create another notice, including after hibernation or a daemon restart. Clients without an ID start a new visit after 30 minutes without an exchange. The last supplied zone persists; until one is supplied, time is UTC. The daemon embeds time-zone data, including daylight-saving rules.

Every contact's own prompt follows a fixed server preamble: the character treats prior events as memories and runtime notices as surroundings. Profile, recall, and new summaries use that perspective. Existing summaries are left intact. The preamble also applies to sub-agents, whose task instructions still describe their assignment. Rolling this out changes each contact's static cache prefix once.

The same API is served on a Unix socket, `contactd.sock` in the data directory, with permissions `0600`. Nothing else about the API is authenticated: the TCP address is for a trusted network, and the socket is for the user who runs the server.

**Reading a contact without missing anything.** A stream runs ahead of the log's group commit, and the log is the only place the past is. A client that wants both opens the stream first. The first delta is always `ready`, and its `seq` is the last sequence number assigned before the stream began (zero when the contact was not in memory). Everything up to there is read from `/events`; everything after it arrives on the stream. Deltas that announce an event carry its `seq`, so reading an event both ways is harmless. A stream opened mid-reply is then sent `typing` and one `text` delta with what has been said so far.

A contact's **name** is what a person calls it. It is optional, unique where given, and never a number. It is stored beside the agent config, not in it, so naming a contact changes no byte of its prompt.

An agent config:

| Field | Meaning |
| --- | --- |
| `system` | The contact’s own system prompt, after the fixed server preamble |
| `provider`, `model` | The route that serves this contact. Either may be omitted to take the deployment's default. Names resolve strictly: a model the provider does not serve is an error, never a substitute |
| `effort` | Reasoning effort on one scale for every provider: `none`, `low`, `medium`, `high`, `xhigh`, `max`. The provider sends the nearest value the model accepts, and the log records what was sent |
| `max_tokens` | Output cap per model call |
| `tools` | Code-execution tools (below) |
| `delegate` | `true` gives the agent the built-in `delegate` tool, which starts sub-agents |

A tool is a code-execution tool: it takes one `code` argument and names the environment it runs in.

```json
{"name": "python", "description": "Run Python 3.12. Files persist; variables may not.",
 "env": {"tier": 0, "base": "python-3.12"}}
```

Tier 0 runs in-process under wazero (`python-3.12`, `quickjs`). Tier 1 is a thin client to an external snapshot-sandbox service (`-sandbox-url`).

### Sub-agents

With `delegate` on, the model can call `delegate({"task": ..., "mode": "spawn" | "fork"})`. The call returns at once with the sub-agent's ID; the model cannot wait for it or poll it. A sub-agent is a contact of its own, with its own log (readable at `/v1/contacts/{child}/events`) and its parent's instructions, tools and route.

- **`spawn`** starts it from the instructions and the task.
- **`fork`** also gives it the parent's conversation up to the parent's last finished reply. Its first request begins with the exact bytes the parent last sent, so it reads the parent's prompt cache.
- **Settlement** arrives in the parent's log as a `notice` event, and starts a turn so the agent can act on it. It never interrupts a reply in progress, and a sub-agent that fails settles the same way: as a notice, with a diagnostic of at most 4,096 bytes.
- **A settlement survives a restart.** The parent's log records each sub-agent it started and each one it has heard from. A parent that wakes with one still open asks it to report again from its own log; a sub-agent that was cut off mid-task reports that it failed.
- **Limits:** one level deep, and `-max-children` (8) running at once per contact. Interrupting the parent does not stop its sub-agents, and an interrupt sent to a sub-agent is ignored: it has a task to settle.

## Configuration

Every setting is a flag and an environment variable (`CONTACTD_` + the flag name in upper snake case); flags win. `contactd -h` lists them all. The ones that matter first:

| Flag | Default | Notes |
| --- | --- | --- |
| `-socket` | `contactd.sock` in the data directory | The Unix socket the API is also served on, for `contactd chat`. `off` disables it |
| `-llm` | first configured | The default provider: `deepseek`, `anthropic`, `openrouter`, `opencode`, `ollama`, `openai` or `fake`. Every provider with credentials is available to agents that name it |
| `-model` | the provider's own | Default model on the default provider. OpenRouter has no default of its own and needs this |
| `-effort`, `-max-tokens` | none | Defaults for agents that set none |
| `-summary-provider`, `-summary-model` | the default route | Where lazy episode summaries are written; point it at something small |
| `-provider-limits` | DeepSeek's documented limits | Concurrent model calls per provider or route: `deepseek/deepseek-v4-pro=500,openrouter=64` |
| `-checkpoint` | on | Fold the oldest part of an over-budget tail into a summary instead of cutting it |
| `-max-children` | 8 | Sub-agents one contact may have running |
| `-infer` | `ort` | `fake` swaps in a deterministic bag-of-words embedder and lexical reranker |
| `-model-variant` | `int8` | or `fp32` |
| `-sync-full` | off | `synchronous=FULL`: never lose an acknowledged message on power loss |
| `-max-hydrated` | derived | From `GOMEMLIMIT` ÷ 2 ÷ `-bytes-per-actor` |
| `-tau`, `-gap`, `-theta`, `-delta`, `-tail-budget` | doc values | Tune with `cmd/replay` before changing them |
| `-no-refusal-fallback` | off | See "Anthropic specifics" below |

`-cache-ttl` is gone: how long a prompt cache stays warm is declared by each model route, not set for the deployment.

## Model providers

The core reaches every provider through one typed contract (`internal/llm`) and acts only on what a model route declares it can do: how its cache is driven and for how long, where a pinning key goes, which reasoning efforts it accepts, whether reasoning must be sent back, whether it can be held to one tool call per step. The core never names a provider; `depguard` enforces the boundary from both sides.

| Provider (`-llm`) | Credentials | Wire | Cache | Effort | Reasoning replay |
| --- | --- | --- | --- | --- | --- |
| `deepseek` | `DEEPSEEK_API_KEY` | Messages at `api.deepseek.com/anthropic` by default; `-deepseek-wire=chat` for Chat Completions | Automatic by prefix, assumed warm 1 hour | `low`, `high`, `max`, or off | Every turn |
| `anthropic` | `ANTHROPIC_API_KEY` | Messages | Breakpoints, 5 minutes | `low` to `max` | Within a turn |
| `openrouter` | `OPENROUTER_API_KEY` | Chat Completions | Per model, from the catalog's prices; pinned with `session_id` | Per model, from the catalog | Every turn where the model reasons |
| `opencode` | `OPENCODE_API_KEY` | Per model: Chat Completions or Messages (Responses-only models are refused) | Automatic, or breakpoints where a cached-write price is listed; pinned with `x-opencode-session` | None declared | Every turn for DeepSeek models, within a turn for Claude |
| `ollama` | `OLLAMA_API_KEY` | Chat Completions at `ollama.com/v1` | Automatic by prefix; reports cache reads; retention unknown | Per model, from `/api/show` | None declared |
| `openai` | `OPENAI_API_KEY` and/or `-openai-base-urls`, plus `-openai-models` | Chat Completions | Automatic by prefix per replica | None | None |

Each profile's source file states where every declared value comes from and what is a guess. OpenRouter, OpenCode Go and Ollama Cloud read their model catalogs once at start-up; `-openrouter-catalog` starts from a saved copy instead.

Native DeepSeek routes automatically offer `web_search` and `web_fetch`, using the same `DEEPSEEK_API_KEY` on both the Messages and Chat Completions wires. No contact configuration is needed. `web_search` accepts `query` (1–128 characters), plus optional `count` (1–50, default 10), `ori_question`, `localization`, and `need_content`; it returns ranked sources with URLs and summaries. `web_fetch` accepts an HTTP(S) `url` and returns the page title and Markdown content. See DeepSeek's [Search](https://api-docs.deepseek.com/api/search), [Fetch](https://api-docs.deepseek.com/api/fetch), and [billing and rate limits](https://api-docs.deepseek.com/guides/search) documentation.

These are ordinary logged tool calls: results enter the conversation within its tool-output budget, transient API errors are retried, and other failures are returned to the model as tool errors. Calls have a one-minute timeout per attempt. They are available only on native DeepSeek routes, not DeepSeek models served through a gateway. An explicitly configured tool with the same name keeps its own definition and runtime. A DeepSeek base URL override also controls the web API host; `/anthropic` or `/v1` at the end is removed before appending `/v1/tools/search` or `/v1/tools/fetch`.

Ollama's [cache accounting](https://docs.ollama.com/api/chat) is exposed by its [OpenAI-compatible endpoint](https://docs.ollama.com/api/openai-compatibility) as `usage.prompt_tokens_details.cached_tokens` ([upstream implementation](https://github.com/ollama/ollama/pull/17943)). We request streaming usage and keep the full input count, cache reads, and raw usage object. An explicit zero means a cold cache; an omitted, null, or invalid count remains unknown and is excluded from cache metrics. Attempts persist that distinction, `ctxtop` reports it, and the TUI only shows a cache percentage when known. Historical attempts with positive cache counts remain readable; historical zeros are ambiguous. Ollama's cache lifetime is undocumented, so we record actual hits without assuming a warm prefix between calls.

### Probing a route

A capability is a claim, and `contactd probe` checks the claims against the live endpoint:

```sh
DEEPSEEK_API_KEY=... bin/contactd probe -provider deepseek -model deepseek-flash
```

It sends about ten small requests (one of them a 20,000-token prompt, twice, to see what is reported as cached) and prints a table of declared against observed. It exits 1 when the endpoint contradicts the profile. Run it when adding a model, and when a provider changes something.

What the probe found on Oct 11, 2026, with real keys:

| Route | Result |
| --- | --- |
| `deepseek/deepseek-flash` and `deepseek-v4-pro`, Messages wire | Every check passed. Cache hits are reported in the Anthropic-style usage fields (21,504 of 21,692 tokens on a repeated prompt). Two tool calls came back in one step. All four effort settings were accepted |
| `deepseek/deepseek-flash`, Chat Completions wire | Every check passed. The cache is shared with the Messages endpoint. This wire also reports reasoning tokens, which the Messages wire does not |
| `openrouter/deepseek/deepseek-v4.1-flash` | Every check passed, including `reasoning_details` going back unmodified |
| `opencode/deepseek-v4.1-flash` (Chat Completions) | Passed after two corrections: the gateway refuses any request without `x-opencode-session` (HTTP 400 `MissingSessionID`), and it does report cached tokens |
| `opencode/qwen3.8-flash` (Messages) | The Messages route answers and reports breakpoint cache hits. The model did not call the tool when asked, so the tool checks did not run |

On every DeepSeek route, a tool loop that left the model's reasoning out was still accepted, though DeepSeek's documentation says the Chat Completions endpoint rejects it. The profiles keep replaying reasoning on every turn, which is the documented contract and costs little when it is read from cache.

A focused Ollama Cloud cache probe on Oct 13, 2026, using `glm-5.3-flash`, reported 0 of 1,929 input tokens cached on the first request and 1,792 (92.9%) on an identical second request. This confirmed cache reporting, not a retention guarantee or a full capability probe.

Not fully probed: `anthropic`, `ollama`, and every OpenRouter and OpenCode Go model other than the ones above.

## How it is laid out

Ports and adapters, one module. Domain packages hold the logic and declare the small interfaces they need; adapters satisfy them; only `cmd/contactd` sees both. `depguard` turns a layering violation into a lint failure.

```text
cmd/contactd      wiring, signals, graceful shutdown
cmd/replay        cache simulator + recall benchmark over testdata/transcripts
cmd/fakellm       fake model server (OpenAI wire format) for load tests
internal/core     ContactID, Event, bodies, Message, sentinel errors
internal/actor    Pieces 2 + 7: actor loop, registry, debounce, interruption, hibernation; sub-agents, checkpoints
internal/turn     Piece 8: the turn loop, the environment handle, the delegate tool; declares Models and EnvLeaser
internal/recall   Piece 4: episodes, segmentation, two-stage recall; declares Embedder and Reranker
internal/plan     Piece 5: tiers, canonical bytes, hash chain, hysteresis, compaction, checkpoints, forks (pure)
internal/eventlog Piece 1: SQLite store, group-commit writer, migrations (sqlcgen/ is generated)
internal/infer    Piece 3: micro-batcher; hugot/ (ONNX Runtime, tag ORT), fake/
internal/llm      the provider contract, and the client: routing, admission, retries, attempt records
  wire/           chat/ and messages/: a plan becomes bytes, a stream becomes events
  provider/       one profile per provider: deepseek, anthropic, openrouter, opencodego, ollamacloud, openaicompat
  conform/        the conformance suite every profile runs
  probe/          the live capability check behind `contactd probe`
  fake/           the scripted provider and its HTTP server
internal/env      Piece 6: wasm/ (tier 0, wazero), remote/ (tier 1), workdir/ (a contact's files, read for the API)
internal/api      HTTP, SSE, WebSocket; apitest/ runs it for tests of its clients
internal/client   the API from the other side: requests, one WebSocket, a session that reconnects, a contact's files
internal/transcript  log events + live deltas -> one conversation (pure)
internal/tui      the screens of `contactd chat` (Bubble Tea), and under it one package for each part of them
internal/e2e      integration tier (tag integration)
examples/mods     three mods for the terminal client, as source
```

The client's own packages are listed under [Where the client's code is](#where-the-clients-code-is).

## The terminal client

`contactd chat` is a subcommand of the same binary, so the client and the server it talks to cannot disagree about the protocol. It is built in three layers, and `depguard` keeps each in its place.

- **`internal/client`** knows the API and nothing behind it. It never opens the data directory: a contact's sequence numbers are assigned in memory by the one actor that owns it, so a second process writing the log would break it. Its `Session` keeps a WebSocket open, reconnects with a growing delay, and reads the log around the stream using the `ready` protocol above, so its caller sees one ordered sequence of updates. After each turn it reads the log once more, which is where the record of the model call comes from.
- **`internal/transcript`** is a pure reducer: events and deltas in, a conversation out. The same event may arrive from the log and from the stream, in either order; the sequence number makes that harmless. It imports no clock and does no I/O, so what a view shows in any situation is a unit test.
- **`internal/tui`** draws the reducer's state and reads keys. It keeps nothing about a contact between runs: contacts, names and messages live on the server. What it keeps is its own: `chat.toml`, `statusline.toml` and the installed mods, in one config directory.

What follows from the platform's shape:

- **There is nothing to pick but a contact.** Each contact keeps one log; visits do not split it into separate conversations. The avatar opens idle, with no previous question or reply on the stage, ready for whatever you want to talk about. The conversation remains in the assistant's context; `ctrl+o` shows the latest turn when you want to read it. Once you start talking, the stage holds the turn in flight, or the last one from this visit. In a thread, earlier history loads a page at a time on scrolling up, and a rule marks each new day.
- **The input never locks.** A message sent mid-reply interrupts it. `esc` interrupts without saying anything: the reply so far is kept and flagged, a running tool finishes first, and the message that was being answered stays unanswered until the next one.
- **A message is shown as sending until the server says it is durable.** One the server refuses, or one that was in flight when the connection dropped and is not in the log afterwards, is put back in the input box.
- **The status bar shows the cache working**: the share of the last prompt that the provider reported as cached, the tokens, and how long the call took.
- **Text from outside is never trusted with the terminal.** The escape character, the other control characters and the bidirectional overrides are taken out of everything a model, a tool, a contact's name, a mod, a status-line command or a pasted draft can put on screen. A test hands one of every kind of terminal sequence to every way in and holds every screen to its own colours and links.

### The screen

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

### The avatar

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
voice = "chirp"   # chirp | pip | mellow | retro | deep              /avatar voice <name>
rate = 38         # characters a second, 8 to 240                    /avatar rate <n>
cells = "auto"    # auto | quadrant | octant                         /avatar cells <kind>
mute = false      #                          -mute, CONTACTD_MUTE,   /avatar mute|unmute
```

- **Cells.** A cell is 2 by 2 pixels (quadrants), which every terminal draws. Octants are 2 by 4, square pixels and twice the detail, but only a terminal that draws block characters itself has them; `auto` uses them in Ghostty.
- **Sound.** The voice is opened with the client when the avatar may be shown, also when it starts muted, and never over SSH, where it would sound at the far end. With no audio device the avatar is silent and `/avatar` says so.
- **An idle avatar is not an idle client.** It breathes and blinks, at 30 frames a second, 60 while it speaks, and each frame rewrites the cells that changed: see [Known limits](#known-limits). A thread still writes nothing when nothing happens.

### Keys

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

### Commands

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

### The status line

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

### Mods

A mod is a small WebAssembly module that adds something to the client. [docs/MODS.md](docs/MODS.md) is the reference for its manifest and for the contract between the client and a mod.

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

### Images

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

### What the terminal can do

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

### Where the client's code is

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

Five of the avatar's seven packages came from a prototype of it alone, `bubble-claude`: `avatar`, `canvas`, `motion`, `speech` and `sound` are here nearly as they were. What changed is that the avatar's chance is its own and seeded, and that the voice can be handed a whole text, skipped to its end and reset. `stage` does here what the prototype's own program did, and `script` is new. Only `probe.go` may import the terminal library under Bubble Tea, which is an unreleased version. `internal/api` reaches a contact's files through a port and never imports `internal/env`. Each is a `depguard` rule.

### Where the build differs from the drawings

The screens were drawn first, as mock-ups. Each has a golden file of its 100 by 30 screen, and these are the places where the build knowingly differs.

| Drawing | Build |
| --- | --- |
| Header `turn 8 · 7 Oct – 12 Oct` | `7 Oct – 12 Oct`. The server's turn counter restarts when a contact hibernates, so no turn number is shown anywhere |
| Hint `alt+enter new line` | `ctrl+j new line`, or `shift+enter new line` where the terminal can tell it from `enter`. `alt+enter` works, and is the least reliable of the three |
| The Frame transcript has no day rules | A rule for each day in view, which is what the header's `7 Oct – 12 Oct` counts. The 23 transcript rows of that screen are full where the drawing leaves four empty |
| The code block has one blue word | chroma's `github-dark` highlighting |
| "kitty graphics, iTerm2 inline, sixel"; "the file's own pixels"; a 44 by 9 box; "18 kB" | kitty placeholders only, in kitty and Ghostty; the client's own re-encoded pixels; 44 by 12 at most, fitted to the picture's shape; the file's real size, about 692 kB for an uncompressed 640 by 360 PNG |
| The editor's 60-column preview drops route and latency | They stay, because they fit. A rule that dropped them would drop latency from the 46-column status bar, which the Compatibility drawing shows |
| The editor's key line | The drawn keys and `a add · d delete · p preset`, on their own row |
| The mods list shows `images`, `mermaid` and a `panel` row | Only installed mods. Images are built in; there are no panels |
| A core `cache` segment and a `cache` mod segment that read the same | The mod's segment is the average over recent calls: `cache avg 61%` |
| The Commands status bar has a `⎇ main` cell | Only with the `git` mod on |
| The narrow Compatibility panels leave their spare transcript row at the bottom | It is at the top: a conversation longer than the view is drawn from its newest line up |
| At 30 columns the question and the reply break a word earlier | They break where the text stops fitting |
| The no-colour panel has no bold and an underscore for a cursor | The name and the prompt are bold, the route faint, and the cursor is the terminal's own |

### Not built

| What | Why |
| --- | --- |
| A turn number, in the header, a segment or a mod's data | The server's counter is in memory and restarts when a contact hibernates |
| Panels, a side dock | Another direction than the approved screen |
| iTerm2 inline images and sixel | The renderer drops the first, both need the screen forced on every scroll, and neither was seen on a terminal |
| Pictures from sub-agents and from tier-1 tools; a list of files on a tool result | A sub-agent's file is under the sub-agent's id and tier 1 has no file path. They show as `not found` |
| SVG, WebP, animated GIF | Two dependencies, and a decode that can take 768 MiB |
| Mods from a URL, a registry, signing, host functions, a mod SDK outside this repository | Each widens what a mod can reach |
| Holding `esc` for a moment to tell it from a split arrow key; holding keys while the terminal answers | It delays every stop, and the stray bytes are typed all the same |
| `/copy`, the mouse, search, a scrollbar | Outside the approved screens |
| The avatar's eyes following the pointer, as in its prototype | The mouse is never asked for. It looks at what it shows and at the draft |
| Pausing, replaying or rewinding the avatar's speech; older turns on the stage | `ctrl+o` shows the turn and `/avatar off` the thread |
| A size for a picture, or a place by row and column | A picture is as large as a thread shows it, 44 by 12 cells at most; nine places and "where there is room" |
| Showing a sub-agent's own log beside its parent's; editing a contact's agent config or profile | The settlement appears as a notice, and `contactd chat <id>` opens a sub-agent like any contact |
| Windows | Not examined. Shell segments report that they are not supported, and `statusline.toml` is never trusted with commands there |

### Known limits

- **Nothing here was run on a real terminal while it was built.** The client was driven through its update loop and through a real Bubble Tea program on a pipe, with what it writes read back byte for byte. That a picture shows in kitty and Ghostty, alone and through tmux, and that the picked colours read well on real themes, is still to be looked at by a person.
- **The avatar was never seen or heard while it was built.** It was run for real, in tmux against the scripted model, and its screen read back as text: the layout, the pacing, a picture placed by a reply, the sheet and `/avatar`. Its colours on real themes, octants in Ghostty, a kitty picture beside it and the sound of its voice are still to be looked at and listened to by a person.
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

### How it is tested

- **The update loop**, with fakes for the server, the mod host and the shell: every command, the palette, notes, mods, pictures, the status line and its editor. Timers are written down, never waited for.
- **The mock-ups**, as golden files of the stripped screen: Frame, Commands and Mods at 100 by 30, and the four Compatibility panels at 46 and 30 columns by 12 rows.
- **Every screen at every size**: the list, both forms, a conversation, a conversation with the list of commands, the editor and the mods screen, at 100, 60, 40, 30, 20 and 10 columns by 30, 16, 12, 8 and 5 rows, for a terminal with all colours on a dark background, one with 16 on a light one, one with 256 and an unknown one, and one with no colour and ASCII. No row is wider than the terminal by either way of counting cells, and with ASCII glyphs every byte is ASCII.
- **Counts**, with 10,000 messages loaded: a key draws no block and does not rebuild the list, a batch of a streaming reply draws no settled block, a resize draws only the blocks in view, and the list of commands or a second draft line encodes no picture and writes nothing beside the screen.
- **The wire**, with a real program on a pipe: no mouse mode is ever switched on, a message that changes nothing writes nothing, a typed character writes at most 40 bytes (13 measured, 30 for the first of a draft), a tick of a running tool at most 120 (82 measured), a picture is sent once and before the cells that show it, and the deadline for the terminal's answers writes nothing.
- **The renderer**, pinned so that an update of it fails a test: a picture's cell with its two marks is one cell of width 1, a colour given as one of the 256 is written as that number at 256 colours and at all of them, what is written beside the screen comes before the frame of the same turn, and a string sequence that never ends is dropped.
- **The avatar**, with time handed in: a reply is said at a speaker's pace and never unsaid, a turn from the log is not said, `esc` and a message stop it, an exhibit is shown when the words reach it and stands at each of the nine places with the avatar whole beside it, the stage has the terminal's rows and width at every size from 140 by 40 down to 1 by 1, nothing a reply holds reaches the terminal as more than text, and under a real program a frame in which nothing moved writes nothing. The reader of replies is fuzzed for the one thing the voice depends on: what it says of a text that only grew begins with what it said before. The avatar's own picture has no golden file, because its shading need not round alike on every machine.
- **Faults**, put into the client one at a time through `go test -overlay` at each step of the integration, several hundred in all, each of which a test had to fail or be shown to change nothing.

## Tests

| Tier | Command | Needs |
| --- | --- | --- |
| Unit | `mise run test` | cgo for SQLite |
| Inference + integration | `mise run test:all` | native libs, WASM runtimes, models |
| Benchmarks | `mise run bench` | nothing extra |
| Everything CI runs | `mise run ci` | all of the above |

Timer behaviour (debounce, the 3 s cap, idle hibernation, lease expiry) is tested on the real `time` API inside `testing/synctest` bubbles, so production code has no `Clock` interface. `internal/plan` has golden tests that pin the rendered bytes and the hash chain: a change that would silently break every contact's prompt cache fails CI.

## Measured

On an Apple M-series laptop (18 cores, darwin/arm64), Go race detector off unless noted. These are first measurements on synthetic data, there to replace the doc's estimates, not to stand as benchmarks of record. `mise run bench` reproduces the first table; `go run -tags ORT ./cmd/replay -infer ort -sweep` the second.

| What | Doc's estimate or target | Measured |
| --- | --- | --- |
| Stage-1 recall, 5,000 centroids × 384 dims (`BenchmarkStage1`) | under 1 ms | 0.60 ms |
| Build and hash the prompt plan, 400-message tail (`BenchmarkBuild`) | under 1 ms | 0.20 ms |
| Group commit, idle (`BenchmarkWriter/writers=1`) | one commit per event | 1.000 events/commit, 11.5 µs per durable append |
| Group commit under load (8 / 64 / 512 writers) | batch size grows with load | 4.0 / 32.8 / 299 events/commit at 107k-139k events/s |
| Memory per hydrated contact, 60-message tail, 40 episodes (`BenchmarkDensity`) | sets `MaxHydrated` | 19.8 KB (15.5 KB heap + 4.3 KB stack) before message bodies |
| Platform overhead without inference: 600 turns across 150 concurrent contacts (`internal/e2e`, fake models, race detector on) | | p50 ≤ 1 ms, p99 ≤ 10 ms |
| Embed one text, 16 / 128 tokens, one thread (macOS arm64; linux/arm64 within 15%) | 3-10 ms | 2.0 / 11 ms fp32, 2.0 / 13 ms int8 |
| Rerank 20 pairs as one call, one thread, 64 / 256 tokens per pair | 20-60 ms | 48 / 242 ms fp32, 61 / 290 ms int8 |
| Rerank as the server does it, spread across workers: 312 turns, 4 concurrent contacts, shortlists up to 20 pairs of short passages (`rerank_ms`) | 20-60 ms | mean 44 ms, p99 ≤ 100 ms |
| Platform overhead with real inference, same run (`turn_overhead_ms`: debounce firing to model request sent) | p99 under 100 ms | mean 46 ms, p99 ≤ 100 ms (histogram bucket bound) |
| Tier-0 lease + first exec, module precompiled | single-digit ms | QuickJS 1.5 ms; CPython 3.12 48 ms (38 ms of it CPython start-up) |
| Tier-0 exec on a warm interpreter | | 78 µs |
| Terminal client, 10,000 messages loaded, 100 by 30: one key and its screen (`BenchmarkKeyPress`) | about 150 µs | 61 µs |
| Terminal client: a batch of three pieces of a streaming reply and its screen (`BenchmarkStreamedBatch`) | 300 µs | 23 µs |
| Terminal client: the screen asked for again with nothing changed (`BenchmarkViewUnchanged`) | under 1 µs | 16 ns, no allocation |
| Terminal client: a resize and its screen (`BenchmarkResize`) | under 20 ms | 0.30 ms |
| Terminal client: one frame of the avatar, stepped and drawn, 100 by 23 (`BenchmarkFrame` in `internal/tui/stage`) | | 0.28 ms |
| Terminal client, bytes written to the terminal: nothing changed / one typed character / one tick of a running tool's time | 0 / at most 40 / at most 120 | 0 / 13, and 30 for the first character of a draft / 82 at most |

**Cache simulator and recall benchmark** (`cmd/replay`), over 12 generated transcripts in `testdata/transcripts` (204 conversations, 632 turns, 8 topics, callbacks labelled), real int8 models, breakpoint-style provider cache, and a deliberately tiny 1,500-token tail so that compaction and recall happen at all:

| Planner | Input tokens cached | Cached on warm turns | Tier-2 / tier-3 breaks |
| --- | --- | --- | --- |
| Step compaction + hysteresis (the design) | 58.4% | **86.6%** | 45 / 17 |
| Sliding window + hysteresis | 37.3% | 55.2% | 28 / 168 |

"Warm" means within the cache TTL of the previous turn, which is the steady state the doc's 80% target is about; the overall figure is lower only because the first turn of every conversation starts cold. With the default 24k-token tail these short transcripts never compact, and the warm share is 94.5%. The planner's own prediction (58.2%) matches what the simulated provider reported (58.4%).

- **Step versus slide is the big lever**, as the doc argues: 86.6% against 55.2%. Hysteresis is worth under a point here (86.6% against 85.8% without it); it matters more when rerank scores are noisier.
- **Rerank cost depends on passage length far more than on anything else.** At the full 256-token bound a 20-pair rerank is about 100 ms even when spread over five workers, which leaves no room in a 100 ms budget. If real episodes run that long, lower `PassageTokens` or the shortlist before anything else.
- **Segmentation** produced 211 episodes for 204 conversations at the starting τ = 0.45 and 30-minute gap.
- **int8 against fp32:** identical at the default threshold (83% recall after rerank, 86.6% against 86.3% warm cached). At θ = -2 and +2 fp32 recalls one more of the 12 callbacks than int8, which is within noise at this sample size but worth re-checking on harder data.
- **Rerank does not beat stage 1 on this data, so the doc's first recall target is not demonstrated.** Recall@4 on the 12 labelled callbacks is 100% for the bi-encoder alone and 83% after the cross-encoder at θ = 0 (92% at θ = -2). Eight cleanly separated topics are too easy for stage 1 to miss, so all the reranker can do here is veto. Transcripts with overlapping topics are needed before θ is worth tuning; the sweep is built for that.
- **Passage content mattered more than any threshold.** Quoting only an episode's first and last message gave 58% recall after rerank; filling the same 256-token budget with as much of the episode as fits gave 83%.

**Run against live providers:** the probe results above, and one conversation through the real binary on `deepseek-flash`: two turns that each ran Python in the tier-0 sandbox, then a delegated sub-agent whose result came back as a notice. Reasoning was stored and replayed on every later request, and the provider's reported cache hits tracked the predictions (for example 512 of 649 input tokens reported cached against 290 of 384 estimated).

**Not measured:** the Anthropic and Ollama Cloud profiles against their live endpoints (tested against written fixtures and the conformance suite only), the tier-1 sandbox client against a real service, checkpoints and forks against a live provider's cache, and any latency on linux/amd64 (CI passes there; nothing was timed).

## Where this departs from the design doc

Each of these is a place where following the doc to the letter would have been wrong, impossible, or worse than a nearby alternative. They are listed so they can be argued with.

**Toolchain**

- **Go 1.26.9, not 1.25.** Go 1.25 is past end of support. `govulncheck`, which the doc makes a CI gate, reports nine standard-library vulnerabilities in 1.25.14 (mostly in the `net/http` server) that are fixed only from 1.26.9. Everything the doc wants from 1.25 (container-aware GOMAXPROCS, stable `testing/synctest`) is in 1.26.
- **Hugot is pinned at v0.7.0.** Later releases need a newer Go and moved to different ONNX Runtime bindings. v0.7.0 matches the doc's description (ORT backend, statically linked Rust tokenizer).
- **One build tag, `ORT`.** The doc uses both `ort` and `ORT`. Hugot only honours the uppercase one, so it is used everywhere.

**Prompt layout (Piece 5)**

- **In-turn tool steps are strictly appended, after the time note.** The doc's `AppendTail` is described as append-only; making that literal means step N of a turn is a byte prefix of step N+1. That is what lets the Anthropic adapter send the model's thinking blocks back unchanged during a tool loop, which the API requires.
- **`FirstChanged` reports tier 4 when the tail only grew.** Read literally, the hash chain would report a tier-3 break on every turn. The doc's own table distinguishes "append every turn" from "reset every N turns", so an append-only extension counts as unchanged.
- **Predicted cached tokens are zero once the cache TTL has passed.** Otherwise the predicted-versus-actual gap is dominated by the hours between conversations and stops being useful for catching bugs.
- **An unchanged recall set reuses the previous tier-2 bytes verbatim**, so a lazy summary landing mid-conversation cannot disturb a warm prefix.

**Turns and actors (Pieces 2, 7, 8)**

- **A tool call is logged before it runs, not after**, and the turn enters its tool phase on the actor goroutine, in mailbox order. A message is therefore cleanly before the call (and cancels the turn) or after it (and waits for the tool). A crash mid-tool leaves a durable record. The planner renders a mid-run message after the tool result (providers require a result directly after its call) and gives an orphaned call a placeholder result.
- **A failed write stops that contact's later writes.** The doc's writer reports each request's error and moves on, which lets a reply commit past a message that failed, leaving a hole in the sequence. Here the writer refuses a contact's writes after one fails until its actor has dropped its state and reset, and the actor waits for that before the contact can be hydrated again.
- **Unanswered messages are capped per contact** (128 KiB; `429` beyond it), so a client that sends faster than the model answers cannot grow the prompt without limit. A refused message does not interrupt the turn in flight, so the backlog clears.
- **The interrupt is applied before the new message is appended**, so a cut-off reply is logged before the message that cut it off.
- **Sending is done under the shard lock with a non-blocking send.** The doc's `select` on `inbox` and `dead` can strand a message in a retiring actor's mailbox. Here retirement flips a flag under the write lock after seeing an empty mailbox, so no forwarding step is needed.
- **Live streams are parked in the registry while their contact is hibernated.** A subscriber neither keeps an actor in memory nor loses its stream when the actor retires, and subscribing never hydrates a contact.
- **`EnvLeaser.Lease` takes the contact ID**, which tier 0 needs to find the contact's directory.
- **Shutdown starts the reply to anything already acknowledged**, even if its debounce had not fired, so the grace period covers it. After a crash, though, a message left unanswered stays unanswered until the contact writes again: it is durable, but nothing scans for it at startup.
- **An idle actor is one with no contact activity.** Bookkeeping commands (embeddings arriving, an environment snapshot) do not reset the idle timer.

**Recall (Piece 4)**

- **The open episode's centroid is kept at float precision** and quantized for storage and search. Requantizing an int8 running mean on every message accumulates rounding error.
- **The previous agent message joins the recall query only when it is recent** (within the episode gap). Yesterday's reply says nothing about today's first message, and including it recalled yesterday's topic.
- **Rerank passages quote as much of an episode as fits in 256 tokens**, not only its first and last message. See "Measured".
- **One rerank is spread across the inference workers** as several model calls instead of one, because a single 20-pair call at the 256-token bound takes about a quarter of a second on one thread.

**Providers and environments**

- **Wire codecs are hand-written HTTP, not the vendors' SDKs.** Requests are frozen to exact bytes before they are sent, so that a retry resends the same request and the prompt cache can be reasoned about; golden tests pin those bytes. An SDK can replace the transport inside a codec later if it passes the same tests and exposes raw usage and reasoning payloads.
- **Anthropic specifics.** Replies default to `claude-opus-5-5` and lazy summaries to `claude-haiku-5-5`. Refusal fallback is on by default (`fallbacks: "default"` with its beta header), as Anthropic recommends for this model: a classifier-declined request is re-run server-side on another model and billed at that model's rates. `-no-refusal-fallback` turns it off, which a gateway that rejects the header will need.
- **Chat Completions routes send tiers 0-2 as one system message**, because many self-hosted chat templates reject more than one.
- **Every tool an agent config defines is a code-execution tool.** The one exception is the built-in `delegate` tool, which has its own argument schema; arbitrary function tools are still not modelled.
- **Tier-0 Python starts in about 48 ms, not single-digit milliseconds.** That is CPython's own initialization; the provider's overhead is about 1.5 ms, which is what QuickJS shows. Variables persist within a lease and files persist always, as the doc's open question allowed.
- **The image runs as a non-root user** with `/data` owned by it.

**Provider core** (against the "Provider core" section of the design)

- **The turn loop's port is `turn.Models`** (resolve a route, get an affinity key, start a call), not the old `turn.LLM`. The loop needs the route's capabilities, so hiding the route behind a one-method port would have hidden what it acts on.
- **A provider is given an opaque affinity key, never a contact ID.** The client computes a keyed hash and blanks it for routes that take none. `Options` also carries the previous request's prefix marks, which is what the cache prediction is made against.
- **An attempt record is an event in the contact's log** (`attempt`): route, a hash and size of the request bytes, the effort actually sent, tries, predicted and reported cache figures, stop reason or error class, and duration. The request bytes themselves are not stored; the hash is there to check a reconstruction.
- **No Responses codec and no native Ollama codec.** OpenCode Go's Responses-only models (GPT Luna, Grok, Muse Spark) resolve to a clear "wire format not supported" error. Ollama Cloud uses `/v1`.
- **DeepSeek `xhigh` is sent as `max`.** The neutral scale maps to the nearest accepted effort and prefers the stronger of two equally near; DeepSeek's own remapping would send `high`. The log shows what was sent.
- **A route's maximum output is a ceiling, not the default.** A call that names no cap asks for 8,192 tokens (64,000 on Anthropic), not the 384,000 a DeepSeek model can write.
- **A checkpoint leaves the cut in place as a guard.** The tail is folded when it passes its budget and cut only at one and a half times it, which is where a tail ends up if checkpoints keep failing.
- **A checkpoint doubles as an episode summary only when the folded span is exactly one unsummarized episode.** A span that covers several conversations has one summary and several episodes; giving them all the same text would make recall worse.
- **A sub-agent is shown the `delegate` tool and refused when it calls it.** Hiding the tool would change the tool list, and with it the first bytes of the prompt, so a child could never read its parent's cache.
- **A fork starts from the prompt of the parent's turn in progress, less that turn**: the same static tier, profile, recalled episodes and history the parent just sent, without the message it is answering.
- **A settlement waits for the turn in flight.** It is held in memory until that turn commits, then logged after the reply. Delivery is best effort, and the parent's log is the record: a delegation's result names its sub-agent, the notice names it again, and a parent that wakes with one unmatched asks that sub-agent to report from its own log. A parent hears about a settlement lost to a shutdown the next time something wakes it, not before.
- **A tool exchange made by another route is shown as text to a route that needs reasoning back.** If a contact is moved to a model that must be given the reasoning behind every earlier tool call, the calls its old model made have none it can take. They are rendered as plain assistant and user text ("ran the python tool", "result of the python tool"), so the request is accepted and the model still sees what happened. The same applies to tool calls logged before reasoning was stored.
- **"Before the first event" includes a response that starts and then fails.** A request the provider accepts with HTTP 200 and then fails before writing anything (an overloaded error as the first frame, a dropped connection) is retried like a refused one. After the first event nothing is retried.
- **Hydration loads the newest checkpoint wherever it is.** The hot window is the last 256 events of the conversation (attempt records are not counted), and a checkpoint can be much older than that.
- **A sub-agent's result is its last message.** The proposal's optional structured value is not built.
- **A reply cut off mid-stream stores no reasoning**, because the stream never finished. On an all-turns route the next request carries that one assistant message without it.

**Platform notes**

- **ONNX Runtime 1.24 on Apple Silicon miscomputes when a signal lands mid-inference**, and Go preempts goroutines with signals. Linux is unaffected. `mise.toml` sets `GODEBUG=asyncpreemptoff=1`, and the binary warns at startup when it is missing on macOS. Avoid CPU profiling there.
- **int8 is not faster than fp32 on arm64.** It is 10-25% slower on the hardware measured, and a quarter of the size. It stays the default, as the doc specifies, because recall is unchanged; x86 with VNNI was not measured.
- **linux/amd64 is covered by CI only.** The GitHub Actions run fetches the native libraries, exports the models and passes every test tier there, but no latency was measured on it and the container image was only built and run on linux/arm64.

**Terminal client** (not in the design doc)

- **A second listener, on a Unix socket, is on by default.** The doc has one TCP address. The socket is how a local client finds the server and the only access control the API has. A data directory whose path is too long for a socket address is logged and skipped unless `-socket` was given by name.
- **Every stream now opens with `ready`, and carries `user`.** Clients that switch on the delta type and ignore what they do not know are unaffected; one that assumed the first delta was `typing` is not.
- **A message refused on the WebSocket no longer closes it.** A refusal (too many unanswered messages, a write that failed) is an `error` frame for that message. Only shutdown ends the connection.
- **`ready` is advisory about durability.** The event it names may be a moment from readable, so the client waits for it, for at most two seconds. If the write failed the event never appears and the client carries on without it.
- **One-shot mode prints the first whole reply after its message** and exits. If that turn delegates, the sub-agent's settlement and the reply it prompts come later and are not waited for.
- **Markdown is rendered once a reply is whole.** While it streams it is shown as written, because re-rendering on every fragment makes the text jump. A call stopped by the user is recorded as `canceled` and shown as `stopped`, rather than as a provider failure.
- **The server serves a contact's tier-0 files**, one at a time, at `/v1/contacts/{id}/files/{path...}`, so that a client can show a picture a tool saved. The files have the same trust as the rest of the API: the directory is opened as a root that a path or a link cannot leave, and anything that is not one of five image types is sent as a download, never as a page.

**Not built**

- Profile updates after creation (an open question in the doc), forgetting / tombstones, multilingual models, multi-node routing.
