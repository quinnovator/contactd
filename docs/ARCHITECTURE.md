# Architecture

contactd is a single Go binary, backed by SQLite and two small local ONNX models, that lets a client talk to an AI contact the way it would talk to a person. There is one continuous relationship per contact. Threads, prompt-cache layout and the REPL environment are not stored objects; each turn derives them just in time from one append-only event log.

This document describes how the system is built and why. It is organised as eight pieces, each adding one capability on top of the last; package comments refer to them as "Piece N". For the HTTP API see [API.md](API.md), for model providers see [PROVIDERS.md](PROVIDERS.md), and for the terminal client see [CLIENT.md](CLIENT.md).

## Overview

**Goals**

- A client sends messages to a contact ID; it never creates, picks or names a thread.
- Relevant past context is recalled automatically, using local embedding and reranking with no external vector DB.
- Every turn is laid out for maximum prompt-cache reuse, and cache hit rate is a first-class metric.
- A REPL environment exists only when the model needs one, and its files persist across days.
- Idle contacts cost zero memory and zero goroutines.
- Everything runs in one container with one volume.

**Non-goals**

- Multi-node clustering. The system keeps a single writer per contact so sharding can be added later without changing anything above the log.
- Hosting LLM inference. Model calls go to a provider API or a separate inference server.
- Running heavyweight sandboxes inside the container. Those are delegated to an external snapshot-sandbox service.

## First principles

The model call dominates every turn, so the platform wins by avoiding model work rather than by shaving its own microseconds. The approximate budget of one turn:

| Step in a turn | Approx. cost | Who controls it |
| --- | --- | --- |
| Hydrate a cold contact (SQLite range scan) | 1–5 ms | Platform |
| Embed the new message (bi-encoder, CPU) | 2–13 ms | Platform |
| Stage-1 recall (brute-force cosine, ≤5k vectors) | under 1 ms | Platform |
| Stage-2 rerank (cross-encoder, up to 20 pairs) | 20–100 ms | Platform |
| Build and hash the prompt plan | under 1 ms | Platform |
| Model time to first token, prefix cached | 0.3–1 s | Platform, via cache layout |
| Model prefill, \~50k tokens uncached | several seconds | Platform, via cache layout |
| REPL: in-process WASM / remote snapshot restore / cold boot | ms / 100s of ms / seconds | Platform, via env tiering |

That table gives three levers, in order of impact:

1. **Prompt-cache hit rate.** One avoided cold prefill is worth more than every other optimization combined.
2. **REPL cold start.** Prefer restoring a snapshot over booting, and prefer in-process execution over remote.
3. **Idle cost per contact.** Zero goroutines and zero heap for contacts that aren't talking, so density scales with active contacts, not total ones.

**Why Go.** The hot path is I/O plus small CPU bursts, which goroutines handle well. `context.Context` makes interrupting a turn safe and simple, and interruption is a core feature here. The two known costs have known mitigations:

- **GC headroom.** Set `GOMEMLIMIT` explicitly, because unlike GOMAXPROCS it does not read cgroup limits. Leave room for ONNX Runtime's native memory, which sits outside the Go heap.
- **cgo.** ONNX Runtime and the Rust tokenizer need cgo. That cost is paid once, and the cgo SQLite driver is used as well, rather than paying for two worlds.

The toolchain is Go 1.26 (`go.mod`; `mise.toml` pins 1.26.9, the first release without the standard-library vulnerabilities `govulncheck` reports in 1.25). Container-aware GOMAXPROCS and stable `testing/synctest` both come with it.

## Core model

Only the event log is stored; threads, cache layout and environments are pure functions of it, evaluated per turn.

- **Contact**: a durable identity with exactly one append-only log. It is the only thing a client addresses. A sub-agent is a contact too.
- **Event**: anything that happens to a contact.
- **Turn**: a short-lived computation triggered by inbound messages. At run time it decides which history matters, how to lay out the prompt, and whether it needs a REPL.

```go
type ContactID uint64

type EventKind uint8

const (
    KindUserMsg EventKind = iota + 1
    KindAgentMsg
    KindToolCall
    KindToolResult
    KindSummary     // lazily generated episode summary
    KindEnvSnapshot // pointer to a REPL snapshot, not the snapshot itself
    KindAttempt     // one model call: what was sent and how it went; never shown to the model
    KindCheckpoint  // a summary that replaces the oldest span of the tail
    KindNotice      // the runtime telling the agent something, such as a sub-agent settling
)

type Event struct {
    Contact ContactID
    Seq     uint64 // monotonic per contact; assigned by that contact's actor
    TS      int64  // unix ms
    Kind    EventKind
    Body    []byte // JSON, opaque to the log
}
```

The one invariant everything depends on: **each contact has exactly one writer at a time.** Piece 2 enforces it in-process. A future cluster would enforce it by routing each contact ID to a single node.

## Piece 1: Event log on SQLite

`internal/eventlog`. One SQLite file in WAL mode, a single writer goroutine doing adaptive group commit, and a separate pool of readers. The driver is `github.com/mattn/go-sqlite3` (cgo).

**Schema** (`sql/schema/`, applied in order via `PRAGMA user_version`). The event key leads with the contact, so one contact's history is a single sequential range scan.

| Table | Holds |
| --- | --- |
| `contacts` | `id`, the agent config (`agent`, JSON), `profile`, `last_active`, plus `name` (optional, unique where given), `parent` (the delegating contact, 0 for a top-level one), `time_zone` and `last_spoke` |
| `events` | `(contact, seq)` → `ts`, `kind`, `body`. `WITHOUT ROWID` |
| `vectors` | one 384-d float32 vector per message event, written after embedding |
| `episodes` | derived index: `(contact, ep)` → span, quantized centroid, `summary_seq`. Rebuildable from `events` and `vectors` at any time |
| `sessions` | the visit IDs each contact has seen (see Piece 7) |

A contact's name is stored beside the agent config, not in it, so naming or renaming a contact never changes a byte of its prompt. `parent` is copied out of the agent config so listing the contacts a person talks to never reads a sub-agent.

**Pragmas** on every connection: `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout`, `temp_store=MEMORY`, `mmap_size=268435456`, and a negative `cache_size` in KiB. In WAL mode, `synchronous=NORMAL` can lose the last few commits on power loss but never corrupts the file. `-sync-full` selects `FULL` instead; group commit amortizes the extra fsync either way.

**Two pools.** The writer pool has `SetMaxOpenConns(1)`. Readers share their own pool, used for hydration, paging and rebuilds.

**Adaptive group commit.** The writer blocks for the first request, then drains whatever else is already queued, and commits it all in one transaction. When idle, one event costs one commit. Under load, batches grow by themselves, with no fixed timer adding latency. In outline:

```go
for {
    r := <-w.in                   // block for the first request
    batch = append(batch[:0], r)
    for len(batch) < cap(batch) { // then take whatever is already waiting
        select {
        case r := <-w.in:
            batch = append(batch, r)
        default:
            goto commit
        }
    }
commit:
    err := w.commit(batch) // one transaction, prepared INSERTs
    for _, r := range batch {
        r.done <- err
    }
}
```

Actors don't wait on the commit before streaming. They wait on `done` only before acknowledging a message to the client as durable.

**A failed write stops that contact's later writes.** If the writer simply reported each request's error and moved on, a reply could commit past a message that failed, leaving a hole in the sequence. Instead the writer refuses a contact's writes after one fails until its actor has dropped its state and reset, and the actor waits for that before the contact can be hydrated again.

## Piece 2: Contact actors

`internal/actor`. Each contact that is currently talking gets one goroutine that owns all of its state. Every other contact is just rows on disk. Because the actor is the only writer, it assigns sequence numbers without locks, and two turns for the same contact can never race.

**The run loop** multiplexes the mailbox, the debounce timer and the idle timer in one `select`, so the actor never blocks on anything slow. Turns run in child goroutines and report back through the mailbox: every delta, tool call, tool result and attempt record a turn produces is a command, and only the actor appends to the log.

**An idle actor is one with no contact activity.** Bookkeeping commands (embeddings arriving, an environment snapshot) do not reset the idle timer. After `-idle-ttl` (10 minutes) with no turn in flight, the actor retires: it snapshots its environment, flushes, and drops its memory.

**Registry.** A sharded map, 64 shards, each a `map[ContactID]*actor` behind a mutex. Sending is done under the shard lock with a non-blocking send into the actor's mailbox (64 deep). Retirement flips a flag under the shard's write lock only after seeing an empty mailbox, so a message can never be stranded in a retiring actor and no forwarding step is needed.

**Live streams are parked in the registry while their contact is hibernated.** A subscriber neither keeps an actor in memory nor loses its stream when the actor retires, and subscribing never hydrates a contact. Each subscriber may fall 256 deltas behind before it is dropped.

**Hydration** reads, on spawn: the hot window (the last 256 events of the conversation; attempt records are not counted), the newest checkpoint wherever it is (it can be much older than the window), the episode rows, and the contact's visit metadata. Message vectors stay on disk. They are only needed to rebuild episodes.

**Memory budget.** Episode centroids are held for search as int8 with a per-vector scale: 384 bytes each, versus 1,536 bytes as float32. That's safe because the cross-encoder in Piece 4 re-scores the shortlist. The registry keeps an approximate LRU of active actors. When the count passes `MaxHydrated` it hibernates the least recently active idle ones, so a traffic spike degrades into rehydration cost instead of an OOM kill. `-max-hydrated` defaults to `GOMEMLIMIT ÷ 2 ÷ -bytes-per-actor` (48 KiB, from `BenchmarkDensity`).

**Unanswered messages are capped per contact** at 128 KiB (`429` beyond it), so a client that sends faster than the model answers cannot grow the prompt without limit. A refused message does not interrupt the turn in flight, so the backlog clears.

## Piece 3: Local embedder + cross-encoder reranker

`internal/infer`. Two small ONNX models run, not one, because a cross-encoder cannot produce embeddings. It scores a (query, passage) pair jointly, which makes it accurate but costs one model pass per pair. So a bi-encoder handles cheap recall over everything, and the cross-encoder re-scores only a shortlist.

| Role | Default model | Size | Output | Runs per turn |
| --- | --- | --- | --- | --- |
| Embedder (bi-encoder) | BAAI/bge-small-en-v1.5 (all-MiniLM-L6-v2 also works) | \~33M params, 384-d | one vector per text | 1 on the critical path, plus background indexing |
| Reranker (cross-encoder) | cross-encoder/ms-marco-MiniLM-L6-v2 | \~22M params | one relevance logit per pair | up to 20 pairs |

**Runtime: Hugot on the ONNX Runtime backend.** [Hugot](https://github.com/knights-analytics/hugot) provides HF-compatible feature-extraction and cross-encoder pipelines in Go. Its ORT backend needs `libonnxruntime` plus the statically linked Rust `libtokenizers.a`, and is compiled in with the build tag `ORT` (uppercase; Hugot honours only that spelling). Hugot is pinned at v0.7.0: later releases need a newer Go and moved to different ONNX Runtime bindings. `-infer=fake` swaps in a deterministic bag-of-words embedder and a lexical reranker, so everything else builds and runs without ONNX Runtime.

Both models are exported to ONNX with HF Optimum and int8 dynamically quantized (`scripts/export_onnx.py`, `scripts/quantize.py`, run by `mise run models:fetch`). int8 stays the default because recall is unchanged against fp32 and the files are a quarter of the size; it is not faster on arm64 (see Measured). `-model-variant=fp32` selects the unquantized files.

**One micro-batcher per model**, using the same drain pattern as the SQLite writer. There are two queues: critical-path requests (the query embedding, rerank) are always drained before background indexing. `embed_queue_depth{priority}` shows whether background work is starving critical requests.

**Threading.** K worker loops share each pipeline, each ORT call pinned to one intra-op and one inter-op thread. K is `-infer-workers`, by default half of GOMAXPROCS. **One rerank is spread across the workers** as several model calls instead of one, because a single 20-pair call at the 256-token bound takes about a quarter of a second on one thread.

**What gets embedded.**

- **Messages:** every user and agent message, embedded in the background after append; the vector is written through the group-commit writer.
- **The recall query:** the new message alone is often too vague ("ok do that again"), so the query is the new message plus the previous agent message, truncated to about 128 tokens. **The previous agent message joins only when it is recent** (within the episode gap): yesterday's reply says nothing about today's first message, and including it recalled yesterday's topic. Session notices are excluded from the query text.
- **Rerank passages:** the episode summary if one exists, otherwise **as much of the episode as fits in 256 tokens**. Bounding pair length bounds rerank cost; filling the budget, rather than quoting only the first and last message, is what made recall work (58% against 83%, see Measured).

**Apple Silicon.** ONNX Runtime 1.24 on Apple Silicon miscomputes when a signal lands mid-inference, and Go preempts goroutines with signals. Linux is unaffected. `mise.toml` sets `GODEBUG=asyncpreemptoff=1`, and the binary warns at startup when it is missing on macOS. Avoid CPU profiling there.

## Piece 4: JIT threads

`internal/recall`. A "thread" is whatever this turn recalls: the hot tail plus up to four past episodes that clear a relevance threshold. Episodes are segmented automatically as messages arrive; nobody names them or chooses one.

**Segmentation.** Each embedded message either extends the current episode or opens a new one. Two rules decide:

- **Time gap.** More than 30 minutes (`-gap`) since the episode's last message opens a new episode.
- **Topic drift.** Cosine similarity to the running centroid falls below τ (`-tau`, 0.45) for **two consecutive** messages. Requiring two stops a single off-topic aside from splitting an episode.

**The open episode's centroid is kept at float precision** and quantized for storage and search. Requantizing an int8 running mean on every message accumulates rounding error.

```go
type Episode struct {
    ID         uint32
    Start, End uint64           // seq span
    Centroid   [384]int8        // quantized, + Scale
    Scale      float32
    N          uint32           // members, for the running mean
    LastTS     int64
    SummarySeq uint64           // 0 until summarized lazily
}
```

The changed episode row is upserted through the group-commit writer. The episodes table is derived data, so a corrupt or outdated index is fixed by replaying `events` and `vectors`.

**Two-stage recall** runs at the start of each turn.

1. **Stage 1, bi-encoder.** Take the int8 dot product of the query against every centroid and keep the top 20 (the shortlist). Skip episodes that overlap the tail, since they're already in context. At 5,000 episodes × 384 dims this is about 2M multiply-adds, comfortably under a millisecond in plain Go, so no ANN index is needed.
2. **Stage 2, cross-encoder.** Score (query, passage) for the shortlist. Add a recency bonus: `score = logit + λ·exp(−age/halfLife)`, with λ = 1 and a half-life of 7 days.
3. **Threshold.** Keep at most K = 4 episodes, and only those above θ (`-theta`, 0). Often nothing clears it, and recalling nothing is correct: a person doesn't bring up unrelated history.

If the reranker fails, recall degrades to the top of stage 1 instead of failing the turn.

**Lazy summaries.** When a closed episode with no summary is recalled, the excerpt is used this turn. A background job also asks a small, cheap model (`-summary-provider`, `-summary-model`) for a summary. The result is appended as a `summary` event and linked from the episode row. Episodes that are never revisited never cost a summarization call. New summaries are written from the character's first-person perspective (see Piece 7).

## Piece 5: JIT cache

`internal/plan`. Every prompt is assembled from five tiers ordered least- to most-volatile, so most turns change only the last tier or two and reuse the cached prefix. Provider caches and local ones (vLLM automatic prefix caching, SGLang RadixAttention) all match byte-identical prefixes, so the layout is the optimization.

| # | Tier | Changes when | Typical churn |
| --- | --- | --- | --- |
| 0 | Static: fixed server preamble + contact system prompt + tool schemas | Agent config is deployed | Rarely |
| 1 | Contact profile / long-term notes | Profile is edited | Days |
| 2 | Recalled episodes | The topic changes | Every few turns |
| 3 | Tail (append-only), opened by the newest checkpoint if any | Each turn appends; compaction resets it | Append every turn, reset every N turns |
| 4 | Live: new message(s), current time, then in-turn tool steps | Every turn | Always |

Routes that take explicit breakpoints get one at the end of tiers 0–3. Anthropic, for example, allows up to four, which is exactly enough. Chat Completions routes send tiers 0–2 as one system message, because many self-hosted chat templates reject more than one.

```go
type Tier struct {
    Msgs   []core.Message
    Hash   uint64 // rolling xxhash over the canonical bytes of each message
    Tokens int    // estimate, for budgets and cache accounting
}

type PromptPlan struct {
    Tiers      [NumTiers]Tier
    Chain      [NumTiers]uint64 // Chain[i] = H(Chain[i-1], Tiers[i].Hash)
    Recall     []uint32         // episode IDs in tier 2, in order
    TailFrom   uint64           // first seq rendered verbatim in tier 3
    Checkpoint uint64           // checkpoint whose summary opens tier 3, or 0
    BuiltAt    int64            // when built or last sent; the cache's age runs from it
    Tools      []core.ToolDef   // ride along with the static tier, whose hash covers them
}

// FirstChanged reports where the cached prefix breaks versus prev.
func (p *PromptPlan) FirstChanged(prev *PromptPlan) int {
    if prev == nil {
        return 0
    }
    for i := range p.Chain {
        if p.Chain[i] == prev.Chain[i] {
            continue
        }
        if TierKind(i) == TierTail && p.tailExtends(prev) {
            return int(TierLive)
        }
        return i
    }
    return len(p.Chain)
}
```

**`FirstChanged` reports tier 4 when the tail only grew.** Read literally, the hash chain would report a tier-3 break on every turn; an append-only extension is not a break, and the plan keeps per-message hash marks so it can tell the two apart.

**Byte stability rules.** Most accidental cache misses come from small nondeterminism, so:

- No timestamps, request IDs or "current date" in tiers 0–3. They go in tier 4.
- Canonical JSON: sorted keys, fixed float formatting, and tool schemas in a fixed order.
- Recalled episodes are rendered from their stored summary or excerpt, never regenerated text.
- `plan` is pure: it takes `now` as an argument and never calls `time.Now`, ranges over a map to produce bytes, or uses randomness. Golden tests pin the rendered bytes and the hash chain, so a change that would silently break every contact's cache fails CI.

**In-turn tool steps are strictly appended, after the time note.** Step N of a turn is a byte prefix of step N+1. That is what lets a Messages-wire route be handed the model's thinking blocks back unchanged during a tool loop, which the API requires. A message that arrives mid-tool is rendered after the tool result (providers require a result directly after its call), and an orphaned call gets a placeholder result.

**Rule 1: hysteresis on recall.** Keep last turn's recall set unchanged while the cache is probably warm (within the route's cache TTL) and every previous episode is still in the fresh top 2K. Switch only if a new episode beats all of them by margin δ (`-delta`, 1.0). A slightly better recall is not worth a full re-prefill of tiers 2–4. **An unchanged recall set reuses the previous tier-2 bytes verbatim**, so a lazy summary landing mid-conversation cannot disturb a warm prefix.

**Rule 2: compact in steps, never slide.** A sliding window drops the oldest message every turn, which changes the start of tier 3 and breaks the cache every turn. Instead the tail grows until it passes its token budget (`-tail-budget`, 24,000, capped at half the model's context window). Then the oldest part leaves the prompt at a turn boundary; it stays recallable as part of its episodes. The result is one tier-3 miss per N turns instead of one per turn. With checkpoints on (the default), leaving the prompt means being folded into a checkpoint (Provider core, below); the plain cut of the oldest 30% is the guard behind it, at one and a half times the budget.

**The cache TTL is the route's, not the deployment's.** Each model route declares how long its cache stays warm (Anthropic 5 minutes, DeepSeek assumed 1 hour, see [PROVIDERS.md](PROVIDERS.md)). **Predicted cached tokens are zero once that TTL has passed**; otherwise the predicted-versus-actual gap would be dominated by the hours between conversations and stop being useful for catching bugs.

**Self-hosted models.** Each contact is routed to the same inference replica by an affinity key, so its KV prefix is still resident on that GPU.

**The metric.** Each turn records `FirstChanged` (the predicted break point) and the predicted cached share alongside the cached-token count the provider reports. The gap between predicted and actual is how cache bugs get caught; `cache_prediction_misses_total` counts the calls where it was large.

## Piece 6: JIT REPL environments

`internal/env`, behind the `turn.EnvLeaser` port. No environment exists until the model emits a tool call that needs one. Execution is split across two tiers, because an ordinary container has no `/dev/kvm` and can't host microVMs.

| Tier | Mechanism | Start cost | What persists | Use for |
| --- | --- | --- | --- | --- |
| 0, in-process | wazero (pure-Go WASM) running WASI builds of CPython 3.12 (`python-3.12`) or QuickJS (`quickjs`) | 1.5 ms (QuickJS) to 48 ms (CPython start-up) | Files in the contact's working directory, always. Interpreter variables only while one lease's interpreter lives | Math, parsing, data munging, quick scripts |
| 1, remote | External snapshot-sandbox service (`-sandbox-url`), microVMs on bare metal | 100s of ms to restore | Files and installed packages, through the snapshot chain | Native toolchains, packages, network, long jobs |

```go
type EnvLeaser interface {
    Lease(ctx context.Context, contact core.ContactID, spec core.EnvSpec, resume core.SnapshotID) (Lease, error)
}

type Lease interface {
    Exec(ctx context.Context, code string) (ExecResult, error)
    Snapshot(ctx context.Context) (core.SnapshotID, error) // then Release
    Release()
}
```

`Lease` takes the contact ID, which tier 0 needs to find the contact's directory.

**Tier 0 on wazero** (`internal/env/wasm`).

- Each base module is compiled once at startup, sharing a compilation cache.
- An instance's memory is capped (4,096 pages, 256 MiB, by default) and created with `WithCloseOnContextDone(true)`, so the turn's context kills runaway code.
- The per-contact filesystem is a directory on the volume, opened as a root that a path or a symbolic link cannot leave, and mounted as the guest's working directory.
- A lease owns at most one interpreter at a time and replaces it lazily when it dies. The tool result tells the agent what persisted (a fresh interpreter, files kept), so it doesn't assume variables survived a long gap.

**Tier 1** (`internal/env/remote`) is a thin client. A lease is a chain of snapshots and nothing else: every `Exec` runs from the chain's head, asks the service to keep the result, and moves the head. Snapshot IDs are logged as `env_snapshot` events.

**Lifecycle.**

- A lease idle for 2 minutes (`-env-idle-ttl`) is snapshotted and released, even while the actor stays hydrated. Hibernation does the same.
- **Speculative restore:** a streamed tool call reveals its name before its arguments finish. When that name implies an environment, the turn starts acquiring it at once, overlapping it with the rest of the stream. `speculative_restore_hits` counts the calls where it was ready or restoring in time.

**Every tool an agent config defines is a code-execution tool**: it takes one `code` argument and names the environment it runs in. The exceptions are built in: `delegate` (Provider core, below), and on native DeepSeek routes, `web_search` and `web_fetch` ([PROVIDERS.md](PROVIDERS.md)). Arbitrary function tools are not modelled.

## Piece 7: Person-like behavior

Two behaviors make a contact feel like a person rather than an endpoint. It waits for you to finish a burst of messages before replying. When you add something mid-reply, it re-plans instead of answering a stale question.

**Burst coalescing.** Every inbound message, and every typing indicator, resets a 700 ms debounce timer (`-debounce`). A hard cap of 3 s after the first pending message (`-debounce-cap`) keeps someone who types continuously from stalling the reply forever.

**Interruption policy** depends on what the in-flight turn is doing:

| Turn phase when a new message arrives | Action |
| --- | --- |
| Planning, or waiting for the first token | Cancel silently and restart after the debounce |
| Streaming text | Cancel. Persist the partial reply as an agent message flagged `interrupted`, so the model knows it was cut off. Restart |
| Executing a tool | Don't kill it, since it may have side effects. Let it finish, append the result, then re-plan with the new message instead of continuing |

Restarting is cheap because of Piece 5: tiers 0–3 are unchanged, so the re-plan only re-prefills the live tier.

**The interrupt is applied before the new message is appended**, so a cut-off reply is logged before the message that cut it off. An explicit interrupt (`POST /interrupt`, `esc` in the client) cancels the same way without a new message; the message being answered stays unanswered until the next one.

**A tool call is logged before it runs, not after**, and the turn enters its tool phase on the actor goroutine, in mailbox order. A message is therefore cleanly before the call (and cancels the turn) or after it (and waits for the tool). A crash mid-tool leaves a durable record.

Each turn carries a monotonically increasing ID, so a result that arrives after its turn was cancelled is ignored. The actor's own state is never written from a turn goroutine.

### Visits and local time

A client may introduce a visit: a WebSocket `hello`, or the same optional fields on `POST /messages`, carries `session_id`, `time_zone` (an IANA name, validated with `time.LoadLocation`) and `presentation` (`avatar` or `text`). The hello changes only connection-local metadata; opening a client does not trigger a turn or write anything.

The first accepted message of a visit passes those fields to the actor, which logs a `session-start` notice immediately before the message. The notice records the person's local date and time, the gap since the last exchange, and the presentation; a first meeting is described as such. Without an ID, a new visit begins after 30 minutes with no exchange.

- The `sessions` table records used IDs per contact, so reusing an ID never creates another notice, including after hibernation or a restart. The writer commits a session ID with its notice.
- `contacts.time_zone` and `contacts.last_spoke` survive hibernation and restart. A time-zone change is committed with the user message; last-spoken time follows message events, not embeddings, summaries or tool results. Until a zone is supplied, time is UTC. The binary embeds Go's tzdata, so named zones work in a minimal image.
- Multiple clients keep their own IDs and zones; a reconnect never becomes a new visit merely because another client spoke meanwhile.
- The actor supplies the location to the pure planner. Only the live tier's time note changes with the current time or zone. Session notices have fixed logged timestamps and text, then age through the tail, checkpoints and recall like other memories. They are excluded from recall query text and hidden by the transcript renderer.

**The character preamble.** Every contact's static tier opens with a fixed server preamble (`internal/plan/preamble.go`): the character treats prior events as memories and runtime notices as its surroundings. Profiles describe the person, recall describes memories, and new summaries use the character's first-person voice. Sub-agents get the preamble too, and forks share it verbatim. One-off summarizer instructions do not.

## Piece 8: The turn loop

`internal/turn`. A turn chains the pieces in order: resolve the contact's route, embed the query (3), recall episodes (4), build the cache-aware plan (5), call the model and stream its reply, lease an environment only when a tool call needs one (6), and stop or re-plan if interrupted (7). A turn may make at most `-max-tool-steps` (8) model calls.

The turn reaches models through the `turn.Models` port: resolve a route, get an affinity key, start a call, start a sub-agent, write a summary. The loop needs the route's capabilities (how its cache works, whether reasoning must be sent back, whether it can be held to one tool call per step), so the route is visible to it rather than hidden behind a one-method interface.

**Ownership rules** keep the turn goroutine from racing the actor:

- The episode index the turn reads is an immutable snapshot, published through an `atomic.Pointer`. The actor swaps in a new one after each change.
- Every event the turn produces goes back to the actor as a command, and only the actor appends to the log.
- The environment handle uses its own mutex. That's sufficient because a contact never has two turns in flight.

**API.** There are no thread endpoints; a client addresses only contacts. `net/http` with pattern routing serves HTTP, SSE and a WebSocket (`github.com/coder/websocket`), on a TCP address and on a Unix socket (`contactd.sock` in the data directory, mode `0600`). Every stream opens with a `ready` delta naming the last sequence number assigned, so a client can read the log up to it and the stream after it without missing anything. The endpoints and the stream protocol are in [API.md](API.md).

## Provider core

`internal/llm`. The core reaches every model provider through one typed contract and acts only on what a model route declares it can do. The core never names a provider; `depguard` enforces the boundary from both sides.

```go
type Provider interface {
    ID() string
    // Route resolves a model name to a route and its declared capabilities.
    // An unknown name is an error, never a silent default.
    Route(ctx context.Context, model string) (Route, error)
    // Prepare freezes one model call: the exact request bytes and headers,
    // and what the call is expected to read from the cache. No I/O.
    Prepare(ctx context.Context, r Route, p *plan.PromptPlan, o Options) (*Frozen, error)
    // Send streams a frozen request. A retry sends the same value again.
    Send(ctx context.Context, f *Frozen) (Stream, error)
}
```

- **A route declares its capabilities**: context window and output ceiling, cache mode (none, automatic by prefix, or breakpoints) and TTL, whether it reports cache usage, where an affinity key goes (header or body), which reasoning efforts it accepts, when reasoning must be replayed (never, within a turn, or on every turn), and whether it can be held to one tool call per step. Each profile under `internal/llm/provider/` states where every declared value comes from and which are guesses. `contactd probe` checks the declarations against the live endpoint.
- **Wire codecs are hand-written HTTP, not vendor SDKs.** `wire/chat` (OpenAI-format Chat Completions) and `wire/messages` (Anthropic-format Messages) turn a plan into bytes and a stream into events. Requests are frozen to exact bytes before they are sent, so a retry resends the same request and the prompt cache can be reasoned about; golden tests pin those bytes. There is no Responses codec: models that speak only that wire resolve to a clear "wire format not supported" error.
- **Effort is one neutral scale**: `none`, `low`, `medium`, `high`, `xhigh`, `max`. A route sends the nearest value it accepts, preferring the stronger of two equally near, and the log records what was sent.
- **A route's maximum output is a ceiling, not the default.** A call that names no cap asks for the route's default: 8,192 tokens on most routes, 64,000 on Anthropic.
- **A provider is given an opaque affinity key, never a contact ID.** The client computes a keyed hash and blanks it for routes that take none. `Options` also carries the previous request's prefix marks, which the cache prediction is made against.
- **Retries happen only before the first event.** A request the provider accepts with HTTP 200 and then fails before writing anything (an overloaded error as the first frame, a dropped connection) is retried like a refused one. After the first event nothing is retried. Concurrent calls are bounded per provider or route (`-provider-limits`).
- **Reasoning is replayed as the route requires.** On an all-turns route it is stored with the agent message and sent back on every later request, tagged with the route that produced it; only that route is ever handed it. A reply cut off mid-stream stores none. A tool exchange made by another route, or logged before reasoning was stored, is shown as plain text ("ran the python tool", "result of the python tool") to a route that needs reasoning back, so the request is still accepted.

**Attempt records.** Every model call is an `attempt` event in the contact's log: purpose, route, a hash and size of the request bytes, the effort actually sent, tries, estimated and predicted tokens, the provider's reported input, cached, output and reasoning tokens (and whether a cache count was reported at all), stop reason or error class, and duration. Attempts are never shown to the model. The request bytes themselves are not stored; the hash is there to check a reconstruction. A call stopped by the user is recorded as `canceled`, not as a provider failure.

**Checkpoints.** With `-checkpoint` on, an over-budget tail is folded rather than cut: the contact's own model writes a summary of the oldest half of the tail, logged as a `checkpoint` event, and tier 3 then opens with it. The cut stays in place as a guard at one and a half times the budget, which is where a tail ends up if checkpoints keep failing. A checkpoint doubles as an episode summary only when the folded span is exactly one unsummarized episode; a span covering several conversations would otherwise give them all the same text and make recall worse.

**Sub-agents.** An agent config with `delegate` on gets a built-in `delegate` tool that starts a sub-agent and returns its ID at once. A sub-agent is a contact of its own, with its parent's instructions, tools and route, and its own log.

- **`spawn`** starts it from the instructions and the task.
- **`fork`** also gives it the parent's conversation: the prompt of the parent's turn in progress, less that turn (the same static tier, profile, recalled episodes and history the parent just sent, without the message it is answering). Its first request begins with the exact bytes the parent last sent, so it reads the parent's prompt cache.
- **A sub-agent is shown the `delegate` tool and refused when it calls it.** Hiding the tool would change the tool list, and with it the first bytes of the prompt, so a child could never read its parent's cache. Delegation is one level deep, with at most `-max-children` (8) running per contact.
- **Settlement.** A sub-agent's result is its last message. It reaches the parent as a `notice` event and starts a turn there, but waits for any turn in flight: it is held in memory until that turn commits, then logged after the reply. A failure settles the same way, with a diagnostic of at most 4,096 bytes.
- **The parent's log is the record.** It names each sub-agent started and each one heard from. A parent that wakes with one unmatched asks that sub-agent to report again from its own log; a sub-agent that was cut off mid-task reports that it failed. A settlement lost to a shutdown is noticed the next time something wakes the parent.

## Container and runtime configuration

`deploy/Dockerfile` builds one glibc-based distroless image (`gcr.io/distroless/cc-debian12:nonroot`) holding the binary, `libonnxruntime.so`, the two int8 models and the tier-0 WASM runtimes, with a single volume at `/data`. Glibc rather than static or musl is required because ONNX Runtime ships as a shared library. The build stage runs the same checksum-verifying fetch scripts as a local build, so the image and local builds link identical libraries. **The image runs as a non-root user** with `/data` owned by it.

**Runtime settings**

| Setting | Value | Why |
| --- | --- | --- |
| GOMAXPROCS | Unset | Go reads the cgroup CPU limit itself |
| GOMEMLIMIT | Container limit minus native reserve (e.g. 4 GiB limit → 3 GiB) | Not cgroup-aware. ONNX Runtime arenas and model weights live outside the Go heap |
| Inference workers | About half of GOMAXPROCS | Leaves cores for request handling and SQLite |
| `MaxHydrated` | `GOMEMLIMIT ÷ 2 ÷ bytes per actor` | Hibernation kicks in before GC thrash |
| Data volume | `/data/contacts.db`, `/data/fs/{contact}/`, `/data/contactd.sock` | SQLite needs a real filesystem with working locks, not an overlay or network mount |

**Graceful shutdown on SIGTERM** (`cmd/contactd`)

1. Stop accepting new messages and return 503 from `/healthz`.
2. Give in-flight turns a grace period (`-shutdown-grace`, 20 s) to finish. Cancel them at the deadline, persisting partial replies as interrupted. **A reply to anything already acknowledged is started**, even if its debounce had not fired, so the grace period covers it.
3. Hibernate every actor, which snapshots environments and releases leases.
4. Close the HTTP server, drain the writer, close SQLite cleanly, and exit.

After a crash, a message left unanswered stays unanswered until the contact writes again: it is durable, but nothing scans for it at startup.

**Scaling past one container** keeps the single-writer rule. A router would consistent-hash `ContactID → node`, and the log would move from local SQLite to per-node SQLite with replication (for example Litestream to object storage) or to Postgres. Nothing above the log interface changes.

## Observability and validation

The headline metric is the share of input tokens served from cache. Everything else either explains that number or guards platform overhead and density.

**Metrics (Prometheus, `/metrics`, `internal/obs`)**

| Metric | What it tells you |
| --- | --- |
| `cache_break_tier` (histogram of `FirstChanged`; 5 = unchanged) | Which tier breaks the prefix, and how often |
| `provider_cached_token_ratio` | The actual hit rate per model call |
| `predicted_cached_token_ratio` | The planner's prediction per turn; compare with the above |
| `cache_prediction_misses_total` | Calls whose reported cached share fell well below the prediction |
| `provider_input_tokens_total`, `provider_cached_tokens_total`, `provider_output_tokens_total` | Token totals as providers report them |
| `turn_overhead_ms` | Time from debounce firing to the model request being sent, i.e. pure platform cost |
| `turns_total{outcome}` | `ok`, `error`, `interrupted`, `replanned`, `step_limit` |
| `checkpoints_total{outcome}`, `delegations_total{mode}` | Tail folds; sub-agents started by `spawn` or `fork` |
| `recall_stage1_ms`, `rerank_ms`, `rerank_batch_size` | Recall cost per turn |
| `embed_queue_depth{model,priority}`, `infer_batch_size{model}` | Whether background indexing is starving critical requests; batching effectiveness |
| `hydrated_actors`, `hydrate_ms`, `hibernations_total{reason}` | Density and churn (`idle`, `pressure`, `shutdown`, `write_failed`) |
| `writer_batch_size`, `commit_ms` | Group-commit effectiveness |
| `env_lease_ms{tier}`, `speculative_restore_hits` | REPL cold-start cost |

Logging goes through `log/slog`; lines in a turn carry the contact and turn IDs, never message bodies.

**Live context diagnostics.** With `-diagnostics` on, `/debug/plans` streams what each call sent, tier by tier, with recall decisions, checkpoints and the cache prediction against what the provider reported; `ctxtop` is a terminal viewer for it. Captures are in memory only and bounded. Diagnostic mode exposes full prompts over the same access as the API, so it is off by default. See [CONTRIBUTING.md](../CONTRIBUTING.md#live-context-diagnostics).

**Benchmarks.** `mise run bench` runs the Go benchmarks; the nightly CI job runs them and compares against the last run. `cmd/replay` is a cache simulator and recall benchmark: it replays the synthetic transcripts in `testdata/transcripts` (multi-topic conversations with labelled callbacks such as "back to the Postgres thing") through the real planner against a fake provider with a breakpoint-style cache, and sweeps τ, gap and θ. `cmd/fakellm` is a standalone fake model server for load tests. Results are under Measured.

## Repo layout

Ports and adapters, one module. Domain packages hold the logic and declare the small interfaces they need; adapters satisfy them; only `cmd/contactd` sees both. `depguard` turns a layering violation into a lint failure. Everything lives under `internal/`, so nothing is importable from outside the module.

```text
cmd/contactd      wiring, signals, graceful shutdown
cmd/replay        cache simulator + recall benchmark over testdata/transcripts
cmd/fakellm       fake model server (OpenAI wire format) for load tests
cmd/ctxtop        the live context diagnostics viewer
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
internal/helpers  A2A helper agent: durable tasks, scheduler, outbox and transport adapter
internal/requester  SDK client tools, callback authentication and durable notices
internal/fetch    bounded web fetches with dial-time private-address filtering
internal/tui/helperboard  pure helper-strip and wire-history reducer
internal/api      HTTP, SSE, WebSocket; apitest/ runs it for tests of its clients
internal/client   the API from the other side: requests, one WebSocket, a session that reconnects, a contact's files
internal/transcript  log events + live deltas -> one conversation (pure)
internal/tui      the screens of `contactd chat` (Bubble Tea), and under it one package for each part of them
internal/ctxtop   the diagnostics viewer's model and views
internal/diagnostic  in-memory capture of plans and calls for /debug/plans
internal/obs      Prometheus metrics
internal/config   one Config struct from flags and environment
internal/e2e      integration tier (tag integration)
sql/schema        migrations, embedded and applied via PRAGMA user_version
sql/queries       queries compiled to typed Go by sqlc
examples/mods     three mods for the terminal client, as source
```

Code patterns, linting and the test tiers are in [CONTRIBUTING.md](../CONTRIBUTING.md#code-patterns).

## Not built yet

- Profile updates after a contact is created: who writes tier 1, and how to batch edits so they protect the cache.
- Forgetting. The log is append-only; deleting user data needs tombstone events plus a rebuild of vectors and episodes.
- Multilingual models. bge-small-en is English-only; another language needs a small multilingual embedder and reranker and a rerun of the recall benchmark.
- Multi-node routing.
- Tier-0 interpreter variables that survive the end of a lease (snapshotting wazero linear memory and globals). Today only files persist.

## Measured

On an Apple M-series laptop (18 cores, darwin/arm64), Go race detector off unless noted. These are first measurements on synthetic data, there to check the targets, not to stand as benchmarks of record. `mise run bench` reproduces the first table; `go run -tags ORT ./cmd/replay -infer ort -sweep` the second.

| What | Target | Measured |
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

Tier-0 Python starts in about 48 ms, not single-digit milliseconds. That is CPython's own initialization; the platform's overhead is about 1.5 ms, which is what QuickJS shows.

**Cache simulator and recall benchmark** (`cmd/replay`), over 12 generated transcripts in `testdata/transcripts` (204 conversations, 632 turns, 8 topics, callbacks labelled), real int8 models, breakpoint-style provider cache, and a deliberately tiny 1,500-token tail so that compaction and recall happen at all:

| Planner | Input tokens cached | Cached on warm turns | Tier-2 / tier-3 breaks |
| --- | --- | --- | --- |
| Step compaction + hysteresis (the design) | 58.4% | **86.6%** | 45 / 17 |
| Sliding window + hysteresis | 37.3% | 55.2% | 28 / 168 |

"Warm" means within the cache TTL of the previous turn, which is the steady state the 80% target is about; the overall figure is lower only because the first turn of every conversation starts cold. With the default 24k-token tail these short transcripts never compact, and the warm share is 94.5%. The planner's own prediction (58.2%) matches what the simulated provider reported (58.4%).

- **Step versus slide is the big lever**: 86.6% against 55.2%. Hysteresis is worth under a point here (86.6% against 85.8% without it); it matters more when rerank scores are noisier.
- **Rerank cost depends on passage length far more than on anything else.** At the full 256-token bound a 20-pair rerank is about 100 ms even when spread over five workers, which leaves no room in a 100 ms budget. If real episodes run that long, lower `PassageTokens` or the shortlist before anything else.
- **Segmentation** produced 211 episodes for 204 conversations at the starting τ = 0.45 and 30-minute gap.
- **int8 against fp32:** identical at the default threshold (83% recall after rerank, 86.6% against 86.3% warm cached). At θ = -2 and +2 fp32 recalls one more of the 12 callbacks than int8, which is within noise at this sample size but worth re-checking on harder data. int8 is 10-25% slower than fp32 on the arm64 hardware measured; x86 with VNNI was not measured.
- **Rerank does not beat stage 1 on this data, so the first recall target is not met.** Recall@4 on the 12 labelled callbacks is 100% for the bi-encoder alone and 83% after the cross-encoder at θ = 0 (92% at θ = -2). Eight cleanly separated topics are too easy for stage 1 to miss, so all the reranker can do here is veto. Transcripts with overlapping topics are needed before θ is worth tuning; the sweep is built for that.
- **Passage content mattered more than any threshold.** Quoting only an episode's first and last message gave 58% recall after rerank; filling the same 256-token budget with as much of the episode as fits gave 83%.

**Run against live providers:** the probe results in [PROVIDERS.md](PROVIDERS.md), and one conversation through the real binary on `deepseek-flash`: two turns that each ran Python in the tier-0 sandbox, then a delegated sub-agent whose result came back as a notice. Reasoning was stored and replayed on every later request, and the provider's reported cache hits tracked the predictions (for example 512 of 649 input tokens reported cached against 290 of 384 estimated).

**Not measured:** the Anthropic and Ollama Cloud profiles against their live endpoints (tested against written fixtures and the conformance suite only), the tier-1 sandbox client against a real service, checkpoints and forks against a live provider's cache, and any latency on linux/amd64 (CI passes there; nothing was timed). The container image was built and run on linux/arm64 only.

## Helpers over A2A

A helper is an unattended contact owned by a durable A2A task. The actor retains exclusive ownership of conversation state; adapters request brief delivery or a scheduled check through narrow ports. Turn outcomes update the task, and an outbox derives complete report artifacts from committed log events. Per-task serialization freezes terminal state and the final report boundary. JSON-RPC and HTTP+JSON use the same SDK-backed handler.

The requester discovers an agent card and calls it over HTTP, including for a local agent. It persists callback credentials before sending and converts authenticated reports into queued notices behind the parent's ongoing reply. A separate notification cursor prevents list refreshes from consuming questions or completion notices. Ephemeral helper deltas feed the terminal strip and wire overlay without waking contacts. See [A2A](A2A.md) for state transitions, metrics, security, and precise restart limitations.
