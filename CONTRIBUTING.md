# Contributing

Issues and pull requests are welcome. For anything larger than a fix, open an issue first so the approach can be agreed before you write it. [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) explains the invariants most changes have to keep; read the piece you are touching before you change it.

By contributing you agree that your contribution is licensed under the [Apache License 2.0](LICENSE).

## Setting up

[mise](https://mise.jdx.dev) pins every tool and is the task runner. `mise install` puts Go, golangci-lint, sqlc, sqlfluff and uv at the versions CI uses; exact versions and checksums are in `mise.lock`. A C compiler is needed for SQLite (cgo).

```sh
mise install
mise run test       # race-enabled unit tests; needs nothing else
mise tasks          # every task
```

The unit tier runs on the fake model and the fake embedder, so it needs no API key, no ONNX Runtime and no models. The inference and integration tiers need the native libraries, WASM runtimes and models:

```sh
mise run deps:native    # libonnxruntime + libtokenizers.a into third_party/lib, checksum-verified
mise run deps:wasm      # CPython 3.12 and QuickJS WASI builds into third_party/wasm
mise run models:fetch   # export + int8-quantize bge-small-en-v1.5 and ms-marco-MiniLM-L6-v2 into models/
mise run test:all
```

On macOS, ONNX Runtime 1.24 on Apple Silicon miscomputes when a signal lands mid-inference, and Go preempts goroutines with signals. `mise.toml` sets `GODEBUG=asyncpreemptoff=1`, and the binary warns at startup when it is missing on macOS. Avoid CPU profiling there. Linux is unaffected.

## Local development

```sh
# Server logs stay here; chat and diagnostics open in two new windows.
# Uses your configured model provider and lightweight lexical recall.
mise run dev
```

In the chat window, select an existing contact or press `n` to create one. The picker waits for the server to be ready before loading. Closing either client leaves the server running; `ctrl+c` in the server terminal stops both clients and closes their windows (Ghostty, macOS Terminal or tmux).

```sh
mise run chat ada       # reopen a contact
mise run chat           # open the picker
mise run chat:new ada   # create a contact and start chatting
mise run chat:new grace -system="You are Grace, a concise coding colleague." -tools=python,js
```

The tasks use `./data` for the server and `./data/chat-config` for the client's settings and mods, both ignored by Git. `CONTACTD_DATA_DIR` and `CONTACTD_CONFIG_DIR` override those paths. Stop a process with `ctrl+c` and rerun its task after changing code; each launch rebuilds it.

Extra arguments are passed through to the server or client. The dev server honours `CONTACTD_LLM` and `CONTACTD_MODEL`, or selects the first provider with configured credentials (for example `DEEPSEEK_API_KEY`). Export keys in your shell, or put them in a `.env` file and load it from a `mise.local.toml` holding `[env]` and `_.file = ".env"`; Git ignores both files. `mise run dev -llm=deepseek` selects one explicitly, and `mise run dev -llm=fake` is an offline demo with scripted replies.

### Live context diagnostics

`mise run dev` enables `/debug/plans` and opens two client windows: chat and `ctxtop`. In Ghostty on macOS both are Ghostty windows; otherwise macOS uses Terminal. Under tmux they are tmux windows, and on Linux the launcher tries Ghostty or `x-terminal-emulator`. `-diagnostics=false` opens only chat.

```sh
mise run ctxtop                         # every contact, over the dev socket
mise run ctxtop -contact=ada            # one contact by name or ID
mise run ctxtop -url=http://127.0.0.1:8080
CONTACTD_DEV_VIEWER=off mise run dev    # server only; diagnostics still enabled
```

The header says which call is shown, how full its context window is, and how much of the prompt the provider reused from cache against what the planner expected. The bar under it draws the same thing, with its legend. Each of the six views opens with the question it answers, and the last line lists the keys that work in it:

| Key | View |
| --- | --- |
| `1` | **Prompt**: what the call sent, tier by tier, with each part's size, share and cache reuse, a marker where the cache ends, and the selected part in full |
| `2` | **Log**: the contact's episodes, what this prompt took from each, and their messages |
| `3` | **Changes**: which tiers held since an earlier call of the same contact and purpose, where the first change is, what it did to the cache, and the bytes at that point |
| `4` | **Recall**: the search text, the outcome, and each candidate's similarity, rerank and final scores with the decision in words |
| `5` | **Checkpoints**: the tail against its budget, and each checkpoint beside the log events it replaced |
| `6` | **Timeline**: prompt size by tier, cache share and events, one column per retained call, plus inherited context for forks |

Use up/down to select, Enter to fold a tier or open the selection full width, left/right to move between calls, and `f` to follow the latest; the header says when the view is paused on an older call. `g` jumps from a part to its episode in Log; `r` shows the exact provider request body. `s` sorts Prompt by size, `/` filters the view, `z` scales the bar to the window or to the prompt, and Page Up/Down scrolls content. In Changes, `[`/`]` picks the call to compare with. In Checkpoints, `m` marks numbers and paths absent from the summary (a heuristic, not a quality score). Tab/Shift+Tab cycles contacts; `?` shows the keys and a short glossary; `q` closes the viewer.

The bar uses the planner's token estimates. Provider token counts come from a different tokenizer, so the reported marker is projected by its cached share; the comparison is in percentage points, not a subtraction of unlike token counts. A gap over five percentage points is highlighted for investigation. It is not by itself proof of a cache bug. Missing usage stays unknown. Per-block cache hits and the break location are estimates; only aggregate usage is reported. Canonical block bytes are the planner's hashing representation. They are distinct from the raw provider JSON body, which is captured before sending and excludes HTTP headers. The provider body can include replayed reasoning.

Some precision still depends on runtime instrumentation: in-turn tool entries can have an unknown log sequence until the next plan; recalls currently appear as one combined system-message block. Log and fold previews load at most 2,000 events per selected span; search covers loaded data. Output counts while streaming are byte estimates and switch to provider usage on completion. Monetary overlays are unavailable until per-route prices are supplied.

Diagnostics are off outside the dev task. They expose full prompts over the API; see [docs/API.md](docs/API.md#diagnostics-endpoints) before turning them on anywhere else.

### The README's captures

`docs/images` holds the README's recordings of the terminal client, made with [vhs](https://github.com/charmbracelet/vhs) from `scripts/demo.tape`. The tape runs `mise run try` in a fresh data directory, so it needs a model provider key; the reply is the model's own and differs on each run. The header of the tape gives the commands, including the `gifsicle` step that keeps the animation near 3 MB.

The A2A captures come from `scripts/demo-a2a.tape`, which runs `scripts/demo-a2a.sh` on ports 18080 and 19090 and turns the status page's API degraded after the helper's first report. Its animation comes to 4 or 5 MB after the steps in its header.

## Tests

| Tier | Command | Needs |
| --- | --- | --- |
| Unit | `mise run test` | cgo for SQLite |
| Inference + integration | `mise run test:all` (build tags `ORT,integration`) | native libs, WASM runtimes, models |
| Benchmarks, cache simulator, recall benchmark | `mise run bench` | the same, for the replay half |
| Everything CI runs | `mise run ci` | all of the above |

Timer behaviour (debounce, the 3 s cap, idle hibernation, lease expiry) is tested on the real `time` API inside `testing/synctest` bubbles, so production code has no `Clock` interface. `internal/plan` has golden tests that pin the rendered bytes and the hash chain: a change that would silently break every contact's prompt cache fails CI. Regenerate golden files only when the change to the bytes is the point of your change, and say so in the pull request.

## Code patterns

Each rule protects an invariant described in the architecture doc. Where a linter enforces one, the next section names it.

1. **Ports live with the consumer.** Each domain package declares the small interface it needs, and adapters satisfy it implicitly. Keep each interface to the one to three methods actually called.
2. **One goroutine owns actor state.** Fields on `actor` are touched only inside its run loop and the methods it calls. Turn goroutines get immutable snapshots and talk back only through commands. If a mutex seems necessary in `internal/actor`, the ownership is wrong.
3. **Context is always the first parameter and never a struct field.** Every blocking `select` includes `<-ctx.Done()`. Cancellation is how interruption works, so a missed `ctx.Done()` is a real bug.
4. **`plan` is pure.** It takes `now` as an argument, never calls `time.Now`, never ranges over a map to produce bytes, and never uses randomness.
5. **Errors are wrapped, never swallowed.** Use `fmt.Errorf("…: %w", err)` at each boundary and check sentinels such as `core.ErrContactNotFound` with `errors.Is`. `panic` is for impossible states at startup.
6. **Logging goes through `log/slog`.** Every line in a turn carries `contact_id` and `turn_id`. Log message text and IDs, never message bodies; those are user data and already in the log store.
7. **No globals besides metrics.** Config is one struct built in `cmd/contactd` and passed down. Prometheus collectors in `internal/obs` are the one exception.
8. **SQL is written as SQL.** Queries live in `sql/queries/` and are compiled to typed Go by sqlc (`mise run gen`). The group-commit writer's batched insert is the one hand-written statement.
9. **Fakes over mocks.** Every port has a hand-written fake (`infer/fake`, `llm/fake`) that behaves plausibly. Tests assert outcomes (events appended, the tier a plan breaks at), not call sequences.
10. **Text from outside never reaches the terminal raw.** Anything a model, tool, contact name, mod or shell segment produces goes through `internal/tui/safe`.

## Linting and CI

golangci-lint v2 does formatting (gofumpt with extra rules, and gci with three import groups: standard library, third party, this module) and linting, starting from `default: none` with an explicit list in `.golangci.yml`. Configure gopls with `gofumpt: true` so editors format the same way. SQL is linted by sqlfluff with the `sqlite` dialect.

| Linter | Protects |
| --- | --- |
| `depguard` | Layering: domain packages cannot import infrastructure, `database/sql`, `net/http`, Hugot or wazero; the client's rule packages cannot import Bubble Tea |
| `forbidigo` | `plan` purity (no `time.Now` or `math/rand`) and logging (no `fmt.Print*` or `log.*`) |
| `containedctx`, `contextcheck`, `noctx` | Context as a parameter, propagated, and used for outbound HTTP |
| `errcheck`, `errorlint`, `nilerr`, `wrapcheck` | Errors handled, wrapped with `%w`, compared with `errors.Is` |
| `sloglint` | No global logger, static messages, snake_case keys |
| `gochecknoglobals`, `gochecknoinits` | No globals (metrics excluded) |
| `exhaustive` | Every `switch` over an event kind, tier or command handles every case |
| `govet`, `staticcheck`, `unused`, `ineffassign` | Correctness baseline |
| `bodyclose`, `sqlclosecheck`, `rowserrcheck` | Resource leaks on HTTP and SQLite |
| `musttag` | Event bodies carry explicit JSON tags, which keeps tier bytes stable |
| `gosec` | Security baseline; matters most in `env/` and `api/` |
| `paralleltest`, `tparallel`, `thelper`, `usetesting` | Test hygiene |
| `modernize`, `intrange`, `perfsprint`, `prealloc` | Idiomatic modern Go |
| `nolintlint` | Every `//nolint` names a linter and gives a reason |

CI (`.github/workflows/ci.yml`) runs `mise run ci` on every push and pull request: `mise run lint`, `mise run gen:check` (no stale `go.mod` or generated query code), `mise run test:all`, and `govulncheck`. A nightly workflow runs the benchmarks and the cache simulator and compares them with the previous run.

## Pull requests

- Run `mise run lint` and `mise run test` before pushing; `mise run ci` if you touched inference, environments or the integration tests.
- Keep the README and the docs in `docs/` true. A change in behaviour that a doc describes changes the doc in the same pull request.
- Write commit messages that say what changed and why, in the imperative.
