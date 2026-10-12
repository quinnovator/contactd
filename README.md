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

In a conversation: `enter` sends and `alt+enter` starts a new line. `esc` stops the reply in flight. `pgup` and `pgdn` scroll, and scrolling past the top loads earlier history. `ctrl+o` unfolds tool calls and their output, `ctrl+p` goes back to the contacts, and `ctrl+c` clears the box, then quits. There is nothing to do before typing while the agent is replying: a message sent then interrupts it, as it does a person.

See [The terminal client](#the-terminal-client) for how it is built.

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
| POST | `/v1/contacts/{id}/messages` | Send `{"text": ...}`; returns 202 with the durable `seq` |
| POST | `/v1/contacts/{id}/interrupt` | Stop the reply in flight; 202 |
| GET | `/v1/contacts/{id}/stream` | SSE: `ready`, `user`, `typing`, `text`, `tool_start`, `tool_call`, `tool_result`, `message`, `interrupted`, `notice`, `error` |
| GET | `/v1/contacts/{id}/live` | WebSocket: send `{"type":"message","text":...}`, `{"type":"typing"}` and `{"type":"interrupt"}`; receive the same deltas, and for each message one `ack` or one `error` frame, in the order sent |
| GET | `/v1/contacts/{id}/events` | Page through the log in log order: `?after={seq}` forward, `?before={seq}` backward, `?tail=1` backward from the end, each with `limit`. `?kinds=user_msg,agent_msg` keeps only those kinds; the others are `tool_call`, `tool_result`, `attempt`, `checkpoint`, `notice`, `summary` and `env_snapshot` |
| GET | `/metrics`, `/healthz` | Prometheus metrics; liveness (503 while draining) |

The same API is served on a Unix socket, `contactd.sock` in the data directory, with permissions `0600`. Nothing else about the API is authenticated: the TCP address is for a trusted network, and the socket is for the user who runs the server.

**Reading a contact without missing anything.** A stream runs ahead of the log's group commit, and the log is the only place the past is. A client that wants both opens the stream first. The first delta is always `ready`, and its `seq` is the last sequence number assigned before the stream began (zero when the contact was not in memory). Everything up to there is read from `/events`; everything after it arrives on the stream. Deltas that announce an event carry its `seq`, so reading an event both ways is harmless. A stream opened mid-reply is then sent `typing` and one `text` delta with what has been said so far.

A contact's **name** is what a person calls it. It is optional, unique where given, and never a number. It is stored beside the agent config, not in it, so naming a contact changes no byte of its prompt.

An agent config:

| Field | Meaning |
| --- | --- |
| `system` | The system prompt |
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
| `ollama` | `OLLAMA_API_KEY` | Chat Completions at `ollama.com/v1` | None declared | Per model, from `/api/show` | None declared |
| `openai` | `OPENAI_API_KEY` and/or `-openai-base-urls`, plus `-openai-models` | Chat Completions | Automatic by prefix per replica | None | None |

Each profile's source file states where every declared value comes from and what is a guess. OpenRouter, OpenCode Go and Ollama Cloud read their model catalogs once at start-up; `-openrouter-catalog` starts from a saved copy instead.

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

Not probed: `anthropic` and `ollama` (no key was available), and every OpenRouter and OpenCode Go model other than the ones above.

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
internal/env      Piece 6: wasm/ (tier 0, wazero), remote/ (tier 1)
internal/api      HTTP, SSE, WebSocket; apitest/ runs it for tests of its clients
internal/client   the API from the other side: requests, one WebSocket, and a session that reconnects
internal/transcript  log events + live deltas -> one conversation (pure)
internal/tui      the screens of `contactd chat` (Bubble Tea)
internal/e2e      integration tier (tag integration)
```

## The terminal client

`contactd chat` is a subcommand of the same binary, so the client and the server it talks to cannot disagree about the protocol. It is built in three layers, and `depguard` keeps each in its place.

- **`internal/client`** knows the API and nothing behind it. It never opens the data directory: a contact's sequence numbers are assigned in memory by the one actor that owns it, so a second process writing the log would break it. Its `Session` keeps a WebSocket open, reconnects with a growing delay, and reads the log around the stream using the `ready` protocol above, so its caller sees one ordered sequence of updates. After each turn it reads the log once more, which is where the record of the model call comes from.
- **`internal/transcript`** is a pure reducer: events and deltas in, a conversation out. The same event may arrive from the log and from the stream, in either order; the sequence number makes that harmless. It imports no clock and does no I/O, so what a view shows in any situation is a unit test.
- **`internal/tui`** draws the reducer's state and reads keys. It keeps nothing between runs: contacts, names and messages live on the server.

What follows from the platform's shape:

- **There is nothing to pick but a contact.** No sessions and no threads; opening a contact opens the end of its one log, and earlier history loads a page at a time on scrolling up. A line marks each new day.
- **The input never locks.** A message sent mid-reply interrupts it. `esc` interrupts without saying anything: the reply so far is kept and flagged, a running tool finishes first, and the message that was being answered stays unanswered until the next one.
- **A message is shown as sending until the server says it is durable.** One the server refuses, or one that was in flight when the connection dropped and is not in the log afterwards, is put back in the input box.
- **The header shows the cache working**: the share of the last prompt that the provider reported as cached, and how long the call took.
- **Text from the server is never trusted with the terminal.** Control characters and bidirectional overrides are stripped from everything a model, a tool or a contact's name can put on screen.

What it does not do yet: show a sub-agent's own log (its settlement appears as a notice, and `contactd chat <id>` opens it like any contact), edit a contact's agent config or profile, use the mouse, or search.

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
- **Markdown is rendered once a reply is whole.** While it streams it is shown as written, because re-rendering on every fragment makes the text jump.

**Not built**

- Profile updates after creation (an open question in the doc), forgetting / tombstones, multilingual models, multi-node routing.
