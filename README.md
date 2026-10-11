# contactd

One Go binary, backed by SQLite and two small local ONNX models, that lets a client talk to an AI contact the way it would talk to a person. There is one continuous relationship per contact. A client sends messages to a contact ID; it never creates, picks or names a thread.

Threads, prompt-cache layout and the REPL environment are not stored objects. Each turn derives them just in time from one append-only event log.

The design is in [docs/DESIGN.md](docs/DESIGN.md). This README covers running it, what was measured, and where the implementation departs from the design.

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

### The real thing

```sh
mise run deps:native    # libonnxruntime + libtokenizers.a into third_party/lib, checksum-verified
mise run deps:wasm      # CPython 3.12 and QuickJS WASI builds into third_party/wasm
mise run models:fetch   # export + int8-quantize bge-small-en-v1.5 and ms-marco-MiniLM-L6-v2 into models/
mise run build          # bin/contactd, built with -tags ORT

ANTHROPIC_API_KEY=... GOMEMLIMIT=3GiB bin/contactd \
  -data-dir=./data -models-dir=./models -wasm-dir=./third_party/wasm
```

Or as a container (`mise run image`), with one volume at `/data`. `GOMEMLIMIT` is not cgroup-aware: set it to the container memory limit minus room for ONNX Runtime's native memory.

## API

There are no thread endpoints; that's the point.

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/v1/contacts` | Create a contact from an agent config (`system`, `model`, `tools`) and an optional `profile` |
| POST | `/v1/contacts/{id}/messages` | Send `{"text": ...}`; returns 202 with the durable `seq` |
| GET | `/v1/contacts/{id}/stream` | SSE: `typing`, `text`, `tool_start`, `tool_call`, `tool_result`, `message`, `interrupted`, `error` |
| GET | `/v1/contacts/{id}/live` | WebSocket: send `{"type":"message","text":...}` and `{"type":"typing"}`; receive `ack` plus the same deltas |
| GET | `/v1/contacts/{id}/events?after={seq}&limit={n}` | Page through history |
| GET | `/metrics`, `/healthz` | Prometheus metrics; liveness (503 while draining) |

A tool is a code-execution tool: it takes one `code` argument and names the environment it runs in.

```json
{"name": "python", "description": "Run Python 3.12. Files persist; variables may not.",
 "env": {"tier": 0, "base": "python-3.12"}}
```

Tier 0 runs in-process under wazero (`python-3.12`, `quickjs`). Tier 1 is a thin client to an external snapshot-sandbox service (`-sandbox-url`).

## Configuration

Every setting is a flag and an environment variable (`CONTACTD_` + the flag name in upper snake case); flags win. `contactd -h` lists them all. The ones that matter first:

| Flag | Default | Notes |
| --- | --- | --- |
| `-llm` | `anthropic` | `anthropic`, `openai` (also vLLM, SGLang and `cmd/fakellm`), or `fake` |
| `-model`, `-summary-model` | adapter defaults | The Anthropic adapter defaults to `claude-opus-5-5` for replies and `claude-haiku-5-5` for lazy summaries |
| `-openai-base-urls` | | Several replicas are rendezvous-hashed by contact, so a contact's KV prefix stays on one GPU |
| `-infer` | `ort` | `fake` swaps in a deterministic bag-of-words embedder and lexical reranker |
| `-model-variant` | `int8` | or `fp32` |
| `-sync-full` | off | `synchronous=FULL`: never lose an acknowledged message on power loss |
| `-max-hydrated` | derived | From `GOMEMLIMIT` ÷ 2 ÷ `-bytes-per-actor` |
| `-tau`, `-gap`, `-theta`, `-delta`, `-tail-budget` | doc values | Tune with `cmd/replay` before changing them |
| `-no-refusal-fallback` | off | See "Anthropic specifics" below |

## How it is laid out

Ports and adapters, one module. Domain packages hold the logic and declare the small interfaces they need; adapters satisfy them; only `cmd/contactd` sees both. `depguard` turns a layering violation into a lint failure.

```text
cmd/contactd      wiring, signals, graceful shutdown
cmd/replay        cache simulator + recall benchmark over testdata/transcripts
cmd/fakellm       fake model server (OpenAI wire format) for load tests
internal/core     ContactID, Event, bodies, Message, sentinel errors
internal/actor    Pieces 2 + 7: actor loop, registry, debounce, interruption, hibernation
internal/turn     Piece 8: the turn loop, the environment handle; declares LLM and EnvLeaser
internal/recall   Piece 4: episodes, segmentation, two-stage recall; declares Embedder and Reranker
internal/plan     Piece 5: tiers, canonical bytes, hash chain, hysteresis, step compaction (pure)
internal/eventlog Piece 1: SQLite store, group-commit writer, migrations (sqlcgen/ is generated)
internal/infer    Piece 3: micro-batcher; hugot/ (ONNX Runtime, tag ORT), fake/
internal/llm      anthropic/, openai/, fake/
internal/env      Piece 6: wasm/ (tier 0, wazero), remote/ (tier 1)
internal/api      HTTP, SSE, WebSocket
internal/e2e      integration tier (tag integration)
```

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

**Not measured:** anything against a live model provider (no API key was available, so the Anthropic and OpenAI adapters are tested only against replayed fixtures and the fake server), the tier-1 sandbox client against a real service, and linux/amd64.

## Where this departs from the design doc

Each of these is a place where following the doc to the letter would have been wrong, impossible, or worse than a nearby alternative. They are listed so they can be argued with.

**Toolchain**

- **Go 1.26.9, not 1.25.** Go 1.25 is past end of support. `govulncheck`, which the doc makes a CI gate, reports nine standard-library vulnerabilities in 1.25.14 (mostly in the `net/http` server) that are fixed only from 1.26.9. Everything the doc wants from 1.25 (container-aware GOMAXPROCS, stable `testing/synctest`) is in 1.26.
- **Hugot is pinned at v0.7.0.** Later releases need a newer Go and moved to different ONNX Runtime bindings. v0.7.0 matches the doc's description (ORT backend, statically linked Rust tokenizer).
- **One build tag, `ORT`.** The doc uses both `ort` and `ORT`. Hugot only honours the uppercase one, so it is used everywhere.
- **`pipx:sqlfluff`**, since mise has no `pypi:` backend.

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

- **The Anthropic adapter is hand-written HTTP, not the official Go SDK.** It was built that way for byte-exact control of the request. Anthropic's guidance is to use the SDK; porting is a contained change behind `turn.LLM`.
- **Anthropic specifics.** Replies default to `claude-opus-5-5`, summaries to `claude-haiku-5-5`. Refusal fallback is on by default (`fallbacks: "default"` with its beta header), as Anthropic recommends for this model: a classifier-declined request is re-run server-side on another model and billed at that model's rates. `-no-refusal-fallback` turns it off, which a gateway that rejects the header will need.
- **The OpenAI adapter sends tiers 0-2 as one system message**, because many self-hosted chat templates reject more than one.
- **Every tool is a code-execution tool.** The doc's turn loop only knows `ToolCall.Code` and `ToolCall.Env`; arbitrary function tools are not modelled.
- **Tier-0 Python starts in about 48 ms, not single-digit milliseconds.** That is CPython's own initialization; the provider's overhead is about 1.5 ms, which is what QuickJS shows. Variables persist within a lease and files persist always, as the doc's open question allowed.
- **The image runs as a non-root user** with `/data` owned by it.

**Platform notes**

- **ONNX Runtime 1.24 on Apple Silicon miscomputes when a signal lands mid-inference**, and Go preempts goroutines with signals. Linux is unaffected. `mise.toml` sets `GODEBUG=asyncpreemptoff=1`, and the binary warns at startup when it is missing on macOS. Avoid CPU profiling there.
- **int8 is not faster than fp32 on arm64.** It is 10-25% slower on the hardware measured, and a quarter of the size. It stays the default, as the doc specifies, because recall is unchanged; x86 with VNNI was not measured.
- **linux/amd64 has not been run.** Its native-library checksums are pinned but that branch of the fetch script was never executed.

**Not built**

- Profile updates after creation (an open question in the doc), forgetting / tombstones, multilingual models, multi-node routing.
