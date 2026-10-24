# Model providers

The core reaches every provider through one typed contract (`internal/llm`) and acts only on what a model route declares it can do: how its cache is driven and for how long, where a pinning key goes, which reasoning efforts it accepts, whether reasoning must be sent back, whether it can be held to one tool call per step. The core never names a provider; `depguard` enforces the boundary from both sides. [ARCHITECTURE.md](ARCHITECTURE.md) describes the contract.

Any provider whose credentials are in the environment is available to agents that name it. `-llm` picks the default.

| Provider (`-llm`) | Credentials | Wire | Cache | Effort | Reasoning replay |
| --- | --- | --- | --- | --- | --- |
| `deepseek` | `DEEPSEEK_API_KEY` | Messages at `api.deepseek.com/anthropic` by default; `-deepseek-wire=chat` for Chat Completions | Automatic by prefix, assumed warm 1 hour | `low`, `high`, `max`, or off | Every turn |
| `anthropic` | `ANTHROPIC_API_KEY` | Messages | Breakpoints, 5 minutes | `low` to `max` | Within a turn |
| `openrouter` | `OPENROUTER_API_KEY` | Chat Completions | Per model, from the catalog's prices; pinned with `session_id` | Per model, from the catalog | Every turn where the model reasons |
| `opencode` | `OPENCODE_API_KEY` | Per model: Chat Completions or Messages (Responses-only models are refused) | Automatic, or breakpoints where a cached-write price is listed; pinned with `x-opencode-session` | None declared | Every turn for DeepSeek models, within a turn for Claude |
| `ollama` | `OLLAMA_API_KEY` | Chat Completions at `ollama.com/v1` | Automatic by prefix; reports cache reads; retention unknown | Per model, from `/api/show` | None declared |
| `openai` | `OPENAI_API_KEY` and/or `-openai-base-urls`, plus `-openai-models` | Chat Completions | Automatic by prefix per replica | None | None |
| `fake` | none | in process | Scripted | | |

Each profile's source file, under `internal/llm/provider/`, states where every declared value comes from and what is a guess. OpenRouter, OpenCode Go and Ollama Cloud read their model catalogs once at start-up; `-openrouter-catalog` starts from a saved copy instead.

`openai` is for any OpenAI-compatible server, such as vLLM or SGLang. With several `-openai-base-urls`, each contact is routed to the same replica by its affinity key, so its KV prefix stays resident there.

## Flags

| Flag | Default | Notes |
| --- | --- | --- |
| `-llm` | first configured | The default provider |
| `-model` | the provider's own | Default model on the default provider. OpenRouter has no default of its own and needs this |
| `-effort`, `-max-tokens` | none | Defaults for agents that set none |
| `-summary-provider`, `-summary-model` | the default route | Where lazy episode summaries and checkpoints are written; point it at something small |
| `-provider-limits` | DeepSeek's documented limits | Concurrent model calls per provider or route: `deepseek/deepseek-v4-pro=500,openrouter=64` |
| `-deepseek-wire` | `messages` | or `chat` |
| `-no-refusal-fallback` | off | See [Anthropic](#anthropic) |

How long a prompt cache stays warm is declared by each model route, not set for the deployment.

## How requests are made

- **Wire codecs are hand-written HTTP, not the vendors' SDKs.** Requests are frozen to exact bytes before they are sent, so that a retry resends the same request and the prompt cache can be reasoned about; golden tests pin those bytes. There are two codecs: Messages (`internal/llm/wire/messages`) and Chat Completions (`internal/llm/wire/chat`). There is no Responses codec and no native Ollama codec.
- **Chat Completions routes send tiers 0-2 as one system message**, because many self-hosted chat templates reject more than one.
- **Effort maps to the nearest accepted value**, preferring the stronger of two equally near. DeepSeek `xhigh` is therefore sent as `max`. The log shows what was sent.
- **A route's maximum output is a ceiling, not the default.** A call that names no cap asks for the route's default (8,192 tokens on most routes, 64,000 on Anthropic).
- **Retries happen only before the first event.** A request the provider accepts with HTTP 200 and then fails before writing anything (an overloaded error as the first frame, a dropped connection) is retried like a refused one. After the first event nothing is retried.
- **A tool exchange made by another route is shown as text to a route that needs reasoning back.** If a contact moves to a model that must be given the reasoning behind every earlier tool call, the calls its old model made have none to give. They are rendered as plain assistant and user text ("ran the python tool", "result of the python tool"), so the request is accepted and the model still sees what happened.
- **A reply cut off mid-stream stores no reasoning**, because the stream never finished.

Every call is recorded as an `attempt` event in the contact's log: route, a hash and size of the request bytes, the effort actually sent, tries, predicted and reported cache figures, stop reason or error class, and duration.

## Anthropic

Replies default to `claude-opus-5-5` and lazy summaries to `claude-haiku-5-5`. Refusal fallback is on by default (`fallbacks: "default"` with its beta header): a classifier-declined request is re-run server-side on another model and billed at that model's rates. `-no-refusal-fallback` turns it off, which a gateway that rejects the header will need.

## DeepSeek web search and fetch

Native DeepSeek routes automatically offer `web_search` and `web_fetch`, using the same `DEEPSEEK_API_KEY` on both the Messages and Chat Completions wires. No contact configuration is needed.

- `web_search` accepts `query` (1–128 characters), plus optional `count` (1–50, default 10), `ori_question`, `localization` and `need_content`; it returns ranked sources with URLs and summaries.
- `web_fetch` accepts an HTTP(S) `url` and returns the page title and Markdown content.

See DeepSeek's [Search](https://api-docs.deepseek.com/api/search), [Fetch](https://api-docs.deepseek.com/api/fetch) and [billing and rate limits](https://api-docs.deepseek.com/guides/search) documentation.

These are ordinary logged tool calls: results enter the conversation within its tool-output budget, transient API errors are retried, and other failures are returned to the model as tool errors. Calls have a one-minute timeout per attempt. They are available only on native DeepSeek routes, not DeepSeek models served through a gateway. An explicitly configured tool with the same name keeps its own definition and runtime. A DeepSeek base URL override also controls the web API host; `/anthropic` or `/v1` at the end is removed before appending `/v1/tools/search` or `/v1/tools/fetch`.

On every DeepSeek route, a tool loop that left the model's reasoning out was still accepted, though DeepSeek's documentation says the Chat Completions endpoint rejects it. The profiles keep replaying reasoning on every turn, which is the documented contract and costs little when it is read from cache.

## Ollama Cloud cache reporting

Ollama's [cache accounting](https://docs.ollama.com/api/chat) is exposed by its [OpenAI-compatible endpoint](https://docs.ollama.com/api/openai-compatibility) as `usage.prompt_tokens_details.cached_tokens` ([upstream implementation](https://github.com/ollama/ollama/pull/17943)). contactd requests streaming usage and keeps the full input count, cache reads and raw usage object. An explicit zero means a cold cache; an omitted, null or invalid count remains unknown and is excluded from cache metrics. Attempts persist that distinction, `ctxtop` reports it, and the terminal client only shows a cache percentage when it is known. Ollama's cache lifetime is undocumented, so actual hits are recorded without assuming a warm prefix between calls.

## Probing a route

A capability is a claim, and `contactd probe` checks the claims against the live endpoint:

```sh
DEEPSEEK_API_KEY=... bin/contactd probe -provider deepseek -model deepseek-flash
```

It sends about ten small requests (one of them a 20,000-token prompt, twice, to see what is reported as cached) and prints a table of declared against observed. It exits 1 when the endpoint contradicts the profile. Run it when adding a model, and when a provider changes something.

Last results, October 2026:

| Route | Result |
| --- | --- |
| `deepseek/deepseek-flash` and `deepseek-v4-pro`, Messages wire | Every check passed. Cache hits are reported in the Anthropic-style usage fields (21,504 of 21,692 tokens on a repeated prompt). Two tool calls came back in one step. All four effort settings were accepted |
| `deepseek/deepseek-flash`, Chat Completions wire | Every check passed. The cache is shared with the Messages endpoint. This wire also reports reasoning tokens, which the Messages wire does not |
| `openrouter/deepseek/deepseek-v4.1-flash` | Every check passed, including `reasoning_details` going back unmodified |
| `opencode/deepseek-v4.1-flash` (Chat Completions) | Every check passed. The gateway refuses any request without `x-opencode-session` (HTTP 400 `MissingSessionID`), and it reports cached tokens |
| `opencode/qwen3.8-flash` (Messages) | The Messages route answers and reports breakpoint cache hits. The model did not call the tool when asked, so the tool checks did not run |
| `ollama/glm-5.3-flash` (cache only) | 0 of 1,929 input tokens cached on the first request and 1,792 (92.9%) on an identical second one. This confirms cache reporting, not a retention guarantee |

Not fully probed: `anthropic`, `ollama`, and every OpenRouter and OpenCode Go model other than the ones above. The Anthropic and Ollama Cloud profiles are tested against written fixtures and the conformance suite.

## Adding a provider

A provider is a profile under `internal/llm/provider/<name>` that declares its routes' capabilities and picks a wire codec. Every profile runs the conformance suite in `internal/llm/conform`, and `contactd probe` checks its claims against the live endpoint. The core must not learn the provider's name: if a new behaviour is needed, add a capability to the contract.
