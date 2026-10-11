# Contact PaaS — Go Design

Oct 11, 2026 · @Jack

## Overview

We build a single Go binary, backed by SQLite and two small local ONNX models, that lets a client talk to an AI contact the way it would talk to a person. There is one continuous relationship per contact. Threads, prompt-cache layout and the REPL environment are not stored objects; each turn derives them just in time from one append-only event log.

The doc is built up piece by piece. Each piece adds one capability on top of the last, so the system can be developed and tested in that order.

**Goals**

- A client sends messages to a contact ID; it never creates, picks or names a thread.
- Relevant past context is recalled automatically, using local embedding and reranking with no external vector DB.
- Every turn is laid out for maximum prompt-cache reuse, and cache hit rate is a first-class metric.
- A REPL environment exists only when the model needs one, and its state persists across days as a snapshot.
- Idle contacts cost zero memory and zero goroutines.
- Everything runs in one container with one volume.

**Non-goals for v1**

- Multi-node clustering. The design keeps a single writer per contact so sharding can be added later without changing anything above the log.
- Hosting LLM inference. Model calls go to a provider API or a separate inference server.
- Running heavyweight sandboxes inside the container. Those are delegated to an external snapshot-sandbox service.

## First principles

The model call dominates every turn, so the platform wins by avoiding model work rather than by shaving its own microseconds. The budget below uses approximate figures, to be replaced with measured ones in the validation benchmarks.

| Step in a turn | Approx. cost | Who controls it |
| --- | --- | --- |
| Hydrate a cold contact (SQLite range scan) | 1–5 ms | Platform |
| Embed the new message (bi-encoder, CPU) | 3–10 ms | Platform |
| Stage-1 recall (brute-force cosine, ≤5k vectors) | under 1 ms | Platform |
| Stage-2 rerank (cross-encoder, \~20 pairs, one batch) | 20–60 ms | Platform |
| Build and hash the prompt plan | under 1 ms | Platform |
| Model time to first token, prefix cached | 0.3–1 s | Platform, via cache layout |
| Model prefill, \~50k tokens uncached | several seconds | Platform, via cache layout |
| REPL: in-process WASM / remote snapshot restore / cold boot | ms / 100s of ms / seconds | Platform, via env tiering |

That table gives three levers, in order of impact:

1. **Prompt-cache hit rate.** One avoided cold prefill is worth more than every other optimization in this doc combined.
2. **REPL cold start.** Prefer restoring a snapshot over booting, and prefer in-process execution over remote.
3. **Idle cost per contact.** Zero goroutines and zero heap for contacts that aren't talking, so density scales with active contacts, not total ones.

**Why Go is a good fit.** The hot path is I/O plus small CPU bursts, which goroutines handle well. `context.Context` makes interrupting a turn safe and simple, and interruption is a core feature here. The two known costs have known mitigations:

- **GC headroom.** Set `GOMEMLIMIT` explicitly, because unlike GOMAXPROCS it does not read cgroup limits. Leave room for ONNX Runtime's native memory, which sits outside the Go heap.
- **cgo.** ONNX Runtime and the Rust tokenizer need cgo. We accept that once and use the cgo SQLite driver as well, rather than paying for two worlds.

## Core model

Only the event log is stored; threads, cache layout and environments are pure functions of it, evaluated per turn.

- **Contact**: a durable identity with exactly one append-only log. It is the only thing a client addresses.
- **Event**: anything that happens to a contact: a user or agent message, a tool call or result, a summary, an environment snapshot.
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
)

type Event struct {
    Contact ContactID
    Seq     uint64 // monotonic per contact; assigned by that contact's actor
    TS      int64  // unix ms
    Kind    EventKind
    Body    []byte // JSON or msgpack, opaque to the log
}
```

The one invariant everything depends on: **each contact has exactly one writer at a time.** Piece 2 enforces it in-process. A future cluster enforces it by routing each contact ID to a single node.

## Piece 1: Event log on SQLite

One SQLite file in WAL mode, a single writer goroutine doing adaptive group commit, and a separate pool of readers. Because cgo is already required for ONNX Runtime, we use the cgo driver `github.com/mattn/go-sqlite3`, which is the fastest Go option.

**Schema.** The key leads with the contact, so one contact's history is a single sequential range scan.

```sql
CREATE TABLE contacts (
  id INTEGER PRIMARY KEY, agent TEXT NOT NULL,
  profile BLOB, last_active INTEGER NOT NULL
);

CREATE TABLE events (
  contact INTEGER NOT NULL, seq INTEGER NOT NULL, ts INTEGER NOT NULL,
  kind INTEGER NOT NULL, body BLOB NOT NULL,
  PRIMARY KEY (contact, seq)
) WITHOUT ROWID;

-- one 384-d float32 vector per message event, written after embedding
CREATE TABLE vectors (
  contact INTEGER NOT NULL, seq INTEGER NOT NULL, vec BLOB NOT NULL,
  PRIMARY KEY (contact, seq)
) WITHOUT ROWID;

-- derived index; rebuildable from events + vectors at any time
CREATE TABLE episodes (
  contact INTEGER NOT NULL, ep INTEGER NOT NULL,
  start_seq INTEGER NOT NULL, end_seq INTEGER NOT NULL,
  centroid BLOB NOT NULL, summary_seq INTEGER,
  PRIMARY KEY (contact, ep)
) WITHOUT ROWID;
```

**Pragmas** on every connection: `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`, `temp_store=MEMORY`, `mmap_size=268435456`, and a negative `cache_size` in KiB. In WAL mode, `synchronous=NORMAL` can lose the last few commits on power loss but never corrupts the file. Use `FULL` instead if a lost message is unacceptable; group commit amortizes the extra fsync either way.

**Two pools.** The writer pool has `SetMaxOpenConns(1)`. Readers share a pool sized to GOMAXPROCS, used only for hydration and rebuilds.

**Adaptive group commit.** The writer blocks for the first request, then drains whatever else is already queued, and commits it all in one transaction. When idle, one event costs one commit. Under load, batches grow by themselves, with no fixed timer adding latency.

```go
type appendReq struct {
    ev   Event
    vec  []float32 // optional; set when the embedding is ready
    done chan error
}

func (w *Writer) Run(ctx context.Context) {
    batch := make([]appendReq, 0, 512)
    for {
        select {
        case <-ctx.Done():
            return
        case r := <-w.in:
            batch = append(batch[:0], r)
        drain:
            for len(batch) < cap(batch) {
                select {
                case r := <-w.in:
                    batch = append(batch, r)
                default:
                    break drain
                }
            }
            err := w.commit(batch) // one tx, prepared INSERTs
            for _, r := range batch {
                r.done <- err
            }
        }
    }
}
```

Actors don't wait on the commit before streaming. They wait on `done` only before acknowledging a message to the client as durable.

## Piece 2: Contact actors

Each contact that is currently talking gets one goroutine that owns all of its state. Every other contact is just rows on disk. Because the actor is the only writer, it assigns sequence numbers without locks, and two turns for the same contact can never race.

```go
type Cmd interface{ isCmd() }

type Inbound struct{ Text string; Ack chan<- uint64 } // ack = durable seq
type TurnDone struct{ ID uint64; Res TurnResult; Err error }
type Subscribe struct{ Out chan<- Delta }
type Hibernate struct{} // sent by the registry under memory pressure

type actor struct {
    id       ContactID
    inbox    chan Cmd       // buffered, e.g. 64
    dead     chan struct{}  // closed when the actor retires
    nextSeq  uint64
    tail     []Event        // hot recent window (Piece 5 budget)
    eps      *EpisodeIndex  // Piece 4
    lastPlan *PromptPlan    // Piece 5
    env      *Lease         // Piece 6, nil until needed
    subs     *fanout        // streaming subscribers
    turn     *inflight      // {id, cancel context.CancelFunc}
    debounce *time.Timer    // Piece 7
}
```

**The run loop** multiplexes the mailbox, the debounce timer and the idle timer in one `select`, so the actor never blocks on anything slow. Turns run in child goroutines and report back as a `TurnDone` command.

```go
func (a *actor) run(r *Registry) {
    idle := time.NewTimer(IdleTTL)
    for {
        select {
        case c := <-a.inbox:
            a.handle(c)
            resetTimer(idle, IdleTTL)
        case <-a.debounce.C:
            a.startTurn() // Piece 8
        case <-idle.C:
            if a.turn != nil {
                resetTimer(idle, IdleTTL)
                continue
            }
            if r.retire(a) { // removes a from the map, closes a.dead
                a.hibernate()           // snapshot env, flush, drop memory
                r.forwardLeftovers(a)   // re-send anything that slipped into inbox
                return
            }
        }
    }
}
```

**Registry.** A sharded map (64 shards, each a `map[ContactID]*actor` behind a mutex) is enough. Sending retries if it races with a retiring actor. Retirement then drains and re-forwards the inbox, so no message is lost in that window.

```go
func (r *Registry) Send(ctx context.Context, id ContactID, c Cmd) error {
    for {
        a := r.getOrSpawn(id) // hydrates on spawn
        select {
        case a.inbox <- c:
            return nil
        case <-a.dead:
            continue // raced with retirement; spawn a fresh one
        case <-ctx.Done():
            return ctx.Err()
        }
    }
}
```

**Hydration** reads two things on spawn: the tail (the last N events, a single range scan) and the episode rows. Message vectors stay on disk. They are only needed to rebuild episodes or to choose excerpts within an episode.

**Memory budget.** Episode centroids are held in memory as int8 with a per-vector scale: 384 bytes each, versus 1,536 bytes as float32. That's safe because the cross-encoder rerank in Piece 4 re-scores the shortlist at full precision. The registry also keeps an approximate LRU of active actors. When the count passes `MaxHydrated`, it sends `Hibernate` to the least recently active idle ones. A traffic spike then degrades into rehydration cost instead of an OOM kill.

## Piece 3: Local embedder + cross-encoder reranker

We run two small ONNX models, not one, because a cross-encoder cannot produce embeddings. It scores a (query, passage) pair jointly, which makes it accurate but O(n) model passes per query. So a bi-encoder handles cheap recall over everything, and the cross-encoder re-scores only a shortlist of about 20.

| Role | Default model | Size | Output | Runs per turn |
| --- | --- | --- | --- | --- |
| Embedder (bi-encoder) | BAAI/bge-small-en-v1.5 (alt: all-MiniLM-L6-v2) | \~33M params, 384-d | one vector per text | 1 on the critical path, plus background indexing |
| Reranker (cross-encoder) | cross-encoder/ms-marco-MiniLM-L6-v2 | \~22M params | one relevance logit per pair | 1 batch of \~20 pairs |

**Runtime: Hugot on the ONNX Runtime backend.** [Hugot](https://github.com/knights-analytics/hugot) provides HF-compatible `featureExtraction` and `crossEncoder` pipelines in Go. Its ORT backend (build tag `ORT`) is the fastest of its three; it needs `libonnxruntime.so` plus the statically linked Rust `tokenizers.a`. Its pure-Go backend is scoped to small models and is only worth it if cgo ever becomes unacceptable. Export both models to ONNX with HF Optimum, then try int8 dynamic quantization. Keep it only if the recall benchmark (Observability section) shows no loss.

**One micro-batcher per model.** This uses the same drain pattern as the SQLite writer, capped at 32 inputs per call (Hugot's suggested starting point). There are two queues: critical-path requests (the query embedding, rerank) are always drained before background indexing.

```go
type Priority uint8
const (Critical Priority = iota; Background)

type Embedder interface {
    Embed(ctx context.Context, texts []string, p Priority) ([][]float32, error)
}
type Reranker interface {
    Score(ctx context.Context, query string, passages []string) ([]float32, error)
}

func (b *batcher) loop() {
    for {
        var batch []job
        select { // critical first, then background
        case j := <-b.critical:
            batch = append(batch, j)
        default:
            select {
            case j := <-b.critical:
                batch = append(batch, j)
            case j := <-b.background:
                batch = append(batch, j)
            }
        }
        batch = drainUpTo(batch, b.critical, b.background, 32)
        out, err := b.pipeline.Run(flatten(batch)) // one ORT call
        scatter(batch, out, err)
    }
}
```

**Threading.** Start with K worker loops sharing each pipeline, each ORT call pinned to one intra-op and one inter-op thread. K is the number of cores reserved for inference, for example half of GOMAXPROCS. Hugot recommends this configuration for throughput. Benchmark it against a single loop with multi-threaded intra-op, which may give lower per-call latency.

**What gets embedded.**

- **Messages:** every user and agent message, embedded in the background after append; the vector is written through the group-commit writer.
- **The recall query:** the new message alone is often too vague ("ok do that again"), so the query is the new message plus the previous agent message, truncated to about 128 tokens.
- **Rerank passages:** the episode summary if one exists, otherwise the episode's first and last messages, truncated so each pair stays under about 256 tokens. Bounding pair length bounds rerank cost.

## Piece 4: JIT threads

A "thread" is whatever this turn recalls: the hot tail plus up to four past episodes that clear a relevance threshold. Episodes are segmented automatically as messages arrive; nobody names them or chooses one.

**Segmentation.** Each embedded message either extends the current episode or opens a new one. Two rules decide:

- **Time gap.** More than 30 minutes since the episode's last message opens a new episode.
- **Topic drift.** Cosine similarity to the running centroid falls below τ for **two consecutive** messages. Requiring two stops a single off-topic aside from splitting an episode.

τ and the gap are starting values, tuned with the recall benchmark.

```go
type Episode struct {
    ID         uint32
    Start, End uint64     // seq span
    Centroid   [384]int8  // quantized, + Scale
    Scale      float32
    N          uint32     // members, for the running mean
    LastTS     int64
    SummarySeq uint64     // 0 until summarized lazily
}

func (x *EpisodeIndex) OnEmbedded(ev Event, v []float32) (changed *Episode) {
    cur := x.current()
    drift := cosine(v, cur.dequant()) < Tau
    if ev.TS-cur.LastTS > GapMS || (drift && x.lastDrifted) {
        x.lastDrifted = false
        return x.open(ev, v)
    }
    x.lastDrifted = drift
    return x.extend(cur, ev, v) // running mean, requantize
}
```

The changed episode row is upserted through the group-commit writer. The episodes table is derived data, so a corrupt or outdated index is fixed by replaying `events` and `vectors`.

**Two-stage recall** runs at the start of each turn.

1. **Stage 1, bi-encoder.** Take the int8 dot product of the query against every centroid and keep the top 20. Skip episodes that overlap the tail, since they're already in context. At 5,000 episodes × 384 dims this is about 2M multiply-adds, comfortably under a millisecond in plain Go, so no ANN index is needed.
2. **Stage 2, cross-encoder.** Score (query, passage) for those 20 in one batch. Add a small recency bonus: `score = logit + λ·exp(−age/halfLife)`.
3. **Threshold.** Keep at most K = 4 episodes, and only those above θ. Often nothing clears it, and recalling nothing is correct: a person doesn't bring up unrelated history.

```go
func (a *actor) recall(ctx context.Context, qText string, q []float32) ([]*Episode, error) {
    cands := a.eps.TopK(q, 20, a.tailStart())
    if len(cands) == 0 {
        return nil, nil
    }
    passages := a.passages(ctx, cands) // summary, or first+last message excerpt
    scores, err := a.deps.Rerank.Score(ctx, qText, passages)
    if err != nil {
        return fallbackStage1(cands, 2), nil // degrade gracefully
    }
    return keepAbove(cands, withRecency(scores, cands), Theta, 4), nil
}
```

**Lazy summaries.** When a closed episode with no summary is recalled, the excerpt is used this turn. A background job also asks a small, cheap model for a summary. The result is appended as a `KindSummary` event and linked from the episode row. Episodes that are never revisited never cost a summarization call.

## Piece 5: JIT cache

Every prompt is assembled from five tiers ordered least- to most-volatile, so most turns change only the last tier or two and reuse the cached prefix. Provider caches and local ones (vLLM automatic prefix caching, SGLang RadixAttention) all match byte-identical prefixes, so the layout is the optimization.

| # | Tier | Changes when | Typical churn |
| --- | --- | --- | --- |
| 0 | Static: system prompt + tool schemas | Agent config is deployed | Rarely |
| 1 | Contact profile / long-term notes | Profile is edited | Days |
| 2 | Recalled episodes | The topic changes | Every few turns |
| 3 | Tail (append-only) | Each turn appends; compaction resets it | Append every turn, reset every N turns |
| 4 | Live: new message(s), current time | Every turn | Always |

Providers that take explicit breakpoints get one at the end of tiers 0–3. Anthropic, for example, allows up to four, which is exactly enough.

```go
type TierKind uint8
const (TierStatic TierKind = iota; TierProfile; TierRecall; TierTail; TierLive)

type Tier struct {
    Msgs []Message
    Hash uint64 // xxhash of canonical bytes
}

type PromptPlan struct {
    Tiers    [5]Tier
    Chain    [5]uint64 // Chain[i] = H(Chain[i-1], Tiers[i].Hash)
    Recall   []uint32  // episode IDs in tier 2, in order
    TailFrom uint64    // first seq in tier 3
    BuiltAt  int64
}

// FirstChanged reports where the cached prefix breaks versus prev.
func (p *PromptPlan) FirstChanged(prev *PromptPlan) int {
    if prev == nil {
        return 0
    }
    for i := range p.Chain {
        if p.Chain[i] != prev.Chain[i] {
            return i
        }
    }
    return len(p.Chain)
}
```

**Byte stability rules.** Most accidental cache misses come from small nondeterminism, so:

- No timestamps, request IDs or "current date" in tiers 0–3. They go in tier 4.
- Canonical JSON: sorted keys, fixed float formatting, and tool schemas in a fixed order.
- Recalled episodes are rendered from their stored summary or excerpt, never regenerated text.

**Rule 1: hysteresis on recall.** Keep last turn's recall set unchanged while the cache is probably warm (within `CacheTTL`) and every previous episode is still in the fresh top 2K. Switch only if a new episode beats all of them by margin δ. A slightly better recall is not worth a full re-prefill of tiers 2–4.

```go
func chooseRecall(prev *PromptPlan, fresh []Scored, now int64) []uint32 {
    if prev != nil && now-prev.BuiltAt < CacheTTL &&
        allIn(prev.Recall, topN(fresh, 2*K)) &&
        !beatsAll(fresh, prev.Recall, Delta) {
        return prev.Recall
    }
    return ids(topN(fresh, K))
}
```

**Rule 2: compact in steps, never slide.** A sliding window drops the oldest message every turn, which changes the start of tier 3 and breaks the cache every turn. Instead, the tail grows until it passes its token budget. Then the oldest \~30% is cut at a turn boundary and leaves the prompt; it stays recallable as part of its episodes. The result is one tier-3 miss per N turns instead of one per turn.

**Self-hosted models.** Route each contact to the same inference replica by consistent hashing on `ContactID`, so its KV prefix is still resident on that GPU.

**The metric.** Each turn logs `FirstChanged` (the predicted break point) alongside the cached-token count the provider reports. The gap between predicted and actual is how cache bugs get caught.

## Piece 6: JIT REPL environments

No environment exists until the model emits a tool call that needs one. Between uses, a contact owns its environment only as a snapshot ID in the log. Execution is split across two tiers, because an ordinary container has no `/dev/kvm` and can't host microVMs.

| Tier | Mechanism | Start cost | State across hibernation | Use for |
| --- | --- | --- | --- | --- |
| 0, in-process | wazero (pure-Go WASM) running a WASI build of Python or a JS engine | ms (precompiled module, fresh instance) | Files persist; interpreter variables do not | Math, parsing, data munging, quick scripts |
| 1, remote | External snapshot-sandbox service (microVMs on bare metal) | 100s of ms to restore | Full memory + disk snapshot | Native toolchains, packages, network, long jobs |

```go
type EnvSpec struct {
    Tier     uint8    // 0 or 1
    Base     string   // "python-3.12", "node-22", "re-ghidra" ...
    Packages []string
    Network  bool     // tier 1 only
}

type EnvProvider interface {
    Lease(ctx context.Context, spec EnvSpec, resume SnapshotID) (Lease, error)
}

type Lease interface {
    Exec(ctx context.Context, code string) (ExecResult, error)
    Snapshot(ctx context.Context) (SnapshotID, error) // then Release
    Release()
}

func (a *actor) ensureEnv(ctx context.Context, spec EnvSpec) (Lease, error) {
    if a.env != nil && a.envSpec.Compatible(spec) {
        return a.env, nil
    }
    resume := a.lastSnapshot(spec) // from the latest KindEnvSnapshot event
    l, err := a.deps.Envs[spec.Tier].Lease(ctx, spec, resume)
    if err != nil {
        return nil, err
    }
    a.env, a.envSpec = l, spec
    return l, nil
}
```

**Tier 0 on wazero.**

- Compile each base module once at startup, sharing a compilation cache across runtimes.
- Each lease is a fresh instance, with memory capped through `WithMemoryLimitPages` and `WithCloseOnContextDone(true)` so the turn's context kills runaway code.
- The per-contact virtual filesystem is a directory on the volume, mounted as the guest's working directory.

**Tier 1** is a thin client to the snapshot-sandbox service. Its snapshot IDs are stored as `KindEnvSnapshot` events.

**Lifecycle.**

- A lease idle for 2 minutes is snapshotted and released, even while the actor stays hydrated. Hibernation does the same.
- **Speculative restore:** a streamed tool call reveals its name before its arguments finish. When that name implies a tier-1 environment, the actor starts the restore immediately, overlapping it with the rest of the stream.
- The tool result tells the agent what persisted, so it doesn't assume tier-0 variables survived a long gap.

## Piece 7: Person-like behavior

Two behaviors make a contact feel like a person rather than an endpoint. It waits for you to finish a burst of messages before replying. When you add something mid-reply, it re-plans instead of answering a stale question.

**Burst coalescing.** Every inbound message, and every typing indicator, resets a 700 ms debounce timer. A hard cap of 3 s after the first pending message keeps someone who types continuously from stalling the reply forever.

**Interruption policy** depends on what the in-flight turn is doing:

| Turn phase when a new message arrives | Action |
| --- | --- |
| Planning, or waiting for the first token | Cancel silently and restart after the debounce |
| Streaming text | Cancel. Persist the partial reply as an agent message flagged `interrupted`, so the model knows it was cut off. Restart. |
| Executing a tool | Don't kill it, since it may have side effects. Let it finish, append the result, then re-plan with the new message instead of continuing |

Restarting is cheap because of Piece 5. Tiers 0–3 are unchanged, so the re-plan only re-prefills the live tier.

```go
func (a *actor) handle(c Cmd) {
    switch c := c.(type) {
    case Inbound:
        a.appendAsync(KindUserMsg, c.Text, c.Ack)
        a.interrupt()
        a.armDebounce()
    case Typing:
        a.armDebounce() // respects the 3 s cap
    case TurnDone:
        if a.turn == nil || c.ID != a.turn.id {
            return // stale result from a cancelled turn
        }
        a.commitTurn(c.Res)
        a.turn = nil
    case Hibernate:
        a.requestRetire()
    }
}

func (a *actor) interrupt() {
    t := a.turn
    if t == nil {
        return
    }
    if t.phase.Load() == phaseTool {
        t.replanAfterTool.Store(true) // the turn checks this after Exec returns
        return
    }
    t.cancel() // context.CancelFunc; the stream and recall stop at their next select
}
```

Each turn carries a monotonically increasing ID, so a `TurnDone` that arrives after its turn was cancelled is ignored. The actor's own state is never written from a turn goroutine; only `commitTurn` on the actor goroutine writes it.

## Piece 8: The turn loop and API

A turn chains the pieces in order: embed the query (3), recall episodes (4), build the cache-aware plan (5), stream the model, lease an environment only when a tool call needs one (6), and stop or re-plan if interrupted (7).

&#91;embedded content: contact platform architecture · one actor per active contact\]

Requests enter on the left. During a turn, the actor calls the services on the right, and everything it produces goes back through the single writer.

```go
func runTurn(ctx context.Context, in TurnInput, d *Deps) (TurnResult, error) {
    qText := in.QueryText() // new msg + previous agent msg, ~128 tokens
    q, err := d.Embed.Embed(ctx, []string{qText}, Critical)
    if err != nil {
        return TurnResult{}, err
    }
    recalled, _ := recall(ctx, in.Episodes, qText, q[0], d.Rerank)    // Piece 4
    plan := BuildPlan(in.LastPlan, in, recalled, time.Now().UnixMilli()) // Piece 5

    for step := 0; step < MaxToolSteps; step++ {
        stream, err := d.LLM.Stream(ctx, plan)
        if err != nil {
            return TurnResult{Plan: plan}, err
        }
        for ev := range stream.Events() {
            switch ev.Type {
            case TextDelta:
                in.Out <- Delta{Text: ev.Text}
            case ToolStart: // name arrives before args: speculative restore
                if spec, ok := d.Tools.EnvFor(ev.Name); ok && spec.Tier == 1 {
                    in.Env.Prewarm(spec)
                }
            }
        }
        stop := stream.Stop()
        if stop.ToolCall == nil {
            return TurnResult{Final: stop.Text, Plan: plan}, nil
        }

        in.SetPhase(phaseTool)
        lease, err := in.Env.Ensure(ctx, stop.ToolCall.Env) // Piece 6
        if err != nil {
            return TurnResult{Plan: plan}, err
        }
        r, err := lease.Exec(ctx, stop.ToolCall.Code)
        in.Emit(KindToolCall, stop.ToolCall) // actor appends; stays durable mid-turn
        in.Emit(KindToolResult, r.OrError(err))
        plan.AppendTail(stop.ToolCall, r) // append-only, cache-safe
        if in.ReplanAfterTool() {
            return TurnResult{Plan: plan, Replan: true}, nil
        }
        in.SetPhase(phaseStream)
    }
    return TurnResult{Plan: plan, StepLimit: true}, nil
}
```

**Ownership rules** keep the turn goroutine from racing the actor:

- `in.Episodes` is an immutable snapshot of the index, published through an `atomic.Pointer`. The actor swaps in a new one after each change.
- Every event the turn produces goes back to the actor as a `TurnEvent` command, and only the actor appends to the log.
- The environment handle uses its own mutex. That's sufficient because a contact never has two turns in flight.

**API surface.** There are no thread endpoints; that's the point.

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/v1/contacts` | Create a contact with an agent config and an optional profile |
| POST | `/v1/contacts/{id}/messages` | Send a message; returns 202 with its durable `seq` |
| GET | `/v1/contacts/{id}/stream` | SSE stream of reply deltas, tool activity and typing state |
| GET | `/v1/contacts/{id}/live` | WebSocket: send messages and typing indicators, receive deltas |
| GET | `/v1/contacts/{id}/events?after={seq}` | Page through history |
| GET | `/metrics`, `/healthz` | Prometheus metrics, liveness |

Use `net/http` with Go 1.22+ pattern routing. No framework is needed, and the only extra dependency is a WebSocket library.

## Container and runtime configuration

One glibc-based distroless image holds the binary, `libonnxruntime.so` and the two quantized models, with a single volume at `/data` for SQLite and the tier-0 filesystems. Glibc rather than static or musl is required because ONNX Runtime ships as a shared library.

```dockerfile
FROM golang:1.25-bookworm AS build
# tokenizers.a and libonnxruntime.so from Hugot's release assets
COPY third_party/libtokenizers.a /usr/lib/
COPY third_party/libonnxruntime.so /usr/lib/
WORKDIR /src
COPY . .
RUN CGO_ENABLED=1 go build -tags ORT -trimpath -ldflags='-s -w' -o /out/contactd ./cmd/contactd

FROM gcr.io/distroless/cc-debian12
COPY --from=build /usr/lib/libonnxruntime.so /usr/lib/
COPY --from=build /out/contactd /contactd
COPY models/ /models/   # bge-small int8 + ms-marco-MiniLM int8 ONNX
VOLUME /data
ENTRYPOINT ["/contactd"]
```

**Runtime settings**

| Setting | Value | Why |
| --- | --- | --- |
| GOMAXPROCS | Unset | Go 1.25+ reads the cgroup CPU limit itself |
| GOMEMLIMIT | Container limit minus native reserve (e.g. 4 GiB limit → 3 GiB) | Not cgroup-aware. ONNX Runtime arenas and model weights live outside the Go heap. |
| Inference workers | About half of GOMAXPROCS | Leaves cores for request handling and SQLite |
| `MaxHydrated` | Derived from GOMEMLIMIT ÷ measured bytes per actor | Hibernation kicks in before GC thrash |
| Data volume | `/data/contacts.db`, `/data/fs/{contact}/` | SQLite needs a real filesystem with working locks, not an overlay or network mount |

**Graceful shutdown on SIGTERM**

1. Stop accepting new messages and return 503 from `/healthz`.
2. Give in-flight turns a grace period to finish. Cancel them at the deadline, persisting partial replies as interrupted.
3. Hibernate every actor, which snapshots environments and releases leases.
4. Drain the writer, close SQLite cleanly, and exit.

**Scaling past one container** keeps the single-writer rule. A router consistent-hashes `ContactID → node`, and the log moves from local SQLite to per-node SQLite with replication (for example Litestream to object storage) or to Postgres. Nothing above the log interface changes.

## Observability and validation

The headline metric is the share of input tokens served from cache. Everything else either explains that number or guards platform overhead and density.

**Metrics (Prometheus)**

| Metric | What it tells you |
| --- | --- |
| `cache_break_tier` (histogram of `FirstChanged`) | Which tier breaks the prefix, and how often |
| `provider_cached_token_ratio` | The actual hit rate; compare it with the predicted break tier |
| `turn_overhead_ms` | Time from debounce firing to the model request being sent, i.e. pure platform cost |
| `recall_stage1_ms`, `rerank_ms`, `rerank_batch_size` | Recall cost per turn |
| `embed_queue_depth{priority}` | Whether background indexing is starving critical requests |
| `hydrated_actors`, `hydrate_ms`, `hibernations_total` | Density and churn |
| `writer_batch_size`, `commit_ms` | Group-commit effectiveness |
| `env_lease_ms{tier}`, `speculative_restore_hits` | REPL cold-start cost |

**Benchmarks to run before tuning anything.** The targets below are starting goals, to be revised with real data.

| Benchmark | Method | Initial target |
| --- | --- | --- |
| Recall quality | Synthetic multi-topic transcripts with labeled callbacks ("back to the Postgres thing"). Compare recall@4 for stage 1 alone vs with rerank, and fp32 vs int8 models. Sweep τ, gap and θ. | Rerank clearly beats stage 1; int8 within noise of fp32 |
| Cache simulator | Replay transcripts through the planner against a fake LLM. Compute the cached share of input tokens for sliding vs step compaction, with hysteresis on and off. | ≥ 80% of input tokens cached at steady state |
| Density | Hydrate 10k actors with realistic tails and episode counts; measure RSS per actor | Sets `MaxHydrated` |
| Platform overhead | Load test with the fake LLM; measure p50/p99 `turn_overhead_ms` | p99 under 100 ms including rerank |
| Writer throughput | Events/s and commit latency across concurrency levels | Batch size grows with load; idle latency stays at a single commit |
| Environments | Tier-0 instance start; tier-1 restore; latency saved by speculative restore | Tier 0 in single-digit ms |

The fake LLM and the cache simulator are worth building first. They let Pieces 4 and 5 be tuned for free, without spending provider tokens.

## Repo structure

One Go module with a ports-and-adapters layout. Domain packages (the pieces of this doc) hold all the logic and import only `core` and the standard library. Infrastructure packages implement the interfaces the domain declares. Only `cmd/contactd` sees both sides and wires them together.

```text
contactd/
├── cmd/
│   ├── contactd/          # main: config, wiring, signals, graceful shutdown
│   ├── replay/            # cache simulator + recall benchmark over testdata transcripts
│   └── fakellm/           # standalone fake model server for load tests
├── internal/
│   ├── core/              # ContactID, Event, EventKind, Message, sentinel errors; no deps
│   ├── actor/             # Piece 2 + 7: actor loop, registry, debounce, interruption
│   ├── turn/              # Piece 8: the turn loop; declares LLM, EnvLeaser ports
│   ├── recall/            # Piece 4: episodes, segmentation, two-stage recall; declares Embedder, Reranker
│   ├── plan/              # Piece 5: tiers, canonical bytes, hashing, hysteresis, compaction (pure)
│   ├── eventlog/          # Piece 1: SQLite store, group-commit writer, migrations
│   │   └── sqlcgen/       # generated by sqlc; never edited by hand
│   ├── infer/             # Piece 3: micro-batcher
│   │   ├── hugot/         # ONNX Runtime adapter (build tag: ort)
│   │   └── fake/          # deterministic hash-based vectors for tests
│   ├── llm/
│   │   ├── anthropic/     # provider adapters: plan tiers → cache breakpoints
│   │   ├── openai/
│   │   └── fake/          # scripted streams, tool calls, usage with cached-token counts
│   ├── env/
│   │   ├── wasm/          # Piece 6 tier 0: wazero
│   │   └── remote/        # Piece 6 tier 1: snapshot-sandbox client
│   ├── api/               # HTTP, SSE, WebSocket handlers
│   ├── obs/               # slog setup, Prometheus metrics
│   └── config/            # one Config struct from env + flags
├── sql/
│   ├── schema/            # 0001_init.sql, 0002_… (embedded, applied via PRAGMA user_version)
│   └── queries/           # *.sql consumed by sqlc
├── testdata/transcripts/  # replay fixtures with labeled callbacks
├── scripts/               # fetch-native-deps.sh, export_onnx.py, quantize.py (Python run via uv)
├── third_party/           # gitignored; libonnxruntime.so + libtokenizers.a, pinned by checksum
├── models/                # gitignored; ONNX files fetched by a mise task
├── deploy/Dockerfile
├── .golangci.yml
├── sqlc.yaml
├── mise.toml              # pinned toolchain, env, and every task
├── mise.lock              # exact tool versions + checksums, committed
└── go.mod                 # go 1.25
```

&#91;embedded content: package layering · allowed and blocked import directions\]

Dependencies only point inward and downward. A domain package that reaches for an adapter fails lint, so the domain stays testable with fakes alone.

**Naming choices**

- `eventlog`, not `log`, to avoid shadowing the standard library package.
- Everything lives under `internal/`, so nothing is importable from outside the module until we deliberately promote a package.
- Adapters are named after what they wrap (`hugot`, `wasm`, `anthropic`). Each one has a `fake` sibling, so every port has a test double that ships with the code.

**Toolchain: mise**

mise pins every developer tool and replaces Make or just as the task runner, so one file defines how the repo is built, linted and tested. Run `mise install` and every tool is at the version CI uses. The exact versions and checksums are committed in `mise.lock` (generated by `mise lock`).

```toml
# mise.toml
[tools]
go = "1.25"
golangci-lint = "2"
sqlc = "1"
"go:golang.org/x/vuln/cmd/govulncheck" = "latest"
"pypi:sqlfluff" = "3"
uv = "latest"          # runs scripts/*.py (ONNX export, quantization)

[env]
CGO_ENABLED = "1"
CGO_LDFLAGS = "-L{{config_root}}/third_party/lib"
LD_LIBRARY_PATH = "{{config_root}}/third_party/lib"
ONNXRUNTIME_LIB_DIR = "{{config_root}}/third_party/lib"

[tasks."deps:native"]
description = "Fetch pinned libonnxruntime.so + libtokenizers.a into third_party/"
run = "./scripts/fetch-native-deps.sh"
sources = ["scripts/fetch-native-deps.sh"]
outputs = ["third_party/lib/libonnxruntime.so", "third_party/lib/libtokenizers.a"]

[tasks."models:fetch"]
description = "Download, export and quantize the embedder + reranker"
run = "uv run scripts/export_onnx.py --out models/"

[tasks.fmt]
run = "golangci-lint fmt"

[tasks.lint]
run = ["golangci-lint fmt --diff", "golangci-lint run", "sqlfluff lint sql/ --dialect sqlite"]

[tasks.gen]
run = "sqlc generate"

[tasks."gen:check"]
run = ["go mod tidy -diff", "sqlc diff"]

[tasks.test]
run = "go test -race -shuffle=on ./..."

[tasks."test:all"]
depends = ["deps:native", "models:fetch"]
run = "go test -race -shuffle=on -tags ort,integration ./..."

[tasks.vuln]
run = "govulncheck ./..."

[tasks.bench]
run = ["go test -run '^$' -bench . ./internal/...", "go run ./cmd/replay --fixtures testdata/transcripts"]

[tasks.ci]
description = "Everything CI runs, in order"
run = ["mise run lint", "mise run gen:check", "mise run test:all", "mise run vuln"]
```

**What mise does not manage:** ONNX Runtime and the tokenizer static library are native C/Rust artifacts rather than CLI tools. The `deps:native` task fetches them at pinned versions and verifies their checksums. Its `sources`/`outputs` let mise skip the download when they're already present. The Dockerfile's build stage runs the same script, so the image and local builds link identical libraries.

## Code patterns

Ten rules, each chosen because it protects an invariant from earlier in this doc. Where a linter can enforce a rule, the next section names it.

**1. Ports live with the consumer.** Each domain package declares the small interface it needs, and adapters satisfy it implicitly. Keep each interface to the one to three methods actually called.

```go
// internal/recall/ports.go
type Embedder interface {
    Embed(ctx context.Context, texts []string, p Priority) ([][]float32, error)
}
```

**2. One goroutine owns actor state.** Fields on `actor` are touched only inside `run` and the methods it calls. Turn goroutines get immutable snapshots and talk back only through `Cmd` values. There are no mutexes in `internal/actor`; if one seems necessary, the ownership is wrong.

**3. Context is always the first parameter and never a struct field.** Every blocking `select` includes `<-ctx.Done()`. Cancellation is how interruption works (Piece 7), so a missed `ctx.Done()` is a real bug, not a style nit.

**4. `plan` is pure.** It takes a `now int64` argument, never calls `time.Now`, never ranges over a map to produce bytes, and never uses randomness. Golden tests pin the rendered bytes and the hash chain for a set of fixtures, so any change that would silently break the cache fails CI.

**5. Time in tests uses `testing/synctest`, which is stable in Go 1.25.** Debounce, idle timers and lease expiry run on the real `time` API inside a synctest bubble, so production code needs no `Clock` interface.

**6. Errors are wrapped, never swallowed.** Use `fmt.Errorf("…: %w", err)` at each boundary. Sentinel errors such as `core.ErrContactNotFound` are checked with `errors.Is`. `panic` is reserved for impossible states at startup.

**7. Logging goes through `log/slog`.** Every line in a turn carries `contact_id` and `turn_id`. Log message text and IDs, never message bodies; those are user data and already in the log store.

**8. No globals besides metrics.** Config is one struct built in `cmd/contactd` and passed down explicitly. Prometheus collectors are the single allowed exception, registered in `obs`.

**9. SQL is written as SQL.** Queries live in `sql/queries/` and are compiled to typed Go by sqlc. The group-commit writer's batched INSERT is the one hand-written statement, because it needs a reusable prepared statement inside the batch transaction.

**10. Fakes over mocks.** Every port has a hand-written fake (`infer/fake`, `llm/fake`) that behaves plausibly: deterministic vectors derived from a hash of the text, scripted streams that report cached-token counts. Tests assert outcomes (events appended, plan break tier), not call sequences.

**Test tiers**

| Tier | Build tag | Runs | Needs |
| --- | --- | --- | --- |
| Unit | none | Every push, `go test -race -shuffle=on ./...` | cgo for SQLite only |
| Inference | `ort` | Every push in the CI image | libonnxruntime.so, tokenizers.a, models |
| Integration | `integration` | Every push | Real SQLite file, fake LLM over HTTP |
| Benchmarks | none (`-bench`) | Nightly; results stored for trend | `testing.B.Loop` for recall, plan, writer |

## Linting, formatting and CI

One tool, golangci-lint v2, handles both formatting and linting. It starts from `default: none` and enables an explicit list, so every enabled linter maps to a rule above rather than to taste. The layering in Repo structure is enforced by `depguard`, which makes an architecture violation a CI failure rather than a review comment.

**Formatting**

- **Go:** gofumpt (a stricter superset of gofmt) plus gci with three import groups: standard library, third party, this module. Configure gopls with `gofumpt: true` so editors format the same way.
- **SQL:** sqlfluff with the `sqlite` dialect, over `sql/`.
- **Everything else:** an `.editorconfig` for YAML, Markdown and TOML.

**Linter → rule it protects**

| Linter | Protects |
| --- | --- |
| `depguard` | Layering: domain packages cannot import infrastructure, `database/sql`, `net/http`, Hugot or wazero |
| `forbidigo` | Rule 4 (no `time.Now` or `math/rand` in `plan`) and Rule 7 (no `fmt.Print*` or `log.*`) |
| `containedctx`, `contextcheck`, `noctx` | Rule 3: context is a parameter, propagated, and used for outbound HTTP |
| `errcheck`, `errorlint`, `nilerr`, `wrapcheck` | Rule 6: errors handled, wrapped with `%w`, compared with `errors.Is` |
| `sloglint` | Rule 7: no global logger, static messages, snake\_case keys |
| `gochecknoglobals`, `gochecknoinits` | Rule 8 (metrics in `obs` excluded) |
| `exhaustive` | Every `switch` over `EventKind`, `TierKind` or `Cmd` handles every case, so adding an event kind can't be silently ignored |
| `govet` (all analyzers except `fieldalignment`, `shadow`), `staticcheck`, `unused`, `ineffassign` | Correctness baseline, including copylocks |
| `bodyclose`, `sqlclosecheck`, `rowserrcheck` | Resource leaks on HTTP and SQLite |
| `musttag` | Event bodies always carry explicit JSON tags, which keeps tier bytes stable |
| `gosec` | Security baseline; matters most in `env/` and `api/` |
| `paralleltest`, `tparallel`, `thelper`, `usetesting` | Test hygiene; parallel tests also surface hidden shared state |
| `modernize`, `intrange`, `perfsprint`, `prealloc` | Idiomatic Go 1.25, cheap wins |
| `nolintlint` | Every `//nolint` names a linter and gives a reason |

**`.golangci.yml`**

```yaml
version: "2"
run:
  build-tags: [ort, integration]
  relative-path-mode: gomod
formatters:
  enable: [gofumpt, gci]
  settings:
    gofumpt:
      extra-rules: true
    gci:
      sections: [standard, default, localmodule]
linters:
  default: none
  enable:
    - depguard
    - forbidigo
    - containedctx
    - contextcheck
    - noctx
    - errcheck
    - errorlint
    - nilerr
    - wrapcheck
    - sloglint
    - gochecknoglobals
    - gochecknoinits
    - exhaustive
    - govet
    - staticcheck
    - unused
    - ineffassign
    - bodyclose
    - sqlclosecheck
    - rowserrcheck
    - musttag
    - gosec
    - paralleltest
    - tparallel
    - thelper
    - usetesting
    - modernize
    - intrange
    - perfsprint
    - prealloc
    - nolintlint
  settings:
    depguard:
      rules:
        domain-stays-pure:
          files:
            - "**/internal/core/**"
            - "**/internal/actor/**"
            - "**/internal/turn/**"
            - "**/internal/recall/**"
            - "**/internal/plan/**"
          deny:
            - pkg: database/sql
              desc: domain code talks to storage through ports
            - pkg: net/http
              desc: HTTP belongs in api/ and adapters
            - pkg: github.com/knights-analytics/hugot
              desc: inference goes through recall.Embedder / recall.Reranker
            - pkg: github.com/tetratelabs/wazero
              desc: execution goes through turn.EnvLeaser
            - pkg: github.com/quinnovator/contactd/internal/eventlog
              desc: adapters are wired in cmd/contactd only
            - pkg: github.com/quinnovator/contactd/internal/infer
              desc: adapters are wired in cmd/contactd only
            - pkg: github.com/quinnovator/contactd/internal/llm
              desc: adapters are wired in cmd/contactd only
            - pkg: github.com/quinnovator/contactd/internal/env
              desc: adapters are wired in cmd/contactd only
            - pkg: github.com/quinnovator/contactd/internal/api
              desc: nothing imports the API layer
    forbidigo:
      analyze-types: true
      forbid:
        - pattern: ^fmt\.Print.*$
          msg: use log/slog
        - pattern: ^log\.
          msg: use log/slog
        - pattern: ^time\.Now$
          msg: plan is pure; pass now in
        - pattern: ^rand\.
          msg: plan is deterministic
    sloglint:
      no-global: all
      static-msg: true
      key-naming-case: snake
    exhaustive:
      default-signifies-exhaustive: false
    govet:
      enable-all: true
      disable: [fieldalignment, shadow]
    nolintlint:
      require-explanation: true
      require-specific: true
  exclusions:
    generated: strict
    rules:
      # time.Now / rand are banned only inside plan
      - path-except: internal/plan/
        text: "(time\\.Now|rand\\.)"
        linters: [forbidigo]
      # Prometheus collectors are the one allowed global
      - path: internal/obs/
        linters: [gochecknoglobals]
      - path: _test\.go
        linters: [wrapcheck, gosec]
```

The module path is `github.com/quinnovator/contactd`.

**CI gates, in order.** Each gate is a mise task, and `mise run ci` runs them all, so local runs and CI are identical. CI installs the toolchain with `jdx/mise-action`, which reads the same `mise.toml` and `mise.lock`.

1. `golangci-lint fmt --diff`: formatting drift fails fast.
2. `golangci-lint run`: everything in the table above.
3. `go mod tidy -diff` and `sqlc diff`: no stale `go.mod` or generated query code.
4. `go test -race -shuffle=on -tags ort,integration ./...` in the CI image. This includes the plan golden tests and the synctest timer tests.
5. `govulncheck ./...`.
6. Nightly: benchmarks plus the `replay` cache simulator, with results compared against the last run.

## Milestones

Build in the same order as the pieces. Each milestone ends with a measurement, so tuning is never done blind.

1. **Log, actors and fake LLM.** Schema, group-commit writer, registry, hydrate/hibernate, SSE stream. Exit: 10k contacts round-trip; bytes per actor measured.
2. **Embedder and reranker.** Hugot with the ORT backend, micro-batchers, background indexing. Exit: embed and rerank latency measured inside the container.
3. **JIT threads.** Episode segmentation, two-stage recall, the recall benchmark. Exit: rerank beats stage 1 alone, and τ, gap and θ are tuned.
4. **JIT cache.** Tiered planner, hysteresis, step compaction, cache simulator. Exit: at least 80% of input tokens cached in simulation.
5. **Real provider and person-like behavior.** Provider adapter with breakpoints, debounce, interruption. Exit: the measured provider cache ratio tracks the simulator.
6. **JIT environments.** Tier 0 on wazero, the tier-1 client, speculative restore. Exit: tier-0 start time and tier-1 restore time measured.
7. **Hardening.** Graceful shutdown, metrics dashboards, load test at the target concurrency.

## Open questions

- **Tier-0 variable persistence.** Snapshot wazero linear memory and globals so interpreter state survives hibernation, or accept that only files persist?
- **arm64 builds.** Hugot is only built and tested on linux/amd64. Building `tokenizers.a` and ONNX Runtime for linux/arm64 (Apple Silicon dev, Graviton) needs verifying.
- **Profile updates.** Who writes tier 1 (the contact profile), and how often? Batching those edits protects the cache.
- **Forgetting.** The log is append-only. Deleting user data needs tombstone events plus a rebuild of vectors and episodes.
- **Languages.** bge-small-en is English-only. If contacts will speak other languages, swap in a small multilingual embedder and reranker and rerun the recall benchmark.

## Sources

- [Hugot: ONNX transformer pipelines for Go](https://github.com/knights-analytics/hugot): pipelines, backends, tokenizer requirements, threading advice
- [Container-aware GOMAXPROCS (new in Go 1.25)](https://appliedgo.net/spotlight/go-1.25-container-aware-gomaxprocs/)
- [Container-aware resource management in Go](https://levelup.gitconnected.com/container-aware-resource-management-in-go-the-problem-go-1-25s-fix-and-what-s-still-missing-211b2e3aeb76): GOMEMLIMIT is still not cgroup-aware
